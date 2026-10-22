CREATE TABLE mcp_access_tokens (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name VARCHAR(120) NOT NULL,
    token_hash CHAR(64) NOT NULL UNIQUE,
    token_ciphertext TEXT NOT NULL,
    token_prefix VARCHAR(24) NOT NULL,
    permissions JSONB NOT NULL DEFAULT '[]'::jsonb,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    expires_at TIMESTAMPTZ,
    last_used_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revoked_at TIMESTAMPTZ,
    CONSTRAINT mcp_access_tokens_permissions_array CHECK (jsonb_typeof(permissions) = 'array')
);
CREATE INDEX mcp_access_tokens_user_id_idx ON mcp_access_tokens(user_id, id);
