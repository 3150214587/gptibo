package commerce

import (
	"net"
	"net/url"
	"os"
	"slices"
	"strings"
)

// The operator, not a browser request, determines where card credentials may go.
// Reserved .invalid hosts are inert examples and are useful in isolated tests.
func ValidateCardBase(base string) error {
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Port() != "" || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || u.Path != "/openapi/v1" || net.ParseIP(u.Hostname()) != nil {
		return Fail("invalid_provider_url", "卡台地址必须是已允许的 HTTPS 域名和 /openapi/v1 路径", 400)
	}
	host := strings.ToLower(u.Hostname())
	allowed := strings.Split(strings.ToLower(os.Getenv("CARD_API_ALLOWED_HOSTS")), ",")
	for i := range allowed {
		allowed[i] = strings.TrimSpace(allowed[i])
	}
	if !strings.HasSuffix(host, ".invalid") && !slices.Contains(allowed, host) {
		return Fail("provider_host_not_allowed", "卡台域名尚未加入服务器允许名单", 400)
	}
	return nil
}
