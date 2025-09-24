package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
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

// UploadResult represents the result of uploading messages to Satellite
type UploadResult struct {
	ProcessedIDs []string `json:"processed_ids"`
	FailedIDs    []string `json:"failed_ids"`
	Message      string   `json:"message"`
}

// DownloadResult represents the result of downloading messages from Satellite
type DownloadResult struct {
	ProcessedIDs []string `json:"processed_ids"`
	FailedIDs    []string `json:"failed_ids"`
	Message      string   `json:"message"`
}

// GmailService provides consolidated Gmail operations
type GmailService struct {
	client      *google.GmailClient
	accessGrant string
	userEmail   string
}

// NewGmailService creates a new GmailService instance
func NewGmailService(client *google.GmailClient, accessGrant, userEmail string) *GmailService {
	return &GmailService{
		client:      client,
		accessGrant: accessGrant,
		userEmail:   userEmail,
	}
}

// UploadMessagesToSatellite uploads Gmail messages to Satellite
func (s *GmailService) UploadMessagesToSatellite(ctx context.Context, messageIDs []string) (*UploadResult, error) {
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(10)

	processedIDs, failedIDs := utils.NewLockedArray(), utils.NewLockedArray()

	for _, id := range messageIDs {
		// Skip empty or whitespace-only IDs
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}

		func(id string) {
			g.Go(func() error {
				msg, err := s.client.GetMessageDirect(id)
				if err != nil {
					failedIDs.Add(id)
					return nil
				}

				b, err := json.Marshal(msg)
				if err != nil {
					failedIDs.Add(id)
					return nil
				}

				messagePath := s.userEmail + "/" + utils.GenerateTitleFromGmailMessage(msg)

				err = satellite.UploadObject(ctx, s.accessGrant, "gmail", messagePath, b)
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
		return nil, err
	}

	return &UploadResult{
		ProcessedIDs: processedIDs.Get(),
		FailedIDs:    failedIDs.Get(),
		Message:      "all mails were successfully uploaded from Google mail to Satellite",
	}, nil
}

// DownloadMessagesFromSatellite downloads messages from Satellite and inserts them into Gmail
func (s *GmailService) DownloadMessagesFromSatellite(ctx context.Context, keys []string) (*DownloadResult, error) {
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(10)

	processedIDs, failedIDs := utils.NewLockedArray(), utils.NewLockedArray()

	for _, key := range keys {
		key := key
		if key == "" {
			continue
		}
		g.Go(func() error {
			// Download file from Satellite
			data, err := satellite.DownloadObject(ctx, s.accessGrant, satellite.ReserveBucket_Gmail, key)
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
			if err := s.client.InsertMessage(&gmailMsg); err != nil {
				fmt.Println(err)
				failedIDs.Add(key)
			} else {
				processedIDs.Add(key)
			}
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return nil, err
	}

	return &DownloadResult{
		ProcessedIDs: processedIDs.Get(),
		FailedIDs:    failedIDs.Get(),
		Message:      "all gmail messages processed",
	}, nil
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

// Helper function to parse request IDs from JSON or form data
func parseRequestIDs(c echo.Context) ([]string, error) {
	var allIDs []string
	if strings.Contains(c.Request().Header.Get(echo.HeaderContentType), echo.MIMEApplicationJSON) {
		// Decode JSON array from request body
		if err := json.NewDecoder(c.Request().Body).Decode(&allIDs); err != nil {
			return nil, errors.New("invalid JSON format")
		}
	} else {
		// Handle form data
		formIDs := c.FormValue("ids")
		allIDs = strings.Split(formIDs, ",")
	}
	return allIDs, nil
}

// Helper function to validate and process request IDs
func validateAndProcessRequestIDs(c echo.Context) ([]string, error) {
	allIDs, err := parseRequestIDs(c)
	if err != nil {
		return nil, err
	}

	// Process base64 decoding for download operations
	for i := range allIDs {
		allIDs[i] = strings.TrimSpace(allIDs[i])
		decodedID, err := base64.StdEncoding.DecodeString(allIDs[i])
		if err != nil {
			return nil, errors.New("invalid base64 format")
		}
		allIDs[i] = string(decodedID)
	}

	// Validate request
	if len(allIDs) == 0 || allIDs[0] == "" {
		return nil, errors.New("no keys provided")
	}
	if len(allIDs) > 10 {
		return nil, errors.New("maximum 10 keys allowed")
	}

	return allIDs, nil
}

// Helper function to setup Gmail handler with all common validations
func setupGmailHandler(c echo.Context) (string, *google.GmailClient, error) {
	// Validate access token
	accessGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accessGrant == "" {
		return "", nil, errors.New("access token not found")
	}

	// Create Gmail client
	gmailClient, err := google.NewGmailClient(c)
	if err != nil {
		return "", nil, err
	}

	return accessGrant, gmailClient, nil
}

func handleListGmailMessagesToSatellite(c echo.Context) error {
	// Setup Gmail handler with all common validations
	accessGrant, gmailClient, err := setupGmailHandler(c)
	if err != nil {
		if err.Error() == "access token not found" {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
		return HandleError(c, err, "setup Gmail handler")
	}

	// Get user details
	userDetails, err := google.GetGoogleAccountDetailsFromContext(c)
	if err != nil {
		return HandleError(c, err, "get Google account details")
	}

	if userDetails.Email == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "user email not found, please check access handling",
		})
	}

	// Parse request IDs
	allIDs, err := parseRequestIDs(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// Create Gmail service and upload messages
	gmailService := NewGmailService(gmailClient, accessGrant, userDetails.Email)
	result, err := gmailService.UploadMessagesToSatellite(c.Request().Context(), allIDs)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error":         err.Error(),
			"failed_ids":    result.FailedIDs,
			"processed_ids": result.ProcessedIDs,
		})
	}

	return c.JSON(http.StatusOK, result)
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

	gmailClient, err := google.NewGmailClient(c)
	if err != nil {
		return HandleError(c, err, "create Gmail client")
	}

	var threads []any

	res, err := gmailClient.GetUserMessagesControlled(nextPageToken, "", numInt, filter)
	if err != nil {
		return HandleError(c, err, "get Gmail messages")
	}
	//threads = append(threads, res.Messages...)

	accessGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accessGrant == "" {
		return errors.New("access token not found")
	}

	userDetails, err := google.GetGoogleAccountDetailsFromContext(c)
	if err != nil {
		return HandleError(c, err, "get Google account details")
	}

	emailListFromBucket, err := satellite.ListObjectsWithPrefix(context.Background(),
		accessGrant, satellite.ReserveBucket_Gmail, userDetails.Email+"/")
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

	// Validate and process request IDs
	allIDs, err := validateAndProcessRequestIDs(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// Get Gmail client
	gmailClient, err := google.NewGmailClient(c)
	if err != nil {
		return HandleError(c, err, "create Gmail client")
	}

	// Create Gmail service and download messages
	gmailService := NewGmailService(gmailClient, accessGrant, "")
	result, err := gmailService.DownloadMessagesFromSatellite(c.Request().Context(), allIDs)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error":         err.Error(),
			"failed_ids":    result.FailedIDs,
			"processed_ids": result.ProcessedIDs,
		})
	}

	return c.JSON(http.StatusOK, result)
}
