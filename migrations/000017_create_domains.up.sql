CREATE TABLE domains (
    node                VARCHAR(64) PRIMARY KEY,
    name                VARCHAR(256) NOT NULL DEFAULT '',
    tld                 VARCHAR(16) NOT NULL DEFAULT '',
    label               VARCHAR(240) NOT NULL DEFAULT '',
    owner               VARCHAR(56) NOT NULL DEFAULT '',
    resolved_address    VARCHAR(56) NOT NULL DEFAULT '',
    target_type         VARCHAR(16) NOT NULL DEFAULT '',
    registered_at       TIMESTAMPTZ,
    expires_at          TIMESTAMPTZ,
    status              VARCHAR(16) NOT NULL DEFAULT 'active',
    last_event_ledger   BIGINT NOT NULL,
    last_event_tx       CHAR(64),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX idx_domains_name ON domains (name) WHERE name <> '';
CREATE INDEX idx_domains_resolved ON domains (resolved_address) WHERE resolved_address <> '';
CREATE INDEX idx_domains_owner ON domains (owner) WHERE owner <> '';
CREATE INDEX idx_domains_status ON domains (status);
CREATE INDEX idx_domains_expires ON domains (expires_at);

CREATE TABLE domain_events (
    id                  BIGSERIAL PRIMARY KEY,
    node                VARCHAR(64) NOT NULL,
    name                VARCHAR(256) NOT NULL DEFAULT '',
    event_type          VARCHAR(32) NOT NULL,
    owner               VARCHAR(56),
    resolved_address    VARCHAR(56),
    expires_at          TIMESTAMPTZ,
    transaction_hash    CHAR(64) NOT NULL,
    ledger_sequence     BIGINT NOT NULL,
    details             JSONB,
    created_at          TIMESTAMPTZ NOT NULL,
    UNIQUE (transaction_hash, node, event_type)
);

CREATE INDEX idx_domain_events_node ON domain_events (node, created_at DESC);
CREATE INDEX idx_domain_events_name ON domain_events (name, created_at DESC) WHERE name <> '';
CREATE INDEX idx_domain_events_ledger ON domain_events (ledger_sequence);
