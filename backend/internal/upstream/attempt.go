package upstream

import (
	"context"
	"net/http"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
)

// ExecutionTarget identifies a selected provider; platform executors interpret its concrete type.
type ExecutionTarget interface{ TargetID() int64 }

// AttemptInput carries the protocol, payload, and selected target for one attempt.
type AttemptInput struct {
	Protocol      protocol.ProtocolID
	Body          []byte
	ResponseModel string
	Stream        bool
	Target        ExecutionTarget
}

// AttemptResult reports observed output and usage even when execution also returns an error.
type AttemptResult struct {
	// UpstreamResponseModel is the model declared before protocol conversion; empty means absent.
	UpstreamResponseModel string
	// HTTPCommitted and RetryCommitted track HTTP commitment and closure of the retry window.
	HTTPCommitted, RetryCommitted bool
	RequestID                     string
	// Responses metadata includes response identity, search count, and image usage.
	ResponseID                                 string
	SearchCount, ImageInputTokens              int
	ImageOutputSizes                           []string
	UpstreamHeaders                            http.Header
	Model, UpstreamModel                       string
	Usage                                      TokenUsage
	HasUsage, Served, Stream, ClientDisconnect bool
	// EstimatedTokenCount is a local countTokens estimate; settlement uses Usage.
	EstimatedTokenCount *int
	// ObservedImages counts images seen upstream; callers apply pricing and model-based defaults.
	ObservedImages int
	// AudioUsage contains observed audio quantities for caller-owned pricing and completion.
	AudioUsage *protocol.AudioUsage
	// MediaBody contains the bounded media JSON read and emitted by this attempt.
	MediaBody []byte
	// FailureClass describes the failure; callers decide retry and settlement policy.
	FailureClass string
	Cancelled    bool
	Duration     time.Duration
	// FirstSemanticOutput measures time to content, excluding leading progress events.
	FirstSemanticOutput          *time.Duration
	FirstTokenMs                 *int
	ServiceTier, ReasoningEffort string
}

// Executor performs one upstream attempt; the gateway handles provider switching and settlement.
// @project-doc docs/architecture/gateway_request_lifecycle.md#upstream_attempt_ownership
type Executor interface {
	Execute(context.Context, AttemptInput, OutputSink) (AttemptResult, error)
}

// TokenUsage is the usage extracted by protocol parsers.
type TokenUsage = protocol.TokenUsage
