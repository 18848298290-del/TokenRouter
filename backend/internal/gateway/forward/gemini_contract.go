package forward

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// Antigravity Gemini forwarding retains both the entry model and its selected
// provider mapping while sending the internal project/model wrapper. Retry and
// recovery operate on request-scoped execution data, with response hooks deciding
// error handling before normal stream output or collection continues.

// GeminiInput supplies entrypoint model, body, session, and provider preparation state.
type GeminiInput struct {
	StartedAt     time.Time // entrypoint forwarding start time
	Model, Action string    // entrypoint request model; requested Gemini action
	// Stream chooses downstream output while the exchange uses a streaming
	// upstream action. Sticky records session binding, and TokenAvailable records
	// credential-source configuration before Credential is called.
	Stream, Sticky, TokenAvailable bool // downstream streaming flag; sticky session flag; configured token-source presence
	// Body supplies entrypoint request bytes to identity and model preparation.
	// This input retains the supplied storage; individual rewriting callbacks
	// determine allocation and sharing of their returned bodies.
	Body                []byte
	GroupID, ProviderID int64 // selected request group; selected upstream provider
	// session affinity hash; provider display name; provider platform identifier;
	// request diagnostic log prefix
	SessionHash, ProviderName, Platform, Prefix string
}

// GeminiRecovery retains body and content-type evidence from the platform recovery response.
type GeminiRecovery struct {
	// ErrorBody contains captured recovery response evidence consumed by Before.
	// It follows the recovery adapter's ownership and is not cloned by this holder.
	ErrorBody []byte
	// ContentType accompanies the captured body. Empty selects application/json
	// when the wrapper writes a final Google error response.
	ContentType string
}

// GeminiExecution carries model, body-stage, and session values for one platform exchange.
type GeminiExecution struct {
	// Model retains the selected mapping used by this wrapper's billing result;
	// OriginalModel retains entry input before provider mapping. Recovery can
	// build another internal request without changing these holder fields.
	Model, OriginalModel string    // selected mapped model; original entrypoint model
	StartedAt            time.Time // entrypoint forwarding start time
	Stream, Sticky       bool      // downstream streaming mode; sticky session flag
	// Body is the wrapped model-parameter result for the first exchange, while
	// InjectedBody retains the identity/schema stage used as recovery input.
	// Callbacks determine byte sharing; recovery reapplies model parameters when
	// preparing a replacement request from that retained base.
	Body, InjectedBody                     []byte // initial wrapped request; identity/schema stage body
	ProjectID, UpstreamAction, SessionHash string // upstream project identifier; selected upstream action; session affinity hash
	GroupID                                int64  // selected request group
}

// GeminiHooks controls response handoff while the platform executor owns exchange resources.
type GeminiHooks struct {
	// Exchange runs retry and recovery before response handling begins. The
	// adapter retains the resulting response and captured error evidence for Before.
	Exchange func() error
	// Before receives nonnil metadata after Exchange. A true result stops
	// ordinary response output because this hook selected failover or handled
	// the error body; hook calls are serialized within the execution.
	Before func(context.Context, *ExchangeResponse) (bool, error)
	// OutputError records response-output failure for the selected stream or
	// collection mode. It supplies diagnostics after that failure rather than
	// replacing already processed output with another client envelope.
	OutputError func(error)
}

// GeminiPorts supplies platform preparation, recovery, and output operations.
type GeminiPorts interface {
	// GoogleError writes the client's Google-format error and returns its
	// handling error. Preparation callers propagate that result, while project
	// lookup preserves its original error after requesting the client envelope.
	GoogleError(int, string) error
	// ImageInputSize extracts the request's image size hint for later result
	// metadata.
	ImageInputSize([]byte) string
	// ImageTier converts an image size hint to the billing-size category used
	// by completion.
	ImageTier(string) string
	// ZeroCount writes the local zero-token result for this Antigravity
	// countTokens path.
	ZeroCount()
	// MappedModel resolves the selected provider's allowed Gemini execution
	// model. An empty result denies the request before credentials are
	// acquired; a usable model becomes this wrapper's captured billing and
	// upstream-model value.
	MappedModel(string) string
	// ModelParameters applies the selected model's effective Gemini request
	// attributes. Errors stop preparation; the returned body is wrapped for the
	// first exchange, and platform recovery can reapply attributes from its
	// retained base body.
	ModelParameters(context.Context, string, []byte) ([]byte, error)
	// FeatureDenied records a local model-eligibility rejection before upstream
	// execution begins.
	FeatureDenied()
	// Credential resolves and retains the selected provider's access token. The
	// wrapper checks TokenAvailable first; acquisition failure returns a
	// gateway failover signal without beginning identity injection or the
	// network exchange.
	Credential(context.Context) error
	// ProjectID resolves the selected provider's required project identifier or
	// returns its lookup error. The wrapper requests a client 400 envelope and
	// propagates that original error before wrapping or exchange.
	ProjectID() (string, error)
	// Transport resolves provider transport state used by the prepared
	// Antigravity exchange.
	Transport()
	// InjectIdentity adds the platform identity instruction to the Gemini
	// request body. Invalid request data returns an error that preparation
	// handles as Google 400; storage allocation remains with the
	// implementation.
	InjectIdentity([]byte) ([]byte, error)
	// CleanSchema returns the platform-compatible schema-cleaned request or an
	// error. The wrapper treats failure as diagnostic evidence and retains the
	// identity-injected input body rather than aborting the request here.
	CleanSchema([]byte) ([]byte, error)
	// Wrap encodes project, model, and prepared Gemini bytes in the Antigravity
	// internal request shape. Project-required errors receive their specific
	// client handling; other failures produce the wrapper's generic
	// request-build error.
	Wrap(string, string, []byte) ([]byte, error)
	// ProjectRequired reports whether wrapping failed because the required
	// project identifier is unavailable. The wrapper uses this distinction to
	// choose the project-specific client message instead of a generic
	// build-error envelope.
	ProjectRequired(error) bool
	// Log writes prepared Gemini attempt diagnostics through the platform
	// logger.
	Log(string)
	// StdLog writes the recognized server-configuration retry diagnostic
	// through the standard logger.
	StdLog(string)
	// Retry performs the platform retry loop for the prepared execution and
	// retains its response in the adapter. Provider-switch errors are
	// translated by the wrapper, while cancellation is checked before generic
	// retry-exhausted output.
	Retry(context.Context, GeminiExecution) error
	// SwitchError returns a sticky-switch flag and whether the error is a
	// recognized provider-switch signal. The first value is meaningful when the
	// second is true and becomes ForceCacheBilling on the gateway failover
	// error.
	SwitchError(error) (bool, bool)
	// Failover constructs a provider-switch error from status, captured body,
	// same-provider permission, and sticky cache-billing treatment. The
	// surrounding request loop still decides whether its remaining budget and
	// output permit a replacement provider.
	Failover(int, []byte, bool, bool) error
	// ClientCanceled reports whether the request context has ended before an
	// upstream response. The wrapper checks it after retry failure so client
	// cancellation or deadline expiry does not become a generic
	// provider-exhaustion explanation.
	ClientCanceled() bool
	// Recover applies provider-local model or signature recovery to the
	// retained response and execution base. It returns captured error bytes and
	// content type for Before, updating the adapter response when recovery
	// completes without error.
	Recover(context.Context, GeminiExecution) (GeminiRecovery, error)
	// RequestID publishes a nonempty upstream request identifier through the
	// client response adapter.
	RequestID(string)
	// Unwrap decodes a captured Antigravity error into the client-compatible
	// Google payload. An error or empty result leaves the original bytes as
	// diagnostic and error-output evidence in the forwarding wrapper.
	Unwrap([]byte) ([]byte, error)
	// Health applies selected-provider policy to the original source status,
	// headers, and body. The execution carries the original model and session
	// scope, while the wrapper separately selects client output or provider
	// failover.
	Health(context.Context, int, map[string][]string, []byte, GeminiExecution)
	// ErrorMessage extracts the platform summary used by Gemini error
	// diagnostics.
	ErrorMessage([]byte) string
	// Sanitize masks recognized query credential values in the extracted error
	// summary. It leaves other supplied text unchanged; raw body detail follows
	// its separate configured logging policy and byte limit.
	Sanitize(string) string
	// Detail returns optional bounded body evidence under the platform logging
	// configuration. A disabled body-log policy returns empty, letting the
	// wrapper record a summary independently of the detailed error payload.
	Detail([]byte) string
	// SetError stores source status, summary, and optional body evidence in
	// request diagnostics.
	SetError(int, string, string)
	// GoogleConfigError recognizes a server/project configuration message after
	// the wrapper case-folds it. A matching source HTTP 400 permits
	// same-provider retry and failover rather than ordinary client-body
	// passthrough.
	GoogleConfigError(string) bool
	// Observe submits Gemini source-error metadata through the platform
	// observation adapter.
	Observe(Notice)
	// ShouldFailover reports whether the selected provider's policy permits
	// recovery for this source status. Server-configuration 400 has its
	// separate earlier path; a true result constructs a failover signal before
	// client error-body output.
	ShouldFailover(int) bool
	// TruncateBytes bounds captured body bytes and escapes CR/LF for a routine
	// log excerpt. Escaping can expand the returned string beyond the
	// source-byte budget, and no raw-response ownership is transferred.
	TruncateBytes([]byte, int) string
	// ErrorBody writes the selected captured Google error body with its status
	// and content type. The wrapper uses application/json when content type is
	// absent and stops ordinary successful-response processing afterward.
	ErrorBody(int, string, []byte)
	// Execute owns the prepared platform exchange, response reading, closure,
	// and output mode. It invokes Exchange and Before serially with nonnil
	// response metadata before return, using OutputError for response-output
	// failures and returning captured attempt observations.
	Execute(context.Context, GeminiExecution, GeminiHooks) (upstream.AttemptResult, error)
	// IsImageModel reports whether the selected mapped model uses
	// image-generation classification.
	IsImageModel(string) bool
}
