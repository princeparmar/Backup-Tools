package db

import (
	"github.com/StorX2-0/Backup-Tools/pkg/gorm"
	"github.com/StorX2-0/Backup-Tools/repo"
)

type PosgresDb struct {
	*gorm.DB
	// Repository instances
	CronJobRepo        *repo.CronJobRepository
	TaskRepo           *repo.TaskRepository
	ScheduledTasksRepo *repo.ScheduledTasksRepository
	AuthRepo           *repo.AuthRepository
}

// NewPostgresStore creates a new PostgreSQL store instance
func NewPostgresStore(dsn string, queryLogging bool) (*PosgresDb, error) {
	config := gorm.PostgresConfig(dsn, queryLogging)
	db, err := gorm.NewDatabase(config)
	if err != nil {
		return nil, err
	}

	return &PosgresDb{
		DB:                 db,
		CronJobRepo:        repo.NewCronJobRepository(db),
		TaskRepo:           repo.NewTaskRepository(db),
		ScheduledTasksRepo: repo.NewScheduledTasksRepository(db),
		AuthRepo:           repo.NewAuthRepository(db),
	}, nil
}

// Migrate runs database migrations
func (s *PosgresDb) Migrate() error {
	return s.DB.Migrate(
		&repo.GoogleAuthStorage{},
		&repo.ShopifyAuthStorage{},
		&repo.QuickbooksAuthStorage{},
		&repo.CronJobListingDB{},
		&repo.TaskListingDB{},
		&repo.ScheduledTasks{},
	)
}
