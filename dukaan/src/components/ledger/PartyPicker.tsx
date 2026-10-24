'use client'

/**
 * Debounced party search (bahi GET /v1/parties?q=). Keyboard-navigable combobox.
 *
 *   const [party, setParty] = useState<Party | null>(null)
 *   <PartyPicker value={party} onChange={setParty} type="customer" />
 *
 * `onCreate` (optional) shows a "+ Add <text>" row at the bottom for screens that can open the party form.
 */
import { useEffect, useId, useRef, useState } from 'react'
import { Loader2, Plus, Search, X } from 'lucide-react'
import { useDebounced, useList } from '@/lib/hooks'
import type { Party } from '@/lib/types'
import { balanceLabel } from '@/lib/ledger/util'

export interface PartyPickerProps {
  value: Party | null
  onChange: (party: Party | null) => void
  /** Narrows the search; bahi returns "both" parties for either. */
  type?: 'customer' | 'supplier'
  placeholder?: string
  disabled?: boolean
  autoFocus?: boolean
  includeArchived?: boolean
  className?: string
  /** Called with the typed text when the user picks "Add new". */
  onCreate?: (name: string) => void
  id?: string
}

export function PartyPicker({ value, onChange, type, placeholder = 'Search party by name, phone or GSTIN', disabled, autoFocus, includeArchived, className = '', onCreate, id }: PartyPickerProps) {
  const [text, setText] = useState(value?.name ?? '')
  const [open, setOpen] = useState(false)
  const [hi, setHi] = useState(0)
  const dq = useDebounced(text.trim(), 250)
  const listId = useId()
  const inputId = id ?? `party-${listId}`
  const wrap = useRef<HTMLDivElement>(null)

  // Keep the box in sync when the parent sets or clears the value.
  useEffect(() => { setText(value?.name ?? '') }, [value?.id, value?.name])

  const q = useList<Party>(open && !disabled ? '/bahi/v1/parties' : null, { q: dq, type, include_archived: includeArchived ? true : undefined, limit: 20 })
  const items = q.data?.items ?? []
  const showCreate = !!onCreate && text.trim().length > 1 && !items.some((p) => p.name.toLowerCase() === text.trim().toLowerCase())
  const count = items.length + (showCreate ? 1 : 0)

  useEffect(() => {
    const h = (e: MouseEvent) => { if (!wrap.current?.contains(e.target as Node)) setOpen(false) }
    document.addEventListener('mousedown', h)
    return () => document.removeEventListener('mousedown', h)
  }, [])
  useEffect(() => { setHi(0) }, [dq, items.length])

  const pick = (p: Party) => { onChange(p); setText(p.name); setOpen(false) }
  const clear = () => { onChange(null); setText(''); setOpen(true) }

  return (
    <div ref={wrap} className={`relative ${className}`}>
      <Search size={16} className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-slate-400" />
      <input
        id={inputId}
        role="combobox"
        aria-expanded={open}
        aria-controls={listId}
        aria-autocomplete="list"
        aria-activedescendant={open && count ? `${listId}-${hi}` : undefined}
        autoComplete="off"
        autoFocus={autoFocus}
        disabled={disabled}
        className="input pl-9 pr-9"
        placeholder={placeholder}
        value={text}
        onFocus={() => setOpen(true)}
        onChange={(e) => { setText(e.target.value); setOpen(true); if (value && e.target.value !== value.name) onChange(null) }}
        onKeyDown={(e) => {
          if (e.key === 'ArrowDown') { e.preventDefault(); setOpen(true); setHi((h) => Math.min(count - 1, h + 1)) }
          else if (e.key === 'ArrowUp') { e.preventDefault(); setHi((h) => Math.max(0, h - 1)) }
          else if (e.key === 'Enter' && open) {
            e.preventDefault()
            if (hi < items.length) pick(items[hi]); else if (showCreate) { onCreate?.(text.trim()); setOpen(false) }
          } else if (e.key === 'Escape') setOpen(false)
        }}
      />
      {(text || value) && !disabled && (
        <button type="button" onClick={clear} aria-label="Clear party" className="absolute right-2 top-1/2 -translate-y-1/2 rounded p-1 text-slate-400 hover:text-ink cursor-pointer"><X size={14} /></button>
      )}
      {open && !disabled && (
        <ul id={listId} role="listbox" className="absolute z-30 mt-1 max-h-72 w-full overflow-auto rounded-xl border border-slate-200 bg-white py-1 shadow-xl">
          {q.isLoading && <li className="flex items-center gap-2 px-3 py-2 text-sm text-slate-500"><Loader2 size={14} className="animate-spin" /> Searching…</li>}
          {q.error && <li className="px-3 py-2 text-sm text-rose-600">{(q.error as Error).message}</li>}
          {!q.isLoading && !q.error && items.length === 0 && !showCreate && <li className="px-3 py-2 text-sm text-slate-500">{dq ? 'No party matches.' : 'Type to search parties.'}</li>}
          {items.map((p, i) => {
            const b = balanceLabel(p.balance_paise)
            return (
              <li key={p.id} id={`${listId}-${i}`} role="option" aria-selected={i === hi}
                onMouseEnter={() => setHi(i)} onMouseDown={(e) => { e.preventDefault(); pick(p) }}
                className={`flex cursor-pointer items-center gap-3 px-3 py-2 ${i === hi ? 'bg-brand-50' : ''}`}>
                <div className="min-w-0 flex-1">
                  <div className="truncate text-sm font-semibold">{p.name}{p.archived && <span className="ml-1 text-xs font-normal text-slate-400">(archived)</span>}</div>
                  <div className="truncate text-xs text-slate-500">{[p.type, p.phone, p.gstin].filter(Boolean).join(' · ')}</div>
                </div>
                <div className={`num shrink-0 text-xs font-semibold ${b.tone === 'green' ? 'text-emerald-700' : b.tone === 'red' ? 'text-rose-700' : 'text-slate-400'}`}>{b.text}</div>
              </li>
            )
          })}
          {showCreate && (
            <li id={`${listId}-${items.length}`} role="option" aria-selected={hi === items.length}
              onMouseEnter={() => setHi(items.length)} onMouseDown={(e) => { e.preventDefault(); onCreate?.(text.trim()); setOpen(false) }}
              className={`flex cursor-pointer items-center gap-2 border-t border-slate-100 px-3 py-2 text-sm font-semibold text-brand-700 ${hi === items.length ? 'bg-brand-50' : ''}`}>
              <Plus size={14} /> Add “{text.trim()}” as a new party
            </li>
          )}
        </ul>
      )}
    </div>
  )
}
