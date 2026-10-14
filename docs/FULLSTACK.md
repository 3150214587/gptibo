# GPTibo 商城与供货平台联调

本套程序包含商城前台/管理后台/代理工作台、商城 Go API/Worker、独立供货工作台/API/Worker。商城与供货平台已对接 X 订单链路。供货平台自身支持 GPT 渠道直充；商城原有 GPT 代收接口仍是独立适配器，使用供货平台采购 GPT 商品前，需要另行实现商城适配。

```text
客户 → GPTibo 商城 → 商城自己的支付渠道
                       ↓ 确认付款 + 已核对的接收账号
                 专用渠道签名 API
                       ↓
               供货平台：冻结人民币采购价
                       ↓
               卡台 / X 履约与原付款查询
                       ↓
             成功扣款 / 明确失败释放 / 未知继续核对
                       ↓
              商城 Worker 查询原供货订单并更新状态
```

商城目前采用原单轮询同步，**不要求配置回调**。供货平台的签名 Webhook 面向其他自建渠道可用；不要填写一个商城尚未提供的 X 回调地址。漏掉通知也不会导致换单重发。

## 统一启动

需要 Docker Compose、Python 3。所有端口只绑定本机；这是一套本地/新部署配置，数据和密钥由部署者独立创建。

```sh
python3 scripts/init-fullstack.py
# 首次开户前，在当前终端设置自己的管理员邮箱；密码在私有文件中生成。
export SUPPLY_ADMIN_EMAIL='admin@example.invalid'
docker compose -f compose.fullstack.yaml build
docker compose -f compose.fullstack.yaml up -d storefront-db supply-db
docker compose -f compose.fullstack.yaml run --rm storefront-maintenance migrate
docker compose -f compose.fullstack.yaml run --rm storefront-maintenance grant-runtime
docker compose -f compose.fullstack.yaml run --rm supply-maintenance migrate
docker compose -f compose.fullstack.yaml run --rm supply-maintenance grant-runtime
docker compose -f compose.fullstack.yaml run --rm supply-maintenance bootstrap
docker compose -f compose.fullstack.yaml up -d web storefront-api storefront-worker supply-api supply-worker
```

商城：`http://127.0.0.1:8080`；供货工作台：`http://127.0.0.1:8092`。首次供货管理员密码在 `.secrets/fullstack/supply-admin`，请在本机私下读取，不粘贴到工单/仓库。重复运行初始化不会覆盖原秘密；不要只删除部分秘密后重新生成，否则会破坏旧数据库或加密数据的关联。

商城管理登录还需要自己的管理员身份、TOTP 与邮件配置；默认邮件和所有交易均关闭。先配置 `services/storefront/.env.integrations` 中的管理员及邮件变量（见商城后端 README），并将配置中的 `MAIL_ENABLED` 显式改为 `true`，不能把默认空配置视作可运营环境。

供货平台的开户邀请邮件和人工通知在私有 `services/platform/.env.notifications` 中配置；留空不会发送。

`SUPPLY_ORIGIN`、`STOREFRONT_ORIGIN` 分别控制两站的 origin。正式部署应配置自己的 HTTPS 反向代理，确保两个 origin 与浏览器地址一致。供货服务端 API 连接必须使用证书可信的 HTTPS；没有放宽 TLS 校验来适配容器内 HTTP。域名、证书和外部卡台须由部署者配置。

## 商城连接供货平台

在供货平台创建 GPTibo 专用渠道，核实入账、设置渠道供货价，再用该渠道创建 API Key。不要使用管理员会话作为采购身份。

在 `services/storefront/.env.integrations` 设置：

```dotenv
XGIFT_BACKEND=platform
XGIFT_ENABLED=false
XGIFT_PLATFORM_ENABLED=true
XGIFT_PLATFORM_API_BASE=https://supply.example.invalid/v1
XGIFT_PLATFORM_KEY_ID=your-channel-key-id
XGIFT_PLATFORM_CLIENT_SECRET_FILE=/run/integrations/xgift-platform
# 以下仅为新造的演示上限，单位人民币分；替换为自己认可的限价。
XGIFT_MAX_SUPPLY_MINOR={"x-premium-3m":123,"x-premium-6m":246}
XGIFT_CHECKOUT_ENABLED=false
```

将真实渠道 secret 保存为本地 `services/storefront/.secrets/integrations/xgift-platform`，权限 0600。两个商城进程共享同一配置与秘密。修改后重新创建 API/Worker 容器。初始先保持下单关闭。

商品映射固定为 `x-premium-3m → x-3m`、`x-premium-6m → x-6m`。商城从专用渠道读取实际供货价，在预检时保存报价；报价必须为正数、不超过自设上限、采购余额足够，且供货平台商品与 Worker 就绪。采购以该渠道的实际报价为准。

供货平台开售前必须设置真实商品/收款方/币种/金额、卡规则、自己的赠送账号与执行配置。商城也必须配置自己的支付渠道、支付回调与 `PAYMENT_CHECKOUT_ENABLED=true`，才能在核对后开启 `XGIFT_CHECKOUT_ENABLED=true`。不开启测试收款模拟真实到账。对于 Cloudflare Worker 版本，使用同名服务器变量与秘密，并通过调度触发原有 `scheduled` 订单轮询。

## 异常处理

- 超时：保留商城订单号和预检身份，查询同一供货商户订单；只有供货 API 明确证明原号不存在才使用同一请求号恢复。已知供货订单消失时禁止新建。
- 改价：订单保存旧报价，供货平台收到不一致的 `expected_supply_minor` 会拒绝；不要自动追价采购。
- 配置切换：已有采购保存后端和凭据指纹，切换供应环境/渠道密钥会阻止旧单误投。处理完旧单或保留原配置再切换。
- 失败：供货钱包释放不等于客户退款，商城管理员必须处理原支付渠道售后。未知付款不能直接退回或重新充值。
- 人工恢复：仅允许已确认未付款的安全失败，保留历史尝试、证据及账务，不覆盖原供货订单。

## 验收

自动测试覆盖订单状态、跨服务 TLS/HMAC、供货冻结与扣款、响应丢失与原单恢复，以及跨渠道权限。外部付款与资格检查使用隔离夹具。部署后仍需验证容器启动、HTTPS、邮件、上游资格查询及实际履约；不要将自动测试通过视作真实权益到账。
