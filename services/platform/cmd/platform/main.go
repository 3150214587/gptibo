package main

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
	"gptibo/platform/internal/commerce"
	"gptibo/platform/internal/delivery"
	"gptibo/platform/internal/httpapi"
	"gptibo/platform/internal/providers"
	"gptibo/platform/internal/security"
	"gptibo/platform/internal/store"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	if e := run(); e != nil {
		slog.Error("platform stopped; check configuration, database and migration status")
		os.Exit(1)
	}
}
func run() error {
	if err := loadSecretFiles(); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	mode := "api"
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}
	s, e := store.Open(ctx, os.Getenv("DATABASE_URL"), os.Getenv("MASTER_KEY"))
	if e != nil {
		return errors.New("database or master key initialization failed")
	}
	defer s.DB.Close()
	if mode == "migrate" {
		return s.Migrate(ctx)
	}
	if mode == "bootstrap" {
		return bootstrap(ctx, s)
	}
	if mode == "grant-runtime" {
		return s.ProvisionRuntime(ctx, os.Getenv("RUNTIME_DB_USER"), os.Getenv("APP_DB_PASSWORD"))
	}
	if e = s.VerifyMigrations(ctx); e != nil {
		return e
	}
	svc := &commerce.Service{Store: s}
	engine := &providers.Engine{Store: s, NoCaptchaToken: os.Getenv("NOCAPTCHA_TOKEN"), NoCaptchaAuto: os.Getenv("NOCAPTCHA_AUTO") == "true"}
	if mode == "worker" {
		w := &delivery.Worker{Store: s, Engine: engine, Commerce: svc, ID: security.ID("worker")}
		w.Run(ctx)
		return nil
	}
	if mode != "api" {
		return errors.New("command must be api, worker, migrate, grant-runtime or bootstrap")
	}
	origin := os.Getenv("PUBLIC_ORIGIN")
	parsed, e := url.Parse(origin)
	if e != nil || parsed.Hostname() == "" || parsed.Path != "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("PUBLIC_ORIGIN must be an origin")
	}
	local := parsed.Hostname() == "127.0.0.1" || parsed.Hostname() == "localhost"
	if parsed.Scheme != "https" && !(local && parsed.Scheme == "http") {
		return errors.New("PUBLIC_ORIGIN requires HTTPS")
	}
	address := os.Getenv("LISTEN_ADDR")
	if address == "" {
		address = "127.0.0.1:8092"
	}
	web := os.Getenv("WEB_DIR")
	if web == "" {
		web = "web/dist"
	}
	server := &httpapi.Server{Store: s, Commerce: svc, Origin: origin, Secure: !local, WebDir: web, Preflight: engine.Preflight, Inventory: engine.Inventory, ResolveCard: engine.ResolveCardOperation, ManualCheckout: engine.ManualCheckout, RegenerateCheckout: engine.RegenerateManualCheckout, GPTPlans: engine.GPTPlans, ValidatePair: engine.ValidatePair}
	// Native deployment's nginx overwrites forwarded headers. Never trust remote clients.
	server.TrustedProxies = []string{"127.0.0.1", "::1"}
	httpServer := &http.Server{Addr: address, Handler: server.Router(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 90 * time.Second, IdleTimeout: 60 * time.Second}
	done := make(chan error, 1)
	go func() { slog.Info("platform API listening", "address", address); done <- httpServer.ListenAndServe() }()
	select {
	case e := <-done:
		if !errors.Is(e, http.ErrServerClosed) {
			return e
		}
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdown)
	}
	return nil
}
func bootstrap(ctx context.Context, s *store.Store) error {
	email, password := strings.ToLower(strings.TrimSpace(os.Getenv("ADMIN_EMAIL"))), os.Getenv("ADMIN_PASSWORD")
	if !strings.Contains(email, "@") || len(password) < 16 || len(password) > 72 {
		return errors.New("ADMIN_EMAIL and 16–72 byte ADMIN_PASSWORD required")
	}
	hash, e := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if e != nil {
		return e
	}
	return s.Tx(ctx, func(tx pgx.Tx) error {
		var exists bool
		if e := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM users WHERE role='admin')").Scan(&exists); e != nil {
			return e
		}
		if exists {
			return errors.New("administrator already exists; bootstrap does not reset passwords")
		}
		id := security.ID("adm")
		if _, e := tx.Exec(ctx, "INSERT INTO users(id,email,name,role,status,password_hash) VALUES($1,$2,'管理员','admin','active',$3)", id, email, string(hash)); e != nil {
			return e
		}
		return store.Audit(ctx, tx, id, "bootstrap", id, "")
	})
}
