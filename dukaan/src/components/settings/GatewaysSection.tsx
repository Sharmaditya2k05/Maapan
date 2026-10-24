'use client'

import { useState } from 'react'
import { Copy, KeyRound, Trash2 } from 'lucide-react'
import { Badge, Button, Card, Confirm, ErrorBox, Field, Input, QueryState, Select, useToast } from '@/components/ui'
import { fmtDate, useGet, usePerm } from '@/lib/hooks'
import { API_URL, api, del, errorMessage } from '@/lib/api'
import { PERMS } from '@/lib/rbac'
import type { GatewayAvailability, GatewayCredentialInput, GatewayName, GatewaySettingsResponse, GatewaySummary } from '@/lib/types.stock'
import { useAction, useCopy } from '@/components/stock/hooks'

const GATEWAYS: Record<GatewayName, { label: string; keyLabel: string; secretLabel: string; webhook: 'required' | 'none'; envs: string[]; dashboard: string }> = {
  razorpay: { label: 'Razorpay', keyLabel: 'Key ID (rzp_live_… / rzp_test_…)', secretLabel: 'Key secret', webhook: 'required', envs: ['live', 'test'], dashboard: 'Razorpay Dashboard → Account & Settings → Webhooks' },
  cashfree: { label: 'Cashfree', keyLabel: 'Client ID', secretLabel: 'Client secret', webhook: 'none', envs: ['production', 'sandbox'], dashboard: 'Cashfree Merchant Dashboard → Developers → Webhooks' },
}

function GatewayCard({ name, current, isDefault, onChanged }: { name: GatewayName; current?: GatewaySummary; isDefault: boolean; onChanged: () => void }) {
  const g = GATEWAYS[name]
  const canDo = usePerm()
  const toast = useToast()
  const copy = useCopy()
  const [open, setOpen] = useState(!current)
  const [d, setD] = useState<GatewayCredentialInput>({ key_id: '', key_secret: '', webhook_secret: '', env: '' })
  const [err, setErr] = useState('')
  const [confirm, setConfirm] = useState(false)
  const put = useAction<GatewaySummary, GatewayCredentialInput>((body) => api(`/lenden/v1/settings/gateways/${name}`, { method: 'PUT', body }), { invalidate: ['/lenden/v1/settings/gateways', '/lenden/v1/gateways'], silent: true })
  const remove = useAction(() => del(`/lenden/v1/settings/gateways/${name}`), { invalidate: ['/lenden/v1/settings/gateways', '/lenden/v1/gateways'], silent: true })
  const webhookUrl = `${API_URL}/lenden/v1/webhooks/${name}`
  const canEdit = canDo(PERMS.settingsManage)

  const save = async () => {
    setErr('')
    if (!d.key_id.trim() || !d.key_secret.trim()) return setErr('Key ID and secret are required.')
    if (g.webhook === 'required' && !d.webhook_secret?.trim()) return setErr(`${g.label} needs the webhook secret too, or payments will never be confirmed.`)
    try {
      await put.mutateAsync({ key_id: d.key_id.trim(), key_secret: d.key_secret.trim(), webhook_secret: d.webhook_secret?.trim() || undefined, env: d.env || undefined })
      toast(`${g.label} keys saved`)
      setD({ key_id: '', key_secret: '', webhook_secret: '', env: '' })
      setOpen(false)
      onChanged()
    } catch (e) { setErr(errorMessage(e)) }
  }

  return (
    <Card title={<span className="inline-flex items-center gap-2">{g.label}{isDefault && <Badge tone="brand">default for new payments</Badge>}</span>}
      actions={current && canEdit && <><Button size="sm" variant="secondary" icon={KeyRound} onClick={() => setOpen((o) => !o)}>{open ? 'Close' : 'Replace keys'}</Button><Button size="sm" variant="ghost" icon={Trash2} className="text-rose-600" onClick={() => setConfirm(true)}>Remove</Button></>}>
      {current ? (
        <div className="mb-4 grid gap-2 text-sm sm:grid-cols-3">
          <div><div className="text-xs text-slate-500">Key ID</div><div className="font-mono font-semibold">{current.key_id_masked}</div></div>
          <div><div className="text-xs text-slate-500">Environment</div><Badge tone={current.env === 'live' || current.env === 'production' ? 'green' : 'amber'}>{current.env || 'default'}</Badge></div>
          <div><div className="text-xs text-slate-500">Webhook secret</div><Badge tone={current.has_webhook_secret || g.webhook === 'none' ? 'green' : 'red'}>{g.webhook === 'none' ? 'signed with client secret' : current.has_webhook_secret ? 'stored' : 'missing'}</Badge></div>
          <div className="text-xs text-slate-500 sm:col-span-3">Saved {fmtDate(current.updated_at, true)}. Secrets are encrypted at rest and never shown again.</div>
        </div>
      ) : (
        <p className="mb-4 text-sm text-slate-600">Not configured: payments use the platform&apos;s {g.label} account (if any). Add this store&apos;s own keys so collections land in your bank account.</p>
      )}

      {open && canEdit && (
        <div className="grid gap-4 rounded-xl border border-slate-100 bg-slate-50/60 p-4 sm:grid-cols-2">
          <Field label={g.keyLabel}><Input value={d.key_id} onChange={(e) => setD((p) => ({ ...p, key_id: e.target.value }))} className="font-mono" autoComplete="off" /></Field>
          <Field label={g.secretLabel}><Input type="password" value={d.key_secret} onChange={(e) => setD((p) => ({ ...p, key_secret: e.target.value }))} className="font-mono" autoComplete="new-password" /></Field>
          {g.webhook === 'required' && <Field label="Webhook secret" hint="Set it in the gateway dashboard when creating the webhook"><Input type="password" value={d.webhook_secret} onChange={(e) => setD((p) => ({ ...p, webhook_secret: e.target.value }))} className="font-mono" autoComplete="new-password" /></Field>}
          <Field label="Environment" hint="Detected from the key when left on auto"><Select value={d.env} onChange={(e) => setD((p) => ({ ...p, env: e.target.value }))}><option value="">Auto</option>{g.envs.map((e) => <option key={e} value={e}>{e}</option>)}</Select></Field>
          {err && <div className="sm:col-span-2"><ErrorBox message={err} /></div>}
          <div className="flex justify-end gap-2 sm:col-span-2">{current && <Button variant="secondary" onClick={() => setOpen(false)}>Cancel</Button>}<Button onClick={save} loading={put.isPending}>Save keys</Button></div>
        </div>
      )}

      <div className="mt-4 rounded-xl border border-dashed border-slate-200 p-3 text-sm">
        <div className="mb-1 text-xs font-semibold uppercase tracking-wide text-slate-500">Webhook URL · paste in {g.dashboard}</div>
        <div className="flex items-center gap-2"><code className="min-w-0 flex-1 truncate font-mono text-xs">{webhookUrl}</code><Button size="sm" variant="secondary" icon={Copy} onClick={() => copy(webhookUrl, 'Webhook URL copied')}>Copy</Button></div>
        <div className="mt-1 text-xs text-slate-500">{name === 'razorpay' ? 'Events: payment.captured, qr_code.credited, payment_link.paid, refund.processed, settlement.processed.' : 'Events: Payment success, Payment link, Refund.'}</div>
      </div>

      <Confirm open={confirm} onClose={() => setConfirm(false)} onConfirm={() => remove.mutate(undefined, { onSuccess: () => { setConfirm(false); toast(`${g.label} keys removed`); onChanged() }, onError: (e) => setErr(errorMessage(e)) })} loading={remove.isPending} danger confirmLabel="Remove keys"
        title={`Remove ${g.label} keys?`} text="New payments fall back to the platform account. Payments already taken on these keys still refund and reconcile." />
    </Card>
  )
}

export function GatewaysSection() {
  const canDo = usePerm()
  const settings = useGet<GatewaySettingsResponse>('/lenden/v1/settings/gateways')
  const avail = useGet<GatewayAvailability>(canDo(PERMS.paymentCollect) ? '/lenden/v1/gateways' : null)
  return (
    <div className="space-y-4">
      {avail.data && <p className="text-sm text-slate-600">Available for this business: {(avail.data.available ?? []).join(', ') || 'none'} · default <span className="font-semibold">{avail.data.default}</span>. The default is set by the platform; a payment request may still name a gateway explicitly.</p>}
      <QueryState isLoading={settings.isLoading} error={settings.error} data={settings.data} retry={() => settings.refetch()}>
        {(d) => (
          <>
            {((d.configurable && d.configurable.length ? d.configurable : ['razorpay', 'cashfree']) as GatewayName[]).filter((n) => GATEWAYS[n]).map((n) => (
              <GatewayCard key={n} name={n} current={d.items.find((i) => i.gateway === n)} isDefault={avail.data?.default === n} onChanged={() => { void settings.refetch(); void avail.refetch() }} />
            ))}
          </>
        )}
      </QueryState>
    </div>
  )
}
