'use client'

/**
 * Debounced party search (GET /bahi/v1/parties?q=&type=) with a quick-add
 * modal (POST /bahi/v1/parties). Used by the counter, the B2B editor and the
 * purchase editor.
 */
import { useEffect, useId, useRef, useState } from 'react'
import { Plus, Search, UserPlus, X } from 'lucide-react'
import { Avatar, Badge, Button, Field, Input, Modal, Money, MoneyInput, Select, Spinner } from '@/components/ui'
import { useDebounced, useList, usePerm, useSave } from '@/lib/hooks'
import { PERMS } from '@/lib/rbac'
import { STATES, validateGSTIN } from '@/lib/gst'
import type { Party } from '@/lib/types'
import type { PartyInput } from '@/lib/types.pos'
import { stateName } from '@/lib/pos/invoice'

type PartyType = 'customer' | 'supplier'

export function PartyPicker({ value, onChange, type = 'customer', placeholder, autoFocus, disabled, compact }: {
  value: Party | null
  onChange: (p: Party | null) => void
  type?: PartyType
  placeholder?: string
  autoFocus?: boolean
  disabled?: boolean
  /** Chip without the balance line (counter). */
  compact?: boolean
}) {
  const canDo = usePerm()
  const [q, setQ] = useState('')
  const dq = useDebounced(q)
  const [open, setOpen] = useState(false)
  const [hi, setHi] = useState(0)
  const [adding, setAdding] = useState<string | null>(null)
  const box = useRef<HTMLDivElement>(null)
  const listId = useId()
  const list = useList<Party>(open && dq.trim() ? '/bahi/v1/parties' : null, { q: dq.trim(), type, limit: 10 })
  const items = list.data?.items ?? []

  useEffect(() => {
    const h = (e: MouseEvent) => { if (!box.current?.contains(e.target as Node)) setOpen(false) }
    document.addEventListener('mousedown', h)
    return () => document.removeEventListener('mousedown', h)
  }, [])
  useEffect(() => setHi(0), [dq])

  const pick = (p: Party) => { onChange(p); setQ(''); setOpen(false) }

  if (value) {
    return (
      <div className="flex items-center gap-3 rounded-xl border border-slate-200 bg-slate-50 px-3 py-2">
        <Avatar name={value.name} className="h-9 w-9 text-xs" />
        <div className="min-w-0 flex-1">
          <div className="truncate text-sm font-semibold">{value.name}</div>
          <div className="truncate text-xs text-slate-500">
            {[value.phone, value.gstin ? <span key="g" className="font-mono">{value.gstin}</span> : null, value.state_code ? stateName(value.state_code) : null].filter(Boolean).map((x, i) => <span key={i}>{i > 0 && ' · '}{x}</span>)}
          </div>
          {!compact && (value.credit_limit_paise > 0 || value.balance_paise !== 0) && (
            <div className="mt-0.5 text-xs text-slate-500">
              Balance <Money paise={value.balance_paise} signed className="font-semibold" />
              {value.credit_limit_paise > 0 && <> · Limit <Money paise={value.credit_limit_paise} /> · {value.credit_days} days</>}
            </div>
          )}
        </div>
        {!disabled && <button type="button" onClick={() => onChange(null)} className="rounded-lg p-1.5 text-slate-400 hover:bg-slate-200 hover:text-ink cursor-pointer" aria-label={`Remove ${type}`}><X size={16} /></button>}
      </div>
    )
  }

  return (
    <div ref={box} className="relative">
      <div className="flex gap-2">
        <div className="relative flex-1">
          <Search size={15} className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-slate-400" />
          <input
            className="input pl-9"
            value={q}
            disabled={disabled}
            autoFocus={autoFocus}
            placeholder={placeholder ?? `Search ${type} by name, phone or GSTIN`}
            onFocus={() => setOpen(true)}
            onChange={(e) => { setQ(e.target.value); setOpen(true) }}
            onKeyDown={(e) => {
              if (e.key === 'ArrowDown') { e.preventDefault(); setHi((h) => Math.min(h + 1, items.length - 1)) }
              else if (e.key === 'ArrowUp') { e.preventDefault(); setHi((h) => Math.max(h - 1, 0)) }
              else if (e.key === 'Enter') { e.preventDefault(); if (items[hi]) pick(items[hi]); else if (q.trim() && canDo(PERMS.partyManage)) setAdding(q.trim()) }
              else if (e.key === 'Escape') { setOpen(false); setQ('') }
            }}
            role="combobox"
            aria-autocomplete="list"
            aria-controls={listId}
            aria-label={`Search ${type}`}
            aria-expanded={open}
          />
        </div>
        {canDo(PERMS.partyManage) && <Button variant="secondary" icon={UserPlus} onClick={() => setAdding(q.trim())} disabled={disabled} aria-label={`Add ${type}`}><span className="hidden sm:inline">Add</span></Button>}
      </div>
      {open && (q.trim() || items.length > 0) && (
        <div className="absolute z-30 mt-1.5 max-h-72 w-full overflow-y-auto rounded-xl border border-slate-200 bg-white p-1 shadow-xl" role="listbox" id={listId}>
          {list.isLoading && <div className="flex items-center gap-2 px-3 py-2 text-sm text-slate-500"><Spinner className="h-4 w-4" /> Searching…</div>}
          {list.error && <div className="px-3 py-2 text-sm text-rose-600">{(list.error as Error).message}</div>}
          {items.map((p, i) => (
            <button key={p.id} type="button" role="option" aria-selected={i === hi} onMouseEnter={() => setHi(i)} onClick={() => pick(p)}
              className={`flex w-full items-center gap-3 rounded-lg px-3 py-2 text-left cursor-pointer ${i === hi ? 'bg-brand-50' : ''}`}>
              <Avatar name={p.name} className="h-8 w-8 text-[10px]" />
              <div className="min-w-0 flex-1">
                <div className="truncate text-sm font-semibold">{p.name}</div>
                <div className="truncate text-xs text-slate-500">{[p.phone, p.gstin, p.state_code && STATES[p.state_code]].filter(Boolean).join(' · ') || p.type}</div>
              </div>
              {p.balance_paise !== 0 && <Money paise={p.balance_paise} signed className="text-xs" />}
            </button>
          ))}
          {!list.isLoading && dq.trim() && items.length === 0 && <div className="px-3 py-2 text-sm text-slate-500">No {type} matches “{dq}”</div>}
          {canDo(PERMS.partyManage) && q.trim() && (
            <button type="button" onClick={() => setAdding(q.trim())} className="flex w-full items-center gap-2 rounded-lg px-3 py-2 text-left text-sm font-semibold text-brand-700 hover:bg-brand-50 cursor-pointer">
              <Plus size={14} /> Add “{q.trim()}” as a new {type}
            </button>
          )}
        </div>
      )}
      <QuickAddPartyModal open={adding !== null} onClose={() => setAdding(null)} type={type} presetName={adding ?? ''} onCreated={(p) => { setAdding(null); pick(p) }} />
    </div>
  )
}

/** Name + phone (+ GSTIN) is enough at the counter; the state follows the GSTIN. */
export function QuickAddPartyModal({ open, onClose, type, presetName, onCreated }: {
  open: boolean; onClose: () => void; type: PartyType; presetName?: string; onCreated: (p: Party) => void
}) {
  const [f, setF] = useState<PartyInput>({ name: '', phone: '', gstin: '', state_code: '', credit_limit_paise: 0, credit_days: 0 })
  const [err, setErr] = useState<string | null>(null)
  useEffect(() => { if (open) { setF({ name: presetName ?? '', phone: '', gstin: '', state_code: '', credit_limit_paise: 0, credit_days: 0 }); setErr(null) } }, [open, presetName])
  const save = useSave<Party, PartyInput>('/bahi/v1/parties', { invalidate: ['/bahi/v1/parties'], silent: true })
  const gstin = (f.gstin ?? '').trim().toUpperCase()
  const gstErr = gstin ? validateGSTIN(gstin) : null

  const submit = async () => {
    setErr(null)
    if (!f.name.trim()) return setErr('Name is required')
    if (gstErr) return setErr(gstErr)
    const body: PartyInput = { name: f.name.trim(), type }
    if (f.phone?.trim()) body.phone = f.phone.trim()
    if (gstin) { body.gstin = gstin; body.state_code = gstin.slice(0, 2) } else if (f.state_code) body.state_code = f.state_code
    if (f.credit_limit_paise) body.credit_limit_paise = f.credit_limit_paise
    if (f.credit_days) body.credit_days = f.credit_days
    try {
      onCreated(await save.mutateAsync(body))
    } catch (e) {
      setErr((e as Error).message)
    }
  }

  return (
    <Modal open={open} onClose={onClose} title={`New ${type}`}
      footer={<><Button variant="secondary" onClick={onClose}>Cancel</Button><Button onClick={submit} loading={save.isPending}>Save {type}</Button></>}>
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label="Name" className="sm:col-span-2"><Input autoFocus value={f.name} onChange={(e) => setF({ ...f, name: e.target.value })} onKeyDown={(e) => e.key === 'Enter' && submit()} /></Field>
        <Field label="Mobile" hint="10 digits; used for WhatsApp receipts and UPI requests"><Input inputMode="tel" value={f.phone} onChange={(e) => setF({ ...f, phone: e.target.value })} placeholder="98765 43210" /></Field>
        <Field label="GSTIN (optional)" error={gstErr ?? undefined} hint={gstin && !gstErr ? `Registered in ${stateName(gstin.slice(0, 2))}` : undefined}>
          <Input className="font-mono uppercase" value={f.gstin} onChange={(e) => setF({ ...f, gstin: e.target.value.toUpperCase() })} maxLength={15} />
        </Field>
        {!gstin && (
          <Field label="State (optional)" hint="Needed for IGST on inter-state customers">
            <Select value={f.state_code} onChange={(e) => setF({ ...f, state_code: e.target.value })}>
              <option value="">Same as store</option>
              {Object.entries(STATES).map(([c, n]) => <option key={c} value={c}>{c} · {n}</option>)}
            </Select>
          </Field>
        )}
        {type === 'customer' && (
          <>
            <Field label="Credit limit (optional)"><MoneyInput value={f.credit_limit_paise ?? 0} onChange={(v) => setF({ ...f, credit_limit_paise: v })} /></Field>
            <Field label="Credit days"><Input type="number" inputMode="numeric" min={0} value={f.credit_days ?? 0} onChange={(e) => setF({ ...f, credit_days: Math.max(0, Number(e.target.value) || 0) })} /></Field>
          </>
        )}
      </div>
      {err && <div className="mt-3 rounded-xl bg-rose-50 px-3 py-2 text-sm text-rose-700">{err}</div>}
      <div className="mt-3"><Badge tone="gray">{type}</Badge></div>
    </Modal>
  )
}
