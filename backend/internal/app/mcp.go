package app

import (
	"context"
	"database/sql"
	"net/url"
	"slices"
	"strings"

	"github.com/redis/go-redis/v9"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	identitypostgres "github.com/TokenFlux/TokenRouter/internal/identity/postgres"
	redisinfra "github.com/TokenFlux/TokenRouter/internal/infra/redis"
	mcphttp "github.com/TokenFlux/TokenRouter/internal/mcp/httpapi"
	mcpredis "github.com/TokenFlux/TokenRouter/internal/mcp/rediscache"
	"github.com/TokenFlux/TokenRouter/internal/settings"
)

// mcp.go assembles dedicated administrator credentials, shared temporary state,
// and the MCP transport. SQL grants are read directly for authorization; Redis
// holds expiring confirmations and encrypted file results. Browser origins come
// from explicit CORS entries and configured public site URLs, preventing an
// arbitrary Host header from establishing a trusted browser origin.

// provideMCPTokens binds token storage to live account reads and durable encryption.
// An unconfigured startup key is ephemeral, so issuance and credential copying
// remain unavailable until the deployment supplies its fixed TOTP encryption key.
func provideMCPTokens(db *sql.DB, users *identity.UserService, cipher identity.SecretEncryptor, cfg *config.Config) *identity.MCPTokenService {
	if cfg == nil || !cfg.Totp.EncryptionKeyConfigured {
		cipher = nil
	}
	return identity.NewMCPTokenService(identitypostgres.NewMCPTokenStore(db), users, cipher)
}

// provideMCPHTTP constructs the management transport using shared identity resources.
// Origin reads fail closed for unavailable dynamic URLs; explicit configured
// origins remain usable. Wildcard CORS entries never authorize MCP browser origins.
func provideMCPHTTP(tokens *identity.MCPTokenService, client *redis.Client, cipher identity.SecretEncryptor, totp *identity.TotpService, users *identity.UserService, identitySettings *identity.RuntimeSettings, store *settings.Store, cfg *config.Config) *mcphttp.Handler {
	origins := func(ctx context.Context) []string {
		allowed := slices.Clone(cfg.CORS.AllowedOrigins)
		for _, key := range []string{"api_base_url", "frontend_url"} {
			value, err := store.GetValue(ctx, key)
			if err != nil {
				// Missing public URL configuration grants no extra browser origin;
				// native MCP clients without Origin still authenticate normally with their dedicated token.
				continue
			}
			parsed, err := url.Parse(strings.TrimSpace(value))
			if err == nil && parsed.Host != "" && parsed.User == nil && (parsed.Scheme == "https" || parsed.Scheme == "http") {
				allowed = append(allowed, parsed.Scheme+"://"+parsed.Host)
			}
		}
		return allowed
	}
	return mcphttp.NewHandler(tokens, mcpredis.NewState(client, cipher), totp, users, identitySettings, mcphttp.Options{Origins: origins, BrowserBaseURL: func(ctx context.Context) string { value, _ := store.GetValue(ctx, "frontend_url"); return value }, Limiter: redisinfra.NewFixedWindowLimiter(client, "mcp:rate:")})
}
