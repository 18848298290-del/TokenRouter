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

// Turn-state ownership is remembered for the original key-session pair after
// state is relayed or committed, while staging leaves ownership unknown. These
// scenarios check provider switching, expired ownership, response-header clearing,
// and the Codex feature headers used alongside state forwarding.

// newTurnStateTestContext returns a Responses request and its output recorder.
// A nonempty session sets the underscore header, and a positive API-key ID
// installs the authenticated-key fixture; omitted values exercise missing seed inputs.
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

// TestOpenAICodexTurnStateSeed supplies both session-header spellings for one API key.
// The hyphenated header must take precedence, and a request lacking session
// identity must produce an empty ownership seed.
func TestOpenAICodexTurnStateSeed(t *testing.T) {
	c, _ := newTurnStateTestContext(t, 7, "session-a")
	require.Equal(t, "7\x00session-a", codexTurnStateSeed(c))
	c.Request.Header.Set("session-id", "session-hyphen")
	require.Equal(t, "7\x00session-hyphen", codexTurnStateSeed(c))

	withoutSession, _ := newTurnStateTestContext(t, 7, "")
	require.Empty(t, codexTurnStateSeed(withoutSession))
}

// TestRelayOpenAICodexTurnStateRecordsOnlyDeliveredState calls Relay with a state
// header and then an empty header set. The first call must install the
// header and remember its provider; the second must clear the response header.
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

	// A subsequent attempt can omit turn state. Clear the earlier response header
	// so the caller's output cannot expose a value supplied by that earlier attempt.
	svc.Relay(c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 43}}, http.Header{})
	require.Empty(t, c.Writer.Header().Get("X-Codex-Turn-State"))
}

// TestStagedOpenAICodexTurnStateOnlyRecordsAfterCommit stages a state header before
// selecting it for output. Staging must leave the owner absent, and Commit
// must then record the supplied provider under the same key-session seed.
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

// TestGuardOpenAICodexTurnStateEchoStripsOnlyForeignOrigin records a state owner
// and checks outgoing headers against matching and foreign providers. Matching
// ownership must retain state, foreign ownership must remove it, and expired
// ownership must permit the same echoed value to pass through.
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

	// Store an already expired TTL so the ownership check exercises expiry
	// immediately, without relying on a timer or waiting between provider checks.
	svc.Origins.Record("9\x00session-guard", 61, -time.Minute)
	expired := http.Header{"X-Codex-Turn-State": []string{"blob-c"}}
	svc.Guard(c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 62}}, expired)
	require.Equal(t, "blob-c", expired.Get("X-Codex-Turn-State"))
}

// TestWriteOpenAIPassthroughResponseHeadersRelaysTurnState copies a state header
// and then processes a response with no state. Output headers must expose
// the first value and remove it when the next source omits it.
func TestWriteOpenAIPassthroughResponseHeadersRelaysTurnState(t *testing.T) {
	destination := http.Header{}
	WriteOpenAIPassthroughResponseHeaders(destination, http.Header{"X-Codex-Turn-State": []string{"blob-p"}}, nil)
	require.Equal(t, "blob-p", destination.Get("X-Codex-Turn-State"))

	WriteOpenAIPassthroughResponseHeaders(destination, http.Header{"Content-Type": []string{"application/json"}}, nil)
	require.Empty(t, destination.Get("X-Codex-Turn-State"))
}

// TestWriteOpenAIPassthroughResponseHeaders_RelaysReasoningIncluded supplies the
// reasoning-included marker under the default response filter. Passthrough output
// must retain that marker so the caller can observe the upstream reasoning signal.
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
// OAuth fills a missing feature value while retaining an existing value; a
// marked native compaction request adds remote_compaction_v2 even for an API-key provider.
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
