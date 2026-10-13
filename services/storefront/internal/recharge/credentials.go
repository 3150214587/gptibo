package recharge

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"gptibo/storefront/internal/fault"
	"gptibo/storefront/internal/orders"
	"gptibo/storefront/internal/security"
)

func (s *Service) credentialCipher() (cipher.AEAD, error) {
	secret, err := hex.DecodeString(s.CredentialSecret)
	if err != nil || len(secret) != 32 {
		return nil, fault.New(503, "充值资料加密服务未配置。")
	}
	h := hmac.New(sha256.New, secret)
	h.Write([]byte("storefront:recharge-credentials:v1"))
	block, err := aes.NewCipher(h.Sum(nil))
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
func (s *Service) saveCredential(ctx context.Context, tx pgx.Tx, order, attempt string, c Credential) error {
	aead, err := s.credentialCipher()
	if err != nil {
		return err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return err
	}
	encrypted := aead.Seal(nonce, nonce, []byte(Raw(c)), []byte(order+":"+attempt))
	_, err = tx.Exec(ctx, `INSERT INTO recharge_credentials(attempt_id,order_id,ciphertext,created_at) VALUES($1,$2,$3,$4) ON CONFLICT(attempt_id) DO NOTHING`, attempt, order, encrypted, time.Now().UnixMilli())
	return err
}
func (s *Service) credential(ctx context.Context, q orders.Query, order, attempt string) (Credential, error) {
	var c Credential
	var encrypted []byte
	err := q.QueryRow(ctx, `SELECT ciphertext FROM recharge_credentials WHERE order_id=$1 AND attempt_id=$2`, order, attempt).Scan(&encrypted)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, fault.New(409, "该次充值未留存资料，请让用户在原订单重新提交 Session。")
	}
	if err != nil {
		return c, err
	}
	aead, err := s.credentialCipher()
	if err != nil {
		return c, err
	}
	if len(encrypted) < aead.NonceSize() {
		return c, errors.New("invalid encrypted credential")
	}
	plain, err := aead.Open(nil, encrypted[:aead.NonceSize()], encrypted[aead.NonceSize():], []byte(order+":"+attempt))
	if err != nil {
		return c, errors.New("credential decryption failed")
	}
	if json.Unmarshal(plain, &c) != nil {
		return c, errors.New("invalid credential encoding")
	}
	return c, nil
}

// Explicit admin-only disclosure; the caller authenticates and enforces same-origin.
// Commit the audit before releasing the decrypted value.
func (s *Service) AdminCredential(ctx context.Context, id, attempt, actor string) (Credential, error) {
	tx, err := s.Store.DB.Begin(ctx)
	if err != nil {
		return Credential{}, err
	}
	defer tx.Rollback(ctx)
	current, err := latest(ctx, tx, id)
	if err != nil {
		return Credential{}, err
	}
	if current == nil || current.ID != attempt {
		return Credential{}, orders.Conflict()
	}
	c, err := s.credential(ctx, tx, id, attempt)
	if err != nil {
		return c, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO audit_events(id,actor_role,actor_id,action,target,detail) VALUES($1,'admin',$2,'recharge-credential-copy',$3,$4)`, security.ID(), actor, id, Raw(map[string]string{"attemptId": attempt}))
	if err != nil {
		return Credential{}, err
	}
	return c, tx.Commit(ctx)
}

// Use the original paid order, re-check the latest task, and never submit an
// unknown/running task again. The expected attempt makes double-clicks stale.
func (s *Service) AdminRetry(ctx context.Context, id, attempt, actor string) error {
	return s.locked(ctx, id, func(r Row) error {
		if err := s.sync(ctx, r, true); err != nil {
			return err
		}
		a, err := latest(ctx, s.Store.DB, id)
		if err != nil {
			return err
		}
		if a == nil || a.ID != attempt || (a.Status != "failed" && a.Status != "cancelled") || a.Fallback != 1 {
			return fault.New(409, "最新任务未明确允许补发，请同步状态后核对，勿重复充值。")
		}
		c, err := s.credential(ctx, s.Store.DB, id, attempt)
		if err != nil {
			return err
		}
		in := Input{Key: security.ID(), Confirmed: true}
		if s.Checker != nil {
			checked, e := s.Precheck(ctx, id, in.Key, c)
			if e != nil {
				return e
			}
			if (checked.State != "eligible" && checked.State != "deferred") || checked.Receipt == "" {
				return fault.New(409, "账号未通过充值条件检测，请让客户重新提交资料或等待订阅到期。")
			}
			in.PrecheckReceipt = checked.Receipt
		}
		return s.submit(ctx, id, in, c, r, "admin:"+actor)
	})
}
