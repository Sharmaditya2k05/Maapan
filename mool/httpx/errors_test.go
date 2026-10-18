package httpx

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestAsError(t *testing.T) {
	if e := AsError(fmt.Errorf("wrap: %w", &pgconn.PgError{Code: "22P02"})); e.Status != http.StatusNotFound {
		t.Fatalf("invalid uuid should be 404, got %d", e.Status)
	}
	if e := AsError(fmt.Errorf("boom")); e.Status != http.StatusInternalServerError {
		t.Fatalf("unknown should be 500, got %d", e.Status)
	}
	if e := AsError(Conflict("x")); e.Status != http.StatusConflict {
		t.Fatal("typed error kept")
	}
}
