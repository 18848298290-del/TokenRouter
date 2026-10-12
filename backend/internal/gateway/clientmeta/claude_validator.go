package clientmeta

import (
	"regexp"
	"strings"

	wire "github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
)

const (
	// Security-monitor detection uses these markers while allowing other prompt wording to vary.
	claudeCodeSecurityMonitorPromptPrefix = "You are a security monitor for autonomous AI coding agents."
	claudeCodeSecurityMonitorPromptMinLen = 10_000

	// claudeCodeBillingHeaderPrefix identifies the billing-attribution block in Claude Code system prompts.
	// This block identifies subrequests that lack identity text. Fixed auxiliary requests have separate detection rules.
	// See protocol/bridge/anthropic_to_responses.go for the corresponding protocol conversion.
	claudeCodeBillingHeaderPrefix = wire.ClaudeCodeBillingHeaderPrefix
	// claudeCodeEntrypointMarker detects the presence of an entrypoint field.
	// Entrypoint values such as cli, claude-vscode, jetbrains, and sdk can be supplied by the requester.
	claudeCodeEntrypointMarker = wire.ClaudeCodeEntrypointMarker

	// ClaudeCodeSystemPromptThreshold is the minimum Dice similarity accepted for identity templates.
	ClaudeCodeSystemPromptThreshold = 0.5

	// ClaudeCodeSecurityMonitorPrefix exposes the detection prefix for compatibility checks.
	ClaudeCodeSecurityMonitorPrefix = claudeCodeSecurityMonitorPromptPrefix
)

var (
	// Match the claude-cli/x.x.x prefix case-insensitively.
	claudeCodeUAPattern = regexp.MustCompile(`(?i)^claude-cli/\d+\.\d+\.\d+`)

	// Minimum system-prompt similarity.
	systemPromptThreshold = ClaudeCodeSystemPromptThreshold

	// Claude Code identity templates.
	// Template source: claude-relay-service/src/utils/contents.js.
	claudeCodeSystemPrompts = []string{
		// claudeOtherSystemPrompt1 - Primary
		"You are Claude Code, Anthropic's official CLI for Claude.",

		// claudeOtherSystemPrompt3 - Agent SDK
		"You are a Claude agent, built on Anthropic's Claude Agent SDK.",

		// claudeOtherSystemPrompt4 - Compact Agent SDK
		"You are Claude Code, Anthropic's official CLI for Claude, running within the Claude Agent SDK.",

		// exploreAgentSystemPrompt
		"You are a file search specialist for Claude Code, Anthropic's official CLI for Claude.",

		// claudeOtherSystemPromptCompact: conversation summarization.
		"You are a helpful AI assistant tasked with summarizing conversations.",

		// claudeOtherSystemPrompt2 is a recognition fragment within the longer system prompt.
		"You are an interactive CLI tool that helps users",
	}

	// claudeCodeSecurityMonitorMarkers are required in addition to the prefix and minimum length.
	claudeCodeSecurityMonitorMarkers = []string{
		"## Threat Model",
		"- `<transcript>`:",
		"## HARD BLOCK",
		"## SOFT BLOCK",
		"## Classification Process",
		"## Output Format",
		"<block>yes</block><reason>",
		"<block>no</block>",
	}
)

// ClaudeCodeValidator recognizes Claude Code request headers and payloads.
// These requester-supplied markers identify client format, not authenticated provenance.
type ClaudeCodeValidator struct{}

// ClaudeCodeValidationInput contains client metadata extracted synchronously by HTTP handlers.
type ClaudeCodeValidationInput struct {
	Path              string
	UserAgent         string
	XApp              string
	AnthropicBeta     string
	AnthropicVersion  string
	MaxTokensOneHaiku bool
}

// IsClaudeCodeClient checks the CLI User-Agent and metadata identity format.
// Both checks must pass before callers can skip request mimicry.
func IsClaudeCodeClient(userAgent, metadataUserID string) bool {
	return claudeCodeUAPattern.MatchString(userAgent) && wire.ParseMetadataUserID(metadataUserID) != nil
}

// NewClaudeCodeValidator creates a client-format validator.
func NewClaudeCodeValidator() *ClaudeCodeValidator {
	return &ClaudeCodeValidator{}
}

// Validate checks the CLI User-Agent before applying path-specific payload checks.
// Non-Messages paths, count_tokens, and marked Haiku probes require the User-Agent alone.
//
// Ordinary Messages requests also require a recognized system prompt, nonempty
// X-App, anthropic-beta, and anthropic-version headers, and valid metadata.user_id.
func (v *ClaudeCodeValidator) Validate(input ClaudeCodeValidationInput, body map[string]any) bool {
	ua := input.UserAgent
	if !claudeCodeUAPattern.MatchString(ua) {
		return false
	}

	// Non-Messages paths require the CLI User-Agent alone.
	path := input.Path
	if !strings.Contains(path, "messages") {
		return true
	}

	// count_tokens auxiliary requests may omit the full Messages system prompt.
	if isMessagesCountTokensPath(path) {
		return true
	}

	// Haiku connectivity probes may omit the system prompt.
	// The caller marks these probes after checking their model and token limit.
	if input.MaxTokensOneHaiku {
		return true // User-Agent validation has already passed.
	}

	if !v.hasClaudeCodeSystemPrompt(body) {
		return false
	}

	// Required headers need nonempty values.
	xApp := input.XApp
	if xApp == "" {
		return false
	}

	anthropicBeta := input.AnthropicBeta
	if anthropicBeta == "" {
		return false
	}

	anthropicVersion := input.AnthropicVersion
	if anthropicVersion == "" {
		return false
	}

	if body == nil {
		return false
	}

	metadata, ok := body["metadata"].(map[string]any)
	if !ok {
		return false
	}

	userID, ok := metadata["user_id"].(string)
	if !ok || userID == "" {
		return false
	}

	if wire.ParseMetadataUserID(userID) == nil {
		return false
	}

	return true
}

func isMessagesCountTokensPath(path string) bool {
	return strings.HasSuffix(path, "/messages/count_tokens")
}

// hasClaudeCodeSystemPrompt recognizes security-monitor markers, billing blocks,
// or identity templates whose Dice similarity meets the threshold.
func (v *ClaudeCodeValidator) hasClaudeCodeSystemPrompt(body map[string]any) bool {
	if body == nil {
		return false
	}

	if _, ok := body["model"].(string); !ok {
		return false
	}

	systemEntries, ok := body["system"].([]any)
	if !ok {
		return false
	}

	if isClaudeCodeSecurityMonitorPrompt(systemEntries) {
		return true
	}

	for _, entry := range systemEntries {
		entryMap, ok := entry.(map[string]any)
		if !ok {
			continue
		}

		text, ok := entryMap["text"].(string)
		if !ok || text == "" {
			continue
		}

		// Recognize the billing block before template similarity checks.
		// The protocol billing prefix and entrypoint marker are case-sensitive.
		if strings.HasPrefix(text, claudeCodeBillingHeaderPrefix) &&
			strings.Contains(text, claudeCodeEntrypointMarker) {
			return true
		}

		bestScore := v.BestSimilarityScore(text)
		if bestScore >= systemPromptThreshold {
			return true
		}
	}

	return false
}

// isClaudeCodeSecurityMonitorPrompt checks individual system blocks, allowing
// the CLI to add conversation context before or after the monitor prompt.
func isClaudeCodeSecurityMonitorPrompt(systemEntries []any) bool {
	for _, rawEntry := range systemEntries {
		entry, ok := rawEntry.(map[string]any)
		if !ok {
			continue
		}

		entryType, ok := entry["type"].(string)
		if !ok || entryType != "text" {
			continue
		}

		text, ok := entry["text"].(string)
		if !ok || len(text) < claudeCodeSecurityMonitorPromptMinLen ||
			!strings.HasPrefix(text, claudeCodeSecurityMonitorPromptPrefix) {
			continue
		}

		if hasAllClaudeCodeSecurityMonitorMarkers(text) {
			return true
		}
	}

	return false
}

func hasAllClaudeCodeSecurityMonitorMarkers(text string) bool {
	for _, marker := range claudeCodeSecurityMonitorMarkers {
		if !strings.Contains(text, marker) {
			return false
		}
	}

	return true
}

// BestSimilarityScore returns the highest Dice similarity against the Claude Code templates.
func (v *ClaudeCodeValidator) BestSimilarityScore(text string) float64 {
	normalizedText := normalizePrompt(text)
	bestScore := 0.0

	for _, template := range claudeCodeSystemPrompts {
		normalizedTemplate := normalizePrompt(template)
		score := DiceCoefficient(normalizedText, normalizedTemplate)
		if score > bestScore {
			bestScore = score
		}
	}

	return bestScore
}

// normalizePrompt collapses whitespace and trims the prompt.
func normalizePrompt(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// DiceCoefficient returns the Sorensen-Dice similarity of case-folded rune bigrams.
// Repeated bigrams contribute their minimum count to the intersection.
// The score is 2 * intersection / (bigrams(a) + bigrams(b)).
func DiceCoefficient(a, b string) float64 {
	if a == b {
		return 1.0
	}

	if len(a) < 2 || len(b) < 2 {
		return 0.0
	}

	bigramsA := getBigrams(a)
	bigramsB := getBigrams(b)

	if len(bigramsA) == 0 || len(bigramsB) == 0 {
		return 0.0
	}

	intersection := 0
	for bigram, countA := range bigramsA {
		if countB, exists := bigramsB[bigram]; exists {
			if countA < countB {
				intersection += countA
			} else {
				intersection += countB
			}
		}
	}

	totalA := 0
	for _, count := range bigramsA {
		totalA += count
	}
	totalB := 0
	for _, count := range bigramsB {
		totalB += count
	}

	return float64(2*intersection) / float64(totalA+totalB)
}

// getBigrams counts adjacent rune pairs after lowercasing the string.
func getBigrams(s string) map[string]int {
	bigrams := make(map[string]int)
	runes := []rune(strings.ToLower(s))

	for i := range len(runes) - 1 {
		bigram := string(runes[i : i+2])
		bigrams[bigram]++
	}

	return bigrams
}

// ValidateUserAgent checks the CLI prefix without reading a request body.
func (v *ClaudeCodeValidator) ValidateUserAgent(ua string) bool {
	return claudeCodeUAPattern.MatchString(ua)
}

// ExtractVersion returns the three-part Claude Code version from the User-Agent,
// or an empty string when the prefix does not match.
func (v *ClaudeCodeValidator) ExtractVersion(ua string) string {
	return ExtractClaudeCLIVersion(ua)
}

// IsHaikuProbe identifies single-token Haiku connectivity probes.
func IsHaikuProbe(model string, maxTokens int) bool {
	return maxTokens == 1 && strings.Contains(strings.ToLower(model), "haiku")
}
