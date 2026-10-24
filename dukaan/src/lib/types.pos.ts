/**
 * Shapes used by the counter and billing screens that src/lib/types.ts does not
 * carry yet. Named after the Go structs they mirror. Money = paise, rates = bp.
 *
 * The services decode JSON with DisallowUnknownFields, so request bodies must
 * contain only the keys listed on the *Input types here (see lib/pos/invoice.ts).
 */
import type { GstLine, LineTax } from './gst'
import type { CatalogItem, CreateInvoiceInput, Invoice, InvoiceLineInput, Item, Party, PaymentRequest, Tender } from './types'

export type TaxCategory = 'taxable' | 'exempt' | 'nil_rated' | 'non_gst'

/** aadesh LineInput = gst.Line + item fields. */
export interface PosLineInput extends InvoiceLineInput {
  discount_bp?: number
  cess_per_unit_paise?: number
  tax_category?: TaxCategory
}

/** CreateInput with the richer line type (assignable to CreateInvoiceInput). */
export interface PosCreateInvoiceInput extends Omit<CreateInvoiceInput, 'lines'> {
  lines: PosLineInput[]
}

/** gst.Line as POST /v1/invoices/preview expects it (no item fields). */
export interface PreviewLine extends GstLine {
  discount_bp?: number
  cess_per_unit_paise?: number
  tax_category?: TaxCategory
}

/** aadesh previewInput. */
export interface PreviewInput {
  store_state: string
  place_of_supply: string
  supply_category?: CreateInvoiceInput['supply_category']
  reverse_charge?: boolean
  invoice_discount_paise?: number
  invoice_discount_bp?: number
  tcs_bp?: number
  invoice_date?: string
  lines: PreviewLine[]
  round_off?: boolean
  tenders?: Tender[]
}

/** mool/gst LineTax (server). */
export interface ServerLineTax extends LineTax { tax_category: string; rate_bp: number }
/** mool/gst RateTotal (server). */
export interface ServerRateTotal { tax_rate_bp: number; tax_category: string; taxable_paise: number; tax_paise: number; cess_paise: number }
/** mool/gst Summary (server). */
export interface ServerSummary {
  inter_state: boolean
  supply_category?: string
  reverse_charge?: boolean
  bill_of_supply?: boolean
  lines: ServerLineTax[]
  taxable_paise: number
  exempt_paise?: number
  nil_rated_paise?: number
  non_gst_paise?: number
  zero_rated_paise?: number
  cgst_paise: number
  sgst_paise: number
  igst_paise: number
  cess_paise: number
  tax_paise: number
  discount_paise: number
  invoice_discount_paise?: number
  tcs_paise?: number
  round_off_paise: number
  grand_total_paise: number
  by_rate: ServerRateTotal[]
}
/** POST /v1/invoices/preview response. */
export interface PreviewResponse { summary: ServerSummary; amount_in_words: string; paid_paise?: number; change_paise?: number; credit_paise?: number }

/** bhandar AltUnit. */
export interface AltUnit { unit: string; factor: number }
/** bhandar Item with every field the API returns. */
export interface ItemFull extends Item { plu?: string; cess_per_unit_paise?: number; tax_category?: TaxCategory; alt_units?: AltUnit[] }
/** bhandar CatalogItem with every field the API returns. */
export interface CatalogItemFull extends CatalogItem { cess_per_unit_paise?: number; tax_category?: TaxCategory; alt_units?: AltUnit[] }
/** bhandar ScaleRead: what a weighing-scale barcode encoded. */
export interface ScaleRead { kind: 'price' | 'weight'; value: number }
/** bhandar CodeLookup: GET /v1/barcodes/{code}. */
export interface CodeLookup { item: ItemFull; scale?: ScaleRead }
/** bhandar Batch: GET /v1/items/{id}/batches → { batches }. */
export interface Batch { id: string; item_id: string; batch_no: string; mfg_date?: string; expiry_date?: string; mrp_paise?: number; qty: number }

/** lenden Request with the fields types.ts omits. */
export interface PaymentRequestFull extends Omit<PaymentRequest, 'status'> {
  status: PaymentRequest['status'] | 'partial'
  gateway_ref?: string
  store_id?: string
  party_id?: string
  party_phone?: string
  created_at?: string
}
/** lenden RequestInput. */
export interface PaymentRequestInput {
  invoice_id?: string
  party_id?: string
  party_phone?: string
  amount_paise: number
  purpose: 'invoice' | 'on_account'
  kind: 'upi_qr' | 'payment_link'
  expires_in_sec?: number
  store_id?: string
}

/** aadesh Invoice with the fields types.ts omits. */
export interface InvoiceFull extends Invoice { ref_invoice_id?: string; client_uuid?: string; created_by?: string }

/** bahi PartyInput (quick-add from the counter). */
export interface PartyInput {
  name: string
  type?: Party['type']
  phone?: string
  email?: string
  gstin?: string
  state_code?: string
  address?: string
  credit_limit_paise?: number
  credit_days?: number
}

/** Details on the 422 aadesh returns when a credit sale breaks the party's limit. */
export interface CreditLimitDetails { credit_limit_paise: number; balance_paise: number; bill_credit_paise: number }

/** One line in the counter cart or a billing editor. Everything the server needs plus display-only fields. */
export interface CartLine {
  key: string
  item_id?: string
  name: string
  hsn?: string
  unit: string
  batch_no?: string
  qty: number
  unit_price_paise: number
  /** 0 when the item has no MRP (free-text lines, services). */
  mrp_paise: number
  discount_bp: number
  discount_paise: number
  tax_rate_bp: number
  cess_rate_bp: number
  cess_per_unit_paise: number
  price_includes_tax: boolean
  tax_category: TaxCategory
  track_batches: boolean
  /** Not in the item master. */
  free_text?: boolean
}

/** A parked counter bill (localStorage). */
export interface HeldBill {
  id: string
  store_id: string
  held_at: string
  lines: CartLine[]
  party?: Party
  notes: string
  total_paise: number
}

/** The bill the counter made last: reprintable even when it only exists in the offline queue. */
export interface LastBill { invoice: InvoiceFull; queued: boolean }

/** Hand-offs to /app/purchases/new use PurchaseDraft from types.more.ts (sessionStorage PURCHASE_DRAFT_KEY). */
