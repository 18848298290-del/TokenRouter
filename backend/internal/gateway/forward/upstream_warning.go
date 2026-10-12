package forward

import (
	"errors"
)

// ErrCyberPolicyForwarded signals that the refusal was sent and the caller should perform error cleanup.
var ErrCyberPolicyForwarded = errors.New("openai cyber_policy forwarded to client")

// UpstreamWarning holds a policy warning; completion decides billing and HTTP decides response output.
type UpstreamWarning struct {
	StatusCode   int
	ResponseBody []byte
	Message      string
}

// UpstreamWarningCarrier exposes an upstream warning through an error chain.
type UpstreamWarningCarrier interface {
	OpenAIUpstreamWarning() *UpstreamWarning
}

// WarningFromError extracts and copies an upstream cyber-policy warning from a forwarding error.
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
