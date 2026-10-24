'use client'

import { useRouter } from 'next/navigation'
import { Lock } from 'lucide-react'
import { Empty, PageHeader } from '@/components/ui'
import { usePerm } from '@/lib/hooks'
import { PERMS } from '@/lib/rbac'
import { ItemForm } from '@/components/stock/ItemForm'

export default function NewItemPage() {
  const router = useRouter()
  const canDo = usePerm()
  if (!canDo(PERMS.itemManage)) {
    return <Empty icon={Lock} title="No access" text="Only owners and store managers can add items." />
  }
  return (
    <>
      <PageHeader title="New item" subtitle="Leave the barcode empty and an in-store EAN-13 is generated" back={() => router.push('/app/items')} />
      <div className="mx-auto max-w-4xl">
        <ItemForm onSaved={(it) => router.replace(`/app/items/${it.id}`)} onCancel={() => router.push('/app/items')} />
      </div>
    </>
  )
}
