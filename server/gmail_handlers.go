package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	google "github.com/StorX2-0/Backup-Tools/apps/google"
	"github.com/StorX2-0/Backup-Tools/satellite"
	"github.com/StorX2-0/Backup-Tools/utils"
	"golang.org/x/sync/errgroup"

	"github.com/labstack/echo/v4"
	"google.golang.org/api/gmail/v1"
)

type MessageListJSON struct {
	gmail.Message
	Synced bool `json:"synced"`
}

// decodeURLFilter decodes a URL-encoded JSON filter parameter and returns a GmailFilter
func DecodeURLFilter(urlEncodedFilter string) (*google.GmailFilter, error) {
	// URL decode the filter string
	decodedFilter, err := url.QueryUnescape(urlEncodedFilter)
	if err != nil {
		return nil, fmt.Errorf("failed to URL decode filter: %v", err)
	}

	// Parse the JSON string into GmailFilter struct
	var filter google.GmailFilter
	if err := json.Unmarshal([]byte(decodedFilter), &filter); err != nil {
		return nil, fmt.Errorf("failed to parse filter JSON: %v", err)
	}

	return &filter, nil
}

func handleListGmailMessagesToSatellite(c echo.Context) error {

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

	userDetails, err := google.GetGoogleAccountDetailsFromContext(c)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	if userDetails.Email == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "user email not found, please check access handling",
		})
	}

	g, ctx := errgroup.WithContext(c.Request().Context())
	g.SetLimit(10)

	processedIDs, failedIDs := utils.NewLockedArray(), utils.NewLockedArray()
	for _, id := range allIDs {
		func(id string) {
			g.Go(func() error {
				msg, err := GmailClient.GetMessageDirect(id)
				if err != nil {
					failedIDs.Add(id)
					return nil
				}

				b, err := json.Marshal(msg)
				if err != nil {
					failedIDs.Add(id)
					return nil
				}

				messagePath := userDetails.Email + "/" + utils.GenerateTitleFromGmailMessage(msg)

				err = satellite.UploadObject(ctx, accesGrant, "gmail", messagePath, b)
				if err != nil {
					fmt.Println("error uploading to satellite", err)
					failedIDs.Add(id)
					return nil
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

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message":       "all mails were successfully uploaded from Google mail to Satellite",
		"failed_ids":    failedIDs.Get(),
		"processed_ids": processedIDs.Get(),
	})
}

// handleGmailGetThreadsIDsControlled - fetches threads IDs from Gmail and returns them in JSON format.
// It uses pagination to fetch threads in chunks of 500.
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

	// Parse filter from JWT-encoded query parameter
	var filter *google.GmailFilter
	if filterParam := c.QueryParam("filter"); filterParam != "" {
		decodedFilter, err := DecodeURLFilter(filterParam)
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"error": "invalid filter parameter: " + err.Error(),
			})
		}
		filter = decodedFilter
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

	var threads []any

	res, err := GmailClient.GetUserMessagesControlled(nextPageToken, "", numInt, filter)
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

	userDetails, err := google.GetGoogleAccountDetailsFromContext(c)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	emailListFromBucket, err := satellite.ListObjectsWithPrefix(context.Background(),
		accesGrant, satellite.ReserveBucket_Gmail, userDetails.Email+"/")
	if err != nil && !strings.Contains(err.Error(), "object not found") {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	for _, message := range res.Messages {
		_, synced := emailListFromBucket[userDetails.Email+"/"+utils.GenerateTitleFromGmailMessage(message)]
		threads = append(threads, MessageListJSON{Message: *message, Synced: synced})
	}
	nextPageToken = res.NextPageToken

	return c.JSON(http.StatusOK, map[string]any{"messages": threads, "nextPageToken": nextPageToken})
}

// handleGmailDownloadAndInsert - downloads emails from Satellite and inserts them into Gmail.
// It uses pagination to download emails in chunks of 10.
func handleGmailDownloadAndInsert(c echo.Context) error {
	// Get access token from header
	accessGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accessGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "access token not found",
		})
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

	for i := range allIDs {
		allIDs[i] = strings.TrimSpace(allIDs[i])
		decodedID, err := base64.StdEncoding.DecodeString(allIDs[i])
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"error": "invalid base64 format",
			})
		}
		allIDs[i] = string(decodedID)
	}

	// Validate request
	if len(allIDs) == 0 || allIDs[0] == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error": "no keys provided",
		})
	}
	if len(allIDs) > 10 {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error": "maximum 10 keys allowed",
		})
	}

	// Get Gmail client
	gmailClient, err := google.NewGmailClient(c)
	if err != nil {
		if err.Error() == "token error" {
			return c.JSON(http.StatusUnauthorized, map[string]interface{}{
				"error": "token expired",
			})
		}
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	g, ctx := errgroup.WithContext(c.Request().Context())
	g.SetLimit(10)

	processedIDs, failedIDs := utils.NewLockedArray(), utils.NewLockedArray()

	for _, key := range allIDs {
		key := key
		if key == "" {
			continue
		}
		g.Go(func() error {
			// Download file from Satellite
			data, err := satellite.DownloadObject(ctx, accessGrant, satellite.ReserveBucket_Gmail, key)
			if err != nil {
				failedIDs.Add(key)
				return nil
			}

			// Parse the email data and insert into Gmail
			var gmailMsg gmail.Message
			if err := json.Unmarshal(data, &gmailMsg); err != nil {
				failedIDs.Add(key)
				return nil
			}

			// Insert message into Gmail
			if err := gmailClient.InsertMessage(&gmailMsg); err != nil {
				fmt.Println(err)
				failedIDs.Add(key)
			} else {
				processedIDs.Add(key)
			}
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error":         err.Error(),
			"failed_ids":    failedIDs.Get(),
			"processed_ids": processedIDs.Get(),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message":       "all gmail messages processed",
		"failed_ids":    failedIDs.Get(),
		"processed_ids": processedIDs.Get(),
	})
}
