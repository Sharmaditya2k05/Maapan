/** Pure helpers for the money screens: date presets, balance wording, phone normalisation, GST period codes. No React. */
import { formatINR } from '../money.ts'
import type { Invoice } from '../types.ts'

/** Where an invoice of a given doc type is viewed in dukaan. */
export function invoiceHref(inv: Pick<Invoice, 'id' | 'doc_type'>): string {
  if (inv.doc_type === 'sale') return `/app/sales/${inv.id}`
  if (inv.doc_type === 'purchase') return `/app/purchases/${inv.id}`
  return `/app/returns/${inv.id}`
}

/* ---------------- dates ---------------- */

export interface DateRange { from: string; to: string }

export type RangePreset = 'today' | 'yesterday' | 'this_week' | 'this_month' | 'last_month' | 'this_quarter' | 'this_fy' | 'last_fy' | 'custom'

export const RANGE_PRESET_LABEL: Record<RangePreset, string> = {
  today: 'Today', yesterday: 'Yesterday', this_week: 'This week', this_month: 'This month', last_month: 'Last month',
  this_quarter: 'This quarter', this_fy: 'This FY', last_fy: 'Last FY', custom: 'Custom',
}

const pad = (n: number) => String(n).padStart(2, '0')
export const ymd = (y: number, m: number, d: number) => `${y}-${pad(m)}-${pad(d)}`

/** Parse YYYY-MM-DD into [y, m, d] numbers (no timezone maths). */
export function splitDate(s: string): [number, number, number] {
  return [Number(s.slice(0, 4)), Number(s.slice(5, 7)), Number(s.slice(8, 10))]
}

/** Add days to a YYYY-MM-DD string using UTC arithmetic (dates only, so no DST issues). */
export function addDays(s: string, n: number): string {
  const [y, m, d] = splitDate(s)
  const t = Date.UTC(y, m - 1, d) + n * 86400_000
  return new Date(t).toISOString().slice(0, 10)
}

/** Last day of the month that contains s. */
export function endOfMonth(s: string): string {
  const [y, m] = splitDate(s)
  return ymd(y, m, new Date(Date.UTC(y, m, 0)).getUTCDate())
}

/** Indian FY start year for a date: 2026-03-31 → 2025, 2026-04-01 → 2026. */
export function fyYearOf(s: string): number {
  const [y, m] = splitDate(s)
  return m < 4 ? y - 1 : y
}

/** FY label "2026-27". */
export const fyLabel = (startYear: number) => `${startYear}-${String(startYear + 1).slice(2)}`

/** Resolve a preset to a concrete range given today's date (YYYY-MM-DD). `custom` returns `current` unchanged. */
export function presetRange(preset: RangePreset, today: string, current?: DateRange): DateRange {
  const [y, m, d] = splitDate(today)
  switch (preset) {
    case 'today': return { from: today, to: today }
    case 'yesterday': { const t = addDays(today, -1); return { from: t, to: t } }
    case 'this_week': {
      // Monday-start week.
      const dow = (new Date(Date.UTC(y, m - 1, d)).getUTCDay() + 6) % 7
      return { from: addDays(today, -dow), to: today }
    }
    case 'this_month': return { from: ymd(y, m, 1), to: today }
    case 'last_month': {
      const first = ymd(y, m, 1)
      const lastPrev = addDays(first, -1)
      const [py, pm] = splitDate(lastPrev)
      return { from: ymd(py, pm, 1), to: lastPrev }
    }
    case 'this_quarter': {
      const qStart = m - ((m - 1) % 3)
      return { from: ymd(y, qStart, 1), to: today }
    }
    case 'this_fy': return { from: `${fyYearOf(today)}-04-01`, to: today }
    case 'last_fy': { const fy = fyYearOf(today) - 1; return { from: `${fy}-04-01`, to: `${fy + 1}-03-31` } }
    case 'custom': default: return current ?? { from: today, to: today }
  }
}

/** Which preset a range corresponds to today (so the picker can highlight it), else 'custom'. */
export function detectPreset(range: DateRange, today: string, candidates: RangePreset[]): RangePreset {
  for (const p of candidates) {
    if (p === 'custom') continue
    const r = presetRange(p, today)
    if (r.from === range.from && r.to === range.to) return p
  }
  return 'custom'
}

/** The twelve months of an FY as ranges, clipped to `today` (future months are dropped). */
export function fyMonths(startYear: number, today: string): { key: string; label: string; from: string; to: string }[] {
  const out: { key: string; label: string; from: string; to: string }[] = []
  for (let i = 0; i < 12; i++) {
    const m = ((3 + i) % 12) + 1
    const y = m >= 4 ? startYear : startYear + 1
    const from = ymd(y, m, 1)
    if (from > today) break
    const to = endOfMonth(from) < today ? endOfMonth(from) : today
    out.push({ key: `${y}-${pad(m)}`, label: monthLabel(`${y}-${pad(m)}`), from, to })
  }
  return out
}

const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec']
/** "2026-10" → "Oct 2026". */
export function monthLabel(yyyyMm: string): string {
  const [y, m] = splitDate(`${yyyyMm}-01`)
  return `${MONTHS[m - 1] ?? '?'} ${y}`
}

/** GST return period: "2026-10" → "102026" (MMYYYY, what kar expects). */
export function gstPeriod(yyyyMm: string): string {
  const [y, m] = splitDate(`${yyyyMm}-01`)
  return `${pad(m)}${y}`
}

/** Months available for a GST return picker: from the start of last FY up to the current month, latest first. */
export function returnMonths(today: string): { value: string; label: string }[] {
  const [ty, tm] = splitDate(today)
  const start = fyYearOf(today) - 1
  const out: { value: string; label: string }[] = []
  for (let y = start; y <= ty; y++) {
    for (let m = 1; m <= 12; m++) {
      if (y === start && m < 4) continue
      if (y === ty && m > tm) break
      out.push({ value: `${y}-${pad(m)}`, label: monthLabel(`${y}-${pad(m)}`) })
    }
  }
  return out.reverse()
}

/* ---------------- money wording ---------------- */

export type BalanceTone = 'green' | 'red' | 'gray'

/** Party balance (+ receivable / − payable) as the shopkeeper reads it. */
export function balanceLabel(paise: number): { text: string; tone: BalanceTone; direction: 'collect' | 'pay' | 'settled' } {
  if (paise > 0) return { text: `${formatINR(paise)} to collect`, tone: 'green', direction: 'collect' }
  if (paise < 0) return { text: `${formatINR(-paise)} to pay`, tone: 'red', direction: 'pay' }
  return { text: 'Settled', tone: 'gray', direction: 'settled' }
}

/** Running balance with the accountant's suffix: 1,000 Dr / 500 Cr. */
export function fmtDrCr(paise: number): string {
  if (paise === 0) return `${formatINR(0)}`
  return `${formatINR(Math.abs(paise))} ${paise > 0 ? 'Dr' : 'Cr'}`
}

/** Credit used as a 0–100 percentage of the limit (over-limit caps at 100). */
export function creditUsagePct(balancePaise: number, limitPaise: number): number {
  if (limitPaise <= 0) return 0
  const used = Math.max(0, balancePaise)
  return Math.min(100, Math.round((used / limitPaise) * 100))
}

/* ---------------- phones & WhatsApp ---------------- */

/** The 10-digit Indian mobile number inside a free-text phone, or null. */
export function mobile10(phone: string | undefined | null): string | null {
  const digits = (phone ?? '').replace(/\D/g, '')
  const d = digits.length === 12 && digits.startsWith('91') ? digits.slice(2) : digits.length === 11 && digits.startsWith('0') ? digits.slice(1) : digits
  return /^[6-9]\d{9}$/.test(d) ? d : null
}

/** +91XXXXXXXXXX as lenden validates it, or null. */
export function toE164IN(phone: string | undefined | null): string | null {
  const m = mobile10(phone)
  return m ? `+91${m}` : null
}

/** A wa.me link that opens WhatsApp with the text ready to send. Null when the phone is unusable. */
export function waLink(phone: string | undefined | null, text: string): string | null {
  const m = mobile10(phone)
  return m ? `https://wa.me/91${m}?text=${encodeURIComponent(text)}` : null
}

/* ---------------- lenden refund wording ---------------- */

/** Honest, one-line wording for each refund state (see lenden README "Refund states"). */
export function refundStateText(status: string): { title: string; text: string; tone: 'green' | 'amber' | 'red' | 'gray' } {
  switch (status) {
    case 'processed': return { title: 'Refund processed', text: 'The gateway confirmed the refund. The ledger entry is posted.', tone: 'green' }
    case 'pending': return { title: 'Refund pending', text: 'The gateway accepted the refund and will confirm it by webhook. The amount is reserved until then.', tone: 'amber' }
    case 'unknown': return { title: 'Refund state unknown', text: 'The gateway did not answer. The refund may have gone through; the amount stays reserved and reconciliation will settle it within the hour. Do not refund again.', tone: 'amber' }
    case 'failed': return { title: 'Refund failed', text: 'The gateway rejected the refund. Nothing was returned; the amount is refundable again.', tone: 'red' }
    default: return { title: `Refund ${status}`, text: '', tone: 'gray' }
  }
}

/** kar verify `source` → wording the user should see. */
export function gstinSourceText(source: string): string {
  switch (source) {
    case 'gsp': return 'Checked with the GST portal just now'
    case 'cache': return 'Checked with the GST portal within the last 7 days'
    case 'checksum_only': return 'Format valid, not checked with the GST portal'
    default: return source
  }
}

/** Cheque lifecycle (mirrors bahi chequeTransitions). */
export function chequeNextStatuses(direction: 'received' | 'issued', status: string): string[] {
  if (direction === 'received') {
    if (status === 'received') return ['deposited', 'cancelled']
    if (status === 'deposited') return ['cleared', 'bounced']
    return []
  }
  if (status === 'issued') return ['cleared', 'bounced', 'cancelled']
  return []
}

/** Voucher balance check for the manual voucher form. */
export function voucherBalance(entries: { debit_paise: number; credit_paise: number; account_id?: string; party_id?: string }[]) {
  const debit = entries.reduce((a, e) => a + (e.debit_paise || 0), 0)
  const credit = entries.reduce((a, e) => a + (e.credit_paise || 0), 0)
  const complete = entries.length >= 2 && entries.every((e) => (e.account_id || e.party_id) && ((e.debit_paise > 0) !== (e.credit_paise > 0)))
  return { debit, credit, diff: debit - credit, balanced: debit === credit && debit > 0, ok: complete && debit === credit && debit > 0 }
}
