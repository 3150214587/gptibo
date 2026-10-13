import { useResource } from '@/lib/use-resource'
import { Feedback } from '@/components/workspace-loading'
type Event = { id: string; time: string; kind: string; ip: string; requestId: string; route: string; method: string; status: number; role: string; account: string; session: string; target: string }
export default function SecurityPage({ revision }: { revision: number }) {
  const result = useResource<{ enabled: boolean; events: Event[] }>('/api/admin/security', revision)
  return <section className="admin-section"><p>最近 7 天的最新 200 条安全记录。连续异常会触发 15 分钟限制，并按 15 分钟窗口汇总邮件告警。成功读取按来源与接口采样，验证码和会话原文不记录。</p><Feedback {...result} />{result.data && (!result.data.enabled ? <p>安全审计尚未启用。</p> : <div className="admin-table-wrap"><table className="admin-table"><thead><tr><th>时间 / 请求</th><th>来源 / 身份</th><th>操作</th><th>结果</th></tr></thead><tbody>{result.data.events.map(e => <tr key={e.id}><td>{new Date(e.time).toLocaleString('zh-CN')}<br /><small>{e.requestId}</small></td><td>{e.ip}<br />{e.account || e.role}<br /><small>会话 {e.session.slice(0, 12) || '—'}</small></td><td>{e.method} {e.route}{e.target && <><br /><small>订单标识 {e.target.slice(0, 12)}</small></>}</td><td>{e.kind === 'block' ? '限制 15 分钟' : e.status}</td></tr>)}</tbody></table>{!result.data.events.length && <p>暂无安全记录。</p>}</div>)}</section>
}
