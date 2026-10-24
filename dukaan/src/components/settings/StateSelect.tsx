'use client'

import { Select } from '@/components/ui'
import { STATES } from '@/lib/gst'

const OPTIONS = Object.entries(STATES).sort((a, b) => a[1].localeCompare(b[1]))

/** GST state code picker ("27 — Maharashtra"). */
export function StateSelect({ value, onChange, disabled, allowEmpty }: { value: string; onChange: (code: string) => void; disabled?: boolean; allowEmpty?: boolean }) {
  return (
    <Select value={value} onChange={(e) => onChange(e.target.value)} disabled={disabled}>
      {(allowEmpty || !value) && <option value="">Choose state…</option>}
      {OPTIONS.map(([code, name]) => <option key={code} value={code}>{code} — {name}</option>)}
    </Select>
  )
}
