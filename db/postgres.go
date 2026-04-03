package db

import (
	"github.com/StorX2-0/Backup-Tools/pkg/gorm"
	"github.com/StorX2-0/Backup-Tools/repo"
)

type PostgresDb struct {
	*gorm.DB
	CronJobRepo        *repo.CronJobRepository
	TaskRepo           *repo.TaskRepository
	ScheduledTasksRepo *repo.ScheduledTasksRepository
	AuthRepo           *repo.AuthRepository
	SyncedObjectRepo   *repo.SyncedObjectRepository
	WebhookEventRepo   *repo.WebhookEventRepository
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
		TaskRepo:           repo.NewTaskRepository(db),
		ScheduledTasksRepo: repo.NewScheduledTasksRepository(db),
		AuthRepo:           repo.NewAuthRepository(db),
		SyncedObjectRepo:   repo.NewSyncedObjectRepository(db),
		WebhookEventRepo:   repo.NewWebhookEventRepository(db),
	}, nil
}

func (s *PostgresDb) Migrate() error {
	if err := s.DB.Migrate(
		&repo.GoogleAuthStorage{},
		&repo.ShopifyAuthStorage{},
		&repo.QuickbooksAuthStorage{},
		&repo.CronJobListingDB{},
		&repo.TaskListingDB{},
		&repo.ScheduledTasks{},
		&repo.SyncedObject{},
		&repo.WebhookEvent{},
	); err != nil {
		return err
	}

	return nil
}
