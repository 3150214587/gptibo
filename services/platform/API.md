# 渠道 API v1

Base URL `https://supply.example.invalid/v1`，全接口使用服务端 HMAC-SHA256。金额均为人民币分整数；实际供货价由平台/渠道价格配置决定。调用方可提供预期价格以防改价，但不能指定实际扣款金额。

管理员商品页自动调用 `GET /api/admin/gpt-plans` 读取卡台 GPT 订阅目录，返回 `data: [{code, name, enabled, purchasable}]`。该接口仅允许管理员会话；`code` 是上游订单使用的注册表 key，名称保持上游原值。商品配置通过选择套餐保存绑定，不能按展示名称猜测上游套餐映射。目录失败不会清空旧绑定；`POST /api/admin/products/:id` 省略 `upstream_plan` 保留原值，显式空串在商品关闭时清除绑定。新增绑定必须存在于实时目录，商品开放时要求对应套餐启用且可购买；查询失败或配置并发变更拒绝开售。

| 方法 | 地址 | 内容 |
| --- | --- | --- |
| GET | /products | code、name、business、supply_minor、enabled、available、unavailable_reason |
| GET | /balance | currency=CNY、available_minor、frozen_minor |
| POST | /preflight | product_code、recipient 或 credential，查询账号资格 |
| POST | /orders | 幂等创建自动充值订单 |
| GET | /orders?limit=50&offset=0 | 本渠道订单，limit 最大 100，支持 status、search |
| GET | /orders/:id | 本渠道原订单 |
| GET | /orders/by-merchant/:number | 精确查询本渠道原商户订单号，用于创建响应丢失后的恢复 |

## 签名

请求头 X-Key-Id、X-Timestamp（Unix 秒）、X-Nonce（16–128 字符，单次使用）、X-Signature。允许时间偏差 300 秒，下单加 Idempotency-Key（8–128 字符，重试保持不变）。排序查询键值时按 URL 编码后的键和值分别排序，RFC3986 编码，空格为 %20，保留重复参数。

以下八行以换行符连接，无结尾换行：

```text
METHOD
PATH
CANONICAL_QUERY
TIMESTAMP
NONCE
KEY_ID
IDEMPOTENCY_KEY
SHA256_RAW_BODY
```

使用 Secret 对此 UTF-8 文本做 HMAC-SHA256，输出小写 hex。GET 的 body 是空字符串。POST 的原始 JSON 字节必须与签名完全一致。重试使用新 Nonce，保持原商户订单号、幂等键和原请求资料。

## 请求

```json
{"merchant_order_no":"channel-order-001","product_code":"gpt-plus","credential":{"mode":"session","session":"<完整 Session JSON 文本>"}}
```

```json
{"merchant_order_no":"channel-order-002","product_code":"gpt-10x","credential":{"mode":"access_token","accessToken":"<token>"}}
```

```json
{"merchant_order_no":"channel-order-003","product_code":"gpt-25x","credential":{"mode":"mailbox","email":"customer@example.com","password":"<password>"}}
```

```json
{"merchant_order_no":"channel-order-004","product_code":"x-3m","recipient":"username"}
```

mode 一次只能填写一种完整资料。X 自动规范 @前缀和用户名大小写。相同幂等键或订单号请求改变资料返回 409 idempotency_conflict。

GPT 网页/API 可先调用预检获得真实 email；创建请求可带 `confirmed_account`（确认过的邮箱）和 `expected_supply_minor`（确认过的人民币供货价）。价格变化返回 409 `price_changed`，不冻结资金；执行时两次预检账号不一致会停止充值。重试必须保留这两个字段的原值，连同原凭据、商户订单号和幂等键保持不变。

X 预检同时核对实时套餐报价与收货人身份。主站接入使用 `x-3m` / `x-6m`，下单传 `confirmed_recipient_id`（预检返回的 X 数字 ID）及 `expected_supply_minor`；派发前身份改变会停止创建赠送账单。原单恢复必须保留这两项，避免改价或用户名归属变化导致错误充值。接口返回的是人民币供货成本；实际支付币种和金额以商品配置及经核对的上游报价为准。

`enabled` 是管理员商品开关，`available` 同时检查本地执行配置、卡规则与 Worker 存活。上游最终购买资格由实时预检确认。不能把“available”当作真实到账证明。新单在配置不齐、专用卡规则停用或 Worker 不可用时不会冻结资金。每个身份总请求限流 300 次/分钟，预检另限 12 次/分钟。

管理后台卡规则 `use_provider_limits=true` 表示沿用卡台默认消费限额：开卡不传消费限额字段，用卡不覆盖当前限额。默认 false，继续校验 `max_order_minor` 对应的单笔上限。该策略和卡头、指定卡 ID 一起固定在订单快照中；不改变自动注资开关、注资预算、实时报价或成功凭证校验。

自动用卡规则分别设置 `refill_threshold_minor`（低于此美元分余额才补款）、`funding_minor`（每次补款美元分）、`open_amount_minor`（开卡入金美元分）。阈值和开卡金额为 0 时兼容原规则，均使用 funding_minor。X 可启用 `replace_on_decline`：必须确认当前支付方式被拒且 PaymentIntent 未收款，才停用原卡、在原 Checkout 使用新卡；超时、处理中、3DS 验证及重复交易不触发换卡。`max_replacements` 为每单最多自动换卡数（0–5）；达到上限保留待核，不继续开卡。新卡按规则版本复用，各次开卡／补款使用独立持久化凭证号；结果未知不会重复发起。

## 响应与状态

成功 `{ "data": ... }`；首次创建 201，幂等重试 200。错误 `{ "error": { "code": "...", "message": "..." } }`，含 400 资料无效、401 签名/身份无效、403 无权限、404 不存在、409 余额不足/业务关闭/幂等冲突、429 限流、500 待核。

queued 排队、running 执行中、unknown 待核对、succeeded 完成扣款、failed 明确失败退回。网络错误或 unknown 不代表未派发，不得换新订单号重充。供货价在下单事务中快照，后续改价不影响原单。

创建超时先用 `/orders/by-merchant/:number` 查询；查询 404 仅表示当前没有可见受理记录，不能证明原请求绝不会受理。需要重试时仍用原始 body 与原幂等键、新 Nonce。原单查询与幂等恢复不受商品后续关闭或 Worker 临时离线影响；渠道封禁仍拒绝访问。

GPT 以绑定原请求、套餐、卡 ID、账号的上游订单完成状态为依据。上游 failed 只有显式、有效的 `final_amount_minor=0` 才证明无实际扣费；金额缺失、非法或非零继续 unknown 并冻结。该金额是上游支付币种，不用于计算人民币采购余额或美元开卡成本。X 的完成证据是原赠送 Checkout 的支付成功；独立的账号权益到账尚需生产业务验收，不把付款状态描述成独立权益查询结果。订单只返回脱敏 account_label 与平台凭证，不返回充值凭据、原始卡数据或秘密。

## 回调

在渠道工作台保存 HTTPS URL 与至少 32 字符密钥。收到 POST JSON `{"id":"evt_...","type":"order.succeeded","data":{...订单}}`。请求头 X-Event-Id、X-Timestamp、X-Signature。

签名 `HMAC-SHA256(secret, timestamp + "." + event_id + "." + raw_body)`，小写 hex。校验时间、签名及 event ID 后按订单 ID 查询原单，持久化去重，返回 2xx。重复/乱序通知不能覆盖已确认终态。最多尝试 10 次，指数退避；渠道工作台可查看投递状态。
