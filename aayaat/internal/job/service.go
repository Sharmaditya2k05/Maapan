package job

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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
	"github.com/maapan/mool/svc"
)

// Limits.
const (
	MaxFileBytes = 50 << 20
	MaxRows      = 200_000
	BatchSize    = 500
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
}

var errStop = errors.New("stop")

// Create stores the upload and registers a job.
func (s *Service) Create(ctx context.Context, p *auth.Principal, kind, fileName, contentType string, r io.Reader, params Params) (*Job, error) {
	if !mapping.ValidKind(kind) {
		return nil, httpx.Validation("kind must be parties, items, opening_balances or opening_stock")
	}
	ext := strings.ToLower(filepath.Ext(fileName))
	if ext != ".xlsx" && ext != ".xlsm" && ext != ".csv" && ext != ".txt" {
		return nil, httpx.Validation("upload .xlsx or .csv (export from MargBooks: Reports → Export → Excel)")
	}
	if err := params.validate(); err != nil {
		return nil, err
	}
	if err := s.checkStore(p, kind, params.StoreID); err != nil {
		return nil, err
	}
	id := uuid.NewString()
	now := s.Now().UTC()
	key := fmt.Sprintf("imports/%s/%s/%s/%s", p.TenantID, now.Format("2006/01"), id, sanitize(fileName))
	size, err := s.Store.Put(ctx, key, io.LimitReader(r, MaxFileBytes+1), contentType)
	if err != nil {
		return nil, fmt.Errorf("store upload: %w", err)
	}
	if size > MaxFileBytes {
		return nil, httpx.Validation("file is larger than 50 MB; split it by sheet or year")
	}
	pj, _ := json.Marshal(params)
	if _, err := s.DB.Exec(ctx, `INSERT INTO import_jobs (id, tenant_id, kind, file_name, file_key, file_size, content_type, params, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, id, p.TenantID, kind, fileName, key, size, contentType, pj, p.UserID()); err != nil {
		return nil, err
	}
	return s.Get(ctx, p.TenantID, id)
}

func (s *Service) checkStore(p *auth.Principal, kind, storeID string) error {
	if kind == mapping.KindOpeningStock && storeID != "" && p.Claims != nil && !p.Claims.CanAccessStore(storeID) {
		return httpx.Forbidden("no access to store")
	}
	return nil
}

// Preview describes the file and suggests a mapping.
type Preview struct {
	Sheets           []string          `json:"sheets"`
	Sheet            string            `json:"sheet"`
	HeaderRow        int               `json:"header_row"`
	Headers          []string          `json:"headers"`
	Rows             [][]string        `json:"rows"`
	SuggestedMapping map[string]string `json:"suggested_mapping"`
	Mapping          map[string]string `json:"mapping"`
	Fields           []mapping.Field   `json:"fields"`
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
		return headerRow, nil, nil, httpx.Validation("sheet has fewer than %d rows", headerRow)
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

func pickSheet(wb sheet.Workbook, want string) (string, error) {
	sheets := wb.Sheets()
	if len(sheets) == 0 {
		return "", httpx.Validation("the file has no sheets")
	}
	if want == "" {
		return sheets[0], nil
	}
	for _, sh := range sheets {
		if sh == want {
			return sh, nil
		}
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
	sh, err := pickSheet(wb, sheetName)
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
	return &Preview{Sheets: wb.Sheets(), Sheet: sh, HeaderRow: hr, Headers: headers, Rows: rows,
		SuggestedMapping: mapping.Suggest(j.Kind, headers), Mapping: j.Mapping, Fields: mapping.Fields[j.Kind]}, nil
}

// SetMapping saves the column mapping and options; previous validation is discarded.
func (s *Service) SetMapping(ctx context.Context, p *auth.Principal, id string, in MappingInput) (*Job, error) {
	j, err := s.Get(ctx, p.TenantID, id)
	if err != nil {
		return nil, err
	}
	if err := in.ValidateFor(j.Kind); err != nil {
		return nil, err
	}
	params := j.Params
	if in.Params != nil {
		params = *in.Params
	}
	if err := s.checkStore(p, j.Kind, params.StoreID); err != nil {
		return nil, err
	}
	mj, _ := json.Marshal(in.Mapping)
	pj, _ := json.Marshal(params)
	err = db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE import_jobs SET mapping=$3, params=$4, sheet=NULLIF($5,''), header_row=$6,
				status='mapped', total_rows=0, valid_rows=0, error_rows=0, dr_total_paise=0, cr_total_paise=0,
				error=NULL, updated_at=now()
			WHERE tenant_id=$1 AND id=$2 AND status IN ('uploaded','mapped','validated','failed')`,
			p.TenantID, id, mj, pj, in.Sheet, in.HeaderRow)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return httpx.Conflict("job is %s; mapping can no longer change", j.Status)
		}
		_, err = tx.Exec(ctx, `DELETE FROM import_rows WHERE tenant_id=$1 AND job_id=$2`, p.TenantID, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, p.TenantID, id)
}

// ValidateSummary is returned by POST /validate.
type ValidateSummary struct {
	Job          *Job        `json:"job"`
	SampleErrors []RowResult `json:"sample_errors"`
}

// Validate reads every row, validates it and stores the outcome in import_rows.
func (s *Service) Validate(ctx context.Context, tenantID, id string) (*ValidateSummary, error) {
	j, err := s.Get(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	if j.Status == StatusRunning || j.Status == StatusQueued || j.Status == StatusCompleted {
		return nil, httpx.Conflict("job is %s", j.Status)
	}
	wb, err := s.open(ctx, j)
	if err != nil {
		return nil, err
	}
	defer wb.Close()
	sh, err := pickSheet(wb, j.Sheet)
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
	if j.Kind == mapping.KindOpeningStock && j.Params.StoreID == "" {
		return nil, httpx.Validation("opening stock needs params.store_id")
	}

	v := validate.New(j.Kind, index)
	var rows [][]any
	var total, valid, invalid int
	var dr, cr int64
	err = wb.Each(sh, func(rowNo int, cells []string) error {
		if rowNo <= hr || sheet.IsBlank(cells) {
			return nil
		}
		total++
		if total > MaxRows {
			return httpx.Validation("more than %d rows; split the file", MaxRows)
		}
		res := v.Row(rowNo, cells)
		status := "valid"
		var rec []byte
		if len(res.Errors) > 0 {
			status = "invalid"
			invalid++
		} else {
			valid++
			rec, _ = json.Marshal(res.Record)
			if res.Side == "dr" {
				dr += res.Amount
			} else if res.Side == "cr" {
				cr += res.Amount
			}
		}
		raw, _ := json.Marshal(res.Raw)
		errs, _ := json.Marshal(nonNil(res.Errors))
		rows = append(rows, []any{id, tenantID, rowNo, status, rec, raw, errs})
		return nil
	})
	if err != nil {
		return nil, err
	}

	err = db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM import_rows WHERE tenant_id=$1 AND job_id=$2`, tenantID, id); err != nil {
			return err
		}
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{"import_rows"},
			[]string{"job_id", "tenant_id", "row_no", "status", "record", "raw", "errors"}, pgx.CopyFromRows(rows)); err != nil {
			return err
		}
		mj, _ := json.Marshal(m)
		_, err := tx.Exec(ctx, `UPDATE import_jobs SET status='validated', sheet=$3, header_row=$4, mapping=$5,
				total_rows=$6, valid_rows=$7, error_rows=$8, dr_total_paise=$9, cr_total_paise=$10, error=NULL, updated_at=now()
			WHERE tenant_id=$1 AND id=$2`, tenantID, id, sh, hr, mj, total, valid, invalid, dr, cr)
		return err
	})
	if err != nil {
		return nil, err
	}
	j, err = s.Get(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	sample, _, err := s.Rows(ctx, tenantID, id, "invalid", httpx.Page{Limit: 20})
	if err != nil {
		return nil, err
	}
	return &ValidateSummary{Job: j, SampleErrors: sample}, nil
}

// Commit starts pushing valid rows to bahi/bhandar. Large jobs go to the worker.
func (s *Service) Commit(ctx context.Context, tenantID, id string) (*Job, error) {
	j, err := s.Get(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	if j.ValidRows == 0 && j.Status == StatusValidated {
		return nil, httpx.Validation("no valid rows to import; fix the errors and validate again")
	}
	next := StatusRunning
	useWorker := s.Worker != nil && s.LargeRows > 0 && j.ValidRows > s.LargeRows
	if useWorker {
		next = StatusQueued
	}
	tag, err := s.DB.Exec(ctx, `UPDATE import_jobs SET status=$3, error=NULL, started_at=now(), updated_at=now()
		WHERE tenant_id=$1 AND id=$2 AND status IN ('validated','failed')`, tenantID, id, next)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, httpx.Conflict("job is %s; validate it before committing", j.Status)
	}
	if useWorker {
		if err := s.Worker.Trigger(ctx, id, tenantID); err != nil {
			s.fail(context.Background(), tenantID, id, "could not start the import worker: "+err.Error())
			return nil, httpx.Unavailable("could not start the import worker")
		}
	} else {
		// Detached from the request: the import outlives the HTTP call.
		go func() {
			if err := s.RunCommit(context.Background(), tenantID, id); err != nil {
				s.Log.Error("import commit failed", "job_id", id, "error", err.Error())
			}
		}()
	}
	return s.Get(ctx, tenantID, id)
}

// RunCommit pushes every valid row in batches and records per-row results.
// It is used by the in-process goroutine and by `aayaat -job <id> -tenant <id>`.
func (s *Service) RunCommit(ctx context.Context, tenantID, id string) (err error) {
	ctx = auth.WithPrincipal(ctx, &auth.Principal{Internal: true, TenantID: tenantID})
	j, err := s.Get(ctx, tenantID, id)
	if err != nil {
		return err
	}
	if j.Status != StatusRunning && j.Status != StatusQueued {
		return fmt.Errorf("job %s is %s, not running", id, j.Status)
	}
	if _, err := s.DB.Exec(ctx, `UPDATE import_jobs SET status='running', updated_at=now() WHERE tenant_id=$1 AND id=$2`, tenantID, id); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			s.fail(context.Background(), tenantID, id, err.Error())
		}
	}()
	if j.Kind == mapping.KindOpeningStock || j.Kind == mapping.KindOpeningBalances {
		if j.Params.AsOf == "" {
			j.Params.AsOf = defaultAsOf(s.Now())
		}
	}

	lastRow := 0
	for {
		rows, err := s.DB.Query(ctx, `SELECT row_no, record FROM import_rows
			WHERE tenant_id=$1 AND job_id=$2 AND status='valid' AND row_no > $3 ORDER BY row_no LIMIT $4`,
			tenantID, id, lastRow, BatchSize)
		if err != nil {
			return err
		}
		var rowNos []int
		var recs []json.RawMessage
		for rows.Next() {
			var n int
			var rec json.RawMessage
			if err := rows.Scan(&n, &rec); err != nil {
				rows.Close()
				return err
			}
			rowNos = append(rowNos, n)
			recs = append(recs, rec)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(rowNos) == 0 {
			break
		}
		lastRow = rowNos[len(rowNos)-1]

		client, path, body := s.batchRequest(j, recs)
		res, err := clients.Bulk(ctx, client, path, body)
		if err != nil {
			return fmt.Errorf("rows %d-%d: %w", rowNos[0], lastRow, err)
		}
		if err := s.recordBatch(ctx, tenantID, id, rowNos, res); err != nil {
			return err
		}
	}
	return s.complete(ctx, tenantID, id)
}

func (s *Service) batchRequest(j *Job, recs []json.RawMessage) (client *svc.Client, path string, body any) {
	switch j.Kind {
	case mapping.KindParties:
		return s.Targets.Bahi, "/internal/v1/bulk/parties", map[string]any{"parties": recs}
	case mapping.KindOpeningBalances:
		return s.Targets.Bahi, "/internal/v1/bulk/opening-balances", map[string]any{"as_of": j.Params.AsOf, "balances": recs}
	case mapping.KindItems:
		return s.Targets.Bhandar, "/internal/v1/bulk/items", map[string]any{"items": recs}
	default:
		return s.Targets.Bhandar, "/internal/v1/bulk/opening-stock",
			map[string]any{"store_id": j.Params.StoreID, "as_of": j.Params.AsOf, "lines": recs}
	}
}

// recordBatch marks rows imported, or failed with the downstream message.
func (s *Service) recordBatch(ctx context.Context, tenantID, id string, rowNos []int, res clients.BulkResult) error {
	failed := map[int]string{}
	for _, e := range res.Errors {
		if e.Index >= 0 && e.Index < len(rowNos) {
			failed[rowNos[e.Index]] = e.Message
		}
	}
	return db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		var ok []int
		for _, n := range rowNos {
			if msg, bad := failed[n]; bad {
				errs, _ := json.Marshal([]string{msg})
				if _, err := tx.Exec(ctx, `UPDATE import_rows SET status='failed', errors=$4 WHERE tenant_id=$1 AND job_id=$2 AND row_no=$3`,
					tenantID, id, n, errs); err != nil {
					return err
				}
			} else {
				ok = append(ok, n)
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE import_rows SET status='imported' WHERE tenant_id=$1 AND job_id=$2 AND row_no = ANY($3)`,
			tenantID, id, ok); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE import_jobs SET imported=imported+$3, failed=failed+$4, updated_at=now() WHERE tenant_id=$1 AND id=$2`,
			tenantID, id, len(ok), len(failed))
		return err
	})
}

func (s *Service) complete(ctx context.Context, tenantID, id string) error {
	err := db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		var kind, by string
		var params []byte
		var total, errorRows, imported, failed int
		err := tx.QueryRow(ctx, `UPDATE import_jobs SET status='completed', finished_at=now(), updated_at=now()
			WHERE tenant_id=$1 AND id=$2
			RETURNING kind, created_by, params, total_rows, error_rows, imported, failed`, tenantID, id).
			Scan(&kind, &by, &params, &total, &errorRows, &imported, &failed)
		if err != nil {
			return err
		}
		var p Params
		_ = json.Unmarshal(params, &p)
		ev, err := events.New(events.ImportCompleted, "aayaat", tenantID, p.StoreID, events.ImportCompletedPayload{
			JobID: id, Kind: kind, Total: total, Imported: imported, Failed: failed + errorRows,
			RequestedBy: by, NotifyPhone: p.NotifyPhone,
		})
		if err != nil {
			return err
		}
		return events.Enqueue(ctx, tx, ev)
	})
	if err == nil && s.Relay != nil {
		s.Relay.Kick()
	}
	return err
}

func (s *Service) fail(ctx context.Context, tenantID, id, msg string) {
	if _, err := s.DB.Exec(ctx, `UPDATE import_jobs SET status='failed', error=$3, finished_at=now(), updated_at=now()
		WHERE tenant_id=$1 AND id=$2`, tenantID, id, msg); err != nil {
		s.Log.Error("mark job failed", "job_id", id, "error", err.Error())
	}
}

// RecoverStale fails jobs left 'running' by an instance that died mid-import
// (Cloud Run scale-in / deploy). Rows already imported stay imported; committing
// again continues with the remaining valid rows. This maintenance sweep is the
// one query that spans tenants.
func (s *Service) RecoverStale(ctx context.Context, olderThan time.Duration) {
	tag, err := s.DB.Exec(ctx, `UPDATE import_jobs SET status='failed', error='interrupted; commit again to continue', updated_at=now()
		WHERE status='running' AND updated_at < now() - make_interval(secs => $1)`, olderThan.Seconds())
	if err != nil {
		s.Log.Warn("recover stale imports", "error", err.Error())
		return
	}
	if n := tag.RowsAffected(); n > 0 {
		s.Log.Warn("marked interrupted imports as failed", "count", n)
	}
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
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
