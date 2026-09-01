package analytics

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"
)

// queryTimeout bounds how long a single request may occupy a database
// connection. The server's write timeout does not reach the query: it closes
// the connection while the statement keeps running and its pool slot stays
// held, so a burst of slow requests can exhaust the pool. Cancelling the
// context releases both.
const queryTimeout = 10 * time.Second

// Route paths, kept together because the explorer's client hard-codes them.
const (
	timeSeriesPath = "GET /api/v1/analytics/timeseries"
	topPath        = "GET /api/v1/analytics/top"
	preflightPath  = "OPTIONS /api/v1/analytics/"
)

// Reader supplies the aggregated data behind the endpoints. It is declared here
// rather than in the store so handlers can be exercised without a database,
// the same seam /healthz uses for its database ping.
type Reader interface {
	// TimeSeries returns the series for metric over [from, to) at resolution.
	// asset narrows the series to a single asset and is only meaningful for
	// MetricAssetSupply; ParseTimeSeriesRequest rejects it on every other
	// metric before a call ever reaches here, so implementations may ignore it
	// for anything else.
	TimeSeries(ctx context.Context, metric Metric, resolution Resolution, from, to time.Time, asset *AssetFilter) ([]TimeSeriesPoint, error)
	TopN(ctx context.Context, metric TopMetric, since, until time.Time, limit int) ([]TopEntry, error)
}

// Handler serves the analytics endpoints.
type Handler struct {
	reader Reader
	// allowedOrigins is the CORS allow-list; AllowAllOrigins opens it to any
	// browser. Empty disables cross-origin access entirely.
	allowedOrigins []string
	// now resolves the end of a Top-N rolling window. Kept as a field so tests
	// can freeze it.
	now func() time.Time
	// queryTimeout bounds a single request's database work. Kept as a field so
	// tests can shorten it.
	queryTimeout time.Duration
}

// NewHandler builds a Handler reading from the given source and answering
// cross-origin requests from allowedOrigins.
func NewHandler(reader Reader, allowedOrigins []string) *Handler {
	return &Handler{
		reader:         reader,
		allowedOrigins: allowedOrigins,
		now:            time.Now,
		queryTimeout:   queryTimeout,
	}
}

// Register mounts the analytics routes on mux.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.Handle(timeSeriesPath, h.withCORS(http.HandlerFunc(h.handleTimeSeries)))
	mux.Handle(topPath, h.withCORS(http.HandlerFunc(h.handleTop)))
	mux.HandleFunc(preflightPath, h.handlePreflight)
}

// errorResponse is the body returned for a rejected or failed request.
type errorResponse struct {
	Error string `json:"error"`
}

func (h *Handler) handleTimeSeries(w http.ResponseWriter, r *http.Request) {
	req, err := ParseTimeSeriesRequest(r.URL.Query())
	if err != nil {
		writeError(w, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), h.queryTimeout)
	defer cancel()

	points, err := h.reader.TimeSeries(ctx, req.Metric, req.Resolution, req.From, req.To, req.Asset)
	if err != nil {
		writeError(w, err)
		return
	}

	resp := TimeSeriesResponse{
		Metric:     req.Metric,
		Resolution: req.Resolution,
		From:       req.From,
		To:         req.To,
		Data:       points,
	}
	// Only set when the request carried a filter, so an unfiltered request's
	// response keeps the exact frozen shape (no empty "asset":"" field).
	if req.Asset != nil {
		resp.Asset = req.Asset.ID()
	}
	writeJSON(w, resp)
}

func (h *Handler) handleTop(w http.ResponseWriter, r *http.Request) {
	req, err := ParseTopRequest(r.URL.Query())
	if err != nil {
		writeError(w, err)
		return
	}

	// The window is closed at both ends so a row timestamped ahead of the
	// server clock cannot leak into a "last 24 hours" ranking.
	until := h.now().UTC()
	since := until.Add(-req.Window.Duration())

	ctx, cancel := context.WithTimeout(r.Context(), h.queryTimeout)
	defer cancel()

	entries, err := h.reader.TopN(ctx, req.Metric, since, until, req.Limit)
	if err != nil {
		writeError(w, err)
		return
	}

	writeJSON(w, TopResponse{
		Metric: req.Metric,
		Window: req.Window,
		Data:   entries,
	})
}

// writeJSON sends a successful response. Nil slices are normalised to empty
// ones first: the explorer inspects the length of data to decide whether a
// metric is available yet, and a null there would fail rather than render the
// empty state.
func writeJSON(w http.ResponseWriter, payload any) {
	switch p := payload.(type) {
	case TimeSeriesResponse:
		if p.Data == nil {
			p.Data = []TimeSeriesPoint{}
		}
		payload = p
	case TopResponse:
		if p.Data == nil {
			p.Data = []TopEntry{}
		}
		payload = p
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		// The status line is already sent, so this can only be logged.
		log.Printf("analytics: write response: %v", err)
	}
}

// writeError maps a failure to a status code. Invalid input is echoed back so
// the caller can fix it; anything else is reported generically and logged
// server-side, since these endpoints may be reachable without authentication
// and the underlying error can name internal infrastructure.
func writeError(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")

	status, message := http.StatusInternalServerError, "internal error"
	if errors.Is(err, ErrInvalidParam) {
		status, message = http.StatusBadRequest, err.Error()
	} else {
		log.Printf("analytics: request failed: %v", err)
	}

	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(errorResponse{Error: message}); err != nil {
		log.Printf("analytics: write error response: %v", err)
	}
}
