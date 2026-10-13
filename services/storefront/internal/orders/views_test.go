package orders

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"gptibo/storefront/internal/rechargecopy"
)

func TestStoredRechargeMessageNeverExposesSupplierDomain(t *testing.T) {
	for _, message := range []string{"请前往 recharge.example.invalid 处理。", "请前往 //RECHARGE.INK/pay 处理。", "请联系 checkout.recharge.example.invalid 客服。"} {
		for _, status := range []string{"running", "failed", "cancelled", "rejected"} {
			for _, admin := range []bool{false, true} {
				order := Order{ID: "YMX-PRIVATE-REASON", Mode: "live", Payment: "paid", Status: "processing", Created: time.Now().UnixMilli()}
				related := viewRelations{
					supplier: []map[string]any{{"payment_status": "paid", "financial_hold": int64(0)}},
					attempts: []map[string]any{{"status": status, "public_message": message, "failure_code": "precheck_rejected", "error_category": "account"}},
				}
				view, err := (Service{}).renderView(order, admin, related)
				if err != nil {
					t.Fatal(err)
				}
				f := view["fulfillment"].(map[string]any)
				if status == "running" {
					if f["message"] != nil {
						t.Fatal("unsafe running message published")
					}
				} else if f["message"] != rechargecopy.FailureMessages["precheck_rejected"] || f["failureReason"] != f["message"] {
					t.Fatal("safe failure reason missing", f)
				}
				raw, err := json.Marshal(view)
				if err != nil || strings.Contains(strings.ToLower(string(raw)), "recharge.example.invalid") {
					t.Fatal("stored supplier domain leaked", err)
				}
			}
		}
	}
}

func TestCheckoutNeverExposesSupplierCashier(t *testing.T) {
	qr := "https://qr.alipay.com/abcdefghijklmnop"
	for _, cached := range []any{nil, qr, "https://recharge.example.invalid/payments/original?token=private-cashier-token", "https://qr.alipay.com.evil.test/abcdefghijklmnop", qr + "?redirect=https://recharge.example.invalid", qr + "\n", "http://qr.alipay.com/abcdefghijklmnop"} {
		for _, cashier := range []string{"https://recharge.example.invalid/payments/original?token=private-cashier-token", "https://recharge.example.invalid/pay/legacy"} {
			for _, state := range []string{"pending", "paid", "expired", "held", "refunded"} {
				o := Order{ID: "YMX-PRIVATE", Mode: "live", Payment: "unpaid", Status: "pending", Created: time.Now().UnixMilli()}
				if state == "paid" {
					o.Payment, o.Status = "paid", "session"
				}
				if state == "expired" {
					o.Created -= 900001
				}
				if state == "held" {
					o.Hold = 1
				}
				if state == "refunded" {
					o.Refunded = 1
				}
				related := viewRelations{supplier: []map[string]any{{"remote_id": "original", "payment_status": "pending", "payment_url": cashier, "payment_qr": cached}}}
				for _, admin := range []bool{false, true} {
					v, err := (Service{}).renderView(o, admin, related)
					if err != nil {
						t.Fatal(err)
					}
					f := v["fulfillment"].(map[string]any)
					if f["paymentUrl"] != nil {
						t.Fatal("supplier cashier published")
					}
					var want any
					if state == "pending" && cached == qr {
						want = qr
					}
					if f["paymentQr"] != want {
						t.Fatalf("state=%s cached=%v: unexpected public QR %v", state, cached, f["paymentQr"])
					}
					raw, err := json.Marshal(v)
					if err != nil {
						t.Fatal(err)
					}
					if strings.Contains(string(raw), "recharge.example.invalid") || strings.Contains(string(raw), "private-cashier-token") {
						t.Fatal("supplier URL/token leaked")
					}
				}
			}
		}
	}
}

func TestStoredUpstreamIDIsAdminOnly(t *testing.T) {
	for _, tc := range []struct {
		name, mode string
		test       int
		remote     any
		want       bool
	}{
		{"stored live order", "live", 0, "ord_original", true},
		{"not created upstream", "live", 0, nil, false},
		{"historical demo", "demo", 0, "ord_original", false},
		{"payment test", "live", 1, "ord_original", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := Order{ID: "YMX-UPSTREAM", Mode: tc.mode, Test: tc.test, Payment: "paid", Status: "session", Price: 13900, Created: time.Now().UnixMilli()}
			related := viewRelations{supplier: []map[string]any{{"remote_id": tc.remote, "payment_status": "paid", "financial_hold": int64(0)}}}
			admin, err := (Service{}).renderView(o, true, related)
			if err != nil {
				t.Fatal(err)
			}
			if tc.want {
				if admin["platformStatus"].(map[string]any)["remoteId"] != tc.remote {
					t.Fatal("stored upstream ID missing")
				}
			} else if admin["platformStatus"] != nil {
				t.Fatal("unexpected upstream snapshot")
			}
			customer, err := (Service{}).renderView(o, false, related)
			if err != nil {
				t.Fatal(err)
			}
			if _, exists := customer["platformStatus"]; exists {
				t.Fatal("admin snapshot exposed to customer")
			}
		})
	}
}

func TestCustomerRechargeFailureReasons(t *testing.T) {
	for _, tc := range []struct {
		name, orderStatus, attemptStatus, failure, category string
		hold, expired                                       bool
		want                                                any
	}{
		{"existing subscription", "failed", "failed", "account_has_subscription", "account", false, false, "账号已有订阅，请核对账号或联系客服。"},
		{"expired session", "failed", "failed", "session_invalid", "credential", false, false, "登录凭据已失效，请重新登录后提交。"},
		{"rejected submission", "failed", "rejected", "request_rejected", "", false, false, "提交未被受理，请核对资料后重新提交。"},
		{"cancelled", "failed", "cancelled", "cancelled", "", false, false, "此次充值已取消。"},
		{"category fallback", "failed", "failed", "other", "credential", false, false, "登录资料未通过验证，请核对资料或联系客服。"},
		{"unknown supplier text", "failed", "failed", "private-supplier-detail", "private-category", false, false, "充值未完成，请联系客服。"},
		{"missing reason", "failed", "failed", "", "", false, false, "充值未完成，请联系客服。"},
		{"financial hold takes priority", "failed", "failed", "account_has_subscription", "account", true, false, "平台退款金额正在核对，充值与代理收益暂时冻结，请联系客服。"},
		{"completed hides old failure", "success", "failed", "account_has_subscription", "account", false, false, nil},
		{"active retry hides failure", "processing", "running", "account_has_subscription", "account", false, false, nil},
		{"expired unknown takes priority", "processing", "unknown", "account_has_subscription", "account", false, true, "原提交长期未确认，请联系客服核对原任务，勿重新提交。"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now().UnixMilli()
			created := now
			if tc.expired {
				created -= 8 * 86400000
			}
			hold := int64(0)
			if tc.hold {
				hold = 1
			}
			order := Order{ID: "YMX-REASON", Mode: "live", Payment: "paid", Status: tc.orderStatus, Price: 13900, Created: now}
			related := viewRelations{
				supplier: []map[string]any{{"remote_id": "original", "payment_status": "paid", "financial_hold": hold}},
				attempts: []map[string]any{{"status": tc.attemptStatus, "failure_code": tc.failure, "error_category": tc.category, "created_at": created}},
			}
			for _, admin := range []bool{false, true} {
				view, err := (Service{SupplierReady: true}).renderView(order, admin, related)
				if err != nil {
					t.Fatal(err)
				}
				got := view["fulfillment"].(map[string]any)["message"]
				if got != tc.want {
					t.Fatalf("admin=%v: got %v, want %v", admin, got, tc.want)
				}
			}
		})
	}
}

func TestCustomerVerifiedRefundBeforeLocalAccounting(t *testing.T) {
	for _, tc := range []struct {
		name, status     string
		amount, verified int64
		want             int64
		hold             bool
	}{
		{"full", "refunded", 10000, 1, 10000, false},
		{"partial", "partially_refunded", 3000, 1, 3000, true},
		{"unverified", "refunded", 10000, 0, 0, true},
		{"unconfirmed", "paid", 10000, 1, 0, true},
		{"wrong full amount", "refunded", 9000, 1, 0, true},
		{"excess", "partially_refunded", 11000, 1, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := Order{ID: "YMX-REFUND", Mode: "live", Payment: "paid", Status: "failed", Price: 10000, Created: time.Now().UnixMilli()}
			related := viewRelations{supplier: []map[string]any{{"remote_id": "private-id", "payment_status": tc.status, "financial_hold": int64(1), "refunded_cents": tc.amount, "verified_at": tc.verified}}}
			customer, err := (Service{}).renderView(o, false, related)
			if err != nil || customer["refundedCents"] != tc.want || customer["refundHold"] != tc.hold {
				t.Fatal("wrong customer money/status", customer, err)
			}
			if _, exists := customer["platformStatus"]; exists {
				t.Fatal("supplier details exposed")
			}
			admin, err := (Service{}).renderView(o, true, related)
			if err != nil || admin["refundedCents"] != int64(0) || admin["refundHold"] != true {
				t.Fatal("local accounting hidden from admin", admin, err)
			}
		})
	}
}
