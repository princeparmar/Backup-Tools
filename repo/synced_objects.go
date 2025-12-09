package repo

import (
	"fmt"
	"time"

	"github.com/StorX2-0/Backup-Tools/pkg/gorm"
)

// SyncedObject represents a synced object in the database
type SyncedObject struct {
	gorm.GormModel

	UserID     string    `json:"user_id" gorm:"not null;uniqueIndex:idx_synced_objects_unique"`
	BucketName string    `json:"bucket_name" gorm:"not null;uniqueIndex:idx_synced_objects_unique"`
	ObjectKey  string    `json:"object_key" gorm:"not null;type:varchar(1000);uniqueIndex:idx_synced_objects_unique"`
	SyncedAt   time.Time `json:"synced_at" gorm:"default:now()"`
	Source     string    `json:"source" gorm:"not null;type:varchar(1000)"`
	Type       string    `json:"type" gorm:"not null;type:varchar(1000)"`
}

// SyncedObjectRepository handles all database operations for synced objects
type SyncedObjectRepository struct {
	db *gorm.DB
}

// NewSyncedObjectRepository creates a new synced object repository
func NewSyncedObjectRepository(db *gorm.DB) *SyncedObjectRepository {
	return &SyncedObjectRepository{db: db}
}

// CreateSyncedObject creates or updates a synced object in the database
func (r *SyncedObjectRepository) CreateSyncedObject(userID, bucketName, objectKey, source, objectType string) error {
	syncedObject := SyncedObject{
		UserID:     userID,
		BucketName: bucketName,
		ObjectKey:  objectKey,
		Source:     source,
		Type:       objectType,
		SyncedAt:   time.Now(),
	}

	result := r.db.Where("user_id = ? AND bucket_name = ? AND object_key = ?",
		userID, bucketName, objectKey).
		FirstOrCreate(&syncedObject)

	if result.Error != nil {
		return fmt.Errorf("error creating synced object: %v", result.Error)
	}

	return nil
}

// GetSyncedObjectByBucketAndKey retrieves a synced object by bucket_name and object_key
func (r *SyncedObjectRepository) GetSyncedObjectByBucketAndKey(bucketName, objectKey string) (*SyncedObject, error) {
	var syncedObject SyncedObject
	result := r.db.Where("bucket_name = ? AND object_key = ?", bucketName, objectKey).First(&syncedObject)

	if result.Error != nil {
		return nil, fmt.Errorf("error getting synced object: %v", result.Error)
	}

	return &syncedObject, nil
}

// DeleteSyncedObject deletes a synced object from the database
func (r *SyncedObjectRepository) DeleteSyncedObject(bucketName, objectKey string) error {
	result := r.db.Where("bucket_name = ? AND object_key = ?",
		bucketName, objectKey).
		Delete(&SyncedObject{})

	if result != nil && result.Error != nil {
		return fmt.Errorf("error deleting synced object: %v", result.Error)
	}

	return nil
}
