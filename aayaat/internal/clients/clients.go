// Package clients pushes validated records to bahi and bhandar bulk endpoints.
package clients

import (
	"context"
	"time"

	"github.com/maapan/mool/httpx"
	"github.com/maapan/mool/svc"
)

// BulkError is one rejected record (index into the sent batch).
type BulkError struct {
	Index   int    `json:"index"`
	Message string `json:"message"`
}

// BulkResult is the common response of the bulk endpoints.
type BulkResult struct {
	Created int         `json:"created"`
	Updated int         `json:"updated"`
	Errors  []BulkError `json:"errors"`
}

// Targets holds the downstream services.
type Targets struct {
	Bahi    *svc.Client
	Bhandar *svc.Client
}

// Bulk posts one batch, retrying transient (5xx / network) failures 3 times.
func Bulk(ctx context.Context, c *svc.Client, path string, body any) (BulkResult, error) {
	var res BulkResult
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		res = BulkResult{}
		err = c.Post(ctx, path, body, &res)
		if err == nil {
			return res, nil
		}
		if e := httpx.AsError(err); e.Status > 0 && e.Status < 500 {
			return res, err // a 4xx will not get better on retry
		}
		select {
		case <-ctx.Done():
			return res, ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 2 * time.Second):
		}
	}
	return res, err
}
