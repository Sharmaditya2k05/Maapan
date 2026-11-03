'use client'

/**
 * GSTR-3B (kar GET /v1/returns/gstr3b?period=MMYYYY[&gstin=]) for one registration:
 * 3.1, 3.2, 4 (A)–(D), 5, the Rule 88A set-off (`computation`), ITC from the books
 * (`itc_books`, incl. bills from suppliers without a GSTIN), books vs GSTR-2B (`itc_2b`),
 * warnings, and the opening electronic credit ledger editor (GET|PUT /v1/returns/itc-opening).
 */
import { useState, type ReactNode } from 'react'
import { Download, FileJson, Pencil, Save } from 'lucide-react'
import { Badge, Button, Card, ErrorBox, Field, Modal, MoneyInput, QueryState, useToast } from '@/components/ui'
import { api, errorMessage } from '@/lib/api'
import { downloadFile, useGet, usePerm } from '@/lib/hooks'
import { PERMS } from '@/lib/rbac'
import { STATES } from '@/lib/gst'
import { openingBody, openingToPaise, type OpeningPaise } from '@/lib/compliance'
import type { GSTR3B, ITCRow, TaxHead, Taxes, UnregSupply } from '@/lib/types.ledger'
import type { ITCOpening } from '@/lib/types.kar'
import { gstPeriod, monthLabel } from '@/lib/ledger/util'
import { SectionTitle, WarningList } from '../bits'
import { useAction } from '../useAction'
import { Rup, TAX_HEADS, TaxCells } from './shared'
import { ItcSummaryTable } from './Gstr2bView'
import { SetOffAction } from './SetOffAction'

const ROWS_31: { key: keyof GSTR3B['sup_details']; label: string }[] = [
  { key: 'osup_det', label: '(a) Outward taxable supplies (other than zero rated, nil rated and exempted)' },
  { key: 'osup_zero', label: '(b) Outward taxable supplies (zero rated)' },
  { key: 'osup_nil_exmp', label: '(c) Other outward supplies (nil rated, exempted)' },
  { key: 'isup_rev', label: '(d) Inward supplies (liable to reverse charge)' },
  { key: 'osup_nongst', label: '(e) Non-GST outward supplies' },
]

const AVL_LABEL: Record<string, string> = {
  IMPG: '(1) Import of goods', IMPS: '(2) Import of services', ISRC: '(3) Inward supplies liable to reverse charge',
  ISD: '(4) Inward supplies from ISD', OTH: '(5) All other ITC',
}
const REV_LABEL: Record<string, string> = { RUL: '(1) As per rules 38, 42 & 43 and section 17(5)', OTH: '(2) Others' }
const INELG_LABEL: Record<string, string> = { RUL: '(1) ITC reclaimed (reversed earlier under rule 37 etc.)', OTH: '(2) Ineligible ITC — section 16(4) and PoS rules' }
const ISUP_LABEL: Record<string, string> = { GST: 'From a supplier under composition scheme, exempt and nil rated supply', NONGST: 'Non-GST supply' }

const HEADS: { key: keyof Taxes; label: string }[] = [{ key: 'igst', label: 'IGST' }, { key: 'cgst', label: 'CGST' }, { key: 'sgst', label: 'SGST' }, { key: 'cess', label: 'Cess' }]
const ZERO: Taxes = { igst: 0, cgst: 0, sgst: 0, cess: 0 }
const sumTaxes = (t?: Taxes | null) => HEADS.reduce((a, h) => a + Math.round((t?.[h.key] ?? 0) * 100), 0) / 100
const HEAD_NAME: Record<string, string> = { igst: 'IGST', cgst: 'CGST', sgst: 'SGST', cess: 'Cess', IGST: 'IGST', CGST: 'CGST', SGST: 'SGST', CESS: 'Cess' }

/** Four tax-head cells (no taxable value) for ITC rows. */
function TaxOnly({ t, bold }: { t?: Partial<TaxHead> | null; bold?: boolean }) {
  const cls = `td num text-right ${bold ? 'font-bold' : ''}`
  return <><td className={cls}><Rup v={t?.iamt} /></td><td className={cls}><Rup v={t?.camt} /></td><td className={cls}><Rup v={t?.samt} /></td><td className={cls}><Rup v={t?.csamt} /></td></>
}
function TaxesCells({ t, bold, tone = '' }: { t?: Taxes | null; bold?: boolean; tone?: string }) {
  const cls = `td num text-right ${bold ? 'font-bold' : ''} ${tone}`
  return <>{HEADS.map((h) => <td key={h.key} className={cls}><Rup v={t?.[h.key]} /></td>)}</>
}
const HeadTh = () => <><th className="th text-right">IGST</th><th className="th text-right">CGST</th><th className="th text-right">SGST</th><th className="th text-right">Cess</th></>

function Tbl({ min = 640, head, children, foot }: { min?: number; head: ReactNode; children: ReactNode; foot?: ReactNode }) {
  return (
    <Card padded={false}><div className="overflow-x-auto"><table className="w-full" style={{ minWidth: min }}>
      <thead className="bg-slate-50/60"><tr>{head}</tr></thead>
      <tbody className="divide-y divide-slate-100">{children}</tbody>
      {foot && <tfoot className="bg-slate-50 font-bold">{foot}</tfoot>}
    </table></div></Card>
  )
}

function UnregTable({ title, rows }: { title: string; rows: UnregSupply[] | null | undefined }) {
  const list = rows ?? []
  return (
    <div className="min-w-0">
      <div className="mb-1 text-xs font-semibold text-slate-500">{title}</div>
      <Tbl min={360} head={<><th className="th">Place of supply</th><th className="th text-right">Taxable value</th><th className="th text-right">IGST</th></>}>
        {list.map((u) => <tr key={u.pos}><td className="td">{u.pos} <span className="text-xs text-slate-500">{STATES[u.pos]}</span></td><td className="td num text-right"><Rup v={u.txval} /></td><td className="td num text-right"><Rup v={u.iamt} /></td></tr>)}
        {list.length === 0 && <tr><td colSpan={3} className="px-4 py-4 text-center text-sm text-slate-400">None</td></tr>}
      </Tbl>
    </div>
  )
}

export function Gstr3bView({ period, gstin }: { period: string; gstin?: string }) {
  const canDo = usePerm()
  const fp = gstPeriod(period)
  const canFile = canDo(PERMS.gstFile)
  const query = { period: fp, gstin: gstin || undefined }
  const q = useGet<GSTR3B>('/kar/v1/returns/gstr3b', query)
  const name = (ext: string) => `GSTR3B_${q.data?.gstin || gstin || ''}_${fp}.${ext}`
  const dl = useAction<Blob, 'json' | 'csv'>((format) => api<Blob>('/kar/v1/returns/gstr3b', { method: 'GET', raw: true, query: { ...query, format } }), {
    onSuccess: (b, format) => downloadFile(name(format), b),
  })
  const [openingOpen, setOpeningOpen] = useState(false)

  return (
    <QueryState isLoading={q.isLoading} error={q.error} data={q.data} retry={() => q.refetch()}>
      {(g) => {
        const c = g.computation
        const books = g.itc_books
        const twoB = g.itc_2b
        const totalCash = sumTaxes(c.total_cash ?? c.cash_payable)
        const isup = g.inward_sup?.isup_details ?? []
        const inelg = g.itc_elg.itc_inelg ?? []
        const outRcm = g.summary?.outward_reverse_charge
        return (
          <div className="space-y-2">
            <div className="flex flex-wrap items-center justify-between gap-3">
              <div className="text-sm text-slate-600">GSTIN <span className="font-mono font-semibold">{g.gstin || '—'}</span> · period <span className="font-mono font-semibold">{g.ret_period}</span>{g.summary && <> · {g.summary.documents} documents{g.summary.excluded ? `, ${g.summary.excluded} of other GSTINs excluded` : ''}</>}</div>
              <div className="flex gap-2">
                <Button size="sm" variant="secondary" icon={FileJson} loading={dl.isPending && dl.variables === 'json'} onClick={() => dl.mutate('json')} disabled={!canFile} title={canFile ? 'GSTN offline-tool JSON' : 'Needs GST filing permission'}>Download JSON</Button>
                <Button size="sm" variant="secondary" icon={Download} loading={dl.isPending && dl.variables === 'csv'} onClick={() => dl.mutate('csv')} disabled={!canFile}>CSV</Button>
              </div>
            </div>
            <WarningList items={g.warnings ?? undefined} />

            <SectionTitle>3.1 Outward and reverse-charge inward supplies</SectionTitle>
            <Tbl min={720} head={<><th className="th">Nature of supplies</th>{TAX_HEADS.map((h) => <th key={h} className="th text-right">{h}</th>)}</>}>
              {ROWS_31.map((r) => <tr key={r.key}><td className="td">{r.label}</td><TaxCells {...g.sup_details[r.key]} /></tr>)}
            </Tbl>
            {outRcm && (outRcm.txval ?? 0) !== 0 && <p className="text-xs text-slate-500">Our own supplies taxed in the recipient&apos;s hands (reverse charge, taxable value <Rup v={outRcm.txval} />) are not in 3.1: the buyer pays that tax.</p>}

            <SectionTitle>3.2 Inter-state supplies to unregistered persons, composition dealers and UIN holders</SectionTitle>
            <div className="grid gap-3 lg:grid-cols-3">
              <UnregTable title="Unregistered persons" rows={g.inter_sup.unreg_details} />
              <UnregTable title="Composition taxable persons" rows={g.inter_sup.comp_details} />
              <UnregTable title="UIN holders" rows={g.inter_sup.uin_details} />
            </div>

            <SectionTitle>4 Eligible ITC</SectionTitle>
            <Tbl head={<><th className="th">Details</th><HeadTh /></>}
              foot={<tr><td className="td">(C) Net ITC available (A) − (B)</td><TaxOnly t={g.itc_elg.itc_net} bold /></tr>}>
              <tr className="bg-slate-50/40"><td className="td text-xs font-semibold uppercase tracking-wide text-slate-500" colSpan={5}>(A) ITC available (whether in full or part)</td></tr>
              {g.itc_elg.itc_avl.map((r: ITCRow) => <tr key={`avl-${r.ty}`}><td className="td pl-6">{AVL_LABEL[r.ty] ?? r.ty}</td><TaxOnly t={r} /></tr>)}
              <tr className="bg-slate-50/40"><td className="td text-xs font-semibold uppercase tracking-wide text-slate-500" colSpan={5}>(B) ITC reversed</td></tr>
              {g.itc_elg.itc_rev.map((r) => <tr key={`rev-${r.ty}`}><td className="td pl-6">{REV_LABEL[r.ty] ?? r.ty}</td><TaxOnly t={r} /></tr>)}
            </Tbl>
            <Tbl head={<><th className="th">(D) Other details</th><HeadTh /></>}>
              {inelg.map((r) => <tr key={`inelg-${r.ty}`}><td className="td">{INELG_LABEL[r.ty] ?? r.ty}</td><TaxOnly t={r} /></tr>)}
              {inelg.length === 0 && <tr><td colSpan={5} className="px-4 py-4 text-center text-sm text-slate-400">None</td></tr>}
            </Tbl>

            <SectionTitle>5 Exempt, nil-rated and non-GST inward supplies</SectionTitle>
            <Tbl min={480} head={<><th className="th">Nature of supplies</th><th className="th text-right">Inter-state</th><th className="th text-right">Intra-state</th></>}>
              {isup.map((r) => <tr key={r.ty}><td className="td">{ISUP_LABEL[r.ty] ?? r.ty}</td><td className="td num text-right"><Rup v={r.inter} /></td><td className="td num text-right"><Rup v={r.intra} /></td></tr>)}
              {isup.length === 0 && <tr><td colSpan={3} className="px-4 py-4 text-center text-sm text-slate-400">None</td></tr>}
            </Tbl>

            {books && (
              <>
                <SectionTitle>ITC from the purchase register</SectionTitle>
                <Tbl head={<><th className="th">Purchase bills ({books.purchase_bills}) and debit notes ({books.purchase_returns})</th><HeadTh /></>}
                  foot={<tr><td className="td">Net ITC claimed (Table 4(C))</td><TaxOnly t={books.net_claimed} bold /></tr>}>
                  <tr><td className="td">Eligible</td><TaxOnly t={books.eligible} /></tr>
                  <tr><td className="td">Blocked credit — section 17(5) <span className="text-xs text-slate-500">(shown in 4(A), reversed in 4(B)(1))</span></td><TaxOnly t={books.ineligible_17_5} /></tr>
                  <tr><td className="td">Ineligible — other</td><TaxOnly t={books.ineligible_other} /></tr>
                  <tr><td className="td">of which on reverse-charge purchases</td><TaxOnly t={books.reverse_charge} /></tr>
                  <tr className={books.no_supplier_gstin_bills > 0 ? 'bg-amber-50/60' : ''}>
                    <td className="td">Charged by suppliers without a GSTIN {books.no_supplier_gstin_bills > 0 && <Badge tone="amber" className="ml-1">{books.no_supplier_gstin_bills} bill{books.no_supplier_gstin_bills === 1 ? '' : 's'}</Badge>}
                      <div className="text-xs text-slate-500">Not claimable: unregistered suppliers cannot charge GST and the tax never reaches GSTR-2B. Correct those bills.</div></td>
                    <TaxOnly t={books.no_supplier_gstin} />
                  </tr>
                </Tbl>
              </>
            )}

            <SectionTitle>ITC — books vs GSTR-2B</SectionTitle>
            {twoB ? (
              <>
                <Tbl head={<><th className="th">Source</th><HeadTh /></>}>
                  <tr><td className="td">Books (Table 4(C))</td><TaxesCells t={twoB.books} /></tr>
                  <tr><td className="td">GSTR-2B (accepted + deemed accepted)</td><TaxesCells t={twoB.gstr2b} /></tr>
                  <tr className="font-semibold"><td className="td">Difference (books − 2B; positive = claiming more than 2B supports)</td>{HEADS.map((h) => <td key={h.key} className={`td num text-right ${twoB.difference[h.key] > 0.005 ? 'text-rose-700' : ''}`}><Rup v={twoB.difference[h.key]} /></td>)}</tr>
                </Tbl>
                {twoB.note && <p className="text-xs text-slate-500">{twoB.note}</p>}
                {twoB.detail && <ItcSummaryTable itc={twoB.detail} />}
              </>
            ) : <Card><p className="text-sm text-slate-500">No GSTR-2B imported for {monthLabel(period)}. Import it in the <span className="font-semibold">GSTR-2B &amp; IMS</span> tab to compare the ITC you claim with what suppliers reported.</p></Card>}

            <SectionTitle right={<Button size="sm" variant="ghost" icon={Pencil} onClick={() => setOpeningOpen(true)}>{canFile ? 'Opening ITC' : 'View opening ITC'}</Button>}>Set-off and tax payable (rule 88A)</SectionTitle>
            <Tbl min={760} head={<><th className="th">Head</th><th className="th text-right">Liability</th><th className="th text-right">Opening ITC</th><th className="th text-right">ITC this month</th><th className="th text-right">ITC available</th><th className="th text-right">ITC used</th><th className="th text-right">Cash (forward charge)</th><th className="th text-right">ITC carried forward</th></>}>
              {HEADS.map((h) => (
                <tr key={h.key}>
                  <td className="td font-medium">{h.label}</td>
                  <td className="td num text-right"><Rup v={c.liability[h.key]} /></td>
                  <td className="td num text-right text-slate-600"><Rup v={(c.opening_itc ?? ZERO)[h.key]} /></td>
                  <td className="td num text-right text-slate-600"><Rup v={(c.current_itc ?? ZERO)[h.key]} /></td>
                  <td className="td num text-right text-emerald-700"><Rup v={c.itc_available[h.key]} /></td>
                  <td className="td num text-right"><Rup v={c.itc_utilised[h.key]} /></td>
                  <td className={`td num text-right font-semibold ${c.cash_payable[h.key] > 0 ? 'text-rose-700' : ''}`}><Rup v={c.cash_payable[h.key]} /></td>
                  <td className="td num text-right text-slate-600"><Rup v={c.itc_carry_forward[h.key]} /></td>
                </tr>
              ))}
            </Tbl>
            <SetOffAction gstin={g.gstin} period={fp} cashTotal={totalCash} />
            <div className="grid gap-3 lg:grid-cols-2">
              <div className="min-w-0">
                <div className="mb-1 text-xs font-semibold text-slate-500">How the credit was used</div>
                <Tbl min={320} head={<><th className="th">Credit</th><th className="th">Against</th><th className="th text-right">Amount</th></>}>
                  {(c.utilisation ?? []).map((u, i) => <tr key={i}><td className="td">{HEAD_NAME[u.from] ?? u.from}</td><td className="td">{HEAD_NAME[u.to] ?? u.to}</td><td className="td num text-right"><Rup v={u.amount} /></td></tr>)}
                  {(c.utilisation ?? []).length === 0 && <tr><td colSpan={3} className="px-4 py-4 text-center text-sm text-slate-400">No credit used</td></tr>}
                </Tbl>
              </div>
              <div className="min-w-0">
                <div className="mb-1 text-xs font-semibold text-slate-500">Payment of tax (Table 6.1)</div>
                <Tbl min={520} head={<><th className="th">Head</th><th className="th text-right">Payable</th><th className="th text-right">Paid IGST</th><th className="th text-right">Paid CGST</th><th className="th text-right">Paid SGST</th><th className="th text-right">Paid cess</th><th className="th text-right">Cash</th></>}>
                  {(c.payable ?? []).map((p) => <tr key={p.head}><td className="td">{HEAD_NAME[p.head] ?? p.head}</td><td className="td num text-right"><Rup v={p.liability} /></td><td className="td num text-right"><Rup v={p.paid_igst} /></td><td className="td num text-right"><Rup v={p.paid_cgst} /></td><td className="td num text-right"><Rup v={p.paid_sgst} /></td><td className="td num text-right"><Rup v={p.paid_cess} /></td><td className="td num text-right font-semibold"><Rup v={p.cash} /></td></tr>)}
                  {(c.payable ?? []).length === 0 && <tr><td colSpan={7} className="px-4 py-4 text-center text-sm text-slate-400">Nothing payable</td></tr>}
                </Tbl>
              </div>
            </div>
            {c.reverse_charge_cash && sumTaxes(c.reverse_charge_cash) !== 0 && (
              <Tbl head={<><th className="th">Reverse charge (3.1(d)) — payable in cash only, section 49(4)</th><HeadTh /></>}>
                <tr><td className="td">Cash</td><TaxesCells t={c.reverse_charge_cash} /></tr>
              </Tbl>
            )}
            <Card padded={false}>
              <div className="flex items-center justify-between rounded-2xl bg-brand-950 px-5 py-4 text-white">
                <div><div className="text-sm text-brand-200">Total GST payable in cash</div><div className="text-[11px] text-brand-200/70">After set-off (IGST credit first; CGST and SGST never cross){c.reverse_charge_cash ? ', plus reverse-charge tax' : ''}.</div></div>
                <span className="num text-xl font-extrabold"><Rup v={totalCash} /></span>
              </div>
            </Card>
            {openingOpen && <OpeningModal gstin={g.gstin} period={fp} canEdit={canFile} onClose={() => setOpeningOpen(false)} />}
          </div>
        )
      }}
    </QueryState>
  )
}

/** Opening balance of the electronic credit ledger for the period (carried in from the portal). */
function OpeningModal({ gstin, period, canEdit, onClose }: { gstin: string; period: string; canEdit: boolean; onClose: () => void }) {
  const q = useGet<ITCOpening>('/kar/v1/returns/itc-opening', { gstin: gstin || undefined, period })
  return (
    <Modal open onClose={onClose} title={`Opening ITC · ${gstin || 'primary'} · ${period}`}>
      <QueryState isLoading={q.isLoading} error={q.error} data={q.data} retry={() => q.refetch()}>
        {(o) => <OpeningForm key={`${o.gstin}-${o.period}-${o.set ? 1 : 0}`} o={o} canEdit={canEdit} onClose={onClose} />}
      </QueryState>
    </Modal>
  )
}

function OpeningForm({ o, canEdit, onClose }: { o: ITCOpening; canEdit: boolean; onClose: () => void }) {
  const toast = useToast()
  const [d, setD] = useState<OpeningPaise>(() => openingToPaise(o))
  const save = useAction<ITCOpening, OpeningPaise>((p) => api<ITCOpening>('/kar/v1/returns/itc-opening', { method: 'PUT', body: openingBody(o.gstin, o.period, p) }), {
    invalidate: ['/kar/v1/returns'], silent: true, onSuccess: () => { toast('Opening ITC saved'); onClose() },
  })
  return (
    <form onSubmit={(e) => { e.preventDefault(); if (canEdit) save.mutate(d) }} noValidate className="space-y-4">
      <p className="text-sm text-slate-600">The balance of the electronic credit ledger on the portal at the start of {o.period.slice(0, 2)}/{o.period.slice(2)} (the previous return&apos;s closing balance). It is added to this month&apos;s ITC before set-off.{!o.set && ' Nothing saved yet — zero is used.'}</p>
      <div className="grid gap-4 sm:grid-cols-2">
        {HEADS.map((h) => <Field key={h.key} label={h.label}><MoneyInput value={d[h.key]} onChange={(v) => setD((p) => ({ ...p, [h.key]: Math.max(0, v) }))} disabled={!canEdit} /></Field>)}
      </div>
      {save.error && <ErrorBox message={errorMessage(save.error)} />}
      {!canEdit && <p className="text-xs text-slate-500">Changing it needs the GST filing permission.</p>}
      <div className="flex justify-end gap-2">
        <Button variant="secondary" onClick={onClose}>Close</Button>
        {canEdit && <Button type="submit" icon={Save} loading={save.isPending}>Save</Button>}
      </div>
    </form>
  )
}
