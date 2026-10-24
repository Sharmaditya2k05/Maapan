/** Pure CSV builder (RFC 4180): quotes what needs quoting, CRLF rows, UTF-8 BOM so Excel opens ₹ names correctly. */

export type CsvCell = string | number | boolean | null | undefined

/** One cell, quoted when it holds a comma, quote, CR/LF or leading/trailing space. */
export function csvEscape(v: CsvCell): string {
  if (v === null || v === undefined) return ''
  const s = typeof v === 'string' ? v : String(v)
  if (s === '') return ''
  const needs = /[",\r\n]/.test(s) || s !== s.trim()
  return needs ? `"${s.replace(/"/g, '""')}"` : s
}

/** Build a CSV document. `bom` (default true) prefixes U+FEFF for Excel. */
export function toCSV(header: CsvCell[], rows: CsvCell[][], bom = true): string {
  const lines = [header, ...rows].map((r) => r.map(csvEscape).join(','))
  return (bom ? '﻿' : '') + lines.join('\r\n') + '\r\n'
}

/** Paise → plain decimal rupees for spreadsheets ("1234.50", no symbol or grouping). Empty for 0 when `blankZero`. */
export function csvRupees(paise: number | null | undefined, blankZero = false): string {
  const p = paise ?? 0
  if (blankZero && p === 0) return ''
  const neg = p < 0
  const a = Math.abs(p)
  return `${neg ? '-' : ''}${Math.floor(a / 100)}.${String(a % 100).padStart(2, '0')}`
}

/** A safe file name from a title: "Party Statement: Ramesh & Co" → "party-statement-ramesh-co". */
export function csvFileName(title: string, suffix = ''): string {
  const base = title.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '') || 'export'
  return `${base}${suffix ? `-${suffix}` : ''}.csv`
}
