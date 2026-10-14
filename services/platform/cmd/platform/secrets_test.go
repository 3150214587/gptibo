package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSecretFileRequiresUnambiguousNonemptyInput(t *testing.T) {
	for _, name := range []string{"DATABASE_URL", "MASTER_KEY", "APP_DB_PASSWORD", "ADMIN_PASSWORD", "RESEND_API_KEY", "FEISHU_APP_SECRET", "NOCAPTCHA_TOKEN"} {
		t.Setenv(name, "")
		t.Setenv(name+"_FILE", "")
	}
	file := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(file, []byte("synthetic-value\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MASTER_KEY_FILE", file)
	if err := loadSecretFiles(); err != nil || os.Getenv("MASTER_KEY") != "synthetic-value" {
		t.Fatal("file not loaded", err)
	}
	if loadSecretFiles() == nil {
		t.Fatal("ambiguous source accepted")
	}
	t.Setenv("MASTER_KEY", "")
	if err := os.WriteFile(file, []byte("\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if loadSecretFiles() == nil {
		t.Fatal("empty secret accepted")
	}
}
