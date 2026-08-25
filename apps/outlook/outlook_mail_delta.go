package outlook

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// OutlookMailAdditionalFolderIDs are synced after inbox in Phase 2 multi-folder backup.
var OutlookMailAdditionalFolderIDs = []string{"sentitems", "archive"}

// ErrOutlookMailDeltaInvalid means the saved deltaLink can no longer be used; rebaseline required.
var ErrOutlookMailDeltaInvalid = fmt.Errorf("outlook mail delta cursor invalid; rebaseline required")

// OutlookMailDeltaMessage is a message stub from Graph mail delta.
type OutlookMailDeltaMessage struct {
	ID                   string
	Subject              string
	From                 string
	ReceivedDateTime     string
	LastModifiedDateTime string
	ChangeKey            string
	HasAttachments       bool
	IsRemoved            bool
}

// OutlookMailDeltaPage is one page of a Graph mail messages delta response.
type OutlookMailDeltaPage struct {
	Messages  []OutlookMailDeltaMessage
	NextLink  string
	DeltaLink string
}

// OutlookMailCronBackupMeta is JSON stored at {mailbox}/meta/.../{messageId}.json.
type OutlookMailCronBackupMeta struct {
	MessageID            string `json:"message_id"`
	Subject              string `json:"subject,omitempty"`
	From                 string `json:"from,omitempty"`
	ReceivedDateTime     string `json:"received_date_time,omitempty"`
	LastModifiedDateTime string `json:"last_modified_date_time,omitempty"`
	ChangeKey            string `json:"change_key,omitempty"`
	HasAttachments       bool   `json:"has_attachments,omitempty"`
	RemovedFromMailbox  bool   `json:"removed_from_mailbox,omitempty"`
	DeletedAt            string `json:"deleted_at,omitempty"`
	DataObjectKey        string `json:"data_object_key,omitempty"`
	UpdatedAt            string `json:"updated_at,omitempty"`
}

type graphOutlookMailDeltaResponse struct {
	Value     []graphOutlookMailMessageRow `json:"value"`
	NextLink  string                    `json:"@odata.nextLink"`
	DeltaLink string                    `json:"@odata.deltaLink"`
}

type graphOutlookMailMessageRow struct {
	ID                   string `json:"id"`
	Subject              string `json:"subject"`
	ReceivedDateTime     string `json:"receivedDateTime"`
	LastModifiedDateTime string `json:"lastModifiedDateTime"`
	ChangeKey            string `json:"changeKey"`
	HasAttachments       bool   `json:"hasAttachments"`
	From                 *struct {
		EmailAddress *struct {
			Address string `json:"address"`
		} `json:"emailAddress"`
	} `json:"from"`
	Removed *struct {
		Reason string `json:"reason"`
	} `json:"@removed"`
}

// MailUserBaseURL returns /me or /users/{mailbox} for Graph mail paths.
func (client *OutlookClient) MailUserBaseURL(mailbox string) (string, error) {
	mailbox = strings.TrimSpace(mailbox)
	user, err := client.GetCurrentUser()
	if err != nil {
		return "", err
	}
	meMail := strings.ToLower(strings.TrimSpace(user.Mail))
	meUPN := strings.ToLower(strings.TrimSpace(user.UserPrincipalName))
	mb := strings.ToLower(mailbox)
	if mailbox == "" || mb == meMail || mb == meUPN {
		return graphBaseURL + "/me", nil
	}
	return graphBaseURL + "/users/" + urlPathEscape(mailbox), nil
}

// outlookMailDeltaSelect asks Graph for list/browse fields on every delta page (including baseline).
// Without $select, some tenants return stub rows (id only) — meta then has empty subject/from and the UI shows raw message ids.
const outlookMailDeltaSelect = "$select=id,subject,from,receivedDateTime,lastModifiedDateTime,changeKey,hasAttachments"

// InboxMessagesDeltaInitialURL is the first baseline URL for inbox message delta.
func InboxMessagesDeltaInitialURL(userBaseURL string) string {
	return strings.TrimRight(strings.TrimSpace(userBaseURL), "/") + "/mailFolders/inbox/messages/delta?" + outlookMailDeltaSelect
}

// MessagesDeltaURL builds initial delta URL for a folder (default inbox when folderID empty).
func MessagesDeltaURL(userBaseURL, folderID string) string {
	userBaseURL = strings.TrimRight(strings.TrimSpace(userBaseURL), "/")
	folderID = strings.TrimSpace(folderID)
	if folderID == "" {
		folderID = "inbox"
	}
	return fmt.Sprintf("%s/mailFolders/%s/messages/delta?%s", userBaseURL, urlPathEscape(folderID), outlookMailDeltaSelect)
}

// FetchOutlookMailMessagesDeltaPage GETs an absolute Graph delta URL.
func FetchOutlookMailMessagesDeltaPage(ctx context.Context, accessToken, requestURL string) (*OutlookMailDeltaPage, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	requestURL = strings.TrimSpace(requestURL)
	accessToken = strings.TrimSpace(accessToken)
	if requestURL == "" {
		return nil, fmt.Errorf("outlook mail delta url is required")
	}
	if accessToken == "" {
		return nil, fmt.Errorf("access token is required")
	}

	body, status, err := graphDoJSON(ctx, accessToken, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, err
	}
	if status == http.StatusGone || isDeltaResyncRequired(body) {
		return nil, ErrOutlookMailDeltaInvalid
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("outlook mail delta http %d: %s", status, truncateForErr(body))
	}

	var parsed graphOutlookMailDeltaResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("decode outlook mail delta: %w", err)
	}
	out := &OutlookMailDeltaPage{
		NextLink:  strings.TrimSpace(parsed.NextLink),
		DeltaLink: strings.TrimSpace(parsed.DeltaLink),
		Messages:  make([]OutlookMailDeltaMessage, 0, len(parsed.Value)),
	}
	for i := range parsed.Value {
		out.Messages = append(out.Messages, mapGraphOutlookMailMessage(parsed.Value[i]))
	}
	return out, nil
}

func mapGraphOutlookMailMessage(row graphOutlookMailMessageRow) OutlookMailDeltaMessage {
	msg := OutlookMailDeltaMessage{
		ID:                   strings.TrimSpace(row.ID),
		Subject:              strings.TrimSpace(row.Subject),
		ReceivedDateTime:     strings.TrimSpace(row.ReceivedDateTime),
		LastModifiedDateTime: strings.TrimSpace(row.LastModifiedDateTime),
		ChangeKey:            strings.TrimSpace(row.ChangeKey),
		HasAttachments:       row.HasAttachments,
		IsRemoved:            row.Removed != nil,
	}
	if row.From != nil && row.From.EmailAddress != nil {
		msg.From = strings.TrimSpace(row.From.EmailAddress.Address)
	}
	return msg
}

// FetchOutlookMailMessageRaw loads full message JSON from Graph.
func FetchOutlookMailMessageRaw(ctx context.Context, accessToken, userBaseURL, messageID string) ([]byte, *GraphOutlookMailMessageDetail, error) {
	messageID = strings.TrimSpace(messageID)
	userBaseURL = strings.TrimRight(strings.TrimSpace(userBaseURL), "/")
	if messageID == "" || userBaseURL == "" {
		return nil, nil, fmt.Errorf("user base and message id are required")
	}
	url := fmt.Sprintf("%s/messages/%s?$select=id,subject,body,from,toRecipients,receivedDateTime,ccRecipients,bccRecipients,attachments,internetMessageHeaders,internetMessageId,isRead,importance,hasAttachments,changeKey,lastModifiedDateTime&$expand=attachments",
		userBaseURL, urlPathEscape(messageID))
	body, status, err := graphDoJSON(ctx, accessToken, http.MethodGet, url, nil)
	if err != nil {
		return nil, nil, err
	}
	if status < 200 || status >= 300 {
		return nil, nil, fmt.Errorf("outlook mail message http %d: %s", status, truncateForErr(body))
	}
	var parsed GraphOutlookMailMessageDetail
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, nil, fmt.Errorf("decode outlook mail message: %w", err)
	}
	return body, &parsed, nil
}

// FetchOutlookMailMessageByID loads a full message with attachments for mailbox.
func FetchOutlookMailMessageByID(ctx context.Context, accessToken, userBaseURL, messageID string) (*OutlookMessage, error) {
	_, detail, err := FetchOutlookMailMessageRaw(ctx, accessToken, userBaseURL, messageID)
	if err != nil {
		return nil, err
	}
	return graphOutlookMailDetailToOutlookMessage(*detail), nil
}

type GraphOutlookMailMessageDetail struct {
	ID                   string `json:"id"`
	Subject              string `json:"subject"`
	ReceivedDateTime     string `json:"receivedDateTime"`
	LastModifiedDateTime string `json:"lastModifiedDateTime"`
	ChangeKey            string `json:"changeKey"`
	HasAttachments       bool   `json:"hasAttachments"`
	InternetMessageID    string `json:"internetMessageId"`
	IsRead               bool   `json:"isRead"`
	Importance           string `json:"importance"`
	Body                 *struct {
		Content     string `json:"content"`
		ContentType string `json:"contentType"`
	} `json:"body"`
	From *struct {
		EmailAddress *struct {
			Address string `json:"address"`
			Name    string `json:"name"`
		} `json:"emailAddress"`
	} `json:"from"`
	ToRecipients []struct {
		EmailAddress *struct {
			Address string `json:"address"`
		} `json:"emailAddress"`
	} `json:"toRecipients"`
	CcRecipients []struct {
		EmailAddress *struct {
			Address string `json:"address"`
		} `json:"emailAddress"`
	} `json:"ccRecipients"`
	BccRecipients []struct {
		EmailAddress *struct {
			Address string `json:"address"`
		} `json:"emailAddress"`
	} `json:"bccRecipients"`
	Attachments []json.RawMessage `json:"attachments"`
}

func graphOutlookMailDetailToOutlookMessage(d GraphOutlookMailMessageDetail) *OutlookMessage {
	from := ""
	if d.From != nil && d.From.EmailAddress != nil {
		from = strings.TrimSpace(d.From.EmailAddress.Address)
	}
	bodyContent := ""
	if d.Body != nil {
		bodyContent = d.Body.Content
	}
	return &OutlookMessage{
		OutlookMinimalMessage: OutlookMinimalMessage{
			ID:               strings.TrimSpace(d.ID),
			Subject:          strings.TrimSpace(d.Subject),
			From:             from,
			ReceivedDateTime: strings.TrimSpace(d.ReceivedDateTime),
			IsRead:           d.IsRead,
			HasAttachments:   d.HasAttachments,
		},
		Body:              bodyContent,
		InternetMessageID: strings.TrimSpace(d.InternetMessageID),
		ToRecipients:      extractEmailAddresses(d.ToRecipients),
		CcRecipients:      extractCcAddresses(d.CcRecipients),
		BccRecipients:     extractBccAddresses(d.BccRecipients),
		Importance:        strings.TrimSpace(d.Importance),
		HasAttachments:    d.HasAttachments,
		IsRead:            d.IsRead,
	}
}

func extractEmailAddresses(rows []struct {
	EmailAddress *struct {
		Address string `json:"address"`
	} `json:"emailAddress"`
}) []string {
	out := make([]string, 0, len(rows))
	for i := range rows {
		if rows[i].EmailAddress != nil {
			if a := strings.TrimSpace(rows[i].EmailAddress.Address); a != "" {
				out = append(out, a)
			}
		}
	}
	return out
}

func extractCcAddresses(rows []struct {
	EmailAddress *struct {
		Address string `json:"address"`
	} `json:"emailAddress"`
}) []string {
	return extractEmailAddresses(rows)
}

func extractBccAddresses(rows []struct {
	EmailAddress *struct {
		Address string `json:"address"`
	} `json:"emailAddress"`
}) []string {
	return extractEmailAddresses(rows)
}

// OutlookMailIDBasedMetaKey is {email}/meta/{yyyy/mm/dd}/{messageId}.json
func OutlookMailIDBasedMetaKey(email, messageID, receivedTime string) string {
	return fmt.Sprintf("%s/meta/%s/%s.json",
		strings.TrimSpace(email),
		objectKeyDatePath(receivedTime),
		strings.TrimSpace(messageID),
	)
}

// OutlookMailIDBasedDataKey is {email}/data/{yyyy/mm/dd}/{messageId}.json
func OutlookMailIDBasedDataKey(email, messageID, receivedTime string) string {
	return fmt.Sprintf("%s/data/%s/%s.json",
		strings.TrimSpace(email),
		objectKeyDatePath(receivedTime),
		strings.TrimSpace(messageID),
	)
}

// ListOutlookMailFlatMessagesPage lists inbox messages for browse (non-delta).
func ListOutlookMailFlatMessagesPage(ctx context.Context, accessToken, userBaseURL string, skip, top int32) ([]OutlookMailDeltaMessage, error) {
	if top <= 0 {
		top = 50
	}
	if skip < 0 {
		skip = 0
	}
	url := fmt.Sprintf("%s/mailFolders/inbox/messages?$top=%d&$skip=%d&$select=id,subject,from,receivedDateTime,lastModifiedDateTime,changeKey,hasAttachments&$orderby=receivedDateTime%%20desc",
		strings.TrimRight(strings.TrimSpace(userBaseURL), "/"), top, skip)
	body, status, err := graphDoJSON(ctx, accessToken, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("outlook mail list http %d: %s", status, truncateForErr(body))
	}
	var parsed graphOutlookMailDeltaResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	out := make([]OutlookMailDeltaMessage, 0, len(parsed.Value))
	for i := range parsed.Value {
		msg := mapGraphOutlookMailMessage(parsed.Value[i])
		if msg.ID != "" {
			out = append(out, msg)
		}
	}
	return out, nil
}

// OpenOutlookMailAttachmentContent opens attachment bytes for a mailbox message.
func OpenOutlookMailAttachmentContent(ctx context.Context, accessToken, userBaseURL, messageID, attachmentID string) ([]byte, error) {
	userBaseURL = strings.TrimRight(strings.TrimSpace(userBaseURL), "/")
	url := fmt.Sprintf("%s/messages/%s/attachments/%s/$value",
		userBaseURL, urlPathEscape(messageID), urlPathEscape(attachmentID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(accessToken))
	resp, err := graphHTTPDoWithRetry(ctx, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("outlook mail attachment http %d: %s", resp.StatusCode, truncateForErr(b))
	}
	return io.ReadAll(io.LimitReader(resp.Body, 32<<20))
}
