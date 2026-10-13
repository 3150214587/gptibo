package management

import (
	"context"
	"github.com/jackc/pgx/v5"
	"gptibo/storefront/internal/fault"
	"gptibo/storefront/internal/finance"
	"time"
)

const reportTotals = `count(*) order_count,COALESCE(sum(paid),0)::bigint paid_count,COALESCE(sum(completed),0)::bigint completed_count,COALESCE(sum(paid_cents),0)::bigint paid_cents,COALESCE(sum(refund_cents),0)::bigint refund_cents,COALESCE(sum(paid_cents) FILTER(WHERE completed=1),0)::bigint completed_paid_cents,COALESCE(sum(refund_cents) FILTER(WHERE completed=1),0)::bigint completed_refund_cents,COALESCE(sum(cost_cents),0)::bigint cost_cents,COALESCE(sum(commission_cents),0)::bigint commission_cents,CASE WHEN count(*)=count(profit_cents) THEN COALESCE(sum(profit_cents),0)::bigint ELSE NULL END profit_cents,count(*) FILTER(WHERE completed=1 AND cost_cents IS NULL) missing_cost_count,count(*) FILTER(WHERE completed=1 AND commission_cents IS NULL) missing_commission_count,COALESCE(sum(held) FILTER(WHERE completed=1),0)::bigint held_count`

func (s Service) AccountingReport(ctx context.Context, page int, status string, start, end *int64) (map[string]any, error) {
	if page < 1 || page > 10000 || (start != nil && (*start < 0 || *start > 99999999999999)) || (end != nil && (*end < 0 || *end > 99999999999999)) || (start != nil && end != nil && *start >= *end) {
		return nil, fault.New(400, "时间范围或页码无效。")
	}
	if status != "all" && status != "pending" && status != "session" && status != "processing" && status != "success" && status != "failed" && status != "cancelled" && status != "refunded" {
		return nil, fault.New(400, "订单状态无效。")
	}
	source := `SELECT o.id,o.plan,o.plan_name,o.status,o.payment_status,o.partner_name,o.created_at,
 CASE WHEN o.payment_status='paid' THEN 1 ELSE 0 END paid,
 CASE WHEN o.payment_status='paid' AND o.status='success' THEN 1 ELSE 0 END completed,
 CASE WHEN o.payment_status='paid' THEN o.price_cents ELSE 0 END paid_cents,
 CASE WHEN o.payment_status='paid' THEN greatest(o.refunded_cents,COALESCE(t.refunded_cents,0)) ELSE 0 END refund_cents,
 CASE WHEN o.payment_status='paid' AND o.status='success' THEN COALESCE(i.cost_cents,CASE WHEN o.plan='gmail-account' THEN k.actual_cost_cents WHEN n.order_id IS NOT NULL THEN n.actual_cost_cents ELSE c.cost_cents END) ELSE 0 END cost_cents,
 CASE WHEN o.payment_status<>'paid' OR o.status<>'success' OR o.partner_id IS NULL THEN 0 WHEN o.cost_cents IS NULL OR (t.order_id IS NOT NULL AND t.merchant_margin_cents IS NULL) THEN NULL ELSE ` + ("(" + finance.Profit("o") + "+COALESCE(partner_order_profit(o.id,o.upstream_partner_id),0))") + ` END commission_cents,
 greatest(o.refund_hold,COALESCE(t.financial_hold,0)) held
 FROM orders o LEFT JOIN recharge_orders t ON t.order_id=o.id LEFT JOIN account_supplier_orders k ON k.order_id=o.id LEFT JOIN delivery_supplier_orders n ON n.order_id=o.id LEFT JOIN accounting_items i ON i.order_id=o.id AND i.kind='order' LEFT JOIN accounting_costs c ON c.plan=o.plan
 WHERE o.mode='live' AND o.is_test=0 AND ($1::bigint IS NULL OR o.created_at>=$1) AND ($2::bigint IS NULL OR o.created_at<$2) AND ($3='all' OR ($3='refunded' AND greatest(o.refunded_cents,COALESCE(t.refunded_cents,0))>0) OR o.status=$3)`
	base := `WITH report AS (SELECT q.*,CASE WHEN completed=0 THEN 0 WHEN held=1 THEN NULL ELSE paid_cents-refund_cents-cost_cents-commission_cents END profit_cents FROM (` + source + `) q) `
	tx, e := s.Store.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	totals, e := rows(ctx, tx, base+`SELECT `+reportTotals+` FROM report`, start, end, status)
	if e != nil {
		return nil, e
	}
	groups, e := rows(ctx, tx, base+`SELECT plan,max(plan_name) plan_name,`+reportTotals+` FROM report GROUP BY plan ORDER BY plan`, start, end, status)
	if e != nil {
		return nil, e
	}
	list, e := rows(ctx, tx, base+`SELECT * FROM report ORDER BY created_at DESC,id DESC LIMIT 20 OFFSET $4`, start, end, status, (page-1)*20)
	if e != nil {
		return nil, e
	}
	return map[string]any{"totals": totals[0], "groups": groups, "orders": list, "page": page, "asOf": time.Now().UnixMilli(), "start": start, "end": end, "status": status}, tx.Commit(ctx)
}
