'use client'

import { useState } from 'react'
import { Plus } from 'lucide-react'
import { Button, ErrorBox, Field, Input, Modal, QueryState } from '@/components/ui'
import { useGet, useSave } from '@/lib/hooks'
import { errorMessage } from '@/lib/api'
import type { Unit } from '@/lib/types.stock'

/** Units of measure: list + add (POST /bhandar/v1/units upserts by code). */
export function UnitsModal({ open, onClose }: { open: boolean; onClose: () => void }) {
  const units = useGet<{ units: Unit[] }>(open ? '/bhandar/v1/units' : null)
  const [code, setCode] = useState('')
  const [name, setName] = useState('')
  const [err, setErr] = useState('')
  const save = useSave<Unit, Unit>('/bhandar/v1/units', { invalidate: ['/bhandar/v1/units'], silent: true })

  const add = async () => {
    const c = code.trim().toUpperCase()
    if (!c || c.length > 12 || !name.trim()) return setErr('A unit needs a code (max 12 characters) and a name.')
    setErr('')
    try {
      await save.mutateAsync({ code: c, name: name.trim() })
      setCode(''); setName('')
    } catch (e) { setErr(errorMessage(e)) }
  }

  return (
    <Modal open={open} onClose={onClose} title="Units of measure" footer={<Button variant="secondary" onClick={onClose}>Done</Button>}>
      <div className="mb-4 grid grid-cols-[1fr_2fr_auto] items-end gap-2">
        <Field label="Code"><Input value={code} onChange={(e) => setCode(e.target.value.toUpperCase())} placeholder="BOX" maxLength={12} /></Field>
        <Field label="Name"><Input value={name} onChange={(e) => setName(e.target.value)} placeholder="Box" onKeyDown={(e) => e.key === 'Enter' && add()} /></Field>
        <Button icon={Plus} onClick={add} loading={save.isPending} aria-label="Add unit">Add</Button>
      </div>
      {err && <div className="mb-3"><ErrorBox message={err} /></div>}
      <QueryState isLoading={units.isLoading} error={units.error} data={units.data?.units} retry={() => units.refetch()}>
        {(list) => (
          <ul className="divide-y divide-slate-100 rounded-xl border border-slate-100">
            {list.map((u) => <li key={u.code} className="flex items-center justify-between px-4 py-2 text-sm"><span className="font-mono font-semibold">{u.code}</span><span className="text-slate-600">{u.name}</span></li>)}
          </ul>
        )}
      </QueryState>
      <p className="mt-3 text-xs text-slate-500">Re-adding an existing code renames it. Units cannot be deleted once items use them.</p>
    </Modal>
  )
}
