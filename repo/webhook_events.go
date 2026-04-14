package repo

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/StorX2-0/Backup-Tools/pkg/gorm"
)

type WebhookEvent struct {
	gorm.GormModel

	Operation   string          `json:"operation" gorm:"not null;type:varchar(50)"`
	Table       string          `json:"table" gorm:"not null;type:varchar(100)"`
	EventTime   time.Time       `json:"event_time" gorm:"not null"`
	Data        json.RawMessage `json:"data" gorm:"type:jsonb"`
	Status      string          `json:"status" gorm:"not null;type:varchar(50);default:'received';index"`
	ErrorMsg    string          `json:"error_msg" gorm:"type:text"`
	RetryCount  uint            `json:"retry_count" gorm:"not null;default:0"`
	ProcessedAt *time.Time      `json:"processed_at" gorm:"default:null"`
}

type WebhookEventRepository struct {
	db *gorm.DB
}

type WebhookEventStatusUpdate struct {
	EventID    uint
	Status     string
	ErrorMsg   string
	RetryCount *uint
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
		updates["error_msg"] = ""
		updates["retry_count"] = uint(0)
	}

	result := r.db.Model(&WebhookEvent{}).Where("id = ?", eventID).Updates(updates)
	if result.Error != nil {
		return fmt.Errorf("error updating webhook event status: %v", result.Error)
	}

	return nil
}

func (r *WebhookEventRepository) GetWebhookEvents(limit int, offset int, table string, status string) ([]WebhookEvent, error) {
	var events []WebhookEvent
	query := r.db.Order("created_at ASC").Limit(limit).Offset(offset)

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

func (r *WebhookEventRepository) UpdateEventStatusesBatch(updates []WebhookEventStatusUpdate) error {
	if len(updates) == 0 {
		return nil
	}
	tx := r.db.Begin()
	if tx.Error != nil {
		return fmt.Errorf("error starting transaction for webhook status batch update: %v", tx.Error)
	}
	committed := false
	defer func() {
		if !committed {
			tx.Rollback()
		}
	}()
	now := time.Now()
	for _, u := range updates {
		updateMap := map[string]interface{}{"status": u.Status}
		if u.Status == "processed" {
			updateMap["processed_at"] = &now
			updateMap["error_msg"] = ""
			updateMap["retry_count"] = uint(0)
		} else {
			if u.ErrorMsg != "" {
				updateMap["error_msg"] = u.ErrorMsg
			}
			if u.RetryCount != nil {
				updateMap["retry_count"] = *u.RetryCount
			}
		}
		if err := tx.Model(&WebhookEvent{}).Where("id = ?", u.EventID).Updates(updateMap).Error; err != nil {
			return fmt.Errorf("error batch updating webhook event status for id %d: %v", u.EventID, err)
		}
	}
	if err := tx.Commit().Error; err != nil {
		return fmt.Errorf("error committing webhook status batch update: %v", err)
	}
	committed = true
	return nil
}

func (r *WebhookEventRepository) GetWebhookEventByID(eventID uint) (*WebhookEvent, error) {
	var event WebhookEvent
	result := r.db.First(&event, eventID)
	if result.Error != nil {
		return nil, fmt.Errorf("error retrieving webhook event: %v", result.Error)
	}

	return &event, nil
}

func (r *WebhookEventRepository) DeleteEventsByStatusOlderThan(status string, before time.Time, limit int) (int64, error) {
	if status == "" || limit <= 0 {
		return 0, nil
	}
	sub := r.db.Model(&WebhookEvent{}).
		Select("id").
		Where("status = ? AND created_at < ?", status, before).
		Limit(limit)

	result := r.db.Unscoped().Where("id IN (?)", sub).Delete(&WebhookEvent{})
	if result.Error != nil {
		return 0, fmt.Errorf("error deleting old webhook events: %v", result.Error)
	}
	return result.RowsAffected, nil
}
