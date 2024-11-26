package crons

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"time"

	"github.com/StorX2-0/Backup-Tools/satellite"
	"storj.io/uplink"
)

type databaseProcessor struct{}

func NewDatabaseProcessor() *databaseProcessor {
	return &databaseProcessor{}
}

func (d *databaseProcessor) Run(input ProcessorInput) error {
	err := input.HeartBeatFunc()
	if err != nil {
		return err
	}

	if input.Job.InputData["database_name"] == nil || input.Job.InputData["host"] == nil || input.Job.InputData["port"] == nil || input.Job.InputData["username"] == nil || input.Job.InputData["password"] == nil {
		return fmt.Errorf("missing required fields")
	}

	upload, err := satellite.GetUploader(context.TODO(), input.Job.StorxToken, "database", fmt.Sprintf("postgresql/%v_%v.sql.tar.gz", input.Job.InputData["database_name"].(string), time.Now().Unix()))
	if err != nil {
		return err
	}

	postgres := &postgresDump{
		host:     input.Job.InputData["host"].(string),
		port:     input.Job.InputData["port"].(string),
		db:       input.Job.InputData["database_name"].(string),
		username: input.Job.InputData["username"].(string),
		password: input.Job.InputData["password"].(string),
		upload:   upload,
	}

	err = postgres.Export()
	if err != nil {
		return err
	}

	err = upload.Commit()
	if err != nil {
		return err
	}

	return nil
}

type postgresDump struct {
	host     string
	port     string
	db       string
	username string
	password string

	upload *uplink.Upload
}

func (p *postgresDump) Export() error {
	cmd := exec.Command("pg_dump", "-U", p.username, "-h", p.host, "-p", p.port, "-d", p.db)
	cmd.Env = append(cmd.Env, "PGPASSWORD="+p.password)

	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}

	_, err = io.Copy(p.upload, pipe)
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

	return nil
}
