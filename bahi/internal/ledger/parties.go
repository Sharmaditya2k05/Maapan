package ledger

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/maapan/bahi/internal/posting"
	"github.com/maapan/mool/auth"
	"github.com/maapan/mool/db"
	"github.com/maapan/mool/gst"
	"github.com/maapan/mool/httpx"
)

// Party is a customer and/or supplier with its own ledger account.
type Party struct {
	ID               string    `json:"id"`
	AccountID        string    `json:"account_id"`
	Name             string    `json:"name"`
	Type             string    `json:"type"`
	Phone            string    `json:"phone"`
	Email            string    `json:"email"`
	GSTIN            string    `json:"gstin"`
	StateCode        string    `json:"state_code"`
	Address          string    `json:"address"`
	CreditLimitPaise int64     `json:"credit_limit_paise"`
	CreditDays       int       `json:"credit_days"`
	GSTVerified      bool      `json:"gst_verified"`
	LegalName        string    `json:"legal_name,omitempty"`
	ExternalRef      string    `json:"external_ref,omitempty"`
	Archived         bool      `json:"archived"`
	BalancePaise     int64     `json:"balance_paise"` // + receivable (party owes us), − payable
	CreatedAt        time.Time `json:"created_at"`
	PAN              string    `json:"pan,omitempty"`
	TDSSection       string    `json:"tds_section,omitempty"`   // default TDS section for bills/payments to this party
	DeducteeType     string    `json:"deductee_type,omitempty"` // individual | huf | company | firm | other
	LowerTDSRateBP   *int      `json:"lower_tds_rate_bp,omitempty"`
	ShippingAddress  string    `json:"shipping_address,omitempty"`
}

const partyCols = `p.id, p.account_id, p.name, p.type, COALESCE(p.phone,''), COALESCE(p.email,''), COALESCE(p.gstin,''),
	COALESCE(p.state_code,''), COALESCE(p.address,''), p.credit_limit_paise, p.credit_days, p.gst_verified,
	COALESCE(p.legal_name,''), COALESCE(p.external_ref,''), p.archived,
	COALESCE((SELECT sum(debit_paise - credit_paise) FROM voucher_entries e WHERE e.tenant_id=p.tenant_id AND e.account_id=p.account_id),0),
	p.created_at, COALESCE(p.pan,''), COALESCE(p.tds_section,''), COALESCE(p.deductee_type,''), p.lower_tds_rate_bp,
	COALESCE(p.shipping_address,'')`

func scanParty(row pgx.Row) (*Party, error) {
	var p Party
	err := row.Scan(&p.ID, &p.AccountID, &p.Name, &p.Type, &p.Phone, &p.Email, &p.GSTIN, &p.StateCode, &p.Address,
		&p.CreditLimitPaise, &p.CreditDays, &p.GSTVerified, &p.LegalName, &p.ExternalRef, &p.Archived, &p.BalancePaise, &p.CreatedAt,
		&p.PAN, &p.TDSSection, &p.DeducteeType, &p.LowerTDSRateBP, &p.ShippingAddress)
	return &p, err
}

func (s *Service) getParty(ctx context.Context, q db.Querier, tenantID, id string) (*Party, error) {
	p, err := scanParty(q.QueryRow(ctx, `SELECT `+partyCols+` FROM parties p WHERE p.tenant_id=$1 AND p.id=$2`, tenantID, id))
	if db.IsNotFound(err) || isBadUUID(err) {
		return nil, httpx.NotFound("party not found")
	}
	return p, err
}

// PartyInput creates or updates a party.
type PartyInput struct {
	Name                string `json:"name"`
	Type                string `json:"type"`
	Phone               string `json:"phone"`
	Email               string `json:"email"`
	GSTIN               string `json:"gstin"`
	StateCode           string `json:"state_code"`
	Address             string `json:"address"`
	CreditLimitPaise    int64  `json:"credit_limit_paise"`
	CreditDays          int    `json:"credit_days"`
	OpeningBalancePaise int64  `json:"opening_balance_paise"`
	OpeningType         string `json:"opening_type"` // dr | cr
	OpeningAsOf         string `json:"opening_as_of"`
	ExternalRef         string `json:"external_ref"`
	PAN                 string `json:"pan"`
	TDSSection          string `json:"tds_section"`
	DeducteeType        string `json:"deductee_type"`
	LowerTDSRateBP      *int   `json:"lower_tds_rate_bp"`
	ShippingAddress     string `json:"shipping_address"`

	createdVia string // "import" for bulk-created parties (rollback may archive them)
}

// validateTDS normalises PAN / TDS section / deductee type.
func validateTDS(pan, section, dtype *string, lower *int) error {
	if pan != nil {
		*pan = strings.ToUpper(strings.TrimSpace(*pan))
		if *pan != "" && !posting.ValidPAN(*pan) {
			return httpx.Validation("pan must look like AAAAA9999A")
		}
	}
	if section != nil && strings.TrimSpace(*section) != "" {
		sec, ok := posting.LookupSection(*section)
		if !ok {
			return httpx.Validation("unknown tds_section %q", *section)
		}
		*section = sec.Code
	}
	if dtype != nil && *dtype != "" {
		switch *dtype {
		case "individual", "huf", "company", "firm", "other":
		default:
			return httpx.Validation("deductee_type must be individual, huf, company, firm or other")
		}
	}
	if lower != nil && (*lower < 0 || *lower > 10000) {
		return httpx.Validation("lower_tds_rate_bp must be 0..10000")
	}
	return nil
}

// Validate normalises and checks the input.
func (in *PartyInput) Validate() error {
	in.Name = strings.TrimSpace(in.Name)
	in.GSTIN = gst.Normalize(in.GSTIN)
	if in.Name == "" {
		return httpx.Validation("name is required")
	}
	if in.Type == "" {
		in.Type = "customer"
	}
	if in.Type != "customer" && in.Type != "supplier" && in.Type != "both" {
		return httpx.Validation("type must be customer, supplier or both")
	}
	if in.GSTIN != "" {
		if err := gst.ValidateGSTIN(in.GSTIN); err != nil {
			return httpx.Validation("gstin: %s", err.Error())
		}
		if in.StateCode == "" {
			in.StateCode = gst.StateCode(in.GSTIN)
		}
	}
	if in.StateCode != "" && !gst.ValidState(in.StateCode) {
		return httpx.Validation("unknown state_code %s", in.StateCode)
	}
	if in.OpeningBalancePaise < 0 || in.CreditLimitPaise < 0 || in.CreditDays < 0 {
		return httpx.Validation("amounts cannot be negative")
	}
	if in.OpeningBalancePaise > 0 && in.OpeningType != "dr" && in.OpeningType != "cr" {
		return httpx.Validation(`opening_type must be "dr" or "cr"`)
	}
	return validateTDS(&in.PAN, &in.TDSSection, &in.DeducteeType, in.LowerTDSRateBP)
}

// gstinInfo is kar's /internal/v1/gstin/verify response.
type gstinInfo struct {
	Valid     bool   `json:"valid"`
	LegalName string `json:"legal_name"`
	TradeName string `json:"trade_name"`
	Status    string `json:"status"`
	StateCode string `json:"state_code"`
	Address   string `json:"address"`
}

// createParty inserts the party and its ledger account (and opening voucher) inside tx.
func (s *Service) createParty(ctx context.Context, tx pgx.Tx, tenantID, userID string, in PartyInput, verified *gstinInfo) (string, error) {
	if err := setActor(ctx, tx, userID); err != nil {
		return "", err
	}
	if err := EnsureChart(ctx, tx, tenantID); err != nil {
		return "", err
	}
	parent := posting.SundryDebtors
	if in.Type == "supplier" {
		parent = posting.SundryCreditors
	}
	var acctID string
	code := "P-" + strings.ToUpper(strings.ReplaceAll(uuid.NewString(), "-", "")[:12])
	err := tx.QueryRow(ctx, `INSERT INTO accounts (tenant_id, code, name, grp, parent_id)
		SELECT $1, $2, $3, grp, id FROM accounts WHERE tenant_id=$1 AND code=$4 RETURNING id`,
		tenantID, code, in.Name, parent).Scan(&acctID)
	if err != nil {
		return "", err
	}
	legal, gstOK := "", false
	if verified != nil && verified.Valid {
		legal, gstOK = verified.LegalName, strings.EqualFold(verified.Status, "Active")
		if in.Address == "" {
			in.Address = verified.Address
		}
	}
	var id string
	err = tx.QueryRow(ctx, `INSERT INTO parties (tenant_id, account_id, name, type, phone, email, gstin, state_code, address,
		credit_limit_paise, credit_days, gst_verified, legal_name, external_ref, pan, tds_section, deductee_type, lower_tds_rate_bp,
		shipping_address, created_via)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20) RETURNING id`,
		tenantID, acctID, in.Name, in.Type, nilIfEmpty(in.Phone), nilIfEmpty(in.Email), nilIfEmpty(in.GSTIN),
		nilIfEmpty(in.StateCode), nilIfEmpty(in.Address), in.CreditLimitPaise, in.CreditDays, gstOK,
		nilIfEmpty(legal), nilIfEmpty(in.ExternalRef), nilIfEmpty(in.PAN), nilIfEmpty(in.TDSSection),
		nilIfEmpty(in.DeducteeType), in.LowerTDSRateBP, nilIfEmpty(in.ShippingAddress), nilIfEmpty(in.createdVia)).Scan(&id)
	if err != nil {
		return "", err
	}
	if in.OpeningBalancePaise > 0 {
		// Opening balances belong to the start of the financial year, not to the
		// day the party happened to be keyed in.
		asOf := in.OpeningAsOf
		if asOf == "" {
			asOf = s.fyStart()
		} else if _, err := time.Parse(time.DateOnly, asOf); err != nil {
			return "", httpx.Validation("opening_as_of must be YYYY-MM-DD")
		}
		v, err := posting.Opening(posting.Entry{PartyID: id}, in.OpeningBalancePaise, in.OpeningType, asOf, "party:"+id)
		if err != nil {
			return "", httpx.Validation("%s", err.Error())
		}
		if _, err := Post(ctx, tx, tenantID, userID, v); err != nil {
			return "", err
		}
	}
	return id, nil
}

func (s *Service) verifyGSTIN(ctx context.Context, gstin string) (*gstinInfo, error) {
	if s.Kar == nil || gstin == "" {
		return nil, nil
	}
	var info gstinInfo
	if err := s.Kar.Post(ctx, "/internal/v1/gstin/verify", map[string]string{"gstin": gstin}, &info); err != nil {
		return nil, err
	}
	if !info.Valid {
		return nil, httpx.Validation("GSTIN %s is not valid on the GST portal", gstin)
	}
	return &info, nil
}

func (s *Service) handleCreateParty(w http.ResponseWriter, r *http.Request) error {
	var in PartyInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	p := auth.FromContext(r.Context())
	var info *gstinInfo
	if httpx.Query(r, "verify") == "true" && in.GSTIN != "" {
		var err error
		if info, err = s.verifyGSTIN(r.Context(), in.GSTIN); err != nil {
			return err
		}
	}
	var id string
	err := s.WithTx(r.Context(), func(tx pgx.Tx) error {
		// Ledger names are unique per business (as in Tally): a second "Britannia Depot"
		// would split one supplier's balance across two ledgers. The lock makes a
		// double-click or a retried request see the first insert.
		key := strings.ToLower(strings.Join(strings.Fields(in.Name), " "))
		if _, err := tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtext($1 || ':party:' || $2))`, p.TenantID, key); err != nil {
			return err
		}
		var existing string
		err := tx.QueryRow(r.Context(), `SELECT id FROM parties WHERE tenant_id=$1 AND NOT archived
			AND lower(regexp_replace(trim(name), '\s+', ' ', 'g')) = $2 LIMIT 1`, p.TenantID, key).Scan(&existing)
		if err == nil {
			return httpx.Conflict("a party named %q already exists", strings.TrimSpace(in.Name)).
				WithDetails(map[string]any{"existing_id": existing})
		}
		if !db.IsNotFound(err) {
			return err
		}
		id, err = s.createParty(r.Context(), tx, p.TenantID, p.UserID(), in, info)
		return err
	})
	if db.IsUniqueViolation(err) {
		return httpx.Conflict("a party with this external_ref already exists")
	}
	if err != nil {
		return err
	}
	party, err := s.getParty(r.Context(), s.DB, p.TenantID, id)
	if err != nil {
		return err
	}
	return httpx.Created(w, party)
}

func (s *Service) handleListParties(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	page := httpx.ParsePage(r)
	where := []string{"p.tenant_id=$1"}
	args := []any{p.TenantID}
	if t := httpx.Query(r, "type"); t != "" {
		args = append(args, t)
		where = append(where, fmt.Sprintf("(p.type=$%d OR p.type='both')", len(args)))
	}
	if q := httpx.Query(r, "q"); q != "" {
		args = append(args, "%"+strings.ToLower(q)+"%")
		n := len(args)
		where = append(where, fmt.Sprintf("(lower(p.name) LIKE $%d OR p.phone LIKE $%d OR lower(COALESCE(p.gstin,'')) LIKE $%d)", n, n, n))
	}
	if httpx.Query(r, "include_archived") != "true" {
		where = append(where, "NOT p.archived")
	}
	cond := strings.Join(where, " AND ")
	var total int
	if err := s.DB.QueryRow(r.Context(), `SELECT count(*) FROM parties p WHERE `+cond, args...).Scan(&total); err != nil {
		return err
	}
	args = append(args, page.Limit, page.Offset)
	rows, err := s.DB.Query(r.Context(), fmt.Sprintf(`SELECT %s FROM parties p WHERE %s ORDER BY lower(p.name) LIMIT $%d OFFSET $%d`,
		partyCols, cond, len(args)-1, len(args)), args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []Party{}
	for rows.Next() {
		pt, err := scanParty(rows)
		if err != nil {
			return err
		}
		items = append(items, *pt)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return httpx.OK(w, httpx.List[Party]{Items: items, Total: total, Limit: page.Limit, Offset: page.Offset})
}

func (s *Service) handleGetParty(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	party, err := s.getParty(r.Context(), s.DB, p.TenantID, r.PathValue("id"))
	if err != nil {
		return err
	}
	return httpx.OK(w, party)
}

type partyPatch struct {
	Name             *string `json:"name"`
	Phone            *string `json:"phone"`
	Email            *string `json:"email"`
	GSTIN            *string `json:"gstin"`
	StateCode        *string `json:"state_code"`
	Address          *string `json:"address"`
	CreditLimitPaise *int64  `json:"credit_limit_paise"`
	CreditDays       *int    `json:"credit_days"`
	Type             *string `json:"type"`
	Archived         *bool   `json:"archived"`
	PAN              *string `json:"pan"`
	TDSSection       *string `json:"tds_section"`
	DeducteeType     *string `json:"deductee_type"`
	LowerTDSRateBP   *int    `json:"lower_tds_rate_bp"`
	ShippingAddress  *string `json:"shipping_address"`
}

func (in *partyPatch) Validate() error {
	if in.GSTIN != nil && *in.GSTIN != "" {
		g := gst.Normalize(*in.GSTIN)
		if err := gst.ValidateGSTIN(g); err != nil {
			return httpx.Validation("gstin: %s", err.Error())
		}
		in.GSTIN = &g
		if in.StateCode == nil {
			sc := gst.StateCode(g)
			in.StateCode = &sc
		}
	}
	if in.StateCode != nil && *in.StateCode != "" && !gst.ValidState(*in.StateCode) {
		return httpx.Validation("unknown state_code")
	}
	if in.Type != nil && *in.Type != "customer" && *in.Type != "supplier" && *in.Type != "both" {
		return httpx.Validation("type must be customer, supplier or both")
	}
	if in.Name != nil && strings.TrimSpace(*in.Name) == "" {
		return httpx.Validation("name cannot be empty")
	}
	return validateTDS(in.PAN, in.TDSSection, in.DeducteeType, in.LowerTDSRateBP)
}

func (s *Service) handleUpdateParty(w http.ResponseWriter, r *http.Request) error {
	var in partyPatch
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	p := auth.FromContext(r.Context())
	id := r.PathValue("id")
	err := s.WithTx(r.Context(), func(tx pgx.Tx) error {
		if err := setActor(r.Context(), tx, p.UserID()); err != nil {
			return err
		}
		if in.PAN != nil || in.TDSSection != nil || in.DeducteeType != nil || in.LowerTDSRateBP != nil {
			if _, err := tx.Exec(r.Context(), `UPDATE parties SET pan=CASE WHEN $3::text IS NULL THEN pan ELSE NULLIF($3,'') END,
				tds_section=CASE WHEN $4::text IS NULL THEN tds_section ELSE NULLIF($4,'') END,
				deductee_type=CASE WHEN $5::text IS NULL THEN deductee_type ELSE NULLIF($5,'') END,
				lower_tds_rate_bp=COALESCE($6, lower_tds_rate_bp) WHERE tenant_id=$1 AND id::text=$2`,
				p.TenantID, id, in.PAN, in.TDSSection, in.DeducteeType, in.LowerTDSRateBP); err != nil {
				return err
			}
		}
		tag, err := tx.Exec(r.Context(), `UPDATE parties SET name=COALESCE($3,name), phone=COALESCE($4,phone),
			email=COALESCE($5,email), gstin=COALESCE(NULLIF($6,''),gstin), state_code=COALESCE($7,state_code),
			address=COALESCE($8,address), credit_limit_paise=COALESCE($9,credit_limit_paise),
			credit_days=COALESCE($10,credit_days), type=COALESCE($11,type), archived=COALESCE($12,archived),
			gst_verified = CASE WHEN $6::text IS NOT NULL AND $6 <> COALESCE(gstin,'') THEN false ELSE gst_verified END,
			shipping_address=COALESCE($13,shipping_address), updated_at=now() WHERE tenant_id=$1 AND id=$2`,
			p.TenantID, id, in.Name, in.Phone, in.Email, in.GSTIN, in.StateCode, in.Address, in.CreditLimitPaise,
			in.CreditDays, in.Type, in.Archived, in.ShippingAddress)
		if isBadUUID(err) {
			return httpx.NotFound("party not found")
		}
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return httpx.NotFound("party not found")
		}
		if in.Name != nil {
			_, err = tx.Exec(r.Context(), `UPDATE accounts SET name=$3 WHERE tenant_id=$1 AND id=(SELECT account_id FROM parties WHERE id=$2)`,
				p.TenantID, id, *in.Name)
		}
		return err
	})
	if err != nil {
		return err
	}
	party, err := s.getParty(r.Context(), s.DB, p.TenantID, id)
	if err != nil {
		return err
	}
	return httpx.OK(w, party)
}

// handleDeleteParty archives (soft-deletes); ledger history must remain.
func (s *Service) handleDeleteParty(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	tag, err := s.DB.Exec(r.Context(), `UPDATE parties SET archived=true, updated_at=now() WHERE tenant_id=$1 AND id=$2`,
		p.TenantID, r.PathValue("id"))
	if isBadUUID(err) || (err == nil && tag.RowsAffected() == 0) {
		return httpx.NotFound("party not found")
	}
	if err != nil {
		return err
	}
	return httpx.NoContent(w)
}

// internalParty is the shape aadesh and others read (ARCHITECTURE §6).
type internalParty struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Type             string `json:"type"`
	Phone            string `json:"phone"`
	Email            string `json:"email"`
	GSTIN            string `json:"gstin"`
	StateCode        string `json:"state_code"`
	Address          string `json:"address"`
	CreditLimitPaise int64  `json:"credit_limit_paise"`
	CreditDays       int    `json:"credit_days"` // aadesh derives due dates and enforces credit from these
	BalancePaise     int64  `json:"balance_paise"`
}

func (s *Service) handleInternalParty(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	if p.TenantID == "" {
		return httpx.BadRequest("X-Tenant-ID header is required")
	}
	pt, err := s.getParty(r.Context(), s.DB, p.TenantID, r.PathValue("id"))
	if err != nil {
		return err
	}
	return httpx.OK(w, internalParty{ID: pt.ID, Name: pt.Name, Type: pt.Type, Phone: pt.Phone, Email: pt.Email,
		GSTIN: pt.GSTIN, StateCode: pt.StateCode, Address: pt.Address, CreditLimitPaise: pt.CreditLimitPaise,
		CreditDays: pt.CreditDays, BalancePaise: pt.BalancePaise})
}

// BulkResult is the shape every bulk endpoint returns. Results carries one
// entry per accepted record (index into the request, the id bahi stored, and
// whether this call created it) so an importer can later roll back only what
// it created.
type BulkResult struct {
	Created int              `json:"created"`
	Updated int              `json:"updated"`
	Errors  []BulkError      `json:"errors"`
	Results []BulkItemResult `json:"results,omitempty"`
}

// BulkError reports one failed row.
type BulkError struct {
	Index   int    `json:"index"`
	Message string `json:"message"`
}

// BulkItemResult is the outcome of one accepted record.
type BulkItemResult struct {
	Index   int    `json:"index"`
	ID      string `json:"id,omitempty"`
	Created bool   `json:"created"`
}

func newBulkResult() BulkResult {
	return BulkResult{Errors: []BulkError{}, Results: []BulkItemResult{}}
}

func (r *BulkResult) fail(i int, err error) {
	r.Errors = append(r.Errors, BulkError{i, httpx.AsError(err).Message})
}

func (r *BulkResult) failf(i int, format string, args ...any) {
	r.Errors = append(r.Errors, BulkError{i, fmt.Sprintf(format, args...)})
}

func (r *BulkResult) ok(i int, id string, created bool) {
	if created {
		r.Created++
	} else {
		r.Updated++
	}
	r.Results = append(r.Results, BulkItemResult{Index: i, ID: id, Created: created})
}

// internalTenant is the tenant of an internal (service-secret) call.
func internalTenant(r *http.Request) (*auth.Principal, error) {
	p := auth.FromContext(r.Context())
	if p.TenantID == "" {
		return nil, httpx.BadRequest("X-Tenant-ID header is required")
	}
	return p, nil
}

type bulkPartiesInput struct {
	Parties []PartyInput `json:"parties"`
}

// handleBulkParties upserts parties by external_ref (used by aayaat). Each row
// runs in its own transaction so one bad row doesn't sink the batch. A
// re-import updates the master fields (name, type, contact, GSTIN, address,
// PAN, shipping address, credit limit and days) but never posts a second
// opening balance.
func (s *Service) handleBulkParties(w http.ResponseWriter, r *http.Request) error {
	var in bulkPartiesInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	p, err := internalTenant(r)
	if err != nil {
		return err
	}
	if len(in.Parties) > 2000 {
		return httpx.Validation("at most 2000 parties per call")
	}
	res := newBulkResult()
	for i := range in.Parties {
		row := in.Parties[i]
		if err := row.Validate(); err != nil {
			res.fail(i, err)
			continue
		}
		row.createdVia = "import"
		var id string
		updated := false
		err := s.WithTx(r.Context(), func(tx pgx.Tx) error {
			updated = false
			if err := setActor(r.Context(), tx, p.UserID()); err != nil {
				return err
			}
			if row.ExternalRef != "" {
				err := tx.QueryRow(r.Context(), `UPDATE parties SET name=$3, type=$4, phone=COALESCE($5,phone),
					gstin=COALESCE($6,gstin), state_code=COALESCE($7,state_code), address=COALESCE($8,address),
					email=COALESCE($9,email), credit_limit_paise=$10, credit_days=$11, pan=COALESCE($12,pan),
					shipping_address=COALESCE($13,shipping_address), updated_at=now()
					WHERE tenant_id=$1 AND external_ref=$2 RETURNING id`, p.TenantID, row.ExternalRef, row.Name, row.Type,
					nilIfEmpty(row.Phone), nilIfEmpty(row.GSTIN), nilIfEmpty(row.StateCode), nilIfEmpty(row.Address),
					nilIfEmpty(row.Email), row.CreditLimitPaise, row.CreditDays, nilIfEmpty(row.PAN),
					nilIfEmpty(row.ShippingAddress)).Scan(&id)
				if err == nil {
					updated = true
					_, err = tx.Exec(r.Context(), `UPDATE accounts SET name=$3 WHERE tenant_id=$1
						AND id=(SELECT account_id FROM parties WHERE tenant_id=$1 AND id=$2)`, p.TenantID, id, row.Name)
					return err
				}
				if !db.IsNotFound(err) {
					return err
				}
			}
			var err error
			id, err = s.createParty(r.Context(), tx, p.TenantID, p.UserID(), row, nil)
			return err
		})
		if err != nil {
			res.fail(i, err)
			continue
		}
		res.ok(i, id, !updated)
	}
	return httpx.OK(w, res)
}

type openingLine struct {
	AccountCode      string `json:"account_code"`
	PartyExternalRef string `json:"party_external_ref"`
	AmountPaise      int64  `json:"amount_paise"`
	Type             string `json:"type"`
	ExternalRef      string `json:"external_ref"` // the importer's key, kept on the voucher
}

type bulkOpeningInput struct {
	AsOf     string        `json:"as_of"`
	Balances []openingLine `json:"balances"`
}

// openingKey is the source_id of a bulk opening line: one per as_of and
// account / party, whatever external_ref the importer sends.
func openingKey(asOf, key string) string { return "bulk:" + asOf + ":" + key }

// handleBulkOpening posts one opening voucher per line against Opening Balance
// Difference. Re-running the same import is a no-op (source_id per line).
func (s *Service) handleBulkOpening(w http.ResponseWriter, r *http.Request) error {
	var in bulkOpeningInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	p, err := internalTenant(r)
	if err != nil {
		return err
	}
	if _, err := time.Parse(time.DateOnly, in.AsOf); err != nil {
		return httpx.Validation("as_of must be YYYY-MM-DD")
	}
	res := newBulkResult()
	for i, l := range in.Balances {
		var target posting.Entry
		var key string
		switch {
		case l.AccountCode != "":
			target, key = posting.Entry{AccountCode: l.AccountCode}, "acct:"+l.AccountCode
		case l.PartyExternalRef != "":
			var pid string
			err := s.DB.QueryRow(r.Context(), `SELECT id FROM parties WHERE tenant_id=$1 AND external_ref=$2`,
				p.TenantID, l.PartyExternalRef).Scan(&pid)
			if err != nil {
				res.failf(i, "party_external_ref not found")
				continue
			}
			target, key = posting.Entry{PartyID: pid}, "party:"+pid
		default:
			res.failf(i, "account_code or party_external_ref is required")
			continue
		}
		v, err := posting.Opening(target, l.AmountPaise, l.Type, in.AsOf, openingKey(in.AsOf, key))
		if err != nil {
			res.failf(i, "%s", err.Error())
			continue
		}
		var posted Posted
		err = s.WithTx(r.Context(), func(tx pgx.Tx) error {
			var err error
			if posted, err = Post(r.Context(), tx, p.TenantID, p.UserID(), v); err != nil {
				return err
			}
			if posted.Created && l.ExternalRef != "" {
				_, err = tx.Exec(r.Context(), `UPDATE vouchers SET external_ref=$3 WHERE tenant_id=$1 AND id=$2`,
					p.TenantID, posted.ID, l.ExternalRef)
			}
			return err
		})
		if err != nil {
			res.fail(i, err)
			continue
		}
		res.ok(i, posted.ID, posted.Created) // not created = already imported
	}
	return httpx.OK(w, res)
}
