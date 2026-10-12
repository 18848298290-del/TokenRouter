package compact

import (
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
)

// RetryObservation snapshots provider, response, and diagnostic settings for one retry.
type RetryObservation struct {
	ProviderPresent      bool
	Platform             string
	ProviderID           int64
	ProviderName         string
	Status               int
	RequestID            string
	Passthrough, LogBody bool
	LogBodyMaxBytes      int
}

// RetryNotice contains diagnostic fields for the caller to log.
type RetryNotice struct {
	RetryObservation
	Message, Detail string
	Kind, Reason    string
}

// Notice builds retry diagnostics, including a bounded payload when body logging is enabled.
func Notice(in RetryObservation, payload []byte, message string, truncate func(string, int) string) *RetryNotice {
	if !in.ProviderPresent {
		return nil
	}
	detail := ""
	if in.LogBody {
		limit := in.LogBodyMaxBytes
		if limit <= 0 {
			limit = 2048
		}
		detail = truncate(string(payload), limit)
	}
	return &RetryNotice{RetryObservation: in, Message: logredact.SanitizeUpstreamQueries(strings.TrimSpace(message)), Detail: detail, Kind: "retry", Reason: "compact_model_fallback"}
}
