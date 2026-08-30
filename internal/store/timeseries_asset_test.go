package store

import (
	"context"
	"testing"

	"github.com/miguelnietoa/stellar-explorer/indexer/internal/analytics"
)

// The fixture's only mint/burn activity is on the classic FIXT-issuer asset
// (10 minted, 3 burned, at the classic 7-decimal scale). Filtering to it must
// reproduce the unfiltered total, since it's the only asset contributing one;
// filtering to anything else must come back empty.
func TestTimeSeriesAssetSupplyFilterIsolatesOneAsset(t *testing.T) {
	store := getTestDB(t)
	defer store.Close()

	from, to, cleanup := insertAnalyticsFixture(t, store)
	defer cleanup()

	classicFilter := &analytics.AssetFilter{Code: fixtureAssetCode, Issuer: fixtureIssuer}
	filtered, err := store.TimeSeries(context.Background(), analytics.MetricAssetSupply, analytics.ResolutionHourly, from, to, classicFilter)
	if err != nil {
		t.Fatalf("TimeSeries: %v", err)
	}
	if len(filtered) != 1 || filtered[0].Value != fixtureExpectations.SupplyHour0 {
		t.Fatalf("classic asset filter = %+v, want a single point worth %v", filtered, fixtureExpectations.SupplyHour0)
	}

	unfiltered, err := store.TimeSeries(context.Background(), analytics.MetricAssetSupply, analytics.ResolutionHourly, from, to, nil)
	if err != nil {
		t.Fatalf("TimeSeries (unfiltered): %v", err)
	}
	if len(unfiltered) != 1 || unfiltered[0].Value != filtered[0].Value {
		t.Errorf("unfiltered = %+v, want to match the single-asset filter since it's the only asset with supply activity", unfiltered)
	}

	for name, filter := range map[string]*analytics.AssetFilter{
		"native":            {Native: true},
		"soroban token":     {ContractID: fixtureTokenContract},
		"unrelated classic": {Code: "NOPE", Issuer: fixtureIssuer},
	} {
		t.Run(name, func(t *testing.T) {
			points, err := store.TimeSeries(context.Background(), analytics.MetricAssetSupply, analytics.ResolutionHourly, from, to, filter)
			if err != nil {
				t.Fatalf("TimeSeries: %v", err)
			}
			if len(points) != 0 {
				t.Errorf("got %+v, want an empty series", points)
			}
		})
	}
}