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

// guardianAffinityTestContext builds review lineage headers and returns the
// resulting affinity context. A blank parent or metadata argument omits that
// header, allowing tests to exercise their independent evidence and conflict
// rules.
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

// TestWithOpenAIGuardianParentAffinityRequiresUnambiguousReviewLineage compares
// accepted guardian lineage with missing or conflicting parent/subagent
// evidence. Only recognized review relationships may bind the expected current
// parent hash.
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

// TestGrokConversationHeaderIsScopedToGrokRequestScheduling supplies Grok
// header evidence before and after selecting OpenAI. Grok uses its native
// conversation signal, while a known OpenAI target retains body-session
// selection and the same ordinary hash.
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

// TestGrokMediaVideoRequestBindingIsScopedToUserAndAPIKey binds one video
// request to its provider with user and Key identity. Matching lookup succeeds,
// while changing either identity rejects access despite a shared client session
// header.
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

// TestResolveOpenAIMessagesMetadataSession_DoesNotDerivePromptCacheKey supplies
// Messages metadata with no upstream cache key. It requires a sticky session
// hash while the returned prompt cache key remains empty.
func TestResolveOpenAIMessagesMetadataSession_DoesNotDerivePromptCacheKey(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4-5","metadata":{"user_id":"claude-code-session"},"messages":[{"role":"user","content":"hello"}]}`)

	sessionHash, promptCacheKey := metadataSessionForTest(nil, "", "", "claude-sonnet-4-5", body)

	require.NotEmpty(t, sessionHash)
	require.Empty(t, promptCacheKey)
}

// TestResolveOpenAIMessagesMetadataSession_PreservesExplicitPromptCacheKey
// combines Messages metadata with a supplied cache key. It requires a routing
// hash and retains that exact upstream cache input.
func TestResolveOpenAIMessagesMetadataSession_PreservesExplicitPromptCacheKey(t *testing.T) {
	body := []byte(`{"metadata":{"user_id":"claude-code-session"}}`)

	sessionHash, promptCacheKey := metadataSessionForTest(nil, "", "explicit-cache", "claude-sonnet-4-5", body)

	require.NotEmpty(t, sessionHash)
	require.Equal(t, "explicit-cache", promptCacheKey)
}

// TestResolveOpenAIMessagesMetadataSession_ClaudeCodeHeaderOverridesContentFallback
// changes prompt bodies under one Claude session header. It requires stable
// header affinity despite different content seeds and creates no upstream
// prompt cache key.
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

// TestResolveOpenAIMessagesMetadataSession_OpenAISignalWinsOverClaudeHeader
// combines a Claude header with existing OpenAI affinity and cache input. The
// supplied OpenAI hash and prompt key must remain authoritative.
func TestResolveOpenAIMessagesMetadataSession_OpenAISignalWinsOverClaudeHeader(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("X-Claude-Code-Session-Id", "claude-session-001")

	hash, cacheKey := metadataSessionForTest(c, "content-hash", "explicit-openai-session", "gpt-5.6-sol", []byte(`{"metadata":{"user_id":"opaque"}}`))
	require.Equal(t, "content-hash", hash, "existing OpenAI session resolution must remain authoritative")
	require.Equal(t, "explicit-openai-session", cacheKey)
}

// TestResolveOpenAIMessagesMetadataSession_BlankClaudeHeaderKeepsContentFallback
// supplies a whitespace Claude header alongside content affinity. The helper
// must retain the content hash and keep its empty upstream cache key.
func TestResolveOpenAIMessagesMetadataSession_BlankClaudeHeaderKeepsContentFallback(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("X-Claude-Code-Session-Id", "   ")

	hash, cacheKey := metadataSessionForTest(c, "content-hash", "", "gpt-5.6-sol", []byte(`{"metadata":{"user_id":"opaque"}}`))
	require.Equal(t, "content-hash", hash)
	require.Empty(t, cacheKey)
}

// metadataSessionForTest delegates Messages affinity resolution with its
// sanitized Claude header. Existing hash and prompt key are passed separately,
// so tests can distinguish routing identity selection from upstream cache-key
// derivation.
func metadataSessionForTest(c *gin.Context, hash, key, model string, body []byte) (string, string) {
	return gatewaysession.MessagesMetadataSession(ClaudeCodeSessionIDFromHeader(c), hash, key, model, body)
}

// TestOpenAIGatewayService_GenerateSessionHash_Priority removes session and
// conversation headers in sequence before body cache input. Each selected seed
// changes the hash, and removing all session/content signals returns empty.
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

// TestOpenAIGatewayService_ClientSessionHeaderPriority removes each populated
// header in configured order. Explicit affinity must select and hash the next
// remaining value before the root body cache key is considered.
func TestOpenAIGatewayService_ClientSessionHeaderPriority(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", nil)
	c.Set("api_key", &apikey.APIKey{ID: 901, Group: &routing.Group{}})

	headers := []struct {
		name  string // ordered session header name
		value string // expected selected header value
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

// TestOpenAIGatewayService_CodexSessionIDKeepsReconnectHashStable compares
// warmup and business frames under one Codex header. Their hashes must match
// while explicit request identity remains the reconnect session.
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

// TestOpenAIGatewayService_ClientSessionHeadersIgnorePerRequestIDs supplies
// rotating request and message correlation headers only. Explicit session
// readers and hashing must remain empty because those values do not identify a
// conversation.
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

// TestOpenAIGatewayService_GenerateSessionHash_UsesXXHash64 supplies a fixed
// session header. It requires the current affinity representation to equal the
// padded hexadecimal XXHash64 value of that exact seed.
func TestOpenAIGatewayService_GenerateSessionHash_UsesXXHash64(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", nil)

	c.Request.Header.Set("session_id", "sess-fixed-value")

	got := GenerateOpenAISessionHash(c, nil)
	want := fmt.Sprintf("%016x", xxhash.Sum64String("sess-fixed-value"))
	require.Equal(t, want, got)
}

// TestOpenAIGatewayService_GenerateSessionHash_AttachesLegacyHashToContext
// derives affinity from a session header. It requires a current hash and
// nonempty legacy evidence attached to the live request context.
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

// TestOpenAIGatewayService_GenerateExplicitSessionHash_SkipsContentFallback
// compares stateless image content with explicit cache and header signals.
// Content alone stays unbound, while explicit values produce the expected hash
// with header precedence.
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

// TestOpenAIGatewayService_GenerateSessionHashWithFallback supplies empty
// normal request evidence and a connection seed. It requires that seed's
// current hash and legacy attachment, while a whitespace fallback returns
// empty.
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

// TestOpenAIGatewayService_GenerateSessionHash_ContentFallback extends a
// conversation after its initial system/user anchor. The selected content hash
// stays stable for that extension and differs for another initial user
// question.
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

// TestOpenAIGatewayService_GenerateSessionHash_ExplicitSignalWinsOverContent
// first derives content affinity, then supplies an explicit header. The second
// nonempty hash must differ, protecting explicit session precedence.
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

// TestOpenAIGatewayService_GenerateSessionHash_EmptyBodyStillEmpty has no
// header or usable content signal. Empty JSON and nil body must both yield
// empty affinity for this request context.
func TestOpenAIGatewayService_GenerateSessionHash_EmptyBodyStillEmpty(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", nil)
	require.Empty(t, GenerateOpenAISessionHash(c, []byte(`{}`)))
	require.Empty(t, GenerateOpenAISessionHash(c, nil))
}

// newSessionHeaderContext builds a Messages request with the supplied header
// values.
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

// TestExtractClientSessionID_NilContext resolves persisted correlation with no
// Gin context. It requires an empty value so absent request state cannot supply
// a client session identity.
func TestExtractClientSessionID_NilContext(t *testing.T) {
	require.Equal(t, "", ExtractClientSessionID(nil))
}

// TestExtractClientSessionID_NilRequest supplies a Gin context without its HTTP
// request. The persistence reader must return empty safely instead of
// attempting to inspect missing headers.
func TestExtractClientSessionID_NilRequest(t *testing.T) {
	require.Equal(t, "", ExtractClientSessionID(&gin.Context{}))
}

// TestExtractClientSessionID_AbsentReturnsEmpty supplies a valid request with
// no configured session headers. It requires empty persisted correlation rather
// than inventing a conversation value.
func TestExtractClientSessionID_AbsentReturnsEmpty(t *testing.T) {
	c := newSessionHeaderContext(t, nil)
	require.Equal(t, "", ExtractClientSessionID(c))
}

// TestExtractClientSessionID_SupportedHeaders supplies each supported common
// conversation header independently. The persistence reader must return the
// exact valid value selected for that request.
func TestExtractClientSessionID_SupportedHeaders(t *testing.T) {
	tests := []struct {
		name   string // persistence scenario label
		header string // supplied supported header name
		value  string // expected valid session value
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

// TestExtractClientSessionID_HeaderPrecedence populates several common session
// headers in one request. The reader must return the first supported value
// according to persistence priority.
func TestExtractClientSessionID_HeaderPrecedence(t *testing.T) {
	// session_id takes precedence over conversation_id and supported X-* headers.
	c := newSessionHeaderContext(t, map[string]string{
		"session_id":      "primary",
		"conversation_id": "secondary", OpenCodeSessionIDHeader: "tertiary", CodeBuddyConversationHeader: "quaternary",
	})
	require.Equal(t, "primary", ExtractClientSessionID(c))
}

// TestExtractClientSessionID_Sanitizes surrounds a supported session value with
// spaces. The persistence reader must return its trimmed identity while
// retaining the internal characters as supplied.
func TestExtractClientSessionID_Sanitizes(t *testing.T) {
	c := newSessionHeaderContext(t, map[string]string{OpenCodeSessionIDHeader: "  clean-123  "})
	require.Equal(t, "clean-123", ExtractClientSessionID(c))
}

// TestExtractClientSessionID_IgnoresNonSessionHeaders supplies cache-key and
// request-correlation headers with no session header. The persistence reader
// must remain empty because those sources are excluded from its identity rule.
func TestExtractClientSessionID_IgnoresNonSessionHeaders(t *testing.T) {
	// Cache keys and per-request IDs are excluded from the persisted client session ID.
	c := newSessionHeaderContext(t, map[string]string{
		"prompt_cache_key": "cache-key-should-not-persist",
		"X-Request-Id":     "req-should-not-persist",
	})
	require.Equal(t, "", ExtractClientSessionID(c))
}

// TestExtractClientSessionID_GrokConversationHeader supplies a Grok
// conversation header and ordinary Key data. With no stronger platform
// evidence, the persistence reader must accept that native conversation signal.
func TestExtractClientSessionID_GrokConversationHeader(t *testing.T) {
	c := newSessionHeaderContext(t, map[string]string{GrokConversationIDHeader: "grok-native-session"})
	c.Set("api_key", &apikey.APIKey{
		ID:    42,
		Group: &routing.Group{},
	})

	require.Equal(t, "grok-native-session", ExtractClientSessionID(c))
}

// TestExtractClientSessionID_EmptyForcedRouteUsesExplicitGrokHeader binds an
// empty forced platform and Grok header evidence. The blank constraint must
// leave native header fallback available for persisted correlation.
func TestExtractClientSessionID_EmptyForcedRouteUsesExplicitGrokHeader(t *testing.T) {
	c := newSessionHeaderContext(t, map[string]string{GrokConversationIDHeader: "grok-group-session"})
	c.Set("api_key", &apikey.APIKey{
		ID:    44,
		Group: &routing.Group{},
	})
	c.Request = c.Request.WithContext(apikey.WithForcePlatform(context.Background(), ""))

	require.Equal(t, "grok-group-session", ExtractClientSessionID(c))
}

// TestExtractClientSessionID_GrokConversationHeaderForForcedRoute binds an
// explicit Grok platform and its native header. The persistence reader must
// accept that Grok signal for the forced request.
func TestExtractClientSessionID_GrokConversationHeaderForForcedRoute(t *testing.T) {
	c := newSessionHeaderContext(t, map[string]string{GrokConversationIDHeader: "grok-composite-session"})
	c.Set("api_key", &apikey.APIKey{
		ID:    43,
		Group: &routing.Group{},
	})
	c.Request = c.Request.WithContext(apikey.WithForcePlatform(context.Background(), capability.PlatformGrok))

	require.Equal(t, "grok-composite-session", ExtractClientSessionID(c))
}

// TestExtractClientSessionID_InjectionHeaderDropped injects CRLF into a
// supported conversation header. The complete invalid value must be rejected,
// leaving empty persisted session correlation in this single-header fixture.
func TestExtractClientSessionID_InjectionHeaderDropped(t *testing.T) {
	// Reject the entire supported header value when it contains CRLF.
	c := newSessionHeaderContext(t, map[string]string{"session_id": "abc"})
	c.Request.Header.Set("session_id", "abc\r\nX-Injected: 1")
	require.Equal(t, "", ExtractClientSessionID(c))
}
