// Mirrors mool/gst/gst_test.go — run with `npm test` (Node's built-in runner, native TS).
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { amountInWords, computeGST, validateGSTIN } from './gst.ts'
import { formatINR, roundToRupee } from './money.ts'

test('GSTIN validation', () => {
  assert.equal(validateGSTIN('27AAPFU0939F1ZV'), null)
  assert.notEqual(validateGSTIN('27AAPFU0939F1ZA'), null)
  assert.notEqual(validateGSTIN('99AAPFU0939F1ZV'), null)
})

test('intra-state CGST+SGST', () => {
  const s = computeGST([{ qty: 2, unit_price_paise: 50000, tax_rate_bp: 1800 }], '27', '27')
  assert.equal(s.taxable_paise, 100000)
  assert.equal(s.cgst_paise, 9000)
  assert.equal(s.sgst_paise, 9000)
  assert.equal(s.grand_total_paise, 118000)
})

test('inter-state IGST with round off', () => {
  const s = computeGST([{ qty: 1, unit_price_paise: 9999, tax_rate_bp: 1200 }], '27', '29')
  assert.equal(s.inter_state, true)
  assert.equal(s.igst_paise, 1200)
  assert.equal(s.grand_total_paise, 11200)
  assert.equal(s.round_off_paise, 1)
})

test('tax-inclusive keeps shelf price', () => {
  const s = computeGST([{ qty: 3, unit_price_paise: 9900, tax_rate_bp: 500, price_includes_tax: true }], '07', '', false)
  assert.equal(s.lines[0].total_paise, 29700)
})

test('amount in words + formatting', () => {
  assert.equal(amountInWords(12345678950),
    'Rupees Twelve Crore Thirty Four Lakh Fifty Six Thousand Seven Hundred Eighty Nine and Fifty Paise Only')
  assert.deepEqual(roundToRupee(11149), [11100, -49])
  assert.equal(formatINR(12345678), '₹1,23,456.78')
})
