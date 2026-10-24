/** Store scoping, a copy of mool/auth StoreScoped: cashiers and store managers always act in their own store(s). */
import type { Role } from '../rbac'
import type { Store } from '../types'

export const isStoreScoped = (role: Role | undefined) => role === 'cashier' || role === 'store_manager'

/** Store name for an id, falling back to a short id for stores the user cannot see. */
export function storeName(stores: Store[], id: string | undefined): string {
  if (!id) return '—'
  return stores.find((s) => s.id === id)?.name ?? `Store ${id.slice(0, 8)}`
}

/** Collect query pages until total is reached (CSV exports), capped so a huge tenant cannot hang the tab. */
export async function fetchAllPages<T>(
  fetchPage: (limit: number, offset: number) => Promise<{ items: T[]; total: number }>,
  limit = 500, maxPages = 20,
): Promise<T[]> {
  const out: T[] = []
  for (let page = 0; page < maxPages; page++) {
    const r = await fetchPage(limit, page * limit)
    out.push(...(r.items ?? []))
    if (out.length >= r.total || (r.items ?? []).length < limit) break
  }
  return out
}
