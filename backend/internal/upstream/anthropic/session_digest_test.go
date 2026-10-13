package anthropic

import (
	"encoding/json"
	"strings"
	"testing"
)

// splitChain splits nonempty chain text at hyphens for fixture assertions.
// Empty input returns nil, and a trailing separator produces no final empty
// entry in the result.
func splitChain(chain string) []string {
	if chain == "" {
		return nil
	}
	var parts []string
	start := 0
	for i := range len(chain) {
		if chain[i] == '-' {
			parts = append(parts, chain[start:i])
			start = i + 1
		}
	}
	if start < len(chain) {
		parts = append(parts, chain[start:])
	}
	return parts
}

// mustParseAnthropicDigestRequest decodes raw system and message fields from a
// fixture. It marks the call as a helper and fails the test on malformed
// request JSON before returning those bytes.
func mustParseAnthropicDigestRequest(t *testing.T, body string) struct {
	System   json.RawMessage // raw system JSON
	Messages json.RawMessage // raw history JSON
} {
	t.Helper()
	var parsed struct {
		System   json.RawMessage // raw system JSON
		Messages json.RawMessage // raw history JSON
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatalf("parse digest fixture: %v", err)
	}
	return parsed
}

// TestBuildAnthropicDigestChain_NilRequest passes nil system and message input
// to digest construction. The result is empty because no conversation content
// contributes a role-prefixed hash entry.
func TestBuildAnthropicDigestChain_NilRequest(t *testing.T) {
	result := BuildAnthropicDigestChain(nil, nil)
	if result != "" {
		t.Errorf("expected empty string for nil request, got: %s", result)
	}
}

// TestBuildAnthropicDigestChain_EmptyMessages extracts a request with an empty
// message array and no system. Digest construction returns an empty chain,
// matching the absence of any content entries.
func TestBuildAnthropicDigestChain_EmptyMessages(t *testing.T) {
	parsed := mustParseAnthropicDigestRequest(t, `{"messages":[]}`)
	result := BuildAnthropicDigestChain(parsed.System, parsed.Messages)
	if result != "" {
		t.Errorf("expected empty string for empty messages, got: %s", result)
	}
}

// TestBuildAnthropicDigestChain_SingleUserMessage extracts one scalar user
// message before building its chain. The result has one entry beginning with u,
// identifying the supplied message's role category.
func TestBuildAnthropicDigestChain_SingleUserMessage(t *testing.T) {
	parsed := mustParseAnthropicDigestRequest(t, `{"messages":[{"role":"user","content":"hello"}]}`)
	result := BuildAnthropicDigestChain(parsed.System, parsed.Messages)
	parts := splitChain(result)
	if len(parts) != 1 {
		t.Fatalf("expected 1 part, got %d: %s", len(parts), result)
	}
	if !strings.HasPrefix(parts[0], "u:") {
		t.Errorf("expected prefix 'u:', got: %s", parts[0])
	}
}

// TestBuildAnthropicDigestChain_UserAndAssistant builds a chain from one user
// and one assistant message. The two entries retain their order and carry u
// then a prefixes for their roles.
func TestBuildAnthropicDigestChain_UserAndAssistant(t *testing.T) {
	parsed := mustParseAnthropicDigestRequest(t, `{"messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi there"}]}`)
	result := BuildAnthropicDigestChain(parsed.System, parsed.Messages)
	parts := splitChain(result)
	if len(parts) != 2 {
		t.Fatalf("expected 2 parts, got %d: %s", len(parts), result)
	}
	if !strings.HasPrefix(parts[0], "u:") {
		t.Errorf("part[0] expected prefix 'u:', got: %s", parts[0])
	}
	if !strings.HasPrefix(parts[1], "a:") {
		t.Errorf("part[1] expected prefix 'a:', got: %s", parts[1])
	}
}

// TestBuildAnthropicDigestChain_WithSystemString builds a chain containing
// scalar system text and one user message. Its two entries put the system hash
// first and the user hash second.
func TestBuildAnthropicDigestChain_WithSystemString(t *testing.T) {
	parsed := mustParseAnthropicDigestRequest(t, `{"system":"You are a helpful assistant","messages":[{"role":"user","content":"hello"}]}`)
	result := BuildAnthropicDigestChain(parsed.System, parsed.Messages)
	parts := splitChain(result)
	if len(parts) != 2 {
		t.Fatalf("expected 2 parts (s + u), got %d: %s", len(parts), result)
	}
	if !strings.HasPrefix(parts[0], "s:") {
		t.Errorf("part[0] expected prefix 's:', got: %s", parts[0])
	}
	if !strings.HasPrefix(parts[1], "u:") {
		t.Errorf("part[1] expected prefix 'u:', got: %s", parts[1])
	}
}

// TestBuildAnthropicDigestChain_WithSystemContentBlocks builds a chain with a
// system text-block array and one user message. The result includes two
// entries, with the system representation identified by its leading s prefix.
func TestBuildAnthropicDigestChain_WithSystemContentBlocks(t *testing.T) {
	parsed := mustParseAnthropicDigestRequest(t, `{"system":[{"type":"text","text":"You are a helpful assistant"}],"messages":[{"role":"user","content":"hello"}]}`)
	result := BuildAnthropicDigestChain(parsed.System, parsed.Messages)
	parts := splitChain(result)
	if len(parts) != 2 {
		t.Fatalf("expected 2 parts (s + u), got %d: %s", len(parts), result)
	}
	if !strings.HasPrefix(parts[0], "s:") {
		t.Errorf("part[0] expected prefix 's:', got: %s", parts[0])
	}
}

// TestBuildAnthropicDigestChain_ConversationPrefixRelationship builds
// successively longer histories that retain earlier system and message content.
// Each later textual chain starts with the earlier chain, verifying the
// append-only fixture's prefix relationship.
func TestBuildAnthropicDigestChain_ConversationPrefixRelationship(t *testing.T) {
	// Appending a conversation turn must retain the previous digest chain as a prefix.
	round1 := mustParseAnthropicDigestRequest(t, `{"system":"You are a helpful assistant","messages":[{"role":"user","content":"hello"}]}`)
	chain1 := BuildAnthropicDigestChain(round1.System, round1.Messages)

	round2 := mustParseAnthropicDigestRequest(t, `{"system":"You are a helpful assistant","messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi there"},{"role":"user","content":"how are you?"}]}`)
	chain2 := BuildAnthropicDigestChain(round2.System, round2.Messages)

	round3 := mustParseAnthropicDigestRequest(t, `{"system":"You are a helpful assistant","messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi there"},{"role":"user","content":"how are you?"},{"role":"assistant","content":"I'm doing well"},{"role":"user","content":"great"}]}`)
	chain3 := BuildAnthropicDigestChain(round3.System, round3.Messages)

	t.Logf("Chain1: %s", chain1)
	t.Logf("Chain2: %s", chain2)
	t.Logf("Chain3: %s", chain3)

	if !strings.HasPrefix(chain2, chain1) {
		t.Errorf("chain1 should be prefix of chain2:\n  chain1: %s\n  chain2: %s", chain1, chain2)
	}
	if !strings.HasPrefix(chain3, chain2) {
		t.Errorf("chain2 should be prefix of chain3:\n  chain2: %s\n  chain3: %s", chain2, chain3)
	}
	if !strings.HasPrefix(chain3, chain1) {
		t.Errorf("chain1 should be prefix of chain3:\n  chain1: %s\n  chain3: %s", chain1, chain3)
	}
}

// TestBuildAnthropicDigestChain_DifferentSystemProducesDifferentChain changes
// system text while retaining the same user content. The complete chains
// differ, while their corresponding user entries remain equal across the two
// requests.
func TestBuildAnthropicDigestChain_DifferentSystemProducesDifferentChain(t *testing.T) {
	parsed1 := mustParseAnthropicDigestRequest(t, `{"system":"System A","messages":[{"role":"user","content":"hello"}]}`)
	parsed2 := mustParseAnthropicDigestRequest(t, `{"system":"System B","messages":[{"role":"user","content":"hello"}]}`)

	chain1 := BuildAnthropicDigestChain(parsed1.System, parsed1.Messages)
	chain2 := BuildAnthropicDigestChain(parsed2.System, parsed2.Messages)

	if chain1 == chain2 {
		t.Error("Different system prompts should produce different chains")
	}

	parts1 := splitChain(chain1)
	parts2 := splitChain(chain2)
	if parts1[1] != parts2[1] {
		t.Error("Same user message should produce same hash regardless of system")
	}
}

// TestBuildAnthropicDigestChain_DifferentContentProducesDifferentChain changes
// an assistant reply while retaining the earlier user text. The complete chains
// and assistant entries differ, while the first user entry remains equal.
func TestBuildAnthropicDigestChain_DifferentContentProducesDifferentChain(t *testing.T) {
	parsed1 := mustParseAnthropicDigestRequest(t, `{"messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"ORIGINAL reply"},{"role":"user","content":"next"}]}`)
	parsed2 := mustParseAnthropicDigestRequest(t, `{"messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"TAMPERED reply"},{"role":"user","content":"next"}]}`)

	chain1 := BuildAnthropicDigestChain(parsed1.System, parsed1.Messages)
	chain2 := BuildAnthropicDigestChain(parsed2.System, parsed2.Messages)

	if chain1 == chain2 {
		t.Error("Different content should produce different chains")
	}

	parts1 := splitChain(chain1)
	parts2 := splitChain(chain2)
	if parts1[0] != parts2[0] {
		t.Error("First user message hash should be the same")
	}
	if parts1[1] == parts2[1] {
		t.Error("Assistant reply hash should differ")
	}
}

// TestBuildAnthropicDigestChain_Deterministic builds twice from the same
// extracted system and message bytes. Both returned chain strings are equal,
// checking repeatability for identical conversation input.
func TestBuildAnthropicDigestChain_Deterministic(t *testing.T) {
	parsed := mustParseAnthropicDigestRequest(t, `{"system":"test system","messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"}]}`)

	chain1 := BuildAnthropicDigestChain(parsed.System, parsed.Messages)
	chain2 := BuildAnthropicDigestChain(parsed.System, parsed.Messages)

	if chain1 != chain2 {
		t.Errorf("BuildAnthropicDigestChain not deterministic: %s vs %s", chain1, chain2)
	}
}

// TestBuildAnthropicDigestChain_CanonicalJSON supplies equivalent object
// content with different key ordering in system and message values. Both chains
// are equal after canonical encoding has normalized those ordering differences.
func TestBuildAnthropicDigestChain_CanonicalJSON(t *testing.T) {
	parsed1 := mustParseAnthropicDigestRequest(t, `{"system":[{"type":"text","text":"system"}],"messages":[{"role":"user","content":{"type":"text","text":"hello"}}]}`)
	parsed2 := mustParseAnthropicDigestRequest(t, `{"system":[{"text":"system","type":"text"}],"messages":[{"role":"user","content":{"text":"hello","type":"text"}}]}`)

	chain1 := BuildAnthropicDigestChain(parsed1.System, parsed1.Messages)
	chain2 := BuildAnthropicDigestChain(parsed2.System, parsed2.Messages)

	if chain1 != chain2 {
		t.Errorf("semantically equivalent JSON should produce same chain: %s vs %s", chain1, chain2)
	}
}

// TestGenerateAnthropicDigestSessionKey covers long, exact-length, short, and
// empty key components. It checks retained eight-byte prefixes and
// distinguishes two UUIDs whose retained prefixes already differ.
func TestGenerateAnthropicDigestSessionKey(t *testing.T) {
	tests := []struct {
		name       string // case display name
		prefixHash string // supplied digest prefix
		uuid       string // supplied session identifier
		want       string // expected comparison value
	}{
		{
			name:       "normal 16 char hash with uuid",
			prefixHash: "abcdefgh12345678",
			uuid:       "550e8400-e29b-41d4-a716-446655440000",
			want:       "anthropic:digest:abcdefgh:550e8400",
		},
		{
			name:       "exactly 8 chars",
			prefixHash: "12345678",
			uuid:       "abcdefgh",
			want:       "anthropic:digest:12345678:abcdefgh",
		},
		{
			name:       "short values",
			prefixHash: "abc",
			uuid:       "xyz",
			want:       "anthropic:digest:abc:xyz",
		},
		{
			name:       "empty values",
			prefixHash: "",
			uuid:       "",
			want:       "anthropic:digest::",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GenerateAnthropicDigestSessionKey(tt.prefixHash, tt.uuid)
			if got != tt.want {
				t.Errorf("GenerateAnthropicDigestSessionKey(%q, %q) = %q, want %q", tt.prefixHash, tt.uuid, got, tt.want)
			}
		})
	}

	t.Run("different uuid different key", func(t *testing.T) {
		hash := "sameprefix123456"
		result1 := GenerateAnthropicDigestSessionKey(hash, "uuid0001-session-a")
		result2 := GenerateAnthropicDigestSessionKey(hash, "uuid0002-session-b")
		if result1 == result2 {
			t.Errorf("Different UUIDs should produce different session keys: %s vs %s", result1, result2)
		}
	})
}

// TestAnthropicSessionTTL reads the exported duration helper for digest session
// caching. Its seconds equal three hundred, pinning the configured five-minute
// lifetime used by cache callers.
func TestAnthropicSessionTTL(t *testing.T) {
	ttl := AnthropicSessionTTL()
	if ttl.Seconds() != 300 {
		t.Errorf("expected 300 seconds, got: %v", ttl.Seconds())
	}
}

// TestBuildAnthropicDigestChain_ContentBlocks builds from a user message
// containing text and image blocks. The returned chain has one user-prefixed
// entry, treating the content array as one message contribution.
func TestBuildAnthropicDigestChain_ContentBlocks(t *testing.T) {
	parsed := mustParseAnthropicDigestRequest(t, `{"messages":[{"role":"user","content":[{"type":"text","text":"describe this image"},{"type":"image","source":{"type":"base64"}}]}]}`)
	result := BuildAnthropicDigestChain(parsed.System, parsed.Messages)
	parts := splitChain(result)
	if len(parts) != 1 {
		t.Fatalf("expected 1 part, got %d: %s", len(parts), result)
	}
	if !strings.HasPrefix(parts[0], "u:") {
		t.Errorf("expected prefix 'u:', got: %s", parts[0])
	}
}
