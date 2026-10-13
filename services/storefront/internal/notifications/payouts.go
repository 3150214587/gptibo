package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"gptibo/storefront/internal/orders"
	"gptibo/storefront/internal/security"
	"gptibo/storefront/internal/store"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

type Feishu struct {
	App, Secret, Recipient, Origin string
	HTTP                           *http.Client
}

func NewFeishu(app, secret, recipient, origin string) (*Feishu, error) {
	if app == "" || secret == "" || !regexp.MustCompile(`^(ou|oc)_[a-z0-9]+$`).MatchString(recipient) {
		return nil, errors.New("invalid payout notification configuration")
	}
	return &Feishu{app, secret, recipient, origin, &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (f *Feishu) request(ctx context.Context, path string, payload any, token string) (string, error) {
	raw, _ := json.Marshal(payload)
	r, e := http.NewRequestWithContext(ctx, "POST", "https://open.feishu.cn/open-apis"+path, strings.NewReader(string(raw)))
	if e != nil {
		return "", e
	}
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	res, e := f.HTTP.Do(r)
	if e != nil {
		return "", errors.New("notification service unavailable")
	}
	defer res.Body.Close()
	b, e := io.ReadAll(io.LimitReader(res.Body, 64001))
	if e != nil || len(b) > 64000 || res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", errors.New("notification response rejected")
	}
	var out struct {
		Code  *int   `json:"code"`
		Token string `json:"tenant_access_token"`
	}
	if json.Unmarshal(b, &out) != nil || out.Code == nil || *out.Code != 0 {
		return "", errors.New("notification response rejected")
	}
	return out.Token, nil
}

type Payout struct {
	ID, Name, Slug, Recipient         string
	Amount, Gross, Deduction, Created int64
}

func Money(n int64) string { return fmt.Sprintf("¥%d.%02d", n/100, n%100) }
func (f *Feishu) Send(ctx context.Context, p Payout) error {
	token, e := f.request(ctx, "/auth/v3/tenant_access_token/internal", map[string]any{"app_id": f.App, "app_secret": f.Secret}, "")
	if e != nil {
		return e
	}
	if token == "" {
		return errors.New("notification token unavailable")
	}
	amount := Money(p.Amount)
	if p.Deduction > 0 {
		amount += "（抵扣售后退款 " + Money(p.Deduction) + "，抵扣前 " + Money(p.Gross) + "）"
	}
	message := strings.Join([]string{"【提现申请】", "代理：" + p.Name + "（" + p.Slug + "）", "金额：" + amount, "收款：" + p.Recipient, "时间：" + time.UnixMilli(p.Created).In(time.FixedZone("Asia/Shanghai", 8*3600)).Format("2006/01/02 15:04:05"), "后台：" + f.Origin + "/admin/partners"}, "\n")
	content, _ := json.Marshal(map[string]string{"text": message})
	kind := "open_id"
	if strings.HasPrefix(f.Recipient, "oc_") {
		kind = "chat_id"
	}
	_, e = f.request(ctx, "/im/v1/messages?receive_id_type="+kind, map[string]any{"receive_id": f.Recipient, "msg_type": "text", "content": string(content), "uuid": "ymx-payout-" + p.ID}, token)
	return e
}

type PayoutSender interface {
	Send(context.Context, Payout) error
}
type Service struct {
	Store  *store.Store
	Sender PayoutSender
}

func (s Service) Reconcile(ctx context.Context) error {
	if s.Sender == nil {
		return nil
	}
	var first error
	for range 5 {
		now, lease := time.Now().UnixMilli(), security.ID()
		rows, e := orders.Rows(ctx, s.Store.DB, `UPDATE payout_notifications SET lock_token=$1,locked_until=$2 WHERE payout_id=(SELECT payout_id FROM payout_notifications WHERE delivered_at IS NULL AND next_attempt_at<=$3 AND locked_until<=$3 ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1) RETURNING payout_id,attempt_count`, lease, now+60000, now)
		if e != nil {
			return e
		}
		if len(rows) == 0 {
			break
		}
		id := rows[0]["payout_id"].(string)
		attempt := rows[0]["attempt_count"].(int64)
		var p Payout
		p.ID = id
		e = s.Store.DB.QueryRow(ctx, `SELECT p.amount_cents,p.gross_cents,p.deduction_cents,p.recipient,p.created_at,t.name,t.slug FROM partner_payouts p JOIN partners t ON t.id=p.partner_id WHERE p.id=$1`, id).Scan(&p.Amount, &p.Gross, &p.Deduction, &p.Recipient, &p.Created, &p.Name, &p.Slug)
		if e == nil {
			e = s.Sender.Send(ctx, p)
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		if e == nil {
			_, e = s.Store.DB.Exec(cleanup, `UPDATE payout_notifications SET delivered_at=$1,locked_until=0,lock_token='',attempt_count=attempt_count+1 WHERE payout_id=$2 AND lock_token=$3 AND locked_until>$1`, time.Now().UnixMilli(), id, lease)
		} else {
			_, _ = s.Store.DB.Exec(cleanup, `UPDATE payout_notifications SET locked_until=0,lock_token='',next_attempt_at=$1,attempt_count=attempt_count+1 WHERE payout_id=$2 AND lock_token=$3`, time.Now().UnixMilli()+min(int64(3600000), int64(60000)<<min(attempt, int64(6))), id, lease)
		}
		cancel()
		if e != nil && first == nil {
			first = e
		}
	}
	return first
}
