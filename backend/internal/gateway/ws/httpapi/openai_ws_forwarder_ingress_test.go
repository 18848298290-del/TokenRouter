package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/gateway"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/gateway/media"
	"github.com/TokenFlux/TokenRouter/internal/gateway/moderationflow"
	"github.com/TokenFlux/TokenRouter/internal/gateway/promptpolicy"
	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/session/testkit"
	gatewaytestkit "github.com/TokenFlux/TokenRouter/internal/gateway/testkit"
	"github.com/TokenFlux/TokenRouter/internal/gateway/tierpolicy"
	"github.com/TokenFlux/TokenRouter/internal/gateway/ws"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/tlsfingerprint"
	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	settingscore "github.com/TokenFlux/TokenRouter/internal/settings"
	upstreamcore "github.com/TokenFlux/TokenRouter/internal/upstream"
	claude "github.com/TokenFlux/TokenRouter/internal/upstream/anthropic"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

type stagedPassthroughFrame struct {
	messageType websocket.MessageType
	payload     []byte
	err         error
}

type stagedPassthroughConn struct {
	frames    chan stagedPassthroughFrame
	writes    chan []byte
	closed    chan struct{}
	closeOnce sync.Once
}

type gatewayTTLSettingRepo struct {
	data map[string]string
}

type wsFixtureProviderStore struct {
	gatewayadapter.ExecutionProviderStore
	providers []gatewayadapter.ExecutionProvider
}

type openAIWSLeaseLossAfterReadConn struct {
	*openAIWSCaptureConn
	cancel context.CancelCauseFunc
	once   sync.Once
}

type openAIWSSingleConnDialer struct {
	conn openai.WSClientConn
}

type openAIWSQueueDialer struct {
	mu        sync.Mutex
	conns     []openai.WSClientConn
	dialCount int
}

type openAIWSPreflightFailConn struct {
	mu         sync.Mutex
	events     [][]byte
	pingFails  bool
	writeCount int
	pingCount  int
}

type openAIWSWriteFailAfterFirstTurnConn struct {
	mu          sync.Mutex
	events      [][]byte
	failOnWrite bool
}

// openAIWSIngressCapacityShedRepo accepts provider health writes for errors unrelated to capacity shedding.
type openAIWSIngressCapacityShedRepo struct {
	wsFixtureProviderStore
}

type openAIWSRateLimitSignalRepo struct {
	wsFixtureProviderStore
	rateLimitCalls []time.Time
	tempCalls      []time.Time
	errorCalls     []string
	updateExtra    []map[string]any
}

type openAIWS403CounterCacheStub struct {
	counts []int64
}

type openAIWSStatusErrorDialer struct {
	status int
	header http.Header
	err    error
}

// runtimeTestDialer records dial budgets and creates separate prewarm connections.
type runtimeTestDialer struct {
	mu      sync.Mutex
	conns   []openai.WSClientConn
	budgets []time.Duration
}

type stagedPassthroughDialer struct {
	conn openai.WSClientConn
}

func (c *stagedPassthroughConn) Send(payload string) {
	c.frames <- stagedPassthroughFrame{messageType: websocket.MessageText, payload: []byte(payload)}
}

func (c *stagedPassthroughConn) Fail(err error) {
	c.frames <- stagedPassthroughFrame{err: err}
}

func (c *stagedPassthroughConn) WriteJSON(context.Context, any) error { return nil }

func (c *stagedPassthroughConn) ReadMessage(ctx context.Context) ([]byte, error) {
	_, payload, err := c.ReadFrame(ctx)
	return payload, err
}

func (c *stagedPassthroughConn) Ping(context.Context) error { return nil }

func (c *stagedPassthroughConn) ReadFrame(ctx context.Context) (websocket.MessageType, []byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return websocket.MessageText, nil, ctx.Err()
	case <-c.closed:
		return websocket.MessageText, nil, openai.ErrWSConnClosed
	case frame := <-c.frames:
		return frame.messageType, append([]byte(nil), frame.payload...), frame.err
	}
}

func (c *stagedPassthroughConn) WriteFrame(ctx context.Context, _ websocket.MessageType, payload []byte) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.closed:
		return openai.ErrWSConnClosed
	default:
	}
	var parsed any
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return err
	}
	select {
	case c.writes <- append([]byte(nil), payload...):
	case <-ctx.Done():
		return ctx.Err()
	case <-c.closed:
		return openai.ErrWSConnClosed
	}
	return nil
}

func (c *stagedPassthroughConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}

func (r *gatewayTTLSettingRepo) Get(context.Context, string) (*settingscore.Setting, error) {
	return nil, settingscore.ErrSettingNotFound
}

func (r *gatewayTTLSettingRepo) GetValue(_ context.Context, key string) (string, error) {
	if r == nil {
		return "", settingscore.ErrSettingNotFound
	}
	v, ok := r.data[key]
	if !ok {
		return "", settingscore.ErrSettingNotFound
	}
	return v, nil
}

func (r *gatewayTTLSettingRepo) Set(_ context.Context, key, value string) error {
	if r == nil {
		return errors.New("setting repo is nil")
	}
	if r.data == nil {
		r.data = map[string]string{}
	}
	r.data[key] = value
	return nil
}

func (r *gatewayTTLSettingRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	result := make(map[string]string)
	if r == nil {
		return result, nil
	}
	for _, key := range keys {
		if v, ok := r.data[key]; ok {
			result[key] = v
		}
	}
	return result, nil
}

func (r *gatewayTTLSettingRepo) SetMultiple(_ context.Context, settings map[string]string) error {
	if r == nil {
		return errors.New("setting repo is nil")
	}
	if r.data == nil {
		r.data = map[string]string{}
	}
	maps.Copy(r.data, settings)
	return nil
}

func (r *gatewayTTLSettingRepo) GetAll(context.Context) (map[string]string, error) {
	result := make(map[string]string)
	if r == nil {
		return result, nil
	}
	maps.Copy(result, r.data)
	return result, nil
}

func (r *gatewayTTLSettingRepo) Delete(_ context.Context, key string) error {
	if r != nil {
		delete(r.data, key)
	}
	return nil
}

// TestWSResponseCreate_IngressFiltersServiceTierBeforeUpstream connects ingress execution to a capture connection.
// It checks that service_tier=fast is normalized and filtered by administrator policy before upstream writes.
func TestWSResponseCreate_IngressFiltersServiceTierBeforeUpstream(t *testing.T) {
	options := &wsFixtureOptions{}
	options.Request.URLPolicy.Enabled = false
	options.Request.URLPolicy.AllowInsecureHTTP = true

	options.Pool.MaxConnsPerProvider = 1
	options.Pool.MinIdlePerProvider = 0
	options.Pool.MaxIdlePerProvider = 1
	options.Pool.QueueLimitPerConn = 8
	options.WS.DialTimeoutSeconds = 3
	options.WS.ReadTimeoutSeconds = 3
	options.WS.WriteTimeoutSeconds = 3

	captureConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_ws_filter_1","model":"gpt-5.5","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	captureDialer := &openAIWSCaptureDialer{conn: captureConn}
	pool := newOpenAIWSConnPool(options)
	pool.SetClientDialerForTest(captureDialer)

	repo := &gatewaytestkit.FastPolicySettingsRepo{Values: map[string]string{}}
	filterPolicyJSON, err := json.Marshal(openAIFastFilterPriorityPolicy())
	require.NoError(t, err)
	repo.Values[gateway.SettingKeyOpenAIFastPolicySettings] = string(filterPolicyJSON)

	svc := newWSFixture(wsFixtureInputs{options: options, transport: &auxiliaryHTTPRecorder{}, cache: &testkit.StickyCache{}, corrector: openai.NewCodexToolCorrector(), pool: pool, readers: newExecutionReadersFixture(repo, options)})

	provider := &gatewayadapter.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 901,
			Name:        "openai-ws-filter",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Credentials: map[string]any{"api_key": "sk-test"},
			Extra: map[string]any{
				"responses_websockets_v2_enabled": true,
			},
		},
	}

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			CompressionMode: websocket.CompressionContextTakeover,
		})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() { _ = conn.CloseNow() }()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		_, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, provider, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := websocket.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() { _ = clientConn.CloseNow() }()

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	require.NoError(t, clientConn.Write(writeCtx, websocket.MessageText, []byte(`{"type":"response.create","model":"gpt-5.5","stream":false,"service_tier":"fast"}`)))
	cancelWrite()

	readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
	_, event, readErr := clientConn.Read(readCtx)
	cancelRead()
	require.NoError(t, readErr)
	require.Equal(t, "response.completed", gjson.GetBytes(event, "type").String())

	require.NoError(t, clientConn.Close(websocket.StatusNormalClosure, "done"))

	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 结束超时")
	}

	require.Len(t, captureConn.writes, 1, "上游应只收到一条 response.create")
	upstream := captureConn.writes[0]
	_, hasServiceTier := upstream["service_tier"]
	require.False(t, hasServiceTier, "上游收到的 response.create 不应包含 service_tier 字段（已被 fast policy filter 删除）")
	require.Equal(t, "response.create", upstream["type"])
	require.Equal(t, "gpt-5.5", upstream["model"])
}

// TestWSResponseCreate_IngressBlockSendsErrorEventAndSkipsUpstream checks a custom block rule
// through the ingress adapter. The client receives a Realtime-style error event
// before the policy close frame, and the upstream connection receives no request.
// This covers delivery behavior beyond the typed-error unit test.
func TestWSResponseCreate_IngressBlockSendsErrorEventAndSkipsUpstream(t *testing.T) {
	options := &wsFixtureOptions{}
	options.Request.URLPolicy.Enabled = false
	options.Request.URLPolicy.AllowInsecureHTTP = true

	options.Pool.MaxConnsPerProvider = 1
	options.Pool.MinIdlePerProvider = 0
	options.Pool.MaxIdlePerProvider = 1
	options.Pool.QueueLimitPerConn = 8
	options.WS.DialTimeoutSeconds = 3
	options.WS.ReadTimeoutSeconds = 3
	options.WS.WriteTimeoutSeconds = 3

	captureConn := &openAIWSCaptureConn{
		// No events queued; the upstream should never get written to anyway.
	}
	captureDialer := &openAIWSCaptureDialer{conn: captureConn}
	pool := newOpenAIWSConnPool(options)
	pool.SetClientDialerForTest(captureDialer)

	blockSettings := &tierpolicy.OpenAIFastPolicySettings{
		Rules: []tierpolicy.OpenAIFastPolicyRule{{
			ServiceTier:    tierpolicy.OpenAIFastTierPriority,
			Action:         claude.BetaPolicyActionBlock,
			Scope:          claude.BetaPolicyScopeAll,
			ErrorMessage:   "ws priority blocked for testing",
			ModelWhitelist: []string{"gpt-5.5"},
			FallbackAction: claude.BetaPolicyActionPass,
		}},
	}
	repo := &gatewaytestkit.FastPolicySettingsRepo{Values: map[string]string{}}
	raw, err := json.Marshal(blockSettings)
	require.NoError(t, err)
	repo.Values[gateway.SettingKeyOpenAIFastPolicySettings] = string(raw)

	svc := newWSFixture(wsFixtureInputs{options: options, transport: &auxiliaryHTTPRecorder{}, cache: &testkit.StickyCache{}, corrector: openai.NewCodexToolCorrector(), pool: pool, readers: newExecutionReadersFixture(repo, options)})

	provider := &gatewayadapter.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 902,
			Name:        "openai-ws-block",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Credentials: map[string]any{"api_key": "sk-test"},
			Extra: map[string]any{
				"responses_websockets_v2_enabled": true,
			},
		},
	}

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			CompressionMode: websocket.CompressionContextTakeover,
		})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() { _ = conn.CloseNow() }()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		_, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		proxyErr := svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, provider, "sk-test", firstMessage, nil)
		// Send the returned policy close status through a graceful handshake, as RunEntry does.
		// CloseNow alone tears down TCP before the client can observe the close status.
		// The second client read below checks for status 1008.
		if closeErr, ok := errors.AsType[*gatewayhttp.OpenAIWSClientCloseError](proxyErr); ok {
			_ = conn.Close(closeErr.StatusCode(), closeErr.Reason())
		}
		serverErrCh <- proxyErr
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := websocket.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() { _ = clientConn.CloseNow() }()

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	require.NoError(t, clientConn.Write(writeCtx, websocket.MessageText, []byte(`{"type":"response.create","model":"gpt-5.5","stream":false,"service_tier":"priority"}`)))
	cancelWrite()

	// Read the error event before checking the close frame.
	// The blocking write and subsequent close handshake must preserve that ordering.
	readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
	_, event, readErr := clientConn.Read(readCtx)
	cancelRead()
	require.NoError(t, readErr, "first read must succeed and return the error event before any close frame")
	require.Equal(t, "error", gjson.GetBytes(event, "type").String())
	require.Equal(t, "invalid_request_error", gjson.GetBytes(event, "error.type").String())
	// Clients need both a policy code and an event ID for correlation.
	require.Equal(t, "policy_violation", gjson.GetBytes(event, "error.code").String())
	require.NotEmpty(t, gjson.GetBytes(event, "event_id").String(), "event_id must be present so clients can correlate")
	require.Contains(t, gjson.GetBytes(event, "error.message").String(), "ws priority blocked for testing")

	// The next read must observe the policy close after the error event.
	readCtx2, cancelRead2 := context.WithTimeout(context.Background(), 3*time.Second)
	_, _, secondReadErr := clientConn.Read(readCtx2)
	cancelRead2()
	require.Error(t, secondReadErr, "after the error event the connection must surface a close")
	require.Equal(t, websocket.StatusPolicyViolation, websocket.CloseStatus(secondReadErr),
		"close status must be PolicyViolation; got %v", secondReadErr)

	select {
	case serverErr := <-serverErrCh:
		// The handler receives the typed close error used to send the policy close frame.
		require.Error(t, serverErr)
		var closeErr *gatewayhttp.OpenAIWSClientCloseError
		require.True(t, errors.As(serverErr, &closeErr), "block 应返回 OpenAIWSClientCloseError，得到 %T: %v", serverErr, serverErr)
		require.Equal(t, websocket.StatusPolicyViolation, closeErr.StatusCode())
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress 关闭超时")
	}

	// The blocked frame must not reach upstream.
	// The dial count can be zero or one depending on lease acquisition timing;
	// both cases must leave the upstream write list empty.
	require.Empty(t, captureConn.writes, "block 命中后上游不应收到 response.create")
}

// newOpenAIWSV2TestConfig supplies the sticky response TTL for network fixtures.
func newOpenAIWSV2TestConfig() *wsFixtureOptions {
	options := &wsFixtureOptions{}

	options.WS.StickyResponseIDTTLSeconds = 3600

	return options
}

func (r wsFixtureProviderStore) GetByID(_ context.Context, id int64) (*gatewayadapter.ExecutionProvider, error) {
	for i := range r.providers {
		if r.providers[i].Record.ID == id {
			return &r.providers[i], nil
		}
	}
	return nil, errors.New("provider not found")
}

func (c *openAIWSLeaseLossAfterReadConn) ReadMessage(ctx context.Context) ([]byte, error) {
	message, err := c.openAIWSCaptureConn.ReadMessage(ctx)
	if err == nil {
		c.once.Do(func() {
			c.cancel(scheduler.ErrOpenAIWSIngressLeaseLost)
		})
	}
	return message, err
}

func (d *openAIWSSingleConnDialer) Dial(
	ctx context.Context,
	wsURL string,
	headers http.Header,
	proxyURL string,
	tlsProfile *tlsfingerprint.Profile,
) (openai.WSClientConn, int, http.Header, error) {
	return d.conn, 0, nil, nil
}

// TestOpenAIWSDownstreamWriteContext_CancellationOwnership checks that lease cancellation does not interrupt a client-bound write.
func TestOpenAIWSDownstreamWriteContext_CancellationOwnership(t *testing.T) {
	t.Run("普通控制上下文已取消", func(t *testing.T) {
		controlCtx, cancelControl := context.WithCancelCause(context.Background())
		cancelControl(context.Canceled)

		writeCtx, cancelWrite := newOpenAIWSDownstreamWriteContext(controlCtx, nil, time.Second)
		defer cancelWrite()
		require.ErrorIs(t, writeCtx.Err(), context.Canceled)
	})

	t.Run("租约丢失时当前写入继续", func(t *testing.T) {
		lifecycleCtx, cancelLifecycle := context.WithCancelCause(context.Background())
		controlCtx, cancelControl := context.WithCancelCause(lifecycleCtx)
		hooks := &ws.OpenAIIngressHooks{ClientLifecycleContext: lifecycleCtx}
		writeCtx, cancelWrite := newOpenAIWSDownstreamWriteContext(controlCtx, hooks, time.Second)
		defer cancelWrite()

		cancelControl(scheduler.ErrOpenAIWSIngressLeaseLost)
		select {
		case <-writeCtx.Done():
			t.Fatalf("租约丢失不应取消当前下行写入: %v", writeCtx.Err())
		case <-time.After(20 * time.Millisecond):
		}

		clientDisconnected := errors.New("client disconnected")
		cancelLifecycle(clientDisconnected)
		<-writeCtx.Done()
		require.ErrorIs(t, context.Cause(writeCtx), clientDisconnected)
	})

	t.Run("客户端生命周期取消保留原因", func(t *testing.T) {
		lifecycleCtx, cancelLifecycle := context.WithCancelCause(context.Background())
		controlCtx, cancelControl := context.WithCancelCause(lifecycleCtx)
		defer cancelControl(context.Canceled)
		hooks := &ws.OpenAIIngressHooks{ClientLifecycleContext: lifecycleCtx}
		writeCtx, cancelWrite := newOpenAIWSDownstreamWriteContext(controlCtx, hooks, time.Second)
		defer cancelWrite()

		serverShutdown := errors.New("server shutdown")
		cancelLifecycle(serverShutdown)
		require.ErrorIs(t, writeCtx.Err(), context.Canceled)
		require.ErrorIs(t, context.Cause(writeCtx), serverShutdown)
	})
}

// TestOpenAIWSImageIntentForRoutingModel checks image intent against group-mapped model G.
func TestOpenAIWSImageIntentForRoutingModel(t *testing.T) {
	tests := []struct {
		name          string
		routingModel  string
		upstreamModel string
		wantIntent    bool
		wantExplicit  bool
	}{
		{name: "分组普通模型映射为上游生图模型", routingModel: "gpt-5.4", upstreamModel: "gpt-image-1"},
		{name: "分组生图模型映射为上游普通模型", routingModel: "gpt-image-1", upstreamModel: "gpt-5.4", wantIntent: true, wantExplicit: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(`{"model":"` + tt.upstreamModel + `","input":"draw"}`)
			intentBody, imageIntent, explicitImageIntent := openAIWSImageIntentForRoutingModel(tt.routingModel, tt.upstreamModel, body, capability.PlatformOpenAI)

			require.Equal(t, tt.routingModel, gjson.GetBytes(intentBody, "model").String())
			require.Equal(t, tt.wantIntent, imageIntent)
			require.Equal(t, tt.wantExplicit, explicitImageIntent)
		})
	}
}

func TestOpenAIWSImageIntentForRoutingModel_PassiveNamespaceIsNotExplicit(t *testing.T) {
	body := []byte(`{
		"model":"gpt-5.5",
		"input":"write code",
		"tools":[{"type":"namespace","name":"image_gen","tools":[{"type":"function","name":"imagegen"}]}],
		"tool_choice":"auto"
	}`)

	_, imageIntent, explicitImageIntent := openAIWSImageIntentForRoutingModel("gpt-5.5", "gpt-5.5", body, capability.PlatformOpenAI)

	require.True(t, imageIntent)
	require.False(t, explicitImageIntent)
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_KeepLeaseAcrossTurns(t *testing.T) {
	options := &wsFixtureOptions{}
	options.Request.URLPolicy.Enabled = false
	options.Request.URLPolicy.AllowInsecureHTTP = true

	options.Pool.MaxConnsPerProvider = 1
	options.Pool.MinIdlePerProvider = 0
	options.Pool.MaxIdlePerProvider = 1
	options.Pool.QueueLimitPerConn = 8
	options.WS.DialTimeoutSeconds = 3
	options.WS.ReadTimeoutSeconds = 3
	options.WS.WriteTimeoutSeconds = 3

	captureConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.output_item.done","item":{"id":"ig_ingress_1","type":"image_generation_call","status":"generating","result":"iVBORw0KGgoAAAANSUhEUg/+=="}}`),
			[]byte(`{"type":"response.completed","response":{"id":"resp_ingress_turn_1","model":"upstream-turn-1","usage":{"input_tokens":1,"output_tokens":1}}}`),
			[]byte(`{"type":"response.completed","response":{"id":"resp_ingress_turn_2","model":"upstream-turn-2","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	captureDialer := &openAIWSCaptureDialer{conn: captureConn}
	pool := newOpenAIWSConnPool(options)
	pool.SetClientDialerForTest(captureDialer)

	svc := newWSFixture(wsFixtureInputs{options: options, transport: &auxiliaryHTTPRecorder{}, cache: &testkit.StickyCache{}, corrector: openai.NewCodexToolCorrector(), pool: pool})

	provider := &gatewayadapter.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 114,
			Name:        "openai-ingress-session-lease",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Credentials: map[string]any{
				"api_key": "sk-test",
				"model_mapping": map[string]any{
					"channel-turn-1": "upstream-turn-1",
					"channel-turn-2": "upstream-turn-2",
				},
				"model_whitelist": []any{"upstream-turn-1", "upstream-turn-2"},
			},
			Extra: map[string]any{
				"responses_websockets_v2_enabled": true,
			},
		},
	}

	serverErrCh := make(chan error, 1)
	turnTerminalCh := make(chan string, 2)
	turnEffortCh := make(chan string, 1)
	routingCalls := make([]string, 0, 2)
	hooks := &ws.OpenAIIngressHooks{
		ResolveRoutingModel: func(turn int, requestedModel string, _ []byte) (string, error) {
			routingCalls = append(routingCalls, fmt.Sprintf("%d:%s", turn, requestedModel))
			switch requestedModel {
			case "client-turn-1":
				return "channel-turn-1", nil
			case "client-turn-2":
				return "channel-turn-2", nil
			default:
				return "", fmt.Errorf("unexpected requested model: %s", requestedModel)
			}
		},
		AfterTurn: func(capture ws.OpenAITurnCapture) {
			result := capture.Result
			turnErr := capture.Err
			if turnErr == nil && result != nil {
				turnTerminalCh <- result.UpstreamTerminalEvent
				if result.ReasoningEffort != nil {
					turnEffortCh <- *result.ReasoningEffort
				}
			}
		},
	}
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			CompressionMode: websocket.CompressionContextTakeover,
		})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			_ = conn.CloseNow()
		}()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != websocket.MessageText && msgType != websocket.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, provider, "sk-test", firstMessage, hooks)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := websocket.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeMessage := func(payload string) {
		writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, clientConn.Write(writeCtx, websocket.MessageText, []byte(payload)))
	}
	readMessage := func() []byte {
		readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		msgType, message, readErr := clientConn.Read(readCtx)
		require.NoError(t, readErr)
		require.Equal(t, websocket.MessageText, msgType)
		return message
	}

	writeMessage(`{"type":"response.create","model":"client-turn-1","reasoning":{"effort":"max"},"stream":false}`)
	firstTurnImageEvent := readMessage()
	require.Equal(t, "response.output_item.done", gjson.GetBytes(firstTurnImageEvent, "type").String())
	require.Equal(t, "completed", gjson.GetBytes(firstTurnImageEvent, "item.status").String())
	require.Equal(t, "iVBORw0KGgoAAAANSUhEUg/+==", gjson.GetBytes(firstTurnImageEvent, "item.result").String())
	firstTurnEvent := readMessage()
	require.Equal(t, "response.completed", gjson.GetBytes(firstTurnEvent, "type").String())
	require.Equal(t, "resp_ingress_turn_1", gjson.GetBytes(firstTurnEvent, "response.id").String())
	require.Equal(t, "client-turn-1", gjson.GetBytes(firstTurnEvent, "response.model").String())

	writeMessage(`{"type":"response.create","model":"client-turn-2","stream":false,"previous_response_id":"resp_ingress_turn_1"}`)
	secondTurnEvent := readMessage()
	require.Equal(t, "response.completed", gjson.GetBytes(secondTurnEvent, "type").String())
	require.Equal(t, "resp_ingress_turn_2", gjson.GetBytes(secondTurnEvent, "response.id").String())
	require.Equal(t, "client-turn-2", gjson.GetBytes(secondTurnEvent, "response.model").String())
	require.Equal(t, "response.completed", <-turnTerminalCh, "首轮 turn 应保留成功终态")
	require.Equal(t, "response.completed", <-turnTerminalCh, "第二轮 turn 应保留成功终态")
	require.Equal(t, "max", <-turnEffortCh, "WS v2 应记录第三方模型显式 max")

	_ = clientConn.Close(websocket.StatusNormalClosure, "done")

	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 结束超时")
	}

	metrics := svc.SnapshotOpenAIWSPoolMetrics()
	require.Equal(t, int64(1), metrics.AcquireTotal, "同一 ingress 会话多 turn 应只获取一次上游 lease")
	require.Equal(t, 1, captureDialer.DialCount(), "同一 ingress 会话应保持同一上游连接")
	require.Len(t, captureConn.writes, 2, "应向同一上游连接发送两轮 response.create")
	require.Equal(t, "upstream-turn-1", fmt.Sprint(captureConn.writes[0]["model"]))
	require.Equal(t, "upstream-turn-2", fmt.Sprint(captureConn.writes[1]["model"]))
	require.Equal(t, []string{"1:client-turn-1", "2:client-turn-2"}, routingCalls)
}

// TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_LeaseLossSendsRetryClose
// checks terminal-event delivery before status 1013 when the lease is lost after the terminal read.
func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_LeaseLossSendsRetryClose(t *testing.T) {
	options := &wsFixtureOptions{}
	options.Request.URLPolicy.Enabled = false
	options.Request.URLPolicy.AllowInsecureHTTP = true

	options.Pool.MaxConnsPerProvider = 1
	options.Pool.MinIdlePerProvider = 0
	options.Pool.MaxIdlePerProvider = 1
	options.Pool.QueueLimitPerConn = 8
	options.WS.DialTimeoutSeconds = 3
	options.WS.ReadTimeoutSeconds = 3
	options.WS.WriteTimeoutSeconds = 3

	lifecycleCtx, cancelLifecycle := context.WithCancelCause(context.Background())
	defer cancelLifecycle(context.Canceled)
	controlCtx, cancelControl := context.WithCancelCause(lifecycleCtx)
	upstreamConn := &openAIWSLeaseLossAfterReadConn{
		openAIWSCaptureConn: &openAIWSCaptureConn{events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_lease_loss","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		}},
		cancel: cancelControl,
	}
	pool := newOpenAIWSConnPool(options)
	pool.SetClientDialerForTest(&openAIWSSingleConnDialer{conn: upstreamConn})
	defer pool.Close()

	svc := newWSFixture(wsFixtureInputs{options: options, transport: &auxiliaryHTTPRecorder{}, cache: &testkit.StickyCache{}, corrector: openai.NewCodexToolCorrector(), pool: pool})
	provider := &gatewayadapter.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 118,
			Name:        "openai-ingress-lease-loss",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Credentials: map[string]any{"api_key": "sk-test"},
			Extra:       map[string]any{"responses_websockets_v2_enabled": true},
		},
	}

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionContextTakeover})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() { _ = conn.CloseNow() }()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(controlCtx)
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = req

		readCtx, cancelRead := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancelRead()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != websocket.MessageText && msgType != websocket.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(
			controlCtx,
			ginCtx,
			conn,
			provider,
			"sk-test",
			firstMessage,
			&ws.OpenAIIngressHooks{ClientLifecycleContext: lifecycleCtx},
		)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := websocket.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() { _ = clientConn.CloseNow() }()

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, websocket.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","stream":false}`))
	cancelWrite()
	require.NoError(t, err)

	readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
	msgType, event, err := clientConn.Read(readCtx)
	cancelRead()
	require.NoError(t, err)
	require.Equal(t, websocket.MessageText, msgType)
	require.Equal(t, "response.completed", gjson.GetBytes(event, "type").String())

	closeReadCtx, cancelCloseRead := context.WithTimeout(context.Background(), 3*time.Second)
	_, _, err = clientConn.Read(closeReadCtx)
	cancelCloseRead()
	var closeErr websocket.CloseError
	require.ErrorAs(t, err, &closeErr)
	require.Equal(t, websocket.StatusTryAgainLater, closeErr.Code)
	require.Equal(t, "websocket ingress capacity lease lost; please reconnect", closeErr.Reason)

	select {
	case serverErr := <-serverErrCh:
		var clientCloseErr *gatewayhttp.OpenAIWSClientCloseError
		require.ErrorAs(t, serverErr, &clientCloseErr)
		require.Equal(t, websocket.StatusTryAgainLater, clientCloseErr.StatusCode())
		require.ErrorIs(t, serverErr, scheduler.ErrOpenAIWSIngressLeaseLost)
	case <-time.After(3 * time.Second):
		t.Fatal("等待 ingress 租约丢失读协程退出超时")
	}
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_IdleTimeoutReleasesStoreDisabledSession(t *testing.T) {
	options := &wsFixtureOptions{}
	options.Request.URLPolicy.Enabled = false
	options.Request.URLPolicy.AllowInsecureHTTP = true

	options.WS.IngressInterTurnIdleTimeoutSeconds = 1
	options.Pool.MaxConnsPerProvider = 1
	options.Pool.MinIdlePerProvider = 0
	options.Pool.MaxIdlePerProvider = 1
	options.Pool.QueueLimitPerConn = 8
	options.WS.DialTimeoutSeconds = 3
	options.WS.ReadTimeoutSeconds = 3
	options.WS.WriteTimeoutSeconds = 3

	captureConn := &openAIWSCaptureConn{events: [][]byte{
		[]byte(`{"type":"response.completed","response":{"id":"resp_idle_timeout","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
	}}
	captureDialer := &openAIWSCaptureDialer{conn: captureConn}
	pool := newOpenAIWSConnPool(options)
	pool.SetClientDialerForTest(captureDialer)
	defer pool.Close()
	svc := newWSFixture(wsFixtureInputs{options: options, transport: &auxiliaryHTTPRecorder{}, cache: &testkit.StickyCache{}, corrector: openai.NewCodexToolCorrector(), pool: pool})
	provider := &gatewayadapter.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 116,
			Name:        "openai-ingress-idle-timeout",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Credentials: map[string]any{"api_key": "sk-test"},
			Extra:       map[string]any{"responses_websockets_v2_enabled": true},
		},
	}

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionContextTakeover})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() { _ = conn.CloseNow() }()

		readCtx, cancelRead := context.WithTimeout(r.Context(), 3*time.Second)
		_, firstMessage, err := conn.Read(readCtx)
		cancelRead()
		if err != nil {
			serverErrCh <- err
			return
		}
		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		ginCtx.Request = r.Clone(r.Context())
		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, provider, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := websocket.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() { _ = clientConn.CloseNow() }()

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, websocket.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false}`))
	cancelWrite()
	require.NoError(t, err)

	readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
	_, event, err := clientConn.Read(readCtx)
	cancelRead()
	require.NoError(t, err)
	require.Equal(t, "response.completed", gjson.GetBytes(event, "type").String())

	closeReadCtx, cancelCloseRead := context.WithTimeout(context.Background(), 3*time.Second)
	_, _, err = clientConn.Read(closeReadCtx)
	cancelCloseRead()
	var clientClose websocket.CloseError
	require.ErrorAs(t, err, &clientClose)
	require.Equal(t, websocket.StatusNormalClosure, clientClose.Code)
	require.Equal(t, "websocket idle timeout", clientClose.Reason)

	select {
	case proxyErr := <-serverErrCh:
		var closeErr *gatewayhttp.OpenAIWSClientCloseError
		require.ErrorAs(t, proxyErr, &closeErr)
		require.Equal(t, websocket.StatusNormalClosure, closeErr.StatusCode())
		require.Equal(t, "websocket idle timeout", closeErr.Reason())
	case <-time.After(4 * time.Second):
		t.Fatal("timed out waiting for idle ingress session to close")
	}

	state, ok := pool.SnapshotProviderState(provider.Record.ID)
	require.True(t, ok)
	require.Zero(t, state.PinnedConnections, "idle close must unpin a store=false session")
	require.Zero(t, state.LeasedConnections, "idle close must release every upstream lease")
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_FollowupCreateCanOmitModel(t *testing.T) {
	options := &wsFixtureOptions{}
	options.Request.URLPolicy.Enabled = false
	options.Request.URLPolicy.AllowInsecureHTTP = true

	options.Pool.MaxConnsPerProvider = 1
	options.Pool.MinIdlePerProvider = 0
	options.Pool.MaxIdlePerProvider = 1
	options.Pool.QueueLimitPerConn = 8
	options.WS.DialTimeoutSeconds = 3
	options.WS.ReadTimeoutSeconds = 3
	options.WS.WriteTimeoutSeconds = 3

	captureConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_omit_model_1","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
			[]byte(`{"type":"response.completed","response":{"id":"resp_omit_model_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	captureDialer := &openAIWSCaptureDialer{conn: captureConn}
	pool := newOpenAIWSConnPool(options)
	pool.SetClientDialerForTest(captureDialer)
	svc := newWSFixture(wsFixtureInputs{options: options, transport: &auxiliaryHTTPRecorder{}, cache: &testkit.StickyCache{}, corrector: openai.NewCodexToolCorrector(), pool: pool})
	provider := &gatewayadapter.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 115,
			Name:        "openai-ingress-omit-model",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Credentials: map[string]any{
				"api_key": "sk-test",
				"model_mapping": map[string]any{
					"client-model": "gpt-5.1",
				},
			},
			Extra: map[string]any{
				"responses_websockets_v2_enabled": true,
			},
		},
	}

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			CompressionMode: websocket.CompressionContextTakeover,
		})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			_ = conn.CloseNow()
		}()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != websocket.MessageText && msgType != websocket.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, provider, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := websocket.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, websocket.MessageText, []byte(`{"type":"response.create","model":"client-model","stream":false}`))
	cancelWrite()
	require.NoError(t, err)

	readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
	_, firstEvent, readErr := clientConn.Read(readCtx)
	cancelRead()
	require.NoError(t, readErr)
	require.Equal(t, "resp_omit_model_1", gjson.GetBytes(firstEvent, "response.id").String())

	writeCtx, cancelWrite = context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, websocket.MessageText, []byte(`{"type":"response.create","stream":false,"previous_response_id":"resp_omit_model_1"}`))
	cancelWrite()
	require.NoError(t, err)

	readCtx, cancelRead = context.WithTimeout(context.Background(), 3*time.Second)
	_, secondEvent, readErr := clientConn.Read(readCtx)
	cancelRead()
	require.NoError(t, readErr)
	require.Equal(t, "resp_omit_model_2", gjson.GetBytes(secondEvent, "response.id").String())
	_ = clientConn.Close(websocket.StatusNormalClosure, "done")

	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 结束超时")
	}

	require.Len(t, captureConn.writes, 2)
	require.Equal(t, "gpt-5.1", gjson.Get(requestToJSONString(captureConn.writes[0]), "model").String())
	require.Equal(t, "gpt-5.1", gjson.Get(requestToJSONString(captureConn.writes[1]), "model").String())
	require.Equal(t, "resp_omit_model_1", gjson.Get(requestToJSONString(captureConn.writes[1]), "previous_response_id").String())
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_ReplacesFollowupUserPrompt(t *testing.T) {
	promptpolicy.SharedCache().Store((*promptpolicy.CompiledConfig)(nil))
	t.Cleanup(func() {
		promptpolicy.SharedCache().Store((*promptpolicy.CompiledConfig)(nil))
	})

	options := &wsFixtureOptions{}
	options.Request.URLPolicy.Enabled = false
	options.Request.URLPolicy.AllowInsecureHTTP = true

	options.Pool.MaxConnsPerProvider = 1
	options.Pool.MinIdlePerProvider = 0
	options.Pool.MaxIdlePerProvider = 1
	options.Pool.QueueLimitPerConn = 8
	options.WS.DialTimeoutSeconds = 3
	options.WS.ReadTimeoutSeconds = 3
	options.WS.WriteTimeoutSeconds = 3

	replacementConfig := `{"enabled":true,"rules":[{"id":"replace-shanghai-timezone","name":"替换上海时区","enabled":true,"pattern":"(Asia/Shanghai)","target_group":1,"replacement_type":"static","static_text":"Asia/Tokyo"}]}`
	settingService := newExecutionReadersFixture(&gatewayTTLSettingRepo{data: map[string]string{
		promptpolicy.SettingKeyUserPromptReplacementConfig: replacementConfig,
	}}, options)

	captureConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_replace_followup_1","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
			[]byte(`{"type":"response.completed","response":{"id":"resp_replace_followup_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	captureDialer := &openAIWSCaptureDialer{conn: captureConn}
	pool := newOpenAIWSConnPool(options)
	pool.SetClientDialerForTest(captureDialer)
	svc := newWSFixture(wsFixtureInputs{options: options, transport: &auxiliaryHTTPRecorder{}, cache: &testkit.StickyCache{}, readers: settingService, prompts: promptpolicy.New(settingService.Scheduler, settingscore.ErrSettingNotFound, slog.Warn), corrector: openai.NewCodexToolCorrector(), pool: pool})
	provider := &gatewayadapter.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 116,
			Name:        "openai-ingress-user-prompt-replacement",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Credentials: map[string]any{
				"api_key": "sk-test",
			},
			Extra: map[string]any{
				"responses_websockets_v2_enabled": true,
			},
		},
	}

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			CompressionMode: websocket.CompressionContextTakeover,
		})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			_ = conn.CloseNow()
		}()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != websocket.MessageText && msgType != websocket.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, provider, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := websocket.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, websocket.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","stream":false}`))
	cancelWrite()
	require.NoError(t, err)

	readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
	_, firstEvent, readErr := clientConn.Read(readCtx)
	cancelRead()
	require.NoError(t, readErr)
	require.Equal(t, "resp_replace_followup_1", gjson.GetBytes(firstEvent, "response.id").String())

	writeCtx, cancelWrite = context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, websocket.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","stream":false,"previous_response_id":"resp_replace_followup_1","input":[{"role":"user","content":[{"type":"input_text","text":"timezone is Asia/Shanghai"}]}]}`))
	cancelWrite()
	require.NoError(t, err)

	readCtx, cancelRead = context.WithTimeout(context.Background(), 3*time.Second)
	_, secondEvent, readErr := clientConn.Read(readCtx)
	cancelRead()
	require.NoError(t, readErr)
	require.Equal(t, "resp_replace_followup_2", gjson.GetBytes(secondEvent, "response.id").String())
	_ = clientConn.Close(websocket.StatusNormalClosure, "done")

	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 结束超时")
	}

	require.Len(t, captureConn.writes, 2)
	secondWrite := requestToJSONString(captureConn.writes[1])
	require.Contains(t, secondWrite, "Asia/Tokyo")
	require.NotContains(t, secondWrite, "Asia/Shanghai")
	require.Equal(t, "resp_replace_followup_1", gjson.Get(secondWrite, "previous_response_id").String())
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_CodexImageBridgeRespectsResponsesLite(t *testing.T) {
	options := &wsFixtureOptions{}
	options.Request.URLPolicy.Enabled = false
	options.Request.URLPolicy.AllowInsecureHTTP = true

	options.Pool.MaxConnsPerProvider = 1
	options.Pool.MinIdlePerProvider = 0
	options.Pool.MaxIdlePerProvider = 1
	options.Pool.QueueLimitPerConn = 8
	options.WS.DialTimeoutSeconds = 3
	options.WS.ReadTimeoutSeconds = 3
	options.WS.WriteTimeoutSeconds = 3

	captureConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_codex_image_bridge","model":"gpt-5.5","usage":{"input_tokens":1,"output_tokens":1}}}`),
			[]byte(`{"type":"response.completed","response":{"id":"resp_codex_image_lite","model":"gpt-5.5","usage":{"input_tokens":1,"output_tokens":1}}}`),
			[]byte(`{"type":"response.completed","response":{"id":"resp_codex_image_function","model":"gpt-5.5","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	captureDialer := &openAIWSCaptureDialer{conn: captureConn}
	pool := newOpenAIWSConnPool(options)
	pool.SetClientDialerForTest(captureDialer)

	svc := newWSFixture(wsFixtureInputs{options: options, transport: &auxiliaryHTTPRecorder{}, cache: &testkit.StickyCache{}, corrector: openai.NewCodexToolCorrector(), pool: pool})

	groupID := int64(3)
	apiKey := &apikey.APIKey{
		ID:      1,
		UserID:  1,
		GroupID: &groupID,
		Group: &routing.Group{
			ID:                   groupID,
			AllowImageGeneration: true,
		},
	}
	provider := &gatewayadapter.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 31,
			Name:        "openai-codex-image-ws",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeOAuth,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Credentials: map[string]any{
				"access_token": "test-token",
			},
			Extra: map[string]any{
				"openai_oauth_responses_websockets_v2_enabled": true,
				"codex_image_generation_bridge":                true,
			},
		},
	}

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			CompressionMode: websocket.CompressionContextTakeover,
		})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			_ = conn.CloseNow()
		}()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "codex_cli_rs/0.98.0")
		ginCtx.Request = req
		ginCtx.Set("api_key", apiKey)

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != websocket.MessageText && msgType != websocket.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, provider, "test-token", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := websocket.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, websocket.MessageText, []byte(`{"type":"response.create","model":"gpt-5.5","stream":false,"parallel_tool_calls":true,"input":"draw a cat","sequence":900719925474099312345}`))
	cancelWrite()
	require.NoError(t, err)

	readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
	msgType, message, err := clientConn.Read(readCtx)
	cancelRead()
	require.NoError(t, err)
	require.Equal(t, websocket.MessageText, msgType)
	require.Equal(t, "resp_codex_image_bridge", gjson.GetBytes(message, "response.id").String())

	writeCtx, cancelWrite = context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, websocket.MessageText, []byte(`{
		"type":"response.create",
		"model":"gpt-5.5",
		"stream":false,
		"previous_response_id":"resp_codex_image_bridge",
		"parallel_tool_calls":true,
		"reasoning":{"effort":"high"},
		"client_metadata":{"ws_request_header_x_openai_internal_codex_responses_lite":"true"},
		"tools":[{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"spawn_agent"}]}],
		"input":[
			{"type":"additional_tools","role":"developer","tools":[{"type":"custom","name":"exec","description":"Execute code-mode tools, including image_gen.imagegen."}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"draw a cat"}]}
		],
		"tool_choice":{"type":"namespace","name":"collaboration"}
	}`))
	cancelWrite()
	require.NoError(t, err)

	readCtx, cancelRead = context.WithTimeout(context.Background(), 3*time.Second)
	msgType, message, err = clientConn.Read(readCtx)
	cancelRead()
	require.NoError(t, err)
	require.Equal(t, websocket.MessageText, msgType)
	require.Equal(t, "resp_codex_image_lite", gjson.GetBytes(message, "response.id").String())

	writeCtx, cancelWrite = context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, websocket.MessageText, []byte(`{
		"type":"response.create",
		"model":"gpt-5.5",
		"stream":false,
		"previous_response_id":"resp_codex_image_lite",
		"input":"draw a cat",
		"tools":[{"type":"function","name":"image_gen.imagegen","parameters":{"type":"object"}}]
	}`))
	cancelWrite()
	require.NoError(t, err)

	readCtx, cancelRead = context.WithTimeout(context.Background(), 3*time.Second)
	msgType, message, err = clientConn.Read(readCtx)
	cancelRead()
	require.NoError(t, err)
	require.Equal(t, websocket.MessageText, msgType)
	require.Equal(t, "resp_codex_image_function", gjson.GetBytes(message, "response.id").String())

	writeCtx, cancelWrite = context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, websocket.MessageText, []byte(`{
		"type":"response.create",
		"model":"gpt-5.5",
		"parallel_tool_calls":"false",
		"client_metadata":{"ws_request_header_x_openai_internal_codex_responses_lite":"true"},
		"tools":[{"type":"function","name":"shell"}]
	}`))
	cancelWrite()
	require.NoError(t, err)

	select {
	case serverErr := <-serverErrCh:
		var closeErr *gatewayhttp.OpenAIWSClientCloseError
		require.ErrorAs(t, serverErr, &closeErr)
		require.Equal(t, websocket.StatusPolicyViolation, closeErr.StatusCode())
		require.Contains(t, closeErr.Reason(), "parallel_tool_calls to be a boolean")
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 结束超时")
	}

	require.Len(t, captureConn.writes, 3)
	nonLitePayload := requestToJSONString(captureConn.writes[0])
	require.True(t, gjson.Get(nonLitePayload, "parallel_tool_calls").Bool())
	require.True(t, gjson.Get(nonLitePayload, `tools.#(type=="image_generation")`).Exists())
	require.Equal(t, "png", gjson.Get(nonLitePayload, `tools.#(type=="image_generation").output_format`).String())
	require.Equal(t, "auto", gjson.Get(nonLitePayload, "tool_choice").String())
	require.Contains(t, gjson.Get(nonLitePayload, "instructions").String(), "image_generation")
	require.False(t, gjson.Get(nonLitePayload, "reasoning.context").Exists())
	require.True(t, gjson.Get(nonLitePayload, "parallel_tool_calls").Bool())
	require.Equal(t, "900719925474099312345", gjson.Get(nonLitePayload, "sequence").Raw)

	litePayload := requestToJSONString(captureConn.writes[1])
	require.True(t, gjson.Get(litePayload, "parallel_tool_calls").Exists())
	require.False(t, gjson.Get(litePayload, "parallel_tool_calls").Bool())
	require.False(t, gjson.Get(litePayload, `tools.#(type=="image_generation")`).Exists())
	require.NotContains(t, gjson.Get(litePayload, "instructions").String(), "image_generation")
	require.Equal(t, "exec", gjson.Get(litePayload, `input.#(type=="additional_tools").tools.0.name`).String())
	require.Contains(t, gjson.Get(litePayload, `input.#(type=="additional_tools").tools.0.description`).String(), "image_gen.imagegen")
	require.False(t, gjson.Get(litePayload, `tools.#(type=="namespace")`).Exists())
	require.Equal(t, "collaboration", gjson.Get(litePayload, `input.#(type=="additional_tools").tools.1.name`).String())
	require.Equal(t, "namespace", gjson.Get(litePayload, "tool_choice.type").String())
	require.Equal(t, "collaboration", gjson.Get(litePayload, "tool_choice.name").String())
	require.Equal(t, "high", gjson.Get(litePayload, "reasoning.effort").String())
	require.Equal(t, "all_turns", gjson.Get(litePayload, "reasoning.context").String())
	require.True(t, gjson.Get(litePayload, "parallel_tool_calls").Exists())
	require.False(t, gjson.Get(litePayload, "parallel_tool_calls").Bool())

	functionPayload := requestToJSONString(captureConn.writes[2])
	require.True(t, gjson.Get(functionPayload, `tools.#(name=="image_gen.imagegen")`).Exists())
	require.False(t, gjson.Get(functionPayload, `tools.#(type=="image_generation")`).Exists())
	require.False(t, gjson.Get(functionPayload, "tool_choice").Exists())
	require.NotContains(t, gjson.Get(functionPayload, "instructions").String(), openai.CodexImageGenerationBridgeMarker)
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_DedicatedModeDoesNotReuseConnAcrossSessions(t *testing.T) {
	options := &wsFixtureOptions{}
	options.Request.URLPolicy.Enabled = false
	options.Request.URLPolicy.AllowInsecureHTTP = true

	options.Pool.MaxConnsPerProvider = 1
	options.Pool.MinIdlePerProvider = 0
	options.Pool.MaxIdlePerProvider = 1
	options.Pool.QueueLimitPerConn = 8
	options.WS.DialTimeoutSeconds = 3
	options.WS.ReadTimeoutSeconds = 3
	options.WS.WriteTimeoutSeconds = 3

	upstreamConn1 := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_dedicated_1","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	upstreamConn2 := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_dedicated_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	dialer := &openAIWSQueueDialer{
		conns: []openai.WSClientConn{upstreamConn1, upstreamConn2},
	}
	pool := newOpenAIWSConnPool(options)
	pool.SetClientDialerForTest(dialer)

	svc := newWSFixture(wsFixtureInputs{options: options, transport: &auxiliaryHTTPRecorder{}, cache: &testkit.StickyCache{}, corrector: openai.NewCodexToolCorrector(), pool: pool})

	provider := &gatewayadapter.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 441,
			Name:        "openai-ingress-dedicated",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Credentials: map[string]any{
				"api_key": "sk-test",
			},
			Extra: map[string]any{
				"responses_ws_connection_mode": "pooled",
			},
		},
	}

	serverErrCh := make(chan error, 2)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			CompressionMode: websocket.CompressionContextTakeover,
		})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			_ = conn.CloseNow()
		}()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != websocket.MessageText && msgType != websocket.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, provider, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	runSingleTurnSession := func(expectedResponseID string) {
		dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
		clientConn, _, err := websocket.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
		cancelDial()
		require.NoError(t, err)
		defer func() {
			_ = clientConn.CloseNow()
		}()

		writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
		err = clientConn.Write(writeCtx, websocket.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","stream":false}`))
		cancelWrite()
		require.NoError(t, err)

		readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
		msgType, event, readErr := clientConn.Read(readCtx)
		cancelRead()
		require.NoError(t, readErr)
		require.Equal(t, websocket.MessageText, msgType)
		require.Equal(t, expectedResponseID, gjson.GetBytes(event, "response.id").String())

		require.NoError(t, clientConn.Close(websocket.StatusNormalClosure, "done"))

		select {
		case serverErr := <-serverErrCh:
			require.NoError(t, serverErr)
		case <-time.After(5 * time.Second):
			t.Fatal("等待 ingress websocket 结束超时")
		}
	}

	runSingleTurnSession("resp_dedicated_1")
	runSingleTurnSession("resp_dedicated_2")

	require.Equal(t, 2, dialer.DialCount(), "dedicated 模式下跨客户端会话不应复用上游连接")
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_PassthroughModeRelaysByCaddyAdapter(t *testing.T) {
	options := &wsFixtureOptions{}
	options.Request.URLPolicy.Enabled = false
	options.Request.URLPolicy.AllowInsecureHTTP = true

	options.WS.DialTimeoutSeconds = 3
	options.WS.ReadTimeoutSeconds = 3
	options.WS.WriteTimeoutSeconds = 3

	upstreamConn := &openAIWSCaptureConn{
		// Send created first so session.update can arrive before the terminal; later terminals wait for their request.
		readDelays: []time.Duration{0, 200 * time.Millisecond, 200 * time.Millisecond},
		events: [][]byte{
			[]byte(`{"type":"response.created","response":{"id":"resp_passthrough_turn_1","model":"upstream-turn-1"}}`),
			[]byte(`{"type":"response.completed","response":{"id":"resp_passthrough_turn_1","model":"upstream-turn-1","output":[{"id":"ig_passthrough_1","type":"image_generation_call","status":"generating","result":"final-image"}],"usage":{"input_tokens":2,"output_tokens":3}}}`),
			[]byte(`{"type":"response.completed","response":{"id":"resp_passthrough_turn_2","model":"upstream-turn-2","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	captureDialer := &openAIWSCaptureDialer{conn: upstreamConn}
	svc := newWSFixture(wsFixtureInputs{options: options, transport: &auxiliaryHTTPRecorder{}, cache: &testkit.StickyCache{}, corrector: openai.NewCodexToolCorrector(), dialer: captureDialer})

	provider := &gatewayadapter.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 452,
			Name:        "openai-ingress-passthrough",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Credentials: map[string]any{
				"api_key": "sk-test",
				"model_mapping": map[string]any{
					"channel-turn-1": "upstream-turn-1",
					"channel-turn-2": "upstream-turn-2",
				},
			},
			Extra: map[string]any{
				"responses_ws_connection_mode": "per_session",
			},
		},
	}

	serverErrCh := make(chan error, 1)
	resultCh := make(chan *forwardcore.OpenAIResult, 2)
	routingCalls := make(chan string, 3)
	beforeTurnCalls := make(chan int, 1)
	hooks := &ws.OpenAIIngressHooks{
		ResolveRoutingModel: func(turn int, requestedModel string, _ []byte) (string, error) {
			routingCalls <- fmt.Sprintf("%d:%s", turn, requestedModel)
			switch requestedModel {
			case "client-turn-1":
				return "channel-turn-1", nil
			case "client-turn-2":
				return "channel-turn-2", nil
			default:
				return "", fmt.Errorf("unexpected requested model: %s", requestedModel)
			}
		},
		BeforeTurn: func(turn int) error {
			beforeTurnCalls <- turn
			return nil
		},
		AfterTurn: func(capture ws.OpenAITurnCapture) {
			result := capture.Result
			turnErr := capture.Err
			if turnErr == nil && result != nil {
				resultCh <- result
			}
		},
	}

	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			CompressionMode: websocket.CompressionContextTakeover,
		})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			_ = conn.CloseNow()
		}()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != websocket.MessageText && msgType != websocket.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, provider, "sk-test", firstMessage, hooks)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := websocket.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, websocket.MessageText, []byte(`{"type":"response.create","model":"client-turn-1","stream":false,"service_tier":"fast","reasoning":{"effort":"max"},"parallel_tool_calls":true,"client_metadata":{"ws_request_header_x_openai_internal_codex_responses_lite":"true"}}`))
	cancelWrite()
	require.NoError(t, err)

	readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
	_, event, readErr := clientConn.Read(readCtx)
	cancelRead()
	require.NoError(t, readErr)
	require.Equal(t, "response.created", gjson.GetBytes(event, "type").String())
	require.Equal(t, "resp_passthrough_turn_1", gjson.GetBytes(event, "response.id").String())
	require.Equal(t, "client-turn-1", gjson.GetBytes(event, "response.model").String())

	// Update the next model during turn one; its terminal must retain turn one's model snapshot.
	sessionWriteCtx, cancelSessionWrite := context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(sessionWriteCtx, websocket.MessageText, []byte(`{"type":"session.update","session":{"model":"client-turn-2"}}`))
	cancelSessionWrite()
	require.NoError(t, err)
	firstTerminalReadCtx, cancelFirstTerminalRead := context.WithTimeout(context.Background(), 3*time.Second)
	_, firstTerminal, firstTerminalErr := clientConn.Read(firstTerminalReadCtx)
	cancelFirstTerminalRead()
	require.NoError(t, firstTerminalErr)
	require.Equal(t, "response.completed", gjson.GetBytes(firstTerminal, "type").String())
	require.Equal(t, "resp_passthrough_turn_1", gjson.GetBytes(firstTerminal, "response.id").String())
	require.Equal(t, "client-turn-1", gjson.GetBytes(firstTerminal, "response.model").String())
	require.Equal(t, "completed", gjson.GetBytes(firstTerminal, "response.output.0.status").String())

	writeCtx2, cancelWrite2 := context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx2, websocket.MessageText, []byte(`{"type":"response.create","model":"client-turn-2","stream":false,"previous_response_id":"resp_passthrough_turn_1","parallel_tool_calls":true,"client_metadata":{"ws_request_header_x_openai_internal_codex_responses_lite":"true"}}`))
	cancelWrite2()
	require.NoError(t, err)
	readCtx2, cancelRead2 := context.WithTimeout(context.Background(), 3*time.Second)
	_, event2, readErr2 := clientConn.Read(readCtx2)
	cancelRead2()
	require.NoError(t, readErr2)
	require.Equal(t, "response.completed", gjson.GetBytes(event2, "type").String())
	require.Equal(t, "resp_passthrough_turn_2", gjson.GetBytes(event2, "response.id").String())
	require.Equal(t, "client-turn-2", gjson.GetBytes(event2, "response.model").String())
	_ = clientConn.Close(websocket.StatusNormalClosure, "done")

	select {
	case serverErr := <-serverErrCh:
		// The server reader can report a normal client close as an error.
		if serverErr != nil {
			require.Contains(t, serverErr.Error(), "StatusNormalClosure",
				"server error should only be a normal close frame, got: %v", serverErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("等待 passthrough websocket 结束超时")
	}

	select {
	case result := <-resultCh:
		require.Equal(t, "resp_passthrough_turn_1", result.RequestID)
		require.Equal(t, "client-turn-1", result.Model)
		require.Equal(t, "upstream-turn-1", result.UpstreamModel)
		require.True(t, result.OpenAIWSMode)
		require.Equal(t, 2, result.Usage.InputTokens)
		require.Equal(t, 3, result.Usage.OutputTokens)
		require.NotNil(t, result.ServiceTier)
		require.Equal(t, "priority", *result.ServiceTier)
		require.NotNil(t, result.ReasoningEffort)
		require.Equal(t, "max", *result.ReasoningEffort)
	case <-time.After(2 * time.Second):
		t.Fatal("未收到 passthrough turn 结果回调")
	}
	select {
	case result := <-resultCh:
		require.Equal(t, "resp_passthrough_turn_2", result.RequestID)
		require.Equal(t, "client-turn-2", result.Model)
		require.Equal(t, "upstream-turn-2", result.UpstreamModel)
	case <-time.After(2 * time.Second):
		t.Fatal("未收到 passthrough 第二轮 turn 结果回调")
	}

	require.Equal(t, 1, captureDialer.DialCount(), "passthrough 模式应直接建立上游 websocket")
	require.Len(t, upstreamConn.writes, 3, "passthrough 模式应转发两轮 response.create 和一次 session.update")
	require.Equal(t, "upstream-turn-1", fmt.Sprint(upstreamConn.writes[0]["model"]))
	require.Equal(t, false, upstreamConn.writes[0]["parallel_tool_calls"])
	// session.update retains its object structure and uses the second turn's upstream model.
	secondTurnSession, ok := upstreamConn.writes[1]["session"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "upstream-turn-2", fmt.Sprint(secondTurnSession["model"]))
	require.Equal(t, "upstream-turn-2", fmt.Sprint(upstreamConn.writes[2]["model"]))
	require.Equal(t, false, upstreamConn.writes[2]["parallel_tool_calls"])
	require.Equal(t, "1:client-turn-1", <-routingCalls)
	require.Equal(t, "2:client-turn-2", <-routingCalls)
	require.Equal(t, "2:client-turn-2", <-routingCalls)
	require.Equal(t, 2, <-beforeTurnCalls)
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_HTTPBridgeModeRelaysHTTPStream(t *testing.T) {
	options := &wsFixtureOptions{}
	options.Request.URLPolicy.Enabled = false
	options.Request.URLPolicy.AllowInsecureHTTP = true

	options.WS.ReadTimeoutSeconds = 3
	options.WS.WriteTimeoutSeconds = 3

	upstream := &auxiliaryHTTPRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_bridge_1"}},
			Body: io.NopCloser(strings.NewReader(
				"data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n" +
					"data: {\"type\":\"response.done\",\"response\":{\"id\":\"resp_http_bridge_1\",\"output\":[{\"id\":\"ig_bridge_1\",\"type\":\"image_generation_call\",\"status\":\"in_progress\",\"result\":\"final-image\"}],\"usage\":{\"input_tokens\":2,\"output_tokens\":1,\"input_tokens_details\":{\"cached_tokens\":1}}}}\n\n" +
					"data: [DONE]\n\n",
			)),
		},
	}
	svc := newWSFixture(wsFixtureInputs{options: options, transport: upstream, cache: &testkit.StickyCache{}, corrector: openai.NewCodexToolCorrector()})

	provider := &gatewayadapter.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 552,
			Name:        "openai-ingress-http-bridge",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Credentials: map[string]any{
				"api_key": "sk-test",
				"model_mapping": map[string]any{
					"channel-bridge-model": "upstream-bridge-model",
				},
			},
			Extra: map[string]any{
				"openai_apikey_responses_websockets_v2_mode": "http_bridge",
			},
		},
	}

	serverErrCh := make(chan error, 1)
	resultCh := make(chan *forwardcore.OpenAIResult, 1)
	routingCallCh := make(chan string, 1)
	hooks := &ws.OpenAIIngressHooks{
		ResolveRoutingModel: func(turn int, requestedModel string, _ []byte) (string, error) {
			routingCallCh <- fmt.Sprintf("%d:%s", turn, requestedModel)
			if turn != 1 || requestedModel != "client-bridge-model" {
				return "", fmt.Errorf("unexpected bridge routing input: turn=%d model=%s", turn, requestedModel)
			}
			return "channel-bridge-model", nil
		},
		AfterTurn: func(capture ws.OpenAITurnCapture) {
			if capture.Err == nil && capture.Result != nil {
				resultCh <- capture.Result
			}
		},
	}

	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			CompressionMode: websocket.CompressionContextTakeover,
		})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			_ = conn.CloseNow()
		}()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != websocket.MessageText && msgType != websocket.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, provider, "sk-test", firstMessage, hooks)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := websocket.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, websocket.MessageText, []byte(`{"type":"response.create","model":"client-bridge-model","reasoning":{"effort":"max"},"stream":false}`))
	cancelWrite()
	require.NoError(t, err)

	readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
	_, event1, readErr1 := clientConn.Read(readCtx)
	cancelRead()
	require.NoError(t, readErr1)
	require.Equal(t, "response.output_text.delta", gjson.GetBytes(event1, "type").String())
	require.Equal(t, "hello", gjson.GetBytes(event1, "delta").String())

	readCtx2, cancelRead2 := context.WithTimeout(context.Background(), 3*time.Second)
	_, event2, readErr2 := clientConn.Read(readCtx2)
	cancelRead2()
	require.NoError(t, readErr2)
	require.Equal(t, "response.done", gjson.GetBytes(event2, "type").String())
	require.Equal(t, "resp_http_bridge_1", gjson.GetBytes(event2, "response.id").String())
	require.Equal(t, "completed", gjson.GetBytes(event2, "response.output.0.status").String())
	_ = clientConn.Close(websocket.StatusNormalClosure, "done")

	select {
	case serverErr := <-serverErrCh:
		if serverErr != nil {
			require.Contains(t, serverErr.Error(), "StatusNormalClosure",
				"server error should only be a normal close frame, got: %v", serverErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("等待 http_bridge websocket 结束超时")
	}

	select {
	case result := <-resultCh:
		require.Equal(t, "resp_http_bridge_1", result.RequestID)
		require.Equal(t, "client-bridge-model", result.Model)
		require.Equal(t, "upstream-bridge-model", result.UpstreamModel)
		require.True(t, result.OpenAIWSMode)
		require.Equal(t, 2, result.Usage.InputTokens)
		require.Equal(t, 1, result.Usage.OutputTokens)
		require.Equal(t, 1, result.Usage.CacheReadInputTokens)
		require.NotNil(t, result.FirstTokenMs)
		require.NotNil(t, result.ReasoningEffort)
		require.Equal(t, "max", *result.ReasoningEffort)
	case <-time.After(2 * time.Second):
		t.Fatal("未收到 http_bridge turn 结果回调")
	}
	require.Equal(t, "upstream-bridge-model", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Equal(t, "max", gjson.GetBytes(upstream.lastBody, "reasoning.effort").String())
	require.Equal(t, "1:client-bridge-model", <-routingCallCh)

	require.NotNil(t, upstream.lastReq, "http_bridge 模式应调用 HTTP 上游")
	require.Equal(t, "https://api.openai.com/v1/responses", upstream.lastReq.URL.String())
	require.True(t, gjson.GetBytes(upstream.lastBody, "stream").Bool())
	require.False(t, gjson.GetBytes(upstream.lastBody, "type").Exists())
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_PassthroughBridgeBoundaries(t *testing.T) {
	tests := []struct {
		name            string
		payload         string
		threshold       int64
		wantRelayReject bool
	}{
		{
			name:      "small response create",
			payload:   `{"type":"response.create","model":"gpt-5.1"}`,
			threshold: 1024,
		},
		{
			name:      "continuation response create",
			payload:   `{"type":"response.create","previous_response_id":"resp_previous","model":"gpt-5.1"}`,
			threshold: 1,
		},
		{
			name:      "other event type",
			payload:   `{"type":"session.update","padding":"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}`,
			threshold: 1,
		},
		{
			name:            "malformed data",
			payload:         `{"type":"response.create","padding":"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"`,
			threshold:       1,
			wantRelayReject: true,
		},
		{
			name:      "duplicate type",
			payload:   `{"type":"response.create","type":"response.create","model":"gpt-5.1"}`,
			threshold: 1,
		},
		{
			name:      "duplicate previous response id",
			payload:   `{"type":"response.create","previous_response_id":null,"previous_response_id":null,"model":"gpt-5.1"}`,
			threshold: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.name == "other event type" {
				t.Skip("fork relay fixture does not provide a response for non-response.create events")
			}

			options := &wsFixtureOptions{}
			options.Request.URLPolicy.Enabled = false
			options.Request.URLPolicy.AllowInsecureHTTP = true

			options.WS.HTTPBridgeThresholdBytes = tt.threshold

			upstreamConn := &openAIWSCaptureConn{events: [][]byte{
				[]byte(`{"type":"response.completed","response":{"id":"resp_duplicate_keys","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
			}}
			dialer := &openAIWSCaptureDialer{conn: upstreamConn}
			httpUpstream := &auxiliaryHTTPRecorder{}
			svc := newWSFixture(wsFixtureInputs{options: options, transport: httpUpstream, cache: &testkit.StickyCache{}, corrector: openai.NewCodexToolCorrector(), dialer: dialer})
			provider := &gatewayadapter.ExecutionProvider{
				Record: providercore.Record{
					LoadLocation: time.LoadLocation, ID: 453,
					Platform:    capability.PlatformOpenAI,
					Type:        capability.ProviderTypeAPIKey,
					Status:      billing.StatusActive,
					Schedulable: true,
					Concurrency: 1,
					Credentials: map[string]any{"api_key": "sk-test"},
					Extra: map[string]any{
						"responses_ws_connection_mode": "per_session",
					},
				},
			}

			errCh := make(chan error, 1)
			wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					errCh <- err
					return
				}
				defer func() { _ = conn.CloseNow() }()
				_, firstMessage, err := conn.Read(r.Context())
				if err != nil {
					errCh <- err
					return
				}
				rec := httptest.NewRecorder()
				ginCtx, _ := gin.CreateTestContext(rec)
				ginCtx.Request = r
				errCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, provider, "sk-test", firstMessage, nil)
			}))
			defer wsServer.Close()

			clientConn, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
			require.NoError(t, err)
			defer func() { _ = clientConn.CloseNow() }()
			require.NoError(t, clientConn.Write(context.Background(), websocket.MessageText, []byte(tt.payload)))
			_, event, err := clientConn.Read(context.Background())
			if tt.wantRelayReject {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, "resp_duplicate_keys", gjson.GetBytes(event, "response.id").String())
				require.NoError(t, clientConn.Close(websocket.StatusNormalClosure, "done"))
			}

			select {
			case proxyErr := <-errCh:
				if tt.wantRelayReject {
					require.Error(t, proxyErr)
				} else if proxyErr != nil {
					require.Contains(t, proxyErr.Error(), "StatusNormalClosure")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("等待 boundary passthrough websocket 结束超时")
			}
			require.Empty(t, httpUpstream.requests, "boundary frame must not use the HTTP bridge")
			if tt.wantRelayReject {
				require.Zero(t, dialer.DialCount())
				require.Empty(t, upstreamConn.writes)
			} else {
				require.Equal(t, 1, dialer.DialCount())
				require.Len(t, upstreamConn.writes, 1)
			}
		})
	}
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_PassthroughHeadersUsePromptCacheAndTurnState(t *testing.T) {
	options := &wsFixtureOptions{}
	options.Request.URLPolicy.Enabled = false
	options.Request.URLPolicy.AllowInsecureHTTP = true

	options.WS.DialTimeoutSeconds = 3
	options.WS.ReadTimeoutSeconds = 3
	options.WS.WriteTimeoutSeconds = 3

	upstreamConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_passthrough_headers","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	captureDialer := &openAIWSCaptureDialer{conn: upstreamConn}
	svc := newWSFixture(wsFixtureInputs{options: options, transport: &auxiliaryHTTPRecorder{}, cache: &testkit.StickyCache{}, corrector: openai.NewCodexToolCorrector(), dialer: captureDialer})
	provider := &gatewayadapter.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 453,
			Name:        "openai-ingress-passthrough-headers",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeOAuth,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Credentials: map[string]any{
				"access_token": "oauth-token",
			},
			Extra: map[string]any{
				"responses_ws_connection_mode": "per_session",
			},
		},
	}

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			CompressionMode: websocket.CompressionContextTakeover,
		})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			_ = conn.CloseNow()
		}()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "codex_cli_rs/0.98.0")
		req.Header.Set(openAIWSTurnStateHeader, "turn-state-1")
		req.Header.Set(openai.WSTurnMetadataHeader, "turn-meta-1")
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != websocket.MessageText && msgType != websocket.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, provider, "oauth-token", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := websocket.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, websocket.MessageText, []byte(`{
		"type":"response.create",
		"model":"gpt-5.1",
		"stream":false,
		"prompt_cache_key":"pcache_passthrough",
		"parallel_tool_calls":true,
		"reasoning":{"effort":"medium","context":"current_turn"},
		"client_metadata":{"ws_request_header_x_openai_internal_codex_responses_lite":"true"},
		"tools":[{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"spawn_agent"}]}],
		"input":[{"type":"message","role":"user","content":"hello"}],
		"tool_choice":{"type":"namespace","name":"collaboration"}
	}`))
	cancelWrite()
	require.NoError(t, err)

	readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
	_, event, readErr := clientConn.Read(readCtx)
	cancelRead()
	require.NoError(t, readErr)
	require.Equal(t, "resp_passthrough_headers", gjson.GetBytes(event, "response.id").String())
	_ = clientConn.Close(websocket.StatusNormalClosure, "done")

	select {
	case serverErr := <-serverErrCh:
		if serverErr != nil {
			require.Contains(t, serverErr.Error(), "StatusNormalClosure")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("等待 passthrough websocket 结束超时")
	}

	require.Equal(t, openai.IsolateOpenAIUpstreamSessionID(0, provideradapter.CodexIdentityNamespace(provider.View()), "pcache_passthrough"), captureDialer.lastHeaders.Get("session_id"))
	require.Equal(t, "turn-state-1", captureDialer.lastHeaders.Get(openAIWSTurnStateHeader))
	require.Equal(t, "turn-meta-1", captureDialer.lastHeaders.Get(openai.WSTurnMetadataHeader))
	require.Len(t, upstreamConn.writes, 1)
	forwarded := requestToJSONString(upstreamConn.writes[0])
	require.True(t, gjson.Get(forwarded, "parallel_tool_calls").Exists())
	require.False(t, gjson.Get(forwarded, "parallel_tool_calls").Bool())
	require.False(t, gjson.Get(forwarded, `tools.#(type=="namespace")`).Exists())
	require.Equal(t, "collaboration", gjson.Get(forwarded, `input.#(type=="additional_tools").tools.0.name`).String())
	require.Equal(t, "namespace", gjson.Get(forwarded, "tool_choice.type").String())
	require.Equal(t, "collaboration", gjson.Get(forwarded, "tool_choice.name").String())
	require.Equal(t, "medium", gjson.Get(forwarded, "reasoning.effort").String())
	require.Equal(t, "all_turns", gjson.Get(forwarded, "reasoning.context").String())
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_StoreDisabledPrevResponseStrictDropToFullCreate(t *testing.T) {
	options := &wsFixtureOptions{}
	options.Request.URLPolicy.Enabled = false
	options.Request.URLPolicy.AllowInsecureHTTP = true

	options.Pool.MaxConnsPerProvider = 1
	options.Pool.MinIdlePerProvider = 0
	options.Pool.MaxIdlePerProvider = 1
	options.Pool.QueueLimitPerConn = 8
	options.WS.DialTimeoutSeconds = 3
	options.WS.ReadTimeoutSeconds = 3
	options.WS.WriteTimeoutSeconds = 3

	captureConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_preflight_rewrite_1","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
			[]byte(`{"type":"response.completed","response":{"id":"resp_preflight_rewrite_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	captureDialer := &openAIWSCaptureDialer{conn: captureConn}
	pool := newOpenAIWSConnPool(options)
	pool.SetClientDialerForTest(captureDialer)

	svc := newWSFixture(wsFixtureInputs{options: options, transport: &auxiliaryHTTPRecorder{}, cache: &testkit.StickyCache{}, corrector: openai.NewCodexToolCorrector(), pool: pool})

	provider := &gatewayadapter.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 140,
			Name:        "openai-ingress-prev-preflight-rewrite",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Credentials: map[string]any{
				"api_key": "sk-test",
			},
			Extra: map[string]any{
				"responses_websockets_v2_enabled": true,
			},
		},
	}

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			CompressionMode: websocket.CompressionContextTakeover,
		})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			_ = conn.CloseNow()
		}()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != websocket.MessageText && msgType != websocket.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, provider, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := websocket.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeMessage := func(payload string) {
		writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, clientConn.Write(writeCtx, websocket.MessageText, []byte(payload)))
	}
	readMessage := func() []byte {
		readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		msgType, message, readErr := clientConn.Read(readCtx)
		require.NoError(t, readErr)
		require.Equal(t, websocket.MessageText, msgType)
		return message
	}

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"input":[{"type":"input_text","text":"hello"}]}`)
	firstTurn := readMessage()
	require.Equal(t, "resp_preflight_rewrite_1", gjson.GetBytes(firstTurn, "response.id").String())

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"previous_response_id":"resp_stale_external","input":[{"type":"input_text","text":"world"}]}`)
	secondTurn := readMessage()
	require.Equal(t, "resp_preflight_rewrite_2", gjson.GetBytes(secondTurn, "response.id").String())

	require.NoError(t, clientConn.Close(websocket.StatusNormalClosure, "done"))
	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 结束超时")
	}

	require.Equal(t, 1, captureDialer.DialCount(), "严格增量不成立时应在同一连接内降级为 full create")
	require.Len(t, captureConn.writes, 2)
	secondWrite := requestToJSONString(captureConn.writes[1])
	require.False(t, gjson.Get(secondWrite, "previous_response_id").Exists(), "严格增量不成立时应移除 previous_response_id，改为 full create")
	require.Equal(t, 2, len(gjson.Get(secondWrite, "input").Array()), "严格降级为 full create 时应重放完整 input 上下文")
	require.Equal(t, "hello", gjson.Get(secondWrite, "input.0.text").String())
	require.Equal(t, "world", gjson.Get(secondWrite, "input.1.text").String())
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_StoreDisabledPrevResponseStrictDropBeforePreflightPingFailReconnects(t *testing.T) {
	prevPreflightPingIdle := openAIWSIngressPreflightPingIdle
	openAIWSIngressPreflightPingIdle = 0
	defer func() {
		openAIWSIngressPreflightPingIdle = prevPreflightPingIdle
	}()

	options := &wsFixtureOptions{}
	options.Request.URLPolicy.Enabled = false
	options.Request.URLPolicy.AllowInsecureHTTP = true

	options.Pool.MaxConnsPerProvider = 2
	options.Pool.MinIdlePerProvider = 0
	options.Pool.MaxIdlePerProvider = 2
	options.Pool.QueueLimitPerConn = 8
	options.WS.DialTimeoutSeconds = 3
	options.WS.ReadTimeoutSeconds = 3
	options.WS.WriteTimeoutSeconds = 3

	firstConn := &openAIWSPreflightFailConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_turn_ping_drop_1","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	secondConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_turn_ping_drop_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	dialer := &openAIWSQueueDialer{
		conns: []openai.WSClientConn{firstConn, secondConn},
	}
	pool := newOpenAIWSConnPool(options)
	pool.SetClientDialerForTest(dialer)

	svc := newWSFixture(wsFixtureInputs{options: options, transport: &auxiliaryHTTPRecorder{}, cache: &testkit.StickyCache{}, corrector: openai.NewCodexToolCorrector(), pool: pool})

	provider := &gatewayadapter.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 142,
			Name:        "openai-ingress-prev-strict-drop-before-ping",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Credentials: map[string]any{
				"api_key": "sk-test",
			},
			Extra: map[string]any{
				"responses_websockets_v2_enabled": true,
			},
		},
	}

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			CompressionMode: websocket.CompressionContextTakeover,
		})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			_ = conn.CloseNow()
		}()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != websocket.MessageText && msgType != websocket.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, provider, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := websocket.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeMessage := func(payload string) {
		writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, clientConn.Write(writeCtx, websocket.MessageText, []byte(payload)))
	}
	readMessage := func() []byte {
		readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		msgType, message, readErr := clientConn.Read(readCtx)
		require.NoError(t, readErr)
		require.Equal(t, websocket.MessageText, msgType)
		return message
	}

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"input":[{"type":"input_text","text":"hello"}]}`)
	firstTurn := readMessage()
	require.Equal(t, "resp_turn_ping_drop_1", gjson.GetBytes(firstTurn, "response.id").String())

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"previous_response_id":"resp_stale_external","input":[{"type":"input_text","text":"world"}]}`)
	secondTurn := readMessage()
	require.Equal(t, "resp_turn_ping_drop_2", gjson.GetBytes(secondTurn, "response.id").String())

	require.NoError(t, clientConn.Close(websocket.StatusNormalClosure, "done"))
	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 严格降级后预检换连超时")
	}

	require.Equal(t, 2, dialer.DialCount(), "严格降级为 full create 后，预检 ping 失败应允许换连")
	require.Equal(t, 1, firstConn.WriteCount(), "首连接在预检失败后不应继续发送第二轮")
	require.GreaterOrEqual(t, firstConn.PingCount(), 1, "第二轮前应执行 preflight ping")
	secondConn.mu.Lock()
	secondWrites := append([]map[string]any(nil), secondConn.writes...)
	secondConn.mu.Unlock()
	require.Len(t, secondWrites, 1)
	secondWrite := requestToJSONString(secondWrites[0])
	require.False(t, gjson.Get(secondWrite, "previous_response_id").Exists(), "严格降级后重试应移除 previous_response_id")
	require.Equal(t, 2, len(gjson.Get(secondWrite, "input").Array()))
	require.Equal(t, "hello", gjson.Get(secondWrite, "input.0.text").String())
	require.Equal(t, "world", gjson.Get(secondWrite, "input.1.text").String())
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_StoreEnabledSkipsStrictPrevResponseEval(t *testing.T) {
	options := &wsFixtureOptions{}
	options.Request.URLPolicy.Enabled = false
	options.Request.URLPolicy.AllowInsecureHTTP = true

	options.Pool.MaxConnsPerProvider = 1
	options.Pool.MinIdlePerProvider = 0
	options.Pool.MaxIdlePerProvider = 1
	options.Pool.QueueLimitPerConn = 8
	options.WS.DialTimeoutSeconds = 3
	options.WS.ReadTimeoutSeconds = 3
	options.WS.WriteTimeoutSeconds = 3

	captureConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_store_enabled_1","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
			[]byte(`{"type":"response.completed","response":{"id":"resp_store_enabled_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	captureDialer := &openAIWSCaptureDialer{conn: captureConn}
	pool := newOpenAIWSConnPool(options)
	pool.SetClientDialerForTest(captureDialer)

	svc := newWSFixture(wsFixtureInputs{options: options, transport: &auxiliaryHTTPRecorder{}, cache: &testkit.StickyCache{}, corrector: openai.NewCodexToolCorrector(), pool: pool})

	provider := &gatewayadapter.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 143,
			Name:        "openai-ingress-store-enabled-skip-strict",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Credentials: map[string]any{
				"api_key": "sk-test",
			},
			Extra: map[string]any{
				"responses_websockets_v2_enabled": true,
			},
		},
	}

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			CompressionMode: websocket.CompressionContextTakeover,
		})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			_ = conn.CloseNow()
		}()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != websocket.MessageText && msgType != websocket.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, provider, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := websocket.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeMessage := func(payload string) {
		writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, clientConn.Write(writeCtx, websocket.MessageText, []byte(payload)))
	}
	readMessage := func() []byte {
		readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		msgType, message, readErr := clientConn.Read(readCtx)
		require.NoError(t, readErr)
		require.Equal(t, websocket.MessageText, msgType)
		return message
	}

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":true}`)
	firstTurn := readMessage()
	require.Equal(t, "resp_store_enabled_1", gjson.GetBytes(firstTurn, "response.id").String())

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":true,"previous_response_id":"resp_stale_external"}`)
	secondTurn := readMessage()
	require.Equal(t, "resp_store_enabled_2", gjson.GetBytes(secondTurn, "response.id").String())

	require.NoError(t, clientConn.Close(websocket.StatusNormalClosure, "done"))
	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 store=true 场景 websocket 结束超时")
	}

	require.Equal(t, 1, captureDialer.DialCount())
	require.Len(t, captureConn.writes, 2)
	require.Equal(t, "resp_stale_external", gjson.Get(requestToJSONString(captureConn.writes[1]), "previous_response_id").String(), "store=true 场景不应触发 store-disabled strict 规则")
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_StoreDisabledPrevResponsePreflightSkipForFunctionCallOutput(t *testing.T) {
	options := &wsFixtureOptions{}
	options.Request.URLPolicy.Enabled = false
	options.Request.URLPolicy.AllowInsecureHTTP = true

	options.Pool.MaxConnsPerProvider = 1
	options.Pool.MinIdlePerProvider = 0
	options.Pool.MaxIdlePerProvider = 1
	options.Pool.QueueLimitPerConn = 8
	options.WS.DialTimeoutSeconds = 3
	options.WS.ReadTimeoutSeconds = 3
	options.WS.WriteTimeoutSeconds = 3

	captureConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_preflight_skip_1","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
			[]byte(`{"type":"response.completed","response":{"id":"resp_preflight_skip_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	captureDialer := &openAIWSCaptureDialer{conn: captureConn}
	pool := newOpenAIWSConnPool(options)
	pool.SetClientDialerForTest(captureDialer)

	svc := newWSFixture(wsFixtureInputs{options: options, transport: &auxiliaryHTTPRecorder{}, cache: &testkit.StickyCache{}, corrector: openai.NewCodexToolCorrector(), pool: pool})

	provider := &gatewayadapter.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 141,
			Name:        "openai-ingress-prev-preflight-skip-fco",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Credentials: map[string]any{
				"api_key": "sk-test",
			},
			Extra: map[string]any{
				"responses_websockets_v2_enabled": true,
			},
		},
	}

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			CompressionMode: websocket.CompressionContextTakeover,
		})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			_ = conn.CloseNow()
		}()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != websocket.MessageText && msgType != websocket.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, provider, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := websocket.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeMessage := func(payload string) {
		writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, clientConn.Write(writeCtx, websocket.MessageText, []byte(payload)))
	}
	readMessage := func() []byte {
		readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		msgType, message, readErr := clientConn.Read(readCtx)
		require.NoError(t, readErr)
		require.Equal(t, websocket.MessageText, msgType)
		return message
	}

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false}`)
	firstTurn := readMessage()
	require.Equal(t, "resp_preflight_skip_1", gjson.GetBytes(firstTurn, "response.id").String())

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"previous_response_id":"resp_stale_external","input":[{"type":"function_call_output","call_id":"call_1","output":"ok"}]}`)
	secondTurn := readMessage()
	require.Equal(t, "resp_preflight_skip_2", gjson.GetBytes(secondTurn, "response.id").String())

	require.NoError(t, clientConn.Close(websocket.StatusNormalClosure, "done"))
	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 结束超时")
	}

	require.Equal(t, 1, captureDialer.DialCount())
	require.Len(t, captureConn.writes, 2)
	require.Equal(t, "resp_stale_external", gjson.Get(requestToJSONString(captureConn.writes[1]), "previous_response_id").String(), "function_call_output 场景不应预改写 previous_response_id")
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_StoreDisabledFunctionCallOutputAutoAttachPreviousResponseID(t *testing.T) {
	options := &wsFixtureOptions{}
	options.Request.URLPolicy.Enabled = false
	options.Request.URLPolicy.AllowInsecureHTTP = true

	options.Pool.MaxConnsPerProvider = 1
	options.Pool.MinIdlePerProvider = 0
	options.Pool.MaxIdlePerProvider = 1
	options.Pool.QueueLimitPerConn = 8
	options.WS.DialTimeoutSeconds = 3
	options.WS.ReadTimeoutSeconds = 3
	options.WS.WriteTimeoutSeconds = 3

	captureConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_auto_prev_1","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
			[]byte(`{"type":"response.completed","response":{"id":"resp_auto_prev_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	captureDialer := &openAIWSCaptureDialer{conn: captureConn}
	pool := newOpenAIWSConnPool(options)
	pool.SetClientDialerForTest(captureDialer)

	svc := newWSFixture(wsFixtureInputs{options: options, transport: &auxiliaryHTTPRecorder{}, cache: &testkit.StickyCache{}, corrector: openai.NewCodexToolCorrector(), pool: pool})

	provider := &gatewayadapter.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 143,
			Name:        "openai-ingress-fco-auto-prev",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Credentials: map[string]any{
				"api_key": "sk-test",
			},
			Extra: map[string]any{
				"responses_websockets_v2_enabled": true,
			},
		},
	}

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			CompressionMode: websocket.CompressionContextTakeover,
		})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			_ = conn.CloseNow()
		}()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != websocket.MessageText && msgType != websocket.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, provider, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := websocket.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeMessage := func(payload string) {
		writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, clientConn.Write(writeCtx, websocket.MessageText, []byte(payload)))
	}
	readMessage := func() []byte {
		readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		msgType, message, readErr := clientConn.Read(readCtx)
		require.NoError(t, readErr)
		require.Equal(t, websocket.MessageText, msgType)
		return message
	}

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"input":[{"type":"input_text","text":"hello"}]}`)
	firstTurn := readMessage()
	require.Equal(t, "resp_auto_prev_1", gjson.GetBytes(firstTurn, "response.id").String())

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"input":[{"type":"function_call_output","call_id":"call_auto_1","output":"ok"}]}`)
	secondTurn := readMessage()
	require.Equal(t, "resp_auto_prev_2", gjson.GetBytes(secondTurn, "response.id").String())

	require.NoError(t, clientConn.Close(websocket.StatusNormalClosure, "done"))
	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 结束超时")
	}

	require.Equal(t, 1, captureDialer.DialCount())
	require.Len(t, captureConn.writes, 2)
	require.Equal(t, "resp_auto_prev_1", gjson.Get(requestToJSONString(captureConn.writes[1]), "previous_response_id").String(), "function_call_output 缺失 previous_response_id 时应回填上一轮响应 ID")
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_StoreDisabledToolSearchOutputAutoAttachesPreviousResponseID(t *testing.T) {
	options := &wsFixtureOptions{}
	options.Request.URLPolicy.Enabled = false
	options.Request.URLPolicy.AllowInsecureHTTP = true

	options.Pool.MaxConnsPerProvider = 1
	options.Pool.MinIdlePerProvider = 0
	options.Pool.MaxIdlePerProvider = 1
	options.Pool.QueueLimitPerConn = 8
	options.WS.DialTimeoutSeconds = 3
	options.WS.ReadTimeoutSeconds = 3
	options.WS.WriteTimeoutSeconds = 3

	captureConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_tool_search_prev_1","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
			[]byte(`{"type":"response.completed","response":{"id":"resp_tool_search_prev_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	captureDialer := &openAIWSCaptureDialer{conn: captureConn}
	pool := newOpenAIWSConnPool(options)
	pool.SetClientDialerForTest(captureDialer)

	svc := newWSFixture(wsFixtureInputs{options: options, transport: &auxiliaryHTTPRecorder{}, cache: &testkit.StickyCache{}, corrector: openai.NewCodexToolCorrector(), pool: pool})

	provider := &gatewayadapter.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 145,
			Name:        "openai-ingress-tool-search-output-auto-prev",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Credentials: map[string]any{
				"api_key": "sk-test",
			},
			Extra: map[string]any{
				"responses_websockets_v2_enabled": true,
			},
		},
	}

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			CompressionMode: websocket.CompressionContextTakeover,
		})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			_ = conn.CloseNow()
		}()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != websocket.MessageText && msgType != websocket.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, provider, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := websocket.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeMessage := func(payload string) {
		writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, clientConn.Write(writeCtx, websocket.MessageText, []byte(payload)))
	}
	readMessage := func() []byte {
		readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		msgType, message, readErr := clientConn.Read(readCtx)
		require.NoError(t, readErr)
		require.Equal(t, websocket.MessageText, msgType)
		return message
	}

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"input":[{"type":"input_text","text":"hello"}]}`)
	firstTurn := readMessage()
	require.Equal(t, "resp_tool_search_prev_1", gjson.GetBytes(firstTurn, "response.id").String())

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"input":[{"type":"tool_search_output","call_id":"call_search_1","output":"ok"}]}`)
	secondTurn := readMessage()
	require.Equal(t, "resp_tool_search_prev_2", gjson.GetBytes(secondTurn, "response.id").String())

	require.NoError(t, clientConn.Close(websocket.StatusNormalClosure, "done"))
	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 结束超时")
	}

	require.Equal(t, 1, captureDialer.DialCount())
	require.Len(t, captureConn.writes, 2)
	secondWrite := requestToJSONString(captureConn.writes[1])
	require.Equal(t, "resp_tool_search_prev_1", gjson.Get(secondWrite, "previous_response_id").String(), "tool_search_output 缺失 previous_response_id 时应回填上一轮响应 ID")
	require.Equal(t, "tool_search_output", gjson.Get(secondWrite, "input.0.type").String())
	require.Equal(t, "call_search_1", gjson.Get(secondWrite, "input.0.call_id").String())
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_StoreDisabledFunctionCallOutputSkipsAutoAttachWhenLastResponseIDMissing(t *testing.T) {
	options := &wsFixtureOptions{}
	options.Request.URLPolicy.Enabled = false
	options.Request.URLPolicy.AllowInsecureHTTP = true

	options.Pool.MaxConnsPerProvider = 1
	options.Pool.MinIdlePerProvider = 0
	options.Pool.MaxIdlePerProvider = 1
	options.Pool.QueueLimitPerConn = 8
	options.WS.DialTimeoutSeconds = 3
	options.WS.ReadTimeoutSeconds = 3
	options.WS.WriteTimeoutSeconds = 3

	captureConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
			[]byte(`{"type":"response.completed","response":{"id":"resp_auto_prev_skip_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	captureDialer := &openAIWSCaptureDialer{conn: captureConn}
	pool := newOpenAIWSConnPool(options)
	pool.SetClientDialerForTest(captureDialer)

	svc := newWSFixture(wsFixtureInputs{options: options, transport: &auxiliaryHTTPRecorder{}, cache: &testkit.StickyCache{}, corrector: openai.NewCodexToolCorrector(), pool: pool})

	provider := &gatewayadapter.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 144,
			Name:        "openai-ingress-fco-auto-prev-skip",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Credentials: map[string]any{
				"api_key": "sk-test",
			},
			Extra: map[string]any{
				"responses_websockets_v2_enabled": true,
			},
		},
	}

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			CompressionMode: websocket.CompressionContextTakeover,
		})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			_ = conn.CloseNow()
		}()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != websocket.MessageText && msgType != websocket.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, provider, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := websocket.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeMessage := func(payload string) {
		writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, clientConn.Write(writeCtx, websocket.MessageText, []byte(payload)))
	}
	readMessage := func() []byte {
		readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		msgType, message, readErr := clientConn.Read(readCtx)
		require.NoError(t, readErr)
		require.Equal(t, websocket.MessageText, msgType)
		return message
	}

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"input":[{"type":"input_text","text":"hello"}]}`)
	firstTurn := readMessage()
	require.Equal(t, "response.completed", gjson.GetBytes(firstTurn, "type").String())
	require.Empty(t, gjson.GetBytes(firstTurn, "response.id").String(), "首轮响应不返回 response.id，模拟无法推导续链锚点")

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"input":[{"type":"function_call_output","call_id":"call_auto_skip_1","output":"ok"}]}`)
	secondTurn := readMessage()
	require.Equal(t, "resp_auto_prev_skip_2", gjson.GetBytes(secondTurn, "response.id").String())

	require.NoError(t, clientConn.Close(websocket.StatusNormalClosure, "done"))
	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 结束超时")
	}

	require.Equal(t, 1, captureDialer.DialCount())
	require.Len(t, captureConn.writes, 2)
	require.False(t, gjson.Get(requestToJSONString(captureConn.writes[1]), "previous_response_id").Exists(), "上一轮缺失 response.id 时不应自动补齐 previous_response_id")
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_StoreDisabledFunctionCallOutputSkipsAutoAttachWhenToolCallContextPresent(t *testing.T) {
	options := &wsFixtureOptions{}
	options.Request.URLPolicy.Enabled = false
	options.Request.URLPolicy.AllowInsecureHTTP = true

	options.Pool.MaxConnsPerProvider = 1
	options.Pool.MinIdlePerProvider = 0
	options.Pool.MaxIdlePerProvider = 1
	options.Pool.QueueLimitPerConn = 8
	options.WS.DialTimeoutSeconds = 3
	options.WS.ReadTimeoutSeconds = 3
	options.WS.WriteTimeoutSeconds = 3

	captureConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_auto_prev_ctx_1","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
			[]byte(`{"type":"response.completed","response":{"id":"resp_auto_prev_ctx_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	captureDialer := &openAIWSQueueDialer{
		conns: []openai.WSClientConn{captureConn},
	}
	pool := newOpenAIWSConnPool(options)
	pool.SetClientDialerForTest(captureDialer)

	svc := newWSFixture(wsFixtureInputs{options: options, transport: &auxiliaryHTTPRecorder{}, cache: &testkit.StickyCache{}, corrector: openai.NewCodexToolCorrector(), pool: pool})

	provider := &gatewayadapter.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 114,
			Name:        "openai-ingress-tool-context",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Credentials: map[string]any{
				"api_key": "sk-test",
			},
			Extra: map[string]any{
				"responses_websockets_v2_enabled": true,
			},
		},
	}

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			CompressionMode: websocket.CompressionContextTakeover,
		})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			_ = conn.CloseNow()
		}()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != websocket.MessageText && msgType != websocket.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, provider, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := websocket.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeMessage := func(payload string) {
		writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, clientConn.Write(writeCtx, websocket.MessageText, []byte(payload)))
	}
	readMessage := func() []byte {
		readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		msgType, message, readErr := clientConn.Read(readCtx)
		require.NoError(t, readErr)
		require.Equal(t, websocket.MessageText, msgType)
		return message
	}

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"input":[{"type":"input_text","text":"hello"}]}`)
	firstTurn := readMessage()
	require.Equal(t, "resp_auto_prev_ctx_1", gjson.GetBytes(firstTurn, "response.id").String())

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"input":[{"type":"function_call","call_id":"call_ctx_1","name":"shell","arguments":"{}"},{"type":"function_call_output","call_id":"call_ctx_1","output":"ok"},{"type":"message","role":"user","content":[{"type":"input_text","text":"retry"}]}]}`)
	secondTurn := readMessage()
	require.Equal(t, "resp_auto_prev_ctx_2", gjson.GetBytes(secondTurn, "response.id").String())

	require.NoError(t, clientConn.Close(websocket.StatusNormalClosure, "done"))
	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 结束超时")
	}

	require.Equal(t, 1, captureDialer.DialCount())
	require.Len(t, captureConn.writes, 2)
	require.False(t, gjson.Get(requestToJSONString(captureConn.writes[1]), "previous_response_id").Exists(), "请求已包含 function_call 上下文时不应自动补齐 previous_response_id")
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_StoreDisabledFunctionCallOutputAutoAttachWhenOnlyItemReferencesPresent(t *testing.T) {
	options := &wsFixtureOptions{}
	options.Request.URLPolicy.Enabled = false
	options.Request.URLPolicy.AllowInsecureHTTP = true

	options.Pool.MaxConnsPerProvider = 1
	options.Pool.MinIdlePerProvider = 0
	options.Pool.MaxIdlePerProvider = 1
	options.Pool.QueueLimitPerConn = 8
	options.WS.DialTimeoutSeconds = 3
	options.WS.ReadTimeoutSeconds = 3
	options.WS.WriteTimeoutSeconds = 3

	captureConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_auto_prev_ref_1","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
			[]byte(`{"type":"response.completed","response":{"id":"resp_auto_prev_ref_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	captureDialer := &openAIWSQueueDialer{
		conns: []openai.WSClientConn{captureConn},
	}
	pool := newOpenAIWSConnPool(options)
	pool.SetClientDialerForTest(captureDialer)

	svc := newWSFixture(wsFixtureInputs{options: options, transport: &auxiliaryHTTPRecorder{}, cache: &testkit.StickyCache{}, corrector: openai.NewCodexToolCorrector(), pool: pool})

	provider := &gatewayadapter.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 115,
			Name:        "openai-ingress-item-reference",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Credentials: map[string]any{
				"api_key": "sk-test",
			},
			Extra: map[string]any{
				"responses_websockets_v2_enabled": true,
			},
		},
	}

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			CompressionMode: websocket.CompressionContextTakeover,
		})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			_ = conn.CloseNow()
		}()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != websocket.MessageText && msgType != websocket.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, provider, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := websocket.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeMessage := func(payload string) {
		writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, clientConn.Write(writeCtx, websocket.MessageText, []byte(payload)))
	}
	readMessage := func() []byte {
		readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		msgType, message, readErr := clientConn.Read(readCtx)
		require.NoError(t, readErr)
		require.Equal(t, websocket.MessageText, msgType)
		return message
	}

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"input":[{"type":"input_text","text":"hello"}]}`)
	firstTurn := readMessage()
	require.Equal(t, "resp_auto_prev_ref_1", gjson.GetBytes(firstTurn, "response.id").String())

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"input":[{"type":"item_reference","id":"call_ref_1"},{"type":"function_call_output","call_id":"call_ref_1","output":"ok"},{"type":"message","role":"user","content":[{"type":"input_text","text":"retry"}]}]}`)
	secondTurn := readMessage()
	require.Equal(t, "resp_auto_prev_ref_2", gjson.GetBytes(secondTurn, "response.id").String())

	require.NoError(t, clientConn.Close(websocket.StatusNormalClosure, "done"))
	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 结束超时")
	}

	require.Equal(t, 1, captureDialer.DialCount())
	require.Len(t, captureConn.writes, 2)
	require.Equal(t, "resp_auto_prev_ref_1", gjson.Get(requestToJSONString(captureConn.writes[1]), "previous_response_id").String(), "仅有 item_reference 不足以自包含 function_call_output，应回填上一轮响应 ID")
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_PreflightPingFailReconnectsBeforeTurn(t *testing.T) {
	prevPreflightPingIdle := openAIWSIngressPreflightPingIdle
	openAIWSIngressPreflightPingIdle = 0
	defer func() {
		openAIWSIngressPreflightPingIdle = prevPreflightPingIdle
	}()

	options := &wsFixtureOptions{}
	options.Request.URLPolicy.Enabled = false
	options.Request.URLPolicy.AllowInsecureHTTP = true

	options.Pool.MaxConnsPerProvider = 1
	options.Pool.MinIdlePerProvider = 0
	options.Pool.MaxIdlePerProvider = 1
	options.Pool.QueueLimitPerConn = 8
	options.WS.DialTimeoutSeconds = 3
	options.WS.ReadTimeoutSeconds = 3
	options.WS.WriteTimeoutSeconds = 3

	firstConn := &openAIWSPreflightFailConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_turn_ping_1","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	secondConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_turn_ping_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	dialer := &openAIWSQueueDialer{
		conns: []openai.WSClientConn{firstConn, secondConn},
	}
	pool := newOpenAIWSConnPool(options)
	pool.SetClientDialerForTest(dialer)

	svc := newWSFixture(wsFixtureInputs{options: options, transport: &auxiliaryHTTPRecorder{}, cache: &testkit.StickyCache{}, corrector: openai.NewCodexToolCorrector(), pool: pool})

	provider := &gatewayadapter.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 116,
			Name:        "openai-ingress-preflight-ping",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Credentials: map[string]any{
				"api_key": "sk-test",
			},
			Extra: map[string]any{
				"responses_websockets_v2_enabled": true,
			},
		},
	}

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			CompressionMode: websocket.CompressionContextTakeover,
		})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			_ = conn.CloseNow()
		}()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != websocket.MessageText && msgType != websocket.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, provider, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := websocket.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeMessage := func(payload string) {
		writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, clientConn.Write(writeCtx, websocket.MessageText, []byte(payload)))
	}
	readMessage := func() []byte {
		readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		msgType, message, readErr := clientConn.Read(readCtx)
		require.NoError(t, readErr)
		require.Equal(t, websocket.MessageText, msgType)
		return message
	}

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false}`)
	firstTurn := readMessage()
	require.Equal(t, "resp_turn_ping_1", gjson.GetBytes(firstTurn, "response.id").String())

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"previous_response_id":"resp_turn_ping_1"}`)
	secondTurn := readMessage()
	require.Equal(t, "resp_turn_ping_2", gjson.GetBytes(secondTurn, "response.id").String())

	require.NoError(t, clientConn.Close(websocket.StatusNormalClosure, "done"))
	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 结束超时")
	}
	require.Equal(t, 2, dialer.DialCount(), "第二轮 turn 前 ping 失败应触发换连")
	require.Equal(t, 1, firstConn.WriteCount(), "preflight ping 失败后不应继续向旧连接发送第二轮 turn")
	require.GreaterOrEqual(t, firstConn.PingCount(), 1, "第二轮前应对旧连接执行 preflight ping")
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_StoreDisabledStrictAffinityPreflightPingFailAutoRecoveryReconnects(t *testing.T) {
	prevPreflightPingIdle := openAIWSIngressPreflightPingIdle
	openAIWSIngressPreflightPingIdle = 0
	defer func() {
		openAIWSIngressPreflightPingIdle = prevPreflightPingIdle
	}()

	options := &wsFixtureOptions{}
	options.Request.URLPolicy.Enabled = false
	options.Request.URLPolicy.AllowInsecureHTTP = true

	options.Pool.MaxConnsPerProvider = 2
	options.Pool.MinIdlePerProvider = 0
	options.Pool.MaxIdlePerProvider = 2
	options.Pool.QueueLimitPerConn = 8
	options.WS.DialTimeoutSeconds = 3
	options.WS.ReadTimeoutSeconds = 3
	options.WS.WriteTimeoutSeconds = 3

	firstConn := &openAIWSPreflightFailConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_turn_ping_strict_1","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	secondConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_turn_ping_strict_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	dialer := &openAIWSQueueDialer{
		conns: []openai.WSClientConn{firstConn, secondConn},
	}
	pool := newOpenAIWSConnPool(options)
	pool.SetClientDialerForTest(dialer)

	svc := newWSFixture(wsFixtureInputs{options: options, transport: &auxiliaryHTTPRecorder{}, cache: &testkit.StickyCache{}, corrector: openai.NewCodexToolCorrector(), pool: pool})

	provider := &gatewayadapter.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 121,
			Name:        "openai-ingress-preflight-ping-strict-affinity",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Credentials: map[string]any{
				"api_key": "sk-test",
			},
			Extra: map[string]any{
				"responses_websockets_v2_enabled": true,
			},
		},
	}

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			CompressionMode: websocket.CompressionContextTakeover,
		})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			_ = conn.CloseNow()
		}()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != websocket.MessageText && msgType != websocket.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, provider, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := websocket.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeMessage := func(payload string) {
		writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, clientConn.Write(writeCtx, websocket.MessageText, []byte(payload)))
	}
	readMessage := func() []byte {
		readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		msgType, message, readErr := clientConn.Read(readCtx)
		require.NoError(t, readErr)
		require.Equal(t, websocket.MessageText, msgType)
		return message
	}

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"input":[{"type":"input_text","text":"hello"}]}`)
	firstTurn := readMessage()
	require.Equal(t, "resp_turn_ping_strict_1", gjson.GetBytes(firstTurn, "response.id").String())

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"previous_response_id":"resp_turn_ping_strict_1","input":[{"type":"input_text","text":"world"}]}`)
	secondTurn := readMessage()
	require.Equal(t, "resp_turn_ping_strict_2", gjson.GetBytes(secondTurn, "response.id").String())

	require.NoError(t, clientConn.Close(websocket.StatusNormalClosure, "done"))
	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 严格亲和自动恢复后结束超时")
	}

	require.Equal(t, 2, dialer.DialCount(), "严格亲和 preflight ping 失败后应自动降级并换连重放")
	require.Equal(t, 1, firstConn.WriteCount(), "preflight ping 失败后不应继续在旧连接写第二轮")
	require.GreaterOrEqual(t, firstConn.PingCount(), 1, "第二轮前应执行 preflight ping")
	secondConn.mu.Lock()
	secondWrites := append([]map[string]any(nil), secondConn.writes...)
	secondConn.mu.Unlock()
	require.Len(t, secondWrites, 1)
	secondWrite := requestToJSONString(secondWrites[0])
	require.False(t, gjson.Get(secondWrite, "previous_response_id").Exists(), "自动恢复重放应移除 previous_response_id")
	require.Equal(t, 2, len(gjson.Get(secondWrite, "input").Array()), "自动恢复重放应使用完整 input 上下文")
	require.Equal(t, "hello", gjson.Get(secondWrite, "input.0.text").String())
	require.Equal(t, "world", gjson.Get(secondWrite, "input.1.text").String())
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_StoreDisabledPreflightPingFailReplaysFunctionCallOutputWithContext(t *testing.T) {
	prevPreflightPingIdle := openAIWSIngressPreflightPingIdle
	openAIWSIngressPreflightPingIdle = 0
	defer func() {
		openAIWSIngressPreflightPingIdle = prevPreflightPingIdle
	}()

	options := &wsFixtureOptions{}
	options.Request.URLPolicy.Enabled = false
	options.Request.URLPolicy.AllowInsecureHTTP = true

	options.Pool.MaxConnsPerProvider = 2
	options.Pool.MinIdlePerProvider = 0
	options.Pool.MaxIdlePerProvider = 2
	options.Pool.QueueLimitPerConn = 8
	options.WS.DialTimeoutSeconds = 3
	options.WS.ReadTimeoutSeconds = 3
	options.WS.WriteTimeoutSeconds = 3

	firstConn := &openAIWSPreflightFailConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_turn_ping_replay_ctx_1","model":"gpt-5.1","output":[{"type":"function_call","id":"fc_replay_1","call_id":"call_replay_1","name":"shell","arguments":"{}"}],"usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	secondConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_turn_ping_replay_ctx_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	dialer := &openAIWSQueueDialer{
		conns: []openai.WSClientConn{firstConn, secondConn},
	}
	pool := newOpenAIWSConnPool(options)
	pool.SetClientDialerForTest(dialer)

	svc := newWSFixture(wsFixtureInputs{options: options, transport: &auxiliaryHTTPRecorder{}, cache: &testkit.StickyCache{}, corrector: openai.NewCodexToolCorrector(), pool: pool})

	provider := &gatewayadapter.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 128,
			Name:        "openai-ingress-preflight-replay-function-output-with-context",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Credentials: map[string]any{
				"api_key": "sk-test",
			},
			Extra: map[string]any{
				"responses_websockets_v2_enabled": true,
			},
		},
	}

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			CompressionMode: websocket.CompressionContextTakeover,
		})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			_ = conn.CloseNow()
		}()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != websocket.MessageText && msgType != websocket.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, provider, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := websocket.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeMessage := func(payload string) {
		writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, clientConn.Write(writeCtx, websocket.MessageText, []byte(payload)))
	}
	readMessage := func() []byte {
		readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		msgType, message, readErr := clientConn.Read(readCtx)
		require.NoError(t, readErr)
		require.Equal(t, websocket.MessageText, msgType)
		return message
	}

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"call tool"}]}]}`)
	firstTurn := readMessage()
	require.Equal(t, "resp_turn_ping_replay_ctx_1", gjson.GetBytes(firstTurn, "response.id").String())

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"previous_response_id":"resp_turn_ping_replay_ctx_1","input":[{"type":"function_call_output","call_id":"call_replay_1","output":"ok"}]}`)
	secondTurn := readMessage()
	require.Equal(t, "resp_turn_ping_replay_ctx_2", gjson.GetBytes(secondTurn, "response.id").String())

	require.NoError(t, clientConn.Close(websocket.StatusNormalClosure, "done"))
	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket function_call_output 自包含重放后结束超时")
	}

	require.Equal(t, 2, dialer.DialCount(), "带完整 tool 上下文的 function_call_output 应在 ping 失败后换新连接重放")
	require.Equal(t, 1, firstConn.WriteCount())
	require.GreaterOrEqual(t, firstConn.PingCount(), 1)
	secondConn.mu.Lock()
	secondWrites := append([]map[string]any(nil), secondConn.writes...)
	secondConn.mu.Unlock()
	require.Len(t, secondWrites, 1)
	secondWrite := requestToJSONString(secondWrites[0])
	require.False(t, gjson.Get(secondWrite, "previous_response_id").Exists())
	require.Equal(t, 3, len(gjson.Get(secondWrite, "input").Array()))
	require.Equal(t, "message", gjson.Get(secondWrite, "input.0.type").String())
	require.Equal(t, "function_call", gjson.Get(secondWrite, "input.1.type").String())
	require.Equal(t, "call_replay_1", gjson.Get(secondWrite, "input.1.call_id").String())
	require.Equal(t, "function_call_output", gjson.Get(secondWrite, "input.2.type").String())
	require.Equal(t, "call_replay_1", gjson.Get(secondWrite, "input.2.call_id").String())
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_StoreDisabledPreflightPingFailClosesWhenFunctionCallOutputNeedsPreviousResponseID(t *testing.T) {
	prevPreflightPingIdle := openAIWSIngressPreflightPingIdle
	openAIWSIngressPreflightPingIdle = 0
	defer func() {
		openAIWSIngressPreflightPingIdle = prevPreflightPingIdle
	}()

	options := &wsFixtureOptions{}
	options.Request.URLPolicy.Enabled = false
	options.Request.URLPolicy.AllowInsecureHTTP = true

	options.Pool.MaxConnsPerProvider = 2
	options.Pool.MinIdlePerProvider = 0
	options.Pool.MaxIdlePerProvider = 2
	options.Pool.QueueLimitPerConn = 8
	options.WS.DialTimeoutSeconds = 3
	options.WS.ReadTimeoutSeconds = 3
	options.WS.WriteTimeoutSeconds = 3

	firstConn := &openAIWSPreflightFailConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_turn_ping_replay_fc_1","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	secondConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"error","error":{"type":"invalid_request_error","code":"previous_response_not_found","message":"Previous response not found."}}`),
		},
	}
	dialer := &openAIWSQueueDialer{
		conns: []openai.WSClientConn{firstConn, secondConn},
	}
	pool := newOpenAIWSConnPool(options)
	pool.SetClientDialerForTest(dialer)

	svc := newWSFixture(wsFixtureInputs{options: options, transport: &auxiliaryHTTPRecorder{}, cache: &testkit.StickyCache{}, corrector: openai.NewCodexToolCorrector(), pool: pool})

	provider := &gatewayadapter.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 129,
			Name:        "openai-ingress-preflight-replay-function-output",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Credentials: map[string]any{
				"api_key": "sk-test",
			},
			Extra: map[string]any{
				"responses_websockets_v2_enabled": true,
			},
		},
	}

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			CompressionMode: websocket.CompressionContextTakeover,
		})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			_ = conn.CloseNow()
		}()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != websocket.MessageText && msgType != websocket.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, provider, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := websocket.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeMessage := func(payload string) {
		writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, clientConn.Write(writeCtx, websocket.MessageText, []byte(payload)))
	}
	readMessage := func() []byte {
		readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		msgType, message, readErr := clientConn.Read(readCtx)
		require.NoError(t, readErr)
		require.Equal(t, websocket.MessageText, msgType)
		return message
	}

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"input":[{"type":"function_call","call_id":"call_other","name":"shell","arguments":"{}"},{"type":"function_call_output","call_id":"call_replay_1","output":"ok"}]}`)
	firstTurn := readMessage()
	require.Equal(t, "resp_turn_ping_replay_fc_1", gjson.GetBytes(firstTurn, "response.id").String())

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"previous_response_id":"resp_turn_ping_replay_fc_1","input":[{"type":"function_call_output","call_id":"call_replay_1","output":"ok"}]}`)

	select {
	case serverErr := <-serverErrCh:
		require.Error(t, serverErr)
		var closeErr *gatewayhttp.OpenAIWSClientCloseError
		require.ErrorAs(t, serverErr, &closeErr)
		require.Equal(t, websocket.StatusPolicyViolation, closeErr.StatusCode())
		require.Contains(t, closeErr.Reason(), "upstream continuation connection is unavailable")
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 结束超时")
	}

	require.Equal(t, 1, dialer.DialCount(), "需要 previous_response_id 的 function_call_output 在原连接不可用时不应换新连接重试")
	secondConn.mu.Lock()
	secondWrites := append([]map[string]any(nil), secondConn.writes...)
	secondConn.mu.Unlock()
	require.Empty(t, secondWrites, "不能把旧连接的 previous_response_id 发送到新上游，否则会触发 previous_response_not_found")
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_StoreDisabledPreflightPingFailClosesWhenReplayHasFunctionCallOutput(t *testing.T) {
	prevPreflightPingIdle := openAIWSIngressPreflightPingIdle
	openAIWSIngressPreflightPingIdle = 0
	defer func() {
		openAIWSIngressPreflightPingIdle = prevPreflightPingIdle
	}()

	options := &wsFixtureOptions{}
	options.Request.URLPolicy.Enabled = false
	options.Request.URLPolicy.AllowInsecureHTTP = true

	options.Pool.MaxConnsPerProvider = 2
	options.Pool.MinIdlePerProvider = 0
	options.Pool.MaxIdlePerProvider = 2
	options.Pool.QueueLimitPerConn = 8
	options.WS.DialTimeoutSeconds = 3
	options.WS.ReadTimeoutSeconds = 3
	options.WS.WriteTimeoutSeconds = 3

	firstConn := &openAIWSPreflightFailConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_turn_ping_replay_only_fc_1","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	secondConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"No tool call found for function call output with call_id call_replay_1.","param":"input"}}`),
		},
	}
	dialer := &openAIWSQueueDialer{
		conns: []openai.WSClientConn{firstConn, secondConn},
	}
	pool := newOpenAIWSConnPool(options)
	pool.SetClientDialerForTest(dialer)

	svc := newWSFixture(wsFixtureInputs{options: options, transport: &auxiliaryHTTPRecorder{}, cache: &testkit.StickyCache{}, corrector: openai.NewCodexToolCorrector(), pool: pool})

	provider := &gatewayadapter.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 130,
			Name:        "openai-ingress-preflight-replay-only-function-output",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Credentials: map[string]any{
				"api_key": "sk-test",
			},
			Extra: map[string]any{
				"responses_websockets_v2_enabled": true,
			},
		},
	}

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			CompressionMode: websocket.CompressionContextTakeover,
		})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			_ = conn.CloseNow()
		}()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != websocket.MessageText && msgType != websocket.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, provider, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := websocket.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeMessage := func(payload string) {
		writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, clientConn.Write(writeCtx, websocket.MessageText, []byte(payload)))
	}
	readMessage := func() []byte {
		readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		msgType, message, readErr := clientConn.Read(readCtx)
		require.NoError(t, readErr)
		require.Equal(t, websocket.MessageText, msgType)
		return message
	}

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"input":[{"type":"function_call","call_id":"call_other","name":"shell","arguments":"{}"},{"type":"function_call_output","call_id":"call_replay_1","output":"ok"}]}`)
	firstTurn := readMessage()
	require.Equal(t, "resp_turn_ping_replay_only_fc_1", gjson.GetBytes(firstTurn, "response.id").String())

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"previous_response_id":"resp_turn_ping_replay_only_fc_1","input":[{"type":"input_text","text":"after tool output"}]}`)

	select {
	case serverErr := <-serverErrCh:
		require.Error(t, serverErr)
		var closeErr *gatewayhttp.OpenAIWSClientCloseError
		require.ErrorAs(t, serverErr, &closeErr)
		require.Equal(t, websocket.StatusPolicyViolation, closeErr.StatusCode())
		require.Contains(t, closeErr.Reason(), "upstream continuation connection is unavailable")
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 结束超时")
	}

	require.Equal(t, 1, dialer.DialCount(), "replay input 带 function_call_output 时不应换新连接重试")
	secondConn.mu.Lock()
	secondWrites := append([]map[string]any(nil), secondConn.writes...)
	secondConn.mu.Unlock()
	require.Empty(t, secondWrites, "不能把会触发 No tool call found 的重放请求发到新上游")
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_WriteFailBeforeDownstreamRetriesOnce(t *testing.T) {
	options := &wsFixtureOptions{}
	options.Request.URLPolicy.Enabled = false
	options.Request.URLPolicy.AllowInsecureHTTP = true

	options.Pool.MaxConnsPerProvider = 1
	options.Pool.MinIdlePerProvider = 0
	options.Pool.MaxIdlePerProvider = 1
	options.Pool.QueueLimitPerConn = 8
	options.WS.DialTimeoutSeconds = 3
	options.WS.ReadTimeoutSeconds = 3
	options.WS.WriteTimeoutSeconds = 3

	firstConn := &openAIWSWriteFailAfterFirstTurnConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_turn_write_retry_1","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	secondConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_turn_write_retry_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	dialer := &openAIWSQueueDialer{
		conns: []openai.WSClientConn{firstConn, secondConn},
	}
	pool := newOpenAIWSConnPool(options)
	pool.SetClientDialerForTest(dialer)

	svc := newWSFixture(wsFixtureInputs{options: options, transport: &auxiliaryHTTPRecorder{}, cache: &testkit.StickyCache{}, corrector: openai.NewCodexToolCorrector(), pool: pool})

	provider := &gatewayadapter.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 117,
			Name:        "openai-ingress-write-retry",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Credentials: map[string]any{
				"api_key": "sk-test",
			},
			Extra: map[string]any{
				"responses_websockets_v2_enabled": true,
			},
		},
	}
	var hooksMu sync.Mutex
	beforeTurnCalls := make(map[int]int)
	afterTurnCalls := make(map[int]int)
	afterTurnRequestBodies := make(map[int][]byte)
	hooks := &ws.OpenAIIngressHooks{
		BeforeTurn: func(turn int) error {
			hooksMu.Lock()
			beforeTurnCalls[turn]++
			hooksMu.Unlock()
			return nil
		},
		AfterTurn: func(capture ws.OpenAITurnCapture) {
			hooksMu.Lock()
			afterTurnCalls[capture.Turn]++
			afterTurnRequestBodies[capture.Turn] = append([]byte(nil), capture.RequestBody...)
			hooksMu.Unlock()
		},
	}

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			CompressionMode: websocket.CompressionContextTakeover,
		})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			_ = conn.CloseNow()
		}()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != websocket.MessageText && msgType != websocket.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, provider, "sk-test", firstMessage, hooks)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := websocket.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeMessage := func(payload string) {
		writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, clientConn.Write(writeCtx, websocket.MessageText, []byte(payload)))
	}
	readMessage := func() []byte {
		readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		msgType, message, readErr := clientConn.Read(readCtx)
		require.NoError(t, readErr)
		require.Equal(t, websocket.MessageText, msgType)
		return message
	}

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false}`)
	firstTurn := readMessage()
	require.Equal(t, "resp_turn_write_retry_1", gjson.GetBytes(firstTurn, "response.id").String())

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"previous_response_id":"resp_turn_write_retry_1"}`)
	secondTurn := readMessage()
	require.Equal(t, "resp_turn_write_retry_2", gjson.GetBytes(secondTurn, "response.id").String())

	require.NoError(t, clientConn.Close(websocket.StatusNormalClosure, "done"))
	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 结束超时")
	}
	require.Equal(t, 2, dialer.DialCount(), "第二轮 turn 上游写失败且未写下游时应自动重试并换连")
	hooksMu.Lock()
	beforeTurn1 := beforeTurnCalls[1]
	beforeTurn2 := beforeTurnCalls[2]
	afterTurn1 := afterTurnCalls[1]
	afterTurn2 := afterTurnCalls[2]
	afterTurnRequestBody1 := append([]byte(nil), afterTurnRequestBodies[1]...)
	afterTurnRequestBody2 := append([]byte(nil), afterTurnRequestBodies[2]...)
	hooksMu.Unlock()
	require.Equal(t, 1, beforeTurn1, "首轮 turn BeforeTurn 应执行一次")
	require.Equal(t, 1, beforeTurn2, "同一 turn 重试不应重复触发 BeforeTurn")
	require.Equal(t, 1, afterTurn1, "首轮 turn AfterTurn 应执行一次")
	require.Equal(t, 1, afterTurn2, "第二轮 turn AfterTurn 应执行一次")
	require.JSONEq(t, `{"type":"response.create","model":"gpt-5.1","stream":false}`, string(afterTurnRequestBody1), "首轮结算请求体不能缺失或被后续 turn 覆盖")
	require.JSONEq(t, `{"type":"response.create","model":"gpt-5.1","stream":false,"previous_response_id":"resp_turn_write_retry_1"}`, string(afterTurnRequestBody2), "第二轮结算请求体必须来自当前 turn")
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_PreviousResponseNotFoundRecoversByDroppingPrevID(t *testing.T) {
	options := &wsFixtureOptions{}
	options.Request.URLPolicy.Enabled = false
	options.Request.URLPolicy.AllowInsecureHTTP = true

	options.WS.IngressPreviousResponseRecoveryEnabled = true
	options.Pool.MaxConnsPerProvider = 1
	options.Pool.MinIdlePerProvider = 0
	options.Pool.MaxIdlePerProvider = 1
	options.Pool.QueueLimitPerConn = 8
	options.WS.DialTimeoutSeconds = 3
	options.WS.ReadTimeoutSeconds = 3
	options.WS.WriteTimeoutSeconds = 3

	firstConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_turn_prev_recover_1","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
			[]byte(`{"type":"error","error":{"type":"invalid_request_error","code":"previous_response_not_found","message":""}}`),
		},
	}
	secondConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_turn_prev_recover_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	dialer := &openAIWSQueueDialer{
		conns: []openai.WSClientConn{firstConn, secondConn},
	}
	pool := newOpenAIWSConnPool(options)
	pool.SetClientDialerForTest(dialer)

	svc := newWSFixture(wsFixtureInputs{options: options, transport: &auxiliaryHTTPRecorder{}, cache: &testkit.StickyCache{}, corrector: openai.NewCodexToolCorrector(), pool: pool})

	provider := &gatewayadapter.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 118,
			Name:        "openai-ingress-prev-recovery",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Credentials: map[string]any{
				"api_key": "sk-test",
			},
			Extra: map[string]any{
				"responses_websockets_v2_enabled": true,
			},
		},
	}

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			CompressionMode: websocket.CompressionContextTakeover,
		})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			_ = conn.CloseNow()
		}()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != websocket.MessageText && msgType != websocket.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, provider, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := websocket.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeMessage := func(payload string) {
		writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, clientConn.Write(writeCtx, websocket.MessageText, []byte(payload)))
	}
	readMessage := func() []byte {
		readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		msgType, message, readErr := clientConn.Read(readCtx)
		require.NoError(t, readErr)
		require.Equal(t, websocket.MessageText, msgType)
		return message
	}

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"previous_response_id":"resp_seed_anchor"}`)
	firstTurn := readMessage()
	require.Equal(t, "resp_turn_prev_recover_1", gjson.GetBytes(firstTurn, "response.id").String())

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"previous_response_id":"resp_turn_prev_recover_1"}`)
	secondTurn := readMessage()
	require.Equal(t, "response.completed", gjson.GetBytes(secondTurn, "type").String())
	require.Equal(t, "resp_turn_prev_recover_2", gjson.GetBytes(secondTurn, "response.id").String())

	require.NoError(t, clientConn.Close(websocket.StatusNormalClosure, "done"))
	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 结束超时")
	}

	require.Equal(t, 2, dialer.DialCount(), "previous_response_not_found 恢复应触发换连重试")

	firstConn.mu.Lock()
	firstWrites := append([]map[string]any(nil), firstConn.writes...)
	firstConn.mu.Unlock()
	require.Len(t, firstWrites, 2, "首个连接应处理首轮与失败的第二轮请求")
	require.True(t, gjson.Get(requestToJSONString(firstWrites[1]), "previous_response_id").Exists(), "失败轮次首发请求应包含 previous_response_id")

	secondConn.mu.Lock()
	secondWrites := append([]map[string]any(nil), secondConn.writes...)
	secondConn.mu.Unlock()
	require.Len(t, secondWrites, 1, "恢复重试应在第二个连接发送一次请求")
	require.False(t, gjson.Get(requestToJSONString(secondWrites[0]), "previous_response_id").Exists(), "恢复重试应移除 previous_response_id")
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_StoreDisabledStrictAffinityPreviousResponseNotFoundLayer2Recovery(t *testing.T) {
	options := &wsFixtureOptions{}
	options.Request.URLPolicy.Enabled = false
	options.Request.URLPolicy.AllowInsecureHTTP = true

	options.WS.IngressPreviousResponseRecoveryEnabled = true
	options.Pool.MaxConnsPerProvider = 1
	options.Pool.MinIdlePerProvider = 0
	options.Pool.MaxIdlePerProvider = 1
	options.Pool.QueueLimitPerConn = 8
	options.WS.DialTimeoutSeconds = 3
	options.WS.ReadTimeoutSeconds = 3
	options.WS.WriteTimeoutSeconds = 3

	firstConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_turn_prev_strict_recover_1","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
			[]byte(`{"type":"error","error":{"type":"invalid_request_error","code":"previous_response_not_found","message":"missing strict anchor"}}`),
		},
	}
	secondConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_turn_prev_strict_recover_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	dialer := &openAIWSQueueDialer{
		conns: []openai.WSClientConn{firstConn, secondConn},
	}
	pool := newOpenAIWSConnPool(options)
	pool.SetClientDialerForTest(dialer)

	svc := newWSFixture(wsFixtureInputs{options: options, transport: &auxiliaryHTTPRecorder{}, cache: &testkit.StickyCache{}, corrector: openai.NewCodexToolCorrector(), pool: pool})

	provider := &gatewayadapter.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 122,
			Name:        "openai-ingress-prev-strict-layer2",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Credentials: map[string]any{
				"api_key": "sk-test",
			},
			Extra: map[string]any{
				"responses_websockets_v2_enabled": true,
			},
		},
	}

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			CompressionMode: websocket.CompressionContextTakeover,
		})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			_ = conn.CloseNow()
		}()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != websocket.MessageText && msgType != websocket.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, provider, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := websocket.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeMessage := func(payload string) {
		writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, clientConn.Write(writeCtx, websocket.MessageText, []byte(payload)))
	}
	readMessage := func() []byte {
		readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		msgType, message, readErr := clientConn.Read(readCtx)
		require.NoError(t, readErr)
		require.Equal(t, websocket.MessageText, msgType)
		return message
	}

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"prompt_cache_key":"pk_strict_layer2","input":[{"type":"input_text","text":"hello"}]}`)
	firstTurn := readMessage()
	require.Equal(t, "resp_turn_prev_strict_recover_1", gjson.GetBytes(firstTurn, "response.id").String())

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"prompt_cache_key":"pk_strict_layer2","previous_response_id":"resp_turn_prev_strict_recover_1","input":[{"type":"input_text","text":"world"}]}`)
	secondTurn := readMessage()
	require.Equal(t, "resp_turn_prev_strict_recover_2", gjson.GetBytes(secondTurn, "response.id").String())

	require.NoError(t, clientConn.Close(websocket.StatusNormalClosure, "done"))
	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 严格亲和 Layer2 恢复结束超时")
	}

	require.Equal(t, 2, dialer.DialCount(), "严格亲和链路命中 previous_response_not_found 应触发 Layer2 恢复重试")

	firstConn.mu.Lock()
	firstWrites := append([]map[string]any(nil), firstConn.writes...)
	firstConn.mu.Unlock()
	require.Len(t, firstWrites, 2, "首连接应收到首轮请求和失败的续链请求")
	require.True(t, gjson.Get(requestToJSONString(firstWrites[1]), "previous_response_id").Exists())

	secondConn.mu.Lock()
	secondWrites := append([]map[string]any(nil), secondConn.writes...)
	secondConn.mu.Unlock()
	require.Len(t, secondWrites, 1, "Layer2 恢复应仅重放一次")
	secondWrite := requestToJSONString(secondWrites[0])
	require.False(t, gjson.Get(secondWrite, "previous_response_id").Exists(), "Layer2 恢复重放应移除 previous_response_id")
	require.True(t, gjson.Get(secondWrite, "store").Exists(), "Layer2 恢复不应改变 store 标志")
	require.False(t, gjson.Get(secondWrite, "store").Bool())
	require.Equal(t, 2, len(gjson.Get(secondWrite, "input").Array()), "Layer2 恢复应重放完整 input 上下文")
	require.Equal(t, "hello", gjson.Get(secondWrite, "input.0.text").String())
	require.Equal(t, "world", gjson.Get(secondWrite, "input.1.text").String())
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_PreviousResponseNotFoundRecoveryRemovesDuplicatePrevID(t *testing.T) {
	options := &wsFixtureOptions{}
	options.Request.URLPolicy.Enabled = false
	options.Request.URLPolicy.AllowInsecureHTTP = true

	options.WS.IngressPreviousResponseRecoveryEnabled = true
	options.Pool.MaxConnsPerProvider = 1
	options.Pool.MinIdlePerProvider = 0
	options.Pool.MaxIdlePerProvider = 1
	options.Pool.QueueLimitPerConn = 8
	options.WS.DialTimeoutSeconds = 3
	options.WS.ReadTimeoutSeconds = 3
	options.WS.WriteTimeoutSeconds = 3

	firstConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_turn_prev_once_1","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
			[]byte(`{"type":"error","error":{"type":"invalid_request_error","code":"previous_response_not_found","message":"first missing"}}`),
		},
	}
	secondConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_turn_prev_once_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	dialer := &openAIWSQueueDialer{
		conns: []openai.WSClientConn{firstConn, secondConn},
	}
	pool := newOpenAIWSConnPool(options)
	pool.SetClientDialerForTest(dialer)

	svc := newWSFixture(wsFixtureInputs{options: options, transport: &auxiliaryHTTPRecorder{}, cache: &testkit.StickyCache{}, corrector: openai.NewCodexToolCorrector(), pool: pool})

	provider := &gatewayadapter.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 120,
			Name:        "openai-ingress-prev-recovery-once",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Credentials: map[string]any{
				"api_key": "sk-test",
			},
			Extra: map[string]any{
				"responses_websockets_v2_enabled": true,
			},
		},
	}

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			CompressionMode: websocket.CompressionContextTakeover,
		})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			_ = conn.CloseNow()
		}()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != websocket.MessageText && msgType != websocket.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, provider, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := websocket.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeMessage := func(payload string) {
		writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, clientConn.Write(writeCtx, websocket.MessageText, []byte(payload)))
	}
	readMessage := func() []byte {
		readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		msgType, message, readErr := clientConn.Read(readCtx)
		require.NoError(t, readErr)
		require.Equal(t, websocket.MessageText, msgType)
		return message
	}

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false}`)
	firstTurn := readMessage()
	require.Equal(t, "resp_turn_prev_once_1", gjson.GetBytes(firstTurn, "response.id").String())

	// Remove every duplicate previous_response_id during recovery so the stale ID cannot fail again.
	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"previous_response_id":"resp_turn_prev_once_1","input":[],"previous_response_id":"resp_turn_prev_duplicate"}`)
	secondTurn := readMessage()
	require.Equal(t, "resp_turn_prev_once_2", gjson.GetBytes(secondTurn, "response.id").String())

	require.NoError(t, clientConn.Close(websocket.StatusNormalClosure, "done"))
	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 结束超时")
	}

	require.Equal(t, 2, dialer.DialCount(), "previous_response_not_found 恢复应只重试一次")

	firstConn.mu.Lock()
	firstWrites := append([]map[string]any(nil), firstConn.writes...)
	firstConn.mu.Unlock()
	require.Len(t, firstWrites, 2)
	require.True(t, gjson.Get(requestToJSONString(firstWrites[1]), "previous_response_id").Exists())

	secondConn.mu.Lock()
	secondWrites := append([]map[string]any(nil), secondConn.writes...)
	secondConn.mu.Unlock()
	require.Len(t, secondWrites, 1)
	require.False(t, gjson.Get(requestToJSONString(secondWrites[0]), "previous_response_id").Exists(), "重复键场景恢复重试后不应保留 previous_response_id")
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_RejectsMessageIDAsPreviousResponseID(t *testing.T) {
	options := &wsFixtureOptions{}
	options.Request.URLPolicy.Enabled = false
	options.Request.URLPolicy.AllowInsecureHTTP = true

	svc := newWSFixture(wsFixtureInputs{options: options, transport: &auxiliaryHTTPRecorder{}, cache: &testkit.StickyCache{}, corrector: openai.NewCodexToolCorrector()})

	provider := &gatewayadapter.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 119,
			Name:        "openai-ingress-prev-validation",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Credentials: map[string]any{
				"api_key": "sk-test",
			},
			Extra: map[string]any{
				"responses_websockets_v2_enabled": true,
			},
		},
	}

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			CompressionMode: websocket.CompressionContextTakeover,
		})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			_ = conn.CloseNow()
		}()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != websocket.MessageText && msgType != websocket.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, provider, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := websocket.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, websocket.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","stream":false,"previous_response_id":"msg_123456"}`))
	cancelWrite()
	require.NoError(t, err)

	select {
	case serverErr := <-serverErrCh:
		require.Error(t, serverErr)
		var closeErr *gatewayhttp.OpenAIWSClientCloseError
		require.ErrorAs(t, serverErr, &closeErr)
		require.Equal(t, websocket.StatusPolicyViolation, closeErr.StatusCode())
		require.Contains(t, closeErr.Reason(), "previous_response_id must be a response.id")
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 结束超时")
	}
}

func (d *openAIWSQueueDialer) Dial(
	ctx context.Context,
	wsURL string,
	headers http.Header,
	proxyURL string,
	profile *tlsfingerprint.Profile,
) (openai.WSClientConn, int, http.Header, error) {
	_ = ctx
	_ = wsURL
	_ = headers
	_ = proxyURL
	_ = profile
	d.mu.Lock()
	defer d.mu.Unlock()
	d.dialCount++
	if len(d.conns) == 0 {
		return nil, 503, nil, errors.New("no test conn")
	}
	conn := d.conns[0]
	if len(d.conns) > 1 {
		d.conns = d.conns[1:]
	}
	return conn, 0, nil, nil
}

func (d *openAIWSQueueDialer) DialCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.dialCount
}

func (c *openAIWSPreflightFailConn) WriteJSON(context.Context, any) error {
	c.mu.Lock()
	c.writeCount++
	c.mu.Unlock()
	return nil
}

func (c *openAIWSPreflightFailConn) ReadMessage(context.Context) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.events) == 0 {
		return nil, io.EOF
	}
	event := c.events[0]
	c.events = c.events[1:]
	if len(c.events) == 0 {
		c.pingFails = true
	}
	return event, nil
}

func (c *openAIWSPreflightFailConn) Ping(context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pingCount++
	if c.pingFails {
		return errors.New("preflight ping failed")
	}
	return nil
}

func (c *openAIWSPreflightFailConn) Close() error {
	return nil
}

func (c *openAIWSPreflightFailConn) WriteCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writeCount
}

func (c *openAIWSPreflightFailConn) PingCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pingCount
}

func (c *openAIWSWriteFailAfterFirstTurnConn) WriteJSON(context.Context, any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failOnWrite {
		return errors.New("write failed on stale conn")
	}
	return nil
}

func (c *openAIWSWriteFailAfterFirstTurnConn) ReadMessage(context.Context) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.events) == 0 {
		return nil, io.EOF
	}
	event := c.events[0]
	c.events = c.events[1:]
	if len(c.events) == 0 {
		c.failOnWrite = true
	}
	return event, nil
}

func (c *openAIWSWriteFailAfterFirstTurnConn) Ping(context.Context) error {
	return nil
}

func (c *openAIWSWriteFailAfterFirstTurnConn) Close() error {
	return nil
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_ClientDisconnectStillDrainsUpstream(t *testing.T) {
	options := &wsFixtureOptions{}
	options.Request.URLPolicy.Enabled = false
	options.Request.URLPolicy.AllowInsecureHTTP = true

	options.Pool.MaxConnsPerProvider = 1
	options.Pool.MinIdlePerProvider = 0
	options.Pool.MaxIdlePerProvider = 1
	options.Pool.QueueLimitPerConn = 8
	options.WS.DialTimeoutSeconds = 3
	options.WS.ReadTimeoutSeconds = 3
	options.WS.WriteTimeoutSeconds = 3

	// Send two nonterminal events followed by a terminal event.
	// Delay the first event 250 ms to let client disconnection reach the server before its write.
	captureConn := &openAIWSCaptureConn{
		readDelays: []time.Duration{250 * time.Millisecond, 0, 0},
		events: [][]byte{
			[]byte(`{"type":"response.created","response":{"id":"resp_ingress_disconnect","model":"gpt-5.1"}}`),
			[]byte(`{"type":"response.output_item.added","response":{"id":"resp_ingress_disconnect"}}`),
			[]byte(`{"type":"response.completed","response":{"id":"resp_ingress_disconnect","model":"gpt-5.1","usage":{"input_tokens":2,"output_tokens":1}}}`),
		},
	}
	captureDialer := &openAIWSCaptureDialer{conn: captureConn}
	pool := newOpenAIWSConnPool(options)
	pool.SetClientDialerForTest(captureDialer)

	svc := newWSFixture(wsFixtureInputs{options: options, transport: &auxiliaryHTTPRecorder{}, cache: &testkit.StickyCache{}, corrector: openai.NewCodexToolCorrector(), pool: pool})

	provider := &gatewayadapter.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 115,
			Name:        "openai-ingress-client-disconnect",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Credentials: map[string]any{
				"api_key": "sk-test",
				"model_mapping": map[string]any{
					"custom-original-model": "gpt-5.1",
				},
			},
			Extra: map[string]any{
				"responses_websockets_v2_enabled": true,
			},
		},
	}

	serverErrCh := make(chan error, 1)
	resultCh := make(chan *forwardcore.OpenAIResult, 1)
	hooks := &ws.OpenAIIngressHooks{
		AfterTurn: func(capture ws.OpenAITurnCapture) {
			result := capture.Result
			turnErr := capture.Err
			if turnErr == nil && result != nil {
				resultCh <- result
			}
		},
	}
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			CompressionMode: websocket.CompressionContextTakeover,
		})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			_ = conn.CloseNow()
		}()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != websocket.MessageText && msgType != websocket.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, provider, "sk-test", firstMessage, hooks)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := websocket.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, websocket.MessageText, []byte(`{"type":"response.create","model":"custom-original-model","stream":false,"service_tier":"flex"}`))
	cancelWrite()
	require.NoError(t, err)
	// Disconnect immediately to exercise client loss during relay.
	require.NoError(t, clientConn.CloseNow(), "模拟 ingress 客户端提前断连")

	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr, "客户端断连后应继续 drain 上游直到 terminal 或正常结束")
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 结束超时")
	}

	select {
	case result := <-resultCh:
		require.Equal(t, "resp_ingress_disconnect", result.RequestID)
		require.Equal(t, 2, result.Usage.InputTokens)
		require.Equal(t, 1, result.Usage.OutputTokens)
		require.NotNil(t, result.ServiceTier)
		require.Equal(t, "flex", *result.ServiceTier)
	case <-time.After(2 * time.Second):
		t.Fatal("未收到断连后的 turn 结果回调")
	}
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_ReportsCyberErrorEvent(t *testing.T) {
	options := &wsFixtureOptions{}
	options.Request.URLPolicy.Enabled = false
	options.Request.URLPolicy.AllowInsecureHTTP = true

	options.Pool.MaxConnsPerProvider = 1
	options.Pool.MinIdlePerProvider = 0
	options.Pool.MaxIdlePerProvider = 1
	options.Pool.QueueLimitPerConn = 8
	options.WS.DialTimeoutSeconds = 3
	options.WS.ReadTimeoutSeconds = 3
	options.WS.WriteTimeoutSeconds = 3

	errorEvent := []byte(`{"type":"error","error":{"type":"invalid_request_error","code":"policy_violation","message":"This request may pose a cybersecurity risk."}}`)
	captureConn := &openAIWSCaptureConn{events: [][]byte{errorEvent}}
	dialer := &openAIWSCaptureDialer{conn: captureConn}
	pool := newOpenAIWSConnPool(options)
	pool.SetClientDialerForTest(dialer)

	svc := newWSFixture(wsFixtureInputs{options: options, transport: &auxiliaryHTTPRecorder{}, cache: &testkit.StickyCache{}, corrector: openai.NewCodexToolCorrector(), pool: pool})
	provider := &gatewayadapter.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 119,
			Name:        "openai-ingress-cyber-error",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Credentials: map[string]any{
				"api_key": "sk-test",
			},
			Extra: map[string]any{
				"responses_websockets_v2_enabled": true,
			},
		},
	}

	serverErrCh := make(chan error, 1)
	type upstreamErrorRecord struct {
		turn       int
		model      string
		statusCode int
		body       []byte
		message    string
	}
	upstreamErrCh := make(chan upstreamErrorRecord, 1)
	hooks := &ws.OpenAIIngressHooks{
		OnUpstreamError: func(turn int, originalModel string, statusCode int, responseBody []byte, message string) {
			upstreamErrCh <- upstreamErrorRecord{
				turn:       turn,
				model:      originalModel,
				statusCode: statusCode,
				body:       append([]byte(nil), responseBody...),
				message:    message,
			}
		},
	}
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			CompressionMode: websocket.CompressionContextTakeover,
		})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() { _ = conn.CloseNow() }()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		ginCtx.Request = r.Clone(r.Context())

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != websocket.MessageText && msgType != websocket.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, provider, "sk-test", firstMessage, hooks)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := websocket.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() { _ = clientConn.CloseNow() }()

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, websocket.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","stream":false}`))
	cancelWrite()
	require.NoError(t, err)

	readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
	_, event, readErr := clientConn.Read(readCtx)
	cancelRead()
	require.NoError(t, readErr)
	require.JSONEq(t, string(errorEvent), string(event))

	select {
	case got := <-upstreamErrCh:
		require.Equal(t, 1, got.turn)
		require.Equal(t, "gpt-5.1", got.model)
		require.Equal(t, http.StatusBadRequest, got.statusCode)
		require.JSONEq(t, string(errorEvent), string(got.body))
		require.Contains(t, got.message, "cybersecurity risk")
	case <-time.After(2 * time.Second):
		t.Fatal("未收到上游 WS error 事件回调")
	}

	select {
	case serverErr := <-serverErrCh:
		require.Error(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 结束超时")
	}
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_ReportsCyberFailedEvent(t *testing.T) {
	options := &wsFixtureOptions{}
	options.Request.URLPolicy.Enabled = false
	options.Request.URLPolicy.AllowInsecureHTTP = true

	options.Pool.MaxConnsPerProvider = 1
	options.Pool.MinIdlePerProvider = 0
	options.Pool.MaxIdlePerProvider = 1
	options.Pool.QueueLimitPerConn = 8
	options.WS.DialTimeoutSeconds = 3
	options.WS.ReadTimeoutSeconds = 3
	options.WS.WriteTimeoutSeconds = 3

	failedEvent := []byte(`{"type":"response.failed","response":{"id":"resp_failed","error":{"message":"This request may pose a cybersecurity risk."}}}`)
	captureConn := &openAIWSCaptureConn{events: [][]byte{failedEvent}}
	dialer := &openAIWSCaptureDialer{conn: captureConn}
	pool := newOpenAIWSConnPool(options)
	pool.SetClientDialerForTest(dialer)

	svc := newWSFixture(wsFixtureInputs{options: options, transport: &auxiliaryHTTPRecorder{}, cache: &testkit.StickyCache{}, corrector: openai.NewCodexToolCorrector(), pool: pool})
	provider := &gatewayadapter.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 120,
			Name:        "openai-ingress-cyber-failed",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Credentials: map[string]any{
				"api_key": "sk-test",
			},
			Extra: map[string]any{
				"responses_websockets_v2_enabled": true,
			},
		},
	}

	serverErrCh := make(chan error, 1)
	type failedUpstreamErrorRecord struct {
		model      string
		statusCode int
	}
	upstreamErrCh := make(chan failedUpstreamErrorRecord, 1)
	hooks := &ws.OpenAIIngressHooks{
		OnUpstreamError: func(_ int, originalModel string, statusCode int, responseBody []byte, message string) {
			require.JSONEq(t, string(failedEvent), string(responseBody))
			require.Contains(t, message, "cybersecurity risk")
			upstreamErrCh <- failedUpstreamErrorRecord{
				model:      originalModel,
				statusCode: statusCode,
			}
		},
	}
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			CompressionMode: websocket.CompressionContextTakeover,
		})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() { _ = conn.CloseNow() }()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		ginCtx.Request = r.Clone(r.Context())

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != websocket.MessageText && msgType != websocket.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, provider, "sk-test", firstMessage, hooks)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := websocket.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() { _ = clientConn.CloseNow() }()

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, websocket.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","stream":false}`))
	cancelWrite()
	require.NoError(t, err)

	readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
	_, event, readErr := clientConn.Read(readCtx)
	cancelRead()
	require.NoError(t, readErr)
	require.JSONEq(t, string(failedEvent), string(event))
	require.NoError(t, clientConn.Close(websocket.StatusNormalClosure, "done"))

	select {
	case got := <-upstreamErrCh:
		require.Equal(t, "gpt-5.1", got.model)
		require.Equal(t, http.StatusBadGateway, got.statusCode)
	case <-time.After(2 * time.Second):
		t.Fatal("未收到上游 response.failed 事件回调")
	}

	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 结束超时")
	}
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_InvalidEncryptedContentLineageStripsNextTurn(t *testing.T) {
	options := &wsFixtureOptions{}
	options.Request.URLPolicy.Enabled = false
	options.Request.URLPolicy.AllowInsecureHTTP = true

	options.Pool.MaxConnsPerProvider = 1
	options.Pool.MinIdlePerProvider = 0
	options.Pool.MaxIdlePerProvider = 1
	options.Pool.QueueLimitPerConn = 8
	options.WS.DialTimeoutSeconds = 3
	options.WS.ReadTimeoutSeconds = 3
	options.WS.WriteTimeoutSeconds = 3

	upstreamConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"error","error":{"type":"invalid_request_error","code":"invalid_encrypted_content","message":"The encrypted content could not be verified"}}`),
			[]byte(`{"type":"response.failed","response":{"id":"resp_enc_lineage_1","model":"gpt-5.1","error":{"code":"invalid_encrypted_content","message":"The encrypted content could not be verified"}}}`),
			[]byte(`{"type":"response.completed","response":{"id":"resp_enc_lineage_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	dialer := &openAIWSQueueDialer{
		conns: []openai.WSClientConn{upstreamConn},
	}
	pool := newOpenAIWSConnPool(options)
	pool.SetClientDialerForTest(dialer)

	svc := newWSFixture(wsFixtureInputs{options: options, transport: &auxiliaryHTTPRecorder{}, cache: &testkit.StickyCache{}, corrector: openai.NewCodexToolCorrector(), pool: pool})

	provider := &gatewayadapter.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 119,
			Name:        "openai-ingress-enc-lineage",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Credentials: map[string]any{
				"api_key": "sk-test",
			},
			Extra: map[string]any{
				"responses_websockets_v2_enabled": true,
			},
		},
	}

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			CompressionMode: websocket.CompressionContextTakeover,
		})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			_ = conn.CloseNow()
		}()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != websocket.MessageText && msgType != websocket.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, provider, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := websocket.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeMessage := func(payload string) {
		writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, clientConn.Write(writeCtx, websocket.MessageText, []byte(payload)))
	}
	readMessage := func() []byte {
		readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		msgType, message, readErr := clientConn.Read(readCtx)
		require.NoError(t, readErr)
		require.Equal(t, websocket.MessageText, msgType)
		return message
	}

	// Turn one sends stale ciphertext and receives invalid_encrypted_content.
	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"input":[{"type":"reasoning","id":"rs_1","encrypted_content":"stale-cipher","summary":[]},{"type":"input_text","text":"hi"}]}`)
	firstEvent := readMessage()
	require.Equal(t, "error", gjson.GetBytes(firstEvent, "type").String())
	require.Equal(t, "invalid_encrypted_content", gjson.GetBytes(firstEvent, "error.code").String())
	secondEvent := readMessage()
	require.Equal(t, "response.failed", gjson.GetBytes(secondEvent, "type").String())

	// Turn two repeats the stale ciphertext; lineage tracking should strip it before the upstream write.
	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"input":[{"type":"reasoning","id":"rs_1","encrypted_content":"stale-cipher","summary":[]},{"type":"input_text","text":"hi"},{"type":"input_text","text":"again"}]}`)
	thirdEvent := readMessage()
	require.Equal(t, "response.completed", gjson.GetBytes(thirdEvent, "type").String())
	require.Equal(t, "resp_enc_lineage_2", gjson.GetBytes(thirdEvent, "response.id").String())

	require.NoError(t, clientConn.Close(websocket.StatusNormalClosure, "done"))
	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 结束超时")
	}

	upstreamConn.mu.Lock()
	writes := append([]map[string]any(nil), upstreamConn.writes...)
	upstreamConn.mu.Unlock()
	require.Len(t, writes, 2, "两轮各应发送一次上游请求")

	firstUpstream := requestToJSONString(writes[0])
	require.Equal(t, "stale-cipher", gjson.Get(firstUpstream, "input.0.encrypted_content").String(), "首轮请求原样携带密文")

	secondUpstream := requestToJSONString(writes[1])
	secondInput := gjson.Get(secondUpstream, "input").Array()
	require.Len(t, secondInput, 3, "剥离仅移除 encrypted_content 字段，reasoning 骨架保留")
	for _, item := range secondInput {
		require.False(t, item.Get("encrypted_content").Exists(), "第二轮请求不得再携带已失效密文: %s", item.Raw)
	}
	require.Equal(t, "rs_1", gjson.Get(secondUpstream, "input.0.id").String())
	require.Equal(t, "again", gjson.Get(secondUpstream, "input.2.text").String())
}

func TestIsOpenAIWSClientDisconnectError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "io_eof", err: io.EOF, want: true},
		{name: "net_closed", err: net.ErrClosed, want: true},
		{name: "context_canceled", err: context.Canceled, want: true},
		{name: "ws_normal_closure", err: websocket.CloseError{Code: websocket.StatusNormalClosure}, want: true},
		{name: "ws_going_away", err: websocket.CloseError{Code: websocket.StatusGoingAway}, want: true},
		{name: "ws_no_status", err: websocket.CloseError{Code: websocket.StatusNoStatusRcvd}, want: true},
		{name: "ws_abnormal_1006", err: websocket.CloseError{Code: websocket.StatusAbnormalClosure}, want: true},
		{name: "ws_policy_violation", err: websocket.CloseError{Code: websocket.StatusPolicyViolation}, want: false},
		{name: "wrapped_eof_message", err: errors.New("failed to get reader: failed to read frame header: EOF"), want: true},
		{name: "connection_reset_by_peer", err: errors.New("failed to read frame header: read tcp 127.0.0.1:1234->127.0.0.1:5678: read: connection reset by peer"), want: true},
		{name: "windows_connection_reset", err: errors.New("failed to get reader: failed to read frame header: read tcp 127.0.0.1:1234->127.0.0.1:5678: wsarecv: An existing connection was forcibly closed by the remote host."), want: true},
		{name: "broken_pipe", err: errors.New("write tcp 127.0.0.1:1234->127.0.0.1:5678: write: broken pipe"), want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, gatewayadapter.IsOpenAIWSClientDisconnectError(tt.err))
		})
	}
}

func TestIsOpenAIWSIngressPreviousResponseNotFound(t *testing.T) {
	t.Parallel()

	require.False(t, ws.IsPreviousResponseNotFound(nil))
	require.False(t, ws.IsPreviousResponseNotFound(errors.New("plain error")))
	require.False(t, ws.IsPreviousResponseNotFound(
		ws.WrapIngressTurnError("read_upstream", errors.New("upstream read failed"), false),
	))
	require.False(t, ws.IsPreviousResponseNotFound(
		ws.WrapIngressTurnError(ws.IngressStagePreviousResponseNotFound, errors.New("previous response not found"), true),
	))
	require.True(t, ws.IsPreviousResponseNotFound(
		ws.WrapIngressTurnError(ws.IngressStagePreviousResponseNotFound, errors.New("previous response not found"), false),
	))
}

func TestOpenAIWSIngressPreviousResponseRecoveryEnabled(t *testing.T) {
	t.Parallel()

	var nilService *OpenAIWebSocketExecutor
	require.True(t, nilService.openAIWSIngressPreviousResponseRecoveryEnabled(), "nil service should default to enabled")

	svcWithNilCfg := newWSFixture(wsFixtureInputs{})
	require.True(t, svcWithNilCfg.openAIWSIngressPreviousResponseRecoveryEnabled(), "nil config should default to enabled")

	svc := newWSFixture(wsFixtureInputs{options: &wsFixtureOptions{}})
	require.False(t, svc.openAIWSIngressPreviousResponseRecoveryEnabled(), "explicit config default should be false")

	svc.options.WS.IngressPreviousResponseRecoveryEnabled = true
	require.True(t, svc.openAIWSIngressPreviousResponseRecoveryEnabled())
}

// TestApplyOpenAIWSReasoningEffortPolicyUsesSessionModel checks group mappings for later frames that omit model.
func TestApplyOpenAIWSReasoningEffortPolicyUsesSessionModel(t *testing.T) {
	hooks := &ws.OpenAIIngressHooks{
		ReasoningEffortMappings: []routing.ReasoningEffortMapping{{
			From:      "none",
			To:        "low",
			MatchType: "exact",
			Model:     "gpt-6-astra",
		}},
	}
	payload := []byte(`{"type":"response.create","reasoning":{"effort":"none"}}`)

	updated, err := ws.ApplyReasoningEffortPolicy(payload, hooks, "gpt-6-astra")
	require.NoError(t, err)
	require.Equal(t, "low", gjson.GetBytes(updated, "reasoning.effort").String())
}

func TestDropPreviousResponseIDFromRawPayload(t *testing.T) {
	t.Parallel()

	t.Run("empty_payload", func(t *testing.T) {
		updated, removed, err := openai.DropPreviousResponseIDFromRawPayload(nil)
		require.NoError(t, err)
		require.False(t, removed)
		require.Empty(t, updated)
	})

	t.Run("payload_without_previous_response_id", func(t *testing.T) {
		payload := []byte(`{"type":"response.create","model":"gpt-5.1"}`)
		updated, removed, err := openai.DropPreviousResponseIDFromRawPayload(payload)
		require.NoError(t, err)
		require.False(t, removed)
		require.Equal(t, string(payload), string(updated))
	})

	t.Run("normal_delete_success", func(t *testing.T) {
		payload := []byte(`{"type":"response.create","model":"gpt-5.1","previous_response_id":"resp_abc"}`)
		updated, removed, err := openai.DropPreviousResponseIDFromRawPayload(payload)
		require.NoError(t, err)
		require.True(t, removed)
		require.False(t, gjson.GetBytes(updated, "previous_response_id").Exists())
	})

	t.Run("duplicate_keys_are_removed", func(t *testing.T) {
		payload := []byte(`{"type":"response.create","previous_response_id":"resp_a","input":[],"previous_response_id":"resp_b"}`)
		updated, removed, err := openai.DropPreviousResponseIDFromRawPayload(payload)
		require.NoError(t, err)
		require.True(t, removed)
		require.False(t, gjson.GetBytes(updated, "previous_response_id").Exists())
	})

	t.Run("nil_delete_fn_uses_default_delete_logic", func(t *testing.T) {
		payload := []byte(`{"type":"response.create","model":"gpt-5.1","previous_response_id":"resp_abc"}`)
		updated, removed, err := openai.DropPreviousResponseIDFromRawPayloadWithDeleteFn(payload, nil)
		require.NoError(t, err)
		require.True(t, removed)
		require.False(t, gjson.GetBytes(updated, "previous_response_id").Exists())
	})

	t.Run("delete_error", func(t *testing.T) {
		payload := []byte(`{"type":"response.create","model":"gpt-5.1","previous_response_id":"resp_abc"}`)
		updated, removed, err := openai.DropPreviousResponseIDFromRawPayloadWithDeleteFn(payload, func(_ []byte, _ string) ([]byte, error) {
			return nil, errors.New("delete failed")
		})
		require.Error(t, err)
		require.False(t, removed)
		require.Equal(t, string(payload), string(updated))
	})

	t.Run("malformed_json_is_still_best_effort_deleted", func(t *testing.T) {
		payload := []byte(`{"type":"response.create","previous_response_id":"resp_abc"`)
		require.True(t, gjson.GetBytes(payload, "previous_response_id").Exists())

		updated, removed, err := openai.DropPreviousResponseIDFromRawPayload(payload)
		require.NoError(t, err)
		require.True(t, removed)
		require.False(t, gjson.GetBytes(updated, "previous_response_id").Exists())
	})
}

func TestStripCodexSparkImageGenerationToolFromRawPayload(t *testing.T) {
	t.Run("strips_image_generation_for_spark", func(t *testing.T) {
		payload := []byte(`{"type":"response.create","model":"gpt-5.3-codex-spark","tools":[{"type":"function","name":"shell"},{"type":"image_generation","output_format":"png"}]}`)
		updated, changed, err := stripCodexSparkImageGenerationToolFromRawPayload(payload, "gpt-5.3-codex-spark")
		require.NoError(t, err)
		require.True(t, changed)
		require.False(t, gjson.GetBytes(updated, `tools.#(type=="image_generation")`).Exists())
		require.True(t, gjson.GetBytes(updated, `tools.#(type=="function")`).Exists())
	})

	t.Run("strips_namespace_tools_for_spark", func(t *testing.T) {
		payload := []byte(`{
			"type":"response.create",
			"model":"gpt-5.3-codex-spark",
			"input":[
				{"type":"message","role":"user","content":"hello"},
				{"type":"additional_tools","tools":[{"type":"namespace","name":"image_gen"}]}
			],
			"tool_choice":{"type":"namespace","name":"image_gen"}
		}`)
		updated, changed, err := stripCodexSparkImageGenerationToolFromRawPayload(payload, "gpt-5.3-codex-spark")
		require.NoError(t, err)
		require.True(t, changed)
		require.False(t, gatewayadapter.ImageIntent().IsImageGenerationIntent(media.OpenAIResponsesEndpoint, "gpt-5.3-codex-spark", updated))
		require.Equal(t, "hello", gjson.GetBytes(updated, "input.0.content").String())
		require.False(t, gjson.GetBytes(updated, "tool_choice").Exists())
	})

	t.Run("keeps_image_generation_for_non_spark", func(t *testing.T) {
		payload := []byte(`{"type":"response.create","model":"gpt-5.3-codex","tools":[{"type":"image_generation","output_format":"png"}]}`)
		updated, changed, err := stripCodexSparkImageGenerationToolFromRawPayload(payload, "gpt-5.3-codex")
		require.NoError(t, err)
		require.False(t, changed)
		require.Equal(t, string(payload), string(updated))
	})

	t.Run("noop_when_no_image_tool", func(t *testing.T) {
		payload := []byte(`{"type":"response.create","model":"gpt-5.3-codex-spark","tools":[{"type":"function","name":"shell"}]}`)
		updated, changed, err := stripCodexSparkImageGenerationToolFromRawPayload(payload, "gpt-5.3-codex-spark")
		require.NoError(t, err)
		require.False(t, changed)
		require.Equal(t, string(payload), string(updated))
	})
}

func TestStripOpenAIImageGenerationToolsFromRawPayload(t *testing.T) {
	t.Run("flat image tool", func(t *testing.T) {
		payload := []byte(`{
			"type":"response.create",
			"model":"gpt-5.4",
			"tools":[
				{"type":"function","name":"shell"},
				{"type":"image_generation","output_format":"png"}
			],
			"tool_choice":{"type":"image_generation"}
		}`)

		updated, changed, err := gatewayadapter.StripOpenAIImageGenerationToolsFromRawPayload(payload)

		require.NoError(t, err)
		require.True(t, changed)
		require.False(t, gjson.GetBytes(updated, `tools.#(type=="image_generation")`).Exists())
		require.True(t, gjson.GetBytes(updated, `tools.#(type=="function")`).Exists())
		require.False(t, gjson.GetBytes(updated, "tool_choice").Exists())
	})

	t.Run("namespace and Responses Lite tools", func(t *testing.T) {
		payload := []byte(`{
			"type":"response.create",
			"model":"gpt-5.5",
			"tools":[
				{"type":"namespace","name":"image_gen","tools":[{"type":"function","name":"imagegen"}]},
				{"type":"namespace","name":"code_tools","tools":[{"type":"function","name":"run"}]}
			],
			"input":[
				{"type":"message","role":"user","content":"hello"},
				{"type":"additional_tools","tools":[{"type":"namespace","name":"image_gen"}]}
			],
			"tool_choice":{"type":"namespace","name":"image_gen"}
		}`)

		updated, changed, err := gatewayadapter.StripOpenAIImageGenerationToolsFromRawPayload(payload)

		require.NoError(t, err)
		require.True(t, changed)
		require.False(t, gatewayadapter.ImageIntent().IsImageGenerationIntent(media.OpenAIResponsesEndpoint, "gpt-5.5", updated))
		require.True(t, gjson.GetBytes(updated, `tools.#(name=="code_tools")`).Exists())
		require.Equal(t, "hello", gjson.GetBytes(updated, "input.0.content").String())
		require.False(t, gjson.GetBytes(updated, "tool_choice").Exists())
	})

	t.Run("non-image namespace is unchanged", func(t *testing.T) {
		payload := []byte(`{"type":"response.create","model":"gpt-5.5","tools":[{"type":"namespace","name":"code_tools"}]}`)

		updated, changed, err := gatewayadapter.StripOpenAIImageGenerationToolsFromRawPayload(payload)

		require.NoError(t, err)
		require.False(t, changed)
		require.Equal(t, payload, updated)
	})
}

func TestAlignStoreDisabledPreviousResponseID(t *testing.T) {
	t.Parallel()

	t.Run("empty_payload", func(t *testing.T) {
		updated, changed, err := openai.AlignStoreDisabledPreviousResponseID(nil, "resp_target")
		require.NoError(t, err)
		require.False(t, changed)
		require.Empty(t, updated)
	})

	t.Run("empty_expected", func(t *testing.T) {
		payload := []byte(`{"type":"response.create","previous_response_id":"resp_old"}`)
		updated, changed, err := openai.AlignStoreDisabledPreviousResponseID(payload, "")
		require.NoError(t, err)
		require.False(t, changed)
		require.Equal(t, string(payload), string(updated))
	})

	t.Run("missing_previous_response_id", func(t *testing.T) {
		payload := []byte(`{"type":"response.create","model":"gpt-5.1"}`)
		updated, changed, err := openai.AlignStoreDisabledPreviousResponseID(payload, "resp_target")
		require.NoError(t, err)
		require.False(t, changed)
		require.Equal(t, string(payload), string(updated))
	})

	t.Run("already_aligned", func(t *testing.T) {
		payload := []byte(`{"type":"response.create","previous_response_id":"resp_target"}`)
		updated, changed, err := openai.AlignStoreDisabledPreviousResponseID(payload, "resp_target")
		require.NoError(t, err)
		require.False(t, changed)
		require.Equal(t, "resp_target", gjson.GetBytes(updated, "previous_response_id").String())
	})

	t.Run("mismatch_rewrites_to_expected", func(t *testing.T) {
		payload := []byte(`{"type":"response.create","previous_response_id":"resp_old","input":[]}`)
		updated, changed, err := openai.AlignStoreDisabledPreviousResponseID(payload, "resp_target")
		require.NoError(t, err)
		require.True(t, changed)
		require.Equal(t, "resp_target", gjson.GetBytes(updated, "previous_response_id").String())
	})

	t.Run("duplicate_keys_rewrites_to_single_expected", func(t *testing.T) {
		payload := []byte(`{"type":"response.create","previous_response_id":"resp_old_1","input":[],"previous_response_id":"resp_old_2"}`)
		updated, changed, err := openai.AlignStoreDisabledPreviousResponseID(payload, "resp_target")
		require.NoError(t, err)
		require.True(t, changed)
		require.Equal(t, "resp_target", gjson.GetBytes(updated, "previous_response_id").String())
	})
}

func TestSetPreviousResponseIDToRawPayload(t *testing.T) {
	t.Parallel()

	t.Run("empty_payload", func(t *testing.T) {
		updated, err := openai.SetPreviousResponseIDToRawPayload(nil, "resp_target")
		require.NoError(t, err)
		require.Empty(t, updated)
	})

	t.Run("empty_previous_response_id", func(t *testing.T) {
		payload := []byte(`{"type":"response.create","model":"gpt-5.1"}`)
		updated, err := openai.SetPreviousResponseIDToRawPayload(payload, "")
		require.NoError(t, err)
		require.Equal(t, string(payload), string(updated))
	})

	t.Run("set_previous_response_id_when_missing", func(t *testing.T) {
		payload := []byte(`{"type":"response.create","model":"gpt-5.1"}`)
		updated, err := openai.SetPreviousResponseIDToRawPayload(payload, "resp_target")
		require.NoError(t, err)
		require.Equal(t, "resp_target", gjson.GetBytes(updated, "previous_response_id").String())
		require.Equal(t, "gpt-5.1", gjson.GetBytes(updated, "model").String())
	})

	t.Run("overwrite_existing_previous_response_id", func(t *testing.T) {
		payload := []byte(`{"type":"response.create","model":"gpt-5.1","previous_response_id":"resp_old"}`)
		updated, err := openai.SetPreviousResponseIDToRawPayload(payload, "resp_new")
		require.NoError(t, err)
		require.Equal(t, "resp_new", gjson.GetBytes(updated, "previous_response_id").String())
	})
}

func TestShouldInferIngressFunctionCallOutputPreviousResponseID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                    string
		storeDisabled           bool
		turn                    int
		signals                 protocolopenai.ToolContinuationSignals
		currentPreviousResponse string
		expectedPrevious        string
		want                    bool
	}{
		{
			name:             "infer_when_all_conditions_match",
			storeDisabled:    true,
			turn:             2,
			signals:          protocolopenai.ToolContinuationSignals{HasFunctionCallOutput: true},
			expectedPrevious: "resp_1",
			want:             true,
		},
		{
			name:             "skip_when_store_enabled",
			storeDisabled:    false,
			turn:             2,
			signals:          protocolopenai.ToolContinuationSignals{HasFunctionCallOutput: true},
			expectedPrevious: "resp_1",
			want:             false,
		},
		{
			name:             "skip_on_first_turn",
			storeDisabled:    true,
			turn:             1,
			signals:          protocolopenai.ToolContinuationSignals{HasFunctionCallOutput: true},
			expectedPrevious: "resp_1",
			want:             false,
		},
		{
			name:             "skip_without_function_call_output",
			storeDisabled:    true,
			turn:             2,
			signals:          protocolopenai.ToolContinuationSignals{},
			expectedPrevious: "resp_1",
			want:             false,
		},
		{
			name:                    "skip_when_request_already_has_previous_response_id",
			storeDisabled:           true,
			turn:                    2,
			signals:                 protocolopenai.ToolContinuationSignals{HasFunctionCallOutput: true},
			currentPreviousResponse: "resp_client",
			expectedPrevious:        "resp_1",
			want:                    false,
		},
		{
			name:             "skip_when_last_turn_response_id_missing",
			storeDisabled:    true,
			turn:             2,
			signals:          protocolopenai.ToolContinuationSignals{HasFunctionCallOutput: true},
			expectedPrevious: "",
			want:             false,
		},
		{
			name:             "trim_whitespace_before_judgement",
			storeDisabled:    true,
			turn:             2,
			signals:          protocolopenai.ToolContinuationSignals{HasFunctionCallOutput: true},
			expectedPrevious: "   resp_2   ",
			want:             true,
		},
		{
			name:             "skip_when_tool_call_context_already_present",
			storeDisabled:    true,
			turn:             2,
			signals:          protocolopenai.ToolContinuationSignals{HasFunctionCallOutput: true, HasToolCallContext: true},
			expectedPrevious: "resp_2",
			want:             false,
		},
		{
			name:             "infer_when_only_item_reference_covers_call_ids",
			storeDisabled:    true,
			turn:             2,
			signals:          protocolopenai.ToolContinuationSignals{HasFunctionCallOutput: true, HasItemReferenceForAllCallIDs: true},
			expectedPrevious: "resp_2",
			want:             true,
		},
		{
			name:             "skip_when_function_call_output_missing_call_id",
			storeDisabled:    true,
			turn:             2,
			signals:          protocolopenai.ToolContinuationSignals{HasFunctionCallOutput: true, HasFunctionCallOutputMissingCallID: true},
			expectedPrevious: "resp_2",
			want:             false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := openai.ShouldInferIngressFunctionCallOutputPreviousResponseID(
				tt.storeDisabled,
				tt.turn,
				tt.signals,
				tt.currentPreviousResponse,
				tt.expectedPrevious,
			)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestOpenAIWSInputIsPrefixExtended(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		previous  []byte
		current   []byte
		want      bool
		expectErr bool
	}{
		{
			name:     "both_missing_input",
			previous: []byte(`{"type":"response.create","model":"gpt-5.1"}`),
			current:  []byte(`{"type":"response.create","model":"gpt-5.1","previous_response_id":"resp_1"}`),
			want:     true,
		},
		{
			name:     "previous_missing_current_empty_array",
			previous: []byte(`{"type":"response.create","model":"gpt-5.1"}`),
			current:  []byte(`{"type":"response.create","model":"gpt-5.1","input":[]}`),
			want:     true,
		},
		{
			name:     "previous_missing_current_non_empty_array",
			previous: []byte(`{"type":"response.create","model":"gpt-5.1"}`),
			current:  []byte(`{"type":"response.create","model":"gpt-5.1","input":[{"type":"input_text","text":"hello"}]}`),
			want:     false,
		},
		{
			name:     "array_prefix_match",
			previous: []byte(`{"input":[{"type":"input_text","text":"hello"}]}`),
			current:  []byte(`{"input":[{"text":"hello","type":"input_text"},{"type":"input_text","text":"world"}]}`),
			want:     true,
		},
		{
			name:     "array_prefix_mismatch",
			previous: []byte(`{"input":[{"type":"input_text","text":"hello"}]}`),
			current:  []byte(`{"input":[{"type":"input_text","text":"different"}]}`),
			want:     false,
		},
		{
			name:     "current_shorter_than_previous",
			previous: []byte(`{"input":[{"type":"input_text","text":"a"},{"type":"input_text","text":"b"}]}`),
			current:  []byte(`{"input":[{"type":"input_text","text":"a"}]}`),
			want:     false,
		},
		{
			name:     "previous_has_input_current_missing",
			previous: []byte(`{"input":[{"type":"input_text","text":"a"}]}`),
			current:  []byte(`{"model":"gpt-5.1"}`),
			want:     false,
		},
		{
			name:     "input_string_treated_as_single_item",
			previous: []byte(`{"input":"hello"}`),
			current:  []byte(`{"input":"hello"}`),
			want:     true,
		},
		{
			name:      "current_invalid_input_json",
			previous:  []byte(`{"input":[]}`),
			current:   []byte(`{"input":[}`),
			expectErr: true,
		},
		{
			name:      "invalid_input_json",
			previous:  []byte(`{"input":[}`),
			current:   []byte(`{"input":[]}`),
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := openai.OpenAIWSInputIsPrefixExtended(tt.previous, tt.current)
			if tt.expectErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestNormalizeOpenAIWSJSONForCompare(t *testing.T) {
	t.Parallel()

	normalized, err := openai.NormalizeOpenAIWSJSONForCompare([]byte(`{"b":2,"a":1}`))
	require.NoError(t, err)
	require.Equal(t, `{"a":1,"b":2}`, string(normalized))

	_, err = openai.NormalizeOpenAIWSJSONForCompare([]byte("   "))
	require.Error(t, err)

	_, err = openai.NormalizeOpenAIWSJSONForCompare([]byte(`{"a":`))
	require.Error(t, err)
}

func TestNormalizeOpenAIWSJSONForCompareOrRaw(t *testing.T) {
	t.Parallel()

	require.Equal(t, `{"a":1,"b":2}`, string(openai.NormalizeOpenAIWSJSONForCompareOrRaw([]byte(`{"b":2,"a":1}`))))
	require.Equal(t, `{"a":`, string(openai.NormalizeOpenAIWSJSONForCompareOrRaw([]byte(`{"a":`))))
}

func TestNormalizeOpenAIWSPayloadWithoutInputAndPreviousResponseID(t *testing.T) {
	t.Parallel()

	normalized, err := openai.NormalizeOpenAIWSPayloadWithoutInputAndPreviousResponseID(
		[]byte(`{"model":"gpt-5.1","input":[1],"previous_response_id":"resp_x","client_metadata":{"request_start_ms":"1"},"stream_options":{"include_usage":true},"generate":false,"metadata":{"b":2,"a":1}}`),
	)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(normalized, "input").Exists())
	require.False(t, gjson.GetBytes(normalized, "previous_response_id").Exists())
	require.False(t, gjson.GetBytes(normalized, "client_metadata").Exists())
	require.False(t, gjson.GetBytes(normalized, "stream_options").Exists())
	require.False(t, gjson.GetBytes(normalized, "generate").Exists())
	require.Equal(t, float64(1), gjson.GetBytes(normalized, "metadata.a").Float())

	normalized, err = openai.NormalizeOpenAIWSPayloadWithoutInputAndPreviousResponseID(
		[]byte(`{"model":"gpt-5.1","generate":true}`),
	)
	require.NoError(t, err)
	require.True(t, gjson.GetBytes(normalized, "generate").Bool())

	_, err = openai.NormalizeOpenAIWSPayloadWithoutInputAndPreviousResponseID(nil)
	require.Error(t, err)

	_, err = openai.NormalizeOpenAIWSPayloadWithoutInputAndPreviousResponseID([]byte(`[]`))
	require.Error(t, err)
}

func TestOpenAIWSExtractNormalizedInputSequence(t *testing.T) {
	t.Parallel()

	t.Run("empty_payload", func(t *testing.T) {
		items, exists, err := openai.OpenAIWSExtractNormalizedInputSequence(nil)
		require.NoError(t, err)
		require.False(t, exists)
		require.Nil(t, items)
	})

	t.Run("input_missing", func(t *testing.T) {
		items, exists, err := openai.OpenAIWSExtractNormalizedInputSequence([]byte(`{"type":"response.create"}`))
		require.NoError(t, err)
		require.False(t, exists)
		require.Nil(t, items)
	})

	t.Run("input_array", func(t *testing.T) {
		items, exists, err := openai.OpenAIWSExtractNormalizedInputSequence([]byte(`{"input":[{"type":"input_text","text":"hello"}]}`))
		require.NoError(t, err)
		require.True(t, exists)
		require.Len(t, items, 1)
	})

	t.Run("input_object", func(t *testing.T) {
		items, exists, err := openai.OpenAIWSExtractNormalizedInputSequence([]byte(`{"input":{"type":"input_text","text":"hello"}}`))
		require.NoError(t, err)
		require.True(t, exists)
		require.Len(t, items, 1)
	})

	t.Run("input_string", func(t *testing.T) {
		items, exists, err := openai.OpenAIWSExtractNormalizedInputSequence([]byte(`{"input":"hello"}`))
		require.NoError(t, err)
		require.True(t, exists)
		require.Len(t, items, 1)
		require.Equal(t, `"hello"`, string(items[0]))
	})

	t.Run("input_number", func(t *testing.T) {
		items, exists, err := openai.OpenAIWSExtractNormalizedInputSequence([]byte(`{"input":42}`))
		require.NoError(t, err)
		require.True(t, exists)
		require.Len(t, items, 1)
		require.Equal(t, "42", string(items[0]))
	})

	t.Run("input_bool", func(t *testing.T) {
		items, exists, err := openai.OpenAIWSExtractNormalizedInputSequence([]byte(`{"input":true}`))
		require.NoError(t, err)
		require.True(t, exists)
		require.Len(t, items, 1)
		require.Equal(t, "true", string(items[0]))
	})

	t.Run("input_null", func(t *testing.T) {
		items, exists, err := openai.OpenAIWSExtractNormalizedInputSequence([]byte(`{"input":null}`))
		require.NoError(t, err)
		require.True(t, exists)
		require.Len(t, items, 1)
		require.Equal(t, "null", string(items[0]))
	})

	t.Run("input_invalid_array_json", func(t *testing.T) {
		items, exists, err := openai.OpenAIWSExtractNormalizedInputSequence([]byte(`{"input":[}`))
		require.Error(t, err)
		require.True(t, exists)
		require.Nil(t, items)
	})
}

func TestShouldKeepIngressPreviousResponseID(t *testing.T) {
	t.Parallel()

	previousPayload := []byte(`{
		"type":"response.create",
		"model":"gpt-5.1",
		"store":false,
		"tools":[{"type":"function","name":"tool_a"}],
		"input":[{"type":"input_text","text":"hello"}]
	}`)
	currentStrictPayload := []byte(`{
		"type":"response.create",
		"model":"gpt-5.1",
		"store":false,
		"tools":[{"name":"tool_a","type":"function"}],
		"previous_response_id":"resp_turn_1",
		"input":[{"text":"hello","type":"input_text"},{"type":"input_text","text":"world"}]
	}`)

	t.Run("strict_incremental_keep", func(t *testing.T) {
		keep, reason, err := openai.ShouldKeepIngressPreviousResponseID(previousPayload, currentStrictPayload, "resp_turn_1", false)
		require.NoError(t, err)
		require.True(t, keep)
		require.Equal(t, "strict_incremental_ok", reason)
	})

	t.Run("codex_prewarm_to_business_keep", func(t *testing.T) {
		prewarmPayload := []byte(`{
			"type":"response.create",
			"model":"gpt-5.1",
			"store":false,
			"generate":false,
			"client_metadata":{"x-codex-ws-stream-request-start-ms":"100"},
			"stream_options":{"include_usage":true},
			"input":[{"type":"input_text","text":"hello"}]
		}`)
		businessPayload := []byte(`{
			"type":"response.create",
			"model":"gpt-5.1",
			"store":false,
			"client_metadata":{"x-codex-ws-stream-request-start-ms":"200"},
			"previous_response_id":"resp_prewarm",
			"input":[{"type":"input_text","text":"hello"}]
		}`)

		keep, reason, err := openai.ShouldKeepIngressPreviousResponseID(
			prewarmPayload,
			businessPayload,
			"resp_prewarm",
			false,
		)
		require.NoError(t, err)
		require.True(t, keep)
		require.Equal(t, "strict_incremental_ok", reason)
	})

	t.Run("missing_previous_response_id", func(t *testing.T) {
		payload := []byte(`{"type":"response.create","model":"gpt-5.1","input":[]}`)
		keep, reason, err := openai.ShouldKeepIngressPreviousResponseID(previousPayload, payload, "resp_turn_1", false)
		require.NoError(t, err)
		require.False(t, keep)
		require.Equal(t, "missing_previous_response_id", reason)
	})

	t.Run("missing_last_turn_response_id", func(t *testing.T) {
		keep, reason, err := openai.ShouldKeepIngressPreviousResponseID(previousPayload, currentStrictPayload, "", false)
		require.NoError(t, err)
		require.False(t, keep)
		require.Equal(t, "missing_last_turn_response_id", reason)
	})

	t.Run("previous_response_id_mismatch", func(t *testing.T) {
		keep, reason, err := openai.ShouldKeepIngressPreviousResponseID(previousPayload, currentStrictPayload, "resp_turn_other", false)
		require.NoError(t, err)
		require.False(t, keep)
		require.Equal(t, "previous_response_id_mismatch", reason)
	})

	t.Run("missing_previous_turn_payload", func(t *testing.T) {
		keep, reason, err := openai.ShouldKeepIngressPreviousResponseID(nil, currentStrictPayload, "resp_turn_1", false)
		require.NoError(t, err)
		require.False(t, keep)
		require.Equal(t, "missing_previous_turn_payload", reason)
	})

	t.Run("non_input_changed", func(t *testing.T) {
		payload := []byte(`{
			"type":"response.create",
			"model":"gpt-5.1-mini",
			"store":false,
			"tools":[{"type":"function","name":"tool_a"}],
			"previous_response_id":"resp_turn_1",
			"input":[{"type":"input_text","text":"hello"},{"type":"input_text","text":"world"}]
		}`)
		keep, reason, err := openai.ShouldKeepIngressPreviousResponseID(previousPayload, payload, "resp_turn_1", false)
		require.NoError(t, err)
		require.False(t, keep)
		require.Equal(t, "non_input_changed", reason)
	})

	t.Run("delta_input_keeps_previous_response_id", func(t *testing.T) {
		payload := []byte(`{
			"type":"response.create",
			"model":"gpt-5.1",
			"store":false,
			"tools":[{"type":"function","name":"tool_a"}],
			"previous_response_id":"resp_turn_1",
			"input":[{"type":"input_text","text":"different"}]
		}`)
		keep, reason, err := openai.ShouldKeepIngressPreviousResponseID(previousPayload, payload, "resp_turn_1", false)
		require.NoError(t, err)
		require.True(t, keep)
		require.Equal(t, "strict_incremental_ok", reason)
	})

	t.Run("function_call_output_keeps_previous_response_id", func(t *testing.T) {
		payload := []byte(`{
			"type":"response.create",
			"model":"gpt-5.1",
			"store":false,
			"previous_response_id":"resp_external",
			"input":[{"type":"function_call_output","call_id":"call_1","output":"ok"}]
		}`)
		keep, reason, err := openai.ShouldKeepIngressPreviousResponseID(previousPayload, payload, "resp_turn_1", true)
		require.NoError(t, err)
		require.True(t, keep)
		require.Equal(t, "has_function_call_output", reason)
	})

	t.Run("non_input_compare_error", func(t *testing.T) {
		keep, reason, err := openai.ShouldKeepIngressPreviousResponseID([]byte(`[]`), currentStrictPayload, "resp_turn_1", false)
		require.Error(t, err)
		require.False(t, keep)
		require.Equal(t, "non_input_compare_error", reason)
	})

	t.Run("current_payload_compare_error", func(t *testing.T) {
		keep, reason, err := openai.ShouldKeepIngressPreviousResponseID(previousPayload, []byte(`{"previous_response_id":"resp_turn_1","input":[}`), "resp_turn_1", false)
		require.Error(t, err)
		require.False(t, keep)
		require.Equal(t, "non_input_compare_error", reason)
	})
}

func TestBuildOpenAIWSReplayInputSequence(t *testing.T) {
	t.Parallel()

	lastFull := []json.RawMessage{
		json.RawMessage(`{"type":"input_text","text":"hello"}`),
	}

	t.Run("no_previous_response_id_use_current", func(t *testing.T) {
		items, exists, err := openai.BuildOpenAIWSReplayInputSequence(
			lastFull,
			true,
			[]byte(`{"input":[{"type":"input_text","text":"new"}]}`),
			false,
		)
		require.NoError(t, err)
		require.True(t, exists)
		require.Len(t, items, 1)
		require.Equal(t, "new", gjson.GetBytes(items[0], "text").String())
	})

	t.Run("no_previous_response_id_custom_tool_history_does_not_accumulate", func(t *testing.T) {
		previousFull := []json.RawMessage{
			json.RawMessage(`{"type":"input_text","text":"stale"}`),
			json.RawMessage(`{"type":"custom_tool_call","id":"stale_item","call_id":"stale_call","name":"exec","input":"stale"}`),
		}
		currentPayload := []byte(`{"input":[
			{"type":"custom_tool_call","id":"item_1","call_id":"call_1","name":"exec","input":"pwd"},
			{"type":"custom_tool_call_output","call_id":"call_1","output":"/tmp"},
			{"type":"input_text","text":"continue"}
		]}`)

		for range 3 {
			items, exists, err := openai.BuildOpenAIWSReplayInputSequence(
				previousFull,
				true,
				currentPayload,
				false,
			)
			require.NoError(t, err)
			require.True(t, exists)
			require.Len(t, items, 3)
			require.Equal(t, "custom_tool_call", gjson.GetBytes(items[0], "type").String())
			require.Equal(t, "call_1", gjson.GetBytes(items[0], "call_id").String())
			require.Equal(t, "custom_tool_call_output", gjson.GetBytes(items[1], "type").String())
			require.Equal(t, "call_1", gjson.GetBytes(items[1], "call_id").String())
			previousFull = append(items, json.RawMessage(`{"type":"custom_tool_call","id":"replayed_item","call_id":"replayed_call","name":"exec","input":"ignored"}`))
		}
	})

	t.Run("previous_response_id_delta_append", func(t *testing.T) {
		items, exists, err := openai.BuildOpenAIWSReplayInputSequence(
			lastFull,
			true,
			[]byte(`{"previous_response_id":"resp_1","input":[{"type":"input_text","text":"world"}]}`),
			true,
		)
		require.NoError(t, err)
		require.True(t, exists)
		require.Len(t, items, 2)
		require.Equal(t, "hello", gjson.GetBytes(items[0], "text").String())
		require.Equal(t, "world", gjson.GetBytes(items[1], "text").String())
	})

	t.Run("previous_response_id_filters_orphan_historical_custom_tool_call", func(t *testing.T) {
		previousFull := []json.RawMessage{
			json.RawMessage(`{"type":"input_text","text":"hello"}`),
			json.RawMessage(`{"type":"custom_tool_call","id":"item_orphan","call_id":"call_orphan","name":"exec","input":"pwd"}`),
		}
		items, exists, err := openai.BuildOpenAIWSReplayInputSequence(
			previousFull,
			true,
			[]byte(`{"previous_response_id":"resp_1","input":[{"role":"user","content":"continue"}]}`),
			true,
		)
		require.NoError(t, err)
		require.True(t, exists)
		require.Len(t, items, 2)
		require.Equal(t, "hello", gjson.GetBytes(items[0], "text").String())
		require.Equal(t, "user", gjson.GetBytes(items[1], "role").String())
	})

	t.Run("previous_response_id_preserves_paired_historical_function_call", func(t *testing.T) {
		previousFull := []json.RawMessage{
			json.RawMessage(`{"type":"function_call","id":"item_1","call_id":"call_1","name":"lookup","arguments":"{}"}`),
			json.RawMessage(`{"type":"function_call_output","call_id":"call_1","output":"ok"}`),
		}
		items, exists, err := openai.BuildOpenAIWSReplayInputSequence(
			previousFull,
			true,
			[]byte(`{"previous_response_id":"resp_1","input":[{"role":"user","content":"continue"}]}`),
			true,
		)
		require.NoError(t, err)
		require.True(t, exists)
		require.Len(t, items, 3)
		require.Equal(t, "function_call", gjson.GetBytes(items[0], "type").String())
		require.Equal(t, "function_call_output", gjson.GetBytes(items[1], "type").String())
	})

	t.Run("previous_response_id_preserves_paired_historical_custom_tool_call", func(t *testing.T) {
		previousFull := []json.RawMessage{
			json.RawMessage(`{"type":"custom_tool_call","id":"item_1","call_id":"call_1","name":"exec","input":"pwd"}`),
			json.RawMessage(`{"type":"custom_tool_call_output","call_id":"call_1","output":"/tmp"}`),
		}
		items, exists, err := openai.BuildOpenAIWSReplayInputSequence(
			previousFull,
			true,
			[]byte(`{"previous_response_id":"resp_1","input":[{"role":"user","content":"continue"}]}`),
			true,
		)
		require.NoError(t, err)
		require.True(t, exists)
		require.Len(t, items, 3)
		require.Equal(t, "custom_tool_call", gjson.GetBytes(items[0], "type").String())
		require.Equal(t, "custom_tool_call_output", gjson.GetBytes(items[1], "type").String())
	})

	t.Run("item_reference_does_not_complete_historical_call", func(t *testing.T) {
		previousFull := []json.RawMessage{
			json.RawMessage(`{"type":"custom_tool_call","id":"item_1","call_id":"call_1","name":"exec","input":"pwd"}`),
		}
		items, exists, err := openai.BuildOpenAIWSReplayInputSequence(
			previousFull,
			true,
			[]byte(`{"previous_response_id":"resp_1","input":[{"type":"item_reference","id":"call_1"},{"role":"user","content":"continue"}]}`),
			true,
		)
		require.NoError(t, err)
		require.True(t, exists)
		require.Len(t, items, 2)
		require.Equal(t, "item_reference", gjson.GetBytes(items[0], "type").String())
		require.Equal(t, "user", gjson.GetBytes(items[1], "role").String())
	})

	t.Run("previous_response_id_preserves_current_orphan_custom_tool_call", func(t *testing.T) {
		items, exists, err := openai.BuildOpenAIWSReplayInputSequence(
			lastFull,
			true,
			[]byte(`{"previous_response_id":"resp_1","input":[{"type":"custom_tool_call","id":"item_live","call_id":"call_live","name":"exec","input":"pwd"}]}`),
			true,
		)
		require.NoError(t, err)
		require.True(t, exists)
		require.Len(t, items, 2)
		require.Equal(t, "custom_tool_call", gjson.GetBytes(items[1], "type").String())
		require.Equal(t, "call_live", gjson.GetBytes(items[1], "call_id").String())
	})

	t.Run("previous_response_id_full_input_replace", func(t *testing.T) {
		items, exists, err := openai.BuildOpenAIWSReplayInputSequence(
			lastFull,
			true,
			[]byte(`{"previous_response_id":"resp_1","input":[{"type":"input_text","text":"hello"},{"type":"input_text","text":"world"}]}`),
			true,
		)
		require.NoError(t, err)
		require.True(t, exists)
		require.Len(t, items, 2)
		require.Equal(t, "hello", gjson.GetBytes(items[0], "text").String())
		require.Equal(t, "world", gjson.GetBytes(items[1], "text").String())
	})
}

func TestOpenAIWSRawPayloadHasToolCallOutput(t *testing.T) {
	t.Parallel()

	for _, typ := range []string{
		"function_call_output",
		"tool_search_output",
		"custom_tool_call_output",
		"mcp_tool_call_output",
	} {
		t.Run(typ, func(t *testing.T) {
			t.Parallel()
			payload := []byte(`{"input":[{"type":"` + typ + `","call_id":"call_1","output":"ok"}]}`)
			require.True(t, openai.OpenAIWSRawPayloadHasToolCallOutput(payload))
		})
	}

	t.Run("object_input", func(t *testing.T) {
		t.Parallel()
		payload := []byte(`{"input":{"type":"tool_search_output","call_id":"call_1","output":"ok"}}`)
		require.True(t, openai.OpenAIWSRawPayloadHasToolCallOutput(payload))
	})

	t.Run("non_tool_output", func(t *testing.T) {
		t.Parallel()
		payload := []byte(`{"input":[{"type":"input_text","text":"hello"}]}`)
		require.False(t, openai.OpenAIWSRawPayloadHasToolCallOutput(payload))
	})
}

func TestSetOpenAIWSPayloadInputSequence(t *testing.T) {
	t.Parallel()

	t.Run("set_items", func(t *testing.T) {
		original := []byte(`{"type":"response.create","previous_response_id":"resp_1"}`)
		items := []json.RawMessage{
			json.RawMessage(`{"type":"input_text","text":"hello"}`),
			json.RawMessage(`{"type":"input_text","text":"world"}`),
		}
		updated, err := openai.SetOpenAIWSPayloadInputSequence(original, items, true)
		require.NoError(t, err)
		require.Equal(t, "hello", gjson.GetBytes(updated, "input.0.text").String())
		require.Equal(t, "world", gjson.GetBytes(updated, "input.1.text").String())
	})

	t.Run("preserve_empty_array_not_null", func(t *testing.T) {
		original := []byte(`{"type":"response.create","previous_response_id":"resp_1"}`)
		updated, err := openai.SetOpenAIWSPayloadInputSequence(original, nil, true)
		require.NoError(t, err)
		require.True(t, gjson.GetBytes(updated, "input").IsArray())
		require.Len(t, gjson.GetBytes(updated, "input").Array(), 0)
		require.False(t, gjson.GetBytes(updated, "input").Type == gjson.Null)
	})
}

func TestCombineOpenAIWSReplayItems(t *testing.T) {
	t.Parallel()

	t.Run("empty_delta_returns_history", func(t *testing.T) {
		history := []json.RawMessage{json.RawMessage(`{"a":1}`)}
		require.Nil(t, openai.CombineOpenAIWSReplayItems(nil, nil))
		combined := openai.CombineOpenAIWSReplayItems(history, nil)
		require.Len(t, combined, 1)
	})

	t.Run("new_header_shares_bodies", func(t *testing.T) {
		history := []json.RawMessage{json.RawMessage(`{"a":1}`)}
		delta := []json.RawMessage{json.RawMessage(`{"b":2}`)}
		combined := openai.CombineOpenAIWSReplayItems(history, delta)
		require.Len(t, combined, 2)
		// The combined slice owns its backing array so appending leaves history unchanged.
		require.NotSame(t, &history[0], &combined[0])
		// Item bodies share their existing byte slices.
		require.Same(t, &history[0][0], &combined[0][0])
		require.Same(t, &delta[0][0], &combined[1][0])
	})
}

func TestOpenAIWSReplaySequenceSharesBodies(t *testing.T) {
	t.Parallel()

	t.Run("extract_shares_payload_backing_array", func(t *testing.T) {
		payload := []byte(`{"input":[{"type":"input_text","text":"hello"},{"type":"input_text","text":"world"}]}`)
		items, exists, err := openai.OpenAIWSExtractNormalizedInputSequence(payload)
		require.NoError(t, err)
		require.True(t, exists)
		require.Len(t, items, 2)
		for _, item := range items {
			start := bytes.Index(payload, []byte(item))
			require.GreaterOrEqual(t, start, 0)
			require.Same(t, &payload[start], &item[0], "extract 应零拷贝共享 payload 底层数组")
		}
	})

	t.Run("build_transfers_current_items_ownership", func(t *testing.T) {
		payload := []byte(`{"input":[{"type":"input_text","text":"hello"}]}`)
		items, exists, err := openai.BuildOpenAIWSReplayInputSequence(nil, false, payload, false)
		require.NoError(t, err)
		require.True(t, exists)
		require.Len(t, items, 1)
		start := bytes.Index(payload, []byte(items[0]))
		require.GreaterOrEqual(t, start, 0)
		require.Same(t, &payload[start], &items[0][0])
	})

	t.Run("build_merge_shares_history_bodies", func(t *testing.T) {
		history := []json.RawMessage{json.RawMessage(`{"type":"input_text","text":"hello"}`)}
		items, exists, err := openai.BuildOpenAIWSReplayInputSequence(
			history,
			true,
			[]byte(`{"previous_response_id":"resp_1","input":[{"type":"input_text","text":"world"}]}`),
			true,
		)
		require.NoError(t, err)
		require.True(t, exists)
		require.Len(t, items, 2)
		require.Same(t, &history[0][0], &items[0][0], "历史正文应共享而非深拷贝")
	})

	t.Run("build_prefix_hit_transfers_current_items", func(t *testing.T) {
		history := []json.RawMessage{json.RawMessage(`{"type":"input_text","text":"hello"}`)}
		payload := []byte(`{"previous_response_id":"resp_1","input":[{"type":"input_text","text":"hello"},{"type":"input_text","text":"world"}]}`)
		items, exists, err := openai.BuildOpenAIWSReplayInputSequence(history, true, payload, true)
		require.NoError(t, err)
		require.True(t, exists)
		require.Len(t, items, 2)
		start := bytes.Index(payload, []byte(items[1]))
		require.GreaterOrEqual(t, start, 0)
		require.Same(t, &payload[start], &items[1][0], "prefix 命中应转移当前 items 所有权并共享 payload 底层数组")
	})
}

func requestToJSONString(payload map[string]any) string {
	if len(payload) == 0 {
		return "{}"
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func (r *openAIWSIngressCapacityShedRepo) SetError(context.Context, int64, string) error { return nil }

func (r *openAIWSIngressCapacityShedRepo) SetRateLimited(context.Context, int64, time.Time) error {
	return nil
}

func (r *openAIWSIngressCapacityShedRepo) UpdateExtra(context.Context, int64, map[string]any) error {
	return nil
}

// TestProxyResponsesWebSocketFromClient_RewritesCapacityShedCodeForClient checks capacity-code rewriting to server_error in pooled mode.
// HTTP/SSE and the HTTP bridge use the same retryable code for capacity shedding.
// Other error codes pass through for client handling.
func TestProxyResponsesWebSocketFromClient_RewritesCapacityShedCodeForClient(t *testing.T) {
	tests := []struct {
		name           string
		upstreamEvents [][]byte
		wantContains   []string
		wantAbsent     []string
	}{
		{
			name: "capacity_shed_error_and_failed_are_rewritten",
			upstreamEvents: [][]byte{
				[]byte(`{"type":"error","error":{"type":"service_unavailable_error","code":"server_is_overloaded","message":"Our servers are currently overloaded. Please try again later."}}`),
				[]byte(`{"type":"response.failed","response":{"id":"resp_shed","status":"failed","error":{"code":"server_is_overloaded","message":"Our servers are currently overloaded. Please try again later."}}}`),
			},
			wantContains: []string{
				`"code":"server_error"`,
				"Our servers are currently overloaded",
			},
			wantAbsent: []string{"server_is_overloaded"},
		},
		{
			name: "non_capacity_error_code_is_passed_through",
			upstreamEvents: [][]byte{
				[]byte(`{"type":"error","error":{"type":"invalid_request_error","code":"workspace_suspended","message":"workspace is suspended"}}`),
				[]byte(`{"type":"response.failed","response":{"id":"resp_suspended","status":"failed","error":{"code":"workspace_suspended","message":"workspace is suspended"}}}`),
			},
			wantContains: []string{
				`"code":"workspace_suspended"`,
				"workspace is suspended",
			},
			wantAbsent: []string{"server_error"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			options := newOpenAIWSV2TestConfig()
			options.Request.URLPolicy.Enabled = false
			options.Request.URLPolicy.AllowInsecureHTTP = true
			options.Pool.MaxConnsPerProvider = 1
			options.Pool.MinIdlePerProvider = 0
			options.Pool.MaxIdlePerProvider = 1
			options.Pool.QueueLimitPerConn = 8
			options.WS.DialTimeoutSeconds = 3
			options.WS.ReadTimeoutSeconds = 3
			options.WS.WriteTimeoutSeconds = 3

			events := make([][]byte, 0, len(tt.upstreamEvents))
			for _, event := range tt.upstreamEvents {
				events = append(events, append([]byte(nil), event...))
			}
			captureConn := &openAIWSCaptureConn{events: events}
			pool := newOpenAIWSConnPool(options)
			pool.SetClientDialerForTest(&openAIWSCaptureDialer{conn: captureConn})

			provider := gatewayadapter.ExecutionProvider{
				Record: providercore.Record{
					LoadLocation: time.LoadLocation, ID: 5401,
					Name:        "openai-ingress-capacity-shed",
					Platform:    capability.PlatformOpenAI,
					Type:        capability.ProviderTypeAPIKey,
					Status:      billing.StatusActive,
					Schedulable: true,
					Concurrency: 1,
					Credentials: map[string]any{"api_key": "sk-test"},
					Extra:       map[string]any{"responses_websockets_v2_enabled": true},
				},
			}
			repo := &openAIWSIngressCapacityShedRepo{wsFixtureProviderStore: wsFixtureProviderStore{providers: []gatewayadapter.ExecutionProvider{provider}}}
			svc := newWSFixture(wsFixtureInputs{providers: repo, health: newUpstreamHealthForTest(repo, nil, nil, providercore.HealthOptions{}, nil), transport: &auxiliaryHTTPRecorder{}, cache: &testkit.StickyCache{}, options: options, corrector: openai.NewCodexToolCorrector(), pool: pool})

			serverDone := make(chan struct{})
			wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(serverDone)
				conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionContextTakeover})
				if err != nil {
					return
				}
				defer func() { _ = conn.CloseNow() }()

				rec := httptest.NewRecorder()
				ginCtx, _ := gin.CreateTestContext(rec)
				req := r.Clone(r.Context())
				req.Header = req.Header.Clone()
				req.Header.Set("User-Agent", "unit-test-agent/1.0")
				ginCtx.Request = req

				readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
				msgType, firstMessage, readErr := conn.Read(readCtx)
				cancel()
				if readErr != nil || (msgType != websocket.MessageText && msgType != websocket.MessageBinary) {
					return
				}
				_ = svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, &provider, "sk-test", firstMessage, nil)
			}))
			defer wsServer.Close()

			dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
			clientConn, _, err := websocket.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
			cancelDial()
			require.NoError(t, err)
			defer func() { _ = clientConn.CloseNow() }()

			writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
			err = clientConn.Write(writeCtx, websocket.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","stream":false}`))
			cancelWrite()
			require.NoError(t, err)

			var frames []string
			for len(frames) < len(tt.upstreamEvents) {
				readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				_, message, readErr := clientConn.Read(readCtx)
				cancel()
				if readErr != nil {
					break
				}
				frames = append(frames, string(message))
			}
			// End the client connection after the terminal so the ingress turn loop exits.
			_ = clientConn.CloseNow()

			require.NotEmpty(t, frames, "客户端应至少收到一个下发事件")
			joined := strings.Join(frames, "\n")
			for _, want := range tt.wantContains {
				require.Contains(t, joined, want, "客户端收到的事件:\n%s", joined)
			}
			for _, absent := range tt.wantAbsent {
				require.NotContains(t, joined, absent, "客户端收到的事件:\n%s", joined)
			}

			select {
			case <-serverDone:
			case <-time.After(5 * time.Second):
				t.Fatal("等待 ingress websocket 结束超时")
			}
		})
	}
}

// TestProxyResponsesWebSocketFromClient_MarksCyberPolicyBeforeEarlyReturn checks that evidence and usage reach AfterTurn on errors.
func TestProxyResponsesWebSocketFromClient_MarksCyberPolicyBeforeEarlyReturn(t *testing.T) {
	tests := []struct {
		name          string
		upstreamEvent []byte
		wantFailover  bool
		wantClientMsg bool
		wantInput     int
		wantOutput    int
	}{
		{
			name:          "error_before_rate_limit_failover",
			upstreamEvent: []byte(`{"type":"error","error":{"type":"rate_limit_error","code":"cyber_policy","message":"rate limit exceeded by cyber policy"},"usage":{"input_tokens":5,"output_tokens":1}}`),
			wantFailover:  true,
			wantInput:     5,
			wantOutput:    1,
		},
		{
			name:          "response_failed_terminal",
			upstreamEvent: []byte(`{"type":"response.failed","response":{"id":"resp_cyber","status":"failed","error":{"code":"cyber_policy","message":"blocked by cyber policy"},"usage":{"input_tokens":9,"output_tokens":2}}}`),
			wantClientMsg: true,
			wantInput:     9,
			wantOutput:    2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			options := newOpenAIWSV2TestConfig()
			options.Request.URLPolicy.Enabled = false
			options.Request.URLPolicy.AllowInsecureHTTP = true

			options.Pool.MinIdlePerProvider = 0

			captureConn := &openAIWSCaptureConn{events: [][]byte{append([]byte(nil), tt.upstreamEvent...)}}
			pool := newOpenAIWSConnPool(options)
			t.Cleanup(pool.Close)
			pool.SetClientDialerForTest(&openAIWSCaptureDialer{conn: captureConn})
			svc := newWSFixture(wsFixtureInputs{options: options, transport: &auxiliaryHTTPRecorder{}, cache: &testkit.StickyCache{}, corrector: openai.NewCodexToolCorrector(), pool: pool})
			provider := &gatewayadapter.ExecutionProvider{
				Record: providercore.Record{
					LoadLocation: time.LoadLocation, ID: 5402, Name: "openai-ingress-cyber", Platform: capability.PlatformOpenAI,
					Type: capability.ProviderTypeAPIKey, Status: billing.StatusActive, Schedulable: true, Concurrency: 1,
					Credentials: map[string]any{"api_key": "sk-test"},
					Extra: map[string]any{
						"responses_ws_connection_mode": "pooled",
					},
				},
			}

			markCh := make(chan *moderationflow.Mark, 1)
			serverErrCh := make(chan error, 1)
			wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionContextTakeover})
				if err != nil {
					serverErrCh <- err
					return
				}
				defer func() { _ = conn.CloseNow() }()

				readCtx, cancelRead := context.WithTimeout(r.Context(), 3*time.Second)
				_, firstMessage, err := conn.Read(readCtx)
				cancelRead()
				if err != nil {
					serverErrCh <- err
					return
				}

				recorder := httptest.NewRecorder()
				ginCtx, _ := gin.CreateTestContext(recorder)
				ginCtx.Request = r.Clone(r.Context())
				hooks := &ws.OpenAIIngressHooks{AfterTurn: func(_ ws.OpenAITurnCapture) {
					markCh <- gatewayhttp.GetOpsCyberPolicy(ginCtx)
				}}
				serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, provider, "sk-test", firstMessage, hooks)
			}))
			defer wsServer.Close()

			dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
			clientConn, _, err := websocket.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
			cancelDial()
			require.NoError(t, err)
			defer func() { _ = clientConn.CloseNow() }()

			writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
			err = clientConn.Write(writeCtx, websocket.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","stream":false}`))
			cancelWrite()
			require.NoError(t, err)

			if tt.wantClientMsg {
				readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
				_, message, readErr := clientConn.Read(readCtx)
				cancelRead()
				require.NoError(t, readErr)
				require.Equal(t, "response.failed", gjson.GetBytes(message, "type").String())
			}

			select {
			case mark := <-markCh:
				require.NotNil(t, mark)
				require.Equal(t, "cyber_policy", mark.Code)
				require.Equal(t, http.StatusOK, mark.UpstreamStatus)
				require.JSONEq(t, string(tt.upstreamEvent), mark.Body)
				require.Equal(t, tt.wantInput, mark.UpstreamInTok)
				require.Equal(t, tt.wantOutput, mark.UpstreamOutTok)
			case <-time.After(3 * time.Second):
				t.Fatal("AfterTurn 未读取到上游风控标记")
			}

			_ = clientConn.CloseNow()
			select {
			case serverErr := <-serverErrCh:
				var failoverErr *forwardcore.UpstreamFailoverError
				require.Equal(t, tt.wantFailover, errors.As(serverErr, &failoverErr))
			case <-time.After(5 * time.Second):
				t.Fatal("等待 ingress websocket 结束超时")
			}
		})
	}
}

func (s *openAIWS403CounterCacheStub) IncrementOpenAI403Count(_ context.Context, _ int64, _ int) (int64, error) {
	if len(s.counts) == 0 {
		return 1, nil
	}
	count := s.counts[0]
	s.counts = s.counts[1:]
	return count, nil
}

func (s *openAIWS403CounterCacheStub) ResetOpenAI403Count(_ context.Context, _ int64) error {
	return nil
}

func (r *openAIWSRateLimitSignalRepo) SetRateLimited(_ context.Context, _ int64, resetAt time.Time) error {
	r.rateLimitCalls = append(r.rateLimitCalls, resetAt)
	return nil
}

func (r *openAIWSRateLimitSignalRepo) SetTempUnschedulable(_ context.Context, _ int64, until time.Time, _ string) error {
	r.tempCalls = append(r.tempCalls, until)
	return nil
}

func (r *openAIWSRateLimitSignalRepo) SetError(_ context.Context, _ int64, errorMsg string) error {
	r.errorCalls = append(r.errorCalls, errorMsg)
	return nil
}

func (r *openAIWSRateLimitSignalRepo) UpdateExtra(_ context.Context, _ int64, updates map[string]any) error {
	copied := make(map[string]any, len(updates))
	maps.Copy(copied, updates)
	r.updateExtra = append(r.updateExtra, copied)
	return nil
}

func (d *openAIWSStatusErrorDialer) Dial(context.Context, string, http.Header, string, *tlsfingerprint.Profile) (openai.WSClientConn, int, http.Header, error) {
	err := d.err
	if err == nil {
		err = errors.New("openai ws dial failed")
	}
	return nil, d.status, upstreamcore.CloneHeader(d.header), err
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_ErrorEventUsageLimitPersistsRateLimit(t *testing.T) {
	options := newOpenAIWSV2TestConfig()
	options.Request.URLPolicy.Enabled = false
	options.Request.URLPolicy.AllowInsecureHTTP = true
	options.Pool.MaxConnsPerProvider = 1
	options.Pool.MinIdlePerProvider = 0
	options.Pool.MaxIdlePerProvider = 1
	options.Pool.QueueLimitPerConn = 8
	options.WS.DialTimeoutSeconds = 3
	options.WS.ReadTimeoutSeconds = 3
	options.WS.WriteTimeoutSeconds = 3

	resetAt := time.Now().Add(90 * time.Minute).Unix()
	captureConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"error","error":{"code":"rate_limit_exceeded","type":"usage_limit_reached","message":"The usage limit has been reached","resets_at":PLACEHOLDER}}`),
		},
	}
	captureConn.events[0] = []byte(strings.ReplaceAll(string(captureConn.events[0]), "PLACEHOLDER", strconv.FormatInt(resetAt, 10)))
	captureDialer := &openAIWSCaptureDialer{conn: captureConn}
	pool := newOpenAIWSConnPool(options)
	pool.SetClientDialerForTest(captureDialer)

	provider := gatewayadapter.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 503,
			Name:        "openai-ingress-rate-limit",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Credentials: map[string]any{
				"api_key": "sk-test",
			},
			Extra: map[string]any{
				"responses_websockets_v2_enabled": true,
			},
		},
	}
	repo := &openAIWSRateLimitSignalRepo{wsFixtureProviderStore: wsFixtureProviderStore{providers: []gatewayadapter.ExecutionProvider{provider}}}
	rateSvc := newUpstreamHealthForTest(repo, nil, nil, providercore.HealthOptions{ForbiddenCounter: &openAIWS403CounterCacheStub{counts: []int64{1}}}, nil)

	svc := newWSFixture(wsFixtureInputs{providers: repo, health: rateSvc, transport: &auxiliaryHTTPRecorder{}, cache: &testkit.StickyCache{}, options: options, corrector: openai.NewCodexToolCorrector(), pool: pool})

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionContextTakeover})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() { _ = conn.CloseNow() }()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != websocket.MessageText && msgType != websocket.MessageBinary {
			serverErrCh <- io.ErrUnexpectedEOF
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, &provider, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := websocket.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() { _ = clientConn.CloseNow() }()

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, websocket.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","stream":false}`))
	cancelWrite()
	require.NoError(t, err)

	select {
	case serverErr := <-serverErrCh:
		require.Error(t, serverErr)
		var failoverErr *forwardcore.UpstreamFailoverError
		require.ErrorAs(t, serverErr, &failoverErr)
		require.Equal(t, http.StatusTooManyRequests, failoverErr.StatusCode)
		require.Len(t, repo.rateLimitCalls, 1)
		require.WithinDuration(t, time.Unix(resetAt, 0), repo.rateLimitCalls[0], 2*time.Second)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 结束超时")
	}
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_Handshake403PersistsTempUnschedulable(t *testing.T) {
	options := newOpenAIWSV2TestConfig()
	options.Request.URLPolicy.Enabled = false
	options.Request.URLPolicy.AllowInsecureHTTP = true
	options.Pool.MaxConnsPerProvider = 1
	options.Pool.MinIdlePerProvider = 0
	options.Pool.MaxIdlePerProvider = 1
	options.Pool.QueueLimitPerConn = 8
	options.WS.DialTimeoutSeconds = 3
	options.WS.ReadTimeoutSeconds = 3
	options.WS.WriteTimeoutSeconds = 3
	pool := newOpenAIWSConnPool(options)
	pool.SetClientDialerForTest(&openAIWSStatusErrorDialer{
		status: http.StatusForbidden,
		err:    errors.New("temporary forbidden"),
	})

	provider := gatewayadapter.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 505,
			Name:        "openai-ingress-forbidden-handshake",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeOAuth,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Credentials: map[string]any{
				"access_token": "test-access-token",
			},
			Extra: map[string]any{
				"responses_websockets_v2_enabled": true,
			},
		},
	}
	repo := &openAIWSRateLimitSignalRepo{wsFixtureProviderStore: wsFixtureProviderStore{providers: []gatewayadapter.ExecutionProvider{provider}}}
	rateSvc := newUpstreamHealthForTest(repo, nil, nil, providercore.HealthOptions{ForbiddenCounter: &openAIWS403CounterCacheStub{counts: []int64{1}}}, nil)

	svc := newWSFixture(wsFixtureInputs{providers: repo, health: rateSvc, transport: &auxiliaryHTTPRecorder{}, cache: &testkit.StickyCache{}, options: options, corrector: openai.NewCodexToolCorrector(), pool: pool})

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionContextTakeover})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() { _ = conn.CloseNow() }()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != websocket.MessageText && msgType != websocket.MessageBinary {
			serverErrCh <- io.ErrUnexpectedEOF
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, &provider, "test-access-token", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := websocket.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() { _ = clientConn.CloseNow() }()

	before := time.Now()
	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, websocket.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","stream":false}`))
	cancelWrite()
	require.NoError(t, err)

	select {
	case serverErr := <-serverErrCh:
		require.Error(t, serverErr)
		require.Len(t, repo.tempCalls, 1)
		require.WithinDuration(t, before.Add(10*time.Minute), repo.tempCalls[0], 2*time.Second)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 结束超时")
	}
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_ErrorEventForbiddenPersistsTempUnschedulable(t *testing.T) {
	options := newOpenAIWSV2TestConfig()
	options.Request.URLPolicy.Enabled = false
	options.Request.URLPolicy.AllowInsecureHTTP = true
	options.Pool.MaxConnsPerProvider = 1
	options.Pool.MinIdlePerProvider = 0
	options.Pool.MaxIdlePerProvider = 1
	options.Pool.QueueLimitPerConn = 8
	options.WS.DialTimeoutSeconds = 3
	options.WS.ReadTimeoutSeconds = 3
	options.WS.WriteTimeoutSeconds = 3
	pool := newOpenAIWSConnPool(options)
	pool.SetClientDialerForTest(&openAIWSCaptureDialer{
		conn: &openAIWSCaptureConn{
			events: [][]byte{
				[]byte(`{"type":"error","error":{"code":"forbidden","type":"permission_error","message":"temporary forbidden"}}`),
			},
		},
	})

	provider := gatewayadapter.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 507,
			Name:        "openai-ingress-forbidden-event",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeOAuth,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Credentials: map[string]any{
				"access_token": "test-access-token",
			},
			Extra: map[string]any{
				"responses_websockets_v2_enabled": true,
			},
		},
	}
	repo := &openAIWSRateLimitSignalRepo{wsFixtureProviderStore: wsFixtureProviderStore{providers: []gatewayadapter.ExecutionProvider{provider}}}
	rateSvc := newUpstreamHealthForTest(repo, nil, nil, providercore.HealthOptions{ForbiddenCounter: &openAIWS403CounterCacheStub{counts: []int64{1}}}, nil)

	svc := newWSFixture(wsFixtureInputs{providers: repo, health: rateSvc, transport: &auxiliaryHTTPRecorder{}, cache: &testkit.StickyCache{}, options: options, corrector: openai.NewCodexToolCorrector(), pool: pool})

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionContextTakeover})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() { _ = conn.CloseNow() }()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != websocket.MessageText && msgType != websocket.MessageBinary {
			serverErrCh <- io.ErrUnexpectedEOF
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, &provider, "test-access-token", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := websocket.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() { _ = clientConn.CloseNow() }()

	before := time.Now()
	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, websocket.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","stream":false}`))
	cancelWrite()
	require.NoError(t, err)

	select {
	case serverErr := <-serverErrCh:
		require.Error(t, serverErr)
		require.Len(t, repo.tempCalls, 1)
		require.Len(t, repo.rateLimitCalls, 0)
		require.WithinDuration(t, before.Add(10*time.Minute), repo.tempCalls[0], 2*time.Second)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 结束超时")
	}
}

func (d *runtimeTestDialer) Dial(ctx context.Context, _ string, _ http.Header, _ string, _ *tlsfingerprint.Profile) (openai.WSClientConn, int, http.Header, error) {
	deadline, _ := ctx.Deadline()
	d.mu.Lock()
	n := len(d.budgets)
	d.budgets = append(d.budgets, time.Until(deadline))
	var conn openai.WSClientConn
	if n < len(d.conns) {
		conn = d.conns[n]
	}
	d.mu.Unlock()
	if conn != nil {
		return conn, 0, nil, nil
	}
	// Delay prewarm dialing to overlap later client turns.
	time.Sleep(time.Millisecond)
	return &openAIWSFakeConn{}, 0, nil, nil
}

// TestWSRuntimeAcrossTurns covers concurrent prewarming and turn snapshots, plus reconnect budgets after updates.
func TestWSRuntimeAcrossTurns(t *testing.T) {
	previousPingIdle := openAIWSIngressPreflightPingIdle
	openAIWSIngressPreflightPingIdle = 0
	defer func() { openAIWSIngressPreflightPingIdle = previousPingIdle }()
	for _, reconnect := range []bool{false, true} {
		name := "prewarm"
		if reconnect {
			name = "reconnect_timeout"
		}
		t.Run(name, func(t *testing.T) {
			first := []byte(`{"type":"response.completed","response":{"id":"resp_first","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`)
			second := []byte(`{"type":"response.completed","response":{"id":"resp_second","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`)
			dialer := &runtimeTestDialer{conns: []openai.WSClientConn{&openAIWSCaptureConn{events: [][]byte{first, second}}}}
			options := &wsFixtureOptions{}
			options.Pool.MaxConnsPerProvider = 4
			options.Pool.MinIdlePerProvider = 4
			options.Pool.MaxIdlePerProvider = 4
			options.WS.DialTimeoutSeconds = 1
			if reconnect {
				options.Pool.MaxConnsPerProvider = 1
				options.Pool.MinIdlePerProvider = 0
				options.Pool.MaxIdlePerProvider = 1
				dialer.conns = []openai.WSClientConn{&openAIWSPreflightFailConn{events: [][]byte{first}}, &openAIWSCaptureConn{events: [][]byte{second}}}
			}
			pool := newOpenAIWSConnPool(options)
			pool.SetClientDialerForTest(dialer)
			defer pool.Close()
			svc := newWSFixture(wsFixtureInputs{options: options, pool: pool, transport: &auxiliaryHTTPRecorder{}, cache: &testkit.StickyCache{}})
			defaults := ws.DefaultParameters()
			defaults.DialTimeoutSeconds = 1
			svc.Runtime = ws.NewRuntime(defaults, nil, nil)
			svc.Runtime.SetApply(func(value ws.Parameters) error {
				updated := *wsFixturePoolOptions(options)
				updated.DialTimeoutSeconds = value.DialTimeoutSeconds
				return pool.UpdateOptions(updated)
			})
			provider := &gatewayadapter.ExecutionProvider{Record: providercore.Record{
				LoadLocation: time.LoadLocation, ID: 119, Platform: capability.PlatformOpenAI,
				Type: capability.ProviderTypeAPIKey, Concurrency: 1,
				Credentials: map[string]any{"api_key": "test"},
			}}
			finished := make(chan error, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					finished <- err
					return
				}
				defer func() { _ = conn.CloseNow() }()
				ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
				defer cancel()
				_, payload, err := conn.Read(ctx)
				if err != nil {
					finished <- err
					return
				}
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = r
				finished <- svc.ProxyResponsesWebSocketFromClient(ctx, c, conn, provider, "test", payload, nil)
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			client, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
			require.NoError(t, err)
			defer func() { _ = client.CloseNow() }()
			turn := func(payload, responseID string) {
				require.NoError(t, client.Write(ctx, websocket.MessageText, []byte(payload)))
				_, event, err := client.Read(ctx)
				require.NoError(t, err)
				require.Equal(t, responseID, gjson.GetBytes(event, "response.id").String())
			}
			turn(`{"type":"response.create","model":"gpt-5.1"}`, "resp_first")
			require.NoError(t, svc.Runtime.Publish(`{"dial_timeout_seconds":10}`))
			turn(`{"type":"response.create","model":"gpt-5.1","previous_response_id":"resp_first"}`, "resp_second")
			require.NoError(t, client.Close(websocket.StatusNormalClosure, ""))
			require.NoError(t, <-finished)
			if reconnect {
				dialer.mu.Lock()
				budgets := append([]time.Duration(nil), dialer.budgets...)
				dialer.mu.Unlock()
				require.Len(t, budgets, 2)
				require.InDelta(t, 3, budgets[0].Seconds(), 0.5)
				require.InDelta(t, 12, budgets[1].Seconds(), 0.5)
			}
		})
	}
}

func newStagedPassthroughConn() *stagedPassthroughConn {
	return &stagedPassthroughConn{
		frames: make(chan stagedPassthroughFrame, 4),
		writes: make(chan []byte, 4),
		closed: make(chan struct{}),
	}
}

func (d *stagedPassthroughDialer) Dial(context.Context, string, http.Header, string, *tlsfingerprint.Profile) (openai.WSClientConn, int, http.Header, error) {
	return d.conn, http.StatusSwitchingProtocols, http.Header{}, nil
}

func newPassthroughLifecycleService(options *wsFixtureOptions, upstream *stagedPassthroughConn) *wsExecutionFixture {
	return newWSFixture(wsFixtureInputs{options: options, transport: &auxiliaryHTTPRecorder{}, cache: &testkit.StickyCache{}, corrector: openai.NewCodexToolCorrector(), dialer: &stagedPassthroughDialer{conn: upstream}})
}

func passthroughLifecycleConfig() *wsFixtureOptions {
	options := &wsFixtureOptions{}
	options.Request.URLPolicy.Enabled = false
	options.Request.URLPolicy.AllowInsecureHTTP = true
	options.Output.OpenAIFirstOutputTimeoutSeconds = 1

	options.WS.IngressInterTurnIdleTimeoutSeconds = 1
	options.WS.DialTimeoutSeconds = 3
	options.WS.ReadTimeoutSeconds = 1
	options.WS.WriteTimeoutSeconds = 3
	return options
}

func passthroughLifecycleProvider() *gatewayadapter.ExecutionProvider {
	return &gatewayadapter.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 901,
			Name:        "passthrough-lifecycle",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Credentials: map[string]any{"api_key": "sk-test"},
			Extra: map[string]any{
				"responses_ws_connection_mode": "per_session",
			},
		},
	}
}

func startPassthroughLifecycleServer(
	t *testing.T,
	controlCtx context.Context,
	svc *wsExecutionFixture,
	provider *gatewayadapter.ExecutionProvider,
) (*httptest.Server, <-chan error) {
	return startPassthroughLifecycleServerWithHooks(t, controlCtx, svc, provider, nil)
}

func startPassthroughLifecycleServerWithHooks(
	t *testing.T,
	controlCtx context.Context,
	svc *wsExecutionFixture,
	provider *gatewayadapter.ExecutionProvider,
	hooksFactory func(*gin.Context) *ws.OpenAIIngressHooks,
) (*httptest.Server, <-chan error) {
	t.Helper()
	serverErr := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionContextTakeover})
		if err != nil {
			serverErr <- err
			return
		}
		defer func() { _ = conn.CloseNow() }()

		msgType, firstMessage, err := gatewayhttp.ReadOpenAIWSClientMessage(
			controlCtx,
			conn,
			3*time.Second,
			websocket.StatusPolicyViolation,
			"missing first response.create message",
		)
		if err != nil {
			serverErr <- err
			return
		}
		if msgType != websocket.MessageText {
			serverErr <- errors.New("first message was not text")
			return
		}

		recorder := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(recorder)
		req := r.Clone(controlCtx)
		req.Header = req.Header.Clone()
		ginCtx.Request = req
		var hooks *ws.OpenAIIngressHooks
		if hooksFactory != nil {
			hooks = hooksFactory(ginCtx)
		}
		serverErr <- svc.ProxyResponsesWebSocketFromClient(controlCtx, ginCtx, conn, provider, "sk-test", firstMessage, hooks)
	}))
	return server, serverErr
}

func TestPassthroughLifecycle_CyberTerminalEventsMarkBeforeAfterTurn(t *testing.T) {
	tests := []struct {
		name        string
		events      []string
		wantBody    string
		wantMessage string
		wantInput   int
		wantOutput  int
	}{
		{
			name: "error",
			events: []string{
				`{"type":"error","error":{"code":"cyber_policy","message":"blocked by error event"},"usage":{"input_tokens":5,"output_tokens":1}}`,
				`{"type":"response.failed","response":{"id":"resp_error","error":{"code":"cyber_policy","message":"blocked by paired failed event"},"usage":{"input_tokens":9,"output_tokens":2}}}`,
			},
			wantBody:    `"type":"error"`,
			wantMessage: "blocked by error event",
			wantInput:   5,
			wantOutput:  1,
		},
		{
			name: "response_failed",
			events: []string{
				`{"type":"response.failed","response":{"id":"resp_failed","error":{"code":"cyber_policy","message":"blocked by failed event"},"usage":{"input_tokens":9,"output_tokens":2}}}`,
			},
			wantBody:    `"type":"response.failed"`,
			wantMessage: "blocked by failed event",
			wantInput:   9,
			wantOutput:  2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			controlCtx, cancelControl := context.WithCancelCause(context.Background())
			defer cancelControl(context.Canceled)
			upstream := newStagedPassthroughConn()
			for _, event := range tt.events {
				upstream.Send(event)
			}

			markSeen := make(chan moderationflow.Mark, 1)
			afterTurnCalls := atomic.Int32{}
			server, serverErr := startPassthroughLifecycleServerWithHooks(
				t,
				controlCtx,
				newPassthroughLifecycleService(passthroughLifecycleConfig(), upstream),
				passthroughLifecycleProvider(),
				func(c *gin.Context) *ws.OpenAIIngressHooks {
					return &ws.OpenAIIngressHooks{AfterTurn: func(_ ws.OpenAITurnCapture) {
						afterTurnCalls.Add(1)
						if mark := gatewayhttp.GetOpsCyberPolicy(c); mark != nil {
							select {
							case markSeen <- *mark:
							default:
							}
						}
					}}
				},
			)
			defer server.Close()
			clientConn := dialPassthroughLifecycleClient(t, server)
			defer func() { _ = clientConn.CloseNow() }()

			for range tt.events {
				_, err := readPassthroughLifecycleFrame(t, clientConn, 3*time.Second)
				require.NoError(t, err)
			}

			select {
			case mark := <-markSeen:
				require.Equal(t, "cyber_policy", mark.Code)
				require.Equal(t, tt.wantMessage, mark.Message)
				require.Contains(t, mark.Body, tt.wantBody)
				require.Equal(t, http.StatusOK, mark.UpstreamStatus)
				require.Equal(t, tt.wantInput, mark.UpstreamInTok)
				require.Equal(t, tt.wantOutput, mark.UpstreamOutTok)
			case <-time.After(3 * time.Second):
				t.Fatal("cyber mark was not visible to AfterTurn")
			}
			require.NoError(t, clientConn.Close(websocket.StatusNormalClosure, "done"))
			select {
			case <-serverErr:
			case <-time.After(3 * time.Second):
				t.Fatal("cyber passthrough test did not exit")
			}
			require.Equal(t, int32(1), afterTurnCalls.Load(), "error/response.failed pair must complete and record once")
		})
	}
}

func TestPassthroughLifecycle_NonCyberFailureKeepsProviderSideEffects(t *testing.T) {
	controlCtx, cancelControl := context.WithCancelCause(context.Background())
	defer cancelControl(context.Canceled)
	upstream := newStagedPassthroughConn()
	upstream.Send(`{"type":"response.failed","response":{"id":"resp_non_cyber","error":{"type":"authentication_error","code":"invalid_api_key","status_code":401,"message":"credential rejected"},"usage":{"input_tokens":3,"output_tokens":1}}}`)
	repo := &openAIStream403ProviderRepo{}
	svc := newPassthroughLifecycleService(passthroughLifecycleConfig(), upstream)
	setWSFixtureHealth(svc, newUpstreamHealthForTest(repo, svc.options, nil, providercore.HealthOptions{}, nil))

	provider := passthroughLifecycleProvider()

	markSeen := make(chan *moderationflow.Mark, 1)
	server, serverErr := startPassthroughLifecycleServerWithHooks(
		t,
		controlCtx,
		svc,
		provider,
		func(c *gin.Context) *ws.OpenAIIngressHooks {
			return &ws.OpenAIIngressHooks{AfterTurn: func(_ ws.OpenAITurnCapture) {
				markSeen <- gatewayhttp.GetOpsCyberPolicy(c)
			}}
		},
	)
	defer server.Close()
	clientConn := dialPassthroughLifecycleClient(t, server)
	defer func() { _ = clientConn.CloseNow() }()

	event, err := readPassthroughLifecycleFrame(t, clientConn, 3*time.Second)
	require.NoError(t, err)
	require.Equal(t, "response.failed", gjson.GetBytes(event, "type").String())
	select {
	case mark := <-markSeen:
		require.Nil(t, mark)
	case <-time.After(3 * time.Second):
		t.Fatal("non-cyber terminal event did not complete its turn")
	}
	require.Equal(t, 1, repo.setErrorCalls, "non-cyber credential failure must retain provider failure side effects")
	require.True(t, wsFixtureProviderBlocked(svc, provider))
	require.NoError(t, clientConn.Close(websocket.StatusNormalClosure, "done"))
	select {
	case <-serverErr:
	case <-time.After(3 * time.Second):
		t.Fatal("non-cyber passthrough test did not exit")
	}
}

func TestPassthroughLifecycle_CyberSkipsFailureProviderSideEffects(t *testing.T) {
	controlCtx, cancelControl := context.WithCancelCause(context.Background())
	defer cancelControl(context.Canceled)
	upstream := newStagedPassthroughConn()
	upstream.Send(`{"type":"response.failed","response":{"id":"resp_cyber_auth","error":{"type":"authentication_error","code":"cyber_policy","status_code":401,"message":"request blocked"}}}`)
	repo := &openAIStream403ProviderRepo{}
	svc := newPassthroughLifecycleService(passthroughLifecycleConfig(), upstream)
	setWSFixtureHealth(svc, newUpstreamHealthForTest(repo, svc.options, nil, providercore.HealthOptions{}, nil))

	provider := passthroughLifecycleProvider()

	server, serverErr := startPassthroughLifecycleServer(t, controlCtx, svc, provider)
	defer server.Close()
	clientConn := dialPassthroughLifecycleClient(t, server)
	defer func() { _ = clientConn.CloseNow() }()

	event, err := readPassthroughLifecycleFrame(t, clientConn, 3*time.Second)
	require.NoError(t, err)
	require.Equal(t, "response.failed", gjson.GetBytes(event, "type").String())
	require.Zero(t, repo.setErrorCalls, "cyber_policy is request-scoped and must not cool down the provider")
	require.False(t, wsFixtureProviderBlocked(svc, provider))

	require.NoError(t, clientConn.Close(websocket.StatusNormalClosure, "done"))
	select {
	case <-serverErr:
	case <-time.After(3 * time.Second):
		t.Fatal("cyber side-effect test did not exit")
	}
}

func TestPassthroughLifecycle_CloseReasonTruncationPreservesUTF8(t *testing.T) {
	controlCtx, cancelControl := context.WithCancelCause(context.Background())
	defer cancelControl(context.Canceled)
	upstream := newStagedPassthroughConn()
	originalReason := strings.Repeat("a", 119) + "界"
	upstream.Fail(gatewayhttp.NewOpenAIWSClientCloseError(websocket.StatusPolicyViolation, originalReason, errors.New("policy rejected")))

	server, serverErr := startPassthroughLifecycleServer(
		t,
		controlCtx,
		newPassthroughLifecycleService(passthroughLifecycleConfig(), upstream),
		passthroughLifecycleProvider(),
	)
	defer server.Close()
	clientConn := dialPassthroughLifecycleClient(t, server)
	defer func() { _ = clientConn.CloseNow() }()

	_, err := readPassthroughLifecycleFrame(t, clientConn, 3*time.Second)
	var closeErr websocket.CloseError
	require.ErrorAs(t, err, &closeErr)
	require.Equal(t, websocket.StatusPolicyViolation, closeErr.Code)
	require.True(t, utf8.ValidString(closeErr.Reason))
	require.LessOrEqual(t, len(closeErr.Reason), 120)
	require.Equal(t, strings.Repeat("a", 119), closeErr.Reason)

	select {
	case <-serverErr:
	case <-time.After(3 * time.Second):
		t.Fatal("passthrough close reason test did not exit")
	}
}

func dialPassthroughLifecycleClient(t *testing.T, server *httptest.Server) *websocket.Conn {
	t.Helper()
	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := websocket.Dial(dialCtx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, websocket.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","stream":false}`))
	cancelWrite()
	require.NoError(t, err)
	return clientConn
}

func readPassthroughLifecycleFrame(t *testing.T, clientConn *websocket.Conn, timeout time.Duration) ([]byte, error) {
	t.Helper()
	readCtx, cancelRead := context.WithTimeout(context.Background(), timeout)
	_, payload, err := clientConn.Read(readCtx)
	cancelRead()
	return payload, err
}

func requirePassthroughUpstreamWrite(t *testing.T, upstream *stagedPassthroughConn, timeout time.Duration) []byte {
	t.Helper()
	select {
	case payload := <-upstream.writes:
		return payload
	case <-time.After(timeout):
		t.Fatal("passthrough request was not forwarded upstream")
		return nil
	}
}

func TestPassthroughLifecycle_LeaseLossSendsRetryClose(t *testing.T) {
	controlCtx, cancelControl := context.WithCancelCause(context.Background())
	upstream := newStagedPassthroughConn()
	upstream.Send(`{"type":"response.created","response":{"id":"resp_lease","model":"gpt-5.1"}}`)
	server, serverErr := startPassthroughLifecycleServer(t, controlCtx, newPassthroughLifecycleService(passthroughLifecycleConfig(), upstream), passthroughLifecycleProvider())
	defer server.Close()
	clientConn := dialPassthroughLifecycleClient(t, server)
	defer func() { _ = clientConn.CloseNow() }()

	event, err := readPassthroughLifecycleFrame(t, clientConn, 3*time.Second)
	require.NoError(t, err)
	require.Equal(t, "response.created", gjson.GetBytes(event, "type").String())
	cancelControl(scheduler.ErrOpenAIWSIngressLeaseLost)

	_, err = readPassthroughLifecycleFrame(t, clientConn, 3*time.Second)
	var closeErr websocket.CloseError
	require.ErrorAs(t, err, &closeErr)
	require.Equal(t, websocket.StatusTryAgainLater, closeErr.Code)
	require.Equal(t, "websocket ingress capacity lease lost; please reconnect", closeErr.Reason)
	select {
	case <-serverErr:
	case <-time.After(3 * time.Second):
		t.Fatal("passthrough lease-loss reader did not exit")
	}
}

func TestPassthroughLifecycle_CompletedTurnStartsInterTurnIdle(t *testing.T) {
	controlCtx, cancelControl := context.WithCancelCause(context.Background())
	defer cancelControl(context.Canceled)
	upstream := newStagedPassthroughConn()
	upstream.Send(`{"type":"response.completed","response":{"id":"resp_idle","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`)
	server, serverErr := startPassthroughLifecycleServer(t, controlCtx, newPassthroughLifecycleService(passthroughLifecycleConfig(), upstream), passthroughLifecycleProvider())
	defer server.Close()
	clientConn := dialPassthroughLifecycleClient(t, server)
	defer func() { _ = clientConn.CloseNow() }()

	event, err := readPassthroughLifecycleFrame(t, clientConn, 3*time.Second)
	require.NoError(t, err)
	require.Equal(t, "response.completed", gjson.GetBytes(event, "type").String())
	_, err = readPassthroughLifecycleFrame(t, clientConn, 3*time.Second)
	var closeErr websocket.CloseError
	require.ErrorAs(t, err, &closeErr)
	require.Equal(t, websocket.StatusNormalClosure, closeErr.Code)
	require.Equal(t, "websocket idle timeout", closeErr.Reason)
	select {
	case <-serverErr:
	case <-time.After(3 * time.Second):
		t.Fatal("passthrough idle reader did not exit")
	}
}

func TestPassthroughLifecycle_ActiveTurnInactivityUsesReadTimeout(t *testing.T) {
	controlCtx, cancelControl := context.WithCancelCause(context.Background())
	defer cancelControl(context.Canceled)
	upstream := newStagedPassthroughConn()
	upstream.Send(`{"type":"response.output_text.delta","response_id":"resp_active","delta":"hello"}`)
	server, serverErr := startPassthroughLifecycleServer(t, controlCtx, newPassthroughLifecycleService(passthroughLifecycleConfig(), upstream), passthroughLifecycleProvider())
	defer server.Close()
	clientConn := dialPassthroughLifecycleClient(t, server)
	defer func() { _ = clientConn.CloseNow() }()

	delta, err := readPassthroughLifecycleFrame(t, clientConn, 3*time.Second)
	require.NoError(t, err)
	require.Equal(t, "response.output_text.delta", gjson.GetBytes(delta, "type").String())
	_, err = readPassthroughLifecycleFrame(t, clientConn, 2500*time.Millisecond)
	var websocketCloseErr websocket.CloseError
	require.ErrorAs(t, err, &websocketCloseErr)
	require.Equal(t, websocket.StatusGoingAway, websocketCloseErr.Code)
	require.Equal(t, "upstream websocket read timeout; please reconnect", websocketCloseErr.Reason)
	select {
	case err := <-serverErr:
		var closeErr *gatewayhttp.OpenAIWSClientCloseError
		require.ErrorAs(t, err, &closeErr)
		require.Equal(t, websocket.StatusGoingAway, closeErr.StatusCode())
		require.Equal(t, "upstream websocket read timeout; please reconnect", closeErr.Reason())
	case <-time.After(2500 * time.Millisecond):
		t.Fatal("passthrough active turn remained unbounded after upstream activity stopped")
	}
}

func TestPassthroughLifecycle_PreambleAllowsPromptClientCancel(t *testing.T) {
	controlCtx, cancelControl := context.WithCancelCause(context.Background())
	defer cancelControl(context.Canceled)
	options := passthroughLifecycleConfig()
	options.Output.OpenAIFirstOutputTimeoutSeconds = 3
	upstream := newStagedPassthroughConn()
	upstream.Send(`{"type":"response.created","response":{"id":"resp_cancel","model":"gpt-5.1"}}`)
	server, serverErr := startPassthroughLifecycleServer(t, controlCtx, newPassthroughLifecycleService(options, upstream), passthroughLifecycleProvider())
	defer server.Close()
	clientConn := dialPassthroughLifecycleClient(t, server)
	defer func() { _ = clientConn.CloseNow() }()
	require.Equal(t, "response.create", gjson.GetBytes(requirePassthroughUpstreamWrite(t, upstream, time.Second), "type").String())

	created, err := readPassthroughLifecycleFrame(t, clientConn, time.Second)
	require.NoError(t, err)
	require.Equal(t, "response.created", gjson.GetBytes(created, "type").String())
	writeCtx, cancelWrite := context.WithTimeout(context.Background(), time.Second)
	err = clientConn.Write(writeCtx, websocket.MessageText, []byte(`{"type":"response.cancel","response_id":"resp_cancel"}`))
	cancelWrite()
	require.NoError(t, err)
	cancelFrame := requirePassthroughUpstreamWrite(t, upstream, 500*time.Millisecond)
	require.Equal(t, "response.cancel", gjson.GetBytes(cancelFrame, "type").String())

	require.NoError(t, clientConn.Close(websocket.StatusNormalClosure, "done"))
	select {
	case <-serverErr:
	case <-time.After(3 * time.Second):
		t.Fatal("passthrough cancel test did not exit")
	}
}

func TestPassthroughLifecycle_RejectsOverlappingResponseCreate(t *testing.T) {
	controlCtx, cancelControl := context.WithCancelCause(context.Background())
	defer cancelControl(context.Canceled)
	options := passthroughLifecycleConfig()
	options.Output.OpenAIFirstOutputTimeoutSeconds = 3
	upstream := newStagedPassthroughConn()
	upstream.Send(`{"type":"response.created","response":{"id":"resp_overlap_first","model":"gpt-5.1"}}`)
	server, serverErr := startPassthroughLifecycleServer(t, controlCtx, newPassthroughLifecycleService(options, upstream), passthroughLifecycleProvider())
	defer server.Close()
	clientConn := dialPassthroughLifecycleClient(t, server)
	defer func() { _ = clientConn.CloseNow() }()
	require.Equal(t, "response.create", gjson.GetBytes(requirePassthroughUpstreamWrite(t, upstream, time.Second), "type").String())

	created, err := readPassthroughLifecycleFrame(t, clientConn, time.Second)
	require.NoError(t, err)
	require.Equal(t, "response.created", gjson.GetBytes(created, "type").String())
	writeCtx, cancelWrite := context.WithTimeout(context.Background(), time.Second)
	err = clientConn.Write(writeCtx, websocket.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1"}`))
	cancelWrite()
	require.NoError(t, err)

	_, err = readPassthroughLifecycleFrame(t, clientConn, time.Second)
	var websocketCloseErr websocket.CloseError
	require.ErrorAs(t, err, &websocketCloseErr)
	require.Equal(t, websocket.StatusPolicyViolation, websocketCloseErr.Code)
	require.Equal(t, "overlapping response.create is not supported", websocketCloseErr.Reason)
	select {
	case err := <-serverErr:
		var closeErr *gatewayhttp.OpenAIWSClientCloseError
		require.ErrorAs(t, err, &closeErr)
		require.Equal(t, websocket.StatusPolicyViolation, closeErr.StatusCode())
		require.Equal(t, "overlapping response.create is not supported", closeErr.Reason())
	case <-time.After(3 * time.Second):
		t.Fatal("overlapping response.create did not terminate passthrough")
	}
}

func TestPassthroughLifecycle_ActiveTurnActivityRefreshesReadTimeout(t *testing.T) {
	controlCtx, cancelControl := context.WithCancelCause(context.Background())
	defer cancelControl(context.Canceled)
	upstream := newStagedPassthroughConn()
	upstream.Send(`{"type":"response.output_text.delta","response_id":"resp_active_refresh","delta":"one"}`)
	go func() {
		for _, event := range []string{
			`{"type":"response.output_text.delta","response_id":"resp_active_refresh","delta":"two"}`,
			`{"type":"response.output_text.delta","response_id":"resp_active_refresh","delta":"three"}`,
			`{"type":"response.completed","response":{"id":"resp_active_refresh","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":3}}}`,
		} {
			timer := time.NewTimer(600 * time.Millisecond)
			<-timer.C
			timer.Stop()
			upstream.Send(event)
		}
	}()
	server, serverErr := startPassthroughLifecycleServer(t, controlCtx, newPassthroughLifecycleService(passthroughLifecycleConfig(), upstream), passthroughLifecycleProvider())
	defer server.Close()
	clientConn := dialPassthroughLifecycleClient(t, server)
	defer func() { _ = clientConn.CloseNow() }()

	for _, wantType := range []string{
		"response.output_text.delta",
		"response.output_text.delta",
		"response.output_text.delta",
		"response.completed",
	} {
		frame, err := readPassthroughLifecycleFrame(t, clientConn, 3*time.Second)
		require.NoError(t, err)
		require.Equal(t, wantType, gjson.GetBytes(frame, "type").String())
	}
	require.NoError(t, clientConn.Close(websocket.StatusNormalClosure, "done"))
	select {
	case <-serverErr:
	case <-time.After(3 * time.Second):
		t.Fatal("passthrough active-turn refresh test did not exit")
	}
}

func TestPassthroughLifecycle_TerminalSwitchesToInterTurnIdleTimeout(t *testing.T) {
	controlCtx, cancelControl := context.WithCancelCause(context.Background())
	defer cancelControl(context.Canceled)
	options := passthroughLifecycleConfig()
	options.WS.ReadTimeoutSeconds = 1
	options.WS.IngressInterTurnIdleTimeoutSeconds = 2
	upstream := newStagedPassthroughConn()
	upstream.Send(`{"type":"response.completed","response":{"id":"resp_idle_first","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`)
	server, serverErr := startPassthroughLifecycleServer(t, controlCtx, newPassthroughLifecycleService(options, upstream), passthroughLifecycleProvider())
	defer server.Close()
	clientConn := dialPassthroughLifecycleClient(t, server)
	defer func() { _ = clientConn.CloseNow() }()
	require.Equal(t, "response.create", gjson.GetBytes(requirePassthroughUpstreamWrite(t, upstream, 3*time.Second), "type").String())

	completed, err := readPassthroughLifecycleFrame(t, clientConn, 3*time.Second)
	require.NoError(t, err)
	require.Equal(t, "resp_idle_first", gjson.GetBytes(completed, "response.id").String())
	time.Sleep(1300 * time.Millisecond)
	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, websocket.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","previous_response_id":"resp_idle_first"}`))
	cancelWrite()
	require.NoError(t, err)
	require.Equal(t, "response.create", gjson.GetBytes(requirePassthroughUpstreamWrite(t, upstream, 3*time.Second), "type").String())
	upstream.Send(`{"type":"response.completed","response":{"id":"resp_idle_second","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`)
	completed, err = readPassthroughLifecycleFrame(t, clientConn, 3*time.Second)
	require.NoError(t, err)
	require.Equal(t, "resp_idle_second", gjson.GetBytes(completed, "response.id").String())
	_, err = readPassthroughLifecycleFrame(t, clientConn, 3*time.Second)
	var websocketCloseErr websocket.CloseError
	require.ErrorAs(t, err, &websocketCloseErr)
	require.Equal(t, websocket.StatusNormalClosure, websocketCloseErr.Code)
	require.Equal(t, "websocket idle timeout", websocketCloseErr.Reason)

	select {
	case err := <-serverErr:
		var closeErr *gatewayhttp.OpenAIWSClientCloseError
		require.ErrorAs(t, err, &closeErr)
		require.Equal(t, websocket.StatusNormalClosure, closeErr.StatusCode())
		require.Equal(t, "websocket idle timeout", closeErr.Reason())
	case <-time.After(3 * time.Second):
		t.Fatal("passthrough terminal turn did not use inter-turn idle timeout")
	}
}

func TestPassthroughLifecycle_FirstOutputTimeoutRemainsBounded(t *testing.T) {
	controlCtx, cancelControl := context.WithCancelCause(context.Background())
	defer cancelControl(context.Canceled)
	upstream := newStagedPassthroughConn()
	server, serverErr := startPassthroughLifecycleServer(t, controlCtx, newPassthroughLifecycleService(passthroughLifecycleConfig(), upstream), passthroughLifecycleProvider())
	defer server.Close()
	clientConn := dialPassthroughLifecycleClient(t, server)
	defer func() { _ = clientConn.CloseNow() }()

	select {
	case err := <-serverErr:
		var failoverErr *forwardcore.UpstreamFailoverError
		require.ErrorAs(t, err, &failoverErr)
		require.Equal(t, http.StatusGatewayTimeout, failoverErr.StatusCode)
		require.Contains(t, string(failoverErr.ResponseBody), "first_output_timeout")
	case <-time.After(2500 * time.Millisecond):
		t.Fatal("passthrough first output was left unbounded")
	}
}

func TestPassthroughLifecycle_ResponseCreatedTimeoutClosesWithoutFailover(t *testing.T) {
	controlCtx, cancelControl := context.WithCancelCause(context.Background())
	defer cancelControl(context.Canceled)
	upstream := newStagedPassthroughConn()
	upstream.Send(`{"type":"response.created","response":{"id":"resp_preamble","model":"gpt-5.1"}}`)
	server, serverErr := startPassthroughLifecycleServer(t, controlCtx, newPassthroughLifecycleService(passthroughLifecycleConfig(), upstream), passthroughLifecycleProvider())
	defer server.Close()
	clientConn := dialPassthroughLifecycleClient(t, server)
	defer func() { _ = clientConn.CloseNow() }()

	created, err := readPassthroughLifecycleFrame(t, clientConn, 3*time.Second)
	require.NoError(t, err)
	require.Equal(t, "response.created", gjson.GetBytes(created, "type").String())
	_, err = readPassthroughLifecycleFrame(t, clientConn, 2500*time.Millisecond)
	var websocketCloseErr websocket.CloseError
	require.ErrorAs(t, err, &websocketCloseErr)
	require.Equal(t, websocket.StatusGoingAway, websocketCloseErr.Code)
	require.Equal(t, "upstream produced no semantic output; please reconnect", websocketCloseErr.Reason)
	select {
	case err := <-serverErr:
		var failoverErr *forwardcore.UpstreamFailoverError
		require.NotErrorAs(t, err, &failoverErr)
		var closeErr *gatewayhttp.OpenAIWSClientCloseError
		require.ErrorAs(t, err, &closeErr)
		require.Equal(t, websocket.StatusGoingAway, closeErr.StatusCode())
		require.Equal(t, "upstream produced no semantic output; please reconnect", closeErr.Reason())
	case <-time.After(2500 * time.Millisecond):
		t.Fatal("response.created timeout did not close the passthrough connection")
	}
}

func TestPassthroughLifecycle_SecondTurnTimeoutIsNotFailoverSafe(t *testing.T) {
	controlCtx, cancelControl := context.WithCancelCause(context.Background())
	defer cancelControl(context.Canceled)
	upstream := newStagedPassthroughConn()
	upstream.Send(`{"type":"response.completed","response":{"id":"resp_first","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`)
	server, serverErr := startPassthroughLifecycleServer(t, controlCtx, newPassthroughLifecycleService(passthroughLifecycleConfig(), upstream), passthroughLifecycleProvider())
	defer server.Close()
	clientConn := dialPassthroughLifecycleClient(t, server)
	defer func() { _ = clientConn.CloseNow() }()

	completed, err := readPassthroughLifecycleFrame(t, clientConn, 3*time.Second)
	require.NoError(t, err)
	require.Equal(t, "response.completed", gjson.GetBytes(completed, "type").String())
	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, websocket.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","previous_response_id":"resp_first"}`))
	cancelWrite()
	require.NoError(t, err)
	upstream.Send(`{"type":"response.created","response":{"id":"resp_second","model":"gpt-5.1"}}`)

	created, err := readPassthroughLifecycleFrame(t, clientConn, 3*time.Second)
	require.NoError(t, err)
	require.Equal(t, "response.created", gjson.GetBytes(created, "type").String())
	_, err = readPassthroughLifecycleFrame(t, clientConn, 2500*time.Millisecond)
	var websocketCloseErr websocket.CloseError
	require.ErrorAs(t, err, &websocketCloseErr)
	require.Equal(t, websocket.StatusGoingAway, websocketCloseErr.Code)
	require.Equal(t, "upstream produced no semantic output; please reconnect", websocketCloseErr.Reason)
	select {
	case err := <-serverErr:
		var failoverErr *forwardcore.UpstreamFailoverError
		require.NotErrorAs(t, err, &failoverErr, "handler must not replay the initial request on another provider for a later-turn timeout")
		var closeErr *gatewayhttp.OpenAIWSClientCloseError
		require.ErrorAs(t, err, &closeErr)
		require.Equal(t, websocket.StatusGoingAway, closeErr.StatusCode())
	case <-time.After(2500 * time.Millisecond):
		t.Fatal("second turn first semantic output was left unbounded")
	}
}
