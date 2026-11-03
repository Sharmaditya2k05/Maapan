package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httputil"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/maapan/dwaar/internal/ratelimit"
	"github.com/maapan/mool/auth"
	"github.com/maapan/mool/httpx"
	"github.com/maapan/mool/logx"
)

// --- per-upstream metrics ---

// upstreamStats are cumulative counters since the instance started.
type upstreamStats struct {
	requests, err4xx, err5xx, bytesIn, bytesOut, totalMs atomic.Int64
}

// Traffic is the counters of one upstream as reported by /api/_status.
type Traffic struct {
	Requests     int64 `json:"requests"`
	Errors4xx    int64 `json:"errors_4xx"`
	Errors5xx    int64 `json:"errors_5xx"`
	BytesIn      int64 `json:"bytes_in"`  // request bodies sent to the service
	BytesOut     int64 `json:"bytes_out"` // response bodies returned to clients
	AvgLatencyMs int64 `json:"avg_latency_ms"`
}

func (s *upstreamStats) snapshot() Traffic {
	t := Traffic{Requests: s.requests.Load(), Errors4xx: s.err4xx.Load(), Errors5xx: s.err5xx.Load(),
		BytesIn: s.bytesIn.Load(), BytesOut: s.bytesOut.Load()}
	if t.Requests > 0 {
		t.AvgLatencyMs = s.totalMs.Load() / t.Requests
	}
	return t
}

// proxyMetered proxies r and records request/response sizes, status and
// latency for the upstream: one structured log line per request (Cloud
// Logging log-based metrics) plus in-process counters for /api/_status.
func (g *Gateway) proxyMetered(service string, proxy *httputil.ReverseProxy, w http.ResponseWriter, r *http.Request) {
	in := &countingReader{rc: r.Body}
	r.Body = in
	cw := &countingWriter{ResponseWriter: w, status: http.StatusOK}
	start := time.Now()
	proxy.ServeHTTP(cw, r)
	elapsed := time.Since(start)

	if st := g.stats[service]; st != nil {
		st.requests.Add(1)
		st.bytesIn.Add(in.n)
		st.bytesOut.Add(cw.n)
		st.totalMs.Add(elapsed.Milliseconds())
		switch {
		case cw.status >= 500:
			st.err5xx.Add(1)
		case cw.status >= 400:
			st.err4xx.Add(1)
		}
	}
	logx.From(r.Context()).Info("upstream", "service", service, "method", r.Method, "status", cw.status,
		"req_bytes", in.n, "resp_bytes", cw.n, "ms", elapsed.Milliseconds())
}

type countingReader struct {
	rc io.ReadCloser
	n  int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.rc.Read(p)
	c.n += int64(n)
	return n, err
}

func (c *countingReader) Close() error { return c.rc.Close() }

// countingWriter counts response bytes. It keeps Flush (SSE) and Unwrap
// (http.ResponseController) working.
type countingWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
	n           int64
}

func (c *countingWriter) WriteHeader(code int) {
	if !c.wroteHeader {
		c.status, c.wroteHeader = code, true
	}
	c.ResponseWriter.WriteHeader(code)
}

func (c *countingWriter) Write(b []byte) (int, error) {
	c.wroteHeader = true
	n, err := c.ResponseWriter.Write(b)
	c.n += int64(n)
	return n, err
}

func (c *countingWriter) Flush() {
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (c *countingWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }

// --- GET /api/_status ---

// Upstream health values.
const (
	HealthUp            = "up"
	HealthDown          = "down"
	HealthStarting      = "starting" // the host's edge answered: the service is waking up
	HealthNotConfigured = "not_configured"
)

// ServiceStatus is one service in /api/_status.
type ServiceStatus struct {
	Status    string   `json:"status"`
	LatencyMs int64    `json:"latency_ms,omitempty"`
	Traffic   *Traffic `json:"traffic,omitempty"` // only for authenticated callers
}

// StatusReport is the body of GET /api/_status.
type StatusReport struct {
	Status      string                   `json:"status"` // ok | degraded
	CheckedAt   time.Time                `json:"checked_at"`
	RateLimiter string                   `json:"rate_limiter"`
	Services    map[string]ServiceStatus `json:"services"`
}

type statusCache struct {
	mu     sync.Mutex // serialises probes so concurrent callers share one round
	at     time.Time
	report map[string]ServiceStatus
}

// Status serves GET /api/_status: health of every upstream (its /healthz),
// so the app can say "stock service is down" instead of a generic error.
// Results are cached for StatusTTL so the endpoint cannot be used to flood
// the services. It needs no token; with a valid access token the reply also
// carries per-upstream traffic counters of this gateway instance.
func (g *Gateway) Status(w http.ResponseWriter, r *http.Request) error {
	securityHeaders(w.Header())
	health, at := g.probeAll(r.Context())
	_, authErr := g.parse(auth.BearerToken(r))
	rep := StatusReport{Status: "ok", CheckedAt: at, RateLimiter: ratelimit.Mode(g.opt.Limiter),
		Services: make(map[string]ServiceStatus, len(Services))}
	for _, name := range Services {
		st := health[name]
		if st.Status == "" {
			st.Status = HealthNotConfigured
		}
		if st.Status != HealthUp && st.Status != HealthNotConfigured {
			rep.Status = "degraded"
		}
		if authErr == nil {
			if s := g.stats[name]; s != nil {
				t := s.snapshot()
				st.Traffic = &t
			}
		}
		rep.Services[name] = st
	}
	return httpx.OK(w, rep)
}

func (g *Gateway) probeAll(ctx context.Context) (map[string]ServiceStatus, time.Time) {
	c := &g.status
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.report != nil && time.Since(c.at) < g.opt.StatusTTL {
		return c.report, c.at
	}
	// Probe on a context detached from the caller: a client that goes away
	// must not leave a half-filled cache behind.
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), g.opt.StatusTimeout)
	defer cancel()
	names := make([]string, 0, len(g.opt.Upstreams))
	for name := range g.opt.Upstreams {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make(map[string]ServiceStatus, len(names))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, name := range names {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			st := g.probe(pctx, name)
			mu.Lock()
			out[name] = st
			mu.Unlock()
		}(name)
	}
	wg.Wait()
	c.report, c.at = out, time.Now().UTC()
	return c.report, c.at
}

var probeClient = &http.Client{}

func (g *Gateway) probe(ctx context.Context, name string) ServiceStatus {
	u := g.opt.Upstreams[name]
	base := strings.TrimRight(u.String(), "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/healthz", nil)
	if err != nil {
		return ServiceStatus{Status: HealthDown}
	}
	if g.opt.IDToken != nil {
		tok, err := g.opt.IDToken(ctx, u.Scheme+"://"+u.Host)
		if err != nil {
			return ServiceStatus{Status: HealthDown}
		}
		req.Header.Set("X-Serverless-Authorization", "Bearer "+tok)
	}
	start := time.Now()
	res, err := probeClient.Do(req)
	ms := time.Since(start).Milliseconds()
	if err != nil {
		return ServiceStatus{Status: HealthDown, LatencyMs: ms}
	}
	io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10)) //nolint:errcheck
	res.Body.Close()
	switch {
	case res.StatusCode == http.StatusOK:
		return ServiceStatus{Status: HealthUp, LatencyMs: ms}
	case fromEdge(res):
		return ServiceStatus{Status: HealthStarting, LatencyMs: ms}
	}
	return ServiceStatus{Status: HealthDown, LatencyMs: ms}
}
