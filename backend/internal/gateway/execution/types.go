package execution

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

const (
	TextMessages TextKind = iota
	TextGeminiMessages
	TextGenericResponses
	TextGenericChat
	TextNativeGemini
	TextOpenAIResponses
	TextOpenAIChat
	TextOpenAIMessages
)

// Executor binds shared dependencies at construction and receives request state and synchronous output per call.
type Executor interface {
	Execute(context.Context, Request, upstream.OutputSink) (ExecutionResult, error)
}
type TextKind uint8

// TextState contains text-request data prepared before execution.
type TextState struct {
	AlternateBudget                                                      bool
	SelectionContext                                                     context.Context
	SelectionSessionHash                                                 string
	Action                                                               string
	UseDigestFallback                                                    bool
	DigestChain, PrefixHash, SessionUUID, MatchedDigestChain             string
	SignatureState                                                       requeststate.GeminiSignatureState
	SessionHashBody                                                      []byte
	ForwardModel, PreviousResponseID, ProviderLayerModel, PromptCacheKey string
	NativeCompactionV2, LegacyCompact, RequireCompact                    bool
	RequiredCapability                                                   provider.OpenAIEndpointCapability
	RoutingStart                                                         time.Time
	Mapping                                                              routing.GroupMappingResult

	Kind            TextKind
	Parsed          *requeststate.ParsedRequest
	Platform        string
	BoundProviderID int64
	HasBoundSession bool
	GeminiBody      []byte
	GeminiModel     string
}
type Request struct {
	Hints       requeststate.ExecutionHints
	Routing     requeststate.RoutingState
	Access      *apikey.AccessSnapshot
	Route       routing.RoutePlan
	UserID      int64
	Concurrency int
	Stream      bool
	Body        []byte
	Model       string
	Metadata    RequestMetadata
	Funding     FundingState
	SessionHash string
	AttemptBody []byte

	Text TextState
}
type RequestMetadata struct {
	Headers          map[string][]string
	UserAgent        string
	ClientIP         string
	InboundEndpoint  string
	UpstreamEndpoint string

	ClaudeCode bool
	StartedAt  time.Time
}
type FundingState struct {
	Key          *apikey.APIKey
	Subscription *billing.UserSubscription
}
type ExecutionResult struct {
	// PlanProvided reports whether the execution adapter obtained this candidate plan.
	PlanProvided bool

	Attempt  upstream.AttemptResult
	Provider provider.ProviderSnapshot
	Plan     routing.CandidatePlan
	Attempts int
}
