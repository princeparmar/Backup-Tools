package server

import (
	"cmp"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"

	google "github.com/StorX2-0/Backup-Tools/apps/google"
	"github.com/StorX2-0/Backup-Tools/satellite"
	"github.com/StorX2-0/Backup-Tools/storage"
	"github.com/StorX2-0/Backup-Tools/utils"

	"github.com/labstack/echo/v4"
	"golang.org/x/sync/errgroup"
	"google.golang.org/api/gmail/v1"
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
	var nextPageToken string
	var threads []*gmail.Thread
	for {
		res, err := GmailClient.GetUserThreads(nextPageToken)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
		threads = append(threads, res.Threads...)
		//allMessages = append(allMessages, msgs.Messages...)
		nextPageToken = res.NextPageToken

		if nextPageToken == "" {
			break
		}
	}

	return c.JSON(http.StatusOK, threads)
}

func handleGmailGetThreadsIDs(c echo.Context) error {
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
	var nextPageToken string
	var threads []*gmail.Thread
	for {
		res, err := GmailClient.GetUserThreadsIDs(nextPageToken)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
		threads = append(threads, res.Threads...)
		//allMessages = append(allMessages, msgs.Messages...)
		nextPageToken = res.NextPageToken

		if nextPageToken == "" {
			break
		}
	}

	return c.JSON(http.StatusOK, threads)
}

type MessageListJSON struct {
	gmail.Message
	Synced bool `json:"synced"`
}

// Fetches user messages, returns their ID's and threat's IDs.
func handleGmailGetMessages(c echo.Context) error {

	accesGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "access token not found",
		})
	}

	// CHECK IF EMAIL DATABASE ALREADY EXISTS AND DOWNLOAD IT, IF NOT - CREATE NEW ONE
	userCacheDBPath := "./cache/" + utils.CreateUserTempCacheFolder() + "/gmails.db"
	defer os.Remove(userCacheDBPath)
	byteDB, err := satellite.DownloadObject(context.Background(), accesGrant, satellite.ReserveBucket_Gmail, "gmails.db")
	// Copy file from satellite to local cache if everything's fine.
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
			allMessages = append(allMessages, MessageListJSON{Message: *message, Synced: synced})
		}
		//allMessages = append(allMessages, msgs.Messages...)
		nextPageToken = msgs.NextPageToken

		if nextPageToken == "" {
			break
		}
	}
	return c.JSON(http.StatusOK, allMessages)
}

// Fetches user messages, returns their ID's and threat's IDs.
func handleGmailGetMessagesIDs(c echo.Context) error {

	accesGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "access token not found",
		})
	}

	// CHECK IF EMAIL DATABASE ALREADY EXISTS AND DOWNLOAD IT, IF NOT - CREATE NEW ONE
	userCacheDBPath := "./cache/" + utils.CreateUserTempCacheFolder() + "/gmails.db"
	defer os.Remove(userCacheDBPath)
	byteDB, err := satellite.DownloadObject(context.Background(), accesGrant, satellite.ReserveBucket_Gmail, "gmails.db")
	// Copy file from satellite to local cache if everything's fine.
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

	var allMessages []any
	var nextPageToken string
	for {
		msgs, err := GmailClient.GetUserMessagesIDs(nextPageToken)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
		for _, message := range msgs.Messages {
			_, synced := slices.BinarySearchFunc(messages, message.Id, func(a *storage.GmailMessageSQL, b string) int {
				return cmp.Compare(a.ID, b)
			})
			allMessages = append(allMessages, MessageListJSON{Message: *message, Synced: synced})
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

func handleGmailGetThread(c echo.Context) error {
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
	msg, err := GmailClient.GetThread(id)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	return c.JSON(http.StatusOK, msg)
}

// Fetches message from Gmail by given ID as a parameter and writes it into SQLite Database in Satellite.
// If there's no database yet - creates one.
func handleGmailMessageToSatellite(c echo.Context) error {
	id := c.Param("ID")
	accesGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "access token not found",
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

	// SAVE ATTACHMENTS TO THE SATELLITE BUCKET AND WRITE THEIR NAMES TO STRUCT

	if len(msg.Attachments) > 0 {
		for _, att := range msg.Attachments {
			err = satellite.UploadObject(context.Background(), accesGrant, "gmail", att.FileName, att.Data)
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
	byteDB, err := satellite.DownloadObject(context.Background(), accesGrant, satellite.ReserveBucket_Gmail, "gmails.db")
	// Copy file from satellite to local cache if everything's fine.
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

	// DELETE OLD DB COPY FROM SATELLITE UPLOAD UP TO DATE DB FILE BACK TO SATELLITE AND DELETE IT FROM LOCAL CACHE

	// get db file data
	dbByte, err := os.ReadFile(userCacheDBPath)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// delete old db copy from SATELLITE
	err = satellite.DeleteObject(context.Background(), accesGrant, "gmail", "gmails.db")
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// upload file to SATELLITE
	err = satellite.UploadObject(context.Background(), accesGrant, "gmail", "gmails.db", dbByte)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{"message": "Email was successfully uploaded"})
}

func handleAllGmailMessagesToSatellite(c echo.Context) error {

	accesGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "access token not found",
		})
	}

	// CHECK IF EMAIL DATABASE ALREADY EXISTS AND DOWNLOAD IT, IF NOT - CREATE NEW ONE
	userCacheDBPath := "./cache/" + utils.CreateUserTempCacheFolder() + "/gmails.db"
	defer os.Remove(userCacheDBPath)
	byteDB, err := satellite.DownloadObject(context.Background(), accesGrant, satellite.ReserveBucket_Gmail, "gmails.db")
	// Copy file from SATELLITE to local cache if everything's fine.
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
	var allMessages []*gmail.Message
	var nextPageToken string
	for {
		msgs, err := GmailClient.GetUserMessagesIDs(nextPageToken)
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

	g, ctx := errgroup.WithContext(c.Request().Context())
	g.SetLimit(10)

	processedIDs, failedIDs := utils.NewLockedArray(), utils.NewLockedArray()
	for _, message := range allMessages {
		func(message *gmail.Message) {
			g.Go(func() error {
				msg, err := GmailClient.GetMessage(message.Id)
				if err != nil {
					failedIDs.Add(message.Id)
					return nil
				}

				msgToSave := storage.GmailMessageSQL{
					ID:      msg.ID,
					Date:    msg.Date,
					From:    msg.From,
					To:      msg.To,
					Subject: msg.Subject,
					Body:    msg.Body,
				}

				// SAVE ATTACHMENTS TO THE SATELLITE BUCKET AND WRITE THEIR NAMES TO STRUCT

				if len(msg.Attachments) > 0 {
					for _, att := range msg.Attachments {
						err = satellite.UploadObject(ctx, accesGrant, "gmail", att.FileName, att.Data)
						if err != nil {
							failedIDs.Add(message.Id)
							return nil
						}
						msgToSave.Attachments = msgToSave.Attachments + "|" + att.FileName
					}
				}

				// WRITE ALL EMAILS TO THE DATABASE LOCALLY

				err = db.WriteEmailToDB(&msgToSave)
				if err != nil {
					if !strings.Contains(err.Error(), "UNIQUE constraint failed") {
						failedIDs.Add(message.Id)
						return nil
					}
				}
				processedIDs.Add(message.Id)
				return nil
			})
		}(message)
	}

	if err := g.Wait(); err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error":         err.Error(),
			"failed_ids":    failedIDs.Get(),
			"processed_ids": processedIDs.Get(),
		})
	}

	// DELETE OLD DB COPY FROM SATELLITE UPLOAD UP TO DATE DB FILE BACK TO SATELLITE AND DELETE IT FROM LOCAL CACHE

	// get db file data
	dbByte, err := os.ReadFile(userCacheDBPath)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// delete old db copy from SATELLITE
	err = satellite.DeleteObject(context.Background(), accesGrant, "gmail", "gmails.db")
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// upload file to SATELLITE
	err = satellite.UploadObject(context.Background(), accesGrant, "gmail", "gmails.db", dbByte)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message":       "all items were successfully uploaded from gmail to Satellite",
		"failed_ids":    failedIDs.Get(),
		"processed_ids": processedIDs.Get(),
	})
}

func handleListGmailMessagesToSatellite(c echo.Context) error {

	accesGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "access token not found",
		})
	}

	// CHECK IF EMAIL DATABASE ALREADY EXISTS AND DOWNLOAD IT, IF NOT - CREATE NEW ONE
	userCacheDBPath := "./cache/" + utils.CreateUserTempCacheFolder() + "/gmails.db"
	defer os.Remove(userCacheDBPath)
	byteDB, err := satellite.DownloadObject(context.Background(), accesGrant, satellite.ReserveBucket_Gmail, "gmails.db")
	// Copy file from SATELLITE to local cache if everything's fine.
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
	if strings.Contains(c.Request().Header.Get(echo.HeaderContentType), echo.MIMEApplicationJSON) {
		// Decode JSON array from request body
		if err := json.NewDecoder(c.Request().Body).Decode(&allIDs); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"error": "invalid JSON format",
			})
		}
	} else {
		// Handle form data
		formIDs := c.FormValue("ids")
		allIDs = strings.Split(formIDs, ",")
	}

	g, ctx := errgroup.WithContext(c.Request().Context())
	g.SetLimit(10)

	processedIDs, failedIDs := utils.NewLockedArray(), utils.NewLockedArray()
	for _, id := range allIDs {
		func(id string) {
			g.Go(func() error {
				msg, err := GmailClient.GetMessage(id)
				if err != nil {
					failedIDs.Add(id)
					return nil
				}

				msgToSave := storage.GmailMessageSQL{
					ID:      msg.ID,
					Date:    msg.Date,
					From:    msg.From,
					To:      msg.To,
					Subject: msg.Subject,
					Body:    msg.Body,
				}

				// SAVE ATTACHMENTS TO THE SATELLITE BUCKET AND WRITE THEIR NAMES TO STRUCT

				if len(msg.Attachments) > 0 {
					for _, att := range msg.Attachments {
						err = satellite.UploadObject(ctx, accesGrant, "gmail", att.FileName, att.Data)
						if err != nil {
							failedIDs.Add(id)
							return nil
						}
						msgToSave.Attachments = msgToSave.Attachments + "|" + att.FileName
					}
				}

				// WRITE ALL EMAILS TO THE DATABASE LOCALLY

				err = db.WriteEmailToDB(&msgToSave)
				if err != nil {
					// This means that message already exist. We just it and go to the next
					if !strings.Contains(err.Error(), "UNIQUE constraint failed") {
						failedIDs.Add(id)
						return nil
					}
				}
				processedIDs.Add(id)
				return nil
			})
		}(id)
	}
	if err := g.Wait(); err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error":         err.Error(),
			"failed_ids":    failedIDs.Get(),
			"processed_ids": processedIDs.Get(),
		})
	}
	// DELETE OLD DB COPY FROM SATELLITE UPLOAD UP TO DATE DB FILE BACK TO SATELLITE AND DELETE IT FROM LOCAL CACHE

	// get db file data
	dbByte, err := os.ReadFile(userCacheDBPath)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// delete old db copy from SATELLITE
	err = satellite.DeleteObject(context.Background(), accesGrant, "gmail", "gmails.db")
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// upload file to SATELLITE
	err = satellite.UploadObject(context.Background(), accesGrant, "gmail", "gmails.db", dbByte)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message":       "all mails were successfully uploaded from Google mail to Satellite",
		"failed_ids":    failedIDs.Get(),
		"processed_ids": processedIDs.Get(),
	})
}

func handleGetGmailDBFromSatellite(c echo.Context) error {
	accesGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "access token not found",
		})
	}

	// Download the SQLite database file from SATELLITE
	byteDB, err := satellite.DownloadObject(context.Background(), accesGrant, satellite.ReserveBucket_Gmail, "gmails.db")
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"message": "no emails saved in satellite",
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
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// Return the list of messages as a JSON response
	return c.JSON(http.StatusOK, messages)
}

func handleGmailGetThreadsIDsControlled(c echo.Context) error {
	num := c.QueryParam("num")
	var numInt int64
	if num != "" {
		var err error
		if numInt, err = strconv.ParseInt(num, 10, 64); err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	} else {
		numInt = 500
	}
	nextPageToken := c.QueryParam("nextPageToken")
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

	var threads []any

	res, err := GmailClient.GetUserMessagesControlled(nextPageToken, numInt)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}
	//threads = append(threads, res.Messages...)
	accesGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "access token not found",
		})
	}

	// CHECK IF EMAIL DATABASE ALREADY EXISTS AND DOWNLOAD IT, IF NOT - CREATE NEW ONE
	userCacheDBPath := "./cache/" + utils.CreateUserTempCacheFolder() + "/gmails.db"
	defer os.Remove(userCacheDBPath)
	byteDB, err := satellite.DownloadObject(context.Background(), accesGrant, satellite.ReserveBucket_Gmail, "gmails.db")
	// Copy file from SATELLITE to local cache if everything's fine.
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
	for _, message := range res.Messages {
		_, synced := slices.BinarySearchFunc(messages, message.Id, func(a *storage.GmailMessageSQL, b string) int {
			return cmp.Compare(a.ID, b)
		})
		threads = append(threads, MessageListJSON{Message: *message, Synced: synced})
	}
	nextPageToken = res.NextPageToken

	return c.JSON(http.StatusOK, map[string]any{"messages": threads, "nextPageToken": nextPageToken})
}

// Fetches user messages, returns their ID's and threat's IDs.
func handleGmailGetMessagesUsingWorkers(c echo.Context) error {

	accesGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "access token not found",
		})
	}

	// CHECK IF EMAIL DATABASE ALREADY EXISTS AND DOWNLOAD IT, IF NOT - CREATE NEW ONE
	userCacheDBPath := "./cache/" + utils.CreateUserTempCacheFolder() + "/gmails.db"
	defer os.Remove(userCacheDBPath)
	byteDB, err := satellite.DownloadObject(context.Background(), accesGrant, satellite.ReserveBucket_Gmail, "gmails.db")
	// Copy file from SATELLITE to local cache if everything's fine.
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
		msgs, err := GmailClient.GetUserMessagesUsingWorkers(nextPageToken, 25)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
		for _, message := range msgs.Messages {
			_, synced := slices.BinarySearchFunc(messages, message.Id, func(a *storage.GmailMessageSQL, b string) int {
				return cmp.Compare(a.ID, b)
			})
			allMessages = append(allMessages, MessageListJSON{Message: *message, Synced: synced})
		}
		//allMessages = append(allMessages, msgs.Messages...)
		nextPageToken = msgs.NextPageToken

		if nextPageToken == "" {
			break
		}
	}
	return c.JSON(http.StatusOK, allMessages)
}
