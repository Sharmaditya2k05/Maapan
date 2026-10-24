'use client'

import { useState } from 'react'
import { Save, Sparkles } from 'lucide-react'
import { Badge, Button, Card, ErrorBox, Field, Input, QueryState, Table, Toggle, useToast } from '@/components/ui'
import { fmtDate, useGet, usePerm, useSave } from '@/lib/hooks'
import { errorMessage } from '@/lib/api'
import { PERMS } from '@/lib/rbac'
import type { AiSettings, AiUsageReport } from '@/lib/types.more'
import type { AiSettingsInput } from '@/lib/types.stock'

const fmtTokens = (n: number) => n >= 1e6 ? `${(n / 1e6).toFixed(2)} M` : n >= 1e3 ? `${(n / 1e3).toFixed(1)} k` : String(n)

function AiForm({ s }: { s: AiSettings }) {
  const canDo = usePerm()
  const toast = useToast()
  const [enabled, setEnabled] = useState(s.ai_enabled)
  const [budget, setBudget] = useState(s.monthly_token_budget ? String(s.monthly_token_budget) : '')
  const [phone, setPhone] = useState(s.owner_phone ?? '')
  const [err, setErr] = useState('')
  const save = useSave<AiSettings, AiSettingsInput>('/buddhi/v1/ai/settings', { method: 'PUT', invalidate: ['/buddhi/v1/ai'], silent: true })
  const canEdit = canDo(PERMS.settingsManage)

  const submit = async () => {
    setErr('')
    const b = budget.trim() === '' ? null : Number(budget)
    if (b !== null && (!Number.isFinite(b) || b < 0)) return setErr('Budget must be a whole number of tokens, or empty for the platform default.')
    const p = phone.trim()
    if (p && !/^\+\d{10,14}$/.test(p)) return setErr('Owner phone must be in +91XXXXXXXXXX format.')
    try { await save.mutateAsync({ ai_enabled: enabled, monthly_token_budget: b, owner_phone: p || null }); toast('AI settings saved') } catch (e) { setErr(errorMessage(e)) }
  }

  return (
    <form onSubmit={(e) => { e.preventDefault(); void submit() }} noValidate>
      <div className="grid gap-4 sm:grid-cols-2">
        <div className="sm:col-span-2"><Toggle checked={enabled} onChange={(v) => canEdit && setEnabled(v)} label={<span>Enable Ask Taraju, bill scanning and weekly summaries <span className="block text-xs text-slate-500">Questions and bill images are sent to the AI provider; your books never leave your account otherwise</span></span>} /></div>
        <Field label="Monthly token budget" hint="Empty = platform default; 0 = unlimited"><Input value={budget} onChange={(e) => setBudget(e.target.value.replace(/\D/g, ''))} inputMode="numeric" className="num" disabled={!canEdit} placeholder="e.g. 2000000" /></Field>
        <Field label="Owner phone for the weekly WhatsApp summary" hint="+91 followed by 10 digits"><Input value={phone} onChange={(e) => setPhone(e.target.value.replace(/[^\d+]/g, ''))} inputMode="tel" className="font-mono" disabled={!canEdit} placeholder="+919876543210" /></Field>
      </div>
      <p className="mt-3 text-xs text-slate-500">Last updated {fmtDate(s.updated_at, true)}.{!canEdit && ' Only owners can change these.'}</p>
      {err && <div className="mt-4"><ErrorBox message={err} /></div>}
      {canEdit && <div className="mt-5 flex justify-end"><Button type="submit" icon={Save} loading={save.isPending}>Save</Button></div>}
    </form>
  )
}

export function AiSection() {
  const canDo = usePerm()
  const settings = useGet<AiSettings>(canDo(PERMS.aiUse) ? '/buddhi/v1/ai/settings' : null)
  const usage = useGet<AiUsageReport>(canDo(PERMS.aiUse) ? '/buddhi/v1/ai/usage' : null)
  const u = usage.data
  const pct = u && u.token_budget > 0 ? Math.min(100, Math.round((u.tokens_used / u.token_budget) * 100)) : null
  return (
    <div className="space-y-4">
      <Card title="AI assistant">
        {!canDo(PERMS.aiUse) ? <p className="text-sm text-slate-500">Your role cannot use the AI features, so there is nothing to configure here.</p> : (
          <QueryState isLoading={settings.isLoading} error={settings.error} data={settings.data} retry={() => settings.refetch()}>
            {(s) => <AiForm key={s.updated_at} s={s} />}
          </QueryState>
        )}
      </Card>
      {canDo(PERMS.aiUse) && (
        <Card title="Usage this month" padded={false} actions={u && <Badge tone={u.ai_configured ? (u.ai_enabled ? 'green' : 'gray') : 'red'}>{!u.ai_configured ? 'AI not configured on the server' : u.ai_enabled ? 'on' : 'off'}</Badge>}>
          <QueryState isLoading={usage.isLoading} error={usage.error} data={u} retry={() => usage.refetch()}>
            {(x) => (
              <>
                <div className="px-4 py-4 sm:px-5">
                  <div className="flex items-center justify-between text-sm"><span className="inline-flex items-center gap-2 font-semibold"><Sparkles size={16} className="text-brand-600" />{fmtTokens(x.tokens_used)} tokens since {fmtDate(x.month_start)}</span><span className="text-slate-500">{x.token_budget > 0 ? `budget ${fmtTokens(x.token_budget)}` : 'no budget cap'}</span></div>
                  {pct !== null && <div className="mt-2 h-2 overflow-hidden rounded-full bg-slate-100" role="progressbar" aria-valuenow={pct} aria-valuemin={0} aria-valuemax={100}><div className={`h-full rounded-full ${pct >= 90 ? 'bg-rose-500' : pct >= 70 ? 'bg-amber-500' : 'bg-brand-500'}`} style={{ width: `${pct}%` }} /></div>}
                </div>
                {(x.by_feature ?? []).length > 0 && (
                  <Table>
                    <thead className="bg-slate-50/60"><tr><th className="th">Feature</th><th className="th">Model</th><th className="th text-right">Calls</th><th className="th text-right">Input</th><th className="th text-right">Output</th><th className="th text-right">Cache read</th></tr></thead>
                    <tbody className="divide-y divide-slate-100">
                      {(x.by_feature ?? []).map((f) => <tr key={`${f.feature}-${f.model}`}><td className="td font-semibold">{f.feature}</td><td className="td font-mono text-xs text-slate-600">{f.model}</td><td className="td num text-right">{f.calls}</td><td className="td num text-right">{fmtTokens(f.input_tokens)}</td><td className="td num text-right">{fmtTokens(f.output_tokens)}</td><td className="td num text-right text-slate-500">{fmtTokens(f.cache_read_tokens)}</td></tr>)}
                    </tbody>
                  </Table>
                )}
              </>
            )}
          </QueryState>
        </Card>
      )}
    </div>
  )
}
