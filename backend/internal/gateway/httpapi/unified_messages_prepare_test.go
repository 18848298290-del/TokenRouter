package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	keyhttp "github.com/TokenFlux/TokenRouter/internal/apikey/httpapi"
	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/provider/messageforward"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// nativeQueueProbe records serialization, throttle and release observations
// with an injected error.
type nativeQueueProbe struct {
	// acquired counts serialization calls; throttled counts spacing calls for the
	// configured fixture.
	acquired, throttled int
	released            atomic.Int32 // atomic serial release count
	err                 error        // injected queue operation failure
}

// nativeBedrockPolicy supplies the compatibility setting used by native
// preparation tests.
type nativeBedrockPolicy struct{}

// AcquireWithWait counts serialization, marks ping commitment and returns a
// counted release. It also returns the injected error, allowing preparation
// tests to observe any callback retained alongside diagnostic queue failure.
func (q *nativeQueueProbe) AcquireWithWait(_ *gin.Context, _ int64, _ int, _ bool, started *bool, _ time.Duration, _ *zap.Logger) (func(), error) {
	q.acquired++
	*started = true
	return func() { q.released.Add(1) }, q.err
}

// ThrottleWithPing counts spacing requests, marks stream state and returns the
// injected error. It obtains no release callback, so tests can distinguish
// throttle observation from serialization ownership.
func (q *nativeQueueProbe) ThrottleWithPing(_ *gin.Context, _ int64, _ int, _ bool, started *bool, _ time.Duration, _ *zap.Logger) error {
	q.throttled++
	*started = true
	return q.err
}

// nativePrepareContext constructs a Messages request using the supplied
// context.
func nativePrepareContext(ctx context.Context) (*gin.Context, *httptest.ResponseRecorder) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil).WithContext(ctx)
	return c, recorder
}

// TestUnifiedWarmupStopsBeforeQueueAndUpstream supplies a recognized native
// warmup probe with an explicit execution deadline. It requires local synthetic
// output, no queue admission and retention of that deadline in Gin request
// context.
func TestUnifiedWarmupStopsBeforeQueueAndUpstream(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	c, rec := nativePrepareContext(context.Background())
	queue := &nativeQueueProbe{}
	executor := &UnifiedTextExecutor{MessageQueue: queue, MessageQueueMode: "serialize"}
	target := gatewayadapter.NewExecutionProvider(&provider.Record{ID: 1, Platform: "anthropic", Type: "oauth", Credentials: map[string]any{"intercept_warmup_requests": true}})
	result, err := executor.Messages(ctx, c, target, []byte(`{"model":"claude-sonnet-4-5","max_tokens":20,"messages":[{"role":"user","content":[{"type":"text","text":"Warmup"}]}]}`), "", "")
	require.NoError(t, err)
	require.Nil(t, result)
	require.True(t, NativeMessageIntercepted(c))
	require.Contains(t, rec.Body.String(), "New Conversation")
	require.Zero(t, queue.acquired)
	deadline, _ := ctx.Deadline()
	retained, ok := c.Request.Context().Deadline()
	require.True(t, ok)
	require.Equal(t, deadline, retained)
}

// TestUnifiedMessageQueueOwnsOneRelease drives upstream acceptance and
// cancellation against the same queue callback. It requires one underlying
// release despite repeated outer cleanup, shared ping state and removal of the
// parsed acceptance callback.
func TestUnifiedMessageQueueOwnsOneRelease(t *testing.T) {
	for _, cancelBeforeAccept := range []bool{false, true} {
		t.Run(map[bool]string{false: "accepted", true: "canceled"}[cancelBeforeAccept], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c, _ := nativePrepareContext(ctx)
			queue := &nativeQueueProbe{}
			executor := &UnifiedTextExecutor{MessageQueue: queue, MessageQueueMode: "serialize", MessageQueueWait: time.Second}
			target := gatewayadapter.NewExecutionProvider(&provider.Record{ID: 1, Platform: "anthropic", Type: "oauth", Extra: map[string]any{"base_rpm": 10}})
			streamStarted := false
			BindNativeMessageStreamState(c, &streamStarted)
			parsed, release, intercepted, err := executor.prepareNativeMessages(c, target, []byte(`{"model":"claude-sonnet-4-5","stream":true,"messages":[{"role":"user","content":"hello"}]}`))
			require.NoError(t, err)
			require.False(t, intercepted)
			require.Equal(t, 1, queue.acquired)
			require.True(t, streamStarted)
			require.NotNil(t, parsed.OnUpstreamAccepted)
			// Select cancellation or upstream acceptance before repeating outer cleanup,
			// demonstrating that those independently triggered paths converge on one
			// counted release.
			if cancelBeforeAccept {
				cancel()
				require.Eventually(t, func() bool { return queue.released.Load() == 1 }, time.Second, time.Millisecond)
			} else {
				parsed.OnUpstreamAccepted()
			}
			release()
			release()
			cancel()
			require.Equal(t, int32(1), queue.released.Load())
			require.Nil(t, parsed.OnUpstreamAccepted)
		})
	}
}

// TestUnifiedMessageThrottleAndToolResults configures provider throttle mode
// with an injected spacing failure. Preparation must continue and skip queueing
// a later tool-result-only message while retaining a valid cleanup function.
func TestUnifiedMessageThrottleAndToolResults(t *testing.T) {
	c, _ := nativePrepareContext(context.Background())
	queue := &nativeQueueProbe{err: errors.New("queue unavailable")}
	executor := &UnifiedTextExecutor{MessageQueue: queue, MessageQueueMode: "serialize"}
	target := gatewayadapter.NewExecutionProvider(&provider.Record{ID: 1, Platform: "anthropic", Type: "oauth", Extra: map[string]any{"user_msg_queue_mode": "throttle"}})
	parsed, release, _, err := executor.prepareNativeMessages(c, target, []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hello"}]}`))
	require.NoError(t, err, "消息队列故障保留既有fail-open行为")
	require.Equal(t, 1, queue.throttled)
	require.Nil(t, parsed.OnUpstreamAccepted)
	release()
	_, release, _, err = executor.prepareNativeMessages(c, target, []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-1","content":"done"}]}]}`))
	require.NoError(t, err)
	release()
	require.Equal(t, 1, queue.throttled, "工具结果不进入真实用户消息队列")
}

// GetGroupPolicy returns enabled Bedrock Claude-Code compatibility for every
// group lookup.
func (nativeBedrockPolicy) GetGroupPolicy(context.Context, int64) (*routing.GroupPolicyView, error) {
	return &routing.GroupPolicyView{GroupRoutingPolicy: routing.GroupRoutingPolicy{Enabled: true, FeaturesConfig: map[string]any{"bedrock_cc_compat": map[string]any{"anthropic": true}}}}, nil
}

// TestUnifiedMessagePreparationKeepsBedrockCompatibility supplies native
// Claude-Code headers and unsupported Bedrock fields. It requires removal of
// service_tier, context_management and the beta header, and checks the installed
// Bedrock Anthropic version.
func TestUnifiedMessagePreparationKeepsBedrockCompatibility(t *testing.T) {
	c, _ := nativePrepareContext(context.Background())
	groupID := int64(7)
	c.Set(string(keyhttp.ContextKeyAPIKey), &apikey.APIKey{ID: 1, GroupID: &groupID})
	c.Request.Header.Set("anthropic-beta", "claude-code-20250219")
	messages := NewMessagesExecutor(messageforward.NewRuntime(messageforward.Dependencies{GroupPolicies: nativeBedrockPolicy{}}, messageforward.Options{}), nil)
	executor := &UnifiedTextExecutor{Anthropic: messages}
	target := gatewayadapter.NewExecutionProvider(&provider.Record{ID: 1, Platform: "anthropic", Type: "bedrock"})
	parsed, release, _, err := executor.prepareNativeMessages(c, target, []byte(`{"model":"claude-sonnet-4-5","service_tier":"auto","interface_geo":"us","context_management":{},"messages":[{"role":"user","content":"hello"}]}`))
	require.NoError(t, err)
	defer release()
	require.False(t, gjson.GetBytes(parsed.Body.Bytes(), "service_tier").Exists())
	require.False(t, gjson.GetBytes(parsed.Body.Bytes(), "context_management").Exists())
	require.Equal(t, "bedrock-2023-05-31", gjson.GetBytes(parsed.Body.Bytes(), "anthropic_version").String())
	require.Empty(t, c.GetHeader("anthropic-beta"))
}
