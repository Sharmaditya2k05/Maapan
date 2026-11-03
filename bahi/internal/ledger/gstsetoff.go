package ledger

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/maapan/bahi/internal/posting"
	"github.com/maapan/mool/auth"
	"github.com/maapan/mool/db"
	"github.com/maapan/mool/events"
	"github.com/maapan/mool/gcp"
	"github.com/maapan/mool/gst"
	"github.com/maapan/mool/httpx"
	"github.com/maapan/mool/money"
)

// GST set-off at month end. kar computes the GSTR-3B payable table with the
// Rule 88A order of ITC utilisation (IGST first, then CGST/SGST, never
// CGST↔SGST); bahi posts it:
//
//	PMT-06 deposit (optional)  Dr Electronic Cash Ledger <head>  Cr Bank
//	set-off journal            Dr Output <head>  Cr Input <head>               ITC utilised
//	                           Dr Output <head>  Cr Electronic Cash Ledger     cash portion
//
// The Input CGST/SGST/IGST/Cess accounts are the books' electronic credit
// ledger: ITC not utilised stays there and is carried forward.

// rs is a rupee amount as kar serialises it (JSON number, two decimals).
type rs int64

func (r *rs) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		*r = 0
		return nil
	}
	p, err := money.ParseRupees(s)
	if err != nil {
		return err
	}
	*r = rs(p)
	return nil
}

type rsHeads struct {
	IGST rs `json:"igst"`
	CGST rs `json:"cgst"`
	SGST rs `json:"sgst"`
	Cess rs `json:"cess"`
}

func (h rsHeads) paise() map[string]int64 {
	return map[string]int64{"igst": int64(h.IGST), "cgst": int64(h.CGST), "sgst": int64(h.SGST), "cess": int64(h.Cess)}
}

// karComputation is the `computation` block of kar's GET /v1/returns/gstr3b.
type karComputation struct {
	Liability    rsHeads `json:"liability"`
	ITCAvailable rsHeads `json:"itc_available"`
	ITCUtilised  rsHeads `json:"itc_utilised"`
	CashPayable  rsHeads `json:"cash_payable"`
	ITCCarryFwd  rsHeads `json:"itc_carry_forward"`
	Utilisation  []struct {
		From   string `json:"from"`
		To     string `json:"to"`
		Amount rs     `json:"amount"`
	} `json:"utilisation"`
	ReverseCharge rsHeads `json:"reverse_charge_cash"`
}

type setOffInput struct {
	GSTIN                string          `json:"gstin"`
	Period               string          `json:"period"` // MMYYYY
	Date                 string          `json:"date"`   // default today (the filing date)
	Computation          *karComputation `json:"computation"`
	PayFromBankAccountID string          `json:"pay_from_bank_account_id"` // also post the PMT-06 cash deposit
}

func (in *setOffInput) Validate() error {
	in.GSTIN = gst.Normalize(in.GSTIN)
	if in.GSTIN != "" {
		if err := gst.ValidateGSTIN(in.GSTIN); err != nil {
			return httpx.Validation("gstin: %s", err.Error())
		}
	}
	if _, err := time.Parse("012006", in.Period); err != nil {
		return httpx.Validation("period must be MMYYYY")
	}
	if in.Date != "" {
		if _, err := time.Parse(time.DateOnly, in.Date); err != nil {
			return httpx.Validation("date must be YYYY-MM-DD")
		}
	}
	return nil
}

// fetchGSTR3B asks kar for the period's computation, forwarding the caller's
// bearer token (the route is a user route guarded by report:read).
func (s *Service) fetchGSTR3B(r *http.Request, gstin, period string) (*karComputation, string, error) {
	if s.Kar == nil || s.Kar.BaseURL == "" {
		return nil, "", httpx.Validation("computation is required (kar is not configured, KAR_URL unset)")
	}
	q := url.Values{"period": {period}}
	if gstin != "" {
		q.Set("gstin", gstin)
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, s.Kar.BaseURL+"/v1/returns/gstr3b?"+q.Encode(), nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", r.Header.Get("Authorization"))
	if id := httpx.RequestIDFrom(r.Context()); id != "" {
		req.Header.Set(httpx.HeaderRequestID, id)
	}
	if s.Kar.UseIDToken {
		u, _ := url.Parse(s.Kar.BaseURL)
		tok, err := gcp.IDToken(r.Context(), u.Scheme+"://"+u.Host)
		if err != nil {
			return nil, "", httpx.Upstream("id token: %s", err.Error())
		}
		req.Header.Set("X-Serverless-Authorization", "Bearer "+tok)
	}
	client := s.Kar.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, "", httpx.Upstream("kar GSTR-3B call failed")
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode >= 400 {
		var env struct {
			Error *httpx.Error `json:"error"`
		}
		if json.Unmarshal(raw, &env) == nil && env.Error != nil {
			env.Error.Status = res.StatusCode
			return nil, "", env.Error
		}
		return nil, "", httpx.Upstream("kar GSTR-3B returned %d", res.StatusCode)
	}
	var out struct {
		Gstin       string         `json:"gstin"`
		Computation karComputation `json:"computation"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, "", httpx.Upstream("kar GSTR-3B: %s", err.Error())
	}
	return &out.Computation, out.Gstin, nil
}

func (c *karComputation) setOffInput(gstin, period, date string) posting.SetOffInput {
	in := posting.SetOffInput{GSTIN: gstin, Period: period, Date: date, Cash: c.CashPayable.paise()}
	for _, u := range c.Utilisation {
		in.Utilisation = append(in.Utilisation, posting.Utilisation{From: u.From, To: u.To, AmountPaise: int64(u.Amount)})
	}
	return in
}

// setOffWarnings compares what the set-off debits/credits with the book
// balances of the GST accounts as of date.
func setOffWarnings(ctx context.Context, q db.Querier, tenantID, date string, v posting.Voucher) ([]string, error) {
	need := map[string]int64{}
	for _, e := range v.Entries {
		need[e.AccountCode] += e.Debit - e.Credit
	}
	var warns []string
	for code, amt := range need {
		var bal int64
		if err := q.QueryRow(ctx, `SELECT COALESCE(sum(e.debit_paise - e.credit_paise),0) FROM voucher_entries e JOIN accounts a ON a.id=e.account_id
			WHERE a.tenant_id=$1 AND a.code=$2 AND e.vdate <= $3`, tenantID, code, date).Scan(&bal); err != nil {
			return nil, err
		}
		switch {
		case amt > 0 && -bal < amt: // debiting an output (liability) account beyond its credit balance
			warns = append(warns, fmt.Sprintf("account %s: set-off %s exceeds the book liability %s", code, paiseStr(amt), paiseStr(-bal)))
		case amt < 0 && bal < -amt && !strings.HasPrefix(code, "145"): // crediting input ITC beyond its debit balance
			warns = append(warns, fmt.Sprintf("account %s: ITC utilised %s exceeds the book credit %s", code, paiseStr(-amt), paiseStr(bal)))
		}
	}
	return warns, nil
}

// postSetOff posts the (optional) cash deposit and the set-off journal and
// records the period. A period already set off is a conflict.
func (s *Service) postSetOff(ctx context.Context, tx pgx.Tx, tenantID, userID string, so posting.SetOffInput, bankAccountID string,
	raw any) (map[string]any, error) {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('bahi-setoff:' || $1 || $2 || $3))`, tenantID, so.GSTIN, so.Period); err != nil {
		return nil, err
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM gst_setoffs WHERE tenant_id=$1 AND gstin=$2 AND period=$3)`,
		tenantID, so.GSTIN, so.Period).Scan(&exists); err != nil {
		return nil, err
	}
	if exists {
		return nil, httpx.Conflict("GST set-off for %s %s is already posted", so.GSTIN, so.Period)
	}
	out := map[string]any{"gstin": so.GSTIN, "period": so.Period, "date": so.Date}
	var cashID, setID *string
	if bankAccountID != "" {
		cv, ok, err := posting.GSTCashDeposit(so.GSTIN, so.Period, so.Date, bankAccountID, so.Cash)
		if err != nil {
			return nil, httpx.Validation("%s", err.Error())
		}
		if ok {
			p, err := Post(ctx, tx, tenantID, userID, cv)
			if err != nil {
				return nil, err
			}
			cashID = &p.ID
			out["cash_voucher"] = p
		}
	}
	v, ok, err := posting.SetOff(so)
	if err != nil {
		return nil, httpx.Validation("%s", err.Error())
	}
	warns := []string{}
	if ok {
		if warns, err = setOffWarnings(ctx, tx, tenantID, so.Date, v); err != nil {
			return nil, err
		}
		if warns == nil {
			warns = []string{}
		}
		p, err := Post(ctx, tx, tenantID, userID, v)
		if err != nil {
			return nil, err
		}
		setID = &p.ID
		out["setoff_voucher"] = p
	}
	b, _ := json.Marshal(raw)
	if _, err := tx.Exec(ctx, `INSERT INTO gst_setoffs (tenant_id, gstin, period, setoff_voucher_id, cash_voucher_id, computation, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`, tenantID, so.GSTIN, so.Period, setID, cashID, b, userID); err != nil {
		return nil, err
	}
	out["warnings"] = warns
	return out, nil
}

// handleGSTSetOff: POST /v1/gst/setoff. Without a computation in the body it
// is fetched from kar (GET /v1/returns/gstr3b) with the caller's token.
func (s *Service) handleGSTSetOff(w http.ResponseWriter, r *http.Request) error {
	var in setOffInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	p := auth.FromContext(r.Context())
	comp, gstin := in.Computation, in.GSTIN
	source := "request"
	if comp == nil {
		var g string
		var err error
		if comp, g, err = s.fetchGSTR3B(r, in.GSTIN, in.Period); err != nil {
			return err
		}
		gstin, source = orDefault(gstin, g), "kar"
	}
	if gstin == "" {
		return httpx.Validation("gstin is required")
	}
	if in.PayFromBankAccountID != "" {
		if err := s.requireBank(r.Context(), s.DB, p.TenantID, in.PayFromBankAccountID); err != nil {
			return err
		}
	}
	date := orDefault(in.Date, s.today())
	var out map[string]any
	err := s.WithTx(r.Context(), func(tx pgx.Tx) error {
		var err error
		out, err = s.postSetOff(r.Context(), tx, p.TenantID, p.UserID(), comp.setOffInput(gstin, in.Period, date), in.PayFromBankAccountID, comp)
		return err
	})
	if err != nil {
		return err
	}
	out["computation_source"] = source
	out["itc_carry_forward_paise"] = comp.ITCCarryFwd.paise()
	if rc := comp.ReverseCharge.paise(); rc["igst"]+rc["cgst"]+rc["sgst"]+rc["cess"] > 0 {
		out["reverse_charge_cash_paise"] = rc
		out["warnings"] = append(out["warnings"].([]string), "reverse-charge tax is payable in cash and is not part of this set-off; book its payment separately")
	}
	return httpx.Created(w, out)
}

func (s *Service) handleListSetOffs(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	rows, err := s.DB.Query(r.Context(), `SELECT gstin, period, setoff_voucher_id::text, cash_voucher_id::text, created_by, created_at
		FROM gst_setoffs WHERE tenant_id=$1 ORDER BY created_at DESC LIMIT 500`, p.TenantID)
	if err != nil {
		return err
	}
	defer rows.Close()
	type row struct {
		GSTIN           string    `json:"gstin"`
		Period          string    `json:"period"`
		SetOffVoucherID *string   `json:"setoff_voucher_id,omitempty"`
		CashVoucherID   *string   `json:"cash_voucher_id,omitempty"`
		CreatedBy       string    `json:"created_by"`
		CreatedAt       time.Time `json:"created_at"`
	}
	items := []row{}
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.GSTIN, &x.Period, &x.SetOffVoucherID, &x.CashVoucherID, &x.CreatedBy, &x.CreatedAt); err != nil {
			return err
		}
		items = append(items, x)
	}
	return httpx.OK(w, map[string]any{"items": items})
}

// onReturnFiled posts the set-off journal kar sends with a filed GSTR-3B.
func (s *Service) onReturnFiled(ctx context.Context, e events.Envelope) error {
	var p events.ReturnFiledPayload
	if err := e.Decode(&p); err != nil {
		return s.quarantine(ctx, e, "malformed return.filed payload: "+err.Error())
	}
	if !strings.EqualFold(strings.ReplaceAll(p.ReturnType, "-", ""), "GSTR3B") {
		return nil
	}
	date := s.eventDate(e)
	if t, err := time.Parse(time.RFC3339, p.FiledAt); err == nil {
		date = t.In(IST).Format(time.DateOnly)
	}
	so := posting.SetOffInput{GSTIN: gst.Normalize(p.GSTIN), Period: p.Period, Date: date, Cash: p.CashPaise}
	for _, u := range p.Utilisation {
		so.Utilisation = append(so.Utilisation, posting.Utilisation{From: u.From, To: u.To, AmountPaise: u.AmountPaise})
	}
	if _, _, err := posting.SetOff(so); err != nil {
		return s.quarantine(ctx, e, "set-off rejected: "+err.Error())
	}
	return s.handle(ctx, e, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM gst_setoffs WHERE tenant_id=$1 AND gstin=$2 AND period=$3)`,
			e.TenantID, so.GSTIN, so.Period).Scan(&exists); err != nil || exists {
			return err // posted by hand already
		}
		_, err := s.postSetOff(ctx, tx, e.TenantID, "system:kar", so, "", p)
		return err
	})
}
