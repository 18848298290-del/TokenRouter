package tierpolicy

// BlockedError carries the client-facing message for a service tier policy rejection.
type BlockedError struct{ Message string }

func (e *BlockedError) Error() string { return e.Message }
