package identity

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/maapan/mool/auth"
	"github.com/maapan/mool/db"
	"github.com/maapan/mool/gst"
	"github.com/maapan/mool/httpx"
)

// Registration is one GSTIN of a business, or its unregistered identity
// (GSTIN ""). Every store bills under exactly one; a tenant has exactly one
// primary (PHASE3-CONTRACTS.md §1).
type Registration struct {
	ID                 string    `json:"id"`
	TenantID           string    `json:"tenant_id"`
	GSTIN              string    `json:"gstin"` // "" = unregistered
	LegalName          string    `json:"legal_name"`
	TradeName          string    `json:"trade_name"`
	StateCode          string    `json:"state_code"`
	Address            string    `json:"address"`
	City               string    `json:"city"`
	PIN                string    `json:"pin"`
	Phone              string    `json:"phone"`
	Email              string    `json:"email"`
	Composition        bool      `json:"composition"`
	EInvoiceApplicable bool      `json:"einvoice_applicable"`
	IsPrimary          bool      `json:"is_primary"`
	Status             string    `json:"status"` // active | inactive
	StoreCount         int       `json:"store_count"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// RegistrationInput creates or updates a registration. On update, omitted
// is_primary and status keep their current values; every other field is replaced.
type RegistrationInput struct {
	GSTIN              string `json:"gstin,omitempty"`
	LegalName          string `json:"legal_name,omitempty"` // defaults to the business name
	TradeName          string `json:"trade_name,omitempty"`
	StateCode          string `json:"state_code,omitempty"` // defaults to the GSTIN's state
	Address            string `json:"address,omitempty"`
	City               string `json:"city,omitempty"`
	PIN                string `json:"pin,omitempty"`
	Phone              string `json:"phone,omitempty"`
	Email              string `json:"email,omitempty"`
	Composition        bool   `json:"composition,omitempty"`
	EInvoiceApplicable bool   `json:"einvoice_applicable,omitempty"`
	IsPrimary          *bool  `json:"is_primary,omitempty"`
	Status             string `json:"status,omitempty"`
}

var pinPattern = regexp.MustCompile(`^[1-9][0-9]{5}$`)

func validPIN(pin string) error {
	if pin != "" && !pinPattern.MatchString(pin) {
		return httpx.Validation("pin must be a 6-digit Indian PIN code")
	}
	return nil
}

// Validate normalises and checks a registration.
func (in *RegistrationInput) Validate() error {
	in.GSTIN = gst.Normalize(in.GSTIN)
	in.LegalName, in.TradeName = strings.TrimSpace(in.LegalName), strings.TrimSpace(in.TradeName)
	in.StateCode = strings.TrimSpace(in.StateCode)
	in.Address, in.City, in.PIN = strings.TrimSpace(in.Address), strings.TrimSpace(in.City), strings.TrimSpace(in.PIN)
	in.Phone, in.Email = strings.TrimSpace(in.Phone), strings.TrimSpace(in.Email)
	in.Status = strings.ToLower(strings.TrimSpace(in.Status))
	if in.GSTIN != "" {
		if err := gst.ValidateGSTIN(in.GSTIN); err != nil {
			return httpx.Validation("gstin: %s", err.Error())
		}
		if in.StateCode == "" {
			in.StateCode = gst.StateCode(in.GSTIN)
		}
		if in.StateCode != gst.StateCode(in.GSTIN) {
			return httpx.Validation("state_code must be the GSTIN's state (%s)", gst.StateCode(in.GSTIN))
		}
	}
	if !gst.ValidState(in.StateCode) {
		return httpx.Validation("state_code must be a valid GST state code")
	}
	if err := validPIN(in.PIN); err != nil {
		return err
	}
	if len(in.LegalName) > 100 || len(in.TradeName) > 100 || len(in.City) > 50 || len(in.Address) > 200 {
		return httpx.Validation("legal_name/trade_name ≤ 100, city ≤ 50 and address ≤ 200 characters")
	}
	if in.Status != "" && in.Status != "active" && in.Status != "inactive" {
		return httpx.Validation("status must be active or inactive")
	}
	if in.GSTIN == "" && (in.Composition || in.EInvoiceApplicable) {
		return httpx.Validation("composition and e-invoicing apply to a registered GSTIN only")
	}
	if in.Composition && in.EInvoiceApplicable {
		return httpx.Validation("composition taxpayers are not covered by e-invoicing")
	}
	return nil
}

// --- SQL ---

const registrationCols = `r.id, r.tenant_id, COALESCE(r.gstin,''), r.legal_name, COALESCE(r.trade_name,''), r.state_code,
	COALESCE(r.address,''), COALESCE(r.city,''), COALESCE(r.pin,''), COALESCE(r.phone,''), COALESCE(r.email,''),
	r.composition, r.einvoice_applicable, r.is_primary, r.status,
	(SELECT count(*) FROM stores s WHERE s.registration_id = r.id), r.created_at, r.updated_at`

func scanRegistration(row interface{ Scan(...any) error }) (Registration, error) {
	var g Registration
	err := row.Scan(&g.ID, &g.TenantID, &g.GSTIN, &g.LegalName, &g.TradeName, &g.StateCode, &g.Address, &g.City, &g.PIN,
		&g.Phone, &g.Email, &g.Composition, &g.EInvoiceApplicable, &g.IsPrimary, &g.Status, &g.StoreCount, &g.CreatedAt, &g.UpdatedAt)
	return g, err
}

// getRegistration loads a registration; tenantID "" means any tenant (internal calls).
func getRegistration(ctx context.Context, q db.Querier, tenantID, id string) (Registration, error) {
	if _, err := uuid.Parse(id); err != nil {
		return Registration{}, pgx.ErrNoRows
	}
	if tenantID == "" {
		return scanRegistration(q.QueryRow(ctx, `SELECT `+registrationCols+` FROM registrations r WHERE r.id=$1`, id))
	}
	return scanRegistration(q.QueryRow(ctx, `SELECT `+registrationCols+` FROM registrations r WHERE r.tenant_id=$1 AND r.id=$2`, tenantID, id))
}

func primaryRegistration(ctx context.Context, q db.Querier, tenantID string) (Registration, error) {
	return scanRegistration(q.QueryRow(ctx, `SELECT `+registrationCols+` FROM registrations r WHERE r.tenant_id=$1 AND r.is_primary`, tenantID))
}

func registrationByGSTIN(ctx context.Context, q db.Querier, tenantID, gstin string) (Registration, error) {
	return scanRegistration(q.QueryRow(ctx, `SELECT `+registrationCols+` FROM registrations r WHERE r.tenant_id=$1 AND r.gstin=$2`, tenantID, gstin))
}

// listRegistrations lists a tenant's registrations, primary first. gstin "" lists all.
func listRegistrations(ctx context.Context, q db.Querier, tenantID, gstin string) ([]Registration, error) {
	sql := `SELECT ` + registrationCols + ` FROM registrations r WHERE r.tenant_id=$1`
	args := []any{tenantID}
	if gstin != "" {
		sql += ` AND r.gstin=$2`
		args = append(args, gst.Normalize(gstin))
	}
	rows, err := q.Query(ctx, sql+` ORDER BY r.is_primary DESC, r.created_at`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Registration{}
	for rows.Next() {
		g, err := scanRegistration(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func insertRegistration(ctx context.Context, q db.Querier, tenantID string, in RegistrationInput, primary bool, status string) (string, error) {
	var id string
	err := q.QueryRow(ctx, `INSERT INTO registrations (tenant_id, gstin, legal_name, trade_name, state_code, address, city, pin,
		phone, email, composition, einvoice_applicable, is_primary, status)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14) RETURNING id`,
		tenantID, nilIfEmpty(in.GSTIN), in.LegalName, nilIfEmpty(in.TradeName), in.StateCode, nilIfEmpty(in.Address),
		nilIfEmpty(in.City), nilIfEmpty(in.PIN), nilIfEmpty(in.Phone), nilIfEmpty(in.Email), in.Composition,
		in.EInvoiceApplicable, primary, status).Scan(&id)
	return id, err
}

// syncTenantFromPrimary keeps tenants.gstin/pan/state_code/address equal to the
// primary registration for readers that predate registrations.
func syncTenantFromPrimary(ctx context.Context, q db.Querier, tenantID string) error {
	_, err := q.Exec(ctx, `UPDATE tenants t SET gstin = r.gstin, pan = COALESCE(substr(r.gstin, 3, 10), t.pan),
		state_code = r.state_code, address = COALESCE(r.address, t.address), updated_at = now()
		FROM registrations r WHERE r.tenant_id = t.id AND r.is_primary AND t.id = $1`, tenantID)
	return err
}

// --- service ---

// Registrations lists the tenant's registrations (gstin "" = all).
func (s *Service) Registrations(ctx context.Context, tenantID, gstin string) ([]Registration, error) {
	return listRegistrations(ctx, s.DB, tenantID, gstin)
}

// Registration returns one registration of the tenant (tenantID "" = any tenant).
func (s *Service) Registration(ctx context.Context, tenantID, id string) (*Registration, error) {
	g, err := getRegistration(ctx, s.DB, tenantID, id)
	if err != nil {
		return nil, notFound(err, "registration")
	}
	return &g, nil
}

// CreateRegistration adds a GSTIN (or the unregistered identity) to the business.
// is_primary=true makes it the primary and demotes the previous one.
func (s *Service) CreateRegistration(ctx context.Context, p *auth.Principal, in RegistrationInput) (*Registration, error) {
	var id string
	err := db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		t, err := getTenant(ctx, tx, p.TenantID)
		if err != nil {
			return notFound(err, "tenant")
		}
		if _, err := tx.Exec(ctx, `SELECT 1 FROM tenants WHERE id=$1 FOR UPDATE`, p.TenantID); err != nil {
			return err // serialises primary changes per tenant
		}
		if in.LegalName == "" {
			in.LegalName = t.BusinessName
		}
		status := in.Status
		if status == "" {
			status = "active"
		}
		var existing int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM registrations WHERE tenant_id=$1`, p.TenantID).Scan(&existing); err != nil {
			return err
		}
		primary := existing == 0 || (in.IsPrimary != nil && *in.IsPrimary)
		if existing == 0 && in.IsPrimary != nil && !*in.IsPrimary {
			return httpx.Validation("the first registration of a business is its primary")
		}
		if primary && status != "active" {
			return httpx.Validation("the primary registration must be active")
		}
		if primary {
			if _, err := tx.Exec(ctx, `UPDATE registrations SET is_primary=false, updated_at=now() WHERE tenant_id=$1 AND is_primary`, p.TenantID); err != nil {
				return err
			}
		}
		id, err = insertRegistration(ctx, tx, p.TenantID, in, primary, status)
		if db.IsUniqueViolation(err) {
			return httpx.Conflict("GSTIN %s is already a registration of this business", in.GSTIN)
		}
		if err != nil {
			return err
		}
		if primary {
			if err := syncTenantFromPrimary(ctx, tx, p.TenantID); err != nil {
				return err
			}
		}
		audit(ctx, tx, &p.TenantID, p.UserID(), "registration.create", id, "", map[string]any{"gstin": in.GSTIN, "primary": primary})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.Registration(ctx, p.TenantID, id)
}

// UpdateRegistration replaces a registration's details. A business always has
// exactly one active primary, and a registration stores still bill under
// cannot be deactivated. A GSTIN change is carried to the stores that had the
// old GSTIN written on them.
func (s *Service) UpdateRegistration(ctx context.Context, p *auth.Principal, id string, in RegistrationInput) (*Registration, error) {
	err := db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT 1 FROM tenants WHERE id=$1 FOR UPDATE`, p.TenantID); err != nil {
			return err
		}
		cur, err := getRegistration(ctx, tx, p.TenantID, id)
		if err != nil {
			return notFound(err, "registration")
		}
		if in.LegalName == "" {
			in.LegalName = cur.LegalName
		}
		primary, status := cur.IsPrimary, cur.Status
		if in.IsPrimary != nil {
			primary = *in.IsPrimary
		}
		if in.Status != "" {
			status = in.Status
		}
		if cur.IsPrimary && !primary {
			return httpx.Validation("a business needs a primary registration: make another registration primary instead")
		}
		if primary && status != "active" {
			return httpx.Validation("the primary registration cannot be inactive")
		}
		if status == "inactive" && cur.Status != "inactive" && cur.StoreCount > 0 {
			return httpx.Conflict("%d store(s) bill under this registration; move them to another registration first", cur.StoreCount)
		}
		if primary && !cur.IsPrimary {
			if _, err := tx.Exec(ctx, `UPDATE registrations SET is_primary=false, updated_at=now() WHERE tenant_id=$1 AND is_primary`, p.TenantID); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `UPDATE registrations SET gstin=$3, legal_name=$4, trade_name=$5, state_code=$6, address=$7, city=$8,
			pin=$9, phone=$10, email=$11, composition=$12, einvoice_applicable=$13, is_primary=$14, status=$15, updated_at=now()
			WHERE tenant_id=$1 AND id=$2`,
			p.TenantID, id, nilIfEmpty(in.GSTIN), in.LegalName, nilIfEmpty(in.TradeName), in.StateCode, nilIfEmpty(in.Address),
			nilIfEmpty(in.City), nilIfEmpty(in.PIN), nilIfEmpty(in.Phone), nilIfEmpty(in.Email), in.Composition,
			in.EInvoiceApplicable, primary, status)
		if db.IsUniqueViolation(err) {
			return httpx.Conflict("GSTIN %s is already a registration of this business", in.GSTIN)
		}
		if err != nil {
			return err
		}
		if in.GSTIN != cur.GSTIN {
			if _, err := tx.Exec(ctx, `UPDATE stores SET gstin=$2, updated_at=now() WHERE registration_id=$1 AND gstin IS NOT NULL`,
				id, nilIfEmpty(in.GSTIN)); err != nil {
				return err
			}
		}
		if primary {
			if err := syncTenantFromPrimary(ctx, tx, p.TenantID); err != nil {
				return err
			}
		}
		audit(ctx, tx, &p.TenantID, p.UserID(), "registration.update", id, "", map[string]any{"gstin": in.GSTIN, "primary": primary, "status": status})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.Registration(ctx, p.TenantID, id)
}

// DeactivateRegistration retires a registration (DELETE). It is a soft delete:
// invoices issued under it keep pointing at it.
func (s *Service) DeactivateRegistration(ctx context.Context, p *auth.Principal, id string) (*Registration, error) {
	cur, err := s.Registration(ctx, p.TenantID, id)
	if err != nil {
		return nil, err
	}
	in := registrationInputOf(*cur)
	in.Status = "inactive"
	return s.UpdateRegistration(ctx, p, id, in)
}

func registrationInputOf(g Registration) RegistrationInput {
	return RegistrationInput{GSTIN: g.GSTIN, LegalName: g.LegalName, TradeName: g.TradeName, StateCode: g.StateCode,
		Address: g.Address, City: g.City, PIN: g.PIN, Phone: g.Phone, Email: g.Email, Composition: g.Composition,
		EInvoiceApplicable: g.EInvoiceApplicable, Status: g.Status}
}

// storeRegistration picks and checks the registration a store bills under
// (cur is the stored store on update, nil on create) and fills in.StateCode.
func storeRegistration(ctx context.Context, q db.Querier, tenantID string, in *StoreInput, cur *Store) (Registration, error) {
	var reg Registration
	var err error
	switch {
	case in.RegistrationID != "":
		reg, err = getRegistration(ctx, q, tenantID, in.RegistrationID)
		if db.IsNotFound(err) {
			return reg, httpx.Validation("registration_id is not a registration of this business")
		}
	case cur != nil:
		reg, err = getRegistration(ctx, q, tenantID, cur.RegistrationID)
	case in.GSTIN != "":
		reg, err = registrationByGSTIN(ctx, q, tenantID, in.GSTIN)
		if db.IsNotFound(err) {
			return reg, httpx.Validation("GSTIN %s is not a registration of this business; add it under /v1/registrations first", in.GSTIN)
		}
	default:
		reg, err = primaryRegistration(ctx, q, tenantID)
	}
	if err != nil {
		return reg, err
	}
	if reg.Status != "active" && (cur == nil || cur.RegistrationID != reg.ID) {
		return reg, httpx.Validation("registration %s is inactive", reg.ID)
	}
	if in.GSTIN != "" && in.GSTIN != reg.GSTIN {
		if reg.GSTIN == "" {
			return reg, httpx.Validation("the store's registration is unregistered; it cannot carry GSTIN %s", in.GSTIN)
		}
		return reg, httpx.Validation("a store's GSTIN must be its registration's (%s)", reg.GSTIN)
	}
	if in.StateCode == "" {
		in.StateCode = reg.StateCode
	}
	changed := cur == nil || cur.RegistrationID != reg.ID || cur.StateCode != in.StateCode
	if reg.GSTIN != "" && in.StateCode != reg.StateCode && changed {
		return reg, httpx.Validation("a store in state %s cannot bill under GSTIN %s (state %s); add a registration for that state",
			in.StateCode, reg.GSTIN, reg.StateCode)
	}
	in.RegistrationID = reg.ID
	return reg, nil
}

// CreateStore adds a store under a registration (default: the registration of
// its GSTIN, else the primary).
func (s *Service) CreateStore(ctx context.Context, p *auth.Principal, in StoreInput) (*Store, error) {
	var st Store
	err := db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if _, err := storeRegistration(ctx, tx, p.TenantID, &in, nil); err != nil {
			return err
		}
		var err error
		st, err = insertStore(ctx, tx, p.TenantID, in)
		if db.IsUniqueViolation(err) {
			return httpx.Conflict("store code %s already exists", in.Code)
		}
		if err != nil {
			return err
		}
		audit(ctx, tx, &p.TenantID, p.UserID(), "store.create", st.ID, "", map[string]any{"code": st.Code, "registration_id": st.RegistrationID})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &st, nil
}

// UpdateStore replaces a store's details; registration_id "" keeps its registration.
func (s *Service) UpdateStore(ctx context.Context, p *auth.Principal, id string, in StoreInput) (*Store, error) {
	var st Store
	err := db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		cur, err := getStore(ctx, tx, p.TenantID, id)
		if err != nil {
			return notFound(err, "store")
		}
		if _, err := storeRegistration(ctx, tx, p.TenantID, &in, &cur); err != nil {
			return err
		}
		st, err = updateStore(ctx, tx, p.TenantID, id, in)
		if db.IsUniqueViolation(err) {
			return httpx.Conflict("store code %s already exists", in.Code)
		}
		if err != nil {
			return notFound(err, "store")
		}
		audit(ctx, tx, &p.TenantID, p.UserID(), "store.update", st.ID, "", map[string]any{"registration_id": st.RegistrationID})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &st, nil
}

// UpdateTenant updates the business profile and carries GSTIN, state and
// address to the primary registration (and the GSTIN to its stores).
func (s *Service) UpdateTenant(ctx context.Context, p *auth.Principal, in TenantInput) (*Tenant, error) {
	var t Tenant
	err := db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT 1 FROM tenants WHERE id=$1 FOR UPDATE`, p.TenantID); err != nil {
			return err
		}
		var pan *string
		if in.GSTIN != "" {
			x := in.GSTIN[2:12]
			pan = &x
		}
		var err error
		t, err = scanTenant(tx.QueryRow(ctx, `UPDATE tenants SET business_name=$2, gstin=$3, pan=COALESCE($4, pan),
			state_code=$5, address=$6, email=$7, phone=$8, updated_at=now() WHERE id=$1 RETURNING `+tenantCols,
			p.TenantID, in.BusinessName, nilIfEmpty(in.GSTIN), pan, in.StateCode, nilIfEmpty(in.Address),
			nilIfEmpty(in.Email), nilIfEmpty(in.Phone)))
		if err != nil {
			return notFound(err, "tenant")
		}
		prim, err := primaryRegistration(ctx, tx, p.TenantID)
		if db.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if in.GSTIN == "" && (prim.Composition || prim.EInvoiceApplicable) {
			return httpx.Validation("the primary registration is composition/e-invoicing; it cannot become unregistered")
		}
		_, err = tx.Exec(ctx, `UPDATE registrations SET gstin=$2, state_code=$3, address=COALESCE($4, address), updated_at=now() WHERE id=$1`,
			prim.ID, nilIfEmpty(in.GSTIN), in.StateCode, nilIfEmpty(in.Address))
		if db.IsUniqueViolation(err) {
			return httpx.Conflict("GSTIN %s is already another registration of this business", in.GSTIN)
		}
		if err != nil {
			return err
		}
		if in.GSTIN != prim.GSTIN {
			if _, err := tx.Exec(ctx, `UPDATE stores SET gstin=$2, updated_at=now() WHERE registration_id=$1 AND gstin IS NOT NULL`,
				prim.ID, nilIfEmpty(in.GSTIN)); err != nil {
				return err
			}
		}
		audit(ctx, tx, &p.TenantID, p.UserID(), "tenant.update", p.TenantID, "", nil)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// StoreInfo is the internal store shape (PHASE3-CONTRACTS.md §1): the store
// plus the registration it bills under. gstin, state_code and address come from
// the registration when the store has none of its own.
type StoreInfo struct {
	ID                  string `json:"id"`
	TenantID            string `json:"tenant_id"`
	Name                string `json:"name"`
	Code                string `json:"code"`
	StateCode           string `json:"state_code"`
	GSTIN               string `json:"gstin"`
	Address             string `json:"address"`
	Phone               string `json:"phone"`
	Active              bool   `json:"active"`
	RegistrationID      string `json:"registration_id"`
	LegalName           string `json:"legal_name"`
	TradeName           string `json:"trade_name"`
	City                string `json:"city"`
	PIN                 string `json:"pin"`
	Composition         bool   `json:"composition"`
	EInvoiceApplicable  bool   `json:"einvoice_applicable"`
	RegistrationPrimary bool   `json:"registration_primary"`
}

// InternalStore resolves a store with its registration.
func (s *Service) InternalStore(ctx context.Context, id string) (*StoreInfo, error) {
	if _, err := uuid.Parse(id); err != nil {
		return nil, httpx.NotFound("store not found")
	}
	var i StoreInfo
	err := s.DB.QueryRow(ctx, `SELECT s.id, s.tenant_id, s.name, s.code, COALESCE(NULLIF(s.state_code,''), r.state_code),
		COALESCE(NULLIF(s.gstin,''), r.gstin, ''), COALESCE(NULLIF(s.address,''), r.address, ''), COALESCE(s.phone,''), s.active,
		r.id, r.legal_name, COALESCE(r.trade_name,''), COALESCE(r.city,''), COALESCE(r.pin,''), r.composition,
		r.einvoice_applicable, r.is_primary
		FROM stores s JOIN registrations r ON r.id = s.registration_id WHERE s.id=$1`, id).Scan(
		&i.ID, &i.TenantID, &i.Name, &i.Code, &i.StateCode, &i.GSTIN, &i.Address, &i.Phone, &i.Active,
		&i.RegistrationID, &i.LegalName, &i.TradeName, &i.City, &i.PIN, &i.Composition, &i.EInvoiceApplicable, &i.RegistrationPrimary)
	if err != nil {
		return nil, notFound(err, "store")
	}
	return &i, nil
}

// --- handlers ---

func (s *Service) listRegistrations(w http.ResponseWriter, r *http.Request) error {
	items, err := s.Registrations(r.Context(), auth.FromContext(r.Context()).TenantID, httpx.Query(r, "gstin"))
	if err != nil {
		return err
	}
	return httpx.OK(w, httpx.List[Registration]{Items: items, Total: len(items), Limit: len(items)})
}

func (s *Service) getRegistration(w http.ResponseWriter, r *http.Request) error {
	g, err := s.Registration(r.Context(), auth.FromContext(r.Context()).TenantID, r.PathValue("id"))
	if err != nil {
		return err
	}
	return httpx.OK(w, g)
}

func (s *Service) createRegistration(w http.ResponseWriter, r *http.Request) error {
	var in RegistrationInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	g, err := s.CreateRegistration(r.Context(), auth.FromContext(r.Context()), in)
	if err != nil {
		return err
	}
	return httpx.Created(w, g)
}

func (s *Service) updateRegistration(w http.ResponseWriter, r *http.Request) error {
	var in RegistrationInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	g, err := s.UpdateRegistration(r.Context(), auth.FromContext(r.Context()), r.PathValue("id"), in)
	if err != nil {
		return err
	}
	return httpx.OK(w, g)
}

func (s *Service) deleteRegistration(w http.ResponseWriter, r *http.Request) error {
	g, err := s.DeactivateRegistration(r.Context(), auth.FromContext(r.Context()), r.PathValue("id"))
	if err != nil {
		return err
	}
	return httpx.OK(w, g)
}

// internalRegistration serves GET /internal/v1/registrations/{id}; an
// X-Tenant-ID header, when sent, must own it.
func (s *Service) internalRegistration(w http.ResponseWriter, r *http.Request) error {
	g, err := s.Registration(r.Context(), auth.FromContext(r.Context()).TenantID, r.PathValue("id"))
	if err != nil {
		return err
	}
	return httpx.OK(w, g)
}

// internalRegistrations serves GET /internal/v1/registrations?tenant_id=&gstin=
// (the tenant may also come from X-Tenant-ID).
func (s *Service) internalRegistrations(w http.ResponseWriter, r *http.Request) error {
	tenant := httpx.Query(r, "tenant_id")
	if h := auth.FromContext(r.Context()).TenantID; tenant == "" {
		tenant = h
	} else if h != "" && !strings.EqualFold(h, tenant) {
		return httpx.Forbidden("tenant_id does not match X-Tenant-ID")
	}
	if _, err := uuid.Parse(tenant); err != nil {
		return httpx.Validation("tenant_id (or X-Tenant-ID) is required")
	}
	items, err := s.Registrations(r.Context(), tenant, httpx.Query(r, "gstin"))
	if err != nil {
		return err
	}
	return httpx.OK(w, httpx.List[Registration]{Items: items, Total: len(items), Limit: len(items)})
}
