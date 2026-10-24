'use client'

import { useRouter } from 'next/navigation'
import { Layers } from 'lucide-react'
import { Empty, PageHeader } from '@/components/ui'
import { usePerm } from '@/lib/hooks'
import { PERMS } from '@/lib/rbac'
import { BomEditor } from '@/components/production/BomEditor'

export default function NewBomPage() {
  const router = useRouter()
  const canDo = usePerm()
  if (!canDo(PERMS.bomManage)) return <Empty icon={Layers} title="No access" text="Only store managers and owners can create bills of materials." />
  return (
    <>
      <PageHeader title="New bill of materials" subtitle="Saved as a draft; activate it when the recipe is final." back={() => router.push('/app/production')} />
      <BomEditor onSaved={(b) => router.push(`/app/production/boms/${b.id}`)} onCancel={() => router.push('/app/production')} />
    </>
  )
}
