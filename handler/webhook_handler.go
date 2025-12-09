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
	maxRequestBodySize = 5 << 20 // 5 MB
	requiredRSAKeySize = 256     // 2048 bits minimum
	uuidStringLength   = 36
	uuidByteLength     = 16
	oaepLabel          = "storx-webhook" // OAEP label for domain separation
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

// TableChangeEvent represents a database change event from StorXMonitor
type TableChangeEvent struct {
	Operation string          `json:"operation"` // "INSERT", "UPDATE", "DELETE"
	Table     string          `json:"table"`     // "objects", "users", "projects", etc.
	Timestamp time.Time       `json:"timestamp"`
	Data      json.RawMessage `json:"data,omitempty"`     // New data (INSERT/UPDATE)
	OldData   json.RawMessage `json:"old_data,omitempty"` // Old data (UPDATE/DELETE)
}

// WebhookResponse represents the response sent to webhook caller
type WebhookResponse struct {
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

// WebhookDecryptor handles RSA decryption of webhook payloads
type WebhookDecryptor struct {
	privateKey *rsa.PrivateKey
}

// NewWebhookDecryptor creates a new decryptor from a private key file
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

// DecryptPayload decrypts a hybrid-encrypted payload (RSA + AES-GCM)
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

// HandleWebhook handles incoming webhook requests from StorXMonitor
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

	var event TableChangeEvent
	if err := json.Unmarshal(plaintext, &event); err != nil {
		logger.Error(ctx, "failed to decode JSON", logger.ErrorField(err))
		return respondWebhookError(c, http.StatusBadRequest, "invalid JSON payload")
	}

	if event.Operation == "" || event.Table == "" {
		logger.Error(ctx, "invalid event structure",
			logger.String("operation", event.Operation),
			logger.String("table", event.Table),
		)
		return respondWebhookError(c, http.StatusBadRequest, "invalid event structure")
	}

	if event.Operation != "INSERT" && event.Operation != "UPDATE" && event.Operation != "DELETE" {
		logger.Error(ctx, "invalid operation type", logger.String("operation", event.Operation))
		return respondWebhookError(c, http.StatusBadRequest, "invalid operation type: must be INSERT, UPDATE, or DELETE")
	}

	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}

	dataJSON := extractEventData(&event)
	if err := storeWebhookEvent(ctx, database, &event, dataJSON); err != nil {
		logger.Error(ctx, "failed to store webhook event",
			logger.String("operation", event.Operation),
			logger.String("table", event.Table),
			logger.ErrorField(err),
		)
	}

	if eventJSON, err := json.MarshalIndent(event, "", "  "); err == nil {
		logger.Info(ctx, "Webhook event received",
			logger.String("operation", event.Operation),
			logger.String("table", event.Table),
			logger.String("timestamp", event.Timestamp.String()),
			logger.String("event_data", string(eventJSON)),
		)
	} else {
		logger.Warn(ctx, "failed to marshal event for logging",
			logger.String("operation", event.Operation),
			logger.String("table", event.Table),
			logger.ErrorField(err),
		)
	}

	return respondWebhookSuccess(c, "event received successfully")
}

// ProcessWebhookEvents processes pending webhook events from the database
func ProcessWebhookEvents(ctx context.Context, database *db.PostgresDb, accessGrant string, limit int) error {
	events, err := database.WebhookEventRepo.GetWebhookEvents(limit, 0, "objects", "received")
	if err != nil {
		return fmt.Errorf("failed to get webhook events: %w", err)
	}

	if len(events) == 0 {
		return nil // No events to process
	}

	if accessGrant != "" {
		logger.Info(ctx, "Processing webhook events with provided access grant",
			logger.String("count", fmt.Sprintf("%d", len(events))))
	} else {
		logger.Info(ctx, "Processing webhook events (will get access grant from database)",
			logger.String("count", fmt.Sprintf("%d", len(events))))
	}

	for _, event := range events {
		if err := processSingleWebhookEvent(ctx, database, &event, accessGrant); err != nil {
			logger.Error(ctx, "Failed to process webhook event",
				logger.String("event_id", fmt.Sprintf("%d", event.ID)),
				logger.String("operation", event.Operation),
				logger.ErrorField(err),
			)
			sanitizedErr := sanitizeErrorMessage(err.Error())
			_ = database.WebhookEventRepo.UpdateEventStatus(event.ID, "failed", sanitizedErr)
		}
	}

	return nil
}

// processSingleWebhookEvent processes a single webhook event
func processSingleWebhookEvent(ctx context.Context, database *db.PostgresDb, event *repo.WebhookEvent, accessGrant string) error {
	if event.Operation != "DELETE" || event.Table != "objects" {
		logger.Info(ctx, "Skipping event (not DELETE operation on objects table)",
			logger.String("event_id", fmt.Sprintf("%d", event.ID)),
			logger.String("operation", event.Operation),
			logger.String("table", event.Table),
		)
		return database.WebhookEventRepo.UpdateEventStatus(event.ID, "processed", "")
	}

	var eventData map[string]interface{}
	if err := json.Unmarshal(event.Data, &eventData); err != nil {
		return fmt.Errorf("failed to parse event data: %w", err)
	}

	bucketRaw := getStringFromMap(eventData, "bucket_name")
	bucketName := autoDecodeString(bucketRaw)
	if bucketName == "" {
		_ = database.WebhookEventRepo.UpdateEventStatus(event.ID, "processed", "bucket_name missing or invalid")
		return nil
	}

	objectKeyRaw := getStringFromMap(eventData, "object_key")
	encryptedObjectKey := autoDecodeString(objectKeyRaw)
	if encryptedObjectKey == "" {
		_ = database.WebhookEventRepo.UpdateEventStatus(event.ID, "processed", "object_key missing or invalid")
		return nil
	}

	var finalAccessGrant string
	if accessGrant != "" {
		finalAccessGrant = accessGrant
	} else {
		projectID := extractProjectID(eventData)
		if projectID == "" {
			_ = database.WebhookEventRepo.UpdateEventStatus(event.ID, "processed", "missing project_id/user_id")
			return nil
		}

		method := mapBucketNameToMethod(bucketName)
		if method == "" {
			_ = database.WebhookEventRepo.UpdateEventStatus(event.ID, "processed", fmt.Sprintf("unknown bucket name: %s", bucketName))
			return nil
		}

		var err error
		finalAccessGrant, err = database.CronJobRepo.GetAccessGrantByProjectID(projectID, method)
		if err != nil {
			errorMsg := sanitizeErrorMessage(fmt.Sprintf("access grant not found for project_id: %s", projectID))
			_ = database.WebhookEventRepo.UpdateEventStatus(event.ID, "processed", errorMsg)
			return nil
		}
	}

	decryptedKey, err := decryptObjectKey(finalAccessGrant, bucketName, encryptedObjectKey)
	if err != nil {
		errorMsg := sanitizeErrorMessage(fmt.Sprintf("decrypt failed: %v", err))
		_ = database.WebhookEventRepo.UpdateEventStatus(event.ID, "processed", errorMsg)
		return nil
	}

	_, err = database.SyncedObjectRepo.GetSyncedObjectByBucketAndKey(bucketName, decryptedKey)
	if err != nil {
		_ = database.WebhookEventRepo.UpdateEventStatus(event.ID, "processed", "decrypted object_key not found in synced_objects")
		return nil
	}

	if err := database.SyncedObjectRepo.DeleteSyncedObject(bucketName, decryptedKey); err != nil {
		return fmt.Errorf("failed to delete synced object: %w", err)
	}

	return database.WebhookEventRepo.UpdateEventStatus(event.ID, "processed", "")
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
