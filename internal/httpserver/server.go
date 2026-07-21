// Package httpserver provides an opt-in HTTP server exposing Prometheus
// metrics and a liveness/readiness health check for the running indexer
// process, so it can be monitored and probed by Docker/k8s.
package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/miguelnietoa/stellar-explorer/indexer/internal/metrics"
)

// pingTimeout bounds how long /healthz waits on the database before
// reporting unhealthy, so a stuck connection can't hang the probe.
const pingTimeout = 2 * time.Second

// dbPinger is the subset of *sql.DB that /healthz needs, kept as an
// interface so the handler can be exercised with a fake in tests.
type dbPinger interface {
	PingContext(ctx context.Context) error
}

// Server serves /metrics and /healthz for a running indexer process.
type Server struct {
	srv *http.Server
}

// New builds a Server listening on addr. db is used by /healthz to verify
// the database is reachable.
func New(addr string, db dbPinger) *Server {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(metrics.Registry, promhttp.HandlerOpts{}))
	mux.HandleFunc("/healthz", healthzHandler(db))

	return &Server{
		srv: &http.Server{
			Addr:    addr,
			Handler: mux,
		},
	}
}

// Start blocks serving requests until the server is shut down. It returns
// nil on a clean shutdown (http.ErrServerClosed).
func (s *Server) Start() error {
	if err := s.srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.srv.Shutdown(ctx)
}

type healthStatus struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// healthzHandler reports readiness based on database connectivity: 200 when
// the database is reachable, 503 otherwise.
func healthzHandler(db dbPinger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), pingTimeout)
		defer cancel()

		w.Header().Set("Content-Type", "application/json")

		if err := db.PingContext(ctx); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(healthStatus{Status: "unhealthy", Error: err.Error()})
			return
		}

		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(healthStatus{Status: "ok"})
	}
}
