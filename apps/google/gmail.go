package google

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"storj-integrations/utils"
	"strings"

	"github.com/labstack/echo/v4"
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
)

type GmailClient struct {
	*gmail.Service
}

type ThreadsResponce struct {
	NextPageToken      string `json:"nextPageToken"`
	ResultSizeEstimate int    `json:"resultSizeEstimate"`
	Threads            []struct {
		HistoryID string `json:"historyId"`
		ID        string `json:"id"`
		Snippet   string `json:"snippet,omitempty"`
	} `json:"threads"`
}

type MessagesResponse struct {
	/*Messages []struct {
		ID       string `json:"id"`
		ThreadID string `json:"threadId"`
	} `json:"messages"`*/
	Messages           []gmail.Message `json:"messages"`
	NextPageToken      string          `json:"nextPageToken"`
	ResultSizeEstimate int             `json:"resultSizeEstimate"`
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
func (client *GmailClient) GetUserThreads(nextPageToken string) (*ThreadsResponce, error) {
	var threads *gmail.ListThreadsResponse
	var err error

	// Checks is there is page token passed to func.
	if nextPageToken == "" {
		threads, err = client.Users.Threads.List("me").Do()
		if err != nil {
			return nil, err
		}
	} else {
		threads, err = client.Users.Threads.List("me").PageToken(nextPageToken).Do()
		if err != nil {
			return nil, err
		}
	}

	jsonThreads, err := threads.MarshalJSON()
	if err != nil {
		return nil, err
	}
	var res ThreadsResponce
	err = json.Unmarshal(jsonThreads, &res)
	if err != nil {
		return nil, err
	}
	return &res, nil
}

// Function takes nextPageToken and returns 100 results of User's messages.
// (Pass `""` if you don't want to specify nextPageToken and get latest messages).
func (client *GmailClient) GetUserMessages(nextPageToken string) (*gmail.ListMessagesResponse, error) {
	var msgs *gmail.ListMessagesResponse
	var err error

	// Checks is there is page token passed to func.
	if nextPageToken == "" {
		msgs, err = client.Users.Messages.List("me").MaxResults(500).Do()
		if err != nil {
			return nil, err
		}
	} else {
		msgs, err = client.Users.Messages.List("me").MaxResults(500).PageToken(nextPageToken).Do()
		if err != nil {
			return nil, err
		}
	}
	return msgs, nil
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
