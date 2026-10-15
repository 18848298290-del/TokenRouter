package openaiforward

import (
	"context"
	"net/http"

	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// chat_ports.go defines the provider capabilities and callbacks used to
// prepare, forward, and read Chat requests.
// A Chat URL can receive either Chat messages or Responses input, and the
// resolved provider protocol determines which representation reaches the
// upstream. Chat bindings expose shape conversion, model-aware cache identity,
// OAuth normalization, and response reading while the runner owns route
// selection and recovery order.

// ChatProfile combines selected provider capabilities with resolved Chat dispatch observations.
type ChatProfile struct {
	MessagesProfile // selected provider capability snapshot
	// resolved upstream protocol identifier
	Protocol protocol.ProtocolID
	// Adaptive enables shape-sensitive dispatch; SupportsNativeCN permits
	// Responses-shaped native CN input; CNProvider identifies a CN platform;
	// OpenAIAPIKey identifies an OpenAI API Key target.
	Adaptive, SupportsNativeCN, CNProvider, OpenAIAPIKey bool
}

// ChatPorts supplies request preparation, routing, and Responses-to-Chat output for one attempt.
type ChatPorts interface {
	// PrepareChat resolves the provider attempt and resets its request
	// observations before identity preparation. Later preparation can fail after
	// provider state is replaced; successful profiles determine the runner's
	// dispatch route.
	PrepareChat(context.Context) (ChatProfile, error)
	// DispatchChat delegates another selected protocol route with the current body
	// and cache identity.
	DispatchChat(context.Context, Dispatch, []byte, string, string) (*Result, error)
	// APIKeyID reads the authenticated downstream key identifier for request
	// isolation.
	APIKeyID() int64
	// ProviderIdentity rewrites client metadata using the selected provider
	// namespace and downstream key. The supplied map is writable and stays bound
	// to this attempt while later header construction uses that identity.
	ProviderIdentity(body map[string]any, key int64)
	// AgentRecoveryTried reports whether request context already records an agent-
	// task recovery.
	AgentRecoveryTried(ctx context.Context) bool
	// AutoCacheKey reports whether the model permits compatibility prompt-cache
	// key injection.
	AutoCacheKey(model string) bool
	// BillingModel selects the charge model using provider mapping and a caller
	// default.
	BillingModel(model, fallback string) string
	// CodexTransform applies OAuth-compatible preparation to the supplied writable
	// request map. Callers inspect its Error before adopting serialized bytes and
	// tool mappings; failed preparation can leave map changes visible.
	CodexTransform(body map[string]any, o openai.CodexOAuthTransformOptions) openai.CodexTransformResult
	// CyberError returns the shared warning failure for observed upstream cyber
	// policy.
	CyberError() error
	// CyberPolicy reports whether this attempt recorded an upstream cyber-policy
	// warning.
	CyberPolicy() bool
	// Debug writes the supplied structured diagnostic fields through the execution
	// logger.
	Debug(msg string, fields ...zap.Field)
	// EnsureInstructions inserts the required instructions field into a writable
	// Codex request.
	EnsureInstructions(body map[string]any)
	// ErrorMessage extracts the vendor explanation and sanitizes query credentials
	// for diagnostics.
	ErrorMessage(body []byte) string
	// FailoverHTTP applies provider failure policy before final client error
	// presentation. A nonnil result returns recovery guidance to the outer
	// provider loop; nil keeps the current response for its protocol presenter.
	FailoverHTTP(ctx context.Context, r *http.Response, body []byte, msg, model string) error
	// HashForLog hashes the supplied session identity for diagnostic comparison in
	// logs.
	HashForLog(s string) string
	// InvalidAgentTask classifies response evidence of a rejected agent execution
	// task.
	InvalidAgentTask(status int, body []byte) bool
	// IsAgentIdentity reports whether the selected provider uses agent-task
	// execution credentials.
	IsAgentIdentity(ctx context.Context) bool
	// MarkAgentRecovery returns context recording the request's spent agent-task
	// recovery opportunity.
	MarkAgentRecovery(ctx context.Context) context.Context
	// ReadUpstreamError captures bounded upstream error bytes and closes the
	// consumed network body. The installed adapter replaces the body for later
	// presentation and returns a query-sanitized explanation alongside captured
	// bytes.
	ReadUpstreamError(r *http.Response) ([]byte, string)
	// RecoverAgentTask delegates task recovery using the selected provider's
	// current credential state.
	RecoverAgentTask(ctx context.Context) error
	// RedactErrorBody removes selected provider credential evidence from captured
	// error bytes.
	RedactErrorBody(ctx context.Context, body []byte) []byte
	// ResolvedServiceTier resolves the completion tier using observed output and
	// outgoing intent.
	ResolvedServiceTier(tier *string) *string
	// PrepareTransport resolves the current provider proxy before its next request
	// exchange.
	PrepareTransport()
	// Send exchanges a prepared request after current provider transport
	// preparation. The runner owns a successful response body until reading or
	// HTTP error handling ends, including recursive recovery paths.
	Send(r *http.Request) (*http.Response, error)
	// ServiceTier extracts the normalized service tier from the prepared request
	// bytes.
	ServiceTier(body []byte) *string
	// Sink returns the synchronous client output destination for this provider
	// attempt.
	Sink() upstream.OutputSink
	// ToolNameReverse publishes copied active-turn names and clears future-session
	// names for this request. Keep the supplied map stable while the setter copies
	// it; output readers later borrow the published copy.
	ToolNameReverse(value map[string]string)
	// TransportError presents exchange failure and returns its request or provider
	// recovery signal.
	TransportError(ctx context.Context, err error) error
	// UpdateCodexUsage observes Codex rate-limit headers for the selected provider
	// record.
	UpdateCodexUsage(ctx context.Context, h http.Header)
	// UpstreamContext returns the detached context used to construct an upstream
	// request. Call the returned release after construction; the installed adapter
	// retains request values and server-abort state while detaching client
	// cancellation.
	UpstreamContext(ctx context.Context) (context.Context, context.CancelFunc)
	// UpstreamModel normalizes the charge model for the selected upstream
	// provider.
	UpstreamModel(model string) string
	// ValidateEffort checks supplied reasoning controls against the request model
	// policy.
	ValidateEffort(body []byte, model string) error
	// ClientAllowed reports client admission using request bytes and the provider
	// TLS policy.
	ClientAllowed(ctx context.Context, body []byte) bool
	// PolicyDenied records local client policy rejection in the request's
	// diagnostics.
	PolicyDenied()
	// Reject writes the local protocol failure response for the current request.
	Reject(status int, kind, message string)
	// ResponsesToChat converts the supplied request using bound reasoning-history
	// lookup for tool continuation.
	ResponsesToChat(r *protocolopenai.ResponsesRequest) (*protocolopenai.ChatCompletionsRequest, error)
	// GrokBridgeEligible reports whether Chat input can be represented by Grok
	// Responses conversion.
	GrokBridgeEligible(body []byte) (bool, string)
	// ChatError commits response state and writes the dedicated Chat rejection for
	// preparation failure.
	ChatError(status int, kind, message string)
	// DefaultChat reports whether the current attempt policy selects ordinary Chat
	// execution.
	DefaultChat() bool
	// DeriveCacheKey derives a compatibility prompt seed from the mapped Chat
	// request model.
	DeriveCacheKey(r *protocolopenai.ChatCompletionsRequest, model string) string
	// IsolateCacheKey namespaces the generated prompt seed with the authenticated
	// downstream key.
	IsolateCacheKey(key string) string
	// NormalizeBodyTier returns Responses bytes and their normalized service tier
	// for completion attribution.
	NormalizeBodyTier(body []byte) ([]byte, string, error)
	// ChatToResponses resolves model capabilities and converts Chat to Responses
	// using their options. Capability publication can precede a later conversion
	// failure; callers return errors before constructing the upstream request.
	ChatToResponses(r *protocolopenai.ChatCompletionsRequest) (*protocolopenai.ResponsesRequest, error)
	// NormalizeRequestTier updates the writable Responses request's service-tier
	// value in place.
	NormalizeRequestTier(r *protocolopenai.ResponsesRequest)
	// ApplyChatFast applies provider fast-tier policy to the outgoing converted
	// request bytes.
	ApplyChatFast(ctx context.Context, model string, body []byte) ([]byte, error)
	// EffectiveEffort reads result effort from the final outgoing body after
	// policy and conversion. The installed adapter ignores the original bytes and
	// model hints, so discarded client controls stay discarded.
	EffectiveEffort(body, original []byte, models ...string) *string
	// ThinkingFallback retains a present effort pointer, including an empty
	// string. Otherwise it derives a display effort from enabled thinking and the
	// supplied model for result attribution after protocol preparation.
	ThinkingFallback(effort *string, body []byte, model string) *string
	// AccessToken resolves the current selected provider's credential for upstream
	// request construction.
	AccessToken(ctx context.Context) (string, error)
	// BuildChat constructs the selected provider's streaming Responses request
	// from prepared bytes. Callers supply the detached upstream context and
	// credential; the builder borrows request state for synchronous header
	// construction.
	BuildChat(ctx context.Context, body []byte, token, key string) (*http.Request, error)
	// UpstreamSessionKey namespaces a prompt seed by downstream key and current
	// Codex identity.
	UpstreamSessionKey(id int64, key string) string
	// SessionUUID derives the deterministic UUID representation of a selected
	// upstream session key.
	SessionUUID(key string) string
	// ChatErrorResponse applies shared Chat failure rules and converts any
	// returned observation. The execution caller retains response-body cleanup and
	// honors returned recovery guidance before another provider attempt.
	ChatErrorResponse(r *http.Response, model string) (*Result, error)
	// ChatResponseOptions binds Chat response reading to request state, provider,
	// and model identities.
	ChatResponseOptions(r *http.Response, original, billing, model string) openai.ChatResponseOptions
}
