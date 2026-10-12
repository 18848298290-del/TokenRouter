package ws

import "fmt"

// Provider custom error-code rules can require a generic client failure while
// internal diagnostics retain the upstream status. GenericPolicyError carries
// that classification through the WebSocket error chain, where HTTP adapters
// choose the final event and close presentation.

// GenericPolicyError identifies a provider status excluded by custom error-code
// rules.
type GenericPolicyError struct {
	upstreamStatus int // upstream HTTP status code
}

// Error formats the retained upstream status for internal generic-policy
// diagnostics. Nil receivers and zero statuses use the generic label, while
// nonzero statuses include their numeric code without an upstream response
// body.
func (e *GenericPolicyError) Error() string {
	if e == nil || e.upstreamStatus == 0 {
		return "upstream websocket error not in custom error codes"
	}
	return fmt.Sprintf("upstream websocket status %d not in custom error codes", e.upstreamStatus)
}

// NewGenericPolicyError creates the generic-policy marker retaining the
// upstream status for diagnostics.
func NewGenericPolicyError(status int) error { return &GenericPolicyError{upstreamStatus: status} }
