package anthropic

import (
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

// DefaultDroppedBetasSet is shared; callers must copy it before adding tokens.
var DefaultDroppedBetasSet = BuildBetaTokenSet(DroppedBetas)

// GetBetaHeader adds the OAuth beta to the client header or selects model defaults.
func GetBetaHeader(modelID string, clientBetaHeader string) string {
	if clientBetaHeader != "" {
		if strings.Contains(clientBetaHeader, BetaOAuth) {
			return clientBetaHeader
		}

		parts := strings.Split(clientBetaHeader, ",")
		for i, p := range parts {
			parts[i] = strings.TrimSpace(p)
		}

		// Place OAuth immediately after the Claude Code beta when present.
		claudeCodeIdx := -1
		for i, p := range parts {
			if p == BetaClaudeCode {
				claudeCodeIdx = i
				break
			}
		}

		if claudeCodeIdx >= 0 {
			newParts := make([]string, 0, len(parts)+1)
			newParts = append(newParts, parts[:claudeCodeIdx+1]...)
			newParts = append(newParts, BetaOAuth)
			newParts = append(newParts, parts[claudeCodeIdx+1:]...)
			return strings.Join(newParts, ",")
		}

		return BetaOAuth + "," + clientBetaHeader
	}

	// Haiku passthrough defaults omit the Claude Code beta. Mimic requests use a separate path.
	if strings.Contains(strings.ToLower(modelID), "haiku") {
		return HaikuBetaHeader
	}

	return DefaultBetaHeader
}

func RequestNeedsBetaFeatures(body []byte) bool {
	tools := gjson.GetBytes(body, "tools")
	if tools.Exists() && tools.IsArray() && len(tools.Array()) > 0 {
		return true
	}
	thinkingType := gjson.GetBytes(body, "thinking.type").String()
	if strings.EqualFold(thinkingType, "enabled") || strings.EqualFold(thinkingType, "adaptive") {
		return true
	}
	return false
}

func DefaultAPIKeyBetaHeader(body []byte) string {
	modelID := gjson.GetBytes(body, "model").String()
	if strings.Contains(strings.ToLower(modelID), "haiku") {
		return APIKeyHaikuBetaHeader
	}
	return APIKeyBetaHeader
}

func ApplyClaudeOAuthHeaderDefaults(req *http.Request) {
	if req == nil {
		return
	}
	if GetHeaderRaw(req.Header, "Accept") == "" {
		SetHeaderRaw(req.Header, "Accept", "application/json")
	}
	for key, value := range DefaultHeaders {
		if value == "" {
			continue
		}
		if GetHeaderRaw(req.Header, key) == "" {
			SetHeaderRaw(req.Header, ResolveWireCasing(key), value)
		}
	}
}

func MergeAnthropicBeta(required []string, incoming string) string {
	seen := make(map[string]struct{}, len(required)+8)
	out := make([]string, 0, len(required)+8)

	add := func(v string) {
		v = strings.TrimSpace(v)
		if v == "" {
			return
		}
		if _, ok := seen[v]; ok {
			return
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}

	for _, r := range required {
		add(r)
	}
	for p := range strings.SplitSeq(incoming, ",") {
		add(p)
	}
	return strings.Join(out, ",")
}

func MergeAnthropicBetaDropping(required []string, incoming string, drop map[string]struct{}) string {
	merged := MergeAnthropicBeta(required, incoming)
	if merged == "" || len(drop) == 0 {
		return merged
	}
	out := make([]string, 0, 8)
	for p := range strings.SplitSeq(merged, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, ok := drop[p]; ok {
			continue
		}
		out = append(out, p)
	}
	return strings.Join(out, ",")
}

// ComputeFinalAnthropicBeta returns the outbound beta header and whether to set it.
// Call this after metadata and billing-version updates, before removing body fields
// that require a beta token. A nil clientHeaders means no client header.
//
// When shouldSet is true, the caller must set the header even if filtering leaves
// an empty value. API Key requests without client betas return false unless beta
// injection is enabled and the body uses beta features.
func ComputeFinalAnthropicBeta(
	tokenType string,
	mimicClaudeCode bool,
	modelID string,
	clientHeaders http.Header,
	body []byte,
	effectiveDropSet map[string]struct{},
	injectAPIKeyBeta bool,
) (string, bool) {
	clientBeta := ""
	if clientHeaders != nil {
		clientBeta = GetHeaderRaw(clientHeaders, "anthropic-beta")
	}

	if tokenType == "oauth" {
		if mimicClaudeCode {
			// Mimic requests use the full Claude Code beta set for every model, including Haiku.
			return MergeAnthropicBetaDropping(FullClaudeCodeMimicryBetas(), "", effectiveDropSet), true
		}
		return StripBetaTokensWithSet(GetBetaHeader(modelID, clientBeta), effectiveDropSet), true
	}

	if clientBeta != "" {
		return StripBetaTokensWithSet(clientBeta, effectiveDropSet), true
	}
	if injectAPIKeyBeta {
		if RequestNeedsBetaFeatures(body) {
			if beta := DefaultAPIKeyBetaHeader(body); beta != "" {
				return beta, true
			}
		}
	}
	return "", false
}

// ComputeFinalCountTokensAnthropicBeta returns the count_tokens beta header.
// OAuth mimic merges client betas with the full mimic set and token-counting beta.
// OAuth passthrough uses CountTokensBetaHeader when the client header is absent,
// or adds the token-counting beta to the client header before filtering.
// The return values follow ComputeFinalAnthropicBeta.
func ComputeFinalCountTokensAnthropicBeta(
	tokenType string,
	mimicClaudeCode bool,
	modelID string,
	clientHeaders http.Header,
	body []byte,
	effectiveDropSet map[string]struct{},
	injectAPIKeyBeta bool,
) (string, bool) {
	clientBeta := ""
	if clientHeaders != nil {
		clientBeta = GetHeaderRaw(clientHeaders, "anthropic-beta")
	}

	if tokenType == "oauth" {
		if mimicClaudeCode {
			// Count-token mimic merges client betas before filtering; Messages mimic ignores them.
			requiredBetas := append(FullClaudeCodeMimicryBetas(), BetaTokenCounting)
			return MergeAnthropicBetaDropping(requiredBetas, clientBeta, effectiveDropSet), true
		}
		if clientBeta == "" {
			return CountTokensBetaHeader, true
		}
		beta := GetBetaHeader(modelID, clientBeta)
		if !strings.Contains(beta, BetaTokenCounting) {
			beta = beta + "," + BetaTokenCounting
		}
		return StripBetaTokensWithSet(beta, effectiveDropSet), true
	}

	if clientBeta != "" {
		return StripBetaTokensWithSet(clientBeta, effectiveDropSet), true
	}
	if injectAPIKeyBeta {
		if RequestNeedsBetaFeatures(body) {
			if beta := DefaultAPIKeyBetaHeader(body); beta != "" {
				return beta, true
			}
		}
	}
	return "", false
}

// StripBetaTokens removes the given beta tokens from a comma-separated header value.
func StripBetaTokens(header string, tokens []string) string {
	if header == "" || len(tokens) == 0 {
		return header
	}
	return StripBetaTokensWithSet(header, BuildBetaTokenSet(tokens))
}

func StripBetaTokensWithSet(header string, drop map[string]struct{}) string {
	if header == "" || len(drop) == 0 {
		return header
	}
	parts := strings.Split(header, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, ok := drop[p]; ok {
			continue
		}
		out = append(out, p)
	}
	if len(out) == len(parts) {
		return header // Return the original header when no token was removed.
	}
	return strings.Join(out, ",")
}

// MergeDropSets merges the static DefaultDroppedBetasSet with dynamic policy filter tokens.
// It returns the shared set when policySet and extra are empty; callers must not mutate it.
func MergeDropSets(policySet map[string]struct{}, extra ...string) map[string]struct{} {
	if len(policySet) == 0 && len(extra) == 0 {
		return DefaultDroppedBetasSet
	}
	m := make(map[string]struct{}, len(DefaultDroppedBetasSet)+len(policySet)+len(extra))
	for t := range DefaultDroppedBetasSet {
		m[t] = struct{}{}
	}
	for t := range policySet {
		m[t] = struct{}{}
	}
	for _, t := range extra {
		m[t] = struct{}{}
	}
	return m
}

// DroppedBetaSet returns DroppedBetas as a set, with optional extra tokens.
func DroppedBetaSet(extra ...string) map[string]struct{} {
	m := make(map[string]struct{}, len(DefaultDroppedBetasSet)+len(extra))
	for t := range DefaultDroppedBetasSet {
		m[t] = struct{}{}
	}
	for _, t := range extra {
		m[t] = struct{}{}
	}
	return m
}

// ContainsBetaToken checks if a comma-separated header value contains the given token.
func ContainsBetaToken(header, token string) bool {
	if header == "" || token == "" {
		return false
	}
	for p := range strings.SplitSeq(header, ",") {
		if strings.TrimSpace(p) == token {
			return true
		}
	}
	return false
}

func FilterBetaTokens(tokens []string, filterSet map[string]struct{}) []string {
	if len(tokens) == 0 || len(filterSet) == 0 {
		return tokens
	}
	kept := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if _, filtered := filterSet[token]; !filtered {
			kept = append(kept, token)
		}
	}
	return kept
}

func BuildBetaTokenSet(tokens []string) map[string]struct{} {
	m := make(map[string]struct{}, len(tokens))
	for _, t := range tokens {
		if t == "" {
			continue
		}
		m[t] = struct{}{}
	}
	return m
}

// ApplyClaudeCodeMimicHeaders sets Claude Code headers for OAuth requests.
func ApplyClaudeCodeMimicHeaders(req *http.Request, isStream bool) {
	if req == nil {
		return
	}
	ApplyClaudeOAuthHeaderDefaults(req)
	// Override client fingerprint headers using their wire casing, such as x-app.
	for key, value := range DefaultHeaders {
		if value == "" {
			continue
		}
		SetHeaderRaw(req.Header, ResolveWireCasing(key), value)
	}
	// Real Claude CLI uses Accept: application/json (even for streaming).
	SetHeaderRaw(req.Header, "Accept", "application/json")
	if isStream {
		SetHeaderRaw(req.Header, "x-stainless-helper-method", "stream")
	}
	// Supply a request UUID when the client has not provided one.
	if GetHeaderRaw(req.Header, "x-client-request-id") == "" {
		SetHeaderRaw(req.Header, "x-client-request-id", uuid.NewString())
	}
}
