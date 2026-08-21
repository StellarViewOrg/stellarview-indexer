CREATE TABLE contract_verifications (
    id                  UUID PRIMARY KEY,
    contract_id         VARCHAR(56) NOT NULL,
    network             VARCHAR(16) NOT NULL,
    status              VARCHAR(16) NOT NULL DEFAULT 'queued',
    reason              TEXT,
    match               BOOLEAN,
    wasm_hash           CHAR(64),
    expected_wasm_hash  CHAR(64),
    source_type         VARCHAR(16) NOT NULL,
    archive_name        VARCHAR(256),
    archive_sha256      CHAR(64),
    git_repository      VARCHAR(512),
    git_reference       VARCHAR(256),
    git_commit          VARCHAR(64),
    toolchain_requested JSONB,
    toolchain_actual    JSONB,
    build_spec          JSONB,
    build_logs          TEXT,
    build_duration_ms   BIGINT,
    submitter_ip        VARCHAR(64),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    started_at          TIMESTAMPTZ,
    completed_at        TIMESTAMPTZ
);

CREATE INDEX idx_verification_contract ON contract_verifications (contract_id, created_at DESC);
CREATE INDEX idx_verification_pending ON contract_verifications (created_at)
    WHERE status IN ('queued', 'building');
CREATE INDEX idx_verification_wasm ON contract_verifications (wasm_hash)
    WHERE wasm_hash IS NOT NULL;

CREATE TABLE verified_sources (
    wasm_hash       CHAR(64) PRIMARY KEY,
    verification_id UUID NOT NULL UNIQUE,
    contract_id     VARCHAR(56),
    network         VARCHAR(16) NOT NULL,
    file_count      INTEGER NOT NULL,
    total_size      BIGINT NOT NULL,
    verified_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE source_files (
    wasm_hash   CHAR(64) NOT NULL,
    path        TEXT NOT NULL,
    size        INTEGER NOT NULL,
    content     BYTEA NOT NULL,
    PRIMARY KEY (wasm_hash, path)
);
