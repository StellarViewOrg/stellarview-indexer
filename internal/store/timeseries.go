package store

import (
	"context"
	"fmt"
	"time"

	"github.com/miguelnietoa/stellar-explorer/indexer/internal/analytics"
)

// stroopsPerXLM scales the native asset's base units into whole XLM.
const stroopsPerXLM = 1e7

// Asset type discriminators shared with token_events.asset_type, matching the
// CASE in topAssetTransfers: 0 native, 1 classic (code+issuer), 2 pure Soroban.
const (
	nativeAssetType  = 0
	classicAssetType = 1
)

// sorobanAssetType is the token_events.asset_type discriminator for a pure
// Soroban token, the only kind whose precision is contract-defined.
const sorobanAssetType = 2

// timeSeriesSource describes how one metric is read: the relation holding its
// hourly buckets, and the expression that turns those rows into a bucket value.
//
// Every metric is served through the same query template, which re-buckets the
// hourly aggregate to the requested resolution. For active_accounts the
// relation already matches the requested resolution, so the outer grouping sees
// exactly one row per bucket and the sum is an identity — the distinct count is
// never rolled up, which would overstate it.
type timeSeriesSource struct {
	relation string
	value    string
}

// hourlySources maps the metrics whose relation does not depend on resolution
// or on any per-request filter. asset_supply is built dynamically instead, by
// assetSupplySource, because an asset filter changes its WHERE clause.
var hourlySources = map[analytics.Metric]timeSeriesSource{
	analytics.MetricTxCount: {
		relation: "analytics_tx_hourly",
		value:    "SUM(tx_count)",
	},
	analytics.MetricFeeClassic: {
		relation: "analytics_tx_hourly",
		// A bucket with only Soroban traffic leaves the filtered sum NULL.
		value: "SUM(COALESCE(fee_classic, 0))",
	},
	analytics.MetricFeeSoroban: {
		relation: "analytics_tx_hourly",
		value:    "SUM(COALESCE(fee_soroban, 0))",
	},
	analytics.MetricNewAccounts: {
		relation: "analytics_new_accounts_hourly",
		value:    "SUM(new_accounts)",
	},
	analytics.MetricTxVolume: {
		relation: "analytics_volume_hourly",
		value:    fmt.Sprintf("SUM(stroops_transferred) / %.0f", stroopsPerXLM),
	},
}

// classicAssetDecimals is the precision the protocol fixes for native and
// classic credit assets. It is not read from anywhere, because for those assets
// it cannot vary.
const classicAssetDecimals = 7

// decimalsExpr builds the scaling exponent for an asset's base units, where
// assetAlias is the relation holding asset_type and contracts is joined as c.
//
// Only Soroban tokens carry their own precision. Every classic asset is wrapped
// by a Stellar Asset Contract, and if the SEP-41 fetcher ever records decimals
// for one of those contracts, reading it here would silently override the
// protocol's fixed 7 and misreport those amounts by orders of magnitude.
//
// The value is also clamped. contracts.token_decimals comes from an arbitrary
// contract's decimals() entry point and is stored unvalidated, so it can be
// absurd or negative — and used raw as an exponent it fails the whole query
// rather than one row: a large value overflows the numeric format and a very
// negative one underflows the divisor to zero.
func decimalsExpr(assetAlias string) string {
	return fmt.Sprintf(
		`GREATEST(0, LEAST(38, CASE WHEN %s.asset_type = %d THEN COALESCE(c.token_decimals, %d) ELSE %d END))`,
		assetAlias, sorobanAssetType, classicAssetDecimals, classicAssetDecimals)
}

// assetSupplySource builds the relation/value pair for asset_supply, optionally
// narrowed to a single asset by filter. Filtering happens inside the subquery,
// on the raw asset_type/asset_code/asset_issuer/asset_contract_id columns,
// before assets are scaled and summed — filtering the summed output could not
// tell one asset's contribution apart from another's.
//
// Bound parameter placeholders for the filter start at paramOffset, so the
// caller can place them after its own positional arguments ($1 interval, $2
// from, $3 to).
func assetSupplySource(filter *analytics.AssetFilter, paramOffset int) (timeSeriesSource, []any) {
	where := ""
	var args []any

	switch {
	case filter == nil:
		// Unfiltered: every asset contributes to the sum.
	case filter.Native:
		where = fmt.Sprintf("WHERE s.asset_type = %d", nativeAssetType)
	case filter.ContractID != "":
		where = fmt.Sprintf("WHERE s.asset_type = %d AND s.asset_contract_id = $%d",
			sorobanAssetType, paramOffset)
		args = []any{filter.ContractID}
	default:
		where = fmt.Sprintf("WHERE s.asset_type = %d AND s.asset_code = $%d AND s.asset_issuer = $%d",
			classicAssetType, paramOffset, paramOffset+1)
		args = []any{filter.Code, filter.Issuer}
	}

	relation := fmt.Sprintf(`(
		SELECT s.bucket,
		       s.net_supply_delta / (10::numeric ^ %s) AS units
		FROM analytics_asset_supply_hourly s
		LEFT JOIN contracts c ON c.contract_id = s.asset_contract_id
		%s
	) AS asset_supply`, decimalsExpr("s"), where)

	return timeSeriesSource{relation: relation, value: "SUM(units)"}, args
}

// activeAccountViews maps each resolution to its dedicated distinct-count
// aggregate.
var activeAccountViews = map[analytics.Resolution]string{
	analytics.ResolutionHourly: "analytics_active_accounts_hourly",
	analytics.ResolutionDaily:  "analytics_active_accounts_daily",
	analytics.ResolutionWeekly: "analytics_active_accounts_weekly",
}

// sourceFor resolves the relation and value expression for a metric at a
// resolution. It does not handle asset_supply — that has its own filter-aware
// path in assetSupplySource, called directly from TimeSeries.
func sourceFor(metric analytics.Metric, resolution analytics.Resolution) (timeSeriesSource, error) {
	if metric == analytics.MetricActiveAccounts {
		view, ok := activeAccountViews[resolution]
		if !ok {
			return timeSeriesSource{}, fmt.Errorf("no active_accounts aggregate for resolution %q", resolution)
		}
		return timeSeriesSource{relation: view, value: "SUM(active_accounts)"}, nil
	}

	source, ok := hourlySources[metric]
	if !ok {
		return timeSeriesSource{}, fmt.Errorf("unsupported metric %q", metric)
	}
	return source, nil
}

// TimeSeries returns one point per bucket for metric over [from, to) at the
// requested resolution. The series is every bucket that overlaps the range,
// each carrying its complete value, so the first and last points can extend a
// little beyond what was asked for rather than being sliced and reported as
// whole.
//
// Buckets with no activity are absent rather than zero, and a range with no
// data at all yields an empty series rather than an error.
//
// asset narrows the series to a single asset and is only meaningful for
// MetricAssetSupply. ParseTimeSeriesRequest rejects an asset filter on every
// other metric before it reaches here, so a non-nil asset alongside a
// different metric is simply ignored by sourceFor, which never looks at it.
func (s *PostgresStore) TimeSeries(
	ctx context.Context,
	metric analytics.Metric,
	resolution analytics.Resolution,
	from, to time.Time,
	asset *analytics.AssetFilter,
) ([]analytics.TimeSeriesPoint, error) {
	// $1 interval, $2 from, $3 to are always present; any filter arguments are
	// appended after them, starting here.
	const paramOffset = 4

	var (
		source timeSeriesSource
		extra  []any
		err    error
	)
	if metric == analytics.MetricAssetSupply {
		source, extra = assetSupplySource(asset, paramOffset)
	} else {
		source, err = sourceFor(metric, resolution)
		if err != nil {
			return nil, err
		}
	}

	// Both bounds are evaluated against whole buckets rather than the caller's
	// raw instants, so the series is the set of buckets overlapping the range
	// and each one carries its complete value.
	//
	// Comparing raw instants sliced the edges differently for each metric
	// family. At the leading edge an hourly-backed metric dropped the hours
	// before the instant and reported the remainder as a whole day, while
	// active_accounts — already bucketed at the requested resolution — dropped
	// the bucket entirely. At the trailing edge the reverse: the hourly-backed
	// metric reported a truncated day while active_accounts reported the full
	// one. The same request therefore returned series of different lengths
	// carrying inconsistent values.
	//
	// The relation and value expression come from the tables above, never from
	// request input; the caller-supplied values are all bound parameters.
	query := fmt.Sprintf(`
		SELECT time_bucket($1::interval, bucket) AS ts,
		       (%s)::double precision AS value
		FROM %s
		WHERE bucket >= time_bucket($1::interval, $2::timestamptz)
		  AND time_bucket($1::interval, bucket) < $3
		GROUP BY ts
		ORDER BY ts`, source.value, source.relation)

	args := append([]any{resolution.BucketInterval(), from, to}, extra...)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query %s time series: %w", metric, err)
	}
	defer rows.Close()

	points := make([]analytics.TimeSeriesPoint, 0)
	for rows.Next() {
		var p analytics.TimeSeriesPoint
		if err := rows.Scan(&p.Timestamp, &p.Value); err != nil {
			return nil, fmt.Errorf("scan %s bucket: %w", metric, err)
		}
		p.Timestamp = p.Timestamp.UTC()
		points = append(points, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read %s time series: %w", metric, err)
	}

	return points, nil
}