'use client'

import { useMemo, useState } from 'react'
import { Plus, Settings2, Trash2 } from 'lucide-react'
import { Button, Card, ErrorBox, Field, IconButton, Input, MoneyInput, Select, Toggle } from '@/components/ui'
import { useGet, useSave } from '@/lib/hooks'
import { errorMessage } from '@/lib/api'
import { GST_SLABS_BP } from '@/lib/gst'
import { normalizeCode, validateBarcode, validateHSN } from '@/lib/stock/ean'
import { TAX_CATEGORY_LABEL, type ItemFull, type ItemInput, type TaxCategory, type Unit } from '@/lib/types.stock'
import { SupplierPicker } from './SupplierPicker'
import { UnitsModal } from './UnitsModal'
import { useCategories } from './hooks'

interface Draft {
  name: string; sku: string; barcode: string; plu: string; hsn: string; unit: string; category: string; brand: string
  mrp: number; sale: number; purchase: number
  tax_rate_bp: number; cess_pct: string; cess_per_unit: number; tax_category: TaxCategory; price_includes_tax: boolean
  track_batches: boolean; reorder_level: string; reorder_qty: string; preferred_supplier_id: string
  alt_units: { unit: string; factor: string }[]
}

const fromItem = (it?: ItemFull): Draft => ({
  name: it?.name ?? '', sku: it?.sku ?? '', barcode: it?.barcode ?? '', plu: it?.plu ?? '', hsn: it?.hsn ?? '', unit: it?.unit ?? 'PCS',
  category: it?.category ?? '', brand: it?.brand ?? '',
  mrp: it?.mrp_paise ?? 0, sale: it?.sale_price_paise ?? 0, purchase: it?.purchase_price_paise ?? 0,
  tax_rate_bp: it?.tax_rate_bp ?? 1800, cess_pct: it?.cess_rate_bp ? String(it.cess_rate_bp / 100) : '', cess_per_unit: it?.cess_per_unit_paise ?? 0,
  tax_category: it?.tax_category ?? 'taxable', price_includes_tax: it?.price_includes_tax ?? true,
  track_batches: it?.track_batches ?? false, reorder_level: it?.reorder_level ? String(it.reorder_level) : '', reorder_qty: it?.reorder_qty ? String(it.reorder_qty) : '',
  preferred_supplier_id: it?.preferred_supplier_id ?? '',
  alt_units: (it?.alt_units ?? []).map((a) => ({ unit: a.unit, factor: String(a.factor) })),
})

const pct = (bp: number) => `${(bp / 100).toString()} %`
const num = (s: string) => { const n = Number(s); return Number.isFinite(n) ? n : NaN }

/** Client-side copy of ItemInput.Validate; the server message wins when they disagree. */
function validate(d: Draft): Record<string, string> {
  const e: Record<string, string> = {}
  if (!d.name.trim()) e.name = 'Name is required'
  else if (d.name.trim().length > 200) e.name = 'Name is too long (max 200)'
  const bc = validateBarcode(d.barcode); if (bc) e.barcode = bc
  const hsn = validateHSN(d.hsn); if (hsn) e.hsn = hsn
  if (d.unit.trim().length > 12) e.unit = 'Unit code is too long'
  if (d.mrp > 0 && d.sale > d.mrp) e.sale = 'Sale price cannot exceed MRP'
  if (d.tax_category === 'taxable') {
    const c = d.cess_pct.trim() === '' ? 0 : num(d.cess_pct)
    if (Number.isNaN(c) || c < 0) e.cess_pct = 'Cess must be 0 or more'
  }
  for (const [k, label] of [['reorder_level', 'Re-order level'], ['reorder_qty', 'Re-order quantity']] as const) {
    const v = d[k].trim(); if (v !== '' && (Number.isNaN(num(v)) || num(v) < 0)) e[k] = `${label} cannot be negative`
  }
  const seen = new Set<string>()
  d.alt_units.forEach((a, i) => {
    const u = a.unit.trim().toUpperCase(); const f = num(a.factor)
    if (!u || Number.isNaN(f) || f <= 0) e[`alt${i}`] = 'Needs a unit and a factor above 0'
    else if (u === d.unit.trim().toUpperCase()) e[`alt${i}`] = `${u} is the base unit`
    else if (seen.has(u)) e[`alt${i}`] = `${u} is repeated`
    seen.add(u)
  })
  return e
}

function toInput(d: Draft, creating: boolean): ItemInput {
  const taxable = d.tax_category === 'taxable'
  const barcode = normalizeCode(d.barcode)
  const body: ItemInput = {
    name: d.name.trim(), sku: d.sku.trim().toUpperCase(), barcode, plu: d.plu.trim(), hsn: d.hsn.trim(), unit: d.unit.trim().toUpperCase() || 'PCS',
    category: d.category.trim(), brand: d.brand.trim(),
    sale_price_paise: d.sale, mrp_paise: d.mrp, purchase_price_paise: d.purchase,
    tax_rate_bp: taxable ? d.tax_rate_bp : 0,
    cess_rate_bp: taxable && d.cess_pct.trim() ? Math.round(num(d.cess_pct) * 100) : 0,
    cess_per_unit_paise: taxable ? d.cess_per_unit : 0,
    tax_category: d.tax_category, price_includes_tax: d.price_includes_tax, track_batches: d.track_batches,
    reorder_level: d.reorder_level.trim() ? num(d.reorder_level) : 0, reorder_qty: d.reorder_qty.trim() ? num(d.reorder_qty) : 0,
    preferred_supplier_id: d.preferred_supplier_id,
    alt_units: d.alt_units.map((a) => ({ unit: a.unit.trim().toUpperCase(), factor: num(a.factor) })),
  }
  if (creating && !barcode) body.generate_barcode = true
  return body
}

/** Create / edit form covering every bhandar ItemInput field. */
export function ItemForm({ initial, onSaved, onCancel }: { initial?: ItemFull; onSaved: (item: ItemFull) => void; onCancel?: () => void }) {
  const [d, setD] = useState<Draft>(() => fromItem(initial))
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [serverErr, setServerErr] = useState('')
  const [unitsOpen, setUnitsOpen] = useState(false)
  const units = useGet<{ units: Unit[] }>('/bhandar/v1/units')
  const categories = useCategories()
  const creating = !initial
  const save = useSave<ItemFull, ItemInput>(creating ? '/bhandar/v1/items' : `/bhandar/v1/items/${initial.id}`, {
    method: creating ? 'POST' : 'PUT', silent: true,
    invalidate: creating ? ['/bhandar/v1/items', '/bhandar/v1/stock'] : ['/bhandar/v1/items', `/bhandar/v1/items/${initial.id}`, '/bhandar/v1/stock'],
  })
  const set = <K extends keyof Draft>(k: K, v: Draft[K]) => setD((x) => ({ ...x, [k]: v }))

  const slabs = useMemo(() => {
    const s = [...GST_SLABS_BP]
    if (initial && !s.includes(initial.tax_rate_bp)) s.push(initial.tax_rate_bp)
    return s.sort((a, b) => a - b)
  }, [initial])
  const unitOptions = useMemo(() => {
    const list = units.data?.units ?? []
    return list.some((u) => u.code === d.unit) || !d.unit ? list : [{ code: d.unit, name: d.unit }, ...list]
  }, [units.data, d.unit])
  const taxable = d.tax_category === 'taxable'

  const submit = async () => {
    const e = validate(d)
    setErrors(e)
    if (Object.keys(e).length) return
    setServerErr('')
    try {
      onSaved(await save.mutateAsync(toInput(d, creating)))
    } catch (err) { setServerErr(errorMessage(err)) }
  }

  return (
    <form className="space-y-4" onSubmit={(e) => { e.preventDefault(); void submit() }} noValidate>
      <Card title="Basics">
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Item name" className="sm:col-span-2" error={errors.name}><Input value={d.name} onChange={(e) => set('name', e.target.value)} autoFocus={creating} maxLength={200} placeholder="Parle-G 100 g" /></Field>
          <Field label="SKU" hint="Your own code; must be unique"><Input value={d.sku} onChange={(e) => set('sku', e.target.value.toUpperCase())} className="font-mono" placeholder="PG-100" /></Field>
          <Field label="Barcode" error={errors.barcode} hint={creating && !d.barcode.trim() ? 'Leave empty to generate an in-store EAN-13 (21…)' : 'EAN-13 check digit is verified; any other code is kept as typed'}>
            <Input value={d.barcode} onChange={(e) => set('barcode', e.target.value)} className="font-mono" inputMode="numeric" placeholder="8901719100017" />
          </Field>
          <Field label="PLU (weighing scale code)" hint="Lets scale labels 2F·PLU·price find this item"><Input value={d.plu} onChange={(e) => set('plu', e.target.value)} className="font-mono" placeholder="00123" /></Field>
          <Field label="HSN / SAC" error={errors.hsn} hint="4, 6 or 8 digits"><Input value={d.hsn} onChange={(e) => set('hsn', e.target.value.replace(/\D/g, '').slice(0, 8))} className="font-mono" inputMode="numeric" placeholder="1905" /></Field>
          <Field label="Unit" error={errors.unit}>
            <div className="flex gap-2">
              <Select value={d.unit} onChange={(e) => set('unit', e.target.value)}>
                {unitOptions.map((u) => <option key={u.code} value={u.code}>{u.code}{u.name && u.name !== u.code ? ` — ${u.name}` : ''}</option>)}
              </Select>
              <Button type="button" variant="secondary" icon={Settings2} onClick={() => setUnitsOpen(true)} className="shrink-0" title="Manage units">Units</Button>
            </div>
          </Field>
          <Field label="Category"><Input value={d.category} onChange={(e) => set('category', e.target.value)} list="item-categories" placeholder="Biscuits" /><datalist id="item-categories">{categories.map((c) => <option key={c} value={c} />)}</datalist></Field>
          <Field label="Brand"><Input value={d.brand} onChange={(e) => set('brand', e.target.value)} placeholder="Parle" /></Field>
        </div>
      </Card>

      <Card title="Pricing & tax">
        <div className="grid gap-4 sm:grid-cols-3">
          <Field label="MRP"><MoneyInput value={d.mrp} onChange={(v) => set('mrp', v)} /></Field>
          <Field label="Sale price" error={errors.sale} hint={d.mrp > 0 && d.sale === 0 ? 'Empty = sold at MRP? Enter the shelf price' : undefined}><MoneyInput value={d.sale} onChange={(v) => set('sale', v)} /></Field>
          <Field label="Purchase price" hint="Default cost for opening stock and re-order drafts"><MoneyInput value={d.purchase} onChange={(v) => set('purchase', v)} /></Field>
          <div className="sm:col-span-3"><Toggle checked={d.price_includes_tax} onChange={(v) => set('price_includes_tax', v)} label="Prices include GST (MRP pricing)" /></div>
          <Field label="Tax category">
            <Select value={d.tax_category} onChange={(e) => set('tax_category', e.target.value as TaxCategory)}>
              {(Object.keys(TAX_CATEGORY_LABEL) as TaxCategory[]).map((k) => <option key={k} value={k}>{TAX_CATEGORY_LABEL[k]}</option>)}
            </Select>
          </Field>
          <Field label="GST slab">
            <Select value={d.tax_rate_bp} onChange={(e) => set('tax_rate_bp', Number(e.target.value))} disabled={!taxable}>
              {slabs.map((bp) => <option key={bp} value={bp}>{pct(bp)}{!GST_SLABS_BP.includes(bp) ? ' (no longer in force)' : ''}</option>)}
            </Select>
          </Field>
          <Field label="Cess %" error={errors.cess_pct} hint="Compensation cess on value (tobacco, aerated drinks)"><Input value={d.cess_pct} onChange={(e) => set('cess_pct', e.target.value.replace(/[^0-9.]/g, ''))} inputMode="decimal" disabled={!taxable} className="num" placeholder="0" /></Field>
          <Field label="Cess per unit" hint="Specific cess per base unit, if any"><MoneyInput value={d.cess_per_unit} onChange={(v) => set('cess_per_unit', v)} disabled={!taxable} /></Field>
        </div>
      </Card>

      <Card title="Stock & re-order">
        <div className="grid gap-4 sm:grid-cols-3">
          <div className="sm:col-span-3"><Toggle checked={d.track_batches} onChange={(v) => set('track_batches', v)} label="Track batches and expiry dates" /></div>
          <Field label="Re-order level" error={errors.reorder_level} hint="Alert when stock falls to this"><Input value={d.reorder_level} onChange={(e) => set('reorder_level', e.target.value.replace(/[^0-9.]/g, ''))} inputMode="decimal" className="num" placeholder="0" /></Field>
          <Field label="Re-order quantity" error={errors.reorder_qty} hint="Suggested order size"><Input value={d.reorder_qty} onChange={(e) => set('reorder_qty', e.target.value.replace(/[^0-9.]/g, ''))} inputMode="decimal" className="num" placeholder="0" /></Field>
          <Field label="Preferred supplier" hint="Re-order drafts are grouped by supplier"><SupplierPicker value={d.preferred_supplier_id} onChange={(id) => set('preferred_supplier_id', id)} /></Field>
        </div>
      </Card>

      <Card title="Alternate units" actions={<Button type="button" size="sm" variant="secondary" icon={Plus} onClick={() => set('alt_units', [...d.alt_units, { unit: '', factor: '' }])}>Add unit</Button>}>
        {d.alt_units.length === 0 ? (
          <p className="text-sm text-slate-500">Buy in boxes, sell in pieces: add BOX = 48 {d.unit || 'PCS'} and purchase lines can be entered per box.</p>
        ) : (
          <div className="space-y-2">
            {d.alt_units.map((a, i) => (
              <div key={i} className="grid grid-cols-[1fr_auto_1fr_auto_auto] items-center gap-2">
                <Select value={a.unit} onChange={(e) => set('alt_units', d.alt_units.map((x, j) => j === i ? { ...x, unit: e.target.value } : x))} aria-label="Alternate unit">
                  <option value="">Unit…</option>
                  {(units.data?.units ?? []).filter((u) => u.code !== d.unit).map((u) => <option key={u.code} value={u.code}>{u.code}</option>)}
                </Select>
                <span className="text-sm text-slate-500">=</span>
                <Input value={a.factor} onChange={(e) => set('alt_units', d.alt_units.map((x, j) => j === i ? { ...x, factor: e.target.value.replace(/[^0-9.]/g, '') } : x))} inputMode="decimal" className="num" placeholder="48" aria-label="Factor" />
                <span className="text-sm text-slate-500">{d.unit || 'PCS'}</span>
                <IconButton icon={Trash2} label="Remove alternate unit" onClick={() => set('alt_units', d.alt_units.filter((_, j) => j !== i))} />
                {errors[`alt${i}`] && <div className="col-span-5 -mt-1 text-xs text-rose-600">{errors[`alt${i}`]}</div>}
              </div>
            ))}
          </div>
        )}
      </Card>

      {initial?.external_ref && (
        <Card title="Import reference">
          <Field label="External ref (read-only)" hint="Set by the data import; used to match re-imports"><Input value={initial.external_ref} readOnly className="font-mono" /></Field>
        </Card>
      )}

      {serverErr && <ErrorBox message={serverErr} />}
      <div className="flex justify-end gap-2 pb-6">
        {onCancel && <Button type="button" variant="secondary" onClick={onCancel}>Cancel</Button>}
        <Button type="submit" loading={save.isPending}>{creating ? 'Create item' : 'Save changes'}</Button>
      </div>
      <UnitsModal open={unitsOpen} onClose={() => setUnitsOpen(false)} />
    </form>
  )
}
