// Package analytics implements the network-wide analytics read API: time series
// for network metrics (transaction volume, fees, account activity, asset supply)
// and Top-N rankings of the most active entities.
//
// The request and response shapes in this file are a frozen contract. The
// explorer builds its dashboards against them, so field names, metric
// identifiers, and the empty-result behaviour must not change without
// coordinating with StellarViewOrg/stellarview-explorer. See docs/analytics-api.md.
package analytics

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrInvalidParam reports a query parameter that failed validation. Handlers
// translate it into a 400 response, distinguishing bad input from a genuine
// server-side failure.
var ErrInvalidParam = errors.New("invalid parameter")

// Metric identifies a network metric available as a time series.
type Metric string

const (
	MetricTxCount        Metric = "tx_count"
	MetricTxVolume       Metric = "tx_volume"
	MetricFeeClassic     Metric = "fee_classic"
	MetricFeeSoroban     Metric = "fee_soroban"
	MetricActiveAccounts Metric = "active_accounts"
	MetricNewAccounts    Metric = "new_accounts"
	// MetricAssetSupply totals net supply change (mints minus burns and
	// clawbacks). Unfiltered, it sums every asset. Pass an AssetFilter to
	// narrow it to one asset's net supply delta instead.
	MetricAssetSupply Metric = "asset_supply"
)

// AllMetrics lists every supported time-series metric, in the order they are
// documented.
var AllMetrics = []Metric{
	MetricTxCount,
	MetricTxVolume,
	MetricFeeClassic,
	MetricFeeSoroban,
	MetricActiveAccounts,
	MetricNewAccounts,
	MetricAssetSupply,
}

// TopMetric identifies a Top-N ranking.
type TopMetric string

const (
	TopContractActivity TopMetric = "contract_activity"
	TopAssetTransfers   TopMetric = "asset_transfers"
	TopHighestFees      TopMetric = "highest_fees"
)

// AllTopMetrics lists every supported Top-N metric.
var AllTopMetrics = []TopMetric{
	TopContractActivity,
	TopAssetTransfers,
	TopHighestFees,
}

// Resolution is the bucket width of a time series.
type Resolution string

const (
	ResolutionHourly Resolution = "hourly"
	ResolutionDaily  Resolution = "daily"
	ResolutionWeekly Resolution = "weekly"
)

// AllResolutions lists every supported resolution, coarsening left to right.
var AllResolutions = []Resolution{ResolutionHourly, ResolutionDaily, ResolutionWeekly}

var bucketIntervals = map[Resolution]string{
	ResolutionHourly: "1 hour",
	ResolutionDaily:  "1 day",
	ResolutionWeekly: "1 week",
}

// BucketInterval returns the PostgreSQL interval literal for this resolution.
func (r Resolution) BucketInterval() string {
	return bucketIntervals[r]
}

// Window is the rolling look-back period of a Top-N query.
type Window string

const (
	Window24h Window = "24h"
	Window7d  Window = "7d"
	Window30d Window = "30d"
)

// AllWindows lists every supported Top-N window.
var AllWindows = []Window{Window24h, Window7d, Window30d}

var windowDurations = map[Window]time.Duration{
	Window24h: 24 * time.Hour,
	Window7d:  7 * 24 * time.Hour,
	Window30d: 30 * 24 * time.Hour,
}

// Duration returns how far back this window reaches from the query time.
func (w Window) Duration() time.Duration {
	return windowDurations[w]
}

// TimeSeriesPoint is one bucket of a time series.
type TimeSeriesPoint struct {
	Timestamp time.Time `json:"timestamp"`
	Value     float64   `json:"value"`
}

// TimeSeriesResponse is the envelope returned by the time-series endpoint.
type TimeSeriesResponse struct {
	Metric Metric `json:"metric"`
	// Asset is set only when the request carried an asset filter. Omitted
	// entirely otherwise, so the frozen unfiltered shape stays byte-for-byte
	// unchanged for existing clients.
	Asset      string            `json:"asset,omitempty"`
	Resolution Resolution        `json:"resolution"`
	From       time.Time         `json:"from"`
	To         time.Time         `json:"to"`
	Data       []TimeSeriesPoint `json:"data"`
}

// TopEntry is one row of a Top-N ranking.
type TopEntry struct {
	ID       string         `json:"id"`
	Label    string         `json:"label"`
	Value    float64        `json:"value"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// TopResponse is the envelope returned by the Top-N endpoint.
type TopResponse struct {
	Metric TopMetric  `json:"metric"`
	Window Window     `json:"window"`
	Data   []TopEntry `json:"data"`
}

// AssetFilter narrows a time series to a single asset. It is only meaningful
// for MetricAssetSupply — ParseTimeSeriesRequest rejects it on every other
// metric. A nil *AssetFilter means unfiltered (the frozen all-assets sum).
type AssetFilter struct {
	Native       bool
	Code, Issuer string
	ContractID   string
}

// ID renders the filter back into the identifier form: "native", "CODE-ISSUER",
// or a bare contract ID — the same shape TopEntry.ID uses for asset_transfers.
func (f AssetFilter) ID() string {
	switch {
	case f.Native:
		return "native"
	case f.Issuer != "":
		return f.Code + "-" + f.Issuer
	default:
		return f.ContractID
	}
}

const strkeyLen = 56
const maxAssetCodeLen = 12

// ParseAssetFilter validates a raw "asset" query parameter. An empty string
// means unfiltered and returns a nil filter. Validation is shape-only, not a
// checksum or existence check — a well-formed but never-seen asset simply
// yields an empty series, same as any other metric with no data.
func ParseAssetFilter(raw string) (*AssetFilter, error) {
	if raw == "" {
		return nil, nil
	}
	if raw == "native" {
		return &AssetFilter{Native: true}, nil
	}
	if code, issuer, ok := strings.Cut(raw, "-"); ok && isAssetCode(code) && isStrkeyShaped(issuer, 'G') {
		return &AssetFilter{Code: code, Issuer: issuer}, nil
	}
	if isStrkeyShaped(raw, 'C') {
		return &AssetFilter{ContractID: raw}, nil
	}
	return nil, fmt.Errorf(`%w: asset %q, want "native", "CODE-ISSUER", or a contract ID`, ErrInvalidParam, raw)
}

func isAssetCode(s string) bool {
	if len(s) == 0 || len(s) > maxAssetCodeLen {
		return false
	}
	for _, r := range s {
		if !(r >= 'A' && r <= 'Z') && !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func isStrkeyShaped(s string, prefix byte) bool {
	if len(s) != strkeyLen || s[0] != prefix {
		return false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		if !(c >= 'A' && c <= 'Z') && !(c >= '2' && c <= '7') {
			return false
		}
	}
	return true
}

// ParseMetric validates a raw metric parameter against AllMetrics.
func ParseMetric(raw string) (Metric, error) {
	for _, m := range AllMetrics {
		if Metric(raw) == m {
			return m, nil
		}
	}
	return "", fmt.Errorf("%w: metric %q, want one of %v", ErrInvalidParam, raw, AllMetrics)
}

// ParseTopMetric validates a raw metric parameter against AllTopMetrics.
func ParseTopMetric(raw string) (TopMetric, error) {
	for _, m := range AllTopMetrics {
		if TopMetric(raw) == m {
			return m, nil
		}
	}
	return "", fmt.Errorf("%w: metric %q, want one of %v", ErrInvalidParam, raw, AllTopMetrics)
}

// ParseResolution validates a raw resolution parameter.
func ParseResolution(raw string) (Resolution, error) {
	for _, r := range AllResolutions {
		if Resolution(raw) == r {
			return r, nil
		}
	}
	return "", fmt.Errorf("%w: resolution %q, want one of %v", ErrInvalidParam, raw, AllResolutions)
}

// ParseWindow validates a raw window parameter.
func ParseWindow(raw string) (Window, error) {
	for _, w := range AllWindows {
		if Window(raw) == w {
			return w, nil
		}
	}
	return "", fmt.Errorf("%w: window %q, want one of %v", ErrInvalidParam, raw, AllWindows)
}
