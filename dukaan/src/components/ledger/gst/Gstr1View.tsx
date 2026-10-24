'use client'

/** GSTR-1 (kar GET /v1/returns/gstr1?period=MMYYYY[&gstin=]) as section tables with totals, plus JSON / CSV downloads. */
import { useMemo } from 'react'
import { Download, FileJson, FileText } from 'lucide-react'
import { Badge, Button, Card, Empty, QueryState } from '@/components/ui'
import { api } from '@/lib/api'
import { downloadFile, useGet, usePerm } from '@/lib/hooks'
import { PERMS } from '@/lib/rbac'
import { STATES } from '@/lib/gst'
import type { GSTR1, Itm } from '@/lib/types.ledger'
import { gstPeriod } from '@/lib/ledger/util'
import { SectionTitle, WarningList } from '../bits'
import { useAction } from '../useAction'
import { addHeads, downloadJSON, Rup, TAX_HEADS, TaxCells, zeroHeads, type Heads } from './shared'

const sumItms = (itms: Itm[]): Heads => itms.reduce((a, i) => addHeads(a, i.itm_det), zeroHeads())

export function Gstr1View({ period, gstin }: { period: string; gstin?: string }) {
  const canDo = usePerm()
  const fp = gstPeriod(period)
  const q = useGet<GSTR1>('/kar/v1/returns/gstr1', { period: fp, gstin: gstin || undefined })
  const csv = useAction<Blob, void>(() => api<Blob>('/kar/v1/returns/gstr1', { method: 'GET', raw: true, query: { period: fp, gstin: gstin || undefined, format: 'csv' } }), {
    onSuccess: (b) => downloadFile(`GSTR1_${fp}.csv`, b),
  })
  // Warnings are ours, not part of the GSTN schema: strip them from the download and show them on screen.
  const json = () => { if (!q.data) return; const gstn: GSTR1 = { ...q.data }; delete gstn.warnings; downloadJSON(`GSTR1_${fp}.json`, gstn) }
  const canFile = canDo(PERMS.gstFile)

  const totals = useMemo(() => {
    if (!q.data) return null
    const b2b = q.data.b2b.flatMap((e) => e.inv).reduce((a, i) => addHeads(a, sumItms(i.itms)), zeroHeads())
    const b2cl = q.data.b2cl.flatMap((e) => e.inv).reduce((a, i) => addHeads(a, sumItms(i.itms)), zeroHeads())
    const b2cs = q.data.b2cs.reduce((a, r) => addHeads(a, r), zeroHeads())
    const cdnr = q.data.cdnr.flatMap((e) => e.nt).reduce((a, n) => addHeads(a, sumItms(n.itms)), zeroHeads())
    const out = addHeads(addHeads(addHeads(b2b, b2cl), b2cs), { txval: -cdnr.txval, iamt: -cdnr.iamt, camt: -cdnr.camt, samt: -cdnr.samt, csamt: -cdnr.csamt })
    return { b2b, b2cl, b2cs, cdnr, out }
  }, [q.data])

  return (
    <QueryState isLoading={q.isLoading} error={q.error} data={q.data} retry={() => q.refetch()}>
      {(g) => {
        const empty = g.b2b.length + g.b2cl.length + g.b2cs.length + g.cdnr.length === 0
        return (
          <div className="space-y-2">
            <div className="flex flex-wrap items-center justify-between gap-3">
              <div className="text-sm text-slate-600">GSTIN <span className="font-mono font-semibold">{g.gstin || '—'}</span> · period <span className="font-mono font-semibold">{g.fp}</span></div>
              <div className="flex gap-2">
                <Button size="sm" variant="secondary" icon={FileJson} onClick={json} disabled={!canFile} title={canFile ? 'GSTN offline-tool JSON' : 'Needs GST filing permission'}>Download JSON</Button>
                <Button size="sm" variant="secondary" icon={Download} loading={csv.isPending} onClick={() => csv.mutate()} disabled={!canFile}>CSV</Button>
              </div>
            </div>
            <WarningList items={g.warnings} />
            {empty ? <Card><Empty icon={FileText} title="No outward supplies in this period" text="Posted sales and credit notes dated in the month appear here." /></Card> : (
              <>
                {totals && (
                  <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
                    {([['Taxable value', totals.out.txval], ['IGST', totals.out.iamt], ['CGST', totals.out.camt], ['SGST', totals.out.samt]] as const).map(([l, v]) => (
                      <div key={l} className="card px-4 py-3"><div className="text-xs text-slate-500">{l} (net of CDNR)</div><div className="text-lg font-bold"><Rup v={v} /></div></div>
                    ))}
                  </div>
                )}

                <SectionTitle right={<Badge>{g.b2b.reduce((a, e) => a + e.inv.length, 0)} invoices</Badge>}>B2B — registered buyers</SectionTitle>
                <Card padded={false}><div className="overflow-x-auto"><table className="w-full min-w-[800px]">
                  <thead className="bg-slate-50/60"><tr><th className="th">Buyer GSTIN</th><th className="th">Invoice</th><th className="th">Date</th><th className="th">POS</th><th className="th text-right">Value</th>{TAX_HEADS.map((h) => <th key={h} className="th text-right">{h}</th>)}</tr></thead>
                  <tbody className="divide-y divide-slate-100">
                    {g.b2b.flatMap((e) => e.inv.map((i) => { const s = sumItms(i.itms); return (
                      <tr key={`${e.ctin}-${i.inum}`}><td className="td font-mono text-xs">{e.ctin}</td><td className="td font-mono text-xs">{i.inum}</td><td className="td">{i.idt}</td><td className="td">{i.pos} {STATES[i.pos] ? <span className="text-xs text-slate-500">{STATES[i.pos]}</span> : ''}{i.rchrg === 'Y' && <Badge tone="amber">RCM</Badge>}</td><td className="td num text-right"><Rup v={i.val} /></td><TaxCells {...s} /></tr>
                    ) }))}
                    {g.b2b.length === 0 && <tr><td colSpan={10} className="px-4 py-6 text-center text-sm text-slate-400">None</td></tr>}
                  </tbody>
                  {totals && g.b2b.length > 0 && <tfoot className="bg-slate-50 font-bold"><tr><td className="td" colSpan={4}>Total</td><td className="td num text-right"><Rup v={g.b2b.flatMap((e) => e.inv).reduce((a, i) => a + i.val, 0)} /></td><TaxCells {...totals.b2b} bold /></tr></tfoot>}
                </table></div></Card>

                <SectionTitle>B2CL — large inter-state invoices to unregistered buyers</SectionTitle>
                <Card padded={false}><div className="overflow-x-auto"><table className="w-full min-w-[720px]">
                  <thead className="bg-slate-50/60"><tr><th className="th">POS</th><th className="th">Invoice</th><th className="th">Date</th><th className="th text-right">Value</th>{TAX_HEADS.map((h) => <th key={h} className="th text-right">{h}</th>)}</tr></thead>
                  <tbody className="divide-y divide-slate-100">
                    {g.b2cl.flatMap((e) => e.inv.map((i) => { const s = sumItms(i.itms); return <tr key={`${e.pos}-${i.inum}`}><td className="td">{e.pos} <span className="text-xs text-slate-500">{STATES[e.pos]}</span></td><td className="td font-mono text-xs">{i.inum}</td><td className="td">{i.idt}</td><td className="td num text-right"><Rup v={i.val} /></td><TaxCells {...s} /></tr> }))}
                    {g.b2cl.length === 0 && <tr><td colSpan={9} className="px-4 py-6 text-center text-sm text-slate-400">None</td></tr>}
                  </tbody>
                  {totals && g.b2cl.length > 0 && <tfoot className="bg-slate-50 font-bold"><tr><td className="td" colSpan={3}>Total</td><td className="td num text-right"><Rup v={g.b2cl.flatMap((e) => e.inv).reduce((a, i) => a + i.val, 0)} /></td><TaxCells {...totals.b2cl} bold /></tr></tfoot>}
                </table></div></Card>

                <SectionTitle>B2CS — other unregistered buyers, by place of supply and rate</SectionTitle>
                <Card padded={false}><div className="overflow-x-auto"><table className="w-full min-w-[640px]">
                  <thead className="bg-slate-50/60"><tr><th className="th">Supply</th><th className="th">POS</th><th className="th text-right">Rate</th>{TAX_HEADS.map((h) => <th key={h} className="th text-right">{h}</th>)}</tr></thead>
                  <tbody className="divide-y divide-slate-100">
                    {g.b2cs.map((r, i) => <tr key={i}><td className="td">{r.sply_ty}</td><td className="td">{r.pos} <span className="text-xs text-slate-500">{STATES[r.pos]}</span></td><td className="td num text-right">{r.rt}%</td><TaxCells {...r} /></tr>)}
                    {g.b2cs.length === 0 && <tr><td colSpan={8} className="px-4 py-6 text-center text-sm text-slate-400">None</td></tr>}
                  </tbody>
                  {totals && g.b2cs.length > 0 && <tfoot className="bg-slate-50 font-bold"><tr><td className="td" colSpan={3}>Total</td><TaxCells {...totals.b2cs} bold /></tr></tfoot>}
                </table></div></Card>

                <SectionTitle>CDNR — credit / debit notes to registered buyers</SectionTitle>
                <Card padded={false}><div className="overflow-x-auto"><table className="w-full min-w-[800px]">
                  <thead className="bg-slate-50/60"><tr><th className="th">Buyer GSTIN</th><th className="th">Note</th><th className="th">Type</th><th className="th">Date</th><th className="th">POS</th><th className="th text-right">Value</th>{TAX_HEADS.map((h) => <th key={h} className="th text-right">{h}</th>)}</tr></thead>
                  <tbody className="divide-y divide-slate-100">
                    {g.cdnr.flatMap((e) => e.nt.map((n) => { const s = sumItms(n.itms); return <tr key={`${e.ctin}-${n.nt_num}`}><td className="td font-mono text-xs">{e.ctin}</td><td className="td font-mono text-xs">{n.nt_num}</td><td className="td">{n.ntty === 'C' ? 'Credit' : 'Debit'}</td><td className="td">{n.nt_dt}</td><td className="td">{n.pos}</td><td className="td num text-right"><Rup v={n.val} /></td><TaxCells {...s} /></tr> }))}
                    {g.cdnr.length === 0 && <tr><td colSpan={11} className="px-4 py-6 text-center text-sm text-slate-400">None</td></tr>}
                  </tbody>
                  {totals && g.cdnr.length > 0 && <tfoot className="bg-slate-50 font-bold"><tr><td className="td" colSpan={5}>Total</td><td className="td num text-right"><Rup v={g.cdnr.flatMap((e) => e.nt).reduce((a, n) => a + n.val, 0)} /></td><TaxCells {...totals.cdnr} bold /></tr></tfoot>}
                </table></div></Card>

                <SectionTitle>HSN summary</SectionTitle>
                <Card padded={false}><div className="overflow-x-auto"><table className="w-full min-w-[800px]">
                  <thead className="bg-slate-50/60"><tr><th className="th">HSN / SAC</th><th className="th">Description</th><th className="th">UQC</th><th className="th text-right">Qty</th><th className="th text-right">Rate</th>{TAX_HEADS.map((h) => <th key={h} className="th text-right">{h}</th>)}</tr></thead>
                  <tbody className="divide-y divide-slate-100">
                    {(g.hsn.data ?? []).map((h) => <tr key={h.num}><td className="td font-mono text-xs">{h.hsn_sc}</td><td className="td max-w-[16rem] truncate" title={h.desc}>{h.desc}</td><td className="td text-xs">{h.uqc}</td><td className="td num text-right">{h.qty}</td><td className="td num text-right">{h.rt}%</td><TaxCells {...h} /></tr>)}
                    {(g.hsn.data ?? []).length === 0 && <tr><td colSpan={10} className="px-4 py-6 text-center text-sm text-slate-400">None</td></tr>}
                  </tbody>
                  {(g.hsn.data ?? []).length > 0 && <tfoot className="bg-slate-50 font-bold"><tr><td className="td" colSpan={5}>Total</td><TaxCells {...(g.hsn.data ?? []).reduce((a, h) => addHeads(a, h), zeroHeads())} bold /></tr></tfoot>}
                </table></div></Card>

                <SectionTitle>Documents issued</SectionTitle>
                <Card padded={false}><div className="overflow-x-auto"><table className="w-full min-w-[560px]">
                  <thead className="bg-slate-50/60"><tr><th className="th">Document type</th><th className="th">From</th><th className="th">To</th><th className="th text-right">Total</th><th className="th text-right">Cancelled</th><th className="th text-right">Net issued</th></tr></thead>
                  <tbody className="divide-y divide-slate-100">
                    {(g.doc_issue.doc_det ?? []).flatMap((d) => d.docs.map((r) => <tr key={`${d.doc_num}-${r.num}`}><td className="td">{d.doc_num === 1 ? 'Invoices for outward supply' : d.doc_num === 5 ? 'Credit notes' : `Type ${d.doc_num}`}</td><td className="td font-mono text-xs">{r.from}</td><td className="td font-mono text-xs">{r.to}</td><td className="td num text-right">{r.totnum}</td><td className="td num text-right">{r.cancel}</td><td className="td num text-right font-semibold">{r.net_issue}</td></tr>))}
                    {(g.doc_issue.doc_det ?? []).length === 0 && <tr><td colSpan={6} className="px-4 py-6 text-center text-sm text-slate-400">None</td></tr>}
                  </tbody>
                </table></div></Card>
              </>
            )}
          </div>
        )
      }}
    </QueryState>
  )
}
