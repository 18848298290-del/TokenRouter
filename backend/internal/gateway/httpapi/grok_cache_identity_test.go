package httpapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	gatewaysession "github.com/TokenFlux/TokenRouter/internal/gateway/session"
	gatewaytestkit "github.com/TokenFlux/TokenRouter/internal/gateway/testkit"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	xai "github.com/TokenFlux/TokenRouter/internal/upstream/grok"
)

// Grok cache identity separates API Keys and final models while allowing
// stable session or prompt-prefix reuse across turns. Header/body precedence
// and raw tool intent affect which seed reaches upstream; Free provider policy
// independently determines whether native search tools accompany that identity.
// Compact requests leave both the isolated cache key and injected tools absent.
//
// Concurrent determinism uses an immutable shared prompt and a separate Gin
// context for each worker. The result channel holds one value per worker so
// producers can finish before collection starts. Waiting for all producers
// before closing the channel lets the test compare completed identities without
// racing a send against closure.

// TestGrokResponsesCacheIdentityIncludesPromotedCodexTools patches two
// additional-tools payloads with the same user prompt. It checks that promoted
// lookup and read_file declarations produce different nonempty isolated cache
// identities.
func TestGrokResponsesCacheIdentityIncludesPromotedCodexTools(t *testing.T) {
	c := newGrokCacheTestContext(4503)
	lookupBody := []byte(`{"model":"grok","input":[{"type":"additional_tools","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}]},{"type":"message","role":"user","content":"same prompt"}]}`)
	readBody := []byte(`{"model":"grok","input":[{"type":"additional_tools","tools":[{"type":"function","name":"read_file","parameters":{"type":"object"}}]},{"type":"message","role":"user","content":"same prompt"}]}`)

	patchedLookup, err := gatewayprovider.GrokBodyCodec().PatchGrokResponsesBody(lookupBody, "grok-4.5")
	require.NoError(t, err)
	patchedRead, err := gatewayprovider.GrokBodyCodec().PatchGrokResponsesBody(readBody, "grok-4.5")
	require.NoError(t, err)

	lookupIdentity := ResolveGrokCacheIdentity(c, patchedLookup, "", "grok-4.5")
	readIdentity := ResolveGrokCacheIdentity(c, patchedRead, "", "grok-4.5")
	require.NotEmpty(t, lookupIdentity)
	require.NotEmpty(t, readIdentity)
	require.NotEqual(t, lookupIdentity, readIdentity)
}

// TestGrokPreviousResponseSessionSeed compares a Responses ID with a Messages
// ID and absent IDs. It checks that the previous-response seed accepts the
// Responses identity and leaves the other supplied cases empty.
func TestGrokPreviousResponseSessionSeed(t *testing.T) {
	require.Equal(t, "grok-prev-resp:resp_abc123", gatewaysession.GrokPreviousResponseSeed([]byte(`{"previous_response_id":"resp_abc123"}`)))
	require.Empty(t, gatewaysession.GrokPreviousResponseSeed([]byte(`{"previous_response_id":"msg_abc123"}`)))
	require.Empty(t, gatewaysession.GrokPreviousResponseSeed([]byte(`{"previous_response_id":""}`)))
	require.Empty(t, gatewaysession.GrokPreviousResponseSeed([]byte(`{}`)))
}

// TestResolveGrokCacheIdentityUsesPreviousResponseIDWhenNoOtherSeed uses a
// previous response as the available session signal. It checks deterministic
// model scoping and verifies an added prompt_cache_key selects a different
// identity.
func TestResolveGrokCacheIdentityUsesPreviousResponseIDWhenNoOtherSeed(t *testing.T) {
	c := newGrokCacheTestContext(301)
	// Use previous_response_id with no prompt_cache_key, identity headers, or reusable prefix.
	body := []byte(`{"model":"grok","input":[{"role":"user","content":"follow up"}],"previous_response_id":"resp_chain_001"}`)
	got := ResolveGrokCacheIdentity(c, body, "", "grok-4.5")
	require.NotEmpty(t, got)

	// The same previous_response_id yields a stable identity scoped to the model.
	again := ResolveGrokCacheIdentity(c, body, "", "grok-4.5")
	require.Equal(t, got, again)

	// Different models have separate cache identities.
	otherModel := ResolveGrokCacheIdentity(c, body, "", "grok-4.3")
	require.NotEqual(t, got, otherModel)

	// prompt_cache_key takes precedence over previous_response_id.
	withCache := []byte(`{"model":"grok","prompt_cache_key":"client-session","previous_response_id":"resp_chain_001","input":[{"role":"user","content":"x"}]}`)
	cacheID := ResolveGrokCacheIdentity(c, withCache, "", "grok-4.5")
	require.NotEmpty(t, cacheID)
	require.NotEqual(t, got, cacheID)
}

// TestResolveGrokCacheIdentityStableAcrossAppendOnlyTurns adds assistant and
// user history beneath unchanged instructions and tools. It checks that both
// turns produce the same nonempty 36-character identity for the same Key and
// model.
func TestResolveGrokCacheIdentityStableAcrossAppendOnlyTurns(t *testing.T) {
	c := newGrokCacheTestContext(101)
	round1 := []byte(`{"model":"grok","instructions":"be concise","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}],"input":[{"role":"user","content":"first question"}]}`)
	round2 := []byte(`{"model":"grok","instructions":"be concise","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}],"input":[{"role":"user","content":"first question"},{"role":"assistant","content":"first answer"},{"role":"user","content":"second question"}]}`)

	first := ResolveGrokCacheIdentity(c, round1, "", "grok-4.5")
	second := ResolveGrokCacheIdentity(c, round2, "", "grok-4.5")

	require.NotEmpty(t, first)
	require.Len(t, first, 36)
	require.Equal(t, first, second)
}

// TestResolveGrokCacheIdentityStableAcrossIndependentPromptsWithSamePrefix
// changes user questions beneath the same instructions and tools. It checks
// that the reusable prefix selects one stable nonempty identity across both
// independent prompt fixtures.
func TestResolveGrokCacheIdentityStableAcrossIndependentPromptsWithSamePrefix(t *testing.T) {
	c := newGrokCacheTestContext(102)
	firstBody := []byte(`{"model":"grok","instructions":"be concise","tools":[{"type":"function","name":"lookup"}],"input":[{"role":"user","content":"Question A"}]}`)
	secondBody := []byte(`{"model":"grok","instructions":"be concise","tools":[{"type":"function","name":"lookup"}],"input":[{"role":"user","content":"Question B"}]}`)

	first := ResolveGrokCacheIdentity(c, firstBody, "", "grok-4.5")
	second := ResolveGrokCacheIdentity(c, secondBody, "", "grok-4.5")

	require.NotEmpty(t, first)
	require.Equal(t, first, second)
}

// TestResolveGrokCacheIdentityStablePrefixIsolation varies Key, upstream
// model, instructions, system content and tools around one prefix. It checks
// each changed dimension produces an identity distinct from the baseline
// fixture.
func TestResolveGrokCacheIdentityStablePrefixIsolation(t *testing.T) {
	baseBody := []byte(`{"model":"grok","instructions":"be concise","tools":[{"type":"function","name":"lookup"}],"input":[{"role":"system","content":"System A"},{"role":"user","content":"Question A"}]}`)
	differentInstructions := []byte(`{"model":"grok","instructions":"be detailed","tools":[{"type":"function","name":"lookup"}],"input":[{"role":"system","content":"System A"},{"role":"user","content":"Question B"}]}`)
	differentSystem := []byte(`{"model":"grok","instructions":"be concise","tools":[{"type":"function","name":"lookup"}],"input":[{"role":"system","content":"System B"},{"role":"user","content":"Question B"}]}`)
	differentTools := []byte(`{"model":"grok","instructions":"be concise","tools":[{"type":"function","name":"search"}],"input":[{"role":"system","content":"System A"},{"role":"user","content":"Question B"}]}`)

	base := ResolveGrokCacheIdentity(newGrokCacheTestContext(103), baseBody, "", "grok-4.5")
	require.NotEqual(t, base, ResolveGrokCacheIdentity(newGrokCacheTestContext(104), baseBody, "", "grok-4.5"))
	require.NotEqual(t, base, ResolveGrokCacheIdentity(newGrokCacheTestContext(103), baseBody, "", "grok-4.3"))
	require.NotEqual(t, base, ResolveGrokCacheIdentity(newGrokCacheTestContext(103), differentInstructions, "", "grok-4.5"))
	require.NotEqual(t, base, ResolveGrokCacheIdentity(newGrokCacheTestContext(103), differentSystem, "", "grok-4.5"))
	require.NotEqual(t, base, ResolveGrokCacheIdentity(newGrokCacheTestContext(103), differentTools, "", "grok-4.5"))
}

// TestResolveGrokCacheIdentityFallsBackWhenStablePrefixIsEmpty provides two
// different user strings with empty tools. It checks content anchoring
// produces distinct nonempty identities when no reusable instructions or tool
// prefix is available.
func TestResolveGrokCacheIdentityFallsBackWhenStablePrefixIsEmpty(t *testing.T) {
	c := newGrokCacheTestContext(105)
	firstBody := []byte(`{"model":"grok","tools":[],"input":"Question A"}`)
	secondBody := []byte(`{"model":"grok","tools":[],"input":"Question B"}`)

	first := ResolveGrokCacheIdentity(c, firstBody, "", "grok-4.5")
	second := ResolveGrokCacheIdentity(c, secondBody, "", "grok-4.5")

	require.NotEmpty(t, first)
	require.NotEmpty(t, second)
	require.NotEqual(t, first, second)
}

// TestResolveGrokCacheIdentitySkipsUnanchoredFallback supplies absent,
// assistant-only and empty user input fixtures. It checks those requests
// produce no cache identity when no reusable prefix or explicit session signal
// exists.
func TestResolveGrokCacheIdentitySkipsUnanchoredFallback(t *testing.T) {
	c := newGrokCacheTestContext(106)
	tests := [][]byte{
		[]byte(`{"model":"grok"}`),
		[]byte(`{"model":"grok","messages":[{"role":"assistant","content":"answer"}]}`),
		[]byte(`{"model":"grok","messages":[{"role":"user","content":""}]}`),
		[]byte(`{"model":"grok","input":"  "}`),
	}

	for _, body := range tests {
		require.Empty(t, ResolveGrokCacheIdentity(c, body, "", "grok-4.5"))
	}
}

// TestResolveGrokCacheIdentityIsolatesAPIKeyAndMappedModel keeps prompt bytes
// fixed while changing Key or final model. It checks that each caller/model
// scope differs from the nonempty baseline identity for the same content.
func TestResolveGrokCacheIdentityIsolatesAPIKeyAndMappedModel(t *testing.T) {
	body := []byte(`{"model":"grok","input":"same prompt"}`)

	base := ResolveGrokCacheIdentity(newGrokCacheTestContext(201), body, "", "grok-4.5")
	otherTenant := ResolveGrokCacheIdentity(newGrokCacheTestContext(202), body, "", "grok-4.5")
	otherModel := ResolveGrokCacheIdentity(newGrokCacheTestContext(201), body, "", "grok-4.3")

	require.NotEmpty(t, base)
	require.NotEqual(t, base, otherTenant)
	require.NotEqual(t, base, otherModel)
}

// TestResolveGrokCacheIdentityUsesAndIsolatesNativeConversationHeader keeps a
// native conversation header while changing body and explicit argument. It
// checks stable opaque output and confirms the raw session label does not
// appear in the identity.
func TestResolveGrokCacheIdentityUsesAndIsolatesNativeConversationHeader(t *testing.T) {
	c := newGrokCacheTestContext(301)
	c.Request.Header.Set(GrokConversationIDHeader, "raw-native-conversation")
	body1 := []byte(`{"model":"grok","input":"one"}`)
	body2 := []byte(`{"model":"grok","input":"different body that must not replace the explicit session"}`)

	first := ResolveGrokCacheIdentity(c, body1, "body-cache-key", "grok-4.5")
	second := ResolveGrokCacheIdentity(c, body2, "another-body-cache-key", "grok-4.5")

	require.Equal(t, "raw-native-conversation", ExplicitOpenAIRequestSessionID(c, body1))
	require.Equal(t, first, second)
	require.NotEqual(t, "raw-native-conversation", first)
	require.NotContains(t, first, "raw-native-conversation")
}

// TestResolveGrokCacheIdentityExplicitHeaderPriority supplies session,
// conversation, native Grok and body signals together. It checks the generic
// session header determines the same identity as that header supplied on its
// own.
func TestResolveGrokCacheIdentityExplicitHeaderPriority(t *testing.T) {
	body := []byte(`{"model":"grok","prompt_cache_key":"body-key","input":"hi"}`)
	c := newGrokCacheTestContext(401)
	c.Request.Header.Set(GrokConversationIDHeader, "grok-key")
	c.Request.Header.Set("conversation_id", "conversation-key")
	c.Request.Header.Set("session_id", "session-key")

	got := ResolveGrokCacheIdentity(c, body, "explicit-argument", "grok-4.5")
	onlySession := newGrokCacheTestContext(401)
	onlySession.Request.Header.Set("session_id", "session-key")
	want := ResolveGrokCacheIdentity(onlySession, []byte(`{"model":"grok","input":"unrelated"}`), "", "grok-4.5")

	require.Equal(t, want, got)
}

// TestResolveGrokCacheIdentityIDEHeaderPriority removes IDE headers in their
// expected precedence order. It checks each remaining header and verifies the
// body cache key wins when only the native Grok conversation label remains.
func TestResolveGrokCacheIdentityIDEHeaderPriority(t *testing.T) {
	body := []byte(`{"model":"grok","prompt_cache_key":"body-key","input":"hi"}`)
	headers := []struct {
		name  string // scenario subtest label
		value string // supplied control value
	}{
		{name: OpenCodeSessionAffinityHeader, value: "opencode-affinity"},
		{name: OpenCodeSessionIDHeader, value: "opencode-session-id"},
		{name: OpenCodeNativeSessionHeader, value: "opencode-native-session"},
		{name: CodeBuddyConversationHeader, value: "codebuddy-conversation"},
		{name: GrokConversationIDHeader, value: "grok-conversation"},
	}

	c := newGrokCacheTestContext(402)
	for _, header := range headers {
		c.Request.Header.Set(header.name, header.value)
	}
	for _, header := range headers {
		got := ResolveGrokCacheIdentity(c, body, "explicit-argument", "grok-4.5")
		onlyCurrent := newGrokCacheTestContext(402)
		onlyCurrent.Request.Header.Set(header.name, header.value)
		if header.name == GrokConversationIDHeader {
			// prompt_cache_key beats X-Grok-Conv-Id: verify the identity is
			// derived from the body key, not the per-call conv label.
			withOnlyBody := newGrokCacheTestContext(402)
			want := ResolveGrokCacheIdentity(withOnlyBody, body, "", "grok-4.5")
			require.Equal(t, want, got, header.name)
			withOnlyConv := newGrokCacheTestContext(402)
			withOnlyConv.Request.Header.Set(header.name, header.value)
			convOnly := ResolveGrokCacheIdentity(withOnlyConv, []byte(`{"model":"grok","input":"unrelated"}`), "", "grok-4.5")
			require.NotEqual(t, convOnly, got, "body key must not be shadowed by X-Grok-Conv-Id")
		} else {
			want := ResolveGrokCacheIdentity(onlyCurrent, body, "", "grok-4.5")
			require.Equal(t, want, got, header.name)
		}
		c.Request.Header.Del(header.name)
	}
}

// TestResolveGrokCacheIdentitySideCallSharesParentCacheKey changes the native
// conversation label for summary and title calls. It checks their parent
// prompt_cache_key produces the same identity as the main turn's native
// session label.
func TestResolveGrokCacheIdentitySideCallSharesParentCacheKey(t *testing.T) {
	parentSession := "6f1c2f46-0f5e-4f9d-9d4e-2f0f1c3d5b7a"

	mainTurn := newGrokCacheTestContext(910)
	mainTurn.Request.Header.Set(GrokConversationIDHeader, parentSession)
	mainIdentity := ResolveGrokCacheIdentity(mainTurn, []byte(`{"model":"grok-4.6","input":"main conversation"}`), "", "grok-4.6")
	require.NotEmpty(t, mainIdentity)

	sideCall := newGrokCacheTestContext(910)
	sideCall.Request.Header.Set(GrokConversationIDHeader, "turn-summary-"+uuidNew())
	sideIdentity := ResolveGrokCacheIdentity(sideCall, []byte(`{"model":"grok-4.6","prompt_cache_key":"`+parentSession+`","input":"summary replay"}`), "", "grok-4.6")
	require.Equal(t, mainIdentity, sideIdentity,
		"side-call with parent prompt_cache_key must share the main turn cache identity")

	titleRefresh := newGrokCacheTestContext(910)
	titleRefresh.Request.Header.Set(GrokConversationIDHeader, "title-refresh-"+uuidNew())
	titleIdentity := ResolveGrokCacheIdentity(titleRefresh, []byte(`{"model":"grok-4.6","prompt_cache_key":"`+parentSession+`","input":"title replay"}`), "", "grok-4.6")
	require.Equal(t, mainIdentity, titleIdentity)
}

// uuidNew formats one fixed UUID-like value for side-call fixture labels.
// Repeated calls return the same string; the summary and title tests
// distinguish their labels with different prefixes.
func uuidNew() string {
	return fmt.Sprintf("%08x-%04x-4%03x-9%03x-%012x", 0x1234, 0x5678, 0x9abc, 0xdef0, 0x1234567890ab)
}

// TestExplicitGrokCacheSeedPriority removes headers in priority order beside a
// body cache key. It checks the selected raw seed and verifies the explicit
// argument supplies the final missing-body seed.
func TestExplicitGrokCacheSeedPriority(t *testing.T) {
	c := newGrokCacheTestContext(403)
	headers := []struct {
		name  string // scenario subtest label
		value string // supplied control value
	}{
		{name: ClaudeCodeSessionHeader, value: "claude-session"},
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

	body := []byte(`{"model":"grok","prompt_cache_key":"body-key","input":"hi"}`)
	for _, header := range headers {
		var want string
		switch header.name {
		case GrokConversationIDHeader:
			// prompt_cache_key takes precedence over X-Grok-Conv-Id so a side call can
			// keep its parent session cache identity despite a new conversation label.

			want = "body-key"
		default:
			want = header.value
		}
		require.Equal(t, want, explicitGrokCacheSeed(c, body, "explicit-argument"), header.name)
		c.Request.Header.Del(header.name)
	}
	require.Equal(t, "body-key", explicitGrokCacheSeed(c, body, "explicit-argument"))
	require.Equal(t, "explicit-argument", explicitGrokCacheSeed(c, []byte(`{"model":"grok"}`), "explicit-argument"))
}

// TestResolveGrokCacheIdentityIDEHeadersAreStableIsolatedAndOpaque varies
// bodies beneath each supported IDE header. It checks stability, opaque output
// and isolation when the Key or final upstream model changes for the same raw
// session.
func TestResolveGrokCacheIdentityIDEHeadersAreStableIsolatedAndOpaque(t *testing.T) {
	tests := []struct {
		name   string // scenario subtest label
		header string // client identity header
	}{
		{name: "OpenCode affinity", header: OpenCodeSessionAffinityHeader},
		{name: "OpenCode session ID", header: OpenCodeSessionIDHeader},
		{name: "OpenCode native session", header: OpenCodeNativeSessionHeader},
		{name: "CodeBuddy conversation", header: CodeBuddyConversationHeader},
	}

	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rawSession := "raw-ide-session-" + tt.name
			apiKeyID := int64(800 + index)
			c := newGrokCacheTestContext(apiKeyID)
			c.Request.Header.Set(tt.header, rawSession)
			firstBody := []byte(`{"model":"grok","prompt_cache_key":"turn-one-body-key","input":"first turn"}`)
			secondBody := []byte(`{"model":"grok","prompt_cache_key":"turn-two-body-key","input":"different second turn"}`)

			first := ResolveGrokCacheIdentity(c, firstBody, "first-explicit-key", "grok-4.5")
			second := ResolveGrokCacheIdentity(c, secondBody, "second-explicit-key", "grok-4.5")
			require.NotEmpty(t, first)
			require.Equal(t, first, second)
			require.NotEqual(t, rawSession, first)
			require.NotContains(t, first, rawSession)

			otherTenant := newGrokCacheTestContext(apiKeyID + 100)
			otherTenant.Request.Header.Set(tt.header, rawSession)
			require.NotEqual(t, first, ResolveGrokCacheIdentity(otherTenant, firstBody, "", "grok-4.5"))
			require.NotEqual(t, first, ResolveGrokCacheIdentity(c, firstBody, "", "grok-4.3"))
		})
	}
}

// TestOpenCodeResponsesHeaderAndBodyCacheSignalsConverge uses matching
// OpenCode headers and a body cache key across turns. It checks one isolated
// identity reaches body and Grok headers while the raw session label
// disappears.
func TestOpenCodeResponsesHeaderAndBodyCacheSignalsConverge(t *testing.T) {
	const rawSession = "opencode-session-42"
	c := newGrokCacheTestContext(901)
	c.Request.Header.Set(OpenCodeSessionAffinityHeader, rawSession)
	c.Request.Header.Set(OpenCodeSessionIDHeader, rawSession)
	firstBody := []byte(`{"model":"grok","prompt_cache_key":"opencode-session-42","input":"first turn"}`)
	secondBody := []byte(`{"model":"grok","prompt_cache_key":"opencode-session-42","input":"different second turn"}`)

	first := ResolveGrokCacheIdentity(c, firstBody, "", "grok-4.5")
	second := ResolveGrokCacheIdentity(c, secondBody, "", "grok-4.5")
	bodyOnly := ResolveGrokCacheIdentity(newGrokCacheTestContext(901), secondBody, "", "grok-4.5")
	require.NotEmpty(t, first)
	require.Equal(t, first, second)
	require.Equal(t, first, bodyOnly)

	patched, err := xai.ApplyGrokResponsesCacheIdentity(secondBody, secondBody, second, false)
	require.NoError(t, err)
	require.Equal(t, second, gjson.GetBytes(patched, "prompt_cache_key").String())
	headers := make(http.Header)
	xai.ApplyGrokCacheHeaders(headers, second)
	require.Equal(t, second, headers.Get(GrokConversationIDHeader))
	require.NotContains(t, string(patched), rawSession)
}

// TestResolveGrokCacheIdentityPrefersClaudeCodeSession changes request content
// beneath a Claude session header and metadata session fixture. It checks both
// forms retain stable identity across their respective turns despite other
// cache signals.
func TestResolveGrokCacheIdentityPrefersClaudeCodeSession(t *testing.T) {
	c := newGrokCacheTestContext(701)
	c.Request.Header.Set(ClaudeCodeSessionHeader, "cc-session-abc")
	c.Request.Header.Set("session_id", "session-key")
	body1 := []byte(`{"model":"grok","input":"turn-1"}`)
	body2 := []byte(`{"model":"grok","input":"turn-2-different-user-text"}`)

	first := ResolveGrokCacheIdentity(c, body1, "", "grok-4.5")
	second := ResolveGrokCacheIdentity(c, body2, "unrelated-explicit", "grok-4.5")
	require.NotEmpty(t, first)
	require.Equal(t, first, second, "same Claude Code session must keep stable Grok cache identity across turns")

	// Claude Code can encode session identity as JSON inside metadata.user_id.
	metaBody := []byte(`{"model":"grok","metadata":{"user_id":"{\"session_id\":\"meta-session-xyz\"}"},"input":"hi"}`)
	metaOnly := newGrokCacheTestContext(702)
	metaID := ResolveGrokCacheIdentity(metaOnly, metaBody, "", "grok-4.5")
	require.NotEmpty(t, metaID)
	metaBody2 := []byte(`{"model":"grok","metadata":{"user_id":"{\"session_id\":\"meta-session-xyz\"}"},"input":"later turn"}`)
	require.Equal(t, metaID, ResolveGrokCacheIdentity(metaOnly, metaBody2, "", "grok-4.5"))
}

// TestResolveGrokCacheIdentityFailsClosedWithoutAPIKeyContext supplies session
// signals with no positive Key identity and an absent HTTP context. It checks
// both cases return empty cache identity so unrelated callers cannot share
// unscoped routing.
func TestResolveGrokCacheIdentityFailsClosedWithoutAPIKeyContext(t *testing.T) {
	c := newGrokCacheTestContext(0)
	c.Request.Header.Set(GrokConversationIDHeader, "native-session")

	require.Empty(t, ResolveGrokCacheIdentity(c, []byte(`{"model":"grok","input":"hi"}`), "", "grok-4.5"))
	require.Empty(t, ResolveGrokCacheIdentity(nil, []byte(`{"model":"grok","prompt_cache_key":"key"}`), "key", "grok-4.5"))
}

// TestApplyGrokCacheIdentityWritesResponsesBodyAndHeader applies an isolated
// identity to a tool-free Responses body and native header. It checks injected
// routing tools, header replacement/removal and removal of unscoped raw cache
// keys.
func TestApplyGrokCacheIdentityWritesResponsesBodyAndHeader(t *testing.T) {
	sourceBody := []byte(`{"model":"grok-4.5","prompt_cache_key":"raw-client-key"}`)
	body, err := xai.ApplyGrokResponsesCacheIdentity(sourceBody, sourceBody, "isolated-id", true)
	require.NoError(t, err)
	require.Equal(t, "isolated-id", gjson.GetBytes(body, "prompt_cache_key").String())
	require.Equal(t, "web_search", gjson.GetBytes(body, "tools.0.type").String())
	require.Equal(t, "x_search", gjson.GetBytes(body, "tools.1.type").String())
	require.Equal(t, "none", gjson.GetBytes(body, "tool_choice").String())

	headers := make(http.Header)
	headers.Set(GrokConversationIDHeader, "spoofed-client-value")
	xai.ApplyGrokCacheHeaders(headers, "isolated-id")
	require.Equal(t, "isolated-id", headers.Get(GrokConversationIDHeader))
	xai.ApplyGrokCacheHeaders(headers, "")
	require.Empty(t, headers.Get(GrokConversationIDHeader))

	chatBody, err := xai.StripGrokChatPromptCacheKey(body)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(chatBody, "prompt_cache_key").Exists())

	unscopedSourceBody := []byte(`{"model":"grok","prompt_cache_key":"raw-client-key"}`)
	unscopedBody, err := xai.ApplyGrokResponsesCacheIdentity(unscopedSourceBody, unscopedSourceBody, "", true)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(unscopedBody, "prompt_cache_key").Exists())
	require.False(t, gjson.GetBytes(unscopedBody, "tools").Exists())
	require.False(t, gjson.GetBytes(unscopedBody, "tool_choice").Exists())
}

// TestGrokFreeMessagesClientToolCacheDefaultsOnForKnownFree varies missing and
// automatic tool choice for a known Free provider. It checks function
// declarations retain order with native search companions and the expected
// choice presence.
func TestGrokFreeMessagesClientToolCacheDefaultsOnForKnownFree(t *testing.T) {
	provider := gatewaytestkit.HealthyGrokOAuthProvider(901, "access-token")
	provider.Record.Credentials["subscription_tier"] = " FREE "
	tests := []struct {
		name           string // scenario subtest label
		toolChoiceJSON string // inserted tool-choice JSON
		wantChoice     bool   // expected choice field presence
	}{
		{name: "missing tool choice"},
		{name: "automatic tool choice", toolChoiceJSON: `,"tool_choice":"auto"`, wantChoice: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			intentBody := []byte(`{"model":"grok","tools":[{"type":"function","name":"lookup","description":"look up a value","parameters":{"type":"object"}},{"type":"function","name":"save","parameters":{"type":"object"}}]` + tt.toolChoiceJSON + `}`)
			body, err := xai.ApplyGrokResponsesCacheIdentity(intentBody, intentBody, "isolated-id", true)
			require.NoError(t, err)
			body, err = gatewayprovider.ApplyGrokFreeMessagesFunctionToolCacheRoute(body, intentBody, provider, "isolated-id")

			require.NoError(t, err)
			require.Equal(t, "isolated-id", gjson.GetBytes(body, "prompt_cache_key").String())
			tools := gjson.GetBytes(body, "tools").Array()
			require.Len(t, tools, 4)
			require.Equal(t, "function", tools[0].Get("type").String())
			require.Equal(t, "lookup", tools[0].Get("name").String())
			require.Equal(t, "function", tools[1].Get("type").String())
			require.Equal(t, "save", tools[1].Get("name").String())
			require.Equal(t, "web_search", tools[2].Get("type").String())
			require.Equal(t, "x_search", tools[3].Get("type").String())
			require.Equal(t, tt.wantChoice, gjson.GetBytes(body, "tool_choice").Exists())
		})
	}
}

// TestGrokFreeFunctionToolCacheRouteConvertsNamedSearchFunctions supplies
// duplicate web_search functions beside other client functions. It checks
// conversion to deduplicated native tools and verifies applying the rule again
// yields equivalent JSON.
func TestGrokFreeFunctionToolCacheRouteConvertsNamedSearchFunctions(t *testing.T) {
	provider := gatewaytestkit.HealthyGrokOAuthProvider(916, "access-token")
	provider.Record.Credentials["subscription_tier"] = "free"
	intentBody := []byte(`{
		"model":"grok",
		"tools":[
			{"type":"function","name":"lookup","parameters":{"type":"object"}},
			{"type":"function","name":"web_search","parameters":{"type":"object"}},
			{"type":"function","name":"web_search","parameters":{"type":"object"}},
			{"type":"function","name":"x_search","parameters":{"type":"object"}}
		],
		"tool_choice":"auto"
	}`)

	patched, err := gatewayprovider.ApplyGrokFreeMessagesFunctionToolCacheRoute(intentBody, intentBody, provider, "isolated-id")
	require.NoError(t, err)
	tools := gjson.GetBytes(patched, "tools").Array()
	require.Len(t, tools, 3)
	require.Equal(t, "function", tools[0].Get("type").String())
	require.Equal(t, "lookup", tools[0].Get("name").String())
	require.Equal(t, "web_search", tools[1].Get("type").String())
	require.False(t, tools[1].Get("name").Exists())
	require.Equal(t, "x_search", tools[2].Get("type").String())
	require.False(t, tools[2].Get("name").Exists())

	second, err := gatewayprovider.ApplyGrokFreeMessagesFunctionToolCacheRoute(patched, intentBody, provider, "isolated-id")
	require.NoError(t, err)
	require.JSONEq(t, string(patched), string(second))
}

// TestGrokFreeMessagesClientToolCacheDefaultsWithMissingProviderSetting varies
// absent, empty and unrelated provider metadata. It checks a known Free
// provider still receives native search companions when no explicit cache
// setting exists.
func TestGrokFreeMessagesClientToolCacheDefaultsWithMissingProviderSetting(t *testing.T) {
	body := []byte(`{"model":"grok","tools":[{"type":"function","name":"view_image","parameters":{"type":"object"}}],"tool_choice":"auto"}`)
	tests := []struct {
		name  string         // scenario subtest label
		extra map[string]any // provider metadata fixture
	}{
		{name: "nil extra"},
		{name: "empty extra", extra: map[string]any{}},
		{name: "unrelated extra", extra: map[string]any{"other_setting": true}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := gatewaytestkit.HealthyGrokOAuthProvider(90101, "access-token")
			provider.Record.Credentials["subscription_tier"] = "free"
			provider.Record.Extra = tt.extra

			patched, err := gatewayprovider.ApplyGrokFreeMessagesFunctionToolCacheRoute(body, body, provider, "isolated-id")

			require.NoError(t, err)
			tools := gjson.GetBytes(patched, "tools").Array()
			require.Len(t, tools, 3)
			require.Equal(t, "web_search", tools[1].Get("type").String())
			require.Equal(t, "x_search", tools[2].Get("type").String())
		})
	}
}

// TestApplyGrokCacheIdentityAppendsNativeToolsWhenSearchPresent supplies a
// client web_search function beside lookup. It checks the Free route converts
// that named search declaration and adds x_search beside the retained client
// function.
func TestApplyGrokCacheIdentityAppendsNativeToolsWhenSearchPresent(t *testing.T) {
	provider := gatewaytestkit.HealthyGrokOAuthProvider(901, "access-token")
	provider.Record.Credentials["subscription_tier"] = " FREE "

	// A web_search function is converted to a native tool and x_search is added.
	intentBody := []byte(`{"model":"grok","tools":[{"type":"function","name":"lookup","description":"look up a value","parameters":{"type":"object"}},{"type":"function","name":"web_search","description":"search","parameters":{"type":"object"}}]}`)
	body, err := xai.ApplyGrokResponsesCacheIdentity(intentBody, intentBody, "isolated-id", true)
	require.NoError(t, err)
	body, err = gatewayprovider.ApplyGrokFreeMessagesFunctionToolCacheRoute(body, intentBody, provider, "isolated-id")
	require.NoError(t, err)

	tools := gjson.GetBytes(body, "tools").Array()
	require.Len(t, tools, 3)
	require.Equal(t, "function", tools[0].Get("type").String())
	require.Equal(t, "lookup", tools[0].Get("name").String())
	require.Equal(t, "web_search", tools[1].Get("type").String())
	require.Equal(t, "x_search", tools[2].Get("type").String())
}

// TestGrokFreeClientToolCacheProviderOptIn enables cache routing in a known
// Free provider's metadata. It checks both ordinary client functions remain
// ordered before appended native web and X search tools.
func TestGrokFreeClientToolCacheProviderOptIn(t *testing.T) {
	provider := gatewaytestkit.HealthyGrokOAuthProvider(9011, "access-token")
	provider.Record.Credentials["subscription_tier"] = "free"
	provider.Record.Extra = map[string]any{"grok_client_tool_cache_enabled": true}
	intentBody := []byte(`{"model":"grok","tools":[{"type":"function","name":"view_image","parameters":{"type":"object"}},{"type":"function","name":"read_file","parameters":{"type":"object"}}],"tool_choice":"auto"}`)

	body, err := xai.ApplyGrokResponsesCacheIdentity(intentBody, intentBody, "isolated-id", true)
	require.NoError(t, err)
	body, err = gatewayprovider.ApplyGrokFreeMessagesFunctionToolCacheRoute(body, intentBody, provider, "isolated-id")
	require.NoError(t, err)

	tools := gjson.GetBytes(body, "tools").Array()
	require.Len(t, tools, 4)
	require.Equal(t, "view_image", tools[0].Get("name").String())
	require.Equal(t, "read_file", tools[1].Get("name").String())
	require.Equal(t, "web_search", tools[2].Get("type").String())
	require.Equal(t, "x_search", tools[3].Get("type").String())
}

// TestGrokFreeMessagesClientToolCacheProviderOptOut supplies false and
// malformed provider setting values. It checks the listed known Free requests
// keep equivalent JSON when explicit metadata disables ordinary client-tool
// cache routing.
func TestGrokFreeMessagesClientToolCacheProviderOptOut(t *testing.T) {
	body := []byte(`{"model":"grok","tools":[{"type":"function","name":"view_image","parameters":{"type":"object"}}],"tool_choice":"auto"}`)
	tests := []struct {
		name  string // scenario subtest label
		value any    // supplied control value
	}{
		{name: "explicit false", value: false},
		{name: "string false is malformed", value: "false"},
		{name: "numeric value is malformed", value: 1},
		{name: "null value is malformed", value: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := gatewaytestkit.HealthyGrokOAuthProvider(90111, "access-token")
			provider.Record.Credentials["subscription_tier"] = "free"
			provider.Record.Extra = map[string]any{"grok_client_tool_cache_enabled": tt.value}

			patched, err := gatewayprovider.ApplyGrokFreeMessagesFunctionToolCacheRoute(body, body, provider, "isolated-id")

			require.NoError(t, err)
			require.JSONEq(t, string(body), string(patched))
		})
	}
}

// TestGrokFreeClientToolCacheRequestOptInOverridesProviderOptOut sets provider
// opt-out and a recognized request opt-in header. It checks the request
// override retains the client function and adds both native search companions.
func TestGrokFreeClientToolCacheRequestOptInOverridesProviderOptOut(t *testing.T) {
	provider := gatewaytestkit.HealthyGrokOAuthProvider(9014, "access-token")
	provider.Record.Credentials["subscription_tier"] = "free"
	provider.Record.Extra = map[string]any{"grok_client_tool_cache_enabled": false}
	c := newGrokCacheTestContext(9014)
	c.Request.Header.Set("X-TokenRouter-Grok-Client-Tool-Cache", "prefer-cache")
	intentBody := []byte(`{"model":"grok","tools":[{"type":"function","name":"view_image","parameters":{"type":"object"}}],"tool_choice":"auto"}`)

	body, err := xai.ApplyGrokResponsesCacheIdentity(intentBody, intentBody, "isolated-id", true)
	require.NoError(t, err)
	body, err = ApplyGrokFreeRequestToolCacheRoute(c, body, intentBody, provider, "isolated-id")
	require.NoError(t, err)

	tools := gjson.GetBytes(body, "tools").Array()
	require.Len(t, tools, 3)
	require.Equal(t, "view_image", tools[0].Get("name").String())
	require.Equal(t, "web_search", tools[1].Get("type").String())
	require.Equal(t, "x_search", tools[2].Get("type").String())
}

// TestGrokFreeChatRequestClientToolCacheDefaultsOnWithoutClientFingerprint
// uses a Chat path with known Free provider metadata and no fingerprint. It
// checks default routing retains the client function and appends native web
// and X search tools.
func TestGrokFreeChatRequestClientToolCacheDefaultsOnWithoutClientFingerprint(t *testing.T) {
	provider := gatewaytestkit.HealthyGrokOAuthProvider(90140, "access-token")
	provider.Record.Credentials["subscription_tier"] = "free"
	c := newGrokCacheTestContext(90140)
	c.Request.URL.Path = "/v1/chat/completions"
	body := []byte(`{"model":"grok","tools":[{"type":"function","name":"view_image","parameters":{"type":"object"}}],"tool_choice":"auto"}`)

	patched, err := ApplyGrokFreeRequestToolCacheRoute(c, body, body, provider, "isolated-id")

	require.NoError(t, err)
	tools := gjson.GetBytes(patched, "tools").Array()
	require.Len(t, tools, 3)
	require.Equal(t, "view_image", tools[0].Get("name").String())
	require.Equal(t, "web_search", tools[1].Get("type").String())
	require.Equal(t, "x_search", tools[2].Get("type").String())
}

// TestGrokFreeClientToolCacheClaudeDesktopResponsesAutoOptIn supplies complete
// desktop headers with both CLI app labels and no explicit provider setting.
// It checks the known Free payload retains client functions with native search
// companions.
func TestGrokFreeClientToolCacheClaudeDesktopResponsesAutoOptIn(t *testing.T) {
	provider := gatewaytestkit.HealthyGrokOAuthProvider(90141, "access-token")
	provider.Record.Credentials["subscription_tier"] = "free"
	body := []byte(`{"model":"grok","tools":[{"type":"function","name":"Read","parameters":{"type":"object"}},{"type":"function","name":"Edit","parameters":{"type":"object"}}],"tool_choice":"auto"}`)

	for _, xApp := range []string{"cli", "cli-bg"} {
		t.Run(xApp, func(t *testing.T) {
			c := newGrokCacheTestContext(90141)
			// Vary the desktop description beside complete fingerprint headers. Known
			// Free default policy also permits routing, so this case does not isolate detection.
			c.Request.Header.Set("User-Agent", "claude-cli/2.1.215 (external, future-desktop, agent-sdk/0.3.215)")
			c.Request.Header.Set("X-App", xApp)
			c.Request.Header.Set("anthropic-client-platform", "desktop_app")
			c.Request.Header.Set("X-Claude-Code-Session-Id", "desktop-session-1")

			patched, err := ApplyGrokFreeRequestToolCacheRoute(c, body, body, provider, "isolated-id")

			require.NoError(t, err)
			tools := gjson.GetBytes(patched, "tools").Array()
			require.Len(t, tools, 4)
			require.Equal(t, "Read", tools[0].Get("name").String())
			require.Equal(t, "Edit", tools[1].Get("name").String())
			require.Equal(t, "web_search", tools[2].Get("type").String())
			require.Equal(t, "x_search", tools[3].Get("type").String())
		})
	}
}

// TestGrokFreeClientToolCacheClaudeDesktopFingerprintRequiresAllSignals varies
// missing desktop signals while provider cache routing is explicitly disabled.
// It checks equivalent output under that configured policy; this fixture does
// not independently isolate fingerprint rejection.
func TestGrokFreeClientToolCacheClaudeDesktopFingerprintRequiresAllSignals(t *testing.T) {
	provider := gatewaytestkit.HealthyGrokOAuthProvider(90142, "access-token")
	provider.Record.Credentials["subscription_tier"] = "free"
	provider.Record.Extra = map[string]any{"grok_client_tool_cache_enabled": false}
	body := []byte(`{"model":"grok","tools":[{"type":"function","name":"Read","parameters":{"type":"object"}}],"tool_choice":"auto"}`)

	tests := []struct {
		name     string // scenario subtest label
		path     string // request endpoint path
		ua       string // client user agent
		xApp     string // client app header
		platform string // client platform header
		session  string // explicit client session
	}{
		{
			name:     "chat path",
			path:     "/v1/chat/completions",
			ua:       "claude-cli/2.1.215 (external, claude-desktop-3p, agent-sdk/0.3.215)",
			xApp:     "cli",
			platform: "desktop_app",
			session:  "desktop-session-1",
		},
		{
			name:     "compact responses path",
			path:     "/v1/responses/compact",
			ua:       "claude-cli/2.1.215 (external, claude-desktop-3p, agent-sdk/0.3.215)",
			xApp:     "cli",
			platform: "desktop_app",
			session:  "desktop-session-1",
		},
		{
			name:     "non claude cli user agent",
			path:     "/v1/responses",
			ua:       "Mozilla/5.0 (claude-desktop-3p)",
			xApp:     "cli",
			platform: "desktop_app",
			session:  "desktop-session-1",
		},
		{
			name:     "missing x app",
			path:     "/v1/responses",
			ua:       "claude-cli/2.1.215 (external, claude-desktop-3p, agent-sdk/0.3.215)",
			platform: "desktop_app",
			session:  "desktop-session-1",
		},
		{
			name:     "wrong x app",
			path:     "/v1/responses",
			ua:       "claude-cli/2.1.215 (external, claude-desktop-3p, agent-sdk/0.3.215)",
			xApp:     "desktop",
			platform: "desktop_app",
			session:  "desktop-session-1",
		},
		{
			name:    "missing client platform",
			path:    "/v1/responses",
			ua:      "claude-cli/2.1.215 (external, claude-desktop-3p, agent-sdk/0.3.215)",
			xApp:    "cli",
			session: "desktop-session-1",
		},
		{
			name:     "wrong client platform",
			path:     "/v1/responses",
			ua:       "claude-cli/2.1.215 (external, claude-desktop-3p, agent-sdk/0.3.215)",
			xApp:     "cli",
			platform: "web",
			session:  "desktop-session-1",
		},
		{
			name:     "missing session header",
			path:     "/v1/responses",
			ua:       "claude-cli/2.1.215 (external, claude-desktop-3p, agent-sdk/0.3.215)",
			xApp:     "cli",
			platform: "desktop_app",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newGrokCacheTestContext(90142)
			c.Request.URL.Path = tt.path
			if tt.ua != "" {
				c.Request.Header.Set("User-Agent", tt.ua)
			}
			if tt.xApp != "" {
				c.Request.Header.Set("X-App", tt.xApp)
			}
			if tt.platform != "" {
				c.Request.Header.Set("anthropic-client-platform", tt.platform)
			}
			if tt.session != "" {
				c.Request.Header.Set("X-Claude-Code-Session-Id", tt.session)
			}

			patched, err := ApplyGrokFreeRequestToolCacheRoute(c, body, body, provider, "isolated-id")

			require.NoError(t, err)
			require.JSONEq(t, string(body), string(patched))
		})
	}
}

// TestGrokFreeClientToolCacheExplicitRequestOptOut supplies each recognized
// opt-out value to a known Free Chat request. It checks equivalent JSON for
// these automatic-tool-choice fixtures after the local request control is
// applied.
func TestGrokFreeClientToolCacheExplicitRequestOptOut(t *testing.T) {
	provider := gatewaytestkit.HealthyGrokOAuthProvider(90144, "access-token")
	provider.Record.Credentials["subscription_tier"] = "free"
	body := []byte(`{"model":"grok","tools":[{"type":"function","name":"Read","parameters":{"type":"object"}}],"tool_choice":"auto"}`)

	for _, value := range []string{"0", "false", "no", "off"} {
		t.Run(value, func(t *testing.T) {
			c := newGrokCacheTestContext(90144)
			c.Request.URL.Path = "/v1/chat/completions"
			c.Request.Header.Set("X-TokenRouter-Grok-Client-Tool-Cache", value)

			patched, err := ApplyGrokFreeRequestToolCacheRoute(c, body, body, provider, "isolated-id")

			require.NoError(t, err)
			require.JSONEq(t, string(body), string(patched))
		})
	}
}

// TestGrokFreeClientToolCacheClaudeDesktopAutoOptInDoesNotOverridePaidTier
// supplies the complete desktop fingerprint to a paid provider. It checks the
// payload remains equivalent because desktop activation does not make that
// provider eligible for the Free route.
func TestGrokFreeClientToolCacheClaudeDesktopAutoOptInDoesNotOverridePaidTier(t *testing.T) {
	provider := gatewaytestkit.HealthyGrokOAuthProvider(90143, "access-token")
	provider.Record.Credentials["subscription_tier"] = "supergrok"
	c := newGrokCacheTestContext(90143)
	c.Request.Header.Set("User-Agent", "claude-cli/2.1.215 (external, claude-desktop-3p, agent-sdk/0.3.215)")
	c.Request.Header.Set("X-App", "cli")
	c.Request.Header.Set("anthropic-client-platform", "desktop_app")
	c.Request.Header.Set("X-Claude-Code-Session-Id", "desktop-session-1")
	body := []byte(`{"model":"grok","tools":[{"type":"function","name":"Read","parameters":{"type":"object"}}],"tool_choice":"auto"}`)

	patched, err := ApplyGrokFreeRequestToolCacheRoute(c, body, body, provider, "isolated-id")

	require.NoError(t, err)
	require.JSONEq(t, string(body), string(patched))
}

// TestGrokFreeRequestClientSearchFunctionUsesDefaultProviderPolicy supplies a
// named search function beside view_image for a known Free provider. It checks
// default policy converts search intent and appends the missing native search
// companion.
func TestGrokFreeRequestClientSearchFunctionUsesDefaultProviderPolicy(t *testing.T) {
	provider := gatewaytestkit.HealthyGrokOAuthProvider(9015, "access-token")
	provider.Record.Credentials["subscription_tier"] = "free"
	c := newGrokCacheTestContext(9015)
	body := []byte(`{"model":"grok","tools":[{"type":"function","name":"view_image","parameters":{"type":"object"}},{"type":"function","name":"web_search","parameters":{"type":"object"}}],"tool_choice":"auto"}`)

	patched, err := ApplyGrokFreeRequestToolCacheRoute(c, body, body, provider, "isolated-id")

	require.NoError(t, err)
	tools := gjson.GetBytes(patched, "tools").Array()
	require.Len(t, tools, 3)
	require.Equal(t, "view_image", tools[0].Get("name").String())
	require.Equal(t, "web_search", tools[1].Get("type").String())
	require.Equal(t, "x_search", tools[2].Get("type").String())
}

// TestGrokFreeRequestToolChoiceNoneUsesSafeCacheRoute supplies a disabled tool
// choice with an ordinary client function. It checks the function remains
// beside appended web_search and x_search declarations in this Free routing
// fixture.
func TestGrokFreeRequestToolChoiceNoneUsesSafeCacheRoute(t *testing.T) {
	provider := gatewaytestkit.HealthyGrokOAuthProvider(9016, "access-token")
	provider.Record.Credentials["subscription_tier"] = "free"
	c := newGrokCacheTestContext(9016)
	body := []byte(`{"model":"grok","tools":[{"type":"function","name":"view_image","parameters":{"type":"object"}}],"tool_choice":"none"}`)

	patched, err := ApplyGrokFreeRequestToolCacheRoute(c, body, body, provider, "isolated-id")

	require.NoError(t, err)
	tools := gjson.GetBytes(patched, "tools").Array()
	require.Len(t, tools, 3)
	require.Equal(t, "view_image", tools[0].Get("name").String())
	require.Equal(t, "web_search", tools[1].Get("type").String())
	require.Equal(t, "x_search", tools[2].Get("type").String())
	require.Equal(t, "none", gjson.GetBytes(patched, "tool_choice").String())
}

// TestApplyGrokCacheIdentityRecognizesResponsesLiteAdditionalTools retains
// additional-tools input as original intent beside a promoted current body. It
// checks isolated identity is written while the existing function stays alone
// and choice remains absent.
func TestApplyGrokCacheIdentityRecognizesResponsesLiteAdditionalTools(t *testing.T) {
	intentBody := []byte(`{"model":"grok","input":[{"type":"additional_tools","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}]},{"type":"message","role":"user","content":"hello"}]}`)
	patchedBody := []byte(`{"model":"grok-4.5","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}],"input":[{"type":"message","role":"user","content":"hello"}]}`)

	patched, err := xai.ApplyGrokResponsesCacheIdentity(patchedBody, intentBody, "isolated-id", true)

	require.NoError(t, err)
	tools := gjson.GetBytes(patched, "tools").Array()
	require.Len(t, tools, 1)
	require.Equal(t, "lookup", tools[0].Get("name").String())
	require.False(t, gjson.GetBytes(patched, "tool_choice").Exists())
	require.Equal(t, "isolated-id", gjson.GetBytes(patched, "prompt_cache_key").String())
}

// TestGrokFreeCacheRoutePreservesMixedSupportedToolsWithSearchIntent mixes
// function, shell and native web_search tools for a Free provider. It checks
// those declarations retain order while the missing x_search companion is
// appended.
func TestGrokFreeCacheRoutePreservesMixedSupportedToolsWithSearchIntent(t *testing.T) {
	provider := gatewaytestkit.HealthyGrokOAuthProvider(9012, "access-token")
	provider.Record.Credentials["subscription_tier"] = "free"
	intentBody := []byte(`{"model":"grok","tools":[{"type":"function","name":"view_image","parameters":{"type":"object"}},{"type":"shell"},{"type":"web_search"}],"tool_choice":"auto"}`)

	body, err := xai.ApplyGrokResponsesCacheIdentity(intentBody, intentBody, "isolated-id", true)
	require.NoError(t, err)
	body, err = gatewayprovider.ApplyGrokFreeMessagesFunctionToolCacheRoute(body, intentBody, provider, "isolated-id")
	require.NoError(t, err)

	tools := gjson.GetBytes(body, "tools").Array()
	require.Len(t, tools, 4)
	require.Equal(t, "function", tools[0].Get("type").String())
	require.Equal(t, "shell", tools[1].Get("type").String())
	require.Equal(t, "web_search", tools[2].Get("type").String())
	require.Equal(t, "x_search", tools[3].Get("type").String())
}

// TestGrokClientToolCacheOptInDoesNotOverridePaidTier sets provider opt-in
// beside a paid subscription tier. It checks equivalent JSON because the
// configured flag alone does not establish eligibility for Free-specific tool
// routing.
func TestGrokClientToolCacheOptInDoesNotOverridePaidTier(t *testing.T) {
	provider := gatewaytestkit.HealthyGrokOAuthProvider(9013, "access-token")
	provider.Record.Credentials["subscription_tier"] = "supergrok"
	provider.Record.Extra = map[string]any{"grok_client_tool_cache_enabled": true}
	body := []byte(`{"model":"grok","tools":[{"type":"function","name":"view_image","parameters":{"type":"object"}}],"tool_choice":"auto"}`)

	patched, err := gatewayprovider.ApplyGrokFreeMessagesFunctionToolCacheRoute(body, body, provider, "isolated-id")

	require.NoError(t, err)
	require.JSONEq(t, string(body), string(patched))
}

// TestApplyGrokCacheIdentityRequiresPatchedFunctionTools varies missing,
// empty, native-only and unsupported patched tools beneath function intent. It
// checks identity is written while tool existence and raw representation stay
// unchanged in these cases.
func TestApplyGrokCacheIdentityRequiresPatchedFunctionTools(t *testing.T) {
	provider := gatewaytestkit.HealthyGrokOAuthProvider(902, "access-token")
	provider.Record.Credentials["subscription_tier"] = "free"
	intentBody := []byte(`{"model":"grok","tools":[{"type":"function","name":"lookup"}],"tool_choice":"auto"}`)
	tests := []struct {
		name        string // scenario subtest label
		patchedBody string // filtered request JSON
	}{
		{name: "missing tools", patchedBody: `{"model":"grok-4.5"}`},
		{name: "empty tools", patchedBody: `{"model":"grok-4.5","tools":[]}`},
		{name: "native tools only", patchedBody: `{"model":"grok-4.5","tools":[{"type":"web_search"}]}`},
		{name: "unexpected patched tool", patchedBody: `{"model":"grok-4.5","tools":[{"type":"function","name":"lookup"},{"type":"namespace","name":"server"}]}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			beforeTools := gjson.Get(tt.patchedBody, "tools")
			body, err := xai.ApplyGrokResponsesCacheIdentity([]byte(tt.patchedBody), intentBody, "isolated-id", true)
			require.NoError(t, err)
			body, err = gatewayprovider.ApplyGrokFreeMessagesFunctionToolCacheRoute(body, intentBody, provider, "isolated-id")

			require.NoError(t, err)
			require.Equal(t, "isolated-id", gjson.GetBytes(body, "prompt_cache_key").String())
			afterTools := gjson.GetBytes(body, "tools")
			require.Equal(t, beforeTools.Exists(), afterTools.Exists())
			require.Equal(t, beforeTools.Raw, afterTools.Raw)
		})
	}
}

// TestGrokFreeMessagesFunctionToolCacheRouteRequiresKnownFreeTier varies
// credential, billing and quota evidence across Free, paid and unknown
// providers. It checks native-tool injection follows the supplied tier
// evidence, including paid evidence overriding stale Free signals.
func TestGrokFreeMessagesFunctionToolCacheRouteRequiresKnownFreeTier(t *testing.T) {
	// A web_search function triggers search-tool injection; ordinary client functions retain their declarations.
	intentBody := []byte(`{"model":"grok","tools":[{"type":"function","name":"lookup"},{"type":"function","name":"web_search"}],"tool_choice":"auto"}`)
	tests := []struct {
		name     string                             // scenario subtest label
		provider *gatewayprovider.ExecutionProvider // configured provider fixture
		wantMix  bool                               // expected native-tool injection
	}{
		{
			name: "free credential tier",
			provider: func() *gatewayprovider.ExecutionProvider {
				a := gatewaytestkit.HealthyGrokOAuthProvider(910, "access-token")
				a.Record.Credentials["subscription_tier"] = "free"
				return a
			}(),
			wantMix: true,
		},
		{
			name: "free billing tier",
			provider: func() *gatewayprovider.ExecutionProvider {
				a := gatewaytestkit.HealthyGrokOAuthProvider(911, "access-token")
				a.Record.Extra = map[string]any{providercore.GrokUsageBillingExtraKey: map[string]any{"plan": "FREE"}}
				return a
			}(),
			wantMix: true,
		},
		{
			name: "free successful billing has blank plan",
			provider: func() *gatewayprovider.ExecutionProvider {
				a := gatewaytestkit.HealthyGrokOAuthProvider(9111, "access-token")
				a.Record.Extra = map[string]any{providercore.GrokUsageBillingExtraKey: map[string]any{
					"status_code":        http.StatusOK,
					"source":             "billing_probe",
					"monthly_updated_at": "2026-07-15T05:00:00Z",
				}}
				return a
			}(),
			wantMix: true,
		},
		{
			name: "free rolling token quota",
			provider: func() *gatewayprovider.ExecutionProvider {
				a := gatewaytestkit.HealthyGrokOAuthProvider(9112, "access-token")
				a.Record.Extra = map[string]any{providercore.GrokQuotaSnapshotExtraKey: map[string]any{
					"headers_observed": true,
					"tokens":           map[string]any{"limit": xai.GrokFreeRolling24hTokenLimit},
				}}
				return a
			}(),
			wantMix: true,
		},
		{
			name: "legacy free rolling token quota",
			provider: func() *gatewayprovider.ExecutionProvider {
				a := gatewaytestkit.HealthyGrokOAuthProvider(9113, "access-token")
				a.Record.Extra = map[string]any{providercore.GrokQuotaSnapshotExtraKey: map[string]any{
					"headers_observed": true,
					"tokens":           map[string]any{"limit": int64(2_000_000)},
				}}
				return a
			}(),
			wantMix: true,
		},
		{
			name: "supergrok remains unchanged",
			provider: func() *gatewayprovider.ExecutionProvider {
				a := gatewaytestkit.HealthyGrokOAuthProvider(912, "access-token")
				a.Record.Credentials["subscription_tier"] = "supergrok"
				return a
			}(),
		},
		{
			name: "paid billing overrides stale free quota",
			provider: func() *gatewayprovider.ExecutionProvider {
				a := gatewaytestkit.HealthyGrokOAuthProvider(9121, "access-token")
				a.Record.Extra = map[string]any{
					providercore.GrokUsageBillingExtraKey: map[string]any{"plan": "SuperGrok", "status_code": http.StatusOK},
					providercore.GrokQuotaSnapshotExtraKey: map[string]any{
						"headers_observed": true,
						"tokens":           map[string]any{"limit": int64(2_000_000)},
					},
				}
				return a
			}(),
		},
		{
			name: "paid billing overrides stale free credentials",
			provider: func() *gatewayprovider.ExecutionProvider {
				a := gatewaytestkit.HealthyGrokOAuthProvider(9123, "access-token")
				a.Record.Credentials["subscription_tier"] = "free"
				a.Record.Extra = map[string]any{
					providercore.GrokUsageBillingExtraKey: map[string]any{"plan": "SuperGrok", "status_code": http.StatusOK},
				}
				return a
			}(),
		},
		{
			name: "partial billing without monthly evidence remains unknown",
			provider: func() *gatewayprovider.ExecutionProvider {
				a := gatewaytestkit.HealthyGrokOAuthProvider(9122, "access-token")
				a.Record.Extra = map[string]any{providercore.GrokUsageBillingExtraKey: map[string]any{
					"status_code":    http.StatusOK,
					"source":         "billing_probe",
					"partial":        true,
					"failed_windows": []string{"monthly"},
				}}
				return a
			}(),
		},
		{
			name:     "unknown tier remains unchanged",
			provider: gatewaytestkit.HealthyGrokOAuthProvider(913, "access-token"),
		},
		{
			name: "api key remains unchanged",
			provider: &gatewayprovider.ExecutionProvider{
				Record: providercore.Record{
					LoadLocation: time.LoadLocation, ID: 914,
					Platform: capability.PlatformGrok,
					Type:     capability.ProviderTypeAPIKey,
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, err := gatewayprovider.ApplyGrokFreeMessagesFunctionToolCacheRoute(intentBody, intentBody, tt.provider, "isolated-id")

			require.NoError(t, err)
			tools := gjson.GetBytes(body, "tools").Array()
			if tt.wantMix {
				require.Len(t, tools, 3)
				require.Equal(t, "web_search", tools[1].Get("type").String())
				require.Equal(t, "x_search", tools[2].Get("type").String())
				return
			}
			require.Len(t, tools, 2, "non-free providers should not get native search injected")
		})
	}
}

// TestGrokFreeMessagesFunctionToolCacheRouteRequiresIdentity passes a known
// Free function request with empty cache identity. It checks the body remains
// equivalent and retains its original single declaration when isolated routing
// is unavailable.
func TestGrokFreeMessagesFunctionToolCacheRouteRequiresIdentity(t *testing.T) {
	provider := gatewaytestkit.HealthyGrokOAuthProvider(915, "access-token")
	provider.Record.Credentials["subscription_tier"] = "free"
	body := []byte(`{"model":"grok","tools":[{"type":"function","name":"lookup"}],"tool_choice":"auto"}`)

	patched, err := gatewayprovider.ApplyGrokFreeMessagesFunctionToolCacheRoute(body, body, provider, "")

	require.NoError(t, err)
	require.JSONEq(t, string(body), string(patched))
	require.Len(t, gjson.GetBytes(patched, "tools").Array(), 1)
}

// TestApplyGrokCacheIdentityPreservesIneligibleClientToolFields varies tool
// shapes and choices rejected by cache injection. It checks the isolated key
// is written while existence and raw bytes of tools and choice retain their
// supplied representations.
func TestApplyGrokCacheIdentityPreservesIneligibleClientToolFields(t *testing.T) {
	tests := []struct {
		name string // scenario subtest label
		body string // input JSON fixture
	}{
		{
			name: "empty tools array",
			body: `{"model":"grok","tools":[]}`,
		},
		{
			name: "null tools",
			body: `{"model":"grok","tools":null}`,
		},
		{
			name: "tool choice only",
			body: `{"model":"grok","tool_choice":{"type":"function","name":"lookup"}}`,
		},
		{
			name: "null tool choice",
			body: `{"model":"grok","tool_choice":null}`,
		},
		{
			name: "native tool with auto choice",
			body: `{"model":"grok","tools":[{"type":"web_search"}],"tool_choice":"auto"}`,
		},
		{
			name: "function with required choice",
			body: `{"model":"grok","tools":[{"type":"function","name":"lookup"}],"tool_choice":"required"}`,
		},
		{
			name: "function with none choice",
			body: `{"model":"grok","tools":[{"type":"function","name":"lookup"}],"tool_choice":"none"}`,
		},
		{
			name: "function with specific choice",
			body: `{"model":"grok","tools":[{"type":"function","name":"lookup"}],"tool_choice":{"type":"function","name":"lookup"}}`,
		},
		{
			name: "function with object auto choice",
			body: `{"model":"grok","tools":[{"type":"function","name":"lookup"}],"tool_choice":{"type":"auto"}}`,
		},
		{
			name: "function mixed with unsupported tool",
			body: `{"model":"grok","tools":[{"type":"function","name":"lookup"},{"type":"namespace","name":"client_tools"}],"tool_choice":"auto"}`,
		},
		{
			name: "unsupported tool only",
			body: `{"model":"grok","tools":[{"type":"namespace","name":"client_tools"}]}`,
		},
		{
			name: "chat completions function shape",
			body: `{"model":"grok","tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}],"tool_choice":"auto"}`,
		},
		{
			name: "incomplete responses function",
			body: `{"model":"grok","tools":[{"type":"function","parameters":{"type":"object"}}]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			beforeTools := gjson.Get(tt.body, "tools")
			beforeChoice := gjson.Get(tt.body, "tool_choice")
			body, err := xai.ApplyGrokResponsesCacheIdentity([]byte(tt.body), []byte(tt.body), "isolated-id", true)
			require.NoError(t, err)
			require.Equal(t, "isolated-id", gjson.GetBytes(body, "prompt_cache_key").String())
			require.Equal(t, beforeTools.Exists(), gjson.GetBytes(body, "tools").Exists())
			require.Equal(t, beforeTools.Raw, gjson.GetBytes(body, "tools").Raw)
			require.Equal(t, beforeChoice.Exists(), gjson.GetBytes(body, "tool_choice").Exists())
			require.Equal(t, beforeChoice.Raw, gjson.GetBytes(body, "tool_choice").Raw)
		})
	}
}

// TestApplyGrokCacheIdentityUsesPreSanitizationToolIntent provides unsupported
// original declarations beside an already stripped current body. It checks
// identity assignment does not treat filtering as permission to create native
// tools or a choice.
func TestApplyGrokCacheIdentityUsesPreSanitizationToolIntent(t *testing.T) {
	tests := []struct {
		name       string // scenario subtest label
		intentBody string // original tool-intent JSON
	}{
		{
			name:       "unsupported tools removed by sanitizer",
			intentBody: `{"model":"grok","tools":[{"type":"namespace","name":"client_tools"}]}`,
		},
		{
			name:       "tool choice removed with unsupported tool",
			intentBody: `{"model":"grok","tools":[{"type":"namespace","name":"client_tools"}],"tool_choice":{"type":"namespace","name":"client_tools"}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// This is the body after PatchGrokResponsesBody removes unsupported tools and their tool_choice.
			patchedBody := []byte(`{"model":"grok-4.5","input":"hello"}`)
			body, err := xai.ApplyGrokResponsesCacheIdentity(patchedBody, []byte(tt.intentBody), "isolated-id", true)

			require.NoError(t, err)
			require.Equal(t, "isolated-id", gjson.GetBytes(body, "prompt_cache_key").String())
			require.False(t, gjson.GetBytes(body, "tools").Exists())
			require.False(t, gjson.GetBytes(body, "tool_choice").Exists())
		})
	}
}

// TestApplyGrokCacheIdentityWithoutFreeTierRoutingOnlyWritesIdentity disables
// Free routing while supplying an isolated key for tool-free input. It checks
// the key is assigned and no tools or tool_choice fields are introduced.
func TestApplyGrokCacheIdentityWithoutFreeTierRoutingOnlyWritesIdentity(t *testing.T) {
	sourceBody := []byte(`{"model":"grok-4.5","input":"hello"}`)
	body, err := xai.ApplyGrokResponsesCacheIdentity(sourceBody, sourceBody, "isolated-id", false)

	require.NoError(t, err)
	require.Equal(t, "isolated-id", gjson.GetBytes(body, "prompt_cache_key").String())
	require.False(t, gjson.GetBytes(body, "tools").Exists())
	require.False(t, gjson.GetBytes(body, "tool_choice").Exists())
}

// TestGrokCompactRequestSkipsCacheIdentityAndNativeTools uses the compact
// route with a raw cache key. It checks identity resolution stays empty and
// application removes the key without adding tools or a tool choice.
func TestGrokCompactRequestSkipsCacheIdentityAndNativeTools(t *testing.T) {
	c := newGrokCacheTestContext(701)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/compact", nil)
	body := []byte(`{"model":"grok","input":"compact this","prompt_cache_key":"raw-client-key"}`)

	identity := ResolveGrokCacheIdentity(c, body, "", "grok-4.5")
	patched, err := xai.ApplyGrokResponsesCacheIdentity(body, body, identity, true)

	require.NoError(t, err)
	require.Empty(t, identity)
	require.False(t, gjson.GetBytes(patched, "prompt_cache_key").Exists())
	require.False(t, gjson.GetBytes(patched, "tools").Exists())
	require.False(t, gjson.GetBytes(patched, "tool_choice").Exists())
}

// TestResolveGrokCacheIdentityConcurrentDeterminism resolves one immutable
// prompt in fifty independent request contexts concurrently. It checks every
// worker returns the same nonempty identity for the same Key and final model.
func TestResolveGrokCacheIdentityConcurrentDeterminism(t *testing.T) {
	// Fifty independent callers exercise deterministic identity under concurrent execution.
	const workers = 50
	body := []byte(`{"model":"grok","messages":[{"role":"system","content":"stable"},{"role":"user","content":"hello"}]}`)
	// Capacity matches the worker count so every producer can publish before
	// Wait completes. Wait and close finish the producer phase before collection,
	// preventing a blocked send or a send to the closed result channel.
	identities := make(chan string, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			identities <- ResolveGrokCacheIdentity(newGrokCacheTestContext(501), body, "", "grok-4.5")
		})
	}
	wg.Wait()
	close(identities)

	var first string
	for identity := range identities {
		if first == "" {
			first = identity
		}
		require.Equal(t, first, identity)
	}
	require.NotEmpty(t, first)
}

// TestGrokCacheHeaderBrandCompatibility combines legacy opt-in with absent or
// recognized current opt-out values. It checks legacy routing is honored only
// when the current header is absent in the supplied cases.
func TestGrokCacheHeaderBrandCompatibility(t *testing.T) {
	for _, value := range []string{"", "0", "false"} {
		provider := gatewaytestkit.HealthyGrokOAuthProvider(9014, "access-token")
		provider.Record.Credentials["subscription_tier"] = "free"
		provider.Record.Extra = map[string]any{"grok_client_tool_cache_enabled": false}
		c := newGrokCacheTestContext(9014)
		c.Request.Header.Set("X-Sub2API-Grok-Client-Tool-Cache", "prefer-cache")
		if value != "" {
			c.Request.Header.Set("X-TokenRouter-Grok-Client-Tool-Cache", value)
		}
		intent := []byte(`{"model":"grok","tools":[{"type":"function","name":"view_image","parameters":{"type":"object"}}],"tool_choice":"auto"}`)
		body, err := xai.ApplyGrokResponsesCacheIdentity(intent, intent, "isolated-id", true)
		require.NoError(t, err)
		body, err = ApplyGrokFreeRequestToolCacheRoute(c, body, intent, provider, "isolated-id")
		require.NoError(t, err)
		want := 1
		if value == "" {
			want = 3
		}
		require.Len(t, gjson.GetBytes(body, "tools").Array(), want)
	}
}

// explicitGrokCacheSeed returns the prioritized raw session seed before
// identity isolation. It delegates supplied HTTP/body/argument precedence, and
// an absent usable signal produces an empty result for caller fallback.
func explicitGrokCacheSeed(c *gin.Context, body []byte, explicitKey string) string {
	return xai.ExplicitCacheSeed(grokCacheInput(c, explicitKey, ""), body)
}

// TestCodexUnsupportedAdditionalToolsDoNotBecomeToolFreeCacheIntent filters
// unsupported additional tools before Free routing sees the patched intent. It
// checks isolated identity remains while neither tools nor choice are injected
// into the resulting body.
func TestCodexUnsupportedAdditionalToolsDoNotBecomeToolFreeCacheIntent(t *testing.T) {
	body := []byte(`{
		"model":"grok-4.5","tool_choice":"auto",
		"input":[
			{"type":"additional_tools","role":"developer","tools":[
				{"type":"custom","name":"apply_patch"},
				{"type":"namespace","name":"collaboration"}
			]},
			{"type":"message","role":"user","content":"hello"}
		]
	}`)
	patched, err := gatewayprovider.GrokBodyCodec().PatchGrokResponsesBody(body, "grok-4.5")
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(patched, "tools").Exists())
	require.False(t, gjson.GetBytes(patched, "tool_choice").Exists())

	mixedCacheIntent := patched
	patched, err = xai.ApplyGrokResponsesCacheIdentity(patched, body, "isolated-id", true)
	require.NoError(t, err)
	provider := gatewaytestkit.HealthyGrokOAuthProvider(4502, "access-token")
	provider.Record.Credentials["subscription_tier"] = "free"
	patched, err = ApplyGrokFreeRequestToolCacheRoute(nil, patched, mixedCacheIntent, provider, "isolated-id")

	require.NoError(t, err)
	require.False(t, gjson.GetBytes(patched, "tools").Exists())
	require.False(t, gjson.GetBytes(patched, "tool_choice").Exists())
	require.Equal(t, "isolated-id", gjson.GetBytes(patched, "prompt_cache_key").String())
}
