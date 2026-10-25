package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/maapan/kar/internal/creds"
	"github.com/maapan/mool/auth"
	"github.com/maapan/mool/db"
	"github.com/maapan/mool/events"
	"github.com/maapan/mool/gst"
	"github.com/maapan/mool/httpx"
)

// Settings are a tenant's tax settings.
type Settings struct {
	TenantID           string    `json:"tenant_id"`
	StateCode          string    `json:"state_code"`
	GSTIN              string    `json:"gstin"`
	Composition        bool      `json:"composition"`
	EInvoiceEnabled    bool      `json:"einvoice_enabled"`
	EWayThresholdPaise int64     `json:"eway_threshold_paise"`
	B2CLThresholdPaise int64     `json:"b2cl_threshold_paise"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// SettingsUpdate is the body of PUT /v1/settings (only sent fields change).
type SettingsUpdate struct {
	StateCode          *string `json:"state_code,omitempty"`
	GSTIN              *string `json:"gstin,omitempty"`
	Composition        *bool   `json:"composition,omitempty"`
	EInvoiceEnabled    *bool   `json:"einvoice_enabled,omitempty"`
	EWayThresholdPaise *int64  `json:"eway_threshold_paise,omitempty"`
	B2CLThresholdPaise *int64  `json:"b2cl_threshold_paise,omitempty"`
}

// Validate checks the update.
func (u *SettingsUpdate) Validate() error {
	if u.StateCode != nil && !gst.ValidState(*u.StateCode) {
		return httpx.Validation("unknown state_code")
	}
	if u.GSTIN != nil && *u.GSTIN != "" {
		if err := gst.ValidateGSTIN(*u.GSTIN); err != nil {
			return httpx.Validation("gstin: %s", err.Error())
		}
	}
	if (u.EWayThresholdPaise != nil && *u.EWayThresholdPaise < 0) || (u.B2CLThresholdPaise != nil && *u.B2CLThresholdPaise < 0) {
		return httpx.Validation("thresholds cannot be negative")
	}
	return nil
}

const settingsCols = `tenant_id, state_code, COALESCE(gstin,''), composition, einvoice_enabled, eway_threshold_paise, b2cl_threshold_paise, updated_at`

func scanSettings(row pgx.Row) (*Settings, error) {
	var s Settings
	err := row.Scan(&s.TenantID, &s.StateCode, &s.GSTIN, &s.Composition, &s.EInvoiceEnabled,
		&s.EWayThresholdPaise, &s.B2CLThresholdPaise, &s.UpdatedAt)
	return &s, err
}

// settings loads a tenant's settings, creating them from pehchaan's tenant the
// first time (covers tenants created before kar subscribed to the event).
func (s *Service) settings(ctx context.Context, tenantID string) (*Settings, error) {
	st, err := scanSettings(s.DB.QueryRow(ctx, `SELECT `+settingsCols+` FROM tax_settings WHERE tenant_id=$1`, tenantID))
	if err == nil {
		return st, nil
	}
	if !db.IsNotFound(err) {
		return nil, err
	}
	t, err := s.Dir.Tenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	return scanSettings(s.DB.QueryRow(ctx, `INSERT INTO tax_settings (tenant_id, state_code, gstin) VALUES ($1,$2,NULLIF($3,''))
		ON CONFLICT (tenant_id) DO UPDATE SET tenant_id=EXCLUDED.tenant_id RETURNING `+settingsCols,
		tenantID, t.StateCode, gst.Normalize(t.GSTIN)))
}

func (s *Service) updateSettings(ctx context.Context, tenantID string, u SettingsUpdate) (*Settings, error) {
	if _, err := s.settings(ctx, tenantID); err != nil {
		return nil, err
	}
	var gstin *string
	if u.GSTIN != nil {
		g := gst.Normalize(*u.GSTIN)
		gstin = &g
	}
	return scanSettings(s.DB.QueryRow(ctx, `UPDATE tax_settings SET
			state_code = COALESCE($2, state_code),
			gstin = CASE WHEN $3::text IS NULL THEN gstin ELSE NULLIF($3,'') END,
			composition = COALESCE($4, composition),
			einvoice_enabled = COALESCE($5, einvoice_enabled),
			eway_threshold_paise = COALESCE($6, eway_threshold_paise),
			b2cl_threshold_paise = COALESCE($7, b2cl_threshold_paise),
			updated_at = now()
		WHERE tenant_id=$1 RETURNING `+settingsCols,
		tenantID, u.StateCode, gstin, u.Composition, u.EInvoiceEnabled, u.EWayThresholdPaise, u.B2CLThresholdPaise))
}

// onTenantCreated seeds tax settings for a new business.
func (s *Service) onTenantCreated(ctx context.Context, e events.Envelope) error {
	var p events.TenantCreatedPayload
	if err := e.Decode(&p); err != nil || p.TenantID == "" {
		return nil
	}
	return db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		fresh, err := db.MarkProcessed(ctx, tx, e.ID, e.Type)
		if err != nil || !fresh {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO tax_settings (tenant_id, state_code, gstin) VALUES ($1,$2,NULLIF($3,''))
			ON CONFLICT (tenant_id) DO NOTHING`, p.TenantID, p.StateCode, gst.Normalize(p.GSTIN))
		return err
	})
}

// ---------- per-GSTIN portal API users ----------

// ownsGSTIN checks that g is one of the tenant's registrations (pehchaan), or
// the tenant's tax-settings GSTIN when pehchaan has no registrations API yet.
func (s *Service) ownsGSTIN(ctx context.Context, tenantID, g string) (bool, error) {
	regs, err := s.Dir.Registrations(ctx, tenantID)
	if err == nil {
		for _, r := range regs {
			if gst.Normalize(r.GSTIN) == g {
				return true, nil
			}
		}
		if len(regs) > 0 {
			return false, nil
		}
	} else {
		s.Log.Warn("registrations lookup failed; checking tax settings GSTIN", "error", err.Error())
	}
	st, err := s.settings(ctx, tenantID)
	if err != nil {
		return false, err
	}
	return st.GSTIN == g, nil
}

func (s *Service) gstinParam(r *http.Request) (string, error) {
	g := gst.Normalize(r.PathValue("gstin"))
	if err := gst.ValidateGSTIN(g); err != nil {
		return "", httpx.Validation("gstin: %s", err.Error())
	}
	return g, nil
}

func (s *Service) listGSPCredentials(w http.ResponseWriter, r *http.Request) error {
	items, err := s.Creds.List(r.Context(), auth.FromContext(r.Context()).TenantID)
	if err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"items": items})
}

func (s *Service) getGSPCredentials(w http.ResponseWriter, r *http.Request) error {
	g, err := s.gstinParam(r)
	if err != nil {
		return err
	}
	sm, ok, err := s.Creds.Summary(r.Context(), auth.FromContext(r.Context()).TenantID, g)
	if err != nil {
		return err
	}
	if !ok {
		return httpx.NotFound("no GST portal API users saved for %s", g)
	}
	return httpx.OK(w, sm)
}

func (s *Service) putGSPCredentials(w http.ResponseWriter, r *http.Request) error {
	g, err := s.gstinParam(r)
	if err != nil {
		return err
	}
	var in creds.Input
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	p := auth.FromContext(r.Context())
	owns, err := s.ownsGSTIN(r.Context(), p.TenantID, g)
	if err != nil {
		return err
	}
	if !owns {
		return httpx.Validation("%s is not one of this business's registrations", g)
	}
	sm, err := s.Creds.Put(r.Context(), p.TenantID, g, in, p.UserID())
	if err != nil {
		if httpx.AsError(err).Status == 500 && !strings.Contains(err.Error(), "SQLSTATE") {
			return httpx.Validation("%s", err.Error())
		}
		return err
	}
	s.Log.Info("gsp credentials saved", "gstin", g, "by", p.UserID()) // never log the values
	return httpx.OK(w, sm)
}

func (s *Service) deleteGSPCredentials(w http.ResponseWriter, r *http.Request) error {
	g, err := s.gstinParam(r)
	if err != nil {
		return err
	}
	ok, err := s.Creds.Delete(r.Context(), auth.FromContext(r.Context()).TenantID, g)
	if err != nil {
		return err
	}
	if !ok {
		return httpx.NotFound("no GST portal API users saved for %s", g)
	}
	return httpx.NoContent(w)
}
