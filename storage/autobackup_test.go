package storage

import "testing"

func Test_Storage_GetJobsToProcess(t *testing.T) {
	postgres, _ := NewPostgresStore("host=127.0.0.1 user=pradip password=password dbname=mystorx port=5432 sslmode=disable TimeZone=Asia/Shanghai")

	out, err := postgres.GetJobsToProcess()
	if err != nil {
		t.Error(err)
	}

	t.Log(out)
}
