'use client'

/** Read-only BOM views: components/operations summary, multi-level explosion tree and cost roll-up. */
import { useState } from 'react'
import Link from 'next/link'
import { AlertTriangle, ChevronRight, GitBranch, IndianRupee } from 'lucide-react'
import { Badge, Card, Empty, Input, Money, QueryState, Table, type Tone } from '@/components/ui'
import { fmtQty, useGet } from '@/lib/hooks'
import type { Bom, BomCost, BomExplode, BomNode } from '@/lib/types.more'

export const bomTone = (status: string): Tone => (status === 'active' ? 'green' : status === 'draft' ? 'blue' : 'gray')

export function BomDetails({ bom }: { bom: Bom }) {
  return (
    <div className="grid gap-4 lg:grid-cols-3">
      <Card title={`Components (per ${fmtQty(bom.output_qty)} ${bom.unit})`} padded={false} className="lg:col-span-2">
        <Table>
          <thead><tr><th className="th">#</th><th className="th">Item</th><th className="th text-right">Qty</th><th className="th">Unit</th><th className="th text-right">Scrap %</th><th className="th">Type</th></tr></thead>
          <tbody>
            {bom.components.map((c, i) => (
              <tr key={c.item_id} className="border-t border-slate-100">
                <td className="td text-slate-400">{c.line_no ?? i + 1}</td>
                <td className="td font-medium">{c.item_name}</td>
                <td className="td num text-right">{fmtQty(c.qty)}</td>
                <td className="td">{c.unit}</td>
                <td className="td num text-right">{c.scrap_percent ? fmtQty(c.scrap_percent) : '—'}</td>
                <td className="td">{c.is_sub_assembly ? <Badge tone="brand">sub-assembly{c.sub_bom_id ? ' · pinned' : ''}</Badge> : <span className="text-xs text-slate-500">raw material</span>}</td>
              </tr>
            ))}
          </tbody>
        </Table>
      </Card>
      <div className="space-y-4">
        <Card title="Operations & overhead" padded={false}>
          {bom.operations.length === 0 ? <p className="px-4 py-4 text-sm text-slate-500">None</p> : (
            <ul className="divide-y divide-slate-100">
              {bom.operations.map((o, i) => (
                <li key={o.line_no ?? i} className="flex items-center justify-between px-4 py-2.5 text-sm"><span>{o.name}</span><Money paise={o.cost_paise} className="font-semibold" /></li>
              ))}
              <li className="flex items-center justify-between px-4 py-2.5 text-sm font-bold"><span>Per batch</span><Money paise={bom.operations.reduce((a, o) => a + o.cost_paise, 0)} /></li>
            </ul>
          )}
        </Card>
        {bom.notes && <Card title="Notes"><p className="whitespace-pre-wrap text-sm text-slate-600">{bom.notes}</p></Card>}
      </div>
    </div>
  )
}

/** GET /v1/boms/{id}/explode?qty= rendered as an indented tree plus the flattened raw-material list. */
export function BomExplodeView({ bom }: { bom: Bom }) {
  const [qty, setQty] = useState(String(bom.output_qty))
  const n = Number(qty)
  const q = useGet<BomExplode>(`/nirmaan/v1/boms/${bom.id}/explode`, { qty: n > 0 ? n : bom.output_qty })
  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-end gap-3">
        <label className="block"><span className="label">Explode for quantity</span><Input className="num w-36" inputMode="decimal" value={qty} onChange={(e) => setQty(e.target.value)} /></label>
        <span className="pb-2.5 text-sm text-slate-500">{bom.unit} of {bom.item_name}</span>
      </div>
      <QueryState isLoading={q.isLoading} error={q.error} data={q.data} retry={() => q.refetch()}>
        {(d) => (
          <div className="grid gap-4 lg:grid-cols-2">
            <Card title={<span className="inline-flex items-center gap-2"><GitBranch size={14} /> Structure</span>} padded={false}>
              <TreeNode node={d.tree} root />
            </Card>
            <Card title="Raw material required (leaves only)" padded={false}>
              {d.requirements.length === 0 ? <Empty icon={GitBranch} title="Nothing to buy" text="Every component is a sub-assembly." /> : (
                <Table>
                  <thead><tr><th className="th">Item</th><th className="th text-right">Qty</th><th className="th">Unit</th></tr></thead>
                  <tbody>{d.requirements.map((r) => (
                    <tr key={r.item_id} className="border-t border-slate-100"><td className="td font-medium">{r.item_name}</td><td className="td num text-right">{fmtQty(r.qty)}</td><td className="td">{r.unit}</td></tr>
                  ))}</tbody>
                </Table>
              )}
            </Card>
          </div>
        )}
      </QueryState>
    </div>
  )
}

function TreeNode({ node, root }: { node: BomNode; root?: boolean }) {
  const [open, setOpen] = useState(true)
  const kids = node.children ?? []
  return (
    <div>
      <div className={`flex items-center gap-2 py-2 pr-4 text-sm ${root ? 'bg-slate-50 font-bold' : ''}`} style={{ paddingLeft: `${16 + node.level * 20}px` }}>
        {kids.length > 0 ? (
          <button type="button" onClick={() => setOpen((o) => !o)} className="grid h-5 w-5 place-items-center rounded text-slate-500 hover:bg-slate-200 cursor-pointer" aria-label={open ? 'Collapse' : 'Expand'} aria-expanded={open}>
            <ChevronRight size={14} className={`transition ${open ? 'rotate-90' : ''}`} />
          </button>
        ) : <span className="w-5" />}
        <span className="min-w-0 flex-1 truncate">
          {node.bom_id && !root ? <Link href={`/app/production/boms/${node.bom_id}`} className="link">{node.item_name}</Link> : node.item_name}
          {node.is_sub_assembly && <Badge tone="brand" className="ml-2">sub-assembly</Badge>}
        </span>
        <span className="num whitespace-nowrap">{fmtQty(node.qty)} {node.unit}</span>
      </div>
      {open && kids.map((k) => <TreeNode key={`${k.item_id}-${k.level}`} node={k} />)}
    </div>
  )
}

/** GET /v1/boms/{id}/cost?qty= as a roll-up table; unpriced leaves are flagged loudly. */
export function BomCostView({ bom }: { bom: Bom }) {
  const [qty, setQty] = useState(String(bom.output_qty))
  const n = Number(qty)
  const q = useGet<BomCost>(`/nirmaan/v1/boms/${bom.id}/cost`, { qty: n > 0 ? n : bom.output_qty })
  const nameOf = (id: string) => bom.components.find((c) => c.item_id === id)?.item_name ?? id
  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-end gap-3">
        <label className="block"><span className="label">Cost for quantity</span><Input className="num w-36" inputMode="decimal" value={qty} onChange={(e) => setQty(e.target.value)} /></label>
        <span className="pb-2.5 text-sm text-slate-500">{bom.unit} · raw material at bhandar purchase price, sub-assemblies rolled up</span>
      </div>
      <QueryState isLoading={q.isLoading} error={q.error} data={q.data} retry={() => q.refetch()}>
        {(d) => (
          <div className="space-y-4">
            {d.missing_prices && d.missing_prices.length > 0 && (
              <div className="flex items-start gap-3 rounded-xl border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-800" role="alert">
                <AlertTriangle size={18} className="mt-0.5 shrink-0" />
                <div>
                  <div className="font-bold">{d.missing_prices.length} item{d.missing_prices.length > 1 ? 's have' : ' has'} no purchase price — costed at ₹0</div>
                  <div className="mt-1">Set a purchase price in <Link href="/app/items" className="link">Items</Link> for: {d.missing_prices.map(nameOf).join(', ')}. The totals below are understated until then.</div>
                </div>
              </div>
            )}
            <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
              <Stat label="Material" paise={d.material_paise} />
              <Stat label="Operations" paise={d.operations_paise} />
              <Stat label={`Total for ${fmtQty(d.qty)} ${bom.unit}`} paise={d.total_paise} strong />
              <Stat label={`Per ${bom.unit}`} paise={d.unit_cost_paise} strong />
            </div>
            <Card title="Direct components" padded={false}>
              <Table>
                <thead><tr><th className="th">Item</th><th className="th text-right">Qty (incl. scrap)</th><th className="th text-right">Unit cost</th><th className="th text-right">Extended</th></tr></thead>
                <tbody>
                  {d.lines.map((l) => {
                    const missing = !l.is_sub_assembly && l.unit_cost_paise === 0
                    return (
                      <tr key={l.item_id} className={`border-t border-slate-100 ${missing ? 'bg-amber-50/60' : ''}`}>
                        <td className="td font-medium">{l.item_name} {l.is_sub_assembly && <Badge tone="brand" className="ml-1">sub-assembly</Badge>} {missing && <Badge tone="amber" className="ml-1">no price</Badge>}</td>
                        <td className="td num text-right">{fmtQty(l.qty)}</td>
                        <td className="td text-right"><Money paise={l.unit_cost_paise} /></td>
                        <td className="td text-right font-semibold"><Money paise={l.extended_paise} /></td>
                      </tr>
                    )
                  })}
                  <tr className="border-t border-slate-200 bg-slate-50 font-bold"><td className="td" colSpan={3}>Material</td><td className="td text-right"><Money paise={d.material_paise} /></td></tr>
                  <tr className="border-t border-slate-100 font-bold"><td className="td" colSpan={3}>Operations ({bom.operations.length})</td><td className="td text-right"><Money paise={d.operations_paise} /></td></tr>
                </tbody>
              </Table>
            </Card>
          </div>
        )}
      </QueryState>
    </div>
  )
}

function Stat({ label, paise, strong }: { label: string; paise: number; strong?: boolean }) {
  return (
    <div className={`card p-4 ${strong ? 'ring-1 ring-brand-500/20' : ''}`}>
      <div className="flex items-center gap-1.5 text-xs font-medium text-slate-500"><IndianRupee size={12} /> {label}</div>
      <div className="mt-1 text-lg font-bold"><Money paise={paise} /></div>
    </div>
  )
}
