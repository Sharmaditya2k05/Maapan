/**
 * Phase 3 shapes: GST registrations (pehchaan) and kar compliance — e-invoice / e-way bill
 * records, GSTR-2B reconciliation and IMS, ITC opening, GSP portal users, HSN import.
 * Named after the Go structs they mirror. Amounts from kar returns / recon are rupees as
 * JSON numbers (two decimals, never float maths: convert with Math.round(v * 100));
 * `*_paise` fields are integer paise.
 */

/* ---------------- pehchaan registrations ---------------- */

/** pehchaan identity.Registration (GET /v1/registrations). gstin "" = the unregistered identity. */
export interface Registration {
  id: string
  tenant_id: string
  gstin: string
  legal_name: string
  trade_name: string
  state_code: string
  address: string
  city: string
  pin: string
  phone: string
  email: string
  composition: boolean
  einvoice_applicable: boolean
  is_primary: boolean
  status: 'active' | 'inactive' | string
  store_count: number
  created_at: string
  updated_at: string
}

/** pehchaan RegistrationInput (POST /v1/registrations, PUT /v1/registrations/{id}). */
export interface RegistrationInput {
  gstin?: string
  legal_name?: string
  trade_name?: string
  state_code?: string
  address?: string
  city?: string
  pin?: string
  phone?: string
  email?: string
  composition?: boolean
  einvoice_applicable?: boolean
  is_primary?: boolean
  status?: 'active' | 'inactive'
}

/* ---------------- kar e-invoice / e-way bill ---------------- */

export type EInvoiceStatus = 'pending' | 'generated' | 'cancelled' | 'failed'
export type EWayStatus = 'generated' | 'cancelled' | 'failed'

/** Decoded signed QR (gsp.QRData). */
export interface EInvoiceQR {
  SellerGstin: string
  BuyerGstin: string
  DocNo: string
  DocTyp: string
  DocDt: string
  TotInvVal: number
  ItemCnt: number
  MainHsnCode: string
  Irn: string
  IrnDt: string
}

/** kar compliance.EInvoiceRecord (GET /v1/einvoices[/{invoice_id}], POST generate / cancel). */
export interface EInvoiceRecord {
  invoice_id: string
  invoice_number: string
  store_id?: string
  gstin: string
  doc_type: 'INV' | 'CRN' | 'DBN' | string
  doc_date?: string
  party_gstin?: string
  party_name?: string
  supply_type?: string
  total_paise: number
  status: EInvoiceStatus | string
  irn?: string
  ack_no?: string
  ack_date?: string
  signed_qr?: string
  signed_invoice?: string
  error_code?: string
  error_message?: string
  attempts: number
  provider?: string
  triggered_by: string
  cancelled_at?: string
  cancel_reason?: number
  cancel_remark?: string
  /** AckDt + 24 h, only while generated. */
  cancellable_until?: string
  created_by: string
  created_at: string
  updated_at: string
  payload?: unknown
  qr?: EInvoiceQR
}

/** One entry of an e-way bill's history. */
export interface EWayHistory { at: string; by: string; action: string; ewb_no?: string; vehicle_no?: string; valid_upto?: string; remark?: string }

/** kar compliance.EWayRecord (GET /v1/eway-bills[/{invoice_id}], POST generate / part-b / extend / cancel). */
export interface EWayRecord {
  invoice_id: string
  invoice_number: string
  /** aadesh document type (sale | sale_return | purchase | purchase_return); empty on bills made before kar stored it */
  doc_type?: string
  store_id?: string
  gstin: string
  doc_date?: string
  irn?: string
  status: EWayStatus | string
  ewb_no?: string
  ewb_date?: string
  valid_upto?: string
  /** "1" road, "2" rail, "3" air, "4" ship. */
  mode?: string
  vehicle_no?: string
  vehicle_type?: 'R' | 'O' | string
  distance_km: number
  transporter_id?: string
  extensions: number
  below_threshold: boolean
  warnings?: string[]
  error_code?: string
  error_message?: string
  provider?: string
  cancelled_at?: string
  cancel_reason?: number
  cancel_remark?: string
  history?: EWayHistory[]
  created_by: string
  created_at: string
  updated_at: string
  payload?: unknown
}

/** Old name kept for callers of GET /v1/einvoices/{id} and /v1/eway-bills/{id}. */
export type StoredCompliance = EInvoiceRecord | EWayRecord

export type EWayMode = '1' | '2' | '3' | '4'

/** POST /v1/eway-bills/{invoice_id} (compliance.Transport). mode is a STRING. */
export interface EWayGenerateInput {
  mode: EWayMode
  distance_km: number
  vehicle_no?: string
  vehicle_type?: 'R' | 'O'
  transporter_id?: string
  transporter_name?: string
  trans_doc_no?: string
  trans_doc_date?: string
  from_pincode?: number
  to_pincode?: number
}

/** PUT /v1/eway-bills/{invoice_id}/part-b. reason_code is a string "1"-"4". */
export interface EWayPartBInput {
  mode: EWayMode
  vehicle_no?: string
  vehicle_type?: 'R' | 'O'
  trans_doc_no?: string
  trans_doc_date?: string
  from_place: string
  from_state: string
  reason_code: '1' | '2' | '3' | '4'
  reason_remark?: string
}

/** POST /v1/eway-bills/{invoice_id}/extend. reason_code is a number (1, 2, 4, 5, 99). */
export interface EWayExtendInput {
  from_place: string
  from_state: string
  from_pincode: number
  remaining_distance_km: number
  consignment_status: 'M' | 'T'
  transit_type?: 'R' | 'W' | 'O'
  reason_code: number
  remark: string
  mode?: EWayMode
  vehicle_no?: string
  trans_doc_no?: string
  trans_doc_date?: string
}

/** POST /v1/einvoices/{id}/cancel and /v1/eway-bills/{id}/cancel. */
export interface ComplianceCancelInput { reason_code: number; remark: string }

/** POST /v1/einvoices/{id}/json and /v1/eway-bills/{id}/json (download only, nothing registered). */
export interface ComplianceJSON { invoice_id: string; warnings?: string[]; below_threshold?: boolean; payload: unknown }

/* ---------------- GSTR-2B / IMS ---------------- */

export type TwoBStatus = 'matched' | 'mismatch' | 'missing_in_books' | 'missing_in_2b'
export type IMSAction = 'accept' | 'reject' | 'pending'

/** recon.Doc — one GSTR-2B document. */
export interface TwoBDoc {
  doc_key: string
  section: 'b2b' | 'b2ba' | 'cdnr' | 'cdnra' | string
  supplier_gstin: string
  supplier_name: string
  doc_type: 'INV' | 'CN' | 'DN' | string
  doc_no: string
  doc_date: string
  supply_type: string
  pos: string
  reverse_charge: boolean
  value: number
  taxable: number
  igst: number
  cgst: number
  sgst: number
  cess: number
  /** Y | N | T (temporarily unavailable). */
  itc_available: string
  reason?: string
  supplier_period?: string
  supplier_filed_on?: string
  irn?: string
}

/** recon.Purchase — a purchase bill / debit note from our books. */
export interface ReconPurchase {
  id: string
  doc_type: 'purchase' | 'purchase_return' | string
  number: string
  invoice_date: string
  supplier_gstin: string
  supplier_name: string
  supplier_invoice_no?: string
  supplier_invoice_date?: string
  reverse_charge?: boolean
  taxable: number
  igst: number
  cgst: number
  sgst: number
  cess: number
}

export interface ReconDiff { field: string; books: unknown; gstr2b: unknown }
export interface IMSRecord { action: IMSAction; remark?: string; actor: string; acted_at: string }

/** recon.Result — one row of the reconciliation. */
export interface ReconResult {
  status: TwoBStatus
  doc_key?: string
  doc?: TwoBDoc
  purchase?: ReconPurchase
  diffs?: ReconDiff[]
  notes?: string[]
  ims?: IMSRecord
}

export interface Heads4 { igst: number; cgst: number; sgst: number; cess: number }
export interface ITCBucket extends Heads4 { docs: number }

/** recon.ITCSummary. */
export interface ITCSummary {
  accepted: ITCBucket
  deemed_accepted: ITCBucket
  pending: ITCBucket
  rejected: ITCBucket
  not_available: ITCBucket
  reverse_charge: Heads4
  all_other: Heads4
  available: Heads4
}

export interface TwoBImport { id: string; source: 'upload' | 'gsp' | string; generated_on?: string; doc_count: number; skipped?: string[]; imported_by: string; imported_at: string }

/** GET /v1/returns/gstr2b. */
export interface Reconciliation {
  gstin: string
  period: string
  import: TwoBImport | null
  counts: Partial<Record<TwoBStatus, number>>
  itc: ITCSummary
  items: ReconResult[]
  warning?: string
}

/** GET|PUT /v1/returns/itc-opening (rupees). */
export interface ITCOpening { gstin: string; period: string; set?: boolean; igst: number; cgst: number; sgst: number; cess: number }

/* ---------------- settings ---------------- */

/** GET /v1/settings/gsp-credentials[/{gstin}] (masked). */
export interface GspCredentialSummary {
  gstin: string
  einvoice_username_masked: string
  has_einvoice_password: boolean
  eway_username_masked: string
  has_eway_password: boolean
  updated_by: string
  updated_at: string
}

/** PUT /v1/settings/gsp-credentials/{gstin}. Empty password keeps the stored one; usernames are replaced. */
export interface GspCredentialInput { einvoice_username: string; einvoice_password: string; eway_username: string; eway_password: string }

/** POST /v1/hsn/import → hsn.Report. */
export interface HsnImportReport { rows: number; inserted: number; updated: number; closed: number; rejected: number; errors: { line: number; code?: string; error: string }[] | null }
