'use client'

import { Select } from '@/components/ui'
import { useSessionUser, useStore } from '@/lib/hooks'
import { isStoreScoped } from '@/lib/stock/scope'

/**
 * Store filter for people who can see more than one store. `allowAll` adds an
 * "All stores" option (value '') for roles that are not store-scoped.
 */
export function StoreSelect({ value, onChange, allowAll, label = 'Store', className = '' }: {
  value: string; onChange: (id: string) => void; allowAll?: boolean; label?: string; className?: string
}) {
  const { stores } = useStore()
  const user = useSessionUser()
  const canAll = !!allowAll && !isStoreScoped(user?.role)
  if (stores.length <= 1 && !canAll) return null
  return (
    <div className={`w-full sm:w-56 ${className}`}>
      <Select aria-label={label} value={value} onChange={(e) => onChange(e.target.value)}>
        {canAll && <option value="">All stores</option>}
        {stores.map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}
      </Select>
    </div>
  )
}
