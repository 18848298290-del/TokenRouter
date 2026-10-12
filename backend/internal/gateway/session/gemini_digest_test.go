package session

import (
	"testing"

	protocolgemini "github.com/TokenFlux/TokenRouter/internal/protocol/gemini"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// TestShortHash hashes empty, plain-text, and JSON byte fixtures.
// Each result repeats deterministically and fits the thirteen-character bound of an encoded uint64.
func TestShortHash(t *testing.T) {
	tests := []struct {
		name  string // named digest or encoding fixture
		input []byte // bytes supplied to short hashing
	}{
		{"empty", []byte{}},
		{"simple", []byte("hello world")},
		{"json", []byte(`{"role":"user","parts":[{"text":"hello"}]}`)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := upstream.ShortHash(tt.input)
			// Thirteen characters bound the Base36 uint64 representation returned here.
			if len(result) > 13 {
				t.Errorf("shortHash result too long: %d characters", len(result))
			}
			// Identical inputs produce identical hashes.
			result2 := upstream.ShortHash(tt.input)
			if result != result2 {
				t.Errorf("shortHash not deterministic: %s vs %s", result, result2)
			}
		})
	}
}

// TestBuildGeminiDigestChain supplies nil, empty, and system-bearing Gemini conversations.
// The resulting chain has the expected component count and each component uses
// a system, user, or model prefix.
func TestBuildGeminiDigestChain(t *testing.T) {
	tests := []struct {
		name     string                        // named digest or encoding fixture
		req      *protocolgemini.GeminiRequest // supplied Gemini request fixture
		wantLen  int                           // expected digest component count
		hasEmpty bool                          // expected empty chain result
	}{
		{
			name:     "nil request",
			req:      nil,
			hasEmpty: true,
		},
		{
			name: "empty contents",
			req: &protocolgemini.GeminiRequest{
				Contents: []protocolgemini.GeminiContent{},
			},
			hasEmpty: true,
		},
		{
			name: "single user message",
			req: &protocolgemini.GeminiRequest{
				Contents: []protocolgemini.GeminiContent{
					{Role: "user", Parts: []protocolgemini.GeminiPart{{Text: "hello"}}},
				},
			},
			wantLen: 1, // u:<hash>
		},
		{
			name: "user and model messages",
			req: &protocolgemini.GeminiRequest{
				Contents: []protocolgemini.GeminiContent{
					{Role: "user", Parts: []protocolgemini.GeminiPart{{Text: "hello"}}},
					{Role: "model", Parts: []protocolgemini.GeminiPart{{Text: "hi there"}}},
				},
			},
			wantLen: 2, // u:<hash>-m:<hash>
		},
		{
			name: "with system instruction",
			req: &protocolgemini.GeminiRequest{
				SystemInstruction: &protocolgemini.GeminiContent{
					Role:  "user",
					Parts: []protocolgemini.GeminiPart{{Text: "You are a helpful assistant"}},
				},
				Contents: []protocolgemini.GeminiContent{
					{Role: "user", Parts: []protocolgemini.GeminiPart{{Text: "hello"}}},
				},
			},
			wantLen: 2, // s:<hash>-u:<hash>
		},
		{
			name: "conversation with system",
			req: &protocolgemini.GeminiRequest{
				SystemInstruction: &protocolgemini.GeminiContent{
					Role:  "user",
					Parts: []protocolgemini.GeminiPart{{Text: "System prompt"}},
				},
				Contents: []protocolgemini.GeminiContent{
					{Role: "user", Parts: []protocolgemini.GeminiPart{{Text: "hello"}}},
					{Role: "model", Parts: []protocolgemini.GeminiPart{{Text: "hi"}}},
					{Role: "user", Parts: []protocolgemini.GeminiPart{{Text: "how are you?"}}},
				},
			},
			wantLen: 4, // s:<hash>-u:<hash>-m:<hash>-u:<hash>
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := BuildGeminiDigestChain(tt.req)

			if tt.hasEmpty {
				if result != "" {
					t.Errorf("expected empty string, got: %s", result)
				}
				return
			}

			// Each included system or message entry contributes one segment.
			parts := splitChain(result)
			if len(parts) != tt.wantLen {
				t.Errorf("expected %d parts, got %d: %s", tt.wantLen, len(parts), result)
			}

			// Each segment has a role prefix and a hash.
			for _, part := range parts {
				if len(part) < 3 || part[1] != ':' {
					t.Errorf("invalid part format: %s", part)
				}
				prefix := part[0]
				if prefix != 's' && prefix != 'u' && prefix != 'm' {
					t.Errorf("invalid prefix: %c", prefix)
				}
			}
		})
	}
}

// TestGenerateGeminiPrefixHash compares repeated source identity and a different user ID.
// The repeated hash matches, the changed-user fixture differs, and the compact
// encoding contains sixteen characters.
func TestGenerateGeminiPrefixHash(t *testing.T) {
	hash1 := GenerateGeminiPrefixHash(1, 100, "192.168.1.1", "Mozilla/5.0", "antigravity", "gemini-2.5-pro")
	hash2 := GenerateGeminiPrefixHash(1, 100, "192.168.1.1", "Mozilla/5.0", "antigravity", "gemini-2.5-pro")
	hash3 := GenerateGeminiPrefixHash(2, 100, "192.168.1.1", "Mozilla/5.0", "antigravity", "gemini-2.5-pro")

	// Identical inputs produce identical hashes.
	if hash1 != hash2 {
		t.Errorf("GenerateGeminiPrefixHash not deterministic: %s vs %s", hash1, hash2)
	}

	// These different user IDs must produce different hashes.
	if hash1 == hash3 {
		t.Errorf("GenerateGeminiPrefixHash collision for different inputs")
	}

	// Twelve retained hash bytes occupy sixteen unpadded URL-safe Base64 characters.
	if len(hash1) != 16 {
		t.Errorf("expected 16 characters, got %d: %s", len(hash1), hash1)
	}
}

// TestGenerateGeminiPrefixHash_IgnoresUserAgentVersionNoise changes a
// product-style client version in an otherwise equal source.
// The two Gemini prefix hashes remain equal after User-Agent normalization
// removes that supplied version difference.
func TestGenerateGeminiPrefixHash_IgnoresUserAgentVersionNoise(t *testing.T) {
	hash1 := GenerateGeminiPrefixHash(1, 100, "192.168.1.1", "Mozilla/5.0 codex_cli_rs/0.1.0", "antigravity", "gemini-2.5-pro")
	hash2 := GenerateGeminiPrefixHash(1, 100, "192.168.1.1", "Mozilla/5.0 codex_cli_rs/0.1.1", "antigravity", "gemini-2.5-pro")

	if hash1 != hash2 {
		t.Fatalf("version-only User-Agent changes should not perturb Gemini prefix hash: %s vs %s", hash1, hash2)
	}
}

// TestGenerateGeminiPrefixHash_IgnoresFreeformUserAgentVersionNoise changes the
// version in a freeform Codex CLI User-Agent.
// The two otherwise equal sources produce the same prefix hash for these supplied version strings.
func TestGenerateGeminiPrefixHash_IgnoresFreeformUserAgentVersionNoise(t *testing.T) {
	hash1 := GenerateGeminiPrefixHash(1, 100, "192.168.1.1", "Codex CLI 0.1.0", "antigravity", "gemini-2.5-pro")
	hash2 := GenerateGeminiPrefixHash(1, 100, "192.168.1.1", "Codex CLI 0.1.1", "antigravity", "gemini-2.5-pro")

	if hash1 != hash2 {
		t.Fatalf("free-form version-only User-Agent changes should not perturb Gemini prefix hash: %s vs %s", hash1, hash2)
	}
}

// TestParseGeminiSessionValue parses empty, malformed, ordinary, and colon-containing UUID values.
// Valid fixtures return the complete UUID and provider; malformed separators
// or numeric suffixes return ok=false.
func TestParseGeminiSessionValue(t *testing.T) {
	tests := []struct {
		name      string // named digest or encoding fixture
		value     string // stored UUID/provider text fixture
		wantUUID  string // expected recovered UUID text
		wantAccID int64  // expected parsed provider identifier
		wantOK    bool   // expected value parsing success
	}{
		{
			name:   "empty",
			value:  "",
			wantOK: false,
		},
		{
			name:   "no colon",
			value:  "abc123",
			wantOK: false,
		},
		{
			name:      "valid",
			value:     "uuid-1234:100",
			wantUUID:  "uuid-1234",
			wantAccID: 100,
			wantOK:    true,
		},
		{
			name:      "uuid with colon",
			value:     "a:b:c:123",
			wantUUID:  "a:b:c",
			wantAccID: 123,
			wantOK:    true,
		},
		{
			name:   "invalid provider id",
			value:  "uuid:abc",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			uuid, accID, ok := ParseGeminiSessionValue(tt.value)

			if ok != tt.wantOK {
				t.Errorf("ok: expected %v, got %v", tt.wantOK, ok)
			}

			if tt.wantOK {
				if uuid != tt.wantUUID {
					t.Errorf("uuid: expected %s, got %s", tt.wantUUID, uuid)
				}
				if accID != tt.wantAccID {
					t.Errorf("providerID: expected %d, got %d", tt.wantAccID, accID)
				}
			}
		})
	}
}

// TestFormatGeminiSessionValue formats one UUID/provider pair and parses it back.
// The encoded value matches its expected text, then decoding recovers the same UUID and provider ID.
func TestFormatGeminiSessionValue(t *testing.T) {
	result := FormatGeminiSessionValue("test-uuid", 123)
	expected := "test-uuid:123"
	if result != expected {
		t.Errorf("expected %s, got %s", expected, result)
	}

	// Parsing must recover the formatted binding.
	uuid, accID, ok := ParseGeminiSessionValue(result)
	if !ok {
		t.Error("ParseGeminiSessionValue failed on formatted value")
	}
	if uuid != "test-uuid" || accID != 123 {
		t.Errorf("round-trip failed: uuid=%s, accID=%d", uuid, accID)
	}
}

// splitChain splits test digest components at hyphens without validating their
// format. Empty input returns nil; a trailing hyphen contributes no final empty
// component, while an internal repeated hyphen retains an empty component.
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

// TestDigestChainDifferentSysInstruction changes only a Gemini system-instruction text fixture.
// The otherwise identical user content produces different full digest chains
// when that system instruction changes.
func TestDigestChainDifferentSysInstruction(t *testing.T) {
	req1 := &protocolgemini.GeminiRequest{
		SystemInstruction: &protocolgemini.GeminiContent{
			Parts: []protocolgemini.GeminiPart{{Text: "SYS_ORIGINAL"}},
		},
		Contents: []protocolgemini.GeminiContent{
			{Role: "user", Parts: []protocolgemini.GeminiPart{{Text: "hello"}}},
		},
	}

	req2 := &protocolgemini.GeminiRequest{
		SystemInstruction: &protocolgemini.GeminiContent{
			Parts: []protocolgemini.GeminiPart{{Text: "SYS_MODIFIED"}},
		},
		Contents: []protocolgemini.GeminiContent{
			{Role: "user", Parts: []protocolgemini.GeminiPart{{Text: "hello"}}},
		},
	}

	chain1 := BuildGeminiDigestChain(req1)
	chain2 := BuildGeminiDigestChain(req2)

	t.Logf("Chain1: %s", chain1)
	t.Logf("Chain2: %s", chain2)

	if chain1 == chain2 {
		t.Error("Different systemInstruction should produce different chains")
	}
}

// TestDigestChainTamperedMiddleContent edits a model reply between unchanged user turns.
// The full chain and model component differ while the first-user digest component stays equal.
func TestDigestChainTamperedMiddleContent(t *testing.T) {
	req1 := &protocolgemini.GeminiRequest{
		Contents: []protocolgemini.GeminiContent{
			{Role: "user", Parts: []protocolgemini.GeminiPart{{Text: "hello"}}},
			{Role: "model", Parts: []protocolgemini.GeminiPart{{Text: "ORIGINAL_REPLY"}}},
			{Role: "user", Parts: []protocolgemini.GeminiPart{{Text: "next"}}},
		},
	}

	req2 := &protocolgemini.GeminiRequest{
		Contents: []protocolgemini.GeminiContent{
			{Role: "user", Parts: []protocolgemini.GeminiPart{{Text: "hello"}}},
			{Role: "model", Parts: []protocolgemini.GeminiPart{{Text: "TAMPERED_REPLY"}}},
			{Role: "user", Parts: []protocolgemini.GeminiPart{{Text: "next"}}},
		},
	}

	chain1 := BuildGeminiDigestChain(req1)
	chain2 := BuildGeminiDigestChain(req2)

	t.Logf("Chain1: %s", chain1)
	t.Logf("Chain2: %s", chain2)

	if chain1 == chain2 {
		t.Error("Tampered middle content should produce different chains")
	}

	// Editing the model reply must leave the first user segment unchanged.
	parts1 := splitChain(chain1)
	parts2 := splitChain(chain2)

	if parts1[0] != parts2[0] {
		t.Error("First user message hash should be the same")
	}
	if parts1[1] == parts2[1] {
		t.Error("Model reply hash should be different")
	}
}

// TestGenerateGeminiDigestSessionKey supplies long, exact-width, short, and
// empty prefix/UUID components.
// Keys match the asserted byte prefixes, repeat deterministically, and differ
// for the tested distinct first-eight-byte UUID values.
func TestGenerateGeminiDigestSessionKey(t *testing.T) {
	tests := []struct {
		name       string // named digest or encoding fixture
		prefixHash string // supplied source prefix hash
		uuid       string // supplied conversation UUID text
		want       string // expected compact sticky key
	}{
		{
			name:       "normal 16 char hash with uuid",
			prefixHash: "abcdefgh12345678",
			uuid:       "550e8400-e29b-41d4-a716-446655440000",
			want:       "gemini:digest:abcdefgh:550e8400",
		},
		{
			name:       "exactly 8 chars prefix and uuid",
			prefixHash: "12345678",
			uuid:       "abcdefgh",
			want:       "gemini:digest:12345678:abcdefgh",
		},
		{
			name:       "short hash and short uuid (less than 8)",
			prefixHash: "abc",
			uuid:       "xyz",
			want:       "gemini:digest:abc:xyz",
		},
		{
			name:       "empty hash and uuid",
			prefixHash: "",
			uuid:       "",
			want:       "gemini:digest::",
		},
		{
			name:       "normal prefix with short uuid",
			prefixHash: "abcdefgh12345678",
			uuid:       "short",
			want:       "gemini:digest:abcdefgh:short",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GenerateGeminiDigestSessionKey(tt.prefixHash, tt.uuid)
			if got != tt.want {
				t.Errorf("GenerateGeminiDigestSessionKey(%q, %q) = %q, want %q", tt.prefixHash, tt.uuid, got, tt.want)
			}
		})
	}

	// Identical inputs produce identical session keys.
	t.Run("deterministic", func(t *testing.T) {
		hash := "testprefix123456"
		uuid := "test-uuid-12345" // supplied conversation UUID text
		result1 := GenerateGeminiDigestSessionKey(hash, uuid)
		result2 := GenerateGeminiDigestSessionKey(hash, uuid)
		if result1 != result2 {
			t.Errorf("GenerateGeminiDigestSessionKey not deterministic: %s vs %s", result1, result2)
		}
	})

	// UUIDs with different first eight bytes produce different session keys.
	t.Run("different uuid different key", func(t *testing.T) {
		hash := "sameprefix123456"
		uuid1 := "uuid0001-session-a"
		uuid2 := "uuid0002-session-b"
		result1 := GenerateGeminiDigestSessionKey(hash, uuid1)
		result2 := GenerateGeminiDigestSessionKey(hash, uuid2)
		if result1 == result2 {
			t.Errorf("Different UUIDs should produce different session keys: %s vs %s", result1, result2)
		}
	})
}
