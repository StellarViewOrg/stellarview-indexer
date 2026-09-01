#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CONFIG="$ROOT/benchmark/config/pubnet-range.json"
OUT_DIR="${BENCHMARK_OUTPUT_DIR:-$ROOT/benchmark/results}"
COMPOSE=(docker compose -f "$ROOT/benchmark/docker-compose.yml")
PG_URL="${BENCHMARK_POSTGRES_URL:-postgresql://benchmark:benchmark@localhost:55432/stellar_benchmark?sslmode=disable}"
CH_URL="${BENCHMARK_CLICKHOUSE_URL:-http://localhost:58123}"

need() { command -v "$1" >/dev/null || { printf 'missing dependency: %s\n' "$1" >&2; exit 2; }; }
json_value() { sed -n "s/.*\"$1\"[[:space:]]*:[[:space:]]*\([^,}]*\).*/\1/p" "$CONFIG" | tr -d '"'; }
start() { need docker; "${COMPOSE[@]}" up -d --wait; }
prepare() {
  need psql; need curl; need go
  (cd "$ROOT" && go run ./benchmark/cmd/manifest --config "$CONFIG")
  (cd "$ROOT" && DATABASE_URL="$PG_URL" go run ./cmd/indexer migrate >/dev/null)
  while ! curl -fsS "$CH_URL/?query=SELECT%201" >/dev/null; do sleep 2; done
  for f in "$ROOT"/benchmark/clickhouse/schema/*.sql; do curl -fsS --data-binary @"$f" "$CH_URL" >/dev/null; done
}
validate_config() {
  local start end count
  start=$(json_value startLedger); end=$(json_value endLedger); count=$(json_value ledgerCount)
  [[ "$start" =~ ^[0-9]+$ && "$end" =~ ^[0-9]+$ && "$count" =~ ^[0-9]+$ ]] || { echo 'invalid benchmark range' >&2; exit 1; }
  (( end >= start && count == end - start + 1 )) || { echo 'ledgerCount does not match inclusive range' >&2; exit 1; }
  [[ "$start" != 0 && "$end" != 0 ]] || { echo 'benchmark range contains placeholder zero' >&2; exit 1; }
}
load_postgres() {
  need go; validate_config
  local start end t0 t1
  start=$(json_value startLedger); end=$(json_value endLedger); t0=$(date -u +%s%3N)
  (cd "$ROOT" && DATABASE_URL="$PG_URL" go run ./cmd/indexer migrate)
  (cd "$ROOT" && DATABASE_URL="$PG_URL" WORKER_COUNT="${BENCHMARK_WORKERS:-1}" go run ./cmd/indexer s3backfill --start "$start" --end "$end")
  t1=$(date -u +%s%3N); mkdir -p "$OUT_DIR"
  printf '{"database":"postgres-timescaledb","startLedger":%s,"endLedger":%s,"processedLedgers":%s,"elapsedMs":%s,"ledgersPerSecond":%.6f}\n' "$start" "$end" "$((end-start+1))" "$((t1-t0))" "$(awk -v n=$((end-start+1)) -v ms=$((t1-t0)) 'BEGIN { print n/(ms/1000) }')" > "$OUT_DIR/ingestion-postgres.json"
  psql "$PG_URL" -c "COPY (SELECT sequence, hash, prev_hash, closed_at, total_coins, fee_pool, base_fee, base_reserve, max_tx_set_size, protocol_version, transaction_count, operation_count, successful_tx_count, failed_tx_count, tx_set_operation_count, coalesce(header_xdr, '') FROM ledgers WHERE sequence BETWEEN $start AND $end ORDER BY sequence) TO STDOUT WITH (FORMAT csv)" > "$OUT_DIR/ledgers.csv"
  psql "$PG_URL" -c "COPY (SELECT id, hash, ledger_sequence, application_order, account, account_muxed, account_muxed_id, account_sequence, fee_charged, max_fee, operation_count, memo_type, memo_text, memo_hash, status, is_soroban::int, soroban_resources::text, envelope_xdr, result_xdr, result_meta_xdr, fee_meta_xdr, created_at FROM transactions WHERE ledger_sequence BETWEEN $start AND $end ORDER BY id) TO STDOUT WITH (FORMAT csv)" > "$OUT_DIR/transactions.csv"
  psql "$PG_URL" -c "COPY (SELECT id, transaction_id, transaction_hash, application_order, type, type_name, source_account, asset_code, asset_issuer, amount, destination, contract_id, function_name, details::text, created_at FROM operations WHERE created_at >= (SELECT min(closed_at) FROM ledgers WHERE sequence=$start) AND created_at <= (SELECT max(closed_at) FROM ledgers WHERE sequence=$end) ORDER BY id) TO STDOUT WITH (FORMAT csv)" > "$OUT_DIR/operations.csv"
}
load_clickhouse() {
  need curl; local start end t0 t1
  start=$(json_value startLedger); end=$(json_value endLedger); t0=$(date -u +%s%3N)
  curl -fsS --data-binary @"$OUT_DIR/ledgers.csv" "$CH_URL/?query=INSERT%20INTO%20stellar_benchmark.ledgers%20(sequence,hash,prev_hash,closed_at,total_coins,fee_pool,base_fee,base_reserve,max_tx_set_size,protocol_version,transaction_count,operation_count,successful_tx_count,failed_tx_count,tx_set_operation_count,header_xdr)%20FORMAT%20CSV" >/dev/null
  curl -fsS --data-binary @"$OUT_DIR/transactions.csv" "$CH_URL/?query=INSERT%20INTO%20stellar_benchmark.transactions%20(id,hash,ledger_sequence,application_order,account,account_muxed,account_muxed_id,account_sequence,fee_charged,max_fee,operation_count,memo_type,memo_text,memo_hash,status,is_soroban,soroban_resources,envelope_xdr,result_xdr,result_meta_xdr,fee_meta_xdr,created_at)%20FORMAT%20CSV" >/dev/null
  curl -fsS --data-binary @"$OUT_DIR/operations.csv" "$CH_URL/?query=INSERT%20INTO%20stellar_benchmark.operations%20(id,transaction_id,transaction_hash,application_order,type,type_name,source_account,asset_code,asset_issuer,amount,destination,contract_id,function_name,details,created_at)%20FORMAT%20CSV" >/dev/null
  t1=$(date -u +%s%3N); printf '{"database":"clickhouse","startLedger":%s,"endLedger":%s,"processedLedgers":%s,"elapsedMs":%s,"ledgersPerSecond":%.6f}\n' "$start" "$end" "$((end-start+1))" "$((t1-t0))" "$(awk -v n=$((end-start+1)) -v ms=$((t1-t0)) 'BEGIN { print n/(ms/1000) }')" > "$OUT_DIR/ingestion-clickhouse.json"
}
measure_storage() {
  mkdir -p "$OUT_DIR"
  psql "$PG_URL" -At -c "SELECT json_build_object('database','postgres-timescaledb','databaseSizeBytes',pg_database_size(current_database()),'ledgersBytes',pg_total_relation_size('ledgers'),'transactionsBytes',pg_total_relation_size('transactions'),'operationsBytes',pg_total_relation_size('operations'),'ledgersRows',(SELECT count(*) FROM ledgers),'transactionsRows',(SELECT count(*) FROM transactions),'operationsRows',(SELECT count(*) FROM operations))" > "$OUT_DIR/storage-postgres.json"
  curl -fsS --data 'SELECT toJSONString(map("database","clickhouse","storedBytes",toString(sum(bytes_on_disk)),"uncompressedBytes",toString(sum(data_uncompressed_bytes)),"rows",toString(sum(rows)))) FROM system.parts WHERE active AND database="stellar_benchmark" FORMAT JSONEachRow' "$CH_URL" > "$OUT_DIR/storage-clickhouse.json"
}
queries() { mkdir -p "$OUT_DIR"; cp "$ROOT/benchmark/clickhouse/queries/workloads.sql" "$OUT_DIR/clickhouse-query-definitions.sql"; psql "$PG_URL" -At -c "SELECT json_build_object('database','postgres-timescaledb','rows', (SELECT count(*) FROM transactions), 'operations',(SELECT count(*) FROM operations), 'ledgers',(SELECT count(*) FROM ledgers))" > "$OUT_DIR/postgres-counts.json"; curl -fsS "$CH_URL/?query=SELECT%20toJSONString(map('database','clickhouse','rows',toString((SELECT%20count()%20FROM%20stellar_benchmark.transactions)),'operations',toString((SELECT%20count()%20FROM%20stellar_benchmark.operations)),'ledgers',toString((SELECT%20count()%20FROM%20stellar_benchmark.ledgers))))" > "$OUT_DIR/clickhouse-counts.json"; }
report() { validate_config; mkdir -p "$OUT_DIR"; date -u +%FT%TZ > "$OUT_DIR/generated-at.txt"; echo "raw results are in $OUT_DIR"; }
clean() { need docker; "${COMPOSE[@]}" down -v; rm -rf "$OUT_DIR"; }
case "${1:-help}" in up) start ;; prepare) prepare ;; load-postgres) load_postgres ;; load-clickhouse) load_clickhouse ;; query) queries ;; storage) measure_storage ;; report) report ;; run) start; prepare; load_postgres; load_clickhouse; measure_storage; queries; report ;; clean) clean ;; validate) validate_config ;; *) echo 'usage: run.sh {up|prepare|load-postgres|load-clickhouse|query|storage|report|run|clean|validate}' >&2; exit 2 ;; esac
