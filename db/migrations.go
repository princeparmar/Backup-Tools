package db

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/StorX2-0/Backup-Tools/pkg/gorm"
)

type Migration struct {
	Version   int
	Name      string
	UpFile    string
	DownFile  string
	IsApplied bool
}

type MigrationRunner struct {
	db *gorm.DB
}

func NewMigrationRunner(db *gorm.DB) *MigrationRunner {
	return &MigrationRunner{db: db}
}

func (mr *MigrationRunner) RunMigrations() error {
	if err := mr.createMigrationsTable(); err != nil {
		return fmt.Errorf("create migrations table: %v", err)
	}

	migrations, err := mr.getMigrationFiles()
	if err != nil {
		return fmt.Errorf("get migration files: %v", err)
	}

	applied, err := mr.getAppliedMigrations()
	if err != nil {
		return fmt.Errorf("get applied migrations: %v", err)
	}

	for _, migration := range migrations {
		if applied[migration.Version] {
			continue
		}

		if err := mr.runMigration(migration, "up"); err != nil {
			return fmt.Errorf("run migration %d: %v", migration.Version, err)
		}

		if err := mr.recordMigration(migration.Version); err != nil {
			return fmt.Errorf("record migration %d: %v", migration.Version, err)
		}
	}

	return nil
}

func (mr *MigrationRunner) createMigrationsTable() error {
	return mr.db.Exec(`
		CREATE TABLE IF NOT EXISTS schema_migrations (
			id SERIAL PRIMARY KEY,
			version INTEGER UNIQUE NOT NULL,
			applied_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		);
	`).Error
}

func (mr *MigrationRunner) getMigrationFiles() ([]Migration, error) {
	const migrationsDir = "migrations"

	if _, err := os.Stat(migrationsDir); os.IsNotExist(err) {
		return nil, nil
	}

	files, err := filepath.Glob(filepath.Join(migrationsDir, "*.up.sql"))
	if err != nil {
		return nil, err
	}

	var migrations []Migration
	for _, file := range files {
		filename := filepath.Base(file)
		parts := strings.SplitN(filename, "_", 2)
		if len(parts) < 2 {
			continue
		}

		version, err := strconv.Atoi(parts[0])
		if err != nil {
			continue
		}

		name := strings.TrimSuffix(parts[1], ".up.sql")
		downFile := filepath.Join(migrationsDir, fmt.Sprintf("%d_%s.down.sql", version, name))

		migrations = append(migrations, Migration{
			Version:  version,
			Name:     name,
			UpFile:   file,
			DownFile: downFile,
		})
	}

	sort.Slice(migrations, func(i, j int) bool {
		return migrations[i].Version < migrations[j].Version
	})

	return migrations, nil
}

func (mr *MigrationRunner) getAppliedMigrations() (map[int]bool, error) {
	applied := make(map[int]bool)

	rows, err := mr.db.Raw("SELECT version FROM schema_migrations").Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			return nil, err
		}
		applied[version] = true
	}

	return applied, rows.Err()
}

func (mr *MigrationRunner) runMigration(migration Migration, direction string) error {
	filePath := migration.UpFile
	if direction == "down" {
		filePath = migration.DownFile
	}

	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		if direction == "down" {
			return nil // Down file is optional
		}
		return fmt.Errorf("migration file not found: %s", filePath)
	}

	content, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("read migration file %s: %v", filePath, err)
	}

	sql := strings.TrimSpace(string(content))
	if sql == "" {
		return nil // Skip empty migrations
	}

	if err := mr.db.Exec(sql).Error; err != nil {
		return fmt.Errorf("execute migration %s: %v", filePath, err)
	}

	return nil
}

func (mr *MigrationRunner) recordMigration(version int) error {
	return mr.db.Exec(
		"INSERT INTO schema_migrations (version) VALUES (?) ON CONFLICT (version) DO NOTHING",
		version,
	).Error
}

func (mr *MigrationRunner) GetMigrationStatus() ([]Migration, error) {
	migrations, err := mr.getMigrationFiles()
	if err != nil {
		return nil, err
	}

	applied, err := mr.getAppliedMigrations()
	if err != nil {
		return nil, err
	}

	for i := range migrations {
		migrations[i].IsApplied = applied[migrations[i].Version]
	}

	return migrations, nil
}
