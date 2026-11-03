import { test } from 'node:test'
import assert from 'node:assert/strict'
import { acceptImportFile, detectSource, distinctColumn, mapToText, textToMap } from './params.ts'

test('accepted import files', () => {
  for (const n of ['a.xlsx', 'b.XLS', 'c.csv', 'd.xml', 'e.tsv', 'f.txt', 'g.xlsm']) assert.equal(acceptImportFile(n), true, n)
  for (const n of ['a.pdf', 'b.xlsb', 'c']) assert.equal(acceptImportFile(n), false, n)
  assert.equal(detectSource('Master.XML'), 'tally')
  assert.equal(detectSource('x.xlsx'), '')
})

test('map text round trip', () => {
  const text = mapToText({ 'Main Godown': 'id-1', Counter: 'id-2' })
  assert.equal(text, 'Counter = id-2\nMain Godown = id-1')
  assert.deepEqual(textToMap(text), { map: { Counter: 'id-2', 'Main Godown': 'id-1' }, bad: [] })
  const r = textToMap('# comment\nOutput CGST: 2210\nDuties & Taxes\t2200\n\nno separator\n = 3')
  assert.deepEqual(r.map, { 'Output CGST': '2210', 'Duties & Taxes': '2200' })
  assert.deepEqual(r.bad, [5, 6])
  assert.equal(mapToText(undefined), '')
})

test('distinctColumn', () => {
  assert.deepEqual(distinctColumn(['Item', 'Godown'], [['a', 'Main'], ['b', ' Counter '], ['c', 'Main'], ['d', '']], 'Godown'), ['Counter', 'Main'])
  assert.deepEqual(distinctColumn(['Item'], [['a']], 'Godown'), [])
  assert.deepEqual(distinctColumn(['Item'], [['a']], undefined), [])
})
