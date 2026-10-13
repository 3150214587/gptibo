package store

import "testing"

func TestPublicMigrationsRequireExactChecksum(t *testing.T) {
	data := []byte("SELECT 1;\n")
	if !migrationMatches("example.sql", data, checksum(data)) {
		t.Fatal("matching migration rejected")
	}
	if migrationMatches("example.sql", []byte("SELECT 2;\n"), checksum(data)) || migrationMatches("example.sql", data, "unknown") {
		t.Fatal("changed or unknown migration accepted")
	}
}
