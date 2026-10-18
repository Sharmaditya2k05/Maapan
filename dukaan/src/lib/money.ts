/** Money is always integer paise. Convert only at the edges (input boxes, display). */

/** round(a*b/c), half away from zero — exact (BigInt), mirrors mool/money.MulDiv. */
export function mulDiv(a: number, b: number, c: number): number {
  if (c === 0) return 0
  const n = BigInt(Math.trunc(a)) * BigInt(Math.trunc(b))
  const d = BigInt(Math.trunc(c))
  let q = n / d
  let r = n % d
  if (r < 0n) r = -r
  const ad = d < 0n ? -d : d
  if (2n * r >= ad) q += (n < 0n) !== (d < 0n) ? -1n : 1n
  return Number(q)
}

/** amount × basis points / 10000, rounded. */
export const applyBP = (amount: number, bp: number) => mulDiv(amount, bp, 10000)

/** Round paise to nearest rupee → [rounded, roundOff]. */
export function roundToRupee(p: number): [number, number] {
  const rounded = mulDiv(p, 1, 100) * 100
  return [rounded, rounded - p]
}

export const toPaise = (rupees: number | string) => Math.round(Number(rupees || 0) * 100)
export const toRupees = (paise: number) => paise / 100

const inr = new Intl.NumberFormat('en-IN', { style: 'currency', currency: 'INR', minimumFractionDigits: 2 })
/** 12345678 → ₹1,23,456.78 */
export const formatINR = (paise: number) => inr.format((paise || 0) / 100)

/** 1800 → "18%" */
export const formatRate = (bp: number) => `${(bp / 100).toString()}%`
