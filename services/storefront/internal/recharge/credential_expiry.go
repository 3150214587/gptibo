package recharge

import (
	"encoding/base64"
	"encoding/json"
	"math"
	"strings"
	"time"

	"gptibo/storefront/internal/fault"
)

// Reject known expiry before a new attempt. Decoding these unsigned input
// claims is not authentication; missing/unreadable dates remain unconfirmed.
func credentialExpiry(c Credential, now time.Time) error {
	token := c.AccessToken
	var expires string
	if c.Mode == "session" {
		var session map[string]json.RawMessage
		if json.Unmarshal([]byte(c.Session), &session) != nil {
			return nil // Structural validation belongs to Action.
		}
		_ = json.Unmarshal(session["accessToken"], &token)
		_ = json.Unmarshal(session["expires"], &expires)
	}
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) == 3 {
		if raw, err := base64.RawURLEncoding.DecodeString(parts[1]); err == nil {
			var claims map[string]json.RawMessage
			var exp float64
			if json.Unmarshal(raw, &claims) == nil && string(claims["exp"]) != "null" && json.Unmarshal(claims["exp"], &exp) == nil && !math.IsNaN(exp) && !math.IsInf(exp, 0) && exp*1000 <= float64(now.UnixMilli()) {
				return fault.New(400, "Session 中的 Access Token 已过期，请在 ChatGPT 重新获取完整 Session 后再提交。")
			}
		}
	}
	if expiry, err := time.Parse(time.RFC3339Nano, expires); err == nil && !expiry.After(now) {
		return fault.New(400, "Session 已超过有效期，请在 ChatGPT 重新获取完整 Session 后再提交。")
	}
	return nil
}
