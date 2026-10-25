package ledger

import (
	"encoding/csv"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/maapan/mool/httpx"
	"github.com/maapan/mool/money"
)

func nilIfEmpty(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// isBadUUID reports an invalid-text-representation error (e.g. a malformed id in the URL).
func isBadUUID(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "22P02"
}

func isSerialization(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && (pe.Code == "40001" || pe.Code == "40P01")
}

// dateRange reads ?from=&to= (defaults: start of current FY .. today).
func (s *Service) dateRange(r *http.Request) (string, string, error) {
	to := httpx.Query(r, "to")
	if to == "" {
		to = s.today()
	}
	from := httpx.Query(r, "from")
	if from == "" {
		t, _ := time.Parse(time.DateOnly, to)
		y := t.Year()
		if t.Month() < time.April {
			y--
		}
		from = fmt.Sprintf("%04d-04-01", y)
	}
	for _, d := range []string{from, to} {
		if _, err := time.Parse(time.DateOnly, d); err != nil {
			return "", "", httpx.Validation("from/to must be YYYY-MM-DD")
		}
	}
	if from > to {
		return "", "", httpx.Validation("from is after to")
	}
	return from, to, nil
}

func wantsCSV(r *http.Request) bool { return httpx.Query(r, "format") == "csv" }

// writeCSV streams rows as a CSV download.
func writeCSV(w http.ResponseWriter, name string, header []string, rows [][]string) error {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.csv"`, name))
	cw := csv.NewWriter(w)
	if err := cw.Write(header); err != nil {
		return err
	}
	if err := cw.WriteAll(rows); err != nil {
		return err
	}
	cw.Flush()
	return cw.Error()
}

// rupees renders paise as a plain decimal for CSV (no symbol/grouping).
func rupees(p int64) string { return fmt.Sprintf("%.2f", money.ToRupees(p)) }
