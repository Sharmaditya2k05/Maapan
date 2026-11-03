/** Pure helpers for the import wizard: accepted files, source detection, name → id map editing. No React. */

/** Extensions aayaat reads: Excel 2007+ / 97-2003 (BIFF8, HTML table or text), CSV/TSV/TXT and Tally XML. */
export const IMPORT_ACCEPT = '.xlsx,.xlsm,.xls,.csv,.tsv,.txt,.xml'

export function acceptImportFile(name: string): boolean {
  return /\.(xlsx|xlsm|xls|csv|tsv|txt|xml)$/i.test(name)
}

/** `.xml` exports are Tally's; everything else is up to the user. */
export function detectSource(name: string): '' | 'tally' {
  return /\.xml$/i.test(name) ? 'tally' : ''
}

/** {"Main Godown": "id"} → "Main Godown = id" lines (sorted by name). */
export function mapToText(map: Record<string, string> | undefined | null): string {
  return Object.entries(map ?? {}).sort(([a], [b]) => a.localeCompare(b)).map(([k, v]) => `${k} = ${v}`).join('\n')
}

/**
 * Parses "name = value" lines (also "name: value" or a tab) into a map. Blank lines and lines starting
 * with # are ignored. Returns the 1-based numbers of lines it could not read.
 */
export function textToMap(text: string): { map: Record<string, string>; bad: number[] } {
  const map: Record<string, string> = {}
  const bad: number[] = []
  text.split(/\r?\n/).forEach((line, i) => {
    const t = line.trim()
    if (!t || t.startsWith('#')) return
    const m = /^(.+?)\s*(?:=|:|\t)\s*(.+)$/.exec(t)
    if (!m || !m[1].trim() || !m[2].trim()) { bad.push(i + 1); return }
    map[m[1].trim()] = m[2].trim()
  })
  return { map, bad }
}

/** Distinct non-empty values of one column in preview rows (store / godown names to map). */
export function distinctColumn(headers: string[], rows: string[][], header: string | undefined): string[] {
  if (!header) return []
  const i = headers.indexOf(header)
  if (i < 0) return []
  return [...new Set(rows.map((r) => (r[i] ?? '').trim()).filter(Boolean))].sort((a, b) => a.localeCompare(b))
}
