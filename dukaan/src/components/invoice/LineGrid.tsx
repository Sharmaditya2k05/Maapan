'use client'

/**
 * Editable invoice lines for the B2B sales and purchase editors: item picker or
 * free text, HSN, qty, unit, price, discount (% or ₹), GST %, batch, amount.
 * Table from md up, stacked cards below. Pure UI over CartLine[].
 */
import { Fragment } from 'react'
import { Plus, Trash2 } from 'lucide-react'
import { Button, IconButton, Money, Select } from '@/components/ui'
import { GST_SLABS_BP } from '@/lib/gst'
import { formatRate } from '@/lib/money'
import type { CartLine, ItemFull } from '@/lib/types.pos'
import { cartTotals, freeTextLine, lineFromItem, newKey, round3, updateLine } from '@/lib/pos/cart'
import { ItemPicker } from './ItemPicker'

export type LineMode = 'sale' | 'purchase'

export function LineGrid({ lines, onChange, mode, storeState, placeOfSupply, disabled }: {
  lines: CartLine[]
  onChange: (lines: CartLine[]) => void
  mode: LineMode
  storeState: string
  placeOfSupply: string
  disabled?: boolean
}) {
  const purchase = mode === 'purchase'
  const set = (key: string, patch: Partial<CartLine>) => onChange(updateLine(lines, key, patch))
  const remove = (key: string) => onChange(lines.filter((l) => l.key !== key))
  const addEmpty = () => onChange([...lines, { ...freeTextLine({ name: '', unit_price_paise: 0, tax_rate_bp: 1800, price_includes_tax: !purchase }), key: newKey(), free_text: false }])
  const pickItem = (key: string, it: ItemFull) => {
    const l = lineFromItem(it, 1)
    const price = purchase ? it.purchase_price_paise || it.sale_price_paise : l.unit_price_paise
    onChange(updateLine(lines, key, { ...l, key, qty: lines.find((x) => x.key === key)?.qty || 1, unit_price_paise: price, price_includes_tax: purchase ? false : l.price_includes_tax, free_text: false }))
  }
  const freeText = (key: string, name: string) => set(key, { name, free_text: true, item_id: undefined, mrp_paise: 0, track_batches: false })

  const amount = (l: CartLine) => {
    if (!l.name || l.qty <= 0) return null
    try { return cartTotals([l], storeState, placeOfSupply, false).grand_total_paise } catch { return null }
  }
  const taxLocked = (l: CartLine) => !purchase && !!l.item_id
  const showBatch = lines.some((l) => l.track_batches || l.batch_no) || purchase

  const nameCell = (l: CartLine) =>
    l.name || l.item_id ? (
      <div>
        {l.free_text && !l.item_id ? (
          <input className="input input-sm" value={l.name} disabled={disabled} onChange={(e) => set(l.key, { name: e.target.value })} aria-label="Line description" />
        ) : (
          <div className="text-sm font-semibold">{l.name}</div>
        )}
        <div className="mt-0.5 text-[11px] text-slate-500">
          {[l.item_id ? 'Item master' : 'Free text', l.mrp_paise ? `MRP ₹${(l.mrp_paise / 100).toFixed(2)}` : '', l.price_includes_tax ? 'incl. tax' : 'excl. tax'].filter(Boolean).join(' · ')}
          {!disabled && <> · <button type="button" className="link cursor-pointer" onClick={() => set(l.key, { price_includes_tax: !l.price_includes_tax })}>{l.price_includes_tax ? 'make exclusive' : 'make inclusive'}</button></>}
        </div>
      </div>
    ) : (
      <ItemPicker onPick={(it) => pickItem(l.key, it)} onFreeText={(n) => freeText(l.key, n)} priceField={purchase ? 'purchase_price_paise' : 'sale_price_paise'} small autoFocus />
    )

  const qtyCell = (l: CartLine) => (
    <input type="number" inputMode="decimal" min={0} step="any" className="input input-sm num" value={l.qty || ''} disabled={disabled}
      onChange={(e) => set(l.key, { qty: round3(Math.max(0, Number(e.target.value) || 0)) })} aria-label="Quantity" />
  )
  const unitCell = (l: CartLine) => (
    <input className="input input-sm uppercase" value={l.unit} disabled={disabled || !!l.item_id} onChange={(e) => set(l.key, { unit: e.target.value.toUpperCase() })} aria-label="Unit" />
  )
  const hsnCell = (l: CartLine) => (
    <input className="input input-sm font-mono" value={l.hsn ?? ''} disabled={disabled || !!l.item_id} onChange={(e) => set(l.key, { hsn: e.target.value.replace(/\D/g, '').slice(0, 8) })} aria-label="HSN" placeholder="HSN" />
  )
  const priceCell = (l: CartLine) => (
    <input type="number" inputMode="decimal" min={0} step="0.01" className="input input-sm num" value={l.unit_price_paise ? (l.unit_price_paise / 100).toString() : ''} disabled={disabled}
      onChange={(e) => {
        const p = Math.round((Number(e.target.value) || 0) * 100)
        set(l.key, { unit_price_paise: !purchase && l.mrp_paise > 0 ? Math.min(p, l.mrp_paise) : p })
      }} aria-label="Unit price" />
  )
  const discCell = (l: CartLine) => {
    const pct = l.discount_paise === 0
    return (
      <div className="flex">
        <input type="number" inputMode="decimal" min={0} step="any" className="input input-sm num rounded-r-none" disabled={disabled}
          value={pct ? (l.discount_bp ? l.discount_bp / 100 : '') : l.discount_paise / 100}
          onChange={(e) => {
            const v = Math.max(0, Number(e.target.value) || 0)
            if (pct) set(l.key, { discount_bp: Math.min(10000, Math.round(v * 100)), discount_paise: 0 })
            else set(l.key, { discount_paise: Math.round(v * 100), discount_bp: 0 })
          }} aria-label="Discount" />
        <button type="button" disabled={disabled} onClick={() => set(l.key, pct ? { discount_paise: 1, discount_bp: 0 } : { discount_paise: 0 })}
          className="rounded-r-lg border border-l-0 border-slate-200 bg-slate-50 px-2 text-xs font-semibold text-slate-600 hover:bg-slate-100 cursor-pointer" aria-label="Toggle discount type">{pct ? '%' : '₹'}</button>
      </div>
    )
  }
  const taxCell = (l: CartLine) => (
    <Select className="input-sm" value={l.tax_rate_bp} disabled={disabled || taxLocked(l)} onChange={(e) => set(l.key, { tax_rate_bp: Number(e.target.value), tax_category: 'taxable' })} aria-label="GST rate" title={taxLocked(l) ? 'Sales must carry the item master rate' : undefined}>
      {GST_SLABS_BP.map((bp) => <option key={bp} value={bp}>{formatRate(bp)}</option>)}
      {!GST_SLABS_BP.includes(l.tax_rate_bp) && <option value={l.tax_rate_bp}>{formatRate(l.tax_rate_bp)}</option>}
    </Select>
  )
  const batchCell = (l: CartLine) => (
    <input className="input input-sm font-mono" value={l.batch_no ?? ''} disabled={disabled} placeholder={l.track_batches ? 'Batch *' : 'Batch'} onChange={(e) => set(l.key, { batch_no: e.target.value || undefined })} aria-label="Batch number" />
  )

  return (
    <div>
      {/* mobile */}
      <div className="divide-y divide-slate-100 md:hidden">
        {lines.map((l, i) => (
          <div key={l.key} className="space-y-2 p-3">
            <div className="flex items-start gap-2">
              <span className="mt-1.5 grid h-6 w-6 shrink-0 place-items-center rounded-full bg-brand-50 text-[11px] font-bold text-brand-700">{i + 1}</span>
              <div className="min-w-0 flex-1">{nameCell(l)}</div>
              {!disabled && <IconButton icon={Trash2} label="Remove line" onClick={() => remove(l.key)} className="text-slate-400 hover:text-rose-600" />}
            </div>
            <div className="grid grid-cols-3 gap-2 pl-8">
              <label className="block"><span className="label">Qty</span>{qtyCell(l)}</label>
              <label className="block"><span className="label">Unit</span>{unitCell(l)}</label>
              <label className="block"><span className="label">HSN</span>{hsnCell(l)}</label>
              <label className="block"><span className="label">Price ₹</span>{priceCell(l)}</label>
              <label className="block"><span className="label">Disc</span>{discCell(l)}</label>
              <label className="block"><span className="label">GST</span>{taxCell(l)}</label>
              {showBatch && <label className="block col-span-2"><span className="label">Batch</span>{batchCell(l)}</label>}
            </div>
            <div className="flex justify-end pl-8 text-sm font-bold"><Money paise={amount(l) ?? 0} /></div>
          </div>
        ))}
      </div>
      {/* desktop */}
      <div className="hidden overflow-x-auto md:block">
        <table className="w-full min-w-[900px]">
          <thead className="bg-slate-50/60">
            <tr>
              <th className="th w-8">#</th><th className="th">Item</th><th className="th w-24">HSN</th><th className="th w-24">Qty</th><th className="th w-20">Unit</th>
              <th className="th w-28">{purchase ? 'Cost ₹' : 'Price ₹'}</th><th className="th w-32">Discount</th><th className="th w-24">GST</th>
              {showBatch && <th className="th w-28">Batch</th>}
              <th className="th text-right">Amount</th><th className="w-10" />
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-100">
            {lines.map((l, i) => (
              <Fragment key={l.key}>
                <tr className="align-top">
                  <td className="td pt-4 text-xs text-slate-400">{i + 1}</td>
                  <td className="px-2 py-2.5">{nameCell(l)}</td>
                  <td className="px-2 py-2.5">{hsnCell(l)}</td>
                  <td className="px-2 py-2.5">{qtyCell(l)}</td>
                  <td className="px-2 py-2.5">{unitCell(l)}</td>
                  <td className="px-2 py-2.5">{priceCell(l)}</td>
                  <td className="px-2 py-2.5">{discCell(l)}</td>
                  <td className="px-2 py-2.5">{taxCell(l)}</td>
                  {showBatch && <td className="px-2 py-2.5">{batchCell(l)}</td>}
                  <td className="td num pt-4 text-right font-semibold"><Money paise={amount(l) ?? 0} /></td>
                  <td className="py-2.5 pr-2">{!disabled && <IconButton icon={Trash2} label="Remove line" onClick={() => remove(l.key)} className="text-slate-400 hover:text-rose-600" />}</td>
                </tr>
              </Fragment>
            ))}
          </tbody>
        </table>
      </div>
      {lines.length === 0 && <p className="px-4 py-6 text-center text-sm text-slate-500">No lines yet — add an item from the master or type a description.</p>}
      {!disabled && <div className="border-t border-slate-100 p-3"><Button variant="ghost" icon={Plus} onClick={addEmpty} className="w-full md:w-auto">Add line</Button></div>}
    </div>
  )
}
