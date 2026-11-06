package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnvFilePathUsesServiceFile(t *testing.T) {
	got := envFilePath(`D:\bin`, `D:\bin\.env.microsoft`)
	if got != `D:\bin\.env.microsoft` {
		t.Fatalf("microsoft path = %q", got)
	}
	got = envFilePath(`D:\bin`, `  D:\bin\.env.google  `)
	if got != `D:\bin\.env.google` {
		t.Fatalf("google path = %q", got)
	}
}

func TestEnvFilePathDefaultsBesideExe(t *testing.T) {
	got := envFilePath(`D:\bin`, "  ")
	want := filepath.Join(`D:\bin`, ".env")
	if got != want {
		t.Fatalf("default path = %q, want %q", got, want)
	}
}

func TestLoadEnvFileUsesOnlyTheServiceFile(t *testing.T) {
	dir := t.TempDir()
	microsoftFile := filepath.Join(dir, ".env.microsoft")
	googleFile := filepath.Join(dir, ".env")
	if err := os.WriteFile(microsoftFile, []byte("STORX_ENV_FILE_TEST_MS=from-microsoft\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(googleFile, []byte("STORX_ENV_FILE_TEST_MS=from-google\nSTORX_ENV_FILE_TEST_GOOGLE=from-google\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENV_FILE", microsoftFile)
	os.Unsetenv("STORX_ENV_FILE_TEST_MS")
	os.Unsetenv("STORX_ENV_FILE_TEST_GOOGLE")

	if err := loadEnvFile(dir); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("STORX_ENV_FILE_TEST_MS"); got != "from-microsoft" {
		t.Fatalf("microsoft value = %q", got)
	}
	if got := os.Getenv("STORX_ENV_FILE_TEST_GOOGLE"); got != "" {
		t.Fatalf("google value leaked: %q", got)
	}
}

func TestLoadEnvFileMissingServiceFileDoesNotUseTheOther(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("STORX_ENV_FILE_TEST_MISSING=from-google\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENV_FILE", filepath.Join(dir, ".env.microsoft"))
	os.Unsetenv("STORX_ENV_FILE_TEST_MISSING")

	if err := loadEnvFile(dir); err == nil {
		t.Fatal("missing ENV_FILE should fail")
	}
	if got := os.Getenv("STORX_ENV_FILE_TEST_MISSING"); got != "" {
		t.Fatalf("fell back to the other env: %q", got)
	}
}
