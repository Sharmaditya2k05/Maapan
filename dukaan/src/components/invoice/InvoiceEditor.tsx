'use client'

/**
 * The B2B sales invoice and purchase bill editor: party, dates, place of
 * supply, lines, invoice-level discount / TCS, advanced GST options, live
 * server preview (POST /v1/invoices/preview), save with credit-limit override.
 */
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useRouter } from 'next/navigation'
import { Save } from 'lucide-react'
import { Button, Card, Field, Input, MoneyInput, Select, Textarea, Toggle } from '@/components/ui'
import { todayIST, useGet, useInvalidate, useSessionUser, useStore } from '@/lib/hooks'
import { ApiError, post } from '@/lib/api'
import { deviceId } from '@/lib/session'
import { STATES, STATE_LIST } from '@/lib/gst'
import type { CreateInvoiceInput, DocType, Party, Tender, TenderMode } from '@/lib/types'
import type { CartLine, CreditLimitDetails, InvoiceFull, PosCreateInvoiceInput, PreviewInput, PreviewResponse } from '@/lib/types.pos'
import { cartTotals, emptySummary, toInvoiceLine } from '@/lib/pos/cart'
import { cleanCreateInput, lineToCart, toPreviewLine } from '@/lib/pos/invoice'
import { PartyPicker } from './PartyPicker'
import { LineGrid } from './LineGrid'
import { PreviewPanel } from './PreviewPanel'

const OVERRIDE_ROLES = new Set(['super_admin', 'store_manager', 'accountant'])
const SUPPLY: { v: NonNullable<CreateInvoiceInput['supply_category']>; l: string }[] = [
  { v: '', l: 'Regular (domestic)' }, { v: 'sez_with_payment', l: 'SEZ — with payment of IGST' }, { v: 'sez_without_payment', l: 'SEZ — without payment (LUT)' },
  { v: 'export_with_payment', l: 'Export — with payment of IGST' }, { v: 'export_without_payment', l: 'Export — without payment (LUT)' }, { v: 'deemed_export', l: 'Deemed export' },
]

export interface EditorDraft {
  party?: Party | null
  party_id?: string
  lines?: CartLine[]
  notes?: string
  ref_invoice_id?: string
  supplier_invoice_no?: string
  supplier_invoice_date?: string
  place_of_supply?: string
  due_date?: string
  invoice_discount_paise?: number
  invoice_discount_bp?: number
  tcs_bp?: number
  supply_category?: CreateInvoiceInput['supply_category']
  reverse_charge?: boolean
}

export function InvoiceEditor({ docType, draft, draftKey, refInvoice, afterSave }: {
  docType: Extract<DocType, 'sale' | 'purchase' | 'purchase_return'>
  draft?: EditorDraft | null
  /** Changes when a new draft arrives (duplicate / hand-off), re-seeding the form. */
  draftKey?: string
  refInvoice?: InvoiceFull
  afterSave: (inv: InvoiceFull) => void
}) {
  const router = useRouter()
  const user = useSessionUser()
  const { storeId, store } = useStore()
  const invalidate = useInvalidate()
  const purchase = docType !== 'sale'
  const debit = docType === 'purchase_return'

  const [party, setParty] = useState<Party | null>(null)
  const [lines, setLines] = useState<CartLine[]>([])
  const [date, setDate] = useState(todayIST())
  const [due, setDue] = useState('')
  const [pos, setPos] = useState('')
  const [discMode, setDiscMode] = useState<'amt' | 'pct'>('amt')
  const [discAmt, setDiscAmt] = useState(0)
  const [discPct, setDiscPct] = useState('')
  const [tcs, setTcs] = useState('')
  const [supply, setSupply] = useState<NonNullable<CreateInvoiceInput['supply_category']>>('')
  const [rcm, setRcm] = useState(false)
  const [advanced, setAdvanced] = useState(false)
  const [supNo, setSupNo] = useState('')
  const [supDate, setSupDate] = useState('')
  const [notes, setNotes] = useState('')
  const [payMode, setPayMode] = useState<TenderMode>('bank')
  const [payAmt, setPayAmt] = useState(0)
  const [payRef, setPayRef] = useState('')
  const [override, setOverride] = useState(false)
  const [credit, setCredit] = useState<CreditLimitDetails | null>(null)
  const [err, setErr] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const seeded = useRef<string | undefined>(undefined)

  // Seed from a draft (duplicate, hand-off, return-to-vendor).
  const draftParty = useGet<Party>(draft?.party_id && !draft.party ? `/bahi/v1/parties/${draft.party_id}` : null)
  useEffect(() => {
    if (!draft || seeded.current === (draftKey ?? 'once')) return
    seeded.current = draftKey ?? 'once'
    if (draft.lines) setLines(draft.lines)
    if (draft.party) setParty(draft.party)
    if (draft.notes) setNotes(draft.notes)
    if (draft.supplier_invoice_no) setSupNo(draft.supplier_invoice_no)
    if (draft.supplier_invoice_date) setSupDate(draft.supplier_invoice_date)
    if (draft.place_of_supply) setPos(draft.place_of_supply)
    if (draft.due_date) setDue(draft.due_date)
    if (draft.invoice_discount_paise) { setDiscMode('amt'); setDiscAmt(draft.invoice_discount_paise) }
    if (draft.invoice_discount_bp) { setDiscMode('pct'); setDiscPct(String(draft.invoice_discount_bp / 100)) }
    if (draft.tcs_bp) setTcs(String(draft.tcs_bp / 100))
    if (draft.supply_category) { setSupply(draft.supply_category); setAdvanced(true) }
    if (draft.reverse_charge) { setRcm(true); setAdvanced(true) }
  }, [draft, draftKey])
  useEffect(() => { if (draftParty.data && !party) setParty(draftParty.data) }, [draftParty.data, party])

  // Place of supply follows the party until the user picks one.
  const posEffective = pos || party?.state_code || store?.state_code || ''
  const discountBP = discMode === 'pct' ? Math.round((Number(discPct) || 0) * 100) : 0
  const discountPaise = discMode === 'amt' ? discAmt : 0
  const tcsBP = Math.round((Number(tcs) || 0) * 100)
  const validLines = useMemo(() => lines.filter((l) => l.name.trim() && l.qty > 0), [lines])

  /* ---------------- preview ---------------- */
  const [preview, setPreview] = useState<PreviewResponse | null>(null)
  const [previewErr, setPreviewErr] = useState<string | null>(null)
  const [previewing, setPreviewing] = useState(false)
  const local = useMemo(() => {
    try { return cartTotals(validLines, store?.state_code ?? '', posEffective) } catch { return emptySummary() }
  }, [validLines, store?.state_code, posEffective])

  useEffect(() => {
    if (!store || validLines.length === 0) { setPreview(null); setPreviewErr(null); return }
    const body: PreviewInput = {
      store_state: store.state_code, place_of_supply: posEffective, lines: validLines.map((l) => toPreviewLine(toInvoiceLine(l))),
      invoice_discount_paise: discountPaise || undefined, invoice_discount_bp: discountBP || undefined, tcs_bp: tcsBP || undefined,
      supply_category: supply || undefined, reverse_charge: rcm || undefined, invoice_date: date || undefined,
    }
    let alive = true
    const t = setTimeout(async () => {
      setPreviewing(true)
      try {
        const r = await post<PreviewResponse>('/aadesh/v1/invoices/preview', body)
        if (alive) { setPreview(r); setPreviewErr(null) }
      } catch (e) {
        if (alive) { setPreview(null); setPreviewErr(e instanceof ApiError && e.isNetwork ? null : (e as Error).message) }
      } finally {
        if (alive) setPreviewing(false)
      }
    }, 400)
    return () => { alive = false; clearTimeout(t) }
  }, [store, validLines, posEffective, discountPaise, discountBP, tcsBP, supply, rcm, date])

  const summary = preview?.summary ?? local
  const canOverride = OVERRIDE_ROLES.has(user?.role ?? '')

  /* ---------------- save ---------------- */
  const buildInput = useCallback((): PosCreateInvoiceInput => {
    const tenders: Tender[] = payAmt > 0 ? [{ mode: payMode, amount_paise: payAmt, reference: payRef.trim() || undefined }] : []
    return cleanCreateInput({
      doc_type: docType, store_id: storeId, party_id: party?.id, invoice_date: date, due_date: due || undefined, place_of_supply: pos || undefined,
      supply_category: supply || undefined, reverse_charge: rcm, supplier_invoice_no: purchase && !debit ? supNo.trim() || undefined : undefined,
      supplier_invoice_date: purchase && !debit ? supDate || undefined : undefined, ref_invoice_id: draft?.ref_invoice_id,
      lines: validLines.map(toInvoiceLine), invoice_discount_paise: discountPaise || undefined, invoice_discount_bp: discountBP || undefined,
      tcs_bp: tcsBP || undefined, tenders, notes: notes.trim() || undefined, override_credit_limit: override, client_uuid: crypto.randomUUID(), device_id: deviceId(),
    })
  }, [docType, storeId, party, date, due, pos, supply, rcm, purchase, debit, supNo, supDate, draft?.ref_invoice_id, validLines, discountPaise, discountBP, tcsBP, payAmt, payMode, payRef, notes, override])

  const save = async () => {
    setErr(null)
    if (!party) return setErr(purchase ? 'Pick the supplier' : 'Pick the customer (B2B invoices need a saved party)')
    if (validLines.length === 0) return setErr('Add at least one line with a quantity')
    if (lines.some((l) => l.name.trim() && l.qty > 0 && l.unit_price_paise <= 0 && !purchase)) return setErr('Every line needs a price')
    if (date > todayIST()) return setErr('Invoice date cannot be in the future')
    if (due && due < date) return setErr('Due date cannot be before the invoice date')
    if (purchase && !debit && ((supNo.trim() && !supDate) || (!supNo.trim() && supDate))) return setErr('Enter both the supplier bill number and its date, or neither')
    if (purchase && lines.some((l) => l.track_batches && !l.batch_no)) return setErr('Batch-tracked items need a batch number')
    if (debit && !draft?.ref_invoice_id) return setErr('A debit note needs the original purchase bill')
    setSaving(true)
    try {
      const inv = await post<InvoiceFull>('/aadesh/v1/invoices', buildInput())
      invalidate('/aadesh/v1/invoices', '/bahi/v1/parties', '/bhandar/v1/items', '/bhandar/v1/stock')
      afterSave(inv)
    } catch (e) {
      const ae = e as ApiError
      setErr(ae.message)
      const d = ae instanceof ApiError ? (ae.details as Partial<CreditLimitDetails> | undefined) : undefined
      setCredit(ae instanceof ApiError && ae.status === 422 && d && typeof d.credit_limit_paise === 'number' ? (d as CreditLimitDetails) : null)
    } finally {
      setSaving(false)
    }
  }

  const title = debit ? 'Debit note' : purchase ? 'Purchase bill' : 'Tax invoice'
  if (!store) return <Card><p className="text-sm text-slate-500">Pick a store first.</p></Card>

  return (
    <div className="grid gap-5 xl:grid-cols-[minmax(0,1fr)_340px] sm:gap-6">
      <div className="min-w-0 space-y-5 sm:space-y-6">
        <Card>
          <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
            <div className="sm:col-span-2">
              <span className="label">{purchase ? 'Supplier' : 'Customer'} *</span>
              <PartyPicker value={party} onChange={(p) => { setParty(p); setPos('') }} type={purchase ? 'supplier' : 'customer'} autoFocus={!party} disabled={debit && !!refInvoice} />
              {debit && refInvoice && <p className="mt-1.5 text-xs text-slate-500">Against purchase bill <span className="font-mono font-semibold">{refInvoice.number}</span> dated {refInvoice.invoice_date}</p>}
            </div>
            <Field label={`${title} date`}><Input type="date" value={date} max={todayIST()} onChange={(e) => setDate(e.target.value)} /></Field>
            {!debit && <Field label="Due date" hint={!due && party?.credit_days ? `Defaults to ${party.credit_days} credit days` : undefined}><Input type="date" value={due} min={date} onChange={(e) => setDue(e.target.value)} /></Field>}
            <Field label="Place of supply" hint={!pos && party?.state_code ? `From the ${purchase ? 'supplier' : 'customer'}'s state` : undefined}>
              <Select value={pos} onChange={(e) => setPos(e.target.value)}>
                <option value="">{party?.state_code ? `${STATES[party.state_code] ?? party.state_code} (${purchase ? 'supplier' : 'customer'})` : `${STATES[store.state_code] ?? store.state_code} (store)`}</option>
                {STATE_LIST.map(([c, n]) => <option key={c} value={c}>{c} · {n}</option>)}
              </Select>
            </Field>
            {purchase && !debit && (
              <>
                <Field label="Supplier bill no." hint="Needed for GSTR-2B matching"><Input value={supNo} onChange={(e) => setSupNo(e.target.value)} className="font-mono" /></Field>
                <Field label="Supplier bill date"><Input type="date" value={supDate} max={todayIST()} onChange={(e) => setSupDate(e.target.value)} /></Field>
              </>
            )}
            {!purchase && (
              <>
                <Field label="Invoice discount">
                  <div className="flex">
                    {discMode === 'amt' ? <div className="flex-1"><MoneyInput value={discAmt} onChange={setDiscAmt} className="rounded-r-none" /></div>
                      : <Input inputMode="decimal" className="num rounded-r-none" value={discPct} onChange={(e) => setDiscPct(e.target.value.replace(/[^0-9.]/g, ''))} placeholder="0" />}
                    <button type="button" onClick={() => setDiscMode(discMode === 'amt' ? 'pct' : 'amt')} className="rounded-r-xl border border-l-0 border-slate-200 bg-slate-50 px-3 text-sm font-semibold text-slate-600 hover:bg-slate-100 cursor-pointer" aria-label="Toggle discount type">{discMode === 'amt' ? '₹' : '%'}</button>
                  </div>
                </Field>
                <Field label="TCS %" hint="206C(1H): 0.1 on buyers above ₹50 L"><Input inputMode="decimal" className="num" value={tcs} onChange={(e) => setTcs(e.target.value.replace(/[^0-9.]/g, ''))} placeholder="0" /></Field>
              </>
            )}
          </div>
          <div className="mt-3 border-t border-slate-100 pt-3">
            <button type="button" className="link text-xs cursor-pointer" onClick={() => setAdvanced((a) => !a)}>{advanced ? 'Hide' : 'Show'} advanced GST options</button>
            {advanced && (
              <div className="mt-3 grid gap-3 sm:grid-cols-2">
                <Field label="Supply category">
                  <Select value={supply} onChange={(e) => setSupply(e.target.value as NonNullable<CreateInvoiceInput['supply_category']>)}>{SUPPLY.map((s) => <option key={s.v} value={s.v}>{s.l}</option>)}</Select>
                </Field>
                <div className="flex items-end pb-2"><Toggle checked={rcm} onChange={setRcm} label="Reverse charge (recipient pays the tax)" /></div>
              </div>
            )}
          </div>
        </Card>

        <Card padded={false} title="Items" actions={<span className="text-xs text-slate-500">{validLines.length} line{validLines.length === 1 ? '' : 's'}{purchase ? ' · prices exclusive of GST by default; the supplier’s rate wins' : ' · item rates follow the master'}</span>}>
          <LineGrid lines={lines} onChange={setLines} mode={purchase ? 'purchase' : 'sale'} storeState={store.state_code} placeOfSupply={posEffective} />
        </Card>

        <div className="grid gap-5 md:grid-cols-2 sm:gap-6">
          <Card title="Notes / terms"><Textarea rows={3} value={notes} onChange={(e) => setNotes(e.target.value)} placeholder={purchase ? 'Delivery challan, transporter…' : 'Payment terms, delivery…'} /></Card>
          {!debit && (
            <Card title={purchase ? 'Paid now (optional)' : 'Received now (optional)'}>
              <div className="grid grid-cols-[1fr_auto] gap-2">
                <MoneyInput value={payAmt} onChange={setPayAmt} />
                <Select value={payMode} onChange={(e) => setPayMode(e.target.value as TenderMode)} className="w-28"><option value="bank">Bank</option><option value="cash">Cash</option><option value="upi">UPI</option><option value="card">Card</option></Select>
              </div>
              <Input className="mt-2 font-mono" value={payRef} onChange={(e) => setPayRef(e.target.value)} placeholder="UTR / cheque / reference" />
              <p className="mt-2 text-xs text-slate-500">Anything not paid now stays on the {purchase ? 'supplier' : 'customer'}&apos;s account.</p>
            </Card>
          )}
        </div>
      </div>

      <div className="space-y-4 xl:sticky xl:top-20 xl:self-start">
        <PreviewPanel summary={summary} words={preview?.amount_in_words} loading={previewing} error={previewErr} title={preview ? 'Summary (server)' : 'Summary'} />
        {err && <div className="rounded-xl bg-rose-50 px-4 py-3 text-sm text-rose-700">{err}</div>}
        {credit && (
          <div className="rounded-xl border border-amber-200 bg-amber-50 p-3 text-sm text-amber-900">
            <div className="font-semibold">Credit limit exceeded</div>
            <div className="mt-1 text-xs">Limit ₹{(credit.credit_limit_paise / 100).toFixed(2)} · balance ₹{(credit.balance_paise / 100).toFixed(2)} · this bill ₹{(credit.bill_credit_paise / 100).toFixed(2)}</div>
            {canOverride ? <label className="mt-2 flex items-center gap-2"><input type="checkbox" checked={override} onChange={(e) => setOverride(e.target.checked)} /> Override the limit and save</label>
              : <p className="mt-2 text-xs">Ask a manager to save this invoice, or record a payment.</p>}
          </div>
        )}
        <div className="flex gap-2">
          <Button variant="secondary" onClick={() => router.back()}>Cancel</Button>
          <Button icon={Save} className="flex-1" onClick={save} loading={saving}>Save {title.toLowerCase()}</Button>
        </div>
      </div>
    </div>
  )
}

/** Seed an editor from a stored invoice (duplicate). */
export function draftFromInvoice(inv: InvoiceFull): EditorDraft {
  return {
    party_id: inv.party_id,
    lines: (inv.lines ?? []).map((l) => lineToCart(l)),
    notes: inv.notes,
    place_of_supply: inv.place_of_supply !== inv.party_state ? inv.place_of_supply : undefined,
    invoice_discount_paise: inv.invoice_discount_paise,
    supply_category: (inv.supply_category ?? '') as CreateInvoiceInput['supply_category'],
    reverse_charge: inv.reverse_charge,
  }
}
