/** API shapes (see ../sanchalan/docs/ARCHITECTURE.md). Money = paise, tax rate = basis points. */
import type { GstLine } from './gst'
import type { Role } from './rbac'

/** pehchaan Store. */
export interface Store { id: string; tenant_id?: string; name: string; code: string; state_code: string; gstin?: string; address?: string; phone?: string; active?: boolean }

/** Built by loadSessionUser() from pehchaan GET /v1/me + GET /v1/stores. */
export interface SessionUser {
  id: string
  name?: string
  phone: string
  role: Role
  tenant_id: string
  business_name?: string
  permissions?: string[]
  /** Stores this user may act in (all tenant stores for unscoped roles). */
  stores: Store[]
  /** The store in the access token (sid claim). */
  default_store_id?: string
}

/** pehchaan TokenPair. */
export interface TokenPair { access_token: string; refresh_token: string; expires_at?: string; token_type?: string }

/** pehchaan User. */
export interface PehchaanUser { id: string; phone: string; name?: string; email?: string; status: string }

/** pehchaan Membership. */
export interface Membership { id: string; tenant_id: string; business_name?: string; user_id: string; role: Role; store_ids: string[]; active: boolean }

/** pehchaan Session — returned by otp/verify, refresh, onboarding, switch. */
export interface PehchaanSession extends TokenPair {
  user: PehchaanUser
  tenant_id: string
  store_id: string
  role: Role
  permissions: string[]
  memberships: Membership[]
}

/** pehchaan POST /v1/auth/otp/verify: a Session, or an onboarding hand-off for a new phone. */
export interface VerifyResponse extends Partial<PehchaanSession> {
  needs_onboarding: boolean
  onboarding_token?: string
  new_user?: PehchaanUser
}

/** pehchaan GET /v1/me. */
export interface MeResponse {
  user: PehchaanUser
  tenant: { id: string; business_name: string; gstin?: string; state_code: string }
  store_id: string
  role: Role
  stores: string[] | null
  permissions: string[]
  memberships: Membership[]
}

export interface CatalogItem {
  id: string
  name: string
  sku?: string
  barcode?: string
  hsn?: string
  unit: string
  sale_price_paise: number
  mrp_paise: number
  tax_rate_bp: number
  cess_rate_bp: number
  price_includes_tax: boolean
  track_batches: boolean
  on_hand: number
}

/** bhandar Item (GET /v1/items). */
export interface Item {
  id: string
  name: string
  sku?: string
  barcode?: string
  hsn?: string
  unit: string
  category?: string
  brand?: string
  sale_price_paise: number
  mrp_paise: number
  purchase_price_paise: number
  tax_rate_bp: number
  cess_rate_bp: number
  price_includes_tax: boolean
  track_batches: boolean
  reorder_level: number
  reorder_qty: number
  status: string
}

/** bhandar Balance (GET /v1/stock). */
export interface StockBalance {
  store_id: string
  item_id: string
  item_name: string
  batch_no?: string
  expiry_date?: string
  qty: number
  avg_cost_paise: number
  value_paise: number
}

export interface Tender { mode: 'cash' | 'card' | 'upi' | 'bank' | 'credit' | 'other'; amount_paise: number; reference?: string }

export interface InvoiceLineInput extends GstLine {
  item_id?: string
  name: string
  unit?: string
  batch_no?: string
}

export type DocType = 'sale' | 'purchase' | 'sale_return' | 'purchase_return'

export interface CreateInvoiceInput {
  doc_type: DocType
  store_id?: string
  invoice_date?: string
  party_id?: string
  party?: { name: string; phone?: string; gstin?: string; state_code?: string }
  place_of_supply?: string
  supplier_invoice_no?: string
  ref_invoice_id?: string
  lines: InvoiceLineInput[]
  tenders?: Tender[]
  notes?: string
  client_uuid?: string
  device_id?: string
  number?: number
  client_created_at?: string
}

export interface InvoiceLine extends InvoiceLineInput {
  line_no: number
  unit: string
  taxable_paise: number
  cgst_paise: number
  sgst_paise: number
  igst_paise: number
  cess_paise: number
  total_paise: number
}

export interface Invoice {
  id: string
  store_id: string
  doc_type: DocType
  number: string
  fy: string
  invoice_date: string
  supplier_invoice_no?: string
  party_id?: string
  party_name: string
  party_phone?: string
  party_gstin?: string
  party_state?: string
  store_state: string
  place_of_supply: string
  inter_state: boolean
  status: 'posted' | 'cancelled'
  payment_status: 'unpaid' | 'partial' | 'paid'
  taxable_paise: number
  discount_paise: number
  cgst_paise: number
  sgst_paise: number
  igst_paise: number
  cess_paise: number
  round_off_paise: number
  grand_total_paise: number
  paid_paise: number
  amount_in_words: string
  notes?: string
  created_at: string
  lines?: InvoiceLine[]
  tenders?: Tender[]
}

export interface ListResponse<T> { items: T[]; total: number; limit: number; offset: number }

/** bahi Party. Text fields come back as "" when unset. */
export interface Party {
  id: string
  account_id: string
  name: string
  type: 'customer' | 'supplier' | 'both'
  phone: string
  email: string
  gstin: string
  state_code: string
  address: string
  credit_limit_paise: number
  credit_days: number
  gst_verified: boolean
  legal_name?: string
  archived: boolean
  /** + receivable (party owes us), − payable. */
  balance_paise: number
}

/** bahi Statement (GET /v1/parties/{id}/statement, /v1/accounts/{id}/statement). */
export interface Statement {
  account_id?: string
  account_name: string
  from: string
  to: string
  opening_balance_paise: number
  total_debit_paise: number
  total_credit_paise: number
  closing_balance_paise: number
  lines: { date: string; voucher_id: string; voucher_number: string; voucher_type: string; narration: string; reference?: string; debit_paise: number; credit_paise: number; balance_paise: number }[]
}

export interface NumberBlock { doc_type: DocType; fy: string; prefix: string; device_id: string; start_no: number; end_no: number }

/** lenden Request. status: created | paid | expired | cancelled | failed */
export interface PaymentRequest {
  id: string
  invoice_id?: string
  amount_paise: number
  paid_paise: number
  purpose: 'invoice' | 'on_account'
  kind: 'upi_qr' | 'payment_link'
  gateway: string
  status: 'created' | 'paid' | 'expired' | 'cancelled' | 'failed'
  upi_qr_image_url?: string
  upi_intent?: string
  short_url?: string
  expires_at: string
}
