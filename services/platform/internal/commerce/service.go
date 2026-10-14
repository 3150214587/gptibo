package commerce

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"gptibo/platform/internal/security"
	"gptibo/platform/internal/store"
	"regexp"
	"strings"
	"time"
)

type Error struct {
	Code, Message string
	Status        int
}

func (e *Error) Error() string                    { return e.Code }
func Fail(code, message string, status int) error { return &Error{code, message, status} }

type Credential struct {
	Mode        string `json:"mode"`
	Session     string `json:"session,omitempty"`
	AccessToken string `json:"accessToken,omitempty"`
	Email       string `json:"email,omitempty"`
	Password    string `json:"password,omitempty"`
}
type Request struct {
	MerchantOrderNo      string     `json:"merchant_order_no"`
	ProductCode          string     `json:"product_code"`
	Recipient            string     `json:"recipient"`
	Credential           Credential `json:"credential"`
	ConfirmedAccount     string     `json:"confirmed_account,omitempty"`
	ConfirmedRecipientID string     `json:"confirmed_recipient_id,omitempty"`
	ExpectedSupplyMinor  *int64     `json:"expected_supply_minor,omitempty"`
}
type CardRule struct {
	ID                   string `json:"id"`
	Business             string `json:"business"`
	ProductCode          string `json:"product_code"`
	CardProduct          string `json:"card_product"`
	CardID               int64  `json:"card_id"`
	BackupCardID         int64  `json:"backup_card_id"`
	AutoOpen             bool   `json:"auto_open"`
	UseProviderLimits    bool   `json:"use_provider_limits"`
	RefillThresholdMinor int64  `json:"refill_threshold_minor"`
	OpenAmountMinor      int64  `json:"open_amount_minor"`
	ReplaceOnDecline     bool   `json:"replace_on_decline"`
	MaxReplacements      int    `json:"max_replacements"`
	FundingMinor         int64  `json:"funding_minor"`
	MaxOrderMinor        int64  `json:"max_order_minor"`
	DailyLimitMinor      int64  `json:"daily_limit_minor"`
	Enabled              bool   `json:"enabled"`
	Version              int64  `json:"version"`
	MaxOpenFeeMinor      int64  `json:"max_open_fee_minor"`
}
type Billing struct {
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Email     string `json:"email"`
	Country   string `json:"country"`
	Line1     string `json:"line1"`
	City      string `json:"city"`
	State     string `json:"state"`
	Postal    string `json:"postal"`
}
type CardResolution struct {
	Reference    string `json:"reference"`
	Status       string `json:"status"`
	CardID       int64  `json:"card_id"`
	Confirmation string `json:"confirmation"`
	Note         string `json:"note"`
}
type ProviderConfig struct {
	APIBase         string  `json:"api_base"`
	Environment     string  `json:"environment"`
	APIKey          string  `json:"api_key"`
	AppID           string  `json:"app_id"`
	WritesEnabled   bool    `json:"writes_enabled"`
	StripeKey       string  `json:"stripe_key"`
	XBearer         string  `json:"x_bearer"`
	PaymentCountry  string  `json:"payment_country"`
	PaymentCurrency string  `json:"payment_currency"`
	Billing         Billing `json:"billing"`
}
type Snapshot struct {
	ProductCode          string         `json:"product_code"`
	Business             string         `json:"business"`
	Plan                 string         `json:"plan"`
	ProductConfig        map[string]any `json:"product_config"`
	Rule                 CardRule       `json:"rule"`
	Provider             ProviderConfig `json:"provider"`
	ConfirmedAccount     string         `json:"confirmed_account,omitempty"`
	ConfirmedRecipientID string         `json:"confirmed_recipient_id,omitempty"`
}
type Order struct {
	ID                  string    `json:"id"`
	UserID              string    `json:"user_id"`
	MerchantOrderNo     string    `json:"merchant_order_no"`
	ProductCode         string    `json:"product_code"`
	Business            string    `json:"business"`
	SupplyMinor         int64     `json:"supply_minor"`
	Status              string    `json:"status"`
	Recipient           string    `json:"recipient"`
	AccountLabel        string    `json:"account_label"`
	Stage               string    `json:"stage"`
	FailureCode         string    `json:"failure_code"`
	Receipt             string    `json:"receipt"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
	ManualPayment       bool      `json:"manual_payment"`
	CaptchaStatus       string    `json:"captcha_status"`
	CaptchaFailure      string    `json:"captcha_failure"`
	ChallengeType       string    `json:"challenge_type"`
	ManualInvoiceStatus string    `json:"manual_invoice_status"`
}

const OrderColumns = "id,user_id,merchant_order_no,product_code,business,supply_minor,status,recipient,account_label,stage,failure_code,receipt,created_at,updated_at,(manual_payment_at IS NOT NULL) AS manual_payment," +
	"COALESCE((SELECT CASE WHEN c.status IN ('checking','solving') AND c.updated_at<now()-interval '5 minutes' THEN 'unconfirmed' ELSE c.status END FROM payment_captcha_attempts c WHERE c.order_id=orders.id),'') AS captcha_status," +
	"COALESCE((SELECT failure_code FROM payment_captcha_attempts c WHERE c.order_id=orders.id),'') AS captcha_failure," +
	"COALESCE((SELECT challenge_type FROM payment_captcha_attempts c WHERE c.order_id=orders.id),'') AS challenge_type," +
	"COALESCE((SELECT status FROM manual_payment_invoices i WHERE i.order_id=orders.id ORDER BY i.created_at DESC,i.id DESC LIMIT 1),'') AS manual_invoice_status"

func ScanOrder(row pgx.Row) (Order, error) {
	var o Order
	e := row.Scan(&o.ID, &o.UserID, &o.MerchantOrderNo, &o.ProductCode, &o.Business, &o.SupplyMinor, &o.Status, &o.Recipient, &o.AccountLabel, &o.Stage, &o.FailureCode, &o.Receipt, &o.CreatedAt, &o.UpdatedAt, &o.ManualPayment, &o.CaptchaStatus, &o.CaptchaFailure, &o.ChallengeType, &o.ManualInvoiceStatus)
	return o, e
}

type Service struct{ Store *store.Store }

var refPattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]{8,128}$`)
var recipientPattern = regexp.MustCompile(`^[a-z0-9_]{1,15}$`)

func Validate(r *Request, business, idem string) error {
	if !refPattern.MatchString(idem) || len(r.MerchantOrderNo) < 1 || len(r.MerchantOrderNo) > 128 || !regexp.MustCompile(`^[A-Za-z0-9_.:-]+$`).MatchString(r.MerchantOrderNo) {
		return Fail("invalid_reference", "订单号或幂等键格式无效", 400)
	}
	if business == "x" {
		r.Recipient = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(r.Recipient), "@"))
		if !recipientPattern.MatchString(r.Recipient) {
			return Fail("invalid_recipient", "X 用户名格式无效", 400)
		}
		if r.ConfirmedRecipientID != "" && !regexp.MustCompile(`^\d{1,25}$`).MatchString(r.ConfirmedRecipientID) {
			return Fail("invalid_recipient", "X 账号身份资料无效", 400)
		}
		r.Credential = Credential{}
		return nil
	}
	r.Recipient = ""
	r.ConfirmedRecipientID = ""
	r.ConfirmedAccount = strings.ToLower(strings.TrimSpace(r.ConfirmedAccount))
	if r.ConfirmedAccount != "" && (len(r.ConfirmedAccount) > 254 || !strings.Contains(r.ConfirmedAccount, "@")) {
		return Fail("invalid_account", "确认账号格式无效", 400)
	}
	c := r.Credential
	valid := c.Mode == "session" && len(c.Session) > 0 && len(c.Session) <= 65536 && c.AccessToken == "" && c.Email == "" && c.Password == "" || c.Mode == "access_token" && len(c.AccessToken) > 0 && len(c.AccessToken) <= 16384 && c.Session == "" && c.Email == "" && c.Password == "" || c.Mode == "mailbox" && strings.Contains(c.Email, "@") && len(c.Email) <= 254 && len(c.Password) > 0 && len(c.Password) <= 1024 && c.Session == "" && c.AccessToken == ""
	if !valid {
		return Fail("invalid_credential", "请提供一种完整的充值资料", 400)
	}
	return nil
}
func (s *Service) Provider(ctx context.Context) (ProviderConfig, error) {
	var encrypted string
	var p ProviderConfig
	e := s.Store.DB.QueryRow(ctx, "SELECT payload_cipher FROM settings WHERE id='provider'").Scan(&encrypted)
	if e != nil {
		if !errors.Is(e, pgx.ErrNoRows) {
			return p, e
		}
		return p, Fail("provider_missing", "请先配置卡台", 409)
	}
	b, e := s.Store.Vault.Open("setting:provider", encrypted)
	if e != nil {
		return p, e
	}
	e = json.Unmarshal(b, &p)
	return p, e
}
func (s *Service) Create(ctx context.Context, user, idem string, r Request) (Order, bool, error) {
	var out Order
	created := false
	e := s.Store.Tx(ctx, func(tx pgx.Tx) error {
		var status string
		if e := tx.QueryRow(ctx, "SELECT status FROM users WHERE id=$1 AND role='channel' FOR UPDATE", user).Scan(&status); e != nil || status != "active" {
			return Fail("channel_disabled", "渠道账户不可用", 403)
		}
		var business string
		if e := tx.QueryRow(ctx, "SELECT business FROM products WHERE code=$1", r.ProductCode).Scan(&business); e != nil {
			return Fail("product_missing", "商品不存在", 404)
		}
		if e := Validate(&r, business, idem); e != nil {
			return e
		}
		body, _ := json.Marshal(r)
		digest := "h2:" + s.Store.Vault.Digest("order-request", string(body))
		rows, e := tx.Query(ctx, "SELECT id,request_hash,idempotency_key FROM orders WHERE user_id=$1 AND (merchant_order_no=$2 OR idempotency_key=$3)", user, r.MerchantOrderNo, idem)
		if e != nil {
			return e
		}
		var prevID, prevHash, prevKey string
		count := 0
		for rows.Next() {
			if e = rows.Scan(&prevID, &prevHash, &prevKey); e != nil {
				rows.Close()
				return e
			}
			count++
		}
		rows.Close()
		if count > 0 {
			if count != 1 || (prevHash != digest && prevHash != security.Hash(string(body))) || prevKey != idem {
				return Fail("idempotency_conflict", "订单号或幂等键已绑定其他请求", 409)
			}
			out, e = ScanOrder(tx.QueryRow(ctx, "SELECT "+OrderColumns+" FROM orders WHERE id=$1", prevID))
			return e
		}
		var snap Snapshot
		snap.ConfirmedAccount = r.ConfirmedAccount
		snap.ConfirmedRecipientID = r.ConfirmedRecipientID
		var price int64
		var config []byte
		var enabled bool
		e = tx.QueryRow(ctx, "SELECT p.code,p.business,p.upstream_plan,p.config,COALESCE(cp.supply_minor,p.supply_minor),p.enabled FROM products p LEFT JOIN channel_prices cp ON cp.product_code=p.code AND cp.user_id=$2 WHERE p.code=$1", r.ProductCode, user).Scan(&snap.ProductCode, &snap.Business, &snap.Plan, &config, &price, &enabled)
		if e != nil {
			return e
		}
		if !enabled || price <= 0 {
			return Fail("product_disabled", "商品暂未开放", 409)
		}
		if r.ExpectedSupplyMinor != nil && *r.ExpectedSupplyMinor != price {
			return Fail("price_changed", "供货价已变化，请刷新商品并重新确认", 409)
		}
		if e = json.Unmarshal(config, &snap.ProductConfig); e != nil {
			return e
		}
		var providerCipher string
		if e = tx.QueryRow(ctx, "SELECT payload_cipher FROM settings WHERE id='provider'").Scan(&providerCipher); e != nil {
			return Fail("provider_missing", "请先配置卡台", 409)
		}
		providerData, e := s.Store.Vault.Open("setting:provider", providerCipher)
		if e != nil {
			return e
		}
		if e = json.Unmarshal(providerData, &snap.Provider); e != nil {
			return e
		}
		if !snap.Provider.WritesEnabled {
			return Fail("execution_disabled", "业务执行已暂停", 409)
		}
		if business == "gpt" && snap.Plan == "" {
			return Fail("plan_missing", "上游套餐未配置", 409)
		}
		e = tx.QueryRow(ctx, "SELECT id,business,product_code,card_product,card_id,auto_open,funding_minor,max_order_minor,daily_limit_minor,enabled,version,max_open_fee_minor,use_provider_limits,refill_threshold_minor,open_amount_minor,replace_on_decline,max_replacements,backup_card_id FROM card_rules WHERE business=$1 AND product_code IN ('',$2) ORDER BY (product_code=$2) DESC LIMIT 1", business, r.ProductCode).Scan(&snap.Rule.ID, &snap.Rule.Business, &snap.Rule.ProductCode, &snap.Rule.CardProduct, &snap.Rule.CardID, &snap.Rule.AutoOpen, &snap.Rule.FundingMinor, &snap.Rule.MaxOrderMinor, &snap.Rule.DailyLimitMinor, &snap.Rule.Enabled, &snap.Rule.Version, &snap.Rule.MaxOpenFeeMinor, &snap.Rule.UseProviderLimits, &snap.Rule.RefillThresholdMinor, &snap.Rule.OpenAmountMinor, &snap.Rule.ReplaceOnDecline, &snap.Rule.MaxReplacements, &snap.Rule.BackupCardID)
		if e != nil {
			return Fail("card_rule_missing", "该商品尚未配置卡规则", 409)
		}
		if !snap.Rule.Enabled {
			return Fail("card_rule_disabled", "该商品卡规则已停用", 409)
		}
		if e = ExecutionReady(snap); e != nil {
			return e
		}
		var workerReady bool
		if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM worker_heartbeats WHERE updated_at>now()-interval '60 seconds')").Scan(&workerReady); e != nil {
			return e
		}
		if !workerReady {
			return Fail("worker_unavailable", "充值服务暂不可用，请稍后下单", 503)
		}
		if snap.Rule.CardID <= 0 && !snap.Rule.AutoOpen {
			return Fail("card_missing", "请指定卡片或允许自动开卡", 409)
		}
		var available int64
		if e = tx.QueryRow(ctx, "SELECT available_minor FROM wallets WHERE user_id=$1 FOR UPDATE", user).Scan(&available); e != nil {
			return e
		}
		if available < price {
			return Fail("insufficient_balance", "采购余额不足", 409)
		}
		id := security.ID("ord")
		snapshot, _ := json.Marshal(snap)
		credential, _ := json.Marshal(r.Credential)
		out, e = ScanOrder(tx.QueryRow(ctx, "INSERT INTO orders(id,user_id,merchant_order_no,idempotency_key,request_hash,product_code,business,supply_minor,recipient,credential_cipher,config_cipher) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING "+OrderColumns, id, user, r.MerchantOrderNo, idem, digest, r.ProductCode, business, price, r.Recipient, s.Store.Vault.Seal("credential:"+id, credential), s.Store.Vault.Seal("order:"+id, snapshot)))
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, "INSERT INTO ledger(id,user_id,order_id,kind,available_delta,frozen_delta,reference,note,actor_id) VALUES($1,$2,$3,'reserve',$4,$5,$6,'订单冻结', $2)", security.ID("led"), user, id, -price, price, id+":reserve")
		if e != nil {
			return e
		}
		created = true
		return store.Audit(ctx, tx, user, "create_order", id, r.ProductCode)
	})
	return out, created, e
}
func (s *Service) Credit(ctx context.Context, actor, user, reference, note string, amount int64) error {
	if amount <= 0 || amount > 100000000000 || !refPattern.MatchString(reference) || len(note) < 2 || len(note) > 500 {
		return Fail("invalid_credit", "金额、凭证号或入账说明无效", 400)
	}
	return s.Store.Tx(ctx, func(tx pgx.Tx) error {
		var status string
		e := tx.QueryRow(ctx, "SELECT status FROM users WHERE id=$1 AND role='channel' FOR UPDATE", user).Scan(&status)
		if e != nil || status == "deleted" {
			return Fail("channel_missing", "渠道不存在", 404)
		}
		// Serialize one bank receipt across all channels, including concurrent credits.
		if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 73021))", reference); e != nil {
			return e
		}
		var oldUser string
		var oldAmount int64
		var oldNote string
		e = tx.QueryRow(ctx, "SELECT user_id,available_delta,note FROM ledger WHERE kind='credit' AND reference=$1", reference).Scan(&oldUser, &oldAmount, &oldNote)
		if e == nil {
			if oldUser != user || oldAmount != amount || oldNote != note {
				return Fail("credit_conflict", "凭证已用于另一笔入账", 409)
			}
			return nil
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		_, e = tx.Exec(ctx, "SELECT user_id FROM wallets WHERE user_id=$1 FOR UPDATE", user)
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, "INSERT INTO ledger(id,user_id,kind,available_delta,frozen_delta,reference,note,actor_id) VALUES($1,$2,'credit',$3,0,$4,$5,$6)", security.ID("led"), user, amount, reference, note, actor)
		if e != nil {
			return e
		}
		return store.Audit(ctx, tx, actor, "credit", user, fmt.Sprintf("%s:%d", reference, amount))
	})
}
func (s *Service) Finish(ctx context.Context, id, token, status, stage, failure, receipt string) error {
	return s.Store.Tx(ctx, func(tx pgx.Tx) error {
		var user string
		if e := tx.QueryRow(ctx, "SELECT user_id FROM orders WHERE id=$1", id).Scan(&user); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, "SELECT user_id FROM wallets WHERE user_id=$1 FOR UPDATE", user); e != nil {
			return e
		}
		var previous string
		var price int64
		e := tx.QueryRow(ctx, "SELECT status,supply_minor FROM orders WHERE id=$1 AND lease_token=$2 FOR UPDATE", id, token).Scan(&previous, &price)
		if errors.Is(e, pgx.ErrNoRows) {
			return Fail("lease_lost", "任务租约已变化", 409)
		}
		if e != nil {
			return e
		}
		if previous == "succeeded" || previous == "failed" {
			return nil
		}
		if status != "succeeded" && status != "failed" && status != "running" && status != "unknown" {
			return errors.New("invalid executor state")
		}
		_, e = tx.Exec(ctx, "UPDATE orders SET status=$2,stage=$3,failure_code=$4,receipt=$5,lease_token='',lease_until=NULL,next_at=CASE WHEN EXISTS(SELECT 1 FROM payment_captcha_attempts c WHERE c.order_id=orders.id AND c.status='pending') THEN now() ELSE now()+interval '20 seconds' END,updated_at=now(),credential_cipher=CASE WHEN $2 IN ('succeeded','failed') THEN '' ELSE credential_cipher END WHERE id=$1", id, status, stage, failure, receipt)
		if e != nil {
			return e
		}
		if status == "unknown" {
			if e = store.EnqueueManualAlert(ctx, tx, id, failure); e != nil {
				return e
			}
		}
		if status == "succeeded" || status == "failed" {
			kind := "consume"
			available := int64(0)
			if status == "failed" {
				kind = "release"
				available = price
			}
			_, e = tx.Exec(ctx, "INSERT INTO ledger(id,user_id,order_id,kind,available_delta,frozen_delta,reference,note,actor_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'worker')", security.ID("led"), user, id, kind, available, -price, id+":settle", "订单"+status)
			if e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, "DELETE FROM resource_locks WHERE order_id=$1", id); e != nil {
				return e
			}
		}
		if previous != status {
			o, e := ScanOrder(tx.QueryRow(ctx, "SELECT "+OrderColumns+" FROM orders WHERE id=$1", id))
			if e != nil {
				return e
			}
			var url, secret string
			e = tx.QueryRow(ctx, "SELECT url,secret_cipher FROM webhook_configs WHERE user_id=$1", user).Scan(&url, &secret)
			if e == nil {
				key, e := s.Store.Vault.Open("webhook:"+user, secret)
				if e != nil {
					return e
				}
				event := security.ID("evt")
				payload, _ := json.Marshal(map[string]any{"url": url, "secret": string(key), "event": map[string]any{"id": event, "type": "order." + status, "data": o}})
				_, e = tx.Exec(ctx, "INSERT INTO outbox(id,user_id,kind,payload_cipher) VALUES($1,$2,'webhook',$3)", event, user, s.Store.Vault.Seal("outbox:"+event, payload))
				if e != nil {
					return e
				}
			} else if !errors.Is(e, pgx.ErrNoRows) {
				return e
			}
			return store.Audit(ctx, tx, "worker", "order."+status, id, failure)
		}
		return nil
	})
}
