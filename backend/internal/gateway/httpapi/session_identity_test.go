package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cespare/xxhash/v2"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/gateway/clientmeta"
	"github.com/TokenFlux/TokenRouter/internal/gateway/media"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	gatewaysession "github.com/TokenFlux/TokenRouter/internal/gateway/session"
	sessiontestkit "github.com/TokenFlux/TokenRouter/internal/gateway/session/testkit"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

func guardianAffinityTestContext(t *testing.T, model, subagent, parentHeader, metadata string) context.Context {
	t.Helper()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", nil)
	c.Request.Header.Set(clientmeta.OpenAISubagentHeader, subagent)
	if parentHeader != "" {
		c.Request.Header.Set(clientmeta.CodexParentThreadIDHeader, parentHeader)
	}
	if metadata != "" {
		c.Request.Header.Set(clientmeta.CodexTurnMetadataHeader, metadata)
	}
	return WithOpenAIGuardianParentAffinity(context.Background(), c, nil, model)
}

func TestWithOpenAIGuardianParentAffinityRequiresUnambiguousReviewLineage(t *testing.T) {
	parentID := "11111111-1111-4111-8111-111111111111"
	wantHash, _ := scheduler.DeriveSessionHashes(parentID)

	for _, subagent := range []string{"guardian", "review", "GUARDIAN"} {
		t.Run(subagent, func(t *testing.T) {
			ctx := guardianAffinityTestContext(t, clientmeta.CodexAutoReviewModel, subagent, parentID, `{"parent_thread_id":"`+parentID+`"}`)
			affinity, ok := requeststate.GuardianParentAffinityFromContext(ctx)
			require.True(t, ok)
			require.Equal(t, wantHash, affinity.CurrentSessionHash)
		})
	}

	t.Run("metadata only", func(t *testing.T) {
		ctx := guardianAffinityTestContext(t, clientmeta.CodexAutoReviewModel, "guardian", "", `{"parent_thread_id":"`+parentID+`"}`)
		_, ok := requeststate.GuardianParentAffinityFromContext(ctx)
		require.True(t, ok)
	})

	t.Run("websocket envelope metadata", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodGet, "/openai/v1/responses", nil)
		body := []byte(`{"type":"response.create","response":{"model":"codex-auto-review","client_metadata":{"x-codex-turn-metadata":"{\"parent_thread_id\":\"` + parentID + `\",\"subagent_kind\":\"guardian\"}"}}}`)
		ctx := WithOpenAIGuardianParentAffinity(context.Background(), c, body, clientmeta.CodexAutoReviewModel)
		affinity, ok := requeststate.GuardianParentAffinityFromContext(ctx)
		require.True(t, ok)
		require.Equal(t, wantHash, affinity.CurrentSessionHash)
	})

	for name, ctx := range map[string]context.Context{
		"ordinary model":       guardianAffinityTestContext(t, "gpt-5.6-sol", "guardian", parentID, ""),
		"ordinary subagent":    guardianAffinityTestContext(t, clientmeta.CodexAutoReviewModel, "collab_spawn", parentID, ""),
		"missing parent":       guardianAffinityTestContext(t, clientmeta.CodexAutoReviewModel, "guardian", "", ""),
		"conflicting lineage":  guardianAffinityTestContext(t, clientmeta.CodexAutoReviewModel, "guardian", parentID, `{"parent_thread_id":"different-parent"}`),
		"conflicting subagent": guardianAffinityTestContext(t, clientmeta.CodexAutoReviewModel, "guardian", parentID, `{"parent_thread_id":"`+parentID+`","subagent_kind":"collab_spawn"}`),
	} {
		t.Run(name, func(t *testing.T) {
			_, ok := requeststate.GuardianParentAffinityFromContext(ctx)
			require.False(t, ok)
		})
	}
}

func TestGrokConversationHeaderIsScopedToGrokRequestScheduling(t *testing.T) {
	body := []byte(`{"model":"grok","prompt_cache_key":"body-session","input":"hi"}`)

	grokContext := newGrokCacheTestContext(601)
	grokContext.Request.Header.Set(GrokConversationIDHeader, "native-grok-session")
	require.Equal(t, "native-grok-session", ExplicitOpenAIRequestSessionID(grokContext, body))

	openAIContext := newGrokCacheTestContext(601)
	SetOpsSelectedProvider(openAIContext, 1, capability.PlatformOpenAI)
	openAIContext.Set("api_key", &apikey.APIKey{ID: 601, Group: &routing.Group{}})
	openAIContext.Request.Header.Set(GrokConversationIDHeader, "must-be-ignored")
	require.Equal(t, "body-session", ExplicitOpenAIRequestSessionID(openAIContext, body))

	withoutGrokHeader := newGrokCacheTestContext(601)
	SetOpsSelectedProvider(withoutGrokHeader, 1, capability.PlatformOpenAI)
	withoutGrokHeader.Set("api_key", &apikey.APIKey{ID: 601, Group: &routing.Group{}})
	require.Equal(t, GenerateOpenAISessionHash(withoutGrokHeader, body), GenerateOpenAISessionHash(openAIContext, body))
}

func TestGrokMediaVideoRequestBindingIsScopedToUserAndAPIKey(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/videos/video-request-123", nil)
	c.Request.Header.Set("session_id", "shared-client-session")
	groupID := int64(7)
	cache := &sessiontestkit.StickyCache{}
	tasks := media.NewVideoTasks(cache, nil, media.VideoOptions{})
	const userID int64 = 41
	const apiKeyID int64 = 51
	require.NotEmpty(t, GenerateExplicitOpenAISessionHash(c, nil))
	ctx := c.Request.Context()

	hash := media.GrokMediaVideoRequestSessionHash("video-request-123", userID, apiKeyID)
	require.NotEmpty(t, hash)
	require.NoError(t, tasks.BindGrokMediaVideoRequestProvider(ctx, &groupID, "video-request-123", userID, apiKeyID, 63))

	providerID, err := tasks.ResolveGrokMediaVideoRequestProvider(ctx, &groupID, "video-request-123", userID, apiKeyID)
	require.NoError(t, err)
	require.Equal(t, int64(63), providerID)

	providerID, err = tasks.ResolveGrokMediaVideoRequestProvider(ctx, &groupID, "video-request-123", userID+1, apiKeyID)
	require.Error(t, err)
	require.Zero(t, providerID)

	providerID, err = tasks.ResolveGrokMediaVideoRequestProvider(ctx, &groupID, "video-request-123", userID, apiKeyID+1)
	require.Error(t, err)
	require.Zero(t, providerID)
}

func TestResolveOpenAIMessagesMetadataSession_DoesNotDerivePromptCacheKey(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4-5","metadata":{"user_id":"claude-code-session"},"messages":[{"role":"user","content":"hello"}]}`)

	sessionHash, promptCacheKey := metadataSessionForTest(nil, "", "", "claude-sonnet-4-5", body)

	require.NotEmpty(t, sessionHash)
	require.Empty(t, promptCacheKey)
}

func TestResolveOpenAIMessagesMetadataSession_PreservesExplicitPromptCacheKey(t *testing.T) {
	body := []byte(`{"metadata":{"user_id":"claude-code-session"}}`)

	sessionHash, promptCacheKey := metadataSessionForTest(nil, "", "explicit-cache", "claude-sonnet-4-5", body)

	require.NotEmpty(t, sessionHash)
	require.Equal(t, "explicit-cache", promptCacheKey)
}

func TestResolveOpenAIMessagesMetadataSession_ClaudeCodeHeaderOverridesContentFallback(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("X-Claude-Code-Session-Id", "claude-session-001")

	body1 := []byte(`{"model":"gpt-5.6-sol","system":"parent","messages":[{"role":"user","content":"parent task"}]}`)
	body2 := []byte(`{"model":"gpt-5.6-sol","system":"subagent","messages":[{"role":"user","content":"child task"}]}`)

	contentHash1 := GenerateOpenAISessionHash(c, body1)
	contentHash2 := GenerateOpenAISessionHash(c, body2)
	require.NotEqual(t, contentHash1, contentHash2, "different bodies should prove the content fallback differs")

	hash1, cacheKey1 := metadataSessionForTest(c, contentHash1, "", "gpt-5.6-sol", body1)
	hash2, cacheKey2 := metadataSessionForTest(c, contentHash2, "", "gpt-5.6-sol", body2)
	want, _ := scheduler.DeriveSessionHashes("claude-session-001")
	require.Equal(t, want, hash1)
	require.Equal(t, hash1, hash2, "the same Claude Code session must keep one sticky provider across changed turn bodies")
	require.Empty(t, cacheKey1, "routing-only fix must not create an upstream prompt cache key")
	require.Empty(t, cacheKey2, "routing-only fix must not create an upstream prompt cache key")
}

func TestResolveOpenAIMessagesMetadataSession_OpenAISignalWinsOverClaudeHeader(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("X-Claude-Code-Session-Id", "claude-session-001")

	hash, cacheKey := metadataSessionForTest(c, "content-hash", "explicit-openai-session", "gpt-5.6-sol", []byte(`{"metadata":{"user_id":"opaque"}}`))
	require.Equal(t, "content-hash", hash, "existing OpenAI session resolution must remain authoritative")
	require.Equal(t, "explicit-openai-session", cacheKey)
}

func TestResolveOpenAIMessagesMetadataSession_BlankClaudeHeaderKeepsContentFallback(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("X-Claude-Code-Session-Id", "   ")

	hash, cacheKey := metadataSessionForTest(c, "content-hash", "", "gpt-5.6-sol", []byte(`{"metadata":{"user_id":"opaque"}}`))
	require.Equal(t, "content-hash", hash)
	require.Empty(t, cacheKey)
}

// metadataSessionForTest combines HTTP session headers and content seeds through the production session resolver.
func metadataSessionForTest(c *gin.Context, hash, key, model string, body []byte) (string, string) {
	return gatewaysession.MessagesMetadataSession(ClaudeCodeSessionIDFromHeader(c), hash, key, model, body)
}

func TestOpenAIGatewayService_GenerateSessionHash_Priority(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", nil)

	bodyWithKey := []byte(`{"prompt_cache_key":"ses_aaa"}`)

	// The session_id header takes precedence.
	c.Request.Header.Set("session_id", "sess-123")
	c.Request.Header.Set("conversation_id", "conv-456")
	h1 := GenerateOpenAISessionHash(c, bodyWithKey)
	if h1 == "" {
		t.Fatalf("expected non-empty hash")
	}

	// Use conversation_id when session_id is absent.
	c.Request.Header.Del("session_id")
	h2 := GenerateOpenAISessionHash(c, bodyWithKey)
	if h2 == "" {
		t.Fatalf("expected non-empty hash")
	}
	if h1 == h2 {
		t.Fatalf("expected different hashes for different keys")
	}

	// Use prompt_cache_key when both headers are absent.
	c.Request.Header.Del("conversation_id")
	h3 := GenerateOpenAISessionHash(c, bodyWithKey)
	if h3 == "" {
		t.Fatalf("expected non-empty hash")
	}
	if h2 == h3 {
		t.Fatalf("expected different hashes for different keys")
	}

	// Return an empty hash when the request has no session or content signal.
	h4 := GenerateOpenAISessionHash(c, []byte(`{}`))
	if h4 != "" {
		t.Fatalf("expected empty hash when no signals")
	}
}

func TestOpenAIGatewayService_ClientSessionHeaderPriority(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", nil)
	c.Set("api_key", &apikey.APIKey{ID: 901, Group: &routing.Group{}})

	headers := []struct {
		name  string
		value string
	}{
		{name: "session-id", value: "codex-session"},
		{name: "session_id", value: "generic-session"},
		{name: "conversation_id", value: "generic-conversation"},
		{name: OpenCodeSessionAffinityHeader, value: "opencode-affinity"},
		{name: OpenCodeSessionIDHeader, value: "opencode-session-id"},
		{name: OpenCodeNativeSessionHeader, value: "opencode-native-session"},
		{name: CodeBuddyConversationHeader, value: "codebuddy-conversation"},
		{name: GrokConversationIDHeader, value: "grok-conversation"},
	}
	for _, header := range headers {
		c.Request.Header.Set(header.name, header.value)
	}
	body := []byte(`{"prompt_cache_key":"body-session"}`)
	for _, header := range headers {
		require.Equal(t, header.value, ExplicitOpenAIRequestSessionID(c, body), header.name)
		require.Equal(t, fmt.Sprintf("%016x", xxhash.Sum64String(header.value)), GenerateExplicitOpenAISessionHash(c, body), header.name)
		if header.name != GrokConversationIDHeader {
			require.Equal(t, header.value, ExplicitOpenAISessionID(c, body), header.name)
		}
		c.Request.Header.Del(header.name)
	}
	require.Equal(t, "body-session", ExplicitOpenAIRequestSessionID(c, body))
}

func TestOpenAIGatewayService_CodexSessionIDKeepsReconnectHashStable(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	c.Request.Header.Set("session-id", "codex-reconnect-session")
	warmup := []byte(`{
		"type":"response.create",
		"model":"gpt-5.6-sol",
		"generate":false,
		"tools":[{"type":"custom","name":"exec"}],
		"input":[{"role":"user","content":"warmup"}]
	}`)
	business := []byte(`{
		"type":"response.create",
		"model":"gpt-5.6-sol",
		"input":[{"role":"user","content":"install codex"}]
	}`)

	require.Equal(t, GenerateOpenAISessionHash(c, warmup), GenerateOpenAISessionHash(c, business))
	require.Equal(t, "codex-reconnect-session", ExplicitOpenAIRequestSessionID(c, business))
}

func TestOpenAIGatewayService_ClientSessionHeadersIgnorePerRequestIDs(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", nil)
	for name, value := range map[string]string{
		"X-Conversation-Request-ID": "request-rotates-every-turn",
		"X-Conversation-Message-ID": "message-rotates-every-turn",
		"X-Request-ID":              "generic-request-id",
	} {
		c.Request.Header.Set(name, value)
	}
	require.Empty(t, ExplicitOpenAIHeaderSessionID(c))
	require.Empty(t, ExplicitOpenAIRequestSessionID(c, nil))
	require.Empty(t, GenerateExplicitOpenAISessionHash(c, nil))
}

func TestOpenAIGatewayService_GenerateSessionHash_UsesXXHash64(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", nil)

	c.Request.Header.Set("session_id", "sess-fixed-value")

	got := GenerateOpenAISessionHash(c, nil)
	want := fmt.Sprintf("%016x", xxhash.Sum64String("sess-fixed-value"))
	require.Equal(t, want, got)
}

func TestOpenAIGatewayService_GenerateSessionHash_AttachesLegacyHashToContext(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", nil)

	c.Request.Header.Set("session_id", "sess-legacy-check")

	sessionHash := GenerateOpenAISessionHash(c, nil)
	require.NotEmpty(t, sessionHash)
	require.NotNil(t, c.Request)
	require.NotNil(t, c.Request.Context())
	require.NotEmpty(t, requeststate.OpenAILegacySessionHashFromContext(c.Request.Context()))
}

func TestOpenAIGatewayService_GenerateExplicitSessionHash_SkipsContentFallback(t *testing.T) {
	body := []byte(`{"model":"gpt-image-2","prompt":"draw a cat"}`)

	t.Run("stateless image body stays unstuck", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)

		require.Empty(t, GenerateExplicitOpenAISessionHash(c, body))
		require.Empty(t, requeststate.OpenAILegacySessionHashFromContext(c.Request.Context()))
	})

	t.Run("prompt_cache_key is explicit", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)

		got := GenerateExplicitOpenAISessionHash(c, []byte(`{"model":"gpt-image-2","prompt_cache_key":"image-session"}`))
		require.Equal(t, fmt.Sprintf("%016x", xxhash.Sum64String("image-session")), got)
		require.NotEmpty(t, requeststate.OpenAILegacySessionHashFromContext(c.Request.Context()))
	})

	t.Run("header overrides body", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
		c.Request.Header.Set("session_id", "header-session")

		got := GenerateExplicitOpenAISessionHash(c, []byte(`{"prompt_cache_key":"body-session"}`))
		require.Equal(t, fmt.Sprintf("%016x", xxhash.Sum64String("header-session")), got)
	})
}

func TestOpenAIGatewayService_GenerateSessionHashWithFallback(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", nil)
	seed := "openai_ws_ingress:9:100:200"

	got := GenerateOpenAISessionHashWithFallback(c, []byte(`{}`), seed)
	want := fmt.Sprintf("%016x", xxhash.Sum64String(seed))
	require.Equal(t, want, got)
	require.NotEmpty(t, requeststate.OpenAILegacySessionHashFromContext(c.Request.Context()))

	empty := GenerateOpenAISessionHashWithFallback(c, []byte(`{}`), "   ")
	require.Equal(t, "", empty)
}

func TestOpenAIGatewayService_GenerateSessionHash_ContentFallback(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", nil)

	body := []byte(`{"model":"gpt-5.4","messages":[{"role":"system","content":"You are helpful."},{"role":"user","content":"Hello"}]}`)

	hash := GenerateOpenAISessionHash(c, body)
	require.NotEmpty(t, hash, "content-based fallback should produce a hash")

	hash2 := GenerateOpenAISessionHash(c, body)
	require.Equal(t, hash, hash2, "same content should produce same hash")

	bodyExtended := []byte(`{"model":"gpt-5.4","messages":[{"role":"system","content":"You are helpful."},{"role":"user","content":"Hello"},{"role":"assistant","content":"Hi!"},{"role":"user","content":"How are you?"}]}`)
	hashExtended := GenerateOpenAISessionHash(c, bodyExtended)
	require.Equal(t, hash, hashExtended, "hash should be stable across later turns")

	bodyDifferent := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"Different question"}]}`)
	hashDifferent := GenerateOpenAISessionHash(c, bodyDifferent)
	require.NotEqual(t, hash, hashDifferent, "different content should produce different hash")
}

func TestOpenAIGatewayService_GenerateSessionHash_ExplicitSignalWinsOverContent(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", nil)
	body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"Hello"}]}`)

	contentHash := GenerateOpenAISessionHash(c, body)
	require.NotEmpty(t, contentHash)

	c.Request.Header.Set("session_id", "explicit-session")
	explicitHash := GenerateOpenAISessionHash(c, body)
	require.NotEmpty(t, explicitHash)
	require.NotEqual(t, contentHash, explicitHash, "explicit session_id should override content fallback")
}

func TestOpenAIGatewayService_GenerateSessionHash_EmptyBodyStillEmpty(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", nil)
	require.Empty(t, GenerateOpenAISessionHash(c, []byte(`{}`)))
	require.Empty(t, GenerateOpenAISessionHash(c, nil))
}

func newSessionHeaderContext(t *testing.T, headers map[string]string) *gin.Context {
	t.Helper()

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	c.Request = req
	return c
}

func TestExtractClientSessionID_NilContext(t *testing.T) {
	require.Equal(t, "", ExtractClientSessionID(nil))
}

func TestExtractClientSessionID_NilRequest(t *testing.T) {
	require.Equal(t, "", ExtractClientSessionID(&gin.Context{}))
}

func TestExtractClientSessionID_AbsentReturnsEmpty(t *testing.T) {
	c := newSessionHeaderContext(t, nil)
	require.Equal(t, "", ExtractClientSessionID(c))
}

func TestExtractClientSessionID_SupportedHeaders(t *testing.T) {
	tests := []struct {
		name   string
		header string
		value  string
	}{
		{"session_id", "session_id", "sess-A"},
		{"conversation_id", "conversation_id", "conv-B"},
		{"X-Session-Affinity", OpenCodeSessionAffinityHeader, "aff-C"},
		{"X-Session-Id", OpenCodeSessionIDHeader, "sid-D"},
		{"X-OpenCode-Session", OpenCodeNativeSessionHeader, "oc-E"},
		{"X-Conversation-ID", CodeBuddyConversationHeader, "cb-F"},
		{"X-Claude-Code-Session-Id", ClaudeCodeSessionHeader, "cc-G"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := newSessionHeaderContext(t, map[string]string{tc.header: tc.value})
			require.Equal(t, tc.value, ExtractClientSessionID(c))
		})
	}
}

func TestExtractClientSessionID_HeaderPrecedence(t *testing.T) {
	// session_id takes precedence over conversation_id and supported X-* headers.
	c := newSessionHeaderContext(t, map[string]string{
		"session_id":      "primary",
		"conversation_id": "secondary", OpenCodeSessionIDHeader: "tertiary", CodeBuddyConversationHeader: "quaternary",
	})
	require.Equal(t, "primary", ExtractClientSessionID(c))
}

func TestExtractClientSessionID_Sanitizes(t *testing.T) {
	c := newSessionHeaderContext(t, map[string]string{OpenCodeSessionIDHeader: "  clean-123  "})
	require.Equal(t, "clean-123", ExtractClientSessionID(c))
}

func TestExtractClientSessionID_IgnoresNonSessionHeaders(t *testing.T) {
	// Cache keys and per-request IDs are excluded from the persisted client session ID.
	c := newSessionHeaderContext(t, map[string]string{
		"prompt_cache_key": "cache-key-should-not-persist",
		"X-Request-Id":     "req-should-not-persist",
	})
	require.Equal(t, "", ExtractClientSessionID(c))
}

func TestExtractClientSessionID_GrokConversationHeader(t *testing.T) {
	c := newSessionHeaderContext(t, map[string]string{GrokConversationIDHeader: "grok-native-session"})
	c.Set("api_key", &apikey.APIKey{
		ID:    42,
		Group: &routing.Group{},
	})

	require.Equal(t, "grok-native-session", ExtractClientSessionID(c))
}

func TestExtractClientSessionID_EmptyForcedRouteUsesExplicitGrokHeader(t *testing.T) {
	c := newSessionHeaderContext(t, map[string]string{GrokConversationIDHeader: "grok-group-session"})
	c.Set("api_key", &apikey.APIKey{
		ID:    44,
		Group: &routing.Group{},
	})
	c.Request = c.Request.WithContext(apikey.WithForcePlatform(context.Background(), ""))

	require.Equal(t, "grok-group-session", ExtractClientSessionID(c))
}

func TestExtractClientSessionID_GrokConversationHeaderForForcedRoute(t *testing.T) {
	c := newSessionHeaderContext(t, map[string]string{GrokConversationIDHeader: "grok-composite-session"})
	c.Set("api_key", &apikey.APIKey{
		ID:    43,
		Group: &routing.Group{},
	})
	c.Request = c.Request.WithContext(apikey.WithForcePlatform(context.Background(), capability.PlatformGrok))

	require.Equal(t, "grok-composite-session", ExtractClientSessionID(c))
}

func TestExtractClientSessionID_InjectionHeaderDropped(t *testing.T) {
	// Reject the entire supported header value when it contains CRLF.
	c := newSessionHeaderContext(t, map[string]string{"session_id": "abc"})
	c.Request.Header.Set("session_id", "abc\r\nX-Injected: 1")
	require.Equal(t, "", ExtractClientSessionID(c))
}
