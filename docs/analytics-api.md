# Network Analytics API

Read-only HTTP API exposing network-wide time series and Top-N rankings, backed by TimescaleDB
continuous aggregates.

**This contract is frozen.** The explorer builds its dashboards against these shapes
(`stellarview-explorer/apps/explorer-web/src/lib/indexer/`). Field names, metric identifiers, and
the empty-result behaviour must not change without coordinating with that repository.

## Running it

```bash
API_ADDR=:8080 ./bin/indexer serve
```

`serve` runs the read API as its own process, which is what the explorer's `NEXT_PUBLIC_INDEXER_URL`
points at. It is deliberately not mounted on the `live` command: sharing that process would put
dashboard queries on the ingestion connection pool, where a burst can exhaust it, stall writes, and
trip the `/healthz` staleness check into a restart.

The same listener also answers `/healthz`, so an orchestrator can probe the API process. It does
not serve `/metrics`: that registry holds ingestion counters, and publishing them from a process
that never ingests reports every one as zero, dragging down any average an alert is built on.

`API_CORS_ORIGINS` sets the browser allow-list, defaulting to `*` — appropriate for a read-only
public surface with no credentials. Set it to a comma-separated list to restrict access, or to an
empty value to refuse cross-origin requests entirely.

## `GET /api/v1/analytics/timeseries`

| Parameter | Required | Values |
| ---------- | -------- | ------ |
| `metric` | yes | `tx_count`, `tx_volume`, `fee_classic`, `fee_soroban`, `active_accounts`, `new_accounts`, `asset_supply` |
| `resolution` | yes | `hourly`, `daily`, `weekly` |
| `from` | yes | RFC 3339 timestamp. Widened to the start of the bucket containing it |
| `to` | yes | RFC 3339 timestamp, exclusive |

```bash
curl 'localhost:8080/api/v1/analytics/timeseries?metric=tx_count&resolution=hourly&from=2026-08-20T19:00:00Z&to=2026-08-20T23:00:00Z'
```

```json
{
  "metric": "tx_count",
  "resolution": "hourly",
  "from": "2026-08-20T19:00:00Z",
  "to": "2026-08-20T23:00:00Z",
  "data": [
    { "timestamp": "2026-08-20T19:00:00Z", "value": 4821 },
    { "timestamp": "2026-08-20T20:00:00Z", "value": 5140 }
  ]
}
```

## `GET /api/v1/analytics/top`

| Parameter | Required | Values |
| ---------- | -------- | ------ |
| `metric` | yes | `contract_activity`, `asset_transfers`, `highest_fees` |
| `window` | yes | `24h`, `7d`, `30d`. Widened to the enclosing hour |
| `limit` | no | 1–100, default 10 |

```bash
curl 'localhost:8080/api/v1/analytics/top?metric=contract_activity&window=24h&limit=10'
```

```json
{
  "metric": "contract_activity",
  "window": "24h",
  "data": [
    {
      "id": "CAABC...",
      "label": "Soroswap Router",
      "value": 12043,
      "metadata": { "contract_type": 0 }
    }
  ]
}
```

`metadata` is omitted when a metric has no extra context to report.

## Metric definitions

| Metric | Source | Value | Unit |
| ------ | ------ | ----- | ---- |
| `tx_count` | `transactions` | `COUNT(*)` | transactions |
| `tx_volume` | `token_events` (`transfer`, native asset) | `SUM(amount)`, scaled to whole units | XLM |
| `fee_classic` | `transactions` where `NOT is_soroban` | `SUM(fee_charged)` | stroops |
| `fee_soroban` | `transactions` where `is_soroban` | `SUM(fee_charged)` — **total** fee, not the resource fee | stroops |
| `active_accounts` | `transactions` | `COUNT(DISTINCT account)` | accounts |
| `new_accounts` | `operations` where `type_name = 'create_account'` | `COUNT(*)` | accounts |
| `asset_supply` | `token_events` (`mint`, `burn`, `clawback`) | net minted minus burned | asset units |

| Top-N metric | Source | `id` | `value` |
| ------------ | ------ | ---- | ------- |
| `contract_activity` | `contract_events` per contract | contract ID | events emitted |
| `asset_transfers` | `token_events` (`transfer`) | `CODE-ISSUER`, or `native` | transferred volume |
| `highest_fees` | `transactions` | transaction hash | fee charged, in stroops |

Notes on the definitions:

- **`tx_volume` counts transfers only.** CAP-67 emits a `fee` token event for every transaction;
  on a sample of testnet data those were 78k of 91k total events. Including them would inflate
  volume by more than an order of magnitude, so only `transfer` events on the native asset count.
- **`fee_soroban` is the total fee charged on Soroban transactions**, not the isolated resource-fee
  component. `transactions.soroban_resources` exists in the schema but the transform layer does not
  populate it, so the resource fee cannot be separated from the inclusion fee. **Do not chart this
  as a resource fee** — doing so overstates it by the inclusion fee on every Soroban transaction.
  Extracting `SorobanTransactionData.resourceFee` is tracked in
  [#43](https://github.com/StellarViewOrg/indexer/issues/43).
- **`contract_activity` counts contract events, not invocations.** `operations.contract_id` is never
  populated: the transform layer records only the host function *type* for `invoke_host_function`,
  not the contract called. `contract_events` is the only per-contract signal available today.
- **Amounts come from `amount`, not `amount_formatted`.** The latter is never populated by the
  transform layer, so stored base units are scaled by the asset's decimals at read time — falling
  back to the classic Stellar precision of 7.
- **`new_accounts` counts `create_account` operations regardless of whether their transaction
  succeeded.** Operations from failed transactions are persisted, and the metric cannot filter them:
  TimescaleDB permits only one hypertable per continuous aggregate, so the aggregate cannot join
  `operations` to `transactions.status` (verified — the definition is rejected outright). On a
  sample of testnet data 1 of 1,932 create_account operations belonged to a failed transaction, but
  nothing bounds that share under a wave of failing submissions. Fixing it properly means
  denormalising the status onto `operations` during transform, or not persisting operations from
  failed transactions at all.
- **`asset_supply` sums signed deltas across every asset** when queried through this endpoint.
  Because assets have different units, that total is an activity indicator rather than a monetary
  figure. The underlying aggregate is stored per asset, so a per-asset series can be exposed later
  without changing the stored data.

## Semantics

**Empty is not an error.** A metric with nothing aggregated yet returns `200` with `"data": []`.
The explorer uses exactly that to render its "not available yet" state, so these endpoints never
answer `404` for a valid-but-unpopulated metric. Only malformed parameters produce `400`, and only
a genuine backend failure produces `500`.

**The requested range is widened to whole buckets.** The lower bound is snapped down to the
requested resolution, so a series asked for from the middle of a day starts at that day's boundary
and every returned bucket is complete. Filtering on the raw instant instead would slice the leading
bucket — reporting part of a day as the whole of it for most metrics, and dropping the bucket
entirely for `active_accounts`. Top-N windows are widened the same way, to the enclosing hour.

**Bucket boundaries follow `time_bucket`, in UTC.** Buckets of a day or more are measured from
2000-01-03, not the UNIX epoch, which puts weekly boundaries on a **Monday**. Alignment is identical
across resolutions and across metrics, which is what lets a daily series derived from hourly rows
agree with one computed directly from the raw tables.

**The most recent bucket is partial.** The aggregates are created with
`timescaledb.materialized_only = false`, so a query transparently unions materialized rows with
live raw data past the refresh watermark. Data stays fresh, and the trailing bucket covers only the
elapsed part of its interval. Clients rendering a "current hour" point should treat it as
in-progress.

**Top-N windows cover whole hours.** Both ends are snapped to the hour, because two of the three
rankings read hourly aggregates and cannot resolve anything finer. A ranking therefore covers whole
hours up to the last completed one — the in-progress hour is excluded, and a row timestamped ahead
of the server clock cannot appear.

**Ranking ties are stable.** Every Top-N query orders by value descending and then by a
deterministic secondary key — contract ID, asset key, or transaction hash — so repeating the same
query over the same window always returns the same list in the same order.

**Ranges are bounded.** A request may span at most 10,000 buckets at the requested resolution;
beyond that the API answers `400` rather than serialising an unbounded response. Every realistic
dashboard range is far below the limit — 10,000 buckets is 416 days of hourly data.

**Resetting the database.** Truncating the raw tables does not empty the aggregates: their
materialized data and watermarks survive. Drop and re-apply migration `000014` — or re-run
`analytics-backfill` over the affected range — after wiping ingested data.

**Values are JSON numbers** (IEEE-754 doubles), matching the `number` type the explorer client
expects. Amounts far beyond 2^53 significant units would lose precision, which no current metric
approaches.

## Aggregation setup

Metrics are materialized hourly. Daily and weekly series are re-bucketed from the hourly
aggregate at query time, which keeps the schema small — a 30-day daily series reads 720 rows.

`active_accounts` is the exception. Distinct counts are not additive, so summing 24 hourly distinct
counts overstates a day's distinct accounts. It therefore has a dedicated aggregate per resolution,
each computed from the raw table.

| Aggregate | Grouped by | Feeds |
| --------- | ---------- | ----- |
| `analytics_tx_hourly` | bucket | `tx_count`, `fee_classic`, `fee_soroban` |
| `analytics_volume_hourly` | bucket | `tx_volume` |
| `analytics_new_accounts_hourly` | bucket | `new_accounts` |
| `analytics_asset_supply_hourly` | bucket, asset | `asset_supply` |
| `analytics_contract_activity_hourly` | bucket, contract | `contract_activity` |
| `analytics_asset_transfers_hourly` | bucket, asset | `asset_transfers` |
| `analytics_active_accounts_{hourly,daily,weekly}` | bucket | `active_accounts` |

`highest_fees` ranks individual transactions, which no aggregate can summarise, so it reads
`transactions` directly through an index on `(fee_charged DESC, created_at DESC)`.

Each aggregate carries a refresh policy sized to its bucket, always excluding the newest bucket so
the policy never competes with active writes:

| Aggregate | Runs every | Looks back | Excludes |
| --------- | ---------- | ---------- | -------- |
| hourly | 30 minutes | 30 days | the newest hour |
| `analytics_active_accounts_daily` | 1 hour | 90 days | the newest day |
| `analytics_active_accounts_weekly` | 6 hours | 1 year | the newest week |

Because the weekly aggregate excludes the week in progress, `active_accounts` at weekly resolution
computes the current week from the raw table on every request. That is the most expensive query the
API can serve; prefer daily resolution for recent activity.

### Backfilling historical data

The migration creates the aggregates empty (`WITH NO DATA`), so it applies instantly to a database
that already holds history. Populate them once afterwards:

```bash
./bin/indexer analytics-backfill                                  # everything already ingested
./bin/indexer analytics-backfill --from 2026-01-01T00:00:00Z      # from a point in time
./bin/indexer analytics-backfill --from=X --to=Y                  # a bounded repair
```

The command refreshes each aggregate over the requested range. TimescaleDB processes the refresh in
batches, each in its own transaction, so an interrupted run can simply be re-run — buckets already
materialized are skipped. It exits non-zero if any aggregate was not refreshed, so a deploy step can
gate on it.

An omitted `--to` ends at the present rather than staying open. An open upper bound would
materialize the bucket currently being written and advance the watermark past it; because watermarks
never move back, that bucket would then stay frozen at its partial value until a refresh policy
reached it again.

**Run this after any historical import.** `backfill` and `s3backfill` write rows below the
watermark, where real-time aggregation does not reach and the refresh policies — which look back 30
days — will never revisit them. Without a follow-up refresh those ledgers stay invisible to the API,
which reports them as an empty series rather than an error.

## References

- [Continuous aggregates](https://www.tigerdata.com/docs/use-timescale/latest/continuous-aggregates/about-continuous-aggregates)
- [`CREATE MATERIALIZED VIEW`](https://www.tigerdata.com/docs/api/latest/continuous-aggregates/create_materialized_view)
- [`refresh_continuous_aggregate`](https://www.tigerdata.com/docs/api/latest/continuous-aggregates/refresh_continuous_aggregate)
- [`add_continuous_aggregate_policy`](https://www.tigerdata.com/docs/api/latest/continuous-aggregates/add_continuous_aggregate_policy)
