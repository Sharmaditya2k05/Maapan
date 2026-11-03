'use client'

/** Tally Prime "Import Data" XML of the books (bahi GET /v1/export/tally?from=&to=&company=). */
import { useState } from 'react'
import { Download } from 'lucide-react'
import { Button, Card, ErrorBox, Field, Input } from '@/components/ui'
import { api, errorMessage } from '@/lib/api'
import { downloadFile, fyStartIST, todayIST, usePerm, useSessionUser } from '@/lib/hooks'
import { PERMS } from '@/lib/rbac'
import { NoPermission } from '@/components/ledger/bits'
import { useAction } from '@/components/ledger/useAction'

export function TallyExportSection() {
  const canDo = usePerm()
  const user = useSessionUser()
  const [from, setFrom] = useState(fyStartIST())
  const [to, setTo] = useState(todayIST())
  const [company, setCompany] = useState(user?.business_name ?? '')
  const dl = useAction<Blob>(() => api<Blob>('/bahi/v1/export/tally', { method: 'GET', raw: true, query: { from, to, company: company.trim() || undefined } }), {
    silent: true, onSuccess: (b) => downloadFile(`tally-${from}-${to}.xml`, b),
  })
  if (!canDo(PERMS.reportRead)) return <Card><NoPermission what="exports" /></Card>
  return (
    <Card title="Export to Tally Prime">
      <p className="mb-4 text-sm text-slate-600">Ledgers used in the period (mapped to Tally’s primary groups, with opening balances, GSTIN / PAN and bill-wise details) followed by every voucher, with bill allocations as New Ref / Agst Ref / On Account. In Tally: Gateway → Import → Data → choose this file.</p>
      <div className="grid gap-4 sm:grid-cols-3">
        <Field label="From"><Input type="date" value={from} onChange={(e) => setFrom(e.target.value)} /></Field>
        <Field label="To"><Input type="date" value={to} max={todayIST()} onChange={(e) => setTo(e.target.value)} /></Field>
        <Field label="Company name in Tally" hint="Must match the company you import into"><Input value={company} onChange={(e) => setCompany(e.target.value)} /></Field>
      </div>
      {dl.error && <div className="mt-4"><ErrorBox message={errorMessage(dl.error)} /></div>}
      <div className="mt-4 flex justify-end"><Button icon={Download} loading={dl.isPending} disabled={!from || !to || from > to} onClick={() => dl.mutate()}>Download XML</Button></div>
    </Card>
  )
}
