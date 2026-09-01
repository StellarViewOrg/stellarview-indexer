package store

import (
	"context"
	"testing"
	"time"

	"github.com/miguelnietoa/stellar-explorer/indexer/internal/analytics"
)

// pointsByBucket indexes a series by bucket start so assertions can name the
// hour they are checking instead of relying on slice positions.
func pointsByBucket(points []analytics.TimeSeriesPoint) map[time.Time]float64 {
	byBucket := make(map[time.Time]float64, len(points))
	for _, p := range points {
		byBucket[p.Timestamp.UTC()] = p.Value
	}
	return byBucket
}

// TestTimeSeriesMatchesManualRecomputation checks every metric against
// hand-computed values for a known range, which is acceptance criterion #1.
func TestTimeSeriesMatchesManualRecomputation(t *testing.T) {
	store := getTestDB(t)
	defer store.Close()

	from, to, cleanup := insertAnalyticsFixture(t, store)
	defer cleanup()

	hour0 := fixtureBase
	hour1 := fixtureBase.Add(time.Hour)
	want := fixtureExpectations

	tests := []struct {
		metric analytics.Metric
		hour0  float64
		hour1  float64
		// hour1Absent marks metrics with no activity in the second hour, which
		// must produce no bucket at all rather than a zero.
		hour1Absent bool
	}{
		{metric: analytics.MetricTxCount, hour0: want.TxCountHour0, hour1: want.TxCountHour1},
		{metric: analytics.MetricFeeClassic, hour0: want.FeeClassicHour0, hour1: want.FeeClassicHour1},
		{metric: analytics.MetricFeeSoroban, hour0: want.FeeSorobanHour0, hour1: want.FeeSorobanHour1},
		{metric: analytics.MetricActiveAccounts, hour0: want.ActiveHour0, hour1: want.ActiveHour1},
		{metric: analytics.MetricNewAccounts, hour0: want.NewAccountsHour0, hour1: want.NewAccountsHour1},
		{metric: analytics.MetricTxVolume, hour0: want.VolumeXLMHour0, hour1: want.VolumeXLMHour1},
		{metric: analytics.MetricAssetSupply, hour0: want.SupplyHour0, hour1Absent: true},
	}

	for _, tt := range tests {
		t.Run(string(tt.metric), func(t *testing.T) {
			points, err := store.TimeSeries(context.Background(), tt.metric, analytics.ResolutionHourly, from, to, nil)
			if err != nil {
				t.Fatalf("TimeSeries: %v", err)
			}

			byBucket := pointsByBucket(points)
			if got := byBucket[hour0]; got != tt.hour0 {
				t.Errorf("hour 0 = %v, want %v (series: %+v)", got, tt.hour0, points)
			}

			if tt.hour1Absent {
				if _, ok := byBucket[hour1]; ok {
					t.Errorf("hour 1 should have no bucket, got %v", byBucket[hour1])
				}
				return
			}
			if got := byBucket[hour1]; got != tt.hour1 {
				t.Errorf("hour 1 = %v, want %v (series: %+v)", got, tt.hour1, points)
			}
		})
	}
}

// TestDailyRollupSumsTheHourlyBuckets covers the derived resolutions: an
// additive metric re-bucketed to a day must equal the sum of its hours.
func TestDailyRollupSumsTheHourlyBuckets(t *testing.T) {
	store := getTestDB(t)
	defer store.Close()

	from, to, cleanup := insertAnalyticsFixture(t, store)
	defer cleanup()

	daily, err := store.TimeSeries(context.Background(), analytics.MetricTxCount, analytics.ResolutionDaily, from, to, nil)
	if err != nil {
		t.Fatalf("TimeSeries daily: %v", err)
	}

	want := fixtureExpectations.TxCountHour0 + fixtureExpectations.TxCountHour1
	got := pointsByBucket(daily)[fixtureBase]
	if got != want {
		t.Errorf("daily tx_count = %v, want %v (series: %+v)", got, want, daily)
	}
}

// TestActiveAccountsDailyIsNotASumOfHours is the reason active_accounts has a
// dedicated aggregate per resolution. Accounts A, A, B transact in the first
// hour and B, C in the second: two distinct accounts per hour, but three across
// the day. A summed rollup would report four.
func TestActiveAccountsDailyIsNotASumOfHours(t *testing.T) {
	store := getTestDB(t)
	defer store.Close()

	from, to, cleanup := insertAnalyticsFixture(t, store)
	defer cleanup()

	daily, err := store.TimeSeries(context.Background(), analytics.MetricActiveAccounts, analytics.ResolutionDaily, from, to, nil)
	if err != nil {
		t.Fatalf("TimeSeries daily: %v", err)
	}

	got := pointsByBucket(daily)[fixtureBase]
	sumOfHours := fixtureExpectations.ActiveHour0 + fixtureExpectations.ActiveHour1

	if got != fixtureExpectations.ActiveDay {
		t.Errorf("daily active_accounts = %v, want %v", got, fixtureExpectations.ActiveDay)
	}
	if got == sumOfHours {
		t.Errorf("daily active_accounts (%v) equals the sum of hourly buckets — "+
			"the distinct count is being rolled up instead of recomputed", got)
	}
}

// TestWeeklyResolutionIsServedForEveryMetric covers acceptance criterion #2:
// every metric answers at every resolution in a single request.
func TestWeeklyResolutionIsServedForEveryMetric(t *testing.T) {
	store := getTestDB(t)
	defer store.Close()

	from, to, cleanup := insertAnalyticsFixture(t, store)
	defer cleanup()

	for _, metric := range analytics.AllMetrics {
		for _, resolution := range analytics.AllResolutions {
			if _, err := store.TimeSeries(context.Background(), metric, resolution, from, to, nil); err != nil {
				t.Errorf("TimeSeries(%s, %s): %v", metric, resolution, err)
			}
		}
	}
}

// TestTimeSeriesReturnsEmptySeriesForAQuietRange guards the explorer's
// "not available yet" path: no data is an empty series, never an error.
func TestTimeSeriesReturnsEmptySeriesForAQuietRange(t *testing.T) {
	store := getTestDB(t)
	defer store.Close()

	quietFrom := time.Date(2031, 6, 1, 0, 0, 0, 0, time.UTC)
	quietTo := quietFrom.Add(24 * time.Hour)

	for _, metric := range analytics.AllMetrics {
		points, err := store.TimeSeries(context.Background(), metric, analytics.ResolutionHourly, quietFrom, quietTo, nil)
		if err != nil {
			t.Errorf("TimeSeries(%s): %v", metric, err)
			continue
		}
		if len(points) != 0 {
			t.Errorf("TimeSeries(%s) returned %d points for a quiet range", metric, len(points))
		}
	}
}

// TestRefreshSkipsAggregatesWiderThanTheWindow covers the backfill path for
// short ranges. A window narrower than a week contains no complete weekly
// bucket, which TimescaleDB reports as an error rather than a no-op, so the
// refresh must recognise the case and skip that aggregate instead of failing.
func TestRefreshSkipsAggregatesWiderThanTheWindow(t *testing.T) {
	store := getTestDB(t)
	defer store.Close()

	from := fixtureBase.Add(-time.Hour)
	to := fixtureBase.Add(48 * time.Hour)

	results, err := store.RefreshAnalyticsAggregates(context.Background(), from, to)
	if err != nil {
		t.Fatalf("RefreshAnalyticsAggregates: %v", err)
	}
	if len(results) != len(analyticsAggregates) {
		t.Fatalf("got %d results, want one per aggregate (%d)", len(results), len(analyticsAggregates))
	}

	skipped := make(map[string]bool)
	for _, r := range results {
		skipped[r.Aggregate] = r.Skipped
	}

	if !skipped["analytics_active_accounts_weekly"] {
		t.Error("a two-day window holds no complete weekly bucket, so the weekly aggregate should be skipped")
	}
	if skipped["analytics_tx_hourly"] {
		t.Error("hourly aggregates fit comfortably in a two-day window and must not be skipped")
	}
}

// TestRefreshWithOpenStartSucceeds covers the backfill invocation that leaves
// the start unbounded, letting TimescaleDB reach back as far as the data goes.
//
// The upper bound is deliberately not left open too. An unbounded refresh
// materializes the in-progress bucket and advances every watermark past now,
// and watermarks never move back — on a developer's own database that would
// silently disable real-time aggregation for their real data, the very damage
// the 2013 fixture dates exist to avoid.
func TestRefreshWithOpenStartSucceeds(t *testing.T) {
	store := getTestDB(t)
	defer store.Close()

	until := fixtureBase.Add(48 * time.Hour)
	if _, err := store.RefreshAnalyticsAggregates(context.Background(), time.Time{}, until); err != nil {
		t.Fatalf("refresh with an open start: %v", err)
	}
}

// TestLeadingBucketIsCompleteAndConsistentAcrossMetrics guards the alignment
// the API documents. Requesting a daily series from the middle of a day used to
// slice the leading bucket: hourly-backed metrics dropped the earlier hours and
// reported the remainder stamped as a whole day, while active_accounts — already
// bucketed daily — dropped the bucket entirely, so the same request returned
// series of different lengths starting at different timestamps.
func TestLeadingBucketIsCompleteAndConsistentAcrossMetrics(t *testing.T) {
	store := getTestDB(t)
	defer store.Close()

	_, to, cleanup := insertAnalyticsFixture(t, store)
	defer cleanup()

	// Start the request midway through the fixture's second hour, well after
	// the day began.
	midDay := fixtureBase.Add(90 * time.Minute)

	txSeries, err := store.TimeSeries(context.Background(), analytics.MetricTxCount, analytics.ResolutionDaily, midDay, to, nil)
	if err != nil {
		t.Fatalf("TimeSeries tx_count: %v", err)
	}
	activeSeries, err := store.TimeSeries(context.Background(), analytics.MetricActiveAccounts, analytics.ResolutionDaily, midDay, to, nil)
	if err != nil {
		t.Fatalf("TimeSeries active_accounts: %v", err)
	}

	wantTx := fixtureExpectations.TxCountHour0 + fixtureExpectations.TxCountHour1
	if got := pointsByBucket(txSeries)[fixtureBase]; got != wantTx {
		t.Errorf("daily tx_count = %v, want the whole day %v — the leading bucket was sliced",
			got, wantTx)
	}
	if got := pointsByBucket(activeSeries)[fixtureBase]; got != fixtureExpectations.ActiveDay {
		t.Errorf("daily active_accounts = %v, want %v", got, fixtureExpectations.ActiveDay)
	}

	if len(txSeries) != len(activeSeries) {
		t.Errorf("same request returned %d tx_count points but %d active_accounts points",
			len(txSeries), len(activeSeries))
	}
	if len(txSeries) > 0 && len(activeSeries) > 0 && !txSeries[0].Timestamp.Equal(activeSeries[0].Timestamp) {
		t.Errorf("series start at different buckets: %s vs %s",
			txSeries[0].Timestamp, activeSeries[0].Timestamp)
	}
}

// TestWeeklyResolutionReturnsRealValues exercises the weekly path with data
// spanning more than one week. The main fixture covers two days, so its weekly
// aggregate is always skipped — leaving weekly bucketing unverified without
// this case.
func TestWeeklyResolutionReturnsRealValues(t *testing.T) {
	store := getTestDB(t)
	defer store.Close()

	_, _, cleanup := insertAnalyticsFixture(t, store)
	defer cleanup()

	// One extra transaction from a new account, eight days later: a different
	// week, and a distinct account the first week never saw. It sits inside the
	// shared fixture window, so the fixture teardown removes it and re-refreshes
	// the same range that materialized it.
	weekLater := fixtureBase.Add(8 * 24 * time.Hour)
	mustExec(t, store, `
		INSERT INTO transactions (hash, ledger_sequence, application_order, account,
			account_sequence, fee_charged, max_fee, operation_count, memo_type, status,
			is_soroban, envelope_xdr, result_xdr, created_at)
		VALUES ($1, 900900, 1, $2, 1, 900, 900, 1, 0, 1, false, 'fixture', 'fixture', $3)`,
		fixtureHash("tx-week2"), "GFIXTUREWEEK2", weekLater)

	from, to := fixtureWindowStart, fixtureWindowEnd
	if _, err := store.RefreshAnalyticsAggregates(context.Background(), from, to); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	for _, metric := range []analytics.Metric{analytics.MetricTxCount, analytics.MetricActiveAccounts} {
		points, err := store.TimeSeries(context.Background(), metric, analytics.ResolutionWeekly, from, to, nil)
		if err != nil {
			t.Fatalf("TimeSeries(%s, weekly): %v", metric, err)
		}
		if len(points) != 2 {
			t.Errorf("%s weekly returned %d buckets, want 2 distinct weeks: %+v", metric, len(points), points)
			continue
		}
		if points[1].Value != 1 {
			t.Errorf("%s second week = %v, want 1", metric, points[1].Value)
		}
	}

	// The first week holds five fixture transactions from three accounts, which
	// is the distinct count a summed rollup would get wrong.
	weekly, err := store.TimeSeries(context.Background(), analytics.MetricActiveAccounts, analytics.ResolutionWeekly, from, to, nil)
	if err != nil {
		t.Fatalf("TimeSeries: %v", err)
	}
	if weekly[0].Value != fixtureExpectations.ActiveDay {
		t.Errorf("first week active_accounts = %v, want %v", weekly[0].Value, fixtureExpectations.ActiveDay)
	}
}

// TestBackfillAndIncrementalRefreshAgree is the consistency the issue asks for:
// a range materialized in one pass, the way a backfill over history does it,
// must match the same range materialized hour by hour as live ingestion
// completes each bucket.
//
// It starts from unmaterialized aggregates deliberately. Refreshing a window
// that is already materialized is a no-op — Timescale skips buckets it has
// already computed — so driving the "incremental" pass over a materialized
// window would compare a result against itself and pass no matter what the
// aggregates contained.
func TestBackfillAndIncrementalRefreshAgree(t *testing.T) {
	store := getTestDB(t)
	defer store.Close()

	from, to, cleanup := insertAnalyticsFixtureRows(t, store)
	defer cleanup()

	ctx := context.Background()
	hour0, hour1 := fixtureBase, fixtureBase.Add(time.Hour)

	// Guard the premise: nothing may be materialized yet, or the incremental
	// pass below would be skipped and this test would prove nothing.
	for _, metric := range analytics.AllMetrics {
		points, err := store.TimeSeries(ctx, metric, analytics.ResolutionHourly, from, to, nil)
		if err != nil {
			t.Fatalf("TimeSeries(%s) before any refresh: %v", metric, err)
		}
		if len(points) != 0 {
			t.Fatalf("%s reports %d buckets before any refresh — the aggregates are not empty, "+
				"so the incremental pass would be a no-op", metric, len(points))
		}
	}

	// Live-like: each hour materialized on its own as it completes.
	for _, hour := range []time.Time{hour0, hour1} {
		results, err := store.RefreshAnalyticsAggregates(ctx, hour, hour.Add(time.Hour))
		if err != nil {
			t.Fatalf("incremental refresh of %s: %v", hour, err)
		}
		for _, r := range results {
			if r.Skipped && r.Aggregate == "analytics_tx_hourly" {
				t.Fatalf("the hourly aggregate was skipped refreshing %s, so nothing was materialized", hour)
			}
		}
	}

	incremental := make(map[analytics.Metric][]analytics.TimeSeriesPoint)
	for _, metric := range analytics.AllMetrics {
		points, err := store.TimeSeries(ctx, metric, analytics.ResolutionHourly, from, to, nil)
		if err != nil {
			t.Fatalf("TimeSeries(%s) after incremental refresh: %v", metric, err)
		}
		if len(points) == 0 {
			t.Fatalf("%s is still empty after the incremental refresh", metric)
		}
		incremental[metric] = points
	}

	// Backfill-like: the whole range recomputed in a single pass. force is
	// required because the buckets are materialized by now.
	for _, aggregate := range analyticsAggregates {
		_, err := store.db.ExecContext(ctx,
			"CALL refresh_continuous_aggregate($1::regclass, $2::timestamptz, $3::timestamptz, force => true)",
			aggregate.name, from, to)
		if err != nil {
			t.Fatalf("forced refresh of %s: %v", aggregate.name, err)
		}
	}

	for _, metric := range analytics.AllMetrics {
		backfilled, err := store.TimeSeries(ctx, metric, analytics.ResolutionHourly, from, to, nil)
		if err != nil {
			t.Fatalf("TimeSeries(%s) after backfill: %v", metric, err)
		}

		want := incremental[metric]
		if len(backfilled) != len(want) {
			t.Errorf("%s: backfill produced %d buckets, incremental produced %d", metric, len(backfilled), len(want))
			continue
		}
		for i := range want {
			if !backfilled[i].Timestamp.Equal(want[i].Timestamp) || backfilled[i].Value != want[i].Value {
				t.Errorf("%s bucket %d: backfill %v@%s, incremental %v@%s",
					metric, i, backfilled[i].Value, backfilled[i].Timestamp,
					want[i].Value, want[i].Timestamp)
			}
		}
	}
}
