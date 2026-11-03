'use client'

/**
 * HSN / SAC rate master import (kar POST /v1/hsn/import, multipart "file"): effective-dated rows,
 * upserted on (code, valid_from, condition); bad rows are reported by line and the rest imported.
 * The master is shared by every business, so kar allows it only for the platform operator's tenants.
 */
import { useRef, useState } from 'react'
import { FileSpreadsheet, Upload } from 'lucide-react'
import { Button, Card, ErrorBox, Table, useToast } from '@/components/ui'
import { api, errorMessage } from '@/lib/api'
import { downloadFile } from '@/lib/hooks'
import type { HsnImportReport } from '@/lib/types.kar'
import { useAction } from '@/components/ledger/useAction'

const SAMPLE = 'code,description,rate_bp,cess_bp,cess_per_unit_paise,valid_from,valid_to,condition\r\n190590,Biscuits and cookies,500,0,0,2025-09-22,,\r\n'

export function HsnImport() {
  const toast = useToast()
  const input = useRef<HTMLInputElement>(null)
  const [name, setName] = useState('')
  const [report, setReport] = useState<HsnImportReport | null>(null)
  const run = useAction<HsnImportReport, File>((f) => {
    const fd = new FormData()
    fd.append('file', f, f.name)
    return api<HsnImportReport>('/kar/v1/hsn/import', { method: 'POST', body: fd })
  }, {
    invalidate: ['/kar/v1/hsn'], silent: true,
    onSuccess: (r) => { setReport(r); toast(`${r.inserted + r.updated} HSN rows imported${r.rejected ? `, ${r.rejected} rejected` : ''}`, r.rejected ? 'info' : 'success') },
  })
  const pick = (f: File | undefined) => {
    if (!f) return
    setName(f.name)
    setReport(null)
    run.mutate(f)
    if (input.current) input.current.value = ''
  }
  const errors = report?.errors ?? []

  return (
    <Card title="HSN / SAC rate import" actions={<Button size="sm" variant="ghost" icon={FileSpreadsheet} onClick={() => downloadFile('hsn-rates-template.csv', SAMPLE, 'text/csv')}>Template</Button>}>
      <p className="text-sm text-slate-600">Load rates from the CBIC notifications as CSV: <span className="font-mono text-xs">code, description, rate_bp, cess_bp, cess_per_unit_paise, valid_from, valid_to, condition</span>. Codes are 4, 6 or 8 digits, rates in basis points (500 = 5 %) and must be a slab in force on <span className="font-mono text-xs">valid_from</span>. A new <span className="font-mono text-xs">valid_from</span> closes the previous open row the day before. Maapan never invents rates; the starter rows are marked unverified.</p>
      <div className="mt-4 flex flex-wrap items-center gap-3">
        <input ref={input} type="file" accept=".csv,text/csv" className="hidden" onChange={(e) => pick(e.target.files?.[0])} aria-label="HSN rates CSV file" />
        <Button icon={Upload} loading={run.isPending} onClick={() => input.current?.click()}>Import CSV</Button>
        {name && <span className="text-xs text-slate-500">{name}</span>}
      </div>
      {run.error && <div className="mt-3"><ErrorBox message={errorMessage(run.error)} /></div>}
      {report && (
        <div className="mt-4 space-y-3">
          <div className="grid grid-cols-3 gap-2 sm:grid-cols-6">
            {([['Rows', report.rows], ['Inserted', report.inserted], ['Updated', report.updated], ['Closed', report.closed], ['Rejected', report.rejected]] as const).map(([k, v]) => (
              <div key={k} className={`rounded-xl px-3 py-2 ${k === 'Rejected' && v > 0 ? 'bg-rose-50 text-rose-700' : 'bg-slate-50'}`}><div className="text-[11px] text-slate-500">{k}</div><div className="text-lg font-bold">{v}</div></div>
            ))}
          </div>
          {errors.length > 0 && (
            <div className="max-h-72 overflow-y-auto rounded-xl ring-1 ring-slate-200">
              <Table>
                <thead className="bg-slate-50/60"><tr><th className="th w-20">Line</th><th className="th w-28">Code</th><th className="th">Problem</th></tr></thead>
                <tbody className="divide-y divide-slate-100">
                  {errors.map((e, i) => <tr key={i}><td className="td num">{e.line}</td><td className="td font-mono text-xs">{e.code || '—'}</td><td className="td text-sm text-rose-700">{e.error}</td></tr>)}
                </tbody>
              </Table>
            </div>
          )}
          {errors.length > 0 && <Button size="sm" variant="ghost" onClick={() => downloadFile('hsn-import-errors.csv', 'line,code,error\r\n' + errors.map((e) => `${e.line},${e.code ?? ''},"${e.error.replace(/"/g, '""')}"`).join('\r\n') + '\r\n', 'text/csv')}>Download the rejected rows</Button>}
        </div>
      )}
    </Card>
  )
}
