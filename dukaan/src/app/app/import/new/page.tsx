'use client'

import { Suspense } from 'react'
import { useRouter, useSearchParams } from 'next/navigation'
import { Upload } from 'lucide-react'
import { Empty, PageHeader } from '@/components/ui'
import { usePerm } from '@/lib/hooks'
import { PERMS } from '@/lib/rbac'
import { ImportWizard } from '@/components/import/Wizard'

export default function NewImportPage() {
  return <Suspense><NewImportInner /></Suspense>
}

function NewImportInner() {
  const router = useRouter()
  const canDo = usePerm()
  const params = useSearchParams()
  const job = params.get('job') ?? undefined
  if (!canDo(PERMS.importRun)) return <Empty icon={Upload} title="Import data" text="Owners and accountants can import data." />
  return (
    <>
      <PageHeader title={job ? 'Continue import' : 'New import'} back={() => router.push('/app/import')} />
      <div className="mx-auto max-w-5xl"><ImportWizard key={job ?? 'new'} jobId={job} /></div>
    </>
  )
}
