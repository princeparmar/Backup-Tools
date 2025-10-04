package outlook

import (
	"time"

	"github.com/StorX2-0/Backup-Tools/pkg/prometheus"
	"github.com/microsoftgraph/msgraph-sdk-go/models"
)

type OutlookMinimalMessage struct {
	ID               string `json:"id"`
	Subject          string `json:"subject"`
	From             string `json:"from"`
	ReceivedDateTime string `json:"received_datetime"`
}

func NewOutlookMinimalMessage(message models.Messageable) *OutlookMinimalMessage {
	if message == nil {
		prometheus.RecordError("outlook_minimal_message_nil_input", "outlook")
		return nil
	}

	result := &OutlookMinimalMessage{
		ID:               stringValue(message.GetId()),
		Subject:          stringValue(message.GetSubject()),
		From:             stringValue(message.GetFrom().GetEmailAddress().GetAddress()),
		ReceivedDateTime: timeValue(message.GetReceivedDateTime()),
	}

	prometheus.RecordCounter("outlook_minimal_message_created_total", 1, "component", "outlook", "status", "success")

	return result
}

type OutlookUser struct {
	ID                string `json:"id"`
	DisplayName       string `json:"display_name"`
	Mail              string `json:"mail"`
	UserPrincipalName string `json:"user_principal_name"`
}

func NewOutlookUser(user models.Userable) *OutlookUser {
	if user == nil {
		prometheus.RecordError("outlook_user_nil_input", "outlook")
		return nil
	}

	result := &OutlookUser{
		ID:                stringValue(user.GetId()),
		DisplayName:       stringValue(user.GetDisplayName()),
		Mail:              stringValue(user.GetMail()),
		UserPrincipalName: stringValue(user.GetUserPrincipalName()),
	}

	prometheus.RecordCounter("outlook_user_created_total", 1, "component", "outlook", "status", "success")

	return result
}

type OutlookMessage struct {
	OutlookMinimalMessage

	Body                   string               `json:"body"`
	ContentType            *models.BodyType     `json:"content_type"`
	ODataType              *string              `json:"odata_type"`
	ToRecipients           []string             `json:"to_recipients"`
	CcRecipients           []string             `json:"cc_recipients"`
	BccRecipients          []string             `json:"bcc_recipients"`
	SentDateTime           string               `json:"sent_datetime"`
	HasAttachments         bool                 `json:"has_attachments"`
	Attachments            []*OutlookAttachment `json:"attachments"`
	IsRead                 bool                 `json:"is_read"`
	Categories             []string             `json:"categories"`
	Importance             string               `json:"importance"`
	InternetMessageID      string               `json:"internet_message_id"`
	InternetMessageHeaders map[string]string    `json:"internet_message_headers"`
}

type OutlookAttachment struct {
	ID             string                 `json:"id"`
	ContentID      string                 `json:"content_id"`
	Name           string                 `json:"name"`
	ContentType    *string                `json:"content_type"`
	ODataType      *string                `json:"odata_type"`
	Size           int64                  `json:"size"`
	Data           []byte                 `json:"data"`
	IsInline       bool                   `json:"is_inline"`
	AdditionalData map[string]interface{} `json:"additional_data"`
}

func NewOutlookAttachment(attachment models.Attachmentable) *OutlookAttachment {
	if attachment == nil {
		prometheus.RecordError("outlook_attachment_nil_input", "outlook")
		return nil
	}

	out := &OutlookAttachment{
		ID:             stringValue(attachment.GetId()),
		Name:           stringValue(attachment.GetName()),
		ContentType:    attachment.GetContentType(),
		ODataType:      attachment.GetOdataType(),
		Size:           int64Value(attachment.GetSize()),
		IsInline:       boolValue(attachment.GetIsInline()),
		AdditionalData: attachment.GetAdditionalData(),
	}

	prometheus.RecordSize("outlook_attachment_created", out.Size, "component", "outlook")

	if fileData, ok := attachment.(models.FileAttachmentable); ok {
		if contentID := fileData.GetContentId(); contentID != nil {
			out.ContentID = stringValue(contentID)
		}
		out.Data = fileData.GetContentBytes()
		prometheus.RecordSize("outlook_file_attachment_data", int64(len(out.Data)), "component", "outlook")
	}

	prometheus.RecordCounter("outlook_attachment_created_total", 1, "component", "outlook", "status", "success")

	return out

}

func NewOutlookMessage(message models.Messageable) *OutlookMessage {
	if message == nil {
		prometheus.RecordError("outlook_message_nil_input", "outlook")
		return nil
	}

	importance := ""
	if i := message.GetImportance(); i != nil {
		importance = i.String()
	}

	internetMessageHeaders := make(map[string]string)
	for _, header := range message.GetInternetMessageHeaders() {
		internetMessageHeaders[stringValue(header.GetName())] = stringValue(header.GetValue())
	}

	msg := &OutlookMessage{
		OutlookMinimalMessage: OutlookMinimalMessage{
			ID:               stringValue(message.GetId()),
			Subject:          stringValue(message.GetSubject()),
			From:             stringValue(message.GetFrom().GetEmailAddress().GetAddress()),
			ReceivedDateTime: timeValue(message.GetReceivedDateTime()),
		},
		Body:                   stringValue(message.GetBody().GetContent()),
		ContentType:            message.GetBody().GetContentType(),
		ODataType:              message.GetBody().GetOdataType(),
		HasAttachments:         boolValue(message.GetHasAttachments()),
		IsRead:                 boolValue(message.GetIsRead()),
		Importance:             importance,
		InternetMessageID:      stringValue(message.GetInternetMessageId()),
		InternetMessageHeaders: internetMessageHeaders,
		SentDateTime:           timeValue(message.GetSentDateTime()),
	}

	// Record message size metrics
	bodySize := int64(len(msg.Body))
	prometheus.RecordSize("outlook_message_body_size", bodySize, "component", "outlook")
	prometheus.RecordCounter("outlook_message_headers_total", int64(len(internetMessageHeaders)), "component", "outlook")

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
		prometheus.RecordCounter("outlook_message_attachments_total", int64(len(attachments)), "component", "outlook")
	}

	prometheus.RecordCounter("outlook_message_created_total", 1, "component", "outlook", "status", "success")

	return msg
}

// Helper functions
func stringValue(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func stringPointer(s string) *string {
	return &s
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

	prometheus.RecordCounter("outlook_recipients_processed_total", int64(len(emails)), "component", "outlook")

	return emails
}
