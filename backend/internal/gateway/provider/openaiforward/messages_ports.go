package openaiforward

import (
	"context"
	"net/http"

	"go.uber.org/zap"

	protocolanthropic "github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// Messages conversion can reuse a prompt-cache prefix or a saved response ID
// under the selected provider and downstream key. Request bindings keep those
// identities with converted effort, session headers, and response observations
// while the runner decides when history is trimmed, continuation is repaired,
// or state is stored after output.

type (
	// TemplateData supplies current instructions and model identities to forced template rendering.
	TemplateData struct {
		// ExistingInstructions is the current instruction text; OriginalModel is the
		// client model; NormalizedModel is the pre-mapping Messages model;
		// BillingModel is the charge model; UpstreamModel is the vendor model.
		ExistingInstructions, OriginalModel, NormalizedModel, BillingModel, UpstreamModel string
	}
	// MessagesProfile identifies provider credentials, shadow mode, and continuation capability for conversion.
	MessagesProfile struct {
		Profile       // platform and credential capabilities
		ID      int64 // selected provider identifier
		// GrokOAuth identifies Grok OAuth credentials; Shadow identifies a shadow
		// provider; ContinuationSupported records saved Responses continuation support.
		GrokOAuth, Shadow, ContinuationSupported bool
	}
)

// MessagesPorts binds Messages conversion, continuation state, and output to one provider attempt.
type MessagesPorts interface {
	// Prepare resolves the selected provider and returns its profile and execution
	// route. Observation and identity preparation run before conversion, and
	// errors stop the runner before it parses Messages input.
	Prepare(context.Context) (MessagesProfile, Dispatch, error)
	// Dispatch executes the prepared alternate route with the supplied Messages
	// body and cache seed.
	Dispatch(context.Context, Dispatch, []byte, string, string) (*Result, error)
	// ValidateEffort checks supplied reasoning controls against the request model
	// policy.
	ValidateEffort(body []byte, model string) error
	// Error writes the selected client protocol's local request failure response.
	Error(status int, kind, message string)
	// CloneDigest copies the request history used for cache digest derivation
	// before conversion. The installed session clone retains selected nested
	// pointers, so callers use it as stable digest evidence rather than a fully
	// independent writable request.
	CloneDigest(r *protocolanthropic.AnthropicRequest) *protocolanthropic.AnthropicRequest
	// BillingModel selects the charge model using provider mapping and a caller
	// default.
	BillingModel(model, fallback string) string
	// UpstreamModel normalizes the charge model for the selected upstream
	// provider.
	UpstreamModel(model string) string
	// APIKeyID reads the authenticated downstream key identifier for request
	// isolation.
	APIKeyID() int64
	// ClaudeSession extracts the supported Claude Code session identity from
	// headers or body.
	ClaudeSession(body []byte) string
	// MetadataSession derives a prompt-cache seed from Anthropic request metadata
	// and session identity.
	MetadataSession(r *protocolanthropic.AnthropicRequest) string
	// AutoCacheKey reports whether the model permits compatibility prompt-cache
	// key injection.
	AutoCacheKey(model string) bool
	// CacheControlKey derives a prompt-cache seed from the request's cache-control
	// anchors.
	CacheControlKey(r *protocolanthropic.AnthropicRequest) string
	// DigestChain derives the ordered prompt digest chain from the retained
	// Messages history.
	DigestChain(r *protocolanthropic.AnthropicRequest) string
	// FindDigestKey looks up provider/key-scoped prompt identity by the supplied
	// digest prefix chain.
	FindDigestKey(key int64, chain string) (string, string)
	// DigestKey derives the cache seed for an unmatched Anthropic prompt digest
	// chain.
	DigestKey(chain string) string
	// ContinuationEnabled reports whether provider credentials, capability, and
	// model permit automatic continuation.
	ContinuationEnabled(model string) bool
	// ResponseID reads provider/key/prompt-scoped continuation identity from
	// process-local session state. Missing or expired bindings return empty, and
	// same-session updates require caller coordination when merged-state retention
	// matters.
	ResponseID(ctx context.Context, key string) string
	// ContinuationDisabled reads the provider/key/prompt-scoped unsupported-
	// continuation decision from local session state. Missing or expired evidence
	// returns false, and callers coordinate same-session updates when lookup and
	// cleanup can overlap.
	ContinuationDisabled(ctx context.Context, key string) bool
	// ReplayGuard trims compatible full replay history in the writable Messages
	// request before conversion.
	ReplayGuard(r *protocolanthropic.AnthropicRequest) bool
	// Convert resolves model capabilities and converts the writable Messages
	// request to Responses. Successful capability publication can precede a
	// conversion failure; callers return the error before constructing or sending
	// upstream bytes.
	Convert(r *protocolanthropic.AnthropicRequest) (*protocolopenai.ResponsesRequest, error)
	// BetaFast reports whether the inbound Anthropic beta list contains fast-mode
	// selection.
	BetaFast() bool
	// MessagesEffort resolves Anthropic reasoning controls using the selected
	// model's conversion attributes.
	MessagesEffort(r *protocolanthropic.AnthropicRequest, model, effort string) string
	// TrimLatestTurn trims writable Responses input for a saved-response
	// continuation request.
	TrimLatestTurn(r *protocolopenai.ResponsesRequest)
	// TodoGuard adds the compatibility developer guard to the writable converted
	// Responses request.
	TodoGuard(r *protocolopenai.ResponsesRequest)
	// HashForLog hashes the supplied session identity for diagnostic comparison in
	// logs.
	HashForLog(s string) string
	// Truncate bounds the supplied identity text for request recovery diagnostic
	// log values.
	Truncate(s string, limit int) string
	// LogIDLimit returns the byte ceiling used for diagnostic request-identity
	// excerpts.
	LogIDLimit() int
	// Debug writes the supplied structured diagnostic fields through the execution
	// logger.
	Debug(msg string, fields ...zap.Field)
	// Info writes the supplied structured observations through the execution
	// logger.
	Info(msg string, fields ...zap.Field)
	// CodexTransform applies OAuth-compatible preparation to the supplied writable
	// request map. Callers inspect its Error before adopting serialized bytes and
	// tool mappings; failed preparation can leave map changes visible.
	CodexTransform(body map[string]any, o openai.CodexOAuthTransformOptions) openai.CodexTransformResult
	// ToolNameReverse publishes copied active-turn names and clears future-session
	// names for this request. Keep the supplied map stable while the setter copies
	// it; output readers later borrow the published copy.
	ToolNameReverse(value map[string]string)
	// ForcedTemplate reads the configured Messages-to-Codex instruction template
	// used during conversion.
	ForcedTemplate() string
	// ForcedInstructions renders and installs configured instructions in the
	// writable request map. Its change flag reports installed text; rendering
	// errors stop the runner before outgoing request serialization and credential
	// resolution.
	ForcedInstructions(body map[string]any, text string, data TemplateData) (bool, error)
	// EnsureInstructions inserts the required instructions field into a writable
	// Codex request.
	EnsureInstructions(body map[string]any)
	// TodoGuardBody adds the compatibility developer guard to the writable decoded
	// Responses body.
	TodoGuardBody(body map[string]any)
	// ProviderIdentity updates writable client metadata with the current Codex
	// namespace and key. Later session construction uses this attempt identity, so
	// callers retain the prepared request state through header building.
	ProviderIdentity(body map[string]any, key int64)
	// TurnState reads saved opaque turn-state text for the provider/key/prompt
	// identity. The installed store can return it while continuation is disabled;
	// expiration follows the shared session lifetime.
	TurnState(ctx context.Context, key string) string
	// ApplyEffort applies the admitted group reasoning limit after Messages
	// conversion. The changed result identifies adopted bytes; a policy error
	// writes the bound Anthropic rejection and stops request construction.
	ApplyEffort(ctx context.Context, body []byte) ([]byte, bool, error)
	// ApplyFast applies the current provider fast-tier policy to outgoing
	// Responses request bytes.
	ApplyFast(ctx context.Context, model string, body []byte) ([]byte, error)
	// ServiceTier extracts the normalized service tier from the prepared request
	// bytes.
	ServiceTier(body []byte) *string
	// ResolvedServiceTier resolves the completion tier using observed output and
	// outgoing intent.
	ResolvedServiceTier(tier *string) *string
	// GrokCacheIdentity derives platform cache identity from converted intent,
	// prompt seed, and model.
	GrokCacheIdentity(body []byte, key, model string) string
	// PatchGrokBody applies the selected Grok model's Responses compatibility body
	// rules.
	PatchGrokBody(body []byte, model string) ([]byte, error)
	// ApplyGrokCache applies cache identity using the prepared body and pre-cache
	// intent bytes.
	ApplyGrokCache(body, intent []byte, key string, oauth bool) ([]byte, error)
	// GrokFreeToolRoute applies Free OAuth function-tool routing using intent and
	// cache identity.
	GrokFreeToolRoute(body, intent []byte, key string) ([]byte, error)
	// Credential resolves the selected provider's request credential before
	// authenticated body construction.
	Credential(ctx context.Context) (string, error)
	// BindMessagesBridge records enabled bridge handling for subsequent Codex
	// request construction. The installed helper ignores false, so clearing prior
	// true state requires a separate request-state operation.
	BindMessagesBridge(v bool)
	// UpstreamContext returns the detached context used to construct an upstream
	// request. Call the returned release after construction; the installed adapter
	// retains request values and server-abort state while detaching client
	// cancellation.
	UpstreamContext(ctx context.Context) (context.Context, context.CancelFunc)
	// Build constructs the selected provider's Responses request from prepared
	// bytes and session identities. The first context argument is unused; the
	// installed Grok builder forces streaming while other builders use the
	// supplied stream flag.
	Build(_ context.Context, ctx context.Context, body []byte, token string, stream bool, key, grokIdentity string) (*http.Request, error)
	// IsolatedSessionID derives the deterministic session UUID from key,
	// namespace, and prompt seed.
	IsolatedSessionID(key int64, cache string) string
	// RestoreIdentity enforces the outgoing Codex User-Agent and originator header
	// pair.
	RestoreIdentity(h http.Header)
	// Header reads the named inbound HTTP header for this provider attempt.
	Header(key string) string
	// PrepareTransport resolves the current provider proxy before its next request
	// exchange.
	PrepareTransport()
	// Send exchanges the prepared request after current proxy preparation and
	// model-observer reset. The runner owns successful response-body closure after
	// output; the installed error reader separately closes and replaces captured
	// failure bodies.
	Send(r *http.Request) (*http.Response, error)
	// TransportError presents exchange failure and returns its request or provider
	// recovery signal.
	TransportError(ctx context.Context, err error) error
	// ReadErrorBody returns bounded available error bytes while suppressing read
	// failures. The caller closes the consumed body before replay or replacement,
	// preserving captured bytes for subsequent presentation.
	ReadErrorBody(r *http.Response) []byte
	// GrokInvalidEncrypted recognizes Grok HTTP evidence of rejected encrypted
	// reasoning history.
	GrokInvalidEncrypted(status int, body []byte) bool
	// GrokHasEncrypted reports whether outgoing Responses history contains
	// encrypted reasoning items.
	GrokHasEncrypted(body []byte) bool
	// TrimGrokEncrypted returns repaired Responses bytes and whether ciphertext
	// removal changed them. Errors stop the current repair path; the runner adopts
	// changed bytes for one provider-local replay.
	TrimGrokEncrypted(body []byte) ([]byte, bool, error)
	// ReadUpstreamError captures bounded upstream error bytes and closes the
	// consumed network body. The installed adapter replaces the body for later
	// presentation and returns a query-sanitized explanation alongside captured
	// bytes.
	ReadUpstreamError(r *http.Response) ([]byte, string)
	// AgentRecoveryTried reports whether request context already records an agent-
	// task recovery.
	AgentRecoveryTried(ctx context.Context) bool
	// IsAgentIdentity reports whether the selected provider uses agent-task
	// execution credentials.
	IsAgentIdentity(ctx context.Context) bool
	// InvalidAgentTask classifies response evidence of a rejected agent execution
	// task.
	InvalidAgentTask(status int, body []byte) bool
	// RecoverAgentTask delegates task recovery using the selected provider's
	// current credential state.
	RecoverAgentTask(ctx context.Context) error
	// MarkAgentRecovery returns context recording the request's spent agent-task
	// recovery opportunity.
	MarkAgentRecovery(ctx context.Context) context.Context
	// RedactErrorBody removes selected provider credential evidence from captured
	// error bytes.
	RedactErrorBody(ctx context.Context, body []byte) []byte
	// ErrorMessage extracts the vendor explanation and sanitizes query credentials
	// for diagnostics.
	ErrorMessage(body []byte) string
	// PreviousMissing classifies rejection of the particular stored continuation
	// response identifier.
	PreviousMissing(status int, msg string, body []byte) bool
	// PreviousUnsupported classifies upstream rejection of server-side Responses
	// continuation capability.
	PreviousUnsupported(status int, msg string, body []byte) bool
	// DisableContinuation records unsupported continuation for the
	// provider/key/prompt session before full replay.
	DisableContinuation(ctx context.Context, key string)
	// DeleteResponseID removes the unusable saved response identifier before
	// rebuilding full request history.
	DeleteResponseID(ctx context.Context, key string)
	// GrokStripRetried reports whether context records a prior client-signature
	// repair attempt.
	GrokStripRetried(ctx context.Context) bool
	// StripThinkingSignatures removes replay signatures from the supplied
	// Anthropic client request body.
	StripThinkingSignatures(body []byte) ([]byte, bool)
	// MarkGrokStrip returns context recording the request's spent client-signature
	// repair opportunity.
	MarkGrokStrip(ctx context.Context) context.Context
	// FailoverHTTP applies provider failure policy before final client error
	// presentation. A nonnil result returns recovery guidance to the outer
	// provider loop; nil keeps the current response for its protocol presenter.
	FailoverHTTP(ctx context.Context, r *http.Response, body []byte, msg, model string) error
	// ErrorResponse applies shared Messages failure rules and returns any
	// accompanying observation. It can produce client output or a recovery error;
	// the execution caller retains final response-body cleanup.
	ErrorResponse(r *http.Response, model string) (*Result, error)
	// UpdateGrokUsage records Grok headers and status for successful OAuth
	// provider observation.
	UpdateGrokUsage(ctx context.Context, model string, h http.Header, status int)
	// BindTurnState stores nonblank opaque state for the provider/key/prompt
	// session identity.
	BindTurnState(ctx context.Context, key, state string)
	// Sink returns the synchronous client output destination for this provider
	// attempt.
	Sink() upstream.OutputSink
	// ResponseOptions binds Messages response conversion to the current request
	// and model identities.
	ResponseOptions(r *http.Response, original, billing, model string) openai.MessagesResponseOptions
	// CyberPolicy reports whether this attempt recorded an upstream cyber-policy
	// warning.
	CyberPolicy() bool
	// CyberError returns the failure used when the warning prevents successful
	// completion.
	CyberError() error
	// BindResponseID stores successful continuation identity for the
	// provider/key/prompt session.
	BindResponseID(ctx context.Context, key, id string)
	// BindDigestKey stores the successful digest-chain/cache-key relation and its
	// matched prefix.
	BindDigestKey(id int64, chain, key, matched string)
	// UpdateCodexUsage observes Codex rate-limit headers for the selected provider
	// record.
	UpdateCodexUsage(ctx context.Context, h http.Header)
}
