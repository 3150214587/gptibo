package procurement

import (
	"context"
	"github.com/jackc/pgx/v5"
	"gptibo/storefront/internal/fault"
	"gptibo/storefront/internal/orders"
	"gptibo/storefront/internal/security"
	"strings"
	"time"
	"unicode/utf8"
)

func (s *Service) View(ctx context.Context, kind, id string) (map[string]any, error) {
	o, e := orders.Get(ctx, s.Store.DB, id, nil, false)
	if e != nil {
		return nil, e
	}
	if Kind(o.Plan) != kind {
		return nil, orders.Conflict()
	}
	p, e := find(ctx, s.Store.DB, kind, id)
	if e == pgx.ErrNoRows {
		return map[string]any{"purchase": nil}, nil
	}
	if e != nil {
		return nil, e
	}
	v := map[string]any{"state": p.State, "supplierOrder": p.Supplier, "actualCostCents": p.Cost, "updatedAt": p.Updated}
	if kind == "gmail" {
		v["goodsId"] = p.Goods
		v["goodsName"] = p.GoodsName
		v["maxCostCents"] = p.Ceiling
	} else {
		v["actualUsdCents"] = p.USD
		v["actualUsdMicros"] = p.USDMicros
		v["cnyPerUsd"] = p.FX
	}
	return map[string]any{"purchase": v}, nil
}

type AdminInput struct {
	Action       string `json:"action"`
	Confirmed    bool   `json:"confirmed"`
	Note         string `json:"note"`
	OrderVersion *int64 `json:"orderVersion"`
	Updated      *int64 `json:"updatedAt"`
}

func (s *Service) Act(ctx context.Context, kind, id, token string, in AdminInput) error {
	if in.Action == "" || in.Action == "sync" {
		if _, e := s.Auth.Require(ctx, s.Store.DB, "admin", token); e != nil {
			return e
		}
		o, e := orders.Get(ctx, s.Store.DB, id, nil, false)
		if e != nil {
			return e
		}
		if Kind(o.Plan) != kind {
			return orders.Conflict()
		}
		return s.Fulfill(ctx, id)
	}
	if in.Action != "confirm-not-purchased" && !(kind == "delivery_supplier" && in.Action == "retry-rejected-receipt") {
		return fault.New(400, "采购操作无效。")
	}
	if !in.Confirmed || in.OrderVersion == nil || in.Updated == nil || utf8.RuneCountInString(strings.TrimSpace(in.Note)) < 6 || utf8.RuneCountInString(in.Note) > 500 {
		return fault.New(409, "请核对原采购单无扣款、无发货，并填写核查说明。")
	}
	tx, e := s.Store.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	actor, e := s.Auth.Require(ctx, tx, "admin", token)
	if e != nil {
		return e
	}
	o, e := orders.Get(ctx, tx, id, nil, true)
	if e != nil {
		return e
	}
	p, e := find(ctx, tx, kind, id)
	if e != nil {
		return e
	}
	if Kind(o.Plan) != kind || o.Version != *in.OrderVersion || p.Updated != *in.Updated || p.Until > time.Now().UnixMilli() {
		return orders.Conflict()
	}
	action, note := kind+"-purchase-reconciled", "管理员确认原采购单无扣款及无发货："+strings.TrimSpace(in.Note)
	next := o
	next.Status = "failed"
	if in.Action == "retry-rejected-receipt" {
		var exact bool
		e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM orders o JOIN order_events e ON e.id=o.last_event_id WHERE o.id=$1 AND e.action='delivery_supplier-receipt-rejected') AND NOT EXISTS(SELECT 1 FROM email_deliveries WHERE order_id=$1)`, id).Scan(&exact)
		if e != nil {
			return e
		}
		if !exact || p.Key != "ymx/"+id || p.State != "rejected" || p.Supplier != nil || p.Value != nil || o.Status != "failed" || o.Mode != "live" || o.Test != 0 || o.Payment != "paid" || o.Hold != 0 || o.Refunded != 0 || o.Blocked != 0 {
			return orders.Conflict()
		}
		_, e = tx.Exec(ctx, `UPDATE delivery_supplier_orders SET request_key=$1,state='prepared',attempted_at=NULL,next_check_at=0,lease_token=$2,updated_at=$3 WHERE order_id=$4`, "ymx_"+id, security.ID(), time.Now().UnixMilli(), id)
		next.Status = "session"
		action = "delivery_supplier-receipt-recovered"
		note = "核实原请求被拒绝且未采购，修正编号恢复原订单：" + strings.TrimSpace(in.Note)
	} else {
		if (p.State != "creating" && p.State != "unknown") || p.Cards != nil || p.Value != nil || (kind == "delivery_supplier" && p.Supplier != nil) {
			return orders.Conflict()
		}
		_, e = tx.Exec(ctx, `UPDATE `+table(kind)+` SET state='rejected',lease_token=$1,updated_at=$2 WHERE order_id=$3`, security.ID(), time.Now().UnixMilli(), id)
	}
	if e != nil {
		return e
	}
	if _, e = orders.Change(ctx, tx, o, next, "admin:"+actor.Subject, action, note); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *Service) Goods(ctx context.Context, page int, query string, id int64) (any, error) {
	if s.AccountSupplier == nil {
		return nil, fault.New(503, "Gmail 供应商尚未配置。")
	}
	if id > 0 {
		g, e := s.AccountSupplier.Goods(ctx, id)
		if e != nil {
			return nil, e
		}
		price, e := cents(g.Price)
		if e != nil {
			return nil, e
		}
		return map[string]any{"goodsId": g.ID, "name": g.Name, "priceCents": price, "stock": g.Stock, "enabled": g.Status == 1}, nil
	}
	if page < 1 || page > 1000 || utf8.RuneCountInString(query) > 100 {
		return nil, fault.New(400, "查询条件无效。")
	}
	var result struct {
		Count int64   `json:"allCount"`
		Pages int64   `json:"allPage"`
		Items []Goods `json:"items"`
	}
	e := s.AccountSupplier.Call(ctx, "/goods/getList", map[string]any{"page": page, "goodsType": "", "keyWord": query, "skuType": "", "showDirId": "", "dirId": "", "brandId": ""}, &result)
	if e != nil {
		return nil, e
	}
	if result.Items == nil {
		return nil, fault.New(502, "商品列表格式异常。")
	}
	values := []any{}
	for _, g := range result.Items {
		price, e := cents(g.Price)
		if e != nil {
			return nil, e
		}
		values = append(values, map[string]any{"goodsId": g.ID, "name": g.Name, "priceCents": price, "enabled": g.Status == 1})
	}
	return map[string]any{"total": result.Count, "pages": result.Pages, "goods": values}, nil
}
