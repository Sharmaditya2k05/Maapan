/**
 * Ledger / payments / tax shapes used by the parties, payments, bank, reports and GST screens.
 * Named after the Go structs in bahi/internal/ledger, lenden/internal/payment and kar/internal.
 * Money = integer paise; dates = YYYY-MM-DD unless noted.
 */
import type { Invoice } from './types'

/* ---------------- bahi: parties ---------------- */

/** bahi PartyInput (POST /v1/parties). */
export interface PartyInput {
  name: string
  type: 'customer' | 'supplier' | 'both'
  phone?: string
  email?: string
  gstin?: string
  state_code?: string
  address?: string
  credit_limit_paise?: number
  credit_days?: number
  opening_balance_paise?: number
  opening_type?: 'dr' | 'cr'
  opening_as_of?: string
}

/** bahi partyPatch (PATCH /v1/parties/{id}); only sent fields change. */
export interface PartyPatch {
  name?: string
  phone?: string
  email?: string
  gstin?: string
  state_code?: string
  address?: string
  credit_limit_paise?: number
  credit_days?: number
  type?: 'customer' | 'supplier' | 'both'
  archived?: boolean
}

/* ---------------- bahi: chart of accounts ---------------- */

/** bahi Account (GET /v1/accounts). `children` only with ?view=tree. */
export interface Account {
  id: string
  code: string
  name: string
  group: 'asset' | 'liability' | 'equity' | 'income' | 'expense' | string
  parent_id?: string
  is_group: boolean
  is_system: boolean
  is_cash: boolean
  is_bank: boolean
  archived: boolean
  /** debit − credit (including children for groups). */
  balance_paise: number
  children?: Account[]
}

/* ---------------- bahi: vouchers ---------------- */

export type VoucherType = 'sales' | 'purchase' | 'receipt' | 'payment' | 'journal' | 'contra' | 'credit_note' | 'debit_note' | 'production' | 'opening'
export type ManualVoucherType = 'journal' | 'receipt' | 'payment' | 'contra'

/** bahi EntryView. */
export interface EntryView {
  line_no: number
  account_id: string
  account_code: string
  account_name: string
  party_id?: string
  store_id?: string
  debit_paise: number
  credit_paise: number
}

/** bahi VoucherView (GET /v1/vouchers, /v1/vouchers/{id}, day book). */
export interface VoucherView {
  id: string
  type: VoucherType
  number: string
  date: string
  narration: string
  source: string
  source_id?: string
  source_ref?: string
  status: 'posted' | 'reversed' | string
  reversal_of?: string
  reversed_by?: string
  total_paise: number
  created_by: string
  created_at: string
  entries?: EntryView[]
}

/** posting.Entry as accepted by POST /v1/vouchers: exactly one of account_id / party_id. */
export interface VoucherEntryInput {
  account_id?: string
  party_id?: string
  store_id?: string
  debit_paise: number
  credit_paise: number
}

/** bahi manualVoucher (POST /v1/vouchers). */
export interface ManualVoucherInput {
  type: ManualVoucherType
  date: string
  narration: string
  reference?: string
  entries: VoucherEntryInput[]
}

/** bahi reverseInput (POST /v1/vouchers/{id}/reverse). */
export interface ReverseInput { reason: string; date?: string }

/** GET /v1/daybook. */
export interface DayBook { from: string; to: string; count: string; vouchers: VoucherView[] }

/* ---------------- bahi: bank & cheques ---------------- */

export type BankTxnKind = 'deposit' | 'withdrawal' | 'charges' | 'interest'

/** bahi BankTxn. */
export interface BankTxn {
  id: string
  bank_account_id: string
  kind: BankTxnKind
  amount_paise: number
  date: string
  reference?: string
  voucher_id: string
  reconciled_at?: string
}

/** bahi bankTxnInput (POST /v1/bank/transactions). */
export interface BankTxnInput {
  bank_account_id: string
  kind: BankTxnKind
  amount_paise: number
  date?: string
  reference?: string
  counter_account_id?: string
}

export type ChequeStatus = 'received' | 'issued' | 'deposited' | 'cleared' | 'bounced' | 'cancelled'

/** bahi Cheque. */
export interface Cheque {
  id: string
  direction: 'received' | 'issued'
  party_id: string
  party_name: string
  bank_account_id: string
  cheque_no: string
  cheque_date: string
  drawee_bank?: string
  amount_paise: number
  status: ChequeStatus
  voucher_id?: string
  status_date?: string
  notes?: string
}

/** bahi chequeInput (POST /v1/cheques). */
export interface ChequeInput {
  direction: 'received' | 'issued'
  party_id: string
  bank_account_id: string
  cheque_no: string
  cheque_date: string
  drawee_bank?: string
  amount_paise: number
  notes?: string
}

/** bahi chequeStatusInput (POST /v1/cheques/{id}/status). */
export interface ChequeStatusInput { status: ChequeStatus; date?: string; notes?: string }

/* ---------------- bahi: reports ---------------- */

export interface TBRow { account_id: string; code: string; name: string; group: string; debit_paise: number; credit_paise: number }
export interface TrialBalance { as_of: string; rows: TBRow[]; total_debit_paise: number; total_credit_paise: number; balanced: boolean }

export interface PLLine { code: string; name: string; amount_paise: number }
/** bahi ProfitLoss. gross = sales − returns − purchases + purchase returns − direct expenses (COGS, stock loss …). */
export interface ProfitLoss {
  from: string
  to: string
  income: PLLine[]
  expenses: PLLine[]
  total_income_paise: number
  total_expenses_paise: number
  gross_profit_paise: number
  net_profit_paise: number
}

export interface BSLine { code: string; name: string; amount_paise: number }
export interface BalanceSheet {
  as_of: string
  assets: BSLine[]
  liabilities: BSLine[]
  equity: BSLine[]
  total_assets_paise: number
  total_liabilities_paise: number
  total_equity_paise: number
  balanced: boolean
}

/** posting.Buckets. */
export interface Buckets {
  d0_30_paise: number
  d31_60_paise: number
  d61_90_paise: number
  d90_plus_paise: number
  total_paise: number
  /** settled more than billed */
  advance_paise: number
  oldest_open_date?: string
}
export interface AgingRow extends Buckets { party_id: string; party_name: string; phone?: string }
export interface AgingReport { kind: 'receivables' | 'payables'; as_of: string; rows: AgingRow[]; totals: Buckets }

export interface CashFlowLine { voucher_type: string; in_paise: number; out_paise: number }
export interface CashFlow {
  from: string
  to: string
  opening_paise: number
  lines: CashFlowLine[]
  total_in_paise: number
  total_out_paise: number
  net_paise: number
  closing_paise: number
}

/** bahi Summary (GET /v1/reports/summary). */
export interface LedgerSummary {
  sales_paise: number
  purchases_paise: number
  receivables_paise: number
  payables_paise: number
  cash_paise: number
  bank_paise: number
}

/* ---------------- bahi: operations ---------------- */

/** bahi QuarantinedEvent. payload is the full event envelope. */
export interface QuarantinedEvent {
  event_id: string
  event_type: string
  reason: string
  payload: EventEnvelope | unknown
  created_at: string
  resolved_at?: string
}

/** mool events.Envelope. */
export interface EventEnvelope {
  id: string
  type: string
  tenant_id: string
  store_id?: string
  source: string
  occurred_at: string
  data: unknown
}

/** bahi PeriodLock. locked_until "" = books open. */
export interface PeriodLock { locked_until: string; locked_by?: string; locked_at?: string }

/* ---------------- lenden: payments ---------------- */

export type PaymentMethod = 'cash' | 'cheque' | 'bank' | 'upi' | 'card' | string
export type PaymentStatus = 'captured' | 'partially_refunded' | 'refunded'

/** lenden Payment. */
export interface Payment {
  id: string
  store_id?: string
  request_id?: string
  invoice_id?: string
  party_id?: string
  party_phone?: string
  amount_paise: number
  refunded_paise: number
  short_paise: number
  excess_paise: number
  method: PaymentMethod
  gateway: string
  gateway_ref?: string
  status: PaymentStatus
  notes?: string
  idempotency_key?: string
  captured_at: string
  created_by: string
}

/** lenden ManualInput (POST /v1/payments/manual). idempotency_key is required by the service. */
export interface ManualReceiptInput {
  invoice_id?: string
  party_id?: string
  party_phone?: string
  store_id?: string
  amount_paise: number
  method: 'cash' | 'cheque' | 'bank' | 'upi' | 'card'
  reference?: string
  notes?: string
  received_at?: string
  idempotency_key: string
}

/** lenden RefundInput. amount_paise 0 = everything not yet refunded. */
export interface RefundInput { amount_paise: number; reason: string }

export type RefundStatus = 'pending' | 'unknown' | 'processed' | 'failed'

/** lenden RefundView. */
export interface RefundView {
  id: string
  payment_id: string
  amount_paise: number
  gateway_ref?: string
  status: RefundStatus
  source: 'taraju' | 'gateway'
  reason: string
  last_error?: string
  created_at: string
  processed_at?: string
}

/** lenden RefundResponse: the payment as it stands plus the refund just made (200 processed, 202 pending/unknown). */
export interface RefundResponse extends Payment { refund: RefundView }

/** lenden RequestInput (POST /v1/payment-requests). */
export interface PaymentRequestInput {
  invoice_id?: string
  party_id?: string
  party_phone?: string
  amount_paise: number
  purpose: 'invoice' | 'on_account'
  kind: 'upi_qr' | 'payment_link'
  expires_in_sec?: number
  store_id?: string
  gateway?: string
}

/** lenden Request (full shape; status includes partial). */
export interface PaymentRequestView {
  id: string
  store_id?: string
  invoice_id?: string
  party_id?: string
  party_phone?: string
  amount_paise: number
  paid_paise: number
  purpose: 'invoice' | 'on_account'
  kind: 'upi_qr' | 'payment_link'
  gateway: string
  gateway_ref?: string
  upi_qr_image_url?: string
  upi_intent?: string
  short_url?: string
  status: 'created' | 'partial' | 'paid' | 'expired' | 'cancelled' | 'failed'
  expires_at: string
  created_at: string
}

/** lenden Settlement (GET /v1/settlements). */
export interface Settlement {
  id: string
  gateway: string
  gateway_ref: string
  utr?: string
  gross_paise: number
  fee_paise: number
  fee_tax_paise: number
  refunds_paise: number
  net_paise: number
  settled_at: string
  created_at: string
}

/** GET /lenden/v1/gateways. */
export interface GatewayList { default: string; available: string[] }

/* ---------------- kar: tax ---------------- */

/** kar gstin.Result. source: gsp | cache | checksum_only. */
export interface GstinResult {
  gstin: string
  valid: boolean
  legal_name: string
  trade_name: string
  status: string
  state_code: string
  address: string
  registration_type: string
  source: 'gsp' | 'cache' | 'checksum_only' | string
  message?: string
}

/** kar HSN. */
export interface HSN { code: string; description: string; kind: string; default_rate_bp: number; cess_rate_bp: number }

/** kar Settings (GET /v1/settings). */
export interface TaxSettings {
  tenant_id: string
  state_code: string
  gstin: string
  composition: boolean
  einvoice_enabled: boolean
  eway_threshold_paise: number
  b2cl_threshold_paise: number
  updated_at: string
}

/** kar SettingsUpdate (PUT /v1/settings); only sent fields change. */
export interface TaxSettingsUpdate {
  state_code?: string
  gstin?: string
  composition?: boolean
  einvoice_enabled?: boolean
  eway_threshold_paise?: number
  b2cl_threshold_paise?: number
}

/** kar compliance.Transport (body of POST /v1/eway-bills/{id}/json). */
export interface EwayTransportInput {
  trans_mode: 'road' | 'rail' | 'air' | 'ship'
  trans_distance: number
  transporter_id?: string
  transporter_name?: string
  trans_doc_no?: string
  trans_doc_date?: string
  vehicle_no?: string
  vehicle_type?: 'R' | 'O'
  from_pincode?: number
  to_pincode?: number
}

export interface EwayResponse { invoice_id: string; below_threshold: boolean; warnings: string[]; payload: unknown }
export interface EinvoiceInput { seller_pincode?: number; buyer_pincode?: number }
export interface EinvoiceResponse { invoice_id: string; warnings?: string[]; payload: unknown }
/** GET /v1/eway-bills/{id} and /v1/einvoices/{id}. */
export interface StoredCompliance { invoice_number: string; payload: unknown; created_by: string; created_at: string }

/* kar returns (GSTN offline-tool structure; amounts in rupees as floats) */

export interface ItemDet { rt: number; txval: number; iamt: number; camt: number; samt: number; csamt: number }
export interface Itm { num: number; itm_det: ItemDet }
export interface B2BInv { inum: string; idt: string; val: number; pos: string; rchrg: string; inv_typ: string; itms: Itm[] }
export interface B2B { ctin: string; inv: B2BInv[] }
export interface B2CLInv { inum: string; idt: string; val: number; itms: Itm[] }
export interface B2CL { pos: string; inv: B2CLInv[] }
export interface B2CS { sply_ty: 'INTRA' | 'INTER'; pos: string; typ: string; rt: number; txval: number; iamt: number; camt: number; samt: number; csamt: number }
export interface CdnrNote { ntty: 'C' | 'D'; nt_num: string; nt_dt: string; val: number; pos: string; rchrg: string; inv_typ: string; itms: Itm[] }
export interface CDNR { ctin: string; nt: CdnrNote[] }
export interface HSNRow { num: number; hsn_sc: string; desc: string; uqc: string; qty: number; rt: number; txval: number; iamt: number; camt: number; samt: number; csamt: number }
export interface DocRange { num: number; from: string; to: string; totnum: number; cancel: number; net_issue: number }
export interface DocDet { doc_num: number; docs: DocRange[] }

/** kar returns.GSTR1. */
export interface GSTR1 {
  gstin: string
  fp: string
  b2b: B2B[]
  b2cl: B2CL[]
  b2cs: B2CS[]
  cdnr: CDNR[]
  hsn: { data: HSNRow[] | null }
  doc_issue: { doc_det: DocDet[] | null }
  warnings?: string[]
}

export interface TaxHead { txval?: number; iamt: number; camt: number; samt: number; csamt: number }
export interface UnregSupply { pos: string; txval: number; iamt: number }
export interface ITCRow extends TaxHead { ty: string }
export interface Taxes { igst: number; cgst: number; sgst: number; cess: number }
export interface SetOff { liability: Taxes; itc_available: Taxes; itc_utilised: Taxes; cash_payable: Taxes; itc_carry_forward: Taxes }

/** kar returns.GSTR3B. */
export interface GSTR3B {
  gstin: string
  ret_period: string
  sup_details: { osup_det: TaxHead; osup_zero: TaxHead; osup_nil_exmp: TaxHead; isup_rev: TaxHead; osup_nongst: TaxHead }
  inter_sup: { unreg_details: UnregSupply[] }
  itc_elg: { itc_avl: ITCRow[]; itc_rev: ITCRow[]; itc_net: TaxHead }
  computation: SetOff
}

/** Re-exported for pickers that hand invoices around. */
export type { Invoice }
