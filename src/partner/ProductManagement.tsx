import { useEffect, useRef, useState } from 'react'
import { closestCenter, DndContext, DragOverlay, KeyboardSensor, PointerSensor, useSensor, useSensors, type DragEndEvent } from '@dnd-kit/core'
import { arrayMove, SortableContext, sortableKeyboardCoordinates, useSortable, verticalListSortingStrategy } from '@dnd-kit/sortable'
import { restrictToVerticalAxis } from '@dnd-kit/modifiers'
import { CSS } from '@dnd-kit/utilities'
import { ArrowUpRight, Check, ChevronDown, Eye, EyeOff, GripVertical, RefreshCw, RotateCcw, Save } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import { ProductMark } from '@/components/brand'
import { Feedback } from '@/components/workspace-loading'
import { api, errorMessage } from '@/lib/api'
import { useResource } from '@/lib/use-resource'
import { DEFAULT_PRODUCT_ORDER, STOREFRONT_PRODUCTS, type ProductOrderSettings, type StorefrontProductId } from '../../shared/product-order'
import { money, type Offer } from './model'
import { Pricing } from './shared'
import './product-management.css'

type Product = typeof STOREFRONT_PRODUCTS[number]
const productFor = (id: string) => STOREFRONT_PRODUCTS.find(product => product.id === id)!

function ProductSummary({ product, plans, hidden = false }: { product: Product; plans: Offer[]; hidden?: boolean }) {
  const available = plans.filter(plan => plan.enabled)
  const lowest = available.length ? Math.min(...available.map(plan => plan.price_cents)) : null
  return <>
    <ProductMark small productId={product.id} />
    <span className="partner-product-info"><strong>{product.name}</strong><small>{hidden ? '已下架 · 商店中隐藏' : 'upcoming' in product ? '待上架 · 可提前安排位置' : plans.length ? `${plans.length} 个套餐${available.length ? '' : ' · 暂停售卖'}` : '暂未开通套餐'}</small></span>
    <span className="partner-product-price">{lowest !== null ? <><strong>{money(lowest)}</strong><small>起</small></> : <small>{'upcoming' in product ? '敬请期待' : '暂无在售套餐'}</small>}</span>
  </>
}

function SortableProduct({ id, index, plans, locked, readOnly, hidden, onToggle, onSaved }: { hidden: boolean; onToggle: () => void; id: StorefrontProductId; index: number; plans: Offer[]; locked: boolean; readOnly: boolean; onSaved: () => void }) {
  const product = productFor(id)
  const [open, setOpen] = useState(id === 'chatgpt')
  const { attributes, listeners, setNodeRef, setActivatorNodeRef, transform, transition, isDragging } = useSortable({ id, disabled: locked })
  return <li ref={setNodeRef} className="partner-product-item" data-dragging={isDragging || undefined} style={{ transform: CSS.Transform.toString(transform), transition }}>
    <Collapsible open={open && plans.length > 0} onOpenChange={setOpen}>
      <div className="partner-product-row">
        {!readOnly && <Button type="button" ref={setActivatorNodeRef} variant="ghost" size="icon" className="partner-product-handle" {...attributes} {...listeners} aria-label={`拖动排序 ${product.name}`} disabled={locked}><GripVertical size={18} /></Button>}
        <span className="partner-product-position" aria-label={`第 ${index + 1} 位`}>{String(index + 1).padStart(2, '0')}</span>
        <CollapsibleTrigger className="partner-product-trigger" disabled={!plans.length} aria-label={`${open ? '收起' : '展开'} ${product.name} 套餐定价`}>
          <ProductSummary product={product} plans={plans} hidden={hidden} />
          {plans.length > 0 && <ChevronDown className="partner-product-chevron" size={16} />}
        </CollapsibleTrigger>
        {!readOnly && <Button type="button" variant="outline" size="sm" className="partner-product-visibility" disabled={locked} aria-label={`${hidden ? '上架' : '下架'} ${product.name}`} onClick={onToggle}>{hidden ? <Eye size={15} /> : <EyeOff size={15} />}{hidden ? '上架' : '下架'}</Button>}
      </div>
      <CollapsibleContent className="partner-product-details" keepMounted>
        <Pricing plans={plans} readOnly={readOnly || locked} endpoint={plan => '/api/partner/prices/' + plan.id} onSaved={onSaved} />
      </CollapsibleContent>
    </Collapsible>
  </li>
}

function ProductManagementForm({ settings, plans, endpoint, disabled, readOnly, onSaved, onReload, loading }: { settings: ProductOrderSettings; plans: Offer[]; endpoint: string; disabled: boolean; readOnly: boolean; onSaved: () => void; onReload: () => void; loading: boolean }) {
  const [saved, setSaved] = useState(settings)
  const [ids, setIds] = useState(settings.productIds)
  const [hiddenIds, setHiddenIds] = useState(settings.hiddenProductIds ?? [])
  const [activeId, setActiveId] = useState<StorefrontProductId | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [message, setMessage] = useState('')
  const submitting = useRef(false)
  const changed = ids.join(',') !== saved.productIds.join(',') || [...hiddenIds].sort().join(',') !== [...(saved.hiddenProductIds ?? [])].sort().join(',')
  const locked = disabled || readOnly || busy
  const sensors = useSensors(useSensor(PointerSensor, { activationConstraint: { distance: 6 } }), useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }))

  useEffect(() => {
    if (!changed) return
    const warn = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = '' }
    window.addEventListener('beforeunload', warn)
    return () => window.removeEventListener('beforeunload', warn)
  }, [changed])

  function move({ active, over }: DragEndEvent) {
    setActiveId(null)
    if (locked || !over || active.id === over.id) return
    const from = ids.indexOf(active.id as StorefrontProductId), to = ids.indexOf(over.id as StorefrontProductId)
    if (from < 0 || to < 0) return
    setIds(arrayMove(ids, from, to)); setError(''); setMessage(`${productFor(String(active.id)).name} 已移至第 ${to + 1} 位，保存设置后生效。`)
  }

  async function save() {
    if (locked || submitting.current || !changed || activeId) return
    submitting.current = true; setBusy(true); setError(''); setMessage('')
    try {
      const result = await api<ProductOrderSettings>(endpoint, { productIds: ids, hiddenProductIds: hiddenIds, version: saved.version })
      setSaved(result); setIds(result.productIds); setHiddenIds(result.hiddenProductIds ?? []); setMessage('商品设置已保存，专属商店已生效。')
    } catch (e) { setError(errorMessage(e)) }
    finally { submitting.current = false; setBusy(false) }
  }

  return <section className="partner-products-panel" aria-label="商品上下架、排序与定价" aria-busy={busy}>
    <div className="partner-products-toolbar">
      <div><strong>商店商品设置</strong><span>{ids.length} 件商品</span><span className="partner-products-dirty">{changed ? '设置未保存' : '已同步'}</span></div>
      <div>
        <Button type="button" variant="ghost" size="sm" disabled={loading || busy || changed || Boolean(activeId)} aria-label="重新读取已保存的商品设置" onClick={onReload}><RefreshCw size={14} />重新读取</Button>
        {!readOnly && <Button type="button" variant="ghost" size="sm" disabled={locked || Boolean(activeId) || ids.join(',') === DEFAULT_PRODUCT_ORDER.join(',')} onClick={() => { setIds([...DEFAULT_PRODUCT_ORDER]); setError(''); setMessage('已恢复默认顺序，保存后生效。') }}><RotateCcw size={14} />恢复默认</Button>}
      </div>
    </div>
    <DndContext sensors={sensors} collisionDetection={closestCenter} modifiers={[restrictToVerticalAxis]} onDragStart={({ active }) => { setActiveId(active.id as StorefrontProductId); setMessage('') }} onDragEnd={move} onDragCancel={() => { setActiveId(null); setMessage('已取消拖动，顺序未改变。') }} accessibility={{
      screenReaderInstructions: { draggable: '按空格键开始拖动，使用上下方向键调整商品顺序，再按空格键放下，按 Escape 取消。' },
      announcements: {
        onDragStart: ({ active }) => `已拾起 ${productFor(String(active.id)).name}。`,
        onDragOver: ({ active, over }) => over ? `${productFor(String(active.id)).name} 将移至第 ${ids.indexOf(over.id as StorefrontProductId) + 1} 位。` : undefined,
        onDragEnd: ({ active, over }) => over ? `${productFor(String(active.id)).name} 已放至第 ${ids.indexOf(over.id as StorefrontProductId) + 1} 位，请保存设置。` : '已取消拖动。',
        onDragCancel: () => '已取消拖动，顺序未改变。',
      },
    }}>
      <SortableContext items={ids} strategy={verticalListSortingStrategy}>
        <ol className="partner-products-list" aria-label="商店商品展示顺序">
          {ids.map((id, index) => <SortableProduct key={id} id={id} index={index} plans={plans.filter(plan => plan.product_id === id)} locked={locked} readOnly={readOnly} hidden={hiddenIds.includes(id)} onToggle={() => { setHiddenIds(current => current.includes(id) ? current.filter(item => item !== id) : [...current, id]); setError(''); setMessage(`${productFor(id).name} 已选择${hiddenIds.includes(id) ? '上架' : '下架'}，保存设置后生效。`) }} onSaved={onSaved} />)}
        </ol>
      </SortableContext>
      <DragOverlay dropAnimation={{ duration: 180, easing: 'ease-out' }}>{activeId && <div className="partner-product-overlay"><GripVertical size={18} /><ProductSummary product={productFor(activeId)} plans={plans.filter(plan => plan.product_id === activeId)} /></div>}</DragOverlay>
    </DndContext>
    <div className="partner-products-footer">
      <div className="partner-products-feedback"><Feedback error={error} /><p role="status">{message || (readOnly ? '只读预览 · 展开商品查看套餐与售价' : '上下架与排序保存后生效；下架包含该商品的全部套餐，已有订单不受影响。')}{!error && message.startsWith('商品设置已保存') && <Check size={14} />}</p></div>
      {!readOnly && <div className="partner-products-actions">
        {changed && <Button type="button" variant="ghost" disabled={locked || Boolean(activeId)} onClick={() => { setIds(saved.productIds); setHiddenIds(saved.hiddenProductIds ?? []); setError(''); setMessage('已撤销本次商品设置调整。') }}>撤销调整</Button>}
        <Button type="button" disabled={locked || !changed || Boolean(activeId)} onClick={() => void save()}><Save size={15} />{busy ? '保存中…' : '保存设置'}</Button>
      </div>}
    </div>
  </section>
}

export function ProductManagement({ endpoint, plans, readOnly = false, disabled = false, onSaved, storeUrl }: { endpoint: string; plans: Offer[]; readOnly?: boolean; disabled?: boolean; onSaved: () => void; storeUrl: string }) {
  const [revision, setRevision] = useState(0)
  const { data, error, loading } = useResource<ProductOrderSettings>(endpoint, revision)
  return <>
    <div className="partner-section-heading partner-products-heading"><div><h2>商品管理</h2><p>下架不卖的商品，调整展示顺序与售价。上架仍以平台开放和库存为准。</p></div><a href={storeUrl} target="_blank" rel="noreferrer">预览商店 <ArrowUpRight size={14} /></a></div>
    <Feedback error={error} loading={loading} data={data} shape="compact" />
    {error && <Button variant="outline" onClick={() => setRevision(value => value + 1)}>重新加载商品</Button>}
    {data && <ProductManagementForm key={endpoint + ':' + revision + ':' + data.version} settings={data} plans={plans} endpoint={endpoint} disabled={disabled || loading || Boolean(error)} loading={loading} readOnly={readOnly} onSaved={onSaved} onReload={() => setRevision(value => value + 1)} />}
  </>
}
