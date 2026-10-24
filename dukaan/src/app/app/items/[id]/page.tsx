'use client'

import { useState } from 'react'
import Link from 'next/link'
import { useParams, useRouter } from 'next/navigation'
import { Archive, Boxes, Layers, Pencil, Printer, RotateCcw, SlidersHorizontal } from 'lucide-react'
import { Badge, Button, Card, Confirm, Empty, ErrorBox, Field, Input, Money, PageHeader, Pagination, QueryState, Spinner, Table, Tabs, Toggle, useToast } from '@/components/ui'
import { fmtDate, fmtQty, useGet, useList, usePerm, useStore } from '@/lib/hooks'
import { del, post } from '@/lib/api'
import { formatRate } from '@/lib/money'
import { PERMS } from '@/lib/rbac'
import { expiryTone } from '@/lib/stock/reasons'
import { storeName } from '@/lib/stock/scope'
import { TAX_CATEGORY_LABEL, type Balance, type Batch, type ItemFull, type Movement } from '@/lib/types.stock'
import type { Party } from '@/lib/types'
import { useAction, useAuthedImage } from '@/components/stock/hooks'
import { LabelJobModal } from '@/components/stock/LabelJobModal'
import { MovementTable } from '@/components/stock/MovementTable'
import { ReorderEditor } from '@/components/stock/ReorderEditor'
import { StoreSelect } from '@/components/stock/StoreSelect'

type Tab = 'stock' | 'ledger' | 'batches'

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return <div className="flex justify-between gap-4 py-1.5 text-sm"><span className="shrink-0 text-slate-500">{label}</span><span className="min-w-0 text-right font-medium">{children}</span></div>
}

function StockTab({ id }: { id: string }) {
  const { stores } = useStore()
  const [byBatch, setByBatch] = useState(false)
  const stock = useList<Balance>('/bhandar/v1/stock', { item_id: id, by_batch: byBatch, limit: 500 })
  return (
    <>
      <div className="flex items-center justify-between gap-3 border-b border-slate-100 px-4 py-3">
        <span className="text-sm font-semibold">Stock per store</span>
        <Toggle checked={byBatch} onChange={setByBatch} label="By batch" />
      </div>
      <QueryState isLoading={stock.isLoading} error={stock.error} data={stock.data} retry={() => stock.refetch()}>
        {(d) => d.items.length === 0 ? <Empty icon={Boxes} title="No stock" text="This item has no stock position yet. Purchases and opening stock create one." /> : (
          <Table>
            <thead className="bg-slate-50/60"><tr><th className="th">Store</th>{byBatch && <><th className="th">Batch</th><th className="th">Expiry</th></>}<th className="th text-right">On hand</th><th className="th text-right">Avg cost</th><th className="th text-right">Value</th><th className="th">Last in / out</th></tr></thead>
            <tbody className="divide-y divide-slate-100">
              {d.items.map((b) => (
                <tr key={`${b.store_id}-${b.batch_id ?? ''}`}>
                  <td className="td font-semibold">{storeName(stores, b.store_id)}</td>
                  {byBatch && <><td className="td font-mono text-xs">{b.batch_no || '—'}</td><td className="td text-xs text-slate-600">{b.expiry_date ? fmtDate(b.expiry_date) : '—'}</td></>}
                  <td className={`td num text-right font-semibold ${b.qty < 0 ? 'text-rose-600' : b.reorder_level && b.qty <= b.reorder_level ? 'text-amber-600' : ''}`}>{fmtQty(b.qty)} <span className="text-xs font-normal text-slate-400">{b.unit}</span></td>
                  <td className="td text-right"><Money paise={b.avg_cost_paise} className="text-slate-600" /></td>
                  <td className="td text-right"><Money paise={b.value_paise} /></td>
                  <td className="td text-xs text-slate-500">{fmtDate(b.last_in_at) || '—'} / {fmtDate(b.last_out_at) || '—'}</td>
                </tr>
              ))}
            </tbody>
            <tfoot><tr className="border-t border-slate-200 bg-slate-50/60 font-semibold"><td className="td" colSpan={byBatch ? 3 : 1}>Total</td><td className="td num text-right">{fmtQty(d.items.reduce((a, b) => a + b.qty, 0))}</td><td className="td" /><td className="td text-right"><Money paise={d.items.reduce((a, b) => a + b.value_paise, 0)} /></td><td className="td" /></tr></tfoot>
          </Table>
        )}
      </QueryState>
    </>
  )
}

function LedgerTab({ id }: { id: string }) {
  const { storeId } = useStore()
  const [store, setStore] = useState(storeId ?? '')
  const [from, setFrom] = useState('')
  const [to, setTo] = useState('')
  const [offset, setOffset] = useState(0)
  const ledger = useList<Movement>(`/bhandar/v1/items/${id}/ledger`, { store_id: store, from, to, limit: 50, offset })
  return (
    <>
      <div className="flex flex-wrap items-end gap-2 border-b border-slate-100 p-3 sm:p-4">
        <StoreSelect value={store} onChange={(v) => { setStore(v); setOffset(0) }} allowAll />
        <Field label="From"><Input type="date" value={from} onChange={(e) => { setFrom(e.target.value); setOffset(0) }} className="w-40" /></Field>
        <Field label="To"><Input type="date" value={to} onChange={(e) => { setTo(e.target.value); setOffset(0) }} className="w-40" /></Field>
      </div>
      <QueryState isLoading={ledger.isLoading} error={ledger.error} data={ledger.data} retry={() => ledger.refetch()}>
        {(d) => d.items.length === 0 ? <Empty icon={Layers} title="No movements" text="Nothing moved in this period." /> : (
          <>
            <MovementTable rows={d.items} showStore={!store} />
            <Pagination total={d.total} limit={d.limit || 50} offset={d.offset} onChange={setOffset} />
          </>
        )}
      </QueryState>
    </>
  )
}

function BatchesTab({ id }: { id: string }) {
  const [store, setStore] = useState('')
  const batches = useGet<{ batches: Batch[] }>(`/bhandar/v1/items/${id}/batches`, { store_id: store })
  const today = Date.now()
  return (
    <>
      <div className="flex flex-wrap items-center justify-between gap-2 border-b border-slate-100 p-3 sm:p-4">
        <span className="text-sm font-semibold">Batches</span>
        <StoreSelect value={store} onChange={setStore} allowAll />
      </div>
      <QueryState isLoading={batches.isLoading} error={batches.error} data={batches.data?.batches} retry={() => batches.refetch()}
        empty={<Empty icon={Layers} title="No batches" text="Batch numbers arrive with purchase lines once batch tracking is on." />}>
        {(rows) => (
          <Table>
            <thead className="bg-slate-50/60"><tr><th className="th">Batch</th><th className="th">Mfg</th><th className="th">Expiry</th><th className="th text-right">Batch MRP</th><th className="th text-right">Qty held</th></tr></thead>
            <tbody className="divide-y divide-slate-100">
              {rows.map((b) => {
                const days = b.expiry_date ? Math.round((new Date(`${b.expiry_date}T00:00:00+05:30`).getTime() - today) / 86400000) : null
                return (
                  <tr key={b.id}>
                    <td className="td font-mono text-sm font-semibold">{b.batch_no}</td>
                    <td className="td text-sm text-slate-600">{b.mfg_date ? fmtDate(b.mfg_date) : '—'}</td>
                    <td className="td text-sm">{b.expiry_date ? <span className="inline-flex items-center gap-2">{fmtDate(b.expiry_date)}{days !== null && <Badge tone={expiryTone(days)}>{days < 0 ? `expired ${-days}d ago` : `${days}d left`}</Badge>}</span> : '—'}</td>
                    <td className="td text-right">{b.mrp_paise ? <Money paise={b.mrp_paise} /> : '—'}</td>
                    <td className={`td num text-right font-semibold ${b.qty <= 0 ? 'text-slate-400' : ''}`}>{fmtQty(b.qty)}</td>
                  </tr>
                )
              })}
            </tbody>
          </Table>
        )}
      </QueryState>
    </>
  )
}

export default function ItemDetailPage() {
  const { id } = useParams<{ id: string }>()
  const router = useRouter()
  const canDo = usePerm()
  const toast = useToast()
  const item = useGet<ItemFull>(`/bhandar/v1/items/${id}`)
  const it = item.data
  const supplier = useGet<Party>(it?.preferred_supplier_id ? `/bahi/v1/parties/${it.preferred_supplier_id}` : null)
  const barcode = useAuthedImage(it?.barcode ? `/bhandar/v1/items/${id}/barcode.svg?module=2&height=60` : null)
  const [tab, setTab] = useState<Tab>('stock')
  const [reorder, setReorder] = useState(false)
  const [labels, setLabels] = useState(false)
  const [confirm, setConfirm] = useState(false)
  const deactivate = useAction(() => del(`/bhandar/v1/items/${id}`), {
    invalidate: ['/bhandar/v1/items', `/bhandar/v1/items/${id}`],
    onSuccess: () => { setConfirm(false); toast('Item deactivated') },
  })
  const reactivate = useAction(() => post(`/bhandar/v1/items/${id}/activate`), {
    invalidate: ['/bhandar/v1/items', `/bhandar/v1/items/${id}`],
    onSuccess: () => toast('Item is active again'),
  })
  const canManage = canDo(PERMS.itemManage)

  return (
    <>
      <PageHeader back={() => router.push('/app/items')}
        title={it ? <span className="inline-flex flex-wrap items-center gap-2">{it.name}{it.status !== 'active' && <Badge tone="red">inactive</Badge>}</span> : 'Item'}
        subtitle={it ? [it.sku, it.category, it.brand].filter(Boolean).join(' · ') || undefined : undefined}
        actions={it && (
          <>
            {it.barcode && <Button variant="secondary" icon={Printer} onClick={() => setLabels(true)}>Print label</Button>}
            {(canManage || canDo(PERMS.stockManage)) && <Button variant="secondary" icon={SlidersHorizontal} onClick={() => setReorder(true)}>Re-order rule</Button>}
            {canManage && <Link href={`/app/items/${id}/edit`}><Button icon={Pencil}>Edit</Button></Link>}
            {canManage && it.status === 'active' && <Button variant="ghost" icon={Archive} className="text-rose-600" onClick={() => setConfirm(true)}>Deactivate</Button>}
          </>
        )} />

      <QueryState isLoading={item.isLoading} error={item.error} data={it} retry={() => item.refetch()}>
        {(x) => (
          <>
            {x.status !== 'active' && (
              <div className="mb-4 flex items-start gap-3 rounded-xl bg-amber-50 px-4 py-3 text-sm text-amber-800">
                <RotateCcw size={18} className="mt-0.5 shrink-0" />
                <div className="flex-1">This item is inactive: it is hidden from the counter and from re-order suggestions, and its stock history stays.</div>
                {canManage && <Button size="sm" variant="secondary" icon={RotateCcw} loading={reactivate.isPending} onClick={() => reactivate.mutate()}>Reactivate</Button>}
              </div>
            )}
            <div className="grid gap-4 lg:grid-cols-3">
              <Card title="Item" className="lg:col-span-1">
                <div className="mb-4 flex flex-col items-center rounded-xl border border-dashed border-slate-200 p-3">
                  {x.barcode ? (
                    barcode.url ? (
                      // eslint-disable-next-line @next/next/no-img-element -- authenticated SVG via object URL
                      <img src={barcode.url} alt={`Barcode ${x.barcode}`} className="h-16 max-w-full" />
                    ) : barcode.error ? <ErrorBox message={barcode.error} /> : <Spinner />
                  ) : <span className="text-xs text-slate-500">No barcode</span>}
                  <div className="mt-1 font-mono text-xs tracking-wider text-slate-600">{x.barcode}</div>
                </div>
                <div className="divide-y divide-slate-100">
                  <Row label="SKU">{x.sku || '—'}</Row>
                  <Row label="PLU">{x.plu || '—'}</Row>
                  <Row label="HSN / SAC">{x.hsn || '—'}</Row>
                  <Row label="Unit">{x.unit}{x.alt_units?.length ? <span className="block text-xs font-normal text-slate-500">{x.alt_units.map((a) => `1 ${a.unit} = ${fmtQty(a.factor)} ${x.unit}`).join(' · ')}</span> : null}</Row>
                  <Row label="MRP"><Money paise={x.mrp_paise} /></Row>
                  <Row label="Sale price"><Money paise={x.sale_price_paise} /></Row>
                  <Row label="Purchase price"><Money paise={x.purchase_price_paise} /></Row>
                  <Row label="GST">{x.tax_category !== 'taxable' ? TAX_CATEGORY_LABEL[x.tax_category] : <>{formatRate(x.tax_rate_bp)}{x.cess_rate_bp ? ` + ${formatRate(x.cess_rate_bp)} cess` : ''}{x.cess_per_unit_paise ? <> + <Money paise={x.cess_per_unit_paise} />/unit</> : null}<span className="block text-xs font-normal text-slate-500">{x.price_includes_tax ? 'prices include GST' : 'GST added on top'}</span></>}</Row>
                  <Row label="Batches">{x.track_batches ? 'Tracked' : 'Not tracked'}</Row>
                  <Row label="Re-order">{x.reorder_level > 0 ? <>at {fmtQty(x.reorder_level)} → order {fmtQty(x.reorder_qty)} {x.unit}</> : 'Off'}</Row>
                  <Row label="Preferred supplier">{x.preferred_supplier_id ? (supplier.data ? <Link href={`/app/parties/${x.preferred_supplier_id}`} className="link">{supplier.data.name}</Link> : '…') : '—'}</Row>
                  {x.external_ref && <Row label="External ref"><span className="font-mono text-xs">{x.external_ref}</span></Row>}
                  <Row label="Updated">{fmtDate(x.updated_at, true)}</Row>
                </div>
              </Card>

              <Card padded={false} className="lg:col-span-2">
                <div className="border-b border-slate-100 px-3 py-3 sm:px-4">
                  <Tabs value={tab} onChange={setTab} options={[{ value: 'stock', label: 'Stock' }, { value: 'ledger', label: 'Ledger' }, { value: 'batches', label: 'Batches' }]} />
                </div>
                {tab === 'stock' && <StockTab id={id} />}
                {tab === 'ledger' && <LedgerTab id={id} />}
                {tab === 'batches' && <BatchesTab id={id} />}
              </Card>
            </div>
          </>
        )}
      </QueryState>

      <ReorderEditor item={reorder && it ? it : null} onClose={() => setReorder(false)} />
      {it && <LabelJobModal open={labels} onClose={() => setLabels(false)} items={[it]} />}
      <Confirm open={confirm} onClose={() => setConfirm(false)} onConfirm={() => deactivate.mutate()} loading={deactivate.isPending} danger confirmLabel="Deactivate"
        title="Deactivate this item?" text="It disappears from the counter and from re-order alerts. Stock movements and past bills keep referring to it." />
    </>
  )
}
