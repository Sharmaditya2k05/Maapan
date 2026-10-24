'use client'

/** Debounced item-master search (GET /bhandar/v1/items?q=) for the billing editors. */
import { useEffect, useId, useLayoutEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { Plus, Search } from 'lucide-react'
import { Money, Spinner } from '@/components/ui'
import { useDebounced, useList } from '@/lib/hooks'
import { formatRate } from '@/lib/money'
import { fmtQty } from '@/lib/hooks'
import type { ItemFull } from '@/lib/types.pos'

export function ItemPicker({ onPick, onFreeText, placeholder = 'Search item by name, SKU or barcode', autoFocus, priceField = 'sale_price_paise', small }: {
  onPick: (it: ItemFull) => void
  onFreeText?: (name: string) => void
  placeholder?: string
  autoFocus?: boolean
  priceField?: 'sale_price_paise' | 'purchase_price_paise'
  small?: boolean
}) {
  const [q, setQ] = useState('')
  const dq = useDebounced(q)
  const [open, setOpen] = useState(false)
  const [hi, setHi] = useState(0)
  const box = useRef<HTMLDivElement>(null)
  const pop = useRef<HTMLDivElement>(null)
  const [rect, setRect] = useState<{ top: number; left: number; width: number } | null>(null)
  const listId = useId()
  const list = useList<ItemFull>(open && dq.trim() ? '/bhandar/v1/items' : null, { q: dq.trim(), limit: 10 })
  const items = list.data?.items ?? []

  useEffect(() => {
    const h = (e: MouseEvent) => {
      const t = e.target as Node
      if (!box.current?.contains(t) && !pop.current?.contains(t)) setOpen(false)
    }
    document.addEventListener('mousedown', h)
    return () => document.removeEventListener('mousedown', h)
  }, [])
  useEffect(() => setHi(0), [dq])

  // The results popup is portalled to <body> at the input's screen position, so it is
  // never clipped by a scrolling table (line grids) or a modal's overflow.
  const showList = open && q.trim() !== ''
  useLayoutEffect(() => {
    if (!showList) return
    const place = () => {
      const r = box.current?.getBoundingClientRect()
      if (r) setRect({ top: r.bottom + 6, left: Math.min(r.left, window.innerWidth - Math.max(r.width, 288) - 8), width: Math.max(r.width, 288) })
    }
    place()
    window.addEventListener('scroll', place, true)
    window.addEventListener('resize', place)
    return () => { window.removeEventListener('scroll', place, true); window.removeEventListener('resize', place) }
  }, [showList])

  const pick = (it: ItemFull) => { onPick(it); setQ(''); setOpen(false) }
  const free = () => { if (onFreeText && q.trim()) { onFreeText(q.trim()); setQ(''); setOpen(false) } }

  return (
    <div ref={box} className="relative">
      <Search size={14} className="pointer-events-none absolute left-2.5 top-1/2 -translate-y-1/2 text-slate-400" />
      <input
        className={`input pl-8 ${small ? 'input-sm' : ''}`}
        value={q}
        autoFocus={autoFocus}
        placeholder={placeholder}
        onFocus={() => setOpen(true)}
        onChange={(e) => { setQ(e.target.value); setOpen(true) }}
        onKeyDown={(e) => {
          if (e.key === 'ArrowDown') { e.preventDefault(); setHi((h) => Math.min(h + 1, items.length - 1)) }
          else if (e.key === 'ArrowUp') { e.preventDefault(); setHi((h) => Math.max(h - 1, 0)) }
          else if (e.key === 'Enter') { e.preventDefault(); if (items[hi]) pick(items[hi]); else free() }
          else if (e.key === 'Escape') { setOpen(false); setQ('') }
        }}
        role="combobox"
        aria-autocomplete="list"
        aria-controls={listId}
        aria-label="Search items"
        aria-expanded={open}
      />
      {showList && rect && typeof document !== 'undefined' && createPortal(
        <div ref={pop} style={{ position: 'fixed', top: rect.top, left: rect.left, width: rect.width }}
          className="z-[60] max-h-72 overflow-y-auto rounded-xl border border-slate-200 bg-white p-1 shadow-xl" role="listbox" id={listId}>
          {list.isLoading && <div className="flex items-center gap-2 px-3 py-2 text-sm text-slate-500"><Spinner className="h-4 w-4" /> Searching…</div>}
          {list.error && <div className="px-3 py-2 text-sm text-rose-600">{(list.error as Error).message}</div>}
          {items.map((it, i) => (
            <button key={it.id} type="button" role="option" aria-selected={i === hi} onMouseEnter={() => setHi(i)} onClick={() => pick(it)}
              className={`block w-full rounded-lg px-3 py-2 text-left cursor-pointer ${i === hi ? 'bg-brand-50' : ''}`}>
              <div className="flex items-center justify-between gap-2 text-sm font-semibold"><span className="truncate">{it.name}</span><Money paise={it[priceField]} className="text-xs" /></div>
              <div className="truncate text-xs text-slate-500">{[it.sku, it.hsn && `HSN ${it.hsn}`, `GST ${formatRate(it.tax_rate_bp)}`, it.unit, it.track_batches ? 'batches' : ''].filter(Boolean).join(' · ')}{it.mrp_paise ? ` · MRP ${fmtQty(it.mrp_paise / 100)}` : ''}</div>
            </button>
          ))}
          {!list.isLoading && dq.trim() && items.length === 0 && <div className="px-3 py-2 text-sm text-slate-500">No item matches “{dq}”</div>}
          {onFreeText && (
            <button type="button" onClick={free} className="flex w-full items-center gap-2 rounded-lg px-3 py-2 text-left text-sm font-semibold text-brand-700 hover:bg-brand-50 cursor-pointer">
              <Plus size={14} /> Add “{q.trim()}” as a free-text line
            </button>
          )}
        </div>,
        document.body,
      )}
    </div>
  )
}
