'use client'

/** Small shared pieces for the money screens: tab routing, key/value rows, permission walls, totals, JSON viewer. */
import { useCallback, useState, type ReactNode } from 'react'
import { usePathname, useRouter, useSearchParams } from 'next/navigation'
import { Lock, type LucideIcon } from 'lucide-react'
import { Badge, Empty, Money, type Tone } from '@/components/ui'
import { balanceLabel } from '@/lib/ledger/util'

/** `?tab=` kept in the URL so links can deep-link into a tab. */
export function useTabParam<T extends string>(allowed: readonly T[], fallback: T, key = 'tab'): [T, (t: T) => void] {
  const params = useSearchParams()
  const router = useRouter()
  const pathname = usePathname()
  const raw = params.get(key)
  const value = (allowed as readonly string[]).includes(raw ?? '') ? (raw as T) : fallback
  const set = useCallback((t: T) => {
    const next = new URLSearchParams(params.toString())
    if (t === fallback) next.delete(key); else next.set(key, t)
    const qs = next.toString()
    router.replace(qs ? `${pathname}?${qs}` : pathname, { scroll: false })
  }, [params, router, pathname, key, fallback])
  return [value, set]
}

/** Any other query param, same mechanics (e.g. `?voucher=<id>` opens a modal). */
export function useQueryParam(key: string): [string | null, (v: string | null) => void] {
  const params = useSearchParams()
  const router = useRouter()
  const pathname = usePathname()
  const value = params.get(key)
  const set = useCallback((v: string | null) => {
    const next = new URLSearchParams(params.toString())
    if (v === null || v === '') next.delete(key); else next.set(key, v)
    const qs = next.toString()
    router.replace(qs ? `${pathname}?${qs}` : pathname, { scroll: false })
  }, [params, router, pathname, key])
  return [value, set]
}

/** Shown in place of a screen or tab the role cannot use. */
export function NoPermission({ what = 'this screen' }: { what?: string }) {
  return <Empty icon={Lock} title="Not available for your role" text={`Ask the owner for access to ${what}.`} />
}

/** A definition-list row. */
export function KV({ k, v, mono }: { k: ReactNode; v: ReactNode; mono?: boolean }) {
  return (
    <div className="flex items-start justify-between gap-4 py-1.5 text-sm">
      <dt className="shrink-0 text-slate-500">{k}</dt>
      <dd className={`min-w-0 text-right font-medium break-words ${mono ? 'font-mono text-xs' : ''}`}>{v ?? '—'}</dd>
    </div>
  )
}

/** Party balance as a coloured phrase ("₹1,200.00 to collect"). */
export function BalancePhrase({ paise, className = '' }: { paise: number; className?: string }) {
  const b = balanceLabel(paise)
  const color = b.tone === 'green' ? 'text-emerald-700' : b.tone === 'red' ? 'text-rose-700' : 'text-slate-400'
  return <span className={`num font-semibold ${color} ${className}`}>{b.text}</span>
}

/** Table footer cell block for totals. */
export function TotalCell({ paise, label, className = '' }: { paise: number; label?: string; className?: string }) {
  return <td className={`td num text-right font-bold ${className}`}>{label && <span className="mr-2 font-sans text-xs font-medium text-slate-500">{label}</span>}<Money paise={paise} /></td>
}

/** Pretty-printed JSON with copy. */
export function JsonBlock({ value, maxHeight = '20rem' }: { value: unknown; maxHeight?: string }) {
  const [copied, setCopied] = useState(false)
  const text = JSON.stringify(value, null, 2)
  return (
    <div className="relative">
      <button type="button" onClick={() => { void navigator.clipboard?.writeText(text).then(() => { setCopied(true); setTimeout(() => setCopied(false), 1500) }) }}
        className="absolute right-2 top-2 rounded-md bg-white/90 px-2 py-0.5 text-[11px] font-semibold text-slate-600 ring-1 ring-slate-200 hover:text-ink cursor-pointer">{copied ? 'Copied' : 'Copy'}</button>
      <pre className="overflow-auto rounded-xl bg-slate-50 p-3 font-mono text-[11px] leading-relaxed text-slate-700" style={{ maxHeight }}>{text}</pre>
    </div>
  )
}

/** A section heading inside a report. */
export function SectionTitle({ children, right }: { children: ReactNode; right?: ReactNode }) {
  return (
    <div className="mb-2 mt-6 flex items-center justify-between gap-2 first:mt-0">
      <h3 className="text-sm font-bold uppercase tracking-wide text-slate-600">{children}</h3>
      {right}
    </div>
  )
}

/** A card-ish stat used on hubs. */
export function MiniStat({ label, value, tone = 'gray', icon: Icon }: { label: string; value: ReactNode; tone?: Tone; icon?: LucideIcon }) {
  return (
    <div className="card flex items-center justify-between gap-3 px-4 py-3">
      <div className="min-w-0">
        <div className="text-xs text-slate-500">{label}</div>
        <div className="num mt-0.5 truncate text-base font-bold sm:text-lg">{value}</div>
      </div>
      {Icon && <Badge tone={tone} className="h-8 w-8 justify-center rounded-xl px-0"><Icon size={16} /></Badge>}
    </div>
  )
}

/** Warnings list the APIs return alongside documents. */
export function WarningList({ items }: { items?: string[] | null }) {
  if (!items || items.length === 0) return null
  return (
    <ul className="space-y-1 rounded-xl bg-amber-50 px-4 py-3 text-sm text-amber-800">
      {items.map((w, i) => <li key={i} className="flex gap-2"><span aria-hidden>•</span><span>{w}</span></li>)}
    </ul>
  )
}

/** Mobile list row (stacked) used by lists that collapse their table below md. */
export function MobileRow({ title, subtitle, right, rightSub, onClick, left }: { title: ReactNode; subtitle?: ReactNode; right?: ReactNode; rightSub?: ReactNode; onClick?: () => void; left?: ReactNode }) {
  const inner = (
    <>
      {left}
      <div className="min-w-0 flex-1">
        <div className="truncate text-sm font-semibold">{title}</div>
        {subtitle && <div className="truncate text-xs text-slate-500">{subtitle}</div>}
      </div>
      {(right || rightSub) && <div className="shrink-0 text-right"><div className="text-sm font-bold">{right}</div>{rightSub && <div className="text-[11px] text-slate-400">{rightSub}</div>}</div>}
    </>
  )
  return onClick
    ? <button type="button" onClick={onClick} className="flex w-full items-center gap-3 px-4 py-3 text-left hover:bg-slate-50 cursor-pointer">{inner}</button>
    : <div className="flex items-center gap-3 px-4 py-3">{inner}</div>
}
