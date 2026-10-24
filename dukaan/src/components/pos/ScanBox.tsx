'use client'

/**
 * The counter's always-focused scan / search box. A USB scanner types the code
 * and Enter; typing words searches the cached catalog locally.
 */
import { useEffect, useMemo, useState, type RefObject } from 'react'
import { AlertTriangle, PackagePlus, ScanLine, Search } from 'lucide-react'
import { Kbd, Money } from '@/components/ui'
import { fmtQty, useDebounced } from '@/lib/hooks'
import type { CatalogItemFull } from '@/lib/types.pos'
import { looksLikeCode, searchCatalog } from '@/lib/pos/cart'

export function ScanBox({ inputRef, catalog, onScan, onAdd, onFreeText, flash, disabled }: {
  inputRef: RefObject<HTMLInputElement | null>
  catalog: CatalogItemFull[]
  onScan: (code: string) => void
  onAdd: (item: CatalogItemFull) => void
  onFreeText: () => void
  /** Message shown with a red flash (unknown code). */
  flash: string | null
  disabled?: boolean
}) {
  const [q, setQ] = useState('')
  const dq = useDebounced(q, 120)
  const [hi, setHi] = useState(0)
  const results = useMemo(() => (dq.trim() && !looksLikeCode(dq) ? searchCatalog(catalog, dq, 12) : []), [catalog, dq])
  useEffect(() => setHi(0), [dq])

  const submit = () => {
    const s = q.trim()
    if (!s) return
    if (!looksLikeCode(s) && results[hi]) onAdd(results[hi])
    else onScan(s)
    setQ('')
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className={`relative rounded-2xl transition ${flash ? 'ring-4 ring-rose-500/40' : ''}`}>
        <ScanLine size={18} className="pointer-events-none absolute left-3.5 top-1/2 -translate-y-1/2 text-brand-600" />
        <input
          ref={inputRef}
          className={`input h-12 pl-11 pr-20 text-base font-medium ${flash ? 'border-rose-400 bg-rose-50' : ''}`}
          value={q}
          disabled={disabled}
          autoFocus
          autoComplete="off"
          autoCorrect="off"
          spellCheck={false}
          placeholder="Scan barcode or type to search…"
          aria-label="Scan barcode or search items"
          onChange={(e) => setQ(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') { e.preventDefault(); submit() }
            else if (e.key === 'ArrowDown') { e.preventDefault(); setHi((h) => Math.min(h + 1, results.length - 1)) }
            else if (e.key === 'ArrowUp') { e.preventDefault(); setHi((h) => Math.max(h - 1, 0)) }
            else if (e.key === 'Escape') setQ('')
          }}
        />
        <div className="pointer-events-none absolute right-3 top-1/2 hidden -translate-y-1/2 items-center gap-1 sm:flex"><Kbd>Enter</Kbd></div>
      </div>
      {flash && (
        <div className="mt-2 flex items-center gap-2 rounded-xl bg-rose-600 px-3 py-2 text-sm font-semibold text-white" role="alert">
          <AlertTriangle size={16} /> {flash}
        </div>
      )}
      {dq.trim() && !looksLikeCode(dq) && (
        <ul className="mt-2 max-h-72 overflow-y-auto rounded-2xl border border-slate-200 bg-white p-1 shadow-sm" role="listbox" aria-label="Search results">
          {results.map((it, i) => (
            <li key={it.id}>
              <button type="button" role="option" aria-selected={i === hi} onMouseEnter={() => setHi(i)} onClick={() => { onAdd(it); setQ('') }}
                className={`flex w-full items-center gap-3 rounded-xl px-3 py-2 text-left cursor-pointer ${i === hi ? 'bg-brand-50' : 'hover:bg-slate-50'}`}>
                <div className="min-w-0 flex-1">
                  <div className="truncate text-sm font-semibold">{it.name}</div>
                  <div className="truncate text-xs text-slate-500">{[it.sku, it.barcode, `${fmtQty(it.on_hand)} ${it.unit} in stock`].filter(Boolean).join(' · ')}</div>
                </div>
                <Money paise={it.sale_price_paise} className="text-sm font-bold" />
              </button>
            </li>
          ))}
          {results.length === 0 && (
            <li className="flex items-center gap-2 px-3 py-2 text-sm text-slate-500"><Search size={14} /> Nothing in the catalog matches “{dq}”</li>
          )}
          <li>
            <button type="button" onClick={() => { onFreeText(); setQ('') }} className="flex w-full items-center gap-2 rounded-xl px-3 py-2 text-left text-sm font-semibold text-brand-700 hover:bg-brand-50 cursor-pointer">
              <PackagePlus size={14} /> Add “{dq}” as a free-text line
            </button>
          </li>
        </ul>
      )}
    </div>
  )
}
