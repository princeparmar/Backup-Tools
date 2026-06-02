package db

import (
	"github.com/StorX2-0/Backup-Tools/pkg/gorm"
	"github.com/StorX2-0/Backup-Tools/repo"
)

type PostgresDb struct {
	*gorm.DB
	CronJobRepo        *repo.CronJobRepository
	CredentialRepo     *repo.GoogleBackupCredentialRepository
	PolicyRepo         *repo.AutosyncBackupPolicyRepository
	TaskRepo           *repo.TaskRepository
	ScheduledTasksRepo *repo.ScheduledTasksRepository
	AuthRepo           *repo.AuthRepository
	SyncedObjectRepo   *repo.SyncedObjectRepository
	WebhookEventRepo   *repo.WebhookEventRepository
	RestoreJobRepo  *repo.RestoreJobRepository
	RestoreTaskRepo *repo.RestoreTaskRepository
}

func NewPostgresStore(dsn string, queryLogging bool) (*PostgresDb, error) {
	config := gorm.PostgresConfig(dsn, queryLogging)
	db, err := gorm.NewDatabase(config)
	if err != nil {
		return nil, err
	}

	return &PostgresDb{
		DB:                 db,
		CronJobRepo:        repo.NewCronJobRepository(db),
		CredentialRepo:     repo.NewGoogleBackupCredentialRepository(db),
		PolicyRepo:         repo.NewAutosyncBackupPolicyRepository(db),
		TaskRepo:           repo.NewTaskRepository(db),
		ScheduledTasksRepo: repo.NewScheduledTasksRepository(db),
		AuthRepo:           repo.NewAuthRepository(db),
		SyncedObjectRepo:   repo.NewSyncedObjectRepository(db),
		WebhookEventRepo:    repo.NewWebhookEventRepository(db),
		RestoreJobRepo:  repo.NewRestoreJobRepository(db),
		RestoreTaskRepo: repo.NewRestoreTaskRepository(db),
	}, nil
}

func (s *PostgresDb) Migrate() error {
	if err := s.DB.Migrate(
		&repo.GoogleAuthStorage{},
		&repo.ShopifyAuthStorage{},
		&repo.QuickbooksAuthStorage{},
		&repo.GoogleBackupCredentialDB{},
		&repo.CronJobListingDB{},
		&repo.AutosyncBackupPolicyDB{},
		&repo.TaskListingDB{},
		&repo.ScheduledTasks{},
		&repo.SyncedObject{},
		&repo.WebhookEvent{},
		&repo.RestoreJobListingDB{},
		&repo.RestoreTaskListingDB{},
		&repo.RestoreDeadItemDB{},
	); err != nil {
		return err
	}

	if err := s.ensureRestoreIndexes(); err != nil {
		return err
	}

	if err := s.ensureAutosyncPolicySchema(); err != nil {
		return err
	}

	if err := s.PolicyRepo.BackfillFromJobs(); err != nil {
		return err
	}

	if err := s.DB.Exec(`ALTER TABLE cron_job_listing_dbs DROP COLUMN IF EXISTS "on"`).Error; err != nil {
		return err
	}

	return nil
}

func (s *PostgresDb) ensureAutosyncPolicySchema() error {
	stmts := []string{
		`ALTER TABLE autosync_backup_policy_dbs ADD COLUMN IF NOT EXISTS user_id TEXT`,
		`ALTER TABLE autosync_backup_policy_dbs ADD COLUMN IF NOT EXISTS retention_type TEXT`,
		`ALTER TABLE autosync_backup_policy_dbs ADD COLUMN IF NOT EXISTS expires_at TIMESTAMPTZ`,
		`ALTER TABLE cron_job_listing_dbs ADD COLUMN IF NOT EXISTS policy_id BIGINT`,
		`CREATE INDEX IF NOT EXISTS idx_cron_job_listing_policy_id ON cron_job_listing_dbs(policy_id)`,
		`UPDATE autosync_backup_policy_dbs SET retention_type = 'never' WHERE retention_type IS NULL OR retention_type = ''`,
		`ALTER TABLE autosync_backup_policy_dbs ALTER COLUMN retention_type SET DEFAULT 'never'`,
		`DROP INDEX IF EXISTS idx_autosync_backup_policy_dbs_job_id`,
		`ALTER TABLE autosync_backup_policy_dbs DROP COLUMN IF EXISTS job_id`,
		`ALTER TABLE autosync_backup_policy_dbs DROP COLUMN IF EXISTS retention_days`,
		`ALTER TABLE autosync_backup_policy_dbs ADD COLUMN IF NOT EXISTS is_expired BOOLEAN NOT NULL DEFAULT false`,
		`UPDATE autosync_backup_policy_dbs SET is_expired = true WHERE deleted_at IS NULL AND expires_at IS NOT NULL AND expires_at <= NOW()`,
		`DROP INDEX IF EXISTS idx_autosync_policy_fingerprint`,
		`DROP INDEX IF EXISTS idx_autosync_policy_fingerprint_active`,
	}
	for _, stmt := range stmts {
		if err := s.DB.Exec(stmt).Error; err != nil {
			return err
		}
	}
	return nil
}

func (s *PostgresDb) ensureRestoreIndexes() error {
	stmts := []string{
		`CREATE INDEX IF NOT EXISTS idx_synced_objects_restore ON synced_objects (user_id, bucket_name, id)`,
		`CREATE INDEX IF NOT EXISTS idx_synced_objects_key_lookup ON synced_objects (user_id, bucket_name, object_key)`,
	}
	for _, stmt := range stmts {
		if err := s.DB.Exec(stmt).Error; err != nil {
			return err
		}
	}
	return nil
}
