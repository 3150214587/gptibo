package main

import (
	"errors"
	"os"
	"strings"
)

// Docker secrets and private files avoid embedding credentials in Compose or Git.
func loadSecretFiles() error {
	for _, name := range []string{"DATABASE_URL", "MASTER_KEY", "APP_DB_PASSWORD", "ADMIN_PASSWORD", "RESEND_API_KEY", "FEISHU_APP_SECRET", "NOCAPTCHA_TOKEN"} {
		path := os.Getenv(name + "_FILE")
		if path == "" {
			continue
		}
		if os.Getenv(name) != "" {
			return errors.New("use either secret value or secret file, not both")
		}
		data, err := os.ReadFile(path)
		if err != nil || len(data) > 65536 || len(strings.TrimSpace(string(data))) == 0 {
			return errors.New("secret file unavailable or invalid")
		}
		if err := os.Setenv(name, strings.TrimSpace(string(data))); err != nil {
			return errors.New("secret configuration failed")
		}
	}
	return nil
}
