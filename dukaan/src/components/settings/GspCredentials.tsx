'use client'

/**
 * GST portal API users per GSTIN (kar /v1/settings/gsp-credentials): the e-invoice (IRP) and
 * e-way bill (NIC) API usernames and passwords the GSP calls are made with. kar keeps them
 * AES-GCM sealed and only ever returns them masked; a blank password keeps the stored one.
 */
import { useMemo, useState } from 'react'
import { KeyRound, Pencil, Trash2 } from 'lucide-react'
import { Badge, Button, Card, Confirm, Empty, ErrorBox, Field, IconButton, Input, Modal, QueryState, Table, useToast } from '@/components/ui'
import { api, errorMessage } from '@/lib/api'
import { fmtDate, useGet } from '@/lib/hooks'
import { registrationLabel } from '@/lib/compliance'
import type { GspCredentialInput, GspCredentialSummary } from '@/lib/types.kar'
import { useRegistrations } from '@/components/compliance/registrations'
import { useAction } from '@/components/ledger/useAction'

const PATH = '/kar/v1/settings/gsp-credentials'

export function GspCredentials() {
  const toast = useToast()
  const regs = useRegistrations()
  const list = useGet<{ items: GspCredentialSummary[] | null }>(PATH)
  const [edit, setEdit] = useState<{ gstin: string; current?: GspCredentialSummary } | null>(null)
  const [remove, setRemove] = useState<string | null>(null)
  const byGstin = useMemo(() => new Map((list.data?.items ?? []).map((c) => [c.gstin, c])), [list.data])
  const del = useAction<void, string>((g) => api<void>(`${PATH}/${g}`, { method: 'DELETE' }), {
    invalidate: [PATH], onSuccess: (_, g) => { toast(`Portal users of ${g} removed`); setRemove(null) },
  })

  return (
    <Card title="GST portal API users" padded={false}>
      <p className="border-b border-slate-100 px-4 py-3 text-xs text-slate-500 sm:px-5">Create an API user for your GSP on einvoice1.gst.gov.in and ewaybillgst.gov.in (Registration → For GSP), then save it here per GSTIN. Needed only with a GSP contract; the IRP simulator ignores them. Passwords are stored encrypted and never shown again.</p>
      <QueryState isLoading={list.isLoading || regs.isLoading} error={list.error ?? regs.error} data={list.data} retry={() => list.refetch()}>
        {() => regs.fileable.length === 0 ? <Empty icon={KeyRound} title="No GSTIN registered" text="Add a GST registration first." /> : (
          <Table>
            <thead className="bg-slate-50/60"><tr><th className="th">Registration</th><th className="th">e-Invoice user</th><th className="th">e-Way bill user</th><th className="th">Updated</th><th className="th w-24" /></tr></thead>
            <tbody className="divide-y divide-slate-100">
              {regs.fileable.map((r) => {
                const c = byGstin.get(r.gstin)
                return (
                  <tr key={r.id} className="hover:bg-slate-50/60">
                    <td className="td"><div className="font-mono text-xs font-semibold">{r.gstin}</div><div className="text-[11px] text-slate-500">{registrationLabel(r)}</div></td>
                    <td className="td text-xs">{c?.einvoice_username_masked ? <><span className="font-mono">{c.einvoice_username_masked}</span>{!c.has_einvoice_password && <Badge tone="amber" className="ml-1">no password</Badge>}</> : <span className="text-slate-400">not set</span>}</td>
                    <td className="td text-xs">{c?.eway_username_masked ? <><span className="font-mono">{c.eway_username_masked}</span>{!c.has_eway_password && <Badge tone="amber" className="ml-1">no password</Badge>}</> : <span className="text-slate-400">not set</span>}</td>
                    <td className="td text-xs text-slate-500">{c ? fmtDate(c.updated_at, true) : '—'}</td>
                    <td className="td">
                      <div className="flex justify-end gap-0.5">
                        <IconButton icon={Pencil} label={`Edit portal users of ${r.gstin}`} onClick={() => setEdit({ gstin: r.gstin, current: c })} />
                        {c && <IconButton icon={Trash2} label={`Remove portal users of ${r.gstin}`} className="hover:text-rose-600" onClick={() => setRemove(r.gstin)} />}
                      </div>
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </Table>
        )}
      </QueryState>
      {edit && <CredentialModal key={edit.gstin} gstin={edit.gstin} current={edit.current} onClose={() => setEdit(null)} />}
      <Confirm open={!!remove} onClose={() => setRemove(null)} danger confirmLabel="Remove" loading={del.isPending} title={`Remove the portal users of ${remove ?? ''}?`}
        text="e-Invoices and e-way bills of this GSTIN will fail through the GSP until new users are saved." onConfirm={() => remove && del.mutate(remove)} />
    </Card>
  )
}

function CredentialModal({ gstin, current, onClose }: { gstin: string; current?: GspCredentialSummary; onClose: () => void }) {
  const toast = useToast()
  const [d, setD] = useState<GspCredentialInput>({ einvoice_username: '', einvoice_password: '', eway_username: '', eway_password: '' })
  const [local, setLocal] = useState('')
  const set = (k: keyof GspCredentialInput, v: string) => setD((p) => ({ ...p, [k]: v }))
  const save = useAction<GspCredentialSummary, GspCredentialInput>((b) => api<GspCredentialSummary>(`${PATH}/${gstin}`, { method: 'PUT', body: b }), {
    invalidate: [PATH], silent: true, onSuccess: () => { toast(`Portal users of ${gstin} saved`); onClose() },
  })
  const submit = () => {
    setLocal('')
    const b: GspCredentialInput = { einvoice_username: d.einvoice_username.trim(), einvoice_password: d.einvoice_password, eway_username: d.eway_username.trim(), eway_password: d.eway_password }
    if (!b.einvoice_username && !b.eway_username && !current) return setLocal('Enter at least one username.')
    if (b.einvoice_username && !b.einvoice_password && !current?.has_einvoice_password) return setLocal('Enter the e-invoice API password.')
    if (b.eway_username && !b.eway_password && !current?.has_eway_password) return setLocal('Enter the e-way bill API password.')
    save.mutate(b)
  }
  const keep = (has?: boolean) => (has ? 'Leave blank to keep the saved password' : undefined)
  return (
    <Modal open onClose={onClose} title={`Portal API users · ${gstin}`}
      footer={<><Button variant="secondary" onClick={onClose}>Cancel</Button><Button icon={KeyRound} onClick={submit} loading={save.isPending}>Save</Button></>}>
      <form onSubmit={(e) => { e.preventDefault(); submit() }} noValidate className="space-y-4" autoComplete="off">
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="e-Invoice API username" hint={current?.einvoice_username_masked ? `Saved: ${current.einvoice_username_masked} · leave blank to keep` : undefined}><Input value={d.einvoice_username} onChange={(e) => set('einvoice_username', e.target.value)} autoComplete="off" autoFocus /></Field>
          <Field label="e-Invoice API password" hint={keep(current?.has_einvoice_password)}><Input type="password" value={d.einvoice_password} onChange={(e) => set('einvoice_password', e.target.value)} autoComplete="new-password" placeholder={current?.has_einvoice_password ? '••••••••' : ''} /></Field>
          <Field label="e-Way bill API username" hint={current?.eway_username_masked ? `Saved: ${current.eway_username_masked} · leave blank to keep` : undefined}><Input value={d.eway_username} onChange={(e) => set('eway_username', e.target.value)} autoComplete="off" /></Field>
          <Field label="e-Way bill API password" hint={keep(current?.has_eway_password)}><Input type="password" value={d.eway_password} onChange={(e) => set('eway_password', e.target.value)} autoComplete="new-password" placeholder={current?.has_eway_password ? '••••••••' : ''} /></Field>
        </div>
        <button type="submit" hidden />
      </form>
      {local && <div className="mt-4"><ErrorBox message={local} /></div>}
      {save.error && <div className="mt-4"><ErrorBox message={errorMessage(save.error)} /></div>}
    </Modal>
  )
}
