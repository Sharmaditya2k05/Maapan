'use client'

/**
 * Message consent (soochna GET|POST /v1/consents): who opted out (STOP keywords, global) or is on this store's
 * do-not-message list. Bulk DND is sent as one POST per address — soochna's bulk endpoint is internal only.
 */
import { useState } from 'react'
import { BellOff, Plus, Upload } from 'lucide-react'
import { Badge, Button, Card, Empty, ErrorBox, Field, Input, Modal, QueryState, SearchInput, Select, Table, Textarea } from '@/components/ui'
import { errorMessage, post } from '@/lib/api'
import { fmtDate, useDebounced, useGet, useInvalidate, usePerm } from '@/lib/hooks'
import { PERMS } from '@/lib/rbac'
import type { Consent, ConsentInput, ConsentStatus } from '@/lib/types.phase4'
import { parseAddresses } from '@/lib/ledger/phase4'
import { NoPermission } from '@/components/ledger/bits'
import { useAction } from '@/components/ledger/useAction'

const STATUS_LABEL: Record<ConsentStatus, string> = { opted_out: 'Opted out', dnd: 'Do not message', opted_in: 'Opted in' }
const tone = (s: ConsentStatus) => (s === 'opted_in' ? 'green' : s === 'dnd' ? 'amber' : 'red')

export function ConsentsSection() {
  const canDo = usePerm()
  const [search, setSearch] = useState('')
  const address = useDebounced(search.trim(), 300)
  const q = useGet<{ items: Consent[] }>(canDo(PERMS.partyManage) ? '/soochna/v1/consents' : null, { address: address || undefined, limit: 200 })
  const [add, setAdd] = useState(false)
  const [bulk, setBulk] = useState(false)
  if (!canDo(PERMS.partyManage)) return <Card><NoPermission what="message consent" /></Card>
  const items = q.data?.items ?? []
  return (
    <Card title="Message consent" padded={false} actions={<div className="flex gap-2"><Button size="sm" variant="secondary" icon={Upload} onClick={() => setBulk(true)}>Bulk DND</Button><Button size="sm" icon={Plus} onClick={() => setAdd(true)}>Add</Button></div>}>
      <div className="p-4">
        <SearchInput value={search} onChange={setSearch} placeholder="Phone (+91…) or e-mail" />
        <p className="mt-2 text-xs text-slate-500">Opt-outs block bills, receipts and reminders on that channel; do-not-message also blocks promotions. OTPs and owner alerts are always sent. Customers can reply STOP / START themselves.</p>
      </div>
      <QueryState isLoading={q.isLoading} error={q.error} data={q.data} retry={() => q.refetch()}>
        {() => items.length === 0 ? <Empty icon={BellOff} title={address ? 'No consent record for this address' : 'Nobody has opted out'} /> : (
          <Table>
            <thead className="bg-slate-50/60"><tr><th className="th">Address</th><th className="th">Channel</th><th className="th">Status</th><th className="th">Scope</th><th className="th">Source</th><th className="th">Updated</th><th className="th" /></tr></thead>
            <tbody className="divide-y divide-slate-100">
              {items.map((c) => (
                <tr key={`${c.scope}-${c.address}-${c.channel}`}>
                  <td className="td font-mono text-xs">{c.address}{c.note && <div className="font-sans text-[11px] text-slate-400">{c.note}</div>}</td>
                  <td className="td">{c.channel}</td>
                  <td className="td"><Badge tone={tone(c.status)}>{STATUS_LABEL[c.status] ?? c.status}</Badge></td>
                  <td className="td text-xs text-slate-600">{c.scope === 'global' ? 'All stores (keyword)' : 'This business'}</td>
                  <td className="td text-xs text-slate-500">{c.source}</td>
                  <td className="td text-xs text-slate-500">{fmtDate(c.updated_at, true)}</td>
                  <td className="td text-right">{c.scope !== 'global' && c.status !== 'opted_in' && <RestoreButton c={c} />}</td>
                </tr>
              ))}
            </tbody>
          </Table>
        )}
      </QueryState>
      {add && <AddModal onClose={() => setAdd(false)} />}
      {bulk && <BulkModal onClose={() => setBulk(false)} />}
    </Card>
  )
}

function RestoreButton({ c }: { c: Consent }) {
  const act = useAction<unknown, ConsentInput>((b) => post('/soochna/v1/consents', b), { invalidate: ['/soochna/v1/consents'], successMessage: 'Messaging restored' })
  return <Button size="sm" variant="ghost" loading={act.isPending} onClick={() => act.mutate({ address: c.address, channel: c.channel as ConsentInput['channel'], status: 'opted_in' })}>Allow again</Button>
}

function AddModal({ onClose }: { onClose: () => void }) {
  const [d, setD] = useState<ConsentInput>({ address: '', channel: 'all', status: 'dnd', note: '' })
  const [err, setErr] = useState<string | null>(null)
  const save = useAction<unknown, ConsentInput>((b) => post('/soochna/v1/consents', b), { invalidate: ['/soochna/v1/consents'], silent: true, successMessage: 'Saved', onSuccess: onClose })
  const submit = async () => {
    const r = parseAddresses(d.address)
    if (r.valid.length !== 1) return setErr('Enter one mobile number or e-mail address.')
    setErr(null)
    try { await save.mutateAsync({ ...d, address: r.valid[0], note: d.note?.trim() || undefined }) } catch (e) { setErr(errorMessage(e)) }
  }
  return (
    <Modal open onClose={onClose} title="Do not message" footer={<><Button variant="secondary" onClick={onClose}>Cancel</Button><Button loading={save.isPending} onClick={submit}>Save</Button></>}>
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Mobile or e-mail" className="sm:col-span-2"><Input value={d.address} onChange={(e) => setD({ ...d, address: e.target.value })} autoFocus /></Field>
        <Field label="Channel"><Select value={d.channel} onChange={(e) => setD({ ...d, channel: e.target.value as ConsentInput['channel'] })}>{['all', 'whatsapp', 'sms', 'email'].map((c) => <option key={c} value={c}>{c}</option>)}</Select></Field>
        <Field label="Status"><Select value={d.status} onChange={(e) => setD({ ...d, status: e.target.value as ConsentStatus })}><option value="dnd">Do not message (incl. promotions)</option><option value="opted_out">Opted out</option><option value="opted_in">Opted in (promotions allowed)</option></Select></Field>
        <Field label="Note" className="sm:col-span-2"><Input value={d.note ?? ''} onChange={(e) => setD({ ...d, note: e.target.value })} placeholder="Asked at the counter on …" /></Field>
      </div>
      {err && <div className="mt-4"><ErrorBox message={err} /></div>}
    </Modal>
  )
}

function BulkModal({ onClose }: { onClose: () => void }) {
  const invalidate = useInvalidate()
  const [text, setText] = useState('')
  const [channel, setChannel] = useState<ConsentInput['channel']>('all')
  const [busy, setBusy] = useState(false)
  const [done, setDone] = useState<{ ok: number; failed: string[] } | null>(null)
  const parsed = parseAddresses(text)
  const run = async () => {
    setBusy(true)
    let ok = 0
    const failed: string[] = []
    for (const address of parsed.valid) {
      try { await post('/soochna/v1/consents', { address, channel, status: 'dnd', note: 'bulk DND' }); ok++ } catch (e) { failed.push(`${address}: ${errorMessage(e)}`) }
    }
    setBusy(false)
    setDone({ ok, failed })
    invalidate('/soochna/v1/consents')
  }
  return (
    <Modal open onClose={onClose} title="Bulk do-not-message"
      footer={done ? <Button onClick={onClose}>Done</Button> : <><Button variant="secondary" onClick={onClose}>Cancel</Button><Button loading={busy} disabled={parsed.valid.length === 0} onClick={run}>Mark {parsed.valid.length} address{parsed.valid.length === 1 ? '' : 'es'}</Button></>}>
      {done ? (
        <div className="space-y-2 text-sm"><p className="font-semibold text-emerald-700">{done.ok} marked do-not-message.</p>{done.failed.length > 0 && <ErrorBox message={done.failed.slice(0, 5).join(' · ')} />}</div>
      ) : (
        <div className="space-y-4">
          <Field label="Phones or e-mails" hint="One per line or separated by commas — paste a column from Excel"><Textarea rows={8} value={text} onChange={(e) => setText(e.target.value)} className="font-mono text-xs" /></Field>
          <Field label="Channel"><Select value={channel} onChange={(e) => setChannel(e.target.value as ConsentInput['channel'])}>{['all', 'whatsapp', 'sms', 'email'].map((c) => <option key={c} value={c}>{c}</option>)}</Select></Field>
          <p className="text-xs text-slate-500">{parsed.valid.length} valid{parsed.invalid.length > 0 && <span className="text-rose-600"> · {parsed.invalid.length} not understood: {parsed.invalid.slice(0, 5).join(', ')}</span>}</p>
        </div>
      )}
    </Modal>
  )
}
