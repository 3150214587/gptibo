import {
  useEffect,
  useRef,
  useState,
  useSyncExternalStore,
  type FormEvent,
  type ReactNode,
} from "react";
import { Button } from "@cloudflare/kumo/components/button";
import { Input } from "@cloudflare/kumo/components/input";
import { Select } from "@cloudflare/kumo/components/select";
import { Dialog } from "@cloudflare/kumo/components/dialog";
import { Table } from "@cloudflare/kumo/components/table";
import { Tooltip } from "@cloudflare/kumo/components/tooltip";
import {
  ArrowClockwise,
  ArrowRight,
  ChartBar,
  Users,
  ListChecks,
  Receipt,
  SlidersHorizontal,
  CreditCard,
  Key,
  Plugs,
  BookOpen,
  SignOut,
  Envelope,
  Plus,
  X,
  SidebarSimple,
} from "@phosphor-icons/react";
import {
  pendingSnapshot,
  pendingSubscribe,
  savePending,
  parsePending,
  type PendingOrder,
} from "./pending-order";

type Row = Record<string, unknown>;
type Session = {
  authenticated: boolean;
  role?: "admin" | "channel";
  id?: string;
  name?: string;
  email?: string;
};
type Field = {
  name: string;
  label: string;
  value?: string | number;
  type?: string;
  optional?: boolean;
  options?: [string, string][];
  help?: string;
  readonly?: boolean;
};
type Modal = {
  title: string;
  description?: string;
  fields?: Field[];
  content?: ReactNode;
  submit?: string;
  action?: (data: Row) => Promise<void | Modal>;
};
const sections = {
  overview: ["工作概览", ChartBar],
  channels: ["渠道管理", Users],
  orders: ["充值订单", ListChecks],
  products: ["商品与供货价", SlidersHorizontal],
  ledger: ["资金流水", Receipt],
  cards: ["卡台与卡规则", CreditCard],
  cardOperations: ["卡台操作", Receipt],
  accounts: ["X 账号与出口", Plugs],
  keys: ["接口密钥", Key],
  webhook: ["订单回调", Plugs],
  notifications: ["通知投递", Envelope],
  audit: ["操作记录", Receipt],
  docs: ["接入文档", BookOpen],
} as const;
type Section = keyof typeof sections;
const sectionDescriptions: Record<Section, string> = {
  overview: "查看供货金额与订单处理情况。",
  channels: "管理渠道账户、邀请状态、人民币余额与专属供货价。",
  orders: "查询充值进度、上游凭证与原单处理记录。",
  products: "管理商品套餐、供货价格与开放状态。",
  ledger: "查询余额入账、订单冻结、扣款与退回记录。",
  cards: "配置 Card provider 连接，并为每项业务指定卡头和用卡规则。",
  cardOperations: "核对开卡、注资与待确认操作的原始记录。",
  accounts: "管理 X 充值账号、网络出口与执行额度。",
  keys: "管理服务端调用供货 API 使用的接口密钥。",
  webhook: "配置订单状态回调地址与签名密钥。",
  notifications: "查询开户邀请与订单回调的投递结果。",
  audit: "查询管理操作、执行记录与变更凭证。",
  docs: "查看 API 鉴权、下单、订单查询与回调说明。",
};
const labels: Record<string, string> = {
  queued: "排队中",
  on_sale: "已开放",
  off_sale: "未开放",
  disabled: "已停用",
  running: "处理中",
  unknown: "待核对",
  succeeded: "已完成",
  failed: "失败已退回",
  active: "正常",
  invited: "待激活",
  banned: "已封禁",
  pending: "待投递",
  sent: "已提交",
  dead: "投递失败",
  credit: "余额入账",
  reserve: "冻结",
  consume: "扣款",
  release: "退回",
  invitation: "开户邀请",
  webhook: "订单回调",
  preflight: "账号预检",
  card: "准备卡片",
  funding: "开卡／注资",
  submitting: "提交充值",
  submitted: "核对充值",
  paid: "付款完成",
  resource_busy: "等待可用资源",
  execution_paused: "执行已暂停",
  channel_disabled: "渠道已禁用",
  invitation_replaced: "旧邀请已被替换",
  invitation_expired: "邀请已失效",
  email_not_configured: "未配置 Resend 邮件密钥或发件人，请联系管理员",
  mail_unauthorized: "Resend 密钥无效或已撤销",
  mail_sender_rejected: "Resend 拒绝发送，请检查密钥权限和发件域名",
  mail_rate_limited: "Resend 发送频率超限，稍后自动重试",
  mail_invalid_request: "Resend 拒绝邮件请求，请检查邮件配置",
  mail_service_unavailable: "Resend 暂不可用，稍后自动重试",
  mail_unconfirmed: "Resend 接收结果未确认，按原请求重试",
  delivery_failed: "投递失败，等待重试或管理员核查",
  card_failed: "明确未扣款",
  account_changed: "充值账号与确认账号不一致",
  card_funding_unconfirmed: "原卡操作待核对",
  card_product_mismatch: "卡 ID 或卡头不匹配",
  card_pair_declined: "两张卡均无法继续付款，暂停核查",
  card_pair_unavailable: "主备卡暂不可用",
  card_pair_unconfigured: "主备卡尚未配置完成",
  card_switching_backup: "正在切换备用卡",
  card_spend_limit_unconfirmed: "卡片消费限额未确认",
  card_open_fee_changed: "开卡费超过配置上限",
  card_recharge_fee_unconfirmed: "注资费率需核实",
  card_daily_limit: "已达每日注资预算",
  payment_pending: "原账单付款结果待核对",
  payment_action_required: "已挂起，等待手动支付验证",
  payment_manual_action: "已挂起，等待手动完成原账单",
  payment_manual_review: "已挂起，等待人工核对付款结果",
  captcha_unsupported: "这类验证不支持 hCaptcha 打码，需手动处理",
  captcha_payment_mismatch: "原账单信息不一致，未提交打码",
  captcha_verification_url_invalid: "原验证接口不匹配，未提交打码",
  captcha_parameters_missing: "验证码参数不完整",
  captcha_session_unavailable: "原支付会话不可用，需手动核对",
  captcha_solver_failed: "打码服务未返回成功结果，已停止自动尝试",
  captcha_solver_invalid_response: "打码结果不完整，已停止自动尝试",
  captcha_solver_unconfirmed: "打码请求结果未确认，不自动重试",
  captcha_challenge_changed: "原验证或支付状态已变化，未提交旧验证码",
  captcha_verification_unconfirmed: "验证提交结果未确认，正在核对原支付",
  captcha_not_accepted: "支付方未接受验证结果，已挂起等待手动处理",
  captcha_execution_paused: "自动支付已暂停，未提交验证码",
  captcha_interrupted: "打码处理曾中断，需手动核对",
  checkout_inactive: "付款账单已失效，可核对未付款后重新生成账单",
  manual_invoice_ready: "人工账单已生成，等待你打开账单付款",
  manual_invoice_state_invalid: "人工账单状态需核对，已停止自动付款",
  upstream_charge_requires_review: "失败单扣费情况待核对",
  x_price_changed: "X 上游报价已变化",
  GPT_SESSION_INVALID: "充值凭据无效",
  MAILBOX_LOGIN_FAILED: "账号邮箱登录失败",
  GPT_PLAN_ALREADY_ACTIVE: "账号已有有效套餐",
  GPT_CREDIT_REQUIRES_SUBSCRIPTION: "需先有有效订阅",
};
const text = (v: unknown) => (v == null || v === "" ? "—" : String(v));
const money = (v: unknown) =>
  v == null
    ? "—"
    : (Number(v) / 100).toLocaleString("zh-CN", {
        style: "currency",
        currency: "CNY",
      });
const date = (v: unknown) =>
  v
    ? new Date(String(v)).toLocaleString("zh-CN", {
        hour12: false,
        timeZone: "Asia/Shanghai",
      })
    : "—";
async function api<T>(path: string, body?: Row, idem?: string): Promise<T> {
  const response = await fetch(path, {
    method: body ? "POST" : "GET",
    credentials: "same-origin",
    headers: {
      ...(body ? { "Content-Type": "application/json" } : {}),
      ...(idem ? { "Idempotency-Key": idem } : {}),
    },
    body: body ? JSON.stringify(body) : undefined,
    signal: AbortSignal.timeout(90000),
  });
  const value = await response.json();
  if (response.status === 401 && path !== "/api/login")
    window.dispatchEvent(new Event("platform-session-expired"));
  if (!response.ok)
    throw new APIError(
      value.error?.message || "请求未确认，请查询原记录",
      response.status,
      value.error?.code || "internal",
    );
  return value.data as T;
}
class APIError extends Error {
  constructor(
    message: string,
    readonly status: number,
    readonly code: string,
  ) {
    super(message);
  }
}
function OrderDetails({
  initial,
  admin,
  reconcile,
}: {
  initial: Row;
  admin: boolean;
  reconcile: (order: Row) => void;
}) {
  const [order, setOrder] = useState(initial),
    [error, setError] = useState(""),
    [paymentURL, setPaymentURL] = useState(""),
    [confirmedUnpaid, setConfirmedUnpaid] = useState(false),
    [handoffBusy, setHandoffBusy] = useState(false);
  const canHandoff =
    admin &&
    order.business === "x" &&
    order.stage === "submitted" &&
    ["running", "unknown"].includes(String(order.status)) &&
    (order.manual_payment === true ||
      [
        "payment_pending",
        "payment_action_required",
        "payment_manual_action",
        "payment_manual_review",
      ].includes(String(order.failure_code)));
  const handoff = async (regenerate = false) => {
    setHandoffBusy(true);
    setError("");
    setPaymentURL("");
    try {
      const result = await api<{ url: string }>(
        `/api/admin/orders/${encodeURIComponent(String(order.id))}/${regenerate ? "payment-regenerate" : "payment-handoff"}`,
        regenerate ? { confirmed_unpaid: confirmedUnpaid } : {},
      );
      const url = new URL(result.url);
      if (
        url.protocol !== "https:" ||
        url.host !== "checkout.stripe.com" ||
        url.username ||
        url.password ||
        !/^\/(c\/)?pay\/cs_live_[A-Za-z0-9]+$/.test(url.pathname)
      )
        throw new Error("付款链接校验失败");
      setPaymentURL(url.href);
      setOrder(await api<Row>(`/api/orders/${encodeURIComponent(String(order.id))}`));
    } catch (e) {
      setError(e instanceof Error ? e.message : "人工付款入口获取失败");
    } finally {
      setHandoffBusy(false);
    }
  };
  useEffect(() => {
    let active = true;
    const update = () => {
      api<Row>(`/api/orders/${encodeURIComponent(String(initial.id))}`)
        .then((value) => {
          if (active) {
            setOrder(value);
          }
        })
        .catch((e) => {
          if (active) setError(e.message);
        });
    };
    update();
    const timer = setInterval(update, 5000);
    return () => {
      active = false;
      clearInterval(timer);
    };
  }, [initial.id]);
  return (
    <>
      <dl className="detail">
        {[
          ["平台订单号", order.id],
          ["渠道订单号", order.merchant_order_no],
          ["商品", order.product_code],
          ["充值账号", order.account_label || order.recipient],
          ["供货价", money(order.supply_minor)],
          [
            "状态",
            order.manual_payment === true &&
            ["running", "unknown"].includes(String(order.status)) ? (
              ["pending", "checking", "solving"].includes(String(order.captcha_status)) ? "正在自动验证 · 其他订单继续处理" : "已挂起 · 等待人工处理"
            ) : (
              <Status key="status" value={order.status} />
            ),
          ],
          ["执行阶段", labels[String(order.stage)] || text(order.stage)],
          [
            "处理提示",
            labels[String(order.failure_code)] || text(order.failure_code),
          ],
          ["上游凭证", order.receipt],
          ["创建时间", date(order.created_at)],
          ["更新时间", date(order.updated_at)],
        ].map(([key, value]) => (
          <div key={String(key)}>
            <dt>{String(key)}</dt>
            <dd>{value as ReactNode}</dd>
          </div>
        ))}
      </dl>
      {error && (
        <p className="error" role="alert">
          {error}
        </p>
      )}
      {canHandoff && (
        <section className="manual-payment">
          {Boolean(order.captcha_status) && !order.manual_invoice_status && (
            <p role="status">
              自动打码：{({ pending: "等待处理", checking: "正在核对验证类型", solving: "正在打码", submitted: "验证结果已提交，等待原支付结果", failed: "失败，已挂起等待手动处理", unconfirmed: "结果未确认，已停止自动尝试" } as Record<string, string>)[String(order.captcha_status)] || "等待核对"}
              {Boolean(order.captcha_failure) && `。${labels[String(order.captcha_failure)] || "需人工核对"}`}
              {order.challenge_type === "intent_confirmation_challenge" && "。验证类型：支付确认人机挑战"}
            </p>
          )}
          <h3>手动处理支付</h3>
          <p>
            此单独立处理，不占用付款卡、不预留卡片金额，其他订单继续执行。重新拉起支付会打开当前账单；账单失效时，可核对未付款后重新生成。新账单由你人工付款，成功后自动完成本订单。
          </p>
          {Boolean(order.manual_invoice_status) && (
            <p role="status">{({ creating: "新账单正在生成或等待核对，请勿重复操作。", created: "新账单已创建，点击重新生成账单可继续核对并获取同一链接。", ready: "人工账单已生成，付款链接可随时重新打开。", unknown: "创建结果未确认，已停止重复生成，请核对创建记录。" } as Record<string, string>)[String(order.manual_invoice_status)]}</p>
          )}
          {paymentURL ? (
            <a
              className="text-link"
              href={paymentURL}
              target="_blank"
              rel="noopener noreferrer"
              referrerPolicy="no-referrer"
            >
              打开当前账单，手动支付 ↗
            </a>
          ) : (
            <Button
              type="button"
              disabled={handoffBusy}
              onClick={() => void handoff()}
            >
              {handoffBusy ? "正在核对原账单…" : "重新拉起支付"}
            </Button>
          )}
          <label className="manual-confirmation">
            <input type="checkbox" checked={confirmedUnpaid} onChange={(event) => setConfirmedUnpaid(event.target.checked)} disabled={handoffBusy} />
            我已核对旧账单未付款，需要重新生成账单
          </label>
          <Button type="button" disabled={handoffBusy || !confirmedUnpaid} onClick={() => void handoff(true)}>
            {handoffBusy ? "正在处理账单…" : "重新生成账单"}
          </Button>
          <p>
            重复点击会复用已生成的有效账单；旧付款记录会保留。本页每 5 秒刷新，付款链接仅供管理员本人使用。
          </p>
        </section>
      )}
      {admin &&
        ["queued", "running", "unknown"].includes(String(order.status)) && (
          <Button type="button" onClick={() => reconcile(order)}>
            处理原单
          </Button>
        )}
    </>
  );
}
function Status({ value }: { value: unknown }) {
  const key = String(value);
  return <span className={`status ${key}`}>{labels[key] || text(value)}</span>;
}
function Grid({
  columns,
  rows,
}: {
  columns: [string, (row: Row) => ReactNode][];
  rows: Row[];
}) {
  return (
    <div className="table-scroll">
      <Table className="platform-table">
        <Table.Header>
          <Table.Row>
            {columns.map(([name]) => (
              <Table.Head key={name}>{name}</Table.Head>
            ))}
          </Table.Row>
        </Table.Header>
        <Table.Body>
          {rows.length ? (
            rows.map((row, index) => (
              <Table.Row key={String(row.id || row.code || index)}>
                {columns.map(([name, render]) => (
                  <Table.Cell key={name}>{render(row)}</Table.Cell>
                ))}
              </Table.Row>
            ))
          ) : (
            <Table.Row>
              <Table.Cell colSpan={columns.length}>
                <div className="empty">暂无记录</div>
              </Table.Cell>
            </Table.Row>
          )}
        </Table.Body>
      </Table>
    </div>
  );
}
function moneyMinor(value: unknown) {
  const raw = String(value);
  if (!/^\d+(\.\d{1,2})?$/.test(raw)) throw new Error("金额最多支持两位小数");
  const [whole, fraction = ""] = raw.split(".");
  const amount = Number(whole) * 100 + Number(fraction.padEnd(2, "0"));
  if (!Number.isSafeInteger(amount)) throw new Error("金额过大");
  return amount;
}
function Fields({ fields }: { fields: Field[] }) {
  return (
    <>
      {fields.map((field) => (
        <label key={field.name} className="field">
          <span>
            {field.label}
            {field.optional && <small>选填</small>}
          </span>
          {field.options ? (
            <Select
              name={field.name}
              aria-label={field.label}
              defaultValue={String(field.value ?? field.options[0]?.[0] ?? "")}
              required={!field.optional}
              items={field.options.map(([value, label]) => ({ value, label }))}
            />
          ) : field.type === "textarea" ? (
            <textarea
              name={field.name}
              defaultValue={field.value ?? ""}
              required={!field.optional}
              rows={4}
              autoComplete="off"
            />
          ) : (
            <Input
              name={field.name}
              aria-label={field.label}
              type={field.type || "text"}
              defaultValue={field.value ?? ""}
              required={!field.optional}
              autoComplete={field.type === "password" ? "new-password" : "off"}
              readOnly={field.readonly}
            />
          )}
          {field.help && <small>{field.help}</small>}
        </label>
      ))}
    </>
  );
}
export function App() {
  const [sidebarCollapsed, setSidebarCollapsed] = useState(() => {
    try {
      return localStorage.getItem("gptibo-sidebar-collapsed") === "true";
    } catch {
      return false;
    }
  });
  const toggleSidebar = () => {
    const collapsed = !sidebarCollapsed;
    setSidebarCollapsed(collapsed);
    try {
      localStorage.setItem("gptibo-sidebar-collapsed", String(collapsed));
    } catch {
      // The navigation remains usable when browser storage is unavailable.
    }
  };
  const [session, setSession] = useState<Session | null>(null),
    [section, setSection] = useState<Section>("overview"),
    [data, setData] = useState<Row>({}),
    [error, setError] = useState(""),
    [revision, setRevision] = useState(0),
    [modal, setModal] = useState<Modal | null>(null),
    [busy, setBusy] = useState(false),
    [modalError, setModalError] = useState(""),
    [notice, setNotice] = useState(""),
    [offset, setOffset] = useState(0),
    [filter, setFilter] = useState(""),
    [search, setSearch] = useState("");
  const generation = useRef(0),
    inviteToken = useRef(
      location.pathname === "/join" ? location.hash.slice(1) : "",
    );
  const pendingRaw = useSyncExternalStore(
    pendingSubscribe,
    () => pendingSnapshot(session?.id || ""),
    () => "",
  );
  const pending = parsePending(pendingRaw);
  useEffect(() => {
    api<Session>("/api/session")
      .then(setSession)
      .catch((e) => setError(e.message));
    if (inviteToken.current) history.replaceState(null, "", "/join");
  }, []);
  useEffect(() => {
    const expired = () => {
      generation.current++;
      setSession({ authenticated: false });
      setModal(null);
      setData({});
      setNotice("登录已失效，请重新登录；未确认订单仍保留原单标识");
    };
    window.addEventListener("platform-session-expired", expired);
    return () =>
      window.removeEventListener("platform-session-expired", expired);
  }, []);
  const admin = session?.role === "admin";
  const requestKey = JSON.stringify([
    section,
    revision,
    session?.id,
    offset,
    filter,
    search,
  ]);
  const viewKey = JSON.stringify([section, session?.id, session?.role]);
  const loading = Boolean(session?.authenticated) && data.viewKey !== viewKey;
  const refreshing = !loading && data.requestKey !== requestKey;
  const refresh = () => setRevision((r) => r + 1);
  const open = (m: Modal) => {
    setModalError("");
    setNotice("");
    setModal(m);
  };
  useEffect(() => {
    if (!session?.authenticated) return;
    const current = ++generation.current;
    const load = async (): Promise<Row> => {
      if (section === "docs") return {};
      if (section === "overview") {
        const [summary, rows, health] = await Promise.all([
          api<Row>("/api/overview"),
          api<Row[]>("/api/orders?limit=8"),
          api<Row>("/healthz"),
        ]);
        return { summary, rows, health };
      }
      if (section === "cards") {
        const [provider, rows] = await Promise.all([
          api<Row>("/api/admin/provider"),
          api<Row[]>("/api/admin/card-rules"),
        ]);
        return { provider, rows };
      }
      if (section === "products" && admin) {
        const [rows, catalog] = await Promise.all([
          api<Row[]>("/api/products"),
          api<Row[]>("/api/admin/gpt-plans")
            .then((plans) => ({ plans, planError: "" }))
            .catch((e: Error) => ({ plans: [], planError: e.message })),
        ]);
        return { rows, ...catalog };
      }
      if (section === "orders")
        return {
          rows: await api<Row[]>(
            `/api/orders?offset=${offset}&status=${encodeURIComponent(filter)}&search=${encodeURIComponent(search)}`,
          ),
        };
      const paths: Partial<Record<Section, string>> = {
        channels: "/api/admin/channels",
        products: "/api/products",
        ledger: "/api/ledger",
        accounts: "/api/admin/x-accounts",
        keys: "/api/keys",
        webhook: "/api/webhook",
        notifications: "/api/notifications",
        audit: "/api/admin/audit",
        cardOperations: "/api/admin/card-operations",
      };
      const paged = [
        "channels",
        "ledger",
        "notifications",
        "audit",
        "cardOperations",
      ].includes(section);
      return {
        rows: await api<Row[]>(
          paths[section]! +
            (paged
              ? `?limit=50&offset=${offset}&search=${encodeURIComponent(search)}`
              : ""),
        ),
      };
    };
    load()
      .then((value) => {
        if (current === generation.current) {
          setData({ ...value, requestKey, viewKey });
          setError("");
        }
      })
      .catch((e) => {
        if (current === generation.current) {
          setData({ requestKey, viewKey });
          setError(e.message);
        }
      });
  }, [
    section,
    revision,
    session,
    admin,
    offset,
    filter,
    search,
    requestKey,
    viewKey,
  ]);
  useEffect(() => {
    if (
      !session?.authenticated ||
      !["orders", "overview", "notifications"].includes(section)
    )
      return;
    const timer = setInterval(refresh, 20000);
    return () => clearInterval(timer);
  }, [section, session]);
  const mutate = async (path: string, body: Row) => {
    await api(path, body);
    refresh();
    setNotice("已保存");
  };
  const confirm = (
    title: string,
    description: string,
    action: () => Promise<void>,
  ) =>
    open({
      title,
      description,
      submit: "确认",
      action: async () => {
        await action();
      },
    });
  const rows = (data.rows as Row[]) || [];
  const upstreamPlans = (data.plans as Row[]) || [];
  const productForm = (product: Row) => {
    const current = String(product.upstream_plan || "");
    const quote = (product.config as Row) || {};
    const canonical = (value: unknown) =>
      String(value || "")
        .toLowerCase()
        .replace(/[\s_-]|chatgpt|gpt/g, "")
        .replace(/×/g, "x");
    const aliases: Record<string, string[]> = {
      "gpt-plus": ["plus"],
      "gpt-5x": ["5x", "pro5x"],
      "gpt-10x": ["10x", "pro10x"],
      "gpt-25x": ["25x", "pro25x"],
    };
    const matches = upstreamPlans.filter(
      (plan) =>
        plan.enabled &&
        plan.purchasable &&
        (aliases[String(product.code)] || []).some(
          (alias) =>
            canonical(plan.code) === alias || canonical(plan.name) === alias,
        ),
    );
    const options: [string, string][] = upstreamPlans.map((plan) => [
      String(plan.code),
      `${plan.name}${plan.enabled && plan.purchasable ? "" : "（暂停供应）"}`,
    ]);
    if (current && !options.some(([code]) => code === current)) {
      options.push([current, "原套餐（上游未提供）"]);
    }
    open({
      title: "商品配置",
      description:
        product.business === "gpt"
          ? "充值套餐从上游自动读取。核对套餐后设置人民币供货价。"
          : undefined,
      fields: [
        { name: "name", label: "展示名称", value: String(product.name) },
        {
          name: "price",
          label: "默认人民币供货价",
          value: Number(product.supply_minor) / 100,
        },
        ...(product.business === "x" ? [
          { name: "stripe_product", label: "X 商品标识", value: String(quote.stripe_product || ""), optional: true },
          { name: "stripe_merchant", label: "预期 Stripe 收款方", value: String(quote.stripe_merchant || ""), optional: true, help: "必须自行核实；用于防止付给错误收款方" },
          { name: "currency", label: "上游币种（小写）", value: String(quote.currency || ""), optional: true },
          { name: "amount_minor", label: "上游金额（最小货币单位整数）", value: Number(quote.amount_minor || 0), optional: true, help: "与人民币供货价分开；例如 100 表示该币种的 100 个最小单位" },
        ] : []),
        ...(product.business === "gpt" && !data.planError
          ? [
              {
                name: "upstream_plan",
                label: "充值套餐",
                optional: true,
                value:
                  current ||
                  (matches.length === 1 ? String(matches[0].code) : ""),
                options: [["", "请选择对应套餐"], ...options] as [
                  string,
                  string,
                ][],
                help: "开放商品前需选择对应套餐；暂停供应的套餐不能开售。",
              },
            ]
          : []),
        {
          name: "enabled",
          label: "售卖状态",
          value: product.enabled ? "true" : "false",
          options: [
            ["false", "关闭"],
            ["true", "开放"],
          ],
        },
      ],
      content:
        product.business === "gpt" && data.planError ? (
          <p className="hint">
            {String(data.planError)}
            。当前套餐绑定保留，可调整供货价或关闭商品；读取成功后再开放。
          </p>
        ) : undefined,
      action: async (b) => {
        await mutate(`/api/admin/products/${product.code}`, {
          name: b.name,
          supply_minor: moneyMinor(b.price),
          ...(product.business === "gpt" && !data.planError
            ? { upstream_plan: b.upstream_plan }
            : {}),
          ...(product.business === "x" ? { config: { months: product.code === "x-3m" ? 3 : 6, stripe_product: b.stripe_product, stripe_merchant: b.stripe_merchant, currency: b.currency, amount_minor: Number(b.amount_minor) } } : {}),
          enabled: b.enabled === "true",
        });
      },
    });
  };
  const orderColumns: [string, (r: Row) => ReactNode][] = [
    [
      "订单",
      (r) => (
        <button
          className="text-link"
          onClick={() =>
            open({
              title: "订单详情",
              content: (
                <OrderDetails
                  initial={r}
                  admin={admin}
                  reconcile={() =>
                    confirm(
                      "处理原订单",
                      "未派发订单按原任务执行；已提交充值的订单仅查询原请求。不会创建新的充值订单。",
                      () => mutate(`/api/admin/orders/${r.id}/reconcile`, {}),
                    )
                  }
                />
              ),
            })
          }
        >
          {text(r.merchant_order_no)}
          <small>{text(r.id).slice(0, 16)}…</small>
        </button>
      ),
    ],
    [
      "商品",
      (r) => (
        <span className="stack">
          {text(r.product_code)}
          <small>
            {r.business === "gpt" ? "GPT 自动充值" : "X Premium 赠送"}
          </small>
        </span>
      ),
    ],
    [
      "供货价",
      (r) => (Number(r.supply_minor) > 0 ? money(r.supply_minor) : "待定价"),
    ],
    [
      "状态",
      (r) => (
        <span className="stack">
          <Status value={r.status} />
          {r.manual_payment === true &&
          ["running", "unknown"].includes(String(r.status)) ? (
            <small>已挂起 · 等待人工处理</small>
          ) : String(r.failure_code).startsWith("payment_") ? (
            <small>
              {labels[String(r.failure_code)] || text(r.failure_code)}
            </small>
          ) : null}
        </span>
      ),
    ],
    ["时间", (r) => date(r.created_at)],
  ];
  function invite() {
    open({
      title: "邀请渠道",
      description: "邀请邮件有效期为 48 小时，渠道激活后即可登录工作台。",
      fields: [
        { name: "name", label: "渠道名称" },
        { name: "email", label: "邮箱", type: "email" },
      ],
      submit: "发送邀请",
      action: async (b) => {
        await mutate("/api/admin/channels", b);
        setNotice("渠道已创建，邀请邮件已加入发送队列");
      },
    });
  }
  async function recoverOrder(meta: PendingOrder) {
    try {
      const original = await api<Row>(
        `/api/orders/by-merchant/${encodeURIComponent(meta.merchant_order_no)}`,
      );
      savePending(session!.id!, null);
      setNotice(
        `已找到原订单：${original.merchant_order_no} · ${labels[String(original.status)] || original.status}`,
      );
      setSection("orders");
      setFilter("");
      setOffset(0);
      setSearch(meta.merchant_order_no);
      refresh();
      return true;
    } catch (e) {
      if (e instanceof APIError && e.status === 404) return false;
      throw e;
    }
  }
  function orderForm(resume?: PendingOrder) {
    open({
      title: "新建充值订单",
      fields: [
        {
          name: "product_code",
          label: "商品",
          value: resume?.product_code,
          options: rows
            .filter((r) =>
              resume ? r.code === resume.product_code : r.available,
            )
            .map((r) => [
              String(r.code),
              `${r.name} · ${money(r.supply_minor)}`,
            ]),
        },
        {
          name: "merchant_order_no",
          label: "渠道订单号",
          value: resume?.merchant_order_no || `web-${crypto.randomUUID()}`,
        },
      ],
      submit: "填写充值资料",
      action: async (selected) => {
        if (resume && selected.merchant_order_no !== resume.merchant_order_no)
          throw new Error("恢复请求必须使用原渠道订单号");
        const product = rows.find((r) => r.code === selected.product_code)!,
          idem = resume?.idempotency_key || `web:${crypto.randomUUID()}`;
        const common: Field[] =
          product.business === "x"
            ? [
                {
                  name: "recipient",
                  label: "X 用户名",
                  help: "支持填写 @用户名",
                },
              ]
            : [
                {
                  name: "mode",
                  label: "资料类型",
                  options: [
                    ["session", "Session JSON"],
                    ["access_token", "Access Token"],
                    ["mailbox", "邮箱密码"],
                  ],
                },
                {
                  name: "credential",
                  label: "Session / Access Token",
                  type: "textarea",
                  optional: true,
                },
                {
                  name: "email",
                  label: "账号邮箱",
                  type: "email",
                  optional: true,
                },
                {
                  name: "password",
                  label: "账号密码",
                  type: "password",
                  optional: true,
                },
              ];
        return {
          title: String(product.name),
          description: `本单冻结 ${money(product.supply_minor)}，充值成功后扣款。请确认渠道订单号和接收账号。`,
          fields: common,
          submit: "预检账号",
          action: async (b) => {
            if (resume && (await recoverOrder(resume))) return;
            let credential: Row | undefined;
            if (product.business === "gpt") {
              const mode = String(b.mode);
              credential =
                mode === "mailbox"
                  ? { mode, email: b.email, password: b.password }
                  : mode === "session"
                    ? { mode, session: b.credential }
                    : { mode, accessToken: b.credential };
            }
            const request = {
              ...selected,
              recipient: b.recipient || "",
              ...(credential ? { credential } : {}),
              expected_supply_minor: Number(product.supply_minor),
            };
            const checked = await api<Row>("/api/preflight", request);
            if (checked.eligible === false) throw new Error("该账号暂不能充值");
            const submission: Row = {
              ...request,
              ...(product.business === "gpt"
                ? { confirmed_account: checked.email }
                : {}),
            };
            const meta: PendingOrder = {
              merchant_order_no: String(selected.merchant_order_no),
              product_code: String(selected.product_code),
              idempotency_key: idem,
              created_at: resume?.created_at || new Date().toISOString(),
            };
            let attempted = Boolean(resume);
            return {
              title: "确认充值账号",
              description:
                "请核对实际接收账号。确认后冻结人民币供货价并开始自动充值。",
              content: (
                <dl className="detail">
                  {[
                    ["商品", product.name],
                    ["接收账号", checked.email || checked.recipient],
                    ["当前套餐", checked.current_plan || "—"],
                    ["渠道订单号", selected.merchant_order_no],
                    ["本次冻结", money(product.supply_minor)],
                  ].map(([k, v]) => (
                    <div key={String(k)}>
                      <dt>{String(k)}</dt>
                      <dd>{text(v)}</dd>
                    </div>
                  ))}
                </dl>
              ),
              submit: "确认并冻结下单",
              action: async () => {
                if (!navigator.locks)
                  throw new Error(
                    "当前浏览器不支持安全下单，请使用新版浏览器或渠道 API",
                  );
                await navigator.locks.request(
                  `gptibo-order:${session!.id}`,
                  async () => {
                    const prior = parsePending(pendingSnapshot(session!.id!));
                    if (prior && prior.idempotency_key !== idem)
                      throw new Error("有另一笔受理结果待确认，请先查询原单");
                    const wasAttempted = attempted;
                    savePending(session!.id!, meta);
                    attempted = true;
                    try {
                      const created = await api<Row>(
                        "/api/orders",
                        submission,
                        idem,
                      );
                      savePending(session!.id!, null);
                      setNotice(
                        `订单已受理：${created.merchant_order_no} · ${labels[String(created.status)] || created.status}`,
                      );
                      setSection("orders");
                      setFilter("");
                      setOffset(0);
                      setSearch(meta.merchant_order_no);
                      refresh();
                    } catch (e) {
                      if (
                        e instanceof APIError &&
                        !wasAttempted &&
                        [
                          "invalid_input",
                          "invalid_reference",
                          "invalid_credential",
                          "invalid_account",
                          "product_disabled",
                          "price_changed",
                          "provider_missing",
                          "execution_disabled",
                          "execution_paused",
                          "plan_missing",
                          "card_rule_missing",
                          "card_rule_disabled",
                          "card_missing",
                          "payment_region_missing",
                          "billing_profile_missing",
                          "x_configuration_missing",
                          "worker_unavailable",
                          "insufficient_balance",
                        ].includes(e.code)
                      ) {
                        savePending(session!.id!, null);
                        attempted = false;
                        throw e;
                      }
                      throw new Error(
                        "受理结果待确认。请查询原单；重试会保留本次订单号、幂等键和充值资料。",
                      );
                    }
                  },
                );
              },
            };
          },
        };
      },
    });
  }
  function providerForm() {
    const provider = (data.provider as Row) || {},
      billing = (provider.billing as Row) || {};
    open({
      title: "卡台接入",
      description:
        "秘密仅保存到服务器。保存配置不会发起充值；启用后处理新订单。",
      fields: [
        { name: "api_base", label: "卡台 API 地址", value: String(provider.api_base || ""), help: "HTTPS 地址，以 /openapi/v1 结尾；域名须先加入服务器允许名单" },
        {
          name: "environment",
          label: "环境",
          value: String(provider.environment || "sandbox"),
          options: [
            ["sandbox", "沙盒"],
            ["production", "正式"],
          ],
        },
        {
          name: "api_key",
          label: "卡台 API Key",
          type: "password",
          optional: true,
          help: "同环境、同地址及同 App ID 留空保留密钥；切换连接需填写新密钥",
        },
        {
          name: "app_id",
          label: "App ID",
          optional: true,
          value: String(provider.app_id || ""),
        },
        {
          name: "writes_enabled",
          label: "自动执行",
          value: provider.writes_enabled ? "true" : "false",
          options: [
            ["false", "暂停"],
            ["true", "启用"],
          ],
        },
        {
          name: "payment_country",
          label: "GPT 支付国家",
          value: String(provider.payment_country || ""),
        },
        {
          name: "payment_currency",
          label: "GPT 支付币种",
          value: String(provider.payment_currency || ""),
        },
        { name: "stripe_key", label: "X Stripe 公开 Key", optional: true },
        {
          name: "x_bearer",
          label: "X Bearer",
          optional: true,
          type: "password",
        },
        ...(
          [
            "first_name",
            "last_name",
            "email",
            "country",
            "line1",
            "city",
            "state",
            "postal",
          ] as const
        ).map((name, index) => ({
          name,
          label: [
            "持卡人名",
            "持卡人姓",
            "账单邮箱",
            "账单国家",
            "街道地址",
            "城市",
            "州／地区",
            "邮编",
          ][index],
          value: String(billing[name] || ""),
          optional: true,
        })),
      ],
      action: async (b) => {
        const saved: Row = {
          ...Object.fromEntries(
            [
              "environment",
              "api_base",
              "api_key",
              "app_id",
              "payment_country",
              "payment_currency",
              "stripe_key",
              "x_bearer",
            ].map((key) => [key, b[key]]),
          ),
          writes_enabled: b.writes_enabled === "true",
          billing: Object.fromEntries(
            [
              "first_name",
              "last_name",
              "email",
              "country",
              "line1",
              "city",
              "state",
              "postal",
            ].map((key) => [key, b[key]]),
          ),
        };
        await mutate("/api/admin/provider", saved);
      },
    });
  }
  async function inventory(kind: "cards" | "balance") {
    try {
      if (kind === "balance") {
        const balance = await api<Row>("/api/admin/inventory?kind=balance");
        open({
          title: "卡台余额",
          content: (
            <dl className="detail">
              <div>
                <dt>币种</dt>
                <dd>{text(balance.currency)}</dd>
              </div>
              <div>
                <dt>可用余额</dt>
                <dd>{text(balance.spendable_balance)}</dd>
              </div>
              <div>
                <dt>总余额</dt>
                <dd>{text(balance.balance)}</dd>
              </div>
            </dl>
          ),
        });
      } else {
        const cards = await api<Row[]>("/api/admin/inventory?kind=cards");
        open({
          title: "卡台卡池",
          description: "按尾号识别卡片，在规则中选择主卡和备用卡。",
          content: (
            <Grid
              rows={cards}
              columns={[
                ["卡片尾号", (r) => text(r.last_four)],
                ["内部 ID", (r) => text(r.id)],
                ["卡头", (r) => text(r.product_code)],
                ["余额（USD）", (r) => text(r.available_amount)],
                ["状态", (r) => text(r.status)],
              ]}
            />
          ),
        });
      }
    } catch (e) {
      setError((e as Error).message);
    }
  }
  async function ruleForm(row?: Row) {
    let cardProducts: Row[] = [];
    let cardChoices: Row[] = [];
    const pool = (Array.isArray(row?.pool) ? row.pool : []) as Row[];
    try {
      [cardProducts, cardChoices] = await Promise.all([api<Row[]>("/api/admin/inventory?kind=products"),api<Row[]>("/api/admin/inventory?kind=cards")]);
    } catch {
      setNotice("卡台目录暂未读取，仍可填写卡产品代码。");
    }
    open({
      title: row ? "修改卡规则" : "添加卡规则",
      description:
        "金额单位为美元。新订单固定此规则；X 与 GPT 分别使用自己的卡。",
      fields: [
        {
          name: "business",
          label: "业务",
          value: String(row?.business || "gpt"),
          options: [
            ["gpt", "GPT 自动充值"],
            ["x", "X Premium"],
          ].filter(([value]) => !row || value === row.business) as [
            string,
            string,
          ][],
        },
        {
          name: "product_code",
          label: "指定商品",
          value: String(row?.product_code || ""),
          optional: true,
          help: "留空适用整个业务，例如 gpt-plus 或 x-3m",
          readonly: Boolean(row),
        },
        {
          name: "card_product",
          label: "卡产品／卡头代码",
          value: String(row?.card_product || ""),
          options: cardProducts.length
            ? [
                ...(row?.card_product &&
                !cardProducts.some((v) => v.product_code === row.card_product)
                  ? [
                      [
                        String(row.card_product),
                        `${row.card_product}（当前配置，卡台目录未列出）`,
                      ] as [string, string],
                    ]
                  : [["", "请选择卡头"] as [string, string]]),
                ...cardProducts.map(
                  (v) =>
                    [
                      String(v.product_code),
                      v.product_name
                        ? `${v.product_name} · ${v.product_code}`
                        : String(v.product_code),
                    ] as [string, string],
                ),
              ]
            : undefined,
        },
        {
          name: "card_id",
          label: "主卡／指定卡",
          value: String(pool.find(p=>p.slot===1)?.card_id ?? row?.card_id ?? 0),
          options: [["0", "按规则自动开卡"], ...cardChoices.map(c=>[String(c.id), `尾号 ${c.last_four} · ${c.product_code} · ${c.available_amount} · ${c.status}`] as [string,string])],
          help: "X 主备模式优先使用主卡；累计两次拒付才换新。",
        },
        {
          name: "backup_card_id",
          label: "X 备用卡",
          value: String(pool.find(p=>p.slot===2)?.card_id ?? row?.backup_card_id ?? 0),
          options: [["0", "不使用主备模式"], ...cardChoices.map(c=>[String(c.id), `尾号 ${c.last_four} · ${c.product_code} · ${c.available_amount} · ${c.status}`] as [string,string])],
          help: "主备卡分别累计拒付；同一订单每卡只尝试一次，两张都拒付则暂停核查。",
        },
        {
          name: "auto_open",
          label: "余额不足处理",
          value: row?.auto_open ? "true" : "false",
          options: [
            ["false", "停止，不自动开卡／注资"],
            ["true", "按额度自动开卡／注资"],
          ],
        },
        {
          name: "funding",
          label: "单次补余额（USD）",
          value: Number(row?.funding_minor || 2000) / 100,
        },
        {
          name: "refill_threshold",
          label: "低于此余额时补款（USD）",
          value: Number(row?.refill_threshold_minor || row?.funding_minor || 2000) / 100,
          help: "按卡台实时余额判断；等于此金额时不补款。",
        },
        {
          name: "open_amount",
          label: "新卡初始入金（USD）",
          value: Number(row?.open_amount_minor || row?.funding_minor || 2000) / 100,
          help: "须满足所选卡头的最低开卡金额，与补余额金额分别设置。",
        },
        {
          name: "replace_on_decline",
          label: "X 支付明确拒付时",
          value: row?.replace_on_decline ? "true" : "false",
          options: [["false", "停止并核对"], ["true", "启用拒付处理"]],
          help: "仅 X 可用。主备模式首次拒付切另一张，单卡累计两次销卡换新；超时或验证要求保留原单核对。",
        },
        {
          name: "max_replacements",
          label: "单卡模式最多换卡次数",
          type: "number",
          value: Number(row?.max_replacements ?? 2),
          help: "仅单卡模式生效。主备模式固定每单最多两张卡，每张卡累计两次拒付才销卡换新。",
        },
        {
          name: "use_provider_limits",
          label: "消费限额方式",
          value: row?.use_provider_limits ? "true" : "false",
          options: [
            ["true", "沿用卡台默认限额"],
            ["false", "设置单笔消费上限"],
          ],
          help: "沿用卡台默认时，开卡与用卡均不覆盖卡台的消费限额。",
        },
        {
          name: "max_order",
          label: "单笔消费上限（USD）",
          value: Number(row?.max_order_minor || 1000) / 100,
          help: "仅选择设置单笔消费上限时生效。",
        },
        {
          name: "open_fee",
          label: "开卡费上限（USD）",
          value: Number(row?.max_open_fee_minor || 0) / 100,
          help: "费用超过上限时停止；每日注资预算包含开卡费",
        },
        {
          name: "daily",
          label: "每日注资总额（USD）",
          value: Number(row?.daily_limit_minor || 2000) / 100,
        },
        {
          name: "enabled",
          label: "状态",
          value: row?.enabled === false ? "false" : "true",
          options: [
            ["true", "启用"],
            ["false", "停用"],
          ],
        },
      ],
      action: async (b) => {
        await mutate("/api/admin/card-rules", {
          business: b.business,
          product_code: b.product_code,
          card_product: b.card_product,
          card_id: Number(b.card_id),
          backup_card_id: Number(b.backup_card_id),
          auto_open: b.auto_open === "true",
          use_provider_limits: b.use_provider_limits === "true",
          funding_minor: moneyMinor(b.funding),
          refill_threshold_minor: moneyMinor(b.refill_threshold),
          open_amount_minor: moneyMinor(b.open_amount),
          replace_on_decline: b.replace_on_decline === "true",
          max_replacements: Number(b.max_replacements),
          max_order_minor: moneyMinor(b.max_order),
          max_open_fee_minor: moneyMinor(b.open_fee),
          daily_limit_minor: moneyMinor(b.daily),
          enabled: b.enabled === "true",
        });
      },
    });
  }
  async function submitModal(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!modal?.action || busy) return;
    const values = Object.fromEntries(new FormData(event.currentTarget));
    setBusy(true);
    setModalError("");
    try {
      const next = await modal.action(values);
      setModal(next || null);
    } catch (e) {
      setModalError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  async function login(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (busy) return;
    setBusy(true);
    setError("");
    try {
      const values = Object.fromEntries(new FormData(event.currentTarget));
      if (inviteToken.current) {
        await api("/api/invitations/accept", {
          token: inviteToken.current,
          password: values.password,
        });
        inviteToken.current = "";
        history.replaceState(null, "", "/");
        setNotice("账户已激活，请登录");
      } else {
        await api("/api/login", values);
        generation.current++;
        setSection("overview");
        setOffset(0);
        setFilter("");
        setSearch("");
        setData({});
        setModal(null);
        setNotice("");
        setSession(await api<Session>("/api/session"));
      }
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  if (!session?.authenticated)
    return (
      <div className="login-shell">
        <div className="login-brand">
          <img
            className="brand-mark"
            src="/gptibo-logo.svg"
            alt="GPTibo"
            width={30}
            height={30}
          />
          <span>
            GPTibo<small>供货平台</small>
          </span>
        </div>
        <form onSubmit={login} className="login-form">
          <h1>{inviteToken.current ? "激活渠道账户" : "登录工作台"}</h1>
          <p>
            {inviteToken.current
              ? "设置密码后，即可使用渠道后台和供货 API。"
              : "管理业务，查询订单与采购余额。"}
          </p>
          {!inviteToken.current && (
            <label className="field">
              <span>邮箱</span>
              <Input
                name="email"
                aria-label="邮箱"
                type="email"
                autoComplete="username"
                required
              />
            </label>
          )}
          <label className="field">
            <span>
              {inviteToken.current ? "设置密码（至少 12 字节）" : "密码"}
            </span>
            <Input
              name="password"
              aria-label="密码"
              type="password"
              autoComplete={
                inviteToken.current ? "new-password" : "current-password"
              }
              required
              minLength={inviteToken.current ? 12 : undefined}
            />
          </label>
          {error && (
            <p role="alert" className="error">
              {error}
            </p>
          )}
          {notice && <p role="status">{notice}</p>}
          <Button variant="primary" type="submit" disabled={busy || !session}>
            {busy ? "处理中…" : inviteToken.current ? "激活账户" : "登录"}
            <ArrowRight />
          </Button>
        </form>
        <footer>supply.example.invalid · GPTibo</footer>
      </div>
    );
  const navigation: Section[] = admin
    ? [
        "overview",
        "channels",
        "orders",
        "products",
        "ledger",
        "cards",
        "cardOperations",
        "accounts",
        "notifications",
        "audit",
      ]
    : [
        "overview",
        "products",
        "orders",
        "ledger",
        "keys",
        "webhook",
        "notifications",
        "docs",
      ];
  const summary = (data.summary as Row) || {},
    wallet = (summary.wallet as Row) || {},
    health = (data.health as Row) || {};
  let content: ReactNode;
  switch (section) {
    case "overview":
      content = (
        <>
          <div className="summary-strip">
            {(admin
              ? [
                  ["完成供货金额", money(summary.volume_minor)],
                  ["累计订单", text(summary.total)],
                  ["待处理", text(summary.pending)],
                  ["已完成", text(summary.succeeded)],
                ]
              : [
                  ["采购余额", money(wallet.available_minor)],
                  ["冻结金额", money(wallet.frozen_minor)],
                  ["累计订单", text(summary.total)],
                  ["待处理", text(summary.pending)],
                ]
            ).map(([label, value]) => (
              <div key={label}>
                <span>{label}</span>
                <strong>{value}</strong>
              </div>
            ))}
          </div>
          <div className="section-heading">
            <h2>最近订单</h2>
            <Button variant="secondary" onClick={() => setSection("orders")}>
              查看全部
              <ArrowRight />
            </Button>
          </div>
          <Grid columns={orderColumns} rows={rows} />
          <div className="service-line">
            <span className={`dot ${health.worker_ready ? "ready" : ""}`} />
            {health.worker_ready ? "订单任务正在运行" : "订单任务尚未就绪"}
            <span>所有余额以人民币结算</span>
          </div>
        </>
      );
      break;
    case "orders":
      content = (
        <>
          <div className="filters">
            <Input
              aria-label="搜索订单"
              placeholder="搜索渠道订单号"
              value={search}
              onChange={(e) => {
                setSearch(e.target.value);
                setOffset(0);
              }}
            />
            <Select
              aria-label="订单状态"
              value={filter}
              onValueChange={(value) => {
                setFilter(value ?? "");
                setOffset(0);
              }}
              items={[
                { value: "", label: "全部状态" },
                ...["queued", "running", "unknown", "succeeded", "failed"].map(
                  (key) => ({ value: key, label: labels[key] }),
                ),
              ]}
            />
          </div>
          <Grid columns={orderColumns} rows={rows} />
          <div className="pager">
            <Button
              disabled={offset === 0}
              onClick={() => setOffset(Math.max(0, offset - 50))}
            >
              上一页
            </Button>
            <span>第 {offset / 50 + 1} 页</span>
            <Button
              disabled={rows.length < 50}
              onClick={() => setOffset(offset + 50)}
            >
              下一页
            </Button>
          </div>
        </>
      );
      break;
    case "cardOperations":
      content = (
        <Grid
          rows={rows}
          columns={[
            [
              "原操作",
              (r) => (
                <span className="stack">
                  {text(r.reference)}
                  <small>{text(r.order_id)}</small>
                </span>
              ),
            ],
            ["类型", (r) => (r.operation === "open" ? "开卡" : "注资")],
            ["卡 ID", (r) => text(r.card_id)],
            [
              "金额 / 费用（USD）",
              (r) =>
                `${(Number(r.amount_minor) / 100).toFixed(2)} / ${(Number(r.fee_minor) / 100).toFixed(2)}`,
            ],
            [
              "状态",
              (r) => (
                <Status
                  value={r.status === "failed" ? "card_failed" : r.status}
                />
              ),
            ],
            ["时间", (r) => date(r.created_at)],
            [
              "操作",
              (r) =>
                ["unknown", "submitting"].includes(String(r.status)) ? (
                  <Button
                    onClick={() =>
                      open({
                        title: "登记原卡操作核对结果",
                        description:
                          "先在卡台核对原操作及资金记录。这里不会重开卡、重新注资。明确未扣款须包含本金与费用；登记成功须对应原卡与卡头。",
                        fields: [
                          {
                            name: "status",
                            label: "核对结果",
                            options: [
                              ["succeeded", "已确认成功"],
                              ["failed", "已确认本金与费用均未扣款"],
                            ],
                          },
                          {
                            name: "card_id",
                            label: "原操作对应卡 ID",
                            type: "number",
                            value: Number(r.card_id || 0),
                          },
                          {
                            name: "note",
                            label: "卡台原操作凭证与核对说明",
                            type: "textarea",
                            help: "至少 10 字，不填写卡号、CVV 或密码",
                          },
                          {
                            name: "confirmation",
                            label: "输入 RESOLVE 确认已核对",
                          },
                        ],
                        action: async (b) => {
                          await mutate("/api/admin/card-operations/resolve", {
                            reference: r.reference,
                            status: b.status,
                            card_id: Number(b.card_id),
                            note: b.note,
                            confirmation: b.confirmation,
                          });
                        },
                      })
                    }
                  >
                    登记核对
                  </Button>
                ) : (
                  <span>{text(r.resolution_note)}</span>
                ),
            ],
          ]}
        />
      );
      break;
    case "channels":
      content = (
        <Grid
          rows={rows}
          columns={[
            [
              "渠道",
              (r) => (
                <span className="stack">
                  {text(r.name)}
                  <small>{text(r.email)}</small>
                </span>
              ),
            ],
            ["采购余额", (r) => money(r.available_minor)],
            ["冻结", (r) => money(r.frozen_minor)],
            ["状态", (r) => <Status value={r.status} />],
            [
              "操作",
              (r) => (
                <div className="row-actions">
                  <Button
                    onClick={() =>
                      open({
                        title: `${r.name} · 余额入账`,
                        description:
                          "核实线下到账后登记。相同凭证不能重复入账。",
                        fields: [
                          { name: "amount", label: "人民币金额" },
                          {
                            name: "reference",
                            label: "入账凭证号",
                            help: "8–128 位字母、数字、点、冒号或连字符",
                          },
                          { name: "note", label: "核实说明" },
                        ],
                        action: async (b) => {
                          await mutate(`/api/admin/channels/${r.id}/credit`, {
                            amount_minor: moneyMinor(b.amount),
                            reference: b.reference,
                            note: b.note,
                          });
                        },
                      })
                    }
                  >
                    入账
                  </Button>
                  <Button
                    onClick={async () => {
                      try {
                        const prices = await api<Row[]>(
                          `/api/admin/channels/${r.id}/prices`,
                        );
                        open({
                          title: `${r.name} · 独立供货价`,
                          fields: [
                            {
                              name: "product_code",
                              label: "商品",
                              options: prices.map((v) => [
                                String(v.code),
                                `${v.name} · 默认 ${money(v.default_minor)} · 渠道 ${money(v.supply_minor)}`,
                              ]),
                            },
                            {
                              name: "price",
                              label: "人民币供货价",
                              optional: true,
                              help: "留空恢复默认供货价",
                            },
                          ],
                          action: async (b) => {
                            await mutate(`/api/admin/channels/${r.id}/prices`, {
                              product_code: b.product_code,
                              supply_minor: b.price
                                ? moneyMinor(b.price)
                                : null,
                            });
                          },
                        });
                      } catch (e) {
                        setError((e as Error).message);
                      }
                    }}
                  >
                    定价
                  </Button>
                  <Button
                    onClick={() =>
                      confirm(
                        "重发邀请",
                        `发送到 ${r.email}，旧邀请将失效。`,
                        () => mutate(`/api/admin/channels/${r.id}/invite`, {}),
                      )
                    }
                  >
                    邀请
                  </Button>
                  <Button
                    onClick={() =>
                      confirm(
                        r.status === "banned" ? "恢复渠道" : "封禁渠道",
                        "封禁后立即停止登录和新 API 请求，已派发订单继续核对。",
                        () =>
                          mutate(`/api/admin/channels/${r.id}/status`, {
                            status: r.status === "banned" ? "active" : "banned",
                          }),
                      )
                    }
                  >
                    {r.status === "banned" ? "恢复" : "封禁"}
                  </Button>
                  <Button
                    onClick={() =>
                      confirm(
                        "删除渠道",
                        "撤销访问并保留历史账务。有余额或未完成订单时不能删除。",
                        () =>
                          mutate(`/api/admin/channels/${r.id}/status`, {
                            status: "deleted",
                          }),
                      )
                    }
                  >
                    删除
                  </Button>
                </div>
              ),
            ],
          ]}
        />
      );
      break;
    case "products":
      content = (
        <>
          {admin && (
            <div className="notice" role="status">
              <span>
                {data.planError
                  ? String(data.planError)
                  : `已读取 ${upstreamPlans.length} 个上游充值套餐。`}
              </span>
              {Boolean(data.planError) && (
                <Button onClick={() => setSection("cards")}>配置卡台</Button>
              )}
              <Button disabled={loading || refreshing} onClick={refresh}>
                刷新套餐
              </Button>
            </div>
          )}
          <Grid
            rows={rows}
            columns={[
              [
                "商品",
                (r) => (
                  <span className="stack">
                    {text(r.name)}
                    <small>{text(r.code)}</small>
                  </span>
                ),
              ],
              [
                "业务",
                (r) => (r.business === "gpt" ? "GPT 自动充值" : "X Premium"),
              ],
              [
                "供货价",
                (r) =>
                  Number(r.supply_minor) > 0 ? money(r.supply_minor) : "待定价",
              ],
              [
                "状态",
                (r) => (
                  <span className="stack">
                    <Status value={r.available ? "on_sale" : "off_sale"} />
                    {r.enabled === true && !r.available && (
                      <small>{text(r.unavailable_reason)}</small>
                    )}
                  </span>
                ),
              ],
              ...(admin
                ? ([
                    [
                      "充值套餐",
                      (r: Row) =>
                        r.business === "gpt"
                          ? text(
                              upstreamPlans.find(
                                (plan) => plan.code === r.upstream_plan,
                              )?.name ||
                                (r.upstream_plan ? "原套餐" : "待绑定"),
                            )
                          : "按商品时长",
                    ],
                    [
                      "操作",
                      (r: Row) => (
                        <Button onClick={() => productForm(r)}>配置</Button>
                      ),
                    ],
                  ] as [string, (r: Row) => ReactNode][])
                : []),
            ]}
          />
          {!admin && (
            <p className="hint">
              选择新建订单填写账号资料。下单冻结供货价，完成后扣款。
            </p>
          )}
        </>
      );
      break;
    case "ledger":
      content = (
        <Grid
          rows={rows}
          columns={[
            ["类型", (r) => <Status value={r.kind} />],
            ["可用余额变动", (r) => money(r.available_delta)],
            ["冻结余额变动", (r) => money(r.frozen_delta)],
            ["凭证", (r) => text(r.reference)],
            ["说明", (r) => text(r.note)],
            ["时间", (r) => date(r.created_at)],
          ]}
        />
      );
      break;
    case "cards": {
      const provider = (data.provider as Row) || {};
      content = (
        <>
          <div className="configuration-line">
            <div>
              <h2>Card provider</h2>
              <p>
                {provider.configured
                  ? `${provider.environment === "production" ? "正式" : "沙盒"}环境 · ${provider.writes_enabled ? "自动执行已开启" : "自动执行已暂停"}`
                  : "尚未配置卡台连接"}
              </p>
            </div>
            <div className="row-actions">
              <Button onClick={() => inventory("balance")}>卡台余额</Button>
              <Button onClick={() => inventory("cards")}>查看卡池</Button>
              <Button onClick={providerForm}>配置连接</Button>
            </div>
          </div>
          <div className="section-heading">
            <h2>业务卡规则</h2>
            <Button onClick={() => ruleForm()}>
              <Plus />
              添加规则
            </Button>
          </div>
          <Grid
            rows={rows}
            columns={[
              [
                "业务／商品",
                (r) => (
                  <span className="stack">
                    {r.business === "gpt" ? "GPT" : "X"}
                    <small>
                      {r.product_code ? text(r.product_code) : "业务默认"}
                    </small>
                  </span>
                ),
              ],
              ["卡头", (r) => text(r.card_product)],
              [
                "主卡／备用卡",
                (r) => Number(r.backup_card_id) && Array.isArray(r.pool) ? <span className="stack">{(r.pool as Row[]).map(p=><span key={String(p.slot)}>{p.slot===1?"主卡":"备用"} · {text(p.last_four)} · 拒付 {text(p.declines)}/2{p.replacement_status ? <small>换卡处理中：{text(p.replacement_error || p.replacement_status)}</small> : null}</span>)}</span> : (Number(r.card_id) ? `内部 ID ${r.card_id}` : "自动开卡"),
              ],
              ["自动补余额", (r) => r.auto_open ? `低于 $${Number(r.refill_threshold_minor || r.funding_minor) / 100} 补 $${Number(r.funding_minor) / 100}` : "未开启"],
              ["拒付换卡", (r) => r.replace_on_decline ? Number(r.backup_card_id) ? "单卡累计 2 次换新" : `每单最多 ${r.max_replacements} 次` : "未开启"],
              ["消费限额", (r) => r.use_provider_limits ? "卡台默认" : `$${Number(r.max_order_minor) / 100}`],
              ["每日注资上限", (r) => `$${Number(r.daily_limit_minor) / 100}`],
              [
                "状态",
                (r) => <Status value={r.enabled ? "on_sale" : "off_sale"} />,
              ],
              [
                "操作",
                (r) => <Button onClick={() => ruleForm(r)}>修改</Button>,
              ],
            ]}
          />
          <p className="hint">
            卡头使用卡台产品代码。按业务分配卡片，不跨业务借用卡池。
          </p>
        </>
      );
      break;
    }
    case "accounts":
      content = (
        <Grid
          rows={rows}
          columns={[
            ["账号", (r) => text(r.name)],
            ["每日订单上限", (r) => text(r.daily_limit)],
            [
              "状态",
              (r) => <Status value={r.enabled ? "on_sale" : "off_sale"} />,
            ],
            [
              "操作",
              (r) => <Button onClick={() => accountForm(r)}>修改</Button>,
            ],
          ]}
        />
      );
      break;
    case "keys":
      content = (
        <Grid
          rows={rows}
          columns={[
            ["名称", (r) => text(r.label)],
            ["Key ID", (r) => <code>{text(r.id)}</code>],
            ["状态", (r) => <Status value={r.revoked ? "banned" : "active"} />],
            ["创建时间", (r) => date(r.created_at)],
            [
              "操作",
              (r) => (
                <Button
                  disabled={Boolean(r.revoked)}
                  onClick={() =>
                    confirm("撤销接口密钥", "撤销后此密钥立即失效。", () =>
                      mutate(`/api/keys/${r.id}/revoke`, {}),
                    )
                  }
                >
                  撤销
                </Button>
              ),
            ],
          ]}
        />
      );
      break;
    case "webhook":
      content = (
        <div className="configuration-line">
          <div>
            <h2>订单状态通知</h2>
            <p>{rows[0]?.url ? String(rows[0].url) : "尚未配置回调地址"}</p>
            <p>POST JSON · HMAC-SHA256 · 失败自动重试</p>
          </div>
          <Button
            onClick={() =>
              open({
                title: "配置回调",
                fields: [
                  {
                    name: "url",
                    label: "HTTPS 回调地址",
                    value: String(rows[0]?.url || ""),
                  },
                  {
                    name: "secret",
                    label: "签名密钥",
                    type: "password",
                    help: "至少 32 字符，与你的服务端保持一致",
                  },
                ],
                action: async (b) => {
                  await mutate("/api/webhook", b);
                },
              })
            }
          >
            配置
          </Button>
        </div>
      );
      break;
    case "notifications":
      content = (
        <Grid
          rows={rows}
          columns={[
            [
              "事件",
              (r) => (
                <span className="stack">
                  {labels[String(r.kind)]}
                  <small>{text(r.id)}</small>
                </span>
              ),
            ],
            ["状态", (r) => <Status value={r.status} />],
            ["尝试次数", (r) => text(r.attempts)],
            [
              "说明",
              (r) => r.last_error
                ? labels[String(r.last_error)] || "投递失败，请管理员核查"
                : r.kind === "invitation" && r.status === "sent"
                  ? "Resend 已接收；收件结果请查看 Resend 邮件记录"
                  : "—",
            ],
            ["下次尝试", (r) => date(r.next_at)],
            ["创建时间", (r) => date(r.created_at)],
          ]}
        />
      );
      break;
    case "audit":
      content = (
        <Grid
          rows={rows}
          columns={[
            ["操作", (r) => text(r.action)],
            ["对象", (r) => text(r.target)],
            ["操作者", (r) => text(r.actor_id)],
            ["说明", (r) => text(r.note)],
            ["时间", (r) => date(r.created_at)],
          ]}
        />
      );
      break;
    case "docs":
      content = (
        <div className="docs">
          <h2>服务端签名接入</h2>
          <p>
            Base URL：<code>https://supply.example.invalid/v1</code>
            。请在你的服务器生成签名，Secret 只保存在服务器。
          </p>
          <Grid
            rows={[
              {
                id: "products",
                method: "GET",
                path: "/products",
                purpose: "商品与渠道供货价",
              },
              {
                id: "balance",
                method: "GET",
                path: "/balance",
                purpose: "可用余额与冻结余额",
              },
              {
                id: "preflight",
                method: "POST",
                path: "/preflight",
                purpose: "账号预检",
              },
              {
                id: "orders",
                method: "POST",
                path: "/orders",
                purpose: "创建自动充值订单",
              },
              {
                id: "query",
                method: "GET",
                path: "/orders/:id",
                purpose: "按平台订单 ID 查询订单详情与状态",
              },
              {
                id: "merchant-query",
                method: "GET",
                path: "/orders/by-merchant/:number",
                purpose: "按你的商户订单号精确查询，也用于下单超时恢复",
              },
              {
                id: "order-list",
                method: "GET",
                path: "/orders?limit=50&offset=0",
                purpose: "分页查询本渠道订单，支持 status、search 筛选",
              },
            ]}
            columns={[
              ["方法", (r) => text(r.method)],
              ["路径", (r) => <code>{text(r.path)}</code>],
              ["用途", (r) => text(r.purpose)],
            ]}
          />
          <h3>签名内容</h3>
          <pre>
            {
              "METHOD\nPATH\nSORTED_QUERY\nTIMESTAMP\nNONCE\nKEY_ID\nIDEMPOTENCY_KEY\nSHA256(RAW_BODY)"
            }
          </pre>
          <p>
            使用 Secret 做
            HMAC-SHA256，输出小写十六进制。请求头：X-Key-Id、X-Timestamp（秒）、X-Nonce、X-Signature；下单必须带稳定
            Idempotency-Key。接口金额单位为人民币分。
          </p>
          <h3>订单查询</h3>
          <p>
            三个查询接口均需签名，只返回当前密钥所属渠道的订单。
            下单成功响应中的 id 是平台订单 ID；merchant_order_no 是你下单时提供的商户订单号。
            路径中的 :id、:number 要替换成实际值，并进行 URL 编码。
          </p>
          <pre>{`GET /v1/orders/ord_example
GET /v1/orders/by-merchant/your-order-001
GET /v1/orders?limit=50&offset=0&status=succeeded&search=your-order`}</pre>
          <Grid
            rows={[
              { id: "limit", name: "limit", description: "每页条数，默认 50，可填 1–100" },
              { id: "offset", name: "offset", description: "跳过条数，默认 0；下一页加上 limit，最大 1000000" },
              { id: "status", name: "status", description: "可选，按下方订单状态筛选；不传表示所有状态" },
              { id: "search", name: "search", description: "可选，模糊匹配商户订单号或精确匹配平台订单 ID" },
            ]}
            columns={[
              ["列表参数", (r) => <code>{text(r.name)}</code>],
              ["说明", (r) => text(r.description)],
            ]}
          />
          <h3>Node.js 查询示例</h3>
          <p>
            在服务器配置 PLATFORM_KEY_ID 和 PLATFORM_API_SECRET，使用“接口密钥”页面创建的 Key ID 与 Secret。
            GET 不发送请求体，也不需要 Idempotency-Key；签名对应的这两项使用空字符串。
            签名 PATH 必须包含 /v1，查询参数按 RFC3986 编码后的键和值排序，八行之间用换行连接，末尾不加换行。
            每次查询生成新的 Nonce，时间戳与服务器时间允许相差 300 秒。
          </p>
          <pre>{`import { createHash, createHmac, randomUUID } from 'node:crypto'

const base = 'https://supply.example.invalid'
const keyId = process.env.PLATFORM_KEY_ID
const secret = process.env.PLATFORM_API_SECRET
if (!keyId || !secret) throw new Error('请配置渠道 Key ID 和 Secret')
const encode = value => encodeURIComponent(value).replace(
  /[!'()*]/g, c => '%' + c.charCodeAt(0).toString(16).toUpperCase()
)

async function queryOrder(path) {
  const url = new URL('/v1' + path, base)
  const timestamp = String(Math.floor(Date.now() / 1000))
  const nonce = randomUUID()
  const query = [...url.searchParams]
    .map(([key, value]) => [encode(key), encode(value)])
    .sort(([ak, av], [bk, bv]) => ak < bk ? -1 : ak > bk ? 1 : av < bv ? -1 : av > bv ? 1 : 0)
    .map(([key, value]) => key + '=' + value).join('&')
  const canonical = [
    'GET', url.pathname, query, timestamp, nonce, keyId, '',
    createHash('sha256').update('').digest('hex')
  ].join('\\n')
  const response = await fetch(url, {
    method: 'GET', redirect: 'error', signal: AbortSignal.timeout(30000),
    headers: {
      'X-Key-Id': keyId, 'X-Timestamp': timestamp, 'X-Nonce': nonce,
      'X-Signature': createHmac('sha256', secret).update(canonical).digest('hex')
    }
  })
  const result = await response.json()
  if (!response.ok) throw Object.assign(
    new Error(result.error?.message || '订单查询失败'),
    { status: response.status, code: result.error?.code }
  )
  return result.data
}

// 按平台订单 ID 查询
const order = await queryOrder('/orders/' + encodeURIComponent('ord_example'))
// 按原商户订单号精确查询
const original = await queryOrder('/orders/by-merchant/' + encodeURIComponent('your-order-001'))
// 查询订单列表，返回数组
const orders = await queryOrder('/orders?limit=50&offset=0&status=succeeded')
console.log({ order, original, orders })`}</pre>
          <h3>查询响应与状态</h3>
          <p>
            详情查询返回 {`{ "data": { ...订单 } }`}，列表查询返回 {`{ "data": [ ...订单 ] }`}。
            supply_minor 为人民币分，1700 表示 ¥17.00；stage 为执行阶段，failure_code 为失败或待核原因，
            receipt 为平台凭证，manual_payment 表示曾进入人工处理。
          </p>
          <pre>{JSON.stringify({
            data: {
              id: "ord_example",
              merchant_order_no: "your-order-001",
              product_code: "x-3m",
              supply_minor: 1700,
              status: "succeeded",
              stage: "paid",
              failure_code: "",
              receipt: "rcpt_example",
              manual_payment: false,
              created_at: "2026-10-06T06:00:00Z",
              updated_at: "2026-10-06T06:01:00Z",
            },
          }, null, 2)}</pre>
          <Grid
            rows={[
              { id: "queued", state: "queued", description: "排队中，供货款已冻结" },
              { id: "running", state: "running", description: "执行中，继续查询原单" },
              { id: "unknown", state: "unknown", description: "结果待核对，供货款保持冻结；不能按失败重新下单" },
              { id: "succeeded", state: "succeeded", description: "订单成功，已完成扣款" },
              { id: "failed", state: "failed", description: "订单明确失败，冻结款已退回可用余额" },
            ]}
            columns={[
              ["订单状态", (r) => <code>{text(r.state)}</code>],
              ["处理方式", (r) => text(r.description)],
            ]}
          />
          <p>
            未完成订单建议每 10–20 秒查询一次，收到终态后停止轮询；所有 API 请求合计每个渠道最多 300 次/分钟，
            429 时延迟重试。401 请检查密钥、签名和时间戳；404 表示该渠道没有可见订单。
            下单超时或响应丢失时，先用原 merchant_order_no 查询，404 不能证明原请求绝不会受理；
            如需重试下单，保留原订单号、Idempotency-Key 和全部请求资料，使用新 Nonce，避免重复充值。
            收到回调后也可用订单查询核实原单状态。
          </p>
          <h3>GPT 自动充值</h3>
          <p>
            支持完整 Session JSON、Access Token、邮箱密码三种资料。预检后可带
            confirmed_account 和
            expected_supply_minor，校验真实账号与确认过的供货价。
          </p>
          <pre>
            {JSON.stringify(
              {
                merchant_order_no: "your-order-001",
                product_code: "gpt-plus",
                credential: {
                  mode: "access_token",
                  accessToken: "<账号 Access Token>",
                },
              },
              null,
              2,
            )}
          </pre>
          <h3>X 自动赠送</h3>
          <pre>
            {JSON.stringify(
              {
                merchant_order_no: "your-order-002",
                product_code: "x-3m",
                recipient: "username",
              },
              null,
              2,
            )}
          </pre>
          <p>
            创建响应丢失时，先按原渠道订单号精确查询。404
            不代表原请求绝不会受理；重试保留原订单号、幂等键和全部资料，使用新
            Nonce。明确未扣费的失败释放冻结款；未知或已扣费待核继续冻结。
          </p>
          <h3>回调验签</h3>
          <p>
            X-Signature = HMAC-SHA256(secret, timestamp + "." + event_id + "." +
            raw_body)。使用 X-Event-Id 去重，校验通过后返回 2xx。
          </p>
        </div>
      );
      break;
  }
  function accountForm(row?: Row) {
    open({
      title: row ? "修改 X 账号" : "添加 X 账号",
      description:
        "Cookie 与代理认证加密保存。修改时留空保留原值；停用账号不需要重新填写秘密。",
      fields: [
        { name: "name", label: "账号名称", value: String(row?.name || "") },
        {
          name: "auth_token",
          label: "auth_token",
          type: "password",
          optional: Boolean(row),
        },
        { name: "ct0", label: "ct0", type: "password", optional: Boolean(row) },
        {
          name: "proxy",
          label: "出口代理",
          optional: true,
          help: "http://用户:密码@主机:端口 或 socks5://…；修改时留空保留原出口",
        },
        {
          name: "clear_proxy",
          label: "清除原出口代理",
          value: "false",
          options: [
            ["false", "保留原代理或更新为填写的地址"],
            ["true", "清除代理，使用服务器直连"],
          ],
        },
        {
          name: "daily_limit",
          label: "每日上限",
          type: "number",
          value: Number(row?.daily_limit || 100),
        },
        {
          name: "enabled",
          label: "状态",
          value: row?.enabled === false ? "false" : "true",
          options: [
            ["true", "启用"],
            ["false", "停用"],
          ],
        },
      ],
      action: async (b) => {
        await mutate("/api/admin/x-accounts", {
          ...b,
          id: row?.id || "",
          daily_limit: Number(b.daily_limit),
          enabled: b.enabled === "true",
          clear_proxy: b.clear_proxy === "true",
        });
      },
    });
  }
  return (
    <div className={`workspace${sidebarCollapsed ? " sidebar-collapsed" : ""}`}>
      <aside className="sidebar">
        <div className="brand">
          <img
            className="brand-mark"
            src="/gptibo-logo.svg"
            alt="GPTibo"
            width={30}
            height={30}
          />
          <span className="brand-name">
            GPTibo<small>供货平台</small>
          </span>
        </div>
        <div className="nav-label">{admin ? "平台管理" : "渠道工作台"}</div>
        <nav id="workspace-navigation" aria-label="工作台导航">
          {navigation.map((key) => {
            const [label, Icon] = sections[key];
            return (
              <Tooltip
                key={key}
                content={label}
                side="right"
                disabled={!sidebarCollapsed}
                delay={150}
                render={
                  <button
                    aria-label={label}
                    aria-current={section === key ? "page" : undefined}
                    onClick={() => {
                      setSection(key);
                      setOffset(0);
                      setSearch("");
                      setFilter("");
                      setNotice("");
                    }}
                  />
                }
              >
                <Icon size={18} />
                <span className="nav-text">{label}</span>
              </Tooltip>
            );
          })}
        </nav>
        <div className="sidebar-bottom">
          <span className="avatar">{session.name?.slice(0, 1)}</span>
          <span className="stack">
            {session.name}
            <small>{session.email}</small>
          </span>
          <button
            aria-label="退出登录"
            disabled={busy}
            onClick={async () => {
              try {
                await api("/api/logout", {});
                setSession({ authenticated: false });
              } catch (e) {
                setError((e as Error).message);
              }
            }}
          >
            <SignOut size={18} />
          </button>
        </div>
        <div className="sidebar-toggle-row">
          <Button
            variant="ghost"
            shape="square"
            className="sidebar-toggle"
            aria-label={sidebarCollapsed ? "展开侧栏" : "收起侧栏"}
            title={sidebarCollapsed ? "展开侧栏" : "收起侧栏"}
            aria-expanded={!sidebarCollapsed}
            aria-controls="workspace-navigation"
            onClick={toggleSidebar}
          >
            <SidebarSimple size={18} mirrored={sidebarCollapsed} />
          </Button>
        </div>
      </aside>
      <div className="workspace-main">
        <div className="workspace-topbar">
          <span className="workspace-origin">{window.location.host}</span>
          <span className="workspace-role">
            {admin ? "管理后台" : "渠道后台"}
          </span>
          <span className="workspace-account">{session.email}</span>
        </div>
        <main>
          <header>
            <div>
              <span className="eyebrow">
                {admin ? "管理后台" : "渠道后台"} / GPTibo
              </span>
              <h1>{sections[section][0]}</h1>
              <p className="page-description">
                {section === "overview" && !admin
                  ? "查看采购余额、冻结金额与订单处理状态。"
                  : section === "products" && !admin
                    ? "查看可采购套餐与供货价，确认账号后提交充值订单。"
                    : sectionDescriptions[section]}
              </p>
            </div>
            <div className="header-actions">
              <Button
                className="mobile-logout"
                shape="square"
                aria-label="退出登录"
                disabled={busy}
                onClick={async () => {
                  try {
                    await api("/api/logout", {});
                    setSession({ authenticated: false });
                  } catch (e) {
                    setError((e as Error).message);
                  }
                }}
              >
                <SignOut />
              </Button>
              <Button
                aria-label="刷新"
                shape="square"
                onClick={refresh}
                disabled={loading || refreshing}
              >
                <ArrowClockwise
                  className={loading || refreshing ? "spin" : ""}
                />
              </Button>
              {section === "channels" && (
                <Button variant="primary" onClick={invite}>
                  <Plus />
                  邀请渠道
                </Button>
              )}
              {section === "accounts" && (
                <Button variant="primary" onClick={() => accountForm()}>
                  <Plus />
                  添加账号
                </Button>
              )}
              {section === "products" && !admin && (
                <Button
                  variant="primary"
                  disabled={
                    loading ||
                    refreshing ||
                    Boolean(pending) ||
                    !rows.some((r) => r.available)
                  }
                  onClick={() => orderForm()}
                >
                  <Plus />
                  新建订单
                </Button>
              )}
              {section === "keys" && (
                <Button
                  variant="primary"
                  onClick={() =>
                    open({
                      title: "创建接口密钥",
                      fields: [{ name: "label", label: "密钥名称" }],
                      action: async (b) => {
                        const key = await api<Row>("/api/keys", b);
                        refresh();
                        return {
                          title: "保存接口密钥",
                          description:
                            "Secret 仅展示这一次，请保存到你的服务端。",
                          content: (
                            <dl className="detail">
                              <div>
                                <dt>Key ID</dt>
                                <dd>
                                  <code>{text(key.id)}</code>
                                </dd>
                              </div>
                              <div>
                                <dt>Secret</dt>
                                <dd>
                                  <code>{text(key.secret)}</code>
                                </dd>
                              </div>
                            </dl>
                          ),
                        };
                      },
                    })
                  }
                >
                  <Plus />
                  创建密钥
                </Button>
              )}
            </div>
          </header>
          {!admin && pending && (
            <div className="notice" role="status">
              <span>
                订单 {pending.merchant_order_no}{" "}
                的受理结果待确认。查询原单后再创建新请求。
              </span>
              <Button
                onClick={async () => {
                  try {
                    if (!(await recoverOrder(pending)))
                      setNotice(
                        "暂未找到原单。请等待后再查询，或使用原资料恢复同一请求。",
                      );
                  } catch (e) {
                    setError((e as Error).message);
                  }
                }}
              >
                查询原单
              </Button>
              {section === "products" && (
                <Button onClick={() => orderForm(pending)}>恢复同一请求</Button>
              )}
            </div>
          )}
          {notice && (
            <div className="notice" role="status">
              {notice}
            </div>
          )}
          {error && (
            <div className="error" role="alert">
              {error}
              <Button onClick={refresh}>重试</Button>
            </div>
          )}
          {loading ? (
            <div className="loading">
              <ArrowClockwise className="spin" />
              正在读取…
            </div>
          ) : (
            !error && (
              <section className="content" key={section} aria-busy={refreshing}>
                {[
                  "channels",
                  "ledger",
                  "notifications",
                  "audit",
                  "cardOperations",
                ].includes(section) && (
                  <div className="table-toolbar">
                    <Input
                      aria-label="搜索记录"
                      value={search}
                      placeholder="搜索名称、订单号或操作凭证"
                      onChange={(e) => {
                        setSearch(e.target.value);
                        setOffset(0);
                      }}
                    />
                  </div>
                )}
                {content}
                {[
                  "channels",
                  "ledger",
                  "notifications",
                  "audit",
                  "cardOperations",
                ].includes(section) && (
                  <div className="pagination">
                    <Button
                      disabled={offset === 0}
                      onClick={() => setOffset(Math.max(0, offset - 50))}
                    >
                      上一页
                    </Button>
                    <span>第 {offset / 50 + 1} 页</span>
                    <Button
                      disabled={rows.length < 50}
                      onClick={() => setOffset(offset + 50)}
                    >
                      下一页
                    </Button>
                  </div>
                )}
              </section>
            )
          )}
        </main>
      </div>
      <Dialog.Root
        open={Boolean(modal)}
        onOpenChange={(value) => {
          if (!value && !busy) {
            setModal(null);
            setModalError("");
          }
        }}
      >
        <Dialog
          className={`platform-dialog ${(modal?.fields?.length || 0) > 4 ? "dialog-wide" : ""}`}
          size={(modal?.fields?.length || 0) > 4 ? "xl" : "lg"}
        >
          <form onSubmit={submitModal} key={modal?.title}>
            <div className="dialog-heading">
              <Dialog.Title>{modal?.title}</Dialog.Title>
              <Button
                type="button"
                variant="ghost"
                shape="square"
                aria-label="关闭弹窗"
                disabled={busy}
                onClick={() => {
                  setModal(null);
                  setModalError("");
                }}
              >
                <X />
              </Button>
            </div>
            {modal?.description && (
              <Dialog.Description className="dialog-description">
                {modal.description}
              </Dialog.Description>
            )}
            <div className="dialog-body">
              {modal?.fields && (
                <div className="dialog-fields">
                  <Fields fields={modal.fields} />
                </div>
              )}
              {modal?.content}
            </div>
            {modalError && (
              <p className="error" role="alert">
                {modalError}
              </p>
            )}
            <div className="dialog-actions">
              <Button
                type="button"
                disabled={busy}
                onClick={() => setModal(null)}
              >
                {modal?.action ? "取消" : "关闭"}
              </Button>
              {modal?.action && (
                <Button type="submit" variant="primary" disabled={busy}>
                  {busy ? "处理中…" : modal.submit || "保存"}
                </Button>
              )}
            </div>
          </form>
        </Dialog>
      </Dialog.Root>
    </div>
  );
}
