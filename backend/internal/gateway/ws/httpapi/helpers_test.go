package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/egress"
	egressprovider "github.com/TokenFlux/TokenRouter/internal/egress/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/gateway/promptpolicy"
	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/provider/selection"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
	gatewaytestkit "github.com/TokenFlux/TokenRouter/internal/gateway/testkit"
	"github.com/TokenFlux/TokenRouter/internal/gateway/tierpolicy"
	gatewayws "github.com/TokenFlux/TokenRouter/internal/gateway/ws"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/tlsfingerprint"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/protocol/wirejson"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/settings"
	upstreamcore "github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/anthropic"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
	openaiws "github.com/TokenFlux/TokenRouter/internal/upstream/openai/ws"
)

var handlerStructuredLogCaptureMu sync.Mutex

type auxiliaryHTTPRecorder struct {
	// Reject sends when the request context is canceled.
	checkContext bool
	lastReq      *http.Request
	lastBody     []byte
	lastProxyURL string
	requests     []*http.Request
	bodies       [][]byte

	resp      *http.Response
	responses []*http.Response
	err       error

	lastTLSProfile *tlsfingerprint.Profile
}

// grokModelStateProviderRepo records model-level Grok health writes.
type grokModelStateProviderRepo struct {
	gatewayadapter.ExecutionProviderStore

	modelRateLimitCalls []grokModelRateLimitCall
}

// grokModelRateLimitCall captures one model rate-limit write.
type grokModelRateLimitCall struct {
	providerID int64
	scope      string
	resetAt    time.Time
	reason     string
}

// auxiliaryFixtureInputs supplies dependencies for auxiliary-request tests.
type auxiliaryFixtureInputs struct {
	allowHTTP     bool
	transport     httpclient.UpstreamTransport
	profiles      *egressprovider.TLSProfiles
	store         gatewayadapter.ExecutionProviderStore
	credentials   *provider.OpenAIExecutionCredentials
	observer      *provideradapter.UpstreamHealth
	authorization *provider.OpenAIAuthorization
}

// wsFixtureOptions contains transport-test options.
type wsFixtureOptions struct {
	WS      OpenAIWSOptions
	Pool    openaiws.WSPoolOptions
	Request gatewayhttp.OpenAIRequestOptions
	Output  gatewayhttp.OpenAIResponseOptions
}

// wsFixtureInputs supplies execution dependencies and I/O substitutes.
type wsFixtureInputs struct {
	options   *wsFixtureOptions
	providers gatewayadapter.ExecutionProviderStore
	cache     session.GatewayCache
	health    *provideradapter.UpstreamHealth
	transport httpclient.UpstreamTransport
	dialer    openai.WSClientDialer
	pool      *openaiws.WSConnPool
	state     session.OpenAIWSStateStore
	prompts   *promptpolicy.Service
	readers   *gatewayadapter.RuntimeReaders
	corrector *openai.CodexToolCorrector
}

// wsExecutionFixture combines HTTP and WS executors sharing connection and session state.
type wsExecutionFixture struct {
	*OpenAIWebSocketExecutor
	Responses *gatewayhttp.OpenAIResponsesExecutor
	Text      *gatewayhttp.OpenAITextExecutor
	choices   *selection.Compatible
	options   *wsFixtureOptions
}

type openAIWSCaptureDialer struct {
	mu          sync.Mutex
	conn        *openAIWSCaptureConn
	lastHeaders http.Header
	handshake   http.Header
	dialCount   int
}

type openAIWSCaptureConn struct {
	mu         sync.Mutex
	readDelays []time.Duration
	events     [][]byte
	lastWrite  map[string]any
	writes     []map[string]any
	closed     bool
}

type openAIWSFakeConn struct {
	mu      sync.Mutex
	closed  bool
	payload [][]byte
}

type httpUpstreamSequenceRecorder struct {
	mu     sync.Mutex
	bodies [][]byte
	reqs   []*http.Request

	responses []*http.Response
	errs      []error
	callCount int
}

type handlerInMemoryLogSink struct {
	mu     sync.Mutex
	events []*logging.LogEvent
}

type grokFixtureProviders struct {
	gatewaytestkit.HealthStoreBase
	providersByID map[int64]*gatewayadapter.ExecutionProvider
	getByIDCalls  int
}

type grokQuotaProviderRepo struct {
	*grokFixtureProviders
	updates               map[int64]map[string]any
	updateCalls           int
	rateLimitedCalls      int
	lastRateLimitedID     int64
	lastRateLimitResetAt  time.Time
	tempUnschedCalls      int
	lastTempUnschedID     int64
	lastTempUnschedUntil  time.Time
	lastTempUnschedReason string
	recoveryClearCalls    int
	recoveryObservedAt    time.Time
	recoveryObservedReset time.Time
	recoveryClearResult   bool
}

type openAIStream403ProviderRepo struct {
	gatewayadapter.ExecutionProviderStore

	setErrorCalls int
}

type transientCooldownProviderRepo struct {
	gatewayadapter.ExecutionProviderStore
}

func (u *auxiliaryHTTPRecorder) Do(req *http.Request, proxyURL string, providerID int64, providerConcurrency int) (*http.Response, error) {
	if u.checkContext && req.Context().Err() != nil {
		return nil, req.Context().Err()
	}
	u.lastReq = req
	u.lastProxyURL = proxyURL
	if req != nil && req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		u.lastBody = b
		u.bodies = append(u.bodies, append([]byte(nil), b...))
		_ = req.Body.Close()
		req.Body = io.NopCloser(bytes.NewReader(b))
	}
	u.requests = append(u.requests, req)
	if u.err != nil {
		return nil, u.err
	}
	if len(u.responses) > 0 {
		resp := u.responses[0]
		u.responses = u.responses[1:]
		return resp, nil
	}
	return u.resp, nil
}

func (u *auxiliaryHTTPRecorder) DoWithTLS(req *http.Request, proxyURL string, providerID int64, providerConcurrency int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	u.lastTLSProfile = profile
	return u.Do(req, proxyURL, providerID, providerConcurrency)
}

// SetModelRateLimit records model health writes for canonical-model-key assertions.
func (r *grokModelStateProviderRepo) SetModelRateLimit(_ context.Context, id int64, scope string, resetAt time.Time, reason ...string) error {
	call := grokModelRateLimitCall{providerID: id, scope: scope, resetAt: resetAt}
	if len(reason) > 0 {
		call.reason = reason[0]
	}
	r.modelRateLimitCalls = append(r.modelRateLimitCalls, call)
	return nil
}

// handleGrokProviderUpstreamError reduces the production decision to a boolean for test assertions.
func (s *wsExecutionFixture) handleGrokProviderUpstreamError(
	ctx context.Context,
	provider *gatewayadapter.ExecutionProvider,
	statusCode int,
	headers http.Header,
	responseBody []byte,
	requestedModel ...string,
) bool {
	return gatewayadapter.ApplyGrokExecutionHealth(ctx, s.Output.GrokHealth, provider, statusCode, headers, responseBody, "", requestedModel...).StopScheduling
}

func newAuxiliaryFixture(v auxiliaryFixtureInputs) *gatewayhttp.OpenAIAuxiliary {
	blocks := provider.NewRuntimeBlockState(time.Now)
	models := provider.NewModelTransientState(0)
	credentials := v.credentials
	if credentials == nil {
		credentials = &provider.OpenAIExecutionCredentials{}
	}
	if v.store != nil {
		credentials.Parent = func(ctx context.Context, id int64) (*provider.Record, error) {
			a, err := v.store.GetByID(ctx, id)
			return gatewayadapter.ExecutionRecord(a), err
		}
	}
	identity := gatewayadapter.NewExecutionAgentIdentity(&provider.OpenAITaskCoordinator{}, v.store, nil, nil)
	turns := &gatewayhttp.CodexTurnStateHeaders{Origins: session.NewCodexTurnOrigins(time.Now), TTL: func() time.Duration { return time.Hour }}
	requests := &gatewayhttp.OpenAIRequests{Options: gatewayhttp.OpenAIRequestOptions{URLPolicy: egress.OperatorURLPolicy{AllowInsecureHTTP: v.allowHTTP}}, Providers: v.store, Identity: identity, Credentials: credentials, Transport: v.transport, Profiles: v.profiles, Turns: turns, ClientPolicy: &provideradapter.OpenAIProbePolicy{Available: true, DefaultBrowserUserAgent: gateway.DefaultOpenAICodexUserAgent, Profiles: v.profiles}, Failure: &gatewayhttp.UpstreamTransportFailure{Health: &provideradapter.TransportHealth{Runtime: blocks}}}
	grok := &provideradapter.GrokHealth{Store: v.store, Health: v.observer, Runtime: blocks, ModelTransient: models, NormalizeModel: func(value *provider.Record, model string) string {
		return (gatewayadapter.ModelPolicy{Record: value}).NormalizeOpenAI(model)
	}}
	output := &gatewayhttp.OpenAIResponseOutput{Options: gatewayhttp.OpenAIResponseOptions{Configured: true, ReadLimit: 128 * 1024 * 1024}, Health: &provideradapter.OpenAIResponseHealth{Health: v.observer, Runtime: blocks, ModelTransient: models}, GrokHealth: grok, Headers: egress.CompileHeaderFilter(egress.ResponseHeaderOptions{})}
	return &gatewayhttp.OpenAIAuxiliary{Requests: requests, Output: output, Authorization: v.authorization, CodexUsage: &provideradapter.CodexUsageObserver{Store: v.store, Throttle: provider.NewWriteThrottle(30 * time.Second)}}
}

func wsFixturePoolOptions(options *wsFixtureOptions) *openaiws.WSPoolOptions {
	if options == nil {
		return nil
	}
	out := options.Pool

	out.DialTimeoutSeconds = options.WS.DialTimeoutSeconds
	out.PrewarmCooldownMS = options.WS.PrewarmCooldownMS
	return &out
}

func newOpenAIWSConnPool(options *wsFixtureOptions) *openaiws.WSConnPool {
	return openaiws.NewWSConnPool(wsFixturePoolOptions(options))
}

func newWSFixture(v wsFixtureInputs) *wsExecutionFixture {
	aux := newAuxiliaryFixture(auxiliaryFixtureInputs{transport: v.transport, store: v.providers, observer: v.health})
	requests, output := aux.Requests, aux.Output
	requests.Readers = v.readers
	output.Observer = v.health
	output.Corrector = v.corrector
	output.Headers = nil
	output.Options = gatewayhttp.OpenAIResponseOptions{ReadLimit: 128 * 1024 * 1024}
	if v.options != nil {
		requests.Options = v.options.Request
		output.Options = v.options.Output
		output.Options.Configured = true
		if output.Options.ReadLimit <= 0 {
			output.Options.ReadLimit = 128 * 1024 * 1024
		}
	}
	state := v.state
	if state == nil {
		state = session.NewOpenAIWSStateStore(v.cache, gatewayadapter.LogOpenAIWSModeInfo)
	}
	output.Responses = state
	output.ProxyCircuit = egress.NewProxyStreamCircuit(egress.DefaultProxyStreamCircuitSettings())
	history, _ := v.cache.(session.ReasoningContentCache)
	output.Reasoning = &session.ReasoningHistory{Cache: history, Warn: gatewayadapter.WarnReasoningCacheFailure}
	output.Redact = requests.Identity.Redact
	choicesOptions := selection.DefaultOptions()
	if v.options != nil {
		ws := v.options.WS
		if ws.StickyResponseIDTTLSeconds > 0 {
			choicesOptions.ResponseTTL = time.Duration(ws.StickyResponseIDTTLSeconds) * time.Second
		}
	}
	choices := selection.NewCompatible(selection.CompatibleDependencies{Reads: selection.Reads{Providers: v.providers}, Shared: selection.Shared{Cache: v.cache, Health: v.health}, Responses: state, RuntimeBlocks: output.Health.Runtime, ModelTransient: output.Health.ModelTransient, ProxyCircuit: output.ProxyCircuit}, choicesOptions)
	output.ResponseTTL = choices.OpenAIHTTPResponseStickyTTL
	requests.Turns.TTL = choices.SessionStickyTTL
	output.Turns = requests.Turns
	if v.readers != nil {
		output.TTFT = v.readers.Gateway.GetOpenAITTFTMode
	}
	credentials := gatewaytestkit.RequestCredentials(v.providers, requests.Credentials, nil, output.Health.Runtime)
	routes := gatewayadapter.GrokRoutes{Validate: grok.ValidateBaseURL}
	if v.options != nil {
		routes.Validate = v.options.Request.URLPolicy.Validate
	}
	if v.readers != nil {
		routes.DefaultMode = v.readers.Gateway.GetGrokDefaultBaseURLMode
	}
	requests.GrokRoutes = routes
	fast := &gatewayadapter.ExecutionFastPolicy{Readers: v.readers}
	connections := openaiws.NewOpenAIWSConnections(wsFixturePoolOptions(v.options), v.dialer, v.pool)
	grokExecutor := &gatewayhttp.GrokExecutor{Credentials: credentials, Transport: v.transport, Output: output, Health: output.GrokHealth, Routes: routes, Dialer: connections.Dialer(), FastPolicy: fast, Failure: requests.Failure}
	text := &gatewayhttp.OpenAITextExecutor{Requests: requests, Output: output, Grok: grokExecutor, Credentials: credentials, FastPolicy: fast, Continuation: &session.CompatResponses{TTL: choices.OpenAIHTTPResponseStickyTTL}, PromptCache: session.NewAnthropicPromptCache(time.Now), CodexUsage: aux.CodexUsage, ResponseTTL: choices.OpenAIHTTPResponseStickyTTL, Compact: &gatewayhttp.CompactExecutor{}}
	lineage := &gatewayhttp.OpenAIEncryptedLineage{Store: state, TTL: choices.SessionStickyTTL}
	imagePolicy := &gatewayadapter.ResponseImagePolicy{}
	var wsOptions *OpenAIWSOptions
	if v.options != nil {
		wsOptions = &v.options.WS
	}
	ws := &OpenAIWebSocketExecutor{OpenAIWSDependencies: OpenAIWSDependencies{Options: wsOptions, Connections: connections, Requests: requests, Output: output, Grok: grokExecutor, FastPolicy: fast, Prompts: v.prompts, Selection: choices, State: state, Lineage: lineage, ImageBridge: imagePolicy, Cache: v.cache}}
	responses := &gatewayhttp.OpenAIResponsesExecutor{Requests: requests, Output: output, Text: text, Grok: grokExecutor, Lineage: lineage, ImageBridge: imagePolicy}
	return &wsExecutionFixture{OpenAIWebSocketExecutor: ws, Responses: responses, Text: text, choices: choices, options: v.options}
}

// newExecutionReadersFixture builds readers sharing the supplied settings store.
func newExecutionReadersFixture(repo settings.Repository, _ *wsFixtureOptions) *gatewayadapter.RuntimeReaders {
	if repo != nil {
		repo = settings.New(repo)
	}
	return gatewaytestkit.RuntimeReaders(repo)
}

func newUpstreamHealthForTest(store gatewayadapter.ExecutionProviderStore, _ *wsFixtureOptions, cache provider.TempUnschedCache, options provider.HealthOptions, readers *gatewayadapter.RuntimeReaders) *provideradapter.UpstreamHealth {
	return gatewaytestkit.NewHealthObserver(gatewaytestkit.HealthInput{Store: store, Cache: cache, Options: options, Readers: readers})
}

// setWSFixtureHealth replaces the health observer used by the fixture's output paths.
func setWSFixtureHealth(s *wsExecutionFixture, observer *provideradapter.UpstreamHealth) {
	s.Output.Health.Health = observer
	s.Output.GrokHealth.Health = observer
	s.Output.Observer = observer
}

// newWSFastPolicy builds a settings reader containing the supplied Fast policy.
func newWSFastPolicy(t *testing.T, values *tierpolicy.OpenAIFastPolicySettings) *gatewayadapter.ExecutionFastPolicy {
	t.Helper()
	repo := &gatewaytestkit.FastPolicySettingsRepo{Values: map[string]string{}}
	if values != nil {
		raw, err := json.Marshal(values)
		require.NoError(t, err)
		repo.Values[gateway.SettingKeyOpenAIFastPolicySettings] = string(raw)
	}
	return &gatewayadapter.ExecutionFastPolicy{Readers: newExecutionReadersFixture(repo, nil)}
}

func openAIFastFilterPriorityPolicy() *tierpolicy.OpenAIFastPolicySettings {
	return &tierpolicy.OpenAIFastPolicySettings{Rules: []tierpolicy.OpenAIFastPolicyRule{{ServiceTier: tierpolicy.OpenAIFastTierPriority, Action: anthropic.BetaPolicyActionFilter, Scope: anthropic.BetaPolicyScopeAll, ModelWhitelist: []string{}, FallbackAction: anthropic.BetaPolicyActionPass}}}
}

// wsFixtureModelBlocked checks health state using the canonical scheduling model key.
func wsFixtureModelBlocked(s *wsExecutionFixture, value *gatewayadapter.ExecutionProvider, model string) bool {
	if value == nil {
		return false
	}
	key := provider.NormalizeTransientModel(gatewayadapter.ExecutionModelPolicy(value).CanonicalSchedulingModel(model))
	return s.Output.Health.ModelTransient.IsBlocked(value.Record.ID, key, time.Now())
}

func wsFixtureProviderBlocked(s *wsExecutionFixture, value *gatewayadapter.ExecutionProvider) bool {
	if value == nil || (value.Record.Platform != capability.PlatformOpenAI && value.Record.Platform != capability.PlatformGrok) {
		return false
	}
	return s.Output.Health.Runtime.Blocked(value.Record.ID, func() string { return provider.RefreshCredentialIdentity(gatewayadapter.ExecutionRecord(value)) })
}

// ProxyResponsesWebSocketFromClient sets up a saved provider and ingress protocol for direct executor tests.
func (s *wsExecutionFixture) ProxyResponsesWebSocketFromClient(ctx context.Context, c *gin.Context, conn *coderws.Conn, value *gatewayadapter.ExecutionProvider, token string, first []byte, hooks *gatewayws.OpenAIIngressHooks) error {
	key := gatewayhttp.GetExecutionAPIKey(c)
	if key == nil {
		key = &apikey.APIKey{}
		c.Set("api_key", key)
	}
	if key.Group == nil {
		key.Group = &routing.Group{AllowedProtocols: []capability.ProtocolID{capability.ProtocolResponsesWebSocket}}
	}
	copy := *value
	record := copy.Record
	if err := provider.NormalizeProviderProtocols(&record); err != nil {
		return err
	}
	copy.Record = record
	ctx = requeststate.WithClientProtocol(ctx, capability.ProtocolResponsesWebSocket)
	if key := gatewayhttp.GetExecutionAPIKey(c); key != nil && key.Group != nil {
		ctx = requeststate.WithGroup(ctx, key.Group)
	}
	return s.OpenAIWebSocketExecutor.ProxyResponsesWebSocketFromClient(ctx, c, conn, &copy, token, first, hooks)
}

func (d *openAIWSCaptureDialer) Dial(
	ctx context.Context,
	wsURL string,
	headers http.Header,
	proxyURL string,
	profile *tlsfingerprint.Profile,
) (openai.WSClientConn, int, http.Header, error) {
	_ = ctx
	_ = wsURL
	_ = proxyURL
	_ = profile
	d.mu.Lock()
	d.lastHeaders = upstreamcore.CloneHeader(headers)
	d.dialCount++
	respHeaders := upstreamcore.CloneHeader(d.handshake)
	d.mu.Unlock()
	return d.conn, 0, respHeaders, nil
}

func (d *openAIWSCaptureDialer) DialCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.dialCount
}

func (c *openAIWSCaptureConn) WriteJSON(ctx context.Context, value any) error {
	_ = ctx
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return openai.ErrWSConnClosed
	}
	switch payload := value.(type) {
	case map[string]any:
		c.lastWrite = cloneMapStringAny(payload)
		c.writes = append(c.writes, cloneMapStringAny(payload))
	case json.RawMessage:
		var parsed map[string]any
		if err := wirejson.DecodeUseNumber(payload, &parsed); err == nil {
			c.lastWrite = cloneMapStringAny(parsed)
			c.writes = append(c.writes, cloneMapStringAny(parsed))
		}
	case []byte:
		var parsed map[string]any
		if err := wirejson.DecodeUseNumber(payload, &parsed); err == nil {
			c.lastWrite = cloneMapStringAny(parsed)
			c.writes = append(c.writes, cloneMapStringAny(parsed))
		}
	}
	return nil
}

func (c *openAIWSCaptureConn) ReadMessage(ctx context.Context) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, openai.ErrWSConnClosed
	}
	if len(c.events) == 0 {
		c.mu.Unlock()
		return nil, io.EOF
	}
	delay := time.Duration(0)
	if len(c.readDelays) > 0 {
		delay = c.readDelays[0]
		c.readDelays = c.readDelays[1:]
	}
	event := c.events[0]
	c.events = c.events[1:]
	c.mu.Unlock()
	if delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return event, nil
}

func (c *openAIWSCaptureConn) ReadFrame(ctx context.Context) (coderws.MessageType, []byte, error) {
	payload, err := c.ReadMessage(ctx)
	if err != nil {
		return coderws.MessageText, nil, err
	}
	return coderws.MessageText, payload, nil
}

func (c *openAIWSCaptureConn) WriteFrame(ctx context.Context, _ coderws.MessageType, payload []byte) error {
	return c.WriteJSON(ctx, json.RawMessage(payload))
}

func (c *openAIWSCaptureConn) Ping(ctx context.Context) error {
	_ = ctx
	return nil
}

func (c *openAIWSCaptureConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}

func cloneMapStringAny(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	dst := make(map[string]any, len(src))
	maps.Copy(dst, src)
	return dst
}

func (c *openAIWSFakeConn) WriteJSON(ctx context.Context, value any) error {
	_ = ctx
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("closed")
	}
	c.payload = append(c.payload, []byte("ok"))
	_ = value
	return nil
}

func (c *openAIWSFakeConn) ReadMessage(ctx context.Context) ([]byte, error) {
	_ = ctx
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, errors.New("closed")
	}
	return []byte(`{"type":"response.completed","response":{"id":"resp_fake"}}`), nil
}

func (c *openAIWSFakeConn) Ping(ctx context.Context) error {
	_ = ctx
	return nil
}

func (c *openAIWSFakeConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}

func (u *httpUpstreamSequenceRecorder) Do(req *http.Request, proxyURL string, providerID int64, providerConcurrency int) (*http.Response, error) {
	u.mu.Lock()
	defer u.mu.Unlock()

	idx := u.callCount
	u.callCount++
	u.reqs = append(u.reqs, req)
	if req != nil && req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		u.bodies = append(u.bodies, b)
		_ = req.Body.Close()
		req.Body = io.NopCloser(bytes.NewReader(b))
	} else {
		u.bodies = append(u.bodies, nil)
	}
	if idx < len(u.errs) && u.errs[idx] != nil {
		return nil, u.errs[idx]
	}
	if idx < len(u.responses) {
		return u.responses[idx], nil
	}
	if len(u.responses) == 0 {
		return nil, nil
	}
	return u.responses[len(u.responses)-1], nil
}

func (u *httpUpstreamSequenceRecorder) DoWithTLS(req *http.Request, proxyURL string, providerID int64, providerConcurrency int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, providerID, providerConcurrency)
}

func (s *wsExecutionFixture) claimOpenAIWSSessionPreemptOwner(ctx context.Context, key openAIWSSessionPreemptKey, owner string) (string, bool) {
	return s.wsPreemption().Claim(ctx, gatewayws.PreemptKey{GroupID: key.groupID, APIKeyID: key.apiKeyID, SessionHash: key.sessionHash}, owner)
}

func (s *wsExecutionFixture) releaseOpenAIWSSessionPreemptOwner(ctx context.Context, key openAIWSSessionPreemptKey, owner string) {
	s.wsPreemption().Release(ctx, gatewayws.PreemptKey{GroupID: key.groupID, APIKeyID: key.apiKeyID, SessionHash: key.sessionHash}, owner)
}

func (s *handlerInMemoryLogSink) WriteLogEvent(event *logging.LogEvent) {
	if event == nil {
		return
	}
	cloned := *event
	if event.Fields != nil {
		cloned.Fields = make(map[string]any, len(event.Fields))
		maps.Copy(cloned.Fields, event.Fields)
	}
	s.mu.Lock()
	s.events = append(s.events, &cloned)
	s.mu.Unlock()
}

func (s *handlerInMemoryLogSink) ContainsMessageAtLevel(substr, level string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	wantLevel := strings.ToLower(strings.TrimSpace(level))
	for _, ev := range s.events {
		if ev == nil {
			continue
		}
		if strings.Contains(ev.Message, substr) && strings.ToLower(strings.TrimSpace(ev.Level)) == wantLevel {
			return true
		}
	}
	return false
}

func (s *handlerInMemoryLogSink) ContainsFieldValue(field, substr string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ev := range s.events {
		if ev == nil || ev.Fields == nil {
			continue
		}
		if v, ok := ev.Fields[field]; ok && strings.Contains(fmt.Sprint(v), substr) {
			return true
		}
	}
	return false
}

func (s *handlerInMemoryLogSink) FieldValueForMessage(message, field string) (any, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, event := range s.events {
		if event == nil || event.Message != message || event.Fields == nil {
			continue
		}
		if value, ok := event.Fields[field]; ok {
			return value, true
		}
	}
	return nil, false
}

func (s *handlerInMemoryLogSink) ContainsMessage(substr string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, event := range s.events {
		if event != nil && strings.Contains(event.Message, substr) {
			return true
		}
	}
	return false
}

func (r *grokFixtureProviders) GetByID(_ context.Context, id int64) (*gatewayadapter.ExecutionProvider, error) {
	r.getByIDCalls++
	if value, ok := r.providersByID[id]; ok {
		return value, nil
	}
	return nil, errors.New("provider not found")
}

func captureHandlerStructuredLog(t *testing.T) (*handlerInMemoryLogSink, func()) {
	t.Helper()
	handlerStructuredLogCaptureMu.Lock()

	err := logging.Init(logging.InitOptions{
		Level:       "debug",
		Format:      "json",
		ServiceName: "tokenrouter",
		Environment: "test",
		Output: logging.OutputOptions{
			ToStdout: true,
			ToFile:   false,
		},
		Sampling: logging.SamplingOptions{Enabled: false},
	})
	require.NoError(t, err)

	sink := &handlerInMemoryLogSink{}
	logging.SetSink(sink)
	return sink, func() {
		logging.SetSink(nil)
		handlerStructuredLogCaptureMu.Unlock()
	}
}

func (r *grokQuotaProviderRepo) UpdateExtra(_ context.Context, id int64, updates map[string]any) error {
	r.updateCalls++
	if r.updates == nil {
		r.updates = make(map[int64]map[string]any)
	}
	r.updates[id] = updates
	if r.grokFixtureProviders != nil {
		value := r.providersByID[id]
		if value != nil {
			if value.Record.Extra == nil {
				value.Record.Extra = make(map[string]any)
			}
			maps.Copy(value.Record.Extra, updates)
		}
	}

	return nil
}

func (r *grokQuotaProviderRepo) SetRateLimited(_ context.Context, id int64, resetAt time.Time) error {
	r.rateLimitedCalls++
	r.lastRateLimitedID = id
	r.lastRateLimitResetAt = resetAt
	return nil
}

func (r *grokQuotaProviderRepo) SetRateLimitedIfLater(ctx context.Context, id int64, resetAt time.Time) error {
	return r.SetRateLimited(ctx, id, resetAt)
}

func (r *grokQuotaProviderRepo) ClearRateLimitIfObserved(_ context.Context, _ int64, observedLimitedAt, observedResetAt time.Time) (bool, error) {
	r.recoveryClearCalls++
	r.recoveryObservedAt = observedLimitedAt
	r.recoveryObservedReset = observedResetAt
	return r.recoveryClearResult, nil
}

func (r *grokQuotaProviderRepo) SetTempUnschedulable(_ context.Context, id int64, until time.Time, reason string) error {
	r.tempUnschedCalls++
	r.lastTempUnschedID = id
	r.lastTempUnschedUntil = until
	r.lastTempUnschedReason = reason
	return nil
}

func (r *openAIStream403ProviderRepo) SetError(context.Context, int64, string) error {
	r.setErrorCalls++
	return nil
}

func (transientCooldownProviderRepo) SetOverloaded(context.Context, int64, time.Time) error {
	return nil
}
