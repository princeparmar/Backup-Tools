package crons

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"time"

	"github.com/StorX2-0/Backup-Tools/satellite"
)

type psqlDatabaseProcessor struct{}

func NewPsqlDatabaseProcessor() *psqlDatabaseProcessor {
	return &psqlDatabaseProcessor{}
}

func (d *psqlDatabaseProcessor) Run(input ProcessorInput) error {
	

	err := input.HeartBeatFunc()
	if err != nil {
		return err
	}

	databaseName, ok := input.InputData["database_name"].(string)
	if !ok {
		return fmt.Errorf("database_name is required")
	}

	upload, err := satellite.GetUploader(context.TODO(), input.Job.StorxToken, "database", fmt.Sprintf("postgresql/%v_%v.sql.tar.gz", databaseName, time.Now().Unix()))
	if err != nil {
		return err
	}

	cmd, err := d.GetCommand(input)
	if err != nil {
		return err
	}

	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}

	_, err = io.Copy(upload, pipe)
	if err != nil {
		return err
	}

	err = cmd.Start()
	if err != nil {
		return err
	}

	err = cmd.Wait()
	if err != nil {
		return err
	}

	err = upload.Commit()
	if err != nil {
		return err
	}

	return nil
}

func (p *psqlDatabaseProcessor) GetCommand(input ProcessorInput) (*exec.Cmd, error) {
	username, ok := input.InputData["username"].(string)
	if !ok {
		return nil, fmt.Errorf("username is required")
	}

	host, ok := input.InputData["host"].(string)
	if !ok {
		return nil, fmt.Errorf("host is required")
	}

	port, ok := input.InputData["port"].(string)
	if !ok {
		return nil, fmt.Errorf("port is required")
	}

	password, ok := input.InputData["password"].(string)
	if !ok {
		return nil, fmt.Errorf("password is required")
	}

	databaseName, ok := input.InputData["database_name"].(string)
	if !ok {
		return nil, fmt.Errorf("database_name is required")
	}

	cmd := exec.Command("pg_dump", "-U", username, "-h", host, "-p", port, "-d", databaseName)
	cmd.Env = append(cmd.Env, "PGPASSWORD="+password)

	return cmd, nil
}
