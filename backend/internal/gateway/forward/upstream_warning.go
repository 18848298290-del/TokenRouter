package forward

import (
	"errors"
)

// Policy warnings can accompany a successful transport exchange and still
// require refusal handling. Error-chain extraction retains the warning's
// captured body and message for HTTP and completion callers, which separately
// decide policy classification, client output, settlement, and cleanup.

// ErrCyberPolicyForwarded marks the refusal-handling outcome so execution callers
// can perform error cleanup after that output path has been selected.
var ErrCyberPolicyForwarded = errors.New("openai cyber_policy forwarded to client")

// UpstreamWarning holds a policy warning; completion decides billing and HTTP decides response output.
type UpstreamWarning struct {
	StatusCode int // observed upstream HTTP status
	// ResponseBody retains captured policy evidence. WarningFromError clones its
	// bytes, while direct result assignments can retain the producer's body slice.
	ResponseBody []byte
	Message      string // captured upstream warning text
}

// UpstreamWarningCarrier exposes an upstream warning through an error chain.
type UpstreamWarningCarrier interface {
	// OpenAIUpstreamWarning returns the warning attached to this error, or nil
	// when no warning is available. Extraction reads it synchronously and clones
	// its body before returning evidence to a caller.
	OpenAIUpstreamWarning() *UpstreamWarning
}

// WarningFromError extracts a carrier-provided warning from an error chain and
// copies its metadata and response bytes. Nil errors, absent carriers, and nil
// warning values return nil and false; the helper does not independently classify
// the supplied warning as a cyber-policy refusal.
func WarningFromError(err error) (*UpstreamWarning, bool) {
	if err == nil {
		return nil, false
	}
	var carrier UpstreamWarningCarrier
	if !errors.As(err, &carrier) || carrier == nil {
		return nil, false
	}
	warning := carrier.OpenAIUpstreamWarning()
	if warning == nil {
		return nil, false
	}
	return cloneWarning(warning), true
}

// cloneWarning copies warning metadata and allocates independent storage for
// nonempty response bytes. Nil input returns nil, and an empty nonnil body can
// become nil because the copy appends zero bytes to a nil slice.
func cloneWarning(warning *UpstreamWarning) *UpstreamWarning {
	if warning == nil {
		return nil
	}
	cloned := *warning
	if warning.ResponseBody != nil {
		cloned.ResponseBody = append([]byte(nil), warning.ResponseBody...)
	}
	return &cloned
}
