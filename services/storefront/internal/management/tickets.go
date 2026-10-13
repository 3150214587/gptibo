package management

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"gptibo/storefront/internal/auth"
	"gptibo/storefront/internal/fault"
	"gptibo/storefront/internal/security"
)

func ticketStatus(status string) bool {
	return status == "open" || status == "processing" || status == "waiting" || status == "resolved"
}

type TicketInput struct {
	Key     string `json:"key"`
	OrderID string `json:"orderId"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

type TicketReplyInput struct {
	Key     string `json:"key"`
	Body    string `json:"body"`
	Status  string `json:"status"`
	Version *int64 `json:"version"`
}

func (s Service) CreateTicket(ctx context.Context, token string, in TicketInput) error {
	in.OrderID, in.Subject, in.Body = strings.TrimSpace(in.OrderID), strings.TrimSpace(in.Subject), strings.TrimSpace(in.Body)
	if !security.ValidID(in.Key) || in.OrderID == "" || !validText(in.OrderID, 128) || in.Subject == "" || !validText(in.Subject, 120) || in.Body == "" || !validText(in.Body, 5000) {
		return fault.New(400, "请填写订单号、问题标题（120 字以内）和问题描述（5000 字以内）。")
	}
	return s.write(ctx, "partner", token, func(tx pgx.Tx, p auth.Principal) error {
		var partner, order, subject, body string
		e := tx.QueryRow(ctx, `SELECT partner_id,order_id,subject,description FROM partner_tickets WHERE id=$1`, in.Key).Scan(&partner, &order, &subject, &body)
		if e == nil {
			if partner == p.Subject && order == in.OrderID && subject == in.Subject && body == in.Body {
				return nil
			}
			return conflict()
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		var owned bool
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM orders WHERE id=$1 AND partner_id=$2)`, in.OrderID, p.Subject).Scan(&owned); e != nil {
			return e
		}
		if !owned {
			return fault.New(404, "未找到该订单，请填写自己商店的本站订单号。")
		}
		now := time.Now().UnixMilli()
		if _, e = tx.Exec(ctx, `INSERT INTO partner_tickets(id,partner_id,order_id,subject,description,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$6)`, in.Key, p.Subject, in.OrderID, in.Subject, in.Body, now); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO partner_ticket_events(id,ticket_id,actor_role,actor_id,body,status,version,created_at) VALUES($1,$1,'partner',$2,$3,'open',0,$4)`, in.Key, p.Subject, in.Body, now); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `UPDATE partner_ticket_notifications SET version=version+1 WHERE id=1`); e != nil {
			return e
		}
		return audit(ctx, tx, p, "ticket-create", in.Key, map[string]any{"orderId": in.OrderID})
	})
}

func (s Service) ReplyTicket(ctx context.Context, role, token, id string, in TicketReplyInput) error {
	in.Body = strings.TrimSpace(in.Body)
	if !security.ValidID(in.Key) || in.Version == nil || *in.Version < 0 || in.Body == "" || !validText(in.Body, 5000) || !ticketStatus(in.Status) || (role == "partner" && in.Status != "open") {
		return fault.New(400, "请填写回复内容（5000 字以内），并选择有效的处理状态。")
	}
	return s.write(ctx, role, token, func(tx pgx.Tx, p auth.Principal) error {
		var partner string
		var version int64
		e := tx.QueryRow(ctx, `SELECT partner_id,version FROM partner_tickets WHERE id=$1 FOR UPDATE`, id).Scan(&partner, &version)
		if errors.Is(e, pgx.ErrNoRows) || (e == nil && role == "partner" && partner != p.Subject) {
			return fault.New(404, "工单不存在。")
		}
		if e != nil {
			return e
		}
		var eventTicket, eventRole, actor, body, status string
		var eventVersion int64
		e = tx.QueryRow(ctx, `SELECT ticket_id,actor_role,actor_id,body,status,version FROM partner_ticket_events WHERE id=$1`, in.Key).Scan(&eventTicket, &eventRole, &actor, &body, &status, &eventVersion)
		if e == nil {
			if eventTicket == id && eventRole == role && actor == p.Subject && body == in.Body && status == in.Status && eventVersion == *in.Version+1 {
				return nil
			}
			return conflict()
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		if version != *in.Version {
			return conflict()
		}
		now := time.Now().UnixMilli()
		if _, e = tx.Exec(ctx, `UPDATE partner_tickets SET status=$1,version=version+1,updated_at=$2 WHERE id=$3`, in.Status, now, id); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO partner_ticket_events(id,ticket_id,actor_role,actor_id,body,status,version,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, in.Key, id, role, p.Subject, in.Body, in.Status, version+1, now); e != nil {
			return e
		}
		if role == "partner" {
			if _, e = tx.Exec(ctx, `UPDATE partner_ticket_notifications SET version=version+1 WHERE id=1`); e != nil {
				return e
			}
		}
		return audit(ctx, tx, p, "ticket-reply", id, map[string]any{"status": in.Status, "version": version + 1})
	})
}

func (s Service) Tickets(ctx context.Context, partner string, page int, query, status string) (map[string]any, error) {
	query = strings.TrimSpace(query)
	if !validText(query, 320) || (status != "" && !ticketStatus(status)) {
		return nil, fault.New(400, "工单筛选条件无效。")
	}
	if page < 1 {
		page = 1
	}
	// One snapshot keeps pagination totals and rows consistent during replies.
	tx, e := s.Store.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	where := ` FROM partner_tickets t JOIN partners p ON p.id=t.partner_id WHERE ($1='' OR t.partner_id=$1) AND ($2='' OR t.status=$2) AND ($3='' OR t.id ILIKE '%'||$3||'%' OR t.order_id ILIKE '%'||$3||'%' OR t.subject ILIKE '%'||$3||'%' OR p.email ILIKE '%'||$3||'%')`
	var total int64
	if e = tx.QueryRow(ctx, `SELECT count(*)`+where, partner, status, query).Scan(&total); e != nil {
		return nil, e
	}
	list, e := rows(ctx, tx, `SELECT t.id,t.order_id,t.subject,t.status,t.version,t.created_at,t.updated_at,p.name AS partner_name,p.email AS partner_email`+where+` ORDER BY t.updated_at DESC,t.id LIMIT 20 OFFSET $4`, partner, status, query, (page-1)*20)
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return map[string]any{"rows": list, "total": total}, nil
}

func (s Service) TicketDetail(ctx context.Context, partner, id string) (map[string]any, error) {
	list, e := rows(ctx, s.Store.DB, `SELECT t.id,t.order_id,t.subject,t.status,t.version,t.created_at,t.updated_at,p.name AS partner_name,p.email AS partner_email,COALESCE((SELECT jsonb_agg(jsonb_build_object('id',e.id,'actor_role',e.actor_role,'body',e.body,'status',e.status,'created_at',e.created_at) ORDER BY e.version) FROM partner_ticket_events e WHERE e.ticket_id=t.id),'[]'::jsonb) AS events FROM partner_tickets t JOIN partners p ON p.id=t.partner_id WHERE t.id=$1 AND ($2='' OR t.partner_id=$2)`, id, partner)
	if e != nil {
		return nil, e
	}
	if len(list) == 0 {
		return nil, fault.New(404, "工单不存在。")
	}
	return list[0], nil
}

func (s Service) TicketNotifications(ctx context.Context) (map[string]any, error) {
	list, e := rows(ctx, s.Store.DB, `SELECT version,(SELECT count(*) FROM partner_tickets WHERE status IN ('open','processing')) AS pending FROM partner_ticket_notifications WHERE id=1`)
	if e != nil {
		return nil, e
	}
	if len(list) == 0 {
		return nil, fault.New(503, "工单提醒暂不可用。")
	}
	return list[0], nil
}
