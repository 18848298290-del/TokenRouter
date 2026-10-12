package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
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

const (
	defaultLiveMaxSessionDuration = time.Hour

	chatGPTLiveCallsURL        = "https://chatgpt.com/backend-api/codex/realtime/calls?intent=quicksilver&architecture=avas"
	chatGPTLiveSidebandBaseURL = "wss://chatgpt.com/backend-api/codex"
)

// OpenAILiveOptions configures session duration and observer retry intervals.
type OpenAILiveOptions struct {
	MaxSessionDuration    time.Duration
	ObserverRetryInterval time.Duration
}

// LiveProviderSelection selects providers at session creation and revalidates routing models.
type LiveProviderSelection interface {
	SelectProviderWithSchedulerForCapability(context.Context, *int64, string, string, string, map[int64]struct{}, egress.OpenAIUpstreamTransport, providercore.OpenAIEndpointCapability, bool, bool, ...string) (*gatewayprovider.SelectionResult, scheduler.PlatformDecision, error)
	ResolveOpenAIWSRoutingModelForProvider(context.Context, *int64, *gatewayprovider.ExecutionProvider, string, providercore.OpenAIEndpointCapability) (string, error)
}

// OpenAILiveExecutor binds Live dependencies and observer registration to shared sessions, leases, and
// transport.
type OpenAILiveExecutor struct {
	Options             OpenAILiveOptions
	Requests            *OpenAIRequests
	Selection           LiveProviderSelection
	Routes              *gatewayprovider.RoutePlanner
	Store               session.LiveCallStore
	Leases              scheduler.LiveConcurrencyCache
	Usage               *completion.Recorder
	Dialer              openai.WSClientDialer
	Attestation         liveattestation.Provider
	AttestationCipher   identity.SecretEncryptor
	Background          func(string, func()) bool
	liveObserverMu      sync.Mutex
	liveObserverStopped bool
	liveObserverCancels map[string]context.CancelFunc
	liveObserverWG      sync.WaitGroup
}

func liveSidebandReadError(err error) error {
	if coderws.CloseStatus(err) == coderws.StatusNormalClosure {
		return session.ErrLiveCallNotFound
	}
	return err
}

func liveOptionalID(value int64) *int64 {
	if value <= 0 {
		return nil
	}
	result := value
	return &result
}

// liveOptionalString returns a trimmed model-trace value or nil when empty.
func liveOptionalString(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func (s *OpenAILiveExecutor) liveStore() (session.LiveCallStore, error) {
	if s == nil || s.Store == nil {
		return nil, session.ErrLiveUnavailable
	}
	return s.Store, nil
}

func (s *OpenAILiveExecutor) liveConcurrencyCache() (scheduler.LiveConcurrencyCache, error) {
	if s == nil || s.Leases == nil {
		return nil, session.ErrLiveUnavailable
	}
	return s.Leases, nil
}

func (s *OpenAILiveExecutor) liveMaxSessionDuration() time.Duration {
	if s != nil && s.Options.MaxSessionDuration > 0 {
		return s.Options.MaxSessionDuration
	}
	return defaultLiveMaxSessionDuration
}

// Create runs Live session creation with provider selection and connection adapters.
func (s *OpenAILiveExecutor) Create(ctx context.Context, request *session.LiveCallRequest, identity session.LiveCallIdentity, userMaxConcurrency int) (*gatewaylive.Created, error) {
	ports := &liveCreatePorts{service: s}
	created, err := gatewaylive.NewCreator(s.liveRuntime(), ports, s.liveMaxSessionDuration()).Create(ctx, request, identity, userMaxConcurrency)
	if err != nil {
		return nil, err
	}
	return created, nil
}

func (s *OpenAILiveExecutor) shouldFailoverLiveCreateError(err error) bool {
	var upstreamErr *forwardcore.UpstreamFailoverError
	if !errors.As(err, &upstreamErr) {
		// Credential and transport errors can be specific to the current provider or proxy.
		return true
	}
	return gatewayprovider.ShouldFailoverOpenAIResponse(
		upstreamErr.StatusCode,
		"",
		upstreamErr.ResponseBody,
	)
}

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

func logLiveCreateStageFailure(ctx context.Context, providerID int64, stage string, err error) {
	logging.FromContext(ctx).Warn(
		"OpenAI Live 创建阶段失败",
		zap.Int64("provider_id", providerID),
		zap.String("stage", stage),
		zap.String("error_type", fmt.Sprintf("%T", err)),
	)
}

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

	logging.FromContext(ctx).Warn(
		"OpenAI Live 上游拒绝请求",
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

// applyLiveUpstreamRouting applies routed User-Agent, originator, and Live identity headers.
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
	headers.Set(openai.LiveAttestationHeader, attestation)
	s.applyLiveUpstreamRouting(ctx, provider, headers, tlsRouterMatch)
	return headers, nil
}

// liveSidebandProvider loads the session-bound provider and checks Live capability.
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

// dialLiveSidebandForProvider opens a control connection using an already-validated session provider.
func (s *OpenAILiveExecutor) dialLiveSidebandForProvider(ctx context.Context, record *session.LiveCallRecord, provider *gatewayprovider.ExecutionProvider) (openai.LiveFrameConn, error) {
	tlsRouterMatch := s.matchLiveTLSFingerprintRouter(provider, record.UserAgent)
	headers, err := s.liveSidebandHeaders(ctx, provider, record, tlsRouterMatch)
	if err != nil {
		return nil, err
	}
	tlsProfile, _ := s.Requests.WSTLSProfile(provider, tlsRouterMatch)
	return openai.DialLiveSideband(ctx, s.Dialer, chatGPTLiveSidebandBaseURL, record.CallID, headers, liveProviderProxyURL(provider), tlsProfile)
}

// matchLiveTLSFingerprintRouter selects TLS routing using the User-Agent recorded at session creation.
func (s *OpenAILiveExecutor) matchLiveTLSFingerprintRouter(provider *gatewayprovider.ExecutionProvider, userAgent string) egress.TLSFingerprintRouterMatchResult {
	if s == nil || s.Requests.Routers == nil || provider == nil || provider.View().GetTLSFingerprintRouterID() <= 0 {
		return egress.TLSFingerprintRouterMatchResult{}
	}
	return s.Requests.Routers.MatchUserAgent(provider.View().GetTLSFingerprintRouterID(), userAgent)
}

// liveClientPolicyResult checks OAuth client restrictions using the session identity headers.
func (s *OpenAILiveExecutor) liveClientPolicyResult(
	ctx context.Context,
	provider *gatewayprovider.ExecutionProvider,
	identity session.LiveCallIdentity,
	tlsRouterMatch egress.TLSFingerprintRouterMatchResult,
) providercore.CodexClientRestrictionDetectionResult {
	if ctx == nil {
		ctx = context.Background()
	}
	request := (&http.Request{Method: http.MethodPost, Header: make(http.Header)}).WithContext(ctx)
	request.Header.Set("User-Agent", identity.UserAgent)
	request.Header.Set("originator", identity.Originator)
	return s.Requests.DetectClient(&gin.Context{Request: request}, provider, tlsRouterMatch)
}

// Lookup delegates session identity validation to the Live runtime.
func (s *OpenAILiveExecutor) Lookup(ctx context.Context, callID string, identity session.LiveCallIdentity) (*session.LiveCallRecord, error) {
	return s.liveRuntime().Lookup(ctx, callID, identity)
}

// rewriteLiveSidebandClientPayload applies Live session model rewriting.
func (s *OpenAILiveExecutor) rewriteLiveSidebandClientPayload(ctx context.Context, record *session.LiveCallRecord, provider *gatewayprovider.ExecutionProvider, payload []byte) ([]byte, string, []string, error) {
	if provider == nil {
		return payload, "", nil, nil
	}
	return gatewaylive.RewriteClientPayload(ctx, record, liveModelResolver{service: s, provider: provider}, payload)
}

// liveRuntime binds the application-owned stores, leases, and observer registry to the Live service.
func (s *OpenAILiveExecutor) liveRuntime() *gatewaylive.Service {
	return gatewaylive.New(livePorts{service: s}, s.Options.ObserverRetryInterval, openai.WSMessageReadLimitBytes)
}

// Proxy adapts the HTTP WebSocket to the frame interface used by gateway/live.
func (s *OpenAILiveExecutor) Proxy(ctx context.Context, record *session.LiveCallRecord, downstream *coderws.Conn) error {
	if downstream == nil {
		return session.ErrLiveCallNotFound
	}
	return s.liveRuntime().ProxyLiveSideband(ctx, record, liveDownstreamFrames{downstream})
}

func (s *OpenAILiveExecutor) observeLiveCall(record *session.LiveCallRecord) {
	s.liveRuntime().Observe(record)
}

// liveObserverState returns access to the shared observer registry.
func (s *OpenAILiveExecutor) liveObserverState() gatewaylive.ObserverState {
	return gatewaylive.ObserverState{Mutex: &s.liveObserverMu, Stopped: &s.liveObserverStopped, Cancels: &s.liveObserverCancels, Wait: &s.liveObserverWG}
}

func (s *OpenAILiveExecutor) beginLiveObserver(owner string) (context.Context, func(), bool) {
	return s.liveObserverState().Begin(owner)
}

// StopLiveObservers cancels registered local observers and waits for them through the Live observer state.
func (s *OpenAILiveExecutor) StopLiveObservers(ctx context.Context) error {
	return s.liveObserverState().Stop(ctx)
}

func liveProviderProxyURL(value *gatewayprovider.ExecutionProvider) string {
	if value == nil || value.Record.ProxyID == nil || value.Record.Proxy == nil {
		return ""
	}
	return value.Record.Proxy.URL()
}
