package forward

import (
	"encoding/json"
	"time"

	protocolcore "github.com/TokenFlux/TokenRouter/internal/protocol"
	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// OpenAIResult holds observations from an OpenAI-compatible execution and private replay input.
type OpenAIResult struct {
	// LocalWarmup marks local warmup responses, which skip billing and upstream health updates.
	LocalWarmup bool `json:"-"`
	// UpstreamResponseModel is the upstream model before protocol conversion; empty means undeclared.
	UpstreamResponseModel string
	// NativeUsage retains the non-OpenAI executor's input token buckets for converted results.
	NativeUsage *protocolcore.TokenUsage
	RequestID   string
	ResponseID  string
	// UpstreamHeaders contains the direct upstream's headers for provider-specific request ID extraction.
	UpstreamHeaders map[string][]string
	Usage           protocolopenai.ForwardUsage
	Model           string // Client-facing model used in responses and logs.
	// BillingModel is the model used for cost calculation.
	// When non-empty, CalculateCost uses this instead of Model.
	// This is set by the Anthropic Messages conversion path where
	// the mapped upstream model differs from the client-facing model.
	BillingModel string
	// UpstreamModel is the actual model sent to the upstream provider after mapping.
	// Empty when no mapping was applied (requested model was used as-is).
	UpstreamModel string
	// UpstreamResponseServiceTier is the observed tier used to downgrade the billed tier when applicable.
	UpstreamResponseServiceTier string
	// UpstreamEndpoint is the API path used for this attempt; a downstream protocol may use several paths.
	UpstreamEndpoint string
	// ServiceTier is the final tier sent upstream after policy rewriting.
	// The upstream response declaration remains separate above and is reconciled
	// at usage-recording time, where the credential protocol is available.
	ServiceTier *string
	// ReasoningEffort is the final upstream effort; nil means absent or inapplicable.
	ReasoningEffort *string
	// RequestedReasoningEffort is the client's effort before policy and model mapping.
	RequestedReasoningEffort *string
	Stream                   bool
	OpenAIWSMode             bool
	// UpstreamTerminalEvent is the normalized Responses WebSocket terminal event.
	// An empty value is treated as success, including for non-WebSocket calls.
	UpstreamTerminalEvent string
	ResponseHeaders       map[string][]string
	Duration              time.Duration
	FirstTokenMs          *int
	ClientDisconnect      bool
	ImageCount            int
	ImageSize             string
	ImageInputSize        string
	ImageOutputSize       string
	ImageOutputSizes      []string
	ImageSizeSource       string
	ImageSizeBreakdown    map[string]int
	// UpstreamWarning holds a terminal policy refusal received after successful transport.
	UpstreamWarning *UpstreamWarning
	VideoCount      int
	VideoResolution string
	// VideoState is running, completed, failed, or canceled for an asynchronous video task.
	VideoState string
	// VideoDurationSeconds is the requested duration normalized to 1-15 seconds for xAI output billing.
	VideoDurationSeconds int
	// WebSearchCalls counts successful Codex alpha/search requests, one per request.
	// The upstream omits usage; positive counts use group unit price times count times multiplier.
	WebSearchCalls int
	// SearchCount counts Grok native web_search or tool search calls, priced per thousand calls.
	SearchCount int
	// AudioUsage holds Voice billing units when present.
	AudioUsage *protocolcore.AudioUsage

	wsReplayInput                 []json.RawMessage
	wsReplayInputExists           bool
	wsProviderFailoverReplayInput []json.RawMessage
}

// SucceededForScheduling reports whether the result qualifies as an upstream scheduling success.
// Nil and zero-value results count as success; local warmups do not.
func (r *OpenAIResult) SucceededForScheduling() bool {
	if r != nil && r.LocalWarmup {
		return false
	}
	if r == nil || !r.OpenAIWSMode || r.UpstreamTerminalEvent == "" {
		return true
	}
	switch r.UpstreamTerminalEvent {
	case "response.completed", "response.done":
		return true
	default:
		return false
	}
}

// SetWSReplayInput stores the caller's normalized replay input and input-presence flag for this turn.
func (r *OpenAIResult) SetWSReplayInput(input []json.RawMessage, exists bool) {
	r.wsReplayInput = input
	r.wsReplayInputExists = exists
}

// WSReplayInput returns the stored input and presence flag without inferring presence from its length.
func (r *OpenAIResult) WSReplayInput() ([]json.RawMessage, bool) {
	return r.wsReplayInput, r.wsReplayInputExists
}

// SetWSProviderFailoverReplayInput stores input collected for recovery across providers.
func (r *OpenAIResult) SetWSProviderFailoverReplayInput(input []json.RawMessage) {
	r.wsProviderFailoverReplayInput = input
}

// WSProviderFailoverReplayInput returns replay input for recovering the current turn.
func (r *OpenAIResult) WSProviderFailoverReplayInput() []json.RawMessage {
	return r.wsProviderFailoverReplayInput
}
