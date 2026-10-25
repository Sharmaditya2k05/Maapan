/**
 * Printing goes through /print/invoice/[id] (a shell-less route that renders
 * only the document and calls window.print() when auto=1). The counter loads it
 * in a hidden iframe so the cashier never leaves the screen; detail pages open
 * it in a new tab.
 */
import type { Prefs } from '../prefs'

export type PrintLayout = 'a4' | 'thermal'

export const layoutFor = (p: Prefs): PrintLayout => (p.printer === 'a4' ? 'a4' : 'thermal')
export const thermalWidth = (p: Prefs): 58 | 80 => (p.printer === 'thermal58' ? 58 : 80)

export function printUrl(id: string, layout: PrintLayout, opts: { auto?: boolean; width?: 58 | 80 } = {}): string {
  const q = new URLSearchParams({ layout })
  if (opts.auto ?? true) q.set('auto', '1')
  if (layout === 'thermal' && opts.width === 58) q.set('w', '58')
  return `/print/invoice/${encodeURIComponent(id)}?${q.toString()}`
}

/** Print without leaving the page: a hidden iframe loads the print route, which prints itself. */
export function printInIframe(id: string, layout: PrintLayout, width?: 58 | 80) {
  if (typeof document === 'undefined') return
  const old = document.getElementById('maapan-print-frame')
  old?.remove()
  const f = document.createElement('iframe')
  f.id = 'maapan-print-frame'
  f.title = 'Printing'
  f.setAttribute('aria-hidden', 'true')
  f.style.cssText = 'position:fixed;right:0;bottom:0;width:1px;height:1px;border:0;opacity:0;pointer-events:none'
  f.src = printUrl(id, layout, { auto: true, width })
  document.body.appendChild(f)
  // The route prints as soon as the invoice loads; give the dialog plenty of time before tidying up.
  setTimeout(() => f.remove(), 120_000)
}

/** Open the print route in a new tab (detail-page buttons). */
export function openPrint(id: string, layout: PrintLayout, width?: 58 | 80) {
  window.open(printUrl(id, layout, { auto: true, width }), '_blank', 'noopener')
}
