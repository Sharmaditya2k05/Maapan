// Counter cart model — run with `node --test src/lib/pos/*.test.ts` (Node's built-in runner, native TS).
import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  addLine, balanceDue, cartTotals, ean13Check, findByCode, freeTextLine, lineDiscount, lineDiscountBP, lineFromItem,
  looksLikeCode, parseScaleCode, scaleQty, searchCatalog, setQty, settle, toInvoiceLine, discountAllowed, type ItemLike,
} from './cart.ts'

const parle: ItemLike = { id: 'i1', name: 'Parle-G 100g', sku: 'PG100', barcode: '8901719100017', hsn: '1905', unit: 'PCS', sale_price_paise: 1000, mrp_paise: 1000, tax_rate_bp: 1800, cess_rate_bp: 0, price_includes_tax: true, track_batches: false }
const salt: ItemLike = { id: 'i2', name: 'Tata Salt 1kg', sku: 'TS1', barcode: '8901234567897', hsn: '2501', unit: 'PCS', sale_price_paise: 2800, mrp_paise: 2800, tax_rate_bp: 0, cess_rate_bp: 0, price_includes_tax: false, track_batches: false, tax_category: 'exempt' }
const onion: ItemLike = { id: 'i3', name: 'Onion', sku: '10021', unit: 'KG', sale_price_paise: 4000, mrp_paise: 0, tax_rate_bp: 0, cess_rate_bp: 0, price_includes_tax: false, track_batches: false }

test('adding the same item again increments qty; different batch stays separate', () => {
  let lines = addLine([], lineFromItem(parle, 1))
  lines = addLine(lines, lineFromItem(parle, 2))
  assert.equal(lines.length, 1)
  assert.equal(lines[0].qty, 3)
  lines = addLine(lines, lineFromItem(parle, 1, { batch_no: 'B2' }))
  assert.equal(lines.length, 2)
  lines = setQty(lines, lines[0].key, 0)
  assert.equal(lines.length, 1)
})

test('non-taxable categories carry no rate', () => {
  const l = lineFromItem(salt)
  assert.equal(l.tax_rate_bp, 0)
  assert.equal(l.tax_category, 'exempt')
  assert.equal(toInvoiceLine(l).tax_category, 'exempt')
})

test('line discount matches gst.grossAndDiscount: ApplyBP(gross, bp) + flat', () => {
  const l = { ...lineFromItem(parle, 3), discount_bp: 1000, discount_paise: 50 } // gross 3000
  assert.equal(lineDiscount(l), 300 + 50)
  assert.equal(lineDiscountBP(l), 1167)
  assert.equal(discountAllowed(l, 'cashier', false), false)
  assert.equal(discountAllowed(l, 'cashier', true), true)
  assert.equal(discountAllowed(l, 'store_manager', false), true)
  assert.equal(discountAllowed({ ...l, discount_paise: 0 }, 'cashier', false), true)
})

test('totals: inclusive 18 % intra-state and inter-state switch', () => {
  const lines = [lineFromItem(parle, 2), lineFromItem(salt, 1)]
  const intra = cartTotals(lines, '27', '27')
  assert.equal(intra.grand_total_paise, 4800)
  assert.equal(intra.cgst_paise, intra.sgst_paise)
  assert.equal(intra.igst_paise, 0)
  assert.equal(intra.cgst_paise + intra.sgst_paise + intra.taxable_paise, 4800)
  const inter = cartTotals(lines, '27', '29')
  assert.equal(inter.inter_state, true)
  assert.equal(inter.cgst_paise, 0)
  // CGST and SGST are rounded independently, so IGST may differ by a paisa; the shelf price never moves.
  assert.ok(Math.abs(inter.igst_paise - (intra.cgst_paise + intra.sgst_paise)) <= 1)
  assert.equal(inter.grand_total_paise, 4800)
  assert.equal(cartTotals([], '27').grand_total_paise, 0)
})

test('toInvoiceLine sends only known keys', () => {
  const l = lineFromItem(parle)
  assert.deepEqual(Object.keys(toInvoiceLine(l)).sort(), ['hsn', 'item_id', 'name', 'price_includes_tax', 'qty', 'tax_rate_bp', 'unit', 'unit_price_paise'])
  const f = freeTextLine({ name: 'Gift wrap', unit_price_paise: 2000, tax_rate_bp: 1800, price_includes_tax: false })
  assert.deepEqual(toInvoiceLine(f), { name: 'Gift wrap', qty: 1, unit_price_paise: 2000, tax_rate_bp: 1800, unit: 'PCS' })
})

test('settle mirrors invoice.Settle', () => {
  assert.deepEqual(settle(10000, [{ mode: 'cash', amount_paise: 10000 }]), { paid: 10000, change: 0, credit: 0 })
  assert.deepEqual(settle(10000, [{ mode: 'cash', amount_paise: 50000 }]), { paid: 10000, change: 40000, credit: 0 })
  assert.deepEqual(settle(10000, [{ mode: 'upi', amount_paise: 6000 }]), { paid: 6000, change: 0, credit: 4000 })
  assert.deepEqual(settle(10000, [{ mode: 'upi', amount_paise: 6000 }, { mode: 'cash', amount_paise: 5000 }]), { paid: 10000, change: 1000, credit: 0 })
  assert.deepEqual(settle(10000, [{ mode: 'card', amount_paise: 4000 }, { mode: 'credit', amount_paise: 3000 }]), { paid: 4000, change: 0, credit: 3000 })
  assert.deepEqual(settle(10000, [{ mode: 'card', amount_paise: 4000 }, { mode: 'credit', amount_paise: 9000 }]), { paid: 4000, change: 0, credit: 6000 })
  assert.deepEqual(settle(10000, []), { paid: 0, change: 0, credit: 10000 })
  assert.equal(settle(10000, [{ mode: 'card', amount_paise: 10001 }]).error, 'card/UPI/bank tenders exceed the bill total')
  assert.equal(balanceDue(10000, [{ mode: 'upi', amount_paise: 2500 }, { mode: 'credit', amount_paise: 1000 }]), 7500)
})

test('weighing-scale labels: price and weight prefixes', () => {
  const mk = (d12: string) => d12 + ean13Check(d12)
  const weight = mk('27' + '10021' + '01250') // 1.250 kg of PLU 10021
  const priced = mk('21' + '10021' + '05000') // ₹50.00 of PLU 10021
  assert.deepEqual(parseScaleCode(weight), { plu: '10021', scale: { kind: 'weight', value: 1250 } })
  assert.deepEqual(parseScaleCode(priced), { plu: '10021', scale: { kind: 'price', value: 5000 } })
  assert.equal(parseScaleCode('8901719100017'), null)
  assert.deepEqual(scaleQty({ kind: 'weight', value: 1250 }, 'KG', 4000), { qty: 1.25 })
  assert.deepEqual(scaleQty({ kind: 'weight', value: 1250 }, 'GM', 4000), { qty: 1250 })
  assert.deepEqual(scaleQty({ kind: 'price', value: 5000 }, 'KG', 4000), { qty: 1.25 })
  assert.deepEqual(scaleQty({ kind: 'price', value: 5000 }, 'PCS', 0), { qty: 1, unit_price_paise: 5000 })
  const hit = findByCode([parle, salt, onion], weight)
  assert.equal(hit?.item.id, 'i3')
  assert.equal(hit?.scale?.kind, 'weight')
  assert.equal(findByCode([parle, salt], '8901719100017')?.item.id, 'i1')
  assert.equal(findByCode([parle, salt], 'pg100')?.item.id, 'i1')
  assert.equal(findByCode([parle, salt], '0000000000000'), null)
})

test('code vs search heuristics and catalog search', () => {
  assert.equal(looksLikeCode('8901719100017'), true)
  assert.equal(looksLikeCode('PG-100'), true)
  assert.equal(looksLikeCode('parle g'), false)
  assert.equal(looksLikeCode('salt'), false)
  const r = searchCatalog([parle, salt, onion], 'tata sa')
  assert.deepEqual(r.map((x) => x.id), ['i2'])
  assert.deepEqual(searchCatalog([parle, salt, onion], 'on').map((x) => x.id), ['i3'])
  assert.deepEqual(searchCatalog([parle, salt, onion], ''), [])
})
