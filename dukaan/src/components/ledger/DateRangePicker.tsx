'use client'

/**
 * Period picker used by statements and reports: presets + from/to boxes.
 *   const [range, setRange] = useState(() => presetRange('this_month', todayIST()))
 *   <DateRangePicker value={range} onChange={setRange} presets={['today','this_month','this_fy','custom']} />
 */
import { Input, Select } from '@/components/ui'
import { todayIST } from '@/lib/hooks'
import { detectPreset, presetRange, RANGE_PRESET_LABEL, type DateRange, type RangePreset } from '@/lib/ledger/util'

export type { DateRange, RangePreset }

export const DEFAULT_PRESETS: RangePreset[] = ['today', 'this_week', 'this_month', 'last_month', 'this_quarter', 'this_fy', 'last_fy', 'custom']

export function DateRangePicker({ value, onChange, presets = DEFAULT_PRESETS, className = '', disabled }: {
  value: DateRange
  onChange: (r: DateRange) => void
  presets?: RangePreset[]
  className?: string
  disabled?: boolean
}) {
  const today = todayIST()
  const preset = detectPreset(value, today, presets)
  const list = presets.includes('custom') ? presets : [...presets, 'custom' as const]
  return (
    <div className={`flex flex-wrap items-center gap-2 ${className}`}>
      <label className="sr-only" htmlFor="range-preset">Period</label>
      <Select id="range-preset" value={preset} disabled={disabled} className="w-auto min-w-[9rem]" aria-label="Period preset"
        onChange={(e) => { const p = e.target.value as RangePreset; if (p !== 'custom') onChange(presetRange(p, today)) }}>
        {list.map((p) => <option key={p} value={p}>{RANGE_PRESET_LABEL[p]}</option>)}
      </Select>
      <div className="flex items-center gap-1.5">
        <Input type="date" value={value.from} max={value.to} disabled={disabled} aria-label="From date" className="w-auto input-sm"
          onChange={(e) => e.target.value && onChange({ from: e.target.value, to: value.to < e.target.value ? e.target.value : value.to })} />
        <span className="text-xs text-slate-400">to</span>
        <Input type="date" value={value.to} min={value.from} disabled={disabled} aria-label="To date" className="w-auto input-sm"
          onChange={(e) => e.target.value && onChange({ from: value.from > e.target.value ? e.target.value : value.from, to: e.target.value })} />
      </div>
    </div>
  )
}

/** A single "as of" date (trial balance, balance sheet, aging). */
export function AsOfPicker({ value, onChange, className = '' }: { value: string; onChange: (d: string) => void; className?: string }) {
  const today = todayIST()
  const fyEnd = `${Number(today.slice(0, 4)) + (Number(today.slice(5, 7)) < 4 ? 0 : 1)}-03-31`
  return (
    <div className={`flex flex-wrap items-center gap-2 ${className}`}>
      <span className="text-xs font-semibold uppercase tracking-wide text-slate-500">As of</span>
      <Input type="date" value={value} aria-label="As of date" className="w-auto input-sm" onChange={(e) => e.target.value && onChange(e.target.value)} />
      <button type="button" className={`rounded-lg px-2 py-1 text-xs font-semibold cursor-pointer ${value === today ? 'bg-brand-50 text-brand-700' : 'text-slate-500 hover:bg-slate-100'}`} onClick={() => onChange(today)}>Today</button>
      <button type="button" className={`rounded-lg px-2 py-1 text-xs font-semibold cursor-pointer ${value === fyEnd ? 'bg-brand-50 text-brand-700' : 'text-slate-500 hover:bg-slate-100'}`} onClick={() => onChange(fyEnd)}>FY end</button>
    </div>
  )
}
