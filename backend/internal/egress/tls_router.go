package egress

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

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
type TLSFingerprintRouterRule struct {
	Name                    string `json:"name"`
	Enabled                 bool   `json:"enabled"`
	MatchType               string `json:"match_type"`
	Pattern                 string `json:"pattern"`
	CaseSensitive           bool   `json:"case_sensitive"`
	TLSFingerprintProfileID int64  `json:"tls_fingerprint_profile_id"`
	UpstreamUserAgent       string `json:"upstream_user_agent,omitempty"`
	UpstreamOriginator      string `json:"upstream_originator,omitempty"`
}

// TLSFingerprintRouter contains request routing rules and token-request settings.
type TLSFingerprintRouter struct {
	ID                                       int64                      `json:"id"`
	Name                                     string                     `json:"name"`
	Description                              *string                    `json:"description"`
	Enabled                                  bool                       `json:"enabled"`
	ChatGPTOAuthTokenUserAgent               string                     `json:"chatgpt_oauth_token_user_agent"`
	ChatGPTOAuthTokenTLSFingerprintProfileID *int64                     `json:"chatgpt_oauth_token_tls_fingerprint_profile_id"`
	CodexInviteResetUserAgent                string                     `json:"codex_invite_reset_user_agent"`
	CodexInviteResetTLSFingerprintProfileID  *int64                     `json:"codex_invite_reset_tls_fingerprint_profile_id"`
	Rules                                    []TLSFingerprintRouterRule `json:"rules"`
	CreatedAt                                time.Time                  `json:"created_at"`
	UpdatedAt                                time.Time                  `json:"updated_at"`
}

// ValidationError identifies the invalid field and the reason.
type ValidationError struct {
	Field   string
	Message string
}

// Validate checks the router name, token settings, and each rule.
func (r *TLSFingerprintRouter) Validate() error {
	if r.Name == "" {
		return &ValidationError{Field: "name", Message: "name is required"}
	}
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

// Validate requires a pattern and compiles it when the normalized match type is regex.
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

// NormalizeTLSRouterMatchType trims and lowercases the type, defaulting unknown values to contains.
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
