package identity

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/maapan/mool/auth"
	"github.com/maapan/mool/db"
	"github.com/maapan/mool/httpx"
)

// Device is a registered POS terminal: device_id is bound to one store until revoked.
type Device struct {
	ID           string     `json:"id"`
	TenantID     string     `json:"tenant_id"`
	StoreID      string     `json:"store_id"`
	DeviceID     string     `json:"device_id"`
	Name         *string    `json:"name,omitempty"`
	Platform     *string    `json:"platform,omitempty"`
	Status       string     `json:"status"` // active | revoked
	RegisteredBy *string    `json:"registered_by,omitempty"`
	RevokedBy    *string    `json:"revoked_by,omitempty"`
	RevokedAt    *time.Time `json:"revoked_at,omitempty"`
	LastSeenAt   *time.Time `json:"last_seen_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
}

// DeviceInput registers a terminal.
type DeviceInput struct {
	DeviceID string `json:"device_id"`
	StoreID  string `json:"store_id"`
	Name     string `json:"name,omitempty"`
	Platform string `json:"platform,omitempty"`
}

var devicePlatforms = map[string]bool{"android": true, "ios": true, "windows": true, "linux": true, "macos": true, "web": true, "other": true}

// Validate checks a registration.
func (in *DeviceInput) Validate() error {
	in.DeviceID = strings.TrimSpace(in.DeviceID)
	in.StoreID = strings.TrimSpace(in.StoreID)
	in.Name = strings.TrimSpace(in.Name)
	in.Platform = strings.ToLower(strings.TrimSpace(in.Platform))
	if err := validDeviceID(in.DeviceID); err != nil {
		return err
	}
	if in.StoreID == "" {
		return httpx.Validation("store_id is required")
	}
	if len(in.Name) > 80 {
		return httpx.Validation("name must be at most 80 characters")
	}
	if in.Platform != "" && !devicePlatforms[in.Platform] {
		return httpx.Validation("platform must be android, ios, windows, linux, macos, web or other")
	}
	return nil
}

func validDeviceID(id string) error {
	if id == "" || len(id) > 128 {
		return httpx.Validation("device_id must be 1-128 characters")
	}
	for _, r := range id {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_.:", r)
		if !ok {
			return httpx.Validation("device_id may contain letters, digits and - _ . : only")
		}
	}
	return nil
}

// canManageDevices: settings:manage for any store, a store manager for their own stores.
func canManageDevices(p *auth.Principal, storeID string) bool {
	if p.Can(auth.PermSettingsManage) {
		return true
	}
	return p.Claims != nil && p.Claims.Role == auth.RoleStoreManager && p.Claims.CanAccessStore(storeID)
}

const deviceCols = `id, tenant_id, store_id, device_id, name, platform, status, registered_by::text, revoked_by::text,
	revoked_at, last_seen_at, created_at`

func scanDevice(row interface{ Scan(...any) error }) (Device, error) {
	var d Device
	err := row.Scan(&d.ID, &d.TenantID, &d.StoreID, &d.DeviceID, &d.Name, &d.Platform, &d.Status, &d.RegisteredBy,
		&d.RevokedBy, &d.RevokedAt, &d.LastSeenAt, &d.CreatedAt)
	return d, err
}

// RegisterDevice binds a terminal to a store. An active registration of the
// same device_id in this business must be revoked first (409).
func (s *Service) RegisterDevice(ctx context.Context, p *auth.Principal, in DeviceInput, ip string) (*Device, error) {
	if !canManageDevices(p, in.StoreID) {
		return nil, httpx.Forbidden("you cannot register devices for this store")
	}
	st, err := getStore(ctx, s.DB, p.TenantID, in.StoreID)
	if err != nil {
		if db.IsNotFound(err) || isInvalidUUID(err) {
			return nil, httpx.Validation("store_id is not a store of this business")
		}
		return nil, err
	}
	if !st.Active {
		return nil, httpx.Conflict("store is inactive")
	}
	var d Device
	err = db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		var err error
		d, err = scanDevice(tx.QueryRow(ctx, `INSERT INTO devices (tenant_id, store_id, device_id, name, platform, registered_by)
			VALUES ($1,$2,$3,$4,$5,$6) RETURNING `+deviceCols,
			p.TenantID, in.StoreID, in.DeviceID, nilIfEmpty(in.Name), nilIfEmpty(in.Platform), nilIfEmpty(principalUserUUID(p))))
		if isUniqueViolation(err) {
			return httpx.Conflict("this device is already registered; revoke it first to move it")
		}
		if err != nil {
			return err
		}
		audit(ctx, tx, &p.TenantID, p.UserID(), "device.register", d.ID, ip,
			map[string]any{"device_id": d.DeviceID, "store_id": d.StoreID})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// Devices lists the business's terminals the caller may see.
func (s *Service) Devices(ctx context.Context, p *auth.Principal, storeID, status string) ([]Device, error) {
	if status != "" && status != "active" && status != "revoked" {
		return nil, httpx.Validation("status must be active or revoked")
	}
	q := `SELECT ` + deviceCols + ` FROM devices WHERE tenant_id=$1`
	args := []any{p.TenantID}
	if storeID != "" {
		args = append(args, storeID)
		q += ` AND store_id::text=$2`
	}
	if status != "" {
		args = append(args, status)
		q += ` AND status=$` + strconv.Itoa(len(args))
	}
	rows, err := s.DB.Query(ctx, q+` ORDER BY created_at DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Device{}
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		if canManageDevices(p, d.StoreID) {
			out = append(out, d)
		}
	}
	return out, rows.Err()
}

func (s *Service) device(ctx context.Context, q db.Querier, tenantID, id string) (Device, error) {
	d, err := scanDevice(q.QueryRow(ctx, `SELECT `+deviceCols+` FROM devices WHERE tenant_id=$1 AND id::text=$2`, tenantID, id))
	if db.IsNotFound(err) {
		return d, httpx.NotFound("device not found")
	}
	return d, err
}

// RevokeDevice stops a terminal from being accepted. The row is kept.
func (s *Service) RevokeDevice(ctx context.Context, p *auth.Principal, id, ip string) (*Device, error) {
	var d Device
	err := db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		var err error
		if d, err = s.device(ctx, tx, p.TenantID, id); err != nil {
			return err
		}
		if !canManageDevices(p, d.StoreID) {
			return httpx.NotFound("device not found")
		}
		if d.Status == "revoked" {
			return nil
		}
		if d, err = scanDevice(tx.QueryRow(ctx, `UPDATE devices SET status='revoked', revoked_by=$3, revoked_at=now(), updated_at=now()
			WHERE tenant_id=$1 AND id=$2 RETURNING `+deviceCols, p.TenantID, d.ID, nilIfEmpty(principalUserUUID(p)))); err != nil {
			return err
		}
		audit(ctx, tx, &p.TenantID, p.UserID(), "device.revoke", d.ID, ip,
			map[string]any{"device_id": d.DeviceID, "store_id": d.StoreID})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// InternalDevice resolves an active device_id for a tenant (aadesh checks POS
// terminals with it) and records that it was seen.
func (s *Service) InternalDevice(ctx context.Context, tenantID, deviceID string) (*Device, error) {
	if err := validDeviceID(deviceID); err != nil {
		return nil, httpx.NotFound("device not found")
	}
	d, err := scanDevice(s.DB.QueryRow(ctx, `UPDATE devices SET last_seen_at=now()
		WHERE tenant_id::text=$1 AND device_id=$2 AND status='active' RETURNING `+deviceCols, tenantID, deviceID))
	if db.IsNotFound(err) {
		return nil, httpx.NotFound("device not found or revoked")
	}
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// --- handlers ---

func (s *Service) listDevices(w http.ResponseWriter, r *http.Request) error {
	ds, err := s.Devices(r.Context(), auth.FromContext(r.Context()), httpx.Query(r, "store_id"), httpx.Query(r, "status"))
	if err != nil {
		return err
	}
	return httpx.OK(w, httpx.List[Device]{Items: ds, Total: len(ds), Limit: len(ds)})
}

func (s *Service) getDevice(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	d, err := s.device(r.Context(), s.DB, p.TenantID, r.PathValue("id"))
	if err != nil {
		return err
	}
	if !canManageDevices(p, d.StoreID) {
		return httpx.NotFound("device not found")
	}
	return httpx.OK(w, d)
}

func (s *Service) registerDevice(w http.ResponseWriter, r *http.Request) error {
	var in DeviceInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	d, err := s.RegisterDevice(r.Context(), auth.FromContext(r.Context()), in, httpx.ClientIP(r))
	if err != nil {
		return err
	}
	return httpx.Created(w, d)
}

func (s *Service) revokeDevice(w http.ResponseWriter, r *http.Request) error {
	d, err := s.RevokeDevice(r.Context(), auth.FromContext(r.Context()), r.PathValue("id"), httpx.ClientIP(r))
	if err != nil {
		return err
	}
	return httpx.OK(w, d)
}

func (s *Service) internalDevice(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	if p.TenantID == "" {
		return httpx.Validation("X-Tenant-ID is required")
	}
	d, err := s.InternalDevice(r.Context(), p.TenantID, r.PathValue("device_id"))
	if err != nil {
		return err
	}
	return httpx.OK(w, d)
}

// --- helpers ---

// principalUserUUID is the acting user's id ("" for internal callers).
func principalUserUUID(p *auth.Principal) string {
	if p.Claims == nil {
		return ""
	}
	return p.Claims.Subject
}

func isUniqueViolation(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505"
}

func isInvalidUUID(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "22P02"
}
