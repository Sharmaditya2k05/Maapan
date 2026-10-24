'use client'

/** kar GSTIN verification result, with honest wording about how far it was checked. */
import { CheckCircle2, ShieldAlert, ShieldCheck, XCircle } from 'lucide-react'
import { Badge } from '@/components/ui'
import { STATES } from '@/lib/gst'
import { gstinSourceText } from '@/lib/ledger/util'
import type { GstinResult } from '@/lib/types.ledger'
import { KV } from './bits'

export function GstinCard({ result: r, compact }: { result: GstinResult; compact?: boolean }) {
  const portal = r.source === 'gsp' || r.source === 'cache'
  const active = r.status.toLowerCase() === 'active'
  const Icon = !r.valid ? XCircle : portal ? (active ? ShieldCheck : ShieldAlert) : CheckCircle2
  const tone = !r.valid ? 'bg-rose-50 text-rose-700' : portal && active ? 'bg-emerald-50 text-emerald-700' : portal ? 'bg-amber-50 text-amber-700' : 'bg-sky-50 text-sky-700'
  return (
    <div className={`rounded-xl px-4 py-3 text-sm ${tone}`}>
      <div className="flex items-start gap-2">
        <Icon size={18} className="mt-0.5 shrink-0" />
        <div className="min-w-0 flex-1">
          <div className="font-semibold">{!r.valid ? 'GSTIN is not valid' : portal ? (active ? 'Active on the GST portal' : `GST portal says: ${r.status || 'status unknown'}`) : 'Format valid, not checked with the GST portal'}</div>
          <div className="text-xs opacity-80">{gstinSourceText(r.source)}{r.message ? ` · ${r.message}` : ''}</div>
          {!compact && r.valid && (
            <dl className="mt-2 divide-y divide-current/10 text-ink">
              <KV k="GSTIN" v={r.gstin} mono />
              {r.legal_name && <KV k="Legal name" v={r.legal_name} />}
              {r.trade_name && <KV k="Trade name" v={r.trade_name} />}
              <KV k="State" v={`${STATES[r.state_code] ?? r.state_code} (${r.state_code})`} />
              {r.registration_type && <KV k="Registration" v={r.registration_type} />}
              {r.address && <KV k="Address" v={r.address} />}
              {r.status && <KV k="Status" v={<Badge tone={active ? 'green' : 'amber'}>{r.status}</Badge>} />}
            </dl>
          )}
        </div>
      </div>
    </div>
  )
}
