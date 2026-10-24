package gateway

import (
	"errors"
	"fmt"
)

// ErrNotConfigured is returned when neither the tenant nor the platform has keys for a gateway.
var ErrNotConfigured = errors.New("gateway not configured")

// Error is a failed gateway API call. Status is the HTTP status the gateway
// answered with, or 0 when the request never got an answer (timeout, DNS,
// connection reset). Only a 4xx answer proves the operation did NOT happen;
// everything else leaves it unknown.
type Error struct {
	Gateway string
	Op      string // "POST /payments/pay_x/refund"
	Status  int
	Code    string
	Message string
	Err     error
}

func (e *Error) Error() string {
	switch {
	case e.Err != nil:
		return fmt.Sprintf("%s %s: %v", e.Gateway, e.Op, e.Err)
	case e.Message != "":
		return fmt.Sprintf("%s %s: %s (%s)", e.Gateway, e.Op, e.Message, e.Code)
	}
	return fmt.Sprintf("%s %s: status %d", e.Gateway, e.Op, e.Status)
}

func (e *Error) Unwrap() error { return e.Err }

// Definitive reports whether the gateway definitively rejected the call, so
// the operation certainly did not execute (an HTTP 4xx answer).
func (e *Error) Definitive() bool { return e.Status >= 400 && e.Status < 500 }

// IsDefinitive reports whether err is a definitive gateway rejection. A plain
// (non-gateway) error, a timeout or a 5xx is not definitive: the money may
// have moved, so the caller must not treat the operation as failed.
func IsDefinitive(err error) bool {
	var ge *Error
	return errors.As(err, &ge) && ge.Definitive()
}
