/**
 * TypeScript port of mool/gst (Go). Must produce identical numbers to the server,
 * because the POS prints totals before (or without) talking to aadesh.
 * Keep in sync with ../../mool/gst/compute.go.
 */
import { applyBP, mulDiv, roundToRupee } from './money.ts'

export interface GstLine {
  hsn?: string
  qty: number
  unit_price_paise: number
  discount_paise?: number
  tax_rate_bp: number
  cess_rate_bp?: number
  price_includes_tax?: boolean
}

export interface LineTax {
  gross_paise: number
  discount_paise: number
  taxable_paise: number
  cgst_paise: number
  sgst_paise: number
  igst_paise: number
  cess_paise: number
  total_paise: number
}

export interface RateTotal { tax_rate_bp: number; taxable_paise: number; tax_paise: number }

export interface GstSummary {
  inter_state: boolean
  lines: LineTax[]
  taxable_paise: number
  cgst_paise: number
  sgst_paise: number
  igst_paise: number
  cess_paise: number
  discount_paise: number
  round_off_paise: number
  grand_total_paise: number
  by_rate: RateTotal[]
}

export const GST_SLABS_BP = [0, 25, 300, 500, 1200, 1800, 2800, 4000]

export const STATES: Record<string, string> = {
  '01': 'Jammu and Kashmir', '02': 'Himachal Pradesh', '03': 'Punjab', '04': 'Chandigarh', '05': 'Uttarakhand',
  '06': 'Haryana', '07': 'Delhi', '08': 'Rajasthan', '09': 'Uttar Pradesh', '10': 'Bihar', '11': 'Sikkim',
  '12': 'Arunachal Pradesh', '13': 'Nagaland', '14': 'Manipur', '15': 'Mizoram', '16': 'Tripura', '17': 'Meghalaya',
  '18': 'Assam', '19': 'West Bengal', '20': 'Jharkhand', '21': 'Odisha', '22': 'Chhattisgarh', '23': 'Madhya Pradesh',
  '24': 'Gujarat', '26': 'Dadra and Nagar Haveli and Daman and Diu', '27': 'Maharashtra', '29': 'Karnataka',
  '30': 'Goa', '31': 'Lakshadweep', '32': 'Kerala', '33': 'Tamil Nadu', '34': 'Puducherry',
  '35': 'Andaman and Nicobar Islands', '36': 'Telangana', '37': 'Andhra Pradesh', '38': 'Ladakh',
  '97': 'Other Territory', '96': 'Other Country',
}

const GSTIN_CHARS = '0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ'
const GSTIN_RE = /^[0-9]{2}[A-Z0-9]{10}[0-9A-Z]Z[0-9A-Z]$/

/** Returns null when valid, otherwise a human-readable error. */
export function validateGSTIN(raw: string): string | null {
  const g = raw.trim().toUpperCase()
  if (g.length !== 15 || !GSTIN_RE.test(g)) return 'GSTIN must be 15 characters'
  if (!STATES[g.slice(0, 2)]) return `Unknown state code ${g.slice(0, 2)}`
  let sum = 0
  for (let i = 0; i < 14; i++) {
    const p = GSTIN_CHARS.indexOf(g[i]) * (i % 2 === 1 ? 2 : 1)
    sum += Math.floor(p / 36) + (p % 36)
  }
  if (GSTIN_CHARS[(36 - (sum % 36)) % 36] !== g[14]) return 'GSTIN checksum does not match'
  return null
}

export const isInterState = (storeState: string, pos: string) => pos !== '' && storeState !== pos

function computeLine(l: GstLine, inter: boolean): LineTax {
  const discount = l.discount_paise ?? 0
  const cessBP = l.cess_rate_bp ?? 0
  if (!(l.qty > 0)) throw new Error('quantity must be positive')
  if (l.unit_price_paise < 0 || discount < 0) throw new Error('price and discount cannot be negative')
  if (l.tax_rate_bp < 0 || l.tax_rate_bp > 10000 || cessBP < 0) throw new Error('invalid tax rate')
  const gross = Math.round(l.qty * l.unit_price_paise)
  if (discount > gross) throw new Error('discount exceeds line value')
  const net = gross - discount
  let taxable = net
  if (l.price_includes_tax) taxable = mulDiv(net, 10000, 10000 + l.tax_rate_bp + cessBP)
  const lt: LineTax = {
    gross_paise: gross, discount_paise: discount, taxable_paise: taxable,
    cgst_paise: 0, sgst_paise: 0, igst_paise: 0, cess_paise: 0, total_paise: 0,
  }
  if (inter) lt.igst_paise = applyBP(taxable, l.tax_rate_bp)
  else {
    const half = mulDiv(taxable, l.tax_rate_bp, 20000)
    lt.cgst_paise = half
    lt.sgst_paise = half
  }
  lt.cess_paise = applyBP(taxable, cessBP)
  lt.total_paise = taxable + lt.cgst_paise + lt.sgst_paise + lt.igst_paise + lt.cess_paise
  if (l.price_includes_tax && lt.total_paise !== net) {
    lt.taxable_paise += net - lt.total_paise
    lt.total_paise = net
  }
  return lt
}

/** Mirror of gst.Compute. Throws on invalid input (message prefixed with the line number). */
export function computeGST(lines: GstLine[], storeState: string, placeOfSupply = '', roundOff = true): GstSummary {
  if (!STATES[storeState]) throw new Error('invalid store state code')
  const pos = placeOfSupply || storeState
  if (!STATES[pos]) throw new Error('invalid place of supply')
  const s: GstSummary = {
    inter_state: isInterState(storeState, pos), lines: [], taxable_paise: 0, cgst_paise: 0, sgst_paise: 0,
    igst_paise: 0, cess_paise: 0, discount_paise: 0, round_off_paise: 0, grand_total_paise: 0, by_rate: [],
  }
  const byRate = new Map<number, RateTotal>()
  lines.forEach((l, i) => {
    let lt: LineTax
    try { lt = computeLine(l, s.inter_state) } catch (e) { throw new Error(`line ${i + 1}: ${(e as Error).message}`) }
    s.lines.push(lt)
    s.taxable_paise += lt.taxable_paise
    s.cgst_paise += lt.cgst_paise
    s.sgst_paise += lt.sgst_paise
    s.igst_paise += lt.igst_paise
    s.cess_paise += lt.cess_paise
    s.discount_paise += lt.discount_paise
    const rt = byRate.get(l.tax_rate_bp) ?? { tax_rate_bp: l.tax_rate_bp, taxable_paise: 0, tax_paise: 0 }
    rt.taxable_paise += lt.taxable_paise
    rt.tax_paise += lt.cgst_paise + lt.sgst_paise + lt.igst_paise
    byRate.set(l.tax_rate_bp, rt)
  })
  const total = s.taxable_paise + s.cgst_paise + s.sgst_paise + s.igst_paise + s.cess_paise
  s.grand_total_paise = total
  if (roundOff) [s.grand_total_paise, s.round_off_paise] = roundToRupee(total)
  s.by_rate = [...byRate.values()].sort((a, b) => a.tax_rate_bp - b.tax_rate_bp)
  return s
}

const ONES = ['', 'One', 'Two', 'Three', 'Four', 'Five', 'Six', 'Seven', 'Eight', 'Nine', 'Ten', 'Eleven', 'Twelve',
  'Thirteen', 'Fourteen', 'Fifteen', 'Sixteen', 'Seventeen', 'Eighteen', 'Nineteen']
const TENS = ['', '', 'Twenty', 'Thirty', 'Forty', 'Fifty', 'Sixty', 'Seventy', 'Eighty', 'Ninety']
const two = (n: number) => (n < 20 ? ONES[n] : n % 10 === 0 ? TENS[Math.floor(n / 10)] : `${TENS[Math.floor(n / 10)]} ${ONES[n % 10]}`)
const three = (n: number) => [n >= 100 ? `${ONES[Math.floor(n / 100)]} Hundred` : '', n % 100 ? two(n % 100) : ''].filter(Boolean).join(' ')
function indian(n: number): string {
  if (n === 0) return 'Zero'
  const parts: string[] = []
  if (n >= 1e7) { parts.push(`${indian(Math.floor(n / 1e7))} Crore`); n %= 1e7 }
  if (n >= 1e5) { parts.push(`${two(Math.floor(n / 1e5))} Lakh`); n %= 1e5 }
  if (n >= 1e3) { parts.push(`${two(Math.floor(n / 1e3))} Thousand`); n %= 1e3 }
  if (n > 0) parts.push(three(n))
  return parts.join(' ')
}

/** Same wording as gst.AmountInWords. */
export function amountInWords(paise: number): string {
  const neg = paise < 0
  const p = Math.abs(paise)
  let s = `Rupees ${indian(Math.floor(p / 100))}`
  if (p % 100) s += ` and ${two(p % 100)} Paise`
  s += ' Only'
  return neg ? `Minus ${s}` : s
}
