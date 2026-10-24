'use client'

import { useEffect, useState } from 'react'
import Link from 'next/link'
import { useRouter } from 'next/navigation'
import { useQuery } from '@tanstack/react-query'
import { Printer, Tag } from 'lucide-react'
import { Button, Empty, ErrorBox, Field, PageHeader, Select, Skeleton, Toggle } from '@/components/ui'
import { post } from '@/lib/api'
import { readLabelJob, type LabelJob } from '@/lib/stock/handoff'
import type { Label, LabelRequest } from '@/lib/types.stock'
import { LabelSheet } from '@/components/stock/LabelSheet'

/** Sticker sheet print view. The job (items, copies, layout) comes from sessionStorage via LabelJobModal. */
export default function LabelsPage() {
  const router = useRouter()
  const [job, setJob] = useState<LabelJob | null | undefined>(undefined)
  const [columns, setColumns] = useState(4)
  const [showPrice, setShowPrice] = useState(true)
  useEffect(() => {
    const j = readLabelJob()
    setJob(j)
    if (j) { setColumns(j.columns); setShowPrice(j.show_price) }
  }, [])

  const labels = useQuery<{ labels: Label[] }>({
    queryKey: ['/bhandar/v1/labels', job?.items, showPrice],
    queryFn: () => post<{ labels: Label[] }>('/bhandar/v1/labels', { items: job!.items, show_price: showPrice } satisfies LabelRequest),
    enabled: !!job,
    staleTime: 5 * 60_000,
  })
  const total = labels.data?.labels.reduce((a, l) => a + Math.max(1, l.copies), 0) ?? 0

  if (job === null) {
    return <Empty icon={Tag} title="Nothing to print" text="Select items in the list and choose Print labels." action={<Link href="/app/items"><Button>Back to items</Button></Link>} />
  }

  return (
    <>
      <div className="no-print">
        <PageHeader title="Print labels" subtitle={total ? `${total} sticker${total === 1 ? '' : 's'} · A4 sheet` : undefined} back={() => router.push('/app/items')}
          actions={<Button icon={Printer} onClick={() => window.print()} disabled={!labels.data}>Print</Button>} />
        <div className="mb-4 flex flex-wrap items-end gap-3">
          <Field label="Columns"><Select value={columns} onChange={(e) => setColumns(Number(e.target.value))} className="w-24">{[2, 3, 4, 5, 6].map((n) => <option key={n} value={n}>{n}</option>)}</Select></Field>
          <div className="pb-2.5"><Toggle checked={showPrice} onChange={setShowPrice} label="Show MRP" /></div>
          <p className="pb-2.5 text-xs text-slate-500">Use plain A4 label paper; set the printer margins to none and scale to 100 %.</p>
        </div>
        {labels.error && <div className="mb-4"><ErrorBox message={(labels.error as Error).message} retry={() => labels.refetch()} /></div>}
      </div>
      {labels.isLoading || job === undefined ? <Skeleton rows={8} /> : labels.data ? <LabelSheet labels={labels.data.labels} columns={columns} showPrice={showPrice} /> : null}
    </>
  )
}
