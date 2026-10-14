package delivery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"gptibo/platform/internal/security"
	"gptibo/platform/internal/store"
)

type manualAlert struct {
	OrderID, MerchantOrder, Product, Reason string
	CreatedAt                               time.Time
}

func (n manualAlert) text() string {
	clean := func(s string) string {
		return strings.Map(func(r rune) rune {
			if r < 32 || r == 127 {
				return ' '
			}
			return r
		}, s)
	}
	return fmt.Sprintf("【订单需要人工处理】\n主站/渠道订单：%s\n平台订单：%s\n套餐：%s\n原因：%s\n时间：%s\n后台：%s\n请在订单详情核对原单。该提醒生成后订单状态可能变化，以后台最新状态为准。", clean(n.MerchantOrder), clean(n.OrderID), clean(n.Product), store.ManualReason(n.Reason), n.CreatedAt.In(time.FixedZone("Asia/Shanghai", 8*60*60)).Format("2006-01-02 15:04:05"), clean(os.Getenv("PUBLIC_ORIGIN")))
}

type feishuSender struct {
	AppID, Secret, Recipient string
	Client                   *http.Client
}

func newFeishuSender() feishuSender {
	return feishuSender{os.Getenv("FEISHU_APP_ID"), os.Getenv("FEISHU_APP_SECRET"), os.Getenv("FEISHU_MANUAL_RECIPIENT"), &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (f feishuSender) post(ctx context.Context, path string, body any, token string) (map[string]json.RawMessage, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", "https://open.feishu.cn/open-apis"+path, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := f.Client.Do(req)
	if err != nil {
		return nil, errors.New("feishu_network_failed")
	}
	defer resp.Body.Close()
	b, err = io.ReadAll(io.LimitReader(resp.Body, 65537))
	if err != nil || len(b) > 65536 {
		return nil, errors.New("feishu_invalid_response")
	}
	var data map[string]json.RawMessage
	if json.Unmarshal(b, &data) != nil {
		return nil, errors.New("feishu_invalid_response")
	}
	var code *int
	if json.Unmarshal(data["code"], &code) != nil || code == nil || *code != 0 || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, errors.New("feishu_rejected")
	}
	return data, nil
}

func (f feishuSender) send(ctx context.Context, n manualAlert) (string, error) {
	if f.AppID == "" || f.Secret == "" || !regexp.MustCompile(`^(ou|oc)_[a-z0-9]+$`).MatchString(f.Recipient) {
		return "", errors.New("feishu_not_configured")
	}
	auth, err := f.post(ctx, "/auth/v3/tenant_access_token/internal", map[string]string{"app_id": f.AppID, "app_secret": f.Secret}, "")
	if err != nil {
		return "", err
	}
	var token string
	if json.Unmarshal(auth["tenant_access_token"], &token) != nil || token == "" {
		return "", errors.New("feishu_token_missing")
	}
	kind := "open_id"
	if strings.HasPrefix(f.Recipient, "oc_") {
		kind = "chat_id"
	}
	content, _ := json.Marshal(map[string]string{"text": n.text()})
	// Feishu uuid covers ambiguous HTTP retries; the database handles long-term deduplication.
	result, err := f.post(ctx, "/im/v1/messages?receive_id_type="+kind, map[string]string{"receive_id": f.Recipient, "msg_type": "text", "content": string(content), "uuid": "platform-manual-" + security.Hash(n.OrderID)[:32]}, token)
	if err != nil {
		return "", err
	}
	var data struct {
		MessageID string `json:"message_id"`
	}
	if json.Unmarshal(result["data"], &data) != nil || data.MessageID == "" {
		return "", errors.New("feishu_message_missing")
	}
	return data.MessageID, nil
}

// This lane has its own timer and HTTP deadline; neither payment execution nor
// slow upstream calls can block a human-action reminder (or vice versa).
func (w *Worker) manualAlerts(ctx context.Context) {
	sender := newFeishuSender()
	timer := time.NewTicker(3 * time.Second)
	defer timer.Stop()
	for {
		for i := 0; i < 8; i++ {
			worked, err := w.manualAlert(ctx, sender.send)
			if err != nil && ctx.Err() == nil {
				slog.Error("manual alert database failure")
			}
			if err != nil || !worked {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}

func (w *Worker) manualAlert(ctx context.Context, send func(context.Context, manualAlert) (string, error)) (bool, error) {
	// Never notify about a pending reminder after the order has already settled.
	_, err := w.Store.DB.Exec(ctx, `UPDATE manual_alerts n SET status='cancelled',lease_token='',lease_until=NULL
 FROM orders o WHERE n.order_id=o.id AND n.status='pending' AND o.status IN ('succeeded','failed') AND (n.lease_until IS NULL OR n.lease_until<now())`)
	if err != nil {
		return false, err
	}
	token := security.Token()
	var n manualAlert
	var attempts int
	err = w.Store.DB.QueryRow(ctx, `WITH candidate AS (
 SELECT n.order_id FROM manual_alerts n JOIN orders o ON o.id=n.order_id
 WHERE n.status='pending' AND n.next_at<=now() AND (n.lease_until IS NULL OR n.lease_until<now()) AND o.status IN ('queued','running','unknown')
 ORDER BY n.next_at FOR UPDATE OF n SKIP LOCKED LIMIT 1)
 UPDATE manual_alerts n SET lease_token=$1,lease_until=now()+interval '60 seconds' FROM candidate c,orders o
 WHERE n.order_id=c.order_id AND o.id=n.order_id RETURNING n.order_id,o.merchant_order_no,o.product_code,n.reason,n.created_at,n.attempts`, token).Scan(&n.OrderID, &n.MerchantOrder, &n.Product, &n.Reason, &n.CreatedAt, &attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	messageID, sendErr := send(callCtx, n)
	cancel()
	status, lastError := "sent", ""
	if sendErr != nil {
		status, lastError = "pending", "feishu_delivery_failed"
		if sendErr.Error() == "feishu_not_configured" {
			lastError = "feishu_not_configured"
		}
		slog.Warn("manual order reminder pending retry", "order_id", n.OrderID, "reason", lastError)
	}
	delay := min(time.Duration(1<<min(attempts, 7))*30*time.Second, time.Hour)
	_, err = w.Store.DB.Exec(ctx, `UPDATE manual_alerts SET status=$3,attempts=attempts+1,last_error=$4,message_id=$5,next_at=$6,lease_token='',lease_until=NULL,sent_at=CASE WHEN $3='sent' THEN now() ELSE NULL END WHERE order_id=$1 AND lease_token=$2 AND status='pending'`, n.OrderID, token, status, lastError, messageID, time.Now().Add(delay))
	return true, err
}
