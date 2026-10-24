'use client'

/**
 * Frame for every report / statement: title, period controls, Print (A4) and CSV, and the printable
 * area (.print-root .print-a4 with a header that only shows on paper).
 */
import { useRouter } from 'next/navigation'
import { Download, Printer } from 'lucide-react'
import { Button, PageHeader } from '@/components/ui'
import { fmtDate, useStore } from '@/lib/hooks'
import type { ReactNode } from 'react'
import { AsOfPicker, DateRangePicker, type DateRange, type RangePreset } from './DateRangePicker'

export function ReportShell({ title, subtitle, back = '/app/reports', range, asOf, onCSV, actions, children, embedded, filters, printNote }: {
  title: string
  subtitle?: ReactNode
  /** Where the back arrow goes; null hides it. */
  back?: string | null
  range?: { value: DateRange; onChange: (r: DateRange) => void; presets?: RangePreset[] }
  asOf?: { value: string; onChange: (d: string) => void }
  onCSV?: () => void
  actions?: ReactNode
  /** Extra filter controls shown next to the period picker. */
  filters?: ReactNode
  children: ReactNode
  /** Inside a tabbed page: no PageHeader, just a toolbar. */
  embedded?: boolean
  printNote?: ReactNode
}) {
  const router = useRouter()
  const { store } = useStore()
  const period = range ? `${fmtDate(range.value.from)} – ${fmtDate(range.value.to)}` : asOf ? `As of ${fmtDate(asOf.value)}` : ''
  const buttons = (
    <>
      {onCSV && <Button variant="secondary" size="sm" icon={Download} onClick={onCSV}>CSV</Button>}
      <Button variant="secondary" size="sm" icon={Printer} onClick={() => window.print()}>Print</Button>
      {actions}
    </>
  )
  const controls = (range || asOf || filters) && (
    <div className="no-print mb-4 flex flex-wrap items-center gap-3">
      {range && <DateRangePicker value={range.value} onChange={range.onChange} presets={range.presets} />}
      {asOf && <AsOfPicker value={asOf.value} onChange={asOf.onChange} />}
      {filters}
    </div>
  )
  return (
    <>
      {embedded ? (
        <div className="no-print mb-4 flex flex-wrap items-center justify-between gap-3">
          <div className="min-w-0"><h2 className="text-base font-bold">{title}</h2>{subtitle && <p className="text-xs text-slate-500">{subtitle}</p>}</div>
          <div className="flex flex-wrap items-center gap-2">{buttons}</div>
        </div>
      ) : (
        <div className="no-print"><PageHeader title={title} subtitle={subtitle ?? period} back={back ? () => router.push(back) : undefined} actions={<div className="flex flex-wrap items-center gap-2">{buttons}</div>} /></div>
      )}
      {controls}
      <div className="print-root print-a4">
        <div className="mb-4 hidden print:block">
          <div className="text-lg font-extrabold">{store?.name ?? 'Taraju'}</div>
          {store?.gstin && <div className="text-xs">GSTIN {store.gstin}</div>}
          <div className="mt-1 text-base font-bold">{title}</div>
          {period && <div className="text-xs">{period}</div>}
          {printNote && <div className="text-xs">{printNote}</div>}
        </div>
        {children}
      </div>
    </>
  )
}
