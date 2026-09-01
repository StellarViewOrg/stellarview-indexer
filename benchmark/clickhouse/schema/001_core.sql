CREATE DATABASE IF NOT EXISTS stellar_benchmark;

CREATE TABLE IF NOT EXISTS stellar_benchmark.ledgers
(
    sequence UInt32,
    hash FixedString(64),
    prev_hash FixedString(64),
    closed_at DateTime64(3, 'UTC'),
    total_coins Int64,
    fee_pool Int64,
    base_fee Int32,
    base_reserve Int32,
    max_tx_set_size Int32,
    protocol_version Int32,
    transaction_count Int32,
    operation_count Int32,
    successful_tx_count Int32,
    failed_tx_count Int32,
    tx_set_operation_count Nullable(Int32),
    header_xdr String,
    version UInt64 DEFAULT 1
)
ENGINE = ReplacingMergeTree(version)
ORDER BY (sequence, closed_at)
PARTITION BY toYYYYMM(closed_at);

CREATE TABLE IF NOT EXISTS stellar_benchmark.transactions
(
    id UInt64,
    hash FixedString(64),
    ledger_sequence UInt32,
    application_order Int32,
    account String,
    account_muxed Nullable(String),
    account_muxed_id Nullable(Int64),
    account_sequence Int64,
    fee_charged Int64,
    max_fee Int64,
    operation_count Int32,
    memo_type Int16,
    memo_text Nullable(String),
    memo_hash Nullable(String),
    status Int16,
    is_soroban UInt8,
    soroban_resources Nullable(String),
    envelope_xdr String,
    result_xdr String,
    result_meta_xdr Nullable(String),
    fee_meta_xdr Nullable(String),
    created_at DateTime64(3, 'UTC'),
    version UInt64 DEFAULT 1
)
ENGINE = ReplacingMergeTree(version)
ORDER BY (hash, created_at, id)
PARTITION BY toYYYYMM(created_at);

CREATE TABLE IF NOT EXISTS stellar_benchmark.operations
(
    id UInt64,
    transaction_id UInt64,
    transaction_hash FixedString(64),
    application_order Int32,
    type Int16,
    type_name String,
    source_account Nullable(String),
    asset_code Nullable(String),
    asset_issuer Nullable(String),
    amount Nullable(Decimal(38, 7)),
    destination Nullable(String),
    contract_id Nullable(String),
    function_name Nullable(String),
    details String,
    created_at DateTime64(3, 'UTC'),
    version UInt64 DEFAULT 1
)
ENGINE = ReplacingMergeTree(version)
ORDER BY (transaction_hash, application_order, created_at, id)
PARTITION BY toYYYYMM(created_at);

CREATE TABLE IF NOT EXISTS stellar_benchmark.token_events
(
    event_type Int16,
    event_type_name String,
    from_address Nullable(String),
    from_muxed Nullable(String),
    to_address Nullable(String),
    to_muxed Nullable(String),
    to_muxed_id Nullable(Int64),
    asset_type Int16,
    asset_code Nullable(String),
    asset_issuer Nullable(String),
    asset_contract_id Nullable(String),
    amount Decimal(38, 0),
    amount_formatted Nullable(String),
    transaction_hash FixedString(64),
    ledger_sequence UInt32,
    operation_index Nullable(Int32),
    created_at DateTime64(3, 'UTC'),
    version UInt64 DEFAULT 1
)
ENGINE = MergeTree
ORDER BY (transaction_hash, ledger_sequence, created_at);

CREATE TABLE IF NOT EXISTS stellar_benchmark.contract_events
(
    contract_id String,
    transaction_hash FixedString(64),
    ledger_sequence UInt32,
    type Int16,
    topic_1 Nullable(String), topic_2 Nullable(String), topic_3 Nullable(String), topic_4 Nullable(String),
    topics_xdr String, value_xdr String,
    topics_decoded Nullable(String), value_decoded Nullable(String),
    created_at DateTime64(3, 'UTC'),
    version UInt64 DEFAULT 1
)
ENGINE = MergeTree
ORDER BY (contract_id, created_at, transaction_hash);
