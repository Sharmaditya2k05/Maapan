import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  addDays, balanceLabel, chequeNextStatuses, creditUsagePct, detectPreset, endOfMonth, fmtDrCr, fyMonths, fyYearOf, gstPeriod,
  mobile10, presetRange, returnMonths, toE164IN, voucherBalance, waLink,
} from './util.ts'

test('date arithmetic stays on calendar dates', () => {
  assert.equal(addDays('2026-03-01', -1), '2026-02-28')
  assert.equal(addDays('2024-02-28', 1), '2024-02-29')
  assert.equal(endOfMonth('2026-02-10'), '2026-02-28')
  assert.equal(endOfMonth('2026-04-01'), '2026-04-30')
  assert.equal(fyYearOf('2026-03-31'), 2025)
  assert.equal(fyYearOf('2026-04-01'), 2026)
})

test('presets resolve against today', () => {
  const today = '2026-10-24'
  assert.deepEqual(presetRange('today', today), { from: today, to: today })
  assert.deepEqual(presetRange('yesterday', today), { from: '2026-10-23', to: '2026-10-23' })
  assert.deepEqual(presetRange('this_week', today), { from: '2026-10-19', to: today }) // Saturday → Monday 19th
  assert.deepEqual(presetRange('this_month', today), { from: '2026-10-01', to: today })
  assert.deepEqual(presetRange('last_month', today), { from: '2026-09-01', to: '2026-09-30' })
  assert.deepEqual(presetRange('this_quarter', today), { from: '2026-10-01', to: today })
  assert.deepEqual(presetRange('this_fy', today), { from: '2026-04-01', to: today })
  assert.deepEqual(presetRange('last_fy', today), { from: '2025-04-01', to: '2026-03-31' })
  assert.deepEqual(presetRange('last_month', '2026-01-15'), { from: '2025-12-01', to: '2025-12-31' })
  assert.equal(detectPreset({ from: '2026-04-01', to: today }, today, ['today', 'this_fy', 'custom']), 'this_fy')
  assert.equal(detectPreset({ from: '2026-04-02', to: today }, today, ['today', 'this_fy', 'custom']), 'custom')
})

test('fyMonths clips at today', () => {
  const ms = fyMonths(2026, '2026-10-24')
  assert.equal(ms.length, 7)
  assert.deepEqual(ms[0], { key: '2026-04', label: 'Apr 2026', from: '2026-04-01', to: '2026-04-30' })
  assert.deepEqual(ms[6], { key: '2026-10', label: 'Oct 2026', from: '2026-10-01', to: '2026-10-24' })
  assert.equal(fyMonths(2025, '2026-10-24').length, 12)
  assert.equal(fyMonths(2025, '2026-10-24')[11].key, '2026-03')
})

test('GST period helpers', () => {
  assert.equal(gstPeriod('2026-10'), '102026')
  assert.equal(gstPeriod('2026-04'), '042026')
  const rm = returnMonths('2026-10-24')
  assert.equal(rm[0].value, '2026-10')
  assert.equal(rm[rm.length - 1].value, '2025-04')
})

test('balance wording', () => {
  assert.equal(balanceLabel(150000).text, '₹1,500.00 to collect')
  assert.equal(balanceLabel(-2050).text, '₹20.50 to pay')
  assert.equal(balanceLabel(0).text, 'Settled')
  assert.equal(fmtDrCr(100), '₹1.00 Dr')
  assert.equal(fmtDrCr(-100), '₹1.00 Cr')
  assert.equal(creditUsagePct(50000, 100000), 50)
  assert.equal(creditUsagePct(-500, 100000), 0)
  assert.equal(creditUsagePct(200000, 100000), 100)
  assert.equal(creditUsagePct(200000, 0), 0)
})

test('phones normalise to Indian mobiles', () => {
  assert.equal(mobile10('98765 43210'), '9876543210')
  assert.equal(mobile10('+91-9876543210'), '9876543210')
  assert.equal(mobile10('09876543210'), '9876543210')
  assert.equal(mobile10('1234567890'), null)
  assert.equal(mobile10(''), null)
  assert.equal(toE164IN('9876543210'), '+919876543210')
  assert.equal(waLink('9876543210', 'Hi & bye'), 'https://wa.me/919876543210?text=Hi%20%26%20bye')
  assert.equal(waLink('12', 'x'), null)
})

test('cheque transitions mirror bahi', () => {
  assert.deepEqual(chequeNextStatuses('received', 'received'), ['deposited', 'cancelled'])
  assert.deepEqual(chequeNextStatuses('received', 'deposited'), ['cleared', 'bounced'])
  assert.deepEqual(chequeNextStatuses('received', 'cleared'), [])
  assert.deepEqual(chequeNextStatuses('issued', 'issued'), ['cleared', 'bounced', 'cancelled'])
})

test('voucher balance check', () => {
  const ok = voucherBalance([{ account_id: 'a', debit_paise: 100, credit_paise: 0 }, { party_id: 'p', debit_paise: 0, credit_paise: 100 }])
  assert.equal(ok.ok, true)
  assert.equal(ok.diff, 0)
  const off = voucherBalance([{ account_id: 'a', debit_paise: 100, credit_paise: 0 }, { party_id: 'p', debit_paise: 0, credit_paise: 90 }])
  assert.equal(off.ok, false)
  assert.equal(off.diff, 10)
  const both = voucherBalance([{ account_id: 'a', debit_paise: 100, credit_paise: 100 }, { account_id: 'b', debit_paise: 100, credit_paise: 100 }])
  assert.equal(both.ok, false)
  const missing = voucherBalance([{ debit_paise: 100, credit_paise: 0 }, { account_id: 'b', debit_paise: 0, credit_paise: 100 }])
  assert.equal(missing.ok, false)
})
