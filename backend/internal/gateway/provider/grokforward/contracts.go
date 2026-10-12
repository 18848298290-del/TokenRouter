package grokforward

import (
	"context"
	"net/http"
	"strings"
	"time"

	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
	"github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
	wire "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
)

const ComposerVisionModel = "grok-build-0.1"

// Input holds the selected provider and request parameters.
type Input struct {
	ProviderID                           int64
	ProviderName, ProviderType, Platform string
	OAuth, HTTPPresent, Compact          bool
	Body                                 []byte
	OriginalModel                        string
	Stream                               bool
	StartedAt                            time.Time
}

// Options holds the codec, line-size limit, and request-lifecycle registration function.
type Options struct {
	ResolveAttributes func(context.Context, string) (modelcatalog.Attributes, error)
	Codec             grok.BodyCodec
	MaxLineSize       int
	Enter             func() (func(), error)
}

type (
	Decision struct{ Generic, Failover, RetrySameProvider bool }
	Retry    struct {
		Retryable bool
		Delay     time.Duration
		Deadline  time.Time
		Max       int
	}
)

type Failure struct {
	StatusCode                                      int
	ResponseBody                                    []byte
	ResponseHeaders                                 http.Header
	RetryableOnSameProvider, RequestScopedTransient bool
	SameProviderRetryDelay                          time.Duration
	SameProviderRetryDeadline                       time.Time
	SameProviderRetryMax                            int
}
type Notice struct {
	ProviderID                       int64
	Platform, ProviderName           string
	UpstreamStatusCode               int
	UpstreamRequestID, Kind, Message string
}

// Ports supplies request preparation, provider health, HTTP exchange, and output operations.
type Ports interface {
	BillingModel(string) string
	UpstreamModel(string) string
	ImageModel(string) bool
	InvalidRequest(string, string)
	SetError(int, string, string)
	ClientTools(bridge.ResponsesClientToolMapping)
	CacheIdentity([]byte, string) string
	FreeCacheRoute([]byte, []byte, string) ([]byte, error)
	Credential(context.Context) error
	Detach(context.Context) (context.Context, func())
	ResolveProxy()
	Build(context.Context, []byte, string, bool) (*http.Request, error)
	Do(*http.Request) (*http.Response, error)
	ReadError(*http.Response) []byte
	Latency(int64)
	TransportError(context.Context, error) error
	ReplayNotice(bool)
	ErrorMessage([]byte) string
	Health(context.Context, int, http.Header, []byte, string, bool) Decision
	Observe(Notice)
	HandleError(context.Context, *http.Response, []byte, string) (*forwardcore.OpenAIResult, error)
	ShouldMarkTeam(int, []byte) bool
	MarkTeam(string)
	RetryMetadata(int, []byte) Retry
	Failure(Failure) error
	ObserveSuccess(context.Context, http.Header, int, string)
	ReadStream(context.Context, *http.Response, time.Time, string, string) (upstream.ResponsesObservation, error)
	ReadNonStream(context.Context, *http.Response, string, string) (upstream.ResponsesObservation, error)
	Sink() upstream.OutputSink
	Effort([]byte, string) *string
	ReadBody(*http.Response) ([]byte, error)
	HasTokens(*wire.ForwardUsage) bool
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
