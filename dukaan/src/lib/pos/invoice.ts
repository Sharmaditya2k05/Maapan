/**
 * Pure helpers shared by the counter, the billing editors and the print
 * documents: request-body whitelisting (the services reject unknown JSON
 * fields), tax summaries from stored lines, offline invoice rendering,
 * WhatsApp / phone formatting. No React, no network. Tested by invoice.test.ts.
 */
import type { GstSummary } from '../gst.ts'
import { amountInWords, STATES } from '../gst.ts'
import type { DocType, Invoice, InvoiceLine, Store, Tender } from '../types'
import type { CartLine, InvoiceFull, PosCreateInvoiceInput, PosLineInput, PreviewLine, TaxCategory } from '../types.pos'
import type { PurchaseDraft } from '../types.more'
import { newKey, normCategory, round3, type Settlement } from './cart.ts'

/* ---------------- request bodies ---------------- */

const LINE_KEYS = ['item_id', 'name', 'unit', 'batch_no', 'hsn', 'qty', 'unit_price_paise', 'discount_paise', 'discount_bp', 'tax_rate_bp', 'cess_rate_bp', 'cess_per_unit_paise', 'price_includes_tax', 'tax_category'] as const
const PREVIEW_KEYS = ['hsn', 'qty', 'unit_price_paise', 'discount_paise', 'discount_bp', 'tax_rate_bp', 'cess_rate_bp', 'cess_per_unit_paise', 'price_includes_tax', 'tax_category'] as const
const INPUT_KEYS = ['doc_type', 'store_id', 'invoice_date', 'due_date', 'party_id', 'party', 'place_of_supply', 'supply_category', 'reverse_charge',
  'supplier_invoice_no', 'supplier_invoice_date', 'ref_invoice_id', 'lines', 'invoice_discount_paise', 'invoice_discount_bp', 'tcs_bp', 'tenders',
  'notes', 'round_off', 'override_credit_limit', 'client_uuid', 'device_id', 'number', 'client_created_at'] as const
const TENDER_KEYS = ['mode', 'amount_paise', 'reference', 'payment_request_id'] as const
const PARTY_KEYS = ['name', 'phone', 'gstin', 'state_code'] as const

type Dict = Record<string, unknown>

function pick<T>(src: Dict, keys: readonly string[]): T {
  const out: Dict = {}
  for (const k of keys) {
    const v = src[k]
    if (v !== undefined && v !== null && v !== '') out[k] = v
  }
  return out as unknown as T
}

/** A line with only the keys aadesh LineInput knows. */
export const cleanLine = (l: object): PosLineInput => pick<PosLineInput>(l as Dict, LINE_KEYS)

/** A gst.Line for POST /v1/invoices/preview (no item fields). */
export const toPreviewLine = (l: object): PreviewLine => pick<PreviewLine>(l as Dict, PREVIEW_KEYS)

export const cleanTender = (t: object): Tender => pick<Tender>(t as Dict, TENDER_KEYS)

/** A CreateInput with only the keys aadesh knows, lines and tenders included. Drops empty strings. */
export function cleanCreateInput(input: Dict): PosCreateInvoiceInput {
  const out = pick<PosCreateInvoiceInput>(input, INPUT_KEYS)
  out.lines = (Array.isArray(input.lines) ? (input.lines as Dict[]) : []).map(cleanLine)
  if (Array.isArray(input.tenders)) {
    const t = (input.tenders as Dict[]).map(cleanTender).filter((x) => x.amount_paise > 0)
    if (t.length) out.tenders = t
    else delete out.tenders
  }
  if (input.party && typeof input.party === 'object') out.party = pick<NonNullable<PosCreateInvoiceInput['party']>>(input.party as Dict, PARTY_KEYS)
  if (typeof input.reverse_charge === 'boolean' && !input.reverse_charge) delete out.reverse_charge
  if (typeof input.override_credit_limit === 'boolean' && !input.override_credit_limit) delete out.override_credit_limit
  return out
}

/** Parse a sessionStorage hand-off (bill scan, re-order, return-to-vendor). Lines are whitelisted to what aadesh accepts (expiry_date is dropped). Null when unusable. */
export function parsePurchaseDraft(raw: string | null): PurchaseDraft | null {
  if (!raw) return null
  try {
    const d = JSON.parse(raw) as Dict
    if (!d || typeof d !== 'object') return null
    const doc = d.doc_type === 'purchase_return' ? 'purchase_return' : 'purchase'
    const lines = (Array.isArray(d.lines) ? (d.lines as Dict[]) : []).map(cleanLine).filter((l) => l.name)
    return {
      doc_type: doc,
      party_id: typeof d.party_id === 'string' ? d.party_id : undefined,
      ref_invoice_id: typeof d.ref_invoice_id === 'string' ? d.ref_invoice_id : undefined,
      store_id: typeof d.store_id === 'string' ? d.store_id : undefined,
      lines,
      notes: typeof d.notes === 'string' ? d.notes : undefined,
      supplier_invoice_no: typeof d.supplier_invoice_no === 'string' ? d.supplier_invoice_no : undefined,
      supplier_invoice_date: typeof d.supplier_invoice_date === 'string' ? d.supplier_invoice_date : undefined,
    }
  } catch {
    return null
  }
}

/** A stored or drafted line as an editable cart line (duplicates, returns, drafts). */
export function lineToCart(l: Partial<InvoiceLine> & PosLineInput, extra: Partial<CartLine> = {}): CartLine {
  return {
    key: newKey(),
    item_id: l.item_id || undefined,
    name: l.name,
    hsn: l.hsn || undefined,
    unit: l.unit || 'PCS',
    batch_no: l.batch_no || undefined,
    qty: round3(l.qty),
    unit_price_paise: l.unit_price_paise,
    mrp_paise: 0,
    discount_bp: l.discount_bp ?? 0,
    // When a percentage was stored, the stored paise already contain it: keep one or the other.
    discount_paise: l.discount_bp ? 0 : l.discount_paise ?? 0,
    tax_rate_bp: l.tax_rate_bp,
    cess_rate_bp: l.cess_rate_bp ?? 0,
    cess_per_unit_paise: l.cess_per_unit_paise ?? 0,
    price_includes_tax: !!l.price_includes_tax,
    tax_category: normCategory(l.tax_category as string | undefined) as TaxCategory,
    track_batches: !!l.batch_no,
    free_text: !l.item_id,
    ...extra,
  }
}

/* ---------------- display ---------------- */

export const DOC_LABEL: Record<DocType, string> = { sale: 'Tax Invoice', purchase: 'Purchase Bill', sale_return: 'Credit Note', purchase_return: 'Debit Note' }
export const DOC_SHORT: Record<DocType, string> = { sale: 'Invoice', purchase: 'Purchase', sale_return: 'Credit note', purchase_return: 'Debit note' }
export const isNote = (d: DocType) => d === 'sale_return' || d === 'purchase_return'
export const isPurchaseSide = (d: DocType) => d === 'purchase' || d === 'purchase_return'

/** Title printed on a sales document: a bill of supply when no GST is charged (composition / unregistered / all exempt). */
export function documentTitle(inv: Pick<Invoice, 'doc_type' | 'cgst_paise' | 'sgst_paise' | 'igst_paise' | 'cess_paise' | 'store_gstin'>): string {
  if (inv.doc_type !== 'sale') return DOC_LABEL[inv.doc_type]
  const tax = inv.cgst_paise + inv.sgst_paise + inv.igst_paise + inv.cess_paise
  return !inv.store_gstin || tax === 0 ? 'Bill of Supply' : 'Tax Invoice'
}

export const stateName = (code: string | undefined | null) => (code ? `${STATES[code] ?? 'Unknown'} (${code})` : '')

export const TENDER_LABEL: Record<Tender['mode'], string> = { cash: 'Cash', card: 'Card', upi: 'UPI', bank: 'Bank', credit: 'Credit', other: 'Other' }

export interface RateRow { tax_rate_bp: number; taxable_paise: number; cgst_paise: number; sgst_paise: number; igst_paise: number; cess_paise: number; tax_category: string }

/** Tax summary grouped by rate (and category) from stored lines, as printed on the invoice. */
export function summarizeByRate(lines: InvoiceLine[] | undefined): RateRow[] {
  const m = new Map<string, RateRow>()
  for (const l of lines ?? []) {
    const cat = l.tax_category || 'taxable'
    const rate = cat === 'taxable' ? l.tax_rate_bp : 0
    const k = `${rate}:${cat}`
    const r = m.get(k) ?? { tax_rate_bp: rate, taxable_paise: 0, cgst_paise: 0, sgst_paise: 0, igst_paise: 0, cess_paise: 0, tax_category: cat }
    r.taxable_paise += l.taxable_paise
    r.cgst_paise += l.cgst_paise
    r.sgst_paise += l.sgst_paise
    r.igst_paise += l.igst_paise
    r.cess_paise += l.cess_paise
    m.set(k, r)
  }
  return [...m.values()].sort((a, b) => a.tax_rate_bp - b.tax_rate_bp || a.tax_category.localeCompare(b.tax_category))
}

export const CATEGORY_LABEL: Record<string, string> = { taxable: '', exempt: 'Exempt', nil_rated: 'Nil rated', non_gst: 'Non-GST' }

/* ---------------- offline bills ---------------- */

/**
 * Build the Invoice a queued (offline) bill prints from, using the same totals
 * the server will compute. `number` is the display number from the device block.
 */
export function buildLocalInvoice(input: PosCreateInvoiceInput, summary: GstSummary, settle: Settlement, ctx: {
  number: string; store: Store; createdAt: string; partyName: string; partyPhone?: string; partyGstin?: string; partyState?: string; clientUuid: string
}): InvoiceFull {
  const date = ctx.createdAt.slice(0, 10)
  const pos = input.place_of_supply || ctx.partyState || ctx.store.state_code
  const lines: InvoiceLine[] = input.lines.map((l, i) => {
    const t = summary.lines[i]
    return {
      ...l,
      line_no: i + 1,
      unit: l.unit || 'PCS',
      discount_paise: t?.discount_paise ?? l.discount_paise ?? 0,
      taxable_paise: t?.taxable_paise ?? 0,
      cgst_paise: t?.cgst_paise ?? 0,
      sgst_paise: t?.sgst_paise ?? 0,
      igst_paise: t?.igst_paise ?? 0,
      cess_paise: t?.cess_paise ?? 0,
      total_paise: t?.total_paise ?? 0,
      tax_category: l.tax_category ?? 'taxable',
    }
  })
  const grand = summary.grand_total_paise
  return {
    id: '',
    store_id: ctx.store.id,
    doc_type: input.doc_type,
    number: ctx.number,
    fy: '',
    invoice_date: date,
    party_id: input.party_id,
    party_name: ctx.partyName,
    party_phone: ctx.partyPhone,
    party_gstin: ctx.partyGstin,
    party_state: ctx.partyState,
    store_state: ctx.store.state_code,
    store_gstin: ctx.store.gstin,
    place_of_supply: pos,
    inter_state: summary.inter_state,
    status: 'posted',
    payment_status: settle.paid >= grand ? 'paid' : settle.paid > 0 ? 'partial' : 'unpaid',
    taxable_paise: summary.taxable_paise,
    discount_paise: summary.discount_paise,
    cgst_paise: summary.cgst_paise,
    sgst_paise: summary.sgst_paise,
    igst_paise: summary.igst_paise,
    cess_paise: summary.cess_paise,
    round_off_paise: summary.round_off_paise,
    grand_total_paise: grand,
    paid_paise: settle.paid,
    change_paise: settle.change,
    balance_paise: Math.max(0, grand - settle.paid),
    amount_in_words: amountInWords(grand),
    notes: input.notes,
    created_at: ctx.createdAt,
    client_uuid: ctx.clientUuid,
    lines,
    tenders: input.tenders ?? [],
  }
}

/* ---------------- phones and sharing ---------------- */

/** Indian mobile as lenden wants it (+91XXXXXXXXXX), or undefined when it is not one. */
export function lendenPhone(raw: string | undefined | null): string | undefined {
  const d = (raw ?? '').replace(/\D/g, '')
  const ten = d.length > 10 && d.startsWith('91') ? d.slice(-10) : d
  return /^[6-9]\d{9}$/.test(ten) ? `+91${ten}` : undefined
}

/** Digits for wa.me (country code included); empty when unknown. */
export function waPhone(raw: string | undefined | null): string {
  const d = (raw ?? '').replace(/\D/g, '')
  if (d.length === 10) return `91${d}`
  if (d.length === 12 && d.startsWith('91')) return d
  return d.length > 10 ? d : ''
}

/** wa.me link with a short bill summary. */
export function whatsappLink(inv: Invoice, business: string, link?: string): string {
  const title = documentTitle(inv)
  const lines = [
    `${business ? business + ' — ' : ''}${title} ${inv.number}`,
    `Date: ${inv.invoice_date}`,
    `Total: ₹${(inv.grand_total_paise / 100).toFixed(2)}`,
  ]
  if (inv.balance_paise > 0) lines.push(`Balance due: ₹${(inv.balance_paise / 100).toFixed(2)}${inv.due_date ? ` by ${inv.due_date}` : ''}`)
  else lines.push('Paid in full — thank you!')
  if (link) lines.push(link)
  return `https://wa.me/${waPhone(inv.party_phone)}?text=${encodeURIComponent(lines.join('\n'))}`
}

export const isWalkIn = (inv: Pick<Invoice, 'party_id' | 'party_name'>) => !inv.party_id && /^walk[- ]?in$/i.test(inv.party_name.trim())
