// Package repo — account tombstone for Satellite-driven soft/hard account deletion.
package repo

import (
	"fmt"
	"strings"
	"time"

	"github.com/StorX2-0/Backup-Tools/pkg/gorm"
	gormio "gorm.io/gorm"
)

const (
	AccountTombstoneStatusPendingDelete = "pending_delete"
)

// AccountTombstoneDB marks a satellite user so backup/restore crons skip them.
// Soft-delete (DeletedAt) clears the tombstone on resume.
type AccountTombstoneDB struct {
	gorm.GormModel

	SatelliteUserID string     `json:"satellite_user_id" gorm:"column:satellite_user_id;uniqueIndex;not null"`
	Status          string     `json:"status" gorm:"column:status;not null;default:pending_delete"`
	DeleteAt        *time.Time `json:"delete_at,omitempty" gorm:"column:delete_at"`
}

func (AccountTombstoneDB) TableName() string { return "account_tombstone_dbs" }

// AccountLifecycleRepository manages pending-delete / resume / purge for a satellite user.
type AccountLifecycleRepository struct {
	db *gorm.DB
}

func NewAccountLifecycleRepository(db *gorm.DB) *AccountLifecycleRepository {
	return &AccountLifecycleRepository{db: db}
}

// IsPendingDelete reports whether the satellite user is tombstoned (pending deletion).
func (r *AccountLifecycleRepository) IsPendingDelete(satelliteUserID string) (bool, error) {
	satelliteUserID = strings.TrimSpace(satelliteUserID)
	if satelliteUserID == "" {
		return false, nil
	}
	var n int64
	err := r.db.Model(&AccountTombstoneDB{}).
		Where("satellite_user_id = ? AND status = ?", satelliteUserID, AccountTombstoneStatusPendingDelete).
		Count(&n).Error
	return n > 0, err
}

// PendingDelete upserts a tombstone and pauses backup/restore activity for the user.
// Tombstone + pause + restore-cancel run in one transaction so a mid-flight failure
// cannot leave jobs running without a tombstone (or the reverse).
func (r *AccountLifecycleRepository) PendingDelete(satelliteUserID string, deleteAt *time.Time) error {
	satelliteUserID = strings.TrimSpace(satelliteUserID)
	if satelliteUserID == "" {
		return fmt.Errorf("satellite_user_id is required")
	}

	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := upsertAccountTombstone(tx, satelliteUserID, deleteAt); err != nil {
			return err
		}
		msg := "Account scheduled for deletion. Automatic backup paused."
		if err := pauseAllJobsTx(tx, satelliteUserID, msg); err != nil {
			return err
		}
		return cancelActiveRestoreJobsTx(tx, satelliteUserID)
	})
}

func upsertAccountTombstone(tx *gorm.DB, satelliteUserID string, deleteAt *time.Time) error {
	var existing AccountTombstoneDB
	// Unscoped: resume soft-deletes the row; pending-delete again must resurrect it
	// (uniqueIndex on satellite_user_id still holds soft-deleted rows).
	err := tx.Unscoped().Where("satellite_user_id = ?", satelliteUserID).First(&existing).Error
	switch {
	case err == nil:
		updates := map[string]interface{}{
			"status":     AccountTombstoneStatusPendingDelete,
			"delete_at":  deleteAt,
			"deleted_at": nil,
			"updated_at": time.Now().UTC(),
		}
		if err := tx.Unscoped().Model(&existing).Updates(updates).Error; err != nil {
			return fmt.Errorf("update account tombstone: %w", err)
		}
		return nil
	case err == gormio.ErrRecordNotFound:
		row := AccountTombstoneDB{
			SatelliteUserID: satelliteUserID,
			Status:          AccountTombstoneStatusPendingDelete,
			DeleteAt:        deleteAt,
		}
		if err := tx.Create(&row).Error; err != nil {
			return fmt.Errorf("create account tombstone: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("lookup account tombstone: %w", err)
	}
}

// Resume clears the tombstone. Jobs stay paused; the user re-enables them in the UI.
func (r *AccountLifecycleRepository) Resume(satelliteUserID string) error {
	satelliteUserID = strings.TrimSpace(satelliteUserID)
	if satelliteUserID == "" {
		return fmt.Errorf("satellite_user_id is required")
	}
	// Soft-delete tombstone rows so IsPendingDelete returns false.
	return r.db.Where("satellite_user_id = ?", satelliteUserID).Delete(&AccountTombstoneDB{}).Error
}

// Purge hard-wipes Backup-Tools data for the satellite user (idempotent).
func (r *AccountLifecycleRepository) Purge(satelliteUserID string) error {
	satelliteUserID = strings.TrimSpace(satelliteUserID)
	if satelliteUserID == "" {
		return fmt.Errorf("satellite_user_id is required")
	}

	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := cancelActiveRestoreJobsTx(tx, satelliteUserID); err != nil {
			return err
		}

		var restoreJobIDs []uint
		if err := tx.Model(&RestoreJobListingDB{}).
			Where("user_id = ?", satelliteUserID).
			Pluck("id", &restoreJobIDs).Error; err != nil {
			return fmt.Errorf("list restore jobs: %w", err)
		}
		if len(restoreJobIDs) > 0 {
			if err := tx.Unscoped().Where("restore_job_id IN ?", restoreJobIDs).Delete(&RestoreTaskListingDB{}).Error; err != nil {
				return fmt.Errorf("delete restore tasks: %w", err)
			}
			if err := tx.Unscoped().Where("restore_job_id IN ?", restoreJobIDs).Delete(&RestoreDeadItemDB{}).Error; err != nil {
				return fmt.Errorf("delete restore dead items: %w", err)
			}
		}
		if err := tx.Unscoped().Where("user_id = ?", satelliteUserID).Delete(&RestoreJobListingDB{}).Error; err != nil {
			return fmt.Errorf("delete restore jobs: %w", err)
		}

		var cronJobIDs []uint
		if err := tx.Unscoped().Model(&CronJobListingDB{}).
			Where("user_id = ?", satelliteUserID).
			Pluck("id", &cronJobIDs).Error; err != nil {
			return fmt.Errorf("list cron jobs: %w", err)
		}
		if len(cronJobIDs) > 0 {
			if err := tx.Unscoped().Where("cron_job_id IN ?", cronJobIDs).Delete(&TaskListingDB{}).Error; err != nil {
				return fmt.Errorf("delete tasks: %w", err)
			}
		}
		if err := tx.Unscoped().Where("user_id = ?", satelliteUserID).Delete(&CronJobListingDB{}).Error; err != nil {
			return fmt.Errorf("delete cron jobs: %w", err)
		}
		if err := tx.Unscoped().Where("user_id = ?", satelliteUserID).Delete(&AutosyncBackupPolicyDB{}).Error; err != nil {
			return fmt.Errorf("delete policies: %w", err)
		}
		if err := tx.Unscoped().Where("user_id = ?", satelliteUserID).Delete(&GoogleBackupCredentialDB{}).Error; err != nil {
			return fmt.Errorf("delete credentials: %w", err)
		}
		// Synced objects can be large; still delete in-TX for consistency with purge semantics.
		if err := tx.Unscoped().Where("user_id = ?", satelliteUserID).Delete(&SyncedObject{}).Error; err != nil {
			return fmt.Errorf("delete synced objects: %w", err)
		}
		if err := tx.Unscoped().Where("user_id = ?", satelliteUserID).Delete(&ScheduledTasks{}).Error; err != nil {
			return fmt.Errorf("delete scheduled tasks: %w", err)
		}
		if err := tx.Unscoped().Where("satellite_user_id = ?", satelliteUserID).Delete(&AccountTombstoneDB{}).Error; err != nil {
			return fmt.Errorf("delete tombstone: %w", err)
		}
		return nil
	})
}

func pauseAllJobsTx(tx *gorm.DB, userID, message string) error {
	updates := map[string]interface{}{
		"active":           false,
		"auto_deactivated": true,
		"message":          message,
		"message_status":   JobMessageStatusError,
		"status":           JobStatusCancelled,
	}
	// Keep terminal success rows as success; cancel everything else while pausing.
	if err := tx.Model(&CronJobListingDB{}).
		Where("user_id = ? AND COALESCE(placeholder, false) = ?", userID, false).
		Where("status NOT IN ?", []string{JobStatusSuccess, JobStatusCancelled}).
		Updates(updates).Error; err != nil {
		return fmt.Errorf("pause cron jobs: %w", err)
	}
	// Already-success jobs still need active=false so they are not claimed again.
	if err := tx.Model(&CronJobListingDB{}).
		Where("user_id = ? AND COALESCE(placeholder, false) = ?", userID, false).
		Where("status = ?", JobStatusSuccess).
		Updates(map[string]interface{}{
			"active":           false,
			"auto_deactivated": true,
			"message":          message,
			"message_status":   JobMessageStatusError,
		}).Error; err != nil {
		return fmt.Errorf("pause successful cron jobs: %w", err)
	}
	return nil
}

func cancelActiveRestoreJobsTx(tx *gorm.DB, userID string) error {
	now := time.Now().UTC()
	return tx.Model(&RestoreJobListingDB{}).
		Where("user_id = ? AND status IN ?", userID, []string{
			RestoreJobStatusQueued, RestoreJobStatusRunning,
		}).
		Updates(map[string]interface{}{
			"status":       RestoreJobStatusCancelled,
			"cancelled_at": now,
		}).Error
}
