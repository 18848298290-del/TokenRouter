package forward

import (
	"time"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// Result holds observations from one forwarding attempt.
type Result struct {
	// UpstreamResponseModel is the upstream model before protocol conversion; empty means undeclared.
	UpstreamResponseModel                      string
	RequestID                                  string
	UpstreamHeaders                            map[string][]string
	Usage                                      upstream.TokenUsage
	Model, UpstreamModel                       string
	Stream                                     bool
	Duration                                   time.Duration
	FirstTokenMs                               *int
	ClientDisconnect                           bool
	ReasoningEffort, RequestedReasoningEffort  *string
	UpstreamResponseServiceTier                string
	ServiceTier                                *string
	ImageCount                                 int
	ImageSize, ImageInputSize, ImageOutputSize string
	ImageOutputSizes                           []string
	ImageSizeSource                            string
	ImageSizeBreakdown                         map[string]int
	SearchCount                                int
	AudioUsage                                 *protocol.AudioUsage
}
