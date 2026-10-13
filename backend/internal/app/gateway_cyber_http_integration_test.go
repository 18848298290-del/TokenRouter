//go:build integration

package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	gatewayredis "github.com/TokenFlux/TokenRouter/internal/gateway/rediscache"
	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
	"github.com/TokenFlux/TokenRouter/internal/moderation"
	"github.com/TokenFlux/TokenRouter/internal/settings"
	"github.com/TokenFlux/TokenRouter/internal/testutil/rediscontainer"
)

// Cyber session blocks written through application assembly must be visible to
// HTTP lookup through the same Redis keys. The scenario checks explicit-session
// expiry and transcript scope, including compatibility across client versions
// and isolation between API-Key identities.

// cyberRuntimeSettingsFixture supplies enabled blocking and a sixty-second
// lifetime.
type cyberRuntimeSettingsFixture struct{}

// GetValue returns enabled Cyber blocking or its sixty-second lifetime by key.
// Unrecognized settings return ErrSettingNotFound so the production runtime
// reader applies its usual missing-setting behavior.
func (cyberRuntimeSettingsFixture) GetValue(_ context.Context, key string) (string, error) {
	switch key {
	case moderation.SettingKeyCyberSessionBlockEnabled:
		return "true", nil
	case moderation.SettingKeyCyberSessionBlockTTLSeconds:
		return "60", nil
	default:
		return "", settings.ErrSettingNotFound
	}
}

// TestNativeCyberSessionBindingOnRedis writes explicit and transcript blocks
// through the assembled service. HTTP lookup must find them in Redis, retain
// the configured TTL bound, accept the tested version change, and isolate
// another Key identity.
func TestNativeCyberSessionBindingOnRedis(t *testing.T) {
	client := rediscontainer.New(t)
	cache := gatewayredis.NewGatewayCache(client)
	runtime := moderation.NewRuntimeSettings(cyberRuntimeSettingsFixture{}, settings.ErrSettingNotFound)
	core := provideCyberBlocks(cache, runtime)
	ctx := t.Context()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header.Set("session_id", "native-cyber-session")
	explicit := gatewayhttp.CyberSessionExplicitBlockKey(91, c, nil)
	require.NotEmpty(t, explicit)
	require.Empty(t, gatewayhttp.FindCyberSessionForRequest(ctx, core, 91, c, nil, "203.0.113.2", "Codex CLI 1.2.3"))
	core.MarkCyberSessionBlocked(ctx, "", []string{explicit})
	require.Equal(t, explicit, gatewayhttp.FindCyberSessionForRequest(ctx, core, 91, c, nil, "203.0.113.2", "Codex CLI 1.2.3"))
	ttl, err := client.TTL(ctx, "cyber_session_block:"+explicit).Result()
	require.NoError(t, err)
	require.Greater(t, ttl, time.Duration(0))
	require.LessOrEqual(t, ttl, time.Minute)

	c.Request.Header.Del("session_id")
	history := []byte(`{"messages":[{"role":"user","content":"start"},{"role":"assistant","content":"ready"},{"role":"user","content":"question"}]}`)
	keys := session.CyberSessionTranscriptBlockKeys(92, history)
	require.NotEmpty(t, keys)
	require.Empty(t, gatewayhttp.FindCyberSessionForRequest(ctx, core, 92, c, history, "203.0.113.3", "Codex CLI 1.2.3"))
	scope := session.CyberSessionScopeKey(92, "203.0.113.3", "Codex CLI 1.2.3")
	core.MarkCyberSessionBlocked(ctx, scope, keys)
	require.NotEmpty(t, gatewayhttp.FindCyberSessionForRequest(ctx, core, 92, c, history, "203.0.113.3", "Codex CLI 1.2.4"))
	active, err := client.Exists(ctx, "cyber_session_scope:"+scope).Result()
	require.NoError(t, err)
	require.EqualValues(t, 1, active)
	require.Empty(t, gatewayhttp.FindCyberSessionForRequest(ctx, core, 93, c, history, "203.0.113.3", "Codex CLI 1.2.4"))
}
