package db

import (
	"github.com/StorX2-0/Backup-Tools/pkg/gorm"
	"github.com/StorX2-0/Backup-Tools/repo"
)

type PostgresDb struct {
	*gorm.DB
	CronJobRepo           *repo.CronJobRepository
	CredentialRepo        *repo.GoogleBackupCredentialRepository
	PolicyRepo            *repo.AutosyncBackupPolicyRepository
	TaskRepo              *repo.TaskRepository
	ScheduledTasksRepo    *repo.ScheduledTasksRepository
	AuthRepo              *repo.AuthRepository
	SyncedObjectRepo      *repo.SyncedObjectRepository
	WebhookEventRepo      *repo.WebhookEventRepository
	RestoreJobRepo        *repo.RestoreJobRepository
	RestoreTaskRepo       *repo.RestoreTaskRepository
	BackupRestoreLogsRepo *repo.BackupRestoreLogsRepository
	AccountLifecycleRepo  *repo.AccountLifecycleRepository
	MicrosoftTenantRepo   *repo.MicrosoftTenantRepository
	MicrosoftLinkRepo     *repo.MicrosoftAccountTenantRepository
	MicrosoftResourceRepo *repo.MicrosoftResourceRepository
	MicrosoftScopeRepo    *repo.MicrosoftBackupScopeRepository
}

func NewPostgresStore(dsn string, queryLogging bool) (*PostgresDb, error) {
	config := gorm.PostgresConfig(dsn, queryLogging)
	db, err := gorm.NewDatabase(config)
	if err != nil {
		return nil, err
	}

	return &PostgresDb{
		DB:                    db,
		CronJobRepo:           repo.NewCronJobRepository(db),
		CredentialRepo:        repo.NewGoogleBackupCredentialRepository(db),
		PolicyRepo:            repo.NewAutosyncBackupPolicyRepository(db),
		TaskRepo:              repo.NewTaskRepository(db),
		ScheduledTasksRepo:    repo.NewScheduledTasksRepository(db),
		AuthRepo:              repo.NewAuthRepository(db),
		SyncedObjectRepo:      repo.NewSyncedObjectRepository(db),
		WebhookEventRepo:      repo.NewWebhookEventRepository(db),
		RestoreJobRepo:        repo.NewRestoreJobRepository(db),
		RestoreTaskRepo:       repo.NewRestoreTaskRepository(db),
		BackupRestoreLogsRepo: repo.NewBackupRestoreLogsRepository(db),
		AccountLifecycleRepo:  repo.NewAccountLifecycleRepository(db),
		MicrosoftTenantRepo:   repo.NewMicrosoftTenantRepository(db),
		MicrosoftLinkRepo:     repo.NewMicrosoftAccountTenantRepository(db),
		MicrosoftResourceRepo: repo.NewMicrosoftResourceRepository(db),
		MicrosoftScopeRepo:    repo.NewMicrosoftBackupScopeRepository(db),
	}, nil
}

func (s *PostgresDb) Migrate() error {
	if err := repo.MigrateBackupIdentity(s.DB.DB); err != nil {
		return err
	}
	if err := s.DB.Migrate(
		&repo.GoogleAuthStorage{},
		&repo.MicrosoftAuthStorage{},
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
		&repo.AccountTombstoneDB{},
		&repo.MicrosoftTenantDB{},
		&repo.MicrosoftAccountTenantDB{},
		&repo.MicrosoftResourceDB{},
		&repo.MicrosoftBackupScopeDB{},
	); err != nil {
		return err
	}

	// Auth mode moved to microsoft_account_tenants; customer-provided app secrets are not supported.
	if err := s.DB.Exec(`ALTER TABLE google_backup_credential_dbs
		DROP COLUMN IF EXISTS microsoft_auth_mode,
		DROP COLUMN IF EXISTS microsoft_app_client_id,
		DROP COLUMN IF EXISTS microsoft_app_client_secret`).Error; err != nil {
		return err
	}
	if err := s.DB.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_ms_account_tenant_object ON microsoft_account_tenants (tenant_id, object_id, credential_id) WHERE object_id IS NOT NULL`).Error; err != nil {
		return err
	}

	if err := s.DB.Exec(`ALTER TABLE cron_job_listing_dbs DROP COLUMN IF EXISTS "on"`).Error; err != nil {
		return err
	}

	if err := s.DB.Exec(`CREATE INDEX IF NOT EXISTS idx_synced_objects_user_id ON synced_objects (user_id) WHERE deleted_at IS NULL`).Error; err != nil {
		return err
	}
	if err := s.DB.Exec(`CREATE INDEX IF NOT EXISTS idx_synced_objects_dashboard_count ON synced_objects (user_id) WHERE deleted_at IS NULL AND object_key NOT LIKE '%/.file_placeholder'`).Error; err != nil {
		return err
	}
	if err := s.DB.Exec(`CREATE INDEX IF NOT EXISTS idx_cron_jobs_user_placeholder ON cron_job_listing_dbs (user_id) WHERE deleted_at IS NULL AND COALESCE(placeholder, false) = false`).Error; err != nil {
		return err
	}

	return nil
}
