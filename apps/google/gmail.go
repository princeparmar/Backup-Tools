package google

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"mime/quotedprintable"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/middleware"
	"github.com/StorX2-0/Backup-Tools/pkg/utils"

	"github.com/labstack/echo/v4"
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

type GmailClient struct {
	*gmail.Service
}

type ThreadsResponse struct {
	NextPageToken      string          `json:"nextPageToken"`
	ResultSizeEstimate int             `json:"resultSizeEstimate"`
	Threads            []*gmail.Thread `json:"threads"`
}

type MessagesResponse struct {
	/*Messages []struct {
		ID       string `json:"id"`
		ThreadID string `json:"threadId"`
	} `json:"messages"`*/
	Messages           []*gmail.Message `json:"messages"`
	NextPageToken      string           `json:"nextPageToken"`
	ResultSizeEstimate int              `json:"resultSizeEstimate"`
}

// GmailFilter represents filter parameters for Gmail message queries
type GmailFilter struct {
	From          string `json:"from,omitempty"`          // Filter by sender email
	To            string `json:"to,omitempty"`            // Filter by recipient email
	Subject       string `json:"subject,omitempty"`       // Filter by subject
	HasAttachment bool   `json:"hasAttachment,omitempty"` // Filter messages with attachments
	After         string `json:"after,omitempty"`         // Filter messages after date (YYYY/MM/DD)
	Before        string `json:"before,omitempty"`        // Filter messages before date (YYYY/MM/DD)
	NewerThan     string `json:"newerThan,omitempty"`     // Filter messages newer than (e.g., "1d", "1w", "1m")
	OlderThan     string `json:"olderThan,omitempty"`     // Filter messages older than (e.g., "1d", "1w", "1m")
	Query         string `json:"query,omitempty"`         // Raw Gmail search query
}

// Change in SQLite too if changing smth here
type GmailMessage struct {
	ID          string        `json:"message_id"`
	Date        int64         `json:"date"`
	From        string        `json:"from"`
	To          string        `json:"to"`
	Subject     string        `json:"subject"`
	Body        string        `json:"body"`
	Attachments []*Attachment `json:"attachments"`
}

// Change in SQLite too if changing smth here
type Attachment struct {
	FileName string
	Data     []byte
}

// gmailLabelsKeySeparator joins label IDs in the object-key labelsSegment (not a Gmail id charset char).
const gmailLabelsKeySeparator = "^"

var gmailLabelsDroppedFromKey = map[string]struct{}{
	"UNREAD": {},
	"CHAT":   {},
}

// sanitizeGmailLabelIDForKey replaces path-unsafe or separator chars in a label id.
func sanitizeGmailLabelIDForKey(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	id = strings.ReplaceAll(id, "/", "_")
	id = strings.ReplaceAll(id, gmailLabelsKeySeparator, "_")
	return id
}

// GmailBackupLabelsForKey returns label ids kept in the object key (sorted, deduped, sanitized).
// Drops UNREAD/CHAT; keeps system + user labels. Empty after filter → nil (caller uses "_").
func GmailBackupLabelsForKey(labelIDs []string) []string {
	seen := make(map[string]struct{}, len(labelIDs))
	out := make([]string, 0, len(labelIDs))
	for _, raw := range labelIDs {
		id := sanitizeGmailLabelIDForKey(raw)
		if id == "" {
			continue
		}
		if _, drop := gmailLabelsDroppedFromKey[id]; drop {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// GmailLabelsSegment joins labels for the key path component, or "_" if none.
func GmailLabelsSegment(labelIDs []string) string {
	labels := GmailBackupLabelsForKey(labelIDs)
	if len(labels) == 0 {
		return "_"
	}
	return strings.Join(labels, gmailLabelsKeySeparator)
}

// GmailObjectKey returns:
//
//	{email}/{labelsSegment}/{yyyy}/{mm}/{dd}/{from} - {subject} - {threadId} - {messageId}.gmail
//
// Older keys omit threadId (from - subject - messageId). labelsSegment is ^-joined label ids.
func GmailObjectKey(email string, msg *gmail.Message) string {
	email = strings.TrimSpace(email)
	title := utils.GenerateTitleFromGmailMessage(msg)
	ms := int64(0)
	var labelIDs []string
	if msg != nil {
		ms = msg.InternalDate
		labelIDs = msg.LabelIds
	}
	seg := GmailLabelsSegment(labelIDs)
	return email + "/" + seg + "/" + ObjectKeyDatePathFromUnixMilli(ms) + "/" + title
}

// ParsedGmailObjectKey is the result of ParseGmailObjectKey.
type ParsedGmailObjectKey struct {
	Email     string
	Labels    []string
	ThreadID  string // empty on older keys that only embed message id
	MessageID string
	Legacy    bool // true when key has no labelsSegment (flat date path)
}

// looksLikeGmailAPIID is a heuristic for opaque Gmail message/thread ids in object keys.
func looksLikeGmailAPIID(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) < 10 || len(s) > 128 || strings.ContainsAny(s, " \t") {
		return false
	}
	for _, r := range s {
		ok := (r >= '0' && r <= '9') ||
			(r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			r == '_' || r == '-'
		if !ok {
			return false
		}
	}
	return true
}

// parseGmailFileIDs extracts threadId (optional) and messageId from the .gmail filename.
func parseGmailFileIDs(file string) (threadID, messageID string) {
	base := strings.TrimSuffix(file, ".gmail")
	i := strings.LastIndex(base, " - ")
	if i < 0 {
		return "", ""
	}
	messageID = strings.TrimSpace(base[i+3:])
	prev := base[:i]
	j := strings.LastIndex(prev, " - ")
	if j >= 0 {
		candidate := strings.TrimSpace(prev[j+3:])
		if looksLikeGmailAPIID(candidate) {
			return candidate, messageID
		}
	}
	return "", messageID
}

// ParseGmailObjectKey parses labeled or legacy Gmail object keys.
func ParseGmailObjectKey(key string) (ParsedGmailObjectKey, bool) {
	key = strings.TrimSpace(key)
	if key == "" || !strings.HasSuffix(key, ".gmail") {
		return ParsedGmailObjectKey{}, false
	}
	parts := strings.Split(key, "/")
	if len(parts) < 5 {
		return ParsedGmailObjectKey{}, false
	}
	email := parts[0]
	file := parts[len(parts)-1]
	threadID, msgID := parseGmailFileIDs(file)
	if msgID == "" {
		return ParsedGmailObjectKey{}, false
	}

	// Labeled: email / labelsSegment / yyyy / mm / dd / file  (len >= 6)
	// Legacy:  email / yyyy / mm / dd / file                   (len == 5)
	if len(parts) >= 6 {
		yyyy, mm, dd := parts[len(parts)-4], parts[len(parts)-3], parts[len(parts)-2]
		if looksLikeYear(yyyy) && looksLikeMonthDay(mm) && looksLikeMonthDay(dd) {
			seg := parts[1]
			var labels []string
			if seg != "_" && seg != "" {
				labels = strings.Split(seg, gmailLabelsKeySeparator)
			}
			return ParsedGmailObjectKey{Email: email, Labels: labels, ThreadID: threadID, MessageID: msgID, Legacy: false}, true
		}
	}
	if len(parts) == 5 {
		yyyy, mm, dd := parts[1], parts[2], parts[3]
		if looksLikeYear(yyyy) && looksLikeMonthDay(mm) && looksLikeMonthDay(dd) {
			return ParsedGmailObjectKey{Email: email, Labels: nil, ThreadID: threadID, MessageID: msgID, Legacy: true}, true
		}
	}
	// Fallback: treat as labeled if we have email/seg/…/file
	if len(parts) >= 6 {
		seg := parts[1]
		var labels []string
		if seg != "_" && seg != "" {
			labels = strings.Split(seg, gmailLabelsKeySeparator)
		}
		return ParsedGmailObjectKey{Email: email, Labels: labels, ThreadID: threadID, MessageID: msgID, Legacy: false}, true
	}
	return ParsedGmailObjectKey{}, false
}

func looksLikeYear(s string) bool {
	if len(s) != 4 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func looksLikeMonthDay(s string) bool {
	if len(s) != 2 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// ObjectKeyHasGmailLabel reports whether the key's labelsSegment contains an exact label token.
// Legacy keys (no segment) never match a specific label.
func ObjectKeyHasGmailLabel(key, labelID string) bool {
	labelID = sanitizeGmailLabelIDForKey(labelID)
	if labelID == "" {
		return false
	}
	parsed, ok := ParseGmailObjectKey(key)
	if !ok || parsed.Legacy {
		return false
	}
	for _, l := range parsed.Labels {
		if l == labelID {
			return true
		}
	}
	return false
}

// FindExistingGmailKeyByMessageID returns any synced key under email ending with " - {id}.gmail".
func FindExistingGmailKeyByMessageID(syncedMap map[string]bool, email, messageID string) string {
	if syncedMap == nil || strings.TrimSpace(messageID) == "" {
		return ""
	}
	suffix := " - " + messageID + ".gmail"
	prefix := strings.TrimSpace(email) + "/"
	for key := range syncedMap {
		if strings.HasPrefix(key, prefix) && strings.HasSuffix(key, suffix) {
			return key
		}
	}
	return ""
}

// IsGmailMessageSynced reports whether a message exists under any dated Gmail object key for its id.
func IsGmailMessageSynced(syncedMap map[string]bool, email string, msg *gmail.Message) bool {
	if syncedMap == nil || msg == nil {
		return false
	}
	if syncedMap[GmailObjectKey(email, msg)] {
		return true
	}
	return FindExistingGmailKeyByMessageID(syncedMap, email, msg.Id) != ""
}

func NewGmailClient(c echo.Context) (*GmailClient, error) {

	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)

	googleToken, err := GetGoogleTokenFromJWT(c)
	if err != nil {
		return nil, fmt.Errorf("unable to retrieve google-auth token from JWT: %v", err)
	}
	token, err := database.AuthRepo.ReadGoogleAuthToken(googleToken)
	if err != nil {
		return nil, fmt.Errorf("unable to retrieve google-auth token from database: %v", err)
	}

	return NewGmailClientUsingToken(token)
}

func NewGmailClientUsingToken(token string) (*GmailClient, error) {
	client, err := clientUsingToken(token)

	if err != nil {
		return nil, err
	}

	serv, err := gmail.NewService(context.Background(), option.WithHTTPClient(client))
	if err != nil {
		return nil, err
	}

	return &GmailClient{serv}, nil
}

// NewGmailClientForRestore builds a Gmail client for restore (requires gmail.insert on token).
func NewGmailClientForRestore(token string) (*GmailClient, error) {
	client, err := clientUsingTokenScopes(token, restoreGmailScope)
	if err != nil {
		return nil, err
	}
	serv, err := gmail.NewService(context.Background(), option.WithHTTPClient(client))
	if err != nil {
		return nil, err
	}
	return &GmailClient{serv}, nil
}

// Function takes nextPageToken and returns 100 results of User's threads.
// (Pass `""` if you don't want to specify nextPageToken and get latest threads).
func (client *GmailClient) GetUserThreads(nextPageToken string) (*ThreadsResponse, error) {

	req := client.Users.Threads.List("me").MaxResults(500)
	if nextPageToken != "" {
		req.PageToken(nextPageToken)
	}

	threads, err := req.Do()
	if err != nil {
		return nil, err
	}

	ts := make([]*gmail.Thread, 0, len(threads.Threads))
	for _, t := range threads.Threads {
		if thread, err := client.Users.Threads.Get("me", t.Id).Do(); err == nil {
			ts = append(ts, thread)
		}
	}

	return &ThreadsResponse{
		NextPageToken:      threads.NextPageToken,
		ResultSizeEstimate: int(threads.ResultSizeEstimate),
		Threads:            ts,
	}, nil
}

// InsertMessage inserts a message into Gmail
func (client *GmailClient) InsertMessage(message *gmail.Message) error {
	raw, err := createRawMessage(message)
	if err != nil {
		return err
	}

	_, err = client.Users.Messages.Import("me", &gmail.Message{
		Raw:      raw,
		LabelIds: message.LabelIds,
	}).Do()

	return err
}

func (client *GmailClient) GetUserThreadsIDs(nextPageToken string) (*gmail.ListThreadsResponse, error) {
	req := client.Users.Threads.List("me").MaxResults(500)
	if nextPageToken != "" {
		req.PageToken(nextPageToken)
	}
	return req.Do()
}

// Function takes nextPageToken and returns 100 results of User's messages.
// (Pass `""` if you don't want to specify nextPageToken and get latest messages).
func (client *GmailClient) GetUserMessages(nextPageToken string) (*MessagesResponse, error) {

	req := client.Users.Messages.List("me").MaxResults(500)
	if nextPageToken != "" {
		req.PageToken(nextPageToken)
	}

	res, err := req.Do()
	if err != nil {
		return nil, err
	}

	messages := make([]*gmail.Message, 0, len(res.Messages))
	for _, msg := range res.Messages {
		if message, err := client.Users.Messages.Get("me", msg.Id).Do(); err == nil {
			messages = append(messages, message)
		}
	}

	return &MessagesResponse{
		Messages:      messages,
		NextPageToken: res.NextPageToken,
	}, nil
}

func (client *GmailClient) GetUserMessagesIDs(nextPageToken string) (*gmail.ListMessagesResponse, error) {

	req := client.Users.Messages.List("me").MaxResults(500)
	if nextPageToken != "" {
		req.PageToken(nextPageToken)
	}

	resp, err := req.Do()
	if err != nil {
		return nil, err
	}

	return resp, nil
}

func (client *GmailClient) GetMessageDirect(msgID string) (*gmail.Message, error) {
	return client.GetMessageDirectForUser("me", msgID)
}

// GetMessageDirectForUser fetches a full message and inlines attachment bytes (same as manual upload).
// userID is "me" for personal OAuth, or the mailbox email for workspace delegation.
func (client *GmailClient) GetMessageDirectForUser(userID, msgID string) (*gmail.Message, error) {
	if strings.TrimSpace(msgID) == "" {
		return nil, fmt.Errorf("message ID cannot be empty")
	}
	if strings.TrimSpace(userID) == "" {
		userID = "me"
	}

	msg, err := client.Users.Messages.Get(userID, msgID).Format("full").Do()
	if err != nil {
		return nil, err
	}

	if msg.Payload != nil {
		if err := client.updateAttachment(userID, msgID, msg.Payload); err != nil {
			return nil, err
		}
	}

	return msg, nil
}

func (client *GmailClient) updateAttachment(userID, msgID string, part *gmail.MessagePart) error {
	if part == nil {
		return nil
	}

	if part.Body != nil && part.Body.AttachmentId != "" {
		p, err := client.getAttachmentWithRetry(userID, msgID, part.Body.AttachmentId)
		if err != nil {
			return err
		}

		p.Data = strings.ReplaceAll(p.Data, "_", "/")
		p.Data = strings.ReplaceAll(p.Data, "-", "+")

		part.Body = p
	}

	for _, p := range part.Parts {
		err := client.updateAttachment(userID, msgID, p)
		if err != nil {
			return err
		}
	}

	return nil
}

func (client *GmailClient) GetMessage(msgID string) (*GmailMessage, error) {

	msg, err := client.GetMessageDirect(msgID)
	if err != nil {
		return nil, err
	}

	gmailMsg := &GmailMessage{
		ID:   msg.Id,
		Date: msg.InternalDate,
	}

	if msg.Payload != nil {
		client.processHeaders(msg.Payload.Headers, gmailMsg)

		if len(msg.Payload.Parts) > 0 {
			client.processMessageParts(msg.Payload.Parts, gmailMsg)
		}
	}

	return gmailMsg, nil
}

func (client *GmailClient) processHeaders(headers []*gmail.MessagePartHeader, gmailMsg *GmailMessage) {
	for _, header := range headers {
		if header == nil {
			continue
		}

		switch header.Name {
		case "To":
			if res, ok := utils.GetStringBetween(header.Value, "\u003c", "\u003e"); ok {
				gmailMsg.To = res
			}
		case "From":
			if res, ok := utils.GetStringBetween(header.Value, "\u003c", "\u003e"); ok {
				gmailMsg.From = res
			}
		case "Subject":
			gmailMsg.Subject = header.Value
		}
	}
}

func (client *GmailClient) processMessageParts(parts []*gmail.MessagePart, gmailMsg *GmailMessage) {
	for _, part := range parts {
		if part == nil {
			continue
		}

		switch part.MimeType {
		case "text/plain", "text/html":
			client.processTextPart(part, gmailMsg)
		case "multipart/alternative":
			client.processMessageParts(part.Parts, gmailMsg)
		case "multipart/mixed":
			client.processMultipartMixed(part.Parts, gmailMsg)
		}

		// Process attachments
		if part.Filename != "" && part.Body != nil {
			client.processAttachment(part, gmailMsg)
		}
	}
}

func (client *GmailClient) processTextPart(part *gmail.MessagePart, gmailMsg *GmailMessage) {
	if part.Body == nil || part.Body.Data == "" {
		return
	}

	// Only process if we haven't found a body yet or this is plain text (preferred)
	if gmailMsg.Body == "" || part.MimeType == "text/plain" {
		data, err := base64.StdEncoding.DecodeString(part.Body.Data)
		if err != nil {
			// If decoding fails, use raw data
			gmailMsg.Body = part.Body.Data
		} else {
			gmailMsg.Body = string(data)
		}
	}
}

func (client *GmailClient) processMultipartMixed(parts []*gmail.MessagePart, gmailMsg *GmailMessage) {
	for _, subpart := range parts {
		if subpart == nil {
			continue
		}

		if subpart.MimeType == "multipart/alternative" {
			client.processMessageParts(subpart.Parts, gmailMsg)
		} else {
			client.processMessageParts([]*gmail.MessagePart{subpart}, gmailMsg)
		}
	}
}

func (client *GmailClient) processAttachment(part *gmail.MessagePart, gmailMsg *GmailMessage) {
	data, err := base64.StdEncoding.DecodeString(part.Body.Data)
	if err != nil {
		slog.Warn("Unable to decode attachment data: ", "error", err)
		return
	}

	gmailMsg.Attachments = append(gmailMsg.Attachments, &Attachment{
		FileName: part.Filename,
		Data:     data,
	})
}

func (client *GmailClient) GetThread(threadID string) (*gmail.Thread, error) {

	thread, err := client.Users.Threads.Get("me", threadID).Format("full").Do()
	if err != nil {
		return nil, err
	}

	return thread, nil
}

func (client *GmailClient) GetAttachment(userID, msgID, attachmentID string) (*gmail.MessagePartBody, error) {
	if strings.TrimSpace(userID) == "" {
		userID = "me"
	}
	msg, err := client.Users.Messages.Attachments.Get(userID, msgID, attachmentID).Do()
	if err != nil {
		return nil, err
	}

	return msg, nil
}

func (client *GmailClient) getAttachmentWithRetry(userID, msgID, attachmentID string) (*gmail.MessagePartBody, error) {
	var lastErr error
	backoff := 200 * time.Millisecond
	for attempt := 0; attempt < 4; attempt++ {
		body, err := client.GetAttachment(userID, msgID, attachmentID)
		if err == nil {
			return body, nil
		}
		lastErr = err
		if !gmailGetRetryable(err) {
			return nil, err
		}
		time.Sleep(backoff)
		if backoff < 4*time.Second {
			backoff *= 2
		}
	}
	return nil, lastErr
}

// buildGmailQuery constructs a Gmail search query string from filter parameters
func (filter *GmailFilter) buildGmailQuery() string {
	var queryParts []string

	// If a raw query is provided, use it directly
	if filter.Query != "" {
		return filter.Query
	}

	// Build query from individual filter parameters
	if filter.From != "" {
		queryParts = append(queryParts, fmt.Sprintf("from:%s", filter.From))
	}

	if filter.To != "" {
		queryParts = append(queryParts, fmt.Sprintf("to:%s", filter.To))
	}

	if filter.Subject != "" {
		queryParts = append(queryParts, fmt.Sprintf("subject:%s", filter.Subject))
	}

	if filter.HasAttachment {
		queryParts = append(queryParts, "has:attachment")
	}

	if filter.After != "" {
		queryParts = append(queryParts, fmt.Sprintf("after:%s", filter.After))
	}

	if filter.Before != "" {
		queryParts = append(queryParts, fmt.Sprintf("before:%s", filter.Before))
	}

	if filter.NewerThan != "" {
		queryParts = append(queryParts, fmt.Sprintf("newer_than:%s", filter.NewerThan))
	}

	if filter.OlderThan != "" {
		queryParts = append(queryParts, fmt.Sprintf("older_than:%s", filter.OlderThan))
	}

	// Join all query parts with spaces
	return strings.Join(queryParts, " ")
}

// GetUserMessages lists and fetches messages for the given userID ("me" for own mailbox, or email for domain-wide delegation).
// Used for both personal and corporate Gmail backup.
func (client *GmailClient) GetUserMessagesWithUserID(userID, nextPageToken, label string, num int64, filter *GmailFilter) (*MessagesResponse, error) {
	if userID == "" {
		userID = "me"
	}
	req := client.Users.Messages.List(userID).MaxResults(num)
	// All-mail backup: include Spam + Trash (API default excludes them).
	req.IncludeSpamTrash(true)
	if nextPageToken != "" {
		req.PageToken(nextPageToken)
	}
	if label != "" {
		req.LabelIds(label)
	}
	if filter != nil {
		if query := filter.buildGmailQuery(); query != "" {
			req.Q(query)
		}
	}

	res, err := req.Do()
	if err != nil {
		return nil, err
	}

	messages := make([]*gmail.Message, 0, len(res.Messages))
	var failedIDs []string
	for _, msg := range res.Messages {
		if msg == nil || strings.TrimSpace(msg.Id) == "" {
			continue
		}
		message, getErr := client.getMessageWithRetry(userID, msg.Id)
		if getErr != nil {
			if gmailMessageGone(getErr) {
				// Deleted / permanently unavailable — do not block the page.
				continue
			}
			failedIDs = append(failedIDs, msg.Id)
			continue
		}
		messages = append(messages, message)
	}

	// Retry only transient failures (usually quota mid-page) without re-fetching successes.
	pageBackoff := 500 * time.Millisecond
	for round := 0; round < 3 && len(failedIDs) > 0; round++ {
		time.Sleep(pageBackoff)
		if pageBackoff < 8*time.Second {
			pageBackoff *= 2
		}
		stillFailed := make([]string, 0, len(failedIDs))
		for _, id := range failedIDs {
			message, getErr := client.getMessageWithRetry(userID, id)
			if getErr != nil {
				if gmailMessageGone(getErr) {
					continue
				}
				stillFailed = append(stillFailed, id)
				continue
			}
			messages = append(messages, message)
		}
		failedIDs = stillFailed
	}

	// Never advance past a page with missing bodies — those IDs would be skipped forever.
	if len(failedIDs) > 0 {
		return nil, fmt.Errorf("gmail messages.get failed for %d/%d ids on page (e.g. %s): refetch page before continuing",
			len(failedIDs), len(res.Messages), failedIDs[0])
	}

	return &MessagesResponse{
		Messages:      messages,
		NextPageToken: res.NextPageToken,
	}, nil
}

// getMessageWithRetry fetches one full message (with attachment bytes inlined); retries transient 429/5xx.
func (client *GmailClient) getMessageWithRetry(userID, msgID string) (*gmail.Message, error) {
	var lastErr error
	backoff := 200 * time.Millisecond
	for attempt := 0; attempt < 4; attempt++ {
		message, err := client.GetMessageDirectForUser(userID, msgID)
		if err == nil {
			return message, nil
		}
		lastErr = err
		if !gmailGetRetryable(err) {
			return nil, err
		}
		time.Sleep(backoff)
		if backoff < 4*time.Second {
			backoff *= 2
		}
	}
	return nil, lastErr
}

func gmailGetRetryable(err error) bool {
	if err == nil {
		return false
	}
	if e, ok := err.(*googleapi.Error); ok && e != nil {
		if e.Code == 429 || e.Code >= 500 {
			return true
		}
		if e.Code == 403 {
			msg := strings.ToLower(e.Message)
			return strings.Contains(msg, "ratelimit") || strings.Contains(msg, "rate limit") || strings.Contains(msg, "quota")
		}
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "429") ||
		strings.Contains(msg, "ratelimitexceeded") ||
		strings.Contains(msg, "rate limit") ||
		strings.Contains(msg, "quota")
}

// gmailMessageGone is true when the message can never be fetched (deleted, etc.).
func gmailMessageGone(err error) bool {
	if err == nil {
		return false
	}
	if e, ok := err.(*googleapi.Error); ok && e != nil {
		return e.Code == 404 || e.Code == 410
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, " 404 ") || strings.Contains(msg, "notfound") ||
		strings.Contains(msg, "\"code\":404") || strings.Contains(msg, " 410 ")
}

// GetUserMessagesControlled is a convenience wrapper for the current user ("me"). Preserved for backward compatibility.
func (client *GmailClient) GetUserMessagesControlled(nextPageToken, label string, num int64, filter *GmailFilter) (*MessagesResponse, error) {
	return client.GetUserMessagesWithUserID("me", nextPageToken, label, num, filter)
}

// GetUserMessageCount returns the total number of messages in the mailbox for userID (email or "me").
// Requires domain-wide delegation when userID is another user's email.
func (client *GmailClient) GetUserMessageCount(userID string) (int64, error) {
	profile, err := client.Users.GetProfile(userID).Do()
	if err != nil {
		return 0, err
	}
	return profile.MessagesTotal, nil
}

func (client *GmailClient) GetUserMessagesUsingWorkers(nextPageToken string, workerCount int) (*MessagesResponse, error) {

	// Fetch list of message IDs
	req := client.Users.Messages.List("me").MaxResults(500)
	if nextPageToken != "" {
		req.PageToken(nextPageToken)
	}

	res, err := req.Do()
	if err != nil {
		return nil, err
	}

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		messages []*gmail.Message
		idCh     = make(chan string, len(res.Messages))
		msgCh    = make(chan *gmail.Message, len(res.Messages))
	)

	// Start worker Goroutines
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for msgID := range idCh {
				if message, err := client.Users.Messages.Get("me", msgID).Do(); err == nil {
					msgCh <- message
				}
			}
		}()
	}

	// Send message IDs to workers
	go func() {
		for _, msg := range res.Messages {
			idCh <- msg.Id
		}
		close(idCh)
	}()

	// Close msgCh when all workers are done
	go func() {
		wg.Wait()
		close(msgCh)
	}()

	// Collect messages
	for message := range msgCh {
		mu.Lock()
		messages = append(messages, message)
		mu.Unlock()
	}

	return &MessagesResponse{
		Messages:      messages,
		NextPageToken: res.NextPageToken,
	}, nil
}

func createRawMessage(gmailMsg *gmail.Message) (string, error) {
	if gmailMsg == nil || gmailMsg.Payload == nil {
		return "", fmt.Errorf("message payload is nil")
	}
	normalizeRFC822RootHeaders(gmailMsg.Payload)

	var rawMessage string
	if err := createMessagePart(&rawMessage, gmailMsg.Payload); err != nil {
		return "", err
	}

	raw := base64.URLEncoding.EncodeToString([]byte(rawMessage))
	return raw, nil
}

// normalizeRFC822RootHeaders keeps a single From header first. YouTube-style
// Subject values with embedded CR/LF can otherwise end the header block early.
func normalizeRFC822RootHeaders(part *gmail.MessagePart) {
	if part == nil {
		return
	}
	var from *gmail.MessagePartHeader
	out := make([]*gmail.MessagePartHeader, 0, len(part.Headers)+1)
	for _, h := range part.Headers {
		if h == nil {
			continue
		}
		value := sanitizeRFC822HeaderValue(h.Value)
		if strings.EqualFold(strings.TrimSpace(h.Name), "From") {
			if from == nil && value != "" {
				from = &gmail.MessagePartHeader{Name: "From", Value: value}
			}
			continue
		}
		out = append(out, &gmail.MessagePartHeader{Name: h.Name, Value: value})
	}
	if from == nil {
		from = &gmail.MessagePartHeader{Name: "From", Value: "restored-message@localhost"}
	}
	part.Headers = append([]*gmail.MessagePartHeader{from}, out...)
}

func sanitizeRFC822HeaderValue(v string) string {
	v = strings.ReplaceAll(v, "\r", " ")
	v = strings.ReplaceAll(v, "\n", " ")
	return strings.TrimSpace(strings.Join(strings.Fields(v), " "))
}

func decodeGmailBodyData(data string) ([]byte, error) {
	data = strings.TrimSpace(data)
	if data == "" {
		return nil, nil
	}
	// Backup may store URL-safe or standard base64 (attachment path rewrites -/_ to +/).
	for _, dec := range []func(string) ([]byte, error){
		base64.URLEncoding.DecodeString,
		base64.RawURLEncoding.DecodeString,
		base64.StdEncoding.DecodeString,
		base64.RawStdEncoding.DecodeString,
	} {
		if b, err := dec(data); err == nil {
			return b, nil
		}
	}
	return base64.URLEncoding.DecodeString(data)
}

func createMessagePart(rawMessage *string, part *gmail.MessagePart) error {
	var boundary, contentTransferEncoding string

	for _, header := range part.Headers {
		if header == nil {
			continue
		}
		value := sanitizeRFC822HeaderValue(header.Value)
		*rawMessage += fmt.Sprintf("%s: %s\r\n", header.Name, value)

		switch header.Name {
		case "Content-Type":
			if strings.Contains(value, "boundary=") {
				boundary = "--" + strings.Trim(strings.TrimSpace(strings.Split(value, "boundary=")[1]), "\"")
			}
		case "Content-Transfer-Encoding":
			contentTransferEncoding = value
		}
	}

	*rawMessage += "\r\n"

	if part.Body != nil && part.Body.Data != "" {
		data, err := decodeGmailBodyData(part.Body.Data)
		if err != nil {
			return fmt.Errorf("decode message body: %w", err)
		}

		switch strings.ToLower(strings.TrimSpace(contentTransferEncoding)) {
		case "base64":
			*rawMessage += base64.StdEncoding.EncodeToString(data)
		case "quoted-printable":
			var buf bytes.Buffer
			writer := quotedprintable.NewWriter(&buf)
			if _, err := writer.Write(data); err != nil {
				return err
			}
			if err := writer.Close(); err != nil {
				return err
			}
			*rawMessage += buf.String()
		default:
			*rawMessage += string(data)
		}
	}

	*rawMessage += "\r\n"

	for _, subpart := range part.Parts {
		*rawMessage += boundary + "\r\n"
		if err := createMessagePart(rawMessage, subpart); err != nil {
			return err
		}
	}

	if boundary != "" {
		*rawMessage += boundary + "--\r\n"
	}

	return nil
}
