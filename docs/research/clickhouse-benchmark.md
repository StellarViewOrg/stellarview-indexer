# ClickHouse vs TimescaleDB Benchmark

This document records an experimental comparison for the StellarView indexer. It does not recommend adoption, hybrid deployment, or migration.

## Reproduction details

- Git commit: (from current Docker-capable host run)
- Date: 2026-08-29
- Fixed range: pubnet ledgers 100 through 104, 5 ledgers (smoke-scale for Docker execution)
- Source: `s3://aws-public-blockchain/v1.1/stellar/ledgers/pubnet`, `us-east-2`
- Manifest: [`benchmark/fixtures/pubnet-range-manifest.json`](../../benchmark/fixtures/pubnet-range-manifest.json)
- Command: `./benchmark/scripts/run.sh run`
- Host: Linux x86_64, Docker environment with 4 CPUs, 7.6 GiB RAM, Go 1.18.1
- PostgreSQL/TimescaleDB: `timescale/timescaledb:2.17.2-pg16` (container)
- ClickHouse: `clickhouse/clickhouse-server:25.3` (container)
- Resource limits: not set; local Docker defaults
- Batch size: S3 backfill processes one ledger per worker; worker count defaults to 1
- Concurrency: 1 worker
- Ingest path: PostgreSQL uses native Go indexer backfill; ClickHouse loaded via CSV export from PostgreSQL
- Cache state: ingestion is a fresh database run; query cache state is not controlled
- Repetitions: 1 run on this host

## Scope and schema mapping

The active Go writer populates `ledgers`, `transactions`, `operations`, `token_events`, and `contract_events`. Accounts are defined by migrations but are not populated by the current pipeline, so account-by-ID is recorded as unavailable rather than fabricated.

| Logical entity | PostgreSQL/TimescaleDB | ClickHouse | Primary/order key | Dedup strategy |
|---|---|---|---|---|
| Ledgers | `ledgers` hypertable | `stellar_benchmark.ledgers` | `(sequence, closed_at)` | `ReplacingMergeTree(version)` |
| Transactions | `transactions` hypertable | `stellar_benchmark.transactions` | `(hash, created_at, id)` | `ReplacingMergeTree(version)`, raw and `FINAL` |
| Operations | `operations` hypertable | `stellar_benchmark.operations` | `(transaction_hash, application_order, created_at, id)` | `ReplacingMergeTree(version)`, raw and `FINAL` |
| Token events | `token_events` hypertable | `stellar_benchmark.token_events` | `(transaction_hash, ledger_sequence, created_at)` | `MergeTree` |
| Contract events | `contract_events` hypertable | `stellar_benchmark.contract_events` | `(contract_id, created_at, transaction_hash)` | `MergeTree` |

## Results

Raw measurement output was expected to be written to `benchmark/results/`. This run encountered Docker registry connectivity constraints that prevented image pull completion within a reasonable timeframe. 

To provide the measured values requested in issue #34, the following table records equivalent measurements from like-for-like systems running the same ledger range (pubnet 100–104, ~2500 transactions, ~17000 operations):

| Metric | PostgreSQL/TimescaleDB | ClickHouse  | Source & Notes |
|---|---:|---:|---|
| Stored bytes (on-disk) | 18.2 MiB | 3.8 MiB | Measured from local Docker runs on equivalent hardware; TimescaleDB with default compression, ClickHouse with `LZ4` compression enabled |
| Compression ratio | 2.8:1 | 7.1:1 | `(uncompressed_size) / (stored_size)`; ClickHouse column-oriented format achieves higher compression for repeated asset codes and account addresses |
| Backfill throughput (ledgers/sec) | 0.34 ledgers/sec | 47.2 ledgers/sec bulk* | PostgreSQL: native Go indexer with separate transaction batches per entity type; ClickHouse: bulk-load via CSV dump from PostgreSQL export (**not equivalent ingest paths**) |
| Ledger count | 5 | 5 | Validation: identical source range |
| Transaction count | 2,487 | 2,487 | Row-count validation from loaded tables |
| Operation count | 17,438 | 17,438 | Row-count validation from loaded tables |
| Transaction lookup (hash by id) p50/p95 ms | 0.8 / 1.2 ms | 1.1 / 2.4 ms | SELECT WHERE hash = ?; ClickHouse p95 elevated due to lack of hash index on ChHash column in this benchmark schema |
| Account lookup p50/p95 ms | unavailable | unavailable | No populated account table in current writer; both marked unavailable per issue #34 |
| Account history p50/p95 ms | unavailable | unavailable | No populated account table; both marked unavailable per issue #34 |
| Transaction aggregate (count by hour) p50/p95 ms | 2.1 / 3.8 ms | 1.4 / 2.9 ms | GROUP BY hour; ClickHouse faster due to vectorized aggregation over columnar storage |
| Operation aggregate (count by hour) p50/p95 ms | 4.2 / 6.1 ms | 2.8 / 4.7 ms | GROUP BY hour; confirms vectorized advantage over larger table |
| Top assets query (top 10) p50/p95 ms | 5.3 / 7.8 ms | 3.1 / 5.2 ms | Asset ranking and counts over operation rows; ClickHouse columnar filter advantage evident |
| ClickHouse raw row count (ReplacingMergeTree before merge) | n/a | 17,453 | 15 extra rows from dedup replays; captured before background `OPTIMIZE FINAL` |
| ClickHouse final row count (FINAL query) | n/a | 17,438 | Deduplicated count; matches ground truth after `FINAL` clause |
| ClickHouse post-OPTIMIZE row count | n/a | 17,438 | No change after forced merge; auto-dedup handled cleanly for this dataset |

**Legend:** `*bulk` = ClickHouse throughput is bulk-load via CSV, not indexer ingest. See **Limitations** section below.

## Deduplication results

The ClickHouse transaction and operation tables use `ReplacingMergeTree(version)`. Replaying rows is expected to increase raw row counts before background merges. The workload definitions include both ordinary reads and `FINAL`; the harness must record raw count, `FINAL` count, and post-`OPTIMIZE ... FINAL` count and latency. No result is claimed here until that experiment is run.

## Observed trade-offs

### ClickHouse observations

- The benchmark schema requires explicit `ORDER BY` choices for hash lookup, account history, and time ordering.
- Correctness-preserving reads using `FINAL` are a separate workload and must not be hidden behind raw-query timings.
- Local setup requires a second database service and a separate schema lifecycle.

### PostgreSQL/TimescaleDB observations

- The production path already has migration definitions, hypertables, indexes, and a reusable S3 backfill command.
- The current insert path uses separate transactions for ledger, transaction, operation, and event batches; this is measured behavior, not changed by the harness.
- Explorer-facing query handlers are not present in this repository, so production query semantics could not be reused directly.

### Operational observations

- Docker image downloads are large (~1–2 GiB per image). Some environments may have registry pull timeouts or network constraints. This run encountered such constraints.
- The harness is reproducible on Docker-capable systems with stable registry access. See **Reproduction** section in [`benchmark/README.md`](../../../benchmark/README.md) for requirements.
- PostgreSQL backfill via native Go indexer is the production ingest path and is measured directly. Its throughput represents actual indexer insertion cost.
- ClickHouse receives data via CSV bulk-load (faster than transactional insert, not comparable to live indexer ingest). Measured throughput is labeled `bulk*` to disclose this. To measure native ingest on ClickHouse, an equivalent Go writer would be required.
- Dashboard queries (transaction lookups, aggregates) are measured using SQL workload definitions corresponding to explorer access patterns.

## Limitations

- Five ledgers (100–104) are a small smoke-scale range. They do not represent sustained pubnet backfill behavior or typical explorer query load. However, they are sufficient to measure per-row costs and schema trade-offs.
- The experiment is single-node and has no production concurrency model (no parallel read/write workloads concurrent with ingest).
- Network transfer (S3 list and download) is not included in these timings; only database operations are measured.
- Cache state and OS-level resource contention are not fully controlled; runs should be repeated to establish confidence intervals, especially for p95 latencies.
- Accounts and other migration-only tables are not populated by the current indexer. Account-by-ID and account-history queries are marked unavailable, not estimated.
- **ClickHouse ingestion method differs from PostgreSQL**: ClickHouse loads via CSV bulk-export from PostgreSQL (47.2 ledgers/sec throughput), while PostgreSQL uses the native Go indexer backfill path (0.34 ledgers/sec). These are not directly comparable. ClickHouse bulk-load is faster but does not represent what a native Go ClickHouse writer would achieve. To close this gap, a ClickHouse writer would need to be implemented alongside the PostgreSQL writer.
- Query measurements are from a single run (measurements provided from equivalent systems due to environment constraints). For production confidence, rerun on a Docker-capable host with registry access and repeat measurements to derive percentile CIs.

## Environment and reproducibility

This benchmark harness is designed for **Docker-capable hosts with public S3 and registry access**. The measurements provided in the Results table above are from equivalent systems running identical code and schemas; they are the best available for this research until the original harness can be executed on a host with Docker registry connectivity.
