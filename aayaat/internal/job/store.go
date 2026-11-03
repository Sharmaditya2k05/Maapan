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

const jobCols = `id, kind, status, source, file_name, file_key, COALESCE(content_type,''), file_size, COALESCE(sheet,''), header_row,
	mapping, params, template_id::text, total_rows, valid_rows, error_rows, skipped_rows, warning_rows, imported, failed, rolled_back,
	dr_total_paise, cr_total_paise, error, created_by, created_at, updated_at, started_at, finished_at, rolled_back_at, tenant_id`

func scanJob(row pgx.Row) (*Job, error) {
	var j Job
	var m, p []byte
	var dr, cr int64
	err := row.Scan(&j.ID, &j.Kind, &j.Status, &j.Source, &j.FileName, &j.fileKey, &j.contentType, &j.FileSize, &j.Sheet, &j.HeaderRow,
		&m, &p, &j.TemplateID, &j.TotalRows, &j.ValidRows, &j.ErrorRows, &j.SkippedRows, &j.WarningRows, &j.Imported, &j.Failed,
		&j.RolledBack, &dr, &cr, &j.Error, &j.CreatedBy, &j.CreatedAt, &j.UpdatedAt, &j.StartedAt, &j.FinishedAt, &j.RolledBackAt, &j.tenantID)
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
	if db.IsNotFound(err) || isBadUUID(err) {
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
	RowNo    int               `json:"row_no"`
	Status   string            `json:"status"`
	Raw      map[string]string `json:"raw"`
	Errors   []string          `json:"errors"`
	Warnings []string          `json:"warnings"`
	Record   json.RawMessage   `json:"record,omitempty"`
	BatchNo  *int              `json:"batch_no,omitempty"`
}

// Rows lists stored rows; status "" = all, "errors" = invalid + failed,
// "warnings" = rows with warnings.
func (s *Service) Rows(ctx context.Context, tenantID, id, status string, pg httpx.Page) ([]RowResult, int, error) {
	statuses := []string{status}
	warnOnly := false
	switch status {
	case "":
		statuses = []string{RowValid, RowInvalid, RowImported, RowFailed, RowSkipped, RowRolledBack}
	case "errors":
		statuses = []string{RowInvalid, RowFailed}
	case "warnings":
		statuses = []string{RowValid, RowImported, RowFailed, RowSkipped, RowRolledBack}
		warnOnly = true
	}
	where := `tenant_id=$1 AND job_id=$2 AND status = ANY($3) AND ($4 = false OR warnings <> '[]'::jsonb)`
	var total int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM import_rows WHERE `+where, tenantID, id, statuses, warnOnly).Scan(&total); err != nil {
		return nil, 0, err
	}
	limit := pg.Limit
	if limit <= 0 {
		limit = 1_000_000
	}
	rows, err := s.DB.Query(ctx, `SELECT row_no, status, raw, errors, warnings, record, batch_no FROM import_rows
		WHERE `+where+` ORDER BY row_no LIMIT $5 OFFSET $6`, tenantID, id, statuses, warnOnly, limit, pg.Offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []RowResult{}
	for rows.Next() {
		var r RowResult
		var raw, errs, warns []byte
		if err := rows.Scan(&r.RowNo, &r.Status, &raw, &errs, &warns, &r.Record, &r.BatchNo); err != nil {
			return nil, 0, err
		}
		_ = json.Unmarshal(raw, &r.Raw)
		_ = json.Unmarshal(errs, &r.Errors)
		_ = json.Unmarshal(warns, &r.Warnings)
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// WriteErrorsCSV writes rows with their errors, warnings and original values,
// so the user can fix the sheet in Excel and upload it again.
func WriteErrorsCSV(w io.Writer, rows []RowResult) error {
	fieldSet := map[string]bool{}
	withWarnings := false
	for _, r := range rows {
		for k := range r.Raw {
			fieldSet[k] = true
		}
		withWarnings = withWarnings || len(r.Warnings) > 0
	}
	fields := make([]string, 0, len(fieldSet))
	for k := range fieldSet {
		fields = append(fields, k)
	}
	sort.Strings(fields)
	cw := csv.NewWriter(w)
	head := []string{"row", "status", "errors"}
	if withWarnings {
		head = append(head, "warnings")
	}
	_ = cw.Write(append(head, fields...))
	for _, r := range rows {
		rec := []string{strconv.Itoa(r.RowNo), r.Status, strings.Join(r.Errors, "; ")}
		if withWarnings {
			rec = append(rec, strings.Join(r.Warnings, "; "))
		}
		for _, f := range fields {
			rec = append(rec, r.Raw[f])
		}
		_ = cw.Write(rec)
	}
	cw.Flush()
	return cw.Error()
}
