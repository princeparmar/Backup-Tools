package outlook

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Listing a calendar returns single events and series masters only. The backup of a series master
// also holds, read from the master itself:
//
//	exceptionOccurrences  full Graph events of the changed occurrences
//	cancelledOccurrences  ids (OID.{masterId}.{date}) of the cancelled occurrences; the date is in
//	                      the series time zone
//
// and its start and end in the series time zone, so a restored series keeps its local time across
// daylight-saving changes. The calendar index keeps the UTC times of its occurrences for the
// calendar view (EventSummary.Instances). A restore creates the series, deletes its cancelled
// occurrences and then updates its changed ones.

const eventTypeSeriesMaster = "seriesMaster"

const seriesQuery = "$select=start,end,cancelledOccurrences,exceptionOccurrences&$expand=exceptionOccurrences"

var seriesFields = []string{"start", "end", "cancelledOccurrences", "exceptionOccurrences"}

// PIMSeriesURL reads a series master with its changed and cancelled occurrences.
func PIMSeriesURL(userBase, calendarID, masterID string) string {
	return strings.TrimRight(userBase, "/") + "/calendars/" + url.PathEscape(calendarID) +
		"/events/" + url.PathEscape(masterID) + "?" + seriesQuery
}

// AddSeriesOccurrences returns the backup of a listed series master: raw with the changed and
// cancelled occurrences read from seriesURL, and start and end in the series time zone.
func AddSeriesOccurrences(ctx context.Context, accessToken, seriesURL string, raw []byte) ([]byte, error) {
	var master map[string]json.RawMessage
	if err := json.Unmarshal(raw, &master); err != nil {
		return nil, fmt.Errorf("parse series master: %w", err)
	}
	body, status, err := graphRequest(ctx, accessToken, http.MethodGet, seriesURL, nil, seriesTimeZone(raw))
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("graph series http %d: %s", status, truncateForErr(body))
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(body, &got); err != nil {
		return nil, fmt.Errorf("decode graph series: %w", err)
	}
	for _, f := range seriesFields {
		if v, ok := got[f]; ok {
			master[f] = v
		}
	}
	return json.Marshal(master)
}

// seriesTimeZone is the time zone of a series: its recurrence time zone, else the time zone it was
// created in. "" when unknown or a legacy custom Outlook time zone.
func seriesTimeZone(raw []byte) string {
	var ev struct {
		OriginalStartTimeZone string `json:"originalStartTimeZone"`
		Recurrence            struct {
			Range struct {
				RecurrenceTimeZone string `json:"recurrenceTimeZone"`
			} `json:"range"`
		} `json:"recurrence"`
	}
	_ = json.Unmarshal(raw, &ev)
	for _, tz := range []string{ev.Recurrence.Range.RecurrenceTimeZone, ev.OriginalStartTimeZone} {
		tz = strings.TrimSpace(tz)
		if tz != "" && !strings.HasPrefix(tz, "tzone://") {
			return tz
		}
	}
	return ""
}

// SeriesRestoreError is a recurring series that was created without all its changed or cancelled
// occurrences. The series exists, so its key must not be restored again automatically.
type SeriesRestoreError struct{ Err error }

func (e *SeriesRestoreError) Error() string {
	return "recurring series restored without all its changed or cancelled occurrences: " + e.Err.Error()
}

func (e *SeriesRestoreError) Unwrap() error { return e.Err }

// RestoreErrorCode is restore.ErrorCodeOutcomeUnknown.
func (e *SeriesRestoreError) RestoreErrorCode() string { return "outcome_unknown" }

type seriesInstance struct {
	ID            string        `json:"id"`
	OriginalStart string        `json:"originalStart"`
	Start         graphDateTime `json:"start"`
	End           graphDateTime `json:"end"`
	IsCancelled   bool          `json:"isCancelled"`
}

// maxSeriesInstances caps the occurrences of one series kept in a calendar index.
const maxSeriesInstances = 2000

// PIMInstancesURL lists the occurrences of a series master between from and to.
func PIMInstancesURL(userBase, calendarID, masterID string, from, to time.Time) string {
	return strings.TrimRight(userBase, "/") + "/calendars/" + url.PathEscape(calendarID) +
		"/events/" + url.PathEscape(masterID) + "/instances?" + instancesQuery(from, to)
}

func instancesQuery(from, to time.Time) string {
	return "startDateTime=" + url.QueryEscape(from.UTC().Format(time.RFC3339)) +
		"&endDateTime=" + url.QueryEscape(to.UTC().Format(time.RFC3339)) +
		"&$select=id,start,end,originalStart,isCancelled&$top=100"
}

// ListSeriesInstances returns the UTC start and end of every occurrence listed from
// instancesURL, cancelled ones left out.
func ListSeriesInstances(ctx context.Context, accessToken, instancesURL string) ([][2]string, error) {
	var out [][2]string
	err := eachSeriesInstance(ctx, accessToken, instancesURL, "UTC", func(i seriesInstance) bool {
		if !i.IsCancelled {
			out = append(out, [2]string{i.Start.utc(), i.End.utc()})
		}
		return len(out) < maxSeriesInstances
	})
	return out, err
}

type seriesBackup struct {
	Type                 string            `json:"type"`
	CancelledOccurrences []string          `json:"cancelledOccurrences"`
	ExceptionOccurrences []json.RawMessage `json:"exceptionOccurrences"`
}

// restoreSeriesOccurrences applies the backed-up cancelled and changed occurrences of the series
// master raw to the new series masterID. Cancelled ones go first, while every new occurrence is
// still at its original time.
func restoreSeriesOccurrences(ctx context.Context, accessToken, base, masterID string, s seriesBackup, raw []byte) error {
	tz := seriesTimeZone(raw)
	var errs []error
	for _, oid := range s.CancelledOccurrences {
		day, err := occurrenceDate(oid)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		date := day.Format(time.DateOnly)
		inst, err := findSeriesInstance(ctx, accessToken, base, masterID, tz, day.AddDate(0, 0, -1), day.AddDate(0, 0, 2),
			func(i seriesInstance) bool { return strings.HasPrefix(i.Start.DateTime, date) })
		if err == nil {
			err = graphWrite(ctx, accessToken, http.MethodDelete, base+"/events/"+url.PathEscape(inst.ID), nil)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("cancel occurrence on %s: %w", date, err))
		}
	}
	for _, ex := range s.ExceptionOccurrences {
		if err := restoreException(ctx, accessToken, base, masterID, tz, ex); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func restoreException(ctx context.Context, accessToken, base, masterID, tz string, raw json.RawMessage) error {
	var ex struct {
		OriginalStart string `json:"originalStart"`
	}
	_ = json.Unmarshal(raw, &ex)
	orig, err := time.Parse(time.RFC3339, ex.OriginalStart)
	if err != nil {
		return fmt.Errorf("changed occurrence without original start %q", ex.OriginalStart)
	}
	payload, err := EventCreatePayload(raw)
	if err != nil {
		return err
	}
	delete(payload, "recurrence")
	inst, err := findSeriesInstance(ctx, accessToken, base, masterID, tz, orig.Add(-24*time.Hour), orig.Add(24*time.Hour),
		func(i seriesInstance) bool {
			t, err := time.Parse(time.RFC3339, i.OriginalStart)
			return err == nil && t.Equal(orig)
		})
	if err == nil {
		err = graphWrite(ctx, accessToken, http.MethodPatch, base+"/events/"+url.PathEscape(inst.ID), payload)
	}
	if err != nil {
		return fmt.Errorf("change occurrence of %s: %w", ex.OriginalStart, err)
	}
	return nil
}

// occurrenceDate is the date of an occurrence id "OID.{masterId}.{yyyy-mm-dd}".
func occurrenceDate(oid string) (time.Time, error) {
	i := strings.LastIndex(oid, ".")
	day, err := time.Parse(time.DateOnly, oid[i+1:])
	if i < 0 || err != nil {
		return time.Time{}, fmt.Errorf("occurrence id %q has no date", oid)
	}
	return day, nil
}

// findSeriesInstance returns the occurrence of masterID between from and to that match accepts.
// Start times are in time zone tz ("" is UTC).
func findSeriesInstance(ctx context.Context, accessToken, base, masterID, tz string, from, to time.Time, match func(seriesInstance) bool) (seriesInstance, error) {
	var found seriesInstance
	instancesURL := base + "/events/" + url.PathEscape(masterID) + "/instances?" + instancesQuery(from, to)
	err := eachSeriesInstance(ctx, accessToken, instancesURL, tz, func(i seriesInstance) bool {
		if match(i) {
			found = i
		}
		return found.ID == ""
	})
	if err == nil && found.ID == "" {
		err = errors.New("occurrence not found in the restored series")
	}
	return found, err
}

// eachSeriesInstance pages through instancesURL, times in time zone tz, until fn returns false.
func eachSeriesInstance(ctx context.Context, accessToken, instancesURL, tz string, fn func(seriesInstance) bool) error {
	for next := instancesURL; next != ""; {
		body, status, err := graphRequest(ctx, accessToken, http.MethodGet, next, nil, tz)
		if err != nil {
			return err
		}
		if status < 200 || status >= 300 {
			return fmt.Errorf("graph instances http %d: %s", status, truncateForErr(body))
		}
		var page struct {
			Value    []seriesInstance `json:"value"`
			NextLink string           `json:"@odata.nextLink"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return fmt.Errorf("decode graph instances: %w", err)
		}
		for _, inst := range page.Value {
			if inst.ID != "" && !fn(inst) {
				return nil
			}
		}
		next = page.NextLink
	}
	return nil
}

func graphWrite(ctx context.Context, accessToken, method, reqURL string, payload any) error {
	var body []byte
	if payload != nil {
		var err error
		if body, err = json.Marshal(payload); err != nil {
			return err
		}
	}
	resp, status, err := graphRequest(ctx, accessToken, method, reqURL, body, "")
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("graph %s http %d: %s", strings.ToLower(method), status, truncateForErr(resp))
	}
	return nil
}

// graphRequest sends one Graph request; tz, when set, is the time zone of returned event times.
func graphRequest(ctx context.Context, accessToken, method, reqURL string, body []byte, tz string) ([]byte, int, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, reqURL, reader)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(accessToken))
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if tz != "" {
		req.Header.Set("Prefer", `outlook.timezone="`+tz+`"`)
	}
	resp, err := graphHTTPDoWithRetry(ctx, req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return b, resp.StatusCode, nil
}
