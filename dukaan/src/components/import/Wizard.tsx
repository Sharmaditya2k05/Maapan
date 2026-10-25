'use client'

/**
 * Import wizard: kind → upload → map columns → validate → commit & poll.
 * The job id lives in the URL (?job=) so a refresh or the history page resumes
 * at the step the job's status implies.
 */
import { useEffect, useMemo, useRef, useState } from 'react'
import Link from 'next/link'
import { useRouter } from 'next/navigation'
import { AlertTriangle, CheckCircle2, Download, FileSpreadsheet, Loader2, RefreshCw, Upload, XCircle } from 'lucide-react'
import { Badge, Button, Card, ErrorBox, Field, Input, Select, Spinner, Table, useToast } from '@/components/ui'
import { api, errorMessage } from '@/lib/api'
import { downloadFile, fyStartIST, useGet, useInvalidate, useSave, useStore } from '@/lib/hooks'
import type { ImportJob, ImportKind, ImportMappingInput, ImportParams, ImportPreview, ImportValidateSummary } from '@/lib/types.more'
import { Counts, DownloadErrors, ErrorRows, JobBadge, Recon, Stepper } from './bits'
import { IMPORT_KINDS, fmtBytes, isJobBusy, kindMeta, stepForStatus, type WizardStep } from './kinds'

export function ImportWizard({ jobId }: { jobId?: string }) {
  const router = useRouter()
  const [kind, setKind] = useState<ImportKind | null>(null)
  const [step, setStep] = useState<WizardStep>(jobId ? 'mapping' : 'kind')
  const job = useGet<ImportJob>(jobId ? `/aayaat/v1/imports/${jobId}` : null, undefined, {
    refetchInterval: (q) => (isJobBusy(q.state.data?.status) ? 1500 : false),
  })
  // Resume: once the job loads, jump to the step its status implies (only on first load / id change).
  const resumed = useRef<string | null>(null)
  useEffect(() => {
    if (job.data && resumed.current !== job.data.id) { resumed.current = job.data.id; setStep(stepForStatus(job.data.status)) }
  }, [job.data])

  const go = (j: ImportJob, s: WizardStep) => { if (j.id !== jobId) router.replace(`/app/import/new?job=${j.id}`); resumed.current = j.id; setStep(s) }

  return (
    <>
      <Stepper current={step} />
      {jobId && job.isLoading && <div className="flex justify-center py-16"><Spinner /></div>}
      {jobId && job.error && <ErrorBox message={(job.error as Error).message} retry={() => job.refetch()} />}
      {step === 'kind' && <StepKind value={kind} onNext={(k) => { setKind(k); setStep('upload') }} />}
      {step === 'upload' && kind && <StepUpload kind={kind} onBack={() => setStep('kind')} onUploaded={(j) => go(j, 'mapping')} />}
      {step === 'mapping' && job.data && <StepMapping job={job.data} onValidated={(j) => go(j, 'validate')} />}
      {step === 'validate' && job.data && <StepValidate job={job.data} onBack={() => setStep('mapping')} onCommitted={(j) => go(j, 'commit')} />}
      {step === 'commit' && job.data && <StepCommit job={job.data} onBackToMapping={() => setStep('mapping')} />}
    </>
  )
}

/* ---------------- step 1: kind ---------------- */

function StepKind({ value, onNext }: { value: ImportKind | null; onNext: (k: ImportKind) => void }) {
  const toast = useToast()
  const [tpl, setTpl] = useState<ImportKind | null>(null)
  const template = async (k: ImportKind) => {
    setTpl(k)
    try {
      const blob = await api<Blob>(`/aayaat/v1/imports/templates/${k}`, { method: 'GET', raw: true })
      downloadFile(`maapan-${k}-template.csv`, blob)
    } catch (e) { toast(errorMessage(e), 'error') } finally { setTpl(null) }
  }
  return (
    <div>
      <p className="mb-4 text-sm text-slate-600">Bring your existing books in from Excel or CSV — MargBooks exports work as they are. Import in this order: <strong>Parties → Items → Opening balances → Opening stock</strong>.</p>
      <div className="grid gap-3 sm:grid-cols-2">
        {IMPORT_KINDS.map((m) => (
          <div key={m.kind} className={`card flex flex-col p-4 transition ${value === m.kind ? 'ring-2 ring-brand-500' : ''}`}>
            <div className="flex items-start gap-3">
              <div className="grid h-10 w-10 shrink-0 place-items-center rounded-xl bg-brand-50 text-brand-700"><m.icon size={20} /></div>
              <div className="min-w-0 flex-1">
                <div className="font-bold">{m.label}</div>
                <p className="mt-0.5 text-sm text-slate-600">{m.description}</p>
                <div className="mt-1 text-xs text-slate-500">Goes to: {m.goesTo}</div>
              </div>
            </div>
            <div className="mt-4 flex flex-wrap items-center justify-between gap-2">
              <Button size="sm" variant="ghost" icon={Download} loading={tpl === m.kind} onClick={() => template(m.kind)}>Download template</Button>
              <Button size="sm" onClick={() => onNext(m.kind)}>Import {m.label.toLowerCase()}</Button>
            </div>
          </div>
        ))}
      </div>
    </div>
  )
}

/* ---------------- step 2: upload ---------------- */

function StepUpload({ kind, onBack, onUploaded }: { kind: ImportKind; onBack: () => void; onUploaded: (j: ImportJob) => void }) {
  const meta = kindMeta(kind)!
  const { storeId, stores } = useStore()
  const [file, setFile] = useState<File | null>(null)
  const [asOf, setAsOf] = useState(fyStartIST())
  const [store, setStore] = useState(storeId ?? '')
  const [phone, setPhone] = useState('')
  const [drag, setDrag] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const inputRef = useRef<HTMLInputElement>(null)
  const upload = useSave<ImportJob, FormData>('/aayaat/v1/imports', { invalidate: ['/aayaat/v1/imports'], silent: true })

  const accept = (f: File | undefined) => {
    if (!f) return
    const ok = /\.(xlsx|xlsm|csv|txt)$/i.test(f.name)
    if (!ok) return setError(`${f.name}: upload .xlsx or .csv. Legacy .xls files must be saved as .xlsx first.`)
    if (f.size > 50 * 1024 * 1024) return setError('The file is larger than 50 MB; split it by sheet or year.')
    setError(null)
    setFile(f)
  }
  const submit = async () => {
    if (!file) return setError('Choose a file first.')
    if (meta.store && !store) return setError('Pick the store that receives the opening stock.')
    const fd = new FormData()
    fd.set('kind', kind)
    fd.set('file', file)
    if (meta.asOf && asOf) fd.set('as_of', asOf)
    if (meta.store && store) fd.set('store_id', store)
    if (phone.trim()) fd.set('notify_phone', phone.trim())
    try { onUploaded(await upload.mutateAsync(fd)) } catch (e) { setError(errorMessage(e)) }
  }

  return (
    <div className="space-y-4">
      <Card title={<span className="inline-flex items-center gap-2"><meta.icon size={16} /> Upload the {meta.label.toLowerCase()} file</span>}>
        <div
          className={`flex cursor-pointer flex-col items-center justify-center rounded-2xl border-2 border-dashed px-4 py-10 text-center transition ${drag ? 'border-brand-500 bg-brand-50' : 'border-slate-300 hover:border-brand-400 hover:bg-slate-50'}`}
          onDragOver={(e) => { e.preventDefault(); setDrag(true) }} onDragLeave={() => setDrag(false)}
          onDrop={(e) => { e.preventDefault(); setDrag(false); accept(e.dataTransfer.files?.[0]) }}
          onClick={() => inputRef.current?.click()} role="button" tabIndex={0} onKeyDown={(e) => (e.key === 'Enter' || e.key === ' ') && inputRef.current?.click()} aria-label="Choose a spreadsheet file">
          <input ref={inputRef} type="file" className="sr-only" accept=".xlsx,.xlsm,.csv,.txt,application/vnd.openxmlformats-officedocument.spreadsheetml.sheet,text/csv" onChange={(e) => accept(e.target.files?.[0])} />
          {file ? (
            <>
              <FileSpreadsheet size={32} className="mb-2 text-brand-600" />
              <div className="font-semibold">{file.name}</div>
              <div className="text-xs text-slate-500">{fmtBytes(file.size)} · click to choose another</div>
            </>
          ) : (
            <>
              <Upload size={32} className="mb-2 text-slate-400" />
              <div className="font-semibold">Drop an .xlsx or .csv here, or tap to choose</div>
              <div className="mt-1 text-xs text-slate-500">Up to 50 MB · MargBooks: Reports → Export → Excel</div>
            </>
          )}
        </div>
        <div className="mt-4 grid gap-4 sm:grid-cols-3">
          {meta.asOf && <Field label={kind === 'parties' ? 'Opening balance date' : 'As of date'} hint="Normally 1 April of the year you are migrating into."><Input type="date" value={asOf} onChange={(e) => setAsOf(e.target.value)} /></Field>}
          {meta.store && (
            <Field label="Store receiving the stock">
              <Select value={store} onChange={(e) => setStore(e.target.value)}>
                <option value="">Select a store</option>
                {stores.map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}
              </Select>
            </Field>
          )}
          <Field label="WhatsApp a summary to (optional)" hint="+91 number; sent when the import finishes."><Input inputMode="tel" placeholder="+919876543210" value={phone} onChange={(e) => setPhone(e.target.value)} /></Field>
        </div>
      </Card>
      {error && <ErrorBox message={error} />}
      <div className="flex justify-between gap-2">
        <Button variant="secondary" onClick={onBack}>Back</Button>
        <Button icon={Upload} loading={upload.isPending} onClick={submit} disabled={!file}>Upload &amp; preview</Button>
      </div>
    </div>
  )
}

/* ---------------- step 3: mapping ---------------- */

function StepMapping({ job, onValidated }: { job: ImportJob; onValidated: (j: ImportJob) => void }) {
  const meta = kindMeta(job.kind)
  const { stores } = useStore()
  const invalidate = useInvalidate()
  const [sheet, setSheet] = useState(job.sheet ?? '')
  const preview = useGet<ImportPreview>(`/aayaat/v1/imports/${job.id}/preview`, { sheet })
  const [mapping, setMapping] = useState<Record<string, string> | null>(null)
  const [headerRow, setHeaderRow] = useState<string>(job.header_row ? String(job.header_row) : '')
  const [params, setParams] = useState<ImportParams>(job.params ?? {})
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState<'save' | 'validate' | null>(null)

  // Seed the mapping from the saved one, else the server's suggestion (per sheet).
  const seededFor = useRef<string | null>(null)
  useEffect(() => {
    if (!preview.data) return
    const key = `${job.id}:${preview.data.sheet}`
    if (seededFor.current === key) return
    seededFor.current = key
    const saved = preview.data.mapping && Object.keys(preview.data.mapping).length > 0 ? preview.data.mapping : preview.data.suggested_mapping
    setMapping({ ...saved })
    if (!sheet) setSheet(preview.data.sheet)
  }, [preview.data, job.id, sheet])

  const p = preview.data
  const m = useMemo(() => mapping ?? {}, [mapping])
  const colIndex = useMemo(() => { const o: Record<string, number> = {}; p?.headers.forEach((h, i) => { if (!(h in o)) o[h] = i }); return o }, [p])
  const samples = (header: string) => { const i = colIndex[header]; return i === undefined ? [] : (p?.rows ?? []).map((r) => r[i] ?? '').filter((v) => v !== '').slice(0, 3) }
  const missing = (p?.fields ?? []).filter((f) => f.required && !m[f.key]?.trim())
  const dupes = useMemo(() => { const c: Record<string, number> = {}; Object.values(m).forEach((h) => { if (h) c[h] = (c[h] ?? 0) + 1 }); return Object.keys(c).filter((h) => c[h] > 1) }, [m])

  const body = (): ImportMappingInput | string => {
    if (missing.length) return `Map the required field${missing.length > 1 ? 's' : ''}: ${missing.map((f) => f.label).join(', ')}.`
    const hr = headerRow.trim() === '' ? 0 : Number(headerRow)
    if (!Number.isInteger(hr) || hr < 0) return 'Header row must be a whole number (blank = auto-detect).'
    if (meta?.store && !params.store_id) return 'Opening stock needs a target store.'
    if (params.as_of && !/^\d{4}-\d{2}-\d{2}$/.test(params.as_of)) return 'As-of date must be YYYY-MM-DD.'
    const clean: Record<string, string> = {}
    for (const [k, v] of Object.entries(m)) if (v && v.trim()) clean[k] = v
    return { sheet: sheet || undefined, header_row: hr, mapping: clean, params }
  }
  const saveMapping = async (): Promise<ImportJob | null> => {
    const b = body()
    if (typeof b === 'string') { setError(b); return null }
    setError(null)
    const j = await api<ImportJob>(`/aayaat/v1/imports/${job.id}/mapping`, { method: 'PUT', body: b })
    invalidate(`/aayaat/v1/imports/${job.id}`, `/aayaat/v1/imports/${job.id}/preview`, '/aayaat/v1/imports')
    return j
  }
  const save = async () => { setBusy('save'); try { if (await saveMapping()) setError(null) } catch (e) { setError(errorMessage(e)) } finally { setBusy(null) } }
  const validate = async () => {
    setBusy('validate')
    try {
      if (!(await saveMapping())) return
      const s = await api<ImportValidateSummary>(`/aayaat/v1/imports/${job.id}/validate`, { method: 'POST' })
      invalidate(`/aayaat/v1/imports/${job.id}`, '/aayaat/v1/imports')
      onValidated(s.job)
    } catch (e) { setError(errorMessage(e)) } finally { setBusy(null) }
  }

  if (isJobBusy(job.status) || job.status === 'completed') {
    return <ErrorBox message={`This import is ${job.status}; its mapping can no longer change.`} />
  }

  return (
    <div className="space-y-4">
      <Card title={<span className="inline-flex flex-wrap items-center gap-2"><FileSpreadsheet size={16} /> {job.file_name} <span className="text-xs font-normal text-slate-500">{fmtBytes(job.file_size)}</span> <JobBadge status={job.status} /></span>}>
        <div className="grid gap-4 sm:grid-cols-3">
          <Field label="Sheet">
            <Select value={sheet} onChange={(e) => { setSheet(e.target.value); setMapping(null) }} disabled={!p || p.sheets.length <= 1}>
              {(p?.sheets ?? (sheet ? [sheet] : [])).map((s) => <option key={s} value={s}>{s}</option>)}
            </Select>
          </Field>
          <Field label="Header row" hint={p ? `Auto-detected row ${p.header_row}. Override and save to re-read the headers.` : undefined}>
            <Input className="num" inputMode="numeric" placeholder={p ? `auto (${p.header_row})` : 'auto'} value={headerRow} onChange={(e) => setHeaderRow(e.target.value.replace(/\D/g, ''))} />
          </Field>
          {meta?.asOf && <Field label={job.kind === 'parties' ? 'Opening balance date' : 'As of date'}><Input type="date" value={params.as_of ?? ''} onChange={(e) => setParams((x) => ({ ...x, as_of: e.target.value }))} /></Field>}
          {meta?.store && (
            <Field label="Store receiving the stock">
              <Select value={params.store_id ?? ''} onChange={(e) => setParams((x) => ({ ...x, store_id: e.target.value }))}>
                <option value="">Select a store</option>
                {stores.map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}
              </Select>
            </Field>
          )}
        </div>
      </Card>

      {preview.isLoading && <div className="flex items-center gap-2 py-8 text-sm text-slate-500"><Loader2 size={16} className="animate-spin" /> Reading the file…</div>}
      {preview.error && <ErrorBox message={(preview.error as Error).message} retry={() => preview.refetch()} />}
      {p && mapping && (
        <Card title="Map Maapan fields to your columns" padded={false}
          actions={<Button size="sm" variant="ghost" icon={RefreshCw} onClick={() => setMapping({ ...p.suggested_mapping })}>Use suggestions</Button>}>
          <div className="hidden grid-cols-[200px_1fr_1fr] gap-3 border-b border-slate-100 px-4 py-2 text-[11px] font-semibold uppercase tracking-wider text-slate-500 md:grid">
            <div>Maapan field</div><div>Your column</div><div>Sample values</div>
          </div>
          <ul className="divide-y divide-slate-100">
            {p.fields.map((f) => {
              const h = m[f.key] ?? ''
              const dup = h && dupes.includes(h)
              return (
                <li key={f.key} className="grid gap-2 px-4 py-3 md:grid-cols-[200px_1fr_1fr] md:items-center">
                  <div>
                    <div className="text-sm font-semibold">{f.label}{f.required && <span className="ml-1 text-rose-600" title="Required">*</span>}</div>
                    {f.example && <div className="text-xs text-slate-500">e.g. {f.example}</div>}
                  </div>
                  <Select className="input-sm" value={h} onChange={(e) => setMapping((x) => ({ ...(x ?? {}), [f.key]: e.target.value }))} aria-label={`Column for ${f.label}`}>
                    <option value="">— not in file —</option>
                    {p.headers.map((hd, i) => hd.trim() !== '' && <option key={`${hd}-${i}`} value={hd}>{hd}</option>)}
                  </Select>
                  <div className="truncate text-xs text-slate-500">
                    {h ? (samples(h).join(' · ') || <span className="italic">empty in preview</span>) : f.required ? <span className="font-semibold text-rose-600">required — pick a column</span> : '—'}
                    {dup && <span className="ml-2 text-amber-700">(also used by another field)</span>}
                  </div>
                </li>
              )
            })}
          </ul>
        </Card>
      )}

      {p && p.rows.length > 0 && (
        <Card title={`Preview · first ${p.rows.length} rows after header row ${p.header_row}`} padded={false}>
          <div className="max-h-72 overflow-auto">
            <table className="min-w-full border-collapse text-xs">
              <thead className="sticky top-0 bg-slate-50"><tr>{p.headers.map((h, i) => <th key={i} className="th whitespace-nowrap">{h || <span className="text-slate-300">(blank)</span>}</th>)}</tr></thead>
              <tbody>{p.rows.slice(0, 10).map((r, ri) => <tr key={ri} className="border-t border-slate-100">{p.headers.map((_, ci) => <td key={ci} className="whitespace-nowrap px-4 py-1.5">{r[ci] ?? ''}</td>)}</tr>)}</tbody>
            </table>
          </div>
        </Card>
      )}

      {error && <ErrorBox message={error} />}
      <div className="flex flex-wrap justify-between gap-2">
        <Button variant="secondary" loading={busy === 'save'} onClick={save} disabled={!p || busy !== null}>Save mapping</Button>
        <Button icon={CheckCircle2} loading={busy === 'validate'} onClick={validate} disabled={!p || busy !== null}>Save &amp; check rows</Button>
      </div>
    </div>
  )
}

/* ---------------- step 4: validate ---------------- */

function StepValidate({ job, onBack, onCommitted }: { job: ImportJob; onBack: () => void; onCommitted: (j: ImportJob) => void }) {
  const invalidate = useInvalidate()
  const preview = useGet<ImportPreview>(`/aayaat/v1/imports/${job.id}/preview`)
  const fieldLabel = (k: string) => preview.data?.fields.find((f) => f.key === k)?.label ?? k
  const [error, setError] = useState<string | null>(null)
  const revalidate = useSave<ImportValidateSummary>(`/aayaat/v1/imports/${job.id}/validate`, { invalidate: [`/aayaat/v1/imports/${job.id}`, '/aayaat/v1/imports'] })
  const commit = useSave<ImportJob>(`/aayaat/v1/imports/${job.id}/commit`, { silent: true, onSuccess: (j) => { invalidate('/aayaat/v1/imports', `/aayaat/v1/imports/${job.id}`); onCommitted(j) } })

  return (
    <div className="space-y-4">
      <Counts job={job} />
      {job.reconciliation && <Recon r={job.reconciliation} />}
      {job.valid_rows === 0 && <ErrorBox message="No valid rows. Fix the errors in the sheet (or the column mapping) and check again." />}
      <Card title="Rows with errors" padded={false} actions={job.error_rows > 0 ? <DownloadErrors jobId={job.id} count={job.error_rows} /> : undefined}>
        {job.error_rows === 0 ? (
          <div className="flex items-center gap-2 px-4 py-6 text-sm text-emerald-700"><CheckCircle2 size={18} /> Every row is valid.</div>
        ) : (
          <>
            <p className="px-4 pt-3 text-xs text-slate-500">Rows with errors are skipped. Download the CSV, fix the cells in Excel and upload again — or import the valid rows now and add the rest later (re-importing updates instead of duplicating).</p>
            <ErrorRows jobId={job.id} fieldLabel={fieldLabel} />
          </>
        )}
      </Card>
      {error && <ErrorBox message={error} />}
      <div className="flex flex-wrap justify-between gap-2">
        <div className="flex gap-2">
          <Button variant="secondary" onClick={onBack}>Back to mapping</Button>
          <Button variant="ghost" icon={RefreshCw} loading={revalidate.isPending} onClick={() => revalidate.mutate(undefined)}>Check again</Button>
        </div>
        <Button icon={Upload} loading={commit.isPending} disabled={job.valid_rows === 0}
          onClick={async () => { setError(null); try { await commit.mutateAsync(undefined) } catch (e) { setError(errorMessage(e)) } }}>
          Import {job.valid_rows.toLocaleString('en-IN')} valid row{job.valid_rows === 1 ? '' : 's'}
        </Button>
      </div>
    </div>
  )
}

/* ---------------- step 5: commit & poll ---------------- */

function StepCommit({ job, onBackToMapping }: { job: ImportJob; onBackToMapping: () => void }) {
  const meta = kindMeta(job.kind)
  const invalidate = useInvalidate()
  const retry = useSave<ImportJob>(`/aayaat/v1/imports/${job.id}/commit`, { invalidate: [`/aayaat/v1/imports/${job.id}`, '/aayaat/v1/imports'] })
  const done = job.imported + job.failed
  const pct = job.valid_rows > 0 ? Math.min(100, Math.round((100 * done) / job.valid_rows)) : 0
  const busy = isJobBusy(job.status)

  useEffect(() => {
    if (job.status === 'completed') invalidate('/bahi/v1/parties', '/bhandar/v1/items', '/bhandar/v1/stock', '/bahi/v1/accounts')
  }, [job.status, invalidate])

  return (
    <div className="space-y-4">
      <Card>
        <div className="flex flex-col items-center py-4 text-center">
          {busy ? <Loader2 size={40} className="mb-3 animate-spin text-brand-600" />
            : job.status === 'completed' ? <CheckCircle2 size={44} className="mb-3 text-emerald-600" />
            : job.status === 'failed' ? <XCircle size={44} className="mb-3 text-rose-600" />
            : <AlertTriangle size={44} className="mb-3 text-amber-500" />}
          <div className="text-lg font-bold">
            {job.status === 'queued' ? 'Queued — a worker is starting up'
              : job.status === 'running' ? `Importing ${meta?.label.toLowerCase() ?? 'rows'}…`
              : job.status === 'completed' ? `${job.imported.toLocaleString('en-IN')} ${meta?.label.toLowerCase() ?? 'rows'} imported`
              : job.status === 'failed' ? 'Import stopped'
              : `Import is ${job.status}`}
          </div>
          <div className="mt-1 text-sm text-slate-500">{job.file_name} · <JobBadge status={job.status} /></div>
          {(busy || job.status === 'completed' || job.status === 'failed') && job.valid_rows > 0 && (
            <div className="mt-5 w-full max-w-md">
              <div className="h-3 w-full overflow-hidden rounded-full bg-slate-100" role="progressbar" aria-valuemin={0} aria-valuemax={100} aria-valuenow={pct}>
                <div className={`h-full transition-all ${job.status === 'failed' ? 'bg-rose-500' : 'bg-brand-500'}`} style={{ width: `${pct}%` }} />
              </div>
              <div className="mt-1.5 flex justify-between text-xs text-slate-500"><span>{done.toLocaleString('en-IN')} of {job.valid_rows.toLocaleString('en-IN')} rows</span><span>{pct}%</span></div>
            </div>
          )}
          {job.error && <div className="mt-4 max-w-lg rounded-xl bg-rose-50 px-4 py-3 text-sm text-rose-700">{job.error}</div>}
        </div>
      </Card>

      {!busy && (
        <>
          <Counts job={job} />
          {job.reconciliation && <Recon r={job.reconciliation} />}
          {(job.failed > 0 || job.error_rows > 0) && (
            <Card title={`${(job.failed + job.error_rows).toLocaleString('en-IN')} rows were not imported`} padded={false} actions={<DownloadErrors jobId={job.id} count={job.failed + job.error_rows} />}>
              <ErrorRows jobId={job.id} />
            </Card>
          )}
          <div className="flex flex-wrap justify-between gap-2">
            <div className="flex gap-2">
              <Link href="/app/import"><Button variant="secondary">Import history</Button></Link>
              {job.status === 'failed' && <Button variant="secondary" onClick={onBackToMapping}>Fix mapping</Button>}
            </div>
            <div className="flex gap-2">
              {job.status === 'failed' && <Button icon={RefreshCw} loading={retry.isPending} onClick={() => retry.mutate(undefined)}>Resume import</Button>}
              {job.status === 'completed' && <Link href="/app/import/new"><Button icon={Upload}>Import something else</Button></Link>}
            </div>
          </div>
        </>
      )}
      {busy && <p className="text-center text-xs text-slate-500">You can leave this page; the import continues on the server and shows up in <Link href="/app/import" className="link">Import history</Link>.</p>}
      {job.status === 'completed' && (
        <Table>
          <tbody>
            <tr><td className="td text-slate-500">Where to look</td><td className="td"><Badge tone="brand">{meta?.goesTo}</Badge></td></tr>
          </tbody>
        </Table>
      )}
    </div>
  )
}
