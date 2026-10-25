// Package hsn is kar's effective-dated HSN/SAC master (hsn_rates).
//
// Every row is a rate in force for a code (and an optional condition such as
// "sale value up to ₹2,500 per piece") from valid_from to valid_to. Real data
// enters only through the CSV import; nothing here invents or edits a rate.
// The 69 starter rows migrated from the Phase-1 table carry
// source='seed-unverified' and valid_from 2017-07-01 — treat them as hints.
package hsn

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/maapan/mool/db"
	"github.com/maapan/mool/gst"
)

// SourceSeed marks the unverified Phase-1 starter rows.
const SourceSeed = "seed-unverified"

// Rate is one hsn_rates row.
type Rate struct {
	Code             string  `json:"code"`
	Description      string  `json:"description"`
	Kind             string  `json:"kind"` // goods | service
	RateBP           int     `json:"rate_bp"`
	CessBP           int     `json:"cess_bp"`
	CessPerUnitPaise int64   `json:"cess_per_unit_paise"`
	ValidFrom        string  `json:"valid_from"`         // YYYY-MM-DD
	ValidTo          *string `json:"valid_to,omitempty"` // inclusive; nil = still in force
	Condition        string  `json:"condition,omitempty"`
	Source           string  `json:"source"`
	Verified         bool    `json:"verified"` // false for seed-unverified rows

	// Kept for clients written against the Phase-1 API.
	DefaultRateBP int `json:"default_rate_bp"`
	CessRateBP    int `json:"cess_rate_bp"`
}

// Code is a code with every rate in force on a date (one per condition).
type Code struct {
	Rate
	On    string `json:"on"`
	Rates []Rate `json:"rates"`
}

// Store reads and writes hsn_rates.
type Store struct {
	DB  *pgxpool.Pool
	Now func() time.Time
}

func (s *Store) today() time.Time {
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	t := now().In(gst.IST)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// ParseOn parses ?on=YYYY-MM-DD ("" = today).
func (s *Store) ParseOn(v string) (time.Time, error) {
	if strings.TrimSpace(v) == "" {
		return s.today(), nil
	}
	t, err := time.Parse(time.DateOnly, strings.TrimSpace(v))
	if err != nil {
		return time.Time{}, errors.New("on must be YYYY-MM-DD")
	}
	return t, nil
}

const rateCols = `code, description, kind, rate_bp, cess_bp, cess_per_unit_paise, to_char(valid_from,'YYYY-MM-DD'),
	to_char(valid_to,'YYYY-MM-DD'), condition, source`

func scanRate(rows pgx.Rows) (Rate, error) {
	var r Rate
	var to *string
	err := rows.Scan(&r.Code, &r.Description, &r.Kind, &r.RateBP, &r.CessBP, &r.CessPerUnitPaise, &r.ValidFrom, &to, &r.Condition, &r.Source)
	r.ValidTo = to
	r.Verified = r.Source != SourceSeed
	r.DefaultRateBP, r.CessRateBP = r.RateBP, r.CessBP
	return r, err
}

func collect(rows pgx.Rows, err error) ([]Rate, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Rate{}
	for rows.Next() {
		r, err := scanRate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// inForce selects, per (code, condition), the latest row valid on $1.
const inForce = `SELECT DISTINCT ON (code, condition) code, description, kind, rate_bp, cess_bp, cess_per_unit_paise,
	valid_from, valid_to, condition, source, search FROM hsn_rates WHERE valid_from <= $1::date AND (valid_to IS NULL OR valid_to >= $1::date)`

var numeric = regexp.MustCompile(`^[0-9]{1,8}$`)

// TSQuery turns free text into a prefix tsquery ("bisc cake" → "bisc:* & cake:*").
// Only letters and digits survive, so user input cannot inject tsquery syntax.
func TSQuery(q string) string {
	var terms []string
	for _, f := range strings.FieldsFunc(strings.ToLower(q), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && !unicode.IsMark(r) }) {
		if len(terms) == 8 {
			break
		}
		terms = append(terms, f+":*")
	}
	return strings.Join(terms, " & ")
}

// Search finds codes in force on `on`: a numeric query matches code prefixes,
// anything else runs a full-text query on the GIN index (hsn_rates_search).
func (s *Store) Search(ctx context.Context, q string, on time.Time, limit int) ([]Rate, error) {
	q = strings.TrimSpace(q)
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	switch {
	case q == "":
		return collect(s.DB.Query(ctx, `SELECT `+rateCols+` FROM (`+inForce+` ORDER BY code, condition, valid_from DESC) r
			ORDER BY code, condition LIMIT $2`, on.Format(time.DateOnly), limit))
	case numeric.MatchString(q):
		return collect(s.DB.Query(ctx, `SELECT `+rateCols+` FROM (`+inForce+` AND code LIKE $2 || '%'
			ORDER BY code, condition, valid_from DESC) r ORDER BY length(code), code, condition LIMIT $3`, on.Format(time.DateOnly), q, limit))
	}
	tsq := TSQuery(q)
	if tsq == "" {
		return []Rate{}, nil
	}
	return collect(s.DB.Query(ctx, `SELECT `+rateCols+` FROM (`+inForce+` AND search @@ to_tsquery('simple', $2)
		ORDER BY code, condition, valid_from DESC) r
		ORDER BY ts_rank(search, to_tsquery('simple', $2)) DESC, code, condition LIMIT $3`, on.Format(time.DateOnly), tsq, limit))
}

// Get returns a code with its rates in force on `on` (ok=false if none).
// The top-level fields are the unconditional rate, or the first condition.
func (s *Store) Get(ctx context.Context, code string, on time.Time) (Code, bool, error) {
	rates, err := collect(s.DB.Query(ctx, `SELECT `+rateCols+` FROM (`+inForce+` AND code = $2
		ORDER BY code, condition, valid_from DESC) r ORDER BY condition`, on.Format(time.DateOnly), strings.TrimSpace(code)))
	if err != nil || len(rates) == 0 {
		return Code{}, false, err
	}
	return Code{Rate: rates[0], On: on.Format(time.DateOnly), Rates: rates}, true, nil
}

// ---------- CSV import ----------

// Header is the required CSV header.
var Header = []string{"code", "description", "rate_bp", "cess_bp", "cess_per_unit_paise", "valid_from", "valid_to", "condition"}

// Row is one validated CSV row.
type Row struct {
	Line             int
	Code             string
	Description      string
	RateBP           int
	CessBP           int
	CessPerUnitPaise int64
	ValidFrom        time.Time
	ValidTo          *time.Time
	Condition        string
}

// RowError reports a rejected CSV row (line numbers count the header as 1).
type RowError struct {
	Line  int    `json:"line"`
	Code  string `json:"code,omitempty"`
	Error string `json:"error"`
}

// Report is the import result.
type Report struct {
	Rows     int        `json:"rows"`
	Inserted int        `json:"inserted"`
	Updated  int        `json:"updated"`
	Closed   int        `json:"closed"` // earlier open rows whose valid_to was set to the day before a new valid_from
	Rejected int        `json:"rejected"`
	Errors   []RowError `json:"errors"`
}

// MaxRows bounds one import.
const MaxRows = 100000

var codePattern = regexp.MustCompile(`^([0-9]{4}|[0-9]{6}|[0-9]{8})$`)

// KindOf classifies a code: SAC chapter 99 is a service.
func KindOf(code string) string {
	if strings.HasPrefix(code, "99") {
		return "service"
	}
	return "goods"
}

// ParseCSV reads and validates the CSV. Valid rows are returned even when
// others fail; a broken header or unreadable file is a hard error.
func ParseCSV(r io.Reader) ([]Row, []RowError, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	cr.TrimLeadingSpace = true
	head, err := cr.Read()
	if err != nil {
		return nil, nil, fmt.Errorf("csv header: %w", err)
	}
	idx := map[string]int{}
	for i, h := range head {
		idx[strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, "\ufeff")))] = i
	}
	for _, h := range Header {
		if _, ok := idx[h]; !ok {
			return nil, nil, fmt.Errorf("csv header must be %s (missing %q)", strings.Join(Header, ","), h)
		}
	}
	var rows []Row
	var errs []RowError
	seen := map[string]int{}
	line := 1
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		line++
		if err != nil {
			errs = append(errs, RowError{Line: line, Error: err.Error()})
			continue
		}
		if len(rows)+len(errs) >= MaxRows {
			return nil, nil, fmt.Errorf("an import is limited to %d rows", MaxRows)
		}
		get := func(k string) string {
			if i := idx[k]; i < len(rec) {
				return strings.TrimSpace(rec[i])
			}
			return ""
		}
		row, rerr := parseRow(line, get)
		if rerr != "" {
			errs = append(errs, RowError{Line: line, Code: get("code"), Error: rerr})
			continue
		}
		key := row.Code + "|" + row.ValidFrom.Format(time.DateOnly) + "|" + row.Condition
		if first, dup := seen[key]; dup {
			errs = append(errs, RowError{Line: line, Code: row.Code, Error: fmt.Sprintf("duplicate of line %d (same code, valid_from and condition)", first)})
			continue
		}
		seen[key] = line
		rows = append(rows, row)
	}
	return rows, errs, nil
}

func parseRow(line int, get func(string) string) (Row, string) {
	row := Row{Line: line, Code: get("code"), Description: get("description"), Condition: get("condition")}
	if !codePattern.MatchString(row.Code) {
		return row, "code must be 4, 6 or 8 digits"
	}
	if row.Description == "" || len([]rune(row.Description)) > 500 {
		return row, "description is required (at most 500 characters)"
	}
	if len([]rune(row.Condition)) > 200 {
		return row, "condition exceeds 200 characters"
	}
	var err error
	if row.ValidFrom, err = time.Parse(time.DateOnly, get("valid_from")); err != nil {
		return row, "valid_from must be YYYY-MM-DD"
	}
	if v := get("valid_to"); v != "" {
		t, err := time.Parse(time.DateOnly, v)
		if err != nil {
			return row, "valid_to must be YYYY-MM-DD or empty"
		}
		if t.Before(row.ValidFrom) {
			return row, "valid_to is before valid_from"
		}
		row.ValidTo = &t
	}
	if row.RateBP, err = strconv.Atoi(get("rate_bp")); err != nil {
		return row, "rate_bp must be an integer (basis points, 1800 = 18%)"
	}
	// ValidRate works on IST calendar dates.
	on := time.Date(row.ValidFrom.Year(), row.ValidFrom.Month(), row.ValidFrom.Day(), 12, 0, 0, 0, gst.IST)
	if !gst.ValidRate(row.RateBP, on) {
		return row, fmt.Sprintf("rate_bp %d is not a GST slab in force on %s (slabs then: %v)", row.RateBP,
			row.ValidFrom.Format(time.DateOnly), gst.RatesOn(on))
	}
	if c := get("cess_bp"); c != "" {
		if row.CessBP, err = strconv.Atoi(c); err != nil || row.CessBP < 0 || row.CessBP > 30000 {
			return row, "cess_bp must be an integer from 0 to 30000"
		}
	}
	if c := get("cess_per_unit_paise"); c != "" {
		if row.CessPerUnitPaise, err = strconv.ParseInt(c, 10, 64); err != nil || row.CessPerUnitPaise < 0 {
			return row, "cess_per_unit_paise must be a non-negative integer"
		}
	}
	return row, ""
}

// Import validates the CSV and upserts every valid row by (code, valid_from,
// condition) in one transaction. When a row starts a new rate, an earlier
// open-ended row for the same code and condition is closed the day before.
func (s *Store) Import(ctx context.Context, r io.Reader, source string) (Report, error) {
	rows, errs, err := ParseCSV(r)
	if err != nil {
		return Report{}, err
	}
	source = strings.TrimSpace(source)
	if source == "" || source == SourceSeed {
		source = "import"
	}
	rep := Report{Rows: len(rows) + len(errs), Rejected: len(errs), Errors: errs}
	if rep.Errors == nil {
		rep.Errors = []RowError{}
	}
	if len(rows) == 0 {
		return rep, nil
	}
	err = db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		for _, row := range rows {
			var inserted bool
			if err := tx.QueryRow(ctx, `INSERT INTO hsn_rates (code, description, kind, rate_bp, cess_bp, cess_per_unit_paise,
					valid_from, valid_to, condition, source, updated_at)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,now())
				ON CONFLICT (code, valid_from, condition) DO UPDATE SET description=EXCLUDED.description, kind=EXCLUDED.kind,
					rate_bp=EXCLUDED.rate_bp, cess_bp=EXCLUDED.cess_bp, cess_per_unit_paise=EXCLUDED.cess_per_unit_paise,
					valid_to=EXCLUDED.valid_to, source=EXCLUDED.source, updated_at=now()
				RETURNING (xmax = 0)`,
				row.Code, row.Description, KindOf(row.Code), row.RateBP, row.CessBP, row.CessPerUnitPaise,
				row.ValidFrom, row.ValidTo, row.Condition, source).Scan(&inserted); err != nil {
				return fmt.Errorf("line %d: %w", row.Line, err)
			}
			if inserted {
				rep.Inserted++
			} else {
				rep.Updated++
			}
			tag, err := tx.Exec(ctx, `UPDATE hsn_rates SET valid_to = $3::date - 1, updated_at = now()
				WHERE code=$1 AND condition=$2 AND valid_from < $3 AND (valid_to IS NULL OR valid_to >= $3)`,
				row.Code, row.Condition, row.ValidFrom)
			if err != nil {
				return fmt.Errorf("line %d: %w", row.Line, err)
			}
			rep.Closed += int(tag.RowsAffected())
		}
		return nil
	})
	return rep, err
}
