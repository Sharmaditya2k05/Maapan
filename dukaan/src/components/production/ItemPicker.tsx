'use client'

/**
 * Searchable item picker over bhandar GET /v1/items?q=. Used for the finished
 * item and every component row of a BOM. Keyboard: ↑ ↓ Enter Esc.
 */
import { useEffect, useId, useRef, useState } from 'react'
import { Loader2, Package, Search, X } from 'lucide-react'
import { useDebounced, useList } from '@/lib/hooks'
import type { Item } from '@/lib/types'

export interface PickedItem { id: string; name: string; unit: string; sku?: string; purchase_price_paise?: number }

export function ItemPicker({ value, onChange, placeholder = 'Search items by name, SKU or barcode', exclude = [], autoFocus, small, label }: {
  value: PickedItem | null
  onChange: (item: PickedItem | null) => void
  placeholder?: string
  /** Item ids hidden from the results (e.g. the finished item, already-added components). */
  exclude?: string[]
  autoFocus?: boolean
  small?: boolean
  /** aria-label for the search box when no visible <label> wraps it. */
  label?: string
}) {
  const [q, setQ] = useState('')
  const [open, setOpen] = useState(false)
  const [active, setActive] = useState(0)
  const dq = useDebounced(q.trim(), 200)
  const listId = useId()
  const rootRef = useRef<HTMLDivElement>(null)
  const list = useList<Item>('/bhandar/v1/items', { q: dq, limit: 10, status: 'active' }, { enabled: open && dq.length > 0 })
  const items = (list.data?.items ?? []).filter((i) => !exclude.includes(i.id))

  useEffect(() => {
    if (!open) return
    const h = (e: MouseEvent) => { if (rootRef.current && !rootRef.current.contains(e.target as Node)) setOpen(false) }
    document.addEventListener('mousedown', h)
    return () => document.removeEventListener('mousedown', h)
  }, [open])
  useEffect(() => setActive(0), [dq])

  const pick = (it: Item) => {
    onChange({ id: it.id, name: it.name, unit: it.unit, sku: it.sku, purchase_price_paise: it.purchase_price_paise })
    setQ('')
    setOpen(false)
  }

  if (value) {
    return (
      <div className={`input flex items-center gap-2 ${small ? 'input-sm' : ''}`}>
        <Package size={14} className="shrink-0 text-brand-600" />
        <span className="min-w-0 flex-1 truncate font-medium" title={value.name}>{value.name}</span>
        {value.sku && <span className="hidden text-xs text-slate-400 sm:inline">{value.sku}</span>}
        <button type="button" onClick={() => onChange(null)} className="rounded p-0.5 text-slate-400 hover:text-ink cursor-pointer" aria-label="Clear item"><X size={14} /></button>
      </div>
    )
  }

  return (
    <div ref={rootRef} className="relative">
      <Search size={14} className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-slate-400" />
      <input
        className={`input pl-8 ${small ? 'input-sm pl-8' : ''}`}
        value={q}
        autoFocus={autoFocus}
        placeholder={placeholder}
        aria-label={label ?? placeholder}
        role="combobox"
        aria-expanded={open && dq.length > 0}
        aria-controls={listId}
        aria-autocomplete="list"
        onChange={(e) => { setQ(e.target.value); setOpen(true) }}
        onFocus={() => setOpen(true)}
        onKeyDown={(e) => {
          if (e.key === 'ArrowDown') { e.preventDefault(); setActive((a) => Math.min(items.length - 1, a + 1)) }
          else if (e.key === 'ArrowUp') { e.preventDefault(); setActive((a) => Math.max(0, a - 1)) }
          else if (e.key === 'Enter' && items[active]) { e.preventDefault(); pick(items[active]) }
          else if (e.key === 'Escape') setOpen(false)
        }}
      />
      {open && dq.length > 0 && (
        <ul id={listId} role="listbox" className="absolute z-30 mt-1 max-h-64 w-full min-w-[260px] overflow-y-auto rounded-xl border border-slate-200 bg-white p-1 shadow-xl">
          {list.isLoading && <li className="flex items-center gap-2 px-3 py-2 text-sm text-slate-500"><Loader2 size={14} className="animate-spin" /> Searching…</li>}
          {list.error && <li className="px-3 py-2 text-sm text-rose-600">{(list.error as Error).message}</li>}
          {!list.isLoading && !list.error && items.length === 0 && <li className="px-3 py-2 text-sm text-slate-500">No items match “{dq}”</li>}
          {items.map((it, i) => (
            <li key={it.id} role="option" aria-selected={i === active}>
              <button type="button" onMouseDown={(e) => e.preventDefault()} onClick={() => pick(it)} onMouseEnter={() => setActive(i)}
                className={`flex w-full items-center gap-3 rounded-lg px-3 py-2 text-left text-sm cursor-pointer ${i === active ? 'bg-brand-50' : 'hover:bg-slate-50'}`}>
                <div className="min-w-0 flex-1">
                  <div className="truncate font-medium">{it.name}</div>
                  <div className="truncate text-xs text-slate-500">{[it.sku, it.hsn && `HSN ${it.hsn}`, it.unit].filter(Boolean).join(' · ')}</div>
                </div>
              </button>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
