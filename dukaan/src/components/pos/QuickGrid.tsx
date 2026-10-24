'use client'

/** Tap-to-add grid of the counter's most used items. */
import { Money } from '@/components/ui'
import type { CatalogItemFull } from '@/lib/types.pos'

export function QuickGrid({ items, onAdd }: { items: CatalogItemFull[]; onAdd: (it: CatalogItemFull) => void }) {
  if (items.length === 0) return null
  return (
    <div>
      <div className="mb-2 text-[11px] font-bold uppercase tracking-wider text-slate-400">Quick items</div>
      <div className="grid grid-cols-2 gap-2 sm:grid-cols-3 md:grid-cols-2 xl:grid-cols-3">
        {items.map((it) => (
          <button key={it.id} type="button" onClick={() => onAdd(it)}
            className="flex min-h-[64px] flex-col justify-between rounded-xl border border-slate-200 bg-white px-3 py-2 text-left transition hover:border-brand-300 hover:bg-brand-50 active:scale-[.98] cursor-pointer">
            <span className="line-clamp-2 text-xs font-semibold leading-tight">{it.name}</span>
            <span className="mt-1 flex items-center justify-between text-[11px] text-slate-500"><span>{it.unit}</span><Money paise={it.sale_price_paise} className="font-semibold text-ink" /></span>
          </button>
        ))}
      </div>
    </div>
  )
}
