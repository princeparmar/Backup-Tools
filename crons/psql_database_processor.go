package crons

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"time"

	"github.com/StorX2-0/Backup-Tools/pkg/prometheus"
	"github.com/StorX2-0/Backup-Tools/satellite"
)

type psqlDatabaseProcessor struct{}

func NewPsqlDatabaseProcessor() *psqlDatabaseProcessor {
	return &psqlDatabaseProcessor{}
}

func (d *psqlDatabaseProcessor) Run(input ProcessorInput) error {
	start := time.Now()

	err := input.HeartBeatFunc()
	if err != nil {
		prometheus.RecordError("psql_processor_heartbeat_failed", "cron")
		return err
	}

	databaseName, ok := input.InputData["database_name"].(string)
	if !ok {
		prometheus.RecordError("psql_processor_database_name_missing", "cron")
		return fmt.Errorf("database_name is required")
	}

	upload, err := satellite.GetUploader(context.TODO(), input.Job.StorxToken, "database", fmt.Sprintf("postgresql/%v_%v.sql.tar.gz", databaseName, time.Now().Unix()))
	if err != nil {
		prometheus.RecordError("psql_processor_uploader_creation_failed", "cron")
		return err
	}

	cmd, err := d.GetCommand(input)
	if err != nil {
		prometheus.RecordError("psql_processor_command_creation_failed", "cron")
		return err
	}

	pipe, err := cmd.StdoutPipe()
	if err != nil {
		prometheus.RecordError("psql_processor_pipe_creation_failed", "cron")
		return err
	}

	bytesWritten, err := io.Copy(upload, pipe)
	if err != nil {
		prometheus.RecordError("psql_processor_data_copy_failed", "cron")
		return err
	}
	prometheus.RecordSize("psql_processor_database_backup", bytesWritten, "database", "postgresql", "database_name", databaseName)

	err = cmd.Start()
	if err != nil {
		prometheus.RecordError("psql_processor_command_start_failed", "cron")
		return err
	}

	err = cmd.Wait()
	if err != nil {
		prometheus.RecordError("psql_processor_command_execution_failed", "cron")
		return err
	}

	err = upload.Commit()
	if err != nil {
		prometheus.RecordError("psql_processor_upload_commit_failed", "cron")
		return err
	}

	duration := time.Since(start)
	prometheus.RecordTimer("psql_processor_duration_seconds", duration, "processor", "psql")
	prometheus.RecordCounter("psql_processor_total", 1, "processor", "psql", "status", "success")
	prometheus.RecordCounter("psql_processor_database_backup_total", 1, "processor", "psql", "database_name", databaseName)

	return nil
}

func (p *psqlDatabaseProcessor) GetCommand(input ProcessorInput) (*exec.Cmd, error) {
	username, ok := input.InputData["username"].(string)
	if !ok {
		prometheus.RecordError("psql_processor_username_missing", "cron")
		return nil, fmt.Errorf("username is required")
	}

	host, ok := input.InputData["host"].(string)
	if !ok {
		prometheus.RecordError("psql_processor_host_missing", "cron")
		return nil, fmt.Errorf("host is required")
	}

	port, ok := input.InputData["port"].(string)
	if !ok {
		prometheus.RecordError("psql_processor_port_missing", "cron")
		return nil, fmt.Errorf("port is required")
	}

	password, ok := input.InputData["password"].(string)
	if !ok {
		prometheus.RecordError("psql_processor_password_missing", "cron")
		return nil, fmt.Errorf("password is required")
	}

	databaseName, ok := input.InputData["database_name"].(string)
	if !ok {
		prometheus.RecordError("psql_processor_database_name_missing", "cron")
		return nil, fmt.Errorf("database_name is required")
	}

	cmd := exec.Command("pg_dump", "-U", username, "-h", host, "-p", port, "-d", databaseName)
	cmd.Env = append(cmd.Env, "PGPASSWORD="+password)

	prometheus.RecordCounter("psql_processor_command_created_total", 1, "processor", "psql", "database_name", databaseName, "host", host)

	return cmd, nil
}
