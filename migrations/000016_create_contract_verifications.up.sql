-- Reproducible source verification for deployed contracts (issue #36).
--
-- A verification record is keyed by wasm_hash rather than contract_id: two
-- different contract_id deployments that share identical bytecode share one
-- verification record and one source tree, matching the acceptance
-- criterion that identical bytecode across multiple deployments reuses a
-- single record. contract_id is retained on the submission for traceability
-- (who submitted verification for which deployment) but is not part of the
-- lookup key.
CREATE TABLE contract_verifications (
    id                  BIGSERIAL PRIMARY KEY,
    wasm_hash           CHAR(64) NOT NULL REFERENCES contract_code(wasm_hash),
    contract_id         VARCHAR(56) NOT NULL,
    network              VARCHAR(16) NOT NULL,
    repository_url      VARCHAR(1024),
    git_ref             VARCHAR(256),
    git_commit          VARCHAR(64),
    rust_version        VARCHAR(64),
    soroban_sdk_version VARCHAR(64),
    build_profile       JSONB,
    -- pending: submitted, awaiting the sandboxed build pipeline (not yet
    -- implemented, tracked as follow-up work for this issue).
    -- verified / mismatch / failed are the terminal states the build
    -- pipeline will transition a record into.
    status              VARCHAR(16) NOT NULL DEFAULT 'pending',
    computed_wasm_hash  CHAR(64),
    failure_reason      TEXT,
    build_log           TEXT,
    submitted_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at        TIMESTAMPTZ,
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_contract_verifications_status
        CHECK (status IN ('pending', 'verified', 'mismatch', 'failed'))
);

CREATE INDEX idx_contract_verifications_wasm_hash ON contract_verifications (wasm_hash, submitted_at DESC);
CREATE INDEX idx_contract_verifications_contract_id ON contract_verifications (contract_id, submitted_at DESC);

-- Verified source, stored per-file so the explorer's source browser can
-- fetch a single file without pulling the whole tree. Content is stored as
-- text (submitted source is source code, not arbitrary binary); size_bytes
-- is persisted alongside so the tree listing can show file sizes without
-- reading content back out.
--
-- Keyed by verification_id (not just wasm_hash) so each submission's file
-- snapshot is independently retrievable: a second submission against the
-- same wasm_hash (e.g. a correction after a mismatch) creates a new
-- contract_verifications row and its own set of source rows, rather than
-- overwriting the files belonging to an earlier, already-completed
-- verification record.
CREATE TABLE contract_verification_sources (
    id              BIGSERIAL PRIMARY KEY,
    verification_id BIGINT NOT NULL REFERENCES contract_verifications(id),
    wasm_hash       CHAR(64) NOT NULL REFERENCES contract_code(wasm_hash),
    file_path       VARCHAR(1024) NOT NULL,
    content         TEXT NOT NULL,
    size_bytes      INTEGER NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (verification_id, file_path)
);

CREATE INDEX idx_contract_verification_sources_wasm_hash ON contract_verification_sources (wasm_hash);
CREATE INDEX idx_contract_verification_sources_verification_id ON contract_verification_sources (verification_id);
