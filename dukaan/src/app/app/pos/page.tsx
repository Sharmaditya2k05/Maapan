'use client'

/**
 * The counter. Scan or search on the right, bill and totals on the left;
 * F2 tenders, F4 hold, Alt+N new bill. Works offline: catalog from IndexedDB,
 * numbers from a device block, bills queued and synced by lib/offline.
 */
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { CloudOff, History, Lock, PackagePlus, Printer, ScanLine, Unlock, UserPlus, Users, Wifi } from 'lucide-react'
import { Badge, Button, Card, Confirm, Empty, Kbd, Money, useToast } from '@/components/ui'
import { useHotkey, useOnline, usePerm, useSessionUser, useStore } from '@/lib/hooks'
import { PERMS } from '@/lib/rbac'
import { usePrefs } from '@/lib/prefs'
import { ApiError, get, post } from '@/lib/api'
import { deviceId } from '@/lib/session'
import { blockStatus, enqueueBill, ensureBlock, loadCatalog, takeOfflineNumber } from '@/lib/offline'
import type { Party, Tender } from '@/lib/types'
import type { CartLine, CatalogItemFull, CodeLookup, CreditLimitDetails, HeldBill, InvoiceFull, LastBill, PosCreateInvoiceInput, ScaleRead } from '@/lib/types.pos'
import { addLine, cartTotals, emptySummary, findByCode, lineFromItem, removeLine, scaleQty, setQty, settle, toInvoiceLine, updateLine, type ItemLike } from '@/lib/pos/cart'
import { buildLocalInvoice, cleanCreateInput } from '@/lib/pos/invoice'
import { bumpFrequent, holdBill, listHeld, quickItems, readLastBill, removeHeld, saveLastBill } from '@/lib/pos/storage'
import { beep } from '@/lib/pos/beep'
import { layoutFor, printInIframe, thermalWidth } from '@/lib/pos/print'
import { ScanBox } from '@/components/pos/ScanBox'
import { QuickGrid } from '@/components/pos/QuickGrid'
import { CartTable } from '@/components/pos/CartTable'
import { TotalsPanel } from '@/components/pos/TotalsPanel'
import { LineEditor } from '@/components/pos/LineEditor'
import { BatchPicker } from '@/components/pos/BatchPicker'
import { FreeTextDialog } from '@/components/pos/FreeTextDialog'
import { HeldBillsDialog } from '@/components/pos/HeldBillsDialog'
import { CustomerDialog } from '@/components/pos/CustomerDialog'
import { TenderDialog } from '@/components/pos/TenderDialog'
import { InvoiceA4 } from '@/components/print/InvoiceA4'
import { ThermalReceipt } from '@/components/print/ThermalReceipt'

type Dialog = null | 'tender' | 'customer' | 'held' | 'free' | 'unlock' | 'new'
const OVERRIDE_ROLES = new Set(['super_admin', 'store_manager', 'accountant'])

export default function PosPage() {
  const user = useSessionUser()
  const { storeId, store } = useStore()
  const canDo = usePerm()
  const online = useOnline()
  const [prefs] = usePrefs()
  const toast = useToast()

  const [lines, setLines] = useState<CartLine[]>([])
  const [party, setParty] = useState<Party | null>(null)
  const [notes] = useState('')
  const [catalog, setCatalog] = useState<CatalogItemFull[]>([])
  const [catalogInfo, setCatalogInfo] = useState<{ at: Date; fromCache: boolean } | null>(null)
  const [blocks, setBlocks] = useState<{ left: number; total: number } | null>(null)
  const [held, setHeld] = useState<HeldBill[]>([])
  const [last, setLast] = useState<LastBill | null>(null)
  const [dlg, setDlg] = useState<Dialog>(null)
  const [editLine, setEditLine] = useState<CartLine | null>(null)
  const [batchFor, setBatchFor] = useState<{ item: ItemLike; qty: number; unit_price_paise?: number } | null>(null)
  const [freePreset, setFreePreset] = useState('')
  const [unlocked, setUnlocked] = useState(false)
  const [flash, setFlash] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const [saveErr, setSaveErr] = useState<string | null>(null)
  const [creditDetails, setCreditDetails] = useState<CreditLimitDetails | null>(null)
  const [inlinePrint, setInlinePrint] = useState<LastBill | null>(null)
  const scanRef = useRef<HTMLInputElement>(null)

  const anyDialog = dlg !== null || editLine !== null || batchFor !== null || inlinePrint !== null
  const dialogRef = useRef(anyDialog)
  dialogRef.current = anyDialog

  const refocus = useCallback(() => {
    if (dialogRef.current) return
    requestAnimationFrame(() => scanRef.current?.focus())
  }, [])
  useEffect(() => { if (!anyDialog) refocus() }, [anyDialog, refocus])
  useEffect(() => {
    const h = (e: MouseEvent) => {
      const t = e.target as HTMLElement | null
      if (!t || dialogRef.current) return
      if (t.closest('input, textarea, select, [contenteditable], [role=dialog], a')) return
      refocus()
    }
    document.addEventListener('click', h)
    window.addEventListener('focus', refocus)
    return () => { document.removeEventListener('click', h); window.removeEventListener('focus', refocus) }
  }, [refocus])

  // Catalog: fresh when online, cached otherwise; refreshed every 5 minutes.
  useEffect(() => {
    if (!storeId) return
    let alive = true
    const load = async () => {
      const r = await loadCatalog(storeId)
      if (!alive) return
      setCatalog(r.items as CatalogItemFull[])
      setCatalogInfo({ at: new Date(), fromCache: r.fromCache })
    }
    void load()
    const t = setInterval(() => { if (navigator.onLine) void load() }, 5 * 60_000)
    window.addEventListener('online', load)
    return () => { alive = false; clearInterval(t); window.removeEventListener('online', load) }
  }, [storeId])

  // Offline numbers, queue sync, held bills, last bill.
  useEffect(() => {
    if (!storeId) return
    void ensureBlock(storeId).then(() => blockStatus(storeId)).then(setBlocks).catch(() => {})
    setHeld(listHeld(storeId))
    setLast(readLastBill(storeId))

  }, [storeId])

  useEffect(() => { if (!flash) return; const t = setTimeout(() => setFlash(null), 2500); return () => clearTimeout(t) }, [flash])

  const pos = party?.state_code || store?.state_code || ''
  const totals = useMemo(() => {
    try { return { s: cartTotals(lines, store?.state_code ?? '', pos), err: null as string | null } } catch (e) { return { s: emptySummary(), err: (e as Error).message } }
  }, [lines, store?.state_code, pos])
  // Re-ranked after every saved bill (usage counts are bumped on save).
  const [quick, setQuick] = useState<CatalogItemFull[]>([])
  useEffect(() => { setQuick(storeId ? quickItems(storeId, catalog, 12) : []) }, [storeId, catalog, last])

  /* ---------------- adding lines ---------------- */

  const addItem = useCallback((it: ItemLike, qty = 1, opts: { batch_no?: string; unit_price_paise?: number; silent?: boolean } = {}) => {
    if (it.track_batches && !opts.batch_no) { setBatchFor({ item: it, qty, unit_price_paise: opts.unit_price_paise }); return }
    setLines((ls) => addLine(ls, lineFromItem(it, qty, { batch_no: opts.batch_no, unit_price_paise: opts.unit_price_paise })))
    if (prefs.beep && !opts.silent) beep(true)
    refocus()
  }, [prefs.beep, refocus])

  const onScan = useCallback(async (code: string) => {
    let found: { item: ItemLike; scale?: ScaleRead } | null = null
    if (navigator.onLine) {
      try {
        const r = await get<CodeLookup>(`/bhandar/v1/barcodes/${encodeURIComponent(code)}`)
        found = { item: r.item, scale: r.scale }
      } catch (e) {
        // 404 → genuinely unknown unless the cache knows it; network trouble → cache.
        found = findByCode(catalog, code)
        if (!found && !(e instanceof ApiError && e.status === 404)) setCatalogInfo((c) => (c ? { ...c, fromCache: true } : c))
      }
    } else found = findByCode(catalog, code)
    if (!found) {
      setFlash(`Unknown code ${code}`)
      if (prefs.beep) beep(false)
      refocus()
      return
    }
    const price = found.item.sale_price_paise || found.item.mrp_paise
    const sq = found.scale ? scaleQty(found.scale, found.item.unit, price) : { qty: 1 }
    addItem(found.item, sq.qty, { unit_price_paise: sq.unit_price_paise })
  }, [catalog, prefs.beep, addItem, refocus])

  /* ---------------- bill actions ---------------- */

  const resetBill = useCallback(() => {
    setLines([]); setParty(null); setUnlocked(false); setSaveErr(null); setCreditDetails(null)
    refocus()
  }, [refocus])

  const hold = useCallback(() => {
    if (!storeId || lines.length === 0) return
    const b: HeldBill = { id: crypto.randomUUID(), store_id: storeId, held_at: new Date().toISOString(), lines, party: party ?? undefined, notes, total_paise: totals.s.grand_total_paise }
    setHeld(holdBill(b))
    resetBill()
    toast(`Bill on hold · ${b.lines.length} lines`)
  }, [storeId, lines, party, notes, totals.s.grand_total_paise, resetBill, toast])

  const resume = (b: HeldBill) => {
    if (!storeId) return
    if (lines.length > 0) hold()
    setLines(b.lines); setParty(b.party ?? null)
    setHeld(removeHeld(storeId, b.id))
    setDlg(null)
  }

  const openTender = useCallback(() => { if (lines.length > 0 && !totals.err) { setSaveErr(null); setCreditDetails(null); setDlg('tender') } }, [lines.length, totals.err])
  useHotkey('F2', openTender)
  useHotkey('F4', hold)
  useHotkey('n', () => (lines.length ? setDlg('new') : resetBill()), { alt: true, always: true })

  const canOverride = OVERRIDE_ROLES.has(user?.role ?? '')

  const saveBill = async (tenders: Tender[], opts: { phone?: string; override?: boolean }) => {
    if (!store || !storeId) return
    setSaving(true); setSaveErr(null); setCreditDetails(null)
    const now = new Date().toISOString()
    const clientUuid = crypto.randomUUID()
    const input: PosCreateInvoiceInput = cleanCreateInput({
      doc_type: 'sale', store_id: storeId, party_id: party?.id, party: party ? undefined : { name: 'Walk-in', phone: opts.phone },
      lines: lines.map(toInvoiceLine), tenders, notes, client_uuid: clientUuid, device_id: deviceId(), client_created_at: now,
      override_credit_limit: opts.override,
    })
    const stl = settle(totals.s.grand_total_paise, tenders)
    let inv: InvoiceFull
    let queued = false
    try {
      if (!navigator.onLine) throw new ApiError(0, 'network', 'offline')
      inv = await post<InvoiceFull>('/aadesh/v1/invoices', input)
    } catch (e) {
      if (!(e instanceof ApiError) || !e.isNetwork) {
        const err = e as ApiError
        setSaveErr(err.message)
        const d = err.details as Partial<CreditLimitDetails> | undefined
        if (err.status === 422 && d && typeof d.credit_limit_paise === 'number') setCreditDetails(d as CreditLimitDetails)
        setSaving(false)
        return
      }
      try {
        const n = await takeOfflineNumber(storeId)
        input.number = n.number
        inv = buildLocalInvoice(input, totals.s, stl, {
          number: n.display, store, createdAt: now, partyName: party?.name ?? 'Walk-in', partyPhone: party?.phone || opts.phone,
          partyGstin: party?.gstin || undefined, partyState: party?.state_code || undefined, clientUuid,
        })
        await enqueueBill({ client_uuid: clientUuid, store_id: storeId, input, display_number: n.display, grand_total_paise: totals.s.grand_total_paise, status: 'pending', created_at: now })
        queued = true
      } catch (e2) {
        setSaveErr((e2 as Error).message)
        setSaving(false)
        return
      }
    }
    const lb: LastBill = { invoice: inv, queued }
    setLast(lb)
    saveLastBill(storeId, lb)
    bumpFrequent(storeId, lines.map((l) => l.item_id).filter((x): x is string => !!x))
    setSaving(false)
    setDlg(null)
    resetBill()
    toast(queued ? `Saved on this device as ${inv.number} — uploads when online` : `Saved ${inv.number}${stl.change ? ` · change ₹${(stl.change / 100).toFixed(2)}` : ''}`)
    if (prefs.autoPrint) reprint(lb)
    void ensureBlock(storeId).then(() => blockStatus(storeId)).then(setBlocks).catch(() => {})
  }

  const reprint = (b: LastBill) => {
    if (b.invoice.id && !b.queued) printInIframe(b.invoice.id, layoutFor(prefs), thermalWidth(prefs))
    else setInlinePrint(b)
  }
  useEffect(() => {
    if (!inlinePrint) return
    const t = setTimeout(() => { window.print(); setInlinePrint(null) }, 200)
    return () => clearTimeout(t)
  }, [inlinePrint])

  if (user && !canDo(PERMS.invoiceCreate)) {
    return <Card><Empty icon={ScanLine} title="Counter not available" text="Your role cannot create bills. Ask the owner for cashier access." /></Card>
  }
  if (!store) {
    return <Card><Empty icon={ScanLine} title="No store selected" text="Pick a store from the top bar to open the counter." /></Card>
  }

  const role = user?.role
  const printLayout = layoutFor(prefs)
  const printW = thermalWidth(prefs)

  return (
    <>
      <div className="no-print -mx-3 -my-4 flex flex-col bg-slate-50 sm:-mx-5 sm:-my-6 lg:-mx-8 md:h-[calc(100dvh-3.5rem)] md:flex-row md:overflow-hidden">
        {/* bill */}
        <section className="flex min-h-0 min-w-0 flex-1 flex-col">
          <header className="flex flex-wrap items-center gap-2 border-b border-slate-200 bg-white px-3 py-2 sm:px-4">
            <div className="flex items-center gap-2">
              <span className="text-sm font-bold">{store.name}</span>
              {online ? <Badge tone="green"><Wifi size={11} /> Online</Badge> : <Badge tone="red"><CloudOff size={11} /> Offline</Badge>}
              {catalogInfo && <span className="hidden text-[11px] text-slate-400 lg:inline">{catalog.length} items{catalogInfo.fromCache ? ' (cached)' : ''}</span>}
              {blocks && blocks.total > 0 && <span className="hidden text-[11px] text-slate-400 xl:inline" title="Invoice numbers reserved for offline billing">· {blocks.left} offline nos.</span>}
            </div>
            <div className="ml-auto flex flex-wrap items-center gap-2">
              {last && (
                <button type="button" onClick={() => reprint(last)} className="inline-flex h-9 items-center gap-1.5 rounded-xl border border-slate-200 bg-white px-3 text-xs font-semibold text-slate-700 hover:bg-slate-50 cursor-pointer" title="Reprint the last bill">
                  <Printer size={13} /> <span className="hidden sm:inline">Last</span> {last.invoice.number} · <Money paise={last.invoice.grand_total_paise} />{last.queued && <Badge tone="amber">queued</Badge>}
                </button>
              )}
              <Button variant="secondary" size="sm" icon={History} onClick={() => setDlg('held')}>Held{held.length ? ` (${held.length})` : ''}</Button>
              {role === 'cashier' && (
                unlocked
                  ? <Badge tone="green"><Unlock size={11} /> Manager unlocked</Badge>
                  : <Button variant="ghost" size="sm" icon={Lock} onClick={() => setDlg('unlock')} title="Allow discounts above 10 % on this bill">Unlock</Button>
              )}
            </div>
          </header>

          <div className="flex items-center gap-2 border-b border-slate-200 bg-white px-3 py-2 sm:px-4">
            <button type="button" onClick={() => setDlg('customer')} className="flex min-w-0 flex-1 items-center gap-2 rounded-xl px-2 py-1.5 text-left hover:bg-slate-50 cursor-pointer" aria-label="Change customer">
              <span className={`grid h-8 w-8 shrink-0 place-items-center rounded-full ${party ? 'bg-brand-50 text-brand-700' : 'bg-slate-100 text-slate-500'}`}>{party ? <Users size={15} /> : <UserPlus size={15} />}</span>
              <span className="min-w-0">
                <span className="block truncate text-sm font-semibold">{party ? party.name : 'Walk-in customer'}</span>
                <span className="block truncate text-xs text-slate-500">
                  {party ? [party.phone, party.gstin, party.state_code && party.state_code !== store.state_code ? `Inter-state · IGST` : null].filter(Boolean).join(' · ') || 'Saved customer' : 'Tap to add a customer (credit, GSTIN, WhatsApp)'}
                </span>
              </span>
            </button>
            {party && (party.credit_limit_paise > 0 || party.balance_paise !== 0) && (
              <div className="hidden text-right text-xs text-slate-500 sm:block">
                Balance <Money paise={party.balance_paise} signed className="font-semibold" />
                {party.credit_limit_paise > 0 && <><br />Limit <Money paise={party.credit_limit_paise} /></>}
              </div>
            )}
          </div>

          <div className="min-h-0 flex-1 overflow-y-auto bg-white">
            <CartTable lines={lines} totals={totals.s} role={role} unlocked={unlocked}
              onQty={(k, q) => { setLines((ls) => setQty(ls, k, q)); refocus() }}
              onEdit={setEditLine}
              onRemove={(k) => { setLines((ls) => removeLine(ls, k)); refocus() }} />
          </div>

          <TotalsPanel lines={lines} totals={totals.s} error={totals.err} interState={totals.s.inter_state} saving={saving}
            onTender={openTender} onHold={hold} onNew={() => (lines.length ? setDlg('new') : resetBill())} />
        </section>

        {/* scan / search */}
        <aside className="order-first flex flex-col gap-4 border-b border-slate-200 bg-slate-50 p-3 md:order-none md:w-[340px] md:shrink-0 md:overflow-y-auto md:border-b-0 md:border-l lg:w-[380px] xl:w-[420px] sm:p-4">
          <ScanBox inputRef={scanRef} catalog={catalog} flash={flash} onScan={onScan} onAdd={(it) => addItem(it)} onFreeText={() => { setFreePreset(''); setDlg('free') }} />
          <div className="flex flex-wrap items-center gap-2 text-[11px] text-slate-500">
            <span><Kbd>F2</Kbd> tenders</span><span><Kbd>F4</Kbd> hold</span><span><Kbd>Alt+N</Kbd> new</span><span><Kbd>Esc</Kbd> close</span>
            <Button variant="ghost" size="sm" icon={PackagePlus} className="ml-auto" onClick={() => { setFreePreset(''); setDlg('free') }}>Free item</Button>
          </div>
          <QuickGrid items={quick} onAdd={(it) => addItem(it)} />
          {catalog.length === 0 && catalogInfo && <p className="text-xs text-slate-500">The catalog is empty{catalogInfo.fromCache ? ' and nothing is cached yet — connect once to download items' : ''}. You can still bill free-text lines.</p>}
        </aside>
      </div>

      {/* dialogs */}
      <TenderDialog open={dlg === 'tender'} onClose={() => setDlg(null)} total={totals.s.grand_total_paise} party={party} storeId={storeId ?? ''} defaultMode={prefs.defaultTender}
        askPhone={prefs.askPhone} saving={saving} error={saveErr} creditDetails={creditDetails} canOverride={canOverride} onComplete={saveBill} />
      <CustomerDialog open={dlg === 'customer'} onClose={() => setDlg(null)} value={party} onChange={setParty} />
      <HeldBillsDialog open={dlg === 'held'} onClose={() => setDlg(null)} bills={held} onResume={resume} onDelete={(id) => storeId && setHeld(removeHeld(storeId, id))} />
      <FreeTextDialog open={dlg === 'free'} onClose={() => setDlg(null)} presetName={freePreset} onAdd={(l) => { setLines((ls) => [...ls, l]); if (prefs.beep) beep(true) }} />
      <LineEditor line={editLine} open={editLine !== null} onClose={() => setEditLine(null)} role={role} unlocked={unlocked} onUnlock={() => setDlg('unlock')}
        onSave={(patch) => editLine && setLines((ls) => updateLine(ls, editLine.key, patch))} onRemove={() => editLine && setLines((ls) => removeLine(ls, editLine.key))} />
      <BatchPicker item={batchFor?.item ?? null} open={batchFor !== null} onClose={() => setBatchFor(null)}
        onPick={(b) => { if (batchFor) addItem(batchFor.item, batchFor.qty, { batch_no: b, unit_price_paise: batchFor.unit_price_paise }); setBatchFor(null) }} />
      <Confirm open={dlg === 'unlock'} onClose={() => setDlg(editLine ? null : null)} title="Manager unlock" confirmLabel="Unlock this bill"
        text="Discounts above 10 % per line need a manager. Unlock only when a manager has approved it; the server records who billed it."
        onConfirm={() => { setUnlocked(true); setDlg(null) }} />
      <Confirm open={dlg === 'new'} onClose={() => setDlg(null)} title="Start a new bill?" confirmLabel="Discard and start new" danger
        text={`The current bill has ${lines.length} line${lines.length === 1 ? '' : 's'}. Hold it with F4 if the customer is coming back.`}
        onConfirm={() => { resetBill(); setDlg(null) }} />

      {/* offline / queued bills print from here (saved bills print through the /print route) */}
      {inlinePrint && (
        <div className={`print-root hidden print:block ${printLayout === 'a4' ? 'print-a4' : `print-thermal ${printW === 58 ? 'thermal-58' : ''}`}`}>
          {printLayout === 'a4'
            ? <InvoiceA4 invoice={inlinePrint.invoice} store={store} businessName={user?.business_name} />
            : <ThermalReceipt invoice={inlinePrint.invoice} store={store} businessName={user?.business_name} width={printW} cashier={user?.name} queued={inlinePrint.queued} />}
        </div>
      )}
    </>
  )
}
