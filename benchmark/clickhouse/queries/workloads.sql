-- Query semantics are the benchmark's logical contract. Replace fixture values
-- with the values emitted by benchmark/scripts/run.sh.
SELECT hash, ledger_sequence, account, status, created_at FROM stellar_benchmark.transactions WHERE hash = {tx_hash:String} LIMIT 1;
SELECT account, account_sequence, balance FROM stellar_benchmark.accounts WHERE account = {account_id:String} LIMIT 1;
SELECT hash, ledger_sequence, application_order, created_at FROM stellar_benchmark.transactions WHERE account = {account_id:String} ORDER BY created_at DESC, application_order DESC LIMIT {page_size:UInt32} OFFSET {offset:UInt32};
SELECT toStartOfHour(created_at) AS bucket, count() FROM stellar_benchmark.transactions WHERE created_at >= {from:DateTime} AND created_at < {to:DateTime} GROUP BY bucket ORDER BY bucket;
SELECT toStartOfHour(created_at) AS bucket, count() FROM stellar_benchmark.operations WHERE created_at >= {from:DateTime} AND created_at < {to:DateTime} GROUP BY bucket ORDER BY bucket;
SELECT coalesce(asset_code, 'native') AS asset, count() AS operation_count FROM stellar_benchmark.operations WHERE created_at >= {from:DateTime} AND created_at < {to:DateTime} GROUP BY asset ORDER BY operation_count DESC LIMIT {limit:UInt32};

-- ReplacingMergeTree correctness comparison:
SELECT count() AS raw_rows FROM stellar_benchmark.transactions WHERE ledger_sequence BETWEEN {start:UInt32} AND {end:UInt32};
SELECT count() AS deduplicated_rows FROM stellar_benchmark.transactions FINAL WHERE ledger_sequence BETWEEN {start:UInt32} AND {end:UInt32};
