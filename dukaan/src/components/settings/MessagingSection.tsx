'use client'

/**
 * Messaging (soochna): the store's own senders and provider template ids, the payment-reminder schedule
 * (GET|PUT /v1/settings/messaging) and messages that kept failing (GET /v1/dead-letters, POST /v1/dead-letters/replay).
 */
import { useState } from 'react'
import { MailWarning, RotateCcw, Save } from 'lucide-react'
import { Badge, Button, Card, Empty, Field, Input, Pagination, QueryState, Select, Table, Toggle } from '@/components/ui'
import { api, post } from '@/lib/api'
import { fmtDate, useGet, useList } from '@/lib/hooks'
import type { DeadLetter, MessagingSettings, ReplayInput, ReplayResult, TemplatesResponse, TenantTemplate } from '@/lib/types.phase4'
import { parseDays } from '@/lib/ledger/phase4'
import { useAction } from '@/components/ledger/useAction'

const CHANNELS = ['whatsapp', 'sms', 'email'] as const

export function MessagingSection() {
  return <div className="space-y-4"><SendersCard /><DeadLettersCard /></div>
}

function SendersCard() {
  const q = useGet<MessagingSettings>('/soochna/v1/settings/messaging')
  return (
    <Card title="Senders, templates and reminders">
      <QueryState isLoading={q.isLoading} error={q.error} data={q.data} retry={() => q.refetch()}>
        {(s) => <SendersForm key={s.updated_at ?? 'new'} initial={s} />}
      </QueryState>
    </Card>
  )
}

function SendersForm({ initial }: { initial: MessagingSettings }) {
  const templates = useGet<TemplatesResponse>('/soochna/v1/templates', undefined, { staleTime: 3600_000 })
  const [s, setS] = useState<MessagingSettings>(initial)
  const [days, setDays] = useState((initial.reminder_days ?? [0, 7, 15, 30]).join(', '))
  const [err, setErr] = useState<string | null>(null)
  const set = <K extends keyof MessagingSettings>(k: K, v: MessagingSettings[K]) => setS((x) => ({ ...x, [k]: v }))
  const tpl = s.templates ?? []
  const tplId = (template: string, channel: string) => tpl.find((t) => t.template === template && t.channel === channel)?.provider_template_id ?? ''
  const setTpl = (template: string, channel: 'sms' | 'whatsapp', id: string) => set('templates', [...tpl.filter((t) => !(t.template === template && t.channel === channel)), ...(id.trim() ? [{ template, channel, provider_template_id: id.trim() } as TenantTemplate] : [])])
  const save = useAction<MessagingSettings, MessagingSettings>((b) => api('/soochna/v1/settings/messaging', { method: 'PUT', body: b }), { invalidate: ['/soochna/v1/settings/messaging'], successMessage: 'Messaging settings saved' })
  const submit = () => {
    const d = parseDays(days)
    if (d === null) return setErr('Reminder days: whole numbers 0–365, separated by commas.')
    setErr(null)
    save.mutate({
      sms_sender_id: s.sms_sender_id.trim(), whatsapp_source: s.whatsapp_source.trim(), whatsapp_app_name: s.whatsapp_app_name.trim(),
      email_from: s.email_from.trim(), email_from_name: s.email_from_name.trim(), email_reply_to: s.email_reply_to.trim(),
      payment_reminders: s.payment_reminders, reminder_days: d, reminder_channels: s.reminder_channels ?? [], templates: tpl,
    })
  }
  const channelsOn = s.reminder_channels ?? []
  const details = (templates.data?.details ?? []).filter((t) => t.category !== 'otp')

  return (
    <div className="space-y-5">
      <p className="text-sm text-slate-600">Leave a field empty to send from Maapan’s shared sender. Your own template ids are used only with your own sender (a DLT template belongs to its header, a WhatsApp template to its number).</p>
      <div className="grid gap-4 sm:grid-cols-3">
        <Field label="SMS sender (DLT header)" hint="6 letters, linked to the templates in MSG91"><Input value={s.sms_sender_id} maxLength={6} onChange={(e) => set('sms_sender_id', e.target.value.toUpperCase())} className="font-mono" /></Field>
        <Field label="WhatsApp number"><Input value={s.whatsapp_source} onChange={(e) => set('whatsapp_source', e.target.value)} placeholder="+919111111111" inputMode="tel" /></Field>
        <Field label="WhatsApp app name (Gupshup)"><Input value={s.whatsapp_app_name} onChange={(e) => set('whatsapp_app_name', e.target.value)} /></Field>
        <Field label="E-mail from" hint="Must be verified with the e-mail provider"><Input type="email" value={s.email_from} onChange={(e) => set('email_from', e.target.value)} /></Field>
        <Field label="From name"><Input value={s.email_from_name} onChange={(e) => set('email_from_name', e.target.value)} /></Field>
        <Field label="Reply-to"><Input type="email" value={s.email_reply_to} onChange={(e) => set('email_reply_to', e.target.value)} /></Field>
      </div>

      <div className="rounded-xl border border-slate-200 p-4">
        <Toggle checked={s.payment_reminders} onChange={(v) => set('payment_reminders', v)} label={<span className="font-semibold">Send payment reminders for overdue bills</span>} />
        {s.payment_reminders && (
          <div className="mt-3 grid gap-4 sm:grid-cols-2">
            <Field label="Days past due to remind on" hint="0 = on the due date"><Input value={days} onChange={(e) => setDays(e.target.value)} placeholder="0, 7, 15, 30" /></Field>
            <Field label="Channels">
              <div className="flex flex-wrap gap-3 pt-2">
                {CHANNELS.map((c) => <label key={c} className="inline-flex items-center gap-2 text-sm"><input type="checkbox" className="accent-brand-600" checked={channelsOn.includes(c)} onChange={() => set('reminder_channels', channelsOn.includes(c) ? channelsOn.filter((x) => x !== c) : [...channelsOn, c])} />{c}</label>)}
              </div>
            </Field>
          </div>
        )}
      </div>

      <div>
        <h3 className="mb-2 text-sm font-bold">Your provider template ids</h3>
        <Table>
          <thead className="bg-slate-50/60"><tr><th className="th">Template</th><th className="th">Variables (in order)</th><th className="th">WhatsApp template id</th><th className="th">SMS (DLT) template id</th></tr></thead>
          <tbody className="divide-y divide-slate-100">
            {details.map((t) => (
              <tr key={t.name}>
                <td className="td"><div className="font-semibold">{t.name}</div><Badge tone={t.category === 'promotional' ? 'amber' : t.category === 'alert' ? 'blue' : 'gray'}>{t.category}</Badge></td>
                <td className="td text-xs text-slate-500">{t.params.map((p, i) => `${i + 1}. ${p}`).join(' · ')}</td>
                <td className="td">{(t.channels ?? []).includes('whatsapp') ? <Input className="input-sm font-mono text-xs" value={tplId(t.name, 'whatsapp')} onChange={(e) => setTpl(t.name, 'whatsapp', e.target.value)} /> : <span className="text-xs text-slate-300">—</span>}</td>
                <td className="td">{(t.channels ?? []).includes('sms') ? <Input className="input-sm font-mono text-xs" value={tplId(t.name, 'sms')} onChange={(e) => setTpl(t.name, 'sms', e.target.value)} /> : <span className="text-xs text-slate-300">—</span>}</td>
              </tr>
            ))}
          </tbody>
        </Table>
      </div>
      {err && <p className="text-sm text-rose-600">{err}</p>}
      <div className="flex items-center justify-between gap-2">
        <span className="text-xs text-slate-400">{initial.updated_at ? `Last saved ${fmtDate(initial.updated_at, true)}` : 'Using Maapan defaults'}</span>
        <Button icon={Save} loading={save.isPending} onClick={submit}>Save</Button>
      </div>
    </div>
  )
}

function DeadLettersCard() {
  const [all, setAll] = useState(false)
  const [channel, setChannel] = useState('')
  const [offset, setOffset] = useState(0)
  const q = useList<DeadLetter>('/soochna/v1/dead-letters', { all: all || undefined, channel: channel || undefined, limit: 25, offset })
  const [picked, setPicked] = useState<string[]>([])
  const replay = useAction<ReplayResult, ReplayInput>((b) => post('/soochna/v1/dead-letters/replay', b), {
    invalidate: ['/soochna/v1/dead-letters', '/soochna/v1/messages'], successMessage: 'Replay sent', onSuccess: () => setPicked([]),
  })
  const items = q.data?.items ?? []
  return (
    <Card title="Undelivered messages" padded={false} actions={
      <div className="flex gap-2">
        <Button size="sm" variant="secondary" icon={RotateCcw} disabled={picked.length === 0} loading={replay.isPending && !!replay.variables?.ids} onClick={() => replay.mutate({ ids: picked })}>Replay {picked.length || ''} selected</Button>
        <Button size="sm" icon={RotateCcw} disabled={items.length === 0} loading={replay.isPending && !replay.variables?.ids} onClick={() => replay.mutate({ channel: channel || undefined, limit: 100 })}>Replay all</Button>
      </div>}>
      <div className="flex flex-wrap items-center gap-2 p-4">
        <Select value={channel} onChange={(e) => { setChannel(e.target.value); setOffset(0) }} className="input-sm w-auto" aria-label="Channel"><option value="">All channels</option>{CHANNELS.map((c) => <option key={c} value={c}>{c}</option>)}</Select>
        <Toggle checked={all} onChange={(v) => { setAll(v); setOffset(0) }} label="Include already replayed" />
        {replay.data && <span className="text-xs text-slate-500">Last replay: {replay.data.replayed} sent · {Object.entries(replay.data.statuses ?? {}).map(([k, v]) => `${v} ${k}`).join(', ')}</span>}
      </div>
      <QueryState isLoading={q.isLoading} error={q.error} data={q.data} retry={() => q.refetch()}>
        {(d) => items.length === 0 ? <Empty icon={MailWarning} title="Nothing stuck" text="Messages that fail 5 times (or for 24 hours) park here; replay them once the cause is fixed." /> : (
          <>
            <Table>
              <thead className="bg-slate-50/60"><tr><th className="th w-8" /><th className="th">When</th><th className="th">To</th><th className="th">Template</th><th className="th">Why</th><th className="th text-right">Attempts</th></tr></thead>
              <tbody className="divide-y divide-slate-100">
                {items.map((m) => (
                  <tr key={m.id}>
                    <td className="td"><input type="checkbox" className="accent-brand-600" checked={picked.includes(m.id)} onChange={() => setPicked((p) => (p.includes(m.id) ? p.filter((x) => x !== m.id) : [...p, m.id]))} aria-label="Select" /></td>
                    <td className="td whitespace-nowrap text-xs text-slate-600">{fmtDate(m.dead_at, true)}{m.replay_count > 0 && <div className="text-[11px] text-slate-400">replayed {m.replay_count}×</div>}</td>
                    <td className="td font-mono text-xs">{m.to}<div className="font-sans text-[11px] text-slate-400">{m.channel}</div></td>
                    <td className="td">{m.template}</td>
                    <td className="td max-w-[280px] text-xs"><Badge tone="red">{m.reason}</Badge>{m.last_error && <div className="mt-0.5 truncate text-slate-500" title={m.last_error}>{m.last_error}</div>}</td>
                    <td className="td num text-right">{m.attempts}</td>
                  </tr>
                ))}
              </tbody>
            </Table>
            <Pagination total={d.total} limit={d.limit} offset={d.offset} onChange={setOffset} />
          </>
        )}
      </QueryState>
    </Card>
  )
}
