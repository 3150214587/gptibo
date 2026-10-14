# GPTibo

**可自托管的数字商品商城与供货管理系统。**

包含商城前台、管理后台、代理工作台，以及独立的 Go 供货平台。支持商品与订单管理、支付适配、渠道采购、异步履约和售后处理，适合二次开发和自建部署。

[快速启动](#快速启动) · [部署与接入](docs/FULLSTACK.md) · [渠道 API](services/platform/API.md) · [安全说明](SECURITY.md) · [MIT 许可证](LICENSE)

## 功能与组成

| 模块 | 主要功能 | 技术栈 |
| --- | --- | --- |
| 商城前台 | 商品展示、下单、订单查询、售后、多语言 | React 19、TypeScript、Vite |
| 管理与代理工作台 | 商品定价、客户、订单、退款、结算、代理商店 | React 19、TypeScript |
| 商城后端 | 支付适配、身份验证、采购、邮件与异步任务 | Go、Gin、PostgreSQL |
| 供货平台 | 渠道钱包、签名 API、卡台适配、GPT 直充、X 赠送 | Go、PostgreSQL、React |
| 可选商城后端 | Cloudflare Workers + D1 实现 | TypeScript、Wrangler |

Go 是整套部署的推荐后端。Cloudflare 实现是商城后端的替代方案，与 Go 商城后端不共享数据库。

### 接入状态

- **X 商城采购已对接供货平台**：预检账号与报价、采购限价、冻结与结算、原单查询和异常恢复。
- **供货平台支持 GPT 渠道直充**：通过渠道工作台或签名 API 使用。
- **商城 GPT 采购仍需适配**：商城现有 GPT 代收协议与供货平台的余额采购 API 不兼容。
- 支付和其他采购适配器保留协议实现，需按自己的服务核对或改造，不承诺兼容任意供应商。

## 快速启动

需要 Git、Python 3 和 Docker Compose v2 或以上。从仓库根目录执行：

```sh
git clone https://github.com/CoolkHz/gptibo.git
cd gptibo
python3 scripts/init-fullstack.py
```

初始化会生成独立随机密钥和关闭交易的本地配置，已有文件不会被覆盖。在当前终端将 `SUPPLY_ADMIN_EMAIL` 设置为自己的邮箱，然后启动：

```sh
export SUPPLY_ADMIN_EMAIL='admin@example.invalid' # 替换为自己的邮箱
docker compose -f compose.fullstack.yaml build
docker compose -f compose.fullstack.yaml up -d --wait storefront-db supply-db
docker compose -f compose.fullstack.yaml run --rm storefront-maintenance migrate
docker compose -f compose.fullstack.yaml run --rm storefront-maintenance grant-runtime
docker compose -f compose.fullstack.yaml run --rm supply-maintenance migrate
docker compose -f compose.fullstack.yaml run --rm supply-maintenance grant-runtime
docker compose -f compose.fullstack.yaml run --rm supply-maintenance bootstrap
docker compose -f compose.fullstack.yaml up -d web storefront-api storefront-worker supply-api supply-worker
```

| 入口 | 地址 |
| --- | --- |
| 商城 | `http://127.0.0.1:8080` |
| 商城管理后台 | `http://127.0.0.1:8080/admin` |
| 商城代理工作台 | `http://127.0.0.1:8080/partner` |
| 供货管理与渠道工作台 | `http://127.0.0.1:8092` |

供货平台首位管理员密码保存在 `.secrets/fullstack/supply-admin`。商城后台采用独立的管理员身份、TOTP 与邮件验证，需要另行配置，详见[商城后端说明](services/storefront/README.md)。

启动后默认不会进行真实支付、采购或通知。按[完整部署与接入指南](docs/FULLSTACK.md)配置自己的域名、HTTPS、支付渠道、卡台、商品、执行账号和渠道密钥，再启用相关功能。

## 本地开发

需要 Node.js 24、npm、Python 3；后端开发需要 Go 1.26+ 和 PostgreSQL 18。

```sh
npm ci
# 配合已启动的 Go 商城 API（本地 8093 端口）
npx vite --config services/storefront/vite.config.ts
```

商城前端地址为 `http://127.0.0.1:5175`。Go 后端的启动、数据库角色及配置见[商城后端说明](services/storefront/README.md)；供货工作台的开发方式见[供货平台说明](services/platform/README.md)。

使用 Cloudflare 本地后端时：

```sh
cp .dev.vars.example .dev.vars
npm exec wrangler -- d1 migrations apply gptibo-local --local
npm run preview:worker
# 另一个终端，可选启动 Vite 热更新（5173 → Worker 8787）
npm run dev
```

`.dev.vars` 只保存在本机。远程部署前需创建自己的 D1 等资源，替换 `wrangler.jsonc` 中的占位配置。不要让两个商城后端同时处理同一套业务数据。

## 目录

```text
src/                   商城、管理员与代理界面
services/storefront/   Go 商城 API、Worker、迁移与测试
services/platform/     Go 供货 API、Worker、渠道工作台与测试
worker/                Cloudflare 商城后端
migrations/            Cloudflare D1 迁移
emails/                邮件模板
scripts/               配置初始化、构建与源码检查
docs/FULLSTACK.md      整套部署、采购接入与异常处理
compose.fullstack.yaml 整套本地部署
```

## 配置与安全

- 密钥、商户身份、卡台地址、账号凭据、生产数据和内部部署资料不随源码提供；匿名接入地址使用保留域名 `example.invalid`。
- 保留交易协议、数据库结构、业务流程和演示商品/金额。部署者需自行设置商品映射、售价、采购限价和支付参数。
- 适用于**新数据库**。匿名化后的迁移不兼容原生产数据库，不要导入旧生产快照。
- 前端变量不可存放服务器秘密。`.env`、`.dev.vars`、`.secrets/` 和运行数据必须保持私有。
- 代码测试使用隔离夹具，不代表真实卡台、支付或权益到账已验收。容器启动与外部交易需在自己的部署环境验证。

漏洞报告与发布检查见 [SECURITY.md](SECURITY.md)。

## 验证与贡献

```sh
npm test
npm run lint
npm run build
npm run security:scan
python3 -m unittest discover -s scripts -p 'test_*.py'
```

两套 Go 服务分别执行 `go vet ./...` 和 `go test ./...`；完整数据库测试需设置 `STOREFRONT_TEST_DATABASE_URL` / `PLATFORM_TEST_DATABASE_URL`，连接本机、库名以 `_test` 结尾的专用数据库。未配置时相关测试会跳过。供货前端在 `services/platform/web` 执行 `npm ci && npm run build`。

欢迎提交 Issue 和 Pull Request。请附上复现步骤、改动说明和相关验证结果；支付、账务和订单恢复改动应包含回归测试。不要提交真实用户资料、凭据、账单链接或生产日志。

## 许可证与致谢

项目代码采用 [MIT License](LICENSE)。第三方资源保留各自授权，商标和美术作品不因项目开源而转授；参见[品牌资源说明](public/brands/README.md)与[样式授权](src/styles/vendor/README.md)。

供货接口设计参考 [onlylm/daichong](https://github.com/onlylm/daichong) 的预检、稳定请求号和原单恢复思路，未复制其源码、配置或数据。具体接口以本项目的 [API 文档](services/platform/API.md)为准。
