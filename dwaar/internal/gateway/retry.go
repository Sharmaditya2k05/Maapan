package gateway

import (
	"bytes"
	"errors"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/maapan/mool/httpx"
)

// edgeRetry retries requests that never reached the upstream service.
//
// Hosts that put idle services to sleep (Render's free plan) answer from their
// edge proxy with 502/503/504 while a service starts or restarts. Every Maapan
// service stamps its responses with X-Request-ID, so a gateway error without
// it came from the edge and the service never saw the request: it is safe to
// send again, whatever the method. Connection failures are retried only when
// the connection was never made or the request is a read.
type edgeRetry struct {
	next   http.RoundTripper
	budget time.Duration // total time spent retrying one request
}

func (e edgeRetry) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil && req.Body != http.NoBody {
		b, err := io.ReadAll(req.Body)
		req.Body.Close()
		if err != nil {
			return nil, err
		}
		body = b
	}
	deadline := time.Now().Add(e.budget)
	delay := time.Second
	for {
		try := req.Clone(req.Context())
		if body != nil {
			try.Body = io.NopCloser(bytes.NewReader(body))
			try.ContentLength = int64(len(body))
			try.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
		}
		res, err := e.next.RoundTrip(try)
		retry := false
		switch {
		case err != nil:
			retry = req.Context().Err() == nil && (isDialError(err) || req.Method == http.MethodGet || req.Method == http.MethodHead)
		case fromEdge(res):
			retry = true
		}
		if !retry || time.Now().Add(delay).After(deadline) {
			return res, err
		}
		if res != nil {
			io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10)) //nolint:errcheck
			res.Body.Close()
		}
		select {
		case <-req.Context().Done():
			return nil, req.Context().Err()
		case <-time.After(delay):
		}
		if delay *= 2; delay > 8*time.Second {
			delay = 8 * time.Second
		}
	}
}

// fromEdge reports a gateway-class error that no Maapan service produced.
func fromEdge(res *http.Response) bool {
	switch res.StatusCode {
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return res.Header.Get(httpx.HeaderRequestID) == ""
	}
	return false
}

func isDialError(err error) bool {
	var op *net.OpError
	return errors.As(err, &op) && op.Op == "dial"
}
