/**
 * Barcode and HSN helpers, mirroring bhandar/internal/barcode/ean13.go and the
 * item validation in bhandar/internal/inventory/items.go. Pure: no React, no I/O.
 */

const digitsOnly = (s: string) => /^[0-9]+$/.test(s)

/** EAN-13 check digit for 12 digits (weights 1,3,1,3,… from the left); null unless exactly 12 digits. */
export function ean13CheckDigit(first12: string): string | null {
  if (first12.length !== 12 || !digitsOnly(first12)) return null
  let sum = 0
  for (let i = 0; i < 12; i++) sum += Number(first12[i]) * (i % 2 === 1 ? 3 : 1)
  return String((10 - (sum % 10)) % 10)
}

/** True for 13 digits whose check digit matches. */
export function isValidEAN13(code: string): boolean {
  return code.length === 13 && digitsOnly(code) && ean13CheckDigit(code.slice(0, 12)) === code[12]
}

/** Trim + upper-case; a 12-digit UPC-A becomes its EAN-13 form (leading 0, same check digit). Same as the server. */
export function normalizeCode(code: string): string {
  const s = code.trim().toUpperCase()
  return s.length === 12 && digitsOnly(s) ? `0${s}` : s
}

/** In-store code (GS1 restricted-circulation prefix 20–29); Taraju generates 21… codes. */
export const isInternalEAN = (code: string) => code.length === 13 && digitsOnly(code) && code[0] === '2'

/**
 * Client-side copy of the server's barcode rule: any text up to 48 chars is accepted,
 * but a 13-digit numeric code must carry a correct EAN-13 check digit.
 * Returns null when fine, else the message to show. Empty is fine (the server generates one).
 */
export function validateBarcode(raw: string): string | null {
  const code = normalizeCode(raw)
  if (code === '') return null
  if (code.length > 48) return 'Barcode is too long (max 48 characters)'
  if (digitsOnly(code) && code.length === 13 && !isValidEAN13(code)) {
    const fix = ean13CheckDigit(code.slice(0, 12))
    return `Not a valid EAN-13: check digit should be ${fix}`
  }
  return null
}

/** HSN/SAC must be 4, 6 or 8 digits (empty allowed). */
export function validateHSN(hsn: string): string | null {
  const h = hsn.trim()
  if (h === '') return null
  return /^[0-9]{4}([0-9]{2}([0-9]{2})?)?$/.test(h) ? null : 'HSN must be 4, 6 or 8 digits'
}

/** Prefix rule from aadesh: 1–4 chars of A-Z, 0-9 or '-', not starting with '-'. */
export function validateNumberPrefix(prefix: string): string | null {
  const p = prefix.trim().toUpperCase()
  return /^[A-Z0-9][A-Z0-9-]{0,3}$/.test(p) ? null : "Prefix must be 1–4 characters of A–Z, 0–9 or '-' (cannot start with '-')"
}
