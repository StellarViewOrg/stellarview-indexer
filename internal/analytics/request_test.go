package analytics

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeReader records what the handler asked for and replays a canned answer.
type fakeReader struct {
	points  []TimeSeriesPoint
	entries []TopEntry
	err     error

	gotMetric     Metric
	gotResolution Resolution
	gotFrom       time.Time
	gotTo         time.Time
	gotAsset      *AssetFilter
	gotTopMetric  TopMetric
	gotSince      time.Time
	gotUntil      time.Time
	gotLimit      int
}

func (f *fakeReader) TimeSeries(_ context.Context, metric Metric, resolution Resolution, from, to time.Time, asset *AssetFilter) ([]TimeSeriesPoint, error) {
	f.gotMetric, f.gotResolution, f.gotFrom, f.gotTo, f.gotAsset = metric, resolution, from, to, asset
	return f.points, f.err
}

func (f *fakeReader) TopN(_ context.Context, metric TopMetric, since, until time.Time, limit int) ([]TopEntry, error) {
	f.gotTopMetric, f.gotSince, f.gotUntil, f.gotLimit = metric, since, until, limit
	return f.entries, f.err
}

// frozenNow keeps the rolling Top-N window deterministic.
var frozenNow = time.Date(2026, 8, 20, 23, 0, 0, 0, time.UTC)

func newTestHandler(reader Reader) *Handler {
	h := NewHandler(reader, []string{AllowAllOrigins})
	h.now = func() time.Time { return frozenNow }
	return h
}

func serve(t *testing.T, reader Reader, target string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	newTestHandler(reader).Register(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func TestTimeSeriesEndpointReturnsTheSeries(t *testing.T) {
	reader := &fakeReader{points: []TimeSeriesPoint{
		{Timestamp: time.Date(2026, 8, 20, 19, 0, 0, 0, time.UTC), Value: 42},
	}}

	rec := serve(t, reader,
		"/api/v1/analytics/timeseries?metric=tx_count&resolution=hourly&from=2026-08-20T19:00:00Z&to=2026-08-20T21:00:00Z")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var got TimeSeriesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Metric != MetricTxCount || got.Resolution != ResolutionHourly || len(got.Data) != 1 {
		t.Errorf("unexpected response: %+v", got)
	}

	// The parsed request must reach the reader unchanged.
	if reader.gotMetric != MetricTxCount || reader.gotResolution != ResolutionHourly {
		t.Errorf("reader saw (%s, %s)", reader.gotMetric, reader.gotResolution)
	}
	if !reader.gotFrom.Equal(time.Date(2026, 8, 20, 19, 0, 0, 0, time.UTC)) {
		t.Errorf("reader saw from = %s", reader.gotFrom)
	}
	if reader.gotAsset != nil {
		t.Errorf("reader saw asset = %+v, want nil for an unfiltered request", reader.gotAsset)
	}
	if strings.Contains(rec.Body.String(), `"asset"`) {
		t.Errorf("unfiltered response must omit the asset field, got %s", rec.Body)
	}
}

// TestTimeSeriesEndpointReportsNoDataAsAnEmptySeries is the explorer's
// "not available yet" path: a metric with nothing aggregated is a successful
// response carrying an empty array, never an error status.
func TestTimeSeriesEndpointReportsNoDataAsAnEmptySeries(t *testing.T) {
	rec := serve(t, &fakeReader{},
		"/api/v1/analytics/timeseries?metric=asset_supply&resolution=daily&from=2026-08-01T00:00:00Z&to=2026-08-20T00:00:00Z")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"data":[]`) {
		t.Errorf("body must carry an empty array, got %s", rec.Body)
	}
}

func TestTimeSeriesEndpointRejectsBadParameters(t *testing.T) {
	targets := []string{
		"/api/v1/analytics/timeseries",
		"/api/v1/analytics/timeseries?metric=nope&resolution=hourly&from=2026-08-20T19:00:00Z&to=2026-08-20T21:00:00Z",
		"/api/v1/analytics/timeseries?metric=tx_count&resolution=hourly&from=nonsense&to=2026-08-20T21:00:00Z",
		"/api/v1/analytics/timeseries?metric=tx_count&resolution=hourly&from=2026-08-20T21:00:00Z&to=2026-08-20T19:00:00Z",
	}

	for _, target := range targets {
		rec := serve(t, &fakeReader{}, target)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", target, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), `"error"`) {
			t.Errorf("%s: body should explain the problem, got %s", target, rec.Body)
		}
	}
}

// A backend failure must not hand internal detail to an unauthenticated
// caller, mirroring how /healthz keeps database errors server-side.
func TestTimeSeriesEndpointHidesBackendErrors(t *testing.T) {
	reader := &fakeReader{err: errors.New(`pq: relation "analytics_tx_hourly" does not exist`)}

	rec := serve(t, reader,
		"/api/v1/analytics/timeseries?metric=tx_count&resolution=hourly&from=2026-08-20T19:00:00Z&to=2026-08-20T21:00:00Z")

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "analytics_tx_hourly") || strings.Contains(rec.Body.String(), "pq:") {
		t.Errorf("internal error leaked into the response: %s", rec.Body)
	}
}

func TestTopEndpointDerivesTheWindowFromTheClock(t *testing.T) {
	reader := &fakeReader{entries: []TopEntry{{ID: "C1", Label: "C1", Value: 3}}}

	rec := serve(t, reader, "/api/v1/analytics/top?metric=contract_activity&window=7d")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body)
	}

	wantSince := frozenNow.Add(-7 * 24 * time.Hour)
	if !reader.gotSince.Equal(wantSince) {
		t.Errorf("reader saw since = %s, want %s", reader.gotSince, wantSince)
	}
	// The window closes at the clock, so a row dated ahead of the server cannot
	// appear in a rolling ranking.
	if !reader.gotUntil.Equal(frozenNow) {
		t.Errorf("reader saw until = %s, want %s", reader.gotUntil, frozenNow)
	}
	if reader.gotLimit != defaultTopLimit {
		t.Errorf("reader saw limit = %d, want the default %d", reader.gotLimit, defaultTopLimit)
	}

	var got TopResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Metric != TopContractActivity || got.Window != Window7d {
		t.Errorf("unexpected envelope: %+v", got)
	}
}

func TestTopEndpointReportsNoDataAsAnEmptyRanking(t *testing.T) {
	rec := serve(t, &fakeReader{}, "/api/v1/analytics/top?metric=highest_fees&window=24h")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"data":[]`) {
		t.Errorf("body must carry an empty array, got %s", rec.Body)
	}
}

func TestTopEndpointRejectsBadParameters(t *testing.T) {
	targets := []string{
		"/api/v1/analytics/top",
		"/api/v1/analytics/top?metric=tx_count&window=24h",
		"/api/v1/analytics/top?metric=highest_fees&window=90d",
		"/api/v1/analytics/top?metric=highest_fees&window=24h&limit=0",
	}

	for _, target := range targets {
		if rec := serve(t, &fakeReader{}, target); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", target, rec.Code)
		}
	}
}

func TestEndpointsRejectNonGetMethods(t *testing.T) {
	mux := http.NewServeMux()
	newTestHandler(&fakeReader{}).Register(mux)

	for _, path := range []string{"/api/v1/analytics/timeseries", "/api/v1/analytics/top"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("POST %s: status = %d, want 405", path, rec.Code)
		}
	}
}

// blockingReader stalls until its context is cancelled, standing in for a query
// that outlives its welcome.
type blockingReader struct{}

func (blockingReader) TimeSeries(ctx context.Context, _ Metric, _ Resolution, _, _ time.Time, _ *AssetFilter) ([]TimeSeriesPoint, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (blockingReader) TopN(ctx context.Context, _ TopMetric, _, _ time.Time, _ int) ([]TopEntry, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// A request must not hold a database connection indefinitely. The server's
// write timeout closes the client connection but leaves the statement running
// and its pool slot held, so the handler has to bound the work itself.
func TestSlowQueriesAreCutOffByTheirDeadline(t *testing.T) {
	h := NewHandler(blockingReader{}, []string{AllowAllOrigins})
	h.now = func() time.Time { return frozenNow }
	h.queryTimeout = 50 * time.Millisecond

	mux := http.NewServeMux()
	h.Register(mux)

	for _, target := range []string{
		"/api/v1/analytics/timeseries?metric=tx_count&resolution=hourly&from=2026-08-20T19:00:00Z&to=2026-08-20T21:00:00Z",
		"/api/v1/analytics/top?metric=highest_fees&window=24h",
	} {
		done := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
			done <- rec
		}()

		select {
		case rec := <-done:
			if rec.Code != http.StatusInternalServerError {
				t.Errorf("%s: status = %d, want 500 once the deadline passes", target, rec.Code)
			}
		case <-time.After(5 * time.Second):
			t.Errorf("%s: handler never returned — the query is not bounded", target)
		}
	}
}

func TestTimeSeriesEndpointPlumbsTheAssetFilterThrough(t *testing.T) {
	reader := &fakeReader{points: []TimeSeriesPoint{
		{Timestamp: time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC), Value: 7},
	}}

	rec := serve(t, reader,
		"/api/v1/analytics/timeseries?metric=asset_supply&resolution=daily&from=2026-08-01T00:00:00Z&to=2026-08-20T00:00:00Z&asset=native")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body)
	}
	if reader.gotAsset == nil || !reader.gotAsset.Native {
		t.Errorf("reader saw asset = %+v, want native", reader.gotAsset)
	}
	if !strings.Contains(rec.Body.String(), `"asset":"native"`) {
		t.Errorf("response must echo the asset filter, got %s", rec.Body)
	}
}

func TestTimeSeriesEndpointRejectsAssetOnUnsupportedMetrics(t *testing.T) {
	rec := serve(t, &fakeReader{},
		"/api/v1/analytics/timeseries?metric=tx_count&resolution=hourly&from=2026-08-20T19:00:00Z&to=2026-08-20T21:00:00Z&asset=native")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}