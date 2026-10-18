package httpx

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5/pgconn"
)

// Error is an API error with a stable machine-readable code.
type Error struct {
	Status  int            `json:"-"`
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Message) }

// WithDetails returns a copy of e carrying extra details.
func (e *Error) WithDetails(d map[string]any) *Error {
	c := *e
	c.Details = d
	return &c
}

func newErr(status int, code string) func(format string, a ...any) *Error {
	return func(format string, a ...any) *Error {
		return &Error{Status: status, Code: code, Message: fmt.Sprintf(format, a...)}
	}
}

var (
	BadRequest   = newErr(http.StatusBadRequest, "bad_request")
	Validation   = newErr(http.StatusUnprocessableEntity, "validation_failed")
	Unauthorized = newErr(http.StatusUnauthorized, "unauthorized")
	Forbidden    = newErr(http.StatusForbidden, "forbidden")
	NotFound     = newErr(http.StatusNotFound, "not_found")
	Conflict     = newErr(http.StatusConflict, "conflict")
	TooMany      = newErr(http.StatusTooManyRequests, "rate_limited")
	Upstream     = newErr(http.StatusBadGateway, "upstream_error")
	Unavailable  = newErr(http.StatusServiceUnavailable, "unavailable")
	Internal     = newErr(http.StatusInternalServerError, "internal_error")
)

// AsError converts any error into an *Error (unknown errors become 500).
func AsError(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	// A malformed id in the URL (e.g. "/v1/items/abc" against a UUID column)
	// is a missing resource, not a server fault.
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "22P02" {
		return NotFound("not found")
	}
	return Internal("something went wrong")
}
