// giftcontract is a test-only process used by the supply platform contract test.
// It is not included in the application binary or exposed as an HTTP route.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"gptibo/storefront/internal/xgift"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type settings struct{ URL, Certificate, Key, Secret, Phase string }
type lostResponse struct {
	base http.RoundTripper
	lost bool
}

func (l *lostResponse) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := l.base.RoundTrip(r)
	if err == nil && !l.lost && r.Method == "POST" && r.URL.Path == "/v1/orders" {
		l.lost = true
		resp.Body.Close()
		return nil, errors.New("synthetic lost response after acceptance")
	}
	return resp, err
}
func run() error {
	var cfg settings
	if err := json.NewDecoder(io.LimitReader(os.Stdin, 16384)).Decode(&cfg); err != nil {
		return err
	}
	c, err := xgift.NewClient(cfg.URL+"/v1", "platform", "", cfg.Key, cfg.Secret)
	if err != nil {
		return err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(cfg.Certificate)) {
		return errors.New("missing fixture CA")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}
	defer transport.CloseIdleConnections()
	c.HTTP = &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	ctx := context.Background()
	if cfg.Phase == "create" {
		products, e := c.Products(ctx)
		if e != nil {
			return e
		}
		found := false
		for _, p := range products {
			if p.Code == "x-premium-3m" && p.Points == 137 && p.Enabled == 1 {
				found = true
			}
		}
		if !found {
			return errors.New("configured product quote not available")
		}
		elig, e := c.Eligibility(ctx, "receiver", "x-premium-3m")
		if e != nil {
			return e
		}
		if elig.Eligible == nil || !*elig.Eligible || elig.Recipient != "12345" {
			return errors.New("recipient mismatch")
		}
		c.HTTP.Transport = &lostResponse{base: transport}
		if _, e = c.Create(ctx, "contract-order", "x-premium-3m", "receiver", "12345", 137); e == nil {
			return errors.New("lost response did not fail")
		}
		original, e := c.Query(ctx, "contract-order")
		if e != nil {
			return e
		}
		repeated, e := c.Create(ctx, "contract-order", "x-premium-3m", "receiver", "12345", 137)
		if e != nil {
			return e
		}
		if original.ID != repeated.ID || original.Points != 137 || original.Status != "queued" {
			return errors.New("original order identity changed")
		}
		if _, e = c.Create(ctx, "wrong-price", "x-premium-3m", "receiver", "12345", 138); e == nil {
			return errors.New("price drift accepted")
		}
		balance, e := c.Balance(ctx)
		if e != nil {
			return e
		}
		if balance.Available != 9863 || balance.Frozen != 137 {
			return errors.New("wallet not frozen exactly once")
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]string{"id": original.ID})
	}
	order, e := c.Query(ctx, "contract-order")
	if e != nil {
		return e
	}
	if order.Status != "succeeded" || order.Points != 137 || (order.Receipt == nil || !strings.HasPrefix(*order.Receipt, "rcpt_")) {
		return errors.New("terminal order lost")
	}
	balance, e := c.Balance(ctx)
	if e != nil {
		return e
	}
	if balance.Available != 9863 || balance.Frozen != 0 {
		return errors.New("terminal wallet mismatch")
	}
	return nil
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
