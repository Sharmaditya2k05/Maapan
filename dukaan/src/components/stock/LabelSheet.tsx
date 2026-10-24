'use client'

import { formatINR } from '@/lib/money'
import type { Label } from '@/lib/types.stock'

const svgUrl = (svg: string) => `data:image/svg+xml;charset=utf-8,${encodeURIComponent(svg)}`

/** Sticker sheet: the server's barcode SVGs laid out in a grid, name above, code and MRP below. Printable (.print-root .print-a4). */
export function LabelSheet({ labels, columns, showPrice }: { labels: Label[]; columns: number; showPrice: boolean }) {
  const stickers = labels.flatMap((l) => Array.from({ length: Math.max(1, l.copies) }, (_, i) => ({ ...l, key: `${l.item_id}-${i}` })))
  return (
    <div className="print-root print-a4 mx-auto w-full max-w-[186mm] rounded-2xl border border-slate-200 bg-white p-3 shadow-sm print:border-0 print:p-0 print:shadow-none">
      <div className="grid gap-2" style={{ gridTemplateColumns: `repeat(${columns}, minmax(0, 1fr))` }}>
        {stickers.map((l) => (
          <div key={l.key} className="flex flex-col items-center rounded border border-dashed border-slate-300 px-1.5 py-2 text-center" style={{ breakInside: 'avoid' }}>
            <div className="w-full truncate text-[10px] font-semibold leading-tight" title={l.name}>{l.name}</div>
            {/* eslint-disable-next-line @next/next/no-img-element -- server-rendered SVG via data URL */}
            <img src={svgUrl(l.svg)} alt={`Barcode ${l.barcode}`} className="my-1 h-12 w-full object-contain" />
            <div className="font-mono text-[9px] tracking-wider text-slate-700">{l.barcode}</div>
            {showPrice && (l.mrp_paise || l.sale_price_paise) ? (
              <div className="text-[10px] font-bold leading-tight">
                {l.mrp_paise ? `MRP ${formatINR(l.mrp_paise)}` : `${formatINR(l.sale_price_paise ?? 0)}`}
                {l.mrp_paise && l.sale_price_paise && l.sale_price_paise !== l.mrp_paise ? <span className="ml-1 font-normal text-slate-500">Our price {formatINR(l.sale_price_paise)}</span> : null}
              </div>
            ) : null}
          </div>
        ))}
      </div>
    </div>
  )
}
