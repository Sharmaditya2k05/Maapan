import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  emptyRegistrationDraft, ewayCancelCheck, ewayExtendCheck, fileableRegistrations, fromMMYYYY, groupTwoB, irnCancelCheck, isValidPin,
  normaliseGSTIN, normaliseVehicleNo, paiseToRupeeString, parseMMYYYY, periodLabel, periodRange, registrationBody, rupeesToPaise,
  stateForGSTIN, toMMYYYY, validateRegistration,
  filterTwoB, taxOf, twoBCsv, noGstinItcWarning, ITC_ELIGIBILITY, openingToPaise, openingBody,
} from './compliance.ts'
import type { ReconResult, Registration } from './types.kar.ts'

const GSTIN = '27AAPFU0939F1ZV'

test('PIN codes are six digits not starting with 0', () => {
  assert.equal(isValidPin('411001'), true)
  assert.equal(isValidPin(' 560025 '), true)
  assert.equal(isValidPin('011001'), false)
  assert.equal(isValidPin('41100'), false)
  assert.equal(isValidPin('4110011'), false)
  assert.equal(isValidPin('41a001'), false)
})

test('registration validation: checksum, state follows GSTIN, PIN, e-invoice needs city + PIN', () => {
  const ok = { ...emptyRegistrationDraft(), gstin: GSTIN, state_code: '27', city: 'Pune', pin: '411001' }
  assert.deepEqual(validateRegistration(ok), {})
  assert.match(validateRegistration({ ...ok, gstin: '27AAPFU0939F1ZW' }).gstin ?? '', /checksum/)
  assert.match(validateRegistration({ ...ok, state_code: '29' }).state_code ?? '', /Maharashtra/)
  assert.equal(validateRegistration({ ...ok, pin: '12345' }).pin, 'PIN code is 6 digits')
  assert.match(validateRegistration({ ...ok, einvoice_applicable: true, pin: '' }).pin ?? '', /IRP/)
  assert.match(validateRegistration({ ...ok, einvoice_applicable: true, city: '' }).city ?? '', /city/)
  assert.match(validateRegistration({ ...ok, gstin: '', einvoice_applicable: true }).einvoice_applicable ?? '', /GSTIN/)
  assert.ok(validateRegistration({ ...ok, einvoice_applicable: true, composition: true }).composition)
  assert.equal(validateRegistration({ ...emptyRegistrationDraft() }).state_code, 'Choose the state')
  // Unregistered identity: no GSTIN, any state.
  assert.deepEqual(validateRegistration({ ...emptyRegistrationDraft(), state_code: '07' }), {})
  assert.equal(validateRegistration({ ...ok, email: 'nope' }).email, 'Not an email address')
})

test('GSTIN input is normalised and drives the state', () => {
  assert.equal(normaliseGSTIN(' 27aapfu-0939f1zv extra'), GSTIN)
  assert.equal(stateForGSTIN(GSTIN, '29'), '27')
  assert.equal(stateForGSTIN('27AAPFU', '29'), '29')
  const body = registrationBody({ ...emptyRegistrationDraft(), gstin: '27aapfu0939f1zv', state_code: '29', city: ' Pune ', pin: '411001 ' })
  assert.equal(body.gstin, GSTIN)
  assert.equal(body.state_code, '27')
  assert.equal(body.city, 'Pune')
  assert.equal(body.pin, '411001')
})

test('fileable registrations: active with a GSTIN, primary first', () => {
  const r = (over: Partial<Registration>): Registration => ({
    id: over.gstin ?? 'x', tenant_id: 't', gstin: '', legal_name: '', trade_name: '', state_code: '27', address: '', city: '', pin: '', phone: '', email: '',
    composition: false, einvoice_applicable: false, is_primary: false, status: 'active', store_count: 0, created_at: '', updated_at: '', ...over,
  })
  const list = fileableRegistrations([r({ gstin: '29AABCB1234C1ZA' }), r({ gstin: '' }), r({ gstin: '07AAAAA0000A1Z5', status: 'inactive' }), r({ gstin: GSTIN, is_primary: true })])
  assert.deepEqual(list.map((x) => x.gstin), [GSTIN, '29AABCB1234C1ZA'])
})

test('MMYYYY periods', () => {
  assert.deepEqual(parseMMYYYY('102026'), { year: 2026, month: 10 })
  assert.equal(parseMMYYYY('132026'), null)
  assert.equal(parseMMYYYY('2026-10'), null)
  assert.equal(parseMMYYYY('012016'), null)
  assert.equal(toMMYYYY('2026-04'), '042026')
  assert.equal(fromMMYYYY('042026'), '2026-04')
  assert.equal(fromMMYYYY('bad'), '')
  assert.equal(periodLabel('022027'), 'Feb 2027')
  assert.deepEqual(periodRange('022028'), { from: '2028-02-01', to: '2028-02-29' })
  assert.deepEqual(periodRange('112026'), { from: '2026-11-01', to: '2026-11-30' })
})

test('rupees ⇄ paise without float drift', () => {
  assert.equal(rupeesToPaise(11.44), 1144)
  assert.equal(rupeesToPaise(0.29), 29)
  assert.equal(rupeesToPaise('70.85'), 7085)
  assert.equal(rupeesToPaise(undefined), 0)
  assert.equal(paiseToRupeeString(123450), '1234.50')
  assert.equal(paiseToRupeeString(5), '0.05')
  assert.equal(paiseToRupeeString(-1999), '-19.99')
})

test('IRN cancel window and the active e-way bill block', () => {
  const ack = '2026-10-24T23:13:45+05:30'
  const until = '2026-10-25T23:13:45+05:30'
  const t = Date.parse('2026-10-25T10:00:00+05:30')
  assert.deepEqual(irnCancelCheck({ status: 'generated', ack_date: ack, cancellable_until: until }, null, t), { ok: true })
  assert.equal(irnCancelCheck({ status: 'generated', ack_date: ack, cancellable_until: until }, null, Date.parse('2026-10-26T00:00:00+05:30')).ok, false)
  const blocked = irnCancelCheck({ status: 'generated', ack_date: ack }, { status: 'generated', ewb_no: '100000000001' }, t)
  assert.equal(blocked.ok, false)
  assert.match(blocked.ok ? '' : blocked.reason, /100000000001/)
  assert.deepEqual(irnCancelCheck({ status: 'generated', ack_date: ack }, { status: 'cancelled', ewb_no: '1' }, t), { ok: true })
  assert.equal(irnCancelCheck({ status: 'cancelled', ack_date: ack }, null, t).ok, false)
  assert.equal(irnCancelCheck(null, null, t).ok, false)
})

test('e-way bill cancel (24 h) and extend (±8 h) windows', () => {
  const gen = '2026-10-24T23:13:49+05:30'
  assert.equal(ewayCancelCheck({ status: 'generated', ewb_date: gen }, Date.parse('2026-10-25T20:00:00+05:30')).ok, true)
  assert.equal(ewayCancelCheck({ status: 'generated', ewb_date: gen }, Date.parse('2026-10-26T00:00:00+05:30')).ok, false)
  const w = { status: 'generated', valid_upto: '2026-10-29T23:59:59+05:30' }
  assert.equal(ewayExtendCheck(w, Date.parse('2026-10-29T10:00:00+05:30')).ok, false)
  assert.equal(ewayExtendCheck(w, Date.parse('2026-10-29T17:00:00+05:30')).ok, true)
  assert.equal(ewayExtendCheck(w, Date.parse('2026-10-30T07:00:00+05:30')).ok, true)
  assert.equal(ewayExtendCheck(w, Date.parse('2026-10-30T09:00:00+05:30')).ok, false)
  assert.equal(ewayExtendCheck({ status: 'generated' }).ok, false)
  assert.equal(normaliseVehicleNo('mh 12-ab 1234'), 'MH12AB1234')
})

test('2B rows grouped by match status and IMS action', () => {
  const rows: ReconResult[] = [
    { status: 'matched', doc_key: 'a' },
    { status: 'matched', doc_key: 'b', ims: { action: 'accept', actor: 'u', acted_at: '' } },
    { status: 'matched', doc_key: 'c', ims: { action: 'pending', actor: 'u', acted_at: '' } },
    { status: 'mismatch', doc_key: 'd', ims: { action: 'reject', actor: 'u', acted_at: '' } },
    { status: 'missing_in_books', doc_key: 'e' },
    { status: 'missing_in_2b' },
  ]
  const g = groupTwoB(rows)
  assert.deepEqual(g.byStatus, { matched: 3, mismatch: 1, missing_in_books: 1, missing_in_2b: 1 })
  assert.deepEqual(g.byAction, { accept: 1, reject: 1, pending: 1, none: 2 })
  assert.equal(g.acceptable, 2)
  assert.equal(g.total, 6)
})

/* ---------------- Phase 3 additions: 2B filters / CSV, ITC helpers, opening ---------------- */

const twoB = (over: Partial<ReconResult>): ReconResult => ({ status: 'matched', ...over })
const doc2b = (no: string, name: string) => ({
  doc_key: `29AABCU9603R1ZJ_INV_${no}_2627`, section: 'b2b', supplier_gstin: '29AABCU9603R1ZJ', supplier_name: name, doc_type: 'INV', doc_no: no, doc_date: '2026-10-04',
  supply_type: 'R', pos: '27', reverse_charge: false, value: 1180, taxable: 1000, igst: 180, cgst: 0, sgst: 0, cess: 0, itc_available: 'Y',
})

test('filterTwoB filters by status, IMS action and search text', () => {
  const items: ReconResult[] = [
    twoB({ status: 'matched', doc_key: 'a', doc: doc2b('A1', 'Acme Traders') }),
    twoB({ status: 'mismatch', doc_key: 'b', doc: doc2b('B7', 'Bharat Steel'), ims: { action: 'reject', actor: 'u', acted_at: '2026-11-01T00:00:00Z' } }),
    twoB({ status: 'missing_in_2b', purchase: { id: 'p', doc_type: 'purchase', number: 'PUR/26-27/00009', invoice_date: '2026-10-02', supplier_gstin: '27AAPFU0939F1ZV', supplier_name: 'Chotu Kirana', taxable: 10, igst: 0, cgst: 0.9, sgst: 0.9, cess: 0 } }),
  ]
  assert.equal(filterTwoB(items, '').length, 3)
  assert.deepEqual(filterTwoB(items, 'mismatch').map((r) => r.doc_key), ['b'])
  assert.deepEqual(filterTwoB(items, '', '', 'none').map((r) => r.doc_key), ['a'], 'rows without a 2B document cannot carry an action')
  assert.deepEqual(filterTwoB(items, '', '', 'reject').map((r) => r.doc_key), ['b'])
  assert.equal(filterTwoB(items, '', 'acme').length, 1)
  assert.equal(filterTwoB(items, '', 'PUR/26-27').length, 1)
  assert.equal(filterTwoB(items, 'matched', 'bharat').length, 0)
})

test('taxOf adds the four heads exactly', () => {
  assert.equal(taxOf({ igst: 0.1, cgst: 0.2, sgst: 0, cess: 0 }), 0.3)
  assert.equal(taxOf({ igst: 0, cgst: 45.05, sgst: 45.05, cess: 1.1 }), 91.2)
  assert.equal(taxOf(undefined), 0)
})

test('twoBCsv writes a BOM, a header and quoted cells', () => {
  const csv = twoBCsv([twoB({ status: 'mismatch', doc_key: 'k1', doc: doc2b('X,1', 'Acme "Best" Co'), diffs: [{ field: 'taxable', books: 990, gstr2b: 1000 }] })])
  assert.ok(csv.startsWith('﻿status,ims_action,'))
  const row = csv.split('\r\n')[1]
  assert.ok(row.startsWith('mismatch,none,b2b,29AABCU9603R1ZJ,"Acme ""Best"" Co",INV,"X,1",2026-10-04,Y,1000,180,0,0,0,'))
  assert.ok(row.includes('taxable: books 990 / 2B 1000'))
  assert.ok(row.endsWith(',k1'))
})

test('noGstinItcWarning only for GST charged by a supplier without a GSTIN', () => {
  assert.equal(noGstinItcWarning('', 0), null, 'no tax, nothing to warn')
  assert.equal(noGstinItcWarning(GSTIN, 1800), null)
  assert.equal(noGstinItcWarning('', 1800, true), null, 'reverse charge: we pay the tax')
  assert.match(noGstinItcWarning(undefined, 1800) ?? '', /no GSTIN/)
  assert.match(noGstinItcWarning('   ', 1) ?? '', /cannot be claimed/)
})

test('ITC eligibility options match aadesh values', () => {
  assert.deepEqual(ITC_ELIGIBILITY.map((o) => o.value), ['eligible', 'ineligible_17_5', 'ineligible_other'])
})

test('opening ITC converts rupees to paise and back as exact strings', () => {
  const p = openingToPaise({ igst: 1234.5, cgst: 0.1, sgst: 0.07, cess: 0 })
  assert.deepEqual(p, { igst: 123450, cgst: 10, sgst: 7, cess: 0 })
  assert.deepEqual(openingBody(GSTIN, '102026', p), { gstin: GSTIN, period: '102026', igst: '1234.50', cgst: '0.10', sgst: '0.07', cess: '0.00' })
  assert.deepEqual(openingToPaise(null), { igst: 0, cgst: 0, sgst: 0, cess: 0 })
})
