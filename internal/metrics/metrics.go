// Package metrics defines the Prometheus collectors exposed by the indexer's
// /metrics endpoint and instruments the live pipeline's ingestion counters,
// ingestion lag, and upstream error rates.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

var (
	LedgersIngested = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "indexer",
		Name:      "ledgers_ingested_total",
		Help:      "Total number of ledgers ingested by the live pipeline.",
	})

	TransactionsIngested = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "indexer",
		Name:      "transactions_ingested_total",
		Help:      "Total number of transactions ingested by the live pipeline.",
	})

	OperationsIngested = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "indexer",
		Name:      "operations_ingested_total",
		Help:      "Total number of operations ingested by the live pipeline.",
	})

	RPCErrors = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "indexer",
		Name:      "rpc_errors_total",
		Help:      "Total number of errors returned by the Stellar RPC source.",
	})

	DBErrors = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "indexer",
		Name:      "db_errors_total",
		Help:      "Total number of errors returned by the database store.",
	})

	// IngestionLagLedgers is the gap between the network's latest ledger
	// sequence and the last ledger the pipeline has ingested.
	IngestionLagLedgers = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "indexer",
		Name:      "ingestion_lag_ledgers",
		Help:      "Number of ledgers between the network tip and the last ingested ledger.",
	})
)

// Registry is the Prometheus registry the /metrics endpoint serves. Using a
// dedicated registry (instead of the global default) keeps the exposed
// surface limited to the collectors this package defines.
var Registry = prometheus.NewRegistry()

func init() {
	Registry.MustRegister(
		LedgersIngested,
		TransactionsIngested,
		OperationsIngested,
		RPCErrors,
		DBErrors,
		IngestionLagLedgers,
	)
}
