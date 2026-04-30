package handler

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/labstack/echo/v4"
	"storj.io/common/encryption"
	"storj.io/common/grant"
	"storj.io/common/paths"
)

const (
	maxRequestBodySize              = 5 << 20
	requiredRSAKeySize              = 256
	uuidStringLength                = 36
	uuidByteLength                  = 16
	oaepLabel                       = "storx-webhook"
	webhookFixedWorkers             = 3
	webhookDeleteChunkSize          = 500
	webhookMaxSyncDeleteRetries     = 3
	webhookProcessedRetention       = 24 * time.Hour
	syncedObjectSoftDeletedPurgeAge = 30 * 24 * time.Hour
	webhookCleanupDeleteBatchSize   = 5000
)

var (
	bucketToMethod = map[string]string{
		"gmail":         "gmail",
		"outlook":       "outlook",
		"google-drive":  "google-drive",
		"google-cloud":  "google-cloud",
		"google-photos": "google-photos",
		"dropbox":       "dropbox",
		"aws-s3":        "aws-s3",
		"github":        "github",
		"shopify":       "shopify",
		"quickbooks":    "quickbooks",
	}
)

type TableChangeEvent struct {
	Operation string          `json:"operation"`
	Table     string          `json:"table"`
	Timestamp time.Time       `json:"timestamp"`
	Data      json.RawMessage `json:"data,omitempty"`
	OldData   json.RawMessage `json:"old_data,omitempty"`
}

type WebhookResponse struct {
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

type WebhookDecryptor struct {
	privateKey *rsa.PrivateKey
}

func NewWebhookDecryptor(privateKeyPath string) (*WebhookDecryptor, error) {
	data, err := os.ReadFile(privateKeyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read private key: %w", err)
	}

	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("failed to decode PEM block")
	}

	var key interface{}
	switch block.Type {
	case "PRIVATE KEY":
		key, err = x509.ParsePKCS8PrivateKey(block.Bytes)
	case "RSA PRIVATE KEY":
		key, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	default:
		return nil, fmt.Errorf("unsupported key type: %s", block.Type)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to parse private key: %w", err)
	}

	rsaKey, ok := key.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("not an RSA private key")
	}

	return &WebhookDecryptor{privateKey: rsaKey}, nil
}

func (d *WebhookDecryptor) DecryptPayload(encryptedData []byte) ([]byte, error) {
	if d.privateKey.Size() < requiredRSAKeySize {
		return nil, fmt.Errorf("weak RSA key: minimum 2048-bit required")
	}

	parts := strings.SplitN(string(encryptedData), ":", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid hybrid payload format, expected base64(aesKey):base64(cipher)")
	}

	encryptedAESKey, err := decodeBase64URL(parts[0])
	if err != nil {
		return nil, fmt.Errorf("invalid AES key encoding: %w", err)
	}

	encryptedPayload, err := decodeBase64URL(parts[1])
	if err != nil {
		return nil, fmt.Errorf("invalid payload encoding: %w", err)
	}

	aesKey, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, d.privateKey, encryptedAESKey, []byte(oaepLabel))
	if err != nil {
		aesKey, err = rsa.DecryptOAEP(sha256.New(), rand.Reader, d.privateKey, encryptedAESKey, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to decrypt AES key (tried with and without OAEP label): %w", err)
		}
	}

	if len(aesKey) != 16 && len(aesKey) != 24 && len(aesKey) != 32 {
		return nil, fmt.Errorf("invalid AES key size: %d bytes (expected 16, 24, or 32)", len(aesKey))
	}

	defer func() {
		for i := range aesKey {
			aesKey[i] = 0
		}
	}()

	block, err := aes.NewCipher(aesKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create AES cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCM: %w", err)
	}

	nonceSize := gcm.NonceSize()
	minSize := nonceSize + gcm.Overhead()
	if len(encryptedPayload) < minSize {
		return nil, fmt.Errorf("encrypted payload too short: need at least %d bytes, got %d", minSize, len(encryptedPayload))
	}

	nonce := encryptedPayload[:nonceSize]
	ciphertext := encryptedPayload[nonceSize:]

	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt payload: %w", err)
	}

	return plaintext, nil
}

func decodeBase64URL(s string) ([]byte, error) {
	if decoded, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		return decoded, nil
	}
	return base64.URLEncoding.DecodeString(s)
}

func HandleWebhook(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	decryptor, ok := c.Get("webhook_decryptor").(*WebhookDecryptor)
	if !ok || decryptor == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "webhook decryptor not initialized")
	}

	database, ok := c.Get("__db").(*db.PostgresDb)
	if !ok || database == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "database not initialized")
	}

	if c.Request().Method != http.MethodPost {
		return respondWebhookError(c, http.StatusMethodNotAllowed, "only POST method is allowed")
	}

	if subtle.ConstantTimeCompare([]byte(c.Request().Header.Get("X-Encryption")), []byte("RSA-AES")) == 0 {
		logger.Info(ctx, "missing or invalid encryption header")
		return respondWebhookError(c, http.StatusBadRequest, "invalid encryption header")
	}

	r := http.MaxBytesReader(c.Response(), c.Request().Body, maxRequestBodySize)
	encryptedData, err := io.ReadAll(r)
	if err != nil {
		logger.Error(ctx, "failed to read request body", logger.ErrorField(err))
		return respondWebhookError(c, http.StatusBadRequest, "failed to read request body")
	}

	plaintext, err := decryptor.DecryptPayload(encryptedData)
	if err != nil {
		logger.Error(ctx, "failed to decrypt payload", logger.ErrorField(err))
		return respondWebhookError(c, http.StatusBadRequest, "failed to decrypt payload")
	}

	events, err := parseWebhookPayloadEvents(plaintext)
	if err != nil {
		logger.Error(ctx, "failed to decode JSON", logger.ErrorField(err))
		return respondWebhookError(c, http.StatusBadRequest, "invalid JSON payload")
	}

	for i := range events {
		if err := validateWebhookEvent(&events[i]); err != nil {
			logger.Error(ctx, "invalid event structure in payload",
				logger.Int("event_index", i),
				logger.ErrorField(err),
			)
			return respondWebhookError(c, http.StatusBadRequest, err.Error())
		}
		dataJSON := extractEventData(&events[i])
		if err := storeWebhookEvent(ctx, database, &events[i], dataJSON); err != nil {
			logger.Error(ctx, "failed to store webhook event",
				logger.Int("event_index", i),
				logger.String("operation", events[i].Operation),
				logger.String("table", events[i].Table),
				logger.ErrorField(err),
			)
		}
	}

	logger.Info(ctx, "Webhook events received",
		logger.Int("count", len(events)),
	)

	return respondWebhookSuccess(c, fmt.Sprintf("%d event(s) received successfully", len(events)))
}

// ProcessWebhookEvents processes pending webhook events from the database
func ProcessWebhookEvents(ctx context.Context, database *db.PostgresDb, accessGrant string, limit int) error {
	events, err := database.WebhookEventRepo.GetWebhookEvents(limit, 0, "objects", "received")
	if err != nil {
		return fmt.Errorf("failed to get webhook events: %w", err)
	}

	if len(events) == 0 {
		return nil
	}

	if accessGrant != "" {
		logger.Info(ctx, "Processing webhook events with provided access grant",
			logger.String("count", fmt.Sprintf("%d", len(events))))
	} else {
		logger.Info(ctx, "Processing webhook events (will get access grant from database)",
			logger.String("count", fmt.Sprintf("%d", len(events))))
	}

	workerCount := webhookFixedWorkers
	logger.Info(ctx, "Starting webhook worker pool",
		logger.String("workers", fmt.Sprintf("%d", workerCount)),
		logger.String("batch_size", fmt.Sprintf("%d", len(events))))

	jobs := make(chan repo.WebhookEvent, len(events))
	results := make(chan webhookProcessOutcome, len(events))
	var wg sync.WaitGroup
	wg.Add(workerCount)
	for i := 0; i < workerCount; i++ {
		go func() {
			defer wg.Done()
			for event := range jobs {
				results <- processSingleWebhookEvent(ctx, database, &event, accessGrant)
			}
		}()
	}
	for _, event := range events {
		jobs <- event
	}
	close(jobs)
	wg.Wait()
	close(results)

	outcomes := make([]webhookProcessOutcome, 0, len(events))
	deleteBuckets := make(map[string]*webhookDeleteBatch)
	for outcome := range results {
		outcomeIdx := len(outcomes)
		outcomes = append(outcomes, outcome)
		if outcome.DeleteCandidate {
			batch, ok := deleteBuckets[outcome.BucketName]
			if !ok {
				batch = &webhookDeleteBatch{
					KeySet:        make(map[string]struct{}),
					OutcomeIdxSet: make(map[int]struct{}),
				}
				deleteBuckets[outcome.BucketName] = batch
			}
			batch.KeySet[outcome.DecryptedKey] = struct{}{}
			batch.OutcomeIdxSet[outcomeIdx] = struct{}{}
		}
	}

	for bucketName, batch := range deleteBuckets {
		keys := make([]string, 0, len(batch.KeySet))
		for key := range batch.KeySet {
			keys = append(keys, key)
		}
		for start := 0; start < len(keys); start += webhookDeleteChunkSize {
			end := start + webhookDeleteChunkSize
			if end > len(keys) {
				end = len(keys)
			}
			if err := database.SyncedObjectRepo.DeleteSyncedObjectsByBucketAndKeys(bucketName, keys[start:end]); err != nil {
				sanitizedErr := sanitizeErrorMessage(fmt.Sprintf("batch delete failed for bucket=%s: %v", bucketName, err))
				for idx := range batch.OutcomeIdxSet {
					newRetry := outcomes[idx].PriorRetryCount + 1
					rc := newRetry
					outcomes[idx].RetryCountUpdate = &rc
					outcomes[idx].ErrorMsg = sanitizedErr
					if newRetry >= webhookMaxSyncDeleteRetries {
						outcomes[idx].Status = "failed"
					} else {
						outcomes[idx].Status = "received"
					}
				}
				break
			}
		}
	}

	updates := make([]repo.WebhookEventStatusUpdate, 0, len(outcomes))
	for _, outcome := range outcomes {
		u := repo.WebhookEventStatusUpdate{
			EventID:  outcome.EventID,
			Status:   outcome.Status,
			ErrorMsg: sanitizeErrorMessage(outcome.ErrorMsg),
		}
		if outcome.Status != "processed" && outcome.RetryCountUpdate != nil {
			u.RetryCount = outcome.RetryCountUpdate
		}
		updates = append(updates, u)
	}
	if err := database.WebhookEventRepo.UpdateEventStatusesBatch(updates); err != nil {
		return fmt.Errorf("failed to batch update webhook statuses: %w", err)
	}
	processedCount, failedCount, requeuedCount := 0, 0, 0
	for _, outcome := range outcomes {
		switch outcome.Status {
		case "failed":
			failedCount++
		case "received":
			requeuedCount++
		default:
			processedCount++
		}
	}
	logger.Info(ctx, "Webhook batch processed",
		logger.Int("total", len(outcomes)),
		logger.Int("processed", processedCount),
		logger.Int("failed", failedCount),
		logger.Int("requeued", requeuedCount),
		logger.Int("workers", workerCount))
	return nil
}

type webhookProcessOutcome struct {
	EventID          uint
	Status           string
	ErrorMsg         string
	BucketName       string
	DecryptedKey     string
	DeleteCandidate  bool
	PriorRetryCount  uint
	RetryCountUpdate *uint
}

type webhookDeleteBatch struct {
	KeySet        map[string]struct{}
	OutcomeIdxSet map[int]struct{}
}

func processSingleWebhookEvent(ctx context.Context, database *db.PostgresDb, event *repo.WebhookEvent, accessGrant string) webhookProcessOutcome {
	outcome := webhookProcessOutcome{
		EventID:         event.ID,
		Status:          "processed",
		PriorRetryCount: event.RetryCount,
	}
	if event.Operation != "DELETE" || event.Table != "objects" {
		logger.Info(ctx, "Skipping event (not DELETE operation on objects table)",
			logger.String("event_id", fmt.Sprintf("%d", event.ID)),
			logger.String("operation", event.Operation),
			logger.String("table", event.Table),
		)
		return outcome
	}

	var eventData map[string]interface{}
	if err := json.Unmarshal(event.Data, &eventData); err != nil {
		outcome.Status = "failed"
		outcome.ErrorMsg = fmt.Sprintf("failed to parse event data: %v", err)
		return outcome
	}

	bucketRaw := getStringFromMap(eventData, "bucket_name")
	bucketName := autoDecodeString(bucketRaw)
	if bucketName == "" {
		outcome.ErrorMsg = "bucket_name missing or invalid"
		return outcome
	}

	objectKeyRaw := getStringFromMap(eventData, "object_key")
	encryptedObjectKey := autoDecodeString(objectKeyRaw)
	if encryptedObjectKey == "" {
		outcome.ErrorMsg = "object_key missing or invalid"
		return outcome
	}

	var finalAccessGrant string
	if accessGrant != "" {
		finalAccessGrant = accessGrant
	} else {
		projectID := extractProjectID(eventData)
		if projectID == "" {
			outcome.ErrorMsg = "missing project_id/user_id"
			return outcome
		}

		method := mapBucketNameToMethod(bucketName)
		if method == "" {
			outcome.ErrorMsg = fmt.Sprintf("unknown bucket name: %s", bucketName)
			return outcome
		}

		var err error
		finalAccessGrant, err = database.CronJobRepo.GetAccessGrantByProjectID(projectID, method)
		if err != nil {
			outcome.ErrorMsg = fmt.Sprintf("access grant not found for project_id: %s", projectID)
			return outcome
		}
	}

	decryptedKey, err := decryptObjectKey(finalAccessGrant, bucketName, encryptedObjectKey)
	if err != nil {
		outcome.ErrorMsg = fmt.Sprintf("decrypt failed: %v", err)
		return outcome
	}

	outcome.BucketName = bucketName
	outcome.DecryptedKey = decryptedKey
	outcome.DeleteCandidate = true
	return outcome
}

func extractEventData(event *TableChangeEvent) json.RawMessage {
	if event.Operation == "DELETE" && len(event.OldData) > 0 {
		var filteredData map[string]interface{}
		if err := json.Unmarshal(event.OldData, &filteredData); err == nil {
			essentialData := make(map[string]interface{})
			for _, key := range []string{"project_id", "user_id", "bucket_name", "object_key"} {
				if val, ok := filteredData[key]; ok && val != nil {
					essentialData[key] = val
				}
			}
			if len(essentialData) > 0 {
				data, _ := json.Marshal(essentialData)
				return data
			}
		}
		return nil
	}
	return event.Data
}

func parseWebhookPayloadEvents(plaintext []byte) ([]TableChangeEvent, error) {
	var events []TableChangeEvent
	if err := json.Unmarshal(plaintext, &events); err == nil {
		return events, nil
	}
	var event TableChangeEvent
	if err := json.Unmarshal(plaintext, &event); err == nil {
		return []TableChangeEvent{event}, nil
	}
	return nil, fmt.Errorf("payload must be a webhook event object or array")
}

func validateWebhookEvent(event *TableChangeEvent) error {
	if event.Operation == "" || event.Table == "" {
		return fmt.Errorf("invalid event structure")
	}
	if event.Operation != "INSERT" && event.Operation != "UPDATE" && event.Operation != "DELETE" {
		return fmt.Errorf("invalid operation type: must be INSERT, UPDATE, or DELETE")
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}
	return nil
}

func storeWebhookEvent(ctx context.Context, database *db.PostgresDb, event *TableChangeEvent, dataJSON json.RawMessage) error {
	webhookEvent, err := database.WebhookEventRepo.CreateWebhookEvent(
		event.Operation,
		event.Table,
		event.Timestamp,
		dataJSON,
	)
	if err != nil {
		return err
	}
	logger.Info(ctx, "Webhook event stored",
		logger.String("operation", event.Operation),
		logger.String("table", event.Table),
		logger.String("event_id", fmt.Sprintf("%d", webhookEvent.ID)),
	)
	return nil
}

func extractProjectID(eventData map[string]interface{}) string {
	projectIDRaw := getStringFromMap(eventData, "project_id")
	projectID := decodeUUIDFromHex(projectIDRaw)
	if projectID == "" {
		userIDRaw := getStringFromMap(eventData, "user_id")
		projectID = decodeUUIDFromHex(userIDRaw)
	}
	return projectID
}

func mapBucketNameToMethod(bucketName string) string {
	return bucketToMethod[bucketName]
}

func decryptObjectKey(accessGrant, bucketName, encryptedObjectKey string) (string, error) {
	grantAccess, err := grant.ParseAccess(accessGrant)
	if err != nil {
		return "", fmt.Errorf("failed to parse access grant: %w", err)
	}

	encStore := grantAccess.EncAccess.Store
	if encStore == nil {
		return "", fmt.Errorf("encryption store not found in access grant")
	}

	unencryptedPath := paths.NewUnencrypted("")
	pi, err := encryption.GetPrefixInfo(bucketName, unencryptedPath, encStore)
	if err != nil {
		return "", fmt.Errorf("failed to get prefix info: %w", err)
	}

	decryptedKey, err := encryption.DecryptPathRaw(encryptedObjectKey, pi.Cipher, &pi.ParentKey)
	if err != nil {
		return "", fmt.Errorf("failed to decrypt object key: %w", err)
	}

	return decryptedKey, nil
}

func getStringFromMap(data map[string]interface{}, key string) string {
	val, ok := data[key]
	if !ok {
		return ""
	}

	switch v := val.(type) {
	case string:
		return v
	case []byte:
		return string(v)
	default:
		return fmt.Sprintf("%v", v)
	}
}

func autoDecodeString(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}

	if strings.HasPrefix(s, "\\x") || strings.HasPrefix(s, "\\X") {
		hexStr := strings.TrimPrefix(strings.TrimPrefix(s, "\\x"), "\\X")
		if decoded, err := hex.DecodeString(hexStr); err == nil {
			return string(decoded)
		}
	}

	if decoded, err := base64.StdEncoding.DecodeString(s); err == nil {
		if base64.StdEncoding.EncodeToString(decoded) == s {
			return string(decoded)
		}
	}
	if decoded, err := base64.URLEncoding.DecodeString(s); err == nil {
		if base64.URLEncoding.EncodeToString(decoded) == s {
			return string(decoded)
		}
	}

	return s
}

func decodeHexString(str string) string {
	if str == "" {
		return ""
	}

	if !strings.HasPrefix(str, "\\x") && !strings.HasPrefix(str, "\\X") {
		return str
	}

	hexStr := strings.TrimPrefix(strings.TrimPrefix(str, "\\x"), "\\X")
	decoded, err := hex.DecodeString(hexStr)
	if err != nil {
		return str
	}

	return string(decoded)
}

func decodeUUIDFromHex(uuidStr string) string {
	if uuidStr == "" {
		return ""
	}

	if strings.Contains(uuidStr, "-") && len(uuidStr) == uuidStringLength {
		return uuidStr
	}

	var hexStr string
	if strings.HasPrefix(uuidStr, "\\x") || strings.HasPrefix(uuidStr, "\\X") {
		hexStr = strings.TrimPrefix(strings.TrimPrefix(uuidStr, "\\x"), "\\X")
	} else if len(uuidStr) == 32 {
		hexStr = uuidStr
	} else {
		return uuidStr
	}

	decoded, err := hex.DecodeString(hexStr)
	if err != nil || len(decoded) != uuidByteLength {
		return uuidStr
	}

	return fmt.Sprintf("%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		decoded[0], decoded[1], decoded[2], decoded[3],
		decoded[4], decoded[5],
		decoded[6], decoded[7],
		decoded[8], decoded[9],
		decoded[10], decoded[11], decoded[12], decoded[13], decoded[14], decoded[15])
}

func sanitizeErrorMessage(msg string) string {
	return strings.ToValidUTF8(msg, "?")
}

func respondWebhookSuccess(c echo.Context, message string) error {
	return c.JSON(http.StatusOK, WebhookResponse{
		Status:  "success",
		Message: message,
	})
}

func respondWebhookError(c echo.Context, statusCode int, message string) error {
	return c.JSON(statusCode, WebhookResponse{
		Status:  "error",
		Message: message,
	})
}

func runWebhookRetentionCleanup(database *db.PostgresDb) (int64, error) {
	cutoff := time.Now().Add(-webhookProcessedRetention)
	var total int64
	for {
		deleted, err := database.WebhookEventRepo.DeleteEventsByStatusOlderThan("processed", cutoff, webhookCleanupDeleteBatchSize)
		if err != nil {
			return total, err
		}
		if deleted == 0 {
			break
		}
		total += deleted
		if deleted < int64(webhookCleanupDeleteBatchSize) {
			break
		}
	}
	return total, nil
}

func runSyncedObjectHardPurge(database *db.PostgresDb) (int64, error) {
	cutoff := time.Now().Add(-syncedObjectSoftDeletedPurgeAge)
	var total int64
	for {
		deleted, err := database.SyncedObjectRepo.PermanentDeleteSyncedObjectsSoftDeletedBefore(cutoff, webhookCleanupDeleteBatchSize)
		if err != nil {
			return total, err
		}
		if deleted == 0 {
			break
		}
		total += deleted
		if deleted < int64(webhookCleanupDeleteBatchSize) {
			break
		}
	}
	return total, nil
}

func sleepUntilNextLocalMidnight(ctx context.Context) error {
	now := time.Now().In(time.Local)
	nextMidnight := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.Local)
	d := time.Until(nextMidnight)
	if d <= 0 {
		nextMidnight = nextMidnight.Add(24 * time.Hour)
		d = time.Until(nextMidnight)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func StartWebhookEventCleanupLoop(ctx context.Context, database *db.PostgresDb) {
	if database == nil {
		return
	}
	logger.Info(ctx, "Webhook event cleanup loop started",
		logger.String("schedule", "daily at 00:00 local"),
		logger.String("webhook_processed_permanent_delete_after", webhookProcessedRetention.String()),
		logger.String("synced_object_permanent_delete_after_soft_delete", syncedObjectSoftDeletedPurgeAge.String()),
		logger.Int("delete_batch_size", webhookCleanupDeleteBatchSize),
	)
	go func() {
		cleanup := func(runCtx context.Context) {
			var wg sync.WaitGroup
			var totalWebhookDeleted, totalSyncedPurged int64
			var webhookErr, syncedErr error
			wg.Add(2)
			go func() {
				defer wg.Done()
				n, err := runWebhookRetentionCleanup(database)
				totalWebhookDeleted = n
				webhookErr = err
			}()
			go func() {
				defer wg.Done()
				n, err := runSyncedObjectHardPurge(database)
				totalSyncedPurged = n
				syncedErr = err
			}()
			wg.Wait()
			if webhookErr != nil {
				logger.Warn(runCtx, "Webhook cleanup failed", logger.ErrorField(webhookErr))
			} else if totalWebhookDeleted > 0 {
				logger.Info(runCtx, "Webhook cleanup completed (processed rows removed permanently)",
					logger.Int("deleted_processed_events", int(totalWebhookDeleted)))
			}
			if syncedErr != nil {
				logger.Warn(runCtx, "Synced objects permanent purge failed", logger.ErrorField(syncedErr))
			} else if totalSyncedPurged > 0 {
				logger.Info(runCtx, "Synced objects permanently removed (soft-deleted before purge window)",
					logger.Int("count", int(totalSyncedPurged)))
			}
		}

		for {
			if err := sleepUntilNextLocalMidnight(ctx); err != nil {
				return
			}
			cleanup(ctx)
		}
	}()
}
