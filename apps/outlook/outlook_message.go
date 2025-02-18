package outlook

import (
	"time"

	"github.com/microsoftgraph/msgraph-sdk-go/models"
)

type OutlookMessage struct {
	ID               string               `json:"id"`
	Subject          string               `json:"subject"`
	Body             string               `json:"body"`
	From             string               `json:"from"`
	ToRecipients     []string             `json:"to_recipients"`
	CcRecipients     []string             `json:"cc_recipients"`
	BccRecipients    []string             `json:"bcc_recipients"`
	ReceivedDateTime string               `json:"received_datetime"`
	SentDateTime     string               `json:"sent_datetime"`
	HasAttachments   bool                 `json:"has_attachments"`
	Attachments      []*OutlookAttachment `json:"attachments"`
	IsRead           bool                 `json:"is_read"`
	Categories       []string             `json:"categories"`
	Importance       string               `json:"importance"`
}

type OutlookAttachment struct {
	ID             string                 `json:"id"`
	ContentID      string                 `json:"content_id"`
	Name           string                 `json:"name"`
	ContentType    string                 `json:"content_type"`
	Size           int64                  `json:"size"`
	Data           []byte                 `json:"data"`
	IsInline       bool                   `json:"is_inline"`
	AdditionalData map[string]interface{} `json:"additional_data"`
}

func NewOutlookAttachment(attachment models.Attachmentable) *OutlookAttachment {
	if attachment == nil {
		return nil
	}

	out := &OutlookAttachment{
		ID:             stringValue(attachment.GetId()),
		Name:           stringValue(attachment.GetName()),
		ContentType:    stringValue(attachment.GetContentType()),
		Size:           int64Value(attachment.GetSize()),
		IsInline:       boolValue(attachment.GetIsInline()),
		AdditionalData: attachment.GetAdditionalData(),
	}

	if fileData, ok := attachment.(models.FileAttachmentable); ok {
		if contentID := fileData.GetContentId(); contentID != nil {
			out.ContentID = stringValue(contentID)
		}
		out.Data = fileData.GetContentBytes()
	}

	return out

}

func NewOutlookMessage(message models.Messageable) *OutlookMessage {
	if message == nil {
		return nil
	}

	importance := ""
	if i := message.GetImportance(); i != nil {
		importance = i.String()
	}

	msg := &OutlookMessage{
		ID:               stringValue(message.GetId()),
		Subject:          stringValue(message.GetSubject()),
		Body:             stringValue(message.GetBody().GetContent()),
		HasAttachments:   boolValue(message.GetHasAttachments()),
		IsRead:           boolValue(message.GetIsRead()),
		Importance:       importance,
		ReceivedDateTime: timeValue(message.GetReceivedDateTime()),
		SentDateTime:     timeValue(message.GetSentDateTime()),
	}

	// Set From address
	if from := message.GetFrom(); from != nil && from.GetEmailAddress() != nil {
		msg.From = stringValue(from.GetEmailAddress().GetAddress())
	}

	// Set Recipients
	msg.ToRecipients = getRecipients(message.GetToRecipients())
	msg.CcRecipients = getRecipients(message.GetCcRecipients())
	msg.BccRecipients = getRecipients(message.GetBccRecipients())

	// Set Categories
	if cats := message.GetCategories(); cats != nil {
		msg.Categories = make([]string, len(cats))
		for i, cat := range cats {
			msg.Categories[i] = stringValue(&cat)
		}
	}

	// Set Attachments
	if attachments := message.GetAttachments(); attachments != nil {
		msg.Attachments = make([]*OutlookAttachment, len(attachments))
		for i, att := range attachments {
			msg.Attachments[i] = NewOutlookAttachment(att)
		}
	}

	return msg
}

// Helper functions
func stringValue(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func boolValue(b *bool) bool {
	if b == nil {
		return false
	}
	return *b
}

func timeValue(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format(time.RFC3339)
}

func int64Value(i *int32) int64 {
	if i == nil {
		return 0
	}
	return int64(*i)
}

func getRecipients(recipients []models.Recipientable) []string {
	if recipients == nil {
		return nil
	}

	emails := make([]string, 0, len(recipients))
	for _, recipient := range recipients {
		if emailAddr := recipient.GetEmailAddress(); emailAddr != nil {
			if addr := stringValue(emailAddr.GetAddress()); addr != "" {
				emails = append(emails, addr)
			}
		}
	}
	return emails
}
