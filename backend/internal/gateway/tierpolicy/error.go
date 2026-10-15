package tierpolicy

// error.go represents blocked Fast policy decisions as errors carrying
// client-facing messages. Keeping that message in a distinct error type lets
// HTTP and WebSocket callers recognize a policy refusal and format it using
// their own response envelope.

// BlockedError carries the client-facing message for a service tier policy
// rejection.
type BlockedError struct {
	Message string // client-facing policy refusal message
}

// Error returns the configured client-facing message for this tier refusal.
func (e *BlockedError) Error() string { return e.Message }
