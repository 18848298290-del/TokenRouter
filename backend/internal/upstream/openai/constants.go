package openai

import (
	_ "embed"
	"strings"
)

const (
	// DefaultTestModel is the default model for OpenAI provider probes.
	DefaultTestModel = "gpt-5.4"

	// CodexUsageProbeModel is the compatibility model for OAuth quota probes.
	CodexUsageProbeModel = "codex-auto-review"
)

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

// latestCodexInstructions returns the GPT-5.5 prompt, falling back to DefaultInstructions if blank.
func latestCodexInstructions() string {
	if v := strings.TrimSpace(instructionsGPT55); v != "" {
		return instructionsGPT55
	}
	return DefaultInstructions
}

// CodexBaseInstructionsForModel selects embedded Codex instructions by model name.
// Names containing codex use DefaultInstructions; gpt-5.1 and gpt-5.2 use dedicated
// prompts when nonblank. Other models and missing dedicated prompts use latestCodexInstructions.
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
	return latestCodexInstructions()
}
