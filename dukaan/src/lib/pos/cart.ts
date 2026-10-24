/**
 * Pure cart model for the counter (no React, no network). Mirrors the parts of
 * aadesh the POS must agree with before the bill reaches the server:
 *   - line discounts: ApplyBP(gross, discount_bp) + discount_paise (gst.grossAndDiscount)
 *   - totals: computeGST (the TypeScript port of mool/gst)
 *   - settle(): invoice.Settle — paid, change, credit, non-cash never above the total
 *   - weighing-scale barcodes: bhandar/barcode.ParseWeighingScale
 * Tested by cart.test.ts (node --test).
 */
import { applyBP } from '../money.ts'
import { computeGST, type GstLine, type GstSummary } from '../gst.ts'
import type { Tender } from '../types'
import type { CartLine, CatalogItemFull, PosLineInput, ScaleRead, TaxCategory } from '../types.pos'

/** Cashiers may discount a line up to 10 % on their own; more needs a manager (client-side nicety). */
export const CASHIER_MAX_DISCOUNT_BP = 1000

/** The fields of an item the cart needs (works for CatalogItem and Item). */
export interface ItemLike {
  id: string
  name: string
  hsn?: string
  unit: string
  sale_price_paise: number
  mrp_paise: number
  tax_rate_bp: number
  cess_rate_bp: number
  cess_per_unit_paise?: number
  tax_category?: string
  price_includes_tax: boolean
  track_batches: boolean
  sku?: string
  barcode?: string
  plu?: string
}

let seq = 0
/** Unique, stable key for a cart line. */
export const newKey = () => `l${Date.now().toString(36)}${(seq++).toString(36)}`

export const round3 = (n: number) => Math.round(n * 1000) / 1000

export const normCategory = (c: string | undefined): TaxCategory =>
  c === 'exempt' || c === 'nil_rated' || c === 'non_gst' ? c : 'taxable'

/** A cart line for a master item. Non-taxable categories carry no rate, as on the server. */
export function lineFromItem(it: ItemLike, qty = 1, opts: { batch_no?: string; unit_price_paise?: number } = {}): CartLine {
  const cat = normCategory(it.tax_category)
  const taxable = cat === 'taxable'
  const price = opts.unit_price_paise ?? (it.sale_price_paise || it.mrp_paise || 0)
  return {
    key: newKey(),
    item_id: it.id,
    name: it.name,
    hsn: it.hsn || undefined,
    unit: it.unit || 'PCS',
    batch_no: opts.batch_no || undefined,
    qty: round3(qty),
    unit_price_paise: it.mrp_paise > 0 ? Math.min(price, it.mrp_paise) : price,
    mrp_paise: it.mrp_paise || 0,
    discount_bp: 0,
    discount_paise: 0,
    tax_rate_bp: taxable ? it.tax_rate_bp : 0,
    cess_rate_bp: taxable ? it.cess_rate_bp || 0 : 0,
    cess_per_unit_paise: taxable ? it.cess_per_unit_paise || 0 : 0,
    price_includes_tax: !!it.price_includes_tax,
    tax_category: cat,
    track_batches: !!it.track_batches,
  }
}

/** A line typed at the counter for something not in the item master. */
export function freeTextLine(input: { name: string; unit_price_paise: number; tax_rate_bp: number; qty?: number; unit?: string; hsn?: string; price_includes_tax?: boolean }): CartLine {
  return {
    key: newKey(),
    name: input.name.trim(),
    hsn: input.hsn?.trim() || undefined,
    unit: (input.unit || 'PCS').toUpperCase(),
    qty: round3(input.qty ?? 1),
    unit_price_paise: Math.max(0, Math.round(input.unit_price_paise)),
    mrp_paise: 0,
    discount_bp: 0,
    discount_paise: 0,
    tax_rate_bp: input.tax_rate_bp,
    cess_rate_bp: 0,
    cess_per_unit_paise: 0,
    price_includes_tax: input.price_includes_tax ?? true,
    tax_category: 'taxable',
    track_batches: false,
    free_text: true,
  }
}

/** Same master item and batch → one line with a bigger quantity. */
export function addLine(lines: CartLine[], line: CartLine): CartLine[] {
  if (line.item_id) {
    const i = lines.findIndex((l) => l.item_id === line.item_id && (l.batch_no || '') === (line.batch_no || '') && l.unit_price_paise === line.unit_price_paise)
    if (i >= 0) return lines.map((l, j) => (j === i ? { ...l, qty: round3(l.qty + line.qty) } : l))
  }
  return [...lines, line]
}

export const updateLine = (lines: CartLine[], key: string, patch: Partial<CartLine>): CartLine[] =>
  lines.map((l) => (l.key === key ? { ...l, ...patch } : l))

export const removeLine = (lines: CartLine[], key: string): CartLine[] => lines.filter((l) => l.key !== key)

/** Set a quantity; zero or less removes the line. */
export function setQty(lines: CartLine[], key: string, qty: number): CartLine[] {
  const q = round3(qty)
  return q > 0 ? updateLine(lines, key, { qty: q }) : removeLine(lines, key)
}

export const lineGross = (l: Pick<CartLine, 'qty' | 'unit_price_paise'>) => Math.round(l.qty * l.unit_price_paise)

/** Total line discount exactly as gst.grossAndDiscount computes it. */
export const lineDiscount = (l: Pick<CartLine, 'qty' | 'unit_price_paise' | 'discount_bp' | 'discount_paise'>) =>
  applyBP(lineGross(l), l.discount_bp) + l.discount_paise

/** Effective discount as a share of gross, in basis points (for the cashier cap). */
export function lineDiscountBP(l: CartLine): number {
  const gross = lineGross(l)
  if (gross <= 0) return 0
  return Math.round((lineDiscount(l) * 10000) / gross)
}

/** Can this role give this discount without a manager? */
export const discountAllowed = (l: CartLine, role: string | undefined, unlocked: boolean) =>
  unlocked || role !== 'cashier' || lineDiscountBP(l) <= CASHIER_MAX_DISCOUNT_BP

/** Price capped at MRP when the item has one. */
export const capPrice = (price: number, mrp: number) => (mrp > 0 ? Math.min(Math.max(0, price), mrp) : Math.max(0, price))

/** What computeGST needs for one line (bp discounts folded into paise, non-taxable rates zeroed). */
export function toGstLine(l: CartLine): GstLine {
  const taxable = l.tax_category === 'taxable'
  return {
    hsn: l.hsn,
    qty: l.qty,
    unit_price_paise: l.unit_price_paise,
    discount_paise: lineDiscount(l),
    tax_rate_bp: taxable ? l.tax_rate_bp : 0,
    cess_rate_bp: taxable ? l.cess_rate_bp : 0,
    price_includes_tax: l.price_includes_tax,
  }
}

export const emptySummary = (): GstSummary => ({
  inter_state: false, lines: [], taxable_paise: 0, cgst_paise: 0, sgst_paise: 0, igst_paise: 0, cess_paise: 0,
  discount_paise: 0, round_off_paise: 0, grand_total_paise: 0, by_rate: [],
})

/** Live totals. Throws (message prefixed with the line number) when a line is invalid. */
export function cartTotals(lines: CartLine[], storeState: string, placeOfSupply = '', roundOff = true): GstSummary {
  if (lines.length === 0 || !storeState) return emptySummary()
  return computeGST(lines.map(toGstLine), storeState, placeOfSupply, roundOff)
}

/** The line as aadesh accepts it: only known keys, nothing undefined. */
export function toInvoiceLine(l: CartLine): PosLineInput {
  const out: PosLineInput = { name: l.name.trim(), qty: l.qty, unit_price_paise: l.unit_price_paise, tax_rate_bp: l.tax_rate_bp }
  if (l.item_id) out.item_id = l.item_id
  if (l.hsn) out.hsn = l.hsn
  if (l.unit) out.unit = l.unit
  if (l.batch_no) out.batch_no = l.batch_no
  if (l.discount_paise > 0) out.discount_paise = l.discount_paise
  if (l.discount_bp > 0) out.discount_bp = l.discount_bp
  if (l.cess_rate_bp > 0) out.cess_rate_bp = l.cess_rate_bp
  if (l.cess_per_unit_paise > 0) out.cess_per_unit_paise = l.cess_per_unit_paise
  if (l.price_includes_tax) out.price_includes_tax = true
  if (l.tax_category !== 'taxable') out.tax_category = l.tax_category
  return out
}

/* ---------------- settlement (mirror of invoice.Settle) ---------------- */

export interface Settlement { paid: number; change: number; credit: number; error?: string }

/**
 * paid counts towards the bill (never above the total), change is cash handed
 * back, credit is what the party still owes. Card/UPI/bank cannot exceed the total.
 */
export function settle(total: number, tenders: Tender[]): Settlement {
  let cash = 0, nonCash = 0, credit = 0
  for (const t of tenders) {
    if (t.mode === 'credit') credit += t.amount_paise
    else if (t.mode === 'cash') cash += t.amount_paise
    else nonCash += t.amount_paise
  }
  if (nonCash > total) return { paid: 0, change: 0, credit: 0, error: 'card/UPI/bank tenders exceed the bill total' }
  let paid = cash + nonCash
  let change = 0
  if (paid > total) {
    change = paid - total
    paid = total
  }
  if (credit === 0 && paid < total) credit = total - paid
  else if (credit > total - paid) credit = total - paid
  return { paid, change, credit }
}

/** What is still to be collected after the tenders entered so far (credit lines do not count as collected). */
export function balanceDue(total: number, tenders: Tender[]): number {
  const got = tenders.filter((t) => t.mode !== 'credit').reduce((a, t) => a + t.amount_paise, 0)
  return Math.max(0, total - got)
}

/* ---------------- codes and search ---------------- */

/** EAN-13 check digit for the first 12 digits. */
export function ean13Check(d12: string): number {
  let sum = 0
  for (let i = 0; i < 12; i++) sum += Number(d12[i]) * (i % 2 === 0 ? 1 : 3)
  return (10 - (sum % 10)) % 10
}

export const validEAN13 = (s: string) => /^\d{13}$/.test(s) && ean13Check(s.slice(0, 12)) === Number(s[12])

/**
 * Decode a retail weighing-scale label (bhandar/barcode.ParseWeighingScale):
 * 2F IIIII VVVVV C — prefix 20–25 = price in paise, 26–29 = weight in grams.
 */
export function parseScaleCode(code: string): { plu: string; scale: ScaleRead } | null {
  if (!validEAN13(code) || code[0] !== '2') return null
  const kind: ScaleRead['kind'] = code[1] <= '5' ? 'price' : 'weight'
  return { plu: code.slice(2, 7), scale: { kind, value: Number(code.slice(7, 12)) } }
}

/** Does the scan box hold a code (barcode / SKU / PLU) rather than a search phrase? */
export function looksLikeCode(q: string): boolean {
  const s = q.trim()
  if (/\s/.test(s)) return false
  if (/^\d{3,48}$/.test(s)) return true
  return /^[A-Za-z0-9][A-Za-z0-9\-._/]{2,47}$/.test(s) && /\d/.test(s)
}

/** Turn a scale read into a quantity (and, for a priced label on an unpriced item, a price). */
export function scaleQty(scale: ScaleRead, unit: string, unitPricePaise: number): { qty: number; unit_price_paise?: number } {
  if (scale.kind === 'weight') {
    const u = unit.toUpperCase()
    return { qty: u === 'KG' || u === 'KGS' ? round3(scale.value / 1000) : scale.value }
  }
  if (unitPricePaise > 0) return { qty: Math.max(0.001, round3(scale.value / unitPricePaise)) }
  return { qty: 1, unit_price_paise: scale.value }
}

/** Offline code lookup in the cached catalog: exact barcode or SKU, then a scale label whose PLU is a SKU. */
export function findByCode<T extends ItemLike>(items: T[], raw: string): { item: T; scale?: ScaleRead } | null {
  const code = raw.trim()
  if (!code) return null
  const upper = code.toUpperCase()
  const exact = items.find((it) => it.barcode === code || (it.sku && it.sku.toUpperCase() === upper) || (it.plu && it.plu === code))
  if (exact) return { item: exact }
  const sc = parseScaleCode(code)
  if (sc) {
    const it = items.find((x) => x.plu === sc.plu || (x.sku && x.sku.toUpperCase() === sc.plu))
    if (it) return { item: it, scale: sc.scale }
  }
  return null
}

/** Name / SKU / barcode search; every word must match, prefix matches first. */
export function searchCatalog<T extends ItemLike>(items: T[], q: string, limit = 12): T[] {
  const words = q.trim().toLowerCase().split(/\s+/).filter(Boolean)
  if (words.length === 0) return []
  const scored: { it: T; score: number }[] = []
  for (const it of items) {
    const name = it.name.toLowerCase()
    const hay = `${name} ${it.sku ?? ''} ${it.barcode ?? ''}`.toLowerCase()
    if (!words.every((w) => hay.includes(w))) continue
    const score = name.startsWith(words[0]) ? 0 : name.includes(words[0]) ? 1 : 2
    scored.push({ it, score })
  }
  return scored.sort((a, b) => a.score - b.score || a.it.name.localeCompare(b.it.name)).slice(0, limit).map((s) => s.it)
}

export const catalogToItemLike = (c: CatalogItemFull): ItemLike => c

/** Sum of quantities (for the "n items" chip). */
export const cartQty = (lines: CartLine[]) => round3(lines.reduce((a, l) => a + l.qty, 0))
