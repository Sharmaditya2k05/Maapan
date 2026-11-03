package job

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/maapan/aayaat/internal/mapping"
	"github.com/maapan/mool/auth"
	"github.com/maapan/mool/db"
	"github.com/maapan/mool/httpx"
)

// Template is a saved column mapping ("our MargBooks party master"), applied
// to later uploads with PUT /mapping {"template_id": …}.
type Template struct {
	ID        string            `json:"id"`
	Kind      string            `json:"kind"`
	Name      string            `json:"name"`
	Source    string            `json:"source,omitempty"`
	Sheet     string            `json:"sheet,omitempty"`
	HeaderRow int               `json:"header_row"`
	Mapping   map[string]string `json:"mapping"`
	Params    Params            `json:"params"`
	CreatedBy string            `json:"created_by"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
	Matches   bool              `json:"matches,omitempty"` // preview only: the file has every mapped header
}

// TemplateInput creates or replaces (by kind + name) a template.
type TemplateInput struct {
	Kind      string            `json:"kind"`
	Name      string            `json:"name"`
	Source    string            `json:"source,omitempty"`
	Sheet     string            `json:"sheet,omitempty"`
	HeaderRow int               `json:"header_row,omitempty"`
	Mapping   map[string]string `json:"mapping"`
	Params    Params            `json:"params,omitempty"`
}

// Validate checks a template.
func (in *TemplateInput) Validate() error {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 80 {
		return httpx.Validation("name is required (max 80 characters)")
	}
	if !mapping.ValidKind(in.Kind) {
		return httpx.Validation("kind must be one of %v", mapping.Kinds)
	}
	if in.HeaderRow < 0 {
		return httpx.Validation("header_row must be >= 0")
	}
	if err := checkMapping(in.Kind, in.Mapping); err != nil {
		return err
	}
	in.Params = in.Params.reusable()
	return in.Params.validate()
}

const tplCols = `id, kind, name, source, COALESCE(sheet,''), header_row, mapping, params, created_by, created_at, updated_at`

func scanTemplate(row pgx.Row) (*Template, error) {
	var t Template
	var m, p []byte
	if err := row.Scan(&t.ID, &t.Kind, &t.Name, &t.Source, &t.Sheet, &t.HeaderRow, &m, &p, &t.CreatedBy, &t.CreatedAt, &t.UpdatedAt); err != nil {
		return nil, err
	}
	_ = json.Unmarshal(m, &t.Mapping)
	_ = json.Unmarshal(p, &t.Params)
	return &t, nil
}

// SaveTemplate upserts a template by (kind, name).
func (s *Service) SaveTemplate(ctx context.Context, p *auth.Principal, in TemplateInput) (*Template, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	mj, _ := json.Marshal(in.Mapping)
	pj, _ := json.Marshal(in.Params)
	return scanTemplate(s.DB.QueryRow(ctx, `INSERT INTO mapping_templates (tenant_id, kind, name, source, sheet, header_row, mapping, params, created_by)
		VALUES ($1,$2,$3,$4,NULLIF($5,''),$6,$7,$8,$9)
		ON CONFLICT (tenant_id, kind, name) DO UPDATE SET source=EXCLUDED.source, sheet=EXCLUDED.sheet, header_row=EXCLUDED.header_row,
			mapping=EXCLUDED.mapping, params=EXCLUDED.params, updated_at=now()
		RETURNING `+tplCols, p.TenantID, in.Kind, in.Name, in.Source, in.Sheet, in.HeaderRow, mj, pj, p.UserID()))
}

// GetTemplate loads one template.
func (s *Service) GetTemplate(ctx context.Context, tenantID, id string) (*Template, error) {
	t, err := scanTemplate(s.DB.QueryRow(ctx, `SELECT `+tplCols+` FROM mapping_templates WHERE tenant_id=$1 AND id=$2`, tenantID, id))
	if db.IsNotFound(err) || isBadUUID(err) {
		return nil, httpx.NotFound("mapping template not found")
	}
	return t, err
}

// ListTemplates returns the tenant's templates (of one kind when kind != "").
func (s *Service) ListTemplates(ctx context.Context, tenantID, kind string) ([]Template, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+tplCols+` FROM mapping_templates WHERE tenant_id=$1 AND ($2='' OR kind=$2)
		ORDER BY kind, name`, tenantID, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Template{}
	for rows.Next() {
		t, err := scanTemplate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// DeleteTemplate removes a template; jobs that used it keep their mapping.
func (s *Service) DeleteTemplate(ctx context.Context, tenantID, id string) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM mapping_templates WHERE tenant_id=$1 AND id=$2`, tenantID, id)
	if isBadUUID(err) || (err == nil && tag.RowsAffected() == 0) {
		return httpx.NotFound("mapping template not found")
	}
	return err
}
