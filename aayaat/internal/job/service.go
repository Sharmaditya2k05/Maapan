package job

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/maapan/aayaat/internal/clients"
	"github.com/maapan/aayaat/internal/mapping"
	"github.com/maapan/aayaat/internal/sheet"
	"github.com/maapan/aayaat/internal/storage"
	"github.com/maapan/aayaat/internal/validate"
	"github.com/maapan/mool/auth"
	"github.com/maapan/mool/db"
	"github.com/maapan/mool/events"
	"github.com/maapan/mool/httpx"
)

// Limits.
const (
	MaxFileBytes = 50 << 20
	MaxRows      = 200_000
	BatchSize    = 500
	copyChunk    = 1_000 // rows per COPY while validating
	previewRows  = 20
)

// Worker triggers a commit in a separate process (the aayaat-worker Cloud Run Job).
type Worker interface {
	Trigger(ctx context.Context, jobID, tenantID string) error
}

// Service implements the import workflow.
type Service struct {
	DB        *pgxpool.Pool
	Store     storage.Storage
	Targets   clients.Targets
	Relay     *events.Relay
	Worker    Worker // nil → always commit in-process
	LargeRows int    // valid rows above which the Worker is used
	Log       *slog.Logger
	Now       func() time.Time
	BatchSize int  // rows per bulk call; 0 = BatchSize (500)
	sync      bool // tests: Commit runs RunCommit inline instead of in a goroutine
}

var errStop = errors.New("stop")

// Create stores the upload and registers a job.
func (s *Service) Create(ctx context.Context, p *auth.Principal, kind, fileName, contentType string, r io.Reader, params Params) (*Job, error) {
	if !mapping.ValidKind(kind) {
		return nil, httpx.Validation("kind must be one of %v", mapping.Kinds)
	}
	ext := strings.ToLower(filepath.Ext(fileName))
	if !slices.Contains(sheet.Extensions, ext) {
		return nil, httpx.Validation("upload .xlsx, .xls, .csv or a Tally .xml export (MargBooks: Reports → Export → Excel; Tally: Export → XML)")
	}
	if ext == ".xml" && params.Source == "" {
		params.Source = "tally"
	}
	if err := params.validate(); err != nil {
		return nil, err
	}
	if err := s.checkStores(p, params); err != nil {
		return nil, err
	}
	id := uuid.NewString()
	now := s.Now().UTC()
	key := fmt.Sprintf("imports/%s/%s/%s/%s", p.TenantID, now.Format("2006/01"), id, sanitize(fileName))
	size, err := s.Store.Put(ctx, key, io.LimitReader(r, MaxFileBytes+1), contentType)
	if err != nil {
		s.removeUpload(key)
		return nil, fmt.Errorf("store upload: %w", err)
	}
	if size > MaxFileBytes {
		s.removeUpload(key) // never leave an orphaned object behind
		return nil, httpx.Validation("file is larger than 50 MB; split it by sheet or year")
	}
	pj, _ := json.Marshal(params)
	if _, err := s.DB.Exec(ctx, `INSERT INTO import_jobs (id, tenant_id, kind, source, file_name, file_key, file_size, content_type, params, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, id, p.TenantID, kind, params.Source, fileName, key, size, contentType, pj, p.UserID()); err != nil {
		s.removeUpload(key)
		return nil, err
	}
	return s.Get(ctx, p.TenantID, id)
}

func (s *Service) batchSize() int {
	if s.BatchSize > 0 {
		return s.BatchSize
	}
	return BatchSize
}

func (s *Service) removeUpload(key string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s.Store.Delete(ctx, key); err != nil {
		s.Log.Warn("remove rejected upload", "key", key, "error", err.Error())
	}
}

func (s *Service) checkStores(p *auth.Principal, params Params) error {
	if p == nil || p.Claims == nil {
		return nil
	}
	for _, id := range params.storeIDs() {
		if !p.Claims.CanAccessStore(id) {
			return httpx.Forbidden("no access to store %s", id)
		}
	}
	return nil
}

// Preview describes the file and suggests a mapping.
type Preview struct {
	Sheets           []string          `json:"sheets"`
	Sheet            string            `json:"sheet"`
	Source           string            `json:"source,omitempty"`
	HeaderRow        int               `json:"header_row"`
	Headers          []string          `json:"headers"`
	Rows             [][]string        `json:"rows"`
	SuggestedMapping map[string]string `json:"suggested_mapping"`
	Mapping          map[string]string `json:"mapping"`
	Fields           []mapping.Field   `json:"fields"`
	Templates        []Template        `json:"templates"` // saved mappings of this kind; matches=true when every mapped header is present
}

func (s *Service) open(ctx context.Context, j *Job) (sheet.Workbook, error) {
	rc, err := s.Store.Open(ctx, j.fileKey)
	if err != nil {
		return nil, fmt.Errorf("read upload: %w", err)
	}
	defer rc.Close()
	wb, err := sheet.Open(rc, j.FileName)
	if err != nil {
		if errors.Is(err, sheet.ErrUnsupported) {
			return nil, httpx.Validation("%s", err.Error())
		}
		return nil, httpx.Validation("could not read the file: %s", err.Error())
	}
	return wb, nil
}

// head reads the header row (detected or given) and up to n rows after it.
func head(wb sheet.Workbook, kind, sheetName string, headerRow, n int) (int, []string, [][]string, error) {
	var top [][]string
	scan := 15
	if headerRow > 0 {
		scan = headerRow
	}
	err := wb.Each(sheetName, func(rowNo int, cells []string) error {
		top = append(top, cells)
		if rowNo >= scan+n {
			return errStop
		}
		return nil
	})
	if err != nil && !errors.Is(err, errStop) {
		return 0, nil, nil, err
	}
	if headerRow == 0 {
		headerRow = mapping.DetectHeaderRow(kind, top[:min(len(top), 15)])
	}
	if headerRow > len(top) {
		return headerRow, nil, nil, httpx.Validation("sheet %q has fewer than %d rows", sheetName, headerRow)
	}
	headers := top[headerRow-1]
	var rows [][]string
	for _, r := range top[headerRow:] {
		if !sheet.IsBlank(r) && len(rows) < n {
			rows = append(rows, r)
		}
	}
	return headerRow, headers, rows, nil
}

// pickSheet returns want, or the sheet that feeds kind (Tally tables), or the first.
func pickSheet(wb sheet.Workbook, want, kind string) (string, error) {
	sheets := wb.Sheets()
	if len(sheets) == 0 {
		return "", httpx.Validation("the file has no sheets")
	}
	if want == "" {
		if ks, ok := wb.(sheet.KindSheets); ok {
			if d := ks.DefaultSheet(kind); slices.Contains(sheets, d) {
				return d, nil
			}
		}
		return sheets[0], nil
	}
	if slices.Contains(sheets, want) {
		return want, nil
	}
	return "", httpx.Validation("sheet %q not found; sheets: %v", want, sheets)
}

// Preview opens the file and returns headers, sample rows and a mapping suggestion.
func (s *Service) Preview(ctx context.Context, tenantID, id, sheetName string) (*Preview, error) {
	j, err := s.Get(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	wb, err := s.open(ctx, j)
	if err != nil {
		return nil, err
	}
	defer wb.Close()
	if sheetName == "" {
		sheetName = j.Sheet
	}
	sh, err := pickSheet(wb, sheetName, j.Kind)
	if err != nil {
		return nil, err
	}
	hr := 0
	if sh == j.Sheet {
		hr = j.HeaderRow
	}
	hr, headers, rows, err := head(wb, j.Kind, sh, hr, previewRows)
	if err != nil {
		return nil, err
	}
	tpls, err := s.ListTemplates(ctx, tenantID, j.Kind)
	if err != nil {
		return nil, err
	}
	for i := range tpls {
		_, unknown := mapping.Resolve(tpls[i].Mapping, headers)
		tpls[i].Matches = len(unknown) == 0 && len(tpls[i].Mapping) > 0
	}
	src := j.Source
	if so, ok := wb.(sheet.Source); ok && src == "" {
		src = so.Source()
	}
	return &Preview{Sheets: wb.Sheets(), Sheet: sh, Source: src, HeaderRow: hr, Headers: headers, Rows: rows,
		SuggestedMapping: mapping.Suggest(j.Kind, headers), Mapping: j.Mapping, Fields: mapping.Fields[j.Kind], Templates: tpls}, nil
}

// SetMapping saves the column mapping and options; previous validation is
// discarded, except rows already imported (the audit of what reached
// bahi/bhandar is never deleted).
func (s *Service) SetMapping(ctx context.Context, p *auth.Principal, id string, in MappingInput) (*Job, error) {
	j, err := s.Get(ctx, p.TenantID, id)
	if err != nil {
		return nil, err
	}
	var tplID *string
	if in.TemplateID != "" {
		t, err := s.GetTemplate(ctx, p.TenantID, in.TemplateID)
		if err != nil {
			return nil, err
		}
		if t.Kind != j.Kind {
			return nil, httpx.Validation("template %q is for %s, not %s", t.Name, t.Kind, j.Kind)
		}
		if len(in.Mapping) == 0 {
			in.Mapping = t.Mapping
		}
		if in.Sheet == "" {
			in.Sheet = t.Sheet
		}
		if in.HeaderRow == 0 {
			in.HeaderRow = t.HeaderRow
		}
		base := j.Params
		if in.Params != nil {
			base = *in.Params
		}
		merged := base.merge(t.Params)
		in.Params = &merged
		tplID = &t.ID
	}
	if err := in.ValidateFor(j.Kind); err != nil {
		return nil, err
	}
	params := j.Params
	if in.Params != nil {
		params = *in.Params
		if params.Source == "" {
			params.Source = j.Params.Source
		}
		if params.NotifyPhone == "" {
			params.NotifyPhone = j.Params.NotifyPhone
		}
	}
	if err := s.checkStores(p, params); err != nil {
		return nil, err
	}
	if j.Imported > 0 && (!maps.Equal(in.Mapping, j.Mapping) || (in.Sheet != "" && in.Sheet != j.Sheet) ||
		(in.HeaderRow != 0 && in.HeaderRow != j.HeaderRow)) {
		return nil, httpx.Conflict("%d rows are already imported with the current mapping; roll the job back or upload the file as a new job to change columns", j.Imported)
	}
	mj, _ := json.Marshal(in.Mapping)
	pj, _ := json.Marshal(params)
	err = db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE import_jobs SET mapping=$3, params=$4, sheet=NULLIF($5,''), header_row=$6,
				template_id=COALESCE($7::uuid, template_id), source=COALESCE(NULLIF($8,''), source),
				status='mapped', total_rows=0, valid_rows=0, error_rows=0, skipped_rows=0, warning_rows=0, failed=0,
				dr_total_paise=0, cr_total_paise=0, error=NULL, updated_at=now()
			WHERE tenant_id=$1 AND id=$2 AND status IN ('uploaded','mapped','validated','failed')`,
			p.TenantID, id, mj, pj, in.Sheet, in.HeaderRow, tplID, params.Source)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return httpx.Conflict("job is %s; mapping can no longer change", j.Status)
		}
		_, err = tx.Exec(ctx, `DELETE FROM import_rows WHERE tenant_id=$1 AND job_id=$2 AND status <> 'imported'`, p.TenantID, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	if in.SaveAs != "" {
		if _, err := s.SaveTemplate(ctx, p, TemplateInput{Kind: j.Kind, Name: in.SaveAs, Source: params.Source, Sheet: in.Sheet,
			HeaderRow: in.HeaderRow, Mapping: in.Mapping, Params: params.reusable()}); err != nil {
			return nil, err
		}
	}
	return s.Get(ctx, p.TenantID, id)
}

// ValidateSummary is returned by POST /validate.
type ValidateSummary struct {
	Job            *Job        `json:"job"`
	SampleErrors   []RowResult `json:"sample_errors"`
	SampleWarnings []RowResult `json:"sample_warnings"`
}

// Validate reads every row, validates it and stores the outcome in
// import_rows. Rows are streamed to Postgres in chunks (COPY), never held all
// in memory. Rows already imported by an earlier, failed commit keep their
// status and audit; they still take part in duplicate detection.
func (s *Service) Validate(ctx context.Context, p *auth.Principal, id string) (*ValidateSummary, error) {
	tenantID := p.TenantID
	j, err := s.Get(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	switch j.Status {
	case StatusRunning, StatusQueued, StatusCompleted, StatusRolledBack:
		return nil, httpx.Conflict("job is %s", j.Status)
	}
	wb, err := s.open(ctx, j)
	if err != nil {
		return nil, err
	}
	defer wb.Close()
	sh, err := pickSheet(wb, j.Sheet, j.Kind)
	if err != nil {
		return nil, err
	}
	hr, headers, _, err := head(wb, j.Kind, sh, j.HeaderRow, 0)
	if err != nil {
		return nil, err
	}
	m := j.Mapping
	if len(m) == 0 {
		m = mapping.Suggest(j.Kind, headers)
	}
	if missing := mapping.MissingRequired(j.Kind, m); len(missing) > 0 {
		return nil, httpx.Validation("map the required fields first: %v", missing)
	}
	index, unknown := mapping.Resolve(m, headers)
	if len(unknown) > 0 {
		return nil, httpx.Validation("mapped columns not found in row %d: %v", hr, unknown)
	}
	asOf := j.Params.AsOf
	if asOf == "" {
		asOf = defaultAsOf(s.Now())
	}
	opts := validate.Options{AsOf: asOf, StoreID: j.Params.StoreID, StoreMap: j.Params.StoreMap, AccountMap: j.Params.AccountMap,
		DocType: j.Params.DocType, Side: j.Params.Side, Today: s.Now()}
	if p.Claims != nil {
		opts.CanAccessStore = p.Claims.CanAccessStore
	}
	if j.Kind == mapping.KindInvoices && opts.DocType == "" {
		if _, ok := index["doc_type"]; !ok {
			opts.DocType = "sale"
		}
	}
	v := validate.New(j.Kind, index, opts)

	imported := map[int]bool{}
	rows, err := s.DB.Query(ctx, `SELECT row_no FROM import_rows WHERE tenant_id=$1 AND job_id=$2 AND status='imported'`, tenantID, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var n int
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return nil, err
		}
		imported[n] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	cols := []string{"job_id", "tenant_id", "row_no", "status", "record", "raw", "errors", "warnings"}
	var c struct{ total, valid, invalid, skipped, warned int }
	var dr, cr int64
	err = db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM import_rows WHERE tenant_id=$1 AND job_id=$2 AND status <> 'imported'`, tenantID, id); err != nil {
			return err
		}
		chunk := make([][]any, 0, copyChunk)
		flush := func() error {
			if len(chunk) == 0 {
				return nil
			}
			_, err := tx.CopyFrom(ctx, pgx.Identifier{"import_rows"}, cols, pgx.CopyFromRows(chunk))
			chunk = chunk[:0]
			return err
		}
		err := wb.Each(sh, func(rowNo int, cells []string) error {
			if rowNo <= hr || sheet.IsBlank(cells) {
				return nil
			}
			c.total++
			if c.total > MaxRows {
				return httpx.Validation("more than %d rows; split the file", MaxRows)
			}
			res := v.Row(rowNo, cells)
			if imported[rowNo] {
				c.valid++
				if res.Side == "dr" {
					dr += res.Amount
				} else if res.Side == "cr" {
					cr += res.Amount
				}
				return nil
			}
			status := RowValid
			var rec []byte
			switch {
			case len(res.Errors) > 0:
				status = RowInvalid
				c.invalid++
			case res.Skip:
				status = RowSkipped
				c.skipped++
				rec, _ = json.Marshal(res.Record)
			default:
				c.valid++
				rec, _ = json.Marshal(res.Record)
				if res.Side == "dr" {
					dr += res.Amount
				} else if res.Side == "cr" {
					cr += res.Amount
				}
			}
			if len(res.Warnings) > 0 {
				c.warned++
			}
			raw, _ := json.Marshal(res.Raw)
			errs, _ := json.Marshal(nonNil(res.Errors))
			warns, _ := json.Marshal(nonNil(res.Warnings))
			chunk = append(chunk, []any{id, tenantID, rowNo, status, rec, raw, errs, warns})
			if len(chunk) >= copyChunk {
				return flush()
			}
			return nil
		})
		if err != nil {
			return err
		}
		if err := flush(); err != nil {
			return err
		}
		mj, _ := json.Marshal(m)
		_, err = tx.Exec(ctx, `UPDATE import_jobs SET status='validated', sheet=$3, header_row=$4, mapping=$5,
				total_rows=$6, valid_rows=$7, error_rows=$8, skipped_rows=$9, warning_rows=$10, dr_total_paise=$11, cr_total_paise=$12,
				imported=(SELECT count(*) FROM import_rows WHERE tenant_id=$1 AND job_id=$2 AND status='imported'),
				failed=0, error=NULL, updated_at=now()
			WHERE tenant_id=$1 AND id=$2`, tenantID, id, sh, hr, mj, c.total, c.valid, c.invalid, c.skipped, c.warned, dr, cr)
		return err
	})
	if err != nil {
		return nil, err
	}
	j, err = s.Get(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	sample, _, err := s.Rows(ctx, tenantID, id, RowInvalid, httpx.Page{Limit: 20})
	if err != nil {
		return nil, err
	}
	warn, _, err := s.Rows(ctx, tenantID, id, "warnings", httpx.Page{Limit: 20})
	if err != nil {
		return nil, err
	}
	return &ValidateSummary{Job: j, SampleErrors: sample, SampleWarnings: warn}, nil
}

// Commit starts pushing valid rows to bahi/bhandar. Large jobs go to the worker.
func (s *Service) Commit(ctx context.Context, tenantID, id string) (*Job, error) {
	j, err := s.Get(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	pending := j.ValidRows - j.Imported
	if pending <= 0 && j.Status == StatusValidated {
		return nil, httpx.Validation("no valid rows to import; fix the errors and validate again")
	}
	next := StatusRunning
	useWorker := s.Worker != nil && s.LargeRows > 0 && pending > s.LargeRows
	if useWorker {
		next = StatusQueued
	}
	tag, err := s.DB.Exec(ctx, `UPDATE import_jobs SET status=$3, error=NULL, started_at=now(), finished_at=NULL, updated_at=now()
		WHERE tenant_id=$1 AND id=$2 AND status IN ('validated','failed')`, tenantID, id, next)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, httpx.Conflict("job is %s; validate it before committing", j.Status)
	}
	switch {
	case useWorker:
		if err := s.Worker.Trigger(ctx, id, tenantID); err != nil {
			s.fail(context.Background(), tenantID, id, "could not start the import worker: "+err.Error())
			return nil, httpx.Unavailable("could not start the import worker")
		}
	case s.sync:
		if err := s.RunCommit(context.Background(), tenantID, id); err != nil {
			s.Log.Error("import commit failed", "job_id", id, "error", err.Error())
		}
	default:
		// Detached from the request: the import outlives the HTTP call.
		go func() {
			if err := s.RunCommit(context.Background(), tenantID, id); err != nil {
				s.Log.Error("import commit failed", "job_id", id, "error", err.Error())
			}
		}()
	}
	return s.Get(ctx, tenantID, id)
}

func (s *Service) fail(ctx context.Context, tenantID, id, msg string) {
	if _, err := s.DB.Exec(ctx, `UPDATE import_jobs SET status='failed', error=$3, finished_at=now(), updated_at=now()
		WHERE tenant_id=$1 AND id=$2`, tenantID, id, msg); err != nil {
		s.Log.Error("mark job failed", "job_id", id, "error", err.Error())
	}
}

// RecoverStale fails jobs left 'running' by an instance that died mid-import
// (Cloud Run scale-in / deploy), and 'queued' jobs whose worker never started.
// Rows already imported stay imported; committing again continues with the
// remaining valid rows. This maintenance sweep is the one query that spans tenants.
func (s *Service) RecoverStale(ctx context.Context, olderThan time.Duration) int64 {
	tag, err := s.DB.Exec(ctx, `UPDATE import_jobs SET status='failed',
			error=CASE status WHEN 'queued' THEN 'the import worker never started; commit again'
				ELSE 'interrupted; commit again to continue' END,
			finished_at=now(), updated_at=now()
		WHERE (status='running' AND updated_at < now() - make_interval(secs => $1))
		   OR (status='queued'  AND updated_at < now() - make_interval(secs => $2))`,
		olderThan.Seconds(), max(olderThan, time.Hour).Seconds())
	if err != nil {
		s.Log.Warn("recover stale imports", "error", err.Error())
		return 0
	}
	n := tag.RowsAffected()
	if n > 0 {
		s.Log.Warn("marked interrupted imports as failed", "count", n)
	}
	return n
}

// Maintain runs RecoverStale now and then every interval until ctx ends, so a
// job interrupted on another instance is released without waiting for a restart.
// RunCommit refreshes updated_at after every batch, so a live import is never
// mistaken for a dead one.
func (s *Service) Maintain(ctx context.Context, every, staleAfter time.Duration) {
	s.RecoverStale(ctx, staleAfter)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.RecoverStale(ctx, staleAfter)
		}
	}
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func isBadUUID(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "22P02"
}

func sanitize(name string) string {
	name = filepath.Base(name)
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}
