import { useState } from 'react'
import { ArrowUpRight } from 'lucide-react'
import { VisitorStats, type VisitorPeriod, type VisitorSummary } from '@/components/visitor-stats'
import { Feedback } from '@/components/workspace-loading'
import { useResource } from '@/lib/use-resource'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { partnerStorefrontPath } from '../../shared/partner-link.ts'

type AdminSummary = VisitorSummary & {
  platform: { visitors: number; pageViews: number }
  partners: { id: string; name: string; slug: string; active: number; visitors: number; pageViews: number }[]
}

export default function AdminVisitorsPage({ revision }: { revision: number }) {
  const [selected, setSelected] = useState<{ id: string; name: string }>()
  const [days, setDays] = useState<VisitorPeriod>(7)
  const { data, error, loading } = useResource<AdminSummary>(`/api/admin/visitors?days=${days}`, revision)

  return (
    <>
      <VisitorStats
        endpoint="/api/admin/visitors"
        revision={revision}
        title="全站访问趋势"
        description="全平台直营与所有代理商店的综合访问与页面浏览走势"
        selectedDays={days}
        onDaysChange={setDays}
      />
      <section className="admin-panel visitor-shops-panel">
        <div className="admin-panel-title">
          <div>
            <h2>各商店渠道访客</h2>
            <span>最近 {days} 天 · 独立访客按商店分别去重统计</span>
          </div>
          {data && <span>共 {data.partners.length + 1} 个访问来源</span>}
        </div>
        <Feedback error={error} loading={loading} data={data} shape="compact" />
        {data && (
          <div className="admin-table-scroll">
            <table className="admin-table">
              <thead>
                <tr>
                  <th>商店渠道</th>
                  <th>专属推广路径</th>
                  <th>独立访客 (UV)</th>
                  <th>页面浏览量 (PV)</th>
                  <th>人均浏览深度</th>
                  <th><span className="sr-only">操作</span></th>
                </tr>
              </thead>
              <tbody>
                <tr>
                  <td>
                    <strong>平台直营</strong>
                    <small>官方主站主通道</small>
                  </td>
                  <td><code>/</code></td>
                  <td className="admin-numeric"><strong>{data.platform.visitors.toLocaleString('zh-CN')}</strong></td>
                  <td className="admin-numeric">{data.platform.pageViews.toLocaleString('zh-CN')}</td>
                  <td className="admin-numeric">
                    {data.platform.visitors > 0 ? (data.platform.pageViews / data.platform.visitors).toFixed(1) : '0.0'} 页/人
                  </td>
                  <td />
                </tr>
                {data.partners.map((partner) => (
                  <tr key={partner.id}>
                    <td>
                      <strong>{partner.name}</strong>
                      <small className={partner.active ? 'text-success' : ''}>
                        {partner.active ? '已启用' : '已停用'}
                      </small>
                    </td>
                    <td><code>{partnerStorefrontPath(partner.slug)}</code></td>
                    <td className="admin-numeric"><strong>{partner.visitors.toLocaleString('zh-CN')}</strong></td>
                    <td className="admin-numeric">{partner.pageViews.toLocaleString('zh-CN')}</td>
                    <td className="admin-numeric">
                      {partner.visitors > 0 ? (partner.pageViews / partner.visitors).toFixed(1) : '0.0'} 页/人
                    </td>
                    <td>
                      <Button variant="ghost" size="sm" onClick={() => setSelected(partner)}>
                        查看趋势 <ArrowUpRight size={13} />
                      </Button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>
      {selected && (
        <Dialog open onOpenChange={(open) => { if (!open) setSelected(undefined) }}>
          <DialogContent className="motion-dialog visitor-detail-dialog">
            <DialogHeader>
              <DialogTitle>{selected.name} · 专属商店访问趋势</DialogTitle>
              <DialogDescription>
                仅统计该代理专属推广链接及对应商品的浏览数据。
              </DialogDescription>
            </DialogHeader>
            <VisitorStats
              endpoint={`/api/admin/partners/${selected.id}/visitors`}
              revision={revision}
              title={`${selected.name} 访客走势`}
              description="专属推广链接独立访客与浏览量趋势"
              selectedDays={days}
              onDaysChange={setDays}
            />
          </DialogContent>
        </Dialog>
      )}
    </>
  )
}
