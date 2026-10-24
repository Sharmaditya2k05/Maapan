'use client'

/**
 * Taraju UI kit (ported from the original prototype). Small, dependency-free
 * primitives styled with the brand tokens in globals.css. Everything a screen
 * needs is exported from '@/components/ui'.
 */
import { useEffect, useId, useRef, useState, type ButtonHTMLAttributes, type InputHTMLAttributes, type ReactNode, type SelectHTMLAttributes, type TextareaHTMLAttributes } from 'react'
import { createPortal } from 'react-dom'
import { AlertTriangle, ChevronLeft, ChevronRight, Loader2, Search, TrendingDown, TrendingUp, X, type LucideIcon } from 'lucide-react'
import { formatINR } from '@/lib/money'

export { useToast, ToastProvider } from './toast'

/* ---------------- buttons ---------------- */

type Variant = 'primary' | 'secondary' | 'ghost' | 'danger' | 'gold'
const variants: Record<Variant, string> = {
  primary: 'bg-brand-700 text-white hover:bg-brand-800 shadow-sm shadow-brand-900/20',
  secondary: 'bg-white text-ink border border-slate-200 hover:bg-slate-50',
  ghost: 'text-slate-600 hover:bg-slate-100 hover:text-ink',
  danger: 'bg-rose-600 text-white hover:bg-rose-700',
  gold: 'bg-brand-500 text-white hover:bg-brand-600 shadow-sm shadow-brand-900/20',
}

export type ButtonProps = ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: Variant
  icon?: LucideIcon
  size?: 'sm' | 'md' | 'lg'
  loading?: boolean
}

export function Button({ variant = 'primary', icon: Icon, size = 'md', className = '', children, loading, disabled, type = 'button', ...rest }: ButtonProps) {
  const sz = size === 'sm' ? 'h-8 px-3 text-xs' : size === 'lg' ? 'h-12 px-6 text-base' : 'h-10 px-4 text-sm'
  return (
    <button
      type={type}
      disabled={disabled || loading}
      {...rest}
      className={`inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-xl font-semibold transition active:scale-[.98] disabled:pointer-events-none disabled:opacity-50 cursor-pointer ${sz} ${variants[variant]} ${className}`}
    >
      {loading ? <Loader2 size={size === 'sm' ? 14 : 16} className="animate-spin" /> : Icon && <Icon size={size === 'sm' ? 14 : 16} strokeWidth={2.25} />}
      {children}
    </button>
  )
}

export function IconButton({ icon: Icon, label, className = '', ...rest }: ButtonHTMLAttributes<HTMLButtonElement> & { icon: LucideIcon; label: string }) {
  return (
    <button type="button" aria-label={label} title={label} {...rest} className={`grid h-9 w-9 place-items-center rounded-xl text-slate-500 transition hover:bg-slate-100 hover:text-ink disabled:opacity-40 cursor-pointer ${className}`}>
      <Icon size={18} />
    </button>
  )
}

/* ---------------- layout ---------------- */

export function PageHeader({ title, subtitle, actions, back }: { title: ReactNode; subtitle?: ReactNode; actions?: ReactNode; back?: () => void }) {
  return (
    <div className="mb-5 flex flex-wrap items-end justify-between gap-3 sm:mb-6 sm:gap-4">
      <div className="flex min-w-0 items-center gap-2">
        {back && <IconButton icon={ChevronLeft} label="Back" onClick={back} className="-ml-2" />}
        <div className="min-w-0">
          <h1 className="text-xl font-extrabold tracking-tight text-ink sm:text-2xl">{title}</h1>
          {subtitle && <p className="mt-1 text-sm text-slate-500">{subtitle}</p>}
        </div>
      </div>
      {actions && <div className="flex flex-wrap items-center gap-2">{actions}</div>}
    </div>
  )
}

export function Card({ children, className = '', title, actions, padded = true }: { children: ReactNode; className?: string; title?: ReactNode; actions?: ReactNode; padded?: boolean }) {
  return (
    <section className={`card overflow-hidden ${className}`}>
      {(title || actions) && (
        <div className="flex items-center justify-between gap-3 border-b border-slate-100 px-4 py-3 sm:px-5">
          <h2 className="text-sm font-bold">{title}</h2>
          {actions && <div className="flex items-center gap-2">{actions}</div>}
        </div>
      )}
      <div className={padded ? 'p-4 sm:p-5' : ''}>{children}</div>
    </section>
  )
}

const tones = {
  green: 'bg-emerald-50 text-emerald-700 ring-emerald-600/15',
  amber: 'bg-amber-50 text-amber-700 ring-amber-600/20',
  red: 'bg-rose-50 text-rose-700 ring-rose-600/15',
  blue: 'bg-sky-50 text-sky-700 ring-sky-600/15',
  gray: 'bg-slate-100 text-slate-600 ring-slate-500/15',
  brand: 'bg-brand-50 text-brand-700 ring-brand-600/15',
}
export type Tone = keyof typeof tones
export function Badge({ tone = 'gray', children, className = '' }: { tone?: Tone; children: ReactNode; className?: string }) {
  return <span className={`inline-flex items-center gap-1 whitespace-nowrap rounded-full px-2.5 py-0.5 text-xs font-semibold ring-1 ring-inset ${tones[tone]} ${className}`}>{children}</span>
}

/** Tone for invoice / payment / document statuses. */
export const statusTone = (s: string | undefined): Tone => {
  switch ((s || '').toLowerCase()) {
    case 'paid': case 'posted': case 'received': case 'active': case 'processed': case 'captured': case 'synced': return 'green'
    case 'partial': case 'pending': case 'dispatched': case 'syncing': case 'unknown': return 'amber'
    case 'unpaid': case 'cancelled': case 'failed': case 'error': case 'reversed': case 'expired': return 'red'
    case 'draft': case 'created': return 'blue'
    default: return 'gray'
  }
}

/* ---------------- forms ---------------- */

export function Field({ label, children, className = '', hint, error }: { label: ReactNode; children: ReactNode; className?: string; hint?: ReactNode; error?: ReactNode }) {
  return (
    <label className={`block ${className}`}>
      <span className="label">{label}</span>
      {children}
      {error ? <span className="mt-1 block text-xs text-rose-600">{error}</span> : hint ? <span className="mt-1 block text-xs text-slate-500">{hint}</span> : null}
    </label>
  )
}

export function Input({ className = '', ref, ...rest }: InputHTMLAttributes<HTMLInputElement> & { ref?: React.Ref<HTMLInputElement> }) {
  return <input ref={ref} {...rest} className={`input ${className}`} />
}

export function Textarea({ className = '', ref, ...rest }: TextareaHTMLAttributes<HTMLTextAreaElement> & { ref?: React.Ref<HTMLTextAreaElement> }) {
  return <textarea ref={ref} {...rest} className={`input ${className}`} />
}

export function Select({ className = '', children, ref, ...rest }: SelectHTMLAttributes<HTMLSelectElement> & { ref?: React.Ref<HTMLSelectElement> }) {
  return <select ref={ref} {...rest} className={`input ${className}`}>{children}</select>
}

/** Rupee input that stores integer paise. */
export function MoneyInput({ value, onChange, className = '', ...rest }: Omit<InputHTMLAttributes<HTMLInputElement>, 'value' | 'onChange'> & { value: number; onChange: (paise: number) => void }) {
  const [text, setText] = useState(value ? (value / 100).toFixed(2) : '')
  const [focused, setFocused] = useState(false)
  useEffect(() => {
    if (!focused) setText(value ? (value / 100).toFixed(2) : '')
  }, [value, focused])
  return (
    <div className="relative">
      <span className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-sm text-slate-400">₹</span>
      <input
        {...rest}
        inputMode="decimal"
        className={`input num pl-7 ${className}`}
        value={text}
        onFocus={(e) => { setFocused(true); e.target.select() }}
        onBlur={() => setFocused(false)}
        onChange={(e) => {
          const t = e.target.value.replace(/[^0-9.]/g, '')
          setText(t)
          const n = Number(t)
          onChange(Number.isFinite(n) ? Math.round(n * 100) : 0)
        }}
      />
    </div>
  )
}

export function SearchInput({ value, onChange, placeholder, className = '', autoFocus, inputRef }: { value: string; onChange: (v: string) => void; placeholder: string; className?: string; autoFocus?: boolean; inputRef?: React.Ref<HTMLInputElement> }) {
  return (
    <div className={`relative w-full md:w-72 ${className}`}>
      <Search size={16} className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-slate-400" />
      <input ref={inputRef} autoFocus={autoFocus} className="input pl-9" value={value} onChange={(e) => onChange(e.target.value)} placeholder={placeholder} />
      {value && <button type="button" onClick={() => onChange('')} className="absolute right-2 top-1/2 -translate-y-1/2 rounded p-1 text-slate-400 hover:text-ink cursor-pointer" aria-label="Clear"><X size={14} /></button>}
    </div>
  )
}

export function Toggle({ checked, onChange, label }: { checked: boolean; onChange: (v: boolean) => void; label?: ReactNode }) {
  return (
    <label className="inline-flex cursor-pointer items-center gap-2 text-sm">
      <span role="switch" aria-checked={checked} tabIndex={0} onKeyDown={(e) => (e.key === ' ' || e.key === 'Enter') && onChange(!checked)} onClick={() => onChange(!checked)}
        className={`relative h-6 w-10 rounded-full transition ${checked ? 'bg-brand-500' : 'bg-slate-300'}`}>
        <span className={`absolute top-0.5 h-5 w-5 rounded-full bg-white shadow transition ${checked ? 'left-[18px]' : 'left-0.5'}`} />
      </span>
      {label}
    </label>
  )
}

/* ---------------- feedback ---------------- */

export function Empty({ icon: Icon, title, text, action }: { icon: LucideIcon; title: string; text?: string; action?: ReactNode }) {
  return (
    <div className="flex flex-col items-center justify-center px-6 py-14 text-center">
      <div className="mb-4 grid h-14 w-14 place-items-center rounded-2xl bg-brand-50 text-brand-600"><Icon size={26} /></div>
      <div className="font-bold">{title}</div>
      {text && <p className="mt-1 max-w-sm text-sm text-slate-500">{text}</p>}
      {action && <div className="mt-5">{action}</div>}
    </div>
  )
}

export function ErrorBox({ message, retry }: { message: string; retry?: () => void }) {
  return (
    <div className="flex items-start gap-3 rounded-xl bg-rose-50 px-4 py-3 text-sm text-rose-700">
      <AlertTriangle size={18} className="mt-0.5 shrink-0" />
      <div className="flex-1">{message}</div>
      {retry && <button type="button" onClick={retry} className="font-semibold underline cursor-pointer">Retry</button>}
    </div>
  )
}

export function Spinner({ className = '' }: { className?: string }) {
  return <Loader2 className={`animate-spin text-brand-600 ${className}`} size={20} aria-label="Loading" />
}

export function Skeleton({ rows = 5, className = '' }: { rows?: number; className?: string }) {
  return (
    <div className={`space-y-2 p-4 ${className}`} aria-busy>
      {Array.from({ length: rows }).map((_, i) => <div key={i} className="skeleton h-9 w-full" />)}
    </div>
  )
}

/** Skeleton that admits, after a few seconds, that the server is slow or unreachable. */
export function SlowSkeleton({ rows = 5, retry, after = 4000 }: { rows?: number; retry?: () => void; after?: number }) {
  const [slow, setSlow] = useState(false)
  useEffect(() => { const t = setTimeout(() => setSlow(true), after); return () => clearTimeout(t) }, [after])
  return (
    <div>
      <Skeleton rows={rows} />
      {slow && (
        <div className="flex items-center gap-2 px-4 pb-4 text-sm text-slate-500" role="status">
          <Spinner className="h-4 w-4" /> Still loading. The server is slow or unreachable.
          {retry && <button type="button" onClick={retry} className="link cursor-pointer">Try again</button>}
        </div>
      )}
    </div>
  )
}

/** Loading / error / empty / content switch for any query. */
export function QueryState<T>({ isLoading, error, data, empty, children, retry }: {
  isLoading: boolean; error: unknown; data: T | undefined; empty?: ReactNode; children: (data: T) => ReactNode; retry?: () => void
}) {
  if (isLoading) return <SlowSkeleton retry={retry} />
  if (error) return <div className="p-4"><ErrorBox message={(error as Error).message || 'Failed to load'} retry={retry} /></div>
  if (data === undefined || data === null) return <>{empty ?? null}</>
  if (Array.isArray(data) && data.length === 0 && empty) return <>{empty}</>
  return <>{children(data)}</>
}

/* ---------------- overlays ---------------- */

export function Modal({ open, onClose, title, children, footer, wide, size }: {
  open: boolean; onClose: () => void; title: ReactNode; children: ReactNode; footer?: ReactNode; wide?: boolean; size?: 'md' | 'lg' | 'xl'
}) {
  useEffect(() => {
    if (!open) return
    const h = (e: KeyboardEvent) => e.key === 'Escape' && onClose()
    window.addEventListener('keydown', h)
    return () => window.removeEventListener('keydown', h)
  }, [open, onClose])
  if (!open || typeof document === 'undefined') return null
  const w = size === 'xl' ? 'sm:max-w-5xl' : size === 'lg' || wide ? 'sm:max-w-3xl' : 'sm:max-w-lg'
  return createPortal(
    <div className="no-print fixed inset-0 z-50 flex items-end justify-center bg-ink/40 p-0 backdrop-blur-sm sm:items-center sm:p-4" onMouseDown={onClose} role="dialog" aria-modal>
      <div className={`animate-rise flex max-h-[92vh] w-full flex-col rounded-t-3xl bg-white shadow-2xl sm:rounded-3xl ${w}`} onMouseDown={(e) => e.stopPropagation()}>
        <div className="flex items-center justify-between border-b border-slate-100 px-5 py-4 sm:px-6">
          <h3 className="text-lg font-bold">{title}</h3>
          <button type="button" onClick={onClose} className="rounded-lg p-1.5 text-slate-400 hover:bg-slate-100 hover:text-ink cursor-pointer" aria-label="Close"><X size={18} /></button>
        </div>
        <div className="overflow-y-auto px-5 py-5 sm:px-6">{children}</div>
        {footer && <div className="flex justify-end gap-2 border-t border-slate-100 px-5 py-4 pb-[calc(1rem+env(safe-area-inset-bottom))] sm:px-6 sm:pb-4">{footer}</div>}
      </div>
    </div>,
    document.body,
  )
}

export function Confirm({ open, onClose, onConfirm, title, text, confirmLabel = 'Confirm', danger, loading, children }: {
  open: boolean; onClose: () => void; onConfirm: () => void; title: string; text?: ReactNode; confirmLabel?: string; danger?: boolean; loading?: boolean; children?: ReactNode
}) {
  return (
    <Modal open={open} onClose={onClose} title={title}
      footer={<><Button variant="secondary" onClick={onClose}>Cancel</Button><Button variant={danger ? 'danger' : 'primary'} onClick={onConfirm} loading={loading}>{confirmLabel}</Button></>}>
      {text && <p className="text-sm text-slate-600">{text}</p>}
      {children}
    </Modal>
  )
}

export function Tabs<T extends string>({ value, onChange, options }: { value: T; onChange: (v: T) => void; options: { value: T; label: string; count?: number }[] }) {
  return (
    <div className="inline-flex max-w-full overflow-x-auto rounded-xl bg-slate-100 p-1 [scrollbar-width:none]">
      {options.map((o) => (
        <button key={o.value} type="button" onClick={() => onChange(o.value)}
          className={`flex shrink-0 items-center gap-1.5 whitespace-nowrap rounded-lg px-3 py-1.5 text-sm font-semibold transition sm:px-3.5 cursor-pointer ${value === o.value ? 'bg-white text-ink shadow-sm' : 'text-slate-500 hover:text-ink'}`}>
          {o.label}
          {o.count !== undefined && <span className="rounded-md bg-slate-200/70 px-1.5 text-[11px]">{o.count}</span>}
        </button>
      ))}
    </div>
  )
}

/* ---------------- data display ---------------- */

export function Money({ paise, className = '', signed }: { paise: number | undefined | null; className?: string; signed?: boolean }) {
  const v = paise ?? 0
  return <span className={`num ${signed && v < 0 ? 'text-rose-600' : ''} ${className}`}>{signed && v > 0 ? '+' : ''}{formatINR(v)}</span>
}

export function Table({ children, className = '' }: { children: ReactNode; className?: string }) {
  return <div className={`overflow-x-auto ${className}`}><table className="w-full min-w-[560px] border-collapse">{children}</table></div>
}

export function Pagination({ total, limit, offset, onChange }: { total: number; limit: number; offset: number; onChange: (offset: number) => void }) {
  if (total <= limit) return null
  const page = Math.floor(offset / limit) + 1
  const pages = Math.ceil(total / limit)
  return (
    <div className="flex items-center justify-between border-t border-slate-100 px-4 py-3 text-sm text-slate-500">
      <span>{offset + 1}–{Math.min(total, offset + limit)} of {total}</span>
      <div className="flex items-center gap-1">
        <IconButton icon={ChevronLeft} label="Previous" disabled={page <= 1} onClick={() => onChange(Math.max(0, offset - limit))} />
        <span className="px-2 font-semibold text-ink">{page} / {pages}</span>
        <IconButton icon={ChevronRight} label="Next" disabled={page >= pages} onClick={() => onChange(offset + limit)} />
      </div>
    </div>
  )
}

export function StatCard({ label, value, hint, icon: Icon, tone = 'brand', trend }: {
  label: string; value: ReactNode; hint?: ReactNode; icon: LucideIcon; tone?: 'brand' | 'gold' | 'rose' | 'sky'; trend?: number | null
}) {
  const chip = { brand: 'bg-brand-50 text-brand-700', gold: 'bg-amber-50 text-amber-600', rose: 'bg-rose-50 text-rose-600', sky: 'bg-sky-50 text-sky-600' }[tone]
  return (
    <div className="card p-4 sm:p-5">
      <div className="flex items-start justify-between gap-2">
        <div className="text-xs font-medium text-slate-500 sm:text-[13px]">{label}</div>
        <div className={`grid h-8 w-8 shrink-0 place-items-center rounded-xl sm:h-9 sm:w-9 ${chip}`}><Icon size={17} /></div>
      </div>
      <div className="num mt-2 truncate text-xl font-bold tracking-tight text-ink sm:mt-3 sm:text-2xl">{value}</div>
      <div className="mt-1 flex items-center gap-2 text-xs text-slate-500">
        {trend !== undefined && trend !== null && Number.isFinite(trend) && (
          <span className={`inline-flex items-center gap-0.5 rounded-md px-1.5 py-0.5 font-semibold ${trend >= 0 ? 'bg-emerald-50 text-emerald-700' : 'bg-rose-50 text-rose-700'}`}>
            {trend >= 0 ? <TrendingUp size={12} /> : <TrendingDown size={12} />}{Math.abs(trend).toFixed(0)}%
          </span>
        )}
        {hint && <span className="truncate">{hint}</span>}
      </div>
    </div>
  )
}

export function Avatar({ name, className = '' }: { name: string; className?: string }) {
  const palettes = ['from-brand-400 to-brand-700', 'from-amber-300 to-amber-500', 'from-sky-400 to-indigo-500', 'from-rose-400 to-pink-600', 'from-emerald-400 to-teal-600', 'from-violet-400 to-purple-600']
  const h = [...name].reduce((a, c) => a + c.charCodeAt(0), 0) % palettes.length
  const initials = name.split(/\s+/).filter(Boolean).slice(0, 2).map((w) => w[0]).join('').toUpperCase()
  return <div className={`grid h-10 w-10 shrink-0 place-items-center rounded-full bg-gradient-to-br text-sm font-bold text-white shadow-sm ${palettes[h]} ${className}`}>{initials || '?'}</div>
}

/** Keyboard shortcut hint. */
export function Kbd({ children }: { children: ReactNode }) {
  return <kbd className="kbd">{children}</kbd>
}

/** Tiny hook for a stable element id (labels ↔ inputs). */
export function useFieldId(prefix = 'f') {
  const id = useId()
  return `${prefix}-${id}`
}

/** Focus the element on mount (barcode box on the POS). */
export function useAutoFocus<T extends HTMLElement>() {
  const ref = useRef<T>(null)
  useEffect(() => { ref.current?.focus() }, [])
  return ref
}
