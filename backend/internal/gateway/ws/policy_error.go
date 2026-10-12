package ws

import "fmt"

// GenericPolicyError marks an upstream status excluded by custom error-code settings.
type GenericPolicyError struct {
	upstreamStatus int
}

func (e *GenericPolicyError) Error() string {
	if e == nil || e.upstreamStatus == 0 {
		return "upstream websocket error not in custom error codes"
	}
	return fmt.Sprintf("upstream websocket status %d not in custom error codes", e.upstreamStatus)
}

// NewGenericPolicyError stores the internal status for generic client-error handling, without the upstream body.
func NewGenericPolicyError(status int) error { return &GenericPolicyError{upstreamStatus: status} }
