package config

import (
	"errors"
	"net"
	"net/url"
	"os"
	"strings"
)

type Config struct {
	DatabaseURL string
	ListenAddr  string
	WorkerID    string
}

// Secret supports Compose file secrets and local environment variables.
func Secret(name string) (string, error) {
	value, file := os.Getenv(name), os.Getenv(name+"_FILE")
	if value != "" && file != "" {
		return "", errors.New(name + " and " + name + "_FILE are mutually exclusive")
	}
	if file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			return "", errors.New("cannot read " + name + "_FILE")
		}
		value = strings.TrimRight(string(b), "\r\n")
	}
	if value == "" {
		return "", errors.New(name + " is required")
	}
	return value, nil
}

func Load() (Config, error) {
	c := Config{ListenAddr: os.Getenv("LISTEN_ADDR"), WorkerID: os.Getenv("WORKER_ID")}
	if c.ListenAddr == "" {
		c.ListenAddr = "127.0.0.1:8093"
	}
	if _, _, err := net.SplitHostPort(c.ListenAddr); err != nil {
		return c, errors.New("invalid LISTEN_ADDR")
	}
	if c.WorkerID == "" {
		c.WorkerID = "storefront-worker"
	}
	if len(c.WorkerID) > 100 {
		return c, errors.New("WORKER_ID too long")
	}
	if os.Getenv("DATABASE_URL") != "" || os.Getenv("DATABASE_URL_FILE") != "" {
		var err error
		c.DatabaseURL, err = Secret("DATABASE_URL")
		return c, err
	}
	password, err := Secret("DB_PASSWORD")
	if err != nil {
		return c, err
	}
	host, user, database := os.Getenv("DB_HOST"), os.Getenv("DB_USER"), os.Getenv("DB_NAME")
	if host == "" || user == "" || database == "" {
		return c, errors.New("DB_HOST, DB_USER and DB_NAME are required")
	}
	port := os.Getenv("DB_PORT")
	if port == "" {
		port = "5432"
	}
	u := url.URL{Scheme: "postgres", User: url.UserPassword(user, password), Host: net.JoinHostPort(host, port), Path: "/" + database}
	ssl := os.Getenv("DB_SSLMODE")
	if ssl == "" {
		ssl = "verify-full"
	}
	q := url.Values{"sslmode": {ssl}}
	u.RawQuery = q.Encode()
	c.DatabaseURL = u.String()
	return c, nil
}
