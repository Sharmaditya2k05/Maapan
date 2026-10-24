'use client'

import { useParams, useRouter } from 'next/navigation'
import { Lock } from 'lucide-react'
import { Empty, PageHeader, QueryState } from '@/components/ui'
import { useGet, usePerm } from '@/lib/hooks'
import { PERMS } from '@/lib/rbac'
import type { ItemFull } from '@/lib/types.stock'
import { ItemForm } from '@/components/stock/ItemForm'

export default function EditItemPage() {
  const { id } = useParams<{ id: string }>()
  const router = useRouter()
  const canDo = usePerm()
  const item = useGet<ItemFull>(canDo(PERMS.itemManage) ? `/bhandar/v1/items/${id}` : null)
  if (!canDo(PERMS.itemManage)) {
    return <Empty icon={Lock} title="No access" text="Only owners and store managers can edit items." />
  }
  const back = () => router.push(`/app/items/${id}`)
  return (
    <>
      <PageHeader title={item.data ? `Edit · ${item.data.name}` : 'Edit item'} back={back} />
      <div className="mx-auto max-w-4xl">
        <QueryState isLoading={item.isLoading} error={item.error} data={item.data} retry={() => item.refetch()}>
          {(it) => <ItemForm initial={it} onSaved={back} onCancel={back} />}
        </QueryState>
      </div>
    </>
  )
}
