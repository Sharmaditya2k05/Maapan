// Mirrors bhandar/internal/barcode/ean13_test.go — run with `node --test src/lib/stock/ean.test.ts`.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { ean13CheckDigit, isInternalEAN, isValidEAN13, normalizeCode, validateBarcode, validateHSN, validateNumberPrefix } from './ean.ts'
import { toCSV } from './csv.ts'

test('EAN-13 check digit', () => {
  assert.equal(ean13CheckDigit('400638133393'), '1')
  assert.equal(ean13CheckDigit('210000000001'), '2') // bhandar InternalEAN13(1)
  assert.equal(ean13CheckDigit('12345'), null)
  assert.equal(ean13CheckDigit('40063813339X'), null)
})

test('EAN-13 validity', () => {
  assert.equal(isValidEAN13('4006381333931'), true)
  assert.equal(isValidEAN13('2100000000012'), true)
  assert.equal(isValidEAN13('4006381333930'), false)
  assert.equal(isValidEAN13('400638133393'), false)
  assert.equal(isInternalEAN('2100000000012'), true)
  assert.equal(isInternalEAN('4006381333931'), false)
})

test('normalizeCode upper-cases and lifts UPC-A to EAN-13', () => {
  assert.equal(normalizeCode('  abc-12 '), 'ABC-12')
  assert.equal(normalizeCode('036000291452'), '0036000291452')
  assert.equal(isValidEAN13(normalizeCode('036000291452')), true)
})

test('validateBarcode only rejects bad 13-digit codes', () => {
  assert.equal(validateBarcode(''), null)
  assert.equal(validateBarcode('SKU-77'), null)
  assert.equal(validateBarcode('4006381333931'), null)
  assert.match(validateBarcode('4006381333930') ?? '', /check digit should be 1/)
  assert.notEqual(validateBarcode('X'.repeat(49)), null)
})

test('HSN and numbering prefix rules', () => {
  assert.equal(validateHSN(''), null)
  assert.equal(validateHSN('1905'), null)
  assert.equal(validateHSN('190531'), null)
  assert.equal(validateHSN('19053100'), null)
  assert.notEqual(validateHSN('190'), null)
  assert.notEqual(validateHSN('19053'), null)
  assert.equal(validateNumberPrefix('inv'), null)
  assert.equal(validateNumberPrefix('A-1'), null)
  assert.notEqual(validateNumberPrefix('-A'), null)
  assert.notEqual(validateNumberPrefix('TOOLONG'), null)
  assert.notEqual(validateNumberPrefix(''), null)
})

test('CSV quoting', () => {
  assert.equal(toCSV(['a', 'b'], [[1, 'x,y'], ['he said "hi"', null]]), 'a,b\r\n1,"x,y"\r\n"he said ""hi""",\r\n')
})
