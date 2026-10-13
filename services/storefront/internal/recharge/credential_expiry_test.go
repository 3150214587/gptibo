package recharge

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func TestCredentialExpiry(t *testing.T) {
	now := time.Unix(1800000000, 0)
	jwt := func(claims string) string {
		return "e30." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".fixture"
	}
	for _, tc := range []struct {
		name, token, expires, want string
	}{
		{"expired token", jwt(`{"exp":1}`), "2099-01-01T00:00:00Z", "Access Token 已过期"},
		{"boundary", jwt(`{"exp":1800000000}`), "", "Access Token 已过期"},
		{"zero", jwt(`{"exp":0}`), "", "Access Token 已过期"},
		{"expired session", jwt(`{"exp":1900000000}`), "2000-01-01T00:00:00Z", "Session 已超过有效期"},
		{"future", jwt(`{"exp":1900000000}`), "2099-01-01T00:00:00Z", ""},
		{"opaque", "fixture-token", "bad-date", ""},
		{"missing", jwt(`{}`), "", ""},
		{"null", jwt(`{"exp":null}`), "", ""},
		{"string", jwt(`{"exp":"1"}`), "", ""},
		{"malformed", "a.%%.b", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := Credential{Mode: "session", Session: Raw(map[string]any{"accessToken": tc.token, "expires": tc.expires})}
			err := credentialExpiry(c, now)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("unexpected expiry result: %v", err)
			}
		})
	}
	if credentialExpiry(Credential{Mode: "access_token", AccessToken: jwt(`{"exp":1}`)}, now) == nil {
		t.Fatal("expired raw token accepted")
	}
}
