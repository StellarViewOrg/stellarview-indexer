package analytics

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestParseMetricAcceptsEverySupportedMetric(t *testing.T) {
	for _, want := range AllMetrics {
		got, err := ParseMetric(string(want))
		if err != nil {
			t.Errorf("ParseMetric(%q) returned error: %v", want, err)
		}
		if got != want {
			t.Errorf("ParseMetric(%q) = %q", want, got)
		}
	}
}

func TestParseMetricRejectsUnknownValues(t *testing.T) {
	for _, raw := range []string{"", "tx_counts", "TX_COUNT", "contract_activity", " tx_count"} {
		if _, err := ParseMetric(raw); !errors.Is(err, ErrInvalidParam) {
			t.Errorf("ParseMetric(%q) error = %v, want ErrInvalidParam", raw, err)
		}
	}
}

func TestParseTopMetricAcceptsEverySupportedMetric(t *testing.T) {
	for _, want := range AllTopMetrics {
		got, err := ParseTopMetric(string(want))
		if err != nil {
			t.Errorf("ParseTopMetric(%q) returned error: %v", want, err)
		}
		if got != want {
			t.Errorf("ParseTopMetric(%q) = %q", want, got)
		}
	}
}

func TestParseTopMetricRejectsTimeSeriesMetrics(t *testing.T) {
	// The two endpoints have disjoint metric sets; mixing them is a client bug
	// worth reporting rather than silently accepting.
	if _, err := ParseTopMetric(string(MetricTxCount)); !errors.Is(err, ErrInvalidParam) {
		t.Errorf("ParseTopMetric(%q) error = %v, want ErrInvalidParam", MetricTxCount, err)
	}
}

func TestParseResolutionAndWindow(t *testing.T) {
	for _, want := range AllResolutions {
		got, err := ParseResolution(string(want))
		if err != nil || got != want {
			t.Errorf("ParseResolution(%q) = (%q, %v)", want, got, err)
		}
	}
	if _, err := ParseResolution("minutely"); !errors.Is(err, ErrInvalidParam) {
		t.Errorf("ParseResolution(\"minutely\") error = %v, want ErrInvalidParam", err)
	}

	for _, want := range AllWindows {
		got, err := ParseWindow(string(want))
		if err != nil || got != want {
			t.Errorf("ParseWindow(%q) = (%q, %v)", want, got, err)
		}
	}
	if _, err := ParseWindow("90d"); !errors.Is(err, ErrInvalidParam) {
		t.Errorf("ParseWindow(\"90d\") error = %v, want ErrInvalidParam", err)
	}
}

func TestEveryResolutionHasABucketInterval(t *testing.T) {
	for _, r := range AllResolutions {
		if r.BucketInterval() == "" {
			t.Errorf("resolution %q has no bucket interval", r)
		}
	}
}

func TestWindowDurations(t *testing.T) {
	want := map[Window]time.Duration{
		Window24h: 24 * time.Hour,
		Window7d:  168 * time.Hour,
		Window30d: 720 * time.Hour,
	}
	for w, d := range want {
		if got := w.Duration(); got != d {
			t.Errorf("Window(%q).Duration() = %v, want %v", w, got, d)
		}
	}
}

// TestTimeSeriesResponseJSONMatchesFrozenContract pins the wire format the
// explorer client parses. Renaming a field here silently breaks its dashboards,
// so the exact JSON is asserted rather than the Go struct.
func TestTimeSeriesResponseJSONMatchesFrozenContract(t *testing.T) {
	resp := TimeSeriesResponse{
		Metric:     MetricTxCount,
		Resolution: ResolutionHourly,
		From:       time.Date(2026, 8, 20, 19, 0, 0, 0, time.UTC),
		To:         time.Date(2026, 8, 20, 21, 0, 0, 0, time.UTC),
		Data: []TimeSeriesPoint{
			{Timestamp: time.Date(2026, 8, 20, 19, 0, 0, 0, time.UTC), Value: 42},
		},
	}

	got, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	const want = `{"metric":"tx_count","resolution":"hourly",` +
		`"from":"2026-08-20T19:00:00Z","to":"2026-08-20T21:00:00Z",` +
		`"data":[{"timestamp":"2026-08-20T19:00:00Z","value":42}]}`

	if string(got) != want {
		t.Errorf("time-series JSON mismatch:\n got: %s\nwant: %s", got, want)
	}
}

// TestTopResponseJSONMatchesFrozenContract pins the Top-N wire format, including
// that metadata disappears when empty rather than serialising as null.
func TestTopResponseJSONMatchesFrozenContract(t *testing.T) {
	resp := TopResponse{
		Metric: TopAssetTransfers,
		Window: Window24h,
		Data: []TopEntry{
			{ID: "USDC-GA5Z", Label: "USDC", Value: 1500.5, Metadata: map[string]any{"issuer": "GA5Z"}},
			{ID: "native", Label: "XLM", Value: 12},
		},
	}

	got, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	const want = `{"metric":"asset_transfers","window":"24h","data":[` +
		`{"id":"USDC-GA5Z","label":"USDC","value":1500.5,"metadata":{"issuer":"GA5Z"}},` +
		`{"id":"native","label":"XLM","value":12}]}`

	if string(got) != want {
		t.Errorf("top-N JSON mismatch:\n got: %s\nwant: %s", got, want)
	}
}

// TestEmptyDataSerialisesAsArray guards the explorer's "not available yet"
// path: it checks data.length, so a null would throw instead of rendering the
// empty state.
func TestEmptyDataSerialisesAsArray(t *testing.T) {
	series, err := json.Marshal(TimeSeriesResponse{Data: []TimeSeriesPoint{}})
	if err != nil {
		t.Fatalf("marshal series: %v", err)
	}
	if !strings.Contains(string(series), `"data":[]`) {
		t.Errorf("empty time series must serialise data as [], got %s", series)
	}

	top, err := json.Marshal(TopResponse{Data: []TopEntry{}})
	if err != nil {
		t.Fatalf("marshal top: %v", err)
	}
	if !strings.Contains(string(top), `"data":[]`) {
		t.Errorf("empty top-N must serialise data as [], got %s", top)
	}
}
