package forward

// Qoder error display can use a normalized client status that differs from the
// upstream status. Provider error classification retains both, together with
// the original formatted message and body, so HTTP adapters can display the
// selected error while preserving source diagnostics.

// QoderErrorView holds classified client-output and source diagnostics for a Qoder error.
type QoderErrorView struct {
	// Recognized reports whether provider classification found a Qoder API error.
	// False leaves callers on their generic error path rather than interpreting
	// the remaining zero-valued display fields.
	Recognized bool
	// Status is the classified client HTTP status, while SourceStatus retains
	// the upstream value. Qoder classification can map authorization and agent-limit
	// errors before the HTTP adapter writes the selected status.
	Status, SourceStatus int // client HTTP status; upstream HTTP status
	// client error kind; formatted upstream message; captured upstream error body
	Kind, Message, Body string
}
