'use client'

import { useState } from 'react'
import { Hash } from 'lucide-react'
import { Badge, Button, Card, Empty, ErrorBox, Field, Input, Modal, QueryState, Table, useToast } from '@/components/ui'
import { fyStartIST, useGet, usePerm, useSave } from '@/lib/hooks'
import { errorMessage } from '@/lib/api'
import { PERMS } from '@/lib/rbac'
import { validateNumberPrefix } from '@/lib/stock/ean'
import type { DocType, ListResponse, Store } from '@/lib/types'
import type { NumberingResponse, NumberingSetting } from '@/lib/types.stock'

const DOC_TYPES: { value: DocType; label: string }[] = [
  { value: 'sale', label: 'Sales invoice' }, { value: 'purchase', label: 'Purchase bill' },
  { value: 'sale_return', label: 'Credit note' }, { value: 'purchase_return', label: 'Debit note' },
]

function fyLabel() {
  const y = Number(fyStartIST().slice(0, 4))
  return `${y}-${String(y + 1).slice(2)}`
}

export function NumberingSection() {
  const canDo = usePerm()
  const toast = useToast()
  const numbering = useGet<NumberingResponse>('/aadesh/v1/settings/numbering')
  const stores = useGet<ListResponse<Store>>('/pehchaan/v1/stores')
  const [edit, setEdit] = useState<{ store: Store; doc: DocType; prefix: string } | null>(null)
  const [err, setErr] = useState('')
  const save = useSave<NumberingSetting, NumberingSetting>('/aadesh/v1/settings/numbering', { method: 'PUT', invalidate: ['/aadesh/v1/settings/numbering'], silent: true })
  const canEdit = canDo(PERMS.settingsManage)
  const defaults = numbering.data?.default_prefixes
  const custom = (storeId: string, doc: DocType) => numbering.data?.stores.find((s) => s.store_id === storeId && s.doc_type === doc)?.prefix

  const submit = async (prefix: string) => {
    if (!edit) return
    const p = prefix.trim().toUpperCase()
    const e = validateNumberPrefix(p)
    if (e) return setErr(e)
    try {
      await save.mutateAsync({ store_id: edit.store.id, doc_type: edit.doc, prefix: p })
      toast(`${edit.store.name}: ${DOC_TYPES.find((d) => d.value === edit.doc)?.label} numbers now start with ${p}`)
      setEdit(null)
    } catch (x) { setErr(errorMessage(x)) }
  }

  return (
    <Card title="Invoice numbering" padded={false}>
      <div className="border-b border-slate-100 px-4 py-3 text-sm text-slate-600 sm:px-5">
        Numbers look like <span className="font-mono font-semibold text-ink">INV/{fyLabel()}/00001</span>: a prefix, the financial year, then a running number per store, per GSTIN and per document type. Prefixes are 1–4 characters (A–Z, 0–9, hyphen). Change them before billing starts for the year; GST rules require a continuous series.
      </div>
      <QueryState isLoading={numbering.isLoading || stores.isLoading} error={numbering.error ?? stores.error} data={numbering.data && stores.data ? stores.data.items : undefined} retry={() => { void numbering.refetch(); void stores.refetch() }}
        empty={<Empty icon={Hash} title="No stores" />}>
        {(rows) => (
          <Table>
            <thead className="bg-slate-50/60"><tr><th className="th">Store</th>{DOC_TYPES.map((d) => <th key={d.value} className="th">{d.label}<span className="block text-[10px] font-normal normal-case text-slate-400">default {defaults?.[d.value]}</span></th>)}</tr></thead>
            <tbody className="divide-y divide-slate-100">
              {rows.map((s) => (
                <tr key={s.id} className="hover:bg-slate-50/60">
                  <td className="td"><div className="font-semibold">{s.name}</div><div className="font-mono text-xs text-slate-500">{s.gstin || 'unregistered'}</div></td>
                  {DOC_TYPES.map((d) => {
                    const c = custom(s.id, d.value)
                    return (
                      <td key={d.value} className="td">
                        <button type="button" disabled={!canEdit} onClick={() => { setErr(''); setEdit({ store: s, doc: d.value, prefix: c ?? defaults?.[d.value] ?? '' }) }}
                          className={`inline-flex items-center gap-2 rounded-lg px-2 py-1 font-mono text-sm ${canEdit ? 'hover:bg-slate-100 cursor-pointer' : ''}`} aria-label={`Prefix for ${d.label} at ${s.name}`}>
                          <span className={c ? 'font-bold text-ink' : 'text-slate-500'}>{c ?? defaults?.[d.value]}</span>{c ? <Badge tone="brand">custom</Badge> : null}
                        </button>
                      </td>
                    )
                  })}
                </tr>
              ))}
            </tbody>
          </Table>
        )}
      </QueryState>
      {edit && <PrefixModal edit={edit} defaultPrefix={defaults?.[edit.doc] ?? ''} err={err} loading={save.isPending} onClose={() => setEdit(null)} onSave={submit} />}
    </Card>
  )
}

function PrefixModal({ edit, defaultPrefix, err, loading, onClose, onSave }: { edit: { store: Store; doc: DocType; prefix: string }; defaultPrefix: string; err: string; loading: boolean; onClose: () => void; onSave: (p: string) => void }) {
  const [prefix, setPrefix] = useState(edit.prefix)
  const local = validateNumberPrefix(prefix)
  return (
    <Modal open onClose={onClose} title={`${DOC_TYPES.find((d) => d.value === edit.doc)?.label} prefix · ${edit.store.name}`}
      footer={<><Button variant="secondary" onClick={onClose}>Cancel</Button>{prefix !== defaultPrefix && <Button variant="ghost" onClick={() => onSave(defaultPrefix)} disabled={loading}>Use default ({defaultPrefix})</Button>}<Button onClick={() => onSave(prefix)} loading={loading} disabled={!!local}>Save</Button></>}>
      <Field label="Prefix" error={prefix && local ? local : undefined} hint={`Preview: ${(prefix || defaultPrefix).toUpperCase()}/${fyLabel()}/00001`}>
        <Input value={prefix} onChange={(e) => setPrefix(e.target.value.toUpperCase().slice(0, 4))} className="font-mono uppercase" maxLength={4} autoFocus onKeyDown={(e) => e.key === 'Enter' && !local && onSave(prefix)} />
      </Field>
      {err && <div className="mt-4"><ErrorBox message={err} /></div>}
    </Modal>
  )
}
