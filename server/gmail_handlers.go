package server

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"slices"
	google "storj-integrations/apps/google"
	"storj-integrations/storage"
	"storj-integrations/storj"
	"storj-integrations/utils"
	"strings"

	"github.com/labstack/echo/v4"
	realgmail "google.golang.org/api/gmail/v1"
)

type ThreadJSON struct {
	ID      string `json:"thread_id"`
	Snippet string `json:"snippet"`
}

// Fetches user threads, returns their IDs and snippets.
func handleGmailGetThreads(c echo.Context) error {
	GmailClient, err := google.NewGmailClient(c)
	if err != nil {
		if err.Error() == "token error" {
			return c.JSON(http.StatusUnauthorized, map[string]interface{}{
				"error": "token expired",
			})
		} else {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}

	threads, err := GmailClient.GetUserThreads("")
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// TODO: implement next page token (now only first page is avialable)

	var jsonResp []*ThreadJSON
	for _, v := range threads.Threads {
		jsonResp = append(jsonResp, &ThreadJSON{
			ID:      v.ID,
			Snippet: v.Snippet,
		})
	}
	return c.JSON(http.StatusOK, jsonResp)
}

type MessageListJSON struct {
	ID       string `json:"message_id"`
	ThreadID string `json:"thread_id"`
	Synced   bool   `json:"synced"`
}

// Fetches user messages, returns their ID's and threat's IDs.
func handleGmailGetMessages(c echo.Context) error {

	accesGrant := c.Request().Header.Get("STORJ_ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "storj access token is missing",
		})
	}

	// CHECK IF EMAIL DATABASE ALREADY EXISTS AND DOWNLOAD IT, IF NOT - CREATE NEW ONE
	userCacheDBPath := "./cache/" + utils.CreateUserTempCacheFolder() + "/gmails.db"
	defer os.Remove(userCacheDBPath)
	byteDB, err := storj.DownloadObject(context.Background(), accesGrant, "gmail", "gmails.db")
	// Copy file from storj to local cache if everything's fine.
	// Skip error check, if there's error - we will check that and create new file
	if err == nil {
		fmt.Println(userCacheDBPath)
		dbFile, err := utils.CreateFile(userCacheDBPath)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
		_, err = dbFile.Write(byteDB)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	} else {
		if strings.Contains(err.Error(), "object not found") {
			slog.Warn("gmail db not found")
			fmt.Println(userCacheDBPath)
			dbFile, err := utils.CreateFile(userCacheDBPath)
			if err != nil {
				return c.JSON(http.StatusForbidden, map[string]interface{}{
					"error": err.Error(),
				})
			}
			_, err = dbFile.Write(byteDB)
			if err != nil {
				return c.JSON(http.StatusForbidden, map[string]interface{}{
					"error": err.Error(),
				})
			}
		} else {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}

	db, err := storage.ConnectToEmailDB(userCacheDBPath)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	messages, err := db.GetAllEmailsFromDB()
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	GmailClient, err := google.NewGmailClient(c)
	if err != nil {
		if err.Error() == "token error" {
			return c.JSON(http.StatusUnauthorized, map[string]interface{}{
				"error": "token expired",
			})
		} else {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}

	var allMessages []MessageListJSON
	var nextPageToken string
	for {
		msgs, err := GmailClient.GetUserMessages(nextPageToken)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
		for _, message := range msgs.Messages {
			_, synced := slices.BinarySearchFunc(messages, message.Id, func(a *storage.GmailMessageSQL, b string) int {
				return cmp.Compare(a.ID, b)
			})
			allMessages = append(allMessages, MessageListJSON{ID: message.Id, ThreadID: message.ThreadId, Synced: synced})
		}
		//allMessages = append(allMessages, msgs.Messages...)
		nextPageToken = msgs.NextPageToken

		if nextPageToken == "" {
			break
		}
	}
	return c.JSON(http.StatusOK, allMessages)
}

// Returns Gmail message in JSON format.
func handleGmailGetMessage(c echo.Context) error {
	id := c.Param("ID")

	GmailClient, err := google.NewGmailClient(c)
	if err != nil {
		if err.Error() == "token error" {
			return c.JSON(http.StatusUnauthorized, map[string]interface{}{
				"error": "token expired",
			})
		} else {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}
	msg, err := GmailClient.GetMessage(id)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	return c.JSON(http.StatusOK, msg)
}

// Fetches message from Gmail by given ID as a parameter and writes it into SQLite Database in Storj.
// If there's no database yet - creates one.
func handleGmailMessageToStorj(c echo.Context) error {
	id := c.Param("ID")
	accesGrant := c.Request().Header.Get("STORJ_ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "storj access token is missing",
		})
	}

	// FETCH THE EMAIL TO GOLANG STRUCT

	GmailClient, err := google.NewGmailClient(c)
	if err != nil {
		if err.Error() == "token error" {
			return c.JSON(http.StatusUnauthorized, map[string]interface{}{
				"error": "token expired",
			})
		} else {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}
	msg, err := GmailClient.GetMessage(id)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	msgToSave := storage.GmailMessageSQL{
		ID:      msg.ID,
		Date:    msg.Date,
		From:    msg.From,
		To:      msg.To,
		Subject: msg.Subject,
		Body:    msg.Body,
	}

	// SAVE ATTACHMENTS TO THE STORJ BUCKET AND WRITE THEIR NAMES TO STRUCT

	if len(msg.Attachments) > 0 {
		for _, att := range msg.Attachments {
			err = storj.UploadObject(context.Background(), accesGrant, "gmail", att.FileName, att.Data)
			if err != nil {
				return c.JSON(http.StatusForbidden, map[string]interface{}{
					"error": err.Error(),
				})
			}
			msgToSave.Attachments = msgToSave.Attachments + "|" + att.FileName
		}
	}

	// CHECK IF EMAIL DATABASE ALREADY EXISTS AND DOWNLOAD IT, IF NOT - CREATE NEW ONE

	userCacheDBPath := "./cache/" + utils.CreateUserTempCacheFolder() + "/gmails.db"
	defer os.Remove(userCacheDBPath)
	byteDB, err := storj.DownloadObject(context.Background(), accesGrant, "gmail", "gmails.db")
	// Copy file from storj to local cache if everything's fine.
	// Skip error check, if there's error - we will check that and create new file
	if err == nil {
		dbFile, err := utils.CreateFile(userCacheDBPath)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
		_, err = dbFile.Write(byteDB)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	} else {
		if strings.Contains(err.Error(), "object not found") {
			slog.Warn("gmail db not found")
			fmt.Println(userCacheDBPath)
			dbFile, err := utils.CreateFile(userCacheDBPath)
			if err != nil {
				return c.JSON(http.StatusForbidden, map[string]interface{}{
					"error": err.Error(),
				})
			}
			_, err = dbFile.Write(byteDB)
			if err != nil {
				return c.JSON(http.StatusForbidden, map[string]interface{}{
					"error": err.Error(),
				})
			}
		} else {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}

	db, err := storage.ConnectToEmailDB(userCacheDBPath)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// WRITE ALL EMAILS TO THE DATABASE LOCALLY

	err = db.WriteEmailToDB(&msgToSave)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// DELETE OLD DB COPY FROM STORJ UPLOAD UP TO DATE DB FILE BACK TO STORJ AND DELETE IT FROM LOCAL CACHE

	// get db file data
	dbByte, err := os.ReadFile(userCacheDBPath)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// delete old db copy from storj
	err = storj.DeleteObject(context.Background(), accesGrant, "gmail", "gmails.db")
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// upload file to storj
	err = storj.UploadObject(context.Background(), accesGrant, "gmail", "gmails.db", dbByte)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{"message": "Email was successfully uploaded"})
}

func handleAllGmailMessagesToStorj(c echo.Context) error {

	accesGrant := c.Request().Header.Get("STORJ_ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "storj access token is missing",
		})
	}

	// CHECK IF EMAIL DATABASE ALREADY EXISTS AND DOWNLOAD IT, IF NOT - CREATE NEW ONE
	userCacheDBPath := "./cache/" + utils.CreateUserTempCacheFolder() + "/gmails.db"
	defer os.Remove(userCacheDBPath)
	byteDB, err := storj.DownloadObject(context.Background(), accesGrant, "gmail", "gmails.db")
	// Copy file from storj to local cache if everything's fine.
	// Skip error check, if there's error - we will check that and create new file
	if err == nil {
		dbFile, err := utils.CreateFile(userCacheDBPath)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
		_, err = dbFile.Write(byteDB)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	} else {
		if strings.Contains(err.Error(), "object not found") {
			slog.Warn("gmail db not found")
			fmt.Println(userCacheDBPath)
			dbFile, err := utils.CreateFile(userCacheDBPath)
			if err != nil {
				return c.JSON(http.StatusForbidden, map[string]interface{}{
					"error": err.Error(),
				})
			}
			_, err = dbFile.Write(byteDB)
			if err != nil {
				return c.JSON(http.StatusForbidden, map[string]interface{}{
					"error": err.Error(),
				})
			}
		} else {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}

	db, err := storage.ConnectToEmailDB(userCacheDBPath)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}
	// FETCH THE EMAIL TO GOLANG STRUCT

	GmailClient, err := google.NewGmailClient(c)
	if err != nil {
		if err.Error() == "token error" {
			return c.JSON(http.StatusUnauthorized, map[string]interface{}{
				"error": "token expired",
			})
		} else {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}
	var allMessages []*realgmail.Message
	var nextPageToken string
	for {
		msgs, err := GmailClient.GetUserMessages(nextPageToken)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}

		allMessages = append(allMessages, msgs.Messages...)
		nextPageToken = msgs.NextPageToken

		if nextPageToken == "" {
			break
		}
	}

	for _, message := range allMessages {
		msg, err := GmailClient.GetMessage(message.Id)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}

		msgToSave := storage.GmailMessageSQL{
			ID:      msg.ID,
			Date:    msg.Date,
			From:    msg.From,
			To:      msg.To,
			Subject: msg.Subject,
			Body:    msg.Body,
		}

		// SAVE ATTACHMENTS TO THE STORJ BUCKET AND WRITE THEIR NAMES TO STRUCT

		if len(msg.Attachments) > 0 {
			for _, att := range msg.Attachments {
				err = storj.UploadObject(context.Background(), accesGrant, "gmail", att.FileName, att.Data)
				if err != nil {
					return c.JSON(http.StatusForbidden, map[string]interface{}{
						"error": err.Error(),
					})
				}
				msgToSave.Attachments = msgToSave.Attachments + "|" + att.FileName
			}
		}

		// WRITE ALL EMAILS TO THE DATABASE LOCALLY

		err = db.WriteEmailToDB(&msgToSave)
		if err != nil {
			if !strings.Contains(err.Error(), "UNIQUE constraint failed") {
				return c.JSON(http.StatusForbidden, map[string]interface{}{
					"error": err.Error(),
				})
			}
		}
	}
	// DELETE OLD DB COPY FROM STORJ UPLOAD UP TO DATE DB FILE BACK TO STORJ AND DELETE IT FROM LOCAL CACHE

	// get db file data
	dbByte, err := os.ReadFile(userCacheDBPath)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// delete old db copy from storj
	err = storj.DeleteObject(context.Background(), accesGrant, "gmail", "gmails.db")
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// upload file to storj
	err = storj.UploadObject(context.Background(), accesGrant, "gmail", "gmails.db", dbByte)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{"message": "Emails were successfully uploaded"})
}

func handleListGmailMessagesToStorj(c echo.Context) error {

	accesGrant := c.Request().Header.Get("STORJ_ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "storj access token is missing",
		})
	}

	// CHECK IF EMAIL DATABASE ALREADY EXISTS AND DOWNLOAD IT, IF NOT - CREATE NEW ONE
	userCacheDBPath := "./cache/" + utils.CreateUserTempCacheFolder() + "/gmails.db"
	defer os.Remove(userCacheDBPath)
	byteDB, err := storj.DownloadObject(context.Background(), accesGrant, "gmail", "gmails.db")
	// Copy file from storj to local cache if everything's fine.
	// Skip error check, if there's error - we will check that and create new file
	if err == nil {
		fmt.Println(userCacheDBPath)
		dbFile, err := utils.CreateFile(userCacheDBPath)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
		_, err = dbFile.Write(byteDB)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	} else {
		if strings.Contains(err.Error(), "object not found") {
			slog.Warn("gmail db not found")
			fmt.Println(userCacheDBPath)
			dbFile, err := utils.CreateFile(userCacheDBPath)
			if err != nil {
				return c.JSON(http.StatusForbidden, map[string]interface{}{
					"error": err.Error(),
				})
			}
			_, err = dbFile.Write(byteDB)
			if err != nil {
				return c.JSON(http.StatusForbidden, map[string]interface{}{
					"error": err.Error(),
				})
			}
		} else {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}

	db, err := storage.ConnectToEmailDB(userCacheDBPath)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}
	// FETCH THE EMAIL TO GOLANG STRUCT

	GmailClient, err := google.NewGmailClient(c)
	if err != nil {
		if err.Error() == "token error" {
			return c.JSON(http.StatusUnauthorized, map[string]interface{}{
				"error": "token expired",
			})
		} else {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}
	var allIDs []string
	json.NewDecoder(c.Request().Body).Decode(&allIDs)
	fmt.Println("all ids", allIDs)
	for _, id := range allIDs {
		msg, err := GmailClient.GetMessage(id)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}

		msgToSave := storage.GmailMessageSQL{
			ID:      msg.ID,
			Date:    msg.Date,
			From:    msg.From,
			To:      msg.To,
			Subject: msg.Subject,
			Body:    msg.Body,
		}

		// SAVE ATTACHMENTS TO THE STORJ BUCKET AND WRITE THEIR NAMES TO STRUCT

		if len(msg.Attachments) > 0 {
			for _, att := range msg.Attachments {
				err = storj.UploadObject(context.Background(), accesGrant, "gmail", att.FileName, att.Data)
				if err != nil {
					return c.JSON(http.StatusForbidden, map[string]interface{}{
						"error": err.Error(),
					})
				}
				msgToSave.Attachments = msgToSave.Attachments + "|" + att.FileName
			}
		}

		// WRITE ALL EMAILS TO THE DATABASE LOCALLY

		err = db.WriteEmailToDB(&msgToSave)
		if err != nil {
			// This means that message already exist. We just it and go to the next
			if !strings.Contains(err.Error(), "UNIQUE constraint failed") {
				return c.JSON(http.StatusForbidden, map[string]interface{}{
					"error": err.Error(),
				})
			}
		}
	}
	// DELETE OLD DB COPY FROM STORJ UPLOAD UP TO DATE DB FILE BACK TO STORJ AND DELETE IT FROM LOCAL CACHE

	// get db file data
	dbByte, err := os.ReadFile(userCacheDBPath)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// delete old db copy from storj
	err = storj.DeleteObject(context.Background(), accesGrant, "gmail", "gmails.db")
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// upload file to storj
	err = storj.UploadObject(context.Background(), accesGrant, "gmail", "gmails.db", dbByte)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{"message": "Emails were successfully uploaded"})
}

func handleGetGmailDBFromStorj(c echo.Context) error {
	accesGrant := c.Request().Header.Get("STORJ_ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "storj access token is missing",
		})
	}

	// Download the SQLite database file from Storj
	byteDB, err := storj.DownloadObject(context.Background(), accesGrant, "gmail", "gmails.db")
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"message": "no emails saved in Storj database",
			"error":   err.Error(),
		})
	}

	// Create a temporary cache directory for the user
	userCacheDBPath := "./cache/" + utils.CreateUserTempCacheFolder() + "/gmails.db"
	defer os.Remove(userCacheDBPath)
	// Write the downloaded database file to the local cache
	dbFile, err := utils.CreateFile(userCacheDBPath)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}
	_, err = dbFile.Write(byteDB)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}
	db, err := storage.ConnectToEmailDB(userCacheDBPath)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	messages, err := db.GetAllEmailsFromDB()
	if err != nil {
		fmt.Println("Error retrieving messages from database:", err)
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// Return the list of messages as a JSON response
	return c.JSON(http.StatusOK, messages)
}
