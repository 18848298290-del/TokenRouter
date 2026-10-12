package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/egress"
	egressadapter "github.com/TokenFlux/TokenRouter/internal/egress/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/completion"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	"github.com/TokenFlux/TokenRouter/internal/gateway/live"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/provider/selection"
	gatewaysession "github.com/TokenFlux/TokenRouter/internal/gateway/session"
	"github.com/TokenFlux/TokenRouter/internal/gateway/telemetry"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/tlsfingerprint"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	openaicore "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	upstreamcore "github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai/liveattestation"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// cancelingLiveStore cancels after controller acquisition and before provider lookup.
type cancelingLiveStore struct {
	liveTestStore
	cancel context.CancelFunc
}

type countingLiveProviders struct {
	gatewayprovider.ExecutionProviderStore

	reads atomic.Int32
}

// liveFixtureInputs supplies stores, frame connections, and identity dependencies for Live tests.
type liveFixtureInputs struct {
	transport   httpclient.UpstreamTransport
	providers   gatewayprovider.ExecutionProviderStore
	store       gatewaysession.LiveCallStore
	concurrency *scheduler.ConcurrencyService
	logs        usage.UsageLogRepository
	profiles    *egressadapter.TLSProfiles
	routers     *egress.TLSFingerprintRouterService
	dialer      openai.WSClientDialer
	attestation liveattestation.Provider
	cipher      identity.SecretEncryptor
	duration    time.Duration
}

// liveProfileStore supplies profiles for preloading; production code matches templates and routing rules.
type liveProfileStore struct {
	egress.TLSFingerprintProfileRepository
	values []*egress.TLSFingerprintProfile
}

type liveRouterStore struct {
	egress.TLSFingerprintRouterRepository
	values []*egress.TLSFingerprintRouter
}

type liveTestFrame struct {
	messageType coderws.MessageType
	payload     []byte
	err         error
}

type liveTestFrameConn struct {
	reads     chan liveTestFrame
	writes    chan liveTestFrame
	closed    chan struct{}
	closeOnce sync.Once
}

type liveTestDialer struct {
	conn       *liveTestFrameConn
	url        string
	headers    http.Header
	tlsProfile *tlsfingerprint.Profile
}

type liveTestProviderRepo struct {
	gatewayprovider.ExecutionProviderStore

	provider *gatewayprovider.ExecutionProvider
}

type liveTestStore struct {
	gatewaysession.GatewayCache
	mu     sync.Mutex
	record *gatewaysession.LiveCallRecord
	// Injected errors distinguish store outages from missing records.
	claimErr         error
	getCallErr       error
	getControllerErr error
}

type liveTestConcurrencyCache struct {
	scheduler.ConcurrencyCache
	mu       sync.Mutex
	releases int
}

type liveTestUsageRepo struct {
	usage.UsageLogRepository
	mu   sync.Mutex
	logs []*usage.UsageLog
}

type liveTestBestEffortUsageRepo struct {
	liveTestUsageRepo
	bestEffortErr   error
	bestEffortCalls int
}

type liveHTTPUpstreamStub struct {
	request    *http.Request
	body       []byte
	tlsProfile *tlsfingerprint.Profile
}

type liveAttestationStub struct {
	header string
	err    error
}

func (s *cancelingLiveStore) ClaimLiveController(ctx context.Context, hash, controller, owner string) (bool, error) {
	ok, err := s.liveTestStore.ClaimLiveController(ctx, hash, controller, owner)
	s.cancel()
	return ok, err
}

func (r *countingLiveProviders) GetByID(ctx context.Context, id int64) (*gatewayprovider.ExecutionProvider, error) {
	r.reads.Add(1)
	return nil, ctx.Err()
}

func TestLiveHandoffCancellationStopsBeforeProviderLookup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	record := &gatewaysession.LiveCallRecord{CallHash: "lifecycle-test", Controller: gatewaysession.LiveControllerPending, ProviderID: 7, ExpiresAt: time.Now().Add(time.Minute)}
	store := &cancelingLiveStore{liveTestStore: liveTestStore{record: record}, cancel: cancel}
	providers := &countingLiveProviders{}
	s := newLiveFixture(liveFixtureInputs{store: store, providers: providers})
	s.liveObserverStopped = true
	start := time.Now()
	err := s.Proxy(ctx, record, &coderws.Conn{})
	if err != context.Canceled {
		t.Errorf("expected cancellation, got %v", err)
	}
	if time.Since(start) >= 100*time.Millisecond {
		t.Error("cancelled Live handoff still slept for the observer interval")
	}
	if providers.reads.Load() != 0 {
		t.Errorf("cancelled Live handoff still queried execution provider: %d", providers.reads.Load())
	}
}

func newLiveFixture(v liveFixtureInputs) *OpenAILiveExecutor {
	aux := newAuxiliaryFixture(auxiliaryFixtureInputs{transport: v.transport, store: v.providers, profiles: v.profiles})
	aux.Requests.Routers = v.routers
	aux.Requests.ClientPolicy.Routers = v.routers
	out := &OpenAILiveExecutor{Options: OpenAILiveOptions{MaxSessionDuration: v.duration, ObserverRetryInterval: time.Second}, Requests: aux.Requests, Store: v.store, Dialer: v.dialer, Attestation: v.attestation, AttestationCipher: v.cipher, Selection: selection.NewCompatible(selection.CompatibleDependencies{}, selection.Options{}), Routes: gatewayprovider.NewRoutePlanner(nil), Background: func(_ string, fn func()) bool { go fn(); return true }}
	if v.concurrency != nil {
		out.Leases = v.concurrency.LiveLeases()
	}
	if v.logs != nil {
		out.Usage = completion.NewRecorder(completion.Dependencies{Logs: completion.SnapshotLogWriter(v.logs), Observe: telemetry.ObserveCompletion}, completion.RecorderOptions{})
	}
	return out
}

func (s *liveProfileStore) List(context.Context) ([]*egress.TLSFingerprintProfile, error) {
	return s.values, nil
}

func (s *liveRouterStore) List(context.Context) ([]*egress.TLSFingerprintRouter, error) {
	return s.values, nil
}

func newLiveTestFrameConn() *liveTestFrameConn {
	return &liveTestFrameConn{
		reads:  make(chan liveTestFrame, 8),
		writes: make(chan liveTestFrame, 8),
		closed: make(chan struct{}),
	}
}

func (c *liveTestFrameConn) ReadFrame(ctx context.Context) (coderws.MessageType, []byte, error) {
	select {
	case frame := <-c.reads:
		return frame.messageType, frame.payload, frame.err
	case <-c.closed:
		return coderws.MessageText, nil, coderws.CloseError{Code: coderws.StatusNormalClosure}
	case <-ctx.Done():
		return coderws.MessageText, nil, context.Cause(ctx)
	}
}

func (c *liveTestFrameConn) WriteFrame(ctx context.Context, messageType coderws.MessageType, payload []byte) error {
	frame := liveTestFrame{messageType: messageType, payload: append([]byte(nil), payload...)}
	select {
	case c.writes <- frame:
		return nil
	case <-c.closed:
		return errors.New("connection closed")
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

func (c *liveTestFrameConn) WriteJSON(ctx context.Context, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return c.WriteFrame(ctx, coderws.MessageText, payload)
}

func (c *liveTestFrameConn) ReadMessage(ctx context.Context) ([]byte, error) {
	_, payload, err := c.ReadFrame(ctx)
	return payload, err
}

func (c *liveTestFrameConn) Ping(context.Context) error { return nil }

func (c *liveTestFrameConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}

func (d *liveTestDialer) Dial(
	_ context.Context,
	wsURL string,
	headers http.Header,
	_ string,
	profile *tlsfingerprint.Profile,
) (openai.WSClientConn, int, http.Header, error) {
	d.url = wsURL
	d.headers = headers.Clone()
	d.tlsProfile = profile
	return d.conn, http.StatusSwitchingProtocols, nil, nil
}

func (r *liveTestProviderRepo) GetByID(context.Context, int64) (*gatewayprovider.ExecutionProvider, error) {
	return r.provider, nil
}

func (s *liveTestStore) SaveLiveCall(_ context.Context, record *gatewaysession.LiveCallRecord, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	copy := *record
	s.record = &copy
	return nil
}

func (s *liveTestStore) GetLiveCall(_ context.Context, callHash string) (*gatewaysession.LiveCallRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.getCallErr != nil {
		return nil, s.getCallErr
	}
	if s.record == nil || s.record.CallHash != callHash {
		return nil, gatewaysession.ErrLiveCallNotFound
	}
	copy := *s.record
	return &copy, nil
}

func (s *liveTestStore) ClaimLiveController(_ context.Context, callHash, controller, owner string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.claimErr != nil {
		return false, s.claimErr
	}
	if s.record == nil || s.record.CallHash != callHash || s.record.Controller == gatewaysession.LiveControllerClosed {
		return false, nil
	}
	if controller == gatewaysession.LiveControllerObserver && s.record.Controller != gatewaysession.LiveControllerPending {
		return false, nil
	}
	if controller == gatewaysession.LiveControllerProxy && s.record.Controller != gatewaysession.LiveControllerPending && s.record.Controller != gatewaysession.LiveControllerObserver {
		return false, nil
	}
	s.record.Controller = controller
	s.record.ControllerOwner = owner
	return true, nil
}

func (s *liveTestStore) ReleaseLiveController(_ context.Context, callHash, owner string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.record == nil || s.record.CallHash != callHash || s.record.ControllerOwner != owner {
		return false, nil
	}
	s.record.Controller = gatewaysession.LiveControllerPending
	s.record.ControllerOwner = ""
	return true, nil
}

func (s *liveTestStore) GetLiveController(_ context.Context, callHash string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.getControllerErr != nil {
		return "", s.getControllerErr
	}
	if s.record == nil || s.record.CallHash != callHash {
		return "", gatewaysession.ErrLiveCallNotFound
	}
	return s.record.Controller, nil
}

func (s *liveTestStore) MarkLiveCallClosed(_ context.Context, callHash string, _ time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.record == nil || s.record.CallHash != callHash || s.record.Controller == gatewaysession.LiveControllerClosed {
		return false, nil
	}
	s.record.Controller = gatewaysession.LiveControllerClosed
	s.record.ControllerOwner = ""
	return true, nil
}

func (c *liveTestConcurrencyCache) AcquireLiveLease(
	context.Context,
	int64,
	int,
	int64,
	int,
	int64,
	string,
	bool,
) (bool, error) {
	return true, nil
}

func (c *liveTestConcurrencyCache) RefreshLiveLease(
	context.Context,
	int64,
	int64,
	int64,
	string,
) (bool, error) {
	return true, nil
}

func (c *liveTestConcurrencyCache) ReleaseLiveLease(
	context.Context,
	int64,
	int64,
	int64,
	string,
) error {
	c.mu.Lock()
	c.releases++
	c.mu.Unlock()
	return nil
}

func (r *liveTestUsageRepo) Create(_ context.Context, log *usage.UsageLog) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	copy := *log
	r.logs = append(r.logs, &copy)
	return true, nil
}

func TestRunLiveControllerClosesExpiredSession(t *testing.T) {
	upstream := newLiveTestFrameConn()
	record := &gatewaysession.LiveCallRecord{ExpiresAt: time.Now().Add(20 * time.Millisecond)}
	service := newLiveFixture(liveFixtureInputs{})

	err := service.liveRuntime().RunController(context.Background(), record, liveUpstreamFrames{upstream}, make(chan error))
	require.ErrorIs(t, err, context.DeadlineExceeded)

	select {
	case frame := <-upstream.writes:
		require.Equal(t, coderws.MessageText, frame.messageType)
		require.JSONEq(t, `{"type":"session.close"}`, string(frame.payload))
	case <-time.After(time.Second):
		t.Fatal("没有向上游发送 session.close")
	}
}

func TestFinalizeLiveCallIsIdempotentAndWritesZeroUsage(t *testing.T) {
	record := &gatewaysession.LiveCallRecord{
		CallID:            "call_secret",
		CallHash:          live.HashCallID("call_secret"),
		ProviderID:        11,
		APIKeyID:          22,
		UserID:            33,
		GroupID:           44,
		LeaseID:           "lease-1",
		Model:             "gpt-live-test",
		RequestedModel:    "live-alias",
		UpstreamModel:     "gpt-live-upstream",
		ModelMappingChain: "live-alias→gpt-live-test→gpt-live-upstream",
		CreatedAt:         time.Now().Add(-time.Second),
		ExpiresAt:         time.Now().Add(time.Hour),
		Controller:        gatewaysession.LiveControllerPending,
		InboundEndpoint:   "/v1/live",
	}
	store := &liveTestStore{}
	require.NoError(t, store.SaveLiveCall(context.Background(), record, time.Hour))
	concurrencyCache := &liveTestConcurrencyCache{}
	usageRepo := &liveTestUsageRepo{}
	service := newLiveFixture(liveFixtureInputs{store: store, concurrency: scheduler.NewConcurrencyService(concurrencyCache, scheduler.Diagnostics{
		Logf:  logging.LegacyPrintf,
		Event: logging.Event,
	},
	), logs: usageRepo})

	service.liveRuntime().Finalize(record)
	service.liveRuntime().Finalize(record)

	concurrencyCache.mu.Lock()
	require.Equal(t, 1, concurrencyCache.releases)
	concurrencyCache.mu.Unlock()
	usageRepo.mu.Lock()
	require.Len(t, usageRepo.logs, 1)
	log := usageRepo.logs[0]
	usageRepo.mu.Unlock()
	require.Equal(t, usage.RequestTypeLive, log.RequestType)
	require.Equal(t, record.CallHash, log.BillingKey)
	require.Len(t, log.RequestID, 36)
	require.NotEqual(t, record.CallID, log.RequestID)
	require.NotNil(t, log.DurationMs)
	require.Zero(t, log.InputTokens)
	require.Zero(t, log.OutputTokens)
	require.Zero(t, log.TotalCost)
	require.Zero(t, log.ActualCost)
	require.Equal(t, record.Model, log.Model)
	require.Equal(t, record.RequestedModel, log.RequestedModel)
	require.NotNil(t, log.UpstreamModel)
	require.Equal(t, record.UpstreamModel, *log.UpstreamModel)
	require.NotNil(t, log.ModelMappingChain)
	require.Equal(t, record.ModelMappingChain, *log.ModelMappingChain)
}

func TestGetLiveCallForIdentityRejectsMismatchedCaller(t *testing.T) {
	groupID := int64(44)
	record := &gatewaysession.LiveCallRecord{
		CallID:     "call_identity",
		CallHash:   live.HashCallID("call_identity"),
		APIKeyID:   22,
		UserID:     33,
		GroupID:    groupID,
		Controller: gatewaysession.LiveControllerPending,
	}
	store := &liveTestStore{}
	require.NoError(t, store.SaveLiveCall(context.Background(), record, time.Hour))
	service := newLiveFixture(liveFixtureInputs{store: store})

	_, err := service.Lookup(context.Background(), record.CallID, gatewaysession.LiveCallIdentity{
		APIKeyID: 99,
		UserID:   record.UserID,
		GroupID:  &groupID,
	})
	require.ErrorIs(t, err, gatewaysession.ErrLiveIdentityMismatch)

	loaded, err := service.Lookup(context.Background(), record.CallID, gatewaysession.LiveCallIdentity{
		APIKeyID: record.APIKeyID,
		UserID:   record.UserID,
		GroupID:  &groupID,
	})
	require.NoError(t, err)
	require.Equal(t, record.ProviderID, loaded.ProviderID)
}

func TestLiveSidebandRewritesEachSessionModelAndRestoresResponse(t *testing.T) {
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 11,
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeOAuth,
			Status:      billing.StatusActive,
			Schedulable: true,
			Credentials: map[string]any{
				"model_whitelist": []string{"gpt-5.4"},
				"model_mapping":   map[string]any{"gpt-5": "gpt-5.4"},
			},
		},
	}
	upstreamModel := gatewayprovider.ExecutionModelPolicy(provider).OpenAIUpstream("gpt-5", false)
	record := &gatewaysession.LiveCallRecord{
		GroupID:            44,
		Model:              "gpt-5",
		RequestedModel:     "live-alias",
		UpstreamModel:      upstreamModel,
		APIKeyModelMapping: map[string]string{"live-alias": "gpt-5", "tool-alias": "tool-target"},
	}
	service := newLiveFixture(liveFixtureInputs{})
	payload := []byte(`{"type":"session.update","session":{"model":"live-alias","tools":[{"model":"tool-alias"}],"instructions":"keep live-alias and gpt-5.1-codex"}}`)

	rewritten, clientModel, internalModels, err := service.rewriteLiveSidebandClientPayload(context.Background(), record, provider, payload)

	require.NoError(t, err)
	require.Equal(t, "live-alias", clientModel)
	require.Equal(t, upstreamModel, gjson.GetBytes(rewritten, "session.model").String())
	require.Equal(t, "tool-target", gjson.GetBytes(rewritten, "session.tools.0.model").String())
	require.Equal(t, "keep live-alias and gpt-5.1-codex", gjson.GetBytes(rewritten, "session.instructions").String())

	response := live.RestoreServerPayload(
		[]byte(fmt.Sprintf(`{"type":"session.updated","session":{"model":%q,"instructions":"keep gpt-5.1-codex"}}`, upstreamModel)),
		clientModel,
		internalModels,
	)
	require.Equal(t, "live-alias", gjson.GetBytes(response, "session.model").String())
	require.Equal(t, "keep gpt-5.1-codex", gjson.GetBytes(response, "session.instructions").String())
}

func TestProxyLiveSidebandForwardsTextAndBinary(t *testing.T) {
	profileService, routerService := newLiveTLSRoutingServices()
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 11,
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeOAuth,
			Concurrency: 2,
			Credentials: map[string]any{
				"access_token":       "test-access-token",
				"chatgpt_account_id": "acct_test",
			},
			Extra: map[string]any{
				"enable_tls_fingerprint":    true,
				"tls_fingerprint_router_id": int64(9),
			},
		},
	}
	record := &gatewaysession.LiveCallRecord{
		CallID:     "call_proxy",
		CallHash:   live.HashCallID("call_proxy"),
		ProviderID: provider.Record.ID,
		APIKeyID:   22,
		UserID:     33,
		LeaseID:    "lease-1",
		CreatedAt:  time.Now(),
		ExpiresAt:  time.Now().Add(time.Minute),
		Controller: gatewaysession.LiveControllerPending,
		UserAgent:  "test-live-client",
	}
	attestationCipher := openai.NewLiveAttestationCipher("live-sideband-test-secret")
	var err error
	record.AttestationCiphertext, err = attestationCipher.Encrypt(`{"v":1,"s":0,"t":"v1.sideband"}`)
	require.NoError(t, err)
	store := &liveTestStore{}
	require.NoError(t, store.SaveLiveCall(context.Background(), record, time.Hour))
	upstream := newLiveTestFrameConn()
	dialer := &liveTestDialer{conn: upstream}
	service := newLiveFixture(liveFixtureInputs{providers: &liveTestProviderRepo{provider: provider}, store: store, dialer: dialer, cipher: attestationCipher, profiles: profileService, routers: routerService})
	proxyResult := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		downstream, err := coderws.Accept(writer, request, nil)
		if err != nil {
			proxyResult <- err
			return
		}
		defer func() { _ = downstream.CloseNow() }()
		proxyResult <- service.Proxy(request.Context(), record, downstream)
	}))
	defer server.Close()

	client, _, err := coderws.Dial(
		context.Background(),
		"ws"+strings.TrimPrefix(server.URL, "http"),
		nil,
	)
	require.NoError(t, err)
	defer func() { _ = client.CloseNow() }()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(`{"type":"client.text"}`)))
	clientText := <-upstream.writes
	require.Equal(t, coderws.MessageText, clientText.messageType)
	require.JSONEq(t, `{"type":"client.text"}`, string(clientText.payload))

	require.NoError(t, client.Write(ctx, coderws.MessageBinary, []byte{1, 2, 3}))
	clientBinary := <-upstream.writes
	require.Equal(t, coderws.MessageBinary, clientBinary.messageType)
	require.Equal(t, []byte{1, 2, 3}, clientBinary.payload)

	upstream.reads <- liveTestFrame{messageType: coderws.MessageText, payload: []byte(`{"type":"server.text"}`)}
	messageType, payload, err := client.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, coderws.MessageText, messageType)
	require.JSONEq(t, `{"type":"server.text"}`, string(payload))

	upstream.reads <- liveTestFrame{messageType: coderws.MessageBinary, payload: []byte{4, 5, 6}}
	messageType, payload, err = client.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, coderws.MessageBinary, messageType)
	require.Equal(t, []byte{4, 5, 6}, payload)

	require.Equal(t, "wss://chatgpt.com/backend-api/codex/call_proxy", dialer.url)
	require.Equal(t, "Bearer test-access-token", dialer.headers.Get("Authorization"))
	require.Equal(t, "acct_test", dialer.headers.Get("Chatgpt-Account-Id"))
	require.Equal(t, `{"v":1,"s":0,"t":"v1.sideband"}`, dialer.headers.Get(openai.LiveAttestationHeader))
	require.Equal(t, "codex_vscode/0.144.1 live-test", dialer.headers.Get("User-Agent"))
	require.Equal(t, "codex_vscode", dialer.headers.Get("Originator"))
	require.NotNil(t, dialer.tlsProfile)
	require.Equal(t, "live-routed", dialer.tlsProfile.Name)
	upstream.reads <- liveTestFrame{err: coderws.CloseError{Code: coderws.StatusNormalClosure}}
	require.ErrorIs(t, <-proxyResult, gatewaysession.ErrLiveCallNotFound)
}

// TestLiveSessionEndedTreatsLeaseLossAsTerminal checks that ErrLiveUnavailable ends the session.
// Retrying after lease loss would allow the session to continue outside concurrency accounting.
func TestLiveSessionEndedTreatsLeaseLossAsTerminal(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"租约丢失", gatewaysession.ErrLiveUnavailable, true},
		{"租约丢失（被包装）", fmt.Errorf("refresh live lease: %w", gatewaysession.ErrLiveUnavailable), true},
		{"上游报告会话已关闭", gatewaysession.ErrLiveCallNotFound, true},
		{"到达会话时长上限", context.DeadlineExceeded, true},
		{"控制权被他人接管", gatewaysession.ErrLiveControllerChanged, false},
		{"临时读错误", errors.New("unexpected EOF"), false},
		{"无错误", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, live.SessionEnded(tc.err))
		})
	}
}

// TestWaitForLiveObserverRetryLeavesExpiryToLoopFinalize checks that an expired, observer-owned call
// returns to the observation loop for usage recording and lease release.
// Stopping at the retry helper would skip finalization.
func TestWaitForLiveObserverRetryLeavesExpiryToLoopFinalize(t *testing.T) {
	record := &gatewaysession.LiveCallRecord{
		CallID:     "call_expired",
		CallHash:   live.HashCallID("call_expired"),
		Controller: gatewaysession.LiveControllerObserver,
		ExpiresAt:  time.Now().Add(-time.Minute),
	}
	store := &liveTestStore{}
	require.NoError(t, store.SaveLiveCall(context.Background(), record, time.Hour))
	svc := newLiveFixture(liveFixtureInputs{store: store})

	require.True(t, svc.liveRuntime().WaitForObserverRetry(context.Background(), record),
		"过期判定必须留给循环顶部，否则不会写 usage log")

	// After takeover, the new controller owns the call and observer retries stop.
	require.NoError(t, store.SaveLiveCall(context.Background(), &gatewaysession.LiveCallRecord{
		CallID:     record.CallID,
		CallHash:   record.CallHash,
		Controller: gatewaysession.LiveControllerProxy,
		ExpiresAt:  time.Now().Add(time.Hour),
	}, time.Hour))
	require.False(t, svc.liveRuntime().WaitForObserverRetry(context.Background(), record))
}

// TestWaitForLiveObserverRetryTreatsStoreErrorAsRetryable distinguishes temporary store errors from missing
// calls.
func TestWaitForLiveObserverRetryTreatsStoreErrorAsRetryable(t *testing.T) {
	record := &gatewaysession.LiveCallRecord{
		CallID:     "call_flaky_store",
		CallHash:   live.HashCallID("call_flaky_store"),
		Controller: gatewaysession.LiveControllerObserver,
		ExpiresAt:  time.Now().Add(time.Hour),
	}
	store := &liveTestStore{getControllerErr: errors.New("redis: connection refused")}
	require.NoError(t, store.SaveLiveCall(context.Background(), record, time.Hour))
	svc := newLiveFixture(liveFixtureInputs{store: store})

	require.True(t, svc.liveRuntime().WaitForObserverRetry(context.Background(), record),
		"store 报错时必须继续重试，否则会话会静默结束")
	require.False(t, newLiveFixture(liveFixtureInputs{store: &liveTestStore{}}).liveRuntime().WaitForObserverRetry(context.Background(), record),
		"记录不存在时应停止重试")
}

// TestObserveLiveCallStoreOutageFallsBackToExpiryFinalize checks expiry settlement from the creation
// snapshot during store outages.
func TestObserveLiveCallStoreOutageFallsBackToExpiryFinalize(t *testing.T) {
	cases := []struct {
		name   string
		inject func(*liveTestStore)
	}{
		{"GetLiveCall 持续报错", func(store *liveTestStore) {
			store.getCallErr = errors.New("redis: i/o timeout")
		}},
		{"ClaimLiveController 报错", func(store *liveTestStore) {
			store.claimErr = errors.New("redis: i/o timeout")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			record := &gatewaysession.LiveCallRecord{
				CallID:     "call_store_outage",
				CallHash:   live.HashCallID("call_store_outage"),
				ProviderID: 11,
				APIKeyID:   22,
				UserID:     33,
				LeaseID:    "lease-1",
				Model:      "gpt-live-test",
				CreatedAt:  time.Now().Add(-time.Minute),
				ExpiresAt:  time.Now().Add(-time.Second),
				Controller: gatewaysession.LiveControllerPending,
			}
			store := &liveTestStore{}
			require.NoError(t, store.SaveLiveCall(context.Background(), record, time.Hour))
			tc.inject(store)
			concurrencyCache := &liveTestConcurrencyCache{}
			usageRepo := &liveTestUsageRepo{}
			svc := newLiveFixture(liveFixtureInputs{store: store, concurrency: scheduler.NewConcurrencyService(concurrencyCache, scheduler.Diagnostics{
				Logf:  logging.LegacyPrintf,
				Event: logging.Event,
			},
			), logs: usageRepo})

			svc.Options.ObserverRetryInterval = time.Millisecond
			svc.observeLiveCall(record)

			concurrencyCache.mu.Lock()
			require.Equal(t, 1, concurrencyCache.releases, "store 故障时租约释放不能丢")
			concurrencyCache.mu.Unlock()
			usageRepo.mu.Lock()
			require.Len(t, usageRepo.logs, 1, "store 故障时 usage log 不能丢")
			require.Equal(t, usage.RequestTypeLive, usageRepo.logs[0].RequestType)
			usageRepo.mu.Unlock()
		})
	}
}

func (r *liveTestBestEffortUsageRepo) CreateBestEffort(_ context.Context, _ *usage.UsageLog) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bestEffortCalls++
	return r.bestEffortErr
}

// TestFinalizeLiveCallUsageLogFallsBackToSyncCreate checks synchronous insertion after best-effort
// insertion fails.
func TestFinalizeLiveCallUsageLogFallsBackToSyncCreate(t *testing.T) {
	record := &gatewaysession.LiveCallRecord{
		CallID:     "call_usage_fallback",
		CallHash:   live.HashCallID("call_usage_fallback"),
		ProviderID: 11,
		APIKeyID:   22,
		UserID:     33,
		LeaseID:    "lease-1",
		Model:      "gpt-live-test",
		CreatedAt:  time.Now().Add(-time.Second),
		ExpiresAt:  time.Now().Add(time.Hour),
		Controller: gatewaysession.LiveControllerPending,
	}
	store := &liveTestStore{}
	require.NoError(t, store.SaveLiveCall(context.Background(), record, time.Hour))
	usageRepo := &liveTestBestEffortUsageRepo{bestEffortErr: errors.New("usage log queue dropped")}
	svc := newLiveFixture(liveFixtureInputs{store: store, concurrency: scheduler.NewConcurrencyService(&liveTestConcurrencyCache{}, scheduler.Diagnostics{
		Logf:  logging.LegacyPrintf,
		Event: logging.Event,
	},
	), logs: usageRepo})

	svc.liveRuntime().Finalize(record)

	usageRepo.mu.Lock()
	defer usageRepo.mu.Unlock()
	require.Equal(t, 1, usageRepo.bestEffortCalls)
	require.Len(t, usageRepo.logs, 1, "best-effort 失败后必须同步兜底落库")
	require.Equal(t, record.CallHash, usageRepo.logs[0].BillingKey)
	require.Len(t, usageRepo.logs[0].RequestID, 36)
}

// TestStopLiveObserversPreservesRemoteCall checks that stopping local observation leaves the stored call open.
func TestStopLiveObserversPreservesRemoteCall(t *testing.T) {
	record := &gatewaysession.LiveCallRecord{CallHash: "test-shutdown", Controller: gatewaysession.LiveControllerPending, ExpiresAt: time.Now().Add(time.Hour)}
	store := &liveTestStore{record: record, claimErr: errors.New("temporary store failure")}
	svc := newLiveFixture(liveFixtureInputs{store: store})
	done := make(chan struct{})
	go func() { svc.observeLiveCall(record); close(done) }()
	require.Eventually(t, func() bool {
		svc.liveObserverMu.Lock()
		defer svc.liveObserverMu.Unlock()
		return len(svc.liveObserverCancels) == 1
	}, time.Second, time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, svc.StopLiveObservers(ctx))
	<-done
	require.NoError(t, svc.StopLiveObservers(ctx))
	stored, err := store.GetLiveCall(context.Background(), record.CallHash)
	require.NoError(t, err)
	require.Equal(t, gatewaysession.LiveControllerPending, stored.Controller)
	svc.observeLiveCall(record)
	svc.liveObserverMu.Lock()
	require.Empty(t, svc.liveObserverCancels)
	svc.liveObserverMu.Unlock()
}

// newLiveTLSRoutingServices supplies routing rules for TLS profiles and identity headers.
func newLiveTLSRoutingServices() (*egressadapter.TLSProfiles, *egress.TLSFingerprintRouterService) {
	profileService := egressadapter.NewTLSProfiles(egress.NewTLSFingerprintProfileService(&liveProfileStore{values: []*egress.TLSFingerprintProfile{{ID: 20, Name: "live-routed"}}}, nil))
	profileService.Start()

	router := &egress.TLSFingerprintRouter{
		ID:      9,
		Name:    "live-router",
		Enabled: true,
		Rules: []egress.TLSFingerprintRouterRule{{
			Name:                    "live-client",
			Enabled:                 true,
			MatchType:               egress.TLSRouterMatchExact,
			Pattern:                 "test-live-client",
			TLSFingerprintProfileID: 20,
			UpstreamUserAgent:       "codex_vscode/0.144.1 live-test",
			UpstreamOriginator:      "codex_vscode",
		}},
	}
	routerService := egress.NewTLSFingerprintRouterService(&liveRouterStore{values: []*egress.TLSFingerprintRouter{router}}, nil)
	routerService.Start()

	return profileService, routerService
}

func (s liveAttestationStub) Check(context.Context) error {
	return s.err
}

func (s liveAttestationStub) Generate(context.Context) (string, error) {
	return s.header, s.err
}

func (s *liveHTTPUpstreamStub) Do(
	request *http.Request,
	_ string,
	_ int64,
	_ int,
) (*http.Response, error) {
	s.request = request
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	s.body = body
	return &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Location": {"/backend-api/codex/call_test"},
		},
		Body: io.NopCloser(strings.NewReader("v=0\r\n")),
	}, nil
}

func (s *liveHTTPUpstreamStub) DoWithTLS(
	request *http.Request,
	proxyURL string,
	providerID int64,
	providerConcurrency int,
	profile *tlsfingerprint.Profile,
) (*http.Response, error) {
	s.tlsProfile = profile
	return s.Do(request, proxyURL, providerID, providerConcurrency)
}

func TestLiveCapabilityOnlyAllowsOpenAIOAuth(t *testing.T) {
	require.True(t, provideradapter.SupportsOpenAIEndpoint(gatewayprovider.ExecutionProtocolRecord(&gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}}), providercore.OpenAIEndpointCapabilityLive))
	require.False(t, provideradapter.SupportsOpenAIEndpoint(gatewayprovider.ExecutionProtocolRecord(&gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}), providercore.OpenAIEndpointCapabilityLive))
	require.False(t, provideradapter.SupportsOpenAIEndpoint(gatewayprovider.ExecutionProtocolRecord(&gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth}}), providercore.OpenAIEndpointCapabilityLive))
	require.False(t, provideradapter.SupportsOpenAIEndpoint(gatewayprovider.ExecutionProtocolRecord(&gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI,
			Type: capability.ProviderTypeOAuth,
			Credentials: map[string]any{
				providercore.OpenAIAuthModeCredentialKey: providercore.OpenAIAuthModePersonalAccessToken,
			},
		},
	}), providercore.OpenAIEndpointCapabilityLive))
	require.False(t, provideradapter.SupportsOpenAIEndpoint(gatewayprovider.ExecutionProtocolRecord(&gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI,
			Type: capability.ProviderTypeOAuth,
			Credentials: map[string]any{
				providercore.OpenAIAuthModeCredentialKey: providercore.OpenAIAuthModeAgentIdentity,
			},
		},
	}), providercore.OpenAIEndpointCapabilityLive))
}

func TestValidateLiveCallRequestDoesNotRequireDelegation(t *testing.T) {
	request := &gatewaysession.LiveCallRequest{
		SDP:     "v=0\r\n",
		Session: json.RawMessage(`{"model":"gpt-live-test","instructions":"hello"}`),
	}
	require.NoError(t, openaicore.ValidateLiveCallRequest(request))
	require.NotContains(t, string(request.Session), "delegation")
}

func TestCreateUpstreamLiveCallPreservesSession(t *testing.T) {
	upstream := &liveHTTPUpstreamStub{}
	profileService, routerService := newLiveTLSRoutingServices()
	service := newLiveFixture(liveFixtureInputs{transport: upstream, profiles: profileService, routers: routerService})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 7,
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeOAuth,
			Concurrency: 2,
			Credentials: map[string]any{
				"access_token":       "test-access-token",
				"chatgpt_account_id": "acct_test",
			},
			Extra: map[string]any{
				"enable_tls_fingerprint":    true,
				"tls_fingerprint_router_id": int64(9),
			},
		},
	}
	session := json.RawMessage(`{
		"model":"gpt-live-test",
		"delegation":{"type":"client"},
		"custom":{"keep":true}
	}`)

	tlsRouterMatch := service.matchLiveTLSFingerprintRouter(provider, "test-live-client")
	created, err := service.createUpstreamLiveCall(context.Background(), provider, &gatewaysession.LiveCallRequest{
		SDP:     "v=offer\r\n",
		Session: session,
	}, `{"v":1,"s":0,"t":"v1.test"}`, tlsRouterMatch)
	require.NoError(t, err)
	require.Equal(t, "call_test", created.CallID)
	require.Equal(t, []byte("v=0\r\n"), created.SDP)
	require.NotNil(t, upstream.tlsProfile)
	require.Equal(t, "live-routed", upstream.tlsProfile.Name)

	var forwarded struct {
		SDP     string          `json:"sdp"`
		Session json.RawMessage `json:"session"`
	}
	require.NoError(t, json.Unmarshal(upstream.body, &forwarded))
	require.Equal(t, "v=offer\r\n", forwarded.SDP)
	require.JSONEq(t, string(session), string(forwarded.Session))
	require.Equal(t, "Bearer test-access-token", upstream.request.Header.Get("Authorization"))
	require.Equal(t, "acct_test", upstream.request.Header.Get("Chatgpt-Account-Id"))
	require.Equal(t, "quicksilver=v2", upstream.request.Header.Get("OpenAI-Alpha"))
	require.Equal(t, "codex_vscode/0.144.1 live-test", upstream.request.Header.Get("User-Agent"))
	require.Equal(t, "codex_vscode", upstream.request.Header.Get("Originator"))
	require.Equal(t, `{"v":1,"s":0,"t":"v1.test"}`, upstream.request.Header.Get(openai.LiveAttestationHeader))
	require.NotEmpty(t, upstream.request.Header.Get("Session-Id"))
	require.NotEmpty(t, upstream.request.Header.Get("Thread-Id"))
	require.Empty(t, upstream.request.Header.Get("OpenAI-Beta"))
	require.Equal(t, upstreamcore.HTTPUpstreamProfileOpenAI, upstreamcore.HTTPUpstreamProfileFromContext(upstream.request.Context()))
	require.True(t, upstreamcore.HTTPUpstreamRedirectsDisabled(upstream.request.Context()))
}

func TestLiveClientPolicyUsesTLSRouterMatch(t *testing.T) {
	_, routerService := newLiveTLSRoutingServices()
	service := newLiveFixture(liveFixtureInputs{routers: routerService})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI,
			Type: capability.ProviderTypeOAuth,
			Extra: map[string]any{
				"tls_fingerprint_router_id":  int64(9),
				"openai_oauth_client_policy": providercore.OpenAIOAuthClientPolicyTLSRouterMatchedOnly,
			},
		},
	}

	matched := service.matchLiveTLSFingerprintRouter(provider, "test-live-client")
	result := service.liveClientPolicyResult(
		context.Background(),
		provider,
		gatewaysession.LiveCallIdentity{UserAgent: "test-live-client"},
		matched,
	)
	require.True(t, result.Enabled)
	require.True(t, result.Matched)

	notMatched := service.matchLiveTLSFingerprintRouter(provider, "unknown-client")
	result = service.liveClientPolicyResult(
		context.Background(),
		provider,
		gatewaysession.LiveCallIdentity{UserAgent: "unknown-client"},
		notMatched,
	)
	require.True(t, result.Enabled)
	require.False(t, result.Matched)
	require.Equal(t, providercore.CodexClientRestrictionReasonNotMatchedTLSRouter, result.Reason)
}

func TestLiveAttestationCipherRoundTripAndRejectsOtherInstanceKey(t *testing.T) {
	first := openai.NewLiveAttestationCipher("first-live-secret")
	second := openai.NewLiveAttestationCipher("second-live-secret")
	require.NotNil(t, first)
	require.NotNil(t, second)

	ciphertext, err := first.Encrypt(`{"v":1,"s":0,"t":"v1.opaque"}`)
	require.NoError(t, err)
	require.NotContains(t, ciphertext, "opaque")

	plaintext, err := first.Decrypt(ciphertext)
	require.NoError(t, err)
	require.Equal(t, `{"v":1,"s":0,"t":"v1.opaque"}`, plaintext)

	_, err = second.Decrypt(ciphertext)
	require.Error(t, err)
}

func TestPrepareLiveAttestationEncryptsHeaderAndReturnsExplicitProviderError(t *testing.T) {
	cipher := openai.NewLiveAttestationCipher("live-attestation-test-secret")
	service := newLiveFixture(liveFixtureInputs{attestation: liveAttestationStub{header: `{"v":1,"s":0,"t":"v1.test"}`}, cipher: cipher})
	header, ciphertext, err := service.prepareLiveAttestation(context.Background())
	require.NoError(t, err)
	require.Equal(t, `{"v":1,"s":0,"t":"v1.test"}`, header)
	require.NotContains(t, ciphertext, "v1.test")
	decrypted, err := cipher.Decrypt(ciphertext)
	require.NoError(t, err)
	require.Equal(t, header, decrypted)

	service.Attestation = liveAttestationStub{err: errors.New("macOS app missing")}
	_, _, err = service.prepareLiveAttestation(context.Background())
	var unavailable *gatewaysession.LiveAttestationUnavailableError
	require.ErrorAs(t, err, &unavailable)
	require.Contains(t, unavailable.Error(), "macOS app missing")
}

func TestLiveMaxSessionDurationDefaultsAndOverrides(t *testing.T) {
	require.Equal(t, defaultLiveMaxSessionDuration, newLiveFixture(liveFixtureInputs{}).liveMaxSessionDuration())
	require.Equal(
		t,
		90*time.Second,
		newLiveFixture(liveFixtureInputs{duration: time.Duration(90) * time.Second}).liveMaxSessionDuration(),
	)
}

func TestLiveSidebandNormalCloseEndsCall(t *testing.T) {
	normalClose := coderws.CloseError{Code: coderws.StatusNormalClosure}
	require.ErrorIs(t, liveSidebandReadError(normalClose), gatewaysession.ErrLiveCallNotFound)

	abnormalClose := coderws.CloseError{Code: coderws.StatusInternalError}
	require.Equal(t, abnormalClose, liveSidebandReadError(abnormalClose))
}

func TestLiveCreateFailoverUsesExistingOpenAIPolicy(t *testing.T) {
	service := newLiveFixture(liveFixtureInputs{})
	require.False(t, service.shouldFailoverLiveCreateError(&forwardcore.UpstreamFailoverError{
		StatusCode:   http.StatusBadRequest,
		ResponseBody: []byte(`{"error":{"message":"invalid session"}}`),
	}))
	require.True(t, service.shouldFailoverLiveCreateError(&forwardcore.UpstreamFailoverError{
		StatusCode: http.StatusForbidden,
	}))
	require.True(t, service.shouldFailoverLiveCreateError(&forwardcore.UpstreamFailoverError{
		StatusCode: http.StatusBadGateway,
	}))
	require.True(t, service.shouldFailoverLiveCreateError(errors.New("transport failed")))
}

func TestLiveCallIDFromLocation(t *testing.T) {
	callID, err := openai.LiveCallIDFromLocation("https://chatgpt.com/backend-api/codex/call_123?intent=quicksilver")
	require.NoError(t, err)
	require.Equal(t, "call_123", callID)

	callID, err = openai.LiveCallIDFromLocation("/backend-api/codex/call_456")
	require.NoError(t, err)
	require.Equal(t, "call_456", callID)
}

func TestRequestTypeLive(t *testing.T) {
	require.True(t, usage.RequestTypeLive.IsValid())
	require.Equal(t, "live", usage.RequestTypeLive.String())
	parsed, err := usage.ParseUsageRequestType("live")
	require.NoError(t, err)
	require.Equal(t, usage.RequestTypeLive, parsed)
}
