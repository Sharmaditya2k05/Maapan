package identity

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/maapan/mool/auth"
	"github.com/maapan/mool/httpx"
)

// AuditEntry is one audit_log row as served by GET /v1/audit.
type AuditEntry struct {
	ID        int64           `json:"id"`
	ActorID   string          `json:"actor_id"`
	ActorName *string         `json:"actor_name,omitempty"`
	Action    string          `json:"action"`
	Target    *string         `json:"target,omitempty"`
	Details   json.RawMessage `json:"details"`
	IP        *string         `json:"ip,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
}

// AuditFilter narrows GET /v1/audit.
type AuditFilter struct {
	Target string // a user id, store id, device id…
	Action string // exact ("user.role_change") or a prefix ending in "." ("user.")
	Actor  string
	Before int64 // cursor: entries with id < Before
	Limit  int
}

// AuditLog lists the tenant's audit entries, newest first.
func (s *Service) AuditLog(ctx context.Context, tenantID string, f AuditFilter) ([]AuditEntry, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	q := `SELECT a.id, a.actor_id, u.name, a.action, a.target, a.details, a.ip, a.created_at
		FROM audit_log a LEFT JOIN users u ON u.id::text = a.actor_id
		WHERE a.tenant_id=$1`
	args := []any{tenantID}
	add := func(cond string, v any) {
		args = append(args, v)
		q += " AND " + strings.ReplaceAll(cond, "$?", "$"+strconv.Itoa(len(args)))
	}
	if f.Target != "" {
		add("a.target=$?", f.Target)
	}
	if f.Action != "" {
		if strings.HasSuffix(f.Action, ".") {
			add("starts_with(a.action, $?)", f.Action)
		} else {
			add("a.action=$?", f.Action)
		}
	}
	if f.Actor != "" {
		add("a.actor_id=$?", f.Actor)
	}
	if f.Before > 0 {
		add("a.id<$?", f.Before)
	}
	args = append(args, f.Limit)
	q += " ORDER BY a.id DESC LIMIT $" + strconv.Itoa(len(args))
	rows, err := s.DB.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditEntry{}
	for rows.Next() {
		var e AuditEntry
		var details []byte
		if err := rows.Scan(&e.ID, &e.ActorID, &e.ActorName, &e.Action, &e.Target, &details, &e.IP, &e.CreatedAt); err != nil {
			return nil, err
		}
		e.Details = details
		out = append(out, e)
	}
	return out, rows.Err()
}

// listAudit serves GET /v1/audit?target=&action=&actor=&before=&limit=.
func (s *Service) listAudit(w http.ResponseWriter, r *http.Request) error {
	f := AuditFilter{Target: httpx.Query(r, "target"), Action: httpx.Query(r, "action"), Actor: httpx.Query(r, "actor")}
	if v := httpx.Query(r, "before"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			return httpx.Validation("before must be an audit entry id")
		}
		f.Before = n
	}
	if v := httpx.Query(r, "limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return httpx.Validation("limit must be a positive number")
		}
		f.Limit = min(n, 200)
	}
	items, err := s.AuditLog(r.Context(), auth.FromContext(r.Context()).TenantID, f)
	if err != nil {
		return err
	}
	res := map[string]any{"items": items}
	if len(items) > 0 {
		res["next_before"] = items[len(items)-1].ID
	}
	return httpx.OK(w, res)
}

// userAudit serves GET /v1/users/{id}/audit: invites, role, store and status
// changes of one member, newest first.
func (s *Service) userAudit(w http.ResponseWriter, r *http.Request) error {
	items, err := s.AuditLog(r.Context(), auth.FromContext(r.Context()).TenantID,
		AuditFilter{Target: r.PathValue("id"), Action: "user.", Limit: 200})
	if err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"items": items})
}

// membershipState is what an audit entry records before/after a change.
type membershipState struct {
	Role     string   `json:"role"`
	StoreIDs []string `json:"store_ids"`
	Active   bool     `json:"active"`
}

// membershipChanges lists the fields that differ ("role", "store_ids", "active").
func membershipChanges(before, after membershipState) []string {
	out := []string{}
	if before.Role != after.Role {
		out = append(out, "role")
	}
	if !sameSet(before.StoreIDs, after.StoreIDs) {
		out = append(out, "store_ids")
	}
	if before.Active != after.Active {
		out = append(out, "active")
	}
	return out
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]int, len(a))
	for _, x := range a {
		seen[strings.ToLower(x)]++
	}
	for _, x := range b {
		k := strings.ToLower(x)
		if seen[k] == 0 {
			return false
		}
		seen[k]--
	}
	return true
}
