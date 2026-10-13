package integration_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"gptibo/storefront/internal/security"
)

func TestPayoutReviewExplainsLaterFinancialChanges(t *testing.T) {
	for _, reason := range []string{"debit", "hold"} {
		t.Run(reason, func(t *testing.T) {
			owner, _, _, h, mail := identityFixture(t)
			admin := login(t, h, mail, "admin", "")
			execSQL(t, owner, `INSERT INTO catalog_plans(id,product_id,name,price_cents,default_cost_cents) VALUES('plus','chatgpt','Plus',10000,6000)`)
			partnerID := object(t, request(h, "POST", "/api/admin/partners", map[string]any{"email": "later-debit@example.test"}, admin), 200)["id"].(string)
			partner := login(t, h, mail, "partner", "later-debit@example.test")
			now := time.Now().UnixMilli()
			execSQL(t, owner, `INSERT INTO partner_payouts(id,partner_id,amount_cents,gross_cents,status,recipient,request_key,created_at) VALUES('old-payout',$1,4000,4000,'paid','fixture','old-payout',$2)`, partnerID, now-172800000)
			for _, id := range []string{"old-order", "new-order"} {
				execSQL(t, owner, `INSERT INTO orders(id,owner_hash,idempotency_key,plan,plan_name,price_cents,cost_cents,partner_id,mode,payment_status,status,created_at,updated_at,last_event_id,payout_id) VALUES($1,'fixture',$1,'plus','Plus',10000,6000,$2,'live','paid','success',$3,$3,'fixture',CASE WHEN $1='old-order' THEN 'old-payout' ELSE NULL END)`, id, partnerID, now)
			}
			object(t, request(h, "POST", "/api/partner/payout-account", map[string]any{"phone": "13800138000", "name": "测试收款人", "version": 0}, partner), 200)
			key := security.ID()
			object(t, request(h, "POST", "/api/partner/payouts", map[string]any{"key": key, "accountVersion": 1}, partner), 201)
			var id string
			if e := owner.DB.QueryRow(context.Background(), `SELECT id FROM partner_payouts WHERE request_key=$1`, key).Scan(&id); e != nil {
				t.Fatal(e)
			}
			want := "已结算订单存在售后或资金冻结"
			if reason == "debit" {
				execSQL(t, owner, `INSERT INTO refund_cases(id,order_id,request_key,reason,status,requested_cents,approved_cents,created_at,updated_at,last_event_id) VALUES('later-refund','old-order','later-refund','fixture','refunded',1500,1500,$1,$1,'fixture')`, now+1)
				execSQL(t, owner, `INSERT INTO partner_debits(id,partner_id,refund_id,amount_cents,created_at) VALUES('later-debit',$1,'later-refund',600,$2)`, partnerID, now+1)
				want = "提现申请后新增了待抵扣款项"
			} else {
				execSQL(t, owner, `UPDATE orders SET refund_hold=1 WHERE id='old-order'`)
			}
			r := request(h, "POST", "/api/admin/partner-payouts/"+id, map[string]any{"action": "approve", "version": 0, "note": "review"}, admin)
			if r.Code != 409 || !strings.Contains(r.Body.String(), want) || !strings.Contains(r.Body.String(), "勿重复转账") {
				t.Fatalf("wrong conflict explanation: %d %s", r.Code, r.Body.String())
			}
			var unchanged bool
			if e := owner.DB.QueryRow(context.Background(), `SELECT status='pending' AND version=0 AND reviewed_at IS NULL AND amount_cents=4000 FROM partner_payouts WHERE id=$1`, id).Scan(&unchanged); e != nil || !unchanged {
				t.Fatal("failed approval changed the payout", e)
			}
			if reason == "debit" {
				if e := owner.DB.QueryRow(context.Background(), `SELECT payout_id IS NULL AND amount_cents=600 FROM partner_debits WHERE id='later-debit'`).Scan(&unchanged); e != nil || !unchanged {
					t.Fatal("failed approval consumed the later debit", e)
				}
			}
		})
	}
}
