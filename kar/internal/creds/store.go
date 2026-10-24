package creds

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/taraju/kar/internal/gsp"
	"github.com/taraju/mool/db"
	"github.com/taraju/mool/gst"
)

// Store keeps gsp_credentials: one row per (tenant, GSTIN) with the e-invoice
// and e-way API users. Passwords are sealed; every query filters tenant_id.
type Store struct {
	DB     *pgxpool.Pool
	Cipher *Cipher
}

func aad(tenantID, gstin, column string) string { return tenantID + "|" + gstin + "|" + column }

// Input is the body of PUT /v1/settings/gsp-credentials/{gstin}. An empty
// password keeps the stored one (so a username can be corrected alone).
type Input struct {
	EInvoiceUsername string `json:"einvoice_username"`
	EInvoicePassword string `json:"einvoice_password"`
	EWayUsername     string `json:"eway_username"`
	EWayPassword     string `json:"eway_password"`
}

// Summary is a stored row without secrets (GET returns it masked).
type Summary struct {
	GSTIN               string    `json:"gstin"`
	EInvoiceUsername    string    `json:"einvoice_username_masked"`
	HasEInvoicePassword bool      `json:"has_einvoice_password"`
	EWayUsername        string    `json:"eway_username_masked"`
	HasEWayPassword     bool      `json:"has_eway_password"`
	UpdatedBy           string    `json:"updated_by"`
	UpdatedAt           time.Time `json:"updated_at"`
}

// Put stores (or updates) a GSTIN's API users.
func (s *Store) Put(ctx context.Context, tenantID, gstin string, in Input, by string) (Summary, error) {
	g := gst.Normalize(gstin)
	if tenantID == "" || g == "" {
		return Summary{}, errors.New("tenant and gstin are required")
	}
	in.EInvoiceUsername, in.EWayUsername = strings.TrimSpace(in.EInvoiceUsername), strings.TrimSpace(in.EWayUsername)
	if in.EInvoiceUsername == "" && in.EWayUsername == "" {
		return Summary{}, errors.New("give an e-invoice and/or e-way API username")
	}
	if len(in.EInvoicePassword) > 200 || len(in.EWayPassword) > 200 || len(in.EInvoiceUsername) > 100 || len(in.EWayUsername) > 100 {
		return Summary{}, errors.New("username or password too long")
	}
	seal := func(v, col string) ([]byte, error) {
		if v == "" {
			return nil, nil
		}
		return s.Cipher.Seal(v, aad(tenantID, g, col))
	}
	einv, err := seal(in.EInvoicePassword, "einvoice_password")
	if err != nil {
		return Summary{}, err
	}
	ewb, err := seal(in.EWayPassword, "eway_password")
	if err != nil {
		return Summary{}, err
	}
	if _, err := s.DB.Exec(ctx, `INSERT INTO gsp_credentials
			(tenant_id, gstin, einvoice_username, einvoice_password_enc, eway_username, eway_password_enc, updated_by, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,now())
		ON CONFLICT (tenant_id, gstin) DO UPDATE SET
			einvoice_username = EXCLUDED.einvoice_username,
			einvoice_password_enc = COALESCE(EXCLUDED.einvoice_password_enc, gsp_credentials.einvoice_password_enc),
			eway_username = EXCLUDED.eway_username,
			eway_password_enc = COALESCE(EXCLUDED.eway_password_enc, gsp_credentials.eway_password_enc),
			updated_by = EXCLUDED.updated_by, updated_at = now()`,
		tenantID, g, in.EInvoiceUsername, einv, in.EWayUsername, ewb, by); err != nil {
		return Summary{}, err
	}
	sm, _, err := s.Summary(ctx, tenantID, g)
	return sm, err
}

// Delete removes a GSTIN's API users.
func (s *Store) Delete(ctx context.Context, tenantID, gstin string) (bool, error) {
	tag, err := s.DB.Exec(ctx, `DELETE FROM gsp_credentials WHERE tenant_id=$1 AND gstin=$2`, tenantID, gst.Normalize(gstin))
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

const summaryCols = `gstin, einvoice_username, einvoice_password_enc IS NOT NULL, eway_username, eway_password_enc IS NOT NULL, updated_by, updated_at`

func scanSummary(scan func(...any) error) (Summary, error) {
	var sm Summary
	var eu, wu string
	if err := scan(&sm.GSTIN, &eu, &sm.HasEInvoicePassword, &wu, &sm.HasEWayPassword, &sm.UpdatedBy, &sm.UpdatedAt); err != nil {
		return sm, err
	}
	sm.EInvoiceUsername, sm.EWayUsername = Mask(eu), Mask(wu)
	return sm, nil
}

// Summary returns one GSTIN's row without secrets.
func (s *Store) Summary(ctx context.Context, tenantID, gstin string) (Summary, bool, error) {
	sm, err := scanSummary(s.DB.QueryRow(ctx, `SELECT `+summaryCols+` FROM gsp_credentials WHERE tenant_id=$1 AND gstin=$2`,
		tenantID, gst.Normalize(gstin)).Scan)
	if db.IsNotFound(err) {
		return Summary{}, false, nil
	}
	return sm, err == nil, err
}

// List returns every GSTIN with stored users for a tenant.
func (s *Store) List(ctx context.Context, tenantID string) ([]Summary, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+summaryCols+` FROM gsp_credentials WHERE tenant_id=$1 ORDER BY gstin`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Summary{}
	for rows.Next() {
		sm, err := scanSummary(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, sm)
	}
	return out, rows.Err()
}

// Credentials returns the decrypted users for a GSTIN (ok=false when none are stored).
func (s *Store) Credentials(ctx context.Context, tenantID, gstin string) (gsp.Credentials, bool, error) {
	g := gst.Normalize(gstin)
	c := gsp.Credentials{GSTIN: g}
	var einv, ewb []byte
	err := s.DB.QueryRow(ctx, `SELECT einvoice_username, einvoice_password_enc, eway_username, eway_password_enc
		FROM gsp_credentials WHERE tenant_id=$1 AND gstin=$2`, tenantID, g).Scan(&c.EInvoiceUsername, &einv, &c.EWayUsername, &ewb)
	if db.IsNotFound(err) {
		return c, false, nil
	}
	if err != nil {
		return c, false, err
	}
	if len(einv) > 0 {
		if c.EInvoicePassword, err = s.Cipher.Open(einv, aad(tenantID, g, "einvoice_password")); err != nil {
			return c, false, fmt.Errorf("e-invoice password of %s: %w", g, err)
		}
	}
	if len(ewb) > 0 {
		if c.EWayPassword, err = s.Cipher.Open(ewb, aad(tenantID, g, "eway_password")); err != nil {
			return c, false, fmt.Errorf("e-way password of %s: %w", g, err)
		}
	}
	return c, true, nil
}
