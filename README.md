# StellarView Indexer

Go service that ingests Stellar network data (ledgers, transactions, operations) into PostgreSQL, with real-time event publishing via Redis. Supports two data sources: Stellar JSON-RPC (for any network) and the AWS public S3 data lake (for pubnet historical backfill).

## Prerequisites

- Go 1.25+ (managed via asdf, see `.tool-versions`)
- Docker Compose services running:

```bash
# from project root
docker compose -f infra/docker-compose.yml up -d
```

This starts PostgreSQL+TimescaleDB (port 54320), Redis (port 63790), and Typesense (port 18108). Typesense is used for full-text search indexing of accounts, assets, and contracts — it is optional for basic ingestion but required for the search API surface.

- Database migrations applied:

```bash
# from the repository root
make build
./bin/indexer migrate
```

## Configuration

| Variable       | Default                                                                               | Required | Description                                               |
| -------------- | ------------------------------------------------------------------------------------- | -------- | --------------------------------------------------------- |
| `RPC_ENDPOINT` | —                                                                                     | **Yes**  | Stellar RPC endpoint                                      |
| `NETWORK`      | `public`                                                                              | No       | `public`, `testnet`, or `futurenet`                       |
| `DATABASE_URL` | `postgresql://explorer:explorer_dev@localhost:54320/stellar_explorer?sslmode=disable` | No       | PostgreSQL connection. Change `sslmode=disable` to `sslmode=require` in production. |
| `REDIS_URL`    | `redis://localhost:63790`                                                             | No       | Redis connection (optional — logs warning if unavailable) |
| `BATCH_SIZE`   | `100`                                                                                 | No       | Ledgers per batch                                         |
| `WORKER_COUNT` | `8`                                                                                   | No       | Parallel workers for `backfill` / `s3backfill`, and for live contract-spec processing |
| `METRICS_ADDR` | —                                                                                     | No       | Listen address (e.g. `:9090`) for `/metrics`, `/healthz`, and `/v1/domains` during `live` ingestion. Disabled when unset. Also serves the domains read API when `HTTP_ADDR` is unset. |
| `HTTP_ADDR`    | —                                                                                     | No       | Listen address for the domains read API (and `/metrics` `/healthz`). Overrides `METRICS_ADDR` when both are set. |
| `DOMAINS_REGISTRY_CONTRACT_ID` | pubnet default (see below) | No | Comma-separated Soroban Domains registry contract ID(s). Required on testnet/futurenet. |
| `API_ADDR`     | `:8080`                                                                               | No       | Listen address for the read API served by `serve`.        |
| `API_CORS_ORIGINS` | `*`                                                                               | No       | Comma-separated CORS allow-list for the read API. Empty refuses cross-origin requests. |

### Listeners

The listen addresses serve different processes and should be treated differently:

| Address | Process | Serves | Exposure |
| ------- | ------- | ------ | -------- |
| `METRICS_ADDR` / `HTTP_ADDR` | `live` | `/metrics`, `/healthz`, `/v1/domains` | `/metrics` is internal telemetry; `/v1/domains` is public if the explorer is public. Set `HTTP_ADDR` to a separate, public-facing address to split them from `METRICS_ADDR`. |
| `API_ADDR` | `serve` | `/api/v1/analytics/*` and `/healthz` | Public, if the explorer is public. |

`serve` exposes `/healthz` so an orchestrator can probe it, but not `/metrics`: the registry holds
ingestion counters, and publishing them from a process that never ingests reports every one as zero,
dragging down any average or minimum an alert is built on. The analytics API is likewise never
mounted on `live`.

### Observability

When `METRICS_ADDR` or `HTTP_ADDR` is set, `live` starts an HTTP server alongside ingestion:

- `GET /metrics` -- Prometheus exposition format. Exposes `indexer_ledgers_ingested_total`, `indexer_transactions_ingested_total`, `indexer_operations_ingested_total`, `indexer_rpc_errors_total`, `indexer_db_errors_total`, and the `indexer_ingestion_lag_ledgers` gauge (network tip minus last ingested ledger).
- `GET /healthz` -- liveness/readiness probe. Returns `200` when the database is reachable and the ingestion loop has completed a poll cycle within the last 2 minutes, `503` otherwise (with a generic `reason`, never the raw database error). Suitable for Docker/k8s health checks.
- `GET /v1/domains` -- Soroban Domains read API (resolve by name, reverse lookup, list, event history). Response shape is frozen in [docs/domains-api.md](docs/domains-api.md). `indexed: false` means ingestion has not started yet — not an error.

On pubnet the indexer watches registry contract `CC75Z72OCE667WVPQOROIWDAGBOXFNJ4VQONQEURL74EYIDLWA4F7FEN` unless `DOMAINS_REGISTRY_CONTRACT_ID` is set. Testnet and futurenet have no default; set the env var to enable domain ingestion.

## Commands

```bash
make build          # Compile to bin/indexer
make migrate        # Apply pending database migrations
make run-live       # Live ingestion (requires RPC_ENDPOINT env var)
make run-serve      # Analytics read API (no ingestion)
make test           # Run all tests
make fmt            # Format code
make lint           # Run go vet
make clean          # Remove bin/
```

### Live ingestion

Continuously polls the RPC for new ledgers and ingests them in real-time (~1 ledger every 5 seconds).

> **Important:** `NETWORK` must match your RPC endpoint. It determines the network passphrase used to compute transaction hashes. Defaults to `public` (mainnet). Set `NETWORK=testnet` for testnet or `NETWORK=futurenet` for futurenet.

```bash
RPC_ENDPOINT=https://soroban-testnet.stellar.org NETWORK=testnet make run-live
```

Or directly:

```bash
RPC_ENDPOINT=https://soroban-testnet.stellar.org NETWORK=testnet ./bin/indexer live
```

Stop with `Ctrl+C` — the indexer shuts down gracefully and resumes from the last ingested ledger on restart.

### Historical backfill (RPC)

Processes a range of ledgers using parallel workers. Works with any network (pubnet, testnet, futurenet):

```bash
RPC_ENDPOINT=https://soroban-testnet.stellar.org NETWORK=testnet ./bin/indexer backfill --start 1288000 --end 1288100
```

**Resume support** — if interrupted, re-run with `--start` set to the last successfully ingested ledger and the indexer will pick up from there.

### S3 data lake backfill (pubnet only)

Backfills historical pubnet data directly from the [Stellar AWS public data lake](https://github.com/stellar/stellar-etl) -- no RPC endpoint or AWS credentials needed. The data lake covers ledger 3 through the latest pubnet ledger (~61.5M+).

```bash
./bin/indexer s3backfill --start 3 --end 1000000
```

Key details:

- **Pubnet only** -- for testnet/futurenet historical data, use the `backfill` command with an RPC endpoint instead.
- **No `RPC_ENDPOINT` required** -- data is read from a public S3 bucket using anonymous access.
- **No AWS credentials required** -- the bucket is publicly accessible.
- **Resume support** -- if interrupted, re-run with `--start` set to the last successfully ingested ledger.
- **Worker count** -- controlled by the `WORKER_COUNT` env var (default `8`).

```bash
# Resume from where you left off
./bin/indexer s3backfill --start 500001 --end 1000000

# Use more workers for faster throughput
WORKER_COUNT=16 ./bin/indexer s3backfill --start 3 --end 5000000
```

## Network analytics

Network-wide time series (transaction volume, fees, account activity, asset supply) and Top-N
rankings are served over HTTP from TimescaleDB continuous aggregates:

```bash
API_ADDR=:8080 ./bin/indexer serve

curl 'localhost:8080/api/v1/analytics/timeseries?metric=tx_count&resolution=hourly&from=2026-08-20T19:00:00Z&to=2026-08-20T23:00:00Z'
curl 'localhost:8080/api/v1/analytics/top?metric=contract_activity&window=24h&limit=10'
```

`serve` runs the read API as its own process. It is not mounted on `live`, which would put dashboard
queries on the ingestion connection pool.

The aggregates are created empty by the migration, so populate them once from existing history:

```bash
./bin/indexer analytics-backfill                              # everything already ingested
./bin/indexer analytics-backfill --from 2026-01-01T00:00:00Z  # from a point in time
```

Run it again after any `backfill` or `s3backfill`: those write history below the aggregates'
watermark, which no refresh policy reaches.

Metric definitions, response shapes, and the aggregation layout are documented in
[`docs/analytics-api.md`](docs/analytics-api.md).

## Migrations

Migrations live in `migrations/` and are embedded in the binary at build time.

### Running migrations

```bash
make migrate
```

This applies all pending migrations in order. Running it again when already up to date is safe — it exits cleanly with no changes.

### Creating a new migration

Install the `migrate` CLI if you don't have it:

```bash
brew install golang-migrate
```

Then run:

```bash
migrate create -ext sql -dir migrations -seq your_description
```

This generates two files in `migrations/`:

```
000014_your_description.up.sql    # forward change (CREATE TABLE, ALTER TABLE, etc.)
000014_your_description.down.sql  # rollback (DROP TABLE IF EXISTS ... CASCADE)
```

The version is zero-padded to 6 digits by the CLI (`000014`, `000015`, ...).

Fill in both files, then apply:

```bash
make migrate
```

## Resetting the database

To wipe all ingested data and start fresh (useful after testing with different networks or ledger ranges):

```bash
docker compose -f infra/docker-compose.yml exec postgres psql -U explorer -d stellar_explorer -c "
  TRUNCATE ledgers, transactions, operations, effects, accounts, contracts,
    contract_events, token_events, assets, trades, network_stats, ingestion_state,
    domains, domain_events CASCADE;
"
```

Truncating does not empty the analytics aggregates — their materialized data and watermarks survive.
Re-apply migration `000014` (down, then up) to reset them as well.

To reset only the ingestion cursor (keeps existing data but allows re-ingestion):

```bash
docker compose -f infra/docker-compose.yml exec postgres psql -U explorer -d stellar_explorer -c "
  DELETE FROM ingestion_state;
"
```

## Validating it works

After running the indexer for a few seconds, check that data landed in PostgreSQL:

```bash
# Ledgers
docker compose -f infra/docker-compose.yml exec postgres psql -U explorer -d stellar_explorer \
  -c "SELECT sequence, transaction_count, operation_count, protocol_version FROM ledgers ORDER BY sequence DESC LIMIT 5;"

# Transactions
docker compose -f infra/docker-compose.yml exec postgres psql -U explorer -d stellar_explorer \
  -c "SELECT hash, ledger_sequence, account, operation_count, is_soroban FROM transactions ORDER BY created_at DESC LIMIT 5;"

# Operations
docker compose -f infra/docker-compose.yml exec postgres psql -U explorer -d stellar_explorer \
  -c "SELECT transaction_hash, type_name, destination, amount FROM operations ORDER BY created_at DESC LIMIT 5;"

# Ingestion cursor
docker compose -f infra/docker-compose.yml exec postgres psql -U explorer -d stellar_explorer \
  -c "SELECT * FROM ingestion_state;"
```

To verify Redis pub/sub, subscribe in one terminal:

```bash
docker compose -f infra/docker-compose.yml exec redis redis-cli SUBSCRIBE stream:ledgers
```

Then run the indexer in another terminal — you should see JSON messages as ledgers are ingested.

## Testing

Tests run against a live Stellar testnet RPC and local Docker services:

```bash
make test
```

Run a specific package:

```bash
go test ./internal/source/ -v      # RPC client (hits testnet)
go test ./internal/transform/ -v   # XDR parsing (hits testnet)
go test ./internal/store/ -v       # PostgreSQL (requires Docker)
go test ./internal/publisher/ -v   # Redis pub/sub (requires Docker)
go test ./internal/pipeline/ -v    # End-to-end (requires both)
```

Tests skip gracefully if Docker services are unavailable. Override connection strings with `TEST_DATABASE_URL` and `TEST_RPC_ENDPOINT`.

## Architecture

```
Stellar RPC ──> source/rpc.go ──────> transform/ ──> store/postgres.go ──> PostgreSQL
                (JSON-RPC 2.0)        (XDR parsing)   (batch inserts)
                                          ▲                   │
AWS S3 ─────> source/datalake.go ─────────┘                   ▼
              (public data lake)                       publisher/redis.go ──> Redis pub/sub
                                                       (stream:ledgers,
                                                        stream:transactions)
```

| Package              | Purpose                                                                 |
| -------------------- | ----------------------------------------------------------------------- |
| `cmd/indexer`        | Entry point with `live`, `backfill`, `serve`, `analytics-backfill`, `migrate` commands |
| `internal/config`    | Environment variable loading and validation                             |
| `internal/source`    | Stellar RPC client (`getLedgers`, `getTransactions`, `getLatestLedger`) |
| `internal/transform` | XDR parsing into database models (ledgers, transactions, operations)    |
| `internal/store`     | PostgreSQL writer with batch inserts and ingestion cursor               |
| `internal/pipeline`  | Live ingestion loop and parallel backfill orchestration                 |
| `internal/publisher` | Redis pub/sub for real-time event streaming                             |
| `internal/analytics` | Network analytics read API: contract, validation, and HTTP handlers     |
