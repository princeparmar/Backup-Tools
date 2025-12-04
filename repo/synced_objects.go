package repo

import (
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
}
