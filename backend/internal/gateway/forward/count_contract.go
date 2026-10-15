package forward

import (
	"context"
)

// count_contract.go defines adapter operations for count-token preparation,
// exchange, recovery, and output. Count requests use provider model and
// credential preparation while retaining the wire body associated with an
// upstream status below 400. A signature-repair retry rebuilds from logical
// input, and its returned response is read before deciding which wire body can
// replace the request's parsed representation.

// CountPorts supplies model, transport, policy, and output operations for one count attempt.
type CountPorts interface {
	// ResolveModel applies the selected provider and platform model rules for
	// counting. Empty results keep the input model at the caller, so absent
	// resolution does not replace it with an empty wire model.
	ResolveModel(context.Context, string) string
	// ReplaceModel returns bytes with the selected model field rewritten.
	// Ordinary count preparation reparses them through
	// ParsedRequest.ReplaceBody; passthrough keeps its mapped body local and
	// forwards it without republishing parsed state here.
	ReplaceModel([]byte, string) []byte
	// IsCountClaudeCode classifies the request from trusted contextual
	// identification or CLI header and metadata evidence. False allows OAuth
	// count normalization; token counting does not require the full Messages
	// system-prompt validator here.
	IsCountClaudeCode(context.Context, string) bool
	// NormalizeOAuth applies the supplied system-cache and metadata
	// normalization choices to count input. Its returned bytes are reparsed
	// before cache and tool policy; byte-slice ownership remains with the
	// platform implementation.
	NormalizeOAuth([]byte, NormalizeOptions) []byte
	// RewriteCache applies configured cache-control rules to the normalized
	// count request. Counting consumes the returned body before tool
	// processing, using the same platform policy reader as ordinary Messages
	// forwarding.
	RewriteCache(context.Context, []byte) []byte
	// RewriteTools returns a renamed-tool body and whether a mapping was
	// produced. Counting retains a true result or uses ToolsLast for false; it
	// does not bind response-restoration state for the count response.
	RewriteTools([]byte) ([]byte, bool)
	// ToolsLast applies the tools-last cache-breakpoint rule when count input
	// needs no tool-name rewrite. Its returned body is reparsed for subsequent
	// request building, and the callback determines whether storage is shared.
	ToolsLast([]byte) []byte
	// Credential resolves and retains the selected provider's token before
	// request building. Acquisition errors produce a count-protocol gateway
	// response at the caller, while passthrough additionally verifies the exact
	// retained token kind.
	Credential(context.Context) error
	// TokenKind returns the credential kind retained for this counting attempt.
	TokenKind() string
	// BuildCount prepares and retains a count request and returns its actual
	// wire body. The booleans select client mimicry and API-key passthrough;
	// callers acquire credentials first and rebuild from logical bytes for a
	// signature retry.
	BuildCount(context.Context, []byte, string, bool, bool) ([]byte, error)
	// SendCount exchanges the previously built request in the selected
	// passthrough mode. A successful result supplies nonnil response metadata;
	// callers consume ReadCount before starting another response, and the
	// adapter owns proxy and TLS selection.
	SendCount(context.Context, bool) (*ExchangeResponse, error)
	// ReadCount reads the current response under its configured size limit and
	// closes its body. A limit error can already write its dedicated count
	// error, while other failures need the caller's generic read-error
	// response.
	ReadCount() ([]byte, error)
	// IsTooLarge recognizes a response-limit error handled by the reader's
	// dedicated count output. The caller returns that error without adding its
	// generic read-error envelope, preventing a second response for the same
	// limit failure.
	IsTooLarge(error) bool
	// RectifyCount reports whether status-400 response evidence qualifies for
	// thinking-signature repair on this model. The count caller consults it
	// after reading the body and permits a single rebuilt request on that
	// provider.
	RectifyCount(context.Context, []byte, string) bool
	// FilterCountRetry removes incompatible thinking evidence from the logical
	// count body for the selected model. Callers rebuild and sign that returned
	// representation, rather than filtering the already serialized first wire
	// body.
	FilterCountRetry([]byte, string) []byte
	// CountError writes a structured count-protocol error using the chosen
	// status and message.
	CountError(int, string, string)
	// CountSuccess writes the upstream count bytes and status with protocol
	// headers selected by the adapter. Passthrough may retain an upstream
	// Content-Type; ordinary counting uses the adapter's JSON response path.
	CountSuccess(int, map[string][]string, []byte, bool)
	// SetError stores source status, diagnostic summary, and optional
	// count-response body detail.
	SetError(int, string, string)
	// UnsupportedCount recognizes response evidence for an unsupported upstream
	// count endpoint. True causes the caller to write a handled 404 and return
	// nil; unrelated errors continue through provider health and display
	// policy.
	UnsupportedCount(int, []byte) bool
	// CountHealth evaluates the captured status, headers, body, and count model
	// for provider policy. Generic and Failover precede ordinary client
	// display; same-provider permission is transferred only with the resulting
	// failover signal.
	CountHealth(context.Context, int, map[string][]string, []byte, string) ErrorDecision
	// CountFailover constructs a retry signal with the supplied response
	// evidence and same-provider permission. Response ownership belongs to the
	// reader, and the request loop separately enforces provider-switch limits
	// and output eligibility.
	CountFailover(int, map[string][]string, []byte, bool) error
	// CountURL returns the prepared count endpoint with query and fragment text
	// removed for diagnostics. Call it after BuildCount has retained a usable
	// request; sanitizing userinfo or path credentials remains the adapter's
	// obligation.
	CountURL() string
	// Observe submits count-request failure observations to the current HTTP
	// diagnostics adapter.
	Observe(Notice)
	// Sanitize masks recognized query credential values in count diagnostics.
	// It recognizes key, client_secret, access_token, and refresh_token
	// patterns while retaining other supplied message content, so it is not
	// general body redaction.
	Sanitize(string) string
	// Log writes prepared count diagnostic text through the selected platform
	// logger.
	Log(string)
	// Truncate bounds valid UTF-8 diagnostic text by input bytes and retains
	// complete characters. A nonpositive limit returns empty; count handling
	// selects its configured or default body-detail budget before calling it.
	Truncate(string, int) string
	// TruncateBytes limits body bytes and escapes CR/LF for single-line count
	// logs. A nonpositive limit uses the adapter default, and escaping can
	// expand the resulting string beyond the input-byte limit.
	TruncateBytes([]byte, int) string
}
