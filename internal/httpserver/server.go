// Package httpserver provides the indexer's HTTP surface: Prometheus metrics
// and a liveness/readiness health check for the running process, plus the
// analytics read API when a data source is supplied.
package httpserver

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/miguelnietoa/stellar-explorer/indexer/internal/analytics"
	"github.com/miguelnietoa/stellar-explorer/indexer/internal/health"
	"github.com/miguelnietoa/stellar-explorer/indexer/internal/metrics"
)

// pingTimeout bounds how long /healthz waits on the database before
// reporting unhealthy, so a stuck connection can't hang the probe.
const pingTimeout = 2 * time.Second

// readHeaderTimeout bounds how long a client may take to send its request
// headers, so a stalled or malicious connection cannot hold a worker open
// indefinitely once this server is exposed as a public read API.
const readHeaderTimeout = 10 * time.Second

// writeTimeout caps how long a single response may take, so one pathological
// analytics query cannot hold a connection indefinitely. idleTimeout reclaims
// keep-alive connections that have gone quiet.
// writeTimeout must stay below the caller's shutdown grace, so a request
// started just before SIGTERM cannot outlive the drain and have the database
// pool closed underneath it.
const (
	writeTimeout = 15 * time.Second
	idleTimeout  = 120 * time.Second
)

// pipelineStaleAfter is how long the ingestion loop can go without
// completing a poll cycle before /healthz reports it as not advancing.
// Ledgers close every ~5s, so this comfortably tolerates transient RPC
// slowness without masking a genuinely stuck pipeline.
const pipelineStaleAfter = 2 * time.Minute

// dbPinger is the subset of *sql.DB that /healthz needs, kept as an
// interface so the handler can be exercised with a fake in tests.
type dbPinger interface {
	PingContext(ctx context.Context) error
}

// staleChecker reports whether the live pipeline has stopped advancing.
// Kept as a function value (defaulting to health.Stale) so tests can
// simulate a stuck pipeline without depending on real elapsed time.
type staleChecker func(maxAge time.Duration) bool

// Server serves /metrics, /healthz, the domains read API, and the contract
// verification API.
type Server struct {
	srv          *http.Server
	domains      DomainReader
	verification VerificationStore
}

// Options configures what a Server exposes. /healthz and the domains read API
// are always mounted, since every process wants a probe and domain lookups
// degrade gracefully (indexed=false) when no reader is attached.
type Options struct {
	// DB backs /healthz, verifying the database is reachable and that the live
	// pipeline is still advancing.
	DB dbPinger
	// Analytics supplies the read API. Nil leaves those routes unmounted, which
	// is what a process that ingests but should not serve queries wants.
	Analytics analytics.Reader
	// AllowedOrigins is the CORS allow-list for the analytics routes.
	AllowedOrigins []string
	// ExposeMetrics mounts /metrics. It belongs on the ingesting process: the
	// registry holds ingestion counters, and publishing them from a process
	// that does not ingest reports every one of them as zero, which drags down
	// any average or minimum an alert is built on.
	ExposeMetrics bool
}

// New builds a Server listening on addr with the given options.
func New(addr string, opts Options) *Server {
	s := &Server{}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", healthzHandler(opts.DB, health.Stale))

	if opts.ExposeMetrics {
		mux.Handle("/metrics", promhttp.HandlerFor(metrics.Registry, promhttp.HandlerOpts{}))
	}
	if opts.Analytics != nil {
		analytics.NewHandler(opts.Analytics, opts.AllowedOrigins).Register(mux)
	}
	mux.HandleFunc("GET /v1/domains/{name}/events", s.handleDomainEvents)
	mux.HandleFunc("GET /v1/domains/{name}", s.handleDomainByName)
	mux.HandleFunc("GET /v1/domains", s.handleDomains)

	mux.HandleFunc("POST /v1/verify", rateLimitMiddleware(newIPRateLimiter(verifyRateLimitPerMinute, verifyRateLimitWindow), s.handleVerifySubmit))
	mux.HandleFunc("GET /v1/verify/contract/{contractId}", s.handleVerifyByContract)
	mux.HandleFunc("GET /v1/verify/wasm/{wasmHash}/source/{path...}", s.handleVerifySourceFile)
	mux.HandleFunc("GET /v1/verify/wasm/{wasmHash}/source", s.handleVerifySourceTree)
	mux.HandleFunc("GET /v1/verify/wasm/{wasmHash}", s.handleVerifyByWasmHash)

	s.srv = &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: readHeaderTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}
	return s
}

// SetDomainReader attaches the domains store used by the read API. When unset,
// domain endpoints return HTTP 200 with indexed=false.
func (s *Server) SetDomainReader(r DomainReader) {
	s.domains = r
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
	Reason string `json:"reason,omitempty"`
}

// healthzHandler reports readiness based on database connectivity and
// pipeline liveness: 200 when the database responds and the ingestion loop
// has ticked within pipelineStaleAfter, 503 otherwise. The underlying DB
// error is logged server-side but never returned in the response body, since
// /healthz may be reachable to unauthenticated callers (probes, load
// balancers) and the error can carry internal infrastructure details.
func healthzHandler(db dbPinger, isStale staleChecker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), pingTimeout)
		defer cancel()

		w.Header().Set("Content-Type", "application/json")

		if err := db.PingContext(ctx); err != nil {
			log.Printf("healthz: database ping failed: %v", err)
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(healthStatus{Status: "unhealthy", Reason: "database unreachable"})
			return
		}

		if isStale(pipelineStaleAfter) {
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(healthStatus{Status: "unhealthy", Reason: "pipeline not advancing"})
			return
		}

		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(healthStatus{Status: "ok"})
	}
}
