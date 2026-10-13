package domains

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
)

type routingDNS struct {
	cname     string
	addresses map[string][]net.IPAddr
}

func (d routingDNS) LookupTXT(context.Context, string) ([]string, error) { return nil, nil }
func (d routingDNS) LookupCNAME(context.Context, string) (string, error) { return d.cname, nil }
func (d routingDNS) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	return d.addresses[host], nil
}

func TestApexFlattenedRouting(t *testing.T) {
	const target = "shops.shop.example.invalid"
	ips := func(values ...string) []net.IPAddr {
		var result []net.IPAddr
		for _, value := range values {
			result = append(result, net.IPAddr{IP: net.ParseIP(value)})
		}
		return result
	}
	for _, tc := range []struct {
		name, host, cname string
		actual, expected  []net.IPAddr
		want              bool
	}{
		{"ordinary CNAME", "shop.example.com", target + ".", nil, nil, true},
		{"apex CNAME", "example.com", target + ".", nil, nil, true},
		{"flattened IPv4", "example.com", "example.com.", ips("104.21.1.2"), ips("104.21.1.2", "172.67.1.2"), true},
		{"flattened dual stack", "example.co.uk", "example.co.uk.", ips("104.21.1.2", "2606:4700::1234"), ips("104.21.1.2", "2606:4700::1234"), true},
		{"wrong IPv6", "example.com", "example.com.", ips("104.21.1.2", "2606:4700::5678"), ips("104.21.1.2", "2606:4700::1234"), false},
		{"mixed origin", "example.com", "example.com.", ips("104.21.1.2", "203.0.113.1"), ips("104.21.1.2"), false},
		{"private target", "example.com", "example.com.", ips("10.0.0.1"), ips("10.0.0.1"), false},
		{"loopback", "example.com", "example.com.", ips("127.0.0.1"), ips("127.0.0.1"), false},
		{"no answer", "example.com", "example.com.", nil, ips("104.21.1.2"), false},
		{"no target", "example.com", "example.com.", ips("104.21.1.2"), nil, false},
		{"unrelated CNAME", "example.com", "elsewhere.example.net.", ips("104.21.1.2"), ips("104.21.1.2"), false},
		{"subdomain requires CNAME", "shop.example.com", "shop.example.com.", ips("104.21.1.2"), ips("104.21.1.2"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := routingDNS{cname: tc.cname, addresses: map[string][]net.IPAddr{tc.host: tc.actual, target: tc.expected}}
			if got := routesToTarget(context.Background(), d, tc.host, target); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNormalizeDomain(t *testing.T) {
	for _, s := range []string{"com", "co.uk", "127.0.0.1", "https://shop.example.com", "shop.example.com:443", "*.example.com", "shop.example.invalid", "shop.shop.example.invalid", "localhost", "shop.localhost", "shop.example.com/", "-shop.example.com", "shop..example.com", "shop.example.com."} {
		if _, e := Normalize(s, "shop.example.invalid"); e == nil {
			t.Errorf("accepted %q", s)
		}
	}
	for _, s := range []string{"example.com", "example.co.uk", "例子.com", "shop.example.com", "www.example.co.uk", " SHOP.EXAMPLE.COM ", "商店.example.com"} {
		if _, e := Normalize(s, "shop.example.invalid"); e != nil {
			t.Errorf("rejected %q: %v", s, e)
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestProviderQuotaAndAmbiguousCreate(t *testing.T) {
	for _, tc := range []struct {
		name       string
		used       int
		existing   bool
		wantCreate bool
	}{{"free slot", 99, false, true}, {"full", 100, false, false}, {"existing recovers without creating", 100, true, false}} {
		t.Run(tc.name, func(t *testing.T) {
			created := false
			p := &Cloudflare{Zone: strings.Repeat("a", 32), Token: "private-token", Limit: 100, Client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host != "api.cloudflare.com" || r.Header.Get("Authorization") != "Bearer private-token" {
					t.Fatal("wrong API target/auth")
				}
				body := `{"success":true,"result":[]}`
				if strings.HasSuffix(r.URL.Path, "/quota") {
					body = fmt.Sprintf(`{"success":true,"result":{"used":%d}}`, tc.used)
				} else if r.Method == "POST" {
					created = true
					body = `{"success":true,"result":{"id":"domain-id","hostname":"shop.example.com"}}`
				} else if tc.existing {
					body = `{"success":true,"result":[{"id":"domain-id","hostname":"shop.example.com"}]}`
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
			})}}
			_, e := p.Ensure(context.Background(), "shop.example.com")
			if created != tc.wantCreate {
				t.Fatal("incorrect billed creation")
			}
			if tc.used == 100 && !tc.existing && e == nil {
				t.Fatal("quota not enforced")
			}
			if (tc.existing || tc.wantCreate) && e != nil {
				t.Fatal(e)
			}
		})
	}
}
func TestProviderFailureDoesNotExposeCredentialOrResponse(t *testing.T) {
	p := &Cloudflare{Zone: "zone", Token: "secret", Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 403, Body: io.NopCloser(strings.NewReader(`{"success":false,"errors":[{"message":"secret private origin"}]}`))}, nil
	})}}
	_, e := p.Get(context.Background(), "id")
	if e == nil || strings.Contains(e.Error(), "secret") || strings.Contains(e.Error(), "private origin") {
		t.Fatal("unsafe provider failure")
	}
}

func TestProviderPendingState(t *testing.T) {
	for _, tc := range []struct {
		name, payload, state, message string
	}{
		{"flattened apex rejected", `{"hostname":"example.com","status":"pending","verification_errors":["custom hostname does not CNAME to this zone."]}`, "pending_dns", "ALIAS"},
		{"Cloudflare routing rejected", `{"hostname":"example.com","status":"pending","verification_errors":["The hostname is using Cloudflare and cannot be activated with an TXT or HTTP validation token. To activate the custom hostname, the DNS target needs to point to the SaaS zone"]}`, "pending_dns", "ALIAS"},
		{"subdomain rejected", `{"hostname":"shop.example.com","status":"pending","verification_errors":["custom hostname does not CNAME to this zone."]}`, "pending_dns", "该子域名的 CNAME"},
		{"unknown provider message stays private", `{"hostname":"example.com","status":"pending","verification_errors":["private diagnostic"]}`, "pending_dns", "尚未完成域名接入验证"},
		{"certificate alone cannot approve routing", `{"hostname":"example.com","status":"pending","ssl":{"status":"active"}}`, "pending_dns", "尚未完成域名接入验证"},
		{"approved routing ignores stale errors", `{"hostname":"example.com","status":"active","ssl":{"status":"pending_validation"},"verification_errors":["custom hostname does not CNAME to this zone."]}`, "pending_certificate", "域名接入已通过"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var h Hostname
			if err := json.Unmarshal([]byte(tc.payload), &h); err != nil {
				t.Fatal(err)
			}
			state, message := h.PendingState()
			if state != tc.state || !strings.Contains(message, tc.message) || strings.Contains(message, "private diagnostic") {
				t.Fatalf("unexpected pending state: %s %s", state, message)
			}
		})
	}
}
