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

	VerificationSubmissions = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "indexer",
		Name:      "verification_submissions_total",
		Help:      "Contract verification submissions by result (accepted/rejected).",
	}, []string{"result"})

	VerificationBuilds = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "indexer",
		Name:      "verification_builds_total",
		Help:      "Completed verification builds by outcome (verified/mismatch/failed).",
	}, []string{"outcome"})

	VerificationQueueLength = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "indexer",
		Name:      "verification_queue_length",
		Help:      "Number of verification jobs waiting to be built.",
	})

	VerificationBuildDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: "indexer",
		Name:      "verification_build_duration_seconds",
		Help:      "Wall-clock duration of verification builds.",
		Buckets:   []float64{1, 5, 15, 30, 60, 120, 300, 600, 1200},
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
		VerificationSubmissions,
		VerificationBuilds,
		VerificationQueueLength,
		VerificationBuildDuration,
	)
}
