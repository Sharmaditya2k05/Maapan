package httpx

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"
)

// Checker is pinged by /readyz (database, redis, ...).
type Checker func(ctx context.Context) error

// Health registers /healthz and /readyz on mux.
func Health(mux *http.ServeMux, checks map[string]Checker) {
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_ = OK(w, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		res := map[string]string{}
		status := http.StatusOK
		for name, c := range checks {
			if err := c(ctx); err != nil {
				res[name] = err.Error()
				status = http.StatusServiceUnavailable
			} else {
				res[name] = "ok"
			}
		}
		_ = JSON(w, status, res)
	})
}

// Serve runs h on :port until ctx is cancelled, then drains for up to 10s
// (Cloud Run sends SIGTERM and allows 10s before SIGKILL).
func Serve(ctx context.Context, log *slog.Logger, port string, h http.Handler) error {
	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "port", port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
		close(errc)
	}()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), 9*time.Second)
	defer cancel()
	return srv.Shutdown(sctx)
}

// Standard wraps a mux with the middleware stack every service uses.
func Standard(log *slog.Logger, corsOrigins []string, h http.Handler) http.Handler {
	return Chain(h, RequestID(log), Recover, AccessLog, CORS(corsOrigins))
}
