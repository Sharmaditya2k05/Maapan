'use client'

/**
 * Scan a supplier bill: photo/PDF → POST /buddhi/v1/scan/purchase-bill (multipart "file") →
 * review the AI draft → hand it to the purchase screen via sessionStorage (PURCHASE_DRAFT_KEY).
 */
import { useEffect, useRef, useState } from 'react'
import Image from 'next/image'
import { useRouter } from 'next/navigation'
import { AlertTriangle, Camera, CheckCircle2, FileText, Link2Off, ShoppingCart, Upload, X } from 'lucide-react'
import { Badge, Button, Card, ErrorBox, Money, Table, useToast } from '@/components/ui'
import { api, errorMessage } from '@/lib/api'
import { fmtDate, fmtQty, usePerm } from '@/lib/hooks'
import { formatRate } from '@/lib/money'
import { PERMS } from '@/lib/rbac'
import { PURCHASE_DRAFT_KEY, type PurchaseDraft, type ScanDraftLine, type ScanResponse } from '@/lib/types.more'
import { AiUnavailable, UsageChip, isAiUnavailable } from './common'

const MAX = 10 * 1024 * 1024

export function ScanTab() {
  const router = useRouter()
  const toast = useToast()
  const canDo = usePerm()
  const [file, setFile] = useState<File | null>(null)
  const [url, setUrl] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [unavailable, setUnavailable] = useState<string | null>(null)
  const [res, setRes] = useState<ScanResponse | null>(null)
  const [lines, setLines] = useState<ScanDraftLine[]>([])
  const cameraRef = useRef<HTMLInputElement>(null)
  const fileRef = useRef<HTMLInputElement>(null)

  useEffect(() => () => { if (url) URL.revokeObjectURL(url) }, [url])

  const pick = (f: File | undefined) => {
    if (!f) return
    if (f.size > MAX) return setError('The file is larger than 10 MB. Take a smaller photo or compress the PDF.')
    if (!/^(image\/(jpeg|png|webp|gif)|application\/pdf)$/.test(f.type)) return setError('Use a JPEG, PNG, WebP, GIF or PDF.')
    setError(null); setRes(null); setLines([])
    if (url) URL.revokeObjectURL(url)
    setFile(f)
    setUrl(f.type.startsWith('image/') ? URL.createObjectURL(f) : null)
  }
  const reset = () => { setFile(null); if (url) URL.revokeObjectURL(url); setUrl(null); setRes(null); setLines([]); setError(null) }

  const scan = async () => {
    if (!file) return
    setBusy(true); setError(null)
    try {
      const fd = new FormData()
      fd.set('file', file)
      const r = await api<ScanResponse>('/buddhi/v1/scan/purchase-bill', { method: 'POST', body: fd })
      setRes(r)
      setLines(r.result.draft.lines)
    } catch (e) {
      if (isAiUnavailable(e)) setUnavailable(errorMessage(e))
      else setError(errorMessage(e))
    } finally { setBusy(false) }
  }

  const createBill = () => {
    if (!res) return
    const d = res.result.draft
    const draft: PurchaseDraft = {
      doc_type: 'purchase',
      party_id: d.party_id || undefined,
      supplier_invoice_no: d.supplier_invoice_no || undefined,
      supplier_invoice_date: d.invoice_date || undefined,
      lines: lines.map((l) => ({
        item_id: l.item_id || undefined, name: l.name, hsn: l.hsn ?? '', unit: l.unit || 'PCS', qty: l.qty, unit_price_paise: l.unit_price_paise,
        discount_paise: l.discount_paise, tax_rate_bp: l.tax_rate_bp, cess_rate_bp: 0, price_includes_tax: l.price_includes_tax,
      })),
      notes: [d.notes, res.result.supplier.name ? `Supplier on bill: ${res.result.supplier.name}${res.result.supplier.gstin ? ` (${res.result.supplier.gstin})` : ''}` : ''].filter(Boolean).join(' · '),
    }
    try { sessionStorage.setItem(PURCHASE_DRAFT_KEY, JSON.stringify(draft)) } catch { return toast('Could not hand the draft over (storage blocked).', 'error') }
    router.push('/app/purchases/new')
  }

  if (unavailable) return <AiUnavailable message={unavailable} />
  if (!canDo(PERMS.purchaseManage)) return <AiUnavailable message="Scanning a bill creates a purchase draft, which needs the purchase permission (owners, store managers, accountants)." />

  const r = res?.result
  const conf = r ? Math.round(r.confidence * 100) : 0
  const confTone = conf >= 80 ? 'green' : conf >= 50 ? 'amber' : 'red'

  return (
    <div className="grid gap-4 lg:grid-cols-[360px_1fr]">
      <div className="space-y-3">
        <Card title="Bill photo or PDF">
          <input ref={cameraRef} type="file" accept="image/*" capture="environment" className="sr-only" onChange={(e) => pick(e.target.files?.[0])} aria-label="Take a photo of the bill" />
          <input ref={fileRef} type="file" accept="image/jpeg,image/png,image/webp,image/gif,application/pdf" className="sr-only" onChange={(e) => pick(e.target.files?.[0])} aria-label="Choose a bill file" />
          {file ? (
            <div className="space-y-3">
              <div className="relative aspect-[3/4] w-full overflow-hidden rounded-xl bg-slate-100">
                {url ? <Image src={url} alt={`Preview of ${file.name}`} fill unoptimized className="object-contain" /> : <div className="grid h-full place-items-center text-slate-400"><FileText size={48} /></div>}
                <button type="button" onClick={reset} className="absolute right-2 top-2 grid h-8 w-8 place-items-center rounded-full bg-white/90 text-slate-600 shadow hover:text-ink cursor-pointer" aria-label="Remove file"><X size={16} /></button>
              </div>
              <div className="truncate text-xs text-slate-500">{file.name} · {(file.size / 1024).toFixed(0)} KB</div>
              <Button className="w-full" icon={Upload} loading={busy} onClick={scan} disabled={!!res}>{res ? 'Scanned' : busy ? 'Reading the bill…' : 'Scan with AI'}</Button>
            </div>
          ) : (
            <div className="space-y-2">
              <Button className="w-full" icon={Camera} onClick={() => cameraRef.current?.click()}>Take a photo</Button>
              <Button className="w-full" variant="secondary" icon={Upload} onClick={() => fileRef.current?.click()}>Choose image or PDF</Button>
              <p className="pt-1 text-xs text-slate-500">Flat, well-lit, whole bill in frame. JPEG, PNG, WebP, GIF or PDF up to 10 MB. The AI only drafts — nothing is posted until you save the purchase bill.</p>
            </div>
          )}
        </Card>
        {error && <ErrorBox message={error} />}
      </div>

      <div className="space-y-4">
        {!r ? (
          <Card><div className="flex flex-col items-center py-12 text-center text-slate-500"><FileText size={36} className="mb-3 text-slate-300" /><div className="font-semibold text-ink">The extracted bill appears here</div><p className="mt-1 max-w-sm text-sm">Supplier, invoice number, date and every item line — matched to your item list with confidence flags.</p></div></Card>
        ) : (
          <>
            <Card>
              <div className="flex flex-wrap items-start justify-between gap-3">
                <div>
                  <div className="text-xs font-semibold uppercase tracking-wide text-slate-500">Supplier on bill</div>
                  <div className="text-lg font-bold">{r.supplier.name || <span className="text-slate-400">not found</span>}</div>
                  <div className="mt-1 flex flex-wrap items-center gap-2 text-sm">
                    {r.supplier.gstin ? <><span className="num">{r.supplier.gstin}</span>{r.supplier.gstin_valid ? <Badge tone="green"><CheckCircle2 size={12} /> GSTIN valid{r.supplier.state_code ? ` · state ${r.supplier.state_code}` : ''}</Badge> : <Badge tone="red">GSTIN checksum failed</Badge>}</> : <Badge tone="amber">No GSTIN</Badge>}
                  </div>
                  <div className="mt-2 text-sm text-slate-600">Invoice <strong>{r.draft.supplier_invoice_no || '—'}</strong>{r.draft.invoice_date ? <> dated <strong>{fmtDate(r.draft.invoice_date)}</strong></> : <> · <span className="text-amber-700">date unreadable</span></>}</div>
                </div>
                <div className="text-right">
                  <div className="text-xs font-semibold uppercase tracking-wide text-slate-500">Confidence</div>
                  <div className={`num text-3xl font-bold ${confTone === 'green' ? 'text-emerald-700' : confTone === 'amber' ? 'text-amber-600' : 'text-rose-600'}`}>{conf}%</div>
                  <div className="text-[11px] text-slate-400">model {Math.round(r.model_confidence * 100)}%</div>
                  <UsageChip usage={res?.usage} />
                </div>
              </div>
              <div className="mt-3 grid grid-cols-2 gap-3 text-sm">
                <div className="rounded-xl bg-slate-50 p-3"><div className="text-xs text-slate-500">Printed grand total</div><div className="font-bold"><Money paise={r.printed_grand_total_paise} /></div></div>
                <div className={`rounded-xl p-3 ${r.computed_grand_total_paise && Math.abs(r.computed_grand_total_paise - r.printed_grand_total_paise) > 200 ? 'bg-amber-50' : 'bg-slate-50'}`}><div className="text-xs text-slate-500">Recomputed from lines</div><div className="font-bold">{r.computed_grand_total_paise ? <Money paise={r.computed_grand_total_paise} /> : '—'}</div></div>
              </div>
            </Card>

            {r.warnings.length > 0 && (
              <div className="rounded-xl border border-amber-200 bg-amber-50 p-3 text-sm text-amber-900" role="alert">
                <div className="mb-1 flex items-center gap-2 font-bold"><AlertTriangle size={16} /> Check before saving ({r.warnings.length})</div>
                <ul className="list-disc space-y-0.5 pl-5">{r.warnings.map((w, i) => <li key={i}>{w}</li>)}</ul>
              </div>
            )}

            <Card title={`Lines (${lines.length})`} padded={false}>
              <Table>
                <thead><tr><th className="th">#</th><th className="th">Billed as</th><th className="th">Matched item</th><th className="th text-right">Qty</th><th className="th text-right">Rate</th><th className="th text-right">Disc.</th><th className="th text-right">GST</th></tr></thead>
                <tbody>
                  {lines.map((l, i) => {
                    const m = r.matches[i]
                    return (
                      <tr key={i} className={`border-t border-slate-100 ${!l.item_id ? 'bg-amber-50/50' : ''}`}>
                        <td className="td text-slate-400">{i + 1}</td>
                        <td className="td"><div className="font-medium">{l.name}</div><div className="text-xs text-slate-500">{[l.hsn && `HSN ${l.hsn}`, l.unit].filter(Boolean).join(' · ')}</div></td>
                        <td className="td">
                          {l.item_id ? (
                            <div className="flex items-center gap-1.5">
                              <Badge tone={(m?.score ?? 0) >= 0.85 ? 'green' : 'amber'}>{m?.matched_name ?? 'matched'} · {Math.round((m?.score ?? 0) * 100)}%</Badge>
                              <button type="button" onClick={() => setLines((ls) => ls.map((x, j) => (j === i ? { ...x, item_id: undefined } : x)))} className="rounded p-1 text-slate-400 hover:text-rose-600 cursor-pointer" aria-label={`Unlink matched item for line ${i + 1}`} title="Wrong match? Unlink"><Link2Off size={14} /></button>
                            </div>
                          ) : <Badge tone="amber">pick item on the bill screen</Badge>}
                        </td>
                        <td className="td num text-right">{fmtQty(l.qty)}</td>
                        <td className="td text-right"><Money paise={l.unit_price_paise} />{l.price_includes_tax && <div className="text-[10px] text-slate-400">incl. tax</div>}</td>
                        <td className="td text-right">{l.discount_paise ? <Money paise={l.discount_paise} /> : '—'}</td>
                        <td className="td num text-right">{formatRate(l.tax_rate_bp)}</td>
                      </tr>
                    )
                  })}
                </tbody>
              </Table>
            </Card>

            <div className="flex flex-wrap justify-between gap-2">
              <Button variant="secondary" onClick={reset}>Scan another</Button>
              <Button icon={ShoppingCart} onClick={createBill} disabled={lines.length === 0}>Create purchase bill</Button>
            </div>
            <p className="text-xs text-slate-500">The purchase screen opens pre-filled. Pick or create the supplier party there and fix any unmatched lines before saving.</p>
          </>
        )}
      </div>
    </div>
  )
}
