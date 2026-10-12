package forward

import (
	"time"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// MessagesResult contains raw Messages usage and completion metadata.
type MessagesResult struct {
	// UpstreamResponseModel is the declaration before protocol conversion; empty means absent.
	UpstreamResponseModel string
	RequestID             string
	// UpstreamHeaders contains direct upstream headers for provider-specific request-ID extraction.
	UpstreamHeaders map[string][]string
	Usage           upstream.TokenUsage
	Model           string
	// UpstreamModel is the actual upstream model after mapping.
	// It may equal Model when no mapping was applied.
	UpstreamModel    string
	Stream           bool
	Duration         time.Duration
	FirstTokenMs     *int // Time to first token, in milliseconds.
	ClientDisconnect bool // Whether the client disconnected during streaming.
	ReasoningEffort  *string
	// RequestedReasoningEffort captures client effort before compatibility mapping.
	RequestedReasoningEffort *string
	// UpstreamResponseServiceTier is the observed tier; empty means absent or unconfirmed.
	UpstreamResponseServiceTier string
	// ServiceTier is the requested tier; billing may lower it based on upstream observations.
	ServiceTier *string
	// Image-generation billing metadata.
	ImageCount         int    // Number of generated images.
	ImageSize          string // Billing tier: "1K", "2K", or "4K".
	ImageInputSize     string // Requested image dimensions.
	ImageOutputSize    string // Dimensions reported by upstream.
	ImageOutputSizes   []string
	ImageSizeSource    string
	ImageSizeBreakdown map[string]int
	SearchCount        int
	AudioUsage         *protocol.AudioUsage
}

// MessagesFromAttempt copies basic attempt observations; callers add headers and media metadata.
func MessagesFromAttempt(result upstream.AttemptResult) *MessagesResult {
	return &MessagesResult{
		RequestID:             result.RequestID,
		Model:                 result.Model,
		UpstreamModel:         result.UpstreamModel,
		UpstreamResponseModel: result.UpstreamResponseModel,
		Usage:                 result.Usage,
		Stream:                result.Stream,
		Duration:              result.Duration,
		ClientDisconnect:      result.ClientDisconnect,
		FirstTokenMs:          result.FirstTokenMs,
	}
}
