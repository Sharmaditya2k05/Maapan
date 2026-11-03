package ledger

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/maapan/mool/auth"
	"github.com/maapan/mool/httpx"
)

// AuditRow is one captured change (written by the bahi_audit trigger).
type AuditRow struct {
	ID       int64           `json:"id"`
	At       time.Time       `json:"at"`
	Actor    string          `json:"actor"`
	Action   string          `json:"action"`
	Entity   string          `json:"entity"`
	EntityID string          `json:"entity_id"`
	Before   json.RawMessage `json:"before,omitempty"`
	After    json.RawMessage `json:"after,omitempty"`
}

// handleAudit: GET /v1/audit?entity=&entity_id=&actor=&action=&from=&to=
// — the edit audit trail, newest first.
func (s *Service) handleAudit(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	from, to, err := s.dateRange(r)
	if err != nil {
		return err
	}
	page := httpx.ParsePage(r)
	args := []any{p.TenantID, from, to}
	cond := "tenant_id=$1 AND at >= $2::date AND at < $3::date + 1"
	for _, f := range []struct{ col, q string }{{"entity", "entity"}, {"entity_id", "entity_id"}, {"actor", "actor"}, {"action", "action"}} {
		if v := httpx.Query(r, f.q); v != "" {
			args = append(args, v)
			cond += fmt.Sprintf(" AND %s=$%d", f.col, len(args))
		}
	}
	args = append(args, page.Limit, page.Offset)
	rows, err := s.DB.Query(r.Context(), fmt.Sprintf(`SELECT id, at, actor, action, entity, COALESCE(entity_id,''), before, after
		FROM audit_log WHERE %s ORDER BY id DESC LIMIT $%d OFFSET $%d`, cond, len(args)-1, len(args)), args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []AuditRow{}
	for rows.Next() {
		var a AuditRow
		var before, after []byte
		if err := rows.Scan(&a.ID, &a.At, &a.Actor, &a.Action, &a.Entity, &a.EntityID, &before, &after); err != nil {
			return err
		}
		a.Before, a.After = before, after
		items = append(items, a)
	}
	return httpx.OK(w, map[string]any{"items": items, "limit": page.Limit, "offset": page.Offset})
}
