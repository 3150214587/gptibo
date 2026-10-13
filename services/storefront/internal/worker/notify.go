package worker

import (
	"context"
	"github.com/jackc/pgx/v5"
	"gptibo/storefront/internal/store"
	"log/slog"
	"time"
)

// Notifications only accelerate discovery. Existing order/purchase rows and leases
// are durable; periodic scans recover work across disconnects and process restarts.
func listen(ctx context.Context, s *store.Store, logger *slog.Logger, signals []chan struct{}) {
	for ctx.Err() == nil {
		setup, cancel := context.WithTimeout(ctx, 5*time.Second)
		conn, e := pgx.ConnectConfig(setup, s.DB.Config().ConnConfig.Copy())
		var schema string
		if e == nil {
			e = conn.QueryRow(setup, `SELECT current_schema()`).Scan(&schema)
		}
		if e == nil {
			_, e = conn.Exec(setup, `LISTEN storefront_work`)
		}
		cancel()
		if e == nil {
			logger.Info("business wake listener ready")
			for _, ch := range signals {
				select {
				case ch <- struct{}{}:
				default:
				}
			}
			for ctx.Err() == nil {
				n, err := conn.WaitForNotification(ctx)
				if err != nil {
					break
				}
				if n.Payload == schema {
					for _, ch := range signals {
						select {
						case ch <- struct{}{}:
						default:
						}
					}
				}
			}
		}
		if conn != nil {
			closeCtx, stop := context.WithTimeout(context.Background(), time.Second)
			_ = conn.Close(closeCtx)
			stop()
		}
		if ctx.Err() != nil {
			return
		}
		logger.Warn("business wake listener reconnecting; periodic scans remain active")
		timer := time.NewTimer(5 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
