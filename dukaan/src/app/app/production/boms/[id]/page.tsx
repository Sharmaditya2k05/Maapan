'use client'

import { useState } from 'react'
import { useParams, useRouter } from 'next/navigation'
import { Archive, CheckCircle2, Copy, Factory, Layers, Pencil } from 'lucide-react'
import { Badge, Button, Confirm, Empty, PageHeader, QueryState, Tabs, useToast } from '@/components/ui'
import { fmtDate, fmtQty, useGet, usePerm, useSave } from '@/lib/hooks'
import { PERMS } from '@/lib/rbac'
import type { Bom } from '@/lib/types.more'
import { BomEditor } from '@/components/production/BomEditor'
import { BomCostView, BomDetails, BomExplodeView, bomTone } from '@/components/production/BomViews'
import { PlanRunModal } from '@/components/production/runs'

type View = 'details' | 'explode' | 'cost'

export default function BomPage() {
  const { id } = useParams<{ id: string }>()
  const router = useRouter()
  const canDo = usePerm()
  const toast = useToast()
  const canWrite = canDo(PERMS.bomManage)
  const bom = useGet<Bom>(canWrite || canDo(PERMS.reportRead) ? `/nirmaan/v1/boms/${id}` : null)
  const [view, setView] = useState<View>('details')
  const [editing, setEditing] = useState(false)
  const [confirm, setConfirm] = useState<'activate' | 'archive' | null>(null)
  const [plan, setPlan] = useState(false)

  const inv = ['/nirmaan/v1/boms', `/nirmaan/v1/boms/${id}`]
  const activate = useSave<Bom>(`/nirmaan/v1/boms/${id}/activate`, { invalidate: inv, onSuccess: () => { toast('BOM activated'); setConfirm(null) } })
  const archive = useSave<Bom>(`/nirmaan/v1/boms/${id}/archive`, { invalidate: inv, onSuccess: () => { toast('BOM archived'); setConfirm(null) } })
  const newVersion = useSave<Bom>(`/nirmaan/v1/boms/${id}/new-version`, { invalidate: inv, onSuccess: (b) => { toast(`Draft v${b.version} created`); router.push(`/app/production/boms/${b.id}`) } })

  if (!canWrite && !canDo(PERMS.reportRead)) return <Empty icon={Layers} title="No access" text="Only store managers and owners can view bills of materials." />

  return (
    <QueryState isLoading={bom.isLoading} error={bom.error} data={bom.data} retry={() => bom.refetch()}>
      {(b) => (
        <>
          <PageHeader
            back={() => router.push('/app/production')}
            title={<span className="inline-flex flex-wrap items-center gap-2">{b.item_name} <Badge tone={bomTone(b.status)}>{b.status}</Badge></span>}
            subtitle={`Version ${b.version} · makes ${fmtQty(b.output_qty)} ${b.unit} per batch · ${b.components.length} components · updated ${fmtDate(b.updated_at)}`}
            actions={canWrite && !editing && (
              <>
                {b.status === 'draft' && <Button variant="secondary" icon={Pencil} onClick={() => setEditing(true)}>Edit</Button>}
                {b.status === 'draft' && <Button icon={CheckCircle2} onClick={() => setConfirm('activate')}>Activate</Button>}
                {b.status === 'active' && <Button icon={Factory} onClick={() => setPlan(true)}>Plan run</Button>}
                <Button variant="secondary" icon={Copy} loading={newVersion.isPending} onClick={() => newVersion.mutate(undefined)}>New version</Button>
                {b.status !== 'archived' && <Button variant="ghost" icon={Archive} onClick={() => setConfirm('archive')}>Archive</Button>}
              </>
            )} />

          {editing ? (
            <BomEditor initial={b} onSaved={() => { setEditing(false); void bom.refetch() }} onCancel={() => setEditing(false)} />
          ) : (
            <>
              <div className="mb-4"><Tabs value={view} onChange={setView} options={[{ value: 'details', label: 'Details' }, { value: 'explode', label: 'Explode' }, { value: 'cost', label: 'Cost' }]} /></div>
              {view === 'details' && <BomDetails bom={b} />}
              {view === 'explode' && <BomExplodeView bom={b} />}
              {view === 'cost' && <BomCostView bom={b} />}
            </>
          )}

          <Confirm open={confirm === 'activate'} onClose={() => setConfirm(null)} onConfirm={() => activate.mutate(undefined)} loading={activate.isPending} title="Activate this BOM?"
            text={`Version ${b.version} becomes the recipe used for new production runs of ${b.item_name}. Any currently active version is archived. The server rejects it if a sub-assembly loop would be created.`} confirmLabel="Activate" />
          <Confirm open={confirm === 'archive'} onClose={() => setConfirm(null)} onConfirm={() => archive.mutate(undefined)} loading={archive.isPending} danger title="Archive this BOM?"
            text="Archived BOMs cannot be edited or produced. Existing runs keep their history. Create a new version to bring the recipe back." confirmLabel="Archive" />
          <PlanRunModal open={plan} onClose={() => setPlan(false)} bomId={b.id} />
        </>
      )}
    </QueryState>
  )
}
