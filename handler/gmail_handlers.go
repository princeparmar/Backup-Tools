package handler

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
	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/middleware"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/pkg/utils"
	"github.com/StorX2-0/Backup-Tools/satellite"
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

// UploadMessagesToSatellite uploads Gmail messages to Satellite and updates synced_objects
func (s *GmailService) UploadMessagesToSatellite(ctx context.Context, database *db.PostgresDb, messageIDs []string) (*UploadResult, error) {
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

				// Use helper function to upload and sync
				// Source and Type are automatically derived from bucket name ("gmail" -> source: "google", type: "gmail")
				err = UploadObjectAndSync(ctx, database, s.accessGrant, "gmail", messagePath, b, s.userEmail)
				if err != nil {
					logger.Info(ctx, "error uploading to satellite", logger.ErrorField(err))
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
				logger.Info(ctx, "error inserting message into Gmail", logger.ErrorField(err))
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
	var filter google.GmailFilter
	if err := decodeFilterJSON(urlEncodedFilter, &filter); err != nil {
		return nil, err
	}
	return &filter, nil
}

// decodeFilterJSON is a generic helper to decode URL-encoded JSON filter parameters
func decodeFilterJSON(urlEncodedFilter string, target interface{}) error {
	// URL decode the filter string
	decodedFilter, err := url.QueryUnescape(urlEncodedFilter)
	if err != nil {
		return fmt.Errorf("failed to URL decode filter: %v", err)
	}

	// Parse the JSON string into target struct
	if err := json.Unmarshal([]byte(decodedFilter), target); err != nil {
		return fmt.Errorf("failed to parse filter JSON: %v", err)
	}

	return nil
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

func HandleListGmailMessagesToSatellite(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	// Setup Gmail handler with all common validations
	accessGrant, gmailClient, err := setupGmailHandler(c)
	if err != nil {
		if err.Error() == "access token not found" {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
		return err
	}

	// Get user details
	userDetails, err := google.GetGoogleAccountDetailsFromContext(c)
	if err != nil {
		return err
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

	// Get database from context
	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)

	// Create Gmail service and upload messages
	gmailService := NewGmailService(gmailClient, accessGrant, userDetails.Email)
	result, err := gmailService.UploadMessagesToSatellite(c.Request().Context(), database, allIDs)
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
func HandleGmailGetThreadsIDsControlled(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	// Extract access grant early for webhook processing
	accessGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accessGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "access token not found",
		})
	}

	go func() {
		processCtx := context.Background()
		database := c.Get(middleware.DbContextKey).(*db.PostgresDb)
		if processErr := ProcessWebhookEvents(processCtx, database, accessGrant, 100); processErr != nil {
			logger.Warn(processCtx, "Failed to process webhook events from listing route",
				logger.ErrorField(processErr))
		}
	}()

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
		return err
	}

	var threads []any

	res, err := gmailClient.GetUserMessagesControlled(nextPageToken, "", numInt, filter)
	if err != nil {
		return err
	}
	//threads = append(threads, res.Messages...)

	userDetails, err := google.GetGoogleAccountDetailsFromContext(c)
	if err != nil {
		return err
	}

	emailListFromBucket, err := satellite.ListObjectsWithPrefix(context.Background(),
		accessGrant, satellite.ReserveBucket_Gmail, userDetails.Email+"/")
	if err != nil {
		logger.Error(ctx, "Failed to list objects from satellite", logger.ErrorField(err))
		userFriendlyError := satellite.FormatSatelliteError(err)
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": userFriendlyError,
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
func HandleGmailDownloadAndInsert(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

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
		return err
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
