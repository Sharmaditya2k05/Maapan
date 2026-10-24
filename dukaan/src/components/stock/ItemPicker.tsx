'use client'

import { useEffect, useId, useRef, useState } from 'react'
import { Loader2, Search } from 'lucide-react'
import { Money } from '@/components/ui'
import { useDebounced, useList } from '@/lib/hooks'
import { normalizeCode } from '@/lib/stock/ean'
import type { ItemFull } from '@/lib/types.stock'

/**
 * Search-as-you-type item picker (name contains, or exact SKU / barcode / PLU — the
 * same matching as GET /bhandar/v1/items). Enter picks the exact code match first,
 * so a barcode scanner works out of the box.
 */
export function ItemPicker({ onPick, placeholder = 'Search item by name, SKU or scan a barcode', autoFocus, className = '', disabled }: {
  onPick: (item: ItemFull) => void; placeholder?: string; autoFocus?: boolean; className?: string; disabled?: boolean
}) {
  const [q, setQ] = useState('')
  const [open, setOpen] = useState(false)
  const [active, setActive] = useState(0)
  const dq = useDebounced(q, 200)
  const list = useList<ItemFull>(dq.trim() ? '/bhandar/v1/items' : null, { q: dq.trim(), limit: 10 })
  const items = list.data?.items ?? []
  const wrap = useRef<HTMLDivElement>(null)
  const listId = useId()

  useEffect(() => {
    const h = (e: MouseEvent) => { if (!wrap.current?.contains(e.target as Node)) setOpen(false) }
    window.addEventListener('mousedown', h)
    return () => window.removeEventListener('mousedown', h)
  }, [])

  const pick = (it: ItemFull) => { onPick(it); setQ(''); setOpen(false); setActive(0) }

  const onKey = (e: React.KeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'ArrowDown') { e.preventDefault(); setOpen(true); setActive((a) => Math.min(items.length - 1, a + 1)) }
    else if (e.key === 'ArrowUp') { e.preventDefault(); setActive((a) => Math.max(0, a - 1)) }
    else if (e.key === 'Escape') { setOpen(false) }
    else if (e.key === 'Enter') {
      e.preventDefault()
      if (!items.length) return
      const code = normalizeCode(q)
      const exact = items.find((i) => i.barcode === code || i.sku === code || i.plu === code)
      pick(exact ?? items[active] ?? items[0])
    }
  }

  return (
    <div ref={wrap} className={`relative ${className}`}>
      <div className="relative">
        {list.isFetching ? <Loader2 size={16} className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 animate-spin text-brand-600" /> : <Search size={16} className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-slate-400" />}
        <input
          role="combobox" aria-expanded={open && items.length > 0} aria-controls={listId} aria-autocomplete="list" aria-label="Search items"
          autoFocus={autoFocus} disabled={disabled} className="input pl-9" value={q} placeholder={placeholder}
          onChange={(e) => { setQ(e.target.value); setOpen(true); setActive(0) }} onFocus={() => setOpen(true)} onKeyDown={onKey}
        />
      </div>
      {open && q.trim() && (
        <ul id={listId} role="listbox" className="absolute z-40 mt-1 max-h-72 w-full overflow-y-auto rounded-2xl border border-slate-200 bg-white py-1 shadow-xl">
          {list.error ? <li className="px-4 py-3 text-sm text-rose-600">{(list.error as Error).message}</li>
            : items.length === 0 && !list.isFetching ? <li className="px-4 py-3 text-sm text-slate-500">No items match “{q}”.</li>
            : items.map((it, i) => (
              <li key={it.id} role="option" aria-selected={i === active}
                onMouseDown={(e) => { e.preventDefault(); pick(it) }} onMouseEnter={() => setActive(i)}
                className={`flex cursor-pointer items-center gap-3 px-4 py-2.5 text-sm ${i === active ? 'bg-brand-50' : 'hover:bg-slate-50'}`}>
                <div className="min-w-0 flex-1">
                  <div className="truncate font-semibold">{it.name}</div>
                  <div className="truncate text-xs text-slate-500">{[it.sku, it.barcode, it.unit].filter(Boolean).join(' · ')}{it.track_batches ? ' · batches' : ''}</div>
                </div>
                <Money paise={it.sale_price_paise} className="text-xs text-slate-600" />
              </li>
            ))}
        </ul>
      )}
    </div>
  )
}
