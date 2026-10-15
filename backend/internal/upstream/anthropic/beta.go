package anthropic

import (
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

// beta.go selects Anthropic beta headers and applies authentication-specific
// header defaults.
// Anthropic beta headers depend on authentication, client tokens, and whether
// the request imitates Claude Code. Messages mimic selects a fixed set, while
// CountTokens mimic also keeps client tokens and adds the counting token before
// configured drops apply.
//
// The returned boolean tells request builders whether to set a header,
// including an empty value after filtering. OAuth passthrough can select model
// defaults, and API Key injection requires both its switch and a tools or
// thinking feature. Body sanitization consumes this final choice later, so
// changing selection or filtering here also changes which extension fields can
// be sent.

// DefaultDroppedBetasSet contains static beta exclusions and is shared when
// MergeDropSets receives no additions; callers copy it before mutation.
var DefaultDroppedBetasSet = BuildBetaTokenSet(DroppedBetas)

// GetBetaHeader returns the OAuth beta header using client tokens or model
// defaults. An existing OAuth substring keeps the client text unchanged;
// otherwise OAuth follows Claude Code when that exact token is present.
func GetBetaHeader(modelID string, clientBetaHeader string) string {
	if clientBetaHeader != "" {
		if strings.Contains(clientBetaHeader, BetaOAuth) {
			return clientBetaHeader
		}

		parts := strings.Split(clientBetaHeader, ",")
		for i, p := range parts {
			parts[i] = strings.TrimSpace(p)
		}

		// Locate the exact trimmed Claude Code token before inserting OAuth. The
		// addition follows that token's supplied position, retaining surrounding
		// capabilities in their client order.
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

	// The absent-client Haiku path selects its reduced passthrough set here;
	// ComputeFinalAnthropicBeta handles full mimic selection before reaching this
	// helper.
	if strings.Contains(strings.ToLower(modelID), "haiku") {
		return HaikuBetaHeader
	}

	return DefaultBetaHeader
}

// RequestNeedsBetaFeatures reports a nonempty tools array or enabled/adaptive
// thinking in the request.
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

// DefaultAPIKeyBetaHeader selects the API Key default from the body's
// configured model field. A case-insensitive Haiku substring chooses its
// reduced header; all other model strings use APIKeyBetaHeader.
func DefaultAPIKeyBetaHeader(body []byte) string {
	modelID := gjson.GetBytes(body, "model").String()
	if strings.Contains(strings.ToLower(modelID), "haiku") {
		return APIKeyHaikuBetaHeader
	}
	return APIKeyBetaHeader
}

// ApplyClaudeOAuthHeaderDefaults fills empty Accept and default fingerprint
// values on the supplied req. Existing nonempty values found through
// GetHeaderRaw take precedence, and a nil request produces no writes. Callers
// initialize Header for each nonnil request before calling.
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

// MergeAnthropicBeta returns required tokens followed by unique incoming tokens
// in encounter order. It trims token whitespace and skips empty values, using
// case-sensitive equality for deduplication.
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

// MergeAnthropicBetaDropping merges required and incoming tokens before
// applying exact drop membership. An empty drop map returns the merged header;
// required tokens can also be removed by that map.
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

// ComputeFinalAnthropicBeta selects the Messages beta value and whether the
// caller should set it. OAuth mimic uses the full built-in set, OAuth
// passthrough uses client tokens or model defaults, and each of those paths
// applies effectiveDropSet.
//
// API Key client headers also pass through the drop set. With no client value,
// default injection requires injectAPIKeyBeta and tools or thinking; that
// default branch returns its header directly. A true boolean can accompany an
// empty filtered value, which callers still set before body sanitization.
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
			// Messages mimic takes its configured identity capabilities from the full set,
			// so unrelated incoming experiments do not enter this branch's selected Header.
			return MergeAnthropicBetaDropping(FullClaudeCodeMimicryBetas(), "", effectiveDropSet), true
		}
		return StripBetaTokensWithSet(GetBetaHeader(modelID, clientBeta), effectiveDropSet), true
	}

	if clientBeta != "" {
		return StripBetaTokensWithSet(clientBeta, effectiveDropSet), true
	}
	// For API Key requests with no client Header, body features trigger defaults
	// only when injection is enabled; the selected defaults return directly from
	// this branch.
	if injectAPIKeyBeta {
		if RequestNeedsBetaFeatures(body) {
			if beta := DefaultAPIKeyBetaHeader(body); beta != "" {
				return beta, true
			}
		}
	}
	return "", false
}

// ComputeFinalCountTokensAnthropicBeta selects the counting beta header and its
// set flag. OAuth mimic merges the full mimic set and counting token with
// client tokens before filtering; passthrough adds counting when its substring
// is absent from the selected Header.
//
// A missing OAuth passthrough Header returns CountTokensBetaHeader directly,
// bypassing effectiveDropSet. API Key handling follows its client-Header or
// feature-injection rules and does not add a counting token separately. The
// caller uses the selected value to sanitize beta-dependent body fields before
// building the request.
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
			// Counting combines required capabilities with client tokens before drops apply,
			// allowing the final body sanitizer to see the same merged set sent upstream.
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
	// For API Key requests with no client Header, body features trigger defaults
	// only when injection is enabled; the selected defaults return directly from
	// this branch.
	if injectAPIKeyBeta {
		if RequestNeedsBetaFeatures(body) {
			if beta := DefaultAPIKeyBetaHeader(body); beta != "" {
				return beta, true
			}
		}
	}
	return "", false
}

// StripBetaTokens removes exact tokens from a comma-separated header through a
// constructed lookup set. An empty header or token list returns the supplied
// header string unchanged.
func StripBetaTokens(header string, tokens []string) string {
	if header == "" || len(tokens) == 0 {
		return header
	}
	return StripBetaTokensWithSet(header, BuildBetaTokenSet(tokens))
}

// StripBetaTokensWithSet filters exact tokens after trimming each
// comma-separated part. Empty inputs leave the header unchanged; when every
// part survives, its supplied spelling and spacing are retained.
func StripBetaTokensWithSet(header string, drop map[string]struct{}) string {
	if header == "" || len(drop) == 0 {
		return header
	}
	// Rebuild a filtered Header from surviving complete tokens; when every part
	// survives, keep the supplied formatting so mere whitespace does not force a
	// rewrite.
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

// MergeDropSets combines static drops, policy drops, and optional exact tokens.
// With no policy or extra tokens it returns the shared DefaultDroppedBetasSet,
// which callers must treat as immutable.
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

// DroppedBetaSet returns a new mutable copy of the static drop set plus extra
// tokens. Mutating this result does not change DefaultDroppedBetasSet, and
// extra strings are inserted as supplied.
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

// ContainsBetaToken reports exact, case-sensitive membership among trimmed
// header parts. Empty header or token strings return false, and a versioned
// token cannot match a longer token by prefix.
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

// FilterBetaTokens removes exact filter members while retaining supplied order.
// Empty tokens or filters return the original slice, so callers requiring
// private mutable storage must copy that result.
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

// BuildBetaTokenSet returns a new case-sensitive set of nonempty strings. It
// keeps token spelling as supplied, skips duplicates through map membership,
// and leaves surrounding whitespace intact.
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

// ApplyClaudeCodeMimicHeaders replaces default fingerprint values on a nonnil
// request and selects JSON Accept for both response modes. Streaming adds the
// helper marker, and a missing request identifier receives a UUID. Callers
// initialize Header for each nonnil request before calling.
func ApplyClaudeCodeMimicHeaders(req *http.Request, isStream bool) {
	if req == nil {
		return
	}
	ApplyClaudeOAuthHeaderDefaults(req)
	// Replace forwarded fingerprint values under their selected wire spelling so
	// the request uses one set of runtime CLI values after default filling.
	for key, value := range DefaultHeaders {
		if value == "" {
			continue
		}
		SetHeaderRaw(req.Header, ResolveWireCasing(key), value)
	}
	// Accept remains JSON for both modes; the streaming helper marker identifies
	// that mode separately, so an event-stream Accept value is not needed here.
	SetHeaderRaw(req.Header, "Accept", "application/json")
	if isStream {
		SetHeaderRaw(req.Header, "x-stainless-helper-method", "stream")
	}
	// Generate a request identifier after checking all recognized key forms, so
	// a supplied identifier remains available for correlation on the outgoing request.
	if GetHeaderRaw(req.Header, "x-client-request-id") == "" {
		SetHeaderRaw(req.Header, "x-client-request-id", uuid.NewString())
	}
}
