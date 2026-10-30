package outlook

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// ErrOutlookMailDeltaInvalid means the saved deltaLink can no longer be used; rebaseline required.
var ErrOutlookMailDeltaInvalid = fmt.Errorf("outlook mail delta cursor invalid; rebaseline required")

// OutlookMailDeltaMessage is a message stub from Graph mail delta.
type OutlookMailDeltaMessage struct {
	ID                      string
	Subject                 string
	From                    string
	ReceivedDateTime        string
	LastModifiedDateTime    string
	ChangeKey               string
	HasAttachments          bool
	IsRemoved               bool
	ParentFolderID          string
	Categories              []string
	IsRead                  bool
	FlagStatus              string
	Importance              string
	IsDraft                 bool
	ConversationID          string
	InternetMessageID       string
	InferenceClassification string
}

// OutlookMailDeltaPage is one page of a Graph mail messages delta response.
type OutlookMailDeltaPage struct {
	Messages  []OutlookMailDeltaMessage
	NextLink  string
	DeltaLink string
}

type graphOutlookMailDeltaResponse struct {
	Value     []graphOutlookMailMessageRow `json:"value"`
	NextLink  string                       `json:"@odata.nextLink"`
	DeltaLink string                       `json:"@odata.deltaLink"`
}

type graphOutlookMailMessageRow struct {
	ID                   string   `json:"id"`
	Subject              string   `json:"subject"`
	ReceivedDateTime     string   `json:"receivedDateTime"`
	LastModifiedDateTime string   `json:"lastModifiedDateTime"`
	ChangeKey            string   `json:"changeKey"`
	HasAttachments       bool     `json:"hasAttachments"`
	ParentFolderID       string   `json:"parentFolderId"`
	Categories           []string `json:"categories"`
	IsRead               bool     `json:"isRead"`
	Importance           string   `json:"importance"`
	IsDraft              bool     `json:"isDraft"`
	ConversationID       string   `json:"conversationId"`
	InternetMessageID    string   `json:"internetMessageId"`
	Inference            string   `json:"inferenceClassification"`
	Flag                 *struct {
		FlagStatus string `json:"flagStatus"`
	} `json:"flag"`
	From *struct {
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
	if client.targetUser != "" {
		if strings.TrimSpace(mailbox) == "" {
			mailbox = client.targetUser
		}
		return UserBaseURL(mailbox, "", "", true)
	}
	user, err := client.GetCurrentUser()
	if err != nil {
		return "", err
	}
	return UserBaseURL(mailbox, user.Mail, user.UserPrincipalName, false)
}

// outlookMailDeltaSelect asks Graph for list/browse fields on every delta page (including baseline).
// Without $select, some tenants return stub rows (id only) — meta then has empty subject/from and the UI shows raw message ids.
const outlookMailDeltaSelect = "$select=id,subject,from,receivedDateTime,lastModifiedDateTime,changeKey,hasAttachments," +
	"parentFolderId,categories,isRead,flag,importance,isDraft,conversationId,internetMessageId,inferenceClassification"

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

	body, status, err := graphMailDo(ctx, accessToken, http.MethodGet, requestURL, nil, 0)
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
		ID:                      strings.TrimSpace(row.ID),
		Subject:                 strings.TrimSpace(row.Subject),
		ReceivedDateTime:        strings.TrimSpace(row.ReceivedDateTime),
		LastModifiedDateTime:    strings.TrimSpace(row.LastModifiedDateTime),
		ChangeKey:               strings.TrimSpace(row.ChangeKey),
		HasAttachments:          row.HasAttachments,
		IsRemoved:               row.Removed != nil,
		ParentFolderID:          strings.TrimSpace(row.ParentFolderID),
		Categories:              row.Categories,
		IsRead:                  row.IsRead,
		Importance:              strings.TrimSpace(row.Importance),
		IsDraft:                 row.IsDraft,
		ConversationID:          strings.TrimSpace(row.ConversationID),
		InternetMessageID:       strings.TrimSpace(row.InternetMessageID),
		InferenceClassification: strings.TrimSpace(row.Inference),
	}
	if row.Flag != nil {
		msg.FlagStatus = strings.TrimSpace(row.Flag.FlagStatus)
	}
	if row.From != nil && row.From.EmailAddress != nil {
		msg.From = strings.TrimSpace(row.From.EmailAddress.Address)
	}
	return msg
}

// FetchOutlookMailMessageRaw loads full message JSON from Graph, filling in attachment bytes
// Graph omits from $expand (large file attachments).
func FetchOutlookMailMessageRaw(ctx context.Context, accessToken, userBaseURL, messageID string) ([]byte, *GraphOutlookMailMessageDetail, error) {
	messageID = strings.TrimSpace(messageID)
	userBaseURL = strings.TrimRight(strings.TrimSpace(userBaseURL), "/")
	if messageID == "" || userBaseURL == "" {
		return nil, nil, fmt.Errorf("user base and message id are required")
	}
	url := fmt.Sprintf("%s/messages/%s?$select=id,subject,body,from,sender,replyTo,toRecipients,receivedDateTime,sentDateTime,ccRecipients,bccRecipients,attachments,internetMessageHeaders,internetMessageId,isRead,importance,hasAttachments,changeKey,lastModifiedDateTime,parentFolderId,categories,flag,isDraft,conversationId,inferenceClassification&$expand=attachments",
		userBaseURL, urlPathEscape(messageID))
	body, status, err := graphMailDo(ctx, accessToken, http.MethodGet, url, nil, mailMessageReadLimit)
	if err != nil {
		return nil, nil, err
	}
	if status < 200 || status >= 300 {
		return nil, nil, fmt.Errorf("outlook mail message http %d: %s", status, truncateForErr(body))
	}
	body, err = ensureMailAttachmentBytes(ctx, accessToken, userBaseURL, messageID, body)
	if err != nil {
		return nil, nil, err
	}
	var parsed GraphOutlookMailMessageDetail
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, nil, fmt.Errorf("decode outlook mail message: %w", err)
	}
	return body, &parsed, nil
}

// GraphOutlookMailMessageDetail holds the fields of a fetched message used to build its backup
// key; the backup itself is the raw Graph JSON.
type GraphOutlookMailMessageDetail struct {
	ID                   string `json:"id"`
	Subject              string `json:"subject"`
	ReceivedDateTime     string `json:"receivedDateTime"`
	LastModifiedDateTime string `json:"lastModifiedDateTime"`
	ChangeKey            string `json:"changeKey"`
	HasAttachments       bool   `json:"hasAttachments"`
	ConversationID       string `json:"conversationId"`
	From                 *struct {
		EmailAddress *struct {
			Address string `json:"address"`
		} `json:"emailAddress"`
	} `json:"from"`
}

// ensureMailAttachmentBytes lists attachments when expand returned none, and fetches
// contentBytes (or /$value) for file attachments Graph omitted from the expand payload.
func ensureMailAttachmentBytes(ctx context.Context, accessToken, userBaseURL, messageID string, raw []byte) ([]byte, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil || doc == nil {
		return raw, nil
	}
	atts, err := mailAttachmentRawList(doc)
	if err != nil {
		return nil, err
	}
	hasAttachments := false
	if v, ok := doc["hasAttachments"]; ok {
		_ = json.Unmarshal(v, &hasAttachments)
	}
	listedFromEmpty := false
	if len(atts) == 0 && hasAttachments {
		listed, listErr := listMailMessageAttachments(ctx, accessToken, userBaseURL, messageID)
		if listErr != nil {
			return nil, listErr
		}
		atts = listed
		listedFromEmpty = len(atts) > 0
	}
	if len(atts) == 0 {
		return raw, nil
	}

	filledAny := false
	for i := range atts {
		var meta struct {
			ODataType    string `json:"@odata.type"`
			ID           string `json:"id"`
			ContentBytes string `json:"contentBytes"`
			Name         string `json:"name"`
			ContentType  string `json:"contentType"`
			IsInline     bool   `json:"isInline"`
			ContentID    string `json:"contentId"`
		}
		if err := json.Unmarshal(atts[i], &meta); err != nil {
			continue
		}
		if meta.ODataType != "#microsoft.graph.fileAttachment" || strings.TrimSpace(meta.ContentBytes) != "" {
			continue
		}
		attID := strings.TrimSpace(meta.ID)
		if attID == "" {
			continue
		}
		filled, fetchErr := fetchMailFileAttachmentJSON(ctx, accessToken, userBaseURL, messageID, attID, meta.Name, meta.ContentType, meta.IsInline, meta.ContentID)
		if fetchErr != nil {
			return nil, fmt.Errorf("attachment %q: %w", meta.Name, fetchErr)
		}
		atts[i] = filled
		filledAny = true
	}
	if !filledAny && !listedFromEmpty {
		return raw, nil
	}
	encoded, err := json.Marshal(atts)
	if err != nil {
		return nil, err
	}
	doc["attachments"] = encoded
	out, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func encodeMailAttachmentBytes(raw []byte) string {
	return base64.StdEncoding.EncodeToString(raw)
}

func mailAttachmentRawList(doc map[string]json.RawMessage) ([]json.RawMessage, error) {
	raw, ok := doc["attachments"]
	if !ok || len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var atts []json.RawMessage
	if err := json.Unmarshal(raw, &atts); err != nil {
		return nil, fmt.Errorf("decode attachments: %w", err)
	}
	return atts, nil
}

func listMailMessageAttachments(ctx context.Context, accessToken, userBaseURL, messageID string) ([]json.RawMessage, error) {
	reqURL := fmt.Sprintf("%s/messages/%s/attachments?$top=100", userBaseURL, urlPathEscape(messageID))
	var out []json.RawMessage
	for reqURL != "" {
		body, status, err := graphMailDo(ctx, accessToken, http.MethodGet, reqURL, nil, mailMessageReadLimit)
		if err != nil {
			return nil, err
		}
		if status < 200 || status >= 300 {
			return nil, fmt.Errorf("list attachments http %d: %s", status, truncateForErr(body))
		}
		var page struct {
			Value    []json.RawMessage `json:"value"`
			NextLink string            `json:"@odata.nextLink"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("decode attachment list: %w", err)
		}
		out = append(out, page.Value...)
		reqURL = strings.TrimSpace(page.NextLink)
	}
	return out, nil
}

func fetchMailFileAttachmentJSON(ctx context.Context, accessToken, userBaseURL, messageID, attachmentID, name, contentType string, isInline bool, contentID string) (json.RawMessage, error) {
	metaURL := fmt.Sprintf("%s/messages/%s/attachments/%s", userBaseURL, urlPathEscape(messageID), urlPathEscape(attachmentID))
	body, status, err := graphMailDo(ctx, accessToken, http.MethodGet, metaURL, nil, mailMessageReadLimit)
	if err != nil {
		return nil, err
	}
	if status >= 200 && status < 300 {
		var meta struct {
			ContentBytes string `json:"contentBytes"`
		}
		if json.Unmarshal(body, &meta) == nil && strings.TrimSpace(meta.ContentBytes) != "" {
			return body, nil
		}
	}

	valueURL := metaURL + "/$value"
	raw, status, err := graphMailDo(ctx, accessToken, http.MethodGet, valueURL, nil, mailMessageReadLimit)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("attachment $value http %d: %s", status, truncateForErr(raw))
	}
	payload := map[string]interface{}{
		"@odata.type":  "#microsoft.graph.fileAttachment",
		"id":           attachmentID,
		"name":         name,
		"contentBytes": encodeMailAttachmentBytes(raw),
		"isInline":     isInline,
	}
	if contentType != "" {
		payload["contentType"] = contentType
	}
	if contentID != "" {
		payload["contentId"] = contentID
	}
	return json.Marshal(payload)
}

