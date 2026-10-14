package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"gptibo/platform/internal/commerce"
	"gptibo/platform/internal/store"
	"gptibo/platform/internal/testutil"
)

type alertTransport func(*http.Request) (*http.Response, error)

func (f alertTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestFeishuSenderValidationAndStableRetry(t *testing.T) {
	for _, mode := range []string{"success", "bad_config", "http_error", "api_error", "missing_code", "null_code", "missing_token", "missing_message", "oversize"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			var uuids []string
			f := feishuSender{AppID: "app_test", Secret: "synthetic-secret", Recipient: "oc_test"}
			if mode == "bad_config" {
				f.Recipient = "not_a_recipient"
			}
			f.Client = &http.Client{Transport: alertTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.Host != "open.feishu.cn" || r.Method != "POST" {
					t.Fatal("wrong endpoint")
				}
				var body map[string]string
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				result := `{"code":0,"tenant_access_token":"synthetic-token"}`
				status := 200
				if strings.HasSuffix(r.URL.Path, "/messages") {
					if r.Header.Get("Authorization") != "Bearer synthetic-token" || body["receive_id"] != "oc_test" || r.URL.Query().Get("receive_id_type") != "chat_id" {
						t.Fatal("wrong target or identity")
					}
					var content map[string]string
					_ = json.Unmarshal([]byte(body["content"]), &content)
					if !strings.Contains(content["text"], "YMX-TEST") || !strings.Contains(content["text"], "验证码") || strings.Contains(content["text"], "checkout.stripe.com") {
						t.Fatal("unsafe or incomplete message")
					}
					uuids = append(uuids, body["uuid"])
					result = `{"code":0,"data":{"message_id":"om_test"}}`
					switch mode {
					case "http_error":
						status = 500
					case "api_error":
						result = `{"code":999,"msg":"sensitive details must not enter logs"}`
					case "missing_code":
						result = `{"data":{"message_id":"om_test"}}`
					case "null_code":
						result = `{"code":null,"data":{"message_id":"om_test"}}`
					case "missing_message":
						result = `{"code":0,"data":{}}`
					case "oversize":
						result = strings.Repeat("x", 65537)
					}
				} else if mode == "missing_token" {
					result = `{"code":0}`
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(result))}, nil
			})}
			n := manualAlert{OrderID: "ord_test", MerchantOrder: "YMX-TEST", Product: "x-3m", Reason: "payment_action_required", CreatedAt: time.Now()}
			for i := 0; i < 2; i++ {
				id, err := f.send(context.Background(), n)
				if mode == "success" {
					if err != nil || id != "om_test" {
						t.Fatal(id, err)
					}
				} else if err == nil {
					t.Fatal("accepted failed delivery")
				}
			}
			if mode == "bad_config" && calls != 0 {
				t.Fatal("called remote with invalid config")
			}
			if len(uuids) == 2 && (uuids[0] != uuids[1] || len(uuids[0]) > 50) {
				t.Fatal("retry lost idempotency")
			}
		})
	}
}

func alertFixture(t *testing.T) *Worker {
	t.Helper()
	s := testutil.Database(t)
	_, err := s.DB.Exec(context.Background(), `INSERT INTO users(id,email,name,role,status) VALUES('user','alerts@example.com','test','channel','active'); INSERT INTO wallets(user_id) VALUES('user');
 INSERT INTO orders(id,user_id,merchant_order_no,idempotency_key,request_hash,product_code,business,supply_minor,status,stage,lease_token,config_cipher) VALUES('order','user','YMX-TEST','idem','hash','x-3m','x',1700,'unknown','submitted','lease','synthetic')`)
	if err != nil {
		t.Fatal(err)
	}
	return &Worker{Store: s, Commerce: &commerce.Service{Store: s}}
}

func TestManualAlertTransactionDedupRetryAndSettlement(t *testing.T) {
	w := alertFixture(t)
	ctx := context.Background()
	// Ordinary waiting never creates a human notification.
	if err := w.Commerce.Finish(ctx, "order", "lease", "unknown", "submitted", "payment_pending", ""); err != nil {
		t.Fatal(err)
	}
	var count int
	_ = w.Store.DB.QueryRow(ctx, "SELECT count(*) FROM manual_alerts").Scan(&count)
	if count != 0 {
		t.Fatal("ordinary poll notified")
	}
	// A rolled-back transition cannot leave an orphan alert.
	_ = w.Store.Tx(ctx, func(tx pgx.Tx) error {
		if err := store.EnqueueManualAlert(ctx, tx, "order", "payment_action_required"); err != nil {
			t.Fatal(err)
		}
		return errors.New("rollback")
	})
	_ = w.Store.DB.QueryRow(ctx, "SELECT count(*) FROM manual_alerts").Scan(&count)
	if count != 0 {
		t.Fatal("rolled back transition notified")
	}
	for _, reason := range []string{"payment_action_required", "payment_action_required", "payment_manual_review"} {
		_, err := w.Store.DB.Exec(ctx, "UPDATE orders SET lease_token='lease' WHERE id='order'")
		if err != nil {
			t.Fatal(err)
		}
		if err = w.Commerce.Finish(ctx, "order", "lease", "unknown", "submitted", reason, ""); err != nil {
			t.Fatal(err)
		}
	}
	_ = w.Store.DB.QueryRow(ctx, "SELECT count(*) FROM manual_alerts").Scan(&count)
	if count != 1 {
		t.Fatal("duplicate alerts", count)
	}
	sends := 0
	send := func(context.Context, manualAlert) (string, error) {
		sends++
		if sends == 1 {
			return "", errors.New("remote outage")
		}
		return "om_delivered", nil
	}
	if worked, err := w.manualAlert(ctx, send); !worked || err != nil {
		t.Fatal(worked, err)
	}
	var status, lastError string
	var attempts int
	_ = w.Store.DB.QueryRow(ctx, "SELECT status,attempts,last_error FROM manual_alerts").Scan(&status, &attempts, &lastError)
	if status != "pending" || attempts != 1 || lastError != "feishu_delivery_failed" {
		t.Fatal(status, attempts, lastError)
	}
	if worked, err := w.manualAlert(ctx, send); worked || err != nil {
		t.Fatal("ignored backoff", err)
	}
	_, _ = w.Store.DB.Exec(ctx, "UPDATE manual_alerts SET next_at=now()")
	if worked, err := w.manualAlert(ctx, send); !worked || err != nil {
		t.Fatal(worked, err)
	}
	if worked, err := w.manualAlert(ctx, send); worked || err != nil || sends != 2 {
		t.Fatal("sent again", err, sends)
	}
	// Already settled before delivery: cancel rather than send an obsolete alert.
	_, err := w.Store.DB.Exec(ctx, "UPDATE manual_alerts SET status='pending',next_at=now(); UPDATE orders SET status='succeeded'")
	if err != nil {
		t.Fatal(err)
	}
	if worked, err := w.manualAlert(ctx, send); worked || err != nil || sends != 2 {
		t.Fatal("settled order notified", err)
	}
	_ = w.Store.DB.QueryRow(ctx, "SELECT status FROM manual_alerts").Scan(&status)
	if status != "cancelled" {
		t.Fatal(status)
	}
}

func TestManualAlertLeaseAndExpiredClaim(t *testing.T) {
	w := alertFixture(t)
	ctx := context.Background()
	if err := w.Store.Tx(ctx, func(tx pgx.Tx) error { return store.EnqueueManualAlert(ctx, tx, "order", "card_pair_declined") }); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := w.manualAlert(ctx, func(context.Context, manualAlert) (string, error) { close(entered); <-release; return "om_sent", nil })
		done <- err
	}()
	<-entered
	worked, err := w.manualAlert(ctx, func(context.Context, manualAlert) (string, error) {
		t.Error("double claimed live notification")
		return "", nil
	})
	close(release)
	if worked || err != nil {
		t.Fatal(worked, err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	_, err = w.Store.DB.Exec(ctx, "UPDATE manual_alerts SET status='pending',next_at=now(),lease_token='crashed',lease_until=now()-interval '1 second'")
	if err != nil {
		t.Fatal(err)
	}
	if worked, err = w.manualAlert(ctx, func(context.Context, manualAlert) (string, error) { return "om_sent", nil }); !worked || err != nil {
		t.Fatal("expired lease not reclaimed", worked, err)
	}
}
