'use client'

/** Pieces shared by the GST tabs: rupee-float formatting (kar returns rupees as floats), period picker, download helpers. */
import { Select } from '@/components/ui'
import { formatINR } from '@/lib/money'
import { returnMonths } from '@/lib/ledger/util'
import { todayIST, downloadFile } from '@/lib/hooks'

/** kar returns amounts in rupees (float) → ₹ string via integer paise. */
export const rup = (v: number | undefined | null) => formatINR(Math.round((v ?? 0) * 100))

export function Rup({ v, className = '' }: { v: number | undefined | null; className?: string }) {
  return <span className={`num ${className}`}>{rup(v)}</span>
}

/** Month picker for returns (MMYYYY periods). value = YYYY-MM. */
export function PeriodPicker({ value, onChange, className = '' }: { value: string; onChange: (v: string) => void; className?: string }) {
  const months = returnMonths(todayIST())
  return (
    <Select value={value} onChange={(e) => onChange(e.target.value)} className={`w-auto input-sm ${className}`} aria-label="Return period">
      {months.map((m) => <option key={m.value} value={m.value}>{m.label}</option>)}
    </Select>
  )
}

/** Default period: the previous month (the one being filed now). */
export function defaultPeriod(): string {
  const m = returnMonths(todayIST())
  return (m[1] ?? m[0]).value
}

export function downloadJSON(name: string, data: unknown) {
  downloadFile(name, JSON.stringify(data, null, 2), 'application/json')
}

/** Table header/footer helpers for the tax-head columns. */
export const TAX_HEADS = ['Taxable', 'IGST', 'CGST', 'SGST', 'Cess'] as const

export function TaxCells({ txval, iamt, camt, samt, csamt, bold }: { txval?: number; iamt?: number; camt?: number; samt?: number; csamt?: number; bold?: boolean }) {
  const cls = `td num text-right ${bold ? 'font-bold' : ''}`
  return <><td className={cls}><Rup v={txval} /></td><td className={cls}><Rup v={iamt} /></td><td className={cls}><Rup v={camt} /></td><td className={cls}><Rup v={samt} /></td><td className={cls}><Rup v={csamt} /></td></>
}

export interface Heads { txval: number; iamt: number; camt: number; samt: number; csamt: number }
export const zeroHeads = (): Heads => ({ txval: 0, iamt: 0, camt: 0, samt: 0, csamt: 0 })
export function addHeads(a: Heads, b: Partial<Heads>): Heads {
  return { txval: a.txval + (b.txval ?? 0), iamt: a.iamt + (b.iamt ?? 0), camt: a.camt + (b.camt ?? 0), samt: a.samt + (b.samt ?? 0), csamt: a.csamt + (b.csamt ?? 0) }
}
