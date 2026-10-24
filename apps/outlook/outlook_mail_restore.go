package outlook

import (
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/microsoftgraph/msgraph-sdk-go/models"
)

// MailObjectDownloader loads one backup object by key.
type MailObjectDownloader func(key string) ([]byte, error)

// Graph rejects create requests above ~4 MB; larger attachments are added after the message exists.
const (
	mailInlineAttachmentBudget = 3 << 20
	mailUploadChunkSize        = 10 * 320 * 1024
)

// MAPI properties that make a created message look received: PR_MESSAGE_FLAGS without
// MSGFLAG_UNSENT (not a draft), plus the delivery and submit times.
const (
	mapiMessageFlags      = "Integer 0x0E07"
	mapiDeliveryTime      = "SystemTime 0x0E06"
	mapiClientSubmitTime  = "SystemTime 0x0039"
	mapiMessageFlagRead   = "1"
	mapiMessageFlagUnread = "0"
)

// MailRestoreFolder is where a message is restored. An empty path restores into WellKnown,
// or the Inbox.
type MailRestoreFolder struct {
	Path      string
	WellKnown string
}

// RestoreMailFromBackup restores one mail backup into its original folder (created if missing).
// Messages removed from the mailbox are restored too. Legacy meta/data backups go to the Inbox.
func RestoreMailFromBackup(ctx context.Context, accessToken, userBaseURL, objectKey string, download MailObjectDownloader) error {
	if obj, ok := ParseOutlookMailObjectKey(objectKey); ok {
		raw, err := download(objectKey)
		if err != nil {
			return fmt.Errorf("download mail backup: %w", err)
		}
		state := ReadOutlookMailBackupState(raw)
		folder := MailRestoreFolder{Path: state.FolderPath, WellKnown: state.WellKnownFolder}
		if folder.Path == "" {
			folder.Path = obj.FolderPath
		}
		return RestoreMailMessage(ctx, accessToken, userBaseURL, folder, raw)
	}
	dataKey := OutlookMailLegacyDataKey(objectKey)
	if dataKey == "" {
		return fmt.Errorf("not an outlook mail key: %s", objectKey)
	}
	data, err := download(dataKey)
	if err != nil {
		return fmt.Errorf("download mail data: %w", err)
	}
	return RestoreMailMessage(ctx, accessToken, userBaseURL, MailRestoreFolder{}, data)
}

// mailRestoreAttachment is one file attachment to restore.
type mailRestoreAttachment struct {
	Name         string `json:"name"`
	ContentType  string `json:"contentType,omitempty"`
	ContentBytes string `json:"contentBytes"`
	IsInline     bool   `json:"isInline"`
	ContentID    string `json:"contentId,omitempty"`
}

func (a mailRestoreAttachment) graphJSON() map[string]interface{} {
	m := map[string]interface{}{
		"@odata.type": "#microsoft.graph.fileAttachment", "name": a.Name,
		"contentBytes": a.ContentBytes, "isInline": a.IsInline,
	}
	if a.ContentType != "" {
		m["contentType"] = a.ContentType
	}
	if a.ContentID != "" {
		m["contentId"] = a.ContentID
	}
	return m
}

// mailRestoreMessage is the message to recreate, from Graph JSON (or the legacy OutlookMessage
// JSON written by older manual backups).
type mailRestoreMessage struct {
	Fields      map[string]json.RawMessage
	Received    string
	Sent        string
	IsRead      bool
	IsDraft     bool
	Categories  []string
	FlagStatus  string
	Attachments []mailRestoreAttachment
	Skipped     int
}

var mailRestoreCopiedFields = []string{"subject", "body", "from", "sender", "toRecipients", "ccRecipients", "bccRecipients", "replyTo", "importance"}

func parseMailRestoreMessage(data []byte) (*mailRestoreMessage, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse outlook message: %w", err)
	}
	if _, legacy := raw["to_recipients"]; legacy {
		return legacyMailRestoreMessage(data)
	}
	if _, legacy := raw["received_datetime"]; legacy {
		return legacyMailRestoreMessage(data)
	}
	out := &mailRestoreMessage{Fields: map[string]json.RawMessage{}}
	for _, f := range mailRestoreCopiedFields {
		if v, ok := raw[f]; ok && string(v) != "null" {
			out.Fields[f] = v
		}
	}
	var d struct {
		ReceivedDateTime string            `json:"receivedDateTime"`
		SentDateTime     string            `json:"sentDateTime"`
		IsRead           bool              `json:"isRead"`
		IsDraft          bool              `json:"isDraft"`
		Categories       []string          `json:"categories"`
		Attachments      []json.RawMessage `json:"attachments"`
		Flag             *struct {
			FlagStatus string `json:"flagStatus"`
		} `json:"flag"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, fmt.Errorf("parse outlook message: %w", err)
	}
	out.Received, out.Sent, out.IsRead, out.IsDraft, out.Categories = d.ReceivedDateTime, d.SentDateTime, d.IsRead, d.IsDraft, d.Categories
	if d.Flag != nil {
		out.FlagStatus = d.Flag.FlagStatus
	}
	for _, a := range d.Attachments {
		var att struct {
			ODataType    string `json:"@odata.type"`
			Name         string `json:"name"`
			ContentType  string `json:"contentType"`
			ContentBytes string `json:"contentBytes"`
			IsInline     bool   `json:"isInline"`
			ContentID    string `json:"contentId"`
		}
		if err := json.Unmarshal(a, &att); err != nil || att.ODataType != "#microsoft.graph.fileAttachment" || att.ContentBytes == "" {
			out.Skipped++
			continue
		}
		out.Attachments = append(out.Attachments, mailRestoreAttachment{
			Name: att.Name, ContentType: att.ContentType, ContentBytes: att.ContentBytes, IsInline: att.IsInline, ContentID: att.ContentID,
		})
	}
	return out, nil
}

func legacyMailRestoreMessage(data []byte) (*mailRestoreMessage, error) {
	var m OutlookMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse outlook message: %w", err)
	}
	contentType := "text"
	if m.ContentType != nil && *m.ContentType == models.HTML_BODYTYPE {
		contentType = "html"
	}
	recipients := func(addrs []string) []map[string]interface{} {
		out := make([]map[string]interface{}, 0, len(addrs))
		for _, a := range addrs {
			if a = strings.TrimSpace(a); a != "" {
				out = append(out, map[string]interface{}{"emailAddress": map[string]string{"address": a}})
			}
		}
		return out
	}
	fields := map[string]interface{}{
		"subject": m.Subject,
		"body":    map[string]string{"contentType": contentType, "content": m.Body},
	}
	if m.From != "" {
		fields["from"] = map[string]interface{}{"emailAddress": map[string]string{"address": m.From}}
	}
	if r := recipients(m.ToRecipients); len(r) > 0 {
		fields["toRecipients"] = r
	}
	if r := recipients(m.CcRecipients); len(r) > 0 {
		fields["ccRecipients"] = r
	}
	if r := recipients(m.BccRecipients); len(r) > 0 {
		fields["bccRecipients"] = r
	}
	if imp := strings.ToLower(strings.TrimSpace(m.Importance)); imp == "low" || imp == "normal" || imp == "high" {
		fields["importance"] = imp
	}
	out := &mailRestoreMessage{Fields: map[string]json.RawMessage{}, IsRead: m.IsRead, Categories: m.Categories}
	for k, v := range fields {
		b, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		out.Fields[k] = b
	}
	out.Received = legacyMailTime(m.ReceivedDateTime)
	out.Sent = legacyMailTime(m.SentDateTime)
	for _, a := range m.Attachments {
		if a == nil || len(a.Data) == 0 {
			continue
		}
		ct := ""
		if a.ContentType != nil {
			ct = *a.ContentType
		}
		out.Attachments = append(out.Attachments, mailRestoreAttachment{
			Name: a.Name, ContentType: ct, ContentBytes: base64.StdEncoding.EncodeToString(a.Data), IsInline: a.IsInline, ContentID: a.ContentID,
		})
	}
	return out, nil
}

// legacyMailTime converts the legacy unix-millis (or RFC3339) date to RFC3339 UTC.
func legacyMailTime(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if ms, err := strconv.ParseInt(v, 10, 64); err == nil {
		return time.UnixMilli(ms).UTC().Format(time.RFC3339)
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t.UTC().Format(time.RFC3339)
	}
	return ""
}

// RestoreMailMessage creates the message in folder as a received (non-draft) item with its
// received date, read state, flag and categories.
func RestoreMailMessage(ctx context.Context, accessToken, userBaseURL string, folder MailRestoreFolder, data []byte) error {
	userBaseURL = strings.TrimRight(strings.TrimSpace(userBaseURL), "/")
	if userBaseURL == "" {
		return fmt.Errorf("restore mailbox is required")
	}
	msg, err := parseMailRestoreMessage(data)
	if err != nil {
		return err
	}

	folderID, err := resolveMailRestoreFolder(ctx, accessToken, userBaseURL, folder.WellKnown, folder.Path)
	if err != nil {
		return fmt.Errorf("restore folder %q: %w", folder.Path, err)
	}

	payload := make(map[string]interface{}, len(msg.Fields)+6)
	for k, v := range msg.Fields {
		payload[k] = v
	}
	payload["isRead"] = msg.IsRead
	if len(msg.Categories) > 0 {
		payload["categories"] = msg.Categories
	}
	if fs := strings.TrimSpace(msg.FlagStatus); fs != "" && fs != "notFlagged" {
		payload["flag"] = map[string]string{"flagStatus": fs}
	}
	var props []map[string]string
	if !msg.IsDraft {
		flags := mapiMessageFlagUnread
		if msg.IsRead {
			flags = mapiMessageFlagRead
		}
		props = append(props, map[string]string{"id": mapiMessageFlags, "value": flags})
	}
	if msg.Received != "" {
		props = append(props, map[string]string{"id": mapiDeliveryTime, "value": msg.Received})
	}
	if sent := cmp.Or(msg.Sent, msg.Received); sent != "" {
		props = append(props, map[string]string{"id": mapiClientSubmitTime, "value": sent})
	}
	if len(props) > 0 {
		payload["singleValueExtendedProperties"] = props
	}

	var inline []map[string]interface{}
	var later []mailRestoreAttachment
	budget := mailInlineAttachmentBudget
	for _, a := range msg.Attachments {
		if len(a.ContentBytes) <= budget {
			inline = append(inline, a.graphJSON())
			budget -= len(a.ContentBytes)
			continue
		}
		later = append(later, a)
	}
	if len(inline) > 0 {
		payload["attachments"] = inline
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	createURL := fmt.Sprintf("%s/mailFolders/%s/messages", userBaseURL, urlPathEscape(folderID))
	resp, status, err := graphMailDo(ctx, accessToken, http.MethodPost, createURL, body, 0)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("create restored message http %d: %s", status, truncateForErr(resp))
	}
	var created struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(resp, &created)

	failed := msg.Skipped
	for _, a := range later {
		if err := addMailRestoreAttachment(ctx, accessToken, userBaseURL, created.ID, a); err != nil {
			failed++
			logger.Warn(ctx, "outlook mail restore: attachment not restored",
				logger.String("attachment", a.Name), logger.ErrorField(err))
		}
	}
	if failed > 0 {
		logger.Warn(ctx, "outlook mail restore: message restored without some attachments",
			logger.String("message_id", created.ID), logger.Int("attachments_missing", failed))
	}
	return nil
}

func addMailRestoreAttachment(ctx context.Context, accessToken, userBaseURL, messageID string, a mailRestoreAttachment) error {
	if strings.TrimSpace(messageID) == "" {
		return fmt.Errorf("restored message id missing")
	}
	if len(a.ContentBytes) <= mailInlineAttachmentBudget {
		body, err := json.Marshal(a.graphJSON())
		if err != nil {
			return err
		}
		resp, status, err := graphMailDo(ctx, accessToken, http.MethodPost,
			fmt.Sprintf("%s/messages/%s/attachments", userBaseURL, urlPathEscape(messageID)), body, 0)
		if err != nil {
			return err
		}
		if status < 200 || status >= 300 {
			return fmt.Errorf("add attachment http %d: %s", status, truncateForErr(resp))
		}
		return nil
	}
	content, err := base64.StdEncoding.DecodeString(a.ContentBytes)
	if err != nil {
		return fmt.Errorf("decode attachment: %w", err)
	}
	item := map[string]interface{}{"attachmentType": "file", "name": a.Name, "size": len(content), "isInline": a.IsInline}
	if a.ContentType != "" {
		item["contentType"] = a.ContentType
	}
	if a.ContentID != "" {
		item["contentId"] = a.ContentID
	}
	body, err := json.Marshal(map[string]interface{}{"AttachmentItem": item})
	if err != nil {
		return err
	}
	resp, status, err := graphMailDo(ctx, accessToken, http.MethodPost,
		fmt.Sprintf("%s/messages/%s/attachments/createUploadSession", userBaseURL, urlPathEscape(messageID)), body, 0)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("create attachment upload session http %d: %s", status, truncateForErr(resp))
	}
	var session struct {
		UploadURL string `json:"uploadUrl"`
	}
	if err := json.Unmarshal(resp, &session); err != nil || strings.TrimSpace(session.UploadURL) == "" {
		return fmt.Errorf("attachment upload session has no uploadUrl")
	}
	total := len(content)
	for start := 0; start < total; start += mailUploadChunkSize {
		end := start + mailUploadChunkSize
		if end > total {
			end = total
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, session.UploadURL, bytes.NewReader(content[start:end]))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end-1, total))
		req.ContentLength = int64(end - start)
		res, err := graphHTTPDoWithRetry(ctx, req)
		if err != nil {
			return err
		}
		_ = res.Body.Close()
		if res.StatusCode < 200 || res.StatusCode >= 300 {
			return fmt.Errorf("upload attachment chunk http %d", res.StatusCode)
		}
	}
	return nil
}

// mailRestoreFolderCache maps mailbox + folder path to the folder id found or created.
var (
	mailRestoreFolderCache   = map[string]string{}
	mailRestoreFolderCacheMu sync.Mutex
)

const mailRestoreFolderCacheMax = 5000

// resolveMailRestoreFolder returns the id of the folder to restore into, creating missing custom
// folders along the path. Keys without a folder (legacy) restore into the Inbox.
func resolveMailRestoreFolder(ctx context.Context, accessToken, userBaseURL, wellKnown, folderPath string) (string, error) {
	segments := splitMailFolderPath(folderPath)
	if len(segments) == 0 {
		if wk := strings.TrimSpace(wellKnown); wk != "" {
			return wk, nil
		}
		return "inbox", nil
	}
	cacheKey := userBaseURL + "\x00" + strings.Join(segments, "/")
	mailRestoreFolderCacheMu.Lock()
	cached := mailRestoreFolderCache[cacheKey]
	mailRestoreFolderCacheMu.Unlock()
	if cached != "" {
		return cached, nil
	}

	parent, rest := "msgfolderroot", segments
	if wk := MailWellKnownFolderForSegment(segments[0]); wk != "" {
		parent, rest = wk, segments[1:]
	}
	for _, name := range rest {
		id, err := findOrCreateMailChildFolder(ctx, accessToken, userBaseURL, parent, name)
		if err != nil {
			return "", err
		}
		parent = id
	}

	mailRestoreFolderCacheMu.Lock()
	if len(mailRestoreFolderCache) >= mailRestoreFolderCacheMax {
		mailRestoreFolderCache = map[string]string{}
	}
	mailRestoreFolderCache[cacheKey] = parent
	mailRestoreFolderCacheMu.Unlock()
	return parent, nil
}

func splitMailFolderPath(p string) []string {
	var out []string
	for _, s := range strings.Split(strings.Trim(strings.TrimSpace(p), "/"), "/") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func findOrCreateMailChildFolder(ctx context.Context, accessToken, userBaseURL, parentID, name string) (string, error) {
	filter := url.QueryEscape("displayName eq '" + strings.ReplaceAll(name, "'", "''") + "'")
	listURL := fmt.Sprintf("%s/mailFolders/%s/childFolders?$filter=%s&$select=id", userBaseURL, urlPathEscape(parentID), filter)
	body, status, err := graphMailDo(ctx, accessToken, http.MethodGet, listURL, nil, 0)
	if err != nil {
		return "", err
	}
	if status >= 200 && status < 300 {
		var page struct {
			Value []struct {
				ID string `json:"id"`
			} `json:"value"`
		}
		if json.Unmarshal(body, &page) == nil && len(page.Value) > 0 && page.Value[0].ID != "" {
			return page.Value[0].ID, nil
		}
	}
	payload, err := json.Marshal(map[string]string{"displayName": name})
	if err != nil {
		return "", err
	}
	body, status, err = graphMailDo(ctx, accessToken, http.MethodPost,
		fmt.Sprintf("%s/mailFolders/%s/childFolders", userBaseURL, urlPathEscape(parentID)), payload, 0)
	if err != nil {
		return "", err
	}
	if status < 200 || status >= 300 {
		return "", fmt.Errorf("create mail folder %q http %d: %s", name, status, truncateForErr(body))
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &created); err != nil || created.ID == "" {
		return "", fmt.Errorf("create mail folder %q: no id returned", name)
	}
	return created.ID, nil
}
