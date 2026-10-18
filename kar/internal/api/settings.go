package api

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/taraju/mool/db"
	"github.com/taraju/mool/events"
	"github.com/taraju/mool/gst"
	"github.com/taraju/mool/httpx"
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
