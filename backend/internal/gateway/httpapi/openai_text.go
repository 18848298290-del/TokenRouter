package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/gateway/execution"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/moderation"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// openai_text.go defines admitted text request state and transfers it to
// synchronous execution.
// Text HTTP admission uses several request identities for different consumers:
// provider affinity can include derived content, explicit isolation requires a
// client session, and upstream prompt caching needs its own raw seed.
// OpenAITextCall carries those prepared values separately when the entry
// invokes execution.
//
// The handler retains admitted key, subscription and body references through
// one synchronous Execute call, with a mutable started flag shared by wait and
// output handling. Its backend supplies validation, policy, ownership and
// resource steps; the entry determines their order for Responses, Chat and
// Messages. Execution receives that prepared state and synchronous output,
// while the adapter discards returned execution observations and errors after
// the call.

// OpenAISessionInput values select affinity, explicit isolation and upstream prompt-cache
// consumers so one request can derive each identity under its own rules.
const (
	OpenAISelectionSession   OpenAISessionInput = iota // provider affinity session selection
	OpenAIExplicitSession                              // explicit client-session isolation input
	OpenAIPromptCacheSession                           // raw upstream prompt-cache seed
)

// OpenAITextOptions carries text HTTP input limits and app assembly settings.
// App binding applies MaxSwitches to the execution loop, while the handler
// uses body size, Compact interval and force-client diagnostic values
// directly.
type OpenAITextOptions struct {
	// MaxBodyBytes bounds normalized lenient JSON bytes after reading and
	// decoding. A nonpositive value uses the normalizer's 64 MiB default.
	MaxBodyBytes int64
	MaxSwitches  int // assembly provider-switch budget
	// CompactKeepaliveInterval selects the heartbeat duration for admitted
	// Compact streams; a nonpositive interval leaves that writer disabled.
	CompactKeepaliveInterval time.Duration
	// ForceCodexCLI records startup forced-client policy in Compact diagnostics;
	// the upstream request builders receive their execution policy separately.
	ForceCodexCLI bool
}

// OpenAISessionInput identifies the distinct session derivation requested by
// an HTTP consumer.
type OpenAISessionInput uint8

// OpenAITextCall carries admitted HTTP state into synchronous text execution.
// Body, key and subscription values are borrowed until Execute returns; the
// completion path takes any required durable snapshots within execution.
type OpenAITextCall struct {
	Route    routing.RoutePlan   // resolved group route plan
	Protocol protocol.ProtocolID // inbound client protocol identity
	Key      *apikey.APIKey      // effective admitted API key
	Subject  authctx.AuthSubject // authenticated acting-user identity
	// resolved funding subscription snapshot
	Subscription *billing.UserSubscription
	// Body, ForwardBody and SessionHashBody retain distinct preparation phases.
	// Execution borrows these bytes until return; unchanged transformations can
	// share their backing storage, so callers keep every retained view stable.
	// Body: admitted canonical execution bytes.
	// ForwardBody: group-mapped first-attempt request bytes.
	// SessionHashBody: Responses bytes before effort/bootstrap rewriting.
	Body, ForwardBody, SessionHashBody []byte
	// Model, ForwardModel, SessionHash, PreviousResponseID and Platform retain
	// separate client, attempt, affinity and continuation identities. The executor
	// receives them together, so provider rewriting can retain the original model
	// and authenticated continuation context for result attribution.
	// Model: original client request model.
	// ForwardModel: group-mapped first-attempt model.
	// SessionHash: provider-affinity selection session hash.
	// PreviousResponseID: admitted client continuation identifier.
	// Platform: compatible request platform identity.
	Model, ForwardModel, SessionHash, PreviousResponseID, Platform string
	// Stream, NativeCompactionV2, LegacyCompact and RequireCompact describe client
	// output and admitted compaction separately. Native and legacy modes select
	// different capability rules before the executor chooses an upstream attempt.
	// Stream: client streaming response preference.
	// NativeCompactionV2: admitted remote-compaction trigger mode.
	// LegacyCompact: admitted legacy Compact endpoint.
	// RequireCompact: provider Compact capability requirement.
	Stream, NativeCompactionV2, LegacyCompact, RequireCompact bool
	// StreamStarted shares the request-local output flag with slot waiting and
	// MessagesOutput. Keep this pointer valid until the synchronous executor returns.
	StreamStarted *bool
	// SelectionContext carries admitted image and lease hints for Responses
	// selection. Other entries can leave it nil for their execution defaults.
	SelectionContext context.Context
	// resolved group model mapping
	Mapping      routing.GroupMappingResult
	RoutingStart time.Time // routing-phase start timestamp
	// selected Responses capability requirement
	RequiredCapability provider.OpenAIEndpointCapability
	// ProviderLayerModel and PromptCacheKey carry protocol-specific preparation
	// values into execution. Messages supplies the provider-layer model, while
	// Chat and Messages pass their upstream cache seed separately from SessionHash.
	// ProviderLayerModel: prepared Messages provider-rule model.
	// PromptCacheKey: prepared upstream cache identity.
	ProviderLayerModel, PromptCacheKey string
	// request-scoped structured diagnostic logger
	Log *zap.Logger
}

// OpenAITextBackend supplies synchronous text admission, resource and
// observation steps. Entries retain the live Gin request while these methods
// prepare execution state; implementations supply their protocol rejection
// behavior when returning a handled failure.
type OpenAITextBackend interface {
	// Access returns the selected key and its lookup flag. The default binding
	// retains the source pointer assertion, so typed nil can accompany true;
	// HTTP entries rely on authentication publishing a nonnil admitted key.
	Access(*gin.Context) (*apikey.APIKey, bool)
	// Dependencies reports readiness of the services used by the text entry.
	// Missing services are logged and can write an available 503 response;
	// callers stop processing on false before reading input.
	Dependencies(*gin.Context, *zap.Logger) bool
	// ReadFailure records request-body read diagnostics for the supplied logger
	// and request.
	ReadFailure(*zap.Logger, *http.Request, error)
	// TransportHTTP records HTTP as the client transport for request observation.
	TransportHTTP(*gin.Context)
	// StartCompact starts the admitted Compact heartbeat and returns a stop
	// callback. Callers retain that callback through request processing and
	// invoke it on return, so heartbeat output finishes before the HTTP writer is
	// released.
	StartCompact(*gin.Context, time.Duration) func()
	// StopCompact prevents later beats and waits for an active Compact heartbeat
	// write. Its result reports first-beat framing state, including a failed
	// write, which selects terminal error presentation; worker exit is independent.
	StopCompact(*gin.Context) bool
	// ObserveRequest publishes the client model and streaming preference for
	// request diagnostics.
	ObserveRequest(*gin.Context, string, bool)
	// ObserveEndpoint records the request endpoint and streaming request-type
	// observation in Ops.
	ObserveEndpoint(*gin.Context, bool)
	// Snapshot captures canonical text input for later cyber-warning attribution
	// using the selected protocol.
	Snapshot(*gin.Context, protocol.ProtocolID, []byte)
	// Reasoning records requested effort and applies the current OpenAI group
	// effort policy to input bytes. Callers adopt changed bytes and reject
	// returned policy errors before moderation or upstream execution begins.
	Reasoning(*gin.Context, *apikey.APIKey, []byte) ([]byte, bool, error)
	// MessageReasoning binds requested Messages effort and any applicable
	// conversion policy in request context. A missing usable client
	// output_config.effort keeps the bridge-generated default outside that
	// binding; later conversion applies the stored policy.
	MessageReasoning(*gin.Context, *apikey.APIKey, []byte)
	// PolicyDenied records local policy limitation on the current request for
	// Ops.
	PolicyDenied(*gin.Context)
	// NormalizeBootstrap repairs delegation bootstrap when its second argument
	// is true, and automation bootstrap otherwise. The change flag identifies
	// adopted repair bytes; the entry retains its current body when false.
	NormalizeBootstrap([]byte, bool) ([]byte, bool)
	// ValidateTier validates the input service-tier field before upstream
	// preparation. A returned error ends the HTTP entry with its protocol
	// validation response, and successful validation leaves later provider
	// fast-tier policy to execution.
	ValidateTier([]byte) error
	// PreviousKind classifies a supplied continuation identifier for HTTP
	// validation and diagnostic fields.
	PreviousKind(string) string
	// ValidateOwner reports whether the response belongs to the authenticated
	// user/key within the selected group. A lookup error is returned separately;
	// the Responses entry uses ownership failure to stop before moderation and
	// provider selection.
	ValidateOwner(context.Context, int64, string, int64, int64) (bool, error)
	// SetOwner publishes authenticated user and key identity for subsequent
	// response-owner binding.
	SetOwner(*gin.Context, int64, int64)
	// Moderate returns the content moderation observation for admitted canonical
	// input. An absent result leaves admission open; a blocked decision supplies
	// protocol rejection status and message before provider execution, and
	// implementation errors follow the moderation adapter's reporting rules.
	Moderate(*gin.Context, *zap.Logger, *apikey.APIKey, authctx.AuthSubject, protocol.ProtocolID, string, []byte) *moderation.Decision
	// Plan resolves group model mapping and route policy for the admitted key and
	// model.
	Plan(context.Context, *apikey.APIKey, string) routing.RoutePlan
	// BindPlan installs the resolved group route plan in the live request
	// context.
	BindPlan(*gin.Context, routing.RoutePlan)
	// ImageIntent derives group-mapped body/model and broad image intent for
	// Responses preparation. Callers use the returned mapped input for explicit
	// admission checks while retaining canonical bytes for request identity and
	// later attempt policy.
	ImageIntent(string, []byte, routing.GroupMappingResult, string) ([]byte, string, bool)
	// ExplicitImageIntent detects client-declared image generation for
	// permission, slot and capability admission.
	ExplicitImageIntent(string, string, []byte) bool
	// ImageContext derives a selection context carrying the admitted
	// image-generation marker.
	ImageContext(context.Context) context.Context
	// AllowsImages reports whether the admitted key group permits explicit
	// Responses image generation.
	AllowsImages(*apikey.APIKey) bool
	// FeatureDenied records local feature-gate limitation on the current request
	// for Ops.
	FeatureDenied(*gin.Context)
	// ImagePermissionMessage supplies the client message for image-generation
	// permission rejection.
	ImagePermissionMessage() string
	// ImageSlot acquires image admission and returns release with the admission
	// result. A false result has produced protocol rejection; callers invoke any
	// nonnil release after execution, and bypassed admission may return true with
	// nil cleanup.
	ImageSlot(*gin.Context, bool) (func(), bool)
	// SeedImageIntent seeds request-local broad and group-mapped image hints
	// before attempt preparation.
	SeedImageIntent(*gin.Context, bool, bool)
	// ValidateTools validates local function-call-output context before upstream
	// execution. A false result means the helper handled the protocol rejection;
	// callers terminate that entry instead of selecting a provider for unusable
	// tool history.
	ValidateTools(*gin.Context, []byte, *zap.Logger) bool
	// BindErrors installs optional administrator error presentation rules for
	// this live request.
	BindErrors(*gin.Context)
	// Platform identifies the compatible request platform from the admitted
	// API-key group.
	Platform(*apikey.APIKey) string
	// AuthLatency records the elapsed authentication/admission preparation time
	// in integer milliseconds.
	AuthLatency(*gin.Context, int64)
	// UserSlot waits for user admission and returns a cleanup callback on
	// success. It can update the started flag while sending wait output; callers
	// invoke nonnil cleanup after execution, and false stops the entry after
	// rejection or cancellation.
	UserSlot(*gin.Context, int64, int, bool, *bool, *zap.Logger) (func(), bool)
	// Eligibility invokes the configured funding admission after user-slot
	// acquisition. The default HTTP binding passes an empty platform and
	// afterWait false, so the common FundingAdmission can include RPM admission
	// alongside its funding check.
	Eligibility(context.Context, *apikey.APIKey, *billing.UserSubscription) error
	// SessionHash selects provider affinity, explicit isolation or prompt-cache
	// identity from request input. OpenAISessionInput distinguishes those uses so
	// callers do not reuse a derived content hash where an explicit upstream
	// session seed is required.
	SessionHash(*gin.Context, OpenAISessionInput, []byte) string
	// RejectCyber reports whether a known session cyber block was rejected in the
	// selected client protocol. True ends entry processing; request bytes and
	// model provide risk attribution before isolation and the provider loop
	// begin.
	RejectCyber(*gin.Context, *apikey.APIKey, []byte, string, protocol.ProtocolID) bool
	// Isolate validates or records the supplied explicit session identity for
	// this admitted user/key. The optional binding may return nil when isolation
	// is unavailable; a conflict or service error reaches the protocol-specific
	// HTTP rejection path.
	Isolate(context.Context, *apikey.APIKey, int64, string, string) error
	// GuardianContext derives parent-affinity state from canonical input for
	// provider selection.
	GuardianContext(context.Context, *gin.Context, []byte, string) context.Context
	// AllowsMessages reports whether the admitted group allows the Messages
	// client protocol.
	AllowsMessages(*apikey.APIKey) bool
	// MessageProviderModel records the group-mapped Messages model before
	// subsequent provider model policy.
	MessageProviderModel(context.Context, *apikey.APIKey, string) string
	// MetadataSession derives Messages selection/cache seeds using Claude Code
	// headers and metadata hints.
	MetadataSession(*gin.Context, string, string, string, []byte) (string, string)
	// ChatImageModel reports whether the group-mapped model is rejected at the
	// Chat endpoint.
	ChatImageModel(string, routing.GroupMappingResult) bool
	// ErrorMetadata returns the current request identifier and model for terminal
	// error output.
	ErrorMetadata(*gin.Context) (string, string)
	// MarkStream records a started-stream error with its intended status for Ops.
	MarkStream(*gin.Context, string, string, int)
	// MarkStreamFailure records an SLA-counted stream failure with status, code
	// and message.
	MarkStreamFailure(*gin.Context, string, string, string, int)
	// EnsureFallback attempts heartbeat-aware final error output when no
	// dedicated rejection was delivered. The result indicates whether output was
	// invoked, while prior response state can suppress fallback and successful
	// network delivery is not confirmed.
	EnsureFallback(*gin.Context, bool) bool
}

// OpenAITextHandler runs HTTP admission before delegating to the app-bound
// text executor.
type OpenAITextHandler struct {
	executor execution.Executor // app-bound synchronous text executor
	// RequestLifetime tracks this admitted HTTP request until handler return.
	// BeginRequest supplies cleanup that each protocol entry defers around execution.
	RequestLifetime

	options OpenAITextOptions // captured text-entry startup settings
	backend OpenAITextBackend // synchronous HTTP admission bindings
	prompt  MessagesPrompt    // shared user-prompt rewrite policy
}

// NewOpenAITextHandler captures options and supplied admission, prompt and
// execution bindings. Construction performs no validation or resource
// acquisition; callers install usable dependencies and any RequestLifetime
// binding before serving the three text entries.
func NewOpenAITextHandler(options OpenAITextOptions, backend OpenAITextBackend, prompt MessagesPrompt, executor execution.Executor) *OpenAITextHandler {
	return &OpenAITextHandler{executor: executor, options: options, backend: backend, prompt: prompt}
}

// executeText translates admitted call data and current context hints into one
// execution request. It borrows key, subscription and byte slices while
// Execute runs with synchronous MessagesOutput; both returned execution result
// and error are discarded by this HTTP adapter.
func (h *OpenAITextHandler) executeText(c *gin.Context, call OpenAITextCall, kind execution.TextKind) {
	request := execution.Request{
		Hints:       requeststate.ExecutionHintsFromContext(c.Request.Context()),
		Routing:     requeststate.RoutingStateFromContext(c.Request.Context()),
		Route:       call.Route,
		UserID:      call.Subject.UserID,
		Concurrency: call.Subject.Concurrency,
		Stream:      call.Stream,
		Body:        call.Body,
		Model:       call.Model,
		Funding: execution.FundingState{
			Key:          call.Key,
			Subscription: call.Subscription,
		},
		SessionHash: call.SessionHash,
		AttemptBody: call.ForwardBody,

		Text: execution.TextState{Kind: kind, Platform: call.Platform, SelectionContext: call.SelectionContext, Mapping: call.Mapping, SessionHashBody: call.SessionHashBody, ForwardModel: call.ForwardModel, PreviousResponseID: call.PreviousResponseID, ProviderLayerModel: call.ProviderLayerModel, PromptCacheKey: call.PromptCacheKey, NativeCompactionV2: call.NativeCompactionV2, LegacyCompact: call.LegacyCompact, RequireCompact: call.RequireCompact, RequiredCapability: call.RequiredCapability, RoutingStart: call.RoutingStart},
	}
	output := &MessagesOutput{ResponseSink: ResponseSink{Writer: c.Writer}, HTTP: c, Log: call.Log, StreamStarted: call.StreamStarted}
	// Execution owns client output through this sink. The adapter discards its
	// result and error, so the executor supplies any terminal rejection before returning.
	_, _ = h.executor.Execute(c.Request.Context(), request, output)
}
