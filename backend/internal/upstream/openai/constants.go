package openai

import (
	_ "embed"
	"strings"
)

// Codex base instructions are embedded with dedicated prompts for GPT-5.1 and
// GPT-5.2 and a latest prompt for other models. A model name containing codex
// selects the Codex prompt before version-prefix matching, so names such as
// gpt-5.2-codex retain that model family's instructions.

// Probe models select ordinary inference and quota-query compatibility targets;
// callers may override the inference model when testing a configured provider.
const (
	// DefaultTestModel is the default model for OpenAI provider probes.
	DefaultTestModel = "gpt-5.4"

	// CodexUsageProbeModel is the compatibility model for OAuth quota probes.
	CodexUsageProbeModel = "codex-auto-review"
)

// Embedded instruction strings retain their authored contents; model selection
// checks availability and returns the prompt appropriate to the requested family.
var (
	// DefaultInstructions contains the embedded GPT-5-Codex base prompt.
	//
	//go:embed instructions.txt
	DefaultInstructions string

	// instructionsGPT51 is the embedded GPT-5.1 coding prompt. Model selection also
	// uses the GPT-5.2 and GPT-5.5 prompts below; GPT-5.5 covers models without a dedicated prompt.
	//
	//go:embed instructions_gpt5_1.txt
	instructionsGPT51 string

	//go:embed instructions_gpt5_2.txt
	instructionsGPT52 string

	//go:embed instructions_gpt5_5.txt
	instructionsGPT55 string
)

// latestCodexInstructions returns the latest embedded prompt, selecting
// DefaultInstructions when it is blank. The selected string is returned as
// embedded, so whitespace checking does not rewrite prompt content.
func latestCodexInstructions() string {
	if v := strings.TrimSpace(instructionsGPT55); v != "" {
		return instructionsGPT55
	}
	// A blank latest prompt still needs usable synthesized instructions; the
	// Codex base prompt supplies that recovery value without altering embedded text.
	return DefaultInstructions
}

// CodexBaseInstructionsForModel selects an embedded prompt from the trimmed,
// lowercased model name. Codex-containing names take precedence over version
// prefixes; missing dedicated prompts and unknown names use
// latestCodexInstructions.
func CodexBaseInstructionsForModel(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.Contains(m, "codex"):
		return DefaultInstructions
	case strings.HasPrefix(m, "gpt-5.5"):
		return latestCodexInstructions()
	case strings.HasPrefix(m, "gpt-5.2"):
		if v := strings.TrimSpace(instructionsGPT52); v != "" {
			return instructionsGPT52
		}
	case strings.HasPrefix(m, "gpt-5.1"):
		if v := strings.TrimSpace(instructionsGPT51); v != "" {
			return instructionsGPT51
		}
	}
	// Unknown families and blank dedicated prompts use the latest available
	// instruction body so synthesized OAuth requests receive a non-family-specific default.
	return latestCodexInstructions()
}
