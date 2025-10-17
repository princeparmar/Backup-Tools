package google

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"mime/quotedprintable"
	"strings"
	"sync"

	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/middleware"
	"github.com/StorX2-0/Backup-Tools/pkg/utils"

	"github.com/labstack/echo/v4"
	"google.golang.org/api/gmail/v1"
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

	if strings.TrimSpace(msgID) == "" {
		return nil, fmt.Errorf("message ID cannot be empty")
	}

	msg, err := client.Users.Messages.Get("me", msgID).Format("full").Do()
	if err != nil {
		return nil, err
	}

	if msg.Payload != nil {
		if err := client.updateAttachment(msgID, msg.Payload); err != nil {
			return nil, err
		}
	}

	return msg, nil
}

func (client *GmailClient) updateAttachment(msgID string, part *gmail.MessagePart) error {
	if part == nil {
		return nil
	}

	if part.Body != nil && part.Body.AttachmentId != "" {
		p, err := client.GetAttachment(msgID, part.Body.AttachmentId)
		if err != nil {
			return err
		}

		p.Data = strings.ReplaceAll(p.Data, "_", "/")
		p.Data = strings.ReplaceAll(p.Data, "-", "+")

		part.Body = p
	}

	for _, p := range part.Parts {
		err := client.updateAttachment(msgID, p)
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

func (client *GmailClient) GetAttachment(msgID, attachmentID string) (*gmail.MessagePartBody, error) {

	msg, err := client.Users.Messages.Attachments.Get("me", msgID, attachmentID).Do()
	if err != nil {
		return nil, err
	}

	return msg, nil
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

func (client *GmailClient) GetUserMessagesControlled(nextPageToken, label string, num int64, filter *GmailFilter) (*MessagesResponse, error) {

	req := client.Users.Messages.List("me").MaxResults(num)
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
	var rawMessage string

	err := createMessagePart(&rawMessage, gmailMsg.Payload)
	if err != nil {
		return "", err
	}

	// Base64 encode the entire message

	raw := base64.URLEncoding.EncodeToString([]byte(rawMessage))
	return raw, nil
}

func createMessagePart(rawMessage *string, part *gmail.MessagePart) error {
	var boundary, contentTransferEncoding string

	for _, header := range part.Headers {
		*rawMessage += fmt.Sprintf("%s: %s\n", header.Name, header.Value)

		switch header.Name {
		case "Content-Type":
			if strings.Contains(header.Value, "boundary=") {
				boundary = "--" + strings.Trim(strings.TrimSpace(strings.Split(header.Value, "boundary=")[1]), "\"")
			}
		case "Content-Transfer-Encoding":
			contentTransferEncoding = header.Value
		}
	}

	*rawMessage += "\n"

	if part.Body != nil && part.Body.Data != "" {
		data, err := base64.URLEncoding.DecodeString(part.Body.Data)
		if err != nil {
			return err
		}

		switch contentTransferEncoding {
		case "base64":
			*rawMessage += part.Body.Data
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

	*rawMessage += "\n"

	for _, subpart := range part.Parts {
		*rawMessage += boundary + "\n"
		if err := createMessagePart(rawMessage, subpart); err != nil {
			return err
		}
	}

	if boundary != "" {
		*rawMessage += boundary + "--\n"
	}

	return nil
}
