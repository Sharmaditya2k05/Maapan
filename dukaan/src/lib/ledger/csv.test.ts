import { test } from 'node:test'
import assert from 'node:assert/strict'
import { csvEscape, csvFileName, csvRupees, toCSV } from './csv.ts'

test('csvEscape quotes only what needs it', () => {
  assert.equal(csvEscape('plain'), 'plain')
  assert.equal(csvEscape('a,b'), '"a,b"')
  assert.equal(csvEscape('say "hi"'), '"say ""hi"""')
  assert.equal(csvEscape('line\nbreak'), '"line\nbreak"')
  assert.equal(csvEscape(' padded '), '" padded "')
  assert.equal(csvEscape(null), '')
  assert.equal(csvEscape(undefined), '')
  assert.equal(csvEscape(0), '0')
  assert.equal(csvEscape(false), 'false')
})

test('toCSV builds CRLF rows with a BOM', () => {
  const out = toCSV(['Date', 'Narration', 'Amount'], [['2026-04-01', 'Opening, FY', 100.5], ['2026-04-02', 'Ramesh "RK" Kumar', -20]])
  assert.equal(out, '﻿Date,Narration,Amount\r\n2026-04-01,"Opening, FY",100.5\r\n2026-04-02,"Ramesh ""RK"" Kumar",-20\r\n')
  assert.equal(toCSV(['a'], [], false), 'a\r\n')
})

test('csvRupees renders paise as plain decimals', () => {
  assert.equal(csvRupees(123450), '1234.50')
  assert.equal(csvRupees(5), '0.05')
  assert.equal(csvRupees(-100001), '-1000.01')
  assert.equal(csvRupees(0), '0.00')
  assert.equal(csvRupees(0, true), '')
  assert.equal(csvRupees(undefined), '0.00')
})

test('csvFileName slugs titles', () => {
  assert.equal(csvFileName('Party Statement: Ramesh & Co', '2026-04-01-2026-10-24'), 'party-statement-ramesh-co-2026-04-01-2026-10-24.csv')
  assert.equal(csvFileName('***'), 'export.csv')
})
