package management

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"gptibo/storefront/internal/auth"
	"gptibo/storefront/internal/fault"
	"gptibo/storefront/internal/security"
)

var membershipPlans = []string{"plus", "pro-5x", "pro-20x", "pro-25x"}

func planRank(plan string) int {
	for i, p := range membershipPlans {
		if plan == p {
			return i
		}
	}
	return -1
}
func getParam(q url.Values, key, fallback string) string {
	v := q.Get(key)
	if v == "" {
		return fallback
	}
	return v
}
func MonthAfter(ms int64) int64 {
	loc := time.FixedZone("Asia/Shanghai", 8*3600)
	t := time.UnixMilli(ms).In(loc)
	first := time.Date(t.Year(), t.Month()+1, 1, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), loc)
	last := time.Date(first.Year(), first.Month()+1, 0, 0, 0, 0, 0, loc).Day()
	return time.Date(first.Year(), first.Month(), min(t.Day(), last), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), loc).UnixMilli()
}
func crmCustomer(ctx context.Context, q queryable, id string, p auth.Principal, lock bool) (map[string]any, error) {
	sql := `SELECT c.*,p.name partner_name FROM crm_customers c LEFT JOIN partners p ON p.id=c.partner_id WHERE c.id=$1 AND ($2::boolean OR c.partner_id=$3)`
	if lock {
		sql += ` FOR UPDATE OF c`
	}
	list, e := rows(ctx, q, sql, id, p.Role == "admin", p.Subject)
	if e != nil {
		return nil, e
	}
	if len(list) == 0 {
		return nil, fault.New(404, "未找到客户。")
	}
	return list[0], nil
}
func crmAccount(ctx context.Context, q queryable, id string, p auth.Principal, lock bool) (map[string]any, error) {
	sql := `SELECT a.*,s.plan,s.plan_name,s.expires_at,s.opened_at,s.paid_cents FROM crm_accounts a JOIN crm_customers c ON c.id=a.customer_id LEFT JOIN crm_subscriptions s ON s.id=a.current_subscription_id WHERE a.id=$1 AND ($2::boolean OR c.partner_id=$3)`
	if lock {
		sql += ` FOR UPDATE OF a`
	}
	list, e := rows(ctx, q, sql, id, p.Role == "admin", p.Subject)
	if e != nil {
		return nil, e
	}
	if len(list) == 0 {
		return nil, fault.New(404, "未找到客户账号。")
	}
	return list[0], nil
}
func (s Service) Customers(ctx context.Context, p auth.Principal, q url.Values, export bool) (map[string]any, error) {
	now := time.Now().UnixMilli()
	soon := now + 7*86400000
	page, e := strconv.Atoi(getParam(q, "page", "1"))
	plan := getParam(q, "plan", "all")
	expiry := getParam(q, "expiry", "all")
	owner := getParam(q, "partner", "all")
	search := strings.TrimSpace(q.Get("q"))
	if e != nil || page < 1 || page > 10000 || !validText(search, 100) || (plan != "all" && planRank(plan) < 0) {
		return nil, fault.New(400, "筛选条件无效。")
	}
	if p.Role != "admin" && owner != "all" {
		return nil, fault.New(403, "不能筛选其他代理的客户。")
	}
	values := []any{p.Role == "admin", p.Subject, now, soon}
	clauses := []string{`($1::boolean OR c.partner_id=$2)`}
	bind := func(v any) string { values = append(values, v); return fmt.Sprintf("$%d", len(values)) }
	if owner != "all" {
		if owner == "direct" {
			clauses = append(clauses, "c.partner_id IS NULL")
		} else {
			if !security.ValidID(owner) {
				return nil, fault.New(400, "代理无效。")
			}
			clauses = append(clauses, "c.partner_id="+bind(owner))
		}
	}
	if search != "" {
		arg := bind(search)
		clauses = append(clauses, `(strpos(lower(c.name||c.wechat||c.qq||c.email||c.note||c.source),lower(`+arg+`))>0 OR EXISTS(SELECT 1 FROM crm_accounts a WHERE a.customer_id=c.id AND strpos(lower(a.account||a.note),lower(`+arg+`))>0))`)
	}
	parts := []string{"a.customer_id=c.id"}
	if plan != "all" {
		parts = append(parts, "s.plan="+bind(plan))
	}
	switch expiry {
	case "all":
	case "active":
		parts = append(parts, "s.expires_at>$3")
	case "soon":
		parts = append(parts, "s.expires_at>$3 AND s.expires_at<=$4")
	case "expired":
		parts = append(parts, "s.expires_at<=$3")
	case "none":
		parts = append(parts, "s.id IS NULL")
	default:
		return nil, fault.New(400, "到期条件无效。")
	}
	if plan != "all" || expiry != "all" {
		clause := `EXISTS(SELECT 1 FROM crm_accounts a LEFT JOIN crm_subscriptions s ON s.id=a.current_subscription_id WHERE ` + strings.Join(parts, " AND ") + `)`
		if expiry == "none" && plan == "all" {
			clause = "(" + clause + " OR NOT EXISTS(SELECT 1 FROM crm_accounts a WHERE a.customer_id=c.id))"
		}
		clauses = append(clauses, clause)
	}
	// Bind all four fixed parameters in every query, including export and count queries.
	base := `WITH bounds AS (SELECT $3::bigint now,$4::bigint soon),filtered AS (SELECT c.*,p.name partner_name FROM crm_customers c LEFT JOIN partners p ON p.id=c.partner_id WHERE ` + strings.Join(clauses, " AND ") + `) `
	tx, e := s.Store.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	if export {
		customers, e := rows(ctx, tx, base+`SELECT * FROM filtered ORDER BY created_at DESC,id LIMIT 5001`, values...)
		if e != nil {
			return nil, e
		}
		accounts, e := rows(ctx, tx, base+`SELECT a.*,s.plan_name,s.opened_at,s.expires_at,s.paid_cents FROM crm_accounts a JOIN filtered f ON f.id=a.customer_id LEFT JOIN crm_subscriptions s ON s.id=a.current_subscription_id ORDER BY a.created_at,a.id LIMIT 10001`, values...)
		if e != nil {
			return nil, e
		}
		subscriptions, e := rows(ctx, tx, base+`SELECT s.*,a.account,a.customer_id FROM crm_subscriptions s JOIN crm_accounts a ON a.id=s.account_id JOIN filtered f ON f.id=a.customer_id ORDER BY s.created_at,s.id LIMIT 50001`, values...)
		if e != nil {
			return nil, e
		}
		if len(customers) > 5000 || len(accounts) > 10000 || len(subscriptions) > 50000 {
			return nil, fault.New(409, "导出记录过多，请缩小筛选范围。")
		}
		return map[string]any{"customers": customers, "accounts": accounts, "subscriptions": subscriptions, "asOf": now}, tx.Commit(ctx)
	}
	list, e := rows(ctx, tx, base+`SELECT f.*,(SELECT string_agg(DISTINCT s.plan_name,',') FROM crm_accounts a JOIN crm_subscriptions s ON s.id=a.current_subscription_id WHERE a.customer_id=f.id) plan_names,(SELECT min(s.expires_at) FROM crm_accounts a JOIN crm_subscriptions s ON s.id=a.current_subscription_id WHERE a.customer_id=f.id) next_expiry,(SELECT count(*) FROM crm_accounts a WHERE a.customer_id=f.id) account_count,(SELECT COALESCE(sum(s.paid_cents),0)::bigint FROM crm_subscriptions s JOIN crm_accounts a ON a.id=s.account_id WHERE a.customer_id=f.id) paid_cents,(SELECT count(*) FROM crm_accounts a JOIN crm_subscriptions s ON s.id=a.current_subscription_id WHERE a.customer_id=f.id AND s.expires_at>$3 AND s.expires_at<=$4) soon_count,(SELECT count(*) FROM crm_accounts a JOIN crm_subscriptions s ON s.id=a.current_subscription_id WHERE a.customer_id=f.id AND s.expires_at<=$3) expired_count FROM filtered f ORDER BY f.created_at DESC,f.id LIMIT 20 OFFSET `+strconv.Itoa((page-1)*20), values...)
	if e != nil {
		return nil, e
	}
	stats, e := rows(ctx, tx, base+`SELECT count(*) customers,(SELECT count(*) FROM crm_accounts a JOIN filtered f ON f.id=a.customer_id) accounts,(SELECT count(*) FROM crm_accounts a JOIN filtered f ON f.id=a.customer_id JOIN crm_subscriptions s ON s.id=a.current_subscription_id WHERE s.expires_at>$3 AND s.expires_at<=$4) soon,(SELECT count(*) FROM crm_accounts a JOIN filtered f ON f.id=a.customer_id JOIN crm_subscriptions s ON s.id=a.current_subscription_id WHERE s.expires_at<=$3) expired,(SELECT COALESCE(sum(s.paid_cents),0)::bigint FROM crm_subscriptions s JOIN crm_accounts a ON a.id=s.account_id JOIN filtered f ON f.id=a.customer_id) paid_cents FROM filtered`, values...)
	if e != nil {
		return nil, e
	}
	plans, e := rows(ctx, tx, `SELECT id,name FROM catalog_plans WHERE product_id='chatgpt' AND id=ANY($1) ORDER BY sort_order,id`, membershipPlans)
	if e != nil {
		return nil, e
	}
	partners := []map[string]any{}
	if p.Role == "admin" {
		partners, e = rows(ctx, tx, `SELECT id,name FROM partners ORDER BY name`)
		if e != nil {
			return nil, e
		}
	}
	return map[string]any{"customers": list, "total": stats[0]["customers"], "stats": stats[0], "plans": plans, "partners": partners, "page": page, "asOf": now}, tx.Commit(ctx)
}
func (s Service) CustomerDetail(ctx context.Context, p auth.Principal, id string) (map[string]any, error) {
	tx, e := s.Store.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	c, e := crmCustomer(ctx, tx, id, p, false)
	if e != nil {
		return nil, e
	}
	accounts, e := rows(ctx, tx, `SELECT a.*,s.plan,s.plan_name,s.opened_at,s.expires_at,s.paid_cents FROM crm_accounts a LEFT JOIN crm_subscriptions s ON s.id=a.current_subscription_id WHERE a.customer_id=$1 ORDER BY a.created_at,a.id`, id)
	if e != nil {
		return nil, e
	}
	subscriptions, e := rows(ctx, tx, `SELECT s.* FROM crm_subscriptions s JOIN crm_accounts a ON a.id=s.account_id WHERE a.customer_id=$1 ORDER BY s.created_at DESC,s.id DESC`, id)
	if e != nil {
		return nil, e
	}
	return map[string]any{"customer": c, "accounts": accounts, "subscriptions": subscriptions, "asOf": time.Now().UnixMilli()}, tx.Commit(ctx)
}

type CustomerInput struct {
	Key       string  `json:"key"`
	PartnerID *string `json:"partnerId"`
	Name      string  `json:"name"`
	Wechat    string  `json:"wechat"`
	QQ        string  `json:"qq"`
	Email     string  `json:"email"`
	Source    string  `json:"source"`
	Note      string  `json:"note"`
	Version   *int64  `json:"version"`
}

func (s Service) SaveCustomer(ctx context.Context, role, token, id string, in CustomerInput) (map[string]any, error) {
	in.Name = strings.TrimSpace(in.Name)
	in.Wechat = strings.TrimSpace(in.Wechat)
	in.QQ = strings.TrimSpace(in.QQ)
	in.Email = strings.TrimSpace(in.Email)
	in.Note = strings.TrimSpace(in.Note)
	in.Source = strings.TrimSpace(in.Source)
	if in.Name == "" || !validText(in.Name, 100) || !validText(in.Wechat, 100) || !validText(in.QQ, 30) || !validText(in.Source, 100) || !validText(in.Note, 2000) || (in.Wechat == "" && in.QQ == "" && in.Email == "") {
		return nil, fault.New(400, "请核对资料并至少填写一种联系方式。")
	}
	if in.Email != "" {
		email, e := auth.Email(in.Email)
		if e != nil {
			return nil, e
		}
		in.Email = email
	}
	create := id == ""
	if create {
		if !security.ValidID(in.Key) {
			return nil, fault.New(400, "请求编号无效。")
		}
		id = in.Key
	} else if in.Version == nil || *in.Version < 0 {
		return nil, fault.New(400, "资料版本无效。")
	}
	var result map[string]any
	e := s.write(ctx, role, token, func(tx pgx.Tx, p auth.Principal) error {
		now := time.Now().UnixMilli()
		var owner any
		if role == "partner" {
			owner = p.Subject
			if in.PartnerID != nil && *in.PartnerID != p.Subject {
				return fault.New(403, "不能修改其他代理的客户。")
			}
		} else if in.PartnerID != nil && *in.PartnerID != "direct" {
			if !security.ValidID(*in.PartnerID) {
				return fault.New(400, "代理无效。")
			}
			var exists bool
			if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM partners WHERE id=$1)`, *in.PartnerID).Scan(&exists); e != nil {
				return e
			}
			if !exists {
				return fault.New(400, "代理不存在。")
			}
			owner = *in.PartnerID
		}
		var before map[string]any
		changed := true
		if create {
			tag, e := tx.Exec(ctx, `INSERT INTO crm_customers(id,partner_id,name,wechat,qq,email,note,source,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$9) ON CONFLICT DO NOTHING`, id, owner, in.Name, in.Wechat, in.QQ, in.Email, in.Note, in.Source, now)
			if e != nil {
				return e
			}
			changed = tag.RowsAffected() == 1
		} else {
			var e error
			before, e = crmCustomer(ctx, tx, id, p, true)
			if e != nil {
				return e
			}
			tag, e := tx.Exec(ctx, `UPDATE crm_customers SET name=$1,wechat=$2,qq=$3,email=$4,note=$5,source=$6,version=version+1,updated_at=$7 WHERE id=$8 AND version=$9`, in.Name, in.Wechat, in.QQ, in.Email, in.Note, in.Source, now, id, *in.Version)
			if e != nil {
				return e
			}
			if tag.RowsAffected() != 1 {
				return conflict()
			}
		}
		var e error
		result, e = crmCustomer(ctx, tx, id, p, false)
		if e != nil {
			return e
		}
		if create && (result["partner_id"] != owner || result["name"] != in.Name || result["wechat"] != in.Wechat || result["qq"] != in.QQ || result["email"] != in.Email || result["note"] != in.Note || result["source"] != in.Source) {
			return conflict()
		}
		if !changed {
			return nil
		}
		return audit(ctx, tx, p, "customer-save", id, map[string]any{"before": before, "after": result})
	})
	return map[string]any{"customer": result}, e
}

type AccountInput struct {
	Key     string `json:"key"`
	Account string `json:"account"`
	Note    string `json:"note"`
	Version *int64 `json:"version"`
}

func (s Service) SaveAccount(ctx context.Context, role, token, id string, in AccountInput, create bool) (map[string]any, error) {
	in.Account = strings.TrimSpace(in.Account)
	in.Note = strings.TrimSpace(in.Note)
	if in.Account == "" || !validText(in.Account, 254) || !validText(in.Note, 2000) || (create && !security.ValidID(in.Key)) || (!create && (in.Version == nil || *in.Version < 0)) {
		return nil, fault.New(400, "客户账号或版本无效。")
	}
	var result map[string]any
	e := s.write(ctx, role, token, func(tx pgx.Tx, p auth.Principal) error {
		now := time.Now().UnixMilli()
		var before map[string]any
		changed := true
		accountID := id
		if create {
			if _, e := crmCustomer(ctx, tx, id, p, false); e != nil {
				return e
			}
			accountID = in.Key
			tag, e := tx.Exec(ctx, `INSERT INTO crm_accounts(id,customer_id,account,note,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$5) ON CONFLICT DO NOTHING`, accountID, id, in.Account, in.Note, now)
			if e != nil {
				return e
			}
			changed = tag.RowsAffected() == 1
		} else {
			var e error
			before, e = crmAccount(ctx, tx, id, p, true)
			if e != nil {
				return e
			}
			tag, e := tx.Exec(ctx, `UPDATE crm_accounts SET account=$1,note=$2,version=version+1,updated_at=$3 WHERE id=$4 AND version=$5`, in.Account, in.Note, now, id, *in.Version)
			if e != nil {
				return e
			}
			if tag.RowsAffected() != 1 {
				return conflict()
			}
		}
		var e error
		result, e = crmAccount(ctx, tx, accountID, p, false)
		if e != nil {
			if create && !changed && fault.Status(e) == 404 {
				return conflict()
			}
			return e
		}
		if create && (result["customer_id"] != id || result["account"] != in.Account || result["note"] != in.Note) {
			return conflict()
		}
		if !changed {
			return nil
		}
		return audit(ctx, tx, p, "customer-account-save", accountID, map[string]any{"before": before, "after": result})
	})
	return map[string]any{"account": result}, e
}

type SubscriptionInput struct {
	Key     string `json:"key"`
	Version *int64 `json:"version"`
	Plan    string `json:"plan"`
	Kind    string `json:"kind"`
	Paid    *int64 `json:"paidCents"`
	Opened  *int64 `json:"openedAt"`
	Note    string `json:"note"`
}

func (s Service) SaveSubscription(ctx context.Context, role, token, id string, in SubscriptionInput) (map[string]any, error) {
	now := time.Now().UnixMilli()
	in.Note = strings.TrimSpace(in.Note)
	if !security.ValidID(in.Key) || in.Version == nil || *in.Version < 0 || planRank(in.Plan) < 0 || (in.Kind != "open" && in.Kind != "renew" && in.Kind != "upgrade") || in.Paid == nil || *in.Paid < 0 || *in.Paid > 10000000 || in.Opened == nil || *in.Opened < 946656000000 || *in.Opened > now+86400000 || !validText(in.Note, 2000) {
		return nil, fault.New(400, "请核对套餐、时间、金额和版本。")
	}
	var result map[string]any
	e := s.write(ctx, role, token, func(tx pgx.Tx, p auth.Principal) error {
		account, e := crmAccount(ctx, tx, id, p, true)
		if e != nil {
			return e
		}
		existing, e := rows(ctx, tx, `SELECT * FROM crm_subscriptions WHERE id=$1`, in.Key)
		if e != nil {
			return e
		}
		if len(existing) > 0 {
			r := existing[0]
			if r["account_id"] != id || r["plan"] != in.Plan || r["kind"] != in.Kind || r["paid_cents"] != *in.Paid || r["opened_at"] != *in.Opened || r["note"] != in.Note {
				return conflict()
			}
			result = r
			return nil
		}
		if account["version"] != *in.Version {
			return conflict()
		}
		previous := account["current_subscription_id"]
		if previous == nil {
			if in.Kind != "open" {
				return fault.New(409, "首次请录入开通记录。")
			}
		} else {
			if in.Kind == "open" {
				return fault.New(409, "账号已有开通记录。")
			}
			if in.Kind == "renew" && (now < account["expires_at"].(int64) || *in.Opened < account["expires_at"].(int64)) {
				return fault.New(409, "会员未到期，不支持提前续费。")
			}
			if in.Kind == "upgrade" && planRank(in.Plan) <= planRank(account["plan"].(string)) {
				return fault.New(409, "升级需要选择更高套餐。")
			}
			if *in.Opened < account["opened_at"].(int64) {
				return fault.New(400, "开通时间不能早于上一条记录。")
			}
		}
		var name string
		if e = tx.QueryRow(ctx, `SELECT name FROM catalog_plans WHERE id=$1`, in.Plan).Scan(&name); e != nil {
			return fault.New(400, "套餐不存在。")
		}
		if _, e = tx.Exec(ctx, `INSERT INTO crm_subscriptions(id,account_id,previous_id,plan,plan_name,kind,paid_cents,opened_at,expires_at,note,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, in.Key, id, previous, in.Plan, name, in.Kind, *in.Paid, *in.Opened, MonthAfter(*in.Opened), in.Note, now); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `UPDATE crm_accounts SET current_subscription_id=$1,version=version+1,updated_at=$2 WHERE id=$3`, in.Key, now, id); e != nil {
			return e
		}
		list, e := rows(ctx, tx, `SELECT * FROM crm_subscriptions WHERE id=$1`, in.Key)
		if e != nil {
			return e
		}
		result = list[0]
		return audit(ctx, tx, p, "customer-subscription", id, map[string]any{"subscription": result})
	})
	return map[string]any{"subscription": result}, e
}
