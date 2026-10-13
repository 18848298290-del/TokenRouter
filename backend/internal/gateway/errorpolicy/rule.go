package errorpolicy

import (
	"time"

	"github.com/TokenFlux/TokenRouter/internal/egress"
)

// Final error-response rules combine status and keyword conditions after a
// separate platform filter. Validation requires configured conditions and
// values for custom response actions; it checks a custom status for
// positivity, so callers do not receive an HTTP range guarantee from this
// model.

// Match modes combine the configured status-code and keyword categories while
// the separate platform filter still qualifies every rule before those conditions.
const (
	// MatchModeAny accepts a match in any configured condition category.
	MatchModeAny = "any"

	// MatchModeAll requires a match in every configured condition category.
	MatchModeAll = "all"
)

// ErrorPassthroughRule controls how upstream errors are returned to clients.
// Platform filters apply independently of the error-code and keyword match mode.
type ErrorPassthroughRule struct {
	ID      int64  `json:"id"`      // persisted error-rule record identifier
	Name    string `json:"name"`    // administrator rule display name
	Enabled bool   `json:"enabled"` // rule participates in final matching
	// Priority selects earlier matches when its value is lower. Matching supplies
	// no secondary key for equal priorities, so callers do not rely on their
	// relative order.
	Priority int `json:"priority"` // ascending error-rule matching priority
	// ErrorCodes, Keywords, and MatchMode describe one condition decision. Empty
	// categories are omitted; at least one category is required, and any/all
	// combines only categories that are configured.
	ErrorCodes []int `json:"error_codes"` // accepted supplied error status values
	// Keywords is a substring list folded to lowercase during compilation.
	// Entries are not trimmed here, and an empty configured keyword matches any
	// converted body.
	Keywords  []string `json:"keywords"`   // case-folded body substring match values
	MatchMode string   `json:"match_mode"` // configured condition category combination mode
	// Platforms is independent of MatchMode and empty lists match all platforms.
	// Compilation folds case but retains supplied whitespace in each platform
	// label.
	Platforms       []string `json:"platforms"`        // case-folded permitted upstream platform labels
	PassthroughCode bool     `json:"passthrough_code"` // retain supplied upstream response status
	// ResponseCode is used when PassthroughCode is false and Validate requires a
	// positive pointed value. Validation supplies no HTTP status range
	// restriction for that replacement.
	ResponseCode    *int `json:"response_code"`    // optional custom response status value
	PassthroughBody bool `json:"passthrough_body"` // retain extracted upstream error message
	// CustomMessage is used when PassthroughBody is false. Validate requires a
	// nonempty pointed string for that action and does not trim its contents.
	CustomMessage *string `json:"custom_message"` // optional administrator response message pointer
	// SkipMonitoring instructs final error consumers to suppress ordinary Ops
	// errors for the request. Provider health classification and other record
	// paths remain the consumers' separate operations.
	SkipMonitoring bool      `json:"skip_monitoring"` // suppress ordinary Ops error observation
	Description    *string   `json:"description"`     // optional administrator rule description text
	CreatedAt      time.Time `json:"created_at"`      // persisted rule creation wall-clock time
	UpdatedAt      time.Time `json:"updated_at"`      // persisted rule update wall-clock time
}

// ValidationError identifies the rejected field and message using the egress
// error representation.
type ValidationError = egress.ValidationError

// Validate returns the first missing name, invalid mode, absent condition
// category, or unusable custom-action value. The receiver must be nonnil;
// condition entries are not validated individually, and custom response status
// requires positivity without an HTTP range check.
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
