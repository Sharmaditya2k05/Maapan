/**
 * Pure helpers for the phase-4 money screens: bill allocation, FY / quarter codes, TDS rate wording,
 * bank-line match candidates, bulk consent address parsing and the gateway status banner. No React.
 */
import { addDays, splitDate, toE164IN } from './util.ts'

/* ---------------- bill-wise allocation ---------------- */

export interface OpenBillLite { id: string; due_date: string; bill_date: string; outstanding_paise: number }
export interface Alloc { bill_id: string; amount_paise: number }

/** Settle `amount` against bills oldest due date first (bahi's auto-allocation order). */
export function fifoAllocate(bills: OpenBillLite[], amount: number): Alloc[] {
  const sorted = [...bills].filter((b) => b.outstanding_paise > 0)
    .sort((a, b) => (a.due_date === b.due_date ? a.bill_date.localeCompare(b.bill_date) : a.due_date.localeCompare(b.due_date)))
  const out: Alloc[] = []
  let left = Math.max(0, Math.round(amount))
  for (const b of sorted) {
    if (left <= 0) break
    const take = Math.min(left, b.outstanding_paise)
    out.push({ bill_id: b.id, amount_paise: take })
    left -= take
  }
  return out
}

export const allocTotal = (allocs: Alloc[]) => allocs.reduce((a, x) => a + (x.amount_paise || 0), 0)

/**
 * Checks allocations the way bahi does (422 otherwise): positive amounts, each within its bill's
 * outstanding, a bill listed once, the total within what the voucher can settle. Null = fine.
 */
export function checkAllocations(allocs: Alloc[], outstanding: Record<string, number>, available: number): string | null {
  const seen = new Set<string>()
  for (const a of allocs) {
    if (!a.bill_id || !(a.amount_paise > 0)) return 'Every allocation needs a bill and a positive amount.'
    if (seen.has(a.bill_id)) return 'A bill is listed twice.'
    seen.add(a.bill_id)
    const max = outstanding[a.bill_id]
    if (max === undefined) return 'An allocation points at a bill that is not open.'
    if (a.amount_paise > max) return 'An allocation is more than the bill’s outstanding.'
  }
  if (allocTotal(allocs) > available) return 'Allocations add up to more than the voucher amount.'
  return null
}

/** Drops zero rows so the body only carries real allocations. */
export const cleanAllocations = (allocs: Alloc[]) => allocs.filter((a) => a.amount_paise > 0)

/* ---------------- financial years ---------------- */

/** bahi's FY code for a date: 2026-11-03 → "26-27", 2026-03-31 → "25-26". */
export function fyCode(date: string): string {
  const [y, m] = splitDate(date)
  const start = m < 4 ? y - 1 : y
  return `${String(start % 100).padStart(2, '0')}-${String((start + 1) % 100).padStart(2, '0')}`
}

/** "26-27" → 2026 (start year, assuming 20xx). */
export function fyStartYear(code: string): number {
  return 2000 + Number(code.slice(0, 2))
}

/** FY codes from the current one back `count` years, latest first. */
export function fyCodeOptions(today: string, count = 4): string[] {
  const [y, m] = splitDate(today)
  const start = m < 4 ? y - 1 : y
  return Array.from({ length: count }, (_, i) => fyCode(`${start - i}-04-01`))
}

/** The most recent FY that has fully ended (the one a year-end close applies to). */
export function lastEndedFY(today: string): string {
  const [y, m] = splitDate(today)
  const start = m < 4 ? y - 1 : y
  return fyCode(`${start - 1}-04-01`)
}

/** TDS quarter of a date within its FY: Apr–Jun 1, Jul–Sep 2, Oct–Dec 3, Jan–Mar 4. */
export function tdsQuarter(date: string): 1 | 2 | 3 | 4 {
  const [, m] = splitDate(date)
  if (m >= 4 && m <= 6) return 1
  if (m >= 7 && m <= 9) return 2
  if (m >= 10) return 3
  return 4
}

/** Basis points as a percentage: 10 → "0.1%", 200 → "2%", 75 → "0.75%". */
export function bpPct(bp: number): string {
  return `${Number((bp / 100).toFixed(2))}%`
}

/* ---------------- bank statement matching ---------------- */

export interface BookLine { date: string; voucher_id: string; voucher_number: string; narration: string; debit_paise: number; credit_paise: number }

/**
 * Book postings a statement line could match by hand: same amount on the right side (a deposit is a
 * debit to the bank account in the books) dated within −before/+after days of the line, closest first.
 */
export function matchCandidates(line: { date: string; deposit_paise: number; withdrawal_paise: number }, book: BookLine[], before = 3, after = 10): BookLine[] {
  const from = addDays(line.date, -after)
  const to = addDays(line.date, before)
  const want = line.deposit_paise > 0 ? { debit: line.deposit_paise, credit: 0 } : { debit: 0, credit: line.withdrawal_paise }
  const dist = (d: string) => Math.abs(Date.parse(`${d}T00:00:00Z`) - Date.parse(`${line.date}T00:00:00Z`))
  return book
    .filter((b) => b.date >= from && b.date <= to && (want.debit ? b.debit_paise === want.debit : b.credit_paise === want.credit))
    .sort((a, b) => dist(a.date) - dist(b.date))
}

/* ---------------- consents ---------------- */

/** Splits pasted phones / e-mails (one per line, or comma / semicolon / space separated) into normalised addresses. */
export function parseAddresses(text: string): { valid: string[]; invalid: string[] } {
  const valid: string[] = []
  const invalid: string[] = []
  const seen = new Set<string>()
  for (const raw of text.split(/[\n,;]+/)) {
    const t = raw.trim()
    if (!t) continue
    let addr: string | null = null
    if (t.includes('@')) addr = /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(t) ? t.toLowerCase() : null
    else addr = toE164IN(t)
    if (!addr) { invalid.push(t); continue }
    if (seen.has(addr)) continue
    seen.add(addr)
    valid.push(addr)
  }
  return { valid, invalid }
}

/** "0, 7,15 30" → [0, 7, 15, 30] (sorted, unique); null when anything is not a whole number 0–365. */
export function parseDays(text: string): number[] | null {
  const parts = text.split(/[\s,]+/).filter(Boolean)
  const out = new Set<number>()
  for (const p of parts) {
    if (!/^\d+$/.test(p)) return null
    const n = Number(p)
    if (n > 365) return null
    out.add(n)
  }
  return [...out].sort((a, b) => a - b)
}

/* ---------------- gateway status banner ---------------- */

/** Services each screen needs (prefix match on the path; longest prefix wins). pehchaan is always needed. */
const PATH_SERVICES: [string, string[]][] = [
  ['/app/pos', ['aadesh', 'bhandar', 'lenden']],
  ['/app/sales', ['aadesh']],
  ['/app/purchases', ['aadesh', 'bhandar']],
  ['/app/returns', ['aadesh']],
  ['/app/parties', ['bahi']],
  ['/app/payments', ['lenden']],
  ['/app/bank', ['bahi']],
  ['/app/tds', ['bahi']],
  ['/app/reports', ['bahi']],
  ['/app/gst', ['kar']],
  ['/app/items', ['bhandar']],
  ['/app/stock', ['bhandar']],
  ['/app/production', ['nirmaan', 'bhandar']],
  ['/app/import', ['aayaat']],
  ['/app/ai', ['buddhi']],
  ['/app/settings', []],
  ['/app', ['bahi', 'aadesh']],
]

export function servicesForPath(pathname: string): string[] {
  let best: [string, string[]] | undefined
  for (const e of PATH_SERVICES) {
    if ((pathname === e[0] || pathname.startsWith(`${e[0]}/`)) && (!best || e[0].length > best[0].length)) best = e
  }
  return ['pehchaan', ...(best?.[1] ?? [])]
}

export interface ServiceIssue { service: string; state: string }

/** Needed services the gateway does not report as up (unknown services are ignored: older gateways). */
export function statusIssues(services: Record<string, { status: string }> | undefined, needed: string[]): ServiceIssue[] {
  if (!services) return []
  const out: ServiceIssue[] = []
  for (const s of needed) {
    const st = services[s]?.status
    if (st && st !== 'up') out.push({ service: s, state: st })
  }
  return out
}

/** One sentence for the banner. */
export function statusMessage(issues: ServiceIssue[]): string {
  if (issues.length === 0) return ''
  const starting = issues.filter((i) => i.state === 'starting').map((i) => i.service)
  const down = issues.filter((i) => i.state !== 'starting').map((i) => i.service)
  const parts: string[] = []
  if (starting.length) parts.push(`${starting.join(', ')} ${starting.length > 1 ? 'are' : 'is'} starting up`)
  if (down.length) parts.push(`${down.join(', ')} ${down.length > 1 ? 'are' : 'is'} not reachable`)
  return `${parts.join('; ')}. Parts of this screen may not load yet.`
}
