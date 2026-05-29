CREATE TABLE api_keys (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     UUID        NOT NULL REFERENCES tenants(id) ON DELETE RESTRICT,
    name          TEXT        NOT NULL,
    key_prefix    TEXT        NOT NULL,
    key_hash      BYTEA       NOT NULL UNIQUE,
    status        TEXT        NOT NULL DEFAULT 'active'
                              CHECK (status IN ('active', 'revoked')),
    last_used_at  TIMESTAMPTZ NULL,
    revoked_at    TIMESTAMPTZ NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_api_keys_tenant_id ON api_keys (tenant_id);
