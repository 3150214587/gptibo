package recharge

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"
)

func retryAfter(value string) time.Duration {
	if n, err := strconv.ParseUint(value, 10, 32); err == nil {
		return time.Duration(n) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil {
		return max(time.Duration(0), time.Until(at))
	}
	return 0
}

// One bounded recovery attempt while the authorized credential is still in memory.
// Reuse the exact body and business key; Request generates fresh signing metadata.
// Credentials are stored encrypted by the caller; polling never replays a submission.
func (c *Client) SubmitRedemption(ctx context.Context, raw, key string, out *Redemption, beforeRetry func() error) error {
	err := c.Request(ctx, "/redemptions", &raw, key, out)
	var api *APIError
	if !errors.As(err, &api) || !api.Retryable || ctx.Err() != nil {
		return err
	}
	delay := max(15*time.Second, api.RetryAfter)
	// Longer Retry-After values must be honored, not shortened to fit this request.
	if delay > 15*time.Second {
		return err
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return err
	case <-timer.C:
		if check := beforeRetry(); check != nil {
			return check
		}
		return c.Request(ctx, "/redemptions", &raw, key, out)
	}
}
