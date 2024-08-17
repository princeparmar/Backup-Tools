package google

import (
	"context"
	"encoding/base64"
	"log"
	"log/slog"
	"storj-integrations/utils"
	"strings"
	"sync"

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
	client, err := client(c)
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
	var threads *gmail.ListThreadsResponse
	var err error
	var ts []*gmail.Thread
	// Checks is there is page token passed to func.
	if nextPageToken == "" {
		threads, err = client.Users.Threads.List("me").MaxResults(500).Do()
		if err != nil {
			return nil, err
		}
	} else {
		threads, err = client.Users.Threads.List("me").MaxResults(500).PageToken(nextPageToken).Do()
		if err != nil {
			return nil, err
		}
	}

	for _, t := range threads.Threads {
		t, _ := client.Users.Threads.Get("me", t.Id).Do()
		ts = append(ts, t)
	}
	return &ThreadsResponse{threads.NextPageToken, int(threads.ResultSizeEstimate), ts}, nil
}

func (client *GmailClient) GetUserThreadsIDs(nextPageToken string) (*gmail.ListThreadsResponse, error) {
	var threads *gmail.ListThreadsResponse
	var err error
	// Checks is there is page token passed to func.
	if nextPageToken == "" {
		threads, err = client.Users.Threads.List("me").MaxResults(500).Do()
		if err != nil {
			return nil, err
		}
	} else {
		threads, err = client.Users.Threads.List("me").MaxResults(500).PageToken(nextPageToken).Do()
		if err != nil {
			return nil, err
		}
	}

	return threads, nil
}

// Function takes nextPageToken and returns 100 results of User's messages.
// (Pass `""` if you don't want to specify nextPageToken and get latest messages).
func (client *GmailClient) GetUserMessages(nextPageToken string) (*MessagesResponse, error) {
	var msgs MessagesResponse
	var err error
	var messages []*gmail.Message
	var res *gmail.ListMessagesResponse
	// Checks is there is page token passed to func.
	if nextPageToken == "" {
		res, err = client.Users.Messages.List("me").MaxResults(500).Do()
		if err != nil {
			return nil, err
		}
	} else {
		res, err = client.Users.Messages.List("me").MaxResults(500).PageToken(nextPageToken).Do()
		if err != nil {
			return nil, err
		}
	}
	for _, msg := range res.Messages {
		message, err := client.Users.Messages.Get("me", msg.Id).Do()
		if err != nil {
			//log.Printf("Failed to retrieve message with ID %s: %v", msg.Id, err)
			continue
		}
		messages = append(messages, message)
	}
	msgs.Messages = messages
	msgs.NextPageToken = res.NextPageToken
	return &msgs, nil
}

func (client *GmailClient) GetUserMessagesIDs(nextPageToken string) (*gmail.ListMessagesResponse, error) {
	var err error
	var res *gmail.ListMessagesResponse
	// Checks is there is page token passed to func.
	if nextPageToken == "" {
		res, err = client.Users.Messages.List("me").MaxResults(500).Do()
		if err != nil {
			return nil, err
		}
	} else {
		res, err = client.Users.Messages.List("me").MaxResults(500).PageToken(nextPageToken).Do()
		if err != nil {
			return nil, err
		}
	}

	return res, nil
}
func (client *GmailClient) GetMessage(msgID string) (*GmailMessage, error) {
	msg, err := client.Users.Messages.Get("me", msgID).Format("full").Do()
	if err != nil {
		return nil, err
	}

	var GmailMSG GmailMessage

	GmailMSG.ID = msg.Id
	GmailMSG.Date = msg.InternalDate

	for _, v := range msg.Payload.Headers {
		switch v.Name {
		case "To":
			res, ok := utils.GetStringInBetweenTwoString(v.Value, "\u003c", "\u003e")
			if ok {
				GmailMSG.To = res
			}
		case "From":
			res, ok := utils.GetStringInBetweenTwoString(v.Value, "\u003c", "\u003e")
			if ok {
				GmailMSG.From = res
			}
		case "Subject":
			GmailMSG.Subject = v.Value
		}
	}

	for _, part := range msg.Payload.Parts {
		// If there is text in first layer payload.
		if part.MimeType == "text/plain" {
			// Body data is in Base64 format.
			data, err := base64.URLEncoding.DecodeString(part.Body.Data)
			if err != nil {
				GmailMSG.Body = part.Body.Data
			} else {
				GmailMSG.Body = string(data)
			}

			// If there is text in second layer payload.
		} else if part.MimeType == "text/html" {
			GmailMSG.Body = string(part.Body.Data)

		} else if part.MimeType == "multipart/alternative" {
			// Body data is split across multiple parts.
			for _, subpart := range part.Parts {
				if subpart.MimeType == "text/plain" {
					// Body data is in Base64 format.
					data, err := base64.StdEncoding.DecodeString(subpart.Body.Data)
					if err != nil {
						GmailMSG.Body = subpart.Body.Data
					} else {
						GmailMSG.Body = string(data)
					}
				}
			}

			// If there is text in third layer payload.
		} else if part.MimeType == "multipart/mixed" {
			for _, subpart := range part.Parts {
				if subpart.MimeType == "multipart/alternative" {
					for _, subsubpart := range part.Parts {
						if subsubpart.MimeType == "text/plain" {
							if strings.HasPrefix(subpart.Body.Data, "Content-Transfer-Encoding: base64") {
								// Body data is in Base64 format.
								data, err := base64.StdEncoding.DecodeString(subpart.Body.Data[28:])
								if err != nil {
									if strings.Contains(err.Error(), "illegal base64 data at input byte 383") {
										slog.Warn("Unable to decode message body: ", "error", err, "WARNING", "using the raw body")
										GmailMSG.Body = subsubpart.Body.Data
									}
								} else {
									GmailMSG.Body = string(data)
								}

							}
						}
					}
				}
			}
		}
	}

	for _, part := range msg.Payload.Parts {
		if part.Filename != "" {
			data, err := base64.URLEncoding.DecodeString(part.Body.Data)
			if err != nil {
				slog.Warn("Unable to decode attachment data: ", "error", err)
			} else {
				GmailMSG.Attachments = append(GmailMSG.Attachments, &Attachment{
					FileName: part.Filename,
					Data:     data,
				})
			}

		}
	}

	return &GmailMSG, nil
}

func (client *GmailClient) GetThread(threadID string) (*gmail.Thread, error) {
	return client.Users.Threads.Get("me", threadID).Format("full").Do()
}

func (client *GmailClient) GetUserMessagesControlled(nextPageToken string, num int64) (*MessagesResponse, error) {
	var msgs MessagesResponse
	var err error
	var messages []*gmail.Message
	var res *gmail.ListMessagesResponse
	// Checks is there is page token passed to func.
	if nextPageToken == "" {
		res, err = client.Users.Messages.List("me").MaxResults(num).Do()
		if err != nil {
			return nil, err
		}
	} else {
		res, err = client.Users.Messages.List("me").MaxResults(num).PageToken(nextPageToken).Do()
		if err != nil {
			return nil, err
		}
	}
	for _, msg := range res.Messages {
		message, err := client.Users.Messages.Get("me", msg.Id).Do()
		if err != nil {
			//log.Printf("Failed to retrieve message with ID %s: %v", msg.Id, err)
			continue
		}
		messages = append(messages, message)
	}
	msgs.Messages = messages
	msgs.NextPageToken = res.NextPageToken
	return &msgs, nil
}

func (client *GmailClient) GetUserMessagesUsingWorkers(nextPageToken string, workerCount int) (*MessagesResponse, error) {
	var msgs MessagesResponse
	var wg sync.WaitGroup
	var mu sync.Mutex
	var messages []*gmail.Message
	errCh := make(chan error, 1)
	msgCh := make(chan *gmail.Message, 500) // Buffer size can be adjusted based on expected number of messages
	idCh := make(chan string, 500)          // Channel to send message IDs to workers

	// Fetch list of message IDs
	var res *gmail.ListMessagesResponse
	var err error

	if nextPageToken == "" {
		res, err = client.Users.Messages.List("me").MaxResults(500).Do()
	} else {
		res, err = client.Users.Messages.List("me").MaxResults(500).PageToken(nextPageToken).Do()
	}
	if err != nil {
		return nil, err
	}

	// Start worker Goroutines
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for msgID := range idCh {
				message, err := client.Users.Messages.Get("me", msgID).Do()
				if err != nil {
					log.Printf("Failed to retrieve message with ID %s: %v", msgID, err)
					continue
				}
				msgCh <- message
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
		close(errCh)
	}()

	// Collect messages
	for message := range msgCh {
		mu.Lock()
		messages = append(messages, message)
		mu.Unlock()
	}

	msgs.Messages = messages
	msgs.NextPageToken = res.NextPageToken
	return &msgs, nil
}
