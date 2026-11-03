package ledger

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/maapan/bahi/internal/posting"
	"github.com/maapan/mool/auth"
	"github.com/maapan/mool/db"
	"github.com/maapan/mool/events"
	"github.com/maapan/mool/httpx"
)

// TDS / TCS. Every bill or payment tested against a section is recorded in
// tds_entries (even below the threshold) so the per-party FY aggregate is the
// sum of recorded bases. A deduction posts Dr Party / Cr TDS Payable (or, for
// TDS customers deduct from us, Dr TDS Receivable / Cr Party); a challan
// settles the payable to Bank; 26Q is exported from the entries.

// TDSSettings is GET/PUT /v1/settings/tds.
type TDSSettings struct {
	TAN          string `json:"tan"`
	DeductorName string `json:"deductor_name"`
	Auto194Q     bool   `json:"auto_194q"` // deduct 194Q on purchase bills from suppliers tagged 194Q
}

func (s *Service) tdsSettings(ctx context.Context, q db.Querier, tenantID string) (TDSSettings, error) {
	var t TDSSettings
	err := q.QueryRow(ctx, `SELECT COALESCE(tan,''), COALESCE(deductor_name,''), auto_194q FROM tds_settings WHERE tenant_id=$1`,
		tenantID).Scan(&t.TAN, &t.DeductorName, &t.Auto194Q)
	if db.IsNotFound(err) {
		return TDSSettings{}, nil
	}
	return t, err
}

func (s *Service) handleGetTDSSettings(w http.ResponseWriter, r *http.Request) error {
	t, err := s.tdsSettings(r.Context(), s.DB, auth.FromContext(r.Context()).TenantID)
	if err != nil {
		return err
	}
	return httpx.OK(w, t)
}

func (in *TDSSettings) Validate() error {
	in.TAN = strings.ToUpper(strings.TrimSpace(in.TAN))
	if in.TAN != "" && !validTAN(in.TAN) {
		return httpx.Validation("tan must look like AAAA99999A")
	}
	return nil
}

func validTAN(t string) bool {
	if len(t) != 10 {
		return false
	}
	for i := 0; i < 10; i++ {
		c := t[i]
		digit := c >= '0' && c <= '9'
		if (i >= 4 && i < 9) != digit || !digit && (c < 'A' || c > 'Z') {
			return false
		}
	}
	return true
}

func (s *Service) handlePutTDSSettings(w http.ResponseWriter, r *http.Request) error {
	var in TDSSettings
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	p := auth.FromContext(r.Context())
	err := s.WithTx(r.Context(), func(tx pgx.Tx) error {
		if err := setActor(r.Context(), tx, p.UserID()); err != nil {
			return err
		}
		_, err := tx.Exec(r.Context(), `INSERT INTO tds_settings (tenant_id, tan, deductor_name, auto_194q, updated_by) VALUES ($1,$2,$3,$4,$5)
			ON CONFLICT (tenant_id) DO UPDATE SET tan=EXCLUDED.tan, deductor_name=EXCLUDED.deductor_name, auto_194q=EXCLUDED.auto_194q,
			updated_by=EXCLUDED.updated_by, updated_at=now()`, p.TenantID, nilIfEmpty(in.TAN), nilIfEmpty(in.DeductorName), in.Auto194Q, p.UserID())
		return err
	})
	if err != nil {
		return err
	}
	return s.handleGetTDSSettings(w, r)
}

func (s *Service) handleTDSSections(w http.ResponseWriter, _ *http.Request) error {
	out := make([]posting.Section, 0, len(posting.Sections))
	for _, sec := range posting.Sections {
		out = append(out, sec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return httpx.OK(w, map[string]any{"items": out})
}

// fyBounds returns 1 April .. 31 March of the FY containing date.
func fyBounds(date string) (string, string) {
	t, _ := time.Parse(time.DateOnly, date)
	y := t.Year()
	if t.Month() < time.April {
		y--
	}
	return fmt.Sprintf("%04d-04-01", y), fmt.Sprintf("%04d-03-31", y+1)
}

func fyOf(date string) string {
	t, _ := time.Parse(time.DateOnly, date)
	return FinancialYear(t)
}

// prior returns the party's FY aggregate base and taxed base recorded under section.
func prior(ctx context.Context, q db.Querier, tenantID, partyID, section, fy string) (base, taxed int64, err error) {
	err = q.QueryRow(ctx, `SELECT COALESCE(sum(base_paise),0), COALESCE(sum(taxable_paise),0) FROM tds_entries
		WHERE tenant_id=$1 AND party_id=$2 AND section=$3 AND fy=$4 AND status='active'`, tenantID, partyID, section, fy).Scan(&base, &taxed)
	return
}

// tdsRequest is the body of compute / deduction calls.
type tdsRequest struct {
	PartyID      string `json:"party_id"`
	Section      string `json:"section"` // default: the party's tds_section
	Date         string `json:"date"`
	BasePaise    int64  `json:"base_paise"` // bill/payment amount the section applies to (excl. GST)
	Direction    string `json:"direction"`  // payable (we deduct/collect, default) | receivable (deducted from us)
	TDSPaise     *int64 `json:"tds_paise"`  // override the computed amount (receivable: what the customer deducted)
	RefVoucherID string `json:"ref_voucher_id"`
	Key          string `json:"key"` // idempotency key; default derived from ref_voucher_id
	Narration    string `json:"narration"`
}

func (in *tdsRequest) Validate() error {
	if in.PartyID == "" || in.BasePaise < 0 {
		return httpx.Validation("party_id and a non-negative base_paise are required")
	}
	if in.Date != "" {
		if _, err := time.Parse(time.DateOnly, in.Date); err != nil {
			return httpx.Validation("date must be YYYY-MM-DD")
		}
	}
	switch in.Direction {
	case "":
		in.Direction = "payable"
	case "payable", "receivable":
	default:
		return httpx.Validation("direction must be payable or receivable")
	}
	if in.TDSPaise != nil && *in.TDSPaise < 0 {
		return httpx.Validation("tds_paise cannot be negative")
	}
	return nil
}

type tdsParty struct {
	ID, Name, PAN, Section, DeducteeType string
	LowerRateBP                          *int
}

func loadTDSParty(ctx context.Context, q db.Querier, tenantID, id string) (*tdsParty, error) {
	var p tdsParty
	err := q.QueryRow(ctx, `SELECT id, name, COALESCE(pan,''), COALESCE(tds_section,''), COALESCE(deductee_type,''), lower_tds_rate_bp
		FROM parties WHERE tenant_id=$1 AND id::text=$2`, tenantID, id).Scan(&p.ID, &p.Name, &p.PAN, &p.Section, &p.DeducteeType, &p.LowerRateBP)
	if db.IsNotFound(err) {
		return nil, httpx.Validation("party %s not found", id)
	}
	return &p, err
}

// computeFor runs posting.ComputeTDS for a party with its FY history.
func computeFor(ctx context.Context, q db.Querier, tenantID string, pt *tdsParty, code, date string, base int64) (posting.Section, posting.TDSResult, error) {
	if code == "" {
		code = pt.Section
	}
	sec, ok := posting.LookupSection(code)
	if !ok {
		return posting.Section{}, posting.TDSResult{}, httpx.Validation("section is required (party has no tds_section) or unknown: %q", code)
	}
	pb, pt2, err := prior(ctx, q, tenantID, pt.ID, sec.Code, fyOf(date))
	if err != nil {
		return sec, posting.TDSResult{}, err
	}
	res := posting.ComputeTDS(posting.TDSInput{Section: sec, PAN: pt.PAN, DeducteeType: pt.DeducteeType, AmountPaise: base,
		PriorPaise: pb, PriorTaxed: pt2, Date: date, LowerRateBP: pt.LowerRateBP})
	return sec, res, nil
}

func (s *Service) handleTDSCompute(w http.ResponseWriter, r *http.Request) error {
	var in tdsRequest
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	p := auth.FromContext(r.Context())
	date := orDefault(in.Date, s.today())
	pt, err := loadTDSParty(r.Context(), s.DB, p.TenantID, in.PartyID)
	if err != nil {
		return err
	}
	sec, res, err := computeFor(r.Context(), s.DB, p.TenantID, pt, in.Section, date, in.BasePaise)
	if err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"party_id": pt.ID, "party_name": pt.Name, "pan": pt.PAN, "fy": fyOf(date),
		"section": sec, "result": res})
}

// TDSEntry is one recorded deduction (or below-threshold base).
type TDSEntry struct {
	ID           string  `json:"id"`
	Kind         string  `json:"kind"`
	Direction    string  `json:"direction"`
	Section      string  `json:"section"`
	PartyID      string  `json:"party_id"`
	PartyName    string  `json:"party_name"`
	PAN          string  `json:"pan,omitempty"`
	Date         string  `json:"date"`
	FY           string  `json:"fy"`
	BasePaise    int64   `json:"base_paise"`
	TaxablePaise int64   `json:"taxable_paise"`
	RateBP       int     `json:"rate_bp"`
	TDSPaise     int64   `json:"tds_paise"`
	HigherRate   bool    `json:"higher_rate"`
	VoucherID    *string `json:"voucher_id,omitempty"`
	RefVoucherID *string `json:"ref_voucher_id,omitempty"`
	ChallanID    *string `json:"challan_id,omitempty"`
	Status       string  `json:"status"`
}

type tdsRecord struct {
	kind, direction, section, partyID, key, date string
	refVoucherID, voucherID                      string
	res                                          posting.TDSResult
	pan, dtype                                   string
}

func insertTDS(ctx context.Context, tx pgx.Tx, tenantID, userID string, t tdsRecord) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `INSERT INTO tds_entries (tenant_id, kind, direction, section, party_id, source_key, ref_voucher_id, voucher_id,
		txn_date, fy, base_paise, taxable_paise, rate_bp, tds_paise, pan, deductee_type, higher_rate, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
		ON CONFLICT (tenant_id, source_key) DO NOTHING RETURNING id`,
		tenantID, t.kind, t.direction, t.section, t.partyID, t.key, nilIfEmpty(t.refVoucherID), nilIfEmpty(t.voucherID), t.date, fyOf(t.date),
		t.res.BasePaise, t.res.TaxablePaise, t.res.RateBP, t.res.TDSPaise, nilIfEmpty(t.pan), nilIfEmpty(t.dtype), t.res.HigherRate, userID).Scan(&id)
	if db.IsNotFound(err) {
		return "", nil // already recorded
	}
	return id, err
}

// deductionVoucher: payable → Dr Party / Cr TDS|TCS Payable; receivable →
// Dr TDS|TCS Receivable / Cr Party.
func deductionVoucher(kind, direction, partyID, date, key, narration string, amt int64) posting.Voucher {
	party := posting.Entry{PartyID: partyID}
	tax := posting.Entry{}
	switch {
	case direction == "payable" && kind == "tcs":
		tax.AccountCode = posting.TCSPayable
	case direction == "payable":
		tax.AccountCode = posting.TDSPayable
	case kind == "tcs":
		tax.AccountCode = posting.TCSReceivable
	default:
		tax.AccountCode = posting.TDSReceivable
	}
	vtype := posting.VJournal
	if direction == "payable" && kind == "tds" {
		party.Debit, tax.Credit = amt, amt
	} else if direction == "payable" { // TCS collected from the buyer
		party.Debit, tax.Credit = amt, amt
	} else {
		tax.Debit, party.Credit = amt, amt
	}
	return posting.Voucher{Type: vtype, Date: date, Source: posting.SrcTDS, SourceID: key, Narration: narration,
		Entries: []posting.Entry{party, tax}}
}

// deduct records a deduction and posts its journal; when refVoucherID is a
// bill of the party the journal is allocated against it.
func (s *Service) deduct(ctx context.Context, tx pgx.Tx, tenantID, userID string, in tdsRequest) (*TDSEntry, error) {
	date := orDefault(in.Date, s.today())
	pt, err := loadTDSParty(ctx, tx, tenantID, in.PartyID)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('bahi-tds:' || $1))`, pt.ID); err != nil {
		return nil, err
	}
	sec, res, err := computeFor(ctx, tx, tenantID, pt, in.Section, date, in.BasePaise)
	if err != nil {
		return nil, err
	}
	if in.TDSPaise != nil {
		res.TDSPaise, res.Applicable = *in.TDSPaise, *in.TDSPaise > 0
		if res.TaxablePaise == 0 {
			res.TaxablePaise = in.BasePaise
		}
	}
	key := in.Key
	if key == "" {
		if in.RefVoucherID == "" {
			return nil, httpx.Validation("key or ref_voucher_id is required (idempotency)")
		}
		key = "voucher:" + in.RefVoucherID + ":" + sec.Code + ":" + in.Direction
	}
	var existing string
	err = tx.QueryRow(ctx, `SELECT id FROM tds_entries WHERE tenant_id=$1 AND source_key=$2`, tenantID, key).Scan(&existing)
	if err == nil {
		return getTDSEntry(ctx, tx, tenantID, existing)
	}
	if !db.IsNotFound(err) {
		return nil, err
	}
	var voucherID string
	if res.TDSPaise > 0 {
		narr := orDefault(in.Narration, fmt.Sprintf("%s %s %s on %s", strings.ToUpper(sec.Kind), sec.Code, in.Direction, paiseStr(in.BasePaise)))
		posted, err := Post(ctx, tx, tenantID, userID, deductionVoucher(sec.Kind, in.Direction, pt.ID, date, key, narr, res.TDSPaise))
		if err != nil {
			return nil, err
		}
		voucherID = posted.ID
		if in.RefVoucherID != "" {
			var billID string
			err := tx.QueryRow(ctx, `SELECT id FROM bills WHERE tenant_id=$1 AND voucher_id::text=$2 AND party_id=$3 AND status='open'`,
				tenantID, in.RefVoucherID, pt.ID).Scan(&billID)
			if err == nil {
				if _, err := allocate(ctx, tx, tenantID, userID, voucherID, billID, 0, "auto"); err != nil && httpx.AsError(err).Status >= 500 {
					return nil, err
				}
			} else if !db.IsNotFound(err) && !isBadUUID(err) {
				return nil, err
			}
		}
	}
	id, err := insertTDS(ctx, tx, tenantID, userID, tdsRecord{kind: sec.Kind, direction: in.Direction, section: sec.Code, partyID: pt.ID,
		key: key, date: date, refVoucherID: in.RefVoucherID, voucherID: voucherID, res: res, pan: pt.PAN, dtype: posting.DeducteeTypeOr(pt.DeducteeType, pt.PAN)})
	if err != nil {
		return nil, err
	}
	return getTDSEntry(ctx, tx, tenantID, id)
}

const tdsCols = `t.id, t.kind, t.direction, t.section, t.party_id, p.name, COALESCE(t.pan,''), t.txn_date, t.fy, t.base_paise, t.taxable_paise,
	t.rate_bp, t.tds_paise, t.higher_rate, t.voucher_id::text, t.ref_voucher_id::text, t.challan_id::text, t.status`

func scanTDS(row pgx.Row) (*TDSEntry, error) {
	var t TDSEntry
	var d time.Time
	err := row.Scan(&t.ID, &t.Kind, &t.Direction, &t.Section, &t.PartyID, &t.PartyName, &t.PAN, &d, &t.FY, &t.BasePaise, &t.TaxablePaise,
		&t.RateBP, &t.TDSPaise, &t.HigherRate, &t.VoucherID, &t.RefVoucherID, &t.ChallanID, &t.Status)
	t.Date = d.Format(time.DateOnly)
	return &t, err
}

func getTDSEntry(ctx context.Context, q db.Querier, tenantID, id string) (*TDSEntry, error) {
	t, err := scanTDS(q.QueryRow(ctx, `SELECT `+tdsCols+` FROM tds_entries t JOIN parties p ON p.id=t.party_id WHERE t.tenant_id=$1 AND t.id=$2`, tenantID, id))
	if db.IsNotFound(err) || isBadUUID(err) {
		return nil, httpx.NotFound("tds entry not found")
	}
	return t, err
}

func (s *Service) handleTDSDeduct(w http.ResponseWriter, r *http.Request) error {
	var in tdsRequest
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	p := auth.FromContext(r.Context())
	var out *TDSEntry
	err := s.WithTx(r.Context(), func(tx pgx.Tx) error {
		if err := setActor(r.Context(), tx, p.UserID()); err != nil {
			return err
		}
		var err error
		out, err = s.deduct(r.Context(), tx, p.TenantID, p.UserID(), in)
		return err
	})
	if err != nil {
		return err
	}
	return httpx.Created(w, out)
}

func (s *Service) handleListTDS(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	args := []any{p.TenantID}
	cond := "t.tenant_id=$1"
	add := func(col, v string) {
		if v != "" {
			args = append(args, v)
			cond += fmt.Sprintf(" AND %s=$%d", col, len(args))
		}
	}
	add("t.fy", httpx.Query(r, "fy"))
	add("t.section", httpx.Query(r, "section"))
	add("t.kind", httpx.Query(r, "kind"))
	add("t.direction", httpx.Query(r, "direction"))
	if pid := httpx.Query(r, "party_id"); pid != "" {
		args = append(args, pid)
		cond += fmt.Sprintf(" AND t.party_id::text=$%d", len(args))
	}
	if httpx.Query(r, "open") == "true" {
		cond += " AND t.challan_id IS NULL AND t.tds_paise > 0 AND t.status='active'"
	}
	rows, err := s.DB.Query(r.Context(), `SELECT `+tdsCols+` FROM tds_entries t JOIN parties p ON p.id=t.party_id WHERE `+cond+`
		ORDER BY t.txn_date, t.created_at LIMIT 5000`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []TDSEntry{}
	for rows.Next() {
		t, err := scanTDS(rows)
		if err != nil {
			return err
		}
		items = append(items, *t)
	}
	return httpx.OK(w, map[string]any{"items": items})
}

// voucherTDS is the optional tds block of POST /v1/vouchers.
type voucherTDS struct {
	PartyID   string `json:"party_id"`
	Section   string `json:"section"`
	BasePaise int64  `json:"base_paise"`
}

type tdsApplied struct {
	party *tdsParty
	sec   posting.Section
	res   posting.TDSResult
	date  string
}

// applyVoucherTDS adds Dr Party / Cr TDS Payable to a manual payment or
// journal for the TDS due on base.
func (s *Service) applyVoucherTDS(ctx context.Context, tx pgx.Tx, tenantID string, v posting.Voucher, t voucherTDS) (posting.Voucher, *tdsApplied, error) {
	if t.PartyID == "" || t.BasePaise <= 0 {
		return v, nil, httpx.Validation("tds needs party_id and a positive base_paise")
	}
	pt, err := loadTDSParty(ctx, tx, tenantID, t.PartyID)
	if err != nil {
		return v, nil, err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('bahi-tds:' || $1))`, pt.ID); err != nil {
		return v, nil, err
	}
	sec, res, err := computeFor(ctx, tx, tenantID, pt, t.Section, v.Date, t.BasePaise)
	if err != nil {
		return v, nil, err
	}
	if sec.Kind != "tds" {
		return v, nil, httpx.Validation("section %s is not a TDS section", sec.Code)
	}
	if res.TDSPaise > 0 {
		v.Entries = append(append([]posting.Entry{}, v.Entries...),
			posting.Entry{PartyID: pt.ID, Debit: res.TDSPaise}, posting.Entry{AccountCode: posting.TDSPayable, Credit: res.TDSPaise})
	}
	return v, &tdsApplied{party: pt, sec: sec, res: res, date: v.Date}, nil
}

func (s *Service) recordVoucherTDS(ctx context.Context, tx pgx.Tx, tenantID, userID, voucherID string, a tdsApplied) error {
	vid := ""
	if a.res.TDSPaise > 0 {
		vid = voucherID
	}
	_, err := insertTDS(ctx, tx, tenantID, userID, tdsRecord{kind: "tds", direction: "payable", section: a.sec.Code, partyID: a.party.ID,
		key: "voucher:" + voucherID, date: a.date, refVoucherID: voucherID, voucherID: vid, res: a.res, pan: a.party.PAN,
		dtype: posting.DeducteeTypeOr(a.party.DeducteeType, a.party.PAN)})
	return err
}

// autoTDSOnPurchase deducts 194Q on a purchase bill (taxable value) when the
// tenant enabled it and the supplier is tagged 194Q. Runs in the event tx.
func (s *Service) autoTDSOnPurchase(ctx context.Context, tx pgx.Tx, tenantID string, p events.InvoicePayload, voucherID string) error {
	if p.DocType != "purchase" || p.PartyID == "" || p.TaxablePaise <= 0 {
		return nil
	}
	set, err := s.tdsSettings(ctx, tx, tenantID)
	if err != nil || !set.Auto194Q {
		return err
	}
	pt, err := loadTDSParty(ctx, tx, tenantID, p.PartyID)
	if err != nil {
		return err
	}
	if pt.Section != posting.Sec194Q {
		return nil
	}
	_, err = s.deduct(ctx, tx, tenantID, "system:bahi-tds", tdsRequest{PartyID: pt.ID, Section: posting.Sec194Q, Date: p.InvoiceDate,
		BasePaise: p.TaxablePaise, Direction: "payable", RefVoucherID: voucherID, Key: "invoice:" + p.InvoiceID,
		Narration: "TDS 194Q on purchase bill " + p.Number})
	return err
}

// challanInput settles open TDS/TCS payable entries.
type challanInput struct {
	Kind          string   `json:"kind"` // tds (default) | tcs
	BSRCode       string   `json:"bsr_code"`
	ChallanNo     string   `json:"challan_no"`
	PaidOn        string   `json:"paid_on"`
	BankAccountID string   `json:"bank_account_id"`
	EntryIDs      []string `json:"entry_ids"` // or month (+ section)
	Month         string   `json:"month"`     // YYYY-MM: every open entry deducted that month
	Section       string   `json:"section"`
	InterestPaise int64    `json:"interest_paise"`
}

func (in *challanInput) Validate() error {
	in.Kind = orDefault(in.Kind, "tds")
	if in.Kind != "tds" && in.Kind != "tcs" {
		return httpx.Validation("kind must be tds or tcs")
	}
	if strings.TrimSpace(in.BSRCode) == "" || strings.TrimSpace(in.ChallanNo) == "" || in.BankAccountID == "" {
		return httpx.Validation("bsr_code, challan_no and bank_account_id are required")
	}
	if _, err := time.Parse(time.DateOnly, in.PaidOn); err != nil {
		return httpx.Validation("paid_on must be YYYY-MM-DD")
	}
	if len(in.EntryIDs) == 0 {
		if _, err := time.Parse("2006-01", in.Month); err != nil {
			return httpx.Validation("give entry_ids or month (YYYY-MM)")
		}
	}
	if in.InterestPaise < 0 {
		return httpx.Validation("interest_paise cannot be negative")
	}
	return nil
}

// TDSChallan is a deposited challan.
type TDSChallan struct {
	ID            string `json:"id"`
	Kind          string `json:"kind"`
	BSRCode       string `json:"bsr_code"`
	ChallanNo     string `json:"challan_no"`
	PaidOn        string `json:"paid_on"`
	AmountPaise   int64  `json:"amount_paise"`
	InterestPaise int64  `json:"interest_paise"`
	BankAccountID string `json:"bank_account_id"`
	VoucherID     string `json:"voucher_id"`
	Entries       int    `json:"entries"`
}

func (s *Service) handleCreateChallan(w http.ResponseWriter, r *http.Request) error {
	var in challanInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	p := auth.FromContext(r.Context())
	if err := s.requireBank(r.Context(), s.DB, p.TenantID, in.BankAccountID); err != nil {
		return err
	}
	var c TDSChallan
	err := s.WithTx(r.Context(), func(tx pgx.Tx) error {
		if err := setActor(r.Context(), tx, p.UserID()); err != nil {
			return err
		}
		args := []any{p.TenantID, in.Kind}
		cond := "tenant_id=$1 AND kind=$2 AND direction='payable' AND status='active' AND challan_id IS NULL AND tds_paise > 0"
		if len(in.EntryIDs) > 0 {
			args = append(args, in.EntryIDs)
			cond += fmt.Sprintf(" AND id::text = ANY($%d)", len(args))
		} else {
			args = append(args, in.Month)
			cond += fmt.Sprintf(" AND to_char(txn_date,'YYYY-MM')=$%d", len(args))
			if in.Section != "" {
				sec, ok := posting.LookupSection(in.Section)
				if !ok {
					return httpx.Validation("unknown section %q", in.Section)
				}
				args = append(args, sec.Code)
				cond += fmt.Sprintf(" AND section=$%d", len(args))
			}
		}
		rows, err := tx.Query(r.Context(), `SELECT id, tds_paise FROM tds_entries WHERE `+cond+` FOR UPDATE`, args...)
		if err != nil {
			return err
		}
		var ids []string
		var total int64
		for rows.Next() {
			var id string
			var amt int64
			if err := rows.Scan(&id, &amt); err != nil {
				rows.Close()
				return err
			}
			ids, total = append(ids, id), total+amt
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(ids) == 0 {
			return httpx.Validation("no open %s entries to deposit", in.Kind)
		}
		if len(in.EntryIDs) > 0 && len(ids) != len(in.EntryIDs) {
			return httpx.Conflict("some entries are already deposited, reversed or not payable")
		}
		payable := posting.TDSPayable
		if in.Kind == "tcs" {
			payable = posting.TCSPayable
		}
		es := []posting.Entry{{AccountCode: payable, Debit: total}}
		if in.InterestPaise > 0 {
			es = append(es, posting.Entry{AccountCode: posting.InterestExpense, Debit: in.InterestPaise})
		}
		es = append(es, posting.Entry{AccountID: in.BankAccountID, Credit: total + in.InterestPaise})
		key := strings.TrimSpace(in.BSRCode) + "/" + strings.TrimSpace(in.ChallanNo) + "/" + in.PaidOn
		posted, err := Post(r.Context(), tx, p.TenantID, p.UserID(), posting.Voucher{Type: posting.VPayment, Date: in.PaidOn,
			Source: posting.SrcTDSChallan, SourceID: key, SourceRef: "Challan " + in.ChallanNo,
			Narration: fmt.Sprintf("%s challan %s (BSR %s), %d entries", strings.ToUpper(in.Kind), in.ChallanNo, in.BSRCode, len(ids)), Entries: es})
		if err != nil {
			return err
		}
		err = tx.QueryRow(r.Context(), `INSERT INTO tds_challans (tenant_id, kind, bsr_code, challan_no, paid_on, amount_paise, interest_paise,
			bank_account_id, voucher_id, created_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING id`,
			p.TenantID, in.Kind, strings.TrimSpace(in.BSRCode), strings.TrimSpace(in.ChallanNo), in.PaidOn, total, in.InterestPaise,
			in.BankAccountID, posted.ID, p.UserID()).Scan(&c.ID)
		if db.IsUniqueViolation(err) {
			return httpx.Conflict("challan %s already recorded", in.ChallanNo)
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `UPDATE tds_entries SET challan_id=$2 WHERE tenant_id=$1 AND id::text = ANY($3)`, p.TenantID, c.ID, ids); err != nil {
			return err
		}
		c.Kind, c.BSRCode, c.ChallanNo, c.PaidOn, c.AmountPaise, c.InterestPaise = in.Kind, in.BSRCode, in.ChallanNo, in.PaidOn, total, in.InterestPaise
		c.BankAccountID, c.VoucherID, c.Entries = in.BankAccountID, posted.ID, len(ids)
		return nil
	})
	if err != nil {
		return err
	}
	return httpx.Created(w, c)
}

func (s *Service) handleListChallans(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	rows, err := s.DB.Query(r.Context(), `SELECT c.id, c.kind, c.bsr_code, c.challan_no, c.paid_on, c.amount_paise, c.interest_paise,
			c.bank_account_id, c.voucher_id, (SELECT count(*) FROM tds_entries t WHERE t.challan_id=c.id)
		FROM tds_challans c WHERE c.tenant_id=$1 ORDER BY c.paid_on DESC LIMIT 1000`, p.TenantID)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []TDSChallan{}
	for rows.Next() {
		var c TDSChallan
		var d time.Time
		if err := rows.Scan(&c.ID, &c.Kind, &c.BSRCode, &c.ChallanNo, &d, &c.AmountPaise, &c.InterestPaise, &c.BankAccountID, &c.VoucherID, &c.Entries); err != nil {
			return err
		}
		c.PaidOn = d.Format(time.DateOnly)
		items = append(items, c)
	}
	return httpx.OK(w, map[string]any{"items": items})
}

// quarterBounds: Q1 Apr–Jun … Q4 Jan–Mar of FY "26-27".
func quarterBounds(fy string, q int) (string, string, error) {
	if len(fy) != 5 || fy[2] != '-' || q < 1 || q > 4 {
		return "", "", httpx.Validation("fy must look like 26-27 and quarter 1..4")
	}
	yy, err := strconv.Atoi(fy[:2])
	if err != nil {
		return "", "", httpx.Validation("fy must look like 26-27")
	}
	start := time.Date(2000+yy, time.April, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 3*(q-1), 0)
	return start.Format(time.DateOnly), start.AddDate(0, 3, -1).Format(time.DateOnly), nil
}

// handle26Q exports the deductee annexure of Form 26Q (non-salary TDS) for a
// quarter as CSV in the column order of the NSDL/Protean RPU deductee sheet.
func (s *Service) handle26Q(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	fy := orDefault(httpx.Query(r, "fy"), FinancialYear(s.Now().In(IST)))
	q, _ := strconv.Atoi(orDefault(httpx.Query(r, "quarter"), "0"))
	from, to, err := quarterBounds(fy, q)
	if err != nil {
		return err
	}
	set, err := s.tdsSettings(r.Context(), s.DB, p.TenantID)
	if err != nil {
		return err
	}
	rows, err := s.DB.Query(r.Context(), `SELECT t.section, COALESCE(t.pan,''), COALESCE(t.deductee_type,''), p.name, t.txn_date,
			t.taxable_paise, t.base_paise, t.rate_bp, t.tds_paise, t.higher_rate,
			COALESCE(c.bsr_code,''), c.paid_on, COALESCE(c.challan_no,'')
		FROM tds_entries t JOIN parties p ON p.id=t.party_id LEFT JOIN tds_challans c ON c.id=t.challan_id
		WHERE t.tenant_id=$1 AND t.kind='tds' AND t.direction='payable' AND t.status='active' AND t.tds_paise > 0
		  AND t.txn_date BETWEEN $2 AND $3
		ORDER BY t.txn_date, p.name`, p.TenantID, from, to)
	if err != nil {
		return err
	}
	defer rows.Close()
	var out [][]string
	n := 0
	var missing int
	for rows.Next() {
		var section, pan, dtype, name, bsr, challan string
		var d time.Time
		var paid *time.Time
		var taxable, base, tds int64
		var rate int
		var higher bool
		if err := rows.Scan(&section, &pan, &dtype, &name, &d, &taxable, &base, &rate, &tds, &higher, &bsr, &paid, &challan); err != nil {
			return err
		}
		n++
		code := section
		if sec, ok := posting.Sections[section]; ok {
			code = sec.Code26Q
		}
		deducteeCode := "02" // other than company
		if dtype == "company" {
			deducteeCode = "01"
		}
		if pan == "" {
			pan = "PANNOTAVBL"
		}
		reason := ""
		if higher {
			reason = "C" // higher deduction: PAN not available (206AA)
		}
		challanDate := ""
		if paid != nil {
			challanDate = paid.Format("02/01/2006")
		} else {
			missing++
		}
		dd := d.Format("02/01/2006")
		out = append(out, []string{strconv.Itoa(n), code, deducteeCode, pan, name, dd, paiseStr(max(taxable, base)),
			fmt.Sprintf("%d.%04d", rate/100, (rate%100)*100), paiseStr(tds), paiseStr(tds), dd, bsr, challanDate, challan, reason, set.TAN})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if !wantsCSV(r) && httpx.Query(r, "format") != "" {
		return httpx.Validation("format must be csv")
	}
	if !wantsCSV(r) {
		return httpx.OK(w, map[string]any{"fy": fy, "quarter": q, "from": from, "to": to, "tan": set.TAN, "rows": out,
			"entries_without_challan": missing})
	}
	return writeCSV(w, fmt.Sprintf("26Q_%s_Q%d", fy, q), []string{"Sr No", "Section Code", "Deductee Code", "PAN of Deductee",
		"Name of Deductee", "Date of Payment/Credit", "Amount Paid/Credited", "TDS Rate (%)", "TDS Amount", "Total Tax Deposited",
		"Date of Deduction", "BSR Code", "Challan Date", "Challan Serial No", "Reason for Lower/Higher Deduction", "TAN of Deductor"}, out)
}

// handleTCS1H tracks 206C(1H): per buyer, FY sales and receipts against the
// ₹50 lakh threshold and the TCS booked on their invoices.
func (s *Service) handleTCS1H(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	from, to := fyBounds(s.today())
	if fy := httpx.Query(r, "fy"); fy != "" {
		f, _, err := quarterBounds(fy, 1)
		if err != nil {
			return err
		}
		from, to = fyBounds(f)
	}
	sec := posting.Sections[posting.Sec206C1H]
	rows, err := s.DB.Query(r.Context(), `SELECT p.id, p.name, COALESCE(p.pan,''),
			COALESCE(sum(e.credit_paise) FILTER (WHERE v.vtype='receipt'),0),
			COALESCE((SELECT sum(b.amount_paise) FROM bills b WHERE b.party_id=p.id AND b.side='dr' AND b.status='open'
			          AND b.bill_date BETWEEN $2 AND $3),0),
			COALESCE((SELECT sum(t.credit_paise - t.debit_paise) FROM voucher_entries t JOIN accounts a ON a.id=t.account_id
			          WHERE a.tenant_id=$1 AND a.code=$4 AND t.vdate BETWEEN $2 AND $3
			            AND t.voucher_id IN (SELECT voucher_id FROM voucher_entries x WHERE x.party_id=p.id)),0)
		FROM parties p LEFT JOIN voucher_entries e ON e.party_id=p.id AND e.vdate BETWEEN $2 AND $3
		LEFT JOIN vouchers v ON v.id=e.voucher_id
		WHERE p.tenant_id=$1 AND p.type IN ('customer','both')
		GROUP BY p.id ORDER BY p.name`, p.TenantID, from, to, posting.TCSPayable)
	if err != nil {
		return err
	}
	defer rows.Close()
	type row struct {
		PartyID        string `json:"party_id"`
		PartyName      string `json:"party_name"`
		PAN            string `json:"pan,omitempty"`
		ReceiptsPaise  int64  `json:"receipts_paise"`
		SalesPaise     int64  `json:"sales_paise"`
		ExcessPaise    int64  `json:"receipts_above_threshold_paise"`
		CollectedPaise int64  `json:"tcs_collected_paise"`
		DuePaise       int64  `json:"tcs_due_paise"` // 0 once the section no longer applies
		Crossed        bool   `json:"threshold_crossed"`
	}
	applicable := from <= sec.ValidUntil
	out := []row{}
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.PartyID, &x.PartyName, &x.PAN, &x.ReceiptsPaise, &x.SalesPaise, &x.CollectedPaise); err != nil {
			return err
		}
		x.ExcessPaise = max(x.ReceiptsPaise-sec.Aggregate, 0)
		x.Crossed = x.ExcessPaise > 0
		if applicable && x.Crossed {
			res := posting.ComputeTDS(posting.TDSInput{Section: sec, PAN: x.PAN, AmountPaise: x.ReceiptsPaise, Date: from})
			x.DuePaise = max(res.TDSPaise-x.CollectedPaise, 0)
		}
		if x.ReceiptsPaise == 0 && x.SalesPaise == 0 && x.CollectedPaise == 0 {
			continue
		}
		out = append(out, x)
	}
	note := ""
	if !applicable {
		note = "Section 206C(1H) was omitted from 1 April 2025 (Finance Act 2025); threshold tracking and TCS already charged are shown for reference"
	}
	return httpx.OK(w, map[string]any{"from": from, "to": to, "section": sec, "applicable": applicable, "note": note, "rows": out})
}

// handleTDSSummary: payable/receivable by section for a FY, deposited vs pending.
func (s *Service) handleTDSSummary(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	fy := orDefault(httpx.Query(r, "fy"), FinancialYear(s.Now().In(IST)))
	rows, err := s.DB.Query(r.Context(), `SELECT kind, direction, section, count(*), COALESCE(sum(base_paise),0), COALESCE(sum(tds_paise),0),
			COALESCE(sum(tds_paise) FILTER (WHERE challan_id IS NOT NULL),0)
		FROM tds_entries WHERE tenant_id=$1 AND fy=$2 AND status='active' GROUP BY kind, direction, section ORDER BY kind, direction, section`,
		p.TenantID, fy)
	if err != nil {
		return err
	}
	defer rows.Close()
	type line struct {
		Kind           string `json:"kind"`
		Direction      string `json:"direction"`
		Section        string `json:"section"`
		Entries        int    `json:"entries"`
		BasePaise      int64  `json:"base_paise"`
		TDSPaise       int64  `json:"tds_paise"`
		DepositedPaise int64  `json:"deposited_paise"`
		PendingPaise   int64  `json:"pending_paise"`
	}
	out := []line{}
	for rows.Next() {
		var l line
		if err := rows.Scan(&l.Kind, &l.Direction, &l.Section, &l.Entries, &l.BasePaise, &l.TDSPaise, &l.DepositedPaise); err != nil {
			return err
		}
		if l.Direction == "payable" {
			l.PendingPaise = l.TDSPaise - l.DepositedPaise
		}
		out = append(out, l)
	}
	return httpx.OK(w, map[string]any{"fy": fy, "lines": out})
}
