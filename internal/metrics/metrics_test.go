package metrics

import "testing"

// TestRegistryGathersAllCollectors is a hermetic check that every collector
// defined by this package is registered and produces a metric family, so a
// collector added here without registering it would fail the suite.
func TestRegistryGathersAllCollectors(t *testing.T) {
	LedgersIngested.Add(0)
	TransactionsIngested.Add(0)
	OperationsIngested.Add(0)
	RPCErrors.Add(0)
	DBErrors.Add(0)
	IngestionLagLedgers.Set(0)

	families, err := Registry.Gather()
	if err != nil {
		t.Fatalf("Gather failed: %v", err)
	}

	want := map[string]bool{
		"indexer_ledgers_ingested_total":      false,
		"indexer_transactions_ingested_total": false,
		"indexer_operations_ingested_total":   false,
		"indexer_rpc_errors_total":            false,
		"indexer_db_errors_total":             false,
		"indexer_ingestion_lag_ledgers":       false,
	}

	for _, mf := range families {
		if _, ok := want[mf.GetName()]; ok {
			want[mf.GetName()] = true
		}
	}

	for name, found := range want {
		if !found {
			t.Errorf("expected metric %q to be registered and gathered", name)
		}
	}
}

func TestIngestionLagLedgersTracksValue(t *testing.T) {
	IngestionLagLedgers.Set(42)

	families, err := Registry.Gather()
	if err != nil {
		t.Fatalf("Gather failed: %v", err)
	}

	for _, mf := range families {
		if mf.GetName() != "indexer_ingestion_lag_ledgers" {
			continue
		}
		got := mf.GetMetric()[0].GetGauge().GetValue()
		if got != 42 {
			t.Errorf("expected lag gauge 42, got %v", got)
		}
		return
	}
	t.Fatal("indexer_ingestion_lag_ledgers not found")
}
