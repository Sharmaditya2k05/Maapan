'use client'

/** The bill's lines: qty +/- and typed entry, price, discount, line total, delete. Table from md, cards below. */
import { useEffect, useState } from 'react'
import { Minus, Pencil, Plus, ShoppingBasket, Trash2 } from 'lucide-react'
import { Empty, IconButton, Money } from '@/components/ui'
import { fmtQty } from '@/lib/hooks'
import { formatRate } from '@/lib/money'
import type { GstSummary } from '@/lib/gst'
import type { CartLine } from '@/lib/types.pos'
import { discountAllowed, lineDiscount, round3 } from '@/lib/pos/cart'

function QtyBox({ value, onChange, unit }: { value: number; onChange: (q: number) => void; unit: string }) {
  const [text, setText] = useState(String(value))
  const [focused, setFocused] = useState(false)
  useEffect(() => { if (!focused) setText(String(value)) }, [value, focused])
  const step = unit === 'KG' || unit === 'LTR' || unit === 'MTR' ? 0.5 : 1
  return (
    <div className="inline-flex items-center rounded-xl border border-slate-200 bg-white">
      <button type="button" onClick={() => onChange(round3(value - step))} className="grid h-9 w-9 place-items-center rounded-l-xl text-slate-600 hover:bg-slate-100 cursor-pointer" aria-label="Decrease quantity"><Minus size={14} /></button>
      <input
        inputMode="decimal"
        className="num h-9 w-16 border-x border-slate-200 text-center text-sm font-semibold outline-none"
        value={text}
        aria-label="Quantity"
        onFocus={(e) => { setFocused(true); e.target.select() }}
        onBlur={() => { setFocused(false); const n = Number(text); if (Number.isFinite(n) && n !== value) onChange(n) }}
        onChange={(e) => setText(e.target.value.replace(/[^0-9.]/g, ''))}
        onKeyDown={(e) => { if (e.key === 'Enter') (e.target as HTMLInputElement).blur() }}
      />
      <button type="button" onClick={() => onChange(round3(value + step))} className="grid h-9 w-9 place-items-center rounded-r-xl text-slate-600 hover:bg-slate-100 cursor-pointer" aria-label="Increase quantity"><Plus size={14} /></button>
    </div>
  )
}

export function CartTable({ lines, totals, onQty, onEdit, onRemove, role, unlocked }: {
  lines: CartLine[]
  totals: GstSummary
  onQty: (key: string, qty: number) => void
  onEdit: (line: CartLine) => void
  onRemove: (key: string) => void
  role: string | undefined
  unlocked: boolean
}) {
  if (lines.length === 0) {
    return <Empty icon={ShoppingBasket} title="Bill is empty" text="Scan a barcode, search by name, or tap a quick item to start." />
  }
  const sub = (l: CartLine) => [l.hsn && `HSN ${l.hsn}`, l.batch_no && `Batch ${l.batch_no}`, l.mrp_paise ? `MRP ₹${(l.mrp_paise / 100).toFixed(2)}` : '', l.tax_category !== 'taxable' ? l.tax_category.replace('_', ' ') : `GST ${formatRate(l.tax_rate_bp)}`, l.free_text ? 'free text' : ''].filter(Boolean).join(' · ')
  const disc = (l: CartLine) => {
    const d = lineDiscount(l)
    if (!d) return null
    const bad = !discountAllowed(l, role, unlocked)
    return <span className={`text-xs ${bad ? 'font-semibold text-rose-600' : 'text-emerald-700'}`}>−<Money paise={d} />{l.discount_bp ? ` (${formatRate(l.discount_bp)})` : ''}{bad ? ' · needs manager' : ''}</span>
  }

  return (
    <>
      {/* mobile */}
      <ul className="divide-y divide-slate-100 md:hidden">
        {lines.map((l, i) => (
          <li key={l.key} className="flex items-start gap-3 px-3 py-3">
            <div className="min-w-0 flex-1">
              <button type="button" onClick={() => onEdit(l)} className="block w-full text-left cursor-pointer">
                <div className="truncate text-sm font-semibold">{l.name}</div>
                <div className="truncate text-xs text-slate-500">{sub(l)}</div>
                <div className="mt-0.5 text-xs text-slate-600"><Money paise={l.unit_price_paise} /> / {l.unit} {disc(l)}</div>
              </button>
              <div className="mt-2 flex items-center justify-between">
                <QtyBox value={l.qty} unit={l.unit} onChange={(q) => onQty(l.key, q)} />
                <Money paise={totals.lines[i]?.total_paise ?? 0} className="text-base font-bold" />
              </div>
            </div>
            <IconButton icon={Trash2} label={`Remove ${l.name}`} onClick={() => onRemove(l.key)} className="text-slate-400 hover:text-rose-600" />
          </li>
        ))}
      </ul>
      {/* desktop */}
      <table className="hidden w-full md:table">
        <thead className="sticky top-0 z-[1] bg-slate-50/95 backdrop-blur">
          <tr><th className="th w-8">#</th><th className="th">Item</th><th className="th w-40">Qty</th><th className="th w-16">Unit</th><th className="th w-28 text-right">Price</th><th className="th w-28 text-right">Discount</th><th className="th w-32 text-right">Total</th><th className="w-20" /></tr>
        </thead>
        <tbody className="divide-y divide-slate-100">
          {lines.map((l, i) => (
            <tr key={l.key} className="group hover:bg-slate-50/60">
              <td className="td text-xs text-slate-400">{i + 1}</td>
              <td className="td">
                <button type="button" onClick={() => onEdit(l)} className="block max-w-full text-left cursor-pointer">
                  <div className="truncate font-semibold">{l.name}</div>
                  <div className="truncate text-xs text-slate-500">{sub(l)}</div>
                </button>
              </td>
              <td className="td"><QtyBox value={l.qty} unit={l.unit} onChange={(q) => onQty(l.key, q)} /></td>
              <td className="td text-xs text-slate-500">{l.unit}</td>
              <td className="td num text-right"><button type="button" onClick={() => onEdit(l)} className="cursor-pointer hover:underline" aria-label={`Edit price of ${l.name}`}><Money paise={l.unit_price_paise} /></button></td>
              <td className="td text-right">{disc(l) ?? <button type="button" onClick={() => onEdit(l)} className="text-xs text-slate-400 hover:text-brand-700 cursor-pointer">add</button>}</td>
              <td className="td num text-right font-bold"><Money paise={totals.lines[i]?.total_paise ?? 0} /></td>
              <td className="td">
                <div className="flex justify-end gap-0.5 opacity-60 group-hover:opacity-100">
                  <IconButton icon={Pencil} label={`Edit ${l.name}`} onClick={() => onEdit(l)} />
                  <IconButton icon={Trash2} label={`Remove ${l.name}`} onClick={() => onRemove(l.key)} className="hover:text-rose-600" />
                </div>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      <div className="px-4 py-2 text-xs text-slate-500 md:hidden">{lines.length} line{lines.length === 1 ? '' : 's'} · qty {fmtQty(lines.reduce((a, l) => a + l.qty, 0))}</div>
    </>
  )
}
