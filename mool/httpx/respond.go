package httpx

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/taraju/mool/logx"
)

// MaxBodyBytes caps JSON request bodies.
const MaxBodyBytes = 2 << 20 // 2 MiB

// HandlerFunc is an http handler that returns an error instead of writing it.
type HandlerFunc func(w http.ResponseWriter, r *http.Request) error

// H adapts a HandlerFunc to http.Handler, writing errors as JSON.
func H(fn HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := fn(w, r); err != nil {
			WriteError(w, r, err)
		}
	})
}

// JSON writes v with the given status.
func JSON(w http.ResponseWriter, status int, v any) error {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v == nil {
		return nil
	}
	return json.NewEncoder(w).Encode(v)
}

// OK writes 200 with v.
func OK(w http.ResponseWriter, v any) error { return JSON(w, http.StatusOK, v) }

// Created writes 201 with v.
func Created(w http.ResponseWriter, v any) error { return JSON(w, http.StatusCreated, v) }

// NoContent writes 204.
func NoContent(w http.ResponseWriter) error { w.WriteHeader(http.StatusNoContent); return nil }

// WriteError writes err as {"error": {...}} and logs 5xx.
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	e := AsError(err)
	if e.Status >= 500 {
		logx.From(r.Context()).Error("request failed", "error", err.Error(), "path", r.URL.Path)
	}
	_ = JSON(w, e.Status, map[string]any{"error": e})
}

// Validator is implemented by request bodies that can check themselves.
type Validator interface{ Validate() error }

// Decode reads a JSON body into dst, rejecting unknown fields, then runs Validate if present.
func Decode(r *http.Request, dst any) error {
	if r.Body == nil {
		return BadRequest("request body is required")
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, MaxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return BadRequest("request body is required")
		}
		return BadRequest("invalid JSON: %s", err.Error())
	}
	if v, ok := dst.(Validator); ok {
		if err := v.Validate(); err != nil {
			var e *Error
			if errors.As(err, &e) {
				return e
			}
			return Validation("%s", err.Error())
		}
	}
	return nil
}

// Page is the standard list pagination input.
type Page struct {
	Limit  int
	Offset int
}

// ParsePage reads ?limit=&offset= with sane bounds (default 50, max 500).
func ParsePage(r *http.Request) Page {
	p := Page{Limit: 50}
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 {
		p.Limit = min(v, 500)
	}
	if v, err := strconv.Atoi(r.URL.Query().Get("offset")); err == nil && v > 0 {
		p.Offset = v
	}
	return p
}

// List is the standard list response.
type List[T any] struct {
	Items  []T `json:"items"`
	Total  int `json:"total"`
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
}

// Query returns a trimmed query parameter.
func Query(r *http.Request, key string) string {
	return strings.TrimSpace(r.URL.Query().Get(key))
}
