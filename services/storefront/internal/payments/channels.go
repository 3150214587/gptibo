package payments

import (
	"context"
	"encoding/json"
	"gptibo/storefront/internal/auth"
	"gptibo/storefront/internal/fault"
	"gptibo/storefront/internal/orders"
	"gptibo/storefront/internal/security"
)

func (s Service) Lookup(id string) Provider {
	if p := s.Providers[id]; p != nil {
		return p
	}
	if s.Provider != nil && s.Provider.ID() == id {
		return s.Provider
	}
	return nil
}
func (s Service) Resolve(id, account string) (Provider, error) {
	p := s.Lookup(id)
	if p == nil || p.Account() != account {
		return nil, fault.New(503, "支付商户配置已变化，请恢复原订单渠道后处理。")
	}
	return p, nil
}

// Existing payments resolve their merchant snapshot. New payments select only
// enabled methods, under the transaction that creates their unique payment row.
func Method(id string) string {
	if id == "gatewaya-wxpay" || id == "gatewayb-wxpay" {
		return "wxpay"
	}
	return "alipay"
}

type MethodOption struct {
	FeeBPS int64  `json:"feeBps"`
	Method string `json:"method"`
	Name   string `json:"name"`
}

func (s Service) enabled(ctx context.Context, q orders.Query, lock bool) (string, []string, error) {
	var def string
	var ids []string
	sql := `SELECT provider,enabled_providers FROM payment_settings WHERE id=true`
	if lock {
		sql += ` FOR SHARE`
	}
	if e := q.QueryRow(ctx, sql).Scan(&def, &ids); e != nil {
		return "", nil, e
	}
	if def == "" && s.Provider != nil {
		def = s.Provider.ID()
	}
	if ids == nil {
		ids = []string{}
		if def != "" {
			ids = append(ids, def)
		}
	}
	return def, ids, nil
}
func (s Service) Methods(ctx context.Context, q orders.Query) ([]MethodOption, error) {
	_, ids, e := s.enabled(ctx, q, false)
	if e != nil {
		return nil, e
	}
	r, e := rates(ctx, q)
	if e != nil {
		return nil, e
	}
	out := []MethodOption{}
	for _, id := range ids {
		if s.Lookup(id) != nil {
			name := "支付宝"
			if Method(id) == "wxpay" {
				name = "微信支付"
			}
			out = append(out, MethodOption{Method: Method(id), Name: name, FeeBPS: r[id]})
		}
	}
	return out, nil
}
func (s Service) Selected(ctx context.Context, q orders.Query, methods ...string) (Provider, error) {
	method := ""
	if len(methods) > 0 {
		method = methods[0]
	}
	if method != "" && method != "alipay" && method != "wxpay" {
		return nil, fault.New(400, "支付方式无效。")
	}
	def, ids, e := s.enabled(ctx, q, true)
	if e != nil {
		return nil, e
	}
	for _, id := range ids {
		if (method != "" && Method(id) == method) || (method == "" && id == def) {
			if p := s.Lookup(id); p != nil {
				return p, nil
			}
		}
	}
	if method == "" && len(ids) > 0 {
		if p := s.Lookup(ids[0]); p != nil {
			return p, nil
		}
	}
	return nil, fault.New(409, "该支付方式当前不可用，请刷新后选择。")
}

type Channel struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Configured bool   `json:"configured"`
}
type ChannelSettings struct {
	FeeRatesBPS      map[string]int64 `json:"feeRatesBps"`
	Provider         string           `json:"provider"`
	EnabledProviders []string         `json:"enabledProviders"`
	Version          int64            `json:"version"`
	Checkout         bool             `json:"checkout"`
	Channels         []Channel        `json:"channels"`
}

func (s Service) Settings(ctx context.Context) (ChannelSettings, error) {
	v := ChannelSettings{Checkout: s.Checkout, Channels: []Channel{{ID: "alipay", Name: "支付宝官方"}, {ID: "gatewaya-alipay", Name: "德云付 · 支付宝"}, {ID: "gatewaya-wxpay", Name: "德云付 · 微信支付"}, {ID: "gatewayb-alipay", Name: "卓付 · 支付宝"}, {ID: "gatewayb-wxpay", Name: "卓付 · 微信支付"}}}
	e := s.Store.DB.QueryRow(ctx, `SELECT provider,version,enabled_providers,fee_rates_bps FROM payment_settings WHERE id=true`).Scan(&v.Provider, &v.Version, &v.EnabledProviders, &v.FeeRatesBPS)
	if e != nil {
		return v, e
	}
	if v.Provider == "" && s.Provider != nil {
		v.Provider = s.Provider.ID()
	}
	if v.EnabledProviders == nil {
		v.EnabledProviders = []string{}
		if v.Provider != "" {
			v.EnabledProviders = append(v.EnabledProviders, v.Provider)
		}
	}
	for i := range v.Channels {
		v.Channels[i].Configured = s.Lookup(v.Channels[i].ID) != nil
	}
	return v, nil
}
func (s Service) SetChannel(ctx context.Context, a *auth.Service, token, id string, version int64) error {
	return s.SetChannels(ctx, a, token, []string{id}, version)
}
func (s Service) SetChannels(ctx context.Context, a *auth.Service, token string, ids []string, version int64, feeRates ...map[string]int64) error {
	var nextRates map[string]int64
	if len(feeRates) > 0 {
		nextRates = feeRates[0]
	}
	for id, bps := range nextRates {
		if !validChannel(id) || bps < 0 || bps > 10000 {
			return fault.New(400, "手续费率须为 0–100%，最多两位小数。")
		}
	}
	if len(ids) < 1 || len(ids) > 2 {
		return fault.New(400, "请至少启用一种支付方式。")
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if !validChannel(id) {
			return fault.New(400, "支付渠道无效。")
		}
		if seen[Method(id)] {
			return fault.New(400, "每种支付方式只能选择一个收款渠道。")
		}
		seen[Method(id)] = true
		if s.Lookup(id) == nil {
			return fault.New(409, "请先配置该渠道的商户密钥。")
		}
		if _, e := s.notifyURL(id); e != nil {
			return e
		}
	}
	tx, e := s.Store.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	p, e := a.Require(ctx, tx, "admin", token)
	if e != nil {
		return e
	}
	var current int64
	if e = tx.QueryRow(ctx, `SELECT version FROM payment_settings WHERE id=true FOR UPDATE`).Scan(&current); e != nil {
		return e
	}
	if current != version {
		return fault.New(409, "支付设置已被更新，请刷新后重试。")
	}
	_, previous, e := s.enabled(ctx, tx, false)
	if e != nil {
		return e
	}
	previousRates, e := rates(ctx, tx)
	if e != nil {
		return e
	}
	if nextRates == nil {
		nextRates = previousRates
	}
	if _, e = tx.Exec(ctx, `UPDATE payment_settings SET provider=$1,enabled_providers=$2,fee_rates_bps=$3,version=version+1,updated_at=now() WHERE id=true`, ids[0], ids, nextRates); e != nil {
		return e
	}
	detail, _ := json.Marshal(map[string]any{"from": previous, "to": ids, "feeRatesBefore": previousRates, "feeRatesAfter": nextRates, "note": "更新本站支付宝和微信收款渠道"})
	if _, e = tx.Exec(ctx, `INSERT INTO audit_events(id,actor_role,actor_id,action,target,detail) VALUES($1,'admin',$2,'payment-channel-changed','payment-settings',$3)`, security.ID(), p.Subject, detail); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

func validChannel(id string) bool {
	return id == "alipay" || id == "gatewaya-alipay" || id == "gatewaya-wxpay" || id == "gatewayb-alipay" || id == "gatewayb-wxpay"
}
