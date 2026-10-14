# GPTibo 供货平台

独立的 Go + PostgreSQL 供货 API、异步执行 Worker 和 React 管理/渠道工作台。支持 X 赠送及 GPT 直充；GPTibo 商城已经提供 X 订单适配，完整配置见 [商城联调指南](../../docs/FULLSTACK.md)。

平台采用渠道人民币预充值钱包：下单冻结供货价，确认成功扣款，明确失败释放，结果未知继续查询原单。商城面向客户的收款、退款与供货平台的采购账务分开，不把采购失败当作客户已退款。

## 架构与交易边界

API 与 Worker 共用供货数据库，与商城数据库分离。渠道调用采用 HMAC-SHA256、时间戳和 Nonce 防重放；凭据采用 AES-256-GCM 加密并绑定用途及记录 ID。订单保存商品、报价、账号、卡规则和连接快照，任务使用数据库租约与资源锁，资金变化通过事务和不可变流水约束。

卡台域名由服务器允许名单约束，禁止重定向和私网解析。X 商品的收款方、币种、商品和金额逐单核验。管理员管理渠道及执行配置，渠道只能访问自己的订单和钱包；普通 API 不返回卡号、Cookie 或原始上游响应。

商品默认关闭，供货价为零，账号、卡台密钥和商品绑定需自行配置。卡台协议为匿名适配器，不承诺兼容任意供应商。本项目仅支持新数据库安装。

## 本地开发

需要 Go 1.26+、Node.js 24 和 PostgreSQL 18。复制 `.env.example` 为私有配置，生成独立 32 字节十六进制主密钥；程序读取进程环境，启动前自行注入配置。支持 `DATABASE_URL_FILE`、`MASTER_KEY_FILE`、`APP_DB_PASSWORD_FILE`、`ADMIN_PASSWORD_FILE` 等秘密文件，不可同时设置同名明文变量。

```sh
cd services/platform/web
npm ci
npm run build
cd ..
go run ./cmd/platform migrate
go run ./cmd/platform grant-runtime
go run ./cmd/platform bootstrap
go run ./cmd/platform api
# 另一个终端，同样加载运行角色配置
go run ./cmd/platform worker
```

迁移/授权/首次管理员开户使用维护角色；API 和 Worker 使用 `platform_app` 受限角色。`bootstrap` 只创建首位管理员，不重置已有密码。前端开发：`cd web && npm run dev`。

推荐从仓库根目录使用 [统一 Compose](../../compose.fullstack.yaml)，秘密初始化和运行命令见联调指南。单独部署也可使用 `deploy/compose.yaml`，填写自己的 origin、数据库密码和管理员资料。

## 接入顺序

1. 在 API、Worker 环境中设置 `CARD_API_ALLOWED_HOSTS`，只填可信卡台的精确域名，不带协议、端口或路径。
2. 管理后台“卡台接入”填写自己的 HTTPS `/openapi/v1` 地址和密钥；自动执行先保持暂停。切换地址、环境或 App ID 不会沿用原卡台密钥。
3. 只读查询余额、卡产品和套餐；按自己的合同配置开卡费、注资上限、单笔与日预算、主备卡。所有金额使用对应币种的最小单位；不能把美元卡成本当成人民币供货价。
4. GPT 商品绑定上游实时目录中的实际套餐，不按倍率名称猜测映射。X 商品填写实际商品 ID、预期 Stripe 收款方、币种和最小单位报价，再配置赠送账号及账单资料。
5. 管理员邀请渠道，核实真实入账后用唯一凭证记账；渠道创建签名密钥。为 GPTibo 配置专用渠道，按联调指南设置采购上限。
6. 核对配置与真实通道后再开启商品及自动执行。保存连接配置本身不产生开卡、注资或付款。

外部响应丢失时查询原单，不自动重复补款或确认付款。原支付需要人工处理时保留订单、查询入口和证据；管理员必须核实原付款结果后才能进行恢复操作。

## 验证与参考

```sh
# 必须为独立本机测试库；每个集成测试使用独立 schema。
PLATFORM_TEST_DATABASE_URL=postgres://test@127.0.0.1:5432/platform_test go test -count=1 -timeout 180s ./...
go vet ./...
cd web && npm run build
```

跨服务契约测试会编译相邻 `services/storefront` 的测试客户端，用实际签名客户端访问实际供货 API。执行完整验证时需要完整仓库；外部资格和支付由测试夹具隔离，不连接生产卡台。

接口设计参考 [onlylm/daichong](https://github.com/onlylm/daichong) 的直充预检、稳定请求号及未知结果恢复思路；未复制该仓库源码、配置或数据，也未将其代码重新授权为 MIT。我们的协议实现与测试在本仓库维护。参考不是兼容性承诺，真实通道仍需自行核对。
