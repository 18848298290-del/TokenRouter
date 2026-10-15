package clientmeta

import (
	"regexp"
	"strings"

	wire "github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
)

// claude_validator.go recognizes Claude Code request formats from User-Agent,
// headers, and payload features. Claude Code identification affects request
// mimicry and client restrictions, so ordinary Messages requests need matching
// payload and header features as well as the CLI User-Agent. Auxiliary
// count-token and marked Haiku probes use a smaller set of checks because they
// can omit the ordinary system prompt. These features are supplied by the
// requester and classify its format.
//
// System-prompt recognition accepts identity-template similarity, billing
// attribution with an entrypoint, or a structured security-monitor prompt.
// Monitor recognition examines one complete text block at a time, allowing
// separate context blocks before or after it. Template scoring counts repeated
// rune bigrams as a multiset so repeated pairs contribute only their shared
// multiplicity when calculating similarity.

// Client recognition constants control the accepted prompt forms and similarity
// threshold used after the CLI User-Agent check passes.
const (
	// Security-monitor detection uses these markers while allowing other prompt
	// wording to vary.
	claudeCodeSecurityMonitorPromptPrefix = "You are a security monitor for autonomous AI coding agents."
	// claudeCodeSecurityMonitorPromptMinLen measures bytes, so short classifier
	// prefixes need the complete prompt body before monitor recognition can pass.
	claudeCodeSecurityMonitorPromptMinLen = 10_000

	// claudeCodeBillingHeaderPrefix identifies the billing-attribution block in
	// Claude Code system prompts. This block identifies subrequests that lack
	// identity text. Fixed auxiliary requests have separate detection rules.
	// See protocol/bridge/anthropic_to_responses.go for the corresponding protocol conversion.
	claudeCodeBillingHeaderPrefix = wire.ClaudeCodeBillingHeaderPrefix
	// claudeCodeEntrypointMarker detects the presence of an entrypoint field.
	// Entrypoint values such as cli, claude-vscode, jetbrains, and sdk can be
	// supplied by the requester.
	claudeCodeEntrypointMarker = wire.ClaudeCodeEntrypointMarker

	// ClaudeCodeSystemPromptThreshold is the minimum Dice similarity accepted
	// for identity templates.
	ClaudeCodeSystemPromptThreshold = 0.5

	// ClaudeCodeSecurityMonitorPrefix exposes the detection prefix for
	// compatibility checks.
	ClaudeCodeSecurityMonitorPrefix = claudeCodeSecurityMonitorPromptPrefix
)

// Client recognition patterns and prompt templates supply the fixed format
// evidence checked by the validator and its similarity calculation.
var (
	// claudeCodeUAPattern accepts a case-insensitive CLI prefix and three numeric
	// version components, allowing trailing client details after the matched prefix.
	claudeCodeUAPattern = regexp.MustCompile(`(?i)^claude-cli/\d+\.\d+\.\d+`)

	// systemPromptThreshold is the minimum template score for identifying an
	// ordinary prompt when the billing and monitor recognizers do not match.
	systemPromptThreshold = ClaudeCodeSystemPromptThreshold

	// claudeCodeSystemPrompts are identity and auxiliary prompt fragments compared
	// with whitespace-normalized request text during client-format recognition.
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

	// claudeCodeSecurityMonitorMarkers are required in addition to the fixed
	// prefix and minimum length when recognizing a monitor prompt block.
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
	Path             string // HTTP request path
	UserAgent        string // client User-Agent value
	XApp             string // client X-App value
	AnthropicBeta    string // requested Anthropic beta markers
	AnthropicVersion string // requested Anthropic protocol version
	// MaxTokensOneHaiku marks a probe identified by the caller's model and token
	// checks. Validate still requires a matching CLI User-Agent before accepting it.
	MaxTokensOneHaiku bool
}

// IsClaudeCodeClient checks the CLI User-Agent and metadata identity format for
// ingress and mimicry decisions. Both predicates need to pass for this result;
// forwarding callers can also consult separately validated context or proxied
// request evidence. Metadata accepts both formats recognized by the Anthropic parser.
func IsClaudeCodeClient(userAgent, metadataUserID string) bool {
	return claudeCodeUAPattern.MatchString(userAgent) && wire.ParseMetadataUserID(metadataUserID) != nil
}

// NewClaudeCodeValidator constructs a stateless validator for ingress client-format checks.
func NewClaudeCodeValidator() *ClaudeCodeValidator {
	return &ClaudeCodeValidator{}
}

// Validate checks the CLI User-Agent before applying path-specific payload checks.
// Non-Messages paths, count_tokens, and marked Haiku probes require the User-Agent alone.
// Path matching uses a case-sensitive messages substring; the count-token
// exemption requires the /messages/count_tokens suffix. Callers identify probe
// intent before setting MaxTokensOneHaiku because that flag waives payload checks.
//
// Ordinary Messages requests also require a recognized system prompt, nonempty
// X-App, anthropic-beta, and anthropic-version headers, and valid metadata.user_id.
// The decoded body needs a string model, a []any system array, and map[string]any
// metadata so those checks can read the expected request representation.
func (v *ClaudeCodeValidator) Validate(input ClaudeCodeValidationInput, body map[string]any) bool {
	ua := input.UserAgent
	if !claudeCodeUAPattern.MatchString(ua) {
		return false
	}

	// Auxiliary paths lack the ordinary Messages payload. The validated CLI
	// header identifies their client format before body and metadata checks are needed.
	path := input.Path
	if !strings.Contains(path, "messages") {
		return true
	}

	// Count-token requests can omit identity prompts and metadata, so a matching
	// CLI header suffices for this auxiliary path after the initial User-Agent gate.
	if isMessagesCountTokensPath(path) {
		return true
	}

	// Callers mark single-token Haiku probes before validation. Their smaller
	// payload can omit system prompts and required Messages headers once the CLI
	// User-Agent gate has passed.
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

// isMessagesCountTokensPath reports whether the path has the Messages counting suffix.
func isMessagesCountTokensPath(path string) bool {
	return strings.HasSuffix(path, "/messages/count_tokens")
}

// hasClaudeCodeSystemPrompt recognizes security-monitor markers, billing blocks,
// or identity templates whose Dice similarity meets the threshold.
// A model string and system array are required; monitor recognition examines
// typed text blocks while billing and identity matching read each block's text.
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

		// Billing attribution can identify auxiliary requests whose text differs
		// from identity templates. Its protocol prefix and entrypoint marker use
		// case-sensitive matching before the broader similarity calculation.
		if strings.HasPrefix(text, claudeCodeBillingHeaderPrefix) &&
			strings.Contains(text, claudeCodeEntrypointMarker) {
			return true
		}

		// When exact structural recognizers do not match, the similarity threshold
		// lets small wording variations still identify a configured client prompt template.
		bestScore := v.BestSimilarityScore(text)
		if bestScore >= systemPromptThreshold {
			return true
		}
	}

	return false
}

// isClaudeCodeSecurityMonitorPrompt checks individual system blocks, allowing
// the CLI to add conversation context before or after the monitor prompt.
// A matching text block needs the fixed prefix, byte-length minimum, and every
// structural marker. Markers split between different blocks cannot satisfy it.
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

		// A prefix alone also appears in shorter or altered classifier prompts.
		// The minimum byte length and complete marker set constrain monitor recognition.
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

// hasAllClaudeCodeSecurityMonitorMarkers checks the full marker set within one prompt.
func hasAllClaudeCodeSecurityMonitorMarkers(text string) bool {
	for _, marker := range claudeCodeSecurityMonitorMarkers {
		if !strings.Contains(text, marker) {
			return false
		}
	}

	return true
}

// BestSimilarityScore returns the highest Dice similarity against the Claude Code templates.
// Whitespace is normalized before comparison, and DiceCoefficient handles case
// folding. Empty input obtains zero against the nonempty configured templates.
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

// normalizePrompt returns prompt text with trimmed, collapsed whitespace between words.
func normalizePrompt(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// DiceCoefficient returns the Sorensen-Dice similarity of case-folded rune bigrams.
// Equal input strings return one immediately, including equal empty strings;
// differing inputs with no usable bigrams return zero.
//
// Each input is lowercased and represented by adjacent rune pairs with their
// multiplicities. The intersection adds the smaller count for each shared pair,
// so one repeated pair cannot match more occurrences than the other input has.
// Dividing twice that intersection by both total pair counts yields the score
// used for identity-template recognition.
func DiceCoefficient(a, b string) float64 {
	if a == b {
		return 1.0
	}

	// Differing strings need an adjacent pair for a meaningful similarity score.
	// The byte guard handles ASCII single characters; the later empty-bigram guard
	// covers inputs containing just one multibyte rune.
	if len(a) < 2 || len(b) < 2 {
		return 0.0
	}

	bigramsA := getBigrams(a)
	bigramsB := getBigrams(b)

	if len(bigramsA) == 0 || len(bigramsB) == 0 {
		return 0.0
	}

	intersection := 0
	// For example, abab and ab share one ab pair among four total pairs.
	// Limiting each shared count keeps their score at one half despite the repeated pair.
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

// ExtractVersion returns the three numeric release components from a leading
// CLI User-Agent. It accepts case variants and trailing suffixes through
// ExtractClaudeCLIVersion, returning an empty string for an unmatched prefix.
func (v *ClaudeCodeValidator) ExtractVersion(ua string) string {
	return ExtractClaudeCLIVersion(ua)
}

// IsHaikuProbe reports whether a single-token request names a Haiku model.
func IsHaikuProbe(model string, maxTokens int) bool {
	return maxTokens == 1 && strings.Contains(strings.ToLower(model), "haiku")
}
