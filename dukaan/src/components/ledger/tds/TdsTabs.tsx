'use client'

/**
 * TDS / TCS (bahi phase 4): FY register + summary, compute & post a deduction, tag a party's PAN / section,
 * challans, the 26Q deductee annexure, the 206C(1H) buyer tracker and TDS settings.
 */
import { useMemo, useState } from 'react'
import Link from 'next/link'
import { Calculator, Download, Landmark, Percent, Plus, Save } from 'lucide-react'
import { Badge, Button, Card, Empty, ErrorBox, Field, Input, Modal, Money, MoneyInput, QueryState, Select, Table, Toggle } from '@/components/ui'
import { api, errorMessage, post } from '@/lib/api'
import { downloadFile, fmtDate, todayIST, useGet, usePerm } from '@/lib/hooks'
import { PERMS } from '@/lib/rbac'
import type { Party } from '@/lib/types'
import type { Challan, ChallanInput, Form26Q, Tcs1HReport, TdsCompute, TdsEntry, TdsRequest, TdsSection, TdsSettings, TdsSummary } from '@/lib/types.phase4'
import { bpPct, fyCode, fyCodeOptions, tdsQuarter } from '@/lib/ledger/phase4'
import { NoPermission } from '../bits'
import { useAccounts } from '../AccountPicker'
import { PartyPicker } from '../PartyPicker'
import { voucherHref } from '../StatementTable'
import { useAction } from '../useAction'

const INVALIDATE = ['/bahi/v1/tds', '/bahi/v1/tcs', '/bahi/v1/parties', '/bahi/v1/vouchers', '/bahi/v1/reports', '/bahi/v1/accounts']

export function useSections() {
  const q = useGet<{ items: TdsSection[] }>('/bahi/v1/tds/sections', undefined, { staleTime: 3600_000 })
  return q.data?.items ?? []
}

export function FySelect({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  return <Select value={value} onChange={(e) => onChange(e.target.value)} className="input-sm w-auto" aria-label="Financial year">{fyCodeOptions(todayIST(), 4).map((f) => <option key={f} value={f}>FY 20{f}</option>)}</Select>
}

/* ---------------- register ---------------- */

export function TdsRegister() {
  const canDo = usePerm()
  const sections = useSections()
  const [fy, setFy] = useState(fyCode(todayIST()))
  const [section, setSection] = useState('')
  const [direction, setDirection] = useState('payable')
  const [openOnly, setOpenOnly] = useState(false)
  const [picked, setPicked] = useState<string[]>([])
  const [challan, setChallan] = useState(false)
  const summary = useGet<TdsSummary>(canDo(PERMS.reportRead) ? '/bahi/v1/tds/summary' : null, { fy })
  const q = useGet<{ items: TdsEntry[] }>('/bahi/v1/tds/entries', { fy, section: section || undefined, direction: direction || undefined, open: openOnly || undefined })
  const items = q.data?.items ?? []
  const toggle = (id: string) => setPicked((p) => (p.includes(id) ? p.filter((x) => x !== id) : [...p, id]))
  const pickedEntries = items.filter((e) => picked.includes(e.id))

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-2">
        <FySelect value={fy} onChange={(v) => { setFy(v); setPicked([]) }} />
        <Select value={section} onChange={(e) => setSection(e.target.value)} className="input-sm w-auto" aria-label="Section"><option value="">All sections</option>{sections.map((s) => <option key={s.code} value={s.code}>{s.code} · {s.name}</option>)}</Select>
        <Select value={direction} onChange={(e) => setDirection(e.target.value)} className="input-sm w-auto" aria-label="Direction"><option value="payable">Deducted / collected by us</option><option value="receivable">Deducted from us</option><option value="">Both</option></Select>
        <Toggle checked={openOnly} onChange={setOpenOnly} label="Not yet deposited" />
        {canDo(PERMS.ledgerWrite) && <Button size="sm" icon={Landmark} className="ml-auto" disabled={pickedEntries.length === 0} onClick={() => setChallan(true)}>Deposit {pickedEntries.length || ''} selected</Button>}
      </div>
      {summary.data && (summary.data.lines ?? []).length > 0 && (
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
          {(summary.data.lines ?? []).map((l) => (
            <div key={`${l.kind}-${l.direction}-${l.section}`} className="card p-3 text-sm">
              <div className="flex items-center justify-between"><span className="font-bold">{l.section}</span><Badge tone={l.direction === 'payable' ? 'brand' : 'blue'}>{l.kind} · {l.direction}</Badge></div>
              <div className="mt-1 text-xs text-slate-500">{l.entries} entries on <Money paise={l.base_paise} /></div>
              <div className="mt-1 flex justify-between text-xs"><span>Tax <Money paise={l.tds_paise} className="font-semibold" /></span><span className={l.pending_paise ? 'text-rose-700' : 'text-emerald-700'}>pending <Money paise={l.pending_paise} /></span></div>
            </div>
          ))}
        </div>
      )}
      <Card padded={false}>
        <QueryState isLoading={q.isLoading} error={q.error} data={q.data} retry={() => q.refetch()}>
          {() => items.length === 0 ? <Empty icon={Percent} title="No TDS / TCS entries" text="Deductions posted here, on payments and (with auto 194Q) on purchase bills show up in this register." /> : (
            <Table>
              <thead className="bg-slate-50/60"><tr><th className="th w-8" /><th className="th">Date</th><th className="th">Party</th><th className="th">Section</th><th className="th text-right">Base</th><th className="th text-right">Rate</th><th className="th text-right">Tax</th><th className="th">Status</th></tr></thead>
              <tbody className="divide-y divide-slate-100">
                {items.map((e) => (
                  <tr key={e.id} className="hover:bg-slate-50/60">
                    <td className="td">{!e.challan_id && e.direction === 'payable' && e.tds_paise > 0 && <input type="checkbox" className="accent-brand-600" checked={picked.includes(e.id)} onChange={() => toggle(e.id)} aria-label="Select for challan" />}</td>
                    <td className="td whitespace-nowrap text-slate-600">{fmtDate(e.date)}</td>
                    <td className="td"><Link href={`/app/parties/${e.party_id}`} className="font-medium hover:underline">{e.party_name}</Link><div className="font-mono text-[11px] text-slate-400">{e.pan || 'PAN not available'}</div></td>
                    <td className="td"><Badge tone="gray">{e.section}</Badge> <span className="text-xs text-slate-500">{e.kind}</span></td>
                    <td className="td num text-right"><Money paise={e.base_paise} />{e.taxable_paise !== e.base_paise && <div className="text-[11px] text-slate-400">taxed <Money paise={e.taxable_paise} /></div>}</td>
                    <td className="td num text-right">{bpPct(e.rate_bp)}{e.higher_rate && <div className="text-[11px] text-rose-600">no PAN</div>}</td>
                    <td className="td num text-right font-semibold"><Money paise={e.tds_paise} />{e.voucher_id && <Link href={voucherHref(e.voucher_id)} className="link block text-[11px]">voucher</Link>}</td>
                    <td className="td">{e.challan_id ? <Badge tone="green">deposited</Badge> : e.tds_paise > 0 ? <Badge tone="amber">pending</Badge> : <Badge>below threshold</Badge>}</td>
                  </tr>
                ))}
              </tbody>
            </Table>
          )}
        </QueryState>
      </Card>
      {challan && <ChallanModal entries={pickedEntries} onClose={() => { setChallan(false); setPicked([]) }} />}
    </div>
  )
}

/* ---------------- compute & deduct ---------------- */

export function TdsDeduct() {
  const canDo = usePerm()
  const sections = useSections()
  const [party, setParty] = useState<Party | null>(null)
  const [section, setSection] = useState('')
  const [base, setBase] = useState(0)
  const [date, setDate] = useState(todayIST())
  const [direction, setDirection] = useState<'payable' | 'receivable'>('payable')
  const [override, setOverride] = useState(0)
  const [refVoucher, setRefVoucher] = useState('')
  const [narration, setNarration] = useState('')
  const [err, setErr] = useState<string | null>(null)
  const [tag, setTag] = useState(false)
  const body = (): TdsRequest => ({
    party_id: party?.id ?? '', section: section || undefined, base_paise: base, date, direction,
    ...(override > 0 ? { tds_paise: override } : {}), ...(refVoucher.trim() ? { ref_voucher_id: refVoucher.trim() } : {}), ...(narration.trim() ? { narration: narration.trim() } : {}),
  })
  const compute = useAction<TdsCompute, TdsRequest>((b) => post('/bahi/v1/tds/compute', { party_id: b.party_id, section: b.section, base_paise: b.base_paise, date: b.date }), { silent: true })
  const deduct = useAction<TdsEntry, TdsRequest>((b) => post('/bahi/v1/tds/deductions', b), {
    invalidate: INVALIDATE, successMessage: 'Deduction posted', onSuccess: () => { setBase(0); setOverride(0); setRefVoucher(''); setNarration(''); compute.reset() },
  })
  const check = () => {
    if (!party) return 'Pick the party.'
    if (base <= 0) return 'Enter the bill / payment amount (excluding GST).'
    if (!section && !party.tds_section) return 'Pick a section (the party has no default section).'
    return null
  }
  const run = (fn: typeof compute | typeof deduct) => { const p = check(); setErr(p); if (!p) fn.mutate(body()) }
  const r = compute.data?.result

  return (
    <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_340px]">
      <Card title="Compute and post a deduction">
        <div className="space-y-4">
          <div className="inline-flex rounded-xl bg-slate-100 p-1 text-sm font-semibold">
            {(['payable', 'receivable'] as const).map((d) => <button key={d} type="button" onClick={() => setDirection(d)} className={`rounded-lg px-3 py-1.5 cursor-pointer ${direction === d ? 'bg-white text-ink shadow-sm' : 'text-slate-500'}`}>{d === 'payable' ? 'We deduct (payable)' : 'Customer deducted from us'}</button>)}
          </div>
          <Field label="Party" hint={party ? `PAN ${party.pan || 'not on file (higher rate)'} · default section ${party.tds_section || 'none'}` : undefined}>
            <div className="flex gap-2"><div className="min-w-0 flex-1"><PartyPicker value={party} onChange={(p) => { setParty(p); compute.reset() }} /></div>{party && canDo(PERMS.partyManage) && <Button variant="secondary" onClick={() => setTag(true)}>PAN &amp; section</Button>}</div>
          </Field>
          <div className="grid gap-4 sm:grid-cols-3">
            <Field label="Section"><Select value={section} onChange={(e) => setSection(e.target.value)}><option value="">{party?.tds_section ? `Party default (${party.tds_section})` : 'Pick…'}</option>{sections.filter((s) => s.kind === 'tds').map((s) => <option key={s.code} value={s.code}>{s.code} · {s.name}</option>)}</Select></Field>
            <Field label="Amount (excl. GST)"><MoneyInput value={base} onChange={setBase} /></Field>
            <Field label="Date"><Input type="date" value={date} max={todayIST()} onChange={(e) => setDate(e.target.value)} /></Field>
          </div>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label={direction === 'receivable' ? 'TDS the customer deducted' : 'Override tax (optional)'} hint={direction === 'receivable' ? 'As shown on their Form 16A / 26AS' : 'Leave blank to use the computed amount'}><MoneyInput value={override} onChange={setOverride} /></Field>
            <Field label="Against voucher id (optional)" hint="The bill this deduction settles"><Input value={refVoucher} onChange={(e) => setRefVoucher(e.target.value)} className="font-mono text-xs" /></Field>
          </div>
          <Field label="Narration"><Input value={narration} onChange={(e) => setNarration(e.target.value)} placeholder="Optional" /></Field>
          {err && <p className="text-sm text-rose-600">{err}</p>}
          {compute.error && <ErrorBox message={errorMessage(compute.error)} />}
          <div className="flex flex-wrap justify-end gap-2">
            <Button variant="secondary" icon={Calculator} loading={compute.isPending} onClick={() => run(compute)}>Preview</Button>
            {canDo(PERMS.ledgerWrite) && <Button icon={Save} loading={deduct.isPending} onClick={() => run(deduct)}>Post deduction</Button>}
          </div>
        </div>
      </Card>
      <Card title="Result">
        {!r ? <p className="text-sm text-slate-500">Preview shows the section’s threshold test against this party’s FY history and the tax to deduct (rounded to the rupee).</p> : (
          <dl className="space-y-1.5 text-sm">
            <div className="flex justify-between"><dt className="text-slate-500">Section</dt><dd className="font-semibold">{r.section} · {compute.data?.section.name}</dd></div>
            <div className="flex justify-between"><dt className="text-slate-500">FY aggregate incl. this</dt><dd><Money paise={r.aggregate_paise} /></dd></div>
            <div className="flex justify-between"><dt className="text-slate-500">Taxed on</dt><dd><Money paise={r.taxable_paise} /></dd></div>
            <div className="flex justify-between"><dt className="text-slate-500">Rate</dt><dd>{bpPct(r.rate_bp)}{r.higher_rate_no_pan && <Badge tone="red" className="ml-1">no PAN</Badge>}</dd></div>
            <div className="flex justify-between border-t border-slate-100 pt-2 text-base font-bold"><dt>Tax</dt><dd><Money paise={r.tds_paise} /></dd></div>
            <p className={`mt-2 rounded-xl px-3 py-2 text-xs ${r.applicable ? 'bg-amber-50 text-amber-800' : 'bg-slate-50 text-slate-600'}`}>{r.reason || (r.applicable ? 'Deduction applies.' : 'Within the threshold: nothing to deduct.')}</p>
          </dl>
        )}
      </Card>
      {tag && party && <PartyTdsModal party={party} sections={sections} onClose={(p) => { setTag(false); if (p) setParty(p) }} />}
    </div>
  )
}

function PartyTdsModal({ party, sections, onClose }: { party: Party; sections: TdsSection[]; onClose: (p?: Party) => void }) {
  const [pan, setPan] = useState(party.pan ?? '')
  const [section, setSection] = useState(party.tds_section ?? '')
  const [dtype, setDtype] = useState(party.deductee_type ?? '')
  const save = useAction<Party, void>(() => api<Party>(`/bahi/v1/parties/${party.id}`, { method: 'PATCH', body: { pan: pan.trim().toUpperCase(), tds_section: section, deductee_type: dtype } }), {
    invalidate: ['/bahi/v1/parties'], successMessage: 'Party updated', onSuccess: (p) => onClose(p),
  })
  return (
    <Modal open onClose={() => onClose()} title={`TDS details · ${party.name}`} footer={<><Button variant="secondary" onClick={() => onClose()}>Cancel</Button><Button loading={save.isPending} onClick={() => save.mutate()}>Save</Button></>}>
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="PAN" hint="Without a PAN the higher rate applies (206AA)"><Input value={pan} onChange={(e) => setPan(e.target.value)} className="font-mono uppercase" placeholder="AAAPA1234A" /></Field>
        <Field label="Deductee type" hint="Blank = from the PAN’s 4th letter"><Select value={dtype} onChange={(e) => setDtype(e.target.value)}><option value="">Auto</option>{['individual', 'huf', 'company', 'firm', 'other'].map((t) => <option key={t} value={t}>{t}</option>)}</Select></Field>
        <Field label="Default TDS section" className="sm:col-span-2" hint="Used for this party’s bills and payments; tag suppliers 194Q for auto-deduction on purchases">
          <Select value={section} onChange={(e) => setSection(e.target.value)}><option value="">None</option>{sections.filter((s) => s.kind === 'tds').map((s) => <option key={s.code} value={s.code}>{s.code} · {s.name}</option>)}</Select>
        </Field>
      </div>
    </Modal>
  )
}

/* ---------------- challans ---------------- */

export function TdsChallans() {
  const canDo = usePerm()
  const q = useGet<{ items: Challan[] }>('/bahi/v1/tds/challans')
  const [open, setOpen] = useState(false)
  const items = q.data?.items ?? []
  return (
    <Card padded={false} title="Challans (ITNS 281)" actions={canDo(PERMS.ledgerWrite) ? <Button size="sm" icon={Plus} onClick={() => setOpen(true)}>Deposit a month</Button> : undefined}>
      <QueryState isLoading={q.isLoading} error={q.error} data={q.data} retry={() => q.refetch()}>
        {() => items.length === 0 ? <Empty icon={Landmark} title="No challans yet" text="Deposit the month’s TDS by the 7th; the challan posts Dr TDS Payable · Cr Bank and links the entries." /> : (
          <Table>
            <thead className="bg-slate-50/60"><tr><th className="th">Paid on</th><th className="th">Kind</th><th className="th">BSR / serial</th><th className="th text-right">Entries</th><th className="th text-right">Tax</th><th className="th text-right">Interest</th><th className="th" /></tr></thead>
            <tbody className="divide-y divide-slate-100">
              {items.map((c) => (
                <tr key={c.id}><td className="td">{fmtDate(c.paid_on)}</td><td className="td uppercase">{c.kind}</td><td className="td font-mono text-xs">{c.bsr_code} / {c.challan_no}</td><td className="td num text-right">{c.entries}</td><td className="td num text-right font-semibold"><Money paise={c.amount_paise} /></td><td className="td num text-right">{c.interest_paise ? <Money paise={c.interest_paise} /> : '—'}</td><td className="td text-right">{c.voucher_id && <Link href={voucherHref(c.voucher_id)} className="link text-xs">voucher</Link>}</td></tr>
              ))}
            </tbody>
          </Table>
        )}
      </QueryState>
      {open && <ChallanModal entries={[]} onClose={() => setOpen(false)} />}
    </Card>
  )
}

function ChallanModal({ entries, onClose }: { entries: TdsEntry[]; onClose: () => void }) {
  const { accounts } = useAccounts()
  const banks = accounts.filter((a) => a.is_bank)
  const today = todayIST()
  const [kind, setKind] = useState<'tds' | 'tcs'>(entries[0]?.kind ?? 'tds')
  const [bsr, setBsr] = useState('')
  const [no, setNo] = useState('')
  const [paidOn, setPaidOn] = useState(today)
  const [bankId, setBankId] = useState('')
  const [month, setMonth] = useState(today.slice(0, 7))
  const [section, setSection] = useState('')
  const [interest, setInterest] = useState(0)
  const [err, setErr] = useState<string | null>(null)
  const bank = bankId || banks[0]?.id || ''
  const total = entries.reduce((a, e) => a + e.tds_paise, 0)
  const save = useAction<Challan, ChallanInput>((b) => post('/bahi/v1/tds/challans', b), { invalidate: INVALIDATE, successMessage: 'Challan recorded', onSuccess: onClose })
  const submit = () => {
    if (!/^\d{7}$/.test(bsr.trim())) return setErr('BSR code is 7 digits.')
    if (!no.trim()) return setErr('Enter the challan serial number.')
    if (!bank) return setErr('Pick the bank account it was paid from.')
    setErr(null)
    save.mutate({ kind, bsr_code: bsr.trim(), challan_no: no.trim(), paid_on: paidOn, bank_account_id: bank, interest_paise: interest || undefined,
      ...(entries.length ? { entry_ids: entries.map((e) => e.id) } : { month, section: section || undefined }) })
  }
  return (
    <Modal open onClose={onClose} title="Record a challan" footer={<><Button variant="secondary" onClick={onClose}>Cancel</Button><Button loading={save.isPending} onClick={submit}>Save challan</Button></>}>
      <div className="space-y-4">
        {entries.length > 0
          ? <p className="rounded-xl bg-slate-50 px-4 py-3 text-sm">{entries.length} selected entries · tax <Money paise={total} className="font-semibold" /></p>
          : <div className="grid gap-4 sm:grid-cols-3">
              <Field label="Kind"><Select value={kind} onChange={(e) => setKind(e.target.value as 'tds' | 'tcs')}><option value="tds">TDS</option><option value="tcs">TCS</option></Select></Field>
              <Field label="Month deducted"><Input type="month" value={month} onChange={(e) => setMonth(e.target.value)} /></Field>
              <Field label="Section (optional)"><Input value={section} onChange={(e) => setSection(e.target.value)} placeholder="All" /></Field>
            </div>}
        <div className="grid gap-4 sm:grid-cols-3">
          <Field label="BSR code"><Input value={bsr} onChange={(e) => setBsr(e.target.value.replace(/\D/g, ''))} className="font-mono" inputMode="numeric" maxLength={7} /></Field>
          <Field label="Challan serial no"><Input value={no} onChange={(e) => setNo(e.target.value)} className="font-mono" /></Field>
          <Field label="Paid on"><Input type="date" value={paidOn} max={today} onChange={(e) => setPaidOn(e.target.value)} /></Field>
        </div>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Paid from"><Select value={bank} onChange={(e) => setBankId(e.target.value)}>{banks.map((b) => <option key={b.id} value={b.id}>{b.name}</option>)}</Select></Field>
          <Field label="Interest (201(1A))" hint="Optional; booked to Interest on Loans"><MoneyInput value={interest} onChange={setInterest} /></Field>
        </div>
        {err && <p className="text-sm text-rose-600">{err}</p>}
      </div>
    </Modal>
  )
}

/* ---------------- 26Q ---------------- */

const Q26_HEAD = ['Sr', 'Section', 'Deductee', 'PAN', 'Name', 'Paid / credited', 'Amount', 'Rate %', 'TDS', 'Deposited', 'Deducted on', 'BSR', 'Challan date', 'Challan no', 'Reason', 'TAN']

export function Tds26Q() {
  const today = todayIST()
  const [fy, setFy] = useState(fyCode(today))
  const [quarter, setQuarter] = useState(String(tdsQuarter(today)))
  const query = { fy, quarter }
  const q = useGet<Form26Q>('/bahi/v1/tds/26q', query)
  const csv = useAction<Blob>(() => api<Blob>('/bahi/v1/tds/26q', { method: 'GET', raw: true, query: { ...query, format: 'csv' } }), { onSuccess: (b) => downloadFile(`26Q_${fy}_Q${quarter}.csv`, b) })
  return (
    <Card padded={false} title="Form 26Q — deductee annexure" actions={<Button size="sm" variant="secondary" icon={Download} loading={csv.isPending} onClick={() => csv.mutate()}>CSV for the RPU</Button>}>
      <div className="flex flex-wrap items-center gap-2 p-4">
        <FySelect value={fy} onChange={setFy} />
        <Select value={quarter} onChange={(e) => setQuarter(e.target.value)} className="input-sm w-auto" aria-label="Quarter">{[['1', 'Q1 Apr–Jun'], ['2', 'Q2 Jul–Sep'], ['3', 'Q3 Oct–Dec'], ['4', 'Q4 Jan–Mar']].map(([v, l]) => <option key={v} value={v}>{l}</option>)}</Select>
        {q.data && <span className="text-xs text-slate-500">TAN {q.data.tan || <span className="text-rose-600">not set (TDS settings)</span>} · {fmtDate(q.data.from)} – {fmtDate(q.data.to)}</span>}
      </div>
      <QueryState isLoading={q.isLoading} error={q.error} data={q.data} retry={() => q.refetch()}>
        {(d) => (
          <>
            {d.entries_without_challan > 0 && <div className="mx-4 mb-3 rounded-xl bg-amber-50 px-4 py-2 text-sm text-amber-800">{d.entries_without_challan} deduction{d.entries_without_challan === 1 ? ' has' : 's have'} no challan yet — deposit before filing.</div>}
            {(d.rows ?? []).length === 0 ? <Empty icon={Percent} title="No deductions in this quarter" /> : (
              <div className="overflow-x-auto"><table className="w-full min-w-[1100px] text-xs">
                <thead className="bg-slate-50/60"><tr>{Q26_HEAD.map((h) => <th key={h} className="th whitespace-nowrap">{h}</th>)}</tr></thead>
                <tbody className="divide-y divide-slate-100">{(d.rows ?? []).map((r, i) => <tr key={i}>{r.map((c, j) => <td key={j} className="whitespace-nowrap px-3 py-1.5">{c}</td>)}</tr>)}</tbody>
              </table></div>
            )}
          </>
        )}
      </QueryState>
    </Card>
  )
}

/* ---------------- 206C(1H) ---------------- */

export function Tcs1H() {
  const [fy, setFy] = useState(fyCode(todayIST()))
  const q = useGet<Tcs1HReport>('/bahi/v1/tcs/206c1h', { fy })
  return (
    <Card padded={false} title="206C(1H) — buyers against the ₹50 lakh threshold">
      <div className="p-4"><FySelect value={fy} onChange={setFy} /></div>
      <QueryState isLoading={q.isLoading} error={q.error} data={q.data} retry={() => q.refetch()}>
        {(d) => (
          <>
            {(d.note || !d.applicable) && <div className="mx-4 mb-3 rounded-xl bg-slate-50 px-4 py-2 text-sm text-slate-600">{d.note || 'The section does not apply in this period; TCS already booked on invoices is still shown.'}</div>}
            {(d.rows ?? []).length === 0 ? <Empty icon={Percent} title="No buyers to track" /> : (
              <Table>
                <thead className="bg-slate-50/60"><tr><th className="th">Buyer</th><th className="th text-right">Sales</th><th className="th text-right">Receipts</th><th className="th text-right">Above threshold</th><th className="th text-right">TCS collected</th><th className="th text-right">TCS due</th></tr></thead>
                <tbody className="divide-y divide-slate-100">
                  {(d.rows ?? []).map((r) => (
                    <tr key={r.party_id}><td className="td"><Link href={`/app/parties/${r.party_id}`} className="font-medium hover:underline">{r.party_name}</Link>{r.threshold_crossed && <Badge tone="amber" className="ml-2">crossed</Badge>}<div className="font-mono text-[11px] text-slate-400">{r.pan || 'no PAN'}</div></td>
                      <td className="td num text-right"><Money paise={r.sales_paise} /></td><td className="td num text-right"><Money paise={r.receipts_paise} /></td><td className="td num text-right"><Money paise={r.receipts_above_threshold_paise} /></td><td className="td num text-right"><Money paise={r.tcs_collected_paise} /></td><td className="td num text-right font-semibold"><Money paise={r.tcs_due_paise} /></td></tr>
                  ))}
                </tbody>
              </Table>
            )}
          </>
        )}
      </QueryState>
    </Card>
  )
}

/* ---------------- settings ---------------- */

export function TdsSettingsCard() {
  const canDo = usePerm()
  const q = useGet<TdsSettings>('/bahi/v1/settings/tds')
  const [draft, setDraft] = useState<TdsSettings | null>(null)
  const d = draft ?? q.data
  const save = useAction<TdsSettings, TdsSettings>((b) => api('/bahi/v1/settings/tds', { method: 'PUT', body: b }), { invalidate: ['/bahi/v1/settings/tds'], successMessage: 'TDS settings saved', onSuccess: () => setDraft(null) })
  const canEdit = canDo(PERMS.settingsManage)
  const tanOk = useMemo(() => !d?.tan || /^[A-Z]{4}\d{5}[A-Z]$/.test(d.tan), [d?.tan])
  if (!canDo(PERMS.ledgerRead)) return <NoPermission what="TDS settings" />
  return (
    <Card title="Deductor">
      <QueryState isLoading={q.isLoading} error={q.error} data={d ?? undefined} retry={() => q.refetch()}>
        {(s) => (
          <div className="max-w-xl space-y-4">
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label="TAN" error={tanOk ? undefined : 'TAN looks like ABCD12345E'}><Input value={s.tan} disabled={!canEdit} onChange={(e) => setDraft({ ...s, tan: e.target.value.toUpperCase() })} className="font-mono" /></Field>
              <Field label="Deductor name"><Input value={s.deductor_name} disabled={!canEdit} onChange={(e) => setDraft({ ...s, deductor_name: e.target.value })} /></Field>
            </div>
            <Toggle checked={s.auto_194q} onChange={(v) => canEdit && setDraft({ ...s, auto_194q: v })} label="Deduct 194Q automatically on purchase bills from suppliers tagged 194Q" />
            {canEdit && <div className="flex justify-end"><Button icon={Save} disabled={!draft || !tanOk} loading={save.isPending} onClick={() => draft && save.mutate(draft)}>Save</Button></div>}
          </div>
        )}
      </QueryState>
    </Card>
  )
}
