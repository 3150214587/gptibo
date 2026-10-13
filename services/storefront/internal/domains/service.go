package domains

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/net/idna"
	"golang.org/x/net/publicsuffix"
	"gptibo/storefront/internal/fault"
	"gptibo/storefront/internal/security"
	"gptibo/storefront/internal/store"
)

type DNS interface {
	LookupTXT(context.Context, string) ([]string, error)
	LookupCNAME(context.Context, string) (string, error)
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}
type Service struct {
	Store           *store.Store
	Provider        Provider
	DNS             DNS
	Target, Zone    string
	Limit           int
	MonthlyFeeCents int64
	// Server-managed partner IDs may bypass only the completed-order threshold.
	EligibilityOverrides map[string]bool
	VerificationReady    func(string) error
}
type Domain struct {
	ID             string     `json:"id"`
	StoreID        string     `json:"storeId"`
	Free           bool       `json:"free"`
	ExpiresAt      *time.Time `json:"expiresAt"`
	BillingVersion int64      `json:"billingVersion"`
	BillingState   string     `json:"billingState"`
	Hostname       string     `json:"hostname"`
	Apex           bool       `json:"apex"`
	State          string     `json:"state"`
	Message        string     `json:"message"`
	Records        []Record   `json:"records"`
	CheckedAt      *time.Time `json:"checkedAt"`
	ProviderID     string     `json:"-"`
	Verification   string     `json:"-"`
}
type View struct {
	MonthlyFeeCents int64    `json:"monthlyFeeCents"`
	Enabled         bool     `json:"enabled"`
	Eligible        bool     `json:"eligible"`
	CompletedOrders int      `json:"completedOrders"`
	RequiredOrders  int      `json:"requiredOrders"`
	Target          string   `json:"target"`
	Domain          *Domain  `json:"domain"`
	Domains         []Domain `json:"domains"`
}

const RequiredOrders = 3
const completedOrdersSQL = `SELECT count(*) FROM orders WHERE partner_id=$1 AND mode='live' AND is_test=0 AND payment_status='paid' AND status='success' AND refunded_cents=0 AND refund_hold=0`
const eligibilityMessage = "至少完成 3 单有效订单后才可开通自定义域名。"

func (s *Service) eligible(id string, completed int) bool {
	return completed >= RequiredOrders || s.EligibilityOverrides[id]
}

var label = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

func Normalize(value, zone string) (string, error) {
	host, e := idna.Lookup.ToASCII(strings.ToLower(strings.TrimSpace(value)))
	bad := func() (string, error) {
		return "", fault.New(400, "请填写完整域名，例如 example.com 或 shop.example.com，不含 https://、路径或端口。")
	}
	if e != nil || len(host) > 253 || net.ParseIP(host) != nil {
		return bad()
	}
	for _, part := range strings.Split(host, ".") {
		if !label.MatchString(part) {
			return bad()
		}
	}
	_, e = publicsuffix.EffectiveTLDPlusOne(host)
	suffix, icann := publicsuffix.PublicSuffix(host)
	if e != nil || !icann || suffix == "" {
		return bad()
	}
	if host == zone || strings.HasSuffix(host, "."+zone) {
		return "", fault.New(400, "不能绑定平台自己的域名。")
	}
	return host, nil
}

func isApex(host string) bool {
	root, err := publicsuffix.EffectiveTLDPlusOne(host)
	return err == nil && root == host
}

// A flattened apex has no public CNAME. Require its entire address set to
// match the configured SaaS target, in addition to ownership and CF validation.
// Never treat an arbitrary Cloudflare address as proof of correct routing.
func routesToTarget(ctx context.Context, resolver DNS, host, target string) bool {
	cname, err := resolver.LookupCNAME(ctx, host)
	if err != nil {
		return false
	}
	if strings.EqualFold(strings.TrimSuffix(cname, "."), target) {
		return true
	}
	if !isApex(host) || !strings.EqualFold(strings.TrimSuffix(cname, "."), host) {
		return false
	}
	addresses, err := resolver.LookupIPAddr(ctx, host)
	if err != nil || len(addresses) == 0 {
		return false
	}
	expected, err := resolver.LookupIPAddr(ctx, target)
	if err != nil || len(expected) == 0 {
		return false
	}
	for _, address := range addresses {
		if !address.IP.IsGlobalUnicast() || address.IP.IsPrivate() {
			return false
		}
		found := false
		for _, candidate := range expected {
			if address.IP.Equal(candidate.IP) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
func (s *Service) Enabled() bool {
	return s != nil && s.Provider != nil && s.Target != "" && s.Zone != "" && s.Limit > 0 && s.Limit <= 100
}
func (s *Service) Read(ctx context.Context, id string) (View, error) {
	v := View{MonthlyFeeCents: s.MonthlyFeeCents, Enabled: s.Enabled(), Target: s.Target, RequiredOrders: RequiredOrders}
	if err := s.Store.DB.QueryRow(ctx, completedOrdersSQL, id).Scan(&v.CompletedOrders); err != nil {
		return v, err
	}
	v.Eligible = s.eligible(id, v.CompletedOrders)
	rows, e := s.Store.DB.Query(ctx, `SELECT id,COALESCE(store_id,partner_id),hostname,state,message,records,checked_at,provider_id,verification,free,expires_at,billing_version FROM partner_domains WHERE partner_id=$1 ORDER BY created_at,id`, id)
	if e != nil {
		return v, e
	}
	defer rows.Close()
	v.Domains = []Domain{}
	for rows.Next() {
		d := Domain{}
		var raw []byte
		if e = rows.Scan(&d.ID, &d.StoreID, &d.Hostname, &d.State, &d.Message, &raw, &d.CheckedAt, &d.ProviderID, &d.Verification, &d.Free, &d.ExpiresAt, &d.BillingVersion); e != nil {
			return v, e
		}
		if e = json.Unmarshal(raw, &d.Records); e != nil {
			return v, e
		}
		d.Records = append([]Record{{"CNAME", d.Hostname, s.Target}, {"TXT", "_gptibo-verify." + d.Hostname, d.Verification}}, d.Records...)
		d.Apex = isApex(d.Hostname)
		d.BillingState = "active"
		if !d.Free {
			if d.ExpiresAt == nil {
				d.BillingState = "awaiting_payment"
			} else if !time.Now().Before(d.ExpiresAt.Add(72 * time.Hour)) {
				d.BillingState = "suspended"
			} else if !time.Now().Before(*d.ExpiresAt) {
				d.BillingState = "grace"
			}
		}
		v.Domains = append(v.Domains, d)
	}
	if len(v.Domains) > 0 {
		v.Domain = &v.Domains[0]
	}
	return v, rows.Err()
}

func (s *Service) Bind(ctx context.Context, id, host string, stores ...string) error {
	shop := id
	if len(stores) > 0 && stores[0] != "" {
		shop = stores[0]
	}
	if !s.Enabled() {
		return fault.New(503, "自定义域名暂未开放，请稍后再试。")
	}
	host, e := Normalize(host, s.Zone)
	if e != nil {
		return e
	}
	tx, e := s.Store.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var completed int
	if e = tx.QueryRow(ctx, completedOrdersSQL, id).Scan(&completed); e != nil {
		return e
	}
	if !s.eligible(id, completed) {
		return fault.New(403, eligibilityMessage)
	}
	var locked bool
	if e = tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(903494)`).Scan(&locked); e != nil {
		return e
	}
	if !locked {
		return fault.New(409, "域名服务正在处理其他请求，请稍后重试。")
	}
	var exists bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM partner_stores WHERE id=$1 AND partner_id=$2 AND active)`, shop, id).Scan(&exists); e != nil {
		return e
	}
	if !exists {
		return fault.New(404, "商店不可用。")
	}
	var existing, existingStore string
	e = tx.QueryRow(ctx, `SELECT partner_id,COALESCE(store_id,partner_id) FROM partner_domains WHERE hostname=$1`, host).Scan(&existing, &existingStore)
	if e == nil {
		if existing == id && existingStore == shop {
			return tx.Commit(ctx)
		}
		return fault.New(409, "域名已被其他商店绑定。")
	}
	if e != pgx.ErrNoRows {
		return e
	}
	var free bool
	if e = tx.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM partner_domains WHERE partner_id=$1 AND free)`, id).Scan(&free); e != nil {
		return e
	}
	var count int
	if e = tx.QueryRow(ctx, `SELECT count(*) FROM partner_domains`).Scan(&count); e != nil {
		return e
	}
	if count >= s.Limit {
		return fault.New(409, "当前域名名额已用完，请联系平台。")
	}
	tag, e := tx.Exec(ctx, `INSERT INTO partner_domains(id,partner_id,store_id,hostname,verification,message,free) SELECT $4,id,$5,$2,$3,'请添加下方解析记录，付费域名需先支付接入费用。',$6 FROM partners WHERE id=$1 AND active ON CONFLICT DO NOTHING`, id, host, security.Token(), security.ID(), shop, free)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return fault.New(409, "域名已被绑定或商店不可用。")
	}
	return tx.Commit(ctx)
}

// Mark locally unavailable before contacting the provider; failed cleanup is retried.
func (s *Service) Remove(ctx context.Context, id, host string) error {
	tx, e := s.Store.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(903500)`); e != nil {
		return e
	}
	var pending bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM domain_charges c JOIN partner_domains d ON d.id=c.domain_id WHERE d.partner_id=$1 AND d.hostname=$2 AND c.state='pending')`, id, host).Scan(&pending); e != nil {
		return e
	}
	if pending {
		return fault.New(409, "域名有待确认付款，核对完成后才能解绑。")
	}
	tag, e := tx.Exec(ctx, `UPDATE partner_domains SET state='deleting',message='正在解除绑定，请稍候。',checked_at=NULL WHERE partner_id=$1 AND hostname=$2`, id, host)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return fault.New(409, "域名已变更，请刷新后重试。")
	}
	return tx.Commit(ctx)
}

func (s *Service) Resolve(ctx context.Context, host string) (Scope, error) {
	scope := Scope{Host: host}
	if !s.Enabled() {
		return scope, fault.New(404, "商店域名未启用。")
	}
	e := s.Store.DB.QueryRow(ctx, `SELECT p.id,s.slug FROM partner_domains d JOIN partners p ON p.id=d.partner_id JOIN partner_stores s ON s.id=COALESCE(d.store_id,p.id) WHERE d.hostname=$1 AND d.state='active' AND d.checked_at>now()-interval '24 hours' AND p.active AND s.active AND (d.free OR d.expires_at+interval '3 days'>now())`, host).Scan(&scope.PartnerID, &scope.Slug)
	if errors.Is(e, pgx.ErrNoRows) {
		return scope, fault.New(404, "商店域名未启用。")
	}
	scope.OwnerID = scope.PartnerID
	return scope, e
}

// A custom hostname may route only its owner or a current direct child.
func (s *Service) SelectShop(ctx context.Context, scope Scope, slug string) (Scope, error) {
	if slug == "" || slug == scope.Slug {
		return scope, nil
	}
	var id string
	err := s.Store.DB.QueryRow(ctx, `SELECT p.id FROM partner_stores s JOIN partners p ON p.id=s.partner_id WHERE s.slug=$1 AND s.active AND p.active AND (p.id=$2 OR p.parent_id=$2)`, slug, scope.Owner()).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return scope, fault.New(404, "该代理商店暂不可用。")
	}
	if err != nil {
		return scope, err
	}
	scope.PartnerID, scope.Slug = id, slug
	return scope, nil
}
func (s *Service) Check(ctx context.Context, partner string, ids ...string) error {
	query := `SELECT id FROM partner_domains WHERE partner_id=$1`
	args := []any{partner}
	if len(ids) > 0 && ids[0] != "" {
		query += ` AND id=$2`
		args = append(args, ids[0])
	}
	rows, e := s.Store.DB.Query(ctx, query, args...)
	if e != nil {
		return e
	}
	list := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return e
		}
		list = append(list, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, id := range list {
		if e = s.checkBinding(ctx, id); e != nil {
			return e
		}
	}
	return nil
}
func (s *Service) checkBinding(ctx context.Context, id string) error {
	if !s.Enabled() {
		return fault.New(503, "自定义域名暂未开放。")
	}
	tx, e := s.Store.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	// Serializes provider creates and row transitions across API and worker processes.
	var locked bool
	if e = tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(903494)`).Scan(&locked); e != nil {
		return e
	}
	if !locked {
		return nil
	}
	var host, verify, pid, state, partner string
	var billable bool
	e = tx.QueryRow(ctx, `SELECT hostname,verification,provider_id,state,partner_id,(free OR expires_at+interval '3 days'>now()) IS TRUE FROM partner_domains WHERE id=$1 FOR UPDATE`, id).Scan(&host, &verify, &pid, &state, &partner, &billable)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	save := func(next, message string, h *Hostname) error {
		raw := []byte("[]")
		if h != nil {
			pid = h.ID
			raw, _ = json.Marshal(h.Records())
		}
		_, err := tx.Exec(ctx, `UPDATE partner_domains SET state=$2,message=$3,provider_id=$4,records=$5,checked_at=now() WHERE id=$1`, id, next, message, pid, raw)
		if err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if state == "deleting" {
		// Recover any provider create whose response/DB commit was lost before removing.
		if pid == "creating" {
			h, err := s.Provider.Find(ctx, host)
			if err != nil && !errors.Is(err, ErrProviderMissing) {
				return err
			}
			pid = h.ID
		}
		if pid != "" {
			if e = s.Provider.Delete(ctx, pid); e != nil {
				return e
			}
		}
		if _, e = tx.Exec(ctx, `DELETE FROM partner_domains WHERE id=$1`, id); e != nil {
			return e
		}
		return tx.Commit(ctx)
	}
	if !billable {
		return save(state, "域名费用未支付或宽限期已结束，主站推广链接仍可使用。", nil)
	}
	// Pending bindings must still qualify before provider creation/activation.
	// Already active shops keep serving customers; removal always remains available.
	if state != "active" {
		var completed int
		if e = tx.QueryRow(ctx, completedOrdersSQL, partner).Scan(&completed); e != nil {
			return e
		}
		if !s.eligible(partner, completed) {
			return save("error", eligibilityMessage, nil)
		}
	}
	resolver := s.DNS
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	txt, err := resolver.LookupTXT(checkCtx, "_gptibo-verify."+host)
	verified := false
	for _, v := range txt {
		if security.Equal(v, verify) {
			verified = true
		}
	}
	if err != nil || !verified {
		return save("pending_dns", "等待域名归属验证，请检查 TXT 记录并保留该记录。", nil)
	}
	if !routesToTarget(checkCtx, resolver, host, s.Target) {
		if isApex(host) {
			return save("pending_dns", "等待根域名解析生效；请在 Cloudflare DNS 添加 @ 的 CNAME，指向接入地址并设为“仅 DNS”，移除 @ 下冲突的 A/AAAA 记录。", nil)
		}
		return save("pending_dns", "等待 CNAME 解析生效；请使用仅 DNS 解析，关闭该记录的其他代理。", nil)
	}
	var h Hostname
	if pid == "" {
		// Persist intent before the external mutation so removal/retries can recover a lost response.
		pid = "creating"
		return save("pending_dns", "解析初步检查通过，正在申请 Cloudflare 域名接入验证。", nil)
	}
	if pid == "creating" {
		h, e = s.Provider.Ensure(ctx, host)
	} else {
		h, e = s.Provider.Get(ctx, pid)
	}
	if errors.Is(e, ErrProviderMissing) {
		pid = ""
		return save("error", "域名接入已失效，系统将自动重新申请。", nil)
	}
	if e != nil {
		if state != "active" {
			return save("error", "域名接入服务暂不可用或名额已满，系统会自动重试。", nil)
		}
		return fault.New(503, "域名接入服务暂不可用或名额已满，请稍后重试。")
	}
	if h.Hostname != host || h.ID == "" {
		return fault.New(503, "域名接入状态不一致，请联系平台。")
	}
	if h.Status == "active" && h.SSL.Status == "active" {
		if s.VerificationReady != nil {
			if err := s.VerificationReady(host); err != nil {
				return save("pending_certificate", "HTTPS 已就绪，正在自动配置安全验证，请稍候。", &h)
			}
		}
		return save("active", "域名已启用，HTTPS 证书已就绪。", &h)
	}
	next, message := h.PendingState()
	return save(next, message, &h)
}
func (s *Service) Reconcile(ctx context.Context) error {
	if !s.Enabled() {
		return nil
	}
	rows, e := s.Store.DB.Query(ctx, `SELECT id FROM partner_domains WHERE checked_at IS NULL OR checked_at<now()-CASE WHEN state='active' THEN interval '1 hour' ELSE interval '1 minute' END ORDER BY checked_at NULLS FIRST LIMIT 100`)
	if e != nil {
		return e
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	var first error
	for _, id := range ids {
		if e = s.checkBinding(ctx, id); e != nil && first == nil {
			first = e
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return first
}
