package openai

import (
	"strings"
	"testing"
)

// firstLine returns the first line of trimmed embedded prompt text. Missing
// newlines return the complete trimmed value, keeping failure output short for
// both one-line and multiline fixtures.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// TestCodexBaseInstructionsForModel checks Codex-containing names, dedicated
// version prefixes, casing, whitespace, and unknown models. Every case must
// return nonblank instructions with the expected embedded prompt heading.
func TestCodexBaseInstructionsForModel(t *testing.T) {
	cases := []struct {
		model    string // requested model name fixture
		wantHead string // expected embedded prompt prefix
	}{
		{"gpt-5-codex", "You are Codex, based on GPT-5"},
		{"gpt-5.3-codex", "You are Codex, based on GPT-5"},
		{"gpt-5.3-codex-spark", "You are Codex, based on GPT-5"},
		{"gpt-5.1-codex-max", "You are Codex, based on GPT-5"},
		{"gpt-5.2-codex", "You are Codex, based on GPT-5"},
		{"gpt-5.5", "You are Codex, a coding agent based on GPT-5"},
		{" GPT-5.5 ", "You are Codex, a coding agent based on GPT-5"},
		{"gpt-5.2", "You are GPT-5.2 running in the Codex CLI"},
		{"gpt-5.1", "You are GPT-5.1 running in the Codex CLI"},
		{"gpt-5", "You are Codex, a coding agent based on GPT-5"},   // Use the latest embedded prompt (GPT-5.5).
		{"gpt-5.4", "You are Codex, a coding agent based on GPT-5"}, // No dedicated prompt.
		{"gpt-5.3", "You are Codex, a coding agent based on GPT-5"}, // No dedicated prompt.
		{"some-unknown-model", "You are Codex, a coding agent based on GPT-5"},
		{"", "You are Codex, a coding agent based on GPT-5"}, // Unknown models use the latest embedded prompt.
	}
	for _, c := range cases {
		got := strings.TrimSpace(CodexBaseInstructionsForModel(c.model))
		if got == "" {
			t.Errorf("model %q: got empty instructions", c.model)
			continue
		}
		if !strings.HasPrefix(got, c.wantHead) {
			t.Errorf("model %q: got prefix %q, want %q", c.model, firstLine(got), c.wantHead)
		}
	}
}
