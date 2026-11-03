'use client'

/**
 * Bank statements tab (bahi phase 4): import a CSV/XLSX statement with a bank preset (POST /v1/bank/statements/import,
 * auto-matches on import), list imports, work the statement lines — auto-match, manual match / unmatch / ignore, and
 * book an unmatched line (charges, interest, a direct deposit) as a voucher.
 */
import { useMemo, useRef, useState } from 'react'
import Link from 'next/link'
import { FileSpreadsheet, Link2, Plus, Unlink, Upload, Wand2, X } from 'lucide-react'
import { Badge, Button, Empty, ErrorBox, Field, Input, Modal, Money, QueryState, Select, Table, Toggle } from '@/components/ui'
import { api, errorMessage, post } from '@/lib/api'
import { fmtDate, todayIST, useGet, usePerm } from '@/lib/hooks'
import { PERMS } from '@/lib/rbac'
import type { Statement } from '@/lib/types'
import type { BankLine, BankLineStatus, StatementImport, StatementMapping, StatementPreset } from '@/lib/types.phase4'
import { addDays, presetRange, type DateRange } from '@/lib/ledger/util'
import { matchCandidates } from '@/lib/ledger/phase4'
import { AccountPicker, useAccounts, type LedgerTarget } from '../AccountPicker'
import { DateRangePicker } from '../DateRangePicker'
import { voucherHref } from '../StatementTable'
import { useAction } from '../useAction'

const INVALIDATE = ['/bahi/v1/bank/', '/bahi/v1/reports/brs', '/bahi/v1/bankbook', '/bahi/v1/vouchers']
const lineTone = (s: BankLineStatus) => (s === 'matched' ? 'green' : s === 'ignored' ? 'gray' : 'amber')

export function BankStatements() {
  const canDo = usePerm()
  const { accounts } = useAccounts()
  const banks = accounts.filter((a) => a.is_bank)
  const [bankId, setBankId] = useState('')
  const bank = bankId || banks[0]?.id || ''
  const [range, setRange] = useState<DateRange>(() => presetRange('this_month', todayIST()))
  const [status, setStatus] = useState<'' | BankLineStatus>('unmatched')
  const [importing, setImporting] = useState(false)
  const [matchLine, setMatchLine] = useState<BankLine | null>(null)
  const [bookLine, setBookLine] = useState<BankLine | null>(null)
  const lines = useGet<{ items: BankLine[] }>(bank ? '/bahi/v1/bank/statement-lines' : null, { bank_account_id: bank, from: range.from, to: range.to, status: status || undefined })
  const imports = useGet<{ items: StatementImport[] }>(bank ? '/bahi/v1/bank/statements' : null, { bank_account_id: bank })
  const canWrite = canDo(PERMS.ledgerWrite)
  const act = useAction<BankLine, { id: string; op: 'unmatch' | 'ignore' }>((i) => post(`/bahi/v1/bank/statement-lines/${i.id}/${i.op}`), { invalidate: INVALIDATE })
  const auto = useAction<{ matched: number }>(() => post('/bahi/v1/bank/auto-match', { bank_account_id: bank, from: range.from, to: range.to }), {
    invalidate: INVALIDATE,
  })
  const items = lines.data?.items ?? []

  if (banks.length === 0 && accounts.length > 0) return <Empty icon={FileSpreadsheet} title="No bank account" text="Create a bank account in the chart of accounts first." />

  return (
    <>
      <div className="mb-4 flex flex-wrap items-center gap-2">
        <Select value={bank} onChange={(e) => setBankId(e.target.value)} className="w-auto input-sm" aria-label="Bank account">{banks.map((b) => <option key={b.id} value={b.id}>{b.name}</option>)}</Select>
        <DateRangePicker value={range} onChange={setRange} />
        <Select value={status} onChange={(e) => setStatus(e.target.value as '' | BankLineStatus)} className="w-auto input-sm" aria-label="Line status">
          <option value="">All lines</option><option value="unmatched">Unmatched</option><option value="matched">Matched</option><option value="ignored">Ignored</option>
        </Select>
        {canWrite && (
          <div className="ml-auto flex gap-2">
            <Button size="sm" variant="secondary" icon={Wand2} loading={auto.isPending} disabled={!bank} onClick={() => auto.mutate()}>{auto.data ? `Auto-match (${auto.data.matched} matched)` : 'Auto-match'}</Button>
            <Button size="sm" icon={Upload} disabled={!bank} onClick={() => setImporting(true)}>Import statement</Button>
          </div>
        )}
      </div>

      <QueryState isLoading={lines.isLoading} error={lines.error} data={lines.data} retry={() => lines.refetch()}>
        {() => items.length === 0 ? (
          <Empty icon={FileSpreadsheet} title={status === 'unmatched' ? 'Nothing left to match' : 'No statement lines'} text="Import the bank’s statement (CSV or XLSX; SBI, HDFC, ICICI, Axis and Kotak formats are known). Lines are matched to your books automatically." />
        ) : (
          <Table>
            <thead className="bg-slate-50/60"><tr><th className="th">Date</th><th className="th">Description</th><th className="th text-right">Withdrawal</th><th className="th text-right">Deposit</th><th className="th text-right">Balance</th><th className="th">Status</th><th className="th" /></tr></thead>
            <tbody className="divide-y divide-slate-100">
              {items.map((l) => (
                <tr key={l.id} className="hover:bg-slate-50/60">
                  <td className="td whitespace-nowrap text-slate-600">{fmtDate(l.date)}</td>
                  <td className="td max-w-[320px]"><div className="truncate text-sm" title={l.description}>{l.description}</div>{l.reference && <div className="font-mono text-[11px] text-slate-400">{l.reference}</div>}</td>
                  <td className="td num text-right text-rose-700">{l.withdrawal_paise ? <Money paise={l.withdrawal_paise} /> : ''}</td>
                  <td className="td num text-right text-emerald-700">{l.deposit_paise ? <Money paise={l.deposit_paise} /> : ''}</td>
                  <td className="td num text-right text-slate-500">{l.balance_paise !== undefined && l.balance_paise !== null ? <Money paise={l.balance_paise} /> : ''}</td>
                  <td className="td"><Badge tone={lineTone(l.status)}>{l.status}</Badge>{l.voucher_id && <Link href={voucherHref(l.voucher_id)} className="link block text-[11px]">{l.voucher_number || 'voucher'}</Link>}{l.match_rule && <div className="text-[11px] text-slate-400">{l.match_rule}</div>}</td>
                  <td className="td">
                    {canWrite && (
                      <div className="flex justify-end gap-1">
                        {l.status === 'unmatched' && <>
                          <Button size="sm" variant="secondary" icon={Link2} onClick={() => setMatchLine(l)}>Match</Button>
                          <Button size="sm" variant="ghost" icon={Plus} onClick={() => setBookLine(l)}>Book</Button>
                          <Button size="sm" variant="ghost" icon={X} loading={act.isPending && act.variables?.id === l.id} onClick={() => act.mutate({ id: l.id, op: 'ignore' })}>Ignore</Button>
                        </>}
                        {l.status !== 'unmatched' && <Button size="sm" variant="ghost" icon={Unlink} loading={act.isPending && act.variables?.id === l.id} onClick={() => act.mutate({ id: l.id, op: 'unmatch' })}>{l.status === 'ignored' ? 'Restore' : 'Unmatch'}</Button>}
                      </div>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </Table>
        )}
      </QueryState>

      {(imports.data?.items ?? []).length > 0 && (
        <div className="mt-6">
          <h3 className="mb-2 text-sm font-bold">Imported statements</h3>
          <Table>
            <thead className="bg-slate-50/60"><tr><th className="th">File</th><th className="th">Format</th><th className="th">Period</th><th className="th text-right">Lines</th><th className="th text-right">Duplicates skipped</th><th className="th text-right">Matched</th></tr></thead>
            <tbody className="divide-y divide-slate-100">
              {(imports.data?.items ?? []).map((s) => (
                <tr key={s.id}><td className="td">{s.file_name || '—'}</td><td className="td uppercase text-slate-600">{s.preset}</td><td className="td text-slate-600">{fmtDate(s.from)} – {fmtDate(s.to)}</td><td className="td num text-right">{s.lines}</td><td className="td num text-right text-slate-500">{s.duplicates}</td><td className="td num text-right text-emerald-700">{s.matched}</td></tr>
              ))}
            </tbody>
          </Table>
        </div>
      )}

      {importing && <ImportStatementModal bankId={bank} bankName={banks.find((b) => b.id === bank)?.name ?? ''} onClose={() => setImporting(false)} />}
      {matchLine && <MatchModal line={matchLine} onClose={() => setMatchLine(null)} />}
      {bookLine && <BookLineModal line={bookLine} onClose={() => setBookLine(null)} />}
    </>
  )
}

const MAP_FIELDS: { key: keyof StatementMapping; label: string }[] = [
  { key: 'date', label: 'Date' }, { key: 'value_date', label: 'Value date' }, { key: 'description', label: 'Narration' }, { key: 'reference', label: 'Cheque / ref no' },
  { key: 'debit', label: 'Withdrawal (Dr)' }, { key: 'credit', label: 'Deposit (Cr)' }, { key: 'amount', label: 'Single amount column' }, { key: 'drcr', label: 'Dr/Cr column' },
  { key: 'balance', label: 'Balance' }, { key: 'date_format', label: 'Date format (e.g. dd/mm/yyyy)' },
]

function ImportStatementModal({ bankId, bankName, onClose }: { bankId: string; bankName: string; onClose: () => void }) {
  const presets = useGet<{ items: StatementPreset[] }>('/bahi/v1/bank/statement-presets')
  const [preset, setPreset] = useState('generic')
  const [file, setFile] = useState<File | null>(null)
  const [autoMatch, setAutoMatch] = useState(true)
  const [showMap, setShowMap] = useState(false)
  const [mapping, setMapping] = useState<StatementMapping>({})
  const [err, setErr] = useState<string | null>(null)
  const ref = useRef<HTMLInputElement>(null)
  const save = useAction<StatementImport, FormData>((fd) => api<StatementImport>('/bahi/v1/bank/statements/import', { method: 'POST', body: fd }), {
    invalidate: INVALIDATE, silent: true,
  })
  const submit = async () => {
    setErr(null)
    if (!file) return setErr('Choose the statement file.')
    if (/\.xls$/i.test(file.name)) return setErr('Old .xls statements are not read; open the file in Excel and save it as .xlsx or CSV.')
    const fd = new FormData()
    fd.set('file', file)
    fd.set('bank_account_id', bankId)
    fd.set('preset', preset)
    fd.set('auto_match', String(autoMatch))
    const m = Object.fromEntries(Object.entries(mapping).filter(([, v]) => v && v.trim()))
    if (Object.keys(m).length) fd.set('mapping', JSON.stringify(m))
    try { await save.mutateAsync(fd) } catch (e) { setErr(errorMessage(e)) }
  }
  const done = save.data
  return (
    <Modal open onClose={onClose} title={`Import statement · ${bankName}`}
      footer={done ? <Button onClick={onClose}>Done</Button> : <><Button variant="secondary" onClick={onClose}>Cancel</Button><Button icon={Upload} loading={save.isPending} onClick={submit}>Import</Button></>}>
      {done ? (
        <div className="space-y-2 text-sm">
          <p className="font-semibold text-emerald-700">Imported {done.lines} line{done.lines === 1 ? '' : 's'} ({fmtDate(done.from)} – {fmtDate(done.to)}).</p>
          <p className="text-slate-600">{done.duplicates} already imported earlier were skipped · {done.matched} matched to the books automatically.</p>
          <p className="text-xs text-slate-500">Work the rest from the Unmatched list: match by hand, book charges and interest, or ignore.</p>
        </div>
      ) : (
        <div className="space-y-4">
          <Field label="Statement file" hint="CSV (comma / semicolon / tab) or .xlsx, up to 10 MB. Re-importing the same file adds nothing.">
            <div className="flex items-center gap-2">
              <input ref={ref} type="file" className="sr-only" accept=".csv,.txt,.xlsx" onChange={(e) => setFile(e.target.files?.[0] ?? null)} />
              <Button variant="secondary" icon={FileSpreadsheet} onClick={() => ref.current?.click()}>{file ? 'Choose another' : 'Choose file'}</Button>
              <span className="truncate text-sm text-slate-600">{file?.name}</span>
            </div>
          </Field>
          <Field label="Bank format">
            <Select value={preset} onChange={(e) => setPreset(e.target.value)}>
              {(presets.data?.items ?? [{ code: 'generic', name: 'Generic' } as StatementPreset]).map((p) => <option key={p.code} value={p.code}>{p.name}</option>)}
            </Select>
          </Field>
          <Toggle checked={autoMatch} onChange={setAutoMatch} label="Match lines to the books after import" />
          <button type="button" className="link text-xs cursor-pointer" onClick={() => setShowMap((s) => !s)}>{showMap ? 'Hide' : 'Columns not recognised? Name them'}</button>
          {showMap && (
            <div className="grid gap-3 sm:grid-cols-2">
              {MAP_FIELDS.map((f) => <Field key={f.key} label={f.label}><Input className="input-sm" value={mapping[f.key] ?? ''} onChange={(e) => setMapping((m) => ({ ...m, [f.key]: e.target.value }))} placeholder="Header text in the file" /></Field>)}
            </div>
          )}
          {err && <ErrorBox message={err} />}
        </div>
      )}
    </Modal>
  )
}

function MatchModal({ line, onClose }: { line: BankLine; onClose: () => void }) {
  const from = addDays(line.date, -30)
  const to = addDays(line.date, 10)
  const book = useGet<Statement>('/bahi/v1/bankbook', { account_id: line.bank_account_id, from, to })
  const [showAll, setShowAll] = useState(false)
  const all = useMemo(() => (book.data?.lines ?? []).map((l) => ({ date: l.date, voucher_id: l.voucher_id, voucher_number: l.voucher_number, narration: l.narration, debit_paise: l.debit_paise, credit_paise: l.credit_paise })), [book.data])
  const candidates = useMemo(() => matchCandidates(line, all), [line, all])
  const amount = line.deposit_paise || line.withdrawal_paise
  const shown = showAll ? all.filter((b) => (line.deposit_paise ? b.debit_paise : b.credit_paise) === amount) : candidates
  const save = useAction<BankLine, string>((voucher_id) => post(`/bahi/v1/bank/statement-lines/${line.id}/match`, { voucher_id }), {
    invalidate: INVALIDATE, successMessage: 'Line matched', onSuccess: onClose,
  })
  return (
    <Modal open onClose={onClose} title="Match to a book entry" size="lg">
      <p className="mb-3 text-sm text-slate-600">{fmtDate(line.date)} · {line.description} · {line.deposit_paise ? 'deposit' : 'withdrawal'} of <Money paise={amount} className="font-semibold" />. Amounts must agree.</p>
      <QueryState isLoading={book.isLoading} error={book.error} data={book.data} retry={() => book.refetch()}>
        {() => shown.length === 0 ? <p className="rounded-xl bg-slate-50 px-4 py-3 text-sm text-slate-500">No unmatched posting of this amount near this date. Book the line as a new voucher instead.</p> : (
          <Table>
            <thead className="bg-slate-50/60"><tr><th className="th">Date</th><th className="th">Voucher</th><th className="th">Narration</th><th className="th text-right">Amount</th><th className="th" /></tr></thead>
            <tbody className="divide-y divide-slate-100">
              {shown.map((b, i) => (
                <tr key={`${b.voucher_id}-${i}`}>
                  <td className="td whitespace-nowrap">{fmtDate(b.date)}</td><td className="td font-mono text-xs">{b.voucher_number}</td>
                  <td className="td max-w-[240px] truncate text-slate-600">{b.narration}</td>
                  <td className="td num text-right"><Money paise={b.debit_paise || b.credit_paise} /></td>
                  <td className="td text-right"><Button size="sm" loading={save.isPending && save.variables === b.voucher_id} onClick={() => save.mutate(b.voucher_id)}>Match</Button></td>
                </tr>
              ))}
            </tbody>
          </Table>
        )}
      </QueryState>
      <button type="button" className="link mt-3 text-xs cursor-pointer" onClick={() => setShowAll((s) => !s)}>{showAll ? 'Only near the statement date' : 'Show every posting of this amount in the last 30 days'}</button>
    </Modal>
  )
}

function BookLineModal({ line, onClose }: { line: BankLine; onClose: () => void }) {
  const [target, setTarget] = useState<LedgerTarget | null>(null)
  const [narration, setNarration] = useState(line.description)
  const [err, setErr] = useState<string | null>(null)
  const save = useAction<unknown, void>(() => post(`/bahi/v1/bank/statement-lines/${line.id}/voucher`, {
    ...(target?.party_id ? { party_id: target.party_id } : { account_id: target?.account_id }), narration: narration.trim(),
  }), { invalidate: [...INVALIDATE, '/bahi/v1/parties', '/bahi/v1/reports', '/bahi/v1/daybook', '/bahi/v1/accounts'], successMessage: 'Voucher booked and matched', onSuccess: onClose })
  const deposit = line.deposit_paise > 0
  return (
    <Modal open onClose={onClose} title="Book this line"
      footer={<><Button variant="secondary" onClick={onClose}>Cancel</Button><Button loading={save.isPending} onClick={() => { if (!target) return setErr('Pick the other side of the entry.'); setErr(null); save.mutate() }}>Book &amp; match</Button></>}>
      <div className="space-y-4">
        <p className="text-sm text-slate-600">{deposit ? 'Deposit' : 'Withdrawal'} of <Money paise={deposit ? line.deposit_paise : line.withdrawal_paise} className="font-semibold" /> on {fmtDate(line.date)}: {deposit ? 'Dr Bank · Cr the account or party below' : 'Dr the account or party below · Cr Bank'}.</p>
        <Field label={deposit ? 'Received from / credited to' : 'Paid to / charged to'} hint="Bank charges, interest, an expense — or a party for a direct deposit or transfer">
          <AccountPicker value={target} onChange={setTarget} allowParties filter={(a) => !a.is_bank} placeholder="Account or party" />
        </Field>
        <Field label="Narration"><Input value={narration} onChange={(e) => setNarration(e.target.value)} /></Field>
        {err && <p className="text-sm text-rose-600">{err}</p>}
      </div>
    </Modal>
  )
}
