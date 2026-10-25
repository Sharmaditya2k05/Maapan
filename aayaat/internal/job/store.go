package job

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/maapan/mool/db"
	"github.com/maapan/mool/httpx"
)

const jobCols = `id, kind, status, file_name, file_key, COALESCE(content_type,''), file_size, COALESCE(sheet,''), header_row,
	mapping, params, total_rows, valid_rows, error_rows, imported, failed, dr_total_paise, cr_total_paise, error,
	created_by, created_at, updated_at, started_at, finished_at, tenant_id`

func scanJob(row pgx.Row) (*Job, error) {
	var j Job
	var m, p []byte
	var dr, cr int64
	err := row.Scan(&j.ID, &j.Kind, &j.Status, &j.FileName, &j.fileKey, &j.contentType, &j.FileSize, &j.Sheet, &j.HeaderRow,
		&m, &p, &j.TotalRows, &j.ValidRows, &j.ErrorRows, &j.Imported, &j.Failed, &dr, &cr, &j.Error,
		&j.CreatedBy, &j.CreatedAt, &j.UpdatedAt, &j.StartedAt, &j.FinishedAt, &j.tenantID)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(m, &j.Mapping)
	_ = json.Unmarshal(p, &j.Params)
	if j.Mapping == nil {
		j.Mapping = map[string]string{}
	}
	if j.Status != StatusUploaded && j.Status != StatusMapped {
		j.Recon = reconciliation(j.Kind, dr, cr)
	}
	return &j, nil
}

// Get loads one job.
func (s *Service) Get(ctx context.Context, tenantID, id string) (*Job, error) {
	j, err := scanJob(s.DB.QueryRow(ctx, `SELECT `+jobCols+` FROM import_jobs WHERE tenant_id=$1 AND id=$2`, tenantID, id))
	if db.IsNotFound(err) {
		return nil, httpx.NotFound("import job not found")
	}
	return j, err
}

// List returns the tenant's jobs, newest first.
func (s *Service) List(ctx context.Context, tenantID, kind string, pg httpx.Page) ([]Job, int, error) {
	var total int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM import_jobs WHERE tenant_id=$1 AND ($2='' OR kind=$2)`,
		tenantID, kind).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.DB.Query(ctx, `SELECT `+jobCols+` FROM import_jobs WHERE tenant_id=$1 AND ($2='' OR kind=$2)
		ORDER BY created_at DESC LIMIT $3 OFFSET $4`, tenantID, kind, pg.Limit, pg.Offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Job{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *j)
	}
	return out, total, rows.Err()
}

// RowResult is one stored row.
type RowResult struct {
	RowNo  int               `json:"row_no"`
	Status string            `json:"status"`
	Raw    map[string]string `json:"raw"`
	Errors []string          `json:"errors"`
	Record json.RawMessage   `json:"record,omitempty"`
}

// Rows lists stored rows; status "" = all, "errors" = invalid + failed.
func (s *Service) Rows(ctx context.Context, tenantID, id, status string, pg httpx.Page) ([]RowResult, int, error) {
	statuses := []string{status}
	switch status {
	case "":
		statuses = []string{"valid", "invalid", "imported", "failed"}
	case "errors":
		statuses = []string{"invalid", "failed"}
	}
	var total int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM import_rows WHERE tenant_id=$1 AND job_id=$2 AND status = ANY($3)`,
		tenantID, id, statuses).Scan(&total); err != nil {
		return nil, 0, err
	}
	limit := pg.Limit
	if limit <= 0 {
		limit = 1_000_000
	}
	rows, err := s.DB.Query(ctx, `SELECT row_no, status, raw, errors, record FROM import_rows
		WHERE tenant_id=$1 AND job_id=$2 AND status = ANY($3) ORDER BY row_no LIMIT $4 OFFSET $5`,
		tenantID, id, statuses, limit, pg.Offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []RowResult{}
	for rows.Next() {
		var r RowResult
		var raw, errs []byte
		if err := rows.Scan(&r.RowNo, &r.Status, &raw, &errs, &r.Record); err != nil {
			return nil, 0, err
		}
		_ = json.Unmarshal(raw, &r.Raw)
		_ = json.Unmarshal(errs, &r.Errors)
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// WriteErrorsCSV writes every invalid/failed row with its errors and original values,
// so the user can fix the sheet in Excel and upload it again.
func WriteErrorsCSV(w io.Writer, rows []RowResult) error {
	fieldSet := map[string]bool{}
	for _, r := range rows {
		for k := range r.Raw {
			fieldSet[k] = true
		}
	}
	fields := make([]string, 0, len(fieldSet))
	for k := range fieldSet {
		fields = append(fields, k)
	}
	sort.Strings(fields)
	cw := csv.NewWriter(w)
	_ = cw.Write(append([]string{"row", "status", "errors"}, fields...))
	for _, r := range rows {
		rec := []string{strconv.Itoa(r.RowNo), r.Status, strings.Join(r.Errors, "; ")}
		for _, f := range fields {
			rec = append(rec, r.Raw[f])
		}
		_ = cw.Write(rec)
	}
	cw.Flush()
	return cw.Error()
}
