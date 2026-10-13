package management

import (
	"context"
	"regexp"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"
	"gptibo/storefront/internal/auth"
	"gptibo/storefront/internal/fault"
)

type PayoutAccount struct {
	Phone   string `json:"phone"`
	Name    string `json:"name"`
	Version int64  `json:"version"`
}

type PayoutAccountInput struct {
	Phone   string `json:"phone"`
	Name    string `json:"name"`
	Version *int64 `json:"version"`
}

var alipayPhone = regexp.MustCompile(`^1[3-9][0-9]{9}$`)

func normalizePayoutAccount(in PayoutAccountInput) (PayoutAccount, error) {
	a := PayoutAccount{Phone: strings.TrimSpace(in.Phone), Name: strings.TrimSpace(in.Name)}
	if !alipayPhone.MatchString(a.Phone) || a.Name == "" || !validText(a.Name, 50) || strings.ContainsFunc(a.Name, unicode.IsControl) || in.Version == nil || *in.Version < 0 {
		return a, fault.New(400, "请填写有效的支付宝手机号和收款人姓名（最多 50 字）。")
	}
	a.Version = *in.Version
	return a, nil
}

func (s Service) SavePayoutAccount(ctx context.Context, token string, in PayoutAccountInput) error {
	a, e := normalizePayoutAccount(in)
	if e != nil {
		return e
	}
	return s.write(ctx, "partner", token, func(tx pgx.Tx, p auth.Principal) error {
		if _, e := tx.Exec(ctx, `INSERT INTO partner_payout_accounts(partner_id) VALUES($1) ON CONFLICT DO NOTHING`, p.Subject); e != nil {
			return e
		}
		tag, e := tx.Exec(ctx, `UPDATE partner_payout_accounts SET phone=$1,name=$2,version=version+1 WHERE partner_id=$3 AND version=$4`, a.Phone, a.Name, p.Subject, a.Version)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return fault.New(409, "收款信息已更新，请刷新后重新确认。")
		}
		return audit(ctx, tx, p, "partner-payout-account", p.Subject, map[string]any{"version": a.Version + 1, "note": "更新支付宝收款信息"})
	})
}

func payoutRecipient(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, partner string, version *int64, lock bool) (string, error) {
	var a PayoutAccount
	sql := `SELECT phone,name,version FROM partner_payout_accounts WHERE partner_id=$1`
	if lock {
		sql += ` FOR UPDATE`
	}
	if e := q.QueryRow(ctx, sql, partner).Scan(&a.Phone, &a.Name, &a.Version); e != nil {
		if e == pgx.ErrNoRows {
			return "", fault.New(409, "请先绑定支付宝手机号和收款人姓名。")
		}
		return "", e
	}
	if a.Phone == "" || a.Name == "" {
		return "", fault.New(409, "请先绑定支付宝手机号和收款人姓名。")
	}
	if version == nil || *version != a.Version {
		return "", fault.New(409, "收款信息已更新，请刷新后重新确认提现。")
	}
	return a.Phone + " / " + a.Name, nil
}
