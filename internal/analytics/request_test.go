package analytics

import (
	"errors"
	"net/url"
	"testing"
	"time"
)

func timeSeriesQuery(overrides map[string]string) url.Values {
	q := url.Values{
		"metric":     {"tx_count"},
		"resolution": {"hourly"},
		"from":       {"2026-08-20T19:00:00Z"},
		"to":         {"2026-08-20T23:00:00Z"},
	}
	for k, v := range overrides {
		if v == "" {
			q.Del(k)
			continue
		}
		q.Set(k, v)
	}
	return q
}

func TestParseTimeSeriesRequestAcceptsAValidQuery(t *testing.T) {
	got, err := ParseTimeSeriesRequest(timeSeriesQuery(nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := TimeSeriesRequest{
		Metric:     MetricTxCount,
		Resolution: ResolutionHourly,
		From:       time.Date(2026, 8, 20, 19, 0, 0, 0, time.UTC),
		To:         time.Date(2026, 8, 20, 23, 0, 0, 0, time.UTC),
	}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestParseTimeSeriesRequestNormalisesTimestampsToUTC(t *testing.T) {
	q := timeSeriesQuery(map[string]string{"from": "2026-08-20T16:00:00-03:00"})

	got, err := ParseTimeSeriesRequest(q)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := time.Date(2026, 8, 20, 19, 0, 0, 0, time.UTC)
	if !got.From.Equal(want) || got.From.Location() != time.UTC {
		t.Errorf("From = %s (%v), want %s in UTC", got.From, got.From.Location(), want)
	}
}

func TestParseTimeSeriesRequestRejectsBadInput(t *testing.T) {
	tests := []struct {
		name      string
		overrides map[string]string
	}{
		{"missing metric", map[string]string{"metric": ""}},
		{"unknown metric", map[string]string{"metric": "tx_counts"}},
		{"missing resolution", map[string]string{"resolution": ""}},
		{"unknown resolution", map[string]string{"resolution": "minutely"}},
		{"missing from", map[string]string{"from": ""}},
		{"missing to", map[string]string{"to": ""}},
		{"from not RFC 3339", map[string]string{"from": "2026-08-20"}},
		{"to not RFC 3339", map[string]string{"to": "yesterday"}},
		{"from after to", map[string]string{"from": "2026-08-21T00:00:00Z", "to": "2026-08-20T00:00:00Z"}},
		{"from equal to to", map[string]string{"from": "2026-08-20T00:00:00Z", "to": "2026-08-20T00:00:00Z"}},
		{"range too wide for resolution", map[string]string{"from": "1990-01-01T00:00:00Z", "to": "2026-01-01T00:00:00Z"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseTimeSeriesRequest(timeSeriesQuery(tt.overrides)); !errors.Is(err, ErrInvalidParam) {
				t.Errorf("error = %v, want ErrInvalidParam", err)
			}
		})
	}
}

// A range too wide at one resolution can be perfectly reasonable at a coarser
// one, so the bucket cap must be evaluated per resolution rather than as a
// fixed span limit.
func TestSeriesSizeCapIsPerResolution(t *testing.T) {
	span := map[string]string{"from": "1990-01-01T00:00:00Z", "to": "2026-01-01T00:00:00Z"}

	if _, err := ParseTimeSeriesRequest(timeSeriesQuery(span)); err == nil {
		t.Error("36 years hourly should exceed the bucket cap")
	}

	span["resolution"] = "weekly"
	if _, err := ParseTimeSeriesRequest(timeSeriesQuery(span)); err != nil {
		t.Errorf("36 years weekly is only ~1.9k buckets and should be accepted, got %v", err)
	}
}

func TestParseTopRequestDefaultsTheLimit(t *testing.T) {
	got, err := ParseTopRequest(url.Values{
		"metric": {"contract_activity"},
		"window": {"24h"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := TopRequest{Metric: TopContractActivity, Window: Window24h, Limit: defaultTopLimit}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestParseTopRequestHonoursAnExplicitLimit(t *testing.T) {
	got, err := ParseTopRequest(url.Values{
		"metric": {"highest_fees"},
		"window": {"30d"},
		"limit":  {"25"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Limit != 25 {
		t.Errorf("Limit = %d, want 25", got.Limit)
	}
}

func TestParseTopRequestRejectsBadInput(t *testing.T) {
	tests := []struct {
		name  string
		query url.Values
	}{
		{"missing metric", url.Values{"window": {"24h"}}},
		{"time-series metric", url.Values{"metric": {"tx_count"}, "window": {"24h"}}},
		{"missing window", url.Values{"metric": {"highest_fees"}}},
		{"unknown window", url.Values{"metric": {"highest_fees"}, "window": {"90d"}}},
		{"limit not a number", url.Values{"metric": {"highest_fees"}, "window": {"24h"}, "limit": {"ten"}}},
		{"limit with trailing text", url.Values{"metric": {"highest_fees"}, "window": {"24h"}, "limit": {"10x"}}},
		{"limit zero", url.Values{"metric": {"highest_fees"}, "window": {"24h"}, "limit": {"0"}}},
		{"limit negative", url.Values{"metric": {"highest_fees"}, "window": {"24h"}, "limit": {"-5"}}},
		{"limit above maximum", url.Values{"metric": {"highest_fees"}, "window": {"24h"}, "limit": {"101"}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseTopRequest(tt.query); !errors.Is(err, ErrInvalidParam) {
				t.Errorf("error = %v, want ErrInvalidParam", err)
			}
		})
	}
}
