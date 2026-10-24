'use client'

import { Suspense, useEffect, useState } from 'react'
import Link from 'next/link'
import { useRouter, useSearchParams } from 'next/navigation'
import { Download, Package, Plus, Printer } from 'lucide-react'
import { Badge, Button, Card, Empty, Money, PageHeader, Pagination, QueryState, SearchInput, Select, Skeleton, Table, Tabs, useToast } from '@/components/ui'
import { downloadFile, fmtQty, useDebounced, useList, usePerm } from '@/lib/hooks'
import { errorMessage, get } from '@/lib/api'
import { formatRate } from '@/lib/money'
import { PERMS } from '@/lib/rbac'
import { csvMoney, toCSV } from '@/lib/stock/csv'
import { fetchAllPages } from '@/lib/stock/scope'
import { TAX_CATEGORY_LABEL, type ItemFull } from '@/lib/types.stock'
import type { ListResponse } from '@/lib/types'
import { useCategories } from '@/components/stock/hooks'
import { LabelJobModal } from '@/components/stock/LabelJobModal'

type Status = 'active' | 'all'
const LIMIT = 50

function TaxCell({ it }: { it: ItemFull }) {
  if (it.tax_category && it.tax_category !== 'taxable') return <Badge tone="gray">{TAX_CATEGORY_LABEL[it.tax_category]}</Badge>
  return (
    <span className="inline-flex flex-wrap items-center gap-1">
      <Badge tone="brand">{formatRate(it.tax_rate_bp)}</Badge>
      {it.cess_rate_bp > 0 && <Badge tone="amber">+{formatRate(it.cess_rate_bp)} cess</Badge>}
      {!!it.cess_per_unit_paise && <Badge tone="amber">cess/unit</Badge>}
      {!it.price_includes_tax && <Badge tone="gray">+GST</Badge>}
    </span>
  )
}

function ItemsList() {
  const router = useRouter()
  const params = useSearchParams()
  const canDo = usePerm()
  const toast = useToast()
  const [q, setQ] = useState(params.get('q') ?? '')
  const dq = useDebounced(q)
  const [category, setCategory] = useState(params.get('category') ?? '')
  const [status, setStatus] = useState<Status>(params.get('status') === 'all' ? 'all' : 'active')
  const [offset, setOffset] = useState(0)
  const [selected, setSelected] = useState<Record<string, ItemFull>>({})
  const [labels, setLabels] = useState(false)
  const [exporting, setExporting] = useState(false)
  const categories = useCategories()
  const filters = { q: dq.trim(), category, status }
  const list = useList<ItemFull>('/bhandar/v1/items', { ...filters, limit: LIMIT, offset })

  useEffect(() => {
    const sp = new URLSearchParams()
    if (filters.q) sp.set('q', filters.q)
    if (category) sp.set('category', category)
    if (status === 'all') sp.set('status', 'all')
    const s = sp.toString()
    router.replace(`/app/items${s ? `?${s}` : ''}`, { scroll: false })
  }, [filters.q, category, status, router])

  const change = <T,>(set: (v: T) => void) => (v: T) => { set(v); setOffset(0) }
  const sel = Object.values(selected)
  const toggle = (it: ItemFull) => setSelected((s) => { const n = { ...s }; if (n[it.id]) delete n[it.id]; else n[it.id] = it; return n })
  const pageIds = list.data?.items.map((i) => i.id) ?? []
  const allOnPage = pageIds.length > 0 && pageIds.every((id) => selected[id])

  const exportCSV = async () => {
    setExporting(true)
    try {
      const rows = await fetchAllPages<ItemFull>((limit, off) => get<ListResponse<ItemFull>>('/bhandar/v1/items', { ...filters, limit, offset: off }))
      downloadFile(`items-${status}.csv`, toCSV(
        ['Name', 'SKU', 'Barcode', 'PLU', 'HSN', 'Unit', 'Category', 'Brand', 'MRP', 'Sale price', 'Purchase price', 'GST %', 'Cess %', 'Tax category', 'Price incl. tax', 'Track batches', 'Re-order level', 'Re-order qty', 'Status'],
        rows.map((i) => [i.name, i.sku, i.barcode, i.plu, i.hsn, i.unit, i.category, i.brand, csvMoney(i.mrp_paise), csvMoney(i.sale_price_paise), csvMoney(i.purchase_price_paise),
          i.tax_rate_bp / 100, i.cess_rate_bp / 100, i.tax_category, i.price_includes_tax, i.track_batches, i.reorder_level, i.reorder_qty, i.status]),
      ), 'text/csv')
    } catch (e) { toast(errorMessage(e), 'error') } finally { setExporting(false) }
  }

  const open = (id: string) => router.push(`/app/items/${id}`)

  return (
    <>
      <PageHeader title="Items" subtitle="Item master: codes, prices, GST and re-order rules"
        actions={
          <>
            {sel.length > 0 && <Button variant="secondary" icon={Printer} onClick={() => setLabels(true)}>Print labels ({sel.length})</Button>}
            <Button variant="secondary" icon={Download} onClick={exportCSV} loading={exporting}>Export</Button>
            {canDo(PERMS.itemManage) && <Link href="/app/items/new"><Button icon={Plus}>Add item</Button></Link>}
          </>
        } />

      <Card padded={false}>
        <div className="flex flex-col gap-3 border-b border-slate-100 p-3 sm:p-4 md:flex-row md:items-center md:justify-between">
          <div className="flex flex-wrap items-center gap-2">
            <Tabs value={status} onChange={change(setStatus)} options={[{ value: 'active', label: 'Active' }, { value: 'all', label: 'All' }]} />
            <Select aria-label="Category" value={category} onChange={(e) => change(setCategory)(e.target.value)} className="w-auto min-w-40">
              <option value="">All categories</option>
              {categories.map((c) => <option key={c} value={c}>{c}</option>)}
            </Select>
          </div>
          <SearchInput value={q} onChange={change(setQ)} placeholder="Name, SKU, barcode or PLU" autoFocus />
        </div>

        <QueryState isLoading={list.isLoading} error={list.error} data={list.data} retry={() => list.refetch()}>
          {(d) => d.items.length === 0 ? (
            <Empty icon={Package} title={filters.q || category ? 'No items match' : 'No items yet'} text={filters.q || category ? 'Try another search or clear the filters.' : 'Add products to start billing; barcodes are generated for you.'}
              action={canDo(PERMS.itemManage) && !filters.q ? <Link href="/app/items/new"><Button icon={Plus}>Add the first item</Button></Link> : undefined} />
          ) : (
            <>
              {/* mobile */}
              <ul className="divide-y divide-slate-100 md:hidden">
                {d.items.map((it) => (
                  <li key={it.id} className="flex items-center gap-3 px-3 py-3">
                    <input type="checkbox" className="h-4 w-4 accent-brand-600" checked={!!selected[it.id]} onChange={() => toggle(it)} aria-label={`Select ${it.name}`} />
                    <button type="button" onClick={() => open(it.id)} className="min-w-0 flex-1 text-left cursor-pointer">
                      <div className="truncate text-sm font-semibold">{it.name}{it.status !== 'active' && <Badge tone="red" className="ml-2">inactive</Badge>}</div>
                      <div className="truncate text-xs text-slate-500">{[it.sku, it.barcode, it.hsn && `HSN ${it.hsn}`].filter(Boolean).join(' · ')}</div>
                      <div className="mt-1 flex items-center gap-2 text-xs"><TaxCell it={it} /><span className="text-slate-400">{it.unit}</span></div>
                    </button>
                    <div className="text-right">
                      <Money paise={it.sale_price_paise} className="block text-sm font-bold" />
                      {it.mrp_paise > 0 && it.mrp_paise !== it.sale_price_paise && <span className="text-[11px] text-slate-400">MRP <Money paise={it.mrp_paise} /></span>}
                    </div>
                  </li>
                ))}
              </ul>
              {/* desktop */}
              <div className="hidden md:block">
                <Table>
                  <thead className="bg-slate-50/60">
                    <tr>
                      <th className="th w-10"><input type="checkbox" className="h-4 w-4 accent-brand-600" checked={allOnPage} onChange={() => setSelected((s) => { const n = { ...s }; if (allOnPage) pageIds.forEach((id) => delete n[id]); else d.items.forEach((i) => { n[i.id] = i }); return n })} aria-label="Select all on this page" /></th>
                      <th className="th">Item</th><th className="th">Barcode</th><th className="th">HSN</th><th className="th">Unit</th>
                      <th className="th text-right">MRP</th><th className="th text-right">Sale</th><th className="th text-right">Purchase</th>
                      <th className="th">GST</th><th className="th text-right">Re-order</th>
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-slate-100">
                    {d.items.map((it) => (
                      <tr key={it.id} onClick={() => open(it.id)} className={`cursor-pointer hover:bg-slate-50/60 ${selected[it.id] ? 'bg-brand-50/40' : ''}`}>
                        <td className="td" onClick={(e) => e.stopPropagation()}><input type="checkbox" className="h-4 w-4 accent-brand-600" checked={!!selected[it.id]} onChange={() => toggle(it)} aria-label={`Select ${it.name}`} /></td>
                        <td className="td">
                          <div className="font-semibold">{it.name}{it.status !== 'active' && <Badge tone="red" className="ml-2">inactive</Badge>}{it.track_batches && <Badge tone="gray" className="ml-2">batches</Badge>}</div>
                          <div className="text-xs text-slate-500">{[it.sku, it.category, it.brand].filter(Boolean).join(' · ') || '—'}</div>
                        </td>
                        <td className="td font-mono text-xs text-slate-600">{it.barcode || '—'}</td>
                        <td className="td font-mono text-xs text-slate-600">{it.hsn || '—'}</td>
                        <td className="td text-xs text-slate-600">{it.unit}{it.alt_units?.length ? <span className="text-slate-400"> +{it.alt_units.length}</span> : null}</td>
                        <td className="td text-right"><Money paise={it.mrp_paise} className="text-slate-600" /></td>
                        <td className="td text-right"><Money paise={it.sale_price_paise} className="font-semibold" /></td>
                        <td className="td text-right"><Money paise={it.purchase_price_paise} className="text-slate-600" /></td>
                        <td className="td"><TaxCell it={it} /></td>
                        <td className="td num text-right text-slate-600">{it.reorder_level > 0 ? fmtQty(it.reorder_level) : '—'}</td>
                      </tr>
                    ))}
                  </tbody>
                </Table>
              </div>
              <Pagination total={d.total} limit={d.limit || LIMIT} offset={d.offset} onChange={setOffset} />
            </>
          )}
        </QueryState>
      </Card>

      <LabelJobModal open={labels} onClose={() => setLabels(false)} items={sel} />
    </>
  )
}

export default function ItemsPage() {
  return <Suspense fallback={<Skeleton />}><ItemsList /></Suspense>
}
