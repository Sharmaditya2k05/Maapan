/**
 * Small per-device stores for the counter (localStorage, never secrets):
 * held bills, the last bill made, and item-usage counts for the quick grid.
 */
import type { CatalogItemFull, HeldBill, LastBill } from '../types.pos'

const HELD = 'maapan.pos.held.v1'
const LAST = 'maapan.pos.last.v1'
const FREQ = 'maapan.pos.freq.v1'

function read<T>(key: string, fallback: T): T {
  if (typeof window === 'undefined') return fallback
  try { return (JSON.parse(localStorage.getItem(key) || 'null') as T | null) ?? fallback } catch { return fallback }
}
function write(key: string, value: unknown) {
  try { localStorage.setItem(key, JSON.stringify(value)) } catch { /* storage full or private mode */ }
}

/* ---------------- held bills ---------------- */

export const listHeld = (storeId: string): HeldBill[] => read<HeldBill[]>(HELD, []).filter((b) => b.store_id === storeId).sort((a, b) => b.held_at.localeCompare(a.held_at))

export function holdBill(bill: HeldBill): HeldBill[] {
  const all = read<HeldBill[]>(HELD, []).filter((b) => b.id !== bill.id)
  all.push(bill)
  write(HELD, all.slice(-50))
  return listHeld(bill.store_id)
}

export function removeHeld(storeId: string, id: string): HeldBill[] {
  write(HELD, read<HeldBill[]>(HELD, []).filter((b) => b.id !== id))
  return listHeld(storeId)
}

/* ---------------- last bill ---------------- */

export const readLastBill = (storeId: string): LastBill | null => read<Record<string, LastBill>>(LAST, {})[storeId] ?? null

export function saveLastBill(storeId: string, bill: LastBill) {
  const all = read<Record<string, LastBill>>(LAST, {})
  all[storeId] = bill
  write(LAST, all)
}

/* ---------------- frequent items ---------------- */

type Freq = Record<string, { n: number; t: number }>

export function bumpFrequent(storeId: string, itemIds: string[]) {
  const all = read<Record<string, Freq>>(FREQ, {})
  const f = all[storeId] ?? {}
  const now = Date.now()
  for (const id of itemIds) f[id] = { n: (f[id]?.n ?? 0) + 1, t: now }
  // Keep the table small: the 200 most recently used.
  const kept = Object.entries(f).sort((a, b) => b[1].t - a[1].t).slice(0, 200)
  all[storeId] = Object.fromEntries(kept)
  write(FREQ, all)
}

/** Frequently and recently used items first, padded with the catalog's first items. */
export function quickItems(storeId: string, catalog: CatalogItemFull[], n = 12): CatalogItemFull[] {
  const f = read<Record<string, Freq>>(FREQ, {})[storeId] ?? {}
  const byId = new Map(catalog.map((c) => [c.id, c]))
  const ranked = Object.entries(f)
    .filter(([id]) => byId.has(id))
    .sort((a, b) => b[1].n - a[1].n || b[1].t - a[1].t)
    .map(([id]) => byId.get(id)!)
  const out = ranked.slice(0, n)
  for (const c of catalog) {
    if (out.length >= n) break
    if (!out.includes(c)) out.push(c)
  }
  return out
}
