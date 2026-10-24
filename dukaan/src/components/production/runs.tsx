'use client'

/** Production-run pieces: status badge, timeline, shortages, plan and complete modals. */
import { useEffect, useMemo, useState } from 'react'
import { useRouter } from 'next/navigation'
import { AlertTriangle, CheckCircle2, Circle, Clock, PackageCheck, Play, XCircle } from 'lucide-react'
import { Badge, Button, ErrorBox, Field, Input, Modal, Select, Table, Textarea, useToast, type Tone } from '@/components/ui'
import { ApiError } from '@/lib/api'
import { fmtDate, fmtQty, todayIST, useGet, useSave, useStore } from '@/lib/hooks'
import type { BomSummary, NirmaanList, ProductionRun, RunCompleteInput, RunCreateInput, RunShortage, RunStatus } from '@/lib/types.more'

export const RUN_LABEL: Record<RunStatus, string> = { planned: 'Planned', in_progress: 'In progress', completed: 'Completed', cancelled: 'Cancelled' }
export const runTone = (s: RunStatus): Tone => (s === 'completed' ? 'green' : s === 'in_progress' ? 'amber' : s === 'cancelled' ? 'red' : 'blue')
export function RunBadge({ status }: { status: RunStatus }) { return <Badge tone={runTone(status)}>{RUN_LABEL[status]}</Badge> }

/** Vertical status timeline: planned → started → completed | cancelled. */
export function RunTimeline({ run }: { run: ProductionRun }) {
  const steps: { label: string; at?: string; state: 'done' | 'current' | 'todo' | 'off'; icon: typeof Circle }[] = [
    { label: 'Planned', at: run.created_at, state: 'done', icon: Clock },
    { label: 'Started', at: run.started_at, state: run.started_at ? 'done' : run.status === 'planned' ? 'current' : 'off', icon: Play },
    run.status === 'cancelled'
      ? { label: 'Cancelled', at: run.cancelled_at, state: 'done', icon: XCircle }
      : { label: 'Completed', at: run.completed_at, state: run.completed_at ? 'done' : run.status === 'in_progress' ? 'current' : 'todo', icon: PackageCheck },
  ]
  return (
    <ol className="relative space-y-4 pl-1">
      {steps.map((s, i) => {
        const Icon = s.state === 'done' ? (s.label === 'Cancelled' ? XCircle : CheckCircle2) : s.icon
        const color = s.state === 'done' ? (s.label === 'Cancelled' ? 'text-rose-600' : 'text-emerald-600') : s.state === 'current' ? 'text-amber-500' : 'text-slate-300'
        return (
          <li key={s.label} className="relative flex items-start gap-3">
            {i < steps.length - 1 && <span className="absolute left-[9px] top-6 h-[calc(100%-4px)] w-px bg-slate-200" aria-hidden />}
            <Icon size={20} className={`relative mt-0.5 shrink-0 ${color}`} />
            <div>
              <div className={`text-sm font-semibold ${s.state === 'off' || s.state === 'todo' ? 'text-slate-400' : ''}`}>{s.label}</div>
              <div className="text-xs text-slate-500">{s.at ? fmtDate(s.at, true) : s.state === 'current' ? 'next step' : s.state === 'off' ? 'skipped' : '—'}</div>
            </div>
          </li>
        )
      })}
    </ol>
  )
}

export function ShortageList({ shortages, title = 'Not enough material in this store' }: { shortages: RunShortage[]; title?: string }) {
  if (!shortages.length) return null
  return (
    <div className="rounded-xl border border-amber-200 bg-amber-50 p-3 text-sm text-amber-900" role="alert">
      <div className="mb-2 flex items-center gap-2 font-bold"><AlertTriangle size={16} /> {title}</div>
      <ul className="space-y-1">
        {shortages.map((s) => (
          <li key={s.item_id} className="flex flex-wrap justify-between gap-x-3">
            <span className="font-medium">{s.item_name}</span>
            <span className="num">need {fmtQty(s.required)} · have {fmtQty(s.on_hand)} · <strong>short {fmtQty(s.short)}</strong></span>
          </li>
        ))}
      </ul>
    </div>
  )
}

/** Shortages out of a 409 from POST /start, if the server attached them. */
export function shortagesFromError(e: unknown): RunShortage[] | null {
  if (e instanceof ApiError && e.status === 409) {
    const d = e.details as { shortages?: RunShortage[] } | undefined
    if (Array.isArray(d?.shortages)) return d.shortages
  }
  return null
}

/* ---------------- plan a run ---------------- */

export function PlanRunModal({ open, onClose, bomId }: { open: boolean; onClose: () => void; bomId?: string }) {
  const router = useRouter()
  const toast = useToast()
  const { storeId, stores } = useStore()
  const boms = useGet<NirmaanList<BomSummary>>(open ? '/nirmaan/v1/boms' : null, { status: 'active', limit: 200 })
  const [bom, setBom] = useState(bomId ?? '')
  const [qty, setQty] = useState('')
  const [store, setStore] = useState(storeId ?? '')
  const [date, setDate] = useState(todayIST())
  const [notes, setNotes] = useState('')
  const [planned, setPlanned] = useState<ProductionRun | null>(null)
  const [startErr, setStartErr] = useState<RunShortage[] | null>(null)

  useEffect(() => { if (open) { setBom(bomId ?? ''); setQty(''); setStore(storeId ?? ''); setDate(todayIST()); setNotes(''); setPlanned(null); setStartErr(null) } }, [open, bomId, storeId])

  const chosen = useMemo(() => boms.data?.items.find((b) => b.id === bom), [boms.data, bom])
  const create = useSave<ProductionRun, RunCreateInput>('/nirmaan/v1/production-runs', { invalidate: ['/nirmaan/v1/production-runs'] })
  const start = useSave<ProductionRun, { force: boolean }>(`/nirmaan/v1/production-runs/${planned?.id}/start`, { invalidate: ['/nirmaan/v1/production-runs'], silent: true })

  const plan = async () => {
    const n = Number(qty)
    if (!bom) return toast('Pick an active BOM', 'error')
    if (!(n > 0)) return toast('Enter a positive quantity', 'error')
    const r = await create.mutateAsync({ bom_id: bom, qty: n, store_id: store || undefined, planned_date: date || undefined, notes: notes.trim() || undefined })
    setPlanned(r)
  }
  const doStart = async (force: boolean) => {
    if (!planned) return
    try {
      await start.mutateAsync({ force })
      toast('Run started')
      onClose()
      router.push(`/app/production/runs/${planned.id}`)
    } catch (e) {
      const sh = shortagesFromError(e)
      if (sh) setStartErr(sh)
      else toast((e as Error).message, 'error')
    }
  }

  const consume = planned?.lines?.filter((l) => l.kind === 'consume') ?? []
  const shortOf = (id: string) => (startErr ?? planned?.shortages ?? []).find((s) => s.item_id === id)

  return (
    <Modal open={open} onClose={onClose} title={planned ? `Run planned · ${planned.item_name}` : 'Plan a production run'} size="lg"
      footer={planned ? (
        <>
          <Button variant="secondary" onClick={() => { onClose(); router.push(`/app/production/runs/${planned.id}`) }}>View run</Button>
          {(startErr || (planned.shortages?.length ?? 0) > 0) ? (
            <Button variant="danger" icon={Play} loading={start.isPending} onClick={() => doStart(true)}>Start anyway (force)</Button>
          ) : (
            <Button icon={Play} loading={start.isPending} onClick={() => doStart(false)}>Start now</Button>
          )}
        </>
      ) : (
        <>
          <Button variant="secondary" onClick={onClose}>Cancel</Button>
          <Button loading={create.isPending} onClick={plan}>Plan run</Button>
        </>
      )}>
      {!planned ? (
        <div className="space-y-4">
          <Field label="Active BOM" hint={boms.data && boms.data.items.length === 0 ? 'No active BOMs yet — activate one first.' : undefined}>
            <Select value={bom} onChange={(e) => setBom(e.target.value)} disabled={!!bomId}>
              <option value="">{boms.isLoading ? 'Loading…' : 'Select a BOM'}</option>
              {boms.data?.items.map((b) => <option key={b.id} value={b.id}>{b.item_name} · v{b.version} ({fmtQty(b.output_qty)} {b.unit}/batch)</option>)}
            </Select>
          </Field>
          {boms.error && <ErrorBox message={(boms.error as Error).message} retry={() => boms.refetch()} />}
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label={`Quantity to produce${chosen ? ` (${chosen.unit})` : ''}`}>
              <Input className="num" inputMode="decimal" value={qty} onChange={(e) => setQty(e.target.value)} placeholder={chosen ? String(chosen.output_qty) : ''} autoFocus />
            </Field>
            <Field label="Planned date"><Input type="date" value={date} onChange={(e) => setDate(e.target.value)} /></Field>
          </div>
          <Field label="Store (material is consumed and output received here)">
            <Select value={store} onChange={(e) => setStore(e.target.value)}>
              {stores.map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}
            </Select>
          </Field>
          <Field label="Notes"><Textarea rows={2} value={notes} onChange={(e) => setNotes(e.target.value)} /></Field>
        </div>
      ) : (
        <div className="space-y-4">
          <p className="text-sm text-slate-600">Planned <strong>{fmtQty(planned.qty)} {planned.lines?.find((l) => l.kind === 'produce')?.unit}</strong> of {planned.item_name} on {fmtDate(planned.planned_date)}. Material to consume:</p>
          <Table>
            <thead><tr><th className="th">Component</th><th className="th text-right">Planned</th><th className="th text-right">Availability</th></tr></thead>
            <tbody>
              {consume.map((l) => {
                const sh = shortOf(l.item_id)
                return (
                  <tr key={l.line_no} className="border-t border-slate-100">
                    <td className="td font-medium">{l.item_name}</td>
                    <td className="td num text-right">{fmtQty(l.planned_qty)} {l.unit}</td>
                    <td className="td text-right">
                      {sh ? <Badge tone="red">short {fmtQty(sh.short)} · have {fmtQty(sh.on_hand)}</Badge>
                        : planned.shortages === undefined && !startErr ? <span className="text-xs text-slate-400">unknown</span>
                        : <Badge tone="green">in stock</Badge>}
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </Table>
          {planned.shortages === undefined && !startErr && <p className="text-xs text-slate-500">Stock availability could not be checked right now; starting will re-check it.</p>}
          {startErr && <ShortageList shortages={startErr} title="Cannot start: not enough material. You can force-start and record the actual consumption on completion." />}
        </div>
      )}
    </Modal>
  )
}

/* ---------------- complete a run ---------------- */

export function CompleteRunModal({ run, open, onClose, onDone }: { run: ProductionRun; open: boolean; onClose: () => void; onDone: (r: ProductionRun) => void }) {
  const toast = useToast()
  const produce = run.lines?.find((l) => l.kind === 'produce')
  const consume = useMemo(() => run.lines?.filter((l) => l.kind === 'consume') ?? [], [run.lines])
  const [outputQty, setOutputQty] = useState(String(run.qty))
  const [batch, setBatch] = useState('')
  const [actual, setActual] = useState<Record<string, string>>({})
  const [error, setError] = useState<string | null>(null)
  useEffect(() => {
    if (open) { setOutputQty(String(run.qty)); setBatch(''); setError(null); setActual(Object.fromEntries(consume.map((l) => [l.item_id, String(l.planned_qty)]))) }
  }, [open, run.qty, consume])

  const complete = useSave<ProductionRun, RunCompleteInput>(`/nirmaan/v1/production-runs/${run.id}/complete`, { invalidate: ['/nirmaan/v1/production-runs', '/bhandar/v1/stock'], silent: true })

  const submit = async () => {
    const oq = Number(outputQty)
    if (!(oq >= 0)) return setError('Output quantity cannot be negative.')
    const consumed: { item_id: string; qty: number }[] = []
    for (const l of consume) {
      const v = Number(actual[l.item_id] ?? l.planned_qty)
      if (!(v >= 0)) return setError(`${l.item_name}: consumed quantity cannot be negative.`)
      if (v !== l.planned_qty) consumed.push({ item_id: l.item_id, qty: v })
    }
    setError(null)
    try {
      const r = await complete.mutateAsync({ output_qty: oq > 0 ? oq : undefined, output_batch_no: batch.trim() || undefined, consumed: consumed.length ? consumed : undefined })
      toast('Run completed — stock updated')
      onDone(r)
    } catch (e) { setError((e as Error).message) }
  }

  return (
    <Modal open={open} onClose={onClose} title="Complete production run" size="lg"
      footer={<><Button variant="secondary" onClick={onClose}>Cancel</Button><Button icon={PackageCheck} loading={complete.isPending} onClick={submit}>Complete &amp; receive stock</Button></>}>
      <div className="space-y-4">
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label={`Actual output (${produce?.unit ?? run.lines?.[0]?.unit ?? ''})`} hint={`Planned ${fmtQty(run.qty)}. Lower output raises the unit cost.`}>
            <Input className="num" inputMode="decimal" value={outputQty} onChange={(e) => setOutputQty(e.target.value)} autoFocus />
          </Field>
          <Field label="Output batch no. (optional)"><Input value={batch} onChange={(e) => setBatch(e.target.value)} placeholder="e.g. B-1016" /></Field>
        </div>
        <div>
          <div className="label">Actual consumption</div>
          <Table>
            <thead><tr><th className="th">Component</th><th className="th text-right">Planned</th><th className="th text-right">Actual</th></tr></thead>
            <tbody>
              {consume.map((l) => (
                <tr key={l.line_no} className="border-t border-slate-100">
                  <td className="td font-medium">{l.item_name}</td>
                  <td className="td num text-right text-slate-500">{fmtQty(l.planned_qty)} {l.unit}</td>
                  <td className="td text-right">
                    <Input className="input-sm num ml-auto w-28 text-right" inputMode="decimal" value={actual[l.item_id] ?? ''} onChange={(e) => setActual((a) => ({ ...a, [l.item_id]: e.target.value }))} aria-label={`Actual ${l.item_name} consumed`} />
                  </td>
                </tr>
              ))}
            </tbody>
          </Table>
        </div>
        {error && <ErrorBox message={error} />}
      </div>
    </Modal>
  )
}
