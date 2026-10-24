/** CSV text for downloads (RFC 4180 quoting, CRLF rows, UTF-8). Pure. */
export type CSVCell = string | number | boolean | null | undefined

const cell = (v: CSVCell) => {
  if (v === null || v === undefined) return ''
  const s = String(v)
  return /[",\r\n]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s
}

export function toCSV(headers: string[], rows: CSVCell[][]): string {
  return [headers, ...rows].map((r) => r.map(cell).join(',')).join('\r\n') + '\r\n'
}

/** Paise → rupees with 2 decimals for spreadsheets (no ₹, no grouping). */
export const csvMoney = (paise: number | undefined | null) => ((paise ?? 0) / 100).toFixed(2)
