package httpapi

import (
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"gptibo/platform/internal/commerce"
)

func (s *Server) guardPairChange(c *gin.Context, tx pgx.Tx, b commerce.CardRule) error {
	var busy bool
	if err := tx.QueryRow(c, `SELECT EXISTS(SELECT 1 FROM card_rules r WHERE business=$1 AND product_code=$2 AND (
  EXISTS(SELECT 1 FROM resource_locks WHERE resource='card-rule:'||r.id)
  OR EXISTS(SELECT 1 FROM card_replacements WHERE rule_id=r.id AND status<>'completed')
 ))`, b.Business, b.ProductCode).Scan(&busy); err != nil {
		return err
	}
	if busy {
		return commerce.Fail("card_rule_busy", "卡片正在执行订单或换卡，请完成核对后修改规则", 409)
	}
	// Do not reset the pool with stale configured IDs after an automatic replacement.
	for _, id := range []int64{b.CardID, b.BackupCardID} {
		if id <= 0 {
			continue
		}
		var blocked bool
		if err := tx.QueryRow(c, `SELECT EXISTS(SELECT 1 FROM card_retirements WHERE card_id=$1)
   OR EXISTS(SELECT 1 FROM resource_locks WHERE resource LIKE 'card:%:'||$1::text)`, id).Scan(&blocked); err != nil {
			return err
		}
		if blocked {
			return commerce.Fail("card_busy", "卡片已停用或正在使用，无法修改分配", 409)
		}
	}
	return nil
}
