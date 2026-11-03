import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  allocTotal, bpPct, checkAllocations, cleanAllocations, fifoAllocate, fyCode, fyCodeOptions, fyStartYear, lastEndedFY, matchCandidates,
  parseAddresses, parseDays, servicesForPath, statusIssues, statusMessage, tdsQuarter,
} from './phase4.ts'

const bills = [
  { id: 'b2', due_date: '2026-06-30', bill_date: '2026-06-01', outstanding_paise: 5000 },
  { id: 'b1', due_date: '2026-05-31', bill_date: '2026-05-01', outstanding_paise: 3000 },
  { id: 'b0', due_date: '2026-05-31', bill_date: '2026-04-20', outstanding_paise: 1000 },
  { id: 'settled', due_date: '2026-04-30', bill_date: '2026-04-01', outstanding_paise: 0 },
]

test('fifoAllocate settles oldest due date first, ties by bill date', () => {
  assert.deepEqual(fifoAllocate(bills, 4500), [{ bill_id: 'b0', amount_paise: 1000 }, { bill_id: 'b1', amount_paise: 3000 }, { bill_id: 'b2', amount_paise: 500 }])
  assert.deepEqual(fifoAllocate(bills, 0), [])
  assert.equal(allocTotal(fifoAllocate(bills, 1_000_000)), 9000)
})

test('checkAllocations mirrors bahi 422 rules', () => {
  const out = { b0: 1000, b1: 3000 }
  assert.equal(checkAllocations([{ bill_id: 'b0', amount_paise: 1000 }], out, 1000), null)
  assert.match(checkAllocations([{ bill_id: 'b0', amount_paise: 1001 }], out, 5000) ?? '', /outstanding/)
  assert.match(checkAllocations([{ bill_id: 'b0', amount_paise: 500 }, { bill_id: 'b0', amount_paise: 1 }], out, 5000) ?? '', /twice/)
  assert.match(checkAllocations([{ bill_id: 'b0', amount_paise: 1000 }, { bill_id: 'b1', amount_paise: 1 }], out, 1000) ?? '', /more than the voucher/)
  assert.match(checkAllocations([{ bill_id: 'zz', amount_paise: 1 }], out, 5) ?? '', /not open/)
  assert.match(checkAllocations([{ bill_id: 'b0', amount_paise: 0 }], out, 5) ?? '', /positive/)
  assert.deepEqual(cleanAllocations([{ bill_id: 'a', amount_paise: 0 }, { bill_id: 'b', amount_paise: 2 }]), [{ bill_id: 'b', amount_paise: 2 }])
})

test('FY codes and quarters', () => {
  assert.equal(fyCode('2026-11-03'), '26-27')
  assert.equal(fyCode('2026-03-31'), '25-26')
  assert.equal(fyCode('2099-04-01'), '99-00')
  assert.equal(fyStartYear('25-26'), 2025)
  assert.deepEqual(fyCodeOptions('2026-11-03', 3), ['26-27', '25-26', '24-25'])
  assert.equal(lastEndedFY('2026-11-03'), '25-26')
  assert.equal(lastEndedFY('2026-02-10'), '24-25')
  assert.deepEqual(['2026-04-01', '2026-09-30', '2026-12-31', '2027-03-01'].map(tdsQuarter), [1, 2, 3, 4])
})

test('bpPct', () => {
  assert.equal(bpPct(10), '0.1%')
  assert.equal(bpPct(200), '2%')
  assert.equal(bpPct(75), '0.75%')
})

test('matchCandidates: same amount, right side, within the window, closest first', () => {
  const book = [
    { date: '2026-10-01', voucher_id: 'far', voucher_number: 'RV/1', narration: '', debit_paise: 5000, credit_paise: 0 },
    { date: '2026-10-12', voucher_id: 'near', voucher_number: 'RV/2', narration: '', debit_paise: 5000, credit_paise: 0 },
    { date: '2026-10-08', voucher_id: 'ok', voucher_number: 'RV/3', narration: '', debit_paise: 5000, credit_paise: 0 },
    { date: '2026-10-12', voucher_id: 'wrongside', voucher_number: 'PY/1', narration: '', debit_paise: 0, credit_paise: 5000 },
    { date: '2026-10-20', voucher_id: 'late', voucher_number: 'RV/4', narration: '', debit_paise: 5000, credit_paise: 0 },
  ]
  const got = matchCandidates({ date: '2026-10-13', deposit_paise: 5000, withdrawal_paise: 0 }, book).map((b) => b.voucher_id)
  assert.deepEqual(got, ['near', 'ok'])
  const out = matchCandidates({ date: '2026-10-13', deposit_paise: 0, withdrawal_paise: 5000 }, book).map((b) => b.voucher_id)
  assert.deepEqual(out, ['wrongside'])
})

test('parseAddresses normalises phones and e-mails, dedupes, reports junk', () => {
  const r = parseAddresses('98765 43210\n+91 9876543210, Ravi@Example.com; 12345\n\nbad@\n09123456789')
  assert.deepEqual(r.valid, ['+919876543210', 'ravi@example.com', '+919123456789'])
  assert.deepEqual(r.invalid, ['12345', 'bad@'])
})

test('parseDays', () => {
  assert.deepEqual(parseDays('0, 7,15 30 7'), [0, 7, 15, 30])
  assert.deepEqual(parseDays(''), [])
  assert.equal(parseDays('1,x'), null)
  assert.equal(parseDays('400'), null)
})

test('status banner: services per screen and issues', () => {
  assert.deepEqual(servicesForPath('/app/bank'), ['pehchaan', 'bahi'])
  assert.deepEqual(servicesForPath('/app/pos'), ['pehchaan', 'aadesh', 'bhandar', 'lenden'])
  assert.deepEqual(servicesForPath('/app/parties/123'), ['pehchaan', 'bahi'])
  assert.deepEqual(servicesForPath('/app'), ['pehchaan', 'bahi', 'aadesh'])
  assert.deepEqual(servicesForPath('/app/settings'), ['pehchaan'])
  const services = { pehchaan: { status: 'up' }, bahi: { status: 'starting' }, aadesh: { status: 'down' } }
  const issues = statusIssues(services, servicesForPath('/app'))
  assert.deepEqual(issues, [{ service: 'bahi', state: 'starting' }, { service: 'aadesh', state: 'down' }])
  assert.equal(statusMessage(issues), 'bahi is starting up; aadesh is not reachable. Parts of this screen may not load yet.')
  assert.deepEqual(statusIssues(undefined, ['bahi']), [])
  assert.equal(statusMessage([]), '')
})
