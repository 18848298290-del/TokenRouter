package session

import (
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// benchmarkStringSink retains generated hashes and cache text after benchmark
// calls so the measured results remain observable outside each loop iteration.
var benchmarkStringSink string

// hashTestAdapter delegates fixture requests to the generator with observation
// disabled.
type hashTestAdapter struct{}

type (
	// SessionContext aliases the source identity shared by these request-hash fixtures.
	SessionContext = requeststate.SessionContext
	// ParsedRequest aliases parsed body ranges and source state used by these fixtures.
	ParsedRequest = requeststate.ParsedRequest
)

// TestGenerateSessionHash_NilParsedRequest passes a nil parsed request through
// the adapter. The generator returns empty so no affinity can be inferred from
// a missing request object.
func TestGenerateSessionHash_NilParsedRequest(t *testing.T) {
	svc := &hashTestAdapter{}
	require.Empty(t, svc.GenerateSessionHash(nil))
}

// TestGenerateSessionHash_EmptyRequest supplies a zero-valued parsed request
// with no source context. Its lack of metadata, marked content, and readable
// fallback data produces an empty hash.
func TestGenerateSessionHash_EmptyRequest(t *testing.T) {
	svc := &hashTestAdapter{}
	require.Empty(t, svc.GenerateSessionHash(&ParsedRequest{}))
}

// TestGenerateSessionHash_MetadataHasHighestPriority supplies valid legacy
// metadata together with system and user text. The result equals the parsed
// session ID rather than a content-derived hash for that request.
func TestGenerateSessionHash_MetadataHasHighestPriority(t *testing.T) {
	svc := &hashTestAdapter{}
	metadata := "user_a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2_account__session_123e4567-e89b-12d3-a456-426614174000"
	parsed := mustParseSessionHashRequest(t, anthropicSessionBody("You are a helpful assistant.", []any{msg("user", "hello")}, metadata), nil)

	hash := svc.GenerateSessionHash(parsed)
	require.Equal(t, "123e4567-e89b-12d3-a456-426614174000", hash, "metadata session_id should have highest priority")
}

// TestGenerateSessionHash_SystemPlusMessages compares the same user message
// with and without system text. Both cases produce hashes and the distinct
// system contribution changes the compared fallback values.
func TestGenerateSessionHash_SystemPlusMessages(t *testing.T) {
	svc := &hashTestAdapter{}
	withSystem := mustParseSessionHashRequest(t, anthropicSessionBody("You are a helpful assistant.", []any{msg("user", "hello")}, ""), nil)
	withoutSystem := mustParseSessionHashRequest(t, anthropicSessionBody(nil, []any{msg("user", "hello")}, ""), nil)

	h1 := svc.GenerateSessionHash(withSystem)
	h2 := svc.GenerateSessionHash(withoutSystem)
	require.NotEmpty(t, h1)
	require.NotEmpty(t, h2)
	require.NotEqual(t, h1, h2, "system prompt should be part of digest, producing different hash")
}

// TestGenerateSessionHash_SystemOnlyProducesHash supplies system text with no
// messages or source context. The fallback still returns a nonempty hash from
// that readable system data alone.
func TestGenerateSessionHash_SystemOnlyProducesHash(t *testing.T) {
	svc := &hashTestAdapter{}
	parsed := mustParseSessionHashRequest(t, anthropicSessionBody("You are a helpful assistant.", nil, ""), nil)

	hash := svc.GenerateSessionHash(parsed)
	require.NotEmpty(t, hash, "system prompt alone should produce a hash as part of full digest")
}

// TestGenerateSessionHash_DifferentSystemsSameMessages changes system text
// while retaining the same user-message fixture. The two fallback hashes
// differ, showing that the compared system values contribute to this identity
// path.
func TestGenerateSessionHash_DifferentSystemsSameMessages(t *testing.T) {
	svc := &hashTestAdapter{}
	parsed1 := mustParseSessionHashRequest(t, anthropicSessionBody("You are assistant A.", []any{msg("user", "hello")}, ""), nil)
	parsed2 := mustParseSessionHashRequest(t, anthropicSessionBody("You are assistant B.", []any{msg("user", "hello")}, ""), nil)

	h1 := svc.GenerateSessionHash(parsed1)
	h2 := svc.GenerateSessionHash(parsed2)
	require.NotEqual(t, h1, h2, "different system prompts with same messages should produce different hashes")
}

// TestGenerateSessionHash_SameSystemSameMessages parses matching system and
// conversation data in two request objects. The generated hashes remain equal
// for repeated readable fallback content in this fixture.
func TestGenerateSessionHash_SameSystemSameMessages(t *testing.T) {
	svc := &hashTestAdapter{}
	mk := func() *ParsedRequest {
		return mustParseSessionHashRequest(t, anthropicSessionBody("You are a helpful assistant.", []any{msg("user", "hello"), msg("assistant", "hi")}, ""), nil)
	}

	h1 := svc.GenerateSessionHash(mk())
	h2 := svc.GenerateSessionHash(mk())
	require.Equal(t, h1, h2, "same system + same messages should produce identical hash")
}

// TestGenerateSessionHash_DifferentMessagesProduceDifferentHash changes one
// user question under equal system text. The Go and Python question fixtures
// produce different fallback hashes from their distinct readable message
// content.
func TestGenerateSessionHash_DifferentMessagesProduceDifferentHash(t *testing.T) {
	svc := &hashTestAdapter{}
	parsed1 := mustParseSessionHashRequest(t, anthropicSessionBody("You are a helpful assistant.", []any{msg("user", "help me with Go")}, ""), nil)
	parsed2 := mustParseSessionHashRequest(t, anthropicSessionBody("You are a helpful assistant.", []any{msg("user", "help me with Python")}, ""), nil)

	h1 := svc.GenerateSessionHash(parsed1)
	h2 := svc.GenerateSessionHash(parsed2)
	require.NotEqual(t, h1, h2, "same system but different messages should produce different hashes")
}

// TestGenerateSessionHash_DifferentSessionContextProducesDifferentHash uses
// equal message text under two different client-source contexts. Both hashes
// are nonempty and differ when the supplied address, client product, and API
// Key change together.
func TestGenerateSessionHash_DifferentSessionContextProducesDifferentHash(t *testing.T) {
	svc := &hashTestAdapter{}
	body := anthropicSessionBody(nil, []any{msg("user", "hello")}, "")
	parsed1 := mustParseSessionHashRequest(t, body, &SessionContext{ClientIP: "192.168.1.1", UserAgent: "Mozilla/5.0", APIKeyID: 100})
	parsed2 := mustParseSessionHashRequest(t, body, &SessionContext{ClientIP: "10.0.0.1", UserAgent: "curl/7.0", APIKeyID: 200})

	h1 := svc.GenerateSessionHash(parsed1)
	h2 := svc.GenerateSessionHash(parsed2)
	require.NotEmpty(t, h1)
	require.NotEmpty(t, h2)
	require.NotEqual(t, h1, h2, "same messages but different SessionContext should produce different hashes")
}

// TestGenerateSessionHash_SameSessionContextProducesSameHash reparses matching
// messages with the same source-context pointer. Both calls return equal
// hashes from the supplied content and client-source values in this fixture.
func TestGenerateSessionHash_SameSessionContextProducesSameHash(t *testing.T) {
	svc := &hashTestAdapter{}
	ctx := &SessionContext{ClientIP: "192.168.1.1", UserAgent: "Mozilla/5.0", APIKeyID: 100}
	body := anthropicSessionBody(nil, []any{msg("user", "hello")}, "")
	mk := func() *ParsedRequest { return mustParseSessionHashRequest(t, body, ctx) }

	h1 := svc.GenerateSessionHash(mk())
	h2 := svc.GenerateSessionHash(mk())
	require.Equal(t, h1, h2, "same messages + same SessionContext should produce identical hash")
}

// TestGenerateSessionHash_MetadataOverridesSessionContext combines valid
// legacy metadata with a populated client-source context. The parsed metadata
// session ID is returned, demonstrating its priority before the supplied
// source fields.
func TestGenerateSessionHash_MetadataOverridesSessionContext(t *testing.T) {
	svc := &hashTestAdapter{}
	metadata := "user_a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2_account__session_123e4567-e89b-12d3-a456-426614174000"
	parsed := mustParseSessionHashRequest(t, anthropicSessionBody(nil, []any{msg("user", "hello")}, metadata), &SessionContext{ClientIP: "192.168.1.1", UserAgent: "Mozilla/5.0", APIKeyID: 100})

	hash := svc.GenerateSessionHash(parsed)
	require.Equal(t, "123e4567-e89b-12d3-a456-426614174000", hash, "metadata session_id should take priority over SessionContext")
}

// TestGenerateSessionHash_MetadataJSON_HasHighestPriority supplies a valid
// JSON-encoded metadata identity with system and message text. The generator
// returns that encoded identity's session ID before considering the readable
// content fallback.
func TestGenerateSessionHash_MetadataJSON_HasHighestPriority(t *testing.T) {
	svc := &hashTestAdapter{}
	metadata := `{"device_id":"a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2","account_uuid":"","session_id":"c72554f2-1234-5678-abcd-123456789abc"}`
	parsed := mustParseSessionHashRequest(t, anthropicSessionBody("You are a helpful assistant.", []any{msg("user", "hello")}, metadata), nil)

	hash := svc.GenerateSessionHash(parsed)
	require.Equal(t, "c72554f2-1234-5678-abcd-123456789abc", hash, "JSON format metadata session_id should have highest priority")
}

// TestGenerateSessionHash_NilSessionContextBackwardCompatible parses the same
// message twice with nil source context in both cases. The resulting hashes
// are equal; this comparison covers repeated nil-context behavior rather than
// a historical implementation.
func TestGenerateSessionHash_NilSessionContextBackwardCompatible(t *testing.T) {
	svc := &hashTestAdapter{}
	body := anthropicSessionBody(nil, []any{msg("user", "hello")}, "")
	withCtx := mustParseSessionHashRequest(t, body, nil)
	withoutCtx := mustParseSessionHashRequest(t, body, nil)

	h1 := svc.GenerateSessionHash(withCtx)
	h2 := svc.GenerateSessionHash(withoutCtx)
	require.Equal(t, h1, h2, "nil SessionContext should produce same hash as no SessionContext")
}

// TestGenerateSessionHash_ContinuousConversation_HashChangesWithMessages grows
// an Anthropic conversation through three distinct transcript snapshots. Every
// hash is nonempty and each pair differs as additional readable message text
// enters the fallback.
func TestGenerateSessionHash_ContinuousConversation_HashChangesWithMessages(t *testing.T) {
	svc := &hashTestAdapter{}
	ctx := &SessionContext{ClientIP: "1.2.3.4", UserAgent: "test", APIKeyID: 1}
	round1 := mustParseSessionHashRequest(t, anthropicSessionBody("You are a helpful assistant.", []any{msg("user", "hello")}, ""), ctx)
	round2 := mustParseSessionHashRequest(t, anthropicSessionBody("You are a helpful assistant.", []any{msg("user", "hello"), msg("assistant", "Hi there!"), msg("user", "How are you?")}, ""), ctx)
	round3 := mustParseSessionHashRequest(t, anthropicSessionBody("You are a helpful assistant.", []any{msg("user", "hello"), msg("assistant", "Hi there!"), msg("user", "How are you?"), msg("assistant", "I'm doing well!"), msg("user", "Tell me a joke")}, ""), ctx)

	h1 := svc.GenerateSessionHash(round1)
	h2 := svc.GenerateSessionHash(round2)
	h3 := svc.GenerateSessionHash(round3)
	require.NotEmpty(t, h1)
	require.NotEmpty(t, h2)
	require.NotEmpty(t, h3)
	require.NotEqual(t, h1, h2, "different conversation rounds should produce different hashes")
	require.NotEqual(t, h2, h3, "each new round should produce a different hash")
	require.NotEqual(t, h1, h3, "round 1 and round 3 should differ")
}

// TestGenerateSessionHash_ContinuousConversation_SameRoundSameHash parses the
// same multi-turn snapshot twice under equal source context. The hash repeats
// for that identical conversation state, supporting retry affinity for this
// fixture.
func TestGenerateSessionHash_ContinuousConversation_SameRoundSameHash(t *testing.T) {
	svc := &hashTestAdapter{}
	ctx := &SessionContext{ClientIP: "1.2.3.4", UserAgent: "test", APIKeyID: 1}
	body := anthropicSessionBody("You are a helpful assistant.", []any{msg("user", "hello"), msg("assistant", "Hi there!"), msg("user", "How are you?")}, "")
	mk := func() *ParsedRequest { return mustParseSessionHashRequest(t, body, ctx) }

	h1 := svc.GenerateSessionHash(mk())
	h2 := svc.GenerateSessionHash(mk())
	require.Equal(t, h1, h2, "same conversation state should produce identical hash on retry")
}

// TestGenerateSessionHash_ResponsesDifferentInputProducesDifferentHash changes
// the first Responses user question under one source context. The two nonempty
// hashes differ for the Go and Python anchor texts supplied in those requests.
func TestGenerateSessionHash_ResponsesDifferentInputProducesDifferentHash(t *testing.T) {
	svc := &hashTestAdapter{}
	ctx := &SessionContext{ClientIP: "1.2.3.4", UserAgent: "codex_cli_rs/0.1.0", APIKeyID: 1}
	first := mustParseResponsesSessionHashRequest(t, `{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"help me with Go"}]}]}`, ctx)
	second := mustParseResponsesSessionHashRequest(t, `{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"help me with Python"}]}]}`, ctx)

	h1 := svc.GenerateSessionHash(first)
	h2 := svc.GenerateSessionHash(second)
	require.NotEmpty(t, h1)
	require.NotEmpty(t, h2)
	require.NotEqual(t, h1, h2, "different Responses input should produce different hashes for the same client")
}

// TestGenerateSessionHash_ResponsesGrowingInputKeepsStableHash extends
// Responses history after equal developer text and first user input. The
// extended request keeps the first request's nonempty hash because later
// dialogue lies beyond its selected anchor.
func TestGenerateSessionHash_ResponsesGrowingInputKeepsStableHash(t *testing.T) {
	svc := &hashTestAdapter{}
	ctx := &SessionContext{ClientIP: "1.2.3.4", UserAgent: "codex_cli_rs/0.1.0", APIKeyID: 1}
	round1 := mustParseResponsesSessionHashRequest(t, `{"input":[{"type":"message","role":"developer","content":[{"type":"input_text","text":"Be concise."}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"help me with Go"}]}]}`, ctx)
	round2 := mustParseResponsesSessionHashRequest(t, `{"input":[{"type":"message","role":"developer","content":[{"type":"input_text","text":"Be concise."}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"help me with Go"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Sure."}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"add tests"}]}]}`, ctx)

	h1 := svc.GenerateSessionHash(round1)
	h2 := svc.GenerateSessionHash(round2)
	require.NotEmpty(t, h1)
	require.Equal(t, h1, h2, "Responses input growth should preserve the hash when the conversation prefix is stable")
}

// TestGenerateSessionHash_MessagesPathIgnoresResponsesInput supplies readable
// messages alongside two different Responses input strings. Both hashes remain
// equal because message text takes precedence over the alternative input
// representation.
func TestGenerateSessionHash_MessagesPathIgnoresResponsesInput(t *testing.T) {
	svc := &hashTestAdapter{}
	ctx := &SessionContext{ClientIP: "1.2.3.4", UserAgent: "test", APIKeyID: 1}
	first := mustParseResponsesSessionHashRequest(t, `{"messages":[{"role":"user","content":"hello"}],"input":"first"}`, ctx)
	second := mustParseResponsesSessionHashRequest(t, `{"messages":[{"role":"user","content":"hello"}],"input":"second"}`, ctx)

	h1 := svc.GenerateSessionHash(first)
	h2 := svc.GenerateSessionHash(second)
	require.Equal(t, h1, h2, "existing messages fallback should remain authoritative when messages contain text")
}

// TestGenerateSessionHash_ResponsesInputDoesNotOverrideHigherPrioritySources
// adds Responses input alongside metadata or marked system evidence. Valid
// metadata yields its session ID, while matching ephemeral text yields equal
// hashes across the two distinct source contexts.
func TestGenerateSessionHash_ResponsesInputDoesNotOverrideHigherPrioritySources(t *testing.T) {
	svc := &hashTestAdapter{}
	ctx := &SessionContext{ClientIP: "1.2.3.4", UserAgent: "test", APIKeyID: 1}

	t.Run("metadata user id", func(t *testing.T) {
		metadata := "user_a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2_account__session_123e4567-e89b-12d3-a456-426614174000"
		parsed := mustParseResponsesSessionHashRequest(t, `{"metadata":{"user_id":"`+metadata+`"},"input":"hello"}`, ctx)
		require.Equal(t, "123e4567-e89b-12d3-a456-426614174000", svc.GenerateSessionHash(parsed))
	})

	t.Run("cache control", func(t *testing.T) {
		body := `{"system":[{"type":"text","text":"stable cache anchor","cache_control":{"type":"ephemeral"}}],"input":"hello"}`
		first := mustParseResponsesSessionHashRequest(t, body, ctx)
		second := mustParseResponsesSessionHashRequest(t, body, &SessionContext{ClientIP: "9.8.7.6", UserAgent: "other", APIKeyID: 2})
		require.Equal(t, svc.GenerateSessionHash(first), svc.GenerateSessionHash(second))
	})
}

// TestGenerateSessionHash_MessageRollback changes the final user message in a
// multi-turn Anthropic transcript. The compared hashes differ, confirming that
// this Messages fallback includes the edited last-turn text.
func TestGenerateSessionHash_MessageRollback(t *testing.T) {
	svc := &hashTestAdapter{}
	ctx := &SessionContext{ClientIP: "1.2.3.4", UserAgent: "test", APIKeyID: 1}
	original := mustParseSessionHashRequest(t, anthropicSessionBody("System prompt", []any{msg("user", "msg1"), msg("assistant", "reply1"), msg("user", "msg2"), msg("assistant", "reply2"), msg("user", "msg3")}, ""), ctx)
	rollback := mustParseSessionHashRequest(t, anthropicSessionBody("System prompt", []any{msg("user", "msg1"), msg("assistant", "reply1"), msg("user", "msg2"), msg("assistant", "reply2"), msg("user", "different msg3")}, ""), ctx)

	hOrig := svc.GenerateSessionHash(original)
	hRollback := svc.GenerateSessionHash(rollback)
	require.NotEqual(t, hOrig, hRollback, "rollback with different last message should produce different hash")
}

// TestGenerateSessionHash_MessageRollbackSameContent reparses the same shorter
// conversation state for two invocations. Both hashes remain equal under the
// same context and content, covering a repeated shorter request.
func TestGenerateSessionHash_MessageRollbackSameContent(t *testing.T) {
	svc := &hashTestAdapter{}
	ctx := &SessionContext{ClientIP: "1.2.3.4", UserAgent: "test", APIKeyID: 1}
	body := anthropicSessionBody("System prompt", []any{msg("user", "msg1"), msg("assistant", "reply1"), msg("user", "msg2")}, "")
	mk := func() *ParsedRequest { return mustParseSessionHashRequest(t, body, ctx) }

	h1 := svc.GenerateSessionHash(mk())
	h2 := svc.GenerateSessionHash(mk())
	require.Equal(t, h1, h2, "rollback and resend same content should produce same hash")
}

// TestGenerateSessionHash_SameSystemDifferentUsers combines equal system text
// with different questions and source identities. The resulting hashes differ
// for these fixtures; content and context both change in the comparison.
func TestGenerateSessionHash_SameSystemDifferentUsers(t *testing.T) {
	svc := &hashTestAdapter{}
	user1 := mustParseSessionHashRequest(t, anthropicSessionBody("You are a code reviewer.", []any{msg("user", "Review this Go code")}, ""), &SessionContext{ClientIP: "1.1.1.1", UserAgent: "vscode", APIKeyID: 1})
	user2 := mustParseSessionHashRequest(t, anthropicSessionBody("You are a code reviewer.", []any{msg("user", "Review this Python code")}, ""), &SessionContext{ClientIP: "2.2.2.2", UserAgent: "vscode", APIKeyID: 2})

	h1 := svc.GenerateSessionHash(user1)
	h2 := svc.GenerateSessionHash(user2)
	require.NotEqual(t, h1, h2, "different users with different messages should get different hashes")
}

// TestGenerateSessionHash_SameSystemSameMessageDifferentContext reuses the
// same system and message under different addresses and API Keys. The two
// fallback hashes differ despite equal content, exposing the source-context
// contribution in this comparison.
func TestGenerateSessionHash_SameSystemSameMessageDifferentContext(t *testing.T) {
	svc := &hashTestAdapter{}
	body := anthropicSessionBody("You are a helpful assistant.", []any{msg("user", "hello")}, "")
	user1 := mustParseSessionHashRequest(t, body, &SessionContext{ClientIP: "1.1.1.1", UserAgent: "Mozilla/5.0", APIKeyID: 10})
	user2 := mustParseSessionHashRequest(t, body, &SessionContext{ClientIP: "2.2.2.2", UserAgent: "Mozilla/5.0", APIKeyID: 20})

	h1 := svc.GenerateSessionHash(user1)
	h2 := svc.GenerateSessionHash(user2)
	require.NotEqual(t, h1, h2, "CRITICAL: same system+messages but different users should get different hashes")
}

// TestGenerateSessionHash_SessionContext_IPDifference changes the client IP
// with equal message, client product, and API Key. The fallback hashes differ
// for the two supplied addresses, isolating the address contribution to the
// comparison.
func TestGenerateSessionHash_SessionContext_IPDifference(t *testing.T) {
	svc := &hashTestAdapter{}
	body := anthropicSessionBody(nil, []any{msg("user", "test")}, "")
	base := func(ip string) *ParsedRequest {
		return mustParseSessionHashRequest(t, body, &SessionContext{ClientIP: ip, UserAgent: "same-ua", APIKeyID: 1})
	}

	h1 := svc.GenerateSessionHash(base("1.1.1.1"))
	h2 := svc.GenerateSessionHash(base("2.2.2.2"))
	require.NotEqual(t, h1, h2, "different IP should produce different hash")
}

// TestGenerateSessionHash_SessionContext_UADifference changes the User-Agent
// product under equal IP, key, and content. The Mozilla and curl fixture
// products produce different fallback hashes after client-product
// normalization.
func TestGenerateSessionHash_SessionContext_UADifference(t *testing.T) {
	svc := &hashTestAdapter{}
	body := anthropicSessionBody(nil, []any{msg("user", "test")}, "")
	base := func(ua string) *ParsedRequest {
		return mustParseSessionHashRequest(t, body, &SessionContext{ClientIP: "1.1.1.1", UserAgent: ua, APIKeyID: 1})
	}

	h1 := svc.GenerateSessionHash(base("Mozilla/5.0"))
	h2 := svc.GenerateSessionHash(base("curl/7.0"))
	require.NotEqual(t, h1, h2, "different User-Agent should produce different hash")
}

// TestGenerateSessionHash_SessionContext_UAVersionNoiseIgnored changes only
// the version of the product-style Codex User-Agent. Equal content and other
// source fields retain the same hash after those version values are
// normalized.
func TestGenerateSessionHash_SessionContext_UAVersionNoiseIgnored(t *testing.T) {
	svc := &hashTestAdapter{}
	body := anthropicSessionBody(nil, []any{msg("user", "test")}, "")
	base := func(ua string) *ParsedRequest {
		return mustParseSessionHashRequest(t, body, &SessionContext{ClientIP: "1.1.1.1", UserAgent: ua, APIKeyID: 1})
	}

	h1 := svc.GenerateSessionHash(base("Mozilla/5.0 codex_cli_rs/0.1.0"))
	h2 := svc.GenerateSessionHash(base("Mozilla/5.0 codex_cli_rs/0.1.1"))
	require.Equal(t, h1, h2, "version-only User-Agent changes should not perturb the sticky session hash")
}

// TestGenerateSessionHash_SessionContext_FreeformUAVersionNoiseIgnored changes
// only the freeform Codex CLI version in source context. The compared hashes
// stay equal for these fixtures while address, API Key, and message text
// remain fixed.
func TestGenerateSessionHash_SessionContext_FreeformUAVersionNoiseIgnored(t *testing.T) {
	svc := &hashTestAdapter{}
	body := anthropicSessionBody(nil, []any{msg("user", "test")}, "")
	base := func(ua string) *ParsedRequest {
		return mustParseSessionHashRequest(t, body, &SessionContext{ClientIP: "1.1.1.1", UserAgent: ua, APIKeyID: 1})
	}

	h1 := svc.GenerateSessionHash(base("Codex CLI 0.1.0"))
	h2 := svc.GenerateSessionHash(base("Codex CLI 0.1.1"))
	require.Equal(t, h1, h2, "free-form version-only User-Agent changes should not perturb the sticky session hash")
}

// TestGenerateSessionHash_SessionContext_APIKeyIDDifference changes the API
// Key ID with equal IP, User-Agent, and message. The two fallback hashes
// differ, isolating credential identity as the changed source component in
// this fixture.
func TestGenerateSessionHash_SessionContext_APIKeyIDDifference(t *testing.T) {
	svc := &hashTestAdapter{}
	body := anthropicSessionBody(nil, []any{msg("user", "test")}, "")
	base := func(keyID int64) *ParsedRequest {
		return mustParseSessionHashRequest(t, body, &SessionContext{ClientIP: "1.1.1.1", UserAgent: "same-ua", APIKeyID: keyID})
	}

	h1 := svc.GenerateSessionHash(base(1))
	h2 := svc.GenerateSessionHash(base(2))
	require.NotEqual(t, h1, h2, "different APIKeyID should produce different hash")
}

// TestGenerateSessionHash_MultipleUsersSameFirstMessage supplies one greeting
// under five different source contexts. Every generated hash is nonempty and
// distinct within that finite address, client-product, and API Key fixture
// set.
func TestGenerateSessionHash_MultipleUsersSameFirstMessage(t *testing.T) {
	svc := &hashTestAdapter{}
	hashes := make(map[string]bool)
	body := anthropicSessionBody(nil, []any{msg("user", "hello")}, "")
	for i := range 5 {
		parsed := mustParseSessionHashRequest(t, body, &SessionContext{ClientIP: "192.168.1." + string(rune('1'+i)), UserAgent: "client-" + string(rune('A'+i)), APIKeyID: int64(i + 1)})
		h := svc.GenerateSessionHash(parsed)
		require.NotEmpty(t, h)
		require.False(t, hashes[h], "hash collision detected for user %d", i)
		hashes[h] = true
	}
	require.Len(t, hashes, 5, "5 different users should produce 5 unique hashes")
}

// TestGenerateSessionHash_SameUserGrowingConversation extends one user's
// Messages transcript through successive odd-length prefixes. Each round
// produces a new nonempty hash, while repeating an unchanged round returns the
// same value.
func TestGenerateSessionHash_SameUserGrowingConversation(t *testing.T) {
	svc := &hashTestAdapter{}
	ctx := &SessionContext{ClientIP: "1.2.3.4", UserAgent: "browser", APIKeyID: 42}
	messages := []any{
		msg("user", "msg1"), msg("assistant", "reply1"), msg("user", "msg2"), msg("assistant", "reply2"),
		msg("user", "msg3"), msg("assistant", "reply3"), msg("user", "msg4"),
	}

	prevHash := ""
	for round := 1; round <= len(messages); round += 2 {
		parsed := mustParseSessionHashRequest(t, anthropicSessionBody("System", messages[:round], ""), ctx)
		h := svc.GenerateSessionHash(parsed)
		require.NotEmpty(t, h, "round %d hash should not be empty", round)
		if prevHash != "" {
			require.NotEqual(t, prevHash, h, "round %d hash should differ from previous round", round)
		}
		prevHash = h
		h2 := svc.GenerateSessionHash(parsed)
		require.Equal(t, h, h2, "retry of round %d should produce same hash", round)
	}
}

// TestGenerateSessionHash_MultipleUserMessages changes the second of five user
// messages in one conversation. The hash differs even though the first user
// message is unchanged, covering full Messages text selection.
func TestGenerateSessionHash_MultipleUserMessages(t *testing.T) {
	svc := &hashTestAdapter{}
	ctx := &SessionContext{ClientIP: "1.2.3.4", UserAgent: "test", APIKeyID: 1}
	parsed := mustParseSessionHashRequest(t, anthropicSessionBody(nil, []any{msg("user", "first"), msg("user", "second"), msg("user", "third"), msg("user", "fourth"), msg("user", "fifth")}, ""), ctx)
	h := svc.GenerateSessionHash(parsed)
	require.NotEmpty(t, h)

	parsed2 := mustParseSessionHashRequest(t, anthropicSessionBody(nil, []any{msg("user", "first"), msg("user", "CHANGED"), msg("user", "third"), msg("user", "fourth"), msg("user", "fifth")}, ""), ctx)
	h2 := svc.GenerateSessionHash(parsed2)
	require.NotEqual(t, h, h2, "changing any message should change the hash")
}

// TestGenerateSessionHash_MessageOrderMatters reverses two readable
// user-message strings under the same source context. The compared fallback
// hashes differ when those text pieces are concatenated in the opposite order.
func TestGenerateSessionHash_MessageOrderMatters(t *testing.T) {
	svc := &hashTestAdapter{}
	ctx := &SessionContext{ClientIP: "1.2.3.4", UserAgent: "test", APIKeyID: 1}
	parsed1 := mustParseSessionHashRequest(t, anthropicSessionBody(nil, []any{msg("user", "alpha"), msg("user", "beta")}, ""), ctx)
	parsed2 := mustParseSessionHashRequest(t, anthropicSessionBody(nil, []any{msg("user", "beta"), msg("user", "alpha")}, ""), ctx)

	h1 := svc.GenerateSessionHash(parsed1)
	h2 := svc.GenerateSessionHash(parsed2)
	require.NotEqual(t, h1, h2, "message order should affect the hash")
}

// TestGenerateSessionHash_StructuredContent supplies one message containing
// two text-typed blocks. Its generated hash is nonempty, covering readable
// array content under the supplied context without asserting nontext handling.
func TestGenerateSessionHash_StructuredContent(t *testing.T) {
	svc := &hashTestAdapter{}
	ctx := &SessionContext{ClientIP: "1.2.3.4", UserAgent: "test", APIKeyID: 1}
	content := []any{map[string]any{"type": "text", "text": "Look at this"}, map[string]any{"type": "text", "text": "And this too"}}
	parsed := mustParseSessionHashRequest(t, anthropicSessionBody(nil, []any{msg("user", content)}, ""), ctx)

	h := svc.GenerateSessionHash(parsed)
	require.NotEmpty(t, h, "structured content should produce a hash")
}

// TestGenerateSessionHash_ArraySystemPrompt supplies two system text blocks
// with a user message. The generator returns a nonempty hash for that array
// representation combined with the populated source context.
func TestGenerateSessionHash_ArraySystemPrompt(t *testing.T) {
	svc := &hashTestAdapter{}
	ctx := &SessionContext{ClientIP: "1.2.3.4", UserAgent: "test", APIKeyID: 1}
	system := []any{map[string]any{"type": "text", "text": "You are a helpful assistant."}, map[string]any{"type": "text", "text": "Be concise."}}
	parsed := mustParseSessionHashRequest(t, anthropicSessionBody(system, []any{msg("user", "hello")}, ""), ctx)

	h := svc.GenerateSessionHash(parsed)
	require.NotEmpty(t, h, "array system prompt should produce a hash")
}

// TestGenerateSessionHash_CacheControlOverridesSessionContext uses matching
// ephemeral system text under two different source contexts. The compared
// hashes are equal because selected marked content is hashed before the
// client-source fallback.
func TestGenerateSessionHash_CacheControlOverridesSessionContext(t *testing.T) {
	svc := &hashTestAdapter{}
	system := []any{map[string]any{"type": "text", "text": "You are a tool-specific assistant.", "cache_control": map[string]any{"type": "ephemeral"}}}
	body := anthropicSessionBody(system, []any{msg("user", "hello")}, "")
	parsed1 := mustParseSessionHashRequest(t, body, &SessionContext{ClientIP: "1.1.1.1", UserAgent: "ua1", APIKeyID: 100})
	parsed2 := mustParseSessionHashRequest(t, body, &SessionContext{ClientIP: "2.2.2.2", UserAgent: "ua2", APIKeyID: 200})

	h1 := svc.GenerateSessionHash(parsed1)
	h2 := svc.GenerateSessionHash(parsed2)
	require.Equal(t, h1, h2, "cache_control ephemeral has higher priority, SessionContext should not affect result")
}

// TestGenerateSessionHash_EmptyMessages supplies an empty messages array and a
// populated source context. The generator still returns a nonempty
// source-derived hash even though no readable message text is available.
func TestGenerateSessionHash_EmptyMessages(t *testing.T) {
	svc := &hashTestAdapter{}
	parsed := mustParseSessionHashRequest(t, anthropicSessionBody(nil, []any{}, ""), &SessionContext{ClientIP: "1.1.1.1", UserAgent: "test", APIKeyID: 1})

	h := svc.GenerateSessionHash(parsed)
	require.NotEmpty(t, h, "empty messages with SessionContext should still produce a hash from context")
}

// TestGenerateSessionHash_EmptyMessagesNoContext supplies an empty messages
// array without system data or source context. The generator returns empty
// because no metadata, marked anchor, or combined fallback seed remains.
func TestGenerateSessionHash_EmptyMessagesNoContext(t *testing.T) {
	svc := &hashTestAdapter{}
	parsed := mustParseSessionHashRequest(t, anthropicSessionBody(nil, []any{}, ""), nil)

	h := svc.GenerateSessionHash(parsed)
	require.Empty(t, h, "empty messages without SessionContext should produce empty hash")
}

// TestGenerateSessionHash_SessionContextWithEmptyFields compares an
// empty-field source context with a nil context for equal text. Their hashes
// differ because a present context still contributes separators and its zero
// API Key value.
func TestGenerateSessionHash_SessionContextWithEmptyFields(t *testing.T) {
	svc := &hashTestAdapter{}
	body := anthropicSessionBody(nil, []any{msg("user", "test")}, "")
	withEmptyCtx := mustParseSessionHashRequest(t, body, &SessionContext{ClientIP: "", UserAgent: "", APIKeyID: 0})
	withoutCtx := mustParseSessionHashRequest(t, body, nil)

	h1 := svc.GenerateSessionHash(withEmptyCtx)
	h2 := svc.GenerateSessionHash(withoutCtx)
	require.NotEqual(t, h1, h2, "empty-field SessionContext should still differ from nil SessionContext")
}

// TestGenerateSessionHash_LongConversation builds twenty user/assistant pairs
// and adds one more pair. The first hash is nonempty and the extension changes
// it as new readable text enters the Messages fallback.
func TestGenerateSessionHash_LongConversation(t *testing.T) {
	svc := &hashTestAdapter{}
	ctx := &SessionContext{ClientIP: "1.2.3.4", UserAgent: "test", APIKeyID: 1}
	messages := make([]any, 0, 40)
	for i := range 20 {
		messages = append(messages, msg("user", "user message "+string(rune('A'+i))))
		messages = append(messages, msg("assistant", "assistant reply "+string(rune('A'+i))))
	}

	parsed := mustParseSessionHashRequest(t, anthropicSessionBody("System prompt", messages, ""), ctx)
	h := svc.GenerateSessionHash(parsed)
	require.NotEmpty(t, h)

	moreMessages := append(append([]any{}, messages...), msg("user", "one more"), msg("assistant", "ok"))
	parsed2 := mustParseSessionHashRequest(t, anthropicSessionBody("System prompt", moreMessages, ""), ctx)
	h2 := svc.GenerateSessionHash(parsed2)
	require.NotEqual(t, h, h2, "adding more messages to long conversation should change hash")
}

// TestGenerateSessionHash_GeminiContentsProducesHash parses a Gemini user-text
// part with a populated source context. The shared generator returns a
// nonempty hash, covering the parsed Gemini parts representation on this
// fallback path.
func TestGenerateSessionHash_GeminiContentsProducesHash(t *testing.T) {
	svc := &hashTestAdapter{}
	parsed := mustParseGeminiSessionHashRequest(t, geminiSessionBody(nil, []any{geminiMsg("user", "Hello from Gemini")}), &SessionContext{ClientIP: "1.2.3.4", UserAgent: "gemini-cli", APIKeyID: 1})

	h := svc.GenerateSessionHash(parsed)
	require.NotEmpty(t, h, "Gemini contents with parts should produce a non-empty hash")
}

// TestGenerateSessionHash_GeminiDifferentContentsDifferentHash changes one
// Gemini user-text part while retaining source context. The Hello and Goodbye
// fixtures return different hashes from their distinct readable Gemini
// content.
func TestGenerateSessionHash_GeminiDifferentContentsDifferentHash(t *testing.T) {
	svc := &hashTestAdapter{}
	ctx := &SessionContext{ClientIP: "1.2.3.4", UserAgent: "gemini-cli", APIKeyID: 1}
	parsed1 := mustParseGeminiSessionHashRequest(t, geminiSessionBody(nil, []any{geminiMsg("user", "Hello")}), ctx)
	parsed2 := mustParseGeminiSessionHashRequest(t, geminiSessionBody(nil, []any{geminiMsg("user", "Goodbye")}), ctx)

	h1 := svc.GenerateSessionHash(parsed1)
	h2 := svc.GenerateSessionHash(parsed2)
	require.NotEqual(t, h1, h2, "different Gemini contents should produce different hashes")
}

// TestGenerateSessionHash_GeminiSameContentsSameHash parses the same
// user/model Gemini exchange twice. The hashes match for repeated parts and
// source context, establishing deterministic output for that supplied
// conversation.
func TestGenerateSessionHash_GeminiSameContentsSameHash(t *testing.T) {
	svc := &hashTestAdapter{}
	ctx := &SessionContext{ClientIP: "1.2.3.4", UserAgent: "gemini-cli", APIKeyID: 1}
	body := geminiSessionBody(nil, []any{geminiMsg("user", "Hello"), geminiMsg("model", "Hi there!")})
	mk := func() *ParsedRequest { return mustParseGeminiSessionHashRequest(t, body, ctx) }

	h1 := svc.GenerateSessionHash(mk())
	h2 := svc.GenerateSessionHash(mk())
	require.Equal(t, h1, h2, "same Gemini contents should produce identical hash")
}

// TestGenerateSessionHash_GeminiMultiTurnHashChanges extends a Gemini request
// with a model reply and later question. Both hashes are nonempty and differ
// as the parsed parts add readable message text to the fallback.
func TestGenerateSessionHash_GeminiMultiTurnHashChanges(t *testing.T) {
	svc := &hashTestAdapter{}
	ctx := &SessionContext{ClientIP: "1.2.3.4", UserAgent: "gemini-cli", APIKeyID: 1}
	round1 := mustParseGeminiSessionHashRequest(t, geminiSessionBody(nil, []any{geminiMsg("user", "hello")}), ctx)
	round2 := mustParseGeminiSessionHashRequest(t, geminiSessionBody(nil, []any{geminiMsg("user", "hello"), geminiMsg("model", "Hi!"), geminiMsg("user", "How are you?")}), ctx)

	h1 := svc.GenerateSessionHash(round1)
	h2 := svc.GenerateSessionHash(round2)
	require.NotEmpty(t, h1)
	require.NotEmpty(t, h2)
	require.NotEqual(t, h1, h2, "Gemini multi-turn should produce different hashes per round")
}

// TestGenerateSessionHash_GeminiDifferentUsersSameContentDifferentHash uses
// identical Gemini parts under different addresses and API Keys. The compared
// hashes differ for those source identities while the client product and
// content remain equal.
func TestGenerateSessionHash_GeminiDifferentUsersSameContentDifferentHash(t *testing.T) {
	svc := &hashTestAdapter{}
	body := geminiSessionBody(nil, []any{geminiMsg("user", "hello")})
	user1 := mustParseGeminiSessionHashRequest(t, body, &SessionContext{ClientIP: "1.1.1.1", UserAgent: "gemini-cli", APIKeyID: 10})
	user2 := mustParseGeminiSessionHashRequest(t, body, &SessionContext{ClientIP: "2.2.2.2", UserAgent: "gemini-cli", APIKeyID: 20})

	h1 := svc.GenerateSessionHash(user1)
	h2 := svc.GenerateSessionHash(user2)
	require.NotEqual(t, h1, h2, "CRITICAL: different Gemini users with same content must get different hashes")
}

// TestGenerateSessionHash_GeminiSystemInstructionAffectsHash adds Gemini
// system-instruction parts to otherwise equal content and context. The
// generated hash differs from the request without those parts, covering the
// parsed system contribution.
func TestGenerateSessionHash_GeminiSystemInstructionAffectsHash(t *testing.T) {
	svc := &hashTestAdapter{}
	ctx := &SessionContext{ClientIP: "1.2.3.4", UserAgent: "gemini-cli", APIKeyID: 1}
	withSys := mustParseGeminiSessionHashRequest(t, geminiSessionBody([]any{map[string]any{"text": "You are a coding assistant."}}, []any{geminiMsg("user", "hello")}), ctx)
	withoutSys := mustParseGeminiSessionHashRequest(t, geminiSessionBody(nil, []any{geminiMsg("user", "hello")}), ctx)

	h1 := svc.GenerateSessionHash(withSys)
	h2 := svc.GenerateSessionHash(withoutSys)
	require.NotEqual(t, h1, h2, "systemInstruction should affect the hash")
}

// TestGenerateSessionHash_GeminiMultiPartMessage changes the middle text part
// of a three-part Gemini user message. The first hash is nonempty and the
// edited-parts hash differs despite equal first and final part text.
func TestGenerateSessionHash_GeminiMultiPartMessage(t *testing.T) {
	svc := &hashTestAdapter{}
	ctx := &SessionContext{ClientIP: "1.2.3.4", UserAgent: "gemini-cli", APIKeyID: 1}
	parsed := mustParseGeminiSessionHashRequest(t, geminiSessionBody(nil, []any{geminiMsg("user", "Part 1", "Part 2", "Part 3")}), ctx)
	h := svc.GenerateSessionHash(parsed)
	require.NotEmpty(t, h, "multi-part Gemini message should produce a hash")

	parsed2 := mustParseGeminiSessionHashRequest(t, geminiSessionBody(nil, []any{geminiMsg("user", "Part 1", "CHANGED", "Part 3")}), ctx)
	h2 := svc.GenerateSessionHash(parsed2)
	require.NotEqual(t, h, h2, "changing a part should change the hash")
}

// TestGenerateSessionHash_GeminiNonTextPartsIgnored supplies one text part
// alongside inline-image data under a populated context. The hash is nonempty;
// this assertion does not isolate the image contribution or compare against a
// text-only request.
func TestGenerateSessionHash_GeminiNonTextPartsIgnored(t *testing.T) {
	svc := &hashTestAdapter{}
	ctx := &SessionContext{ClientIP: "1.2.3.4", UserAgent: "gemini-cli", APIKeyID: 1}
	content := []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "Describe this image"}, map[string]any{"inline_data": map[string]any{"mime_type": "image/png", "data": "base64..."}}}}}
	parsed := mustParseGeminiSessionHashRequest(t, geminiSessionBody(nil, content), ctx)

	h := svc.GenerateSessionHash(parsed)
	require.NotEmpty(t, h, "Gemini message with mixed parts should still produce a hash from text parts")
}

// TestGenerateSessionHash_GeminiMultiTurnHashNotSticky compares three growing
// Gemini transcripts under one source identity. The three hashes differ
// pairwise, and reparsing the second transcript returns its previous hash for
// unchanged content.
func TestGenerateSessionHash_GeminiMultiTurnHashNotSticky(t *testing.T) {
	svc := &hashTestAdapter{}
	ctx := &SessionContext{ClientIP: "10.0.0.1", UserAgent: "gemini-cli", APIKeyID: 42}
	rounds := []string{
		geminiSessionBody([]any{map[string]any{"text": "You are a coding assistant."}}, []any{geminiMsg("user", "Write a Go function")}),
		geminiSessionBody([]any{map[string]any{"text": "You are a coding assistant."}}, []any{geminiMsg("user", "Write a Go function"), geminiMsg("model", "func hello() {}"), geminiMsg("user", "Add error handling")}),
		geminiSessionBody([]any{map[string]any{"text": "You are a coding assistant."}}, []any{geminiMsg("user", "Write a Go function"), geminiMsg("model", "func hello() {}"), geminiMsg("user", "Add error handling"), geminiMsg("model", "func hello() error { return nil }"), geminiMsg("user", "Now add tests")}),
	}

	hashes := make([]string, len(rounds))
	for i, body := range rounds {
		parsed := mustParseGeminiSessionHashRequest(t, body, ctx)
		hashes[i] = svc.GenerateSessionHash(parsed)
		require.NotEmpty(t, hashes[i], "round %d hash should not be empty", i+1)
	}
	require.NotEqual(t, hashes[0], hashes[1], "round 1 vs 2 hash should differ (contents grow)")
	require.NotEqual(t, hashes[1], hashes[2], "round 2 vs 3 hash should differ (contents grow)")
	require.NotEqual(t, hashes[0], hashes[2], "round 1 vs 3 hash should differ")

	parsedAgain := mustParseGeminiSessionHashRequest(t, rounds[1], ctx)
	h2Again := svc.GenerateSessionHash(parsedAgain)
	require.Equal(t, hashes[1], h2Again, "retry of same round should produce same hash")
}

// TestGenerateSessionHash_GeminiEndToEnd parses Gemini system parts and a
// multi-turn conversation before hashing. Repeated content/context returns the
// same nonempty hash, while changing address and API Key produces a different
// value.
func TestGenerateSessionHash_GeminiEndToEnd(t *testing.T) {
	svc := &hashTestAdapter{}
	body := geminiSessionBody([]any{map[string]any{"text": "You are a coding assistant."}}, []any{geminiMsg("user", "Write a Go function"), geminiMsg("model", "Here is a function..."), geminiMsg("user", "Now add error handling")})
	parsed := mustParseGeminiSessionHashRequest(t, body, &SessionContext{ClientIP: "10.0.0.1", UserAgent: "gemini-cli/1.0", APIKeyID: 42})

	h := svc.GenerateSessionHash(parsed)
	require.NotEmpty(t, h, "end-to-end Gemini flow should produce a hash")

	parsed2 := mustParseGeminiSessionHashRequest(t, body, &SessionContext{ClientIP: "10.0.0.1", UserAgent: "gemini-cli/1.0", APIKeyID: 42})
	h2 := svc.GenerateSessionHash(parsed2)
	require.Equal(t, h, h2, "same request should produce same hash")

	parsed3 := mustParseGeminiSessionHashRequest(t, body, &SessionContext{ClientIP: "10.0.0.2", UserAgent: "gemini-cli/1.0", APIKeyID: 99})
	h3 := svc.GenerateSessionHash(parsed3)
	require.NotEqual(t, h, h3, "different user with same Gemini request should get different hash")
}

// mustParseSessionHashRequest parses an Anthropic fixture and binds the
// supplied source-context reference. A parse error fails the calling test
// through require; the returned parsed request retains its newly encoded body
// bytes.
func mustParseSessionHashRequest(t *testing.T, body string, ctx *SessionContext) *ParsedRequest {
	t.Helper()
	parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(body)), capability.PlatformAnthropic)
	require.NoError(t, err)
	parsed.SessionContext = ctx
	return parsed
}

// mustParseGeminiSessionHashRequest parses a Gemini fixture and attaches the
// supplied source-context reference. Parsing failure fails the calling test,
// while the returned body ranges expose Gemini instruction and content parts.
func mustParseGeminiSessionHashRequest(t *testing.T, body string, ctx *SessionContext) *ParsedRequest {
	t.Helper()
	parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(body)), capability.PlatformGemini)
	require.NoError(t, err)
	parsed.SessionContext = ctx
	return parsed
}

// mustParseResponsesSessionHashRequest parses a Responses fixture and attaches
// its supplied source-context reference. The test fails on a parse error;
// otherwise the returned ranges retain that fixture's encoded input bytes.
func mustParseResponsesSessionHashRequest(t *testing.T, body string, ctx *SessionContext) *ParsedRequest {
	t.Helper()
	parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(body)), "responses")
	require.NoError(t, err)
	parsed.SessionContext = ctx
	return parsed
}

// anthropicSessionBody encodes supplied system, messages, and nonempty
// metadata into fixture JSON. Nil system/messages and empty metadata are
// omitted; marshal errors are discarded, so callers supply encodable values to
// obtain usable bytes.
func anthropicSessionBody(system any, messages []any, metadataUserID string) string {
	body := map[string]any{}
	if system != nil {
		body["system"] = system
	}
	if messages != nil {
		body["messages"] = messages
	}
	if metadataUserID != "" {
		body["metadata"] = map[string]any{"user_id": metadataUserID}
	}
	// Fixture encoders expose no marshal error result; an unsupported supplied
	// value can yield empty bytes, so callers use the encodable maps shown by these tests.
	data, _ := json.Marshal(body)
	return string(data)
}

// geminiSessionBody encodes nonnil system parts and contents into Gemini
// fixture JSON. Nil fields are omitted; marshal failures produce the returned
// byte text, so these helpers expect JSON-encodable values in their calling
// tests.
func geminiSessionBody(systemParts []any, contents []any) string {
	body := map[string]any{}
	if systemParts != nil {
		body["systemInstruction"] = map[string]any{"parts": systemParts}
	}
	if contents != nil {
		body["contents"] = contents
	}
	// Fixture encoders expose no marshal error result; an unsupported supplied
	// value can yield empty bytes, so callers use the encodable maps shown by these tests.
	data, _ := json.Marshal(body)
	return string(data)
}

// msg builds a fresh fixture message map containing the supplied role and
// content. Mutable content values remain shared with their caller, so tests
// coordinate later changes before encoding this message.
func msg(role string, content any) map[string]any {
	return map[string]any{"role": role, "content": content}
}

// geminiMsg builds fresh part maps for the supplied role and text strings.
func geminiMsg(role string, texts ...string) map[string]any {
	parts := make([]any, 0, len(texts))
	for _, text := range texts {
		parts = append(parts, map[string]any{"text": text})
	}
	return map[string]any{"role": role, "parts": parts}
}

// GenerateSessionHash delegates to the shared generator with diagnostics
// disabled. Nil requests return empty through that generator; other inputs
// follow its metadata, marked-content, and readable-text priority rules for
// these fixtures.
func (*hashTestAdapter) GenerateSessionHash(p *ParsedRequest) string {
	return GenerateSessionHash(p, nil)
}

// ParseGatewayRequest delegates fixture parsing to requeststate using the
// supplied body reference and protocol. Parsing errors are returned to the
// caller; accepted requests retain the supplied body storage for range-based
// reads.
func ParseGatewayRequest(body *requeststate.RequestBodyRef, protocol string) (*ParsedRequest, error) {
	return requeststate.ParseGatewayRequest(body, protocol)
}

// NewRequestBodyRef wraps fixture bytes without copying their backing storage.
// The returned reference retains the slice, so callers keep bytes stable while
// parsed request ranges or JSON views read them.
func NewRequestBodyRef(body []byte) *requeststate.RequestBodyRef {
	return requeststate.NewRequestBodyRef(body)
}

// BenchmarkGenerateSessionHash_Metadata parses a request containing an
// unrecognized metadata user_id on each iteration. The measured path includes
// metadata rejection, readable-message hashing, and synchronous slog.Info
// observation, with allocations reported by the harness.
func BenchmarkGenerateSessionHash_Metadata(b *testing.B) {
	body := []byte(`{"metadata":{"user_id":"session_123e4567-e89b-12d3-a456-426614174000"},"messages":[{"content":"hello"}]}`)

	b.ReportAllocs()
	for range b.N {
		parsed, err := requeststate.ParseGatewayRequest(requeststate.NewRequestBodyRef(body), "")
		if err != nil {
			b.Fatalf("解析请求失败: %v", err)
		}
		benchmarkStringSink = GenerateSessionHash(parsed, slog.Info)
	}
}

// BenchmarkGenerateSessionHash_LargeAnthropicMessages hashes already parsed
// Messages bodies at four target sizes. Marked content selects the early
// message anchor; parsing and payload construction are outside ResetTimer,
// while generator diagnostics remain inside the measured loop.
func BenchmarkGenerateSessionHash_LargeAnthropicMessages(b *testing.B) {
	for _, size := range benchmarkBodySizes() {
		b.Run(size.name, func(b *testing.B) {
			body := buildLargeAnthropicMessagesBody(size.bytes, true)
			parsed, err := requeststate.ParseGatewayRequest(requeststate.NewRequestBodyRef(body), capability.PlatformAnthropic)
			if err != nil {
				b.Fatalf("解析请求失败: %v", err)
			}

			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				benchmarkStringSink = GenerateSessionHash(parsed, slog.Info)
			}
		})
	}
}

// BenchmarkExtractCacheableContent_System repeatedly extracts twelve ephemeral
// system-text blocks from parsed fixture state. The allocation-reporting loop
// retains the concatenated result, measuring extraction rather than request
// parsing or session hash generation.
func BenchmarkExtractCacheableContent_System(b *testing.B) {
	req := buildSystemCacheableRequest(12)

	b.ReportAllocs()
	for range b.N {
		benchmarkStringSink = ExtractCacheableContent(req)
	}
}

// benchmarkBodySizes returns the four target byte sizes used by hash
// benchmarks.
func benchmarkBodySizes() []struct {
	name  string // named benchmark payload size
	bytes int    // target encoded payload bytes
} {
	return []struct {
		name  string // named benchmark payload size
		bytes int    // target encoded payload bytes
	}{
		{name: "4MB", bytes: 4 << 20},
		{name: "8MB", bytes: 8 << 20},
		{name: "16MB", bytes: 16 << 20},
		{name: "32MB", bytes: 32 << 20},
	}
}

// buildLargeAnthropicMessagesBody appends complete repeated-text messages
// until the requested byte target is reached, then closes JSON framing. The
// result can exceed targetBytes; callers supply a nonnegative capacity target
// for builder.Grow. When enabled, every thirty-second message beginning with
// the first is marked.
func buildLargeAnthropicMessagesBody(targetBytes int, includeCacheControl bool) []byte {
	var builder strings.Builder
	// Reserve additional capacity for message framing beyond the target payload.
	builder.Grow(targetBytes + 1024)
	_, _ = builder.WriteString(`{"model":"claude-sonnet-4-5","stream":true,"system":[{"type":"text","text":"system seed"}],"messages":[`)
	for i := 0; builder.Len() < targetBytes; i++ {
		if i > 0 {
			_ = builder.WriteByte(',')
		}
		_, _ = builder.WriteString(`{"role":"user","content":[{"type":"text","text":"`)
		_, _ = builder.WriteString(strings.Repeat("anthropic payload ", 64))
		_, _ = builder.WriteString(strconv.Itoa(i))
		_ = builder.WriteByte('"')
		// Mark every thirty-second message, including the first, to model repeated
		// anchors while ExtractCacheableContent still stops at its earliest marked message.
		if includeCacheControl && i%32 == 0 {
			_, _ = builder.WriteString(`,"cache_control":{"type":"ephemeral"}`)
		}
		_, _ = builder.WriteString(`}]}`)
	}
	_, _ = builder.WriteString(`]}`)
	return []byte(builder.String())
}

// buildSystemCacheableRequest parses an array of numbered, ephemeral system
// blocks for benchmarking. Nonpositive parts produces an empty array;
// generated JSON parsing failure panics because the caller requires usable
// parsed fixture state.
func buildSystemCacheableRequest(parts int) *requeststate.ParsedRequest {
	var builder strings.Builder
	_, _ = builder.WriteString(`{"system":[`)
	for i := range parts {
		if i > 0 {
			_ = builder.WriteByte(',')
		}
		_, _ = builder.WriteString(`{"text":"system_part_`)
		_, _ = builder.WriteString(strconv.Itoa(i))
		_, _ = builder.WriteString(`","cache_control":{"type":"ephemeral"}}`)
	}
	_, _ = builder.WriteString(`]}`)
	parsed, err := requeststate.ParseGatewayRequest(requeststate.NewRequestBodyRef([]byte(builder.String())), "")
	if err != nil {
		panic(err)
	}
	return parsed
}
