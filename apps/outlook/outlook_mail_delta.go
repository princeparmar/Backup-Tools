package outlook

import (
	"context"
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

// FetchOutlookMailMessageRaw loads full message JSON from Graph.
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
	ConversationID       string `json:"conversationId"`
	From                 *struct {
		EmailAddress *struct {
			Address string `json:"address"`
		} `json:"emailAddress"`
	} `json:"from"`
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
	body, status, err := graphMailDo(ctx, accessToken, http.MethodGet, url, nil, 0)
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
