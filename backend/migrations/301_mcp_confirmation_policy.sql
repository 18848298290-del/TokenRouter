ALTER TABLE mcp_access_tokens
    ADD COLUMN skip_browser_confirmation BOOLEAN NOT NULL DEFAULT FALSE;
