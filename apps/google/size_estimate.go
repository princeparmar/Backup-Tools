package google

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/api/calendar/v3"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/gmail/v1"
	people "google.golang.org/api/people/v1"
)

const (
	// ContactsBytesPerContact is a rough average serialized contact size.
	ContactsBytesPerContact = 2 * 1024
	// ContactsMinEstimateBytes clamps small contact sets.
	ContactsMinEstimateBytes = 1 * 1024 * 1024
	// ContactsMaxEstimateBytes clamps huge address books.
	ContactsMaxEstimateBytes = 50 * 1024 * 1024

	// CalendarMinEstimateBytes is the lower fixed calendar buffer.
	CalendarMinEstimateBytes = 5 * 1024 * 1024
	// CalendarMaxEstimateBytes is the upper fixed calendar buffer.
	CalendarMaxEstimateBytes = 20 * 1024 * 1024

	// GmailSampleMaxMessages caps how many messages we fetch sizeEstimate for.
	GmailSampleMaxMessages = 40
	// GmailSampleTimeout is the max wall time for Gmail size sampling.
	GmailSampleTimeout = 12 * time.Second
	// GmailDefaultAvgBytes used when sample yields no sizeEstimate values.
	GmailDefaultAvgBytes = 75 * 1024
)

// EstimateDriveBytes uses Drive about.storageQuota.usageInDrive (excludes trash when possible).
func EstimateDriveBytes(ctx context.Context, svc *drive.Service) (int64, error) {
	if svc == nil {
		return 0, fmt.Errorf("drive service is nil")
	}
	about, err := svc.About.Get().Fields("storageQuota").Context(ctx).Do()
	if err != nil {
		return 0, fmt.Errorf("drive about.get: %w", err)
	}
	if about == nil || about.StorageQuota == nil {
		return 0, fmt.Errorf("drive about.get: missing storageQuota")
	}
	q := about.StorageQuota
	if q.UsageInDrive > 0 {
		usage := q.UsageInDrive
		if q.UsageInDriveTrash > 0 && usage >= q.UsageInDriveTrash {
			usage -= q.UsageInDriveTrash
		}
		return usage, nil
	}
	if q.Usage > 0 {
		return q.Usage, nil
	}
	return 0, nil
}

// EstimateGmailBytes samples messages with sizeEstimate and extrapolates to the mailbox.
// Never scans the entire mailbox. Honors GmailSampleTimeout.
func EstimateGmailBytes(ctx context.Context, svc *gmail.Service, apiUser string) (int64, error) {
	if svc == nil {
		return 0, fmt.Errorf("gmail service is nil")
	}
	apiUser = trimOrMe(apiUser)

	ctx, cancel := context.WithTimeout(ctx, GmailSampleTimeout)
	defer cancel()

	listCall := svc.Users.Messages.List(apiUser).MaxResults(int64(GmailSampleMaxMessages)).Q("in:anywhere")
	list, err := listCall.Context(ctx).Do()
	if err != nil {
		return 0, fmt.Errorf("gmail messages.list: %w", err)
	}

	totalEstimate := int64(list.ResultSizeEstimate)
	if totalEstimate <= 0 && len(list.Messages) > 0 {
		totalEstimate = int64(len(list.Messages))
	}

	var sumSizes int64
	var sized int64
	for _, m := range list.Messages {
		if ctx.Err() != nil {
			break
		}
		if m == nil || m.Id == "" {
			continue
		}
		msg, getErr := svc.Users.Messages.Get(apiUser, m.Id).Format("minimal").Fields("id,sizeEstimate").Context(ctx).Do()
		if getErr != nil {
			continue
		}
		if msg != nil && msg.SizeEstimate > 0 {
			sumSizes += msg.SizeEstimate
			sized++
		}
	}

	avg := int64(GmailDefaultAvgBytes)
	if sized > 0 {
		avg = sumSizes / sized
	}
	if totalEstimate <= 0 {
		return avg * int64(len(list.Messages)), nil
	}
	return avg * totalEstimate, nil
}

// EstimateGmailBytesQuick uses messages.list ResultSizeEstimate × default average only
// (no per-message gets). For onboarding / multi-mailbox admin Workspace pre-check.
func EstimateGmailBytesQuick(ctx context.Context, svc *gmail.Service, apiUser string) (int64, error) {
	if svc == nil {
		return 0, fmt.Errorf("gmail service is nil")
	}
	apiUser = trimOrMe(apiUser)

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	list, err := svc.Users.Messages.List(apiUser).MaxResults(1).Q("in:anywhere").Context(ctx).Do()
	if err != nil {
		return 0, fmt.Errorf("gmail messages.list: %w", err)
	}
	totalEstimate := int64(list.ResultSizeEstimate)
	if totalEstimate <= 0 && len(list.Messages) > 0 {
		totalEstimate = int64(len(list.Messages))
	}
	if totalEstimate <= 0 {
		return 0, nil
	}
	return totalEstimate * int64(GmailDefaultAvgBytes), nil
}

// EstimateContactsBytes uses People connections count × ~2KiB, clamped to 1–50 MiB.
func EstimateContactsBytes(ctx context.Context, svc *people.Service) (int64, error) {
	if svc == nil {
		// Fixed mid buffer when service unavailable.
		return ContactsMinEstimateBytes * 5, nil
	}
	resp, err := svc.People.Connections.List("people/me").
		PersonFields("names").
		PageSize(1).
		Context(ctx).
		Do()
	if err != nil {
		return clampContacts(ContactsMinEstimateBytes * 5), nil
	}
	count := int64(resp.TotalItems)
	if count <= 0 {
		count = int64(resp.TotalPeople)
	}
	if count <= 0 {
		return ContactsMinEstimateBytes, nil
	}
	return clampContacts(count * ContactsBytesPerContact), nil
}

func clampContacts(n int64) int64 {
	if n < ContactsMinEstimateBytes {
		return ContactsMinEstimateBytes
	}
	if n > ContactsMaxEstimateBytes {
		return ContactsMaxEstimateBytes
	}
	return n
}

// EstimateCalendarBytes returns a fixed buffer based on calendar list size (5–20 MiB).
func EstimateCalendarBytes(ctx context.Context, svc *calendar.Service) (int64, error) {
	if svc == nil {
		return CalendarMinEstimateBytes, nil
	}
	list, err := svc.CalendarList.List().MaxResults(250).Context(ctx).Do()
	if err != nil || list == nil {
		return CalendarMinEstimateBytes, nil
	}
	n := len(list.Items)
	if n <= 1 {
		return CalendarMinEstimateBytes, nil
	}
	// Scale roughly with calendar count toward the max buffer.
	est := CalendarMinEstimateBytes + int64(n-1)*1024*1024
	if est > CalendarMaxEstimateBytes {
		return CalendarMaxEstimateBytes, nil
	}
	return est, nil
}

func trimOrMe(s string) string {
	if s == "" {
		return "me"
	}
	return s
}
