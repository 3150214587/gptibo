-- Apply the manual-suspension policy to existing orders as well as new ones.
-- Recipient locks and the channel's order accounting remain unchanged.
WITH released AS (
    DELETE FROM resource_locks l USING orders o
    WHERE l.order_id=o.id AND o.business='x'
      AND o.manual_payment_at IS NOT NULL
      AND o.status IN ('queued','running','unknown')
      AND (l.resource LIKE 'card:%' OR l.resource LIKE 'card-rule:%' OR l.resource LIKE 'x-account:%')
    RETURNING l.order_id
)
INSERT INTO audit(id,actor_id,action,target,note)
SELECT 'manual-release:'||order_id,'migration','manual_resources_released',order_id,
       '待人工支付订单释放付款卡、X 账号和卡规则；不预留卡片金额，保留原支付及接收账号防重复保护'
FROM (SELECT DISTINCT order_id FROM released) r;
