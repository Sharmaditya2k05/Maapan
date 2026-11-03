'use client'

/**
 * GSTR-1 (kar GET /v1/returns/gstr1?period=MMYYYY[&gstin=]) for one registration: B2B, B2CL, B2CS,
 * CDNR, CDNUR, exports, nil / exempt (Table 8), HSN summary split B2B / B2C (Table 12), documents
 * issued (Table 13). Downloads: the clean upload JSON (GET /v1/returns/gstr1/export) and CSV — the
 * whole review sheet or one section in the offline-tool template (format=csv&section=).
 */
import { useMemo, type ReactNode } from 'react'
import { Download, FileJson, FileText } from 'lucide-react'
import { Badge, Button, Card, Empty, QueryState } from '@/components/ui'
import { api } from '@/lib/api'
import { downloadFile, useGet, usePerm } from '@/lib/hooks'
import { PERMS } from '@/lib/rbac'
import { STATES } from '@/lib/gst'
import type { GSTR1, HSNRow, Itm } from '@/lib/types.ledger'
import { gstPeriod } from '@/lib/ledger/util'
import { SectionTitle, WarningList } from '../bits'
import { useAction } from '../useAction'
import { addHeads, Rup, TAX_HEADS, TaxCells, zeroHeads, type Heads } from './shared'

const sumItms = (itms: Itm[]): Heads => itms.reduce((a, i) => addHeads(a, i.itm_det), zeroHeads())
const NIL_LABEL: Record<string, string> = { INTRB2B: 'Inter-state to registered', INTRAB2B: 'Intra-state to registered', INTRB2C: 'Inter-state to unregistered', INTRAB2C: 'Intra-state to unregistered' }
const Pos = ({ pos }: { pos?: string }) => pos ? <>{pos} <span className="text-xs text-slate-500">{STATES[pos] ?? ''}</span></> : <>—</>
const None = ({ cols }: { cols: number }) => <tr><td colSpan={cols} className="px-4 py-6 text-center text-sm text-slate-400">None</td></tr>

type Section = 'b2b' | 'b2cl' | 'b2cs' | 'cdnr' | 'cdnur' | 'exp' | 'nil' | 'hsn_b2b' | 'hsn_b2c' | 'docs'

export function Gstr1View({ period, gstin }: { period: string; gstin?: string }) {
  const canDo = usePerm()
  const fp = gstPeriod(period)
  const canFile = canDo(PERMS.gstFile)
  const q = useGet<GSTR1>('/kar/v1/returns/gstr1', { period: fp, gstin: gstin || undefined })
  const name = (ext: string, section?: string) => `GSTR1_${q.data?.gstin || gstin || ''}_${fp}${section ? `_${section}` : ''}.${ext}`
  const exportJson = useAction<Blob, void>(() => api<Blob>('/kar/v1/returns/gstr1/export', { method: 'GET', raw: true, query: { period: fp, gstin: gstin || undefined } }), {
    onSuccess: (b) => downloadFile(name('json'), b),
  })
  const csv = useAction<Blob, Section | undefined>((section) => api<Blob>('/kar/v1/returns/gstr1/export', { method: 'GET', raw: true, query: { period: fp, gstin: gstin || undefined, format: 'csv', section } }), {
    onSuccess: (b, section) => downloadFile(name('csv', section), b),
  })
  const csvButton = (section: Section, n: number) => canFile && n > 0
    ? <Button size="sm" variant="ghost" icon={Download} loading={csv.isPending && csv.variables === section} onClick={() => csv.mutate(section)} title="This section in the offline-tool template">CSV</Button>
    : null

  const totals = useMemo(() => {
    const g = q.data
    if (!g) return null
    const inv = <T extends { itms: Itm[] }>(xs: T[]) => xs.reduce((a, i) => addHeads(a, sumItms(i.itms)), zeroHeads())
    return {
      b2b: inv(g.b2b.flatMap((e) => e.inv)), b2cl: inv(g.b2cl.flatMap((e) => e.inv)), b2cs: g.b2cs.reduce((a, r) => addHeads(a, r), zeroHeads()),
      cdnr: inv(g.cdnr.flatMap((e) => e.nt)), cdnur: inv(g.cdnur ?? []),
      exp: (g.exp ?? []).flatMap((e) => e.inv).flatMap((i) => i.itms).reduce((a, i) => addHeads(a, { txval: i.txval, iamt: i.iamt, csamt: i.csamt }), zeroHeads()),
    }
  }, [q.data])

  return (
    <QueryState isLoading={q.isLoading} error={q.error} data={q.data} retry={() => q.refetch()}>
      {(g) => {
        const cdnur = g.cdnur ?? []
        const exp = g.exp ?? []
        const nil = g.nil?.inv ?? []
        const hsnB2B = g.hsn.hsn_b2b ?? []
        const hsnB2C = g.hsn.hsn_b2c ?? []
        const docs = g.doc_issue.doc_det ?? []
        const s = g.summary
        const empty = g.b2b.length + g.b2cl.length + g.b2cs.length + g.cdnr.length + cdnur.length + exp.length + nil.length === 0
        return (
          <div className="space-y-2">
            <div className="flex flex-wrap items-center justify-between gap-3">
              <div className="text-sm text-slate-600">GSTIN <span className="font-mono font-semibold">{g.gstin || '—'}</span> · period <span className="font-mono font-semibold">{g.fp}</span>{s && <> · {s.documents} documents{s.cancelled ? `, ${s.cancelled} cancelled` : ''}{s.excluded ? `, ${s.excluded} of other GSTINs excluded` : ''}</>}</div>
              <div className="flex gap-2">
                <Button size="sm" variant="secondary" icon={FileJson} loading={exportJson.isPending} onClick={() => exportJson.mutate()} disabled={!canFile} title={canFile ? 'Clean offline-tool / portal upload JSON' : 'Needs GST filing permission'}>Upload JSON</Button>
                <Button size="sm" variant="secondary" icon={Download} loading={csv.isPending && csv.variables === undefined} onClick={() => csv.mutate(undefined)} disabled={!canFile} title="Every section in one review sheet">CSV</Button>
              </div>
            </div>
            <WarningList items={g.warnings ?? undefined} />
            {empty ? <Card><Empty icon={FileText} title="No outward supplies in this period" text="Posted sales and credit notes of this GSTIN dated in the month appear here." /></Card> : (
              <>
                {s && (
                  <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
                    {([['IGST', s.liability.igst], ['CGST', s.liability.cgst], ['SGST', s.liability.sgst], ['Cess', s.liability.cess]] as const).map(([l, v]) => (
                      <div key={l} className="card px-4 py-3"><div className="text-xs text-slate-500">{l} liability (net of notes)</div><div className="text-lg font-bold"><Rup v={v} /></div></div>
                    ))}
                  </div>
                )}

                <Block title="4A · B2B — registered buyers (incl. RCM, SEZ, deemed exports)" right={<><Badge>{g.b2b.reduce((a, e) => a + e.inv.length, 0)} invoices</Badge>{csvButton('b2b', g.b2b.length)}</>}
                  head={<><th className="th">Buyer GSTIN</th><th className="th">Invoice</th><th className="th">Date</th><th className="th">POS</th><th className="th">Type</th><th className="th text-right">Value</th></>}
                  foot={totals && g.b2b.length > 0 && <tr><td className="td" colSpan={5}>Total</td><td className="td num text-right"><Rup v={g.b2b.flatMap((e) => e.inv).reduce((a, i) => a + i.val, 0)} /></td><TaxCells {...totals.b2b} bold /></tr>}>
                  {g.b2b.flatMap((e) => e.inv.map((i) => <tr key={`${e.ctin}-${i.inum}`}><td className="td font-mono text-xs">{e.ctin}</td><td className="td font-mono text-xs">{i.inum}</td><td className="td">{i.idt}</td><td className="td"><Pos pos={i.pos} /></td><td className="td text-xs">{i.inv_typ}{i.rchrg === 'Y' && <Badge tone="amber" className="ml-1">RCM</Badge>}</td><td className="td num text-right"><Rup v={i.val} /></td><TaxCells {...sumItms(i.itms)} /></tr>))}
                  {g.b2b.length === 0 && <None cols={11} />}
                </Block>

                <Block title="5 · B2CL — large inter-state invoices to unregistered buyers" right={csvButton('b2cl', g.b2cl.length)}
                  head={<><th className="th">POS</th><th className="th">Invoice</th><th className="th">Date</th><th className="th text-right">Value</th></>}
                  foot={totals && g.b2cl.length > 0 && <tr><td className="td" colSpan={3}>Total</td><td className="td num text-right"><Rup v={g.b2cl.flatMap((e) => e.inv).reduce((a, i) => a + i.val, 0)} /></td><TaxCells {...totals.b2cl} bold /></tr>}>
                  {g.b2cl.flatMap((e) => e.inv.map((i) => <tr key={`${e.pos}-${i.inum}`}><td className="td"><Pos pos={e.pos} /></td><td className="td font-mono text-xs">{i.inum}</td><td className="td">{i.idt}</td><td className="td num text-right"><Rup v={i.val} /></td><TaxCells {...sumItms(i.itms)} /></tr>))}
                  {g.b2cl.length === 0 && <None cols={9} />}
                </Block>

                <Block title="7 · B2CS — other unregistered buyers, by place of supply and rate" right={csvButton('b2cs', g.b2cs.length)}
                  head={<><th className="th">Supply</th><th className="th">POS</th><th className="th text-right">Rate</th></>}
                  foot={totals && g.b2cs.length > 0 && <tr><td className="td" colSpan={3}>Total</td><TaxCells {...totals.b2cs} bold /></tr>}>
                  {g.b2cs.map((r, i) => <tr key={i}><td className="td">{r.sply_ty}</td><td className="td"><Pos pos={r.pos} /></td><td className="td num text-right">{r.rt}%</td><TaxCells {...r} /></tr>)}
                  {g.b2cs.length === 0 && <None cols={8} />}
                </Block>

                <Block title="9B · CDNR — credit / debit notes to registered buyers" right={csvButton('cdnr', g.cdnr.length)}
                  head={<><th className="th">Buyer GSTIN</th><th className="th">Note</th><th className="th">Type</th><th className="th">Date</th><th className="th">POS</th><th className="th text-right">Value</th></>}
                  foot={totals && g.cdnr.length > 0 && <tr><td className="td" colSpan={5}>Total</td><td className="td num text-right"><Rup v={g.cdnr.flatMap((e) => e.nt).reduce((a, n) => a + n.val, 0)} /></td><TaxCells {...totals.cdnr} bold /></tr>}>
                  {g.cdnr.flatMap((e) => e.nt.map((n) => <tr key={`${e.ctin}-${n.nt_num}`}><td className="td font-mono text-xs">{e.ctin}</td><td className="td font-mono text-xs">{n.nt_num}</td><td className="td">{n.ntty === 'C' ? 'Credit' : 'Debit'}</td><td className="td">{n.nt_dt}</td><td className="td"><Pos pos={n.pos} /></td><td className="td num text-right"><Rup v={n.val} /></td><TaxCells {...sumItms(n.itms)} /></tr>))}
                  {g.cdnr.length === 0 && <None cols={11} />}
                </Block>

                <Block title="9B · CDNUR — notes to unregistered buyers (B2CL / exports)" right={csvButton('cdnur', cdnur.length)}
                  head={<><th className="th">Type</th><th className="th">Note</th><th className="th">Note type</th><th className="th">Date</th><th className="th">POS</th><th className="th text-right">Value</th></>}
                  foot={totals && cdnur.length > 0 && <tr><td className="td" colSpan={5}>Total</td><td className="td num text-right"><Rup v={cdnur.reduce((a, n) => a + n.val, 0)} /></td><TaxCells {...totals.cdnur} bold /></tr>}>
                  {cdnur.map((n) => <tr key={n.nt_num}><td className="td text-xs">{n.typ}</td><td className="td font-mono text-xs">{n.nt_num}</td><td className="td">{n.ntty === 'C' ? 'Credit' : 'Debit'}</td><td className="td">{n.nt_dt}</td><td className="td"><Pos pos={n.pos} /></td><td className="td num text-right"><Rup v={n.val} /></td><TaxCells {...sumItms(n.itms)} /></tr>)}
                  {cdnur.length === 0 && <None cols={11} />}
                </Block>

                <Block title="6A · Exports (with / without payment of tax)" right={csvButton('exp', exp.length)}
                  head={<><th className="th">Type</th><th className="th">Invoice</th><th className="th">Date</th><th className="th">Shipping bill</th><th className="th text-right">Value</th></>}
                  foot={totals && exp.length > 0 && <tr><td className="td" colSpan={4}>Total</td><td className="td num text-right"><Rup v={exp.flatMap((e) => e.inv).reduce((a, i) => a + i.val, 0)} /></td><TaxCells {...totals.exp} bold /></tr>}>
                  {exp.flatMap((e) => e.inv.map((i) => { const h = i.itms.reduce((a, x) => addHeads(a, { txval: x.txval, iamt: x.iamt, csamt: x.csamt }), zeroHeads()); return <tr key={`${e.exp_typ}-${i.inum}`}><td className="td"><Badge tone={e.exp_typ === 'WPAY' ? 'blue' : 'gray'}>{e.exp_typ}</Badge></td><td className="td font-mono text-xs">{i.inum}</td><td className="td">{i.idt}</td><td className="td text-xs">{i.sbnum ? `${i.sbnum} · ${i.sbdt ?? ''} · ${i.sbpcode ?? ''}` : <span className="text-amber-700">not entered</span>}</td><td className="td num text-right"><Rup v={i.val} /></td><TaxCells {...h} /></tr> }))}
                  {exp.length === 0 && <None cols={10} />}
                </Block>

                <SectionTitle right={csvButton('nil', nil.length)}>8 · Nil-rated, exempt and non-GST supplies</SectionTitle>
                <Card padded={false}><div className="overflow-x-auto"><table className="w-full min-w-[560px]">
                  <thead className="bg-slate-50/60"><tr><th className="th">Supply</th><th className="th text-right">Nil rated</th><th className="th text-right">Exempted</th><th className="th text-right">Non-GST</th></tr></thead>
                  <tbody className="divide-y divide-slate-100">
                    {nil.map((r) => <tr key={r.sply_ty}><td className="td">{NIL_LABEL[r.sply_ty] ?? r.sply_ty}</td><td className="td num text-right"><Rup v={r.nil_amt} /></td><td className="td num text-right"><Rup v={r.expt_amt} /></td><td className="td num text-right"><Rup v={r.ngsup_amt} /></td></tr>)}
                    {nil.length === 0 && <None cols={4} />}
                  </tbody>
                </table></div></Card>

                <HsnTable title="12 · HSN summary — B2B supplies" rows={hsnB2B} right={csvButton('hsn_b2b', hsnB2B.length)} />
                <HsnTable title="12 · HSN summary — B2C supplies" rows={hsnB2C} right={csvButton('hsn_b2c', hsnB2C.length)} />

                <SectionTitle right={csvButton('docs', docs.length)}>13 · Documents issued</SectionTitle>
                <Card padded={false}><div className="overflow-x-auto"><table className="w-full min-w-[560px]">
                  <thead className="bg-slate-50/60"><tr><th className="th">Document type</th><th className="th">From</th><th className="th">To</th><th className="th text-right">Total</th><th className="th text-right">Cancelled</th><th className="th text-right">Net issued</th></tr></thead>
                  <tbody className="divide-y divide-slate-100">
                    {docs.flatMap((d) => d.docs.map((r) => <tr key={`${d.doc_num}-${r.num}`}><td className="td">{d.doc_typ || (d.doc_num === 1 ? 'Invoices for outward supply' : d.doc_num === 5 ? 'Credit notes' : `Type ${d.doc_num}`)}</td><td className="td font-mono text-xs">{r.from}</td><td className="td font-mono text-xs">{r.to}</td><td className="td num text-right">{r.totnum}</td><td className="td num text-right">{r.cancel}</td><td className="td num text-right font-semibold">{r.net_issue}</td></tr>))}
                    {docs.length === 0 && <None cols={6} />}
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

/** A GSTR-1 section table: own columns + the five tax-head columns. */
function Block({ title, right, head, foot, children }: { title: string; right?: ReactNode; head: ReactNode; foot?: ReactNode; children: ReactNode }) {
  return (
    <>
      <SectionTitle right={right ? <div className="flex items-center gap-2">{right}</div> : undefined}>{title}</SectionTitle>
      <Card padded={false}><div className="overflow-x-auto"><table className="w-full min-w-[800px]">
        <thead className="bg-slate-50/60"><tr>{head}{TAX_HEADS.map((h) => <th key={h} className="th text-right">{h}</th>)}</tr></thead>
        <tbody className="divide-y divide-slate-100">{children}</tbody>
        {foot && <tfoot className="bg-slate-50 font-bold">{foot}</tfoot>}
      </table></div></Card>
    </>
  )
}

function HsnTable({ title, rows, right }: { title: string; rows: HSNRow[]; right?: ReactNode }) {
  return (
    <Block title={title} right={right}
      head={<><th className="th">HSN / SAC</th><th className="th">Description</th><th className="th">UQC</th><th className="th text-right">Qty</th><th className="th text-right">Rate</th></>}
      foot={rows.length > 0 && <tr><td className="td" colSpan={5}>Total</td><TaxCells {...rows.reduce((a, h) => addHeads(a, h), zeroHeads())} bold /></tr>}>
      {rows.map((h) => <tr key={`${h.num}-${h.hsn_sc}-${h.rt}`}><td className="td font-mono text-xs">{h.hsn_sc}</td><td className="td max-w-[16rem] truncate" title={h.desc}>{h.desc}</td><td className="td text-xs">{h.uqc}</td><td className="td num text-right">{h.qty}</td><td className="td num text-right">{h.rt}%</td><TaxCells {...h} /></tr>)}
      {rows.length === 0 && <None cols={10} />}
    </Block>
  )
}
