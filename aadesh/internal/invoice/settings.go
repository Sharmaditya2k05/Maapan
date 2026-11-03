package invoice

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/maapan/mool/db"
	"github.com/maapan/mool/gst"
	"github.com/maapan/mool/httpx"
	"github.com/maapan/mool/money"
)

// Round-off modes (billing_settings.round_off_mode).
const (
	RoundNearest = "nearest" // half up to the step (the default: ₹10.50 → ₹11)
	RoundUp      = "up"      // always up to the next step
	RoundDown    = "down"    // always down (customer-friendly)
	RoundNone    = "none"    // bill to the paisa
)

var validRoundSteps = map[int64]bool{1: true, 10: true, 50: true, 100: true, 500: true, 1000: true}

// BillingSettings is a tenant's round-off rule and the details printed on bills.
type BillingSettings struct {
	RoundOffMode      string `json:"round_off_mode"`       // nearest | up | down | none
	RoundOffStepPaise int64  `json:"round_off_step_paise"` // 1, 10, 50, 100 (₹1, default), 500, 1000
	BankName          string `json:"bank_name,omitempty"`
	BankAccountName   string `json:"bank_account_name,omitempty"`
	BankAccountNo     string `json:"bank_account_no,omitempty"`
	BankIFSC          string `json:"bank_ifsc,omitempty"`
	BankBranch        string `json:"bank_branch,omitempty"`
	UPIID             string `json:"upi_id,omitempty"`
	InvoiceTerms      string `json:"invoice_terms,omitempty"`
	InvoiceFooter     string `json:"invoice_footer,omitempty"`
	ThermalWidthMM    int    `json:"thermal_width_mm"` // 58 (2") | 80 (3")
}

// DefaultBillingSettings is what a tenant without a row gets.
func DefaultBillingSettings() BillingSettings {
	return BillingSettings{RoundOffMode: RoundNearest, RoundOffStepPaise: 100, ThermalWidthMM: 80}
}

// Validate normalises and checks the settings.
func (b *BillingSettings) Validate() error {
	if b.RoundOffMode == "" {
		b.RoundOffMode = RoundNearest
	}
	if b.RoundOffStepPaise == 0 {
		b.RoundOffStepPaise = 100
	}
	if b.ThermalWidthMM == 0 {
		b.ThermalWidthMM = 80
	}
	switch b.RoundOffMode {
	case RoundNearest, RoundUp, RoundDown, RoundNone:
	default:
		return httpx.Validation("round_off_mode must be nearest, up, down or none")
	}
	if !validRoundSteps[b.RoundOffStepPaise] {
		return httpx.Validation("round_off_step_paise must be 1, 10, 50, 100, 500 or 1000")
	}
	if b.ThermalWidthMM != 58 && b.ThermalWidthMM != 80 {
		return httpx.Validation("thermal_width_mm must be 58 or 80")
	}
	b.BankIFSC = strings.ToUpper(strings.TrimSpace(b.BankIFSC))
	if b.BankIFSC != "" && len(b.BankIFSC) != 11 {
		return httpx.Validation("bank_ifsc must be 11 characters")
	}
	if len(b.InvoiceTerms) > 2000 || len(b.InvoiceFooter) > 500 {
		return httpx.Validation("invoice_terms ≤ 2000 and invoice_footer ≤ 500 characters")
	}
	return nil
}

// RoundTotal applies a round-off rule to a total in paise and returns the
// rounded total and the round-off (rounded − total).
func RoundTotal(total int64, mode string, step int64) (int64, int64) {
	if step <= 1 || mode == RoundNone {
		return total, 0
	}
	var r int64
	switch mode {
	case RoundUp:
		r = floorDiv(total+step-1, step) * step
	case RoundDown:
		r = floorDiv(total, step) * step
	default:
		r = money.MulDiv(total, 1, step) * step
	}
	return r, r - total
}

func floorDiv(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

// applyRoundOff re-rounds a summary computed without rounding. disabled is the
// per-document "round_off": false switch.
func applyRoundOff(sum *gst.Summary, bs BillingSettings, disabled bool) {
	if disabled {
		return
	}
	sum.GrandPaise, sum.RoundOffPaise = RoundTotal(sum.GrandPaise, bs.RoundOffMode, bs.RoundOffStepPaise)
}

// BillingSettings loads a tenant's settings (defaults when none are saved).
func (s *Service) BillingSettings(ctx context.Context, tenantID string) (BillingSettings, error) {
	return loadBillingSettings(ctx, s.DB, tenantID)
}

func loadBillingSettings(ctx context.Context, q db.Querier, tenantID string) (BillingSettings, error) {
	b := DefaultBillingSettings()
	var bank, acctName, acct, ifsc, branch, upi, terms, footer *string
	err := q.QueryRow(ctx, `SELECT round_off_mode, round_off_step_paise, bank_name, bank_account_name, bank_account_no, bank_ifsc,
		bank_branch, upi_id, invoice_terms, invoice_footer, thermal_width_mm FROM billing_settings WHERE tenant_id=$1`, tenantID).
		Scan(&b.RoundOffMode, &b.RoundOffStepPaise, &bank, &acctName, &acct, &ifsc, &branch, &upi, &terms, &footer, &b.ThermalWidthMM)
	if db.IsNotFound(err) {
		return b, nil
	}
	if err != nil {
		return b, err
	}
	b.BankName, b.BankAccountName, b.BankAccountNo, b.BankIFSC = deref(bank), deref(acctName), deref(acct), deref(ifsc)
	b.BankBranch, b.UPIID, b.InvoiceTerms, b.InvoiceFooter = deref(branch), deref(upi), deref(terms), deref(footer)
	return b, nil
}

// SetBillingSettings saves a tenant's settings.
func (s *Service) SetBillingSettings(ctx context.Context, tenantID string, b BillingSettings) (BillingSettings, error) {
	if err := b.Validate(); err != nil {
		return b, err
	}
	err := db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO billing_settings (tenant_id, round_off_mode, round_off_step_paise, bank_name, bank_account_name,
			bank_account_no, bank_ifsc, bank_branch, upi_id, invoice_terms, invoice_footer, thermal_width_mm, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12, now())
			ON CONFLICT (tenant_id) DO UPDATE SET round_off_mode=EXCLUDED.round_off_mode, round_off_step_paise=EXCLUDED.round_off_step_paise,
			  bank_name=EXCLUDED.bank_name, bank_account_name=EXCLUDED.bank_account_name, bank_account_no=EXCLUDED.bank_account_no,
			  bank_ifsc=EXCLUDED.bank_ifsc, bank_branch=EXCLUDED.bank_branch, upi_id=EXCLUDED.upi_id, invoice_terms=EXCLUDED.invoice_terms,
			  invoice_footer=EXCLUDED.invoice_footer, thermal_width_mm=EXCLUDED.thermal_width_mm, updated_at=now()`,
			tenantID, b.RoundOffMode, b.RoundOffStepPaise, nilIfEmpty(b.BankName), nilIfEmpty(b.BankAccountName), nilIfEmpty(b.BankAccountNo),
			nilIfEmpty(b.BankIFSC), nilIfEmpty(b.BankBranch), nilIfEmpty(b.UPIID), nilIfEmpty(b.InvoiceTerms), nilIfEmpty(b.InvoiceFooter), b.ThermalWidthMM)
		return err
	})
	return b, err
}
