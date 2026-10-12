package messageforward_test

// These scenarios cover response options, upstream exchange handling, and passthrough output.

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/provider/messageforward"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	gatewaytestkit "github.com/TokenFlux/TokenRouter/internal/gateway/testkit"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	claude "github.com/TokenFlux/TokenRouter/internal/upstream/anthropic"
)

type nonJSONTempUnschedProviderRepo struct {
	gatewayprovider.ExecutionProviderStore

	tempUnschedCalls    int
	tempReason          string
	modelRateLimitCalls int
	modelScope          string
	modelReason         string
}

type failWriteResponseWriter struct {
	gin.ResponseWriter
}

func (r *nonJSONTempUnschedProviderRepo) SetTempUnschedulable(_ context.Context, _ int64, _ time.Time, reason string) error {
	r.tempUnschedCalls++
	r.tempReason = reason
	return nil
}

func (r *nonJSONTempUnschedProviderRepo) SetModelRateLimit(_ context.Context, _ int64, scope string, _ time.Time, reason ...string) error {
	r.modelRateLimitCalls++
	r.modelScope = scope
	if len(reason) > 0 {
		r.modelReason = reason[0]
	}
	return nil
}

func TestHandleNonStreamingResponse_NonJSON2xxTriggersFailover(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	body := []byte("(upstream request failed)")
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"text/plain"},
			"X-Request-Id": []string{"rid-invalid-json"},
		},
		Body: io.NopCloser(bytes.NewReader(body)),
	}
	svc := newResponseRuntimeFixture(
		&messageforward.Options{Configured: true, PreserveContentType: true, ResponseReadLimit: 134217728}, messageforward.Dependencies{Health: newPartialHealthFixture()}, nil,
	)

	usage, err := nonStreamResponseFixture(svc, context.Background(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1}}, "claude-sonnet-4-6", "claude-sonnet-4-6")

	require.Nil(t, usage)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.True(t, errors.As(err, &failoverErr))
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.Equal(t, body, failoverErr.ResponseBody)
	require.Equal(t, "rid-invalid-json", http.Header(failoverErr.ResponseHeaders).Get("x-request-id"))
	require.False(t, c.Writer.Written(), "invalid upstream response must not be committed before failover")
}

func TestHandleNonStreamingResponse_ValidJSONUnchanged(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	body := []byte(`{"id":"msg_1","type":"message","usage":{"input_tokens":12,"output_tokens":7}}`)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(body)),
	}
	svc := newResponseRuntimeFixture(
		&messageforward.Options{Configured: true, PreserveContentType: true, ResponseReadLimit: 134217728}, messageforward.Dependencies{Health: newPartialHealthFixture()}, nil,
	)

	usage, err := nonStreamResponseFixture(svc, context.Background(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1}}, "claude-sonnet-4-6", "claude-sonnet-4-6")

	require.NoError(t, err)
	require.NotNil(t, usage)
	require.Equal(t, 12, usage.InputTokens)
	require.Equal(t, 7, usage.OutputTokens)
	require.JSONEq(t, string(body), rec.Body.String())
}

func TestHandleNonStreamingResponseAnthropicAPIKeyPassthrough_NonJSON2xxTriggersFailover(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	body := []byte("(upstream request failed)")
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/plain"}},
		Body:       io.NopCloser(bytes.NewReader(body)),
	}
	svc := newResponseRuntimeFixture(&messageforward.Options{Configured: true, PreserveContentType: true, ResponseReadLimit: 134217728}, messageforward.Dependencies{}, nil)

	usage, err := passthroughResponseFixture(svc, context.Background(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 2}})

	require.Nil(t, usage)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.True(t, errors.As(err, &failoverErr))
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.Equal(t, body, failoverErr.ResponseBody)
	require.False(t, c.Writer.Written(), "invalid passthrough response must not be committed before failover")
}

func TestHandleNonStreamingResponseAnthropicAPIKeyPassthrough_ValidJSONUnchanged(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	body := []byte(`{"id":"msg_1","type":"message","usage":{"input_tokens":5,"output_tokens":3}}`)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(body)),
	}
	svc := newResponseRuntimeFixture(&messageforward.Options{Configured: true, PreserveContentType: true, ResponseReadLimit: 134217728}, messageforward.Dependencies{}, nil)

	usage, err := passthroughResponseFixture(svc, context.Background(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 2}})

	require.NoError(t, err)
	require.NotNil(t, usage)
	require.Equal(t, 5, usage.InputTokens)
	require.Equal(t, 3, usage.OutputTokens)
	require.JSONEq(t, string(body), rec.Body.String())
}

func TestHandleNonStreamingResponseAnthropicAPIKeyPassthrough_ForceCacheBillingResponse(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "converts input tokens for downstream billing",
			body: `{"id":"msg_1","type":"message","content":[{"type":"text","text":"unchanged"}],"usage":{"input_tokens":5,"output_tokens":3}}`,
			want: `{"id":"msg_1","type":"message","content":[{"type":"text","text":"unchanged"}],"usage":{"input_tokens":0,"output_tokens":3,"cache_read_input_tokens":5}}`,
		},
		{
			name: "adds to genuine cache reads",
			body: `{"id":"msg_2","type":"message","usage":{"input_tokens":5,"output_tokens":3,"cache_read_input_tokens":7,"cache_creation_input_tokens":11}}`,
			want: `{"id":"msg_2","type":"message","usage":{"input_tokens":0,"output_tokens":3,"cache_read_input_tokens":12,"cache_creation_input_tokens":11}}`,
		},
		{
			name: "zero input leaves response unchanged",
			body: `{"id":"msg_3","type":"message","usage":{"input_tokens":0,"output_tokens":3,"cache_read_input_tokens":7}}`,
			want: `{"id":"msg_3","type":"message","usage":{"input_tokens":0,"output_tokens":3,"cache_read_input_tokens":7}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			resp := &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(bytes.NewBufferString(tt.body)),
			}
			svc := newResponseRuntimeFixture(&messageforward.Options{Configured: true, PreserveContentType: true, ResponseReadLimit: 134217728}, messageforward.Dependencies{}, nil)

			usage, err := passthroughResponseFixture(svc, requeststate.WithForceCacheBilling(context.Background()), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 2}})

			require.NoError(t, err)
			require.Equal(t, int(gjson.Get(tt.body, "usage.input_tokens").Int()), usage.InputTokens, "本地计费必须保留未归类的输入 token")
			require.Equal(t, int(gjson.Get(tt.body, "usage.cache_read_input_tokens").Int()), usage.CacheReadInputTokens, "本地计费只能在 RecordUsage 中换算一次")
			require.JSONEq(t, tt.want, rec.Body.String())
		})
	}
}

func TestHandleNonStreamingResponse_NonJSON2xxMatchesModelScopedTempUnschedulableRule(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	repo := &nonJSONTempUnschedProviderRepo{}
	healthObserver := gatewaytestkit.NewHealthObserver(gatewaytestkit.HealthInput{Store: repo, Options: providercore.HealthOptions{}})

	svc := newResponseRuntimeFixture(
		&messageforward.Options{Configured: true, PreserveContentType: true, ResponseReadLimit: 134217728}, messageforward.Dependencies{Health: healthObserver}, nil,
	)
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 3,
			Platform: capability.PlatformAnthropic,
			Type:     capability.ProviderTypeAPIKey,
			Credentials: map[string]any{
				"temp_unschedulable_enabled": true,
				"temp_unschedulable_rules": []any{
					map[string]any{
						"error_code":       float64(http.StatusBadGateway),
						"keywords":         []any{"upstream request failed"},
						"duration_minutes": float64(10),
					},
				},
			},
		},
	}
	body := []byte("(upstream request failed)")
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(bytes.NewReader(body)),
	}

	_, err := nonStreamResponseFixture(svc, context.Background(), resp, c, provider, "claude-sonnet-4-6", "claude-sonnet-4-6")

	var failoverErr *forwardcore.UpstreamFailoverError
	require.True(t, errors.As(err, &failoverErr))
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.Equal(t, body, failoverErr.ResponseBody)
	require.Zero(t, repo.tempUnschedCalls)
	require.Equal(t, 1, repo.modelRateLimitCalls)
	require.Equal(t, "claude-sonnet-4-6", repo.modelScope)
	require.Contains(t, repo.modelReason, `"status_code":502`)
	require.Contains(t, repo.modelReason, `"matched_keyword":"upstream request failed"`)
}

func TestAnthropicPriorHeartbeatPreservesReadFailureBoundary(t *testing.T) {
	svc := newStreamingRuntimeFixture(0)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Writer.Header().Set("X-Fixture", "prior")
	_, err := c.Writer.Write([]byte(": ping\n\n"))
	require.NoError(t, err)
	c.Writer.Flush()
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: &streamReadCloser{err: io.ErrUnexpectedEOF}}
	result, err := streamResponseFixture(svc, context.Background(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1}}, time.Now(), "model", "model", false)
	require.Error(t, err)
	require.NotNil(t, result)
	var failover *forwardcore.UpstreamFailoverError
	require.False(t, errors.As(err, &failover))
	require.Contains(t, rec.Body.String(), "stream_read_error")
	require.Equal(t, "prior", c.Writer.Header().Get("X-Fixture"))
}

func (w *failWriteResponseWriter) Write(data []byte) (int, error) {
	return 0, errors.New("client disconnected")
}

func (w *failWriteResponseWriter) WriteString(_ string) (int, error) {
	return 0, errors.New("client disconnected")
}

func TestGatewayService_AnthropicAPIKeyPassthrough_StreamingStillCollectsUsageAfterClientDisconnect(t *testing.T) {
	// Cancel the request context to simulate a disconnected client.
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	ctx, cancel := context.WithCancel(req.Context())
	cancel()
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	svc := newResponseRuntimeFixture(
		&messageforward.Options{Configured: true, PreserveContentType: true, ResponseReadLimit: 134217728, MaxLineSize: defaultMaxLineSize}, messageforward.Dependencies{Health: newPartialHealthFixture()}, nil,
	)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`data: {"type":"message_start","message":{"usage":{"input_tokens":11}}}`,
			"",
			`data: {"type":"message_delta","usage":{"output_tokens":5}}`,
			"",
			"data: [DONE]",
			"",
		}, "\n"))),
	}

	result, err := passthroughStreamFixture(svc, context.Background(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1}}, time.Now(), "claude-3-7-sonnet-20250219")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.Usage)
	require.Equal(t, 11, result.Usage.InputTokens)
	require.Equal(t, 5, result.Usage.OutputTokens)
}

func TestGatewayService_AnthropicAPIKeyPassthrough_MissingTerminalEventReturnsError(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	svc := newResponseRuntimeFixture(
		&messageforward.Options{Configured: true, PreserveContentType: true, ResponseReadLimit: 134217728, MaxLineSize: defaultMaxLineSize}, messageforward.Dependencies{Health: newPartialHealthFixture()}, nil,
	)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`data: {"type":"message_start","message":{"usage":{"input_tokens":11}}}`,
			"",
			`data: {"type":"message_delta","usage":{"output_tokens":5}}`,
			"",
		}, "\n"))),
	}

	result, err := passthroughStreamFixture(svc, context.Background(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1}}, time.Now(), "claude-3-7-sonnet-20250219")
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing terminal event")
	require.NotNil(t, result)
}

func TestGatewayService_AnthropicAPIKeyPassthrough_StreamingErrTooLong(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	svc := newResponseRuntimeFixture(
		&messageforward.Options{Configured: true, PreserveContentType: true, ResponseReadLimit: 134217728, MaxLineSize: 32}, messageforward.

			// Exceed the scanner's 64 KiB initial buffer to trigger bufio.ErrTooLong.
			Dependencies{}, nil,
	)

	longLine := "data: " + strings.Repeat("x", 80*1024)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(longLine)),
	}

	result, err := passthroughStreamFixture(svc, context.Background(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 2}}, time.Now(), "claude-3-7-sonnet-20250219")
	require.Error(t, err)
	require.ErrorIs(t, err, bufio.ErrTooLong)
	require.NotNil(t, result)
}

func TestGatewayService_AnthropicAPIKeyPassthrough_StreamingDataIntervalTimeout(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	svc := newResponseRuntimeFixture(
		&messageforward.Options{Configured: true, PreserveContentType: true, ResponseReadLimit: 134217728, StreamInterval: time.Duration(1) * time.Second, MaxLineSize: defaultMaxLineSize}, messageforward.Dependencies{Health: newPartialHealthFixture()}, nil,
	)

	pr, pw := io.Pipe()
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       pr,
	}

	result, err := passthroughStreamFixture(svc, context.Background(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 5}}, time.Now(), "claude-3-7-sonnet-20250219")
	_ = pw.Close()
	_ = pr.Close()

	require.Error(t, err)
	require.Contains(t, err.Error(), "stream data interval timeout")
	require.NotNil(t, result)
	require.False(t, result.ClientDisconnect)
}

func TestGatewayService_AnthropicAPIKeyPassthrough_StreamingSendsKeepaliveDuringIdle(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	svc := newResponseRuntimeFixture(
		&messageforward.Options{Configured: true, PreserveContentType: true, ResponseReadLimit: 134217728, StreamKeepalive: time.Duration(1) * time.Second, MaxLineSize: defaultMaxLineSize}, messageforward.Dependencies{Health: newPartialHealthFixture()}, nil,
	)

	pr, pw := io.Pipe()
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       pr,
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		// Delay the first upstream SSE event to check downstream ping keepalives.
		time.Sleep(1200 * time.Millisecond)
		_, _ = pw.Write([]byte(strings.Join([]string{
			`data: {"type":"message_start","message":{"usage":{"input_tokens":3}}}`,
			"",
			`data: {"type":"message_delta","usage":{"output_tokens":2}}`,
			"",
			"data: [DONE]",
			"",
		}, "\n")))
		_ = pw.Close()
	}()

	result, err := passthroughStreamFixture(svc, context.Background(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 8}}, time.Now(), "claude-3-7-sonnet-20250219")
	_ = pr.Close()
	<-done

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Contains(t, rec.Body.String(), "event: ping\ndata: {\"type\": \"ping\"}\n\n")
	require.Contains(t, rec.Body.String(), "data: [DONE]")
}

func TestGatewayService_AnthropicAPIKeyPassthrough_StreamingKeepaliveDoesNotInterleavePartialEvent(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	svc := newResponseRuntimeFixture(
		&messageforward.Options{Configured: true, PreserveContentType: true, ResponseReadLimit: 134217728, StreamKeepalive: time.Duration(1) * time.Second, MaxLineSize: defaultMaxLineSize}, messageforward.Dependencies{Health: newPartialHealthFixture()}, nil,
	)

	pr, pw := io.Pipe()
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       pr,
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		// Leave an SSE event incomplete to check that keepalives cannot split it.
		_, _ = pw.Write([]byte(`data: {"type":"message_start","message":{"usage":{"input_tokens":4}}}` + "\n"))
		time.Sleep(1200 * time.Millisecond)
		_, _ = pw.Write([]byte("\n"))
		_, _ = pw.Write([]byte("data: [DONE]\n\n"))
		_ = pw.Close()
	}()

	result, err := passthroughStreamFixture(svc, context.Background(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 9}}, time.Now(), "claude-3-7-sonnet-20250219")
	_ = pr.Close()
	<-done

	require.NoError(t, err)
	require.NotNil(t, result)
	body := rec.Body.String()
	require.NotContains(t, body, `data: {"type":"message_start","message":{"usage":{"input_tokens":4}}}`+"\n"+"event: ping")
	require.NotContains(t, body, "event: ping")
	require.Contains(t, body, "data: [DONE]")
}

func TestGatewayService_AnthropicAPIKeyPassthrough_StreamingReadError(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	svc := newResponseRuntimeFixture(
		&messageforward.Options{Configured: true, PreserveContentType: true, ResponseReadLimit: 134217728, MaxLineSize: defaultMaxLineSize}, messageforward.Dependencies{}, nil,
	)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: &streamReadCloser{
			err: io.ErrUnexpectedEOF,
		},
	}

	result, err := passthroughStreamFixture(svc, context.Background(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 6}}, time.Now(), "claude-3-7-sonnet-20250219")
	require.Error(t, err)
	require.Contains(t, err.Error(), "stream read error")
	require.NotNil(t, result)
	require.False(t, result.ClientDisconnect)
}

func TestGatewayService_AnthropicAPIKeyPassthrough_StreamingTimeoutAfterClientDisconnect(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Writer = &failWriteResponseWriter{ResponseWriter: c.Writer}

	svc := newResponseRuntimeFixture(
		&messageforward.Options{Configured: true, PreserveContentType: true, ResponseReadLimit: 134217728, StreamInterval: time.Duration(1) * time.Second, MaxLineSize: defaultMaxLineSize}, messageforward.Dependencies{Health: newPartialHealthFixture()}, nil,
	)

	pr, pw := io.Pipe()
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       pr,
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = pw.Write([]byte(`data: {"type":"message_start","message":{"usage":{"input_tokens":9}}}` + "\n"))
		// Keep the upstream connection idle to trigger the data interval timeout.
		time.Sleep(1500 * time.Millisecond)
		_ = pw.Close()
	}()

	result, err := passthroughStreamFixture(svc, context.Background(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 7}}, time.Now(), "claude-3-7-sonnet-20250219")
	_ = pr.Close()
	<-done

	require.Error(t, err)
	require.Contains(t, err.Error(), "stream usage incomplete after timeout")
	require.NotNil(t, result)
	require.True(t, result.ClientDisconnect)
	require.Equal(t, 9, result.Usage.InputTokens)
}

func TestGatewayService_AnthropicAPIKeyPassthrough_StreamingContextCanceled(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	svc := newResponseRuntimeFixture(
		&messageforward.Options{Configured: true, PreserveContentType: true, ResponseReadLimit: 134217728, MaxLineSize: defaultMaxLineSize}, messageforward.Dependencies{}, nil,
	)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: &streamReadCloser{
			err: context.Canceled,
		},
	}

	result, err := passthroughStreamFixture(svc, context.Background(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 3}}, time.Now(), "claude-3-7-sonnet-20250219")
	require.Error(t, err)
	require.Contains(t, err.Error(), "stream usage incomplete")
	require.NotNil(t, result)
	require.True(t, result.ClientDisconnect)
}

func TestGatewayService_AnthropicAPIKeyPassthrough_StreamingUpstreamReadErrorAfterClientDisconnect(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Writer = &failWriteResponseWriter{ResponseWriter: c.Writer}

	svc := newResponseRuntimeFixture(
		&messageforward.Options{Configured: true, PreserveContentType: true, ResponseReadLimit: 134217728, MaxLineSize: defaultMaxLineSize}, messageforward.Dependencies{}, nil,
	)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: &streamReadCloser{
			payload: []byte(`data: {"type":"message_start","message":{"usage":{"input_tokens":8}}}` + "\n\n"),
			err:     io.ErrUnexpectedEOF,
		},
	}

	result, err := passthroughStreamFixture(svc, context.Background(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 4}}, time.Now(), "claude-3-7-sonnet-20250219")
	require.Error(t, err)
	require.Contains(t, err.Error(), "stream usage incomplete after disconnect")
	require.NotNil(t, result)
	require.True(t, result.ClientDisconnect)
	require.Equal(t, 8, result.Usage.InputTokens)
}

// newResponseRuntimeFixture combines HTTP output, test options, and the platform response reader.
func newResponseRuntimeFixture(options *messageforward.Options, deps messageforward.Dependencies, _ *egress.CompiledHeaderFilter) *messageforward.Runtime {
	value := messageforward.Options{ResponseReadLimit: 128 * 1024 * 1024}
	if options != nil {
		value = *options
	}
	return messageforward.NewRuntime(deps, value)
}

func nonStreamResponseFixture(runtime *messageforward.Runtime, ctx context.Context, response *http.Response, c *gin.Context, target *gatewayprovider.ExecutionProvider, original, mapped string) (*upstream.TokenUsage, error) {
	boundary := gatewayhttp.NewMessageForwardBoundary(c, nil)
	options := messageforward.ResponseOptionsForTest(runtime, ctx, boundary, target, mapped, false)
	return claude.NonStreamResponse(ctx, response, upstream.NewOutputContext(boundary.Sink()), options, original, mapped)
}

func passthroughResponseFixture(runtime *messageforward.Runtime, ctx context.Context, response *http.Response, c *gin.Context, target *gatewayprovider.ExecutionProvider) (*upstream.TokenUsage, error) {
	boundary := gatewayhttp.NewMessageForwardBoundary(c, nil)
	options := messageforward.ResponseOptionsForTest(runtime, ctx, boundary, target, "", true)
	return claude.NonStreamResponsePassthrough(ctx, response, upstream.NewOutputContext(boundary.Sink()), options)
}

// newStreamingRuntimeFixture sets the keepalive interval before constructing the runtime.
func newStreamingRuntimeFixture(keepalive time.Duration) *messageforward.Runtime {
	return messageforward.NewRuntime(messageforward.Dependencies{Health: newPartialHealthFixture()}, messageforward.Options{Configured: true, MaxLineSize: defaultMaxLineSize, StreamKeepalive: keepalive})
}

func streamResponseFixture(runtime *messageforward.Runtime, ctx context.Context, response *http.Response, c *gin.Context, target *gatewayprovider.ExecutionProvider, started time.Time, original, mapped string, mimic bool) (*claude.StreamResult, error) {
	boundary := gatewayhttp.NewMessageForwardBoundary(c, nil)
	options := messageforward.ResponseOptionsForTest(runtime, ctx, boundary, target, mapped, false)
	return claude.StreamResponse(ctx, response, upstream.NewOutputContext(boundary.Sink()), options.StreamOptions, started, original, mapped, mimic)
}

func passthroughStreamFixture(runtime *messageforward.Runtime, ctx context.Context, response *http.Response, c *gin.Context, target *gatewayprovider.ExecutionProvider, started time.Time, model string) (*claude.StreamResult, error) {
	boundary := gatewayhttp.NewMessageForwardBoundary(c, nil)
	options := messageforward.ResponseOptionsForTest(runtime, ctx, boundary, target, model, true)
	return claude.StreamResponsePassthrough(ctx, response, upstream.NewOutputContext(boundary.Sink()), options.StreamOptions, started, model)
}

func TestGatewayService_StreamingReusesScannerBufferAndStillParsesUsage(t *testing.T) {
	svc := newStreamingRuntimeFixture(0)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	pr, pw := io.Pipe()
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: pr}

	go func() {
		defer func() { _ = pw.Close() }()
		// A minimal SSE event exercises usage parsing.
		_, _ = pw.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":3}}}\n\n"))
		_, _ = pw.Write([]byte("data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":7}}\n\n"))
		_, _ = pw.Write([]byte("data: [DONE]\n\n"))
	}()

	result, err := streamResponseFixture(svc, context.Background(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1}}, time.Now(), "model", "model", false)
	_ = pr.Close()
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.Usage)
	require.Equal(t, 3, result.Usage.InputTokens)
	require.Equal(t, 7, result.Usage.OutputTokens)
}

func TestGatewayService_StreamingKeepaliveUsesIdleTimer(t *testing.T) {
	svc := newStreamingRuntimeFixture(time.Second)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	pr, pw := io.Pipe()
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: pr}

	go func() {
		defer func() { _ = pw.Close() }()
		_, _ = pw.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":1}}}\n\n"))
		time.Sleep(1100 * time.Millisecond)
		_, _ = pw.Write([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	}()

	result, err := streamResponseFixture(svc, context.Background(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1}}, time.Now(), "model", "model", false)
	_ = pr.Close()
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Contains(t, rec.Body.String(), "event: ping")
}

func TestGatewayService_StreamingKeepaliveUsesNoopDeltaForAffectedClaudeCodeVersion(t *testing.T) {
	svc := newStreamingRuntimeFixture(time.Second)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("User-Agent", "claude-cli/2.1.198 (external, cli)")

	pr, pw := io.Pipe()
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: pr}

	go func() {
		defer func() { _ = pw.Close() }()
		_, _ = pw.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":1}}}\n\n"))
		_, _ = pw.Write([]byte("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"))
		time.Sleep(1100 * time.Millisecond)
		_, _ = pw.Write([]byte("event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"))
		_, _ = pw.Write([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	}()

	result, err := streamResponseFixture(svc, context.Background(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1}}, time.Now(), "model", "model", false)
	_ = pr.Close()
	require.NoError(t, err)
	require.NotNil(t, result)
	body := rec.Body.String()
	require.Contains(t, body, "event: content_block_delta")
	require.Contains(t, body, `"delta":{"type":"text_delta","text":""}`)
}

func TestGatewayService_StreamingKeepaliveUsesNoopDeltaDuringToolUseForAffectedClaudeCodeVersion(t *testing.T) {
	svc := newStreamingRuntimeFixture(time.Second)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("User-Agent", "claude-cli/2.1.198 (external, cli)")

	pr, pw := io.Pipe()
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: pr}

	go func() {
		defer func() { _ = pw.Close() }()
		_, _ = pw.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":1}}}\n\n"))
		_, _ = pw.Write([]byte("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"Edit\",\"input\":{}}}\n\n"))
		time.Sleep(1100 * time.Millisecond)
		_, _ = pw.Write([]byte("event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":1}\n\n"))
		_, _ = pw.Write([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	}()

	result, err := streamResponseFixture(svc, context.Background(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1}}, time.Now(), "model", "model", false)
	_ = pr.Close()
	require.NoError(t, err)
	require.NotNil(t, result)
	body := rec.Body.String()
	require.Contains(t, body, "event: content_block_delta")
	require.Contains(t, body, `"index":1`)
	require.Contains(t, body, `"delta":{"type":"input_json_delta","partial_json":""}`)
}

func TestGatewayService_StreamingKeepaliveKeepsPingForOlderClaudeCodeVersion(t *testing.T) {
	svc := newStreamingRuntimeFixture(time.Second)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("User-Agent", "claude-cli/2.1.187 (external, cli)")

	pr, pw := io.Pipe()
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: pr}

	go func() {
		defer func() { _ = pw.Close() }()
		_, _ = pw.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":1}}}\n\n"))
		_, _ = pw.Write([]byte("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"))
		time.Sleep(1100 * time.Millisecond)
		_, _ = pw.Write([]byte("event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"))
		_, _ = pw.Write([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	}()

	result, err := streamResponseFixture(svc, context.Background(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1}}, time.Now(), "model", "model", false)
	_ = pr.Close()
	require.NoError(t, err)
	require.NotNil(t, result)
	body := rec.Body.String()
	require.Contains(t, body, "event: ping")
	require.NotContains(t, body, `"delta":{"type":"text_delta","text":""}`)
}

func TestHandleStreamingResponse_CacheTokens(t *testing.T) {
	svc := newStreamingRuntimeFixture(0)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	pr, pw := io.Pipe()
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: pr}

	go func() {
		defer func() { _ = pw.Close() }()
		_, _ = pw.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10,\"cache_creation_input_tokens\":20,\"cache_read_input_tokens\":30}}}\n\n"))
		_, _ = pw.Write([]byte("data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":15}}\n\n"))
		_, _ = pw.Write([]byte("data: [DONE]\n\n"))
	}()

	result, err := streamResponseFixture(svc, context.Background(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1}}, time.Now(), "model", "model", false)
	_ = pr.Close()
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.Usage)
	require.Equal(t, 10, result.Usage.InputTokens)
	require.Equal(t, 15, result.Usage.OutputTokens)
	require.Equal(t, 20, result.Usage.CacheCreationInputTokens)
	require.Equal(t, 30, result.Usage.CacheReadInputTokens)
}

func TestHandleStreamingResponse_EmptyStream(t *testing.T) {
	svc := newStreamingRuntimeFixture(0)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	pr, pw := io.Pipe()
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: pr}

	go func() {
		// Close an empty stream.
		_ = pw.Close()
	}()

	result, err := streamResponseFixture(svc, context.Background(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1}}, time.Now(), "model", "model", false)
	_ = pr.Close()
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing terminal event")
	require.NotNil(t, result)
}

func TestHandleStreamingResponse_SpecialCharactersInJSON(t *testing.T) {
	svc := newStreamingRuntimeFixture(0)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	pr, pw := io.Pipe()
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: pr}

	go func() {
		defer func() { _ = pw.Close() }()
		// Exercise quotes, newlines, and Unicode inside content_block_delta.
		_, _ = pw.Write([]byte("data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello \\\"world\\\"\\n你好\"}}\n\n"))
		_, _ = pw.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":5}}}\n\n"))
		_, _ = pw.Write([]byte("data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":3}}\n\n"))
		_, _ = pw.Write([]byte("data: [DONE]\n\n"))
	}()

	result, err := streamResponseFixture(svc, context.Background(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1}}, time.Now(), "model", "model", false)
	_ = pr.Close()
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.Usage)
	require.Equal(t, 5, result.Usage.InputTokens)
	require.Equal(t, 3, result.Usage.OutputTokens)

	body := rec.Body.String()
	require.Contains(t, body, "content_block_delta", "响应应包含转发的 SSE 事件")
}

// TestHandleStreamingResponse_StreamReadErrorBeforeOutput_TriggersFailover checks unexpected EOF before client output.
// The typed error lets the outer handler retry or select another provider.
func TestHandleStreamingResponse_StreamReadErrorBeforeOutput_TriggersFailover(t *testing.T) {
	svc := newStreamingRuntimeFixture(0)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       &streamReadCloser{err: io.ErrUnexpectedEOF},
	}

	result, err := streamResponseFixture(svc, context.Background(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1}}, time.Now(), "model", "model", false)

	require.Error(t, err)
	require.Nil(t, result, "失败移交场景下不应返回 streamingResult")

	var failoverErr *forwardcore.UpstreamFailoverError
	require.True(t, errors.As(err, &failoverErr), "未输出过字节时 stream read error 必须包成 UpstreamFailoverError，期望: %v", err)
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.True(t, failoverErr.RetryableOnSameProvider, "GOAWAY 类错误应允许同提供商重试")

	// The failover body uses the Anthropic error format so exhaustion handling and Ops
	// can extract error.message and identify error.type as upstream_disconnected.
	extractedMsg := upstream.ExtractErrorMessage(failoverErr.ResponseBody)
	require.NotEmpty(t, extractedMsg, "ExtractUpstreamErrorMessage 必须从 ResponseBody 取到非空 message，否则 ops 日志会丢失诊断信息")
	require.Contains(t, extractedMsg, "upstream stream disconnected")
	require.Contains(t, string(failoverErr.ResponseBody), `"type":"error"`)
	require.Contains(t, string(failoverErr.ResponseBody), `"upstream_disconnected"`)

	// The handler decides the final response after failover; this attempt emits no stream error.
	require.NotContains(t, rec.Body.String(), "stream_read_error")
}

// TestHandleStreamingResponse_StreamReadErrorAfterOutput_PassesThrough checks read failures after client output starts.
// The response ends with a stream_read_error event.
func TestHandleStreamingResponse_StreamReadErrorAfterOutput_PassesThrough(t *testing.T) {
	svc := newStreamingRuntimeFixture(0)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	// The first read supplies a complete SSE event; the second returns unexpected EOF.
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: &streamReadCloser{
			payload: []byte("data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":5}}}\n\n"),
			err:     io.ErrUnexpectedEOF,
		},
	}

	result, err := streamResponseFixture(svc, context.Background(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1}}, time.Now(), "model", "model", false)

	require.Error(t, err)
	require.Contains(t, err.Error(), "stream read error", "已开始流后应透传普通 stream read error")
	require.NotNil(t, result, "透传场景下应返回已收集的 streamingResult")

	// Once output starts, a read error cannot request failover.
	var failoverErr *forwardcore.UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr), "已经向客户端写过字节时不能再 failover")

	// The Anthropic SSE error includes a machine-readable stream_read_error type
	// and a diagnostic message for clients to display.
	body := rec.Body.String()
	require.Contains(t, body, "event: error\n", "必须按 Anthropic SSE 标准发送 error 事件帧")
	require.Contains(t, body, `"type":"error"`, "data 必须含 type:error 顶层字段（Anthropic 标准）")
	require.Contains(t, body, `"stream_read_error"`, "error.type 必须为 stream_read_error")
	require.Contains(t, body, "upstream stream disconnected", "error.message 必须包含具体根因，Claude Code 等客户端才能显示有效错误文案")
}

// TestHandleStreamingResponse_FailoverBodyDoesNotLeakAddresses checks redaction of internal and upstream addresses from net.OpError.
func TestHandleStreamingResponse_FailoverBodyDoesNotLeakAddresses(t *testing.T) {
	svc := newStreamingRuntimeFixture(0)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	src, _ := net.ResolveTCPAddr("tcp", "10.0.0.1:54321")
	dst, _ := net.ResolveTCPAddr("tcp", "52.1.2.3:443")
	netErr := &net.OpError{
		Op:     "read",
		Net:    "tcp",
		Source: src,
		Addr:   dst,
		Err:    syscall.ECONNRESET,
	}

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       &streamReadCloser{err: netErr},
	}

	_, err := streamResponseFixture(svc, context.Background(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1}}, time.Now(), "model", "model", false)
	require.Error(t, err)

	var failoverErr *forwardcore.UpstreamFailoverError
	require.True(t, errors.As(err, &failoverErr))

	body := string(failoverErr.ResponseBody)
	require.NotContains(t, body, "10.0.0.1", "failover ResponseBody 不得泄露内部源 IP")
	require.NotContains(t, body, "54321")
	require.NotContains(t, body, "52.1.2.3", "failover ResponseBody 不得泄露上游 IP")
	require.NotContains(t, body, "443")
	// Keep the diagnostic cause after redacting addresses.
	require.Contains(t, body, "connection reset by peer")
	require.Contains(t, body, "upstream stream disconnected")
}

// TestHandleStreamingResponse_SSEErrorEvent_ReturnsTypedErrorWithRawData checks raw data from an error event in an HTTP 200 stream.
// Forward uses that data for the failover response body and Ops diagnostics.
func TestHandleStreamingResponse_SSEErrorEvent_ReturnsTypedErrorWithRawData(t *testing.T) {
	svc := newStreamingRuntimeFixture(0)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	const errorJSON = `{"type":"error","error":{"type":"overloaded_error","message":"Anthropic upstream is overloaded"}}`

	pr, pw := io.Pipe()
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: pr}

	go func() {
		defer func() { _ = pw.Close() }()
		_, _ = pw.Write([]byte("event: error\ndata: " + errorJSON + "\n\n"))
	}()

	result, err := streamResponseFixture(svc, context.Background(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1}}, time.Now(), "model", "model", false)
	_ = pr.Close()

	require.Error(t, err)
	require.Nil(t, result)

	var sseErr *claude.StreamErrorEventError
	require.True(t, errors.As(err, &sseErr), "SSE event:error 必须包成 *sseStreamErrorEventError，期望: %v", err)
	require.Equal(t, errorJSON, sseErr.RawData)
	require.Equal(t, "have error in stream", err.Error())
	require.Equal(t, "Anthropic upstream is overloaded", upstream.ExtractErrorMessage([]byte(sseErr.RawData)))
}

// TestHandleStreamingResponse_SSEErrorEvent_EmptyDataLine checks typed errors for events without data.
func TestHandleStreamingResponse_SSEErrorEvent_EmptyDataLine(t *testing.T) {
	svc := newStreamingRuntimeFixture(0)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	pr, pw := io.Pipe()
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: pr}

	go func() {
		defer func() { _ = pw.Close() }()
		_, _ = pw.Write([]byte("event: error\n\n"))
	}()

	_, err := streamResponseFixture(svc, context.Background(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1}}, time.Now(), "model", "model", false)
	_ = pr.Close()

	require.Error(t, err)
	var sseErr *claude.StreamErrorEventError
	require.True(t, errors.As(err, &sseErr), "即使 data 行为空，也必须返回 typed error")
	require.Equal(t, "", sseErr.RawData)
}

// TestHandleStreamingResponse_SSEErrorEvent_AfterPartialStreamOutput checks that raw error data survives partial output.
// The handler stops failover after the client response has started.
func TestHandleStreamingResponse_SSEErrorEvent_AfterPartialStreamOutput(t *testing.T) {
	svc := newStreamingRuntimeFixture(0)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	const errorJSON = `{"type":"error","error":{"type":"rate_limit_error","message":"Rate limited"}}`

	pr, pw := io.Pipe()
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: pr}

	go func() {
		defer func() { _ = pw.Close() }()
		_, _ = pw.Write([]byte(`data: {"type":"message_start","message":{"usage":{"input_tokens":5}}}` + "\n\n"))
		_, _ = pw.Write([]byte("event: error\ndata: " + errorJSON + "\n\n"))
	}()

	_, err := streamResponseFixture(svc, context.Background(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1}}, time.Now(), "model", "model", false)
	_ = pr.Close()

	require.Error(t, err)
	var sseErr *claude.StreamErrorEventError
	require.True(t, errors.As(err, &sseErr), "已发数据后再来的 SSE event:error 必须仍包成 typed error，期望: %v", err)
	require.Equal(t, errorJSON, sseErr.RawData)
	require.Greater(t, rec.Body.Len(), 0, "message_start 应被转发到客户端")
	require.Contains(t, rec.Body.String(), "message_start")
}

// TestHandleStreamingResponse_SSEErrorEvent_NonJSONDataLine checks that RawData accepts non-JSON errors.
func TestHandleStreamingResponse_SSEErrorEvent_NonJSONDataLine(t *testing.T) {
	svc := newStreamingRuntimeFixture(0)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	pr, pw := io.Pipe()
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: pr}

	go func() {
		defer func() { _ = pw.Close() }()
		_, _ = pw.Write([]byte("event: error\ndata: not-a-json-payload\n\n"))
	}()

	_, err := streamResponseFixture(svc, context.Background(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1}}, time.Now(), "model", "model", false)
	_ = pr.Close()

	require.Error(t, err)
	var sseErr *claude.StreamErrorEventError
	require.True(t, errors.As(err, &sseErr))
	require.Equal(t, "not-a-json-payload", sseErr.RawData)
	require.NotPanics(t, func() {
		_ = upstream.ExtractErrorMessage([]byte(sseErr.RawData))
	})
	require.Equal(t, "", upstream.ExtractErrorMessage([]byte(sseErr.RawData)))
}
