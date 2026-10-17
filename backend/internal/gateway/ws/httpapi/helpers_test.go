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

// handlerStructuredLogCaptureMu serializes temporary process-wide log sink
// replacement until each fixture invokes the release returned by captureHandlerStructuredLog.
var handlerStructuredLogCaptureMu sync.Mutex

// auxiliaryHTTPRecorder records HTTP requests and supplies controlled
// auxiliary responses.
type auxiliaryHTTPRecorder struct {
	// lastReq, lastBody, lastProxyURL, requests and bodies record attempted sends.
	// Body reads are restored on the retained request so execution assertions can
	// read it again. This recorder is used sequentially and has no mutex.
	// checkContext makes Do reject an already canceled request before recording
	// its body. When enabled, callers supply a nonnil request for Context access.
	checkContext bool            // reject already canceled request sends
	lastReq      *http.Request   // last attempted HTTP request reference
	lastBody     []byte          // last consumed request body bytes
	lastProxyURL string          // last selected upstream proxy URL
	requests     []*http.Request // recorded attempted HTTP request references
	bodies       [][]byte        // recorded consumed request body bytes

	// err takes precedence over responses and resp after recording the send.
	// responses is consumed in order; resp is returned when that sequence is empty.
	resp      *http.Response   // default configured HTTP response
	responses []*http.Response // scripted HTTP response sequence
	err       error            // configured transport error result

	lastTLSProfile *tlsfingerprint.Profile // last supplied TLS fingerprint profile
}

// grokModelStateProviderRepo records model-scoped Grok cooldown writes for
// execution assertions.
type grokModelStateProviderRepo struct {
	gatewayadapter.ExecutionProviderStore // unused provider repository methods

	modelRateLimitCalls []grokModelRateLimitCall // recorded provider model cooldown writes
}

// grokModelRateLimitCall stores one observed provider model cooldown request.
type grokModelRateLimitCall struct {
	providerID int64     // affected upstream provider identifier
	scope      string    // provider model cooldown scope
	resetAt    time.Time // model cooldown expiration time
	reason     string    // first supplied cooldown reason
}

// auxiliaryFixtureInputs selects credentials, transport and health services
// for auxiliary fixtures.
type auxiliaryFixtureInputs struct {
	allowHTTP     bool                                  // permit insecure fixture endpoint URLs
	transport     httpclient.UpstreamTransport          // scripted upstream HTTP transport
	profiles      *egressprovider.TLSProfiles           // optional TLS profile resolver
	store         gatewayadapter.ExecutionProviderStore // fixture execution provider repository
	credentials   *provider.OpenAIExecutionCredentials  // optional request credential service
	observer      *provideradapter.UpstreamHealth       // optional upstream health observer
	authorization *provider.OpenAIAuthorization         // optional auxiliary authorization service
}

// wsFixtureOptions supplies separate WS, pool, request and output settings to
// fixtures.
type wsFixtureOptions struct {
	WS      OpenAIWSOptions                   // WebSocket turn execution settings
	Pool    openaiws.WSPoolOptions            // connection pool sizing settings
	Request gatewayhttp.OpenAIRequestOptions  // HTTP request preparation settings
	Output  gatewayhttp.OpenAIResponseOptions // HTTP response handling settings
}

// wsFixtureInputs supplies optional settings and shared execution services to
// WS fixtures.
type wsFixtureInputs struct {
	options   *wsFixtureOptions                     // optional fixture transport settings
	providers gatewayadapter.ExecutionProviderStore // execution provider repository fixture
	cache     session.GatewayCache                  // optional shared gateway cache
	health    *provideradapter.UpstreamHealth       // optional health effects observer
	transport httpclient.UpstreamTransport          // scripted upstream HTTP transport
	dialer    openai.WSClientDialer                 // optional upstream WS dialer
	pool      *openaiws.WSConnPool                  // optional retained WS pool
	state     session.OpenAIWSStateStore            // optional retained response affinity store
	prompts   *promptpolicy.Service                 // optional user prompt policy
	readers   *gatewayadapter.RuntimeReaders        // optional runtime settings readers
	corrector *openai.CodexToolCorrector            // optional Codex tool call corrector
}

// wsExecutionFixture shares request, response and session dependencies across
// HTTP and WS tests.
type wsExecutionFixture struct {
	*OpenAIWebSocketExecutor                                      // shared Responses WS execution facade
	Responses                *gatewayhttp.OpenAIResponsesExecutor // HTTP Responses execution facade
	Text                     *gatewayhttp.OpenAITextExecutor      // shared text execution facade
	choices                  *selection.Compatible                // bound compatible provider selector
	options                  *wsFixtureOptions                    // optional fixture transport settings
}

// openAIWSCaptureDialer records handshake inputs and returns a supplied shared
// connection.
type openAIWSCaptureDialer struct {
	// mu protects lastHeaders, handshake and dialCount during Dial and getters.
	// conn is configured before execution and every dial returns that same
	// connection; fixture consumers share its separately synchronized state.
	mu          sync.Mutex           // fixture state access mutex
	conn        *openAIWSCaptureConn // returned shared scripted connection
	lastHeaders http.Header          // copied most recent dial headers
	handshake   http.Header          // configured upstream response handshake headers
	dialCount   int                  // recorded handshake attempt count
}

// openAIWSCaptureConn records request maps and consumes scripted events with
// optional delays.
type openAIWSCaptureConn struct {
	// mu protects readDelays, events, lastWrite, writes and closed. ReadMessage
	// removes one event and delay together before unlocking for its wait;
	// captured request maps copy top-level entries and retain nested references.
	mu         sync.Mutex       // fixture state access mutex
	readDelays []time.Duration  // queued per-event delivery delays
	events     [][]byte         // queued scripted upstream event bytes
	lastWrite  map[string]any   // latest decoded request map
	writes     []map[string]any // recorded decoded request maps
	closed     bool             // transport has been closed
}

// openAIWSFakeConn records writes and repeats a fixed completed event until
// closed.
type openAIWSFakeConn struct {
	// mu protects closed and payload during fixture transport operations. Each
	// accepted write appends placeholder bytes, while reads return a fixed terminal
	// event until Close publishes the closed flag.
	mu      sync.Mutex // fixture state access mutex
	closed  bool       // transport has been closed
	payload [][]byte   // placeholder bytes for accepted writes
}

// httpUpstreamSequenceRecorder pairs recorded requests with scripted HTTP
// responses and errors.
type httpUpstreamSequenceRecorder struct {
	// mu protects bodies, reqs, callCount, errs and responses during each attempted
	// send and its scripted result selection. Later exchanges reuse the final
	// response after the configured response sequence is exhausted.
	mu     sync.Mutex      // fixture state access mutex
	bodies [][]byte        // recorded consumed request body bytes
	reqs   []*http.Request // recorded attempted HTTP request references

	responses []*http.Response // scripted HTTP response sequence
	errs      []error          // scripted per-attempt transport errors
	callCount int              // recorded HTTP exchange count
}

// handlerInMemoryLogSink retains structured log events for concurrent fixture
// assertions.
type handlerInMemoryLogSink struct {
	// mu protects events while the active process-wide sink is written and
	// queried. WriteLogEvent copies each event and its Fields map, but nested
	// field values remain shared with the supplied event.
	mu     sync.Mutex          // fixture state access mutex
	events []*logging.LogEvent // retained structured log event copies
}

// grokFixtureProviders returns configured execution providers and counts
// successful or missing lookups.
type grokFixtureProviders struct {
	gatewaytestkit.HealthStoreBase                                             // default no-op health repository methods
	providersByID                  map[int64]*gatewayadapter.ExecutionProvider // configured provider references by identifier
	getByIDCalls                   int                                         // recorded provider lookup count
}

// grokQuotaProviderRepo records quota snapshots, cooldowns and conditional
// recovery observations.
type grokQuotaProviderRepo struct {
	// recoveryClearCalls, recoveryObservedAt and recoveryObservedReset record
	// compare-clear inputs; recoveryClearResult supplies the returned match. Other
	// counters record health calls whose fixture callers serialize; they apply no
	// persistent or shared-cache comparison rules.
	*grokFixtureProviders // shared configured provider lookup fixture
	// updates retains each supplied map by reference. UpdateExtra also merges its
	// top-level values into configured providers, whose nested values remain shared.
	updates               map[int64]map[string]any // most recent quota update maps
	updateCalls           int                      // recorded Extra update count
	rateLimitedCalls      int                      // recorded provider cooldown write count
	lastRateLimitedID     int64                    // last provider cooldown target
	lastRateLimitResetAt  time.Time                // last provider cooldown expiry
	tempUnschedCalls      int                      // recorded temporary block write count
	lastTempUnschedID     int64                    // last temporary block target
	lastTempUnschedUntil  time.Time                // last temporary block expiry
	lastTempUnschedReason string                   // last temporary block reason
	recoveryClearCalls    int                      // recorded conditional recovery attempt count
	recoveryObservedAt    time.Time                // observed provider cooldown start
	recoveryObservedReset time.Time                // observed provider cooldown expiry
	recoveryClearResult   bool                     // configured conditional recovery match result
}

// openAIStream403ProviderRepo counts provider error writes for structured WS
// rejection tests.
type openAIStream403ProviderRepo struct {
	gatewayadapter.ExecutionProviderStore // unused provider repository methods

	setErrorCalls int // recorded provider error write count
}

// transientCooldownProviderRepo accepts overload writes used by transient
// model health fixtures.
type transientCooldownProviderRepo struct {
	gatewayadapter.ExecutionProviderStore // unused provider repository methods
}

// Do records an attempted HTTP exchange and returns the configured response or
// error. Optional cancellation checking runs before observation; callers
// supply a nonnil request when enabling it. Bodies are consumed, retained and
// restored on the request before response selection, so downstream assertions
// can inspect the sent bytes.
//
// Configured error wins over the queued response list, whose first value is
// removed per successful selection; an empty queue returns resp. Body read and
// close errors are ignored by this fixture, which records any partial bytes
// and replaces Body with a new reader. The recorder is intended for sequential
// use.
func (u *auxiliaryHTTPRecorder) Do(req *http.Request, proxyURL string, providerID int64, providerConcurrency int) (*http.Response, error) {
	if u.checkContext && req.Context().Err() != nil {
		return nil, req.Context().Err()
	}
	u.lastReq = req
	u.lastProxyURL = proxyURL
	if req != nil && req.Body != nil {
		// The scripted transport records partial bytes even if reading fails, then
		// reinstalls Body so later execution assertions can inspect the attempted payload.
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

// DoWithTLS stores the supplied TLS profile and delegates exchange recording
// to Do. Response sequencing and optional context checking retain Do's
// behavior; the profile pointer remains available for the fixture's routing
// assertions.
func (u *auxiliaryHTTPRecorder) DoWithTLS(req *http.Request, proxyURL string, providerID int64, providerConcurrency int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	u.lastTLSProfile = profile
	return u.Do(req, proxyURL, providerID, providerConcurrency)
}

// SetModelRateLimit records the provider, model scope and reset time for later
// assertions. Only the first optional reason is retained; it returns nil and
// models no persistence or cooldown enforcement.
func (r *grokModelStateProviderRepo) SetModelRateLimit(_ context.Context, id int64, scope string, resetAt time.Time, reason ...string) error {
	call := grokModelRateLimitCall{providerID: id, scope: scope, resetAt: resetAt}
	if len(reason) > 0 {
		call.reason = reason[0]
	}
	r.modelRateLimitCalls = append(r.modelRateLimitCalls, call)
	return nil
}

// handleGrokProviderUpstreamError applies the fixture's bound Grok health
// operations and returns StopScheduling. The boolean discards the richer
// policy decision for tests that assert provider blocking; optional model
// arguments reach the same production health classifier.
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

// newAuxiliaryFixture creates HTTP request and response services around
// supplied test dependencies. Missing credentials use an empty service; a
// nonnil provider store replaces Parent on the shared supplied credential
// service before execution. The fixture binds runtime blocks, model-transient
// state and turn origins with an optional health observer and a 128 MiB
// response read limit.
func newAuxiliaryFixture(v auxiliaryFixtureInputs) *gatewayhttp.OpenAIAuxiliary {
	blocks := provider.NewRuntimeBlockState(time.Now)
	models := provider.NewModelTransientState(0)
	credentials := v.credentials // optional request credential service
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

// wsFixturePoolOptions returns a copied Pool configuration or nil for missing
// fixture options. WS dial timeout and prewarm cooldown override the copied
// fields; callers can pass the returned value to pool construction without
// modifying the fixture's Pool value.
func wsFixturePoolOptions(options *wsFixtureOptions) *openaiws.WSPoolOptions {
	if options == nil {
		return nil
	}
	out := options.Pool

	out.DialTimeoutSeconds = options.WS.DialTimeoutSeconds
	out.PrewarmCooldownMS = options.WS.PrewarmCooldownMS
	return &out
}

// newOpenAIWSConnPool constructs a test pool from the fixture's copied pool
// settings. Construction itself starts no workers; execution tests start the
// pool or pass it to the on-demand connection owner as needed.
func newOpenAIWSConnPool(options *wsFixtureOptions) *openaiws.WSConnPool {
	return openaiws.NewWSConnPool(wsFixturePoolOptions(options))
}

// newWSFixture constructs HTTP, Grok and WS executors sharing request,
// response, connection and session services. Optional state is retained,
// otherwise state storage is constructed from Cache. Missing options use
// executor defaults; supplied output settings become configured and receive a
// 128 MiB read limit when nonpositive. Callers own any started pool's
// shutdown.
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
	state := v.state // optional retained response affinity store
	if state == nil {
		state = session.NewOpenAIWSStateStore(v.cache, gatewayadapter.LogOpenAIWSModeInfo)
	}
	output.Responses = state
	output.ProxyCircuit = egress.NewProxyStreamCircuit(egress.DefaultProxyStreamCircuitSettings())
	// A gateway cache may omit reasoning history capability; bind nil here so
	// other fixture session operations can still use the supplied cache implementation.
	history, _ := v.cache.(session.ReasoningContentCache)
	output.Reasoning = &session.ReasoningHistory{Cache: history, Warn: gatewayadapter.WarnReasoningCacheFailure}
	output.Redact = requests.Identity.Redact
	choicesOptions := selection.DefaultOptions()
	if v.options != nil {
		ws := v.options.WS
		if ws.StickyResponseIDTTLSeconds > 0 {
			// Convert fixture seconds into response-affinity expiration durations.
			choicesOptions.ResponseTTL = time.Duration(ws.StickyResponseIDTTLSeconds) * time.Second
		}
	}
	choices := selection.NewCompatible(selection.CompatibleDependencies{Reads: selection.Reads{Providers: v.providers}, Shared: selection.Shared{Cache: v.cache, Health: v.health}, Responses: state, RuntimeBlocks: output.Health.Runtime, ModelTransient: output.Health.ModelTransient, ProxyCircuit: output.ProxyCircuit}, choicesOptions) // bound compatible provider selector
	output.ResponseTTL = choices.OpenAIHTTPResponseStickyTTL
	requests.Turns.TTL = choices.SessionStickyTTL
	output.Turns = requests.Turns
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

// newExecutionReadersFixture returns runtime readers backed by the supplied
// settings repository. A nonnil repository is wrapped in settings.Store; nil
// remains available for the testkit's default readers, and the fixture options
// parameter is unused.
func newExecutionReadersFixture(repo settings.Repository, _ *wsFixtureOptions) *gatewayadapter.RuntimeReaders {
	if repo != nil {
		repo = settings.New(repo)
	}
	return gatewaytestkit.RuntimeReaders(repo)
}

// newUpstreamHealthForTest returns a testkit health observer using the
// supplied store, cache, health options and readers. The transport fixture
// options parameter is unused; health defaults and injected observations come
// from the HealthInput passed to the testkit.
func newUpstreamHealthForTest(store gatewayadapter.ExecutionProviderStore, _ *wsFixtureOptions, cache provider.TempUnschedCache, options provider.HealthOptions, readers *gatewayadapter.RuntimeReaders) *provideradapter.UpstreamHealth {
	return gatewaytestkit.NewHealthObserver(gatewaytestkit.HealthInput{Store: store, Cache: cache, Options: options, Readers: readers})
}

// setWSFixtureHealth replaces the OpenAI, Grok and general response observer
// references with one supplied health observer. Callers perform this fixture
// mutation before execution; already running callbacks are not synchronized
// with replacement here.
func setWSFixtureHealth(s *wsExecutionFixture, observer *provideradapter.UpstreamHealth) {
	s.Output.Health.Health = observer
	s.Output.GrokHealth.Health = observer
	s.Output.Observer = observer
}

// newWSFastPolicy builds a settings-backed Fast evaluator from the supplied
// policy. Nil leaves the settings key absent; nonnil policy is JSON encoded
// with a test assertion before constructing the shared runtime readers.
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

// openAIFastFilterPriorityPolicy returns an all-model priority filter with
// pass fallback for policy fixtures.
func openAIFastFilterPriorityPolicy() *tierpolicy.OpenAIFastPolicySettings {
	return &tierpolicy.OpenAIFastPolicySettings{Rules: []tierpolicy.OpenAIFastPolicyRule{{ServiceTier: tierpolicy.OpenAIFastTierPriority, Action: anthropic.BetaPolicyActionFilter, Scope: anthropic.BetaPolicyScopeAll, ModelWhitelist: []string{}, FallbackAction: anthropic.BetaPolicyActionPass}}}
}

// wsFixtureModelBlocked checks the shared transient state using this
// provider's canonical scheduling model and current time. A nil provider
// returns false; normalization matches the model key used by the bound health
// operations.
func wsFixtureModelBlocked(s *wsExecutionFixture, value *gatewayadapter.ExecutionProvider, model string) bool {
	if value == nil {
		return false
	}
	key := provider.NormalizeTransientModel(gatewayadapter.ExecutionModelPolicy(value).CanonicalSchedulingModel(model))
	return s.Output.Health.ModelTransient.IsBlocked(value.Record.ID, key, time.Now())
}

// wsFixtureProviderBlocked checks runtime blocking with the provider's current
// credential identity. Nil providers and platforms other than OpenAI or Grok
// return false; the credential callback lets the runtime discard blocks from
// stale identities.
func wsFixtureProviderBlocked(s *wsExecutionFixture, value *gatewayadapter.ExecutionProvider) bool {
	if value == nil || (value.Record.Platform != capability.PlatformOpenAI && value.Record.Platform != capability.PlatformGrok) {
		return false
	}
	return s.Output.Health.Runtime.Blocked(value.Record.ID, func() string { return provider.RefreshCredentialIdentity(gatewayadapter.ExecutionRecord(value)) })
}

// ProxyResponsesWebSocketFromClient prepares a saved provider and WS protocol
// context for direct executor fixtures. It creates a minimal Key or WS-enabled
// group when absent, normalizes a copied provider record, and binds client
// protocol and group before execution. Normalization errors stop before the
// shared executor receives the request.
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

// Dial copies outgoing and configured handshake headers while incrementing the
// attempt count under mu. It returns the configured shared connection with
// zero status and no error, ignoring URL, proxy, TLS profile and context
// cancellation so tests control transport behavior separately.
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

// DialCount returns the captured handshake attempt count under the fixture
// mutex.
func (d *openAIWSCaptureDialer) DialCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.dialCount
}

// WriteJSON captures map, raw JSON or byte requests while the fixture
// transport is open. Closed state returns ErrWSConnClosed, and context
// cancellation is ignored. Captured maps have independent top-level storage
// with shared nested values; unsupported input and malformed JSON return nil
// without adding a write observation.
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
		// Scripted writes preserve JSON numbers through UseNumber; malformed JSON
		// is accepted without a capture so fixture failures depend on explicit assertions.
		if err := wirejson.DecodeUseNumber(payload, &parsed); err == nil {
			c.lastWrite = cloneMapStringAny(parsed)
			c.writes = append(c.writes, cloneMapStringAny(parsed))
		}
	case []byte:
		var parsed map[string]any
		// Scripted writes preserve JSON numbers through UseNumber; malformed JSON
		// is accepted without a capture so fixture failures depend on explicit assertions.
		if err := wirejson.DecodeUseNumber(payload, &parsed); err == nil {
			c.lastWrite = cloneMapStringAny(parsed)
			c.writes = append(c.writes, cloneMapStringAny(parsed))
		}
	}
	return nil
}

// ReadMessage removes one scripted event and its optional delay under mu, then
// returns it after a cancellation-aware wait. Nil context uses Background;
// closed state returns ErrWSConnClosed and exhausted events return EOF.
// Returned bytes share the supplied script storage.
//
// Dequeue happens before waiting. If cancellation wins the select during a
// positive delay, the event stays consumed and the read returns the context
// error; if both timer and cancellation are ready, either branch can win. Close
// changes subsequent reads but does not interrupt a dequeued delay. With no
// delay, the event returns immediately even when its context is already
// canceled.
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
	// Queue ownership ends before the delay begins, letting Close and later reads
	// use the mutex while cancellation decides delivery of this consumed event.
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

// ReadFrame reads the next scripted event as a text frame. ReadMessage owns
// delay, cancellation and queue exhaustion rules; its error retains a text
// frame type with nil payload for the relay fixture.
func (c *openAIWSCaptureConn) ReadFrame(ctx context.Context) (coderws.MessageType, []byte, error) {
	payload, err := c.ReadMessage(ctx)
	if err != nil {
		return coderws.MessageText, nil, err
	}
	return coderws.MessageText, payload, nil
}

// WriteFrame captures payload bytes as raw JSON through WriteJSON, ignoring
// the supplied frame type. Its caller receives WriteJSON's closed-state
// result, and numeric JSON decoding uses the same fixture path as pooled
// requests.
func (c *openAIWSCaptureConn) WriteFrame(ctx context.Context, _ coderws.MessageType, payload []byte) error {
	return c.WriteJSON(ctx, json.RawMessage(payload))
}

// Ping returns nil for every fixture call and ignores context cancellation. It
// models a successful health probe without consuming events or changing the
// transport's closed flag, including calls made after Close.
func (c *openAIWSCaptureConn) Ping(ctx context.Context) error {
	_ = ctx
	return nil
}

// Close publishes the fixture's closed flag under mu and returns nil. Repeated
// calls retain that state; subsequent reads and writes reject it while an
// already dequeued ReadMessage delay finishes by its own context or timer.
func (c *openAIWSCaptureConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}

// cloneMapStringAny copies the source map's top-level entries into a new map.
// Nil input returns nil; nested maps, slices and pointers remain shared, so
// capture assertions keep those values stable while retained.
func cloneMapStringAny(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	dst := make(map[string]any, len(src))
	maps.Copy(dst, src)
	return dst
}

// WriteJSON appends an ok placeholder for each write while the fake transport
// is open. It ignores payload content and context cancellation; closed state
// returns a plain closed error without recording another placeholder.
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

// ReadMessage returns the fixed resp_fake completed event while the fake
// transport is open. It ignores context cancellation and repeats the same
// event for every read; closed state returns a plain closed error.
func (c *openAIWSFakeConn) ReadMessage(ctx context.Context) ([]byte, error) {
	_ = ctx
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, errors.New("closed")
	}
	return []byte(`{"type":"response.completed","response":{"id":"resp_fake"}}`), nil
}

// Ping returns nil and ignores context or transport state for the fake
// connection. It models a successful preflight probe independently of the
// placeholder request writes and repeated terminal reads.
func (c *openAIWSFakeConn) Ping(ctx context.Context) error {
	_ = ctx
	return nil
}

// Close marks the fake transport closed under its state mutex and returns nil.
// Repeated calls are accepted; later fake reads and writes report closed while
// Ping retains its configured no-op success.
func (c *openAIWSFakeConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}

// Do records an exchange and selects scripted results under one mutex using
// its call index. Request bodies are consumed and restored for later
// inspection; read and close errors are ignored. A matching nonnil scripted
// error wins over the response at that index.
//
// After configured responses end, later calls reuse the last response pointer;
// an entirely empty response sequence returns nil with no error. The call
// count advances even for failures, keeping bodies, request references and
// error selection tied to the same attempted exchange. Proxy and provider
// inputs are accepted but not recorded.
func (u *httpUpstreamSequenceRecorder) Do(req *http.Request, proxyURL string, providerID int64, providerConcurrency int) (*http.Response, error) {
	u.mu.Lock()
	defer u.mu.Unlock()

	idx := u.callCount
	u.callCount++
	u.reqs = append(u.reqs, req)
	if req != nil && req.Body != nil {
		// The scripted transport records partial bytes even if reading fails, then
		// reinstalls Body so later execution assertions can inspect the attempted payload.
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
	// Later exchanges intentionally reuse the final configured response pointer,
	// so callers preparing retries must provide reusable response bodies when needed.
	return u.responses[len(u.responses)-1], nil
}

// DoWithTLS delegates to the scripted HTTP exchange and ignores the supplied
// TLS profile. Body capture, attempt indexing, configured errors and
// repeated-final-response behavior all come from Do for this fixture.
func (u *httpUpstreamSequenceRecorder) DoWithTLS(req *http.Request, proxyURL string, providerID int64, providerConcurrency int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, providerID, providerConcurrency)
}

// claimOpenAIWSSessionPreemptOwner invokes core claiming with the fixture's
// full scoped key and token. Missing cache or failed claiming returns the
// core's false result; successful claims return the previous trimmed owner
// token.
func (s *wsExecutionFixture) claimOpenAIWSSessionPreemptOwner(ctx context.Context, key openAIWSSessionPreemptKey, owner string) (string, bool) {
	return s.wsPreemption().Claim(ctx, gatewayws.PreemptKey{GroupID: key.groupID, APIKeyID: key.apiKeyID, SessionHash: key.sessionHash}, owner)
}

// releaseOpenAIWSSessionPreemptOwner delegates token-checked remote cleanup
// for the fixture scope. The core ignores comparison and cache errors,
// allowing stale-release tests to inspect whether the newer owner token
// remains stored.
func (s *wsExecutionFixture) releaseOpenAIWSSessionPreemptOwner(ctx context.Context, key openAIWSSessionPreemptKey, owner string) {
	s.wsPreemption().Release(ctx, gatewayws.PreemptKey{GroupID: key.groupID, APIKeyID: key.apiKeyID, SessionHash: key.sessionHash}, owner)
}

// WriteLogEvent appends a copied event under the sink mutex. Nil events are
// ignored and Fields map storage is copied before publication; nested values
// stay shared, so callers keep them stable while fixture assertions inspect the
// stored event.
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

// ContainsMessageAtLevel reports a substring match at the requested
// case-folded, trimmed level. It skips nil events and holds the sink mutex
// while scanning, so concurrent writes cannot change the inspected event
// slice.
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

// ContainsFieldValue reports whether a formatted field value contains the
// supplied substring. Nil events and missing field maps are skipped under the
// sink mutex; formatting supports the mixed scalar values emitted by the
// structured logger.
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

// FieldValueForMessage returns the first stored field for an exactly matching
// message with a present key. It holds the sink mutex while scanning, returns
// false when missing, and shares the selected value with the stored event.
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

// ContainsMessage reports whether any nonnil stored event message contains the
// supplied substring. Scanning holds the sink mutex so assertions can read
// event membership while the active sink receives additional records.
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

// GetByID counts lookup attempts and returns the configured provider reference
// by ID. Missing entries return provider not found; context is ignored. Callers
// serialize lookup and fixture mutation, and returned providers remain shared
// with the maps used by health assertions.
func (r *grokFixtureProviders) GetByID(_ context.Context, id int64) (*gatewayadapter.ExecutionProvider, error) {
	r.getByIDCalls++
	if value, ok := r.providersByID[id]; ok {
		return value, nil
	}
	return nil, errors.New("provider not found")
}

// captureHandlerStructuredLog installs a debug JSON logger and an in-memory
// process-wide sink for one test. It acquires a global mutex before replacing
// logger state, then returns the sink and a release callback. Callers invoke
// release once after the test's captured logging is complete.
//
// Release clears the active sink before unlocking so another fixture can begin
// its capture. The helper leaves the initialized logger in place and models
// serialized sink access, not a restoration of every prior logger setting.
// Assertions inside logger initialization run while the mutex is held, so
// successful setup is required to obtain cleanup.
func captureHandlerStructuredLog(t *testing.T) (*handlerInMemoryLogSink, func()) {
	t.Helper()
	// Keep process-wide sink replacement serialized until the returned release
	// clears this fixture sink, preventing another capture from stealing its events.
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
		// Clear this global sink before another fixture obtains the capture mutex;
		// their subsequent log observations then belong to the newly installed sink.
		logging.SetSink(nil)
		handlerStructuredLogCaptureMu.Unlock()
	}
}

// UpdateExtra records the supplied map and merges its top-level values into an
// available fixture provider's Extra map. The updates entry retains the
// supplied map, and nested values remain shared with the provider. Callers
// serialize these operations and keep retained values stable; the helper
// returns nil even when no provider is configured.
//
// The call count advances before update storage or provider lookup. An absent
// Extra map is created for an available provider before merging, so quota
// assertions read the same supplied values recorded in updates. The fixture
// models these observation effects through memory and counters; conditional
// persistence belongs to the production repository.
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

// SetRateLimited counts the cooldown write and retains its provider ID and
// expiry. It returns nil without changing configured provider state, letting
// tests distinguish the attempted health write from any repository
// persistence.
func (r *grokQuotaProviderRepo) SetRateLimited(_ context.Context, id int64, resetAt time.Time) error {
	r.rateLimitedCalls++
	r.lastRateLimitedID = id
	r.lastRateLimitResetAt = resetAt
	return nil
}

// SetRateLimitedIfLater delegates to SetRateLimited for this fixture and
// accepts every expiry. It records the attempted conditional write without
// comparing existing state, so tests assert calls rather than monotonic
// repository updates.
func (r *grokQuotaProviderRepo) SetRateLimitedIfLater(ctx context.Context, id int64, resetAt time.Time) error {
	return r.SetRateLimited(ctx, id, resetAt)
}

// ClearRateLimitIfObserved records observed cooldown timestamps and returns
// the configured recoveryClearResult. It ignores provider ID and performs no
// actual clear, letting tests choose the match outcome while checking the
// production recovery inputs.
func (r *grokQuotaProviderRepo) ClearRateLimitIfObserved(_ context.Context, _ int64, observedLimitedAt, observedResetAt time.Time) (bool, error) {
	r.recoveryClearCalls++
	r.recoveryObservedAt = observedLimitedAt
	r.recoveryObservedReset = observedResetAt
	return r.recoveryClearResult, nil
}

// SetTempUnschedulable records a temporary-block count, target, expiry and
// reason and returns nil. The fixture does not mutate provider state; tests
// inspect these recorded values to verify the health operation requested by
// execution.
func (r *grokQuotaProviderRepo) SetTempUnschedulable(_ context.Context, id int64, until time.Time, reason string) error {
	r.tempUnschedCalls++
	r.lastTempUnschedID = id
	r.lastTempUnschedUntil = until
	r.lastTempUnschedReason = reason
	return nil
}

// SetError counts provider error writes made by structured rejection fixtures.
func (r *openAIStream403ProviderRepo) SetError(context.Context, int64, string) error {
	r.setErrorCalls++
	return nil
}

// SetOverloaded accepts the fixture overload write and returns nil without
// storage mutation. Transient model fixtures observe the bound runtime and
// model state separately, so this repository method supplies no additional
// failure.
func (transientCooldownProviderRepo) SetOverloaded(context.Context, int64, time.Time) error {
	return nil
}
