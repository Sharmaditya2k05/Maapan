'use client'

/**
 * Ledger account picker (bahi GET /v1/accounts), optionally letting the user pick a party instead —
 * voucher entries take `account_id` or `party_id`.
 *
 *   <AccountPicker value={t} onChange={setT} allowParties filter={(a) => a.is_cash || a.is_bank} />
 */
import { useEffect, useId, useMemo, useRef, useState } from 'react'
import { ChevronDown, X } from 'lucide-react'
import { useGet } from '@/lib/hooks'
import type { Account } from '@/lib/types.ledger'
import type { Party } from '@/lib/types'
import { Money } from '@/components/ui'
import { PartyPicker } from './PartyPicker'

/** What a voucher line points at. */
export interface LedgerTarget { account_id?: string; party_id?: string; label: string; code?: string }

export const GROUP_LABEL: Record<string, string> = { asset: 'Assets', liability: 'Liabilities', equity: 'Equity', income: 'Income', expense: 'Expenses' }

/** All postable (non-group, non-archived) accounts, cached. */
export function useAccounts(opts?: { includeGroups?: boolean; includeParties?: boolean }) {
  const q = useGet<{ items: Account[] }>('/bahi/v1/accounts', { include_parties: opts?.includeParties ? true : undefined })
  const items = useMemo(() => (q.data?.items ?? []).filter((a) => !a.archived && (opts?.includeGroups || !a.is_group)), [q.data, opts?.includeGroups])
  return { ...q, accounts: items }
}

export function AccountPicker({ value, onChange, allowParties, filter, placeholder = 'Pick an account', disabled, className = '', partyType }: {
  value: LedgerTarget | null
  onChange: (t: LedgerTarget | null) => void
  allowParties?: boolean
  filter?: (a: Account) => boolean
  placeholder?: string
  disabled?: boolean
  className?: string
  partyType?: 'customer' | 'supplier'
}) {
  const [mode, setMode] = useState<'account' | 'party'>(value?.party_id ? 'party' : 'account')
  const [open, setOpen] = useState(false)
  const [text, setText] = useState('')
  const [hi, setHi] = useState(0)
  const [party, setParty] = useState<Party | null>(null)
  const { accounts, isLoading, error } = useAccounts()
  const listId = useId()
  const wrap = useRef<HTMLDivElement>(null)

  useEffect(() => { if (value?.party_id) setMode('party'); if (!value) setParty(null) }, [value])
  useEffect(() => {
    const h = (e: MouseEvent) => { if (!wrap.current?.contains(e.target as Node)) setOpen(false) }
    document.addEventListener('mousedown', h)
    return () => document.removeEventListener('mousedown', h)
  }, [])

  const options = useMemo(() => {
    const t = text.trim().toLowerCase()
    return accounts.filter((a) => (!filter || filter(a)) && (!t || a.code.toLowerCase().includes(t) || a.name.toLowerCase().includes(t)))
  }, [accounts, filter, text])
  useEffect(() => { setHi(0) }, [text, options.length])

  const pick = (a: Account) => { onChange({ account_id: a.id, label: a.name, code: a.code }); setText(''); setOpen(false) }

  const modeChips = allowParties && (
    <div className="mb-1.5 inline-flex rounded-lg bg-slate-100 p-0.5 text-xs font-semibold">
      {(['account', 'party'] as const).map((m) => (
        <button key={m} type="button" disabled={disabled} onClick={() => { setMode(m); onChange(null); setParty(null) }}
          className={`rounded-md px-2.5 py-1 cursor-pointer ${mode === m ? 'bg-white text-ink shadow-sm' : 'text-slate-500'}`}>{m === 'account' ? 'Account' : 'Party'}</button>
      ))}
    </div>
  )

  if (mode === 'party') {
    return (
      <div className={className}>
        {modeChips}
        <PartyPicker value={party} disabled={disabled} type={partyType} placeholder="Search party ledger"
          onChange={(p) => { setParty(p); onChange(p ? { party_id: p.id, label: p.name } : null) }} />
      </div>
    )
  }

  return (
    <div ref={wrap} className={`relative ${className}`}>
      {modeChips}
      <div className="relative">
        <input
          role="combobox" aria-expanded={open} aria-controls={listId} aria-autocomplete="list" autoComplete="off" disabled={disabled}
          aria-activedescendant={open && options.length ? `${listId}-${hi}` : undefined}
          className="input pr-16"
          placeholder={isLoading ? 'Loading accounts…' : placeholder}
          value={open ? text : value?.label ? `${value.code ? `${value.code} · ` : ''}${value.label}` : ''}
          onFocus={() => { setOpen(true); setText('') }}
          onChange={(e) => { setText(e.target.value); setOpen(true) }}
          onKeyDown={(e) => {
            if (e.key === 'ArrowDown') { e.preventDefault(); setOpen(true); setHi((h) => Math.min(options.length - 1, h + 1)) }
            else if (e.key === 'ArrowUp') { e.preventDefault(); setHi((h) => Math.max(0, h - 1)) }
            else if (e.key === 'Enter' && open && options[hi]) { e.preventDefault(); pick(options[hi]) }
            else if (e.key === 'Escape') setOpen(false)
          }}
        />
        <div className="absolute right-2 top-1/2 flex -translate-y-1/2 items-center gap-0.5">
          {value && !disabled && <button type="button" aria-label="Clear account" onClick={() => onChange(null)} className="rounded p-1 text-slate-400 hover:text-ink cursor-pointer"><X size={14} /></button>}
          <button type="button" aria-label="Show accounts" tabIndex={-1} onClick={() => setOpen((o) => !o)} className="rounded p-1 text-slate-400 hover:text-ink cursor-pointer"><ChevronDown size={14} /></button>
        </div>
      </div>
      {open && !disabled && (
        <ul id={listId} role="listbox" className="absolute z-30 mt-1 max-h-72 w-full overflow-auto rounded-xl border border-slate-200 bg-white py-1 shadow-xl">
          {error && <li className="px-3 py-2 text-sm text-rose-600">{(error as Error).message}</li>}
          {!isLoading && options.length === 0 && <li className="px-3 py-2 text-sm text-slate-500">No account matches.</li>}
          {options.map((a, i) => {
            const showHead = i === 0 || options[i - 1].group !== a.group
            return (
              <li key={a.id}>
                {showHead && <div className="px-3 pb-0.5 pt-2 text-[10px] font-bold uppercase tracking-wider text-slate-400">{GROUP_LABEL[a.group] ?? a.group}</div>}
                <div id={`${listId}-${i}`} role="option" aria-selected={i === hi} onMouseEnter={() => setHi(i)} onMouseDown={(e) => { e.preventDefault(); pick(a) }}
                  className={`flex cursor-pointer items-center gap-3 px-3 py-1.5 ${i === hi ? 'bg-brand-50' : ''}`}>
                  <span className="w-12 shrink-0 font-mono text-[11px] text-slate-500">{a.code}</span>
                  <span className="min-w-0 flex-1 truncate text-sm font-medium">{a.name}</span>
                  <Money paise={a.balance_paise} className="shrink-0 text-xs text-slate-500" />
                </div>
              </li>
            )
          })}
        </ul>
      )}
    </div>
  )
}
