package openai

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

// fingerprintOriginalSeed supplies a fixed provider seed so deterministic and
// cross-client derivation assertions compare identities under the same namespace.
const fingerprintOriginalSeed = "11111111-1111-4111-8111-111111111111"

// TestResolveConvergedThreadID_PerClientSession derives two threads with one
// provider seed and different client sessions. The identifiers must differ so
// session mode keeps those clients on separate upstream threads.
func TestResolveConvergedThreadID_PerClientSession(t *testing.T) {
	a := ResolveConvergedThreadID(fingerprintOriginalSeed, "session-aaa")
	b := ResolveConvergedThreadID(fingerprintOriginalSeed, "session-bbb")
	assert.NotEqual(t, a, b, "不同客户端 session 应得到不同 thread_id")
}

// TestResolveConvergedThreadID_Deterministic derives the same thread twice from
// matching seed and client session. The resulting identifiers must match so
// later turns retain the same upstream thread.
func TestResolveConvergedThreadID_Deterministic(t *testing.T) {
	a := ResolveConvergedThreadID(fingerprintOriginalSeed, "session-aaa")
	b := ResolveConvergedThreadID(fingerprintOriginalSeed, "session-aaa")
	assert.Equal(t, a, b, "同一客户端 session 应得到相同 thread_id")
}

// TestResolveConvergedThreadID_EmptySession supplies a provider seed with a
// missing client-session value. It expects empty text, preserving the special
// result used by the caller to select a session-based thread.
func TestResolveConvergedThreadID_EmptySession(t *testing.T) {
	assert.Equal(t, "", ResolveConvergedThreadID(fingerprintOriginalSeed, ""))
}

// TestExtractClientSessionID supplies both session-header spellings, the
// underscore spelling alone, and no session. It checks hyphen precedence,
// legacy alias reading, and empty results for absent values.
func TestExtractClientSessionID(t *testing.T) {
	tests := []struct {
		name     string      // session header case description
		headers  http.Header // supplied inbound session headers
		expected string      // expected extracted session identifier
	}{
		{"连字符形式优先", func() http.Header {
			h := http.Header{}
			h.Set("session-id", "hyphen-form")
			h.Set("session_id", "underscore-form")
			return h
		}(), "hyphen-form"},
		{"回退到下划线形式", func() http.Header {
			h := http.Header{}
			h.Set("session_id", "underscore-form")
			return h
		}(), "underscore-form"},
		{"都没有", http.Header{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, ExtractClientSessionID(tt.headers))
		})
	}
}
