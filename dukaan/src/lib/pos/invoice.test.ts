// Billing helpers — run with `node --test src/lib/pos/*.test.ts`.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { buildLocalInvoice, cleanCreateInput, documentTitle, lendenPhone, parsePurchaseDraft, summarizeByRate, toPreviewLine, waPhone, whatsappLink } from './invoice.ts'
import { cartTotals, lineFromItem, settle, toInvoiceLine } from './cart.ts'
import type { InvoiceLine, Store } from '../types'

test('request bodies carry only the keys the services know', () => {
  const dirty = {
    doc_type: 'sale', store_id: 's1', party: { name: 'Walk-in', phone: '', junk: 1 }, notes: '', reverse_charge: false, override_credit_limit: false,
    lines: [{ key: 'x', item_id: 'i1', name: 'Parle', qty: 1, unit_price_paise: 1000, tax_rate_bp: 1800, mrp_paise: 1000, track_batches: false, expiry_date: '2026-01-01' }],
    tenders: [{ mode: 'cash', amount_paise: 1000, label: 'Cash' }, { mode: 'upi', amount_paise: 0 }],
  }
  const clean = cleanCreateInput(dirty)
  assert.deepEqual(clean, {
    doc_type: 'sale', store_id: 's1', party: { name: 'Walk-in' },
    lines: [{ item_id: 'i1', name: 'Parle', qty: 1, unit_price_paise: 1000, tax_rate_bp: 1800 }],
    tenders: [{ mode: 'cash', amount_paise: 1000 }],
  })
  assert.deepEqual(toPreviewLine(dirty.lines[0]), { qty: 1, unit_price_paise: 1000, tax_rate_bp: 1800 })
})

test('purchase drafts from other screens are sanitised', () => {
  assert.equal(parsePurchaseDraft(null), null)
  assert.equal(parsePurchaseDraft('{bad'), null)
  const d = parsePurchaseDraft(JSON.stringify({ doc_type: 'purchase_return', party_id: 'p1', ref_invoice_id: 'inv1', lines: [{ item_id: 'i1', name: 'Milk', qty: 2, unit_price_paise: 2500, tax_rate_bp: 500, batch_no: 'B1', expiry_date: '2026-12-01' }, { name: '' }] }))
  assert.equal(d?.doc_type, 'purchase_return')
  assert.equal(d?.ref_invoice_id, 'inv1')
  assert.equal(d?.lines.length, 1)
  assert.deepEqual(d?.lines[0], { item_id: 'i1', name: 'Milk', qty: 2, unit_price_paise: 2500, tax_rate_bp: 500, batch_no: 'B1' })
})

test('tax summary groups stored lines by rate and category', () => {
  const lines: InvoiceLine[] = [
    { line_no: 1, name: 'A', unit: 'PCS', qty: 1, unit_price_paise: 1000, tax_rate_bp: 1800, taxable_paise: 847, cgst_paise: 76, sgst_paise: 76, igst_paise: 0, cess_paise: 0, total_paise: 999, tax_category: 'taxable' },
    { line_no: 2, name: 'B', unit: 'PCS', qty: 1, unit_price_paise: 1000, tax_rate_bp: 1800, taxable_paise: 847, cgst_paise: 77, sgst_paise: 77, igst_paise: 0, cess_paise: 0, total_paise: 1001, tax_category: 'taxable' },
    { line_no: 3, name: 'Salt', unit: 'PCS', qty: 1, unit_price_paise: 2800, tax_rate_bp: 0, taxable_paise: 2800, cgst_paise: 0, sgst_paise: 0, igst_paise: 0, cess_paise: 0, total_paise: 2800, tax_category: 'exempt' },
  ]
  const rows = summarizeByRate(lines)
  assert.equal(rows.length, 2)
  assert.deepEqual(rows[0], { tax_rate_bp: 0, taxable_paise: 2800, cgst_paise: 0, sgst_paise: 0, igst_paise: 0, cess_paise: 0, tax_category: 'exempt' })
  assert.equal(rows[1].cgst_paise, 153)
  assert.equal(rows[1].taxable_paise, 1694)
  assert.deepEqual(summarizeByRate(undefined), [])
})

test('offline bill renders from the same numbers the server will compute', () => {
  const store: Store = { id: 's1', name: 'Main', code: 'MAIN', state_code: '27', gstin: '27AAPFU0939F1ZV' }
  const cart = [lineFromItem({ id: 'i1', name: 'Parle-G', unit: 'PCS', sale_price_paise: 1000, mrp_paise: 1000, tax_rate_bp: 1800, cess_rate_bp: 0, price_includes_tax: true, track_batches: false }, 2)]
  const summary = cartTotals(cart, '27')
  const tenders = [{ mode: 'cash' as const, amount_paise: 5000 }]
  const input = { doc_type: 'sale' as const, store_id: 's1', party: { name: 'Walk-in' }, lines: cart.map(toInvoiceLine), tenders, client_uuid: 'u1' }
  const inv = buildLocalInvoice(input, summary, settle(summary.grand_total_paise, tenders), { number: 'INV/26-27/00042', store, createdAt: '2026-10-24T09:30:00.000Z', partyName: 'Walk-in', clientUuid: 'u1' })
  assert.equal(inv.number, 'INV/26-27/00042')
  assert.equal(inv.grand_total_paise, 2000)
  assert.equal(inv.paid_paise, 2000)
  assert.equal(inv.change_paise, 3000)
  assert.equal(inv.payment_status, 'paid')
  assert.equal(inv.lines?.[0].total_paise, 2000)
  assert.equal(inv.lines?.[0].cgst_paise + inv.lines![0].sgst_paise + inv.lines![0].taxable_paise, 2000)
  assert.equal(inv.amount_in_words, 'Rupees Twenty Only')
  assert.equal(documentTitle(inv), 'Tax Invoice')
  assert.equal(documentTitle({ ...inv, store_gstin: undefined }), 'Bill of Supply')
  assert.equal(documentTitle({ ...inv, doc_type: 'sale_return' }), 'Credit Note')
})

test('phones and WhatsApp links', () => {
  assert.equal(lendenPhone('98765 43210'), '+919876543210')
  assert.equal(lendenPhone('+91-9876543210'), '+919876543210')
  assert.equal(lendenPhone('12345'), undefined)
  assert.equal(waPhone('9876543210'), '919876543210')
  assert.equal(waPhone(''), '')
  const link = whatsappLink({ id: '', store_id: '', doc_type: 'sale', number: 'INV/26-27/00001', fy: '', invoice_date: '2026-10-24', party_name: 'Sharma', party_phone: '9876543210', store_state: '27', store_gstin: '27X', place_of_supply: '27', inter_state: false, status: 'posted', payment_status: 'partial', taxable_paise: 0, discount_paise: 0, cgst_paise: 9, sgst_paise: 9, igst_paise: 0, cess_paise: 0, round_off_paise: 0, grand_total_paise: 12000, paid_paise: 2000, balance_paise: 10000, amount_in_words: '', created_at: '' }, 'Sharma Stores')
  assert.ok(link.startsWith('https://wa.me/919876543210?text='))
  assert.ok(decodeURIComponent(link).includes('Balance due: ₹100.00'))
})
