package httpapi

import (
	"testing"
)

func TestRehearsalRemovesNestedPaymentEntrypoints(t *testing.T) {
	v := map[string]any{"rows": []any{map[string]any{"fulfillment": map[string]any{"paymentUrl": "https://cashier.example.test", "paymentQr": "live", "canSubmit": true}, "price": 99}}}
	hidePaymentLinks(v)
	order := v["rows"].([]any)[0].(map[string]any)
	f := order["fulfillment"].(map[string]any)
	if f["paymentUrl"] != nil || f["paymentQr"] != nil || f["canSubmit"] != false || order["price"] != 99 {
		t.Fatal("unsafe rehearsal view")
	}
}
