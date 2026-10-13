package worker

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"gptibo/storefront/internal/store"
)

// Run maintains liveness and authentication retention. Business jobs run independently;
// the heartbeat does not certify external payment or supplier availability.
type Job struct {
	Name              string
	Interval, Timeout time.Duration
	Run               func(context.Context) error
	Wake              bool
}

func Run(ctx context.Context, s *store.Store, id string, logger *slog.Logger, jobs ...Job) error {
	ctx, stop := context.WithCancel(ctx)
	var workers sync.WaitGroup
	defer func() { stop(); workers.Wait() }()
	var signals []chan struct{}
	for _, job := range jobs {
		wake := make(chan struct{}, 1)
		if job.Wake {
			signals = append(signals, wake)
		}
		workers.Add(1)
		go func() {
			defer workers.Done()
			ticker := time.NewTicker(job.Interval)
			defer ticker.Stop()
			for {
				if ctx.Err() != nil {
					return
				}
				run, cancel := context.WithTimeout(ctx, job.Timeout)
				started := time.Now()
				err := job.Run(run)
				cancel()
				if err != nil && ctx.Err() == nil {
					logger.Error("business reconciliation failed", "job", job.Name)
				}
				if time.Since(started) > time.Second {
					logger.Info("business reconciliation duration", "job", job.Name, "duration_ms", time.Since(started).Milliseconds())
				}
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				case <-wake:
				}
			}
		}()
	}
	if len(signals) > 0 {
		workers.Add(1)
		go func() { defer workers.Done(); listen(ctx, s, logger, signals) }()
	}
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	var lastCleanup time.Time
	for {
		beat, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := s.Heartbeat(beat, id)
		cancel()
		if err != nil && ctx.Err() == nil {
			logger.Error("worker heartbeat failed")
		}
		if time.Since(lastCleanup) >= time.Minute {
			cleanup, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := s.PruneAuth(cleanup)
			cancel()
			if err != nil && ctx.Err() == nil {
				logger.Error("authentication retention failed")
			}
			lastCleanup = time.Now()
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
