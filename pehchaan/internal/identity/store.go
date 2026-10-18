package identity

import (
	"context"
	"encoding/json"

	"github.com/taraju/mool/db"
)

const userCols = `id, phone, name, email, status, last_login_at`

func scanUser(row interface{ Scan(...any) error }) (User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Phone, &u.Name, &u.Email, &u.Status, &u.LastLoginAt)
	return u, err
}

func upsertUser(ctx context.Context, q db.Querier, phone string, name *string) (User, error) {
	return scanUser(q.QueryRow(ctx, `INSERT INTO users (phone, name) VALUES ($1, $2)
		ON CONFLICT (phone) DO UPDATE SET name = COALESCE(users.name, EXCLUDED.name), updated_at = now()
		RETURNING `+userCols, phone, name))
}

func getUser(ctx context.Context, q db.Querier, id string) (User, error) {
	return scanUser(q.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE id=$1`, id))
}

func getUserByPhone(ctx context.Context, q db.Querier, phone string) (User, error) {
	return scanUser(q.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE phone=$1`, phone))
}

func touchLogin(ctx context.Context, q db.Querier, userID string) error {
	_, err := q.Exec(ctx, `UPDATE users SET last_login_at=now() WHERE id=$1`, userID)
	return err
}

const membershipCols = `m.id, m.tenant_id, t.business_name, m.user_id, m.role, m.store_ids::text[], m.active`

func scanMembership(row interface{ Scan(...any) error }) (Membership, error) {
	var m Membership
	err := row.Scan(&m.ID, &m.TenantID, &m.BusinessName, &m.UserID, &m.Role, &m.StoreIDs, &m.Active)
	if m.StoreIDs == nil {
		m.StoreIDs = []string{}
	}
	return m, err
}

// activeMemberships lists the businesses a user can log into, oldest first.
func activeMemberships(ctx context.Context, q db.Querier, userID string) ([]Membership, error) {
	rows, err := q.Query(ctx, `SELECT `+membershipCols+` FROM memberships m JOIN tenants t ON t.id = m.tenant_id
		WHERE m.user_id=$1 AND m.active AND t.status='active' ORDER BY m.created_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Membership{}
	for rows.Next() {
		m, err := scanMembership(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func getMembership(ctx context.Context, q db.Querier, tenantID, userID string) (Membership, error) {
	return scanMembership(q.QueryRow(ctx, `SELECT `+membershipCols+` FROM memberships m JOIN tenants t ON t.id = m.tenant_id
		WHERE m.tenant_id=$1 AND m.user_id=$2`, tenantID, userID))
}

const storeCols = `id, tenant_id, name, code, state_code, gstin, address, phone, active`

func scanStore(row interface{ Scan(...any) error }) (Store, error) {
	var s Store
	err := row.Scan(&s.ID, &s.TenantID, &s.Name, &s.Code, &s.StateCode, &s.GSTIN, &s.Address, &s.Phone, &s.Active)
	return s, err
}

func getStore(ctx context.Context, q db.Querier, tenantID, id string) (Store, error) {
	return scanStore(q.QueryRow(ctx, `SELECT `+storeCols+` FROM stores WHERE tenant_id=$1 AND id=$2`, tenantID, id))
}

// getStoreAnyTenant is for internal calls that only know the store id.
func getStoreAnyTenant(ctx context.Context, q db.Querier, id string) (Store, error) {
	return scanStore(q.QueryRow(ctx, `SELECT `+storeCols+` FROM stores WHERE id=$1`, id))
}

func listStores(ctx context.Context, q db.Querier, tenantID string) ([]Store, error) {
	rows, err := q.Query(ctx, `SELECT `+storeCols+` FROM stores WHERE tenant_id=$1 ORDER BY created_at`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Store{}
	for rows.Next() {
		s, err := scanStore(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func firstActiveStore(ctx context.Context, q db.Querier, tenantID string) (string, error) {
	var id string
	err := q.QueryRow(ctx, `SELECT id FROM stores WHERE tenant_id=$1 AND active ORDER BY created_at LIMIT 1`, tenantID).Scan(&id)
	return id, err
}

func insertStore(ctx context.Context, q db.Querier, tenantID string, in StoreInput) (Store, error) {
	return scanStore(q.QueryRow(ctx, `INSERT INTO stores (tenant_id, name, code, state_code, gstin, address, phone)
		VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING `+storeCols,
		tenantID, in.Name, in.Code, in.StateCode, nilIfEmpty(in.GSTIN), nilIfEmpty(in.Address), nilIfEmpty(in.Phone)))
}

func updateStore(ctx context.Context, q db.Querier, tenantID, id string, in StoreInput) (Store, error) {
	active := true
	if in.Active != nil {
		active = *in.Active
	}
	return scanStore(q.QueryRow(ctx, `UPDATE stores SET name=$3, code=$4, state_code=$5, gstin=$6, address=$7, phone=$8,
		active=$9, updated_at=now() WHERE tenant_id=$1 AND id=$2 RETURNING `+storeCols,
		tenantID, id, in.Name, in.Code, in.StateCode, nilIfEmpty(in.GSTIN), nilIfEmpty(in.Address), nilIfEmpty(in.Phone), active))
}

const tenantCols = `id, business_name, gstin, pan, state_code, address, email, phone, status, created_at`

func scanTenant(row interface{ Scan(...any) error }) (Tenant, error) {
	var t Tenant
	err := row.Scan(&t.ID, &t.BusinessName, &t.GSTIN, &t.PAN, &t.StateCode, &t.Address, &t.Email, &t.Phone, &t.Status, &t.CreatedAt)
	return t, err
}

func getTenant(ctx context.Context, q db.Querier, id string) (Tenant, error) {
	return scanTenant(q.QueryRow(ctx, `SELECT `+tenantCols+` FROM tenants WHERE id=$1`, id))
}

// ownerPhone is the phone of the earliest active super_admin.
func ownerPhone(ctx context.Context, q db.Querier, tenantID string) (string, error) {
	var phone string
	err := q.QueryRow(ctx, `SELECT u.phone FROM memberships m JOIN users u ON u.id = m.user_id
		WHERE m.tenant_id=$1 AND m.role='super_admin' AND m.active AND u.status='active'
		ORDER BY m.created_at LIMIT 1`, tenantID).Scan(&phone)
	if db.IsNotFound(err) {
		return "", nil
	}
	return phone, err
}

func listMembers(ctx context.Context, q db.Querier, tenantID string) ([]Member, error) {
	rows, err := q.Query(ctx, `SELECT u.id, u.phone, u.name, u.email, u.status, u.last_login_at,
		m.role, m.store_ids::text[], m.active
		FROM memberships m JOIN users u ON u.id = m.user_id WHERE m.tenant_id=$1 ORDER BY m.created_at`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Member{}
	for rows.Next() {
		var m Member
		if err := rows.Scan(&m.ID, &m.Phone, &m.Name, &m.Email, &m.Status, &m.LastLoginAt, &m.Role, &m.StoreIDs, &m.Active); err != nil {
			return nil, err
		}
		if m.StoreIDs == nil {
			m.StoreIDs = []string{}
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// countStoresInTenant verifies every id belongs to the tenant.
func countStoresInTenant(ctx context.Context, q db.Querier, tenantID string, ids []string) (int, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT count(*) FROM stores WHERE tenant_id=$1 AND id = ANY($2::uuid[])`, tenantID, ids).Scan(&n)
	return n, err
}

func audit(ctx context.Context, q db.Querier, tenantID *string, actor, action, target, ip string, details map[string]any) {
	if details == nil {
		details = map[string]any{}
	}
	b, _ := json.Marshal(details)
	_, _ = q.Exec(ctx, `INSERT INTO audit_log (tenant_id, actor_id, action, target, details, ip) VALUES ($1,$2,$3,$4,$5,$6)`,
		tenantID, actor, action, nilIfEmpty(target), b, nilIfEmpty(ip))
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
