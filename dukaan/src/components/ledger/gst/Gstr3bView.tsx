'use client'

/** GSTR-3B (kar GET /v1/returns/gstr3b): 3.1, 3.2, 4 (ITC) and the set-off / cash payable computation. */
import { Download, FileJson } from 'lucide-react'
import { Button, Card, QueryState } from '@/components/ui'
import { api } from '@/lib/api'
import { downloadFile, useGet, usePerm } from '@/lib/hooks'
import { PERMS } from '@/lib/rbac'
import { STATES } from '@/lib/gst'
import type { GSTR3B, Taxes } from '@/lib/types.ledger'
import { gstPeriod } from '@/lib/ledger/util'
import { SectionTitle } from '../bits'
import { useAction } from '../useAction'
import { downloadJSON, Rup, TAX_HEADS, TaxCells } from './shared'

const ROWS_31: { key: keyof GSTR3B['sup_details']; label: string }[] = [
  { key: 'osup_det', label: '(a) Outward taxable supplies (other than zero rated, nil rated and exempted)' },
  { key: 'osup_zero', label: '(b) Outward taxable supplies (zero rated)' },
  { key: 'osup_nil_exmp', label: '(c) Other outward supplies (nil rated, exempted)' },
  { key: 'isup_rev', label: '(d) Inward supplies (liable to reverse charge)' },
  { key: 'osup_nongst', label: '(e) Non-GST outward supplies' },
]

const HEADS: { key: keyof Taxes; label: string }[] = [{ key: 'igst', label: 'IGST' }, { key: 'cgst', label: 'CGST' }, { key: 'sgst', label: 'SGST' }, { key: 'cess', label: 'Cess' }]

export function Gstr3bView({ period, gstin }: { period: string; gstin?: string }) {
  const canDo = usePerm()
  const fp = gstPeriod(period)
  const q = useGet<GSTR3B>('/kar/v1/returns/gstr3b', { period: fp, gstin: gstin || undefined })
  const csv = useAction<Blob, void>(() => api<Blob>('/kar/v1/returns/gstr3b', { method: 'GET', raw: true, query: { period: fp, gstin: gstin || undefined, format: 'csv' } }), {
    onSuccess: (b) => downloadFile(`GSTR3B_${fp}.csv`, b),
  })
  const canFile = canDo(PERMS.gstFile)
  return (
    <QueryState isLoading={q.isLoading} error={q.error} data={q.data} retry={() => q.refetch()}>
      {(g) => {
        const c = g.computation
        const totalCash = HEADS.reduce((a, h) => a + c.cash_payable[h.key], 0)
        return (
          <div className="space-y-2">
            <div className="flex flex-wrap items-center justify-between gap-3">
              <div className="text-sm text-slate-600">GSTIN <span className="font-mono font-semibold">{g.gstin || '—'}</span> · period <span className="font-mono font-semibold">{g.ret_period}</span></div>
              <div className="flex gap-2">
                <Button size="sm" variant="secondary" icon={FileJson} onClick={() => downloadJSON(`GSTR3B_${fp}.json`, g)} disabled={!canFile} title={canFile ? 'GSTN offline-tool JSON' : 'Needs GST filing permission'}>Download JSON</Button>
                <Button size="sm" variant="secondary" icon={Download} loading={csv.isPending} onClick={() => csv.mutate()} disabled={!canFile}>CSV</Button>
              </div>
            </div>

            <SectionTitle>3.1 Outward and reverse-charge inward supplies</SectionTitle>
            <Card padded={false}><div className="overflow-x-auto"><table className="w-full min-w-[720px]">
              <thead className="bg-slate-50/60"><tr><th className="th">Nature of supplies</th>{TAX_HEADS.map((h) => <th key={h} className="th text-right">{h}</th>)}</tr></thead>
              <tbody className="divide-y divide-slate-100">{ROWS_31.map((r) => <tr key={r.key}><td className="td">{r.label}</td><TaxCells {...g.sup_details[r.key]} /></tr>)}</tbody>
            </table></div></Card>

            <SectionTitle>3.2 Inter-state supplies to unregistered persons, by place of supply</SectionTitle>
            <Card padded={false}><div className="overflow-x-auto"><table className="w-full min-w-[480px]">
              <thead className="bg-slate-50/60"><tr><th className="th">Place of supply</th><th className="th text-right">Taxable value</th><th className="th text-right">IGST</th></tr></thead>
              <tbody className="divide-y divide-slate-100">
                {g.inter_sup.unreg_details.map((u) => <tr key={u.pos}><td className="td">{u.pos} <span className="text-xs text-slate-500">{STATES[u.pos]}</span></td><td className="td num text-right"><Rup v={u.txval} /></td><td className="td num text-right"><Rup v={u.iamt} /></td></tr>)}
                {g.inter_sup.unreg_details.length === 0 && <tr><td colSpan={3} className="px-4 py-6 text-center text-sm text-slate-400">None</td></tr>}
              </tbody>
            </table></div></Card>

            <SectionTitle>4 Eligible ITC</SectionTitle>
            <Card padded={false}><div className="overflow-x-auto"><table className="w-full min-w-[640px]">
              <thead className="bg-slate-50/60"><tr><th className="th">Details</th><th className="th text-right">IGST</th><th className="th text-right">CGST</th><th className="th text-right">SGST</th><th className="th text-right">Cess</th></tr></thead>
              <tbody className="divide-y divide-slate-100">
                {g.itc_elg.itc_avl.map((r) => <tr key={`avl-${r.ty}`}><td className="td">(A) ITC available — {r.ty === 'OTH' ? 'all other ITC (purchases net of returns)' : r.ty}</td><td className="td num text-right"><Rup v={r.iamt} /></td><td className="td num text-right"><Rup v={r.camt} /></td><td className="td num text-right"><Rup v={r.samt} /></td><td className="td num text-right"><Rup v={r.csamt} /></td></tr>)}
                {g.itc_elg.itc_rev.map((r) => <tr key={`rev-${r.ty}`}><td className="td">(B) ITC reversed — {r.ty}</td><td className="td num text-right"><Rup v={r.iamt} /></td><td className="td num text-right"><Rup v={r.camt} /></td><td className="td num text-right"><Rup v={r.samt} /></td><td className="td num text-right"><Rup v={r.csamt} /></td></tr>)}
                {g.itc_elg.itc_rev.length === 0 && <tr><td className="td text-slate-400">(B) ITC reversed</td><td className="td num text-right text-slate-400" colSpan={4}>None</td></tr>}
              </tbody>
              <tfoot className="bg-slate-50 font-bold"><tr><td className="td">(C) Net ITC available</td><td className="td num text-right"><Rup v={g.itc_elg.itc_net.iamt} /></td><td className="td num text-right"><Rup v={g.itc_elg.itc_net.camt} /></td><td className="td num text-right"><Rup v={g.itc_elg.itc_net.samt} /></td><td className="td num text-right"><Rup v={g.itc_elg.itc_net.csamt} /></td></tr></tfoot>
            </table></div></Card>

            <SectionTitle>Set-off and tax payable</SectionTitle>
            <Card padded={false}>
              <div className="overflow-x-auto"><table className="w-full min-w-[640px]">
                <thead className="bg-slate-50/60"><tr><th className="th">Head</th><th className="th text-right">Liability</th><th className="th text-right">ITC available</th><th className="th text-right">ITC used</th><th className="th text-right">Pay in cash</th><th className="th text-right">ITC carried forward</th></tr></thead>
                <tbody className="divide-y divide-slate-100">
                  {HEADS.map((h) => (
                    <tr key={h.key}><td className="td font-medium">{h.label}</td><td className="td num text-right"><Rup v={c.liability[h.key]} /></td><td className="td num text-right text-emerald-700"><Rup v={c.itc_available[h.key]} /></td><td className="td num text-right"><Rup v={c.itc_utilised[h.key]} /></td><td className={`td num text-right font-semibold ${c.cash_payable[h.key] > 0 ? 'text-rose-700' : ''}`}><Rup v={c.cash_payable[h.key]} /></td><td className="td num text-right text-slate-600"><Rup v={c.itc_carry_forward[h.key]} /></td></tr>
                  ))}
                </tbody>
              </table></div>
              <div className="flex items-center justify-between bg-brand-950 px-5 py-4 text-white">
                <div><div className="text-sm text-brand-200">Total GST payable in cash after set-off</div><div className="text-[11px] text-brand-200/70">IGST credit first, then CGST and SGST; CGST and SGST never cross (rule 88A).</div></div>
                <span className="num text-xl font-extrabold"><Rup v={totalCash} /></span>
              </div>
            </Card>
            <p className="text-xs text-slate-500">Table 5 (exempt / nil-rated / non-GST inward supplies) is not computed by kar yet; fill it on the portal from your purchase register.</p>
          </div>
        )
      }}
    </QueryState>
  )
}
