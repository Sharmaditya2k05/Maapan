'use client'

/** Shared pieces of the AI screens: the "AI not enabled" gate, usage chip, store scope select. */
import Link from 'next/link'
import { Settings, Sparkles } from 'lucide-react'
import { Button, Select } from '@/components/ui'
import { ApiError } from '@/lib/api'
import { useGet, useSessionUser, useStore } from '@/lib/hooks'
import type { AiUsage, AiUsageReport } from '@/lib/types.more'

/** 503 (no API key on the server) or 403 from the per-tenant pilot flag. */
export function isAiUnavailable(e: unknown): boolean {
  if (!(e instanceof ApiError)) return false
  if (e.status === 503) return true
  return e.status === 403 && /not enabled|ai_enabled|pilot/i.test(e.message)
}

export function AiUnavailable({ message, compact }: { message?: string; compact?: boolean }) {
  return (
    <div className={`flex flex-col items-center rounded-2xl border border-dashed border-brand-300 bg-brand-50/60 text-center ${compact ? 'px-4 py-6' : 'px-6 py-12'}`} role="status">
      <div className="mb-3 grid h-12 w-12 place-items-center rounded-2xl bg-white text-brand-600 shadow-sm"><Sparkles size={24} /></div>
      <div className="font-bold">AI features are not switched on for this business</div>
      <p className="mt-1 max-w-md text-sm text-slate-600">{message || 'Turn on Ask Maapan and bill scanning for your business and set a monthly token budget.'}</p>
      <Link href="/app/settings?tab=ai" className="mt-4"><Button icon={Settings} variant="secondary">Open Settings → AI</Button></Link>
    </div>
  )
}

/** GET /buddhi/v1/ai/usage — flags and this month's token use. */
export function useAiStatus(enabled = true) {
  return useGet<AiUsageReport>(enabled ? '/buddhi/v1/ai/usage' : null)
}

export function UsageChip({ usage }: { usage?: AiUsage }) {
  if (!usage) return null
  const total = usage.input_tokens + usage.output_tokens + usage.cache_read_tokens + usage.cache_write_tokens
  if (!total) return null
  return (
    <span className="num inline-flex items-center gap-1 rounded-md bg-slate-100 px-1.5 py-0.5 text-[11px] text-slate-500" title={`${usage.model} · in ${usage.input_tokens} · out ${usage.output_tokens} · cache read ${usage.cache_read_tokens} · cache write ${usage.cache_write_tokens}`}>
      {total.toLocaleString('en-IN')} tokens{usage.model ? ` · ${usage.model}` : ''}
    </span>
  )
}

/** Store scope for buddhi reads. Store-scoped roles only see their own stores; owners/accountants may pick "all". */
export function StoreScope({ value, onChange, allowAll }: { value: string; onChange: (v: string) => void; allowAll?: boolean }) {
  const user = useSessionUser()
  const { stores } = useStore()
  const canAll = allowAll && (user?.role === 'super_admin' || user?.role === 'accountant')
  if (stores.length <= 1 && !canAll) return null
  return (
    <Select className="w-auto" value={value} onChange={(e) => onChange(e.target.value)} aria-label="Store">
      {canAll && <option value="all">All stores</option>}
      {stores.map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}
    </Select>
  )
}
