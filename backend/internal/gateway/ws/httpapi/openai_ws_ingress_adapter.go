package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	coderws "github.com/coder/websocket"

	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	gatewayws "github.com/TokenFlux/TokenRouter/internal/gateway/ws"
	wire "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
	openaiws "github.com/TokenFlux/TokenRouter/internal/upstream/openai/ws"
)

// Pooled Responses sessions retain continuation state and replay input while
// concrete HTTP adapters own credential, header and frame operations. The
// gateway core chooses when to reconnect or rebuild input; these adapters
// expose the selected pool lease and upstream payload algorithms with their
// distinct empty, unsafe and shared-storage results.

// wsIngressAdapter dispatches bound HTTP operations for the gateway session loop.
// Callers initialize every callback used by their selected bridge or pool path;
// the methods call those callbacks directly while the core owns recovery order.
type wsIngressAdapter struct {
	RecoverAcquireFn func(context.Context) error                                                                                                        // Agent Identity recovery operation
	ParseFn          func(raw []byte, replace bool, turn int) (gatewayws.ClientPayload, error)                                                          // client turn normalization operation
	ReadFn           func() ([]byte, error)                                                                                                             // next client frame read operation
	GenerateHashFn   func(body []byte) string                                                                                                           // session affinity hash operation
	StoreDisabledFn  func(body []byte) bool                                                                                                             // disabled-store affinity policy operation
	ShouldBridgeFn   func(payload gatewayws.ClientPayload) bool                                                                                         // HTTP bridge transport selection
	InvalidFn        func(groupID int64, hash string) map[string]struct{}                                                                               // rejected ciphertext digest lookup
	StripFn          func(body []byte, digests map[string]struct{}, key string, id int64, turn int) ([]byte, int)                                       // current ciphertext stripping operation
	BridgeIdentityFn func(body []byte, model string, turn int) (string, error)                                                                          // Grok per-turn cache identity resolver
	BridgeFn         func(ctx context.Context, input gatewayws.ClientPayload, body []byte, identity string, turn int) (*gatewayws.ForwardResult, error) // one HTTP bridge turn executor
	SetStateFn       func(state, hash string)                                                                                                           // request turn-state publication operation
	OpenPoolFn       func(payload gatewayws.ClientPayload) error                                                                                        // initial handshake and pool preparation
	AcquireFn        func(turn int, preferred string, force bool, allowRecovery bool) (gatewayws.ConnLease, error)                                      // provider pool lease acquisition
	RelayFn          func(turn int, lease gatewayws.ConnLease, payload gatewayws.ClientPayload) (*gatewayws.ForwardResult, error)                       // one pooled turn frame relay
	PinFn            func(id int64, conn string) bool                                                                                                   // disabled-store connection reservation operation
	UnpinFn          func(id int64, conn string)                                                                                                        // connection reservation cleanup operation
	HeaderFn         func(key string) string                                                                                                            // current handshake diagnostic header reader
	UpdateHeadersFn  func(payload gatewayws.ClientPayload, state string)                                                                                // next acquisition handshake update
	BindOwnerFn      func(ctx context.Context, responseID string)                                                                                       // pooled response ownership observation
}

// wsIngressLease exposes pool lease operations to the core while the adapter retains transport access.
type wsIngressLease struct {
	lease *openaiws.WSConnLease /* retained pool transport lease */
}

// wsReplayCodec calls upstream payload algorithms; the gateway chooses when and how often to replay.
type wsReplayCodec struct{}

// wsStrictTurn retains the non-input comparison state used by later continuations.
type wsStrictTurn struct {
	// state is constructed from the prior accepted payload by BuildStrict. Keep
	// mutability with the upstream implementation; Keep uses this retained comparison.
	state *openai.WSPreviousTurnStrictState
}

// Parse normalizes raw bytes for the supplied local turn using the bound
// parser. The replace flag selects user prompt rewriting, and the returned
// ClientPayload keeps the request forms needed by core forwarding and replay.
func (p *wsIngressAdapter) Parse(raw []byte, replace bool, turn int) (gatewayws.ClientPayload, error) {
	return p.ParseFn(raw, replace, turn)
}

// ReadClient returns the next client payload using the bound session read
// operation.
func (p *wsIngressAdapter) ReadClient() ([]byte, error) { return p.ReadFn() }

// GenerateHash derives the session affinity hash using the bound request
// operation.
func (p *wsIngressAdapter) GenerateHash(body []byte) string { return p.GenerateHashFn(body) }

// StoreDisabled reports whether the bound provider's request uses
// disabled-store affinity rules.
func (p *wsIngressAdapter) StoreDisabled(body []byte) bool { return p.StoreDisabledFn(body) }

// ShouldBridge reports whether this normalized payload selects the bound HTTP
// bridge path.
func (p *wsIngressAdapter) ShouldBridge(payload gatewayws.ClientPayload) bool {
	return p.ShouldBridgeFn(payload)
}

// InvalidDigests returns cached ciphertext rejections for the supplied group
// and session hash.
func (p *wsIngressAdapter) InvalidDigests(groupID int64, hash string) map[string]struct{} {
	return p.InvalidFn(groupID, hash)
}

// StripInvalid removes cached rejected ciphertext through the bound lineage
// operation and returns its removed count. The remaining parameters identify
// the diagnostic key, provider and turn; ingress publishes replacement bytes
// only after positive removal.
func (p *wsIngressAdapter) StripInvalid(body []byte, digests map[string]struct{}, key string, id int64, turn int) ([]byte, int) {
	return p.StripFn(body, digests, key, id, turn)
}

// BridgeIdentity derives cache identity from the replay body and routing model
// for this logical turn. The bound implementation selects the turn context;
// errors stop the core before submitting an HTTP bridge request.
func (p *wsIngressAdapter) BridgeIdentity(body []byte, model string, turn int) (string, error) {
	return p.BridgeIdentityFn(body, model, turn)
}

// Bridge executes one HTTP turn with the supplied replay-expanded body and
// cache identity. The bound operation returns gateway result metadata and any
// failure; the core attaches current-turn replay when replacement-provider
// selection is needed.
func (p *wsIngressAdapter) Bridge(ctx context.Context, input gatewayws.ClientPayload, body []byte, identity string, turn int) (*gatewayws.ForwardResult, error) {
	return p.BridgeFn(ctx, input, body, identity, turn)
}

// SetRequestState publishes retained turn state and the session hash through
// the bound request updater.
func (p *wsIngressAdapter) SetRequestState(state, hash string) { p.SetStateFn(state, hash) }

// OpenPool prepares handshake headers and the shared pool from the first
// normalized payload. The core calls it for pooled execution before leasing
// connections, and a preparation failure returns before acquisition.
func (p *wsIngressAdapter) OpenPool(payload gatewayws.ClientPayload) error {
	return p.OpenPoolFn(payload)
}

// Acquire obtains a provider lease using optional preferred connection
// identity for this turn. Force requires that continuation connection;
// allowRecovery permits the bound acquire operation to return a
// credential-recovery marker. The core owns eventual lease release.
func (p *wsIngressAdapter) Acquire(turn int, preferred string, force bool, allowRecovery bool) (gatewayws.ConnLease, error) {
	return p.AcquireFn(turn, preferred, force, allowRecovery)
}

// Relay executes this turn's payload over its held lease and returns
// observations or classified failure. The lease belongs to ingress, which
// chooses recovery and release after the bound relay completes.
func (p *wsIngressAdapter) Relay(turn int, lease gatewayws.ConnLease, payload gatewayws.ClientPayload) (*gatewayws.ForwardResult, error) {
	return p.RelayFn(turn, lease, payload)
}

// PinConn reserves the supplied connection for a disabled-store session and
// reports whether pinning succeeded. The core remembers successful
// reservations and passes that exact provider and connection identity to
// UnpinConn during cleanup.
func (p *wsIngressAdapter) PinConn(id int64, conn string) bool { return p.PinFn(id, conn) }

// UnpinConn removes the bound provider connection reservation before ingress
// releases its lease. The caller uses the same provider and connection
// identifiers retained after successful PinConn; the bound pool operation owns
// synchronization.
func (p *wsIngressAdapter) UnpinConn(id int64, conn string) { p.UnpinFn(id, conn) }

// Header returns the current handshake value used by bound continuation
// diagnostics.
func (p *wsIngressAdapter) Header(key string) string { return p.HeaderFn(key) }

// UpdateHeaders prepares future acquisition headers from this normalized
// payload and retained turn state. Its bound operation can retain prior
// headers after a failed rebuild while still refreshing the current routing
// hint.
func (p *wsIngressAdapter) UpdateHeaders(payload gatewayws.ClientPayload, state string) {
	p.UpdateHeadersFn(payload, state)
}

// BindOwner observes response-ID ownership after a pooled relay returns a
// result without an error. That result can contain a failed response terminal;
// the binding records its identity and treats cache or isolation errors as
// best effort after relay completion.
func (p *wsIngressAdapter) BindOwner(ctx context.Context, responseID string) {
	p.BindOwnerFn(ctx, responseID)
}

// IsDisconnect recognizes client transport errors accepted as a clean ingress
// read exit.
func (*wsIngressAdapter) IsDisconnect(err error) bool {
	return gatewayprovider.IsOpenAIWSClientDisconnectError(err)
}

// IsFailover recognizes a nonnil upstream failover marker in the error chain.
func (*wsIngressAdapter) IsFailover(err error) bool {
	var value *forwardcore.UpstreamFailoverError
	return errors.As(err, &value) && value != nil
}

// CloseError constructs an HTTP WS close instruction retaining the supplied
// status and cause.
func (*wsIngressAdapter) CloseError(status int, reason string, err error) error {
	return gatewayhttp.NewOpenAIWSClientCloseError(coderws.StatusCode(status), reason, err)
}

// Log sends an ingress session diagnostic to the configured WS information
// logger.
func (*wsIngressAdapter) Log(message string) { gatewayprovider.LogOpenAIWSModeInfo("%s", message) }

// NormalizeLog trims diagnostic text and applies the provider helper's fixed
// substring substitutions. Blank input returns "-"; nonblank input rewrites
// lowercase error, fallback, warning and failed to their diagnostic labels.
func (*wsIngressAdapter) NormalizeLog(value string) string {
	return gatewayprovider.NormalizeOpenAIWSLogValue(value)
}

// TruncateLog normalizes diagnostic text and retains at most limit source bytes
// before adding an ellipsis. Nonpositive limits keep normalized text intact;
// byte truncation can split a UTF-8 encoding.
func (*wsIngressAdapter) TruncateLog(value string, limit int) string {
	return gatewayprovider.TruncateOpenAIWSLogValue(value, limit)
}

// SummarizeClose returns status and reason labels from an upstream frame read
// error.
func (*wsIngressAdapter) SummarizeClose(err error) (string, string) {
	return gatewayprovider.SummarizeOpenAIWSReadCloseError(err)
}

// BindWarning logs a failed response-provider affinity binding with its group
// and identifiers.
func (*wsIngressAdapter) BindWarning(group, provider int64, response string, err error) {
	gatewayprovider.LogOpenAIWSBindResponseProviderWarn(group, provider, response, err)
}

// ConnID returns the connection identifier retained by the wrapped pool lease.
// A nonnil wrapper with an absent underlying lease or connection returns empty;
// ingress uses a live identifier when pinning or requesting forced
// continuation.
func (l *wsIngressLease) ConnID() string { return l.lease.ConnID() }

// MarkBroken evicts the wrapped connection immediately while retaining lease
// ownership until Release. The pool ignores absent or already released leases;
// callers still invoke Release after invalidation to finish the acquisition's
// cleanup.
func (l *wsIngressLease) MarkBroken() { l.lease.MarkBroken() }

// Release returns the wrapped lease to pool ownership after this session's
// use. The pool coordinates repeated release calls, capacity notification and
// connection retirement; callers stop performing frame reads or writes after
// release.
func (l *wsIngressLease) Release() { l.lease.Release() }

// SupportsIdlePingWithoutReader reports whether the wrapped live connection
// supports an idle health probe. Inactive leases return false through the pool
// guard, letting the core skip a ping that needs an active frame reader.
func (l *wsIngressLease) SupportsIdlePingWithoutReader() bool {
	return l.lease.SupportsIdlePingWithoutReader()
}

// PingWithTimeout probes the active lease using the supplied budget. Released
// connections are rejected and a nonpositive timeout selects the pool's fixed
// two-second health budget; its Background-derived ping context is independent
// of ingress control cancellation.
func (l *wsIngressLease) PingWithTimeout(timeout time.Duration) error {
	return l.lease.PingWithTimeout(timeout)
}

// Extract returns normalized input items and their presence flag through the
// upstream parser. Absent input returns false; array extraction can share
// request bytes, so retained replay bodies stay immutable while used across
// turns.
func (wsReplayCodec) Extract(body []byte) ([]json.RawMessage, bool, error) {
	return openai.OpenAIWSExtractNormalizedInputSequence(body)
}

// BuildFromItems combines previous and current input according to continuation
// presence and replay availability. Upstream sanitizes historical unpaired
// tool calls and recognizes prefix-extended input; returned item bytes remain
// shared and must be kept immutable while retained.
func (wsReplayCodec) BuildFromItems(a []json.RawMessage, b bool, c []json.RawMessage, d, e bool) ([]json.RawMessage, bool) {
	return openai.BuildOpenAIWSReplayInputSequenceFromItems(a, b, c, d, e)
}

// Build parses the current payload before combining input with prior replay
// and continuation state. Extraction errors return before a usable sequence;
// successful items can share request and history bytes, so callers keep
// retained input immutable.
func (wsReplayCodec) Build(a []json.RawMessage, b bool, c []byte, d bool) ([]json.RawMessage, bool, error) {
	return openai.BuildOpenAIWSReplayInputSequence(a, b, c, d)
}

// SetInput writes full replay input when its presence flag is true. False
// returns supplied bytes unchanged; a present nil sequence encodes as an empty
// array, preserving present-empty input across protocol conversion.
func (wsReplayCodec) SetInput(a []byte, b []json.RawMessage, c bool) ([]byte, error) {
	return openai.SetOpenAIWSPayloadInputSequence(a, b, c)
}

// RetryPayload builds a self-contained current turn for provider replacement
// from pre-scope bytes and retained input. Missing input presence or uncovered
// tool outputs return false. Eligible bytes remove previous_response_id and
// restore a nonblank client model before the entry loop retries them.
func (wsReplayCodec) RetryPayload(a []byte, b []json.RawMessage, c bool, d string) ([]byte, bool, error) {
	return openai.BuildOpenAIWSCurrentTurnRetryPayload(a, b, c, d)
}

// Combine appends output item references to history through the upstream
// replay helper. Empty output can return the original history slice; nonempty
// output allocates slice storage while sharing item bytes, which callers keep
// immutable.
func (wsReplayCodec) Combine(a, b []json.RawMessage) []json.RawMessage {
	return openai.CombineOpenAIWSReplayItems(a, b)
}

// HasOutput reports whether the request contains protocol tool-call output
// input items.
func (wsReplayCodec) HasOutput(body []byte) bool {
	return openai.OpenAIWSRawPayloadHasToolCallOutput(body)
}

// ItemsHaveOutput reports whether replay items contain protocol tool-call
// output entries.
func (wsReplayCodec) ItemsHaveOutput(items []json.RawMessage) bool {
	return openai.OpenAIWSRawItemsHasFunctionCallOutput(items)
}

// ItemsCoverOutput reports whether every replay tool output has matching
// actual call context. Empty items, no outputs, missing call IDs or missing
// matching calls return false; item references alone do not supply call
// context.
func (wsReplayCodec) ItemsCoverOutput(items []json.RawMessage) bool {
	return openai.OpenAIWSRawItemsHaveToolCallContextForOutputs(items)
}

// DropPrevious removes previous_response_id using the upstream raw-payload
// compatibility helper. Its changed flag reports deletion; the core retains
// supplied bytes when deletion fails or required replay input cannot be
// installed afterward.
func (wsReplayCodec) DropPrevious(body []byte) ([]byte, bool, error) {
	return openai.DropPreviousResponseIDFromRawPayload(body)
}

// SetPrevious writes the supplied continuation identifier into request bytes
// through the upstream helper. The core publishes the updated payload and
// derived ID after successful mutation; errors retain the prior request.
func (wsReplayCodec) SetPrevious(body []byte, id string) ([]byte, error) {
	return openai.SetPreviousResponseIDToRawPayload(body, id)
}

// BuildStrict constructs the upstream non-input state for later continuation
// comparison. Empty body returns nil with no error; other builder errors
// return nil with the error, letting ingress retain full request bytes for raw
// comparison.
func (wsReplayCodec) BuildStrict(body []byte) (gatewayws.PreviousTurn, error) {
	state, err := openai.BuildOpenAIWSIngressPreviousTurnStrictState(body)
	if err != nil || state == nil {
		return nil, err
	}
	return wsStrictTurn{state}, nil
}

// KeepPrevious compares raw previous and current requests through the upstream
// strict continuation rule. Tool output retains its anchor first; ordinary
// continuation requires matching last response identity and normalized
// non-input fields. Parsing errors accompany a false result for the core's
// error handling.
func (wsReplayCodec) KeepPrevious(a, b []byte, c string, d bool) (bool, string, error) {
	return openai.ShouldKeepIngressPreviousResponseID(a, b, c, d)
}

// StripItems removes replay entries whose encrypted-content digests are
// rejected for the session. The returned count identifies removed entries;
// ingress applies the same digest set to current request and retained
// histories before further prefix comparison.
func (wsReplayCodec) StripItems(a []json.RawMessage, b map[string]struct{}) ([]json.RawMessage, int) {
	return openai.StripOpenAIInvalidEncryptedContentFromReplayItems(a, b)
}

// ShouldInfer reports whether a later disabled-store turn needs the known last
// response anchor for tool output. The upstream rule requires an absent
// supplied ID and missing actual call context; its decision leaves payload
// mutation to ingress.
func (wsReplayCodec) ShouldInfer(a bool, b int, c wire.ToolContinuationSignals, d, e string) bool {
	return openai.ShouldInferIngressFunctionCallOutputPreviousResponseID(a, b, c, d, e)
}

// ClassifyPrevious labels the continuation identifier using the shared OpenAI
// protocol parser.
func (wsReplayCodec) ClassifyPrevious(id string) string {
	return wire.ClassifyOpenAIPreviousResponseIDKind(id)
}

// Keep compares the current request against this saved strict non-input state
// and last response ID. Tool output retains its continuation first; changed
// ordinary fields return a reason, while parsing failure remains an error for
// ingress.
func (s wsStrictTurn) Keep(a []byte, b string, c bool) (bool, string, error) {
	return openai.ShouldKeepIngressPreviousResponseIDWithStrictState(s.state, a, b, c)
}

// wsIngressHooks converts optional HTTP execution hooks into the gateway turn
// callbacks. Nil hooks return nil. AfterTurn converts result representation
// while retaining request and replay metadata; payload bytes and hook
// references keep their existing sharing and lifetime.
func wsIngressHooks(h *gatewayws.OpenAIIngressHooks) *gatewayws.IngressHooks {
	if h == nil {
		return nil
	}
	out := &gatewayws.IngressHooks{TurnStarted: h.TurnStarted, BeforeTurn: h.BeforeTurn, BeforeRequest: h.BeforeRequest}
	if h.AfterTurn != nil {
		out.AfterTurn = func(c gatewayws.TurnCapture) {
			h.AfterTurn(gatewayws.OpenAITurnCapture{Turn: c.Turn, StartedAt: c.StartedAt, RequestBody: c.RequestBody, OriginalModel: c.OriginalModel, PreviousResponseID: c.PreviousResponseID, Result: gatewayprovider.ForwardResultFromWS(c.Result), Err: c.Err, PayloadSource: c.PayloadSource})
		}
	}
	return out
}

// Debug sends detailed ingress diagnostics through the configured WS debug
// logger.
func (*wsIngressAdapter) Debug(message string) { gatewayprovider.LogOpenAIWSModeDebug("%s", message) }

// RecoverAcquire invokes the bound credential repair under the supplied
// control context. Ingress marks its single recovery attempt before calling
// this method and retries acquisition only after a successful return.
func (p *wsIngressAdapter) RecoverAcquire(ctx context.Context) error { return p.RecoverAcquireFn(ctx) }
