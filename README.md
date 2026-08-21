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
| `WORKER_COUNT` | `8`                                                                                   | No       | Parallel workers for `backfill` and `s3backfill`          |
| `METRICS_ADDR` | —                                                                                     | No       | Listen address (e.g. `:9090`) for `/metrics` and `/healthz` during `live` ingestion. Disabled when unset. |

### Contract verification

| Variable                 | Default                              | Required | Description                                              |
| ------------------------ | ------------------------------------ | -------- | -------------------------------------------------------- |
| `VERIFY_API_ADDR`        | `:8080`                              | No       | Listen address for the verification API (`api` command)  |
| `VERIFY_BUILDER_IMAGE`   | `stellarview/soroban-builder:latest` | No       | Docker image used for sandboxed reproducible builds      |
| `VERIFY_WORKSPACE_DIR`   | OS temp dir                          | No       | Scratch dir for build workspaces                         |
| `VERIFY_MAX_ARCHIVE_MB`  | `20`                                 | No       | Max compressed upload size in MB                         |
| `VERIFY_MAX_EXTRACTED_MB`| `100`                                | No       | Max uncompressed source size in MB                       |
| `VERIFY_RATE_RPS`        | `1`                                  | No       | Verification submissions per second per IP               |
| `VERIFY_RATE_BURST`      | `5`                                  | No       | Per-IP burst allowance                                   |
| `VERIFY_QUEUE_SIZE`      | `16`                                 | No       | Max queued verification jobs                             |
| `VERIFY_BUILD_CONCURRENCY` | `2`                                | No       | Parallel sandboxed builds                                |
| `VERIFY_BUILD_TIMEOUT_MIN` | `20`                               | No       | Per-build timeout in minutes                             |

### Observability

When `METRICS_ADDR` is set, `live` starts an HTTP server alongside ingestion:

- `GET /metrics` -- Prometheus exposition format. Exposes `indexer_ledgers_ingested_total`, `indexer_transactions_ingested_total`, `indexer_operations_ingested_total`, `indexer_rpc_errors_total`, `indexer_db_errors_total`, and the `indexer_ingestion_lag_ledgers` gauge (network tip minus last ingested ledger).
- `GET /healthz` -- liveness/readiness probe. Returns `200` when the database is reachable and the ingestion loop has completed a poll cycle within the last 2 minutes, `503` otherwise (with a generic `reason`, never the raw database error). Suitable for Docker/k8s health checks.

## Commands

```bash
make build          # Compile to bin/indexer
make migrate        # Apply pending database migrations
make run-live       # Live ingestion (requires RPC_ENDPOINT env var)
make run-api        # Contract verification API (requires Docker + builder image)
make test           # Run all tests
make fmt            # Format code
make lint           # Run go vet
make builder-image  # Build the sandboxed Soroban builder image
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

## Contract source verification

The `api` command serves the contract verification API: developers submit their
contract source (zip/tar archive or git reference), it is built reproducibly in
a sandboxed Docker container, and the resulting WASM hash is compared against
the on-chain `contract_code.wasm_hash`. Matching source is stored and served by
wasm hash.

```bash
# one-time: build the pinned builder image (Rust + stellar-cli, offline cargo cache)
make builder-image

# run the API
make run-api
```

The full frozen v1 HTTP contract lives in [`docs/api/contract-verification.md`](docs/api/contract-verification.md).

Key properties:

- **Reproducible** — builds run in a pinned image (`rust:1.85`, `stellar-cli@22.0.8`) with no network; identical bytecode shares a single verified-source record keyed by wasm hash.
- **Sandboxed** — containers run with `--network=none --read-only --cap-drop=ALL --user 1000:1000`, memory/CPU/pids caps, and tmpfs scratch space.
- **Hardened uploads** — archives are magic-byte sniffed; path traversal, absolute paths, symlinks/devices, zip bombs (ratio + size caps), and excessive file counts are rejected before the builder ever runs.
- **Rate limited** — per-IP token bucket (`VERIFY_RATE_RPS`/`VERIFY_RATE_BURST`).

The golden-file test proves build reproducibility end-to-end (requires Docker
and the builder image; skips otherwise):

```bash
go test ./internal/verify/ -run TestGoldenReproducibleBuild -v

# after an intentional toolchain bump, regenerate:
go test ./internal/verify/ -run TestGoldenReproducibleBuild -update-golden
```

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
    contract_events, token_events, assets, trades, network_stats, ingestion_state CASCADE;
"
```

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
| `cmd/indexer`        | Entry point with `live`, `backfill`, `migrate` commands                 |
| `internal/config`    | Environment variable loading and validation                             |
| `internal/source`    | Stellar RPC client (`getLedgers`, `getTransactions`, `getLatestLedger`) |
| `internal/transform` | XDR parsing into database models (ledgers, transactions, operations)    |
| `internal/store`     | PostgreSQL writer with batch inserts and ingestion cursor               |
| `internal/pipeline`  | Live ingestion loop and parallel backfill orchestration                 |
| `internal/publisher` | Redis pub/sub for real-time event streaming                             |
| `internal/verify`    | Contract source verification: submission, sandboxed builds, source hosting |
