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
	// MetricTxCount counts transactions per bucket.
	MetricTxCount Metric = "tx_count"
	// MetricTxVolume totals native (XLM) transferred per bucket.
	MetricTxVolume Metric = "tx_volume"
	// MetricFeeClassic totals fees charged on non-Soroban transactions, in stroops.
	MetricFeeClassic Metric = "fee_classic"
	// MetricFeeSoroban totals the fee charged on Soroban transactions, in
	// stroops. This is the whole fee, not the Soroban resource fee: the
	// indexer does not record the resource component, so it cannot be
	// separated from the inclusion fee. Charting this as a resource fee
	// overstates it by the inclusion fee on every transaction.
	MetricFeeSoroban Metric = "fee_soroban"
	// MetricActiveAccounts counts distinct transaction source accounts per bucket.
	MetricActiveAccounts Metric = "active_accounts"
	// MetricNewAccounts counts create_account operations per bucket, including
	// those in transactions that failed — the operations are recorded either
	// way and an aggregate cannot join them to the transaction's status. Do
	// not present it as accounts successfully created.
	MetricNewAccounts Metric = "new_accounts"
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
	// TopContractActivity ranks contracts by events emitted, which is the
	// only per-contract activity signal the indexer records.
	TopContractActivity TopMetric = "contract_activity"
	// TopAssetTransfers ranks assets by transferred volume.
	TopAssetTransfers TopMetric = "asset_transfers"
	// TopHighestFees ranks individual transactions by fee charged.
	TopHighestFees TopMetric = "highest_fees"
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

// bucketIntervals maps each resolution to the PostgreSQL interval literal passed
// to time_bucket. Boundaries follow time_bucket's own origin — 2000-01-03 for
// buckets of a day or more, which puts weekly boundaries on a Monday — so a
// series derived from hourly rows lines up with one computed directly from the
// raw table.
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
	// Timestamp marks the start of the bucket, in UTC.
	Timestamp time.Time `json:"timestamp"`
	// Value is the aggregated value for the bucket.
	Value float64 `json:"value"`
}

// TimeSeriesResponse is the envelope returned by the time-series endpoint. Data
// is never null: a metric with nothing aggregated yet returns an empty slice,
// which the explorer renders as a "not available yet" state.
type TimeSeriesResponse struct {
	Metric Metric `json:"metric"`
	// Asset is set only when the request carried an asset filter (currently
	// only meaningful for asset_supply). Omitted entirely otherwise, so the
	// frozen unfiltered shape stays byte-for-byte unchanged for existing
	// clients — this field is additive, not a breaking change to the contract.
	Asset      string            `json:"asset,omitempty"`
	Resolution Resolution        `json:"resolution"`
	From       time.Time         `json:"from"`
	To         time.Time         `json:"to"`
	Data       []TimeSeriesPoint `json:"data"`
}

// TopEntry is one row of a Top-N ranking.
type TopEntry struct {
	// ID identifies the entity: a contract ID, a "CODE-ISSUER" asset key, or a
	// transaction hash.
	ID string `json:"id"`
	// Label is the human-readable form of ID.
	Label string `json:"label"`
	// Value is the ranking value: events emitted, transferred volume, or fee.
	Value float64 `json:"value"`
	// Metadata carries per-metric context and is omitted when empty.
	Metadata map[string]any `json:"metadata,omitempty"`
}

// TopResponse is the envelope returned by the Top-N endpoint. As with
// TimeSeriesResponse, Data is never null.
type TopResponse struct {
	Metric TopMetric  `json:"metric"`
	Window Window     `json:"window"`
	Data   []TopEntry `json:"data"`
}

// AssetFilter narrows a time series to a single asset. It is only meaningful
// for MetricAssetSupply — ParseTimeSeriesRequest rejects it on every other
// metric. A nil *AssetFilter means unfiltered (the frozen all-assets sum).
type AssetFilter struct {
	// Native is set for the native XLM asset.
	Native bool
	// Code and Issuer identify a classic (code-issuer) asset. Both are set
	// together or not at all.
	Code, Issuer string
	// ContractID identifies a pure Soroban token by its contract, for assets
	// never wrapped in a classic code/issuer pair.
	ContractID string
}

// ID renders the filter back into the identifier form: "native", "CODE-ISSUER",
// or a bare contract ID. This is the same shape TopEntry.ID uses for
// asset_transfers, so a client can round-trip an identifier from one endpoint
// into a filter on the other.
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

// strkeyLen is the fixed length of a StrKey-encoded Stellar account or
// contract address.
const strkeyLen = 56

// maxAssetCodeLen is the longest a Stellar asset code may be.
const maxAssetCodeLen = 12

// ParseAssetFilter validates a raw "asset" query parameter. An empty string is
// not an error — it means unfiltered, and returns a nil filter.
//
// Validation is shape-only: code charset and length, and address
// length/prefix/alphabet for issuers and contract IDs. It is not a checksum or
// existence check — decoding the StrKey checksum here would cost a decode for
// no benefit, since a checksum failure and a well-formed-but-nonexistent asset
// both simply produce an empty series, which is not an error for any other
// metric either.
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

// isAssetCode reports whether s is shaped like a Stellar asset code: 1 to 12
// alphanumeric characters. The protocol does not restrict case, so both are
// accepted here.
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

// isStrkeyShaped reports whether s has the length and alphabet of a StrKey
// address starting with prefix ('G' for an account, 'C' for a contract).
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