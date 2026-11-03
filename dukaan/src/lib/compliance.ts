/**
 * Pure helpers for the Phase 3 GST screens: registration validation, MMYYYY return periods,
 * e-invoice / e-way bill windows, GSTR-2B status grouping and rupee ⇄ paise conversion for
 * kar's rupee amounts. No React, no network — unit-tested in compliance.test.ts.
 */
import { STATES, validateGSTIN } from './gst.ts'
import type { EInvoiceRecord, EWayRecord, IMSAction, ReconResult, Registration, RegistrationInput, TwoBStatus } from './types.kar.ts'
import type { ItcEligibility } from './types.ts'

/* ---------------- registrations ---------------- */

/** Indian PIN code: six digits, not starting with 0. */
export const isValidPin = (pin: string) => /^[1-9][0-9]{5}$/.test(pin.trim())

export interface RegistrationDraft {
  gstin: string
  legal_name: string
  trade_name: string
  state_code: string
  address: string
  city: string
  pin: string
  phone: string
  email: string
  composition: boolean
  einvoice_applicable: boolean
}

export const emptyRegistrationDraft = (): RegistrationDraft => ({
  gstin: '', legal_name: '', trade_name: '', state_code: '', address: '', city: '', pin: '', phone: '', email: '', composition: false, einvoice_applicable: false,
})

export const draftFromRegistration = (r: Registration): RegistrationDraft => ({
  gstin: r.gstin ?? '', legal_name: r.legal_name ?? '', trade_name: r.trade_name ?? '', state_code: r.state_code ?? '', address: r.address ?? '',
  city: r.city ?? '', pin: r.pin ?? '', phone: r.phone ?? '', email: r.email ?? '', composition: !!r.composition, einvoice_applicable: !!r.einvoice_applicable,
})

/** Normalise what the user types into a GSTIN box: upper case, A-Z0-9 only, 15 characters. */
export const normaliseGSTIN = (raw: string) => raw.toUpperCase().replace(/[^0-9A-Z]/g, '').slice(0, 15)

/** The state follows the GSTIN once it is valid; otherwise keep what was chosen. */
export function stateForGSTIN(gstin: string, current: string): string {
  const g = normaliseGSTIN(gstin)
  return g.length === 15 && !validateGSTIN(g) ? g.slice(0, 2) : current
}

/** Field → message for a registration form; empty object = valid. Mirrors pehchaan's checks. */
export function validateRegistration(d: RegistrationDraft): Partial<Record<keyof RegistrationDraft, string>> {
  const errs: Partial<Record<keyof RegistrationDraft, string>> = {}
  const g = normaliseGSTIN(d.gstin)
  if (g) {
    const e = validateGSTIN(g)
    if (e) errs.gstin = e
    else if (d.state_code && d.state_code !== g.slice(0, 2)) errs.state_code = `The GSTIN is registered in ${STATES[g.slice(0, 2)] ?? g.slice(0, 2)} (${g.slice(0, 2)})`
  }
  if (!d.state_code) errs.state_code ??= 'Choose the state'
  else if (!STATES[d.state_code]) errs.state_code ??= 'Unknown state code'
  if (d.pin.trim() && !isValidPin(d.pin)) errs.pin = 'PIN code is 6 digits'
  if (d.email.trim() && !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(d.email.trim())) errs.email = 'Not an email address'
  if (d.einvoice_applicable) {
    if (!g) errs.einvoice_applicable = 'e-Invoicing needs a GSTIN'
    else if (!d.pin.trim()) errs.pin ??= 'The IRP needs the PIN code for e-invoices'
    else if (!d.city.trim()) errs.city = 'The IRP needs the city (location) for e-invoices'
  }
  if (d.einvoice_applicable && d.composition) errs.composition = 'Composition dealers issue bills of supply; they cannot e-invoice'
  return errs
}

/** Body for POST / PUT /v1/registrations: trimmed, GSTIN normalised, state from the GSTIN. */
export function registrationBody(d: RegistrationDraft): RegistrationInput {
  const g = normaliseGSTIN(d.gstin)
  return {
    gstin: g, legal_name: d.legal_name.trim(), trade_name: d.trade_name.trim(), state_code: g && !validateGSTIN(g) ? g.slice(0, 2) : d.state_code,
    address: d.address.trim(), city: d.city.trim(), pin: d.pin.trim(), phone: d.phone.trim(), email: d.email.trim(),
    composition: d.composition, einvoice_applicable: d.einvoice_applicable,
  }
}

/** Active registrations with a GSTIN, primary first — the ones returns can be filed for. */
export function fileableRegistrations(regs: Registration[]): Registration[] {
  return regs.filter((r) => r.gstin && r.status === 'active').sort((a, b) => Number(b.is_primary) - Number(a.is_primary) || a.gstin.localeCompare(b.gstin))
}

/** "27AAPFU0939F1ZV · Maharashtra · Pune" for selects and headers. */
export function registrationLabel(r: Pick<Registration, 'gstin' | 'state_code' | 'city' | 'trade_name' | 'legal_name'>): string {
  return [r.gstin || 'Unregistered', STATES[r.state_code] ?? r.state_code, r.city || r.trade_name || r.legal_name].filter(Boolean).join(' · ')
}

/* ---------------- return periods (MMYYYY) ---------------- */

/** "102026" → { year: 2026, month: 10 }, null when malformed. */
export function parseMMYYYY(p: string): { year: number; month: number } | null {
  if (!/^\d{6}$/.test(p)) return null
  const month = Number(p.slice(0, 2))
  const year = Number(p.slice(2))
  if (month < 1 || month > 12 || year < 2017) return null
  return { year, month }
}

/** "2026-10" → "102026". */
export function toMMYYYY(yyyyMm: string): string {
  const [y, m] = yyyyMm.split('-')
  return `${m.padStart(2, '0')}${y}`
}

/** "102026" → "2026-10". */
export function fromMMYYYY(p: string): string {
  const v = parseMMYYYY(p)
  return v ? `${v.year}-${String(v.month).padStart(2, '0')}` : ''
}

const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec']

/** "102026" → "Oct 2026". */
export function periodLabel(p: string): string {
  const v = parseMMYYYY(p)
  return v ? `${MONTHS[v.month - 1]} ${v.year}` : p
}

/** First and last calendar day of an MMYYYY period (YYYY-MM-DD). */
export function periodRange(p: string): { from: string; to: string } | null {
  const v = parseMMYYYY(p)
  if (!v) return null
  const last = new Date(Date.UTC(v.year, v.month, 0)).getUTCDate()
  const mm = String(v.month).padStart(2, '0')
  return { from: `${v.year}-${mm}-01`, to: `${v.year}-${mm}-${String(last).padStart(2, '0')}` }
}

/* ---------------- rupees (kar) ⇄ paise ---------------- */

/** kar rupee number (two decimals) → integer paise. */
export const rupeesToPaise = (v: number | string | undefined | null) => Math.round(Number(v ?? 0) * 100)

/** Integer paise → "1234.50" — exact, for request bodies kar parses as rupees. */
export function paiseToRupeeString(p: number): string {
  const n = Math.round(p)
  const sign = n < 0 ? '-' : ''
  const a = Math.abs(n)
  return `${sign}${Math.floor(a / 100)}.${String(a % 100).padStart(2, '0')}`
}

/* ---------------- e-invoice / e-way windows ---------------- */

const HOUR = 3600_000

export type Check = { ok: true } | { ok: false; reason: string }

/** Can this IRN be cancelled now? 24 h after AckDt, never with an active e-way bill. */
export function irnCancelCheck(e: Pick<EInvoiceRecord, 'status' | 'ack_date' | 'cancellable_until'> | null | undefined, ewb: Pick<EWayRecord, 'status' | 'ewb_no'> | null | undefined, now = Date.now()): Check {
  if (!e || e.status !== 'generated') return { ok: false, reason: 'There is no active IRN to cancel.' }
  const until = e.cancellable_until ? Date.parse(e.cancellable_until) : e.ack_date ? Date.parse(e.ack_date) + 24 * HOUR : NaN
  if (Number.isFinite(until) && now > until) return { ok: false, reason: 'The 24-hour cancellation window is over. Issue a credit note instead.' }
  if (ewb && ewb.status === 'generated') return { ok: false, reason: `E-way bill ${ewb.ewb_no ?? ''} is active for this invoice. Cancel it before the IRN.`.replace('  ', ' ') }
  return { ok: true }
}

/** E-way bill cancellation: within 24 h of generation. */
export function ewayCancelCheck(w: Pick<EWayRecord, 'status' | 'ewb_date'> | null | undefined, now = Date.now()): Check {
  if (!w || w.status !== 'generated') return { ok: false, reason: 'There is no active e-way bill.' }
  if (w.ewb_date && now > Date.parse(w.ewb_date) + 24 * HOUR) return { ok: false, reason: 'An e-way bill can be cancelled only within 24 hours of generation.' }
  return { ok: true }
}

/** Validity extension: from 8 h before to 8 h after valid_upto. */
export function ewayExtendCheck(w: Pick<EWayRecord, 'status' | 'valid_upto'> | null | undefined, now = Date.now()): Check {
  if (!w || w.status !== 'generated') return { ok: false, reason: 'There is no active e-way bill.' }
  if (!w.valid_upto) return { ok: false, reason: 'A Part-A-only e-way bill has no validity yet; update Part-B first.' }
  const end = Date.parse(w.valid_upto)
  if (now < end - 8 * HOUR) return { ok: false, reason: 'Validity can be extended from 8 hours before it ends.' }
  if (now > end + 8 * HOUR) return { ok: false, reason: 'More than 8 hours have passed since the validity ended.' }
  return { ok: true }
}

export const EWAY_MODES: { value: '1' | '2' | '3' | '4'; label: string }[] = [
  { value: '1', label: 'Road' }, { value: '2', label: 'Rail' }, { value: '3', label: 'Air' }, { value: '4', label: 'Ship' },
]
export const ewayModeLabel = (m?: string) => EWAY_MODES.find((x) => x.value === m)?.label ?? (m || '—')

/** IRN cancel reasons (IRP CnlRsn). */
export const IRN_CANCEL_REASONS = [{ code: 1, label: 'Duplicate' }, { code: 2, label: 'Data entry mistake' }, { code: 3, label: 'Order cancelled' }, { code: 4, label: 'Others' }]
/** E-way bill cancel reasons (NIC cancelRsnCode) — note 2 and 3 are swapped vs the IRP list. */
export const EWB_CANCEL_REASONS = [{ code: 1, label: 'Duplicate' }, { code: 2, label: 'Order cancelled' }, { code: 3, label: 'Data entry mistake' }, { code: 4, label: 'Others' }]
/** Part-B (vehicle change) reasons. */
export const PARTB_REASONS = [{ code: '1', label: 'Due to break down' }, { code: '2', label: 'Due to transhipment' }, { code: '3', label: 'Others' }, { code: '4', label: 'First time' }] as const
/** Validity extension reasons (NIC extnRsnCode). */
export const EXTEND_REASONS = [{ code: 1, label: 'Natural calamity' }, { code: 2, label: 'Law and order situation' }, { code: 4, label: 'Transhipment' }, { code: 5, label: 'Accident' }, { code: 99, label: 'Others' }]

/** Vehicle numbers as NIC wants them: upper case, no spaces or dashes. */
export const normaliseVehicleNo = (v: string) => v.toUpperCase().replace(/[\s-]/g, '')

/* ---------------- GSTR-2B ---------------- */

export const TWOB_STATUSES: TwoBStatus[] = ['matched', 'mismatch', 'missing_in_books', 'missing_in_2b']
export const TWOB_STATUS_LABEL: Record<TwoBStatus, string> = {
  matched: 'Matched', mismatch: 'Mismatch', missing_in_books: 'Not in books', missing_in_2b: 'Not in 2B',
}

/** The IMS action shown for a row: an explicit action, or "none" (deemed accepted when the period closes). */
export const imsActionOf = (r: Pick<ReconResult, 'ims'>): IMSAction | 'none' => r.ims?.action ?? 'none'

/** Counts by match status and by IMS action, for the filter chips. Rows without a 2B document cannot carry an action. */
export function groupTwoB(items: ReconResult[]) {
  const byStatus: Record<TwoBStatus, number> = { matched: 0, mismatch: 0, missing_in_books: 0, missing_in_2b: 0 }
  const byAction: Record<IMSAction | 'none', number> = { accept: 0, reject: 0, pending: 0, none: 0 }
  let acceptable = 0
  for (const it of items) {
    byStatus[it.status] = (byStatus[it.status] ?? 0) + 1
    if (!it.doc_key) continue
    const a = imsActionOf(it)
    byAction[a] += 1
    if (it.status === 'matched' && (a === 'none' || a === 'pending')) acceptable += 1
  }
  return { byStatus, byAction, acceptable, total: items.length }
}

/** The row's text for search: supplier, GSTIN, 2B document number, our bill number. */
function twoBHaystack(r: ReconResult): string {
  return [r.doc?.supplier_name, r.doc?.supplier_gstin, r.doc?.doc_no, r.purchase?.supplier_name, r.purchase?.supplier_gstin, r.purchase?.number, r.purchase?.supplier_invoice_no]
    .filter(Boolean).join(' ').toLowerCase()
}

/** Rows of a reconciliation for one status chip ('' = all), IMS action ('' = any) and a free-text search. */
export function filterTwoB(items: ReconResult[], status: TwoBStatus | '', q = '', action: IMSAction | 'none' | '' = ''): ReconResult[] {
  const needle = q.trim().toLowerCase()
  return items.filter((r) => (!status || r.status === status)
    && (!action || (r.doc_key ? imsActionOf(r) === action : false))
    && (!needle || twoBHaystack(r).includes(needle)))
}

/** Total tax (rupees, kar's two-decimal numbers) of a 2B document or a purchase bill, exact via paise. */
export const taxOf = (x: { igst: number; cgst: number; sgst: number; cess: number } | undefined | null) =>
  x ? (rupeesToPaise(x.igst) + rupeesToPaise(x.cgst) + rupeesToPaise(x.sgst) + rupeesToPaise(x.cess)) / 100 : 0

const csvCell = (v: unknown): string => {
  if (v === null || v === undefined) return ''
  const s = typeof v === 'string' ? v : typeof v === 'number' || typeof v === 'boolean' ? String(v) : JSON.stringify(v)
  return /[",\r\n]/.test(s) || s !== s.trim() ? `"${s.replace(/"/g, '""')}"` : s
}

/** The reconciliation as a CSV sheet (one row per result; amounts in rupees as kar sent them). */
export function twoBCsv(items: ReconResult[]): string {
  const header = ['status', 'ims_action', 'section', 'supplier_gstin', 'supplier_name', 'doc_type', 'doc_no', 'doc_date', 'itc_available', '2b_taxable', '2b_igst', '2b_cgst', '2b_sgst', '2b_cess',
    'bill_number', 'supplier_invoice_no', 'bill_date', 'books_taxable', 'books_igst', 'books_cgst', 'books_sgst', 'books_cess', 'differences', 'notes', 'doc_key']
  const rows = items.map((r) => {
    const d = r.doc
    const p = r.purchase
    return [r.status, r.doc_key ? imsActionOf(r) : '', d?.section, d?.supplier_gstin ?? p?.supplier_gstin, d?.supplier_name || p?.supplier_name, d?.doc_type, d?.doc_no, d?.doc_date, d?.itc_available,
      d?.taxable, d?.igst, d?.cgst, d?.sgst, d?.cess,
      p?.number, p?.supplier_invoice_no, p?.supplier_invoice_date || p?.invoice_date, p?.taxable, p?.igst, p?.cgst, p?.sgst, p?.cess,
      (r.diffs ?? []).map((x) => `${x.field}: books ${String(x.books)} / 2B ${String(x.gstr2b)}`).join('; '), (r.notes ?? []).join('; '), r.doc_key]
  })
  return '﻿' + [header, ...rows].map((row) => row.map(csvCell).join(',')).join('\r\n') + '\r\n'
}

/* ---------------- purchase ITC ---------------- */

export const ITC_ELIGIBILITY: { value: ItcEligibility; label: string; short: string }[] = [
  { value: 'eligible', label: 'ITC eligible', short: 'Eligible' },
  { value: 'ineligible_17_5', label: 'Blocked credit — Sec 17(5) (motor vehicles, food, personal use…)', short: 'Blocked 17(5)' },
  { value: 'ineligible_other', label: 'Ineligible — other (exempt supplies, non-business use…)', short: 'Ineligible' },
]

/**
 * A purchase from a supplier without a GSTIN that still charges GST: the tax is not claimable
 * (no GSTR-2B entry) and the bill is probably wrong. Reverse charge is the exception (we pay the tax).
 */
export function noGstinItcWarning(supplierGstin: string | undefined | null, taxPaise: number, reverseCharge = false): string | null {
  if (reverseCharge || taxPaise <= 0 || (supplierGstin ?? '').trim()) return null
  return 'This supplier has no GSTIN but the bill charges GST. Unregistered suppliers cannot charge GST, and the tax will not appear in GSTR-2B, so it cannot be claimed as ITC (it shows under "no supplier GSTIN" in GSTR-3B). Check the bill, add the supplier\'s GSTIN, or set the lines to 0 %.'
}

/* ---------------- ITC opening ---------------- */

export interface OpeningPaise { igst: number; cgst: number; sgst: number; cess: number }

/** GET /v1/returns/itc-opening (rupees) → integer paise per head. */
export const openingToPaise = (o: { igst: number; cgst: number; sgst: number; cess: number } | undefined | null): OpeningPaise =>
  ({ igst: rupeesToPaise(o?.igst), cgst: rupeesToPaise(o?.cgst), sgst: rupeesToPaise(o?.sgst), cess: rupeesToPaise(o?.cess) })

/** Body of PUT /v1/returns/itc-opening: rupees as exact decimal strings (kar's Rs accepts numeric strings). */
export function openingBody(gstin: string, period: string, p: OpeningPaise) {
  return { gstin, period, igst: paiseToRupeeString(p.igst), cgst: paiseToRupeeString(p.cgst), sgst: paiseToRupeeString(p.sgst), cess: paiseToRupeeString(p.cess) }
}
