package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/gateway/completion"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewaylive "github.com/TokenFlux/TokenRouter/internal/gateway/live"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai/liveattestation"
)

// openai_live.go connects Live creation and sideband control to saved sessions
// and shared services.
// Live control reconnects load the saved provider and resolve current
// credentials while reusing the encrypted attestation issued during creation.
// Session storage, concurrency leases and WebSocket transport are
// application-owned; short-lived Live service facades delegate creation,
// controller handling and expiry to gateway/live.
//
// Every facade references the executor's same observer manager, so local
// shutdown applies across previously constructed facades. Atomic publication
// selects one manager when registration and shutdown first run concurrently
// on a zero-value executor. ObserverManager owns admission and waiting. The
// transport adapters distinguish ordinary close from reconnectable errors, and
// creation diagnostics retain bounded status and error fields without
// serializing credential-bearing request options.

const (
	// defaultLiveMaxSessionDuration bounds a call when no positive session duration
	// is configured; Creator uses it for the logical record expiry time.
	defaultLiveMaxSessionDuration = time.Hour

	// chatGPTLiveCallsURL and chatGPTLiveSidebandBaseURL select creation and control
	// transports for the same Codex Live service. The call ID is appended at dialing.
	chatGPTLiveCallsURL        = "https://chatgpt.com/backend-api/codex/realtime/calls?intent=quicksilver&architecture=avas"
	chatGPTLiveSidebandBaseURL = "wss://chatgpt.com/backend-api/codex"
)

// OpenAILiveOptions supplies logical session duration and observer
// store-retry timing.
type OpenAILiveOptions struct {
	// MaxSessionDuration supplies logical call lifetime. liveMaxSessionDuration
	// selects the one-hour default when this configured value is nonpositive.
	MaxSessionDuration time.Duration
	// ObserverRetryInterval controls retries after observer store-read failures.
	// The controller/dial retry helper uses its separate fixed one-second delay.
	ObserverRetryInterval time.Duration
}

// LiveProviderSelection supplies Live candidate admission and selected-target
// model qualification.
type LiveProviderSelection interface {
	// SelectProviderWithSchedulerForCapability returns a selected target with
	// ordinary provider admission evidence. Live creation requires an acquired
	// candidate and usable cleanup; rejected or absent admission is presented as
	// concurrency failure before upstream creation.
	SelectProviderWithSchedulerForCapability(context.Context, *int64, string, string, string, map[int64]struct{}, egress.OpenAIUpstreamTransport, providercore.OpenAIEndpointCapability, bool, bool, ...string) (*gatewayprovider.SelectionResult, scheduler.PlatformDecision, error)
	// ResolveOpenAIWSRoutingModelForProvider qualifies a concrete model for the
	// selected provider and Live capability. It returns the group routing name
	// or a qualification error; provider-specific mapping is performed
	// separately afterward.
	ResolveOpenAIWSRoutingModelForProvider(context.Context, *int64, *gatewayprovider.ExecutionProvider, string, providercore.OpenAIEndpointCapability) (string, error)
}

// OpenAILiveExecutor binds Live transport and shared application services
// with local observer state.
type OpenAILiveExecutor struct {
	Options   OpenAILiveOptions             // configured Live execution timing
	Requests  *OpenAIRequests               // shared credentials and request transport
	Selection LiveProviderSelection         // shared Live provider qualification
	Routes    *gatewayprovider.RoutePlanner // group model trace planning
	// Store is optional at assembly; liveStore returns unavailable when absent.
	// Creation and controller paths need a usable call store before proceeding.
	Store session.LiveCallStore
	// Leases supplies persistent session accounting. Missing binding causes
	// creation to return unavailable before selecting and creating an upstream call.
	Leases scheduler.LiveConcurrencyCache
	// Usage records zero-cost Live observations when present. A nil recorder
	// skips that write while the runtime still handles call and lease finalization.
	Usage  *completion.Recorder
	Dialer openai.WSClientDialer // shared upstream WebSocket dialer
	// Attestation supplies the creation-time plaintext header. A missing provider
	// produces a typed unavailable error before any upstream call is opened.
	Attestation liveattestation.Provider
	// AttestationCipher protects the generated header for saved sideband reuse.
	// Creation and reconnect require this binding. The production Live cipher
	// derives its key from the JWT secret and rejects another configured secret.
	AttestationCipher identity.SecretEncryptor
	// Background schedules the initial observer through application task tracking.
	// liveCreatePorts.Observe invokes it after saving the created call record.
	Background func(string, func()) bool
	// liveObservers publishes one manager on first registration or shutdown.
	// Every runtime facade uses that instance throughout the executor's lifetime.
	liveObservers atomic.Pointer[gatewaylive.ObserverManager]
}

// liveSidebandReadError converts a normal upstream close to the session-ended
// sentinel. Other errors, including nil, remain unchanged, letting
// gateway/live separate finalized sessions from control connections eligible
// for observer recovery.
func liveSidebandReadError(err error) error {
	// A normal upstream control close is terminal for Live accounting, so expose
	// the call-ended sentinel that controller finalization recognizes before reconnecting.
	if coderws.CloseStatus(err) == coderws.StatusNormalClosure {
		return session.ErrLiveCallNotFound
	}
	return err
}

// liveOptionalID copies a positive identifier into a new optional scalar
// pointer. Nonpositive input returns nil, matching usage-record fields where
// absent team, group or subscription identifiers should be omitted.
func liveOptionalID(value int64) *int64 {
	if value <= 0 {
		return nil
	}
	result := value
	return &result
}

// liveOptionalString returns a new pointer to trimmed model-trace text when
// present. Blank input returns nil, so usage fields represent missing
// upstream or mapping evidence as absent optional values.
func liveOptionalString(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

// liveStore returns the bound call store for Live runtime operations. A nil
// executor or missing store returns ErrLiveUnavailable, allowing creation and
// controller handling to report absent storage before using it.
func (s *OpenAILiveExecutor) liveStore() (session.LiveCallStore, error) {
	if s == nil || s.Store == nil {
		return nil, session.ErrLiveUnavailable
	}
	return s.Store, nil
}

// liveConcurrencyCache returns the shared persistent Live lease cache for
// runtime admission. A nil executor or missing cache returns
// ErrLiveUnavailable, keeping session creation from proceeding with untracked
// user and provider capacity.
func (s *OpenAILiveExecutor) liveConcurrencyCache() (scheduler.LiveConcurrencyCache, error) {
	if s == nil || s.Leases == nil {
		return nil, session.ErrLiveUnavailable
	}
	return s.Leases, nil
}

// liveMaxSessionDuration returns the configured positive logical session
// lifetime. Nil executors and nonpositive values use one hour; Creator
// derives call expiry and its separate storage lifetime from the returned
// duration.
func (s *OpenAILiveExecutor) liveMaxSessionDuration() time.Duration {
	if s != nil && s.Options.MaxSessionDuration > 0 {
		return s.Options.MaxSessionDuration
	}
	return defaultLiveMaxSessionDuration
}

// Create delegates validated session creation through selected-target and
// attestation adapters. Callers provide a usable executor and hold ordinary
// user admission; gateway/live acquires its separate Live lease before
// returning a stored session.
func (s *OpenAILiveExecutor) Create(ctx context.Context, request *session.LiveCallRequest, identity session.LiveCallIdentity, userMaxConcurrency int) (*gatewaylive.Created, error) {
	ports := &liveCreatePorts{service: s}
	created, err := gatewaylive.NewCreator(s.liveRuntime(), ports, s.liveMaxSessionDuration()).Create(ctx, request, identity, userMaxConcurrency)
	if err != nil {
		return nil, err
	}
	return created, nil
}

// shouldFailoverLiveCreateError selects recovery for a failed provider
// creation attempt. Classified HTTP failures use the existing OpenAI response
// policy, while another credential or transport error permits selecting
// another provider after lease cleanup.
func (s *OpenAILiveExecutor) shouldFailoverLiveCreateError(err error) bool {
	var upstreamErr *forwardcore.UpstreamFailoverError
	if !errors.As(err, &upstreamErr) {
		// Non-HTTP creation errors permit a different provider after the runtime
		// releases its failed Live lease, preserving the normal candidate retry path.
		return true
	}
	return gatewayprovider.ShouldFailoverOpenAIResponse(
		upstreamErr.StatusCode,
		"",
		upstreamErr.ResponseBody,
	)
}

// createUpstreamLiveCall opens one provider-specific call through native
// OpenAI request handling. The request carries already resolved session
// settings and a plaintext creation attestation; routing, credentials,
// account headers and TLS callbacks are resolved for the supplied target.
//
// Native creation invokes stage diagnostics for preparation errors and the
// bound HTTP failure constructor for status rejection. That constructor
// clones response headers into the classified failure before native body
// cleanup. Successful results expose the returned SDP, call ID and Location
// to Creator, which installs saved ownership separately. Callers retain
// provider and request storage throughout this synchronous exchange.
func (s *OpenAILiveExecutor) createUpstreamLiveCall(ctx context.Context, provider *gatewayprovider.ExecutionProvider, request *session.LiveCallRequest, attestation string, tlsRouterMatch egress.TLSFingerprintRouterMatchResult) (*gatewaylive.Created, error) {
	result, err := openai.CreateLiveCall(ctx, request, openai.LiveCreateOptions{
		URL:         chatGPTLiveCallsURL,
		Attestation: attestation,
		Token: func(ctx context.Context) (string, error) {
			token, _, err := s.Requests.Credentials.Resolve(ctx, gatewayprovider.ExecutionRecord(provider))
			return token, err
		},
		Authentication: func(ctx context.Context, token string) (http.Header, error) {
			return s.Requests.Identity.Headers(ctx, provider, token)
		},
		ProviderHeaders: func(ctx context.Context, headers http.Header) error {
			return gatewayprovider.CredentialChatGPTHeaders(ctx, s.Requests.Providers, headers, provider)
		},
		Routing: func(ctx context.Context, headers http.Header) {
			s.applyLiveUpstreamRouting(ctx, provider, headers, tlsRouterMatch)
		},
		Do: func(request *http.Request) (*http.Response, error) {
			return s.Requests.Transport.DoWithTLS(request, liveProviderProxyURL(provider), provider.Record.ID, provider.Record.Concurrency, s.Requests.TLSProfile(provider, tlsRouterMatch))
		},
		StageFailure: func(stage string, err error) { logLiveCreateStageFailure(ctx, provider.Record.ID, stage, err) },
		HTTPFailure: func(status int, headers http.Header, body []byte) error {
			logLiveUpstreamFailure(ctx, provider.Record.ID, status, headers, body)
			return &forwardcore.UpstreamFailoverError{StatusCode: status, ResponseBody: body, ResponseHeaders: headers.Clone()}
		},
	})
	if err != nil {
		return nil, err
	}
	return &gatewaylive.Created{SDP: result.SDP, CallID: result.CallID, Location: result.Location}, nil
}

// logLiveCreateStageFailure records provider, stage and error type for
// creation diagnostics. The error value is used for its concrete type label,
// so this stage log does not print its potentially credential-bearing
// message.
func logLiveCreateStageFailure(ctx context.Context, providerID int64, stage string, err error) {
	logging.FromContext(ctx).Warn(
		"OpenAI Live creation failed",
		zap.Int64("provider_id", providerID),
		zap.String("stage", stage),
		zap.String("error_type", fmt.Sprintf("%T", err)),
	)
}

// logLiveUpstreamFailure records bounded HTTP rejection fields and selected
// upstream headers. Error type, code and message prefer the nested error
// object, then top-level fields; missing message finally checks detail.
//
// Every emitted text field passes the shared log normalizer and byte truncator,
// with a longer budget for message text. This retains provider/status
// correlation and request ID diagnostics while restricting each field's size.
// The response body supplies structured diagnostic fields rather than a full
// body dump, and callers pass captured headers before native response
// cleanup.
func logLiveUpstreamFailure(
	ctx context.Context,
	providerID int64,
	statusCode int,
	headers http.Header,
	body []byte,
) {
	errorType := strings.TrimSpace(gjson.GetBytes(body, "error.type").String())
	errorCode := strings.TrimSpace(gjson.GetBytes(body, "error.code").String())
	errorMessage := strings.TrimSpace(gjson.GetBytes(body, "error.message").String())
	if errorType == "" {
		errorType = strings.TrimSpace(gjson.GetBytes(body, "type").String())
	}
	if errorCode == "" {
		errorCode = strings.TrimSpace(gjson.GetBytes(body, "code").String())
	}
	if errorMessage == "" {
		errorMessage = strings.TrimSpace(gjson.GetBytes(body, "message").String())
	}
	if errorMessage == "" {
		errorMessage = strings.TrimSpace(gjson.GetBytes(body, "detail").String())
	}

	// Limit normalized source prefixes to 120 bytes and message text to 300 bytes
	// before adding the ellipsis, keeping each rejection diagnostic field bounded.
	logging.FromContext(ctx).Warn(
		"OpenAI Live upstream rejected request",
		zap.Int64("provider_id", providerID),
		zap.Int("upstream_status_code", statusCode),
		zap.String("upstream_error_type", gatewayprovider.TruncateOpenAIWSLogValue(errorType, 120)),
		zap.String("upstream_error_code", gatewayprovider.TruncateOpenAIWSLogValue(errorCode, 120)),
		zap.String("upstream_error_message", gatewayprovider.TruncateOpenAIWSLogValue(errorMessage, 300)),
		zap.String("upstream_content_type", gatewayprovider.TruncateOpenAIWSLogValue(headers.Get("Content-Type"), 120)),
		zap.String("upstream_server", gatewayprovider.TruncateOpenAIWSLogValue(headers.Get("Server"), 120)),
		zap.String("upstream_cf_mitigated", gatewayprovider.TruncateOpenAIWSLogValue(headers.Get("Cf-Mitigated"), 120)),
		zap.String("upstream_cf_ray", gatewayprovider.TruncateOpenAIWSLogValue(headers.Get("Cf-Ray"), 120)),
		zap.String("upstream_request_id", gatewayprovider.TruncateOpenAIWSLogValue(headers.Get("X-Request-Id"), 120)),
	)
}

// applyLiveUpstreamRouting applies a nonblank matched originator, routed
// User-Agent and fixed Live identity headers in order. The native identity
// helper then establishes Codex headers and session/thread IDs, with Live
// alpha and beta-header rules.
func (s *OpenAILiveExecutor) applyLiveUpstreamRouting(
	ctx context.Context,
	provider *gatewayprovider.ExecutionProvider,
	headers http.Header,
	routerMatch egress.TLSFingerprintRouterMatchResult,
) {
	if routerMatch.Matched {
		if originator := strings.TrimSpace(routerMatch.UpstreamOriginator); originator != "" {
			headers.Set("originator", originator)
		}
	}
	s.Requests.ApplyUserAgentHeader(ctx, nil, provider, headers, false, routerMatch)
	openai.ApplyLiveUpstreamIdentityHeaders(headers)
}

// liveSidebandHeaders prepares current provider authentication and the saved
// attestation for a control connection. Credential, identity or decryption
// errors return before headers are usable. Successful headers receive the
// same Live routing and identity rules as creation.
func (s *OpenAILiveExecutor) liveSidebandHeaders(
	ctx context.Context,
	provider *gatewayprovider.ExecutionProvider,
	record *session.LiveCallRecord,
	tlsRouterMatch egress.TLSFingerprintRouterMatchResult,
) (http.Header, error) {
	token, _, err := s.Requests.Credentials.Resolve(ctx, gatewayprovider.ExecutionRecord(provider))
	if err != nil {
		return nil, err
	}
	headers, err := s.Requests.Identity.Headers(ctx, provider, token)
	if err != nil {
		return nil, err
	}
	if err := gatewayprovider.CredentialChatGPTHeaders(ctx, s.Requests.Providers, headers, provider); err != nil {
		return nil, err
	}
	attestation, err := s.decryptLiveAttestation(record)
	if err != nil {
		return nil, err
	}
	// Reuse the issued creation header from encrypted session storage, keeping a
	// reconnect on that call's attestation instead of generating another device token.
	headers.Set(openai.LiveAttestationHeader, attestation)
	s.applyLiveUpstreamRouting(ctx, provider, headers, tlsRouterMatch)
	return headers, nil
}

// liveSidebandProvider loads the call's saved provider and checks its current
// Live endpoint capability. Nil records return not found; absent or
// no-longer-supported providers return unavailable, while repository errors
// remain available to controller recovery.
func (s *OpenAILiveExecutor) liveSidebandProvider(ctx context.Context, record *session.LiveCallRecord) (*gatewayprovider.ExecutionProvider, error) {
	if record == nil {
		return nil, session.ErrLiveCallNotFound
	}
	provider, err := s.Requests.Providers.GetByID(ctx, record.ProviderID)
	if err != nil {
		return nil, err
	}
	if provider == nil || !provideradapter.SupportsOpenAIEndpoint(gatewayprovider.ExecutionProtocolRecord(provider), providercore.OpenAIEndpointCapabilityLive) {
		return nil, session.ErrLiveUnavailable
	}
	return provider, nil
}

// dialLiveSidebandForProvider opens control transport for an already
// qualified saved provider. It matches the creation-time User-Agent, prepares
// current authentication and decrypted attestation, then passes proxy and
// HTTP/1-compatible TLS selection to native dialing; callers close successful
// connections.
func (s *OpenAILiveExecutor) dialLiveSidebandForProvider(ctx context.Context, record *session.LiveCallRecord, provider *gatewayprovider.ExecutionProvider) (openai.LiveFrameConn, error) {
	tlsRouterMatch := s.matchLiveTLSFingerprintRouter(provider, record.UserAgent)
	headers, err := s.liveSidebandHeaders(ctx, provider, record, tlsRouterMatch)
	if err != nil {
		return nil, err
	}
	tlsProfile, _ := s.Requests.WSTLSProfile(provider, tlsRouterMatch)
	return openai.DialLiveSideband(ctx, s.Dialer, chatGPTLiveSidebandBaseURL, record.CallID, headers, liveProviderProxyURL(provider), tlsProfile)
}

// matchLiveTLSFingerprintRouter matches routing against the saved
// creation-time User-Agent. Missing executor, router service, provider or
// positive router ID returns an unmatched result, letting the shared request
// configuration select its ordinary TLS policy.
func (s *OpenAILiveExecutor) matchLiveTLSFingerprintRouter(provider *gatewayprovider.ExecutionProvider, userAgent string) egress.TLSFingerprintRouterMatchResult {
	if s == nil || s.Requests.Routers == nil || provider == nil || provider.View().GetTLSFingerprintRouterID() <= 0 {
		return egress.TLSFingerprintRouterMatchResult{}
	}
	return s.Requests.Routers.MatchUserAgent(provider.View().GetTLSFingerprintRouterID(), userAgent)
}

// liveClientPolicyResult checks current provider client restrictions using
// captured User-Agent and originator. A nil context uses Background; the
// synthetic POST request exists for identity detection and performs no
// network exchange itself.
func (s *OpenAILiveExecutor) liveClientPolicyResult(
	ctx context.Context,
	provider *gatewayprovider.ExecutionProvider,
	identity session.LiveCallIdentity,
	tlsRouterMatch egress.TLSFingerprintRouterMatchResult,
) providercore.CodexClientRestrictionDetectionResult {
	if ctx == nil {
		// Identity detection reads a synthetic request context. Background supplies
		// a usable context when direct callers provide none for this local policy check.
		ctx = context.Background()
	}
	request := (&http.Request{Method: http.MethodPost, Header: make(http.Header)}).WithContext(ctx)
	request.Header.Set("User-Agent", identity.UserAgent)
	request.Header.Set("originator", identity.Originator)
	return s.Requests.DetectClient(&gin.Context{Request: request}, provider, tlsRouterMatch)
}

// Lookup delegates saved call, Key, funding subject and group validation to
// Live storage handling. Missing or closed calls and identity mismatch retain
// their runtime errors; expiry is handled by controller execution, rather
// than this lookup.
func (s *OpenAILiveExecutor) Lookup(ctx context.Context, callID string, identity session.LiveCallIdentity) (*session.LiveCallRecord, error) {
	return s.liveRuntime().Lookup(ctx, callID, identity)
}

// rewriteLiveSidebandClientPayload applies saved Key rules and current
// group/provider model resolution to a control payload. A nil provider
// returns original bytes and empty observations; the Live rewriter retains
// original bytes on validation or mapping errors.
func (s *OpenAILiveExecutor) rewriteLiveSidebandClientPayload(ctx context.Context, record *session.LiveCallRecord, provider *gatewayprovider.ExecutionProvider, payload []byte) ([]byte, string, []string, error) {
	if provider == nil {
		return payload, "", nil, nil
	}
	return gatewaylive.RewriteClientPayload(ctx, record, liveModelResolver{service: s, provider: provider}, payload)
}

// liveRuntime constructs a lightweight Live service facade referencing
// application stores, leases and this executor's observer registry.
// Construction starts no transport or observer; it passes current retry
// timing and the shared client message limit.
func (s *OpenAILiveExecutor) liveRuntime() *gatewaylive.Service {
	return gatewaylive.New(livePorts{service: s}, s.Options.ObserverRetryInterval, openai.WSMessageReadLimitBytes)
}

// Proxy adapts an accepted client WebSocket to Live control execution for the
// saved call. Nil downstream returns not found; HTTP retains client teardown
// while gateway/live manages upstream controller ownership and observer
// recovery.
func (s *OpenAILiveExecutor) Proxy(ctx context.Context, record *session.LiveCallRecord, downstream *coderws.Conn) error {
	if downstream == nil {
		return session.ErrLiveCallNotFound
	}
	return s.liveRuntime().ProxyLiveSideband(ctx, record, liveDownstreamFrames{downstream})
}

// observeLiveCall delegates saved-call monitoring through the shared Live
// runtime facade.
func (s *OpenAILiveExecutor) observeLiveCall(record *session.LiveCallRecord) {
	s.liveRuntime().Observe(record)
}

// beginLiveObserver registers owner in the shared local observer registry and
// returns a background context with cleanup. Callers use an owner unique among
// active registrations and call accepted cleanup exactly once. Registration
// after stopping returns false with no usable observer context.
func (s *OpenAILiveExecutor) beginLiveObserver(owner string) (context.Context, func(), bool) {
	return s.liveObserverManager().Begin(owner)
}

// StopLiveObservers rejects later local registration, cancels current
// observers and waits within the supplied context budget. Callers provide a
// nonnil executor and context; stopping local observation leaves stored
// remote call lifetime to the Live runtime.
func (s *OpenAILiveExecutor) StopLiveObservers(ctx context.Context) error {
	return s.liveObserverManager().Stop(ctx)
}

// liveObserverManager returns this executor's shared local observer manager.
// Callers provide a nonnil executor; its zero value obtains a manager when
// registration or shutdown first needs one.
//
// Concurrent first callers can allocate separate candidates, but the successful
// atomic publication selects the instance every caller uses. The published
// pointer remains installed after shutdown, so runtime facades created later
// observe closed admission and cannot start a separate observer registry.
func (s *OpenAILiveExecutor) liveObserverManager() *gatewaylive.ObserverManager {
	if manager := s.liveObservers.Load(); manager != nil {
		return manager
	}
	manager := &gatewaylive.ObserverManager{}
	// Publish a candidate before using it; a concurrent winner owns the manager
	// that registration and shutdown must both consult for this executor.
	if s.liveObservers.CompareAndSwap(nil, manager) {
		return manager
	}
	return s.liveObservers.Load()
}

// liveProviderProxyURL returns the saved provider's proxy URL when both proxy
// ID and hydrated proxy are present. Missing provider or either proxy field
// returns an empty URL for the shared transport configuration.
func liveProviderProxyURL(value *gatewayprovider.ExecutionProvider) string {
	if value == nil || value.Record.ProxyID == nil || value.Record.Proxy == nil {
		return ""
	}
	return value.Record.Proxy.URL()
}
