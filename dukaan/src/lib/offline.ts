/**
 * Offline-first POS storage (IndexedDB via idb):
 *   catalog  – items for barcode/keyword search when the network is down
 *   blocks   – invoice number ranges reserved from aadesh for this device
 *   queue    – bills made offline (or whose upload failed), synced to /aadesh/v1/sync/invoices
 */
import { openDB, type DBSchema, type IDBPDatabase } from 'idb'
import { ApiError, get, post } from './api'
import { deviceId } from './session'
import type { CatalogItem, CreateInvoiceInput, Invoice, NumberBlock } from './types'

export type QueueStatus = 'pending' | 'syncing' | 'synced' | 'error'

export interface QueuedBill {
  client_uuid: string
  store_id: string
  input: CreateInvoiceInput
  display_number: string
  grand_total_paise: number
  status: QueueStatus
  error?: string
  server_number?: string
  invoice_id?: string
  created_at: string
}

export interface LocalBlock extends NumberBlock { key: string; store_id: string; next: number }

interface PosDB extends DBSchema {
  catalog: { key: string; value: CatalogItem & { store_id: string }; indexes: { by_store: string } }
  blocks: { key: string; value: LocalBlock }
  queue: { key: string; value: QueuedBill; indexes: { by_status: QueueStatus } }
  meta: { key: string; value: { key: string; value: string } }
}

let dbp: Promise<IDBPDatabase<PosDB>> | null = null
function db() {
  dbp ??= openDB<PosDB>('taraju-pos', 1, {
    upgrade(d) {
      d.createObjectStore('catalog', { keyPath: 'id' }).createIndex('by_store', 'store_id')
      d.createObjectStore('blocks', { keyPath: 'key' })
      d.createObjectStore('queue', { keyPath: 'client_uuid' }).createIndex('by_status', 'status')
      d.createObjectStore('meta', { keyPath: 'key' })
    },
  })
  return dbp
}

/* ---------------- catalog ---------------- */

/** Fresh catalog from bhandar when online, cached copy otherwise. */
export async function loadCatalog(storeId: string): Promise<{ items: CatalogItem[]; fromCache: boolean }> {
  const d = await db()
  try {
    const res = await get<{ items: CatalogItem[] }>('/bhandar/v1/items/pos-catalog', { store_id: storeId })
    const tx = d.transaction('catalog', 'readwrite')
    for (const old of await tx.store.index('by_store').getAllKeys(storeId)) await tx.store.delete(old)
    for (const it of res.items ?? []) await tx.store.put({ ...it, store_id: storeId })
    await tx.done
    await d.put('meta', { key: `catalog:${storeId}`, value: new Date().toISOString() })
    return { items: res.items ?? [], fromCache: false }
  } catch {
    return { items: await d.getAllFromIndex('catalog', 'by_store', storeId), fromCache: true }
  }
}

/* ---------------- number blocks ---------------- */

const blockKey = (storeId: string, doc = 'sale') => `${storeId}:${doc}`

/** Reserve a new block when none is left or less than 20% remains. Safe to call often. */
// 50 stays under every role's server cap (cashier 100) and is refilled at 20 % left.
export async function ensureBlock(storeId: string, size = 50): Promise<LocalBlock | undefined> {
  const d = await db()
  const cur = await d.get('blocks', blockKey(storeId))
  const remaining = cur ? cur.end_no - cur.next + 1 : 0
  if (cur && remaining > (cur.end_no - cur.start_no + 1) * 0.2) return cur
  try {
    const b = await post<NumberBlock>('/aadesh/v1/number-blocks', { store_id: storeId, doc_type: 'sale', device_id: deviceId(), size })
    // Keep using the old block until it runs out; the new one replaces it only when empty.
    if (cur && remaining > 0) {
      await d.put('meta', { key: `nextblock:${storeId}`, value: JSON.stringify(b) })
      return cur
    }
    const lb: LocalBlock = { ...b, key: blockKey(storeId), store_id: storeId, next: b.start_no }
    await d.put('blocks', lb)
    return lb
  } catch {
    return cur
  }
}

/** Take the next offline number. Throws when the device has no numbers left. */
export async function takeOfflineNumber(storeId: string): Promise<{ number: number; display: string }> {
  const d = await db()
  let b = await d.get('blocks', blockKey(storeId))
  if (!b || b.next > b.end_no) {
    const queued = await d.get('meta', `nextblock:${storeId}`)
    if (!queued) throw new Error('No offline invoice numbers left on this device. Connect to the internet once to reserve more.')
    const nb = JSON.parse(queued.value) as NumberBlock
    b = { ...nb, key: blockKey(storeId), store_id: storeId, next: nb.start_no }
    await d.delete('meta', `nextblock:${storeId}`)
  }
  const number = b.next
  await d.put('blocks', { ...b, next: number + 1 })
  return { number, display: `${b.prefix}/${b.fy}/${String(number).padStart(5, '0')}` }
}

/**
 * Hand back a number taken with takeOfflineNumber when the bill was rejected
 * (validation, credit limit) so the device's sequence has no gap. Only the most
 * recent number can be returned.
 */
export async function returnOfflineNumber(storeId: string, number: number): Promise<void> {
  const d = await db()
  const b = await d.get('blocks', blockKey(storeId))
  if (b && b.next === number + 1 && number >= b.start_no) await d.put('blocks', { ...b, next: number })
}

export async function blockStatus(storeId: string) {
  const b = await (await db()).get('blocks', blockKey(storeId))
  return b ? { left: Math.max(0, b.end_no - b.next + 1), total: b.end_no - b.start_no + 1 } : { left: 0, total: 0 }
}

/* ---------------- offline queue ---------------- */

export async function enqueueBill(bill: QueuedBill) {
  // The server dates an offline bill by the moment it was made, not by when it finally syncs.
  if (!bill.input.client_created_at) bill.input.client_created_at = bill.created_at
  await (await db()).put('queue', bill)
  notify()
}

export async function listQueue(): Promise<QueuedBill[]> {
  const all = await (await db()).getAll('queue')
  return all.sort((a, b) => b.created_at.localeCompare(a.created_at))
}

export async function clearSynced() {
  const d = await db()
  for (const k of await d.getAllKeysFromIndex('queue', 'by_status', 'synced')) await d.delete('queue', k)
  notify()
}

/** `retry` means a downstream service (stock, parties) was unavailable: the bill stays queued. */
interface SyncResult { client_uuid: string; status: 'created' | 'duplicate' | 'error' | 'retry'; invoice?: Invoice; error?: { message: string } }

let syncing = false

/** Upload pending/error bills in chunks of 50. Idempotent on client_uuid, so retries are safe. */
export async function syncQueue(): Promise<{ synced: number; failed: number }> {
  if (syncing || (typeof navigator !== 'undefined' && !navigator.onLine)) return { synced: 0, failed: 0 }
  syncing = true
  let synced = 0, failed = 0
  try {
    const d = await db()
    const pending = [
      ...(await d.getAllFromIndex('queue', 'by_status', 'pending')),
      ...(await d.getAllFromIndex('queue', 'by_status', 'error')),
      ...(await d.getAllFromIndex('queue', 'by_status', 'syncing')), // interrupted uploads
    ]
    for (let i = 0; i < pending.length; i += 50) {
      const chunk = pending.slice(i, i + 50)
      for (const b of chunk) await d.put('queue', { ...b, status: 'syncing' })
      notify()
      try {
        const res = await post<{ results: SyncResult[] }>('/aadesh/v1/sync/invoices', { invoices: chunk.map((b) => b.input) })
        for (const r of res.results) {
          const b = chunk.find((x) => x.client_uuid === r.client_uuid)
          if (!b) continue
          if (r.status === 'error') {
            failed++
            await d.put('queue', { ...b, status: 'error', error: r.error?.message ?? 'rejected' })
          } else if (r.status === 'retry') {
            failed++
            await d.put('queue', { ...b, status: 'pending', error: r.error?.message ?? 'service unavailable, will retry' })
          } else {
            synced++
            await d.put('queue', { ...b, status: 'synced', error: undefined, server_number: r.invoice?.number, invoice_id: r.invoice?.id })
          }
        }
      } catch (e) {
        const msg = e instanceof ApiError ? e.message : 'network error'
        for (const b of chunk) await d.put('queue', { ...b, status: 'pending', error: msg })
        failed += chunk.length
        break
      }
    }
  } finally {
    syncing = false
    notify()
  }
  return { synced, failed }
}

/** Sync whenever the browser comes back online and every 30 s. Returns a stop function. */
export function startAutoSync(intervalMs = 30_000): () => void {
  const run = () => { void syncQueue() }
  window.addEventListener('online', run)
  const t = setInterval(run, intervalMs)
  run()
  return () => { window.removeEventListener('online', run); clearInterval(t) }
}

const subs = new Set<() => void>()
function notify() { subs.forEach((f) => f()) }
export function onQueueChange(fn: () => void) { subs.add(fn); return () => { subs.delete(fn) } }
