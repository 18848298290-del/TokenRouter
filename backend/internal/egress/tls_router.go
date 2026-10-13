package egress

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// TLS router rules select a profile and outgoing client headers from a request
// User-Agent. Unknown match-type text is normalized to contains, while regex
// patterns are compiled during validation and later cached with the configured
// case behavior for ordered runtime matching.

// TLS router match types define substring, prefix, equality, and
// regular-expression rules used during runtime User-Agent matching.
const (
	// TLSRouterMatchContains matches a User-Agent containing the pattern.
	TLSRouterMatchContains = "contains"
	// TLSRouterMatchPrefix matches a User-Agent starting with the pattern.
	TLSRouterMatchPrefix = "prefix"
	// TLSRouterMatchExact matches a User-Agent equal to the pattern.
	TLSRouterMatchExact = "exact"
	// TLSRouterMatchRegex matches a User-Agent using a regular expression.
	TLSRouterMatchRegex = "regex"
)

// TLSFingerprintRouterRule selects TLS and header settings by User-Agent.
//
// MatchType, Pattern, and CaseSensitive define matching. Runtime caching
// normalizes the mode and trims the pattern; disabled rules remain in
// configuration but are skipped during request selection.
type TLSFingerprintRouterRule struct {
	Name          string `json:"name"`           // administrator rule display name
	Enabled       bool   `json:"enabled"`        // rule matching activation flag
	MatchType     string `json:"match_type"`     // normalized User-Agent matching mode
	Pattern       string `json:"pattern"`        // configured User-Agent match expression
	CaseSensitive bool   `json:"case_sensitive"` // User-Agent case matching flag
	// TLSFingerprintProfileID selects a stored profile when positive and random
	// selection at -1. The routable resolver treats other IDs as built-in
	// selection; rule validation does not check this ID.
	TLSFingerprintProfileID int64 `json:"tls_fingerprint_profile_id"` // matched profile selection identity
	// UpstreamUserAgent carries the outgoing client header selected by a matching
	// rule. A blank value leaves the consuming upstream caller to apply its
	// existing User-Agent selection.
	// selected outgoing User-Agent text
	UpstreamUserAgent string `json:"upstream_user_agent,omitempty"`
	// UpstreamOriginator carries the outgoing originator selected by a matching
	// rule. A blank value leaves the consuming upstream caller to apply its own
	// originator handling.
	// selected outgoing originator text
	UpstreamOriginator string `json:"upstream_originator,omitempty"`
}

// TLSFingerprintRouter contains request routing rules and token-request settings.
//
// ChatGPTOAuthTokenUserAgent and ChatGPTOAuthTokenTLSFingerprintProfileID
// configure token-request identity. CodexInviteResetUserAgent and
// CodexInviteResetTLSFingerprintProfileID configure reset identity; request
// Rules are evaluated separately and in stored order.
type TLSFingerprintRouter struct {
	ID   int64  `json:"id"`   // stored router database identity
	Name string `json:"name"` // administrator router display name
	// optional router description text
	Description *string `json:"description"`
	Enabled     bool    `json:"enabled"` // request matching activation flag
	// ChatGPTOAuthTokenUserAgent supplies dedicated token-request identity when
	// its trimmed value is nonblank. OpenAI authorization otherwise selects its
	// existing token User-Agent through the consuming caller.
	// dedicated OAuth token User-Agent
	ChatGPTOAuthTokenUserAgent string `json:"chatgpt_oauth_token_user_agent"`
	// ChatGPTOAuthTokenTLSFingerprintProfileID distinguishes absent dedicated
	// selection from zero/built-in, -1/random, and a positive profile ID. Router
	// validation rejects values below -1; token callers handle unavailable
	// selected profiles.
	// optional OAuth token profile selection
	ChatGPTOAuthTokenTLSFingerprintProfileID *int64 `json:"chatgpt_oauth_token_tls_fingerprint_profile_id"`
	// CodexInviteResetUserAgent supplies dedicated reset identity when its trimmed
	// value is nonblank. Codex reset and quota callers otherwise use their
	// existing User-Agent selection.
	// dedicated Codex reset User-Agent
	CodexInviteResetUserAgent string `json:"codex_invite_reset_user_agent"`
	// CodexInviteResetTLSFingerprintProfileID distinguishes absent dedicated
	// selection from zero/built-in, -1/random, and a positive profile ID. Router
	// validation rejects values below -1; reset callers handle unavailable
	// selected profiles.
	// optional Codex reset profile selection
	CodexInviteResetTLSFingerprintProfileID *int64 `json:"codex_invite_reset_tls_fingerprint_profile_id"`
	// Rules is an ordered list consumed by MatchUserAgent until its first enabled
	// match. Cached routers and runtime copies allocate a separate rule slice.
	// ordered request User-Agent rules
	Rules []TLSFingerprintRouterRule `json:"rules"`
	// persisted router creation time
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"` // persisted router update time
}

// ValidationError identifies the invalid field and the reason.
type ValidationError struct {
	Field   string // rejected configuration field name
	Message string // configuration validation failure reason
}

// Validate checks a nonempty router name, token/reset User-Agent byte limits,
// optional dedicated profile ID ranges, and every rule in order. Rules are
// validated even when disabled; the first rule error is wrapped with its
// one-based position.
func (r *TLSFingerprintRouter) Validate() error {
	if r.Name == "" {
		return &ValidationError{Field: "name", Message: "name is required"}
	}
	// Token and reset User-Agents share a byte cap, while dedicated profile IDs
	// separately admit absent, built-in, random, and positive stored selections.
	if len(r.ChatGPTOAuthTokenUserAgent) > 512 {
		return &ValidationError{Field: "chatgpt_oauth_token_user_agent", Message: "chatgpt_oauth_token_user_agent must be at most 512 characters"}
	}
	if r.ChatGPTOAuthTokenTLSFingerprintProfileID != nil && *r.ChatGPTOAuthTokenTLSFingerprintProfileID < -1 {
		return &ValidationError{Field: "chatgpt_oauth_token_tls_fingerprint_profile_id", Message: "chatgpt_oauth_token_tls_fingerprint_profile_id must be null, 0, -1, or a positive profile ID"}
	}
	if len(r.CodexInviteResetUserAgent) > 512 {
		return &ValidationError{Field: "codex_invite_reset_user_agent", Message: "codex_invite_reset_user_agent must be at most 512 characters"}
	}
	if r.CodexInviteResetTLSFingerprintProfileID != nil && *r.CodexInviteResetTLSFingerprintProfileID < -1 {
		return &ValidationError{Field: "codex_invite_reset_tls_fingerprint_profile_id", Message: "codex_invite_reset_tls_fingerprint_profile_id must be null, 0, -1, or a positive profile ID"}
	}
	for i, rule := range r.Rules {
		if err := rule.Validate(); err != nil {
			return &ValidationError{Field: "rules", Message: "rule " + strconv.Itoa(i+1) + " " + err.Error()}
		}
	}
	return nil
}

// Validate requires a nonblank rule pattern and compiles it for a normalized
// regex mode. Unknown mode text normalizes to contains before this switch, and
// profile IDs or outgoing header values are not validated here.
func (r TLSFingerprintRouterRule) Validate() error {
	if strings.TrimSpace(r.Pattern) == "" {
		return &ValidationError{Field: "pattern", Message: "pattern is required"}
	}
	switch NormalizeTLSRouterMatchType(r.MatchType) {
	case TLSRouterMatchContains, TLSRouterMatchPrefix, TLSRouterMatchExact:
		return nil
	case TLSRouterMatchRegex:
		if _, err := regexp.Compile(r.Pattern); err != nil {
			return &ValidationError{Field: "pattern", Message: "regex is invalid"}
		}
		return nil
	default:
		return &ValidationError{Field: "match_type", Message: "match_type must be contains, prefix, exact, or regex"}
	}
}

// NormalizeTLSRouterMatchType trims and lowercases mode text, retaining prefix,
// exact, and regex. Empty, contains, and unrecognized values return contains,
// so validation and cached runtime matching share that default mode.
func NormalizeTLSRouterMatchType(matchType string) string {
	switch strings.ToLower(strings.TrimSpace(matchType)) {
	case TLSRouterMatchPrefix:
		return TLSRouterMatchPrefix
	case TLSRouterMatchExact:
		return TLSRouterMatchExact
	case TLSRouterMatchRegex:
		return TLSRouterMatchRegex
	default:
		return TLSRouterMatchContains
	}
}

// Error returns the field name and validation reason.
func (e *ValidationError) Error() string {
	return e.Field + ": " + e.Message
}
