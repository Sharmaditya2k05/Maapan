'use client'

/**
 * /print/invoice/[id]?layout=a4|thermal&w=58&auto=1 — renders only the document
 * (no app shell; this route is outside /app) and prints itself when auto=1.
 * Opened in a new tab by detail pages and in a hidden iframe by the counter.
 */
import { Suspense, useEffect, useRef, useState } from 'react'
import { useParams, useSearchParams } from 'next/navigation'
import { Printer } from 'lucide-react'
import { Button, ErrorBox, Spinner } from '@/components/ui'
import { InvoiceA4 } from '@/components/print/InvoiceA4'
import { ThermalReceipt } from '@/components/print/ThermalReceipt'
import { useGet, useSessionUser } from '@/lib/hooks'
import { session } from '@/lib/session'
import { refreshSessionUser } from '@/lib/pehchaan'
import { usePrefs } from '@/lib/prefs'
import type { Party } from '@/lib/types'
import type { InvoiceFull } from '@/lib/types.pos'
import { errorMessage } from '@/lib/api'

export default function PrintInvoicePage() {
  return (
    <Suspense fallback={<div className="grid min-h-dvh place-items-center"><Spinner /></div>}>
      <PrintInvoice />
    </Suspense>
  )
}

function PrintInvoice() {
  const { id } = useParams<{ id: string }>()
  const sp = useSearchParams()
  const [prefs] = usePrefs()
  const [layout, setLayout] = useState<'a4' | 'thermal'>(sp.get('layout') === 'a4' ? 'a4' : sp.get('layout') === 'thermal' ? 'thermal' : prefs.printer === 'a4' ? 'a4' : 'thermal')
  const width: 58 | 80 = sp.get('w') === '58' || (!sp.get('w') && prefs.printer === 'thermal58') ? 58 : 80
  const auto = sp.get('auto') === '1'
  const user = useSessionUser()
  const [sessionErr, setSessionErr] = useState<string | null>(null)

  // A fresh tab has tokens but no cached user: load it so the business name prints.
  useEffect(() => {
    if (session.user()) return
    if (!session.accessToken() && !session.refreshToken()) { setSessionErr('Sign in to print this document.'); return }
    refreshSessionUser().catch((e) => setSessionErr(errorMessage(e)))
  }, [])

  const inv = useGet<InvoiceFull>(id ? `/aadesh/v1/invoices/${id}` : null)
  const party = useGet<Party>(inv.data?.party_id ? `/bahi/v1/parties/${inv.data.party_id}` : null)
  const ref = useGet<InvoiceFull>(inv.data?.ref_invoice_id ? `/aadesh/v1/invoices/${inv.data.ref_invoice_id}` : null)
  const store = user?.stores.find((s) => s.id === inv.data?.store_id)

  const printed = useRef(false)
  useEffect(() => {
    if (!auto || !inv.data || printed.current) return
    if (inv.data.party_id && party.isLoading) return
    printed.current = true
    const t = setTimeout(() => window.print(), 250)
    return () => clearTimeout(t)
  }, [auto, inv.data, party.isLoading])

  useEffect(() => { document.title = inv.data ? `${inv.data.number} · Maapan` : 'Print · Maapan' }, [inv.data])

  if (sessionErr) return <div className="p-6"><ErrorBox message={sessionErr} /></div>
  if (inv.isLoading) return <div className="grid min-h-dvh place-items-center text-sm text-slate-500"><div className="flex items-center gap-2"><Spinner /> Preparing document…</div></div>
  if (inv.error || !inv.data) return <div className="p-6"><ErrorBox message={errorMessage(inv.error) || 'Invoice not found'} retry={() => inv.refetch()} /></div>
  const doc = inv.data

  return (
    <div className="min-h-dvh bg-slate-100 print:bg-white">
      <div className="no-print sticky top-0 z-10 flex flex-wrap items-center gap-2 border-b border-slate-200 bg-white/90 px-4 py-2 backdrop-blur">
        <span className="mr-auto text-sm font-bold">{doc.number}</span>
        <div className="inline-flex rounded-xl bg-slate-100 p-1 text-xs font-semibold">
          <button type="button" onClick={() => setLayout('thermal')} className={`rounded-lg px-3 py-1.5 cursor-pointer ${layout === 'thermal' ? 'bg-white shadow-sm' : 'text-slate-500'}`}>Receipt</button>
          <button type="button" onClick={() => setLayout('a4')} className={`rounded-lg px-3 py-1.5 cursor-pointer ${layout === 'a4' ? 'bg-white shadow-sm' : 'text-slate-500'}`}>A4</button>
        </div>
        <Button icon={Printer} size="sm" onClick={() => window.print()}>Print</Button>
      </div>
      <div className="p-4 print:p-0">
        <div className={`print-root mx-auto bg-white shadow-sm print:shadow-none ${layout === 'a4' ? 'print-a4 max-w-[210mm] p-[12mm]' : `print-thermal ${width === 58 ? 'thermal-58 max-w-[58mm]' : 'max-w-[80mm]'} p-[4mm]`}`}>
          {layout === 'a4'
            ? <InvoiceA4 invoice={doc} store={store} businessName={user?.business_name} party={party.data} refNumber={ref.data?.number} />
            : <ThermalReceipt invoice={doc} store={store} businessName={user?.business_name} width={width} cashier={user?.name} />}
        </div>
      </div>
    </div>
  )
}
