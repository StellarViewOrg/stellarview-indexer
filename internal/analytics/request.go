package analytics

import (
	"fmt"
	"net/url"
	"strconv"
	"time"
)

// maxSeriesPoints bounds how many buckets a single time-series request may span.
// Without a bound, an hourly series over a decade would ask the database for
// ~88k rows and serialize them all. Every realistic dashboard range stays far
// below this; requests beyond it are rejected rather than silently truncated.
const maxSeriesPoints = 10000

// Top-N result size bounds. defaultTopLimit matches the explorer client, which
// omits the parameter for a 10-row ranking.
const (
	defaultTopLimit = 10
	maxTopLimit     = 100
)

// TimeSeriesRequest is a validated /timeseries query.
type TimeSeriesRequest struct {
	Metric     Metric
	Resolution Resolution
	From       time.Time
	To         time.Time
	// Asset narrows MetricAssetSupply to a single asset. Nil means unfiltered.
	// Always nil for every other metric — ParseTimeSeriesRequest rejects an
	// asset parameter on any metric that isn't asset_supply.
	Asset *AssetFilter
}

// TopRequest is a validated /top query.
type TopRequest struct {
	Metric TopMetric
	Window Window
	Limit  int
}

// ParseTimeSeriesRequest validates the query parameters of a /timeseries
// request. Every returned error wraps ErrInvalidParam.
func ParseTimeSeriesRequest(q url.Values) (TimeSeriesRequest, error) {
	metric, err := ParseMetric(q.Get("metric"))
	if err != nil {
		return TimeSeriesRequest{}, err
	}

	resolution, err := ParseResolution(q.Get("resolution"))
	if err != nil {
		return TimeSeriesRequest{}, err
	}

	from, err := parseTimestamp("from", q.Get("from"))
	if err != nil {
		return TimeSeriesRequest{}, err
	}

	to, err := parseTimestamp("to", q.Get("to"))
	if err != nil {
		return TimeSeriesRequest{}, err
	}

	if !from.Before(to) {
		return TimeSeriesRequest{}, fmt.Errorf("%w: from (%s) must be before to (%s)",
			ErrInvalidParam, from.Format(time.RFC3339), to.Format(time.RFC3339))
	}

	if err := checkSeriesSize(resolution, from, to); err != nil {
		return TimeSeriesRequest{}, err
	}

	asset, err := ParseAssetFilter(q.Get("asset"))
	if err != nil {
		return TimeSeriesRequest{}, err
	}
	// The filter only has a column to match against on asset_supply's per-asset
	// aggregate. Accepting it elsewhere and silently ignoring it would look to
	// the caller like filtering happened when it didn't.
	if asset != nil && metric != MetricAssetSupply {
		return TimeSeriesRequest{}, fmt.Errorf("%w: asset is only valid with metric=%s", ErrInvalidParam, MetricAssetSupply)
	}

	return TimeSeriesRequest{
		Metric:     metric,
		Resolution: resolution,
		From:       from,
		To:         to,
		Asset:      asset,
	}, nil
}

// ParseTopRequest validates the query parameters of a /top request. The limit
// parameter is optional and defaults to defaultTopLimit.
func ParseTopRequest(q url.Values) (TopRequest, error) {
	metric, err := ParseTopMetric(q.Get("metric"))
	if err != nil {
		return TopRequest{}, err
	}

	window, err := ParseWindow(q.Get("window"))
	if err != nil {
		return TopRequest{}, err
	}

	limit, err := parseLimit(q.Get("limit"))
	if err != nil {
		return TopRequest{}, err
	}

	return TopRequest{Metric: metric, Window: window, Limit: limit}, nil
}

// parseTimestamp accepts an RFC 3339 timestamp and normalises it to UTC, so
// bucket boundaries are comparable regardless of the caller's offset.
func parseTimestamp(name, raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, fmt.Errorf("%w: %s is required (RFC 3339)", ErrInvalidParam, name)
	}
	ts, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: %s %q is not RFC 3339", ErrInvalidParam, name, raw)
	}
	return ts.UTC(), nil
}

// parseLimit validates the optional Top-N limit.
func parseLimit(raw string) (int, error) {
	if raw == "" {
		return defaultTopLimit, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%w: limit %q is not an integer", ErrInvalidParam, raw)
	}
	if limit < 1 || limit > maxTopLimit {
		return 0, fmt.Errorf("%w: limit %d out of range [1, %d]", ErrInvalidParam, limit, maxTopLimit)
	}
	return limit, nil
}

// checkSeriesSize rejects ranges that would produce more than maxSeriesPoints
// buckets at the requested resolution.
func checkSeriesSize(resolution Resolution, from, to time.Time) error {
	width := map[Resolution]time.Duration{
		ResolutionHourly: time.Hour,
		ResolutionDaily:  24 * time.Hour,
		ResolutionWeekly: 7 * 24 * time.Hour,
	}[resolution]

	if buckets := to.Sub(from) / width; buckets > maxSeriesPoints {
		return fmt.Errorf("%w: range spans %d %s buckets, limit is %d — widen the resolution or shorten the range",
			ErrInvalidParam, buckets, resolution, maxSeriesPoints)
	}
	return nil
}
