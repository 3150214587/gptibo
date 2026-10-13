package recharge

import (
	"context"
	"errors"
	"gptibo/storefront/internal/orders"
	"gptibo/storefront/internal/security"
	"time"
)

type fulfillmentRef struct {
	ID     string `json:"fulfillment_id"`
	Order  string `json:"order_id"`
	Number int64  `json:"attempt_no"`
}

// A lost submission response or a retry made upstream can change the current
// task. Resolve the latest attempt from the original order before applying it.
func (s *Service) syncLatest(ctx context.Context, r Row, a *Attempt, hasFulfillment bool) error {
	if r.Remote == nil {
		return nil
	}
	if !hasFulfillment && a == nil {
		return nil
	}
	var refs []fulfillmentRef
	if err := s.Client.Request(ctx, "/orders/"+*r.Remote+"/fulfillments", nil, "", &refs); err != nil {
		return err
	}
	var newest *fulfillmentRef
	for i := range refs {
		v := &refs[i]
		if v.Order != *r.Remote || !remoteID.MatchString(v.ID) || v.Number < 1 || v.Number > 9007199254740991 {
			return errors.New("invalid fulfillment identity")
		}
		if newest == nil || v.Number > newest.Number {
			newest = v
		} else if v.Number == newest.Number && v.ID != newest.ID {
			return errors.New("duplicate fulfillment attempt")
		}
	}
	if newest == nil {
		if a != nil && a.Remote != nil {
			return errors.New("current fulfillment missing")
		}
		if hasFulfillment {
			return errors.New("fulfillment summary without task")
		}
		return nil
	}
	if a != nil && newest.Number < a.Number {
		// A later submission can remain unconfirmed while the upstream list
		// still contains only the preceding task. Preserve that uncertainty;
		// never bind its older result or grant permission to create a new one.
		if a.Status == "unknown" && a.Remote == nil {
			return nil
		}
		return errors.New("stale fulfillment list")
	}
	var v Redemption
	if err := s.Client.Request(ctx, "/redemptions/"+newest.ID, nil, "", &v); err != nil {
		return err
	}
	if v.ID != newest.ID || v.Order != newest.Order || v.Attempt == nil || *v.Attempt != newest.Number || v.Fallback == nil {
		return errors.New("latest redemption identity mismatch")
	}
	if a == nil || newest.Number > a.Number {
		tx, err := s.Store.DB.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		if _, err = orders.Get(ctx, tx, r.OrderID, nil, true); err != nil {
			return err
		}
		if _, err = s.fence(ctx, tx, r); err != nil {
			return err
		}
		current, err := latest(ctx, tx, r.OrderID)
		if err != nil {
			return err
		}
		if (a == nil && current != nil) || (a != nil && (current == nil || current.ID != a.ID)) {
			return orders.Conflict()
		}
		a = &Attempt{ID: security.ID(), Order: r.OrderID, Number: newest.Number, Mode: "session", Status: "unknown", Created: time.Now().UnixMilli()}
		_, err = tx.Exec(ctx, `INSERT INTO recharge_redemptions(id,order_id,attempt_no,request_hash,credential_mode,created_at) VALUES($1,$2,$3,'','session',$4)`, a.ID, a.Order, a.Number, a.Created)
		if err != nil {
			return err
		}
		if err = tx.Commit(ctx); err != nil {
			return err
		}
	}
	return s.applyRedemption(ctx, r, *a, v)
}
