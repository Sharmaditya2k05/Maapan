'use client'

/** Per-device preferences (printer, counter behaviour). Stored in localStorage; never secrets. */
import { useSyncExternalStore } from 'react'

export interface Prefs {
  /** Receipt printer: thermal 80 mm, thermal 58 mm, or A4 laser/inkjet. */
  printer: 'thermal80' | 'thermal58' | 'a4'
  /** Open the print dialog as soon as a bill is saved at the counter. */
  autoPrint: boolean
  /** Ask for the customer's phone before saving a counter bill (for WhatsApp receipts). */
  askPhone: boolean
  /** Default tender mode on the counter. */
  defaultTender: 'cash' | 'upi' | 'card'
  /** Beep on a successful barcode scan. */
  beep: boolean
}

const KEY = 'taraju.prefs.v1'
const DEFAULTS: Prefs = { printer: 'thermal80', autoPrint: true, askPhone: false, defaultTender: 'cash', beep: true }
const listeners = new Set<() => void>()
let cache: Prefs | null = null

function load(): Prefs {
  if (cache) return cache
  if (typeof window === 'undefined') return DEFAULTS
  try { cache = { ...DEFAULTS, ...JSON.parse(localStorage.getItem(KEY) || '{}') } } catch { cache = { ...DEFAULTS } }
  return cache!
}

export const prefs = {
  get: load,
  set(patch: Partial<Prefs>) {
    cache = { ...load(), ...patch }
    try { localStorage.setItem(KEY, JSON.stringify(cache)) } catch { /* ignore */ }
    listeners.forEach((l) => l())
  },
  subscribe(fn: () => void) { listeners.add(fn); return () => { listeners.delete(fn) } },
}

/** React binding: `const [p, setPrefs] = usePrefs()`. */
export function usePrefs(): [Prefs, (patch: Partial<Prefs>) => void] {
  const p = useSyncExternalStore(prefs.subscribe, prefs.get, () => DEFAULTS)
  return [p, prefs.set]
}
