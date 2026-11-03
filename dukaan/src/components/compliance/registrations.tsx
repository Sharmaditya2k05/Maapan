'use client'

/** The tenant's GST registrations (pehchaan GET /v1/registrations) and the picker the GST centre uses. */
import { useMemo } from 'react'
import { Select } from '@/components/ui'
import { useGet } from '@/lib/hooks'
import { fileableRegistrations, registrationLabel } from '@/lib/compliance'
import type { ListResponse } from '@/lib/types'
import type { Registration } from '@/lib/types.kar'

export const REGISTRATIONS_PATH = '/pehchaan/v1/registrations'

/** All registrations (primary first, as pehchaan returns them) plus the fileable subset and the primary. */
export function useRegistrations(enabled = true) {
  const q = useGet<ListResponse<Registration>>(enabled ? REGISTRATIONS_PATH : null, undefined, { staleTime: 60_000 })
  const all = useMemo(() => q.data?.items ?? [], [q.data])
  const fileable = useMemo(() => fileableRegistrations(all), [all])
  const primary = all.find((r) => r.is_primary)
  return { ...q, all, fileable, primary }
}

/**
 * Select of the active registrations with a GSTIN. value = GSTIN ('' = primary, which is what kar
 * uses when `gstin=` is empty). Falls back to a disabled box while loading.
 */
export function RegistrationPicker({ value, onChange, className = '' }: { value: string; onChange: (gstin: string) => void; className?: string }) {
  const regs = useRegistrations()
  if (regs.isLoading) return <Select disabled className={`w-auto input-sm ${className}`} aria-label="GST registration"><option>Loading registrations…</option></Select>
  if (regs.error || regs.fileable.length === 0) {
    return <Select disabled className={`w-auto input-sm ${className}`} aria-label="GST registration"><option>{regs.error ? 'Registrations unavailable' : 'No GSTIN registered'}</option></Select>
  }
  const current = value || regs.primary?.gstin || regs.fileable[0].gstin
  return (
    <Select value={current} onChange={(e) => onChange(e.target.value === regs.primary?.gstin ? '' : e.target.value)} className={`w-auto max-w-full input-sm font-mono ${className}`} aria-label="GST registration">
      {regs.fileable.map((r) => <option key={r.id} value={r.gstin}>{registrationLabel(r)}{r.is_primary ? ' (primary)' : ''}</option>)}
    </Select>
  )
}
