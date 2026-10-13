// Package access validates Cloudflare Access assertions at the origin, including
// requests arriving through alternate hosts. Header presence alone is not trust.
package access

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"gptibo/storefront/internal/fault"
)

type Service struct {
	issuer, audience, email string
	client                  *http.Client
	mu                      sync.Mutex
	keys                    map[string]*rsa.PublicKey
	fetched, attempt        time.Time
}

var teamPattern = regexp.MustCompile(`^https://[a-z0-9][a-z0-9-]*\.cloudflareaccess\.com$`)
var audiencePattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func New(issuer, audience, email string) (*Service, error) {
	if !teamPattern.MatchString(issuer) || !audiencePattern.MatchString(audience) || email == "" {
		return nil, errors.New("Access requires exact team HTTPS origin, application audience and admin email")
	}
	return &Service{issuer: issuer, audience: audience, email: strings.ToLower(email), keys: map[string]*rsa.PublicKey{}, client: &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

type claims struct {
	jwt.RegisteredClaims
	Email string `json:"email"`
	Type  string `json:"type"`
}

func denied() error { return fault.New(403, "请先通过管理员安全准入，再登录后台。") }
func (s *Service) Verify(ctx context.Context, raw string) error {
	if len(raw) == 0 || len(raw) > 16384 {
		return denied()
	}
	c := &claims{}
	token, err := jwt.ParseWithClaims(raw, c, func(token *jwt.Token) (any, error) {
		kid, ok := token.Header["kid"].(string)
		if !ok || len(kid) > 256 {
			return nil, denied()
		}
		return s.key(ctx, kid)
	}, jwt.WithValidMethods([]string{"RS256"}), jwt.WithIssuer(s.issuer), jwt.WithAudience(s.audience), jwt.WithExpirationRequired(), jwt.WithIssuedAt())
	if err != nil || !token.Valid || c.Type != "app" || c.Subject == "" || !strings.EqualFold(c.Email, s.email) {
		return denied()
	}
	return nil
}
func (s *Service) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := s.keys[kid]
	if key != nil && time.Since(s.fetched) < time.Hour {
		return key, nil
	}
	// Unknown keys cannot make each forged request trigger a network lookup.
	if time.Since(s.attempt) < time.Minute {
		return nil, denied()
	}
	s.attempt = time.Now()
	req, err := http.NewRequestWithContext(ctx, "GET", s.issuer+"/cdn-cgi/access/certs", nil)
	if err != nil {
		return nil, denied()
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, denied()
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, denied()
	}
	var doc struct {
		Keys []struct{ Kid, Kty, Alg, Use, N, E string }
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 128<<10)).Decode(&doc) != nil {
		return nil, denied()
	}
	keys := map[string]*rsa.PublicKey{}
	for _, k := range doc.Keys {
		if k.Kty != "RSA" || k.Alg != "RS256" || k.Use != "sig" {
			continue
		}
		n, err := base64.RawURLEncoding.DecodeString(k.N)
		if err != nil || len(n) < 256 || len(n) > 1024 {
			continue
		}
		e, err := base64.RawURLEncoding.DecodeString(k.E)
		if err != nil || len(e) > 4 {
			continue
		}
		exponent := new(big.Int).SetBytes(e).Int64()
		if exponent < 3 || exponent > 2147483647 || exponent%2 == 0 {
			continue
		}
		keys[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(exponent)}
	}
	if len(keys) == 0 {
		return nil, denied()
	}
	s.keys = keys
	s.fetched = time.Now()
	if key = keys[kid]; key == nil {
		return nil, denied()
	}
	return key, nil
}
