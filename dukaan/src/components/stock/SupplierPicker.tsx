'use client'

import { useEffect, useId, useRef, useState } from 'react'
import { Loader2, Search, X } from 'lucide-react'
import { useDebounced, useGet, useList } from '@/lib/hooks'
import type { Party } from '@/lib/types'

/** Supplier search (GET /bahi/v1/parties?type=supplier&q=). Holds a party id; shows the name as a removable chip. */
export function SupplierPicker({ value, onChange, placeholder = 'Search supplier by name or phone', disabled }: {
  value: string; onChange: (id: string, party?: Party) => void; placeholder?: string; disabled?: boolean
}) {
  const [q, setQ] = useState('')
  const [open, setOpen] = useState(false)
  const [picked, setPicked] = useState<Party | undefined>()
  const dq = useDebounced(q, 200)
  const list = useList<Party>(open ? '/bahi/v1/parties' : null, { type: 'supplier', q: dq.trim(), limit: 10 })
  const resolved = useGet<Party>(value && picked?.id !== value ? `/bahi/v1/parties/${value}` : null)
  const wrap = useRef<HTMLDivElement>(null)
  const listId = useId()

  useEffect(() => {
    const h = (e: MouseEvent) => { if (!wrap.current?.contains(e.target as Node)) setOpen(false) }
    window.addEventListener('mousedown', h)
    return () => window.removeEventListener('mousedown', h)
  }, [])

  const current = value ? (picked?.id === value ? picked : resolved.data) : undefined

  if (value) {
    return (
      <div className="flex min-h-10 items-center gap-2 rounded-xl border border-slate-200 bg-slate-50 px-3 py-1.5 text-sm">
        <span className="min-w-0 flex-1 truncate font-semibold">{current?.name ?? (resolved.isLoading ? 'Loading…' : `Supplier ${value.slice(0, 8)}`)}</span>
        {current?.phone && <span className="hidden text-xs text-slate-500 sm:inline">{current.phone}</span>}
        {!disabled && <button type="button" onClick={() => { onChange('', undefined); setPicked(undefined) }} className="rounded p-1 text-slate-400 hover:text-ink cursor-pointer" aria-label="Clear supplier"><X size={14} /></button>}
      </div>
    )
  }

  const items = list.data?.items ?? []
  return (
    <div ref={wrap} className="relative">
      <div className="relative">
        {list.isFetching ? <Loader2 size={16} className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 animate-spin text-brand-600" /> : <Search size={16} className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-slate-400" />}
        <input role="combobox" aria-expanded={open} aria-controls={listId} aria-autocomplete="list" aria-label="Search suppliers" disabled={disabled}
          className="input pl-9" value={q} placeholder={placeholder}
          onChange={(e) => { setQ(e.target.value); setOpen(true) }} onFocus={() => setOpen(true)}
          onKeyDown={(e) => { if (e.key === 'Escape') setOpen(false); if (e.key === 'Enter' && items[0]) { e.preventDefault(); setPicked(items[0]); onChange(items[0].id, items[0]); setOpen(false) } }} />
      </div>
      {open && (
        <ul id={listId} role="listbox" className="absolute z-40 mt-1 max-h-64 w-full overflow-y-auto rounded-2xl border border-slate-200 bg-white py-1 shadow-xl">
          {list.error ? <li className="px-4 py-3 text-sm text-rose-600">{(list.error as Error).message}</li>
            : items.length === 0 && !list.isFetching ? <li className="px-4 py-3 text-sm text-slate-500">{dq ? 'No suppliers match.' : 'Type to search suppliers.'}</li>
            : items.map((p) => (
              <li key={p.id} role="option" aria-selected={false} onMouseDown={(e) => { e.preventDefault(); setPicked(p); onChange(p.id, p); setOpen(false); setQ('') }}
                className="cursor-pointer px-4 py-2.5 text-sm hover:bg-slate-50">
                <div className="font-semibold">{p.name}</div>
                <div className="text-xs text-slate-500">{[p.phone, p.gstin].filter(Boolean).join(' · ') || p.type}</div>
              </li>
            ))}
        </ul>
      )}
    </div>
  )
}
