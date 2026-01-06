package repo

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/StorX2-0/Backup-Tools/pkg/gorm"
)

type WebhookEvent struct {
	gorm.GormModel

	Operation   string          `json:"operation" gorm:"not null;type:varchar(50)"`                 // INSERT, UPDATE, DELETE
	Table       string          `json:"table" gorm:"not null;type:varchar(100)"`                    // objects, users, projects, etc.
	EventTime   time.Time       `json:"event_time" gorm:"not null"`                                 // Timestamp from webhook event
	Data        json.RawMessage `json:"data" gorm:"type:jsonb"`                                     // Event data (for DELETE: old_data, for INSERT/UPDATE: new data)
	Status      string          `json:"status" gorm:"not null;type:varchar(50);default:'received'"` // received, processed, failed
	ErrorMsg    string          `json:"error_msg" gorm:"type:text"`                                 // Error message if processing failed
	ProcessedAt *time.Time      `json:"processed_at" gorm:"default:null"`                           // When event was processed
}

type WebhookEventRepository struct {
	db *gorm.DB
}

func NewWebhookEventRepository(db *gorm.DB) *WebhookEventRepository {
	return &WebhookEventRepository{db: db}
}

func (r *WebhookEventRepository) CreateWebhookEvent(operation, table string, eventTime time.Time, data json.RawMessage) (*WebhookEvent, error) {
	event := WebhookEvent{
		Operation: operation,
		Table:     table,
		EventTime: eventTime,
		Data:      data,
		Status:    "received",
	}

	result := r.db.Create(&event)
	if result.Error != nil {
		return nil, fmt.Errorf("error creating webhook event: %v", result.Error)
	}

	return &event, nil
}

// UpdateEventStatus updates the status of a webhook event
func (r *WebhookEventRepository) UpdateEventStatus(eventID uint, status string, errorMsg string) error {
	updates := map[string]interface{}{
		"status": status,
	}

	if errorMsg != "" {
		updates["error_msg"] = errorMsg
	}

	if status == "processed" {
		now := time.Now()
		updates["processed_at"] = &now
	}

	result := r.db.Model(&WebhookEvent{}).Where("id = ?", eventID).Updates(updates)
	if result.Error != nil {
		return fmt.Errorf("error updating webhook event status: %v", result.Error)
	}

	return nil
}

// GetWebhookEvents retrieves webhook events with optional filters
func (r *WebhookEventRepository) GetWebhookEvents(limit int, offset int, table string, status string) ([]WebhookEvent, error) {
	var events []WebhookEvent
	query := r.db.Order("created_at DESC").Limit(limit).Offset(offset)

	if table != "" {
		query = query.Where("\"table\" = ?", table)
	}
	if status != "" {
		query = query.Where("status = ?", status)
	}

	result := query.Find(&events)
	if result.Error != nil {
		return nil, fmt.Errorf("error retrieving webhook events: %v", result.Error)
	}

	return events, nil
}

// GetWebhookEventByID retrieves a webhook event by ID
func (r *WebhookEventRepository) GetWebhookEventByID(eventID uint) (*WebhookEvent, error) {
	var event WebhookEvent
	result := r.db.First(&event, eventID)
	if result.Error != nil {
		return nil, fmt.Errorf("error retrieving webhook event: %v", result.Error)
	}

	return &event, nil
}
