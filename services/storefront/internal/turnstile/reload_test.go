package turnstile

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestRegistryReloadKeepsCheckoutProtected(t *testing.T) {
	p := filepath.Join(t.TempDir(), "widgets.json")
	write := func(raw string) {
		t.Helper()
		if e := os.WriteFile(p, []byte(raw), 0600); e != nil {
			t.Fatal(e)
		}
	}
	write(`[{"sitekey":"primary","secret":"one","hosts":["main.test"]}]`)
	s, e := NewFile(p)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.SiteKey("new.test"); e == nil {
		t.Fatal("unknown domain accepted")
	}
	write(`[{"sitekey":"primary","secret":"one","hosts":["main.test"]},{"sitekey":"new","secret":"two","hosts":["new.test"]}]`)
	if k, e := s.SiteKey("new.test"); e != nil || k != "new" {
		t.Fatalf("new hostname not reloaded: %q %v", k, e)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Form.Get("secret") != "two" {
			t.Error("wrong widget secret")
		}
		json.NewEncoder(w).Encode(map[string]any{"success": true, "hostname": "new.test", "action": Action, "challenge_ts": time.Now()})
	}))
	defer srv.Close()
	s.endpoint = srv.URL
	if e = s.Verify(context.Background(), "new.test", "valid"); e != nil {
		t.Fatal(e)
	}
	// A partial host-side write cannot drop protection on already working stores.
	write(`[{`)
	if k, e := s.SiteKey("main.test"); e != nil || k != "primary" {
		t.Fatal("lost valid snapshot", e)
	}
	if _, e = s.SiteKey("attacker.test"); e == nil {
		t.Fatal("invalid registry opened unknown hostname")
	}
	if e = s.Verify(context.Background(), "new.test", ""); e == nil {
		t.Fatal("reload bypassed token enforcement")
	}
	if e = os.Remove(p); e != nil {
		t.Fatal(e)
	}
	if _, e = s.SiteKey("new.test"); e != nil {
		t.Fatal("missing file lost valid snapshot")
	}
	// Explicit valid changes (including removal) do replace the cached snapshot.
	write(`[{"sitekey":"primary","secret":"one","hosts":["main.test"]}]`)
	if _, e = s.SiteKey("new.test"); e == nil {
		t.Fatal("removed hostname retained")
	}
}

func TestRegistryConcurrentReaders(t *testing.T) {
	p := filepath.Join(t.TempDir(), "widgets.json")
	raw := []byte(`[{"sitekey":"primary","secret":"one","hosts":["main.test"]}]`)
	if e := os.WriteFile(p, raw, 0600); e != nil {
		t.Fatal(e)
	}
	s, e := NewFile(p)
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 30 {
				if _, e := s.SiteKey("main.test"); e != nil {
					t.Error(e)
				}
			}
		}()
	}
	for range 20 {
		if e := os.WriteFile(p, []byte("["), 0600); e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(p, raw, 0600); e != nil {
			t.Fatal(e)
		}
	}
	wg.Wait()
}
