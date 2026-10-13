package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

type configureTransport func(*http.Request) (*http.Response, error)

func (f configureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestConfigureModerationPreservesWebhookAndScopesAdministratorMenu(t *testing.T) {
	for _, allowed := range [][]string{{"message", "callback_query", "chat_member"}, nil} {
		c, _ := NewClient("999:test-only", "ExampleBot", strings.Repeat("a", 64), []int64{-42})
		seen := []string{}
		c.HTTP = &http.Client{Transport: configureTransport(func(r *http.Request) (*http.Response, error) {
			method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			seen = append(seen, method)
			var data map[string]any
			_ = json.NewDecoder(r.Body).Decode(&data)
			var result any = true
			switch method {
			case "getWebhookInfo":
				result = map[string]any{"url": "https://shop.example.invalid/api/telegram/webhook", "allowed_updates": allowed}
			case "setWebhook":
				if data["drop_pending_updates"] != false || data["secret_token"] != c.Secret || data["max_connections"] != float64(1) {
					t.Fatal("webhook settings lost")
				}
				kinds, ok := data["allowed_updates"].([]any)
				if !ok {
					t.Fatal("null subscription")
				}
				if len(allowed) > 0 && (!slices.Contains(kinds, any("edited_message")) || !slices.Contains(kinds, any("chat_member"))) {
					t.Fatal("existing update subscriptions lost")
				}
			case "setMyCommands":
				scope := data["scope"].(map[string]any)
				if scope["type"] != "chat_administrators" || scope["chat_id"] != float64(-42) {
					t.Fatal("wrong menu scope")
				}
				commands := []string{}
				for _, item := range data["commands"].([]any) {
					commands = append(commands, item.(map[string]any)["command"].(string))
				}
				for _, command := range []string{"apply", "mute", "unmute", "blockword", "unblockword", "blockwords"} {
					if !slices.Contains(commands, command) {
						t.Fatal("missing administrator command", command)
					}
				}
			default:
				t.Fatal("unexpected mutation", method)
			}
			w := httptest.NewRecorder()
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
			return w.Result(), nil
		})}
		if err := c.ConfigureModeration(context.Background(), "https://shop.example.invalid/api/telegram/webhook"); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(seen, []string{"getWebhookInfo", "setWebhook", "setMyCommands"}) {
			t.Fatal("unexpected operations", seen)
		}
	}
}
func TestConfigureModerationRejectsAnotherWebhook(t *testing.T) {
	c, _ := NewClient("999:test-only", "ExampleBot", strings.Repeat("a", 64), []int64{-42})
	calls := 0
	c.HTTP = &http.Client{Transport: configureTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		w := httptest.NewRecorder()
		w.WriteString(`{"ok":true,"result":{"url":"https://other.example/webhook"}}`)
		return w.Result(), nil
	})}
	if err := c.ConfigureModeration(context.Background(), "https://shop.example.invalid/api/telegram/webhook"); err == nil || calls != 1 {
		t.Fatal("another webhook overwritten")
	}
}
