package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/egress"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

func newTurnStateTestContext(t *testing.T, apiKeyID int64, sessionID string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	if sessionID != "" {
		c.Request.Header.Set("session_id", sessionID)
	}
	if apiKeyID > 0 {
		c.Set("api_key", &apikey.APIKey{ID: apiKeyID})
	}
	return c, recorder
}

func TestOpenAICodexTurnStateSeed(t *testing.T) {
	c, _ := newTurnStateTestContext(t, 7, "session-a")
	require.Equal(t, "7\x00session-a", codexTurnStateSeed(c))
	c.Request.Header.Set("session-id", "session-hyphen")
	require.Equal(t, "7\x00session-hyphen", codexTurnStateSeed(c))

	withoutSession, _ := newTurnStateTestContext(t, 7, "")
	require.Empty(t, codexTurnStateSeed(withoutSession))
}

func TestRelayOpenAICodexTurnStateRecordsOnlyDeliveredState(t *testing.T) {
	svc := &CodexTurnStateHeaders{Origins: session.NewCodexTurnOrigins(time.Now), TTL: func() time.Duration { return time.Hour }}
	c, _ := newTurnStateTestContext(t, 7, "session-delivered")
	upstream := http.Header{"X-Codex-Turn-State": []string{"blob-a"}}

	svc.Relay(c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 42}}, upstream)
	require.Equal(t, "blob-a", c.Writer.Header().Get("X-Codex-Turn-State"))
	origin, ok := svc.Origins.Owner("7\x00session-delivered")
	require.True(t, ok)
	require.NotZero(t, origin)
	require.Equal(t, int64(42), origin)

	// An upstream response with no state clears the previous attempt's header.
	svc.Relay(c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 43}}, http.Header{})
	require.Empty(t, c.Writer.Header().Get("X-Codex-Turn-State"))
}

func TestStagedOpenAICodexTurnStateOnlyRecordsAfterCommit(t *testing.T) {
	svc := &CodexTurnStateHeaders{Origins: session.NewCodexTurnOrigins(time.Now), TTL: func() time.Duration { return time.Hour }}
	c, _ := newTurnStateTestContext(t, 8, "session-staged")
	var staged http.Header
	StageCodexTurnState(&staged, http.Header{"X-Codex-Turn-State": []string{"blob-b"}})
	require.Equal(t, "blob-b", staged.Get("X-Codex-Turn-State"))
	_, exists := svc.Origins.Owner("8\x00session-staged")
	require.False(t, exists)

	svc.Commit(c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 52}}, staged)
	origin, exists := svc.Origins.Owner("8\x00session-staged")
	require.True(t, exists)
	require.NotZero(t, origin)
	require.Equal(t, int64(52), origin)
}

func TestGuardOpenAICodexTurnStateEchoStripsOnlyForeignOrigin(t *testing.T) {
	svc := &CodexTurnStateHeaders{Origins: session.NewCodexTurnOrigins(time.Now), TTL: func() time.Duration { return time.Hour }}
	c, _ := newTurnStateTestContext(t, 9, "session-guard")
	svc.Relay(c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 61}}, http.Header{"X-Codex-Turn-State": []string{"blob-c"}})

	sameProvider := http.Header{"X-Codex-Turn-State": []string{"blob-c"}}
	svc.Guard(c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 61}}, sameProvider)
	require.Equal(t, "blob-c", sameProvider.Get("X-Codex-Turn-State"))

	foreignProvider := http.Header{"X-Codex-Turn-State": []string{"blob-c"}}
	svc.Guard(c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 62}}, foreignProvider)
	require.Empty(t, foreignProvider.Get("X-Codex-Turn-State"))

	svc.Origins.Record("9\x00session-guard", 61, -time.Minute)
	expired := http.Header{"X-Codex-Turn-State": []string{"blob-c"}}
	svc.Guard(c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 62}}, expired)
	require.Equal(t, "blob-c", expired.Get("X-Codex-Turn-State"))
}

func TestWriteOpenAIPassthroughResponseHeadersRelaysTurnState(t *testing.T) {
	destination := http.Header{}
	WriteOpenAIPassthroughResponseHeaders(destination, http.Header{"X-Codex-Turn-State": []string{"blob-p"}}, nil)
	require.Equal(t, "blob-p", destination.Get("X-Codex-Turn-State"))

	WriteOpenAIPassthroughResponseHeaders(destination, http.Header{"Content-Type": []string{"application/json"}}, nil)
	require.Empty(t, destination.Get("X-Codex-Turn-State"))
}

func TestWriteOpenAIPassthroughResponseHeaders_RelaysReasoningIncluded(t *testing.T) {
	dst := http.Header{}
	src := http.Header{}
	src.Set("X-Reasoning-Included", "1")

	WriteOpenAIPassthroughResponseHeaders(
		dst,
		src,
		egress.CompileHeaderFilter(egress.ResponseHeaderOptions{}),
	)
	require.Equal(t, "1", dst.Get("X-Reasoning-Included"))
}

// TestApplyOpenAICodexBetaFeatures checks OAuth defaults, existing headers, and explicit compaction requests.
func TestApplyOpenAICodexBetaFeatures(t *testing.T) {
	oauthProvider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}}
	apiKeyProvider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 2, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}
	c, _ := newTurnStateTestContext(t, 1, "session-beta")

	headers := http.Header{}
	ApplyOpenAICodexBetaFeatures(c, oauthProvider != nil && oauthProvider.View().IsOpenAIOAuthLike(), headers)
	require.Equal(t, "remote_compaction_v2", headers.Get("X-Codex-Beta-Features"))

	headers.Set("X-Codex-Beta-Features", "other_feature")
	ApplyOpenAICodexBetaFeatures(c, oauthProvider != nil && oauthProvider.View().IsOpenAIOAuthLike(), headers)
	require.Equal(t, "other_feature", headers.Get("X-Codex-Beta-Features"))

	MarkOpenAINativeCompactionV2(c)
	ApplyOpenAICodexBetaFeatures(c, apiKeyProvider != nil && apiKeyProvider.View().IsOpenAIOAuthLike(), headers)
	require.Contains(t, headers.Get("X-Codex-Beta-Features"), "remote_compaction_v2")
}
