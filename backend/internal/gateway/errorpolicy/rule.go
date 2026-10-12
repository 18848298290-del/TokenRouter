package errorpolicy

import (
	"time"

	"github.com/TokenFlux/TokenRouter/internal/egress"
)

const (
	// MatchModeAny accepts a match in any configured condition category.
	MatchModeAny = "any"

	// MatchModeAll requires a match in every configured condition category.
	MatchModeAll = "all"
)

// ErrorPassthroughRule controls how upstream errors are returned to clients.
// Platform filters apply independently of the error-code and keyword match mode.
type ErrorPassthroughRule struct {
	ID              int64     `json:"id"`
	Name            string    `json:"name"`
	Enabled         bool      `json:"enabled"`
	Priority        int       `json:"priority"`         // Lower values match first.
	ErrorCodes      []int     `json:"error_codes"`      // Any listed status code may match.
	Keywords        []string  `json:"keywords"`         // Any listed keyword may match.
	MatchMode       string    `json:"match_mode"`       // Combines configured code and keyword categories: "any" or "all".
	Platforms       []string  `json:"platforms"`        // Empty matches all platforms.
	PassthroughCode bool      `json:"passthrough_code"` // Use the upstream status code.
	ResponseCode    *int      `json:"response_code"`    // Used when PassthroughCode is false.
	PassthroughBody bool      `json:"passthrough_body"` // Use the extracted upstream error message.
	CustomMessage   *string   `json:"custom_message"`   // Used when PassthroughBody is false.
	SkipMonitoring  bool      `json:"skip_monitoring"`  // Skip ordinary Ops error monitoring.
	Description     *string   `json:"description"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type ValidationError = egress.ValidationError

// Validate checks required match conditions and response actions.
func (r *ErrorPassthroughRule) Validate() error {
	if r.Name == "" {
		return &ValidationError{Field: "name", Message: "name is required"}
	}
	if r.MatchMode != MatchModeAny && r.MatchMode != MatchModeAll {
		return &ValidationError{Field: "match_mode", Message: "match_mode must be 'any' or 'all'"}
	}

	if len(r.ErrorCodes) == 0 && len(r.Keywords) == 0 {
		return &ValidationError{Field: "conditions", Message: "at least one error_code or keyword is required"}
	}
	if !r.PassthroughCode && (r.ResponseCode == nil || *r.ResponseCode <= 0) {
		return &ValidationError{Field: "response_code", Message: "response_code is required when passthrough_code is false"}
	}
	if !r.PassthroughBody && (r.CustomMessage == nil || *r.CustomMessage == "") {
		return &ValidationError{Field: "custom_message", Message: "custom_message is required when passthrough_body is false"}
	}
	return nil
}
