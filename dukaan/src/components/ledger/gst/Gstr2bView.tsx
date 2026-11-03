'use client'

/**
 * GSTR-2B & IMS (kar /v1/returns/gstr2b, /v1/returns/ims) for one registration and period:
 * import the portal's 2B JSON or fetch it through the GSP, the match against purchase bills
 * (counts by status), every document with the bill it matched, IMS accept / reject / pending,
 * accept-all-matched, the ITC summary, and the IMS upload / reconciliation CSV exports.
 */
import { useMemo, useRef, useState } from 'react'
import Link from 'next/link'
import { Check, CheckCheck, CloudDownload, Download, FileJson, FileText, Hourglass, Upload, X } from 'lucide-react'
import { Badge, Button, Card, Empty, ErrorBox, Field, Input, Modal, QueryState, SearchInput, Table, useToast, type Tone } from '@/components/ui'
import { api, errorMessage } from '@/lib/api'
import { downloadFile, fmtDate, useDebounced, useGet, usePerm } from '@/lib/hooks'
import { PERMS } from '@/lib/rbac'
import { filterTwoB, groupTwoB, imsActionOf, taxOf, TWOB_STATUS_LABEL, TWOB_STATUSES, twoBCsv } from '@/lib/compliance'
import type { IMSAction, ITCSummary, ReconResult, Reconciliation, TwoBStatus } from '@/lib/types.kar'
import { gstPeriod, monthLabel } from '@/lib/ledger/util'
import { SectionTitle, WarningList } from '../bits'
import { useAction } from '../useAction'
import { Rup } from './shared'

const STATUS_TONE: Record<TwoBStatus, Tone> = { matched: 'green', mismatch: 'amber', missing_in_books: 'red', missing_in_2b: 'gray' }
const ACTION_TONE: Record<IMSAction | 'none', Tone> = { accept: 'green', reject: 'red', pending: 'amber', none: 'gray' }
const ACTION_LABEL: Record<IMSAction | 'none', string> = { accept: 'Accepted', reject: 'Rejected', pending: 'Pending', none: 'No action (deemed accepted)' }
const STATUS_HINT: Record<TwoBStatus, string> = {
  matched: 'Supplier reported it and a purchase bill agrees (₹1 tolerance).',
  mismatch: 'A purchase bill was found but some fields differ.',
  missing_in_books: 'In GSTR-2B, no purchase bill in the books.',
  missing_in_2b: 'Purchase bill the supplier has not reported (yet).',
}

const PATH = '/kar/v1/returns/gstr2b'

export function Gstr2bView({ period, gstin }: { period: string; gstin?: string }) {
  const canDo = usePerm()
  const toast = useToast()
  const canFile = canDo(PERMS.gstFile)
  const fp = gstPeriod(period)
  const query = { gstin: gstin || undefined, period: fp }
  const q = useGet<Reconciliation>(PATH, query)
  const [status, setStatus] = useState<TwoBStatus | ''>('')
  const [action, setAction] = useState<IMSAction | 'none' | ''>('')
  const [search, setSearch] = useState('')
  const dq = useDebounced(search)
  const [reject, setReject] = useState<ReconResult | null>(null)
  const [importErr, setImportErr] = useState<unknown>(null)
  const file = useRef<HTMLInputElement>(null)

  const items = useMemo(() => q.data?.items ?? [], [q.data])
  const groups = useMemo(() => groupTwoB(items), [items])
  const rows = useMemo(() => filterTwoB(items, status, dq, action), [items, status, dq, action])
  const inv = ['/kar/v1/returns']

  const importJson = useAction<unknown, unknown>((body) => api(`${PATH}/import`, { method: 'POST', query, body }), {
    invalidate: inv, silent: true, onSuccess: () => { setImportErr(null); toast(`GSTR-2B for ${monthLabel(period)} imported`) },
  })
  const fetchGsp = useAction<unknown, void>(() => api(`${PATH}/fetch`, { method: 'POST', query }), {
    invalidate: inv, silent: true, onSuccess: () => { setImportErr(null); toast('GSTR-2B fetched from the portal') },
  })
  const acceptMatched = useAction<{ accepted: number }, void>(() => api<{ accepted: number }>('/kar/v1/returns/ims/accept-matched', { method: 'POST', query }), {
    invalidate: inv, onSuccess: (r) => toast(r.accepted ? `${r.accepted} matched document${r.accepted === 1 ? '' : 's'} accepted` : 'Nothing left to accept'),
  })
  const setIms = useAction<unknown, { key: string; action: IMSAction; remark?: string }>(
    ({ key, action: a, remark }) => api(`/kar/v1/returns/ims/${encodeURIComponent(key)}`, { method: 'PUT', query: { gstin: gstin || undefined }, body: { action: a, remark: remark || undefined } }),
    { invalidate: inv },
  )
  const exportIms = useAction<Blob, void>(() => api<Blob>('/kar/v1/returns/ims/export', { method: 'GET', raw: true, query }), {
    onSuccess: (b) => downloadFile(`IMS_${q.data?.gstin || gstin || ''}_${fp}.json`, b),
  })

  const onFile = async (f: File | undefined) => {
    if (!f) return
    setImportErr(null)
    try {
      const text = (await f.text()).replace(/^﻿/, '')
      const body: unknown = JSON.parse(text)
      importJson.mutate(body, { onError: (e) => setImportErr(e) })
    } catch {
      setImportErr(new Error(`${f.name} is not a JSON file. Download the GSTR-2B as JSON from the GST portal (Returns → GSTR-2B → Download JSON).`))
    } finally {
      if (file.current) file.current.value = ''
    }
  }

  return (
    <QueryState isLoading={q.isLoading} error={q.error} data={q.data} retry={() => q.refetch()}>
      {(r) => (
        <div className="space-y-3">
          <Card>
            <div className="@container">
              <div className="flex flex-col gap-3 @2xl:flex-row @2xl:items-center @2xl:justify-between">
                <div className="min-w-0 text-sm text-slate-600">
                  <div>GSTIN <span className="font-mono font-semibold">{r.gstin}</span> · period <span className="font-mono font-semibold">{r.period}</span></div>
                  {r.import ? (
                    <div className="mt-0.5 text-xs text-slate-500">
                      {r.import.doc_count} documents · {r.import.source === 'gsp' ? 'fetched through the GSP' : 'uploaded'} {fmtDate(r.import.imported_at, true)}
                      {r.import.generated_on ? ` · 2B generated ${fmtDate(r.import.generated_on)}` : ''}
                      {r.import.skipped?.length ? ` · not reconciled: ${r.import.skipped.join(', ')}` : ''}
                    </div>
                  ) : <div className="mt-0.5 text-xs text-amber-700">No GSTR-2B imported for this period yet.</div>}
                </div>
                <div className="flex flex-wrap gap-2">
                  {canFile && (
                    <>
                      <input ref={file} type="file" accept=".json,application/json" className="hidden" onChange={(e) => void onFile(e.target.files?.[0])} aria-label="GSTR-2B JSON file" />
                      <Button size="sm" icon={Upload} loading={importJson.isPending} onClick={() => file.current?.click()}>{r.import ? 'Re-import JSON' : 'Import 2B JSON'}</Button>
                      <Button size="sm" variant="secondary" icon={CloudDownload} loading={fetchGsp.isPending} onClick={() => { setImportErr(null); fetchGsp.mutate(undefined, { onError: (e) => setImportErr(e) }) }} title="Download through the GST Suvidha Provider with this GSTIN's portal users">Fetch via GSP</Button>
                      <Button size="sm" variant="secondary" icon={CheckCheck} loading={acceptMatched.isPending} disabled={!r.import || groups.acceptable === 0} onClick={() => acceptMatched.mutate()} title="Accept every exact match that has no action yet">Accept all matched{groups.acceptable ? ` (${groups.acceptable})` : ''}</Button>
                      <Button size="sm" variant="ghost" icon={FileJson} loading={exportIms.isPending} disabled={!r.import} onClick={() => exportIms.mutate()} title="IMS actions in the portal upload shape">IMS JSON</Button>
                    </>
                  )}
                  <Button size="sm" variant="ghost" icon={Download} disabled={items.length === 0} onClick={() => downloadFile(`GSTR2B_recon_${r.gstin}_${r.period}.csv`, twoBCsv(rows), 'text/csv')}>CSV</Button>
                </div>
              </div>
            </div>
            {!!importErr && <div className="mt-3"><ErrorBox message={errorMessage(importErr)} /></div>}
            {r.import?.source === 'gsp' && r.import.doc_count === 0 && <p className="mt-2 text-xs text-slate-500">The GSP returned an empty statement (the simulator always does). Upload the portal JSON to reconcile real documents.</p>}
          </Card>
          {r.warning && <WarningList items={[r.warning]} />}

          <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
            {TWOB_STATUSES.map((s) => (
              <button key={s} type="button" onClick={() => setStatus((cur) => (cur === s ? '' : s))} aria-pressed={status === s} title={STATUS_HINT[s]}
                className={`card px-4 py-3 text-left transition cursor-pointer ${status === s ? 'ring-2 ring-brand-600' : 'hover:border-brand-300'}`}>
                <div className="text-xs text-slate-500">{TWOB_STATUS_LABEL[s]}</div>
                <div className="flex items-center gap-2"><span className="text-xl font-bold">{r.counts[s] ?? groups.byStatus[s]}</span><Badge tone={STATUS_TONE[s]}>{s.replace(/_/g, ' ')}</Badge></div>
              </button>
            ))}
          </div>

          <ItcSummaryTable itc={r.itc} />

          <SectionTitle>Documents</SectionTitle>
          <Card padded={false}>
            <div className="flex flex-wrap items-center gap-2 p-3">
              <SearchInput value={search} onChange={setSearch} placeholder="Supplier, GSTIN, invoice no" className="md:w-64" />
              <div className="flex flex-wrap gap-1" role="group" aria-label="IMS action filter">
                {(['', 'none', 'accept', 'pending', 'reject'] as const).map((a) => (
                  <button key={a || 'any'} type="button" onClick={() => setAction(a)} aria-pressed={action === a}
                    className={`rounded-full px-3 py-1 text-xs font-semibold ring-1 ring-inset transition cursor-pointer ${action === a ? 'bg-brand-700 text-white ring-brand-700' : 'bg-white text-slate-600 ring-slate-200 hover:text-ink'}`}>
                    {a === '' ? 'Any action' : a === 'none' ? `No action (${groups.byAction.none})` : `${ACTION_LABEL[a]} (${groups.byAction[a]})`}
                  </button>
                ))}
              </div>
              {(status || action || dq) && <Button size="sm" variant="ghost" icon={X} onClick={() => { setStatus(''); setAction(''); setSearch('') }}>Clear</Button>}
              <span className="ml-auto text-xs text-slate-500">{rows.length} of {items.length}</span>
            </div>
            {rows.length === 0 ? <Empty icon={FileText} title={items.length ? 'Nothing matches the filters' : 'No documents'} text={items.length ? undefined : 'Import the GSTR-2B to see what suppliers reported against your purchase bills.'} /> : (
              <Table>
                <thead className="bg-slate-50/60"><tr><th className="th">Supplier</th><th className="th">GSTR-2B document</th><th className="th text-right">2B taxable / tax</th><th className="th">Purchase bill</th><th className="th text-right">Books taxable / tax</th><th className="th">Match</th><th className="th">IMS</th></tr></thead>
                <tbody className="divide-y divide-slate-100">
                  {rows.map((x, i) => {
                    const d = x.doc
                    const p = x.purchase
                    const a = imsActionOf(x)
                    const busy = setIms.isPending && setIms.variables?.key === x.doc_key
                    return (
                      <tr key={x.doc_key || p?.id || i} className="align-top hover:bg-slate-50/60">
                        <td className="td"><div className="max-w-[14rem] truncate font-medium" title={d?.supplier_name || p?.supplier_name}>{d?.supplier_name || p?.supplier_name || '—'}</div><div className="font-mono text-[11px] text-slate-500">{d?.supplier_gstin || p?.supplier_gstin}</div></td>
                        <td className="td">
                          {d ? <>
                            <div className="font-mono text-xs font-semibold">{d.doc_no}</div>
                            <div className="text-[11px] text-slate-500">{d.doc_type} · {fmtDate(d.doc_date)} · {d.section}{d.reverse_charge ? ' · RCM' : ''}</div>
                            {d.itc_available !== 'Y' && <Badge tone={d.itc_available === 'N' ? 'red' : 'amber'} className="mt-0.5">ITC {d.itc_available === 'N' ? 'not available' : 'temporarily unavailable'}{d.reason ? ` · ${d.reason}` : ''}</Badge>}
                          </> : <span className="text-xs text-slate-400">not in 2B</span>}
                        </td>
                        <td className="td num text-right">{d ? <><Rup v={d.taxable} /><div className="text-[11px] text-slate-500"><Rup v={taxOf(d)} /></div></> : '—'}</td>
                        <td className="td">
                          {p ? <>
                            <Link href={p.doc_type === 'purchase_return' ? `/app/returns/${p.id}` : `/app/purchases/${p.id}`} className="font-mono text-xs font-semibold text-brand-700 hover:underline">{p.number}</Link>
                            <div className="text-[11px] text-slate-500">{p.supplier_invoice_no ? `bill ${p.supplier_invoice_no} · ` : ''}{fmtDate(p.supplier_invoice_date || p.invoice_date)}</div>
                          </> : <span className="text-xs text-slate-400">no bill</span>}
                        </td>
                        <td className="td num text-right">{p ? <><Rup v={p.taxable} /><div className="text-[11px] text-slate-500"><Rup v={taxOf(p)} /></div></> : '—'}</td>
                        <td className="td max-w-[16rem]">
                          <Badge tone={STATUS_TONE[x.status]}>{TWOB_STATUS_LABEL[x.status]}</Badge>
                          {x.diffs?.map((df) => <div key={df.field} className="mt-0.5 text-[11px] text-amber-800">{df.field}: books {String(df.books)} · 2B {String(df.gstr2b)}</div>)}
                          {x.notes?.map((n, j) => <div key={j} className="mt-0.5 text-[11px] text-slate-500">{n}</div>)}
                        </td>
                        <td className="td">
                          {x.doc_key ? (
                            <>
                              <Badge tone={ACTION_TONE[a]}>{a === 'none' ? 'no action' : ACTION_LABEL[a]}</Badge>
                              {x.ims?.remark && <div className="mt-0.5 max-w-[12rem] truncate text-[11px] text-slate-500" title={x.ims.remark}>{x.ims.remark}</div>}
                              {canFile && (
                                <div className="mt-1 flex gap-1">
                                  <ImsButton icon={Check} label="Accept" active={a === 'accept'} busy={busy} onClick={() => setIms.mutate({ key: x.doc_key!, action: 'accept' })} />
                                  <ImsButton icon={Hourglass} label="Keep pending" active={a === 'pending'} busy={busy} onClick={() => setIms.mutate({ key: x.doc_key!, action: 'pending' })} />
                                  <ImsButton icon={X} label="Reject" active={a === 'reject'} busy={busy} onClick={() => setReject(x)} danger />
                                </div>
                              )}
                            </>
                          ) : <span className="text-xs text-slate-400">—</span>}
                        </td>
                      </tr>
                    )
                  })}
                </tbody>
              </Table>
            )}
          </Card>
          <p className="text-xs text-slate-500">IMS: documents with no action are deemed accepted when GSTR-3B is filed. Reject only what you never received or what is wrong; the supplier then has to amend it. Pending keeps the ITC out of this month.</p>
          {reject && <RejectModal row={reject} onClose={() => setReject(null)} onSubmit={(remark) => setIms.mutate({ key: reject.doc_key!, action: 'reject', remark }, { onSuccess: () => setReject(null) })} loading={setIms.isPending} />}
        </div>
      )}
    </QueryState>
  )
}

function ImsButton({ icon: Icon, label, active, busy, onClick, danger }: { icon: typeof Check; label: string; active: boolean; busy: boolean; onClick: () => void; danger?: boolean }) {
  return (
    <button type="button" onClick={onClick} disabled={busy || active} aria-label={label} title={label} aria-pressed={active}
      className={`grid h-7 w-7 place-items-center rounded-lg ring-1 ring-inset transition disabled:cursor-default cursor-pointer ${active ? (danger ? 'bg-rose-600 text-white ring-rose-600' : 'bg-brand-700 text-white ring-brand-700') : `bg-white text-slate-500 ring-slate-200 ${danger ? 'hover:text-rose-600' : 'hover:text-ink'}`} ${busy ? 'opacity-50' : ''}`}>
      <Icon size={14} />
    </button>
  )
}

function RejectModal({ row, onClose, onSubmit, loading }: { row: ReconResult; onClose: () => void; onSubmit: (remark: string) => void; loading: boolean }) {
  const [remark, setRemark] = useState('')
  return (
    <Modal open onClose={onClose} title={`Reject ${row.doc?.doc_no ?? 'document'}`}
      footer={<><Button variant="secondary" onClick={onClose}>Cancel</Button><Button variant="danger" icon={X} loading={loading} onClick={() => onSubmit(remark.trim())}>Reject</Button></>}>
      <p className="text-sm text-slate-600">{row.doc?.supplier_name} ({row.doc?.supplier_gstin}) will see the rejection on the portal after you upload the IMS actions. The ITC on it is not claimed.</p>
      <Field label="Remark (optional)" className="mt-4" hint={`${remark.length}/500`}><Input value={remark} maxLength={500} onChange={(e) => setRemark(e.target.value)} placeholder="e.g. goods not received" autoFocus /></Field>
    </Modal>
  )
}

const BUCKETS: { key: keyof Pick<ITCSummary, 'accepted' | 'deemed_accepted' | 'pending' | 'rejected' | 'not_available'>; label: string }[] = [
  { key: 'accepted', label: 'Accepted' }, { key: 'deemed_accepted', label: 'No action (deemed accepted)' }, { key: 'pending', label: 'Pending' },
  { key: 'rejected', label: 'Rejected' }, { key: 'not_available', label: 'ITC not available (2B)' },
]

/** recon.ITCSummary: 2B ITC by IMS bucket and what goes into GSTR-3B 4(A). Shared with the GSTR-3B view. */
export function ItcSummaryTable({ itc }: { itc: ITCSummary }) {
  return (
    <Card padded={false} title="ITC from GSTR-2B">
      <div className="overflow-x-auto"><table className="w-full min-w-[640px]">
        <thead className="bg-slate-50/60"><tr><th className="th">Bucket</th><th className="th text-right">Docs</th><th className="th text-right">IGST</th><th className="th text-right">CGST</th><th className="th text-right">SGST</th><th className="th text-right">Cess</th></tr></thead>
        <tbody className="divide-y divide-slate-100">
          {BUCKETS.map((b) => { const v = itc[b.key]; return <tr key={b.key}><td className="td">{b.label}</td><td className="td num text-right">{v.docs}</td><td className="td num text-right"><Rup v={v.igst} /></td><td className="td num text-right"><Rup v={v.cgst} /></td><td className="td num text-right"><Rup v={v.sgst} /></td><td className="td num text-right"><Rup v={v.cess} /></td></tr> })}
          <tr><td className="td">4(A)(3) Reverse charge</td><td className="td" /><td className="td num text-right"><Rup v={itc.reverse_charge.igst} /></td><td className="td num text-right"><Rup v={itc.reverse_charge.cgst} /></td><td className="td num text-right"><Rup v={itc.reverse_charge.sgst} /></td><td className="td num text-right"><Rup v={itc.reverse_charge.cess} /></td></tr>
          <tr><td className="td">4(A)(5) All other ITC</td><td className="td" /><td className="td num text-right"><Rup v={itc.all_other.igst} /></td><td className="td num text-right"><Rup v={itc.all_other.cgst} /></td><td className="td num text-right"><Rup v={itc.all_other.sgst} /></td><td className="td num text-right"><Rup v={itc.all_other.cess} /></td></tr>
        </tbody>
        <tfoot className="bg-slate-50 font-bold"><tr><td className="td">Available to claim</td><td className="td" /><td className="td num text-right"><Rup v={itc.available.igst} /></td><td className="td num text-right"><Rup v={itc.available.cgst} /></td><td className="td num text-right"><Rup v={itc.available.sgst} /></td><td className="td num text-right"><Rup v={itc.available.cess} /></td></tr></tfoot>
      </table></div>
    </Card>
  )
}
