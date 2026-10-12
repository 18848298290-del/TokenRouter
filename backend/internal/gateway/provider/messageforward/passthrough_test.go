package messageforward

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	gatewaytestkit "github.com/TokenFlux/TokenRouter/internal/gateway/testkit"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/tlsfingerprint"
	"github.com/TokenFlux/TokenRouter/internal/infra/timingwheel"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

type anthropicHTTPUpstreamRecorder struct {
	lastReq  *http.Request
	lastBody []byte
	resp     *http.Response
	err      error
}

// deferredActivityRepository records batched last-used updates for assertions.
type deferredActivityRepository struct {
	updates sync.Map
}

func TestGatewayService_AnthropicAPIKeyPassthrough_ForwardDirect_NonStreamingSuccess(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := newPrivateHTTPFixture(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	body := []byte(`{"model":"claude-3-5-sonnet-latest","messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`)
	upstreamJSON := `{"id":"msg_1","type":"message","usage":{"input_tokens":12,"output_tokens":7,"cache_creation":{"ephemeral_5m_input_tokens":2,"ephemeral_1h_input_tokens":3},"cached_tokens":4}}`
	upstream := &anthropicHTTPUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"application/json"},
				"x-request-id": []string{"rid-nonstream"},
			},
			Body: io.NopCloser(strings.NewReader(upstreamJSON)),
		},
	}
	svc := newPrivateRuntimeFixture(
		&Options{Configured: true, PreserveContentType: true, ResponseReadLimit: 134217728}, Dependencies{Transport: upstream, Health: newPrivateHealthFixture()}, nil,
	)

	result, err := passthroughFixture(svc, context.Background(), c, newAnthropicAPIKeyProviderForTest(), body, "claude-3-5-sonnet-latest", "claude-3-5-sonnet-latest", false, time.Now())
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 12, result.Usage.InputTokens)
	require.Equal(t, 7, result.Usage.OutputTokens)
	require.Equal(t, 5, result.Usage.CacheCreationInputTokens)
	require.Equal(t, 4, result.Usage.CacheReadInputTokens)
	require.Equal(t, upstreamJSON, rec.Body.String())
}

func TestGatewayService_AnthropicAPIKeyPassthrough_ForwardDirect_InvalidTokenType(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := newPrivateHTTPFixture(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 202,
			Name:     "anthropic-oauth",
			Platform: capability.PlatformAnthropic,
			Type:     capability.ProviderTypeOAuth,
			Credentials: map[string]any{
				"access_token": "oauth-token",
			},
		},
	}
	svc := newPrivateRuntimeFixture(nil, Dependencies{}, nil)

	result, err := passthroughFixture(svc, context.Background(), c, provider, []byte(`{}`), "claude-3-5-sonnet-latest", "claude-3-5-sonnet-latest", false, time.Now())
	require.Nil(t, result)
	require.Error(t, err)
	require.Contains(t, err.Error(), "requires apikey token")
}

func TestGatewayService_AnthropicAPIKeyPassthrough_ForwardDirect_UpstreamRequestError(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := newPrivateHTTPFixture(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	upstream := &anthropicHTTPUpstreamRecorder{
		err: errors.New("dial tcp timeout"),
	}
	svc := newPrivateRuntimeFixture(
		&Options{Configured: true, PreserveContentType: true, ResponseReadLimit: 134217728}, Dependencies{Transport: upstream}, nil,
	)
	provider := newAnthropicAPIKeyProviderForTest()

	result, err := passthroughFixture(svc, context.Background(), c, provider, []byte(`{"model":"x"}`), "x", "x", false, time.Now())
	require.Nil(t, result)
	require.Error(t, err)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.True(t, failoverErr.ShouldRetryNextProvider())
	// The handler owns failover after transport errors, so this attempt leaves the response unwritten.
	require.False(t, c.Writer.Written())
}

func TestGatewayService_AnthropicAPIKeyPassthrough_ForwardDirect_EmptyResponseBody(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := newPrivateHTTPFixture(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	upstream := &anthropicHTTPUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"x-request-id": []string{"rid-empty-body"}},
			Body:       nil,
		},
	}
	svc := newPrivateRuntimeFixture(
		&Options{Configured: true, PreserveContentType: true, ResponseReadLimit: 134217728}, Dependencies{Transport: upstream}, nil,
	)

	result, err := passthroughFixture(svc, context.Background(), c, newAnthropicAPIKeyProviderForTest(), []byte(`{"model":"x"}`), "x", "x", false, time.Now())
	require.Nil(t, result)
	require.Error(t, err)
	require.Contains(t, err.Error(), "empty response")
}

func TestGatewayService_AnthropicAPIKeyPassthrough_TransportErrorRecordsOllamaActivity(t *testing.T) {
	deferred, activity := newDeferredActivityRecorder(t)
	upstream := &anthropicHTTPUpstreamRecorder{err: errors.New("dial tcp timeout")}
	svc := newPrivateRuntimeFixture(
		&Options{Configured: true, PreserveContentType: true, ResponseReadLimit: 134217728}, Dependencies{Transport: upstream, Deferred: deferred}, nil,
	)

	ollama := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 601, Name: "ollama-anthropic", Platform: capability.PlatformAnthropic, Type: capability.ProviderTypeAPIKey,
			Concurrency: 1,
			Credentials: map[string]any{"api_key": "k-ollama", "base_url": "https://ollama.com"},
			Extra:       map[string]any{"anthropic_passthrough": true},
			Status:      billing.StatusActive, Schedulable: true,
		},
	}
	other := newAnthropicAPIKeyProviderForTest()
	other.Record.ID = 602

	rec := httptest.NewRecorder()
	c, _ := newPrivateHTTPFixture(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	_, err := passthroughFixture(svc, context.Background(), c, ollama, []byte(`{"model":"x"}`), "x", "x", false, time.Now())
	require.Error(t, err)

	rec2 := httptest.NewRecorder()
	c2, _ := newPrivateHTTPFixture(rec2)
	c2.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	_, err = passthroughFixture(svc, context.Background(), c2, other, []byte(`{"model":"x"}`), "x", "x", false, time.Now())
	require.Error(t, err)

	require.NoError(t, deferred.StopContext(context.Background()))
	_, ok := activity.Load(int64(601))
	require.True(t, ok, "Anthropic passthrough transport error on Ollama provider must record activity")
	_, ok = activity.Load(int64(602))
	require.False(t, ok, "non-Ollama Anthropic passthrough transport error must not record Ollama activity")
}

func TestGatewayService_AnthropicAPIKeyPassthrough_ContextCanceledSkipsOllamaActivity(t *testing.T) {
	deferred, activity := newDeferredActivityRecorder(t)
	upstream := &anthropicHTTPUpstreamRecorder{err: context.Canceled}
	svc := newPrivateRuntimeFixture(
		&Options{Configured: true, PreserveContentType: true, ResponseReadLimit: 134217728}, Dependencies{Transport: upstream, Deferred: deferred}, nil,
	)
	ollama := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 603, Name: "ollama-canceled", Platform: capability.PlatformAnthropic, Type: capability.ProviderTypeAPIKey,
			Concurrency: 1,
			Credentials: map[string]any{"api_key": "k-ollama", "base_url": "https://ollama.com"},
			Extra:       map[string]any{"anthropic_passthrough": true},
			Status:      billing.StatusActive, Schedulable: true,
		},
	}
	rec := httptest.NewRecorder()
	c, _ := newPrivateHTTPFixture(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	_, err := passthroughFixture(svc, context.Background(), c, ollama, []byte(`{"model":"x"}`), "x", "x", false, time.Now())

	require.Error(t, err)
	require.NoError(t, deferred.StopContext(context.Background()))
	_, ok := activity.Load(int64(603))
	require.False(t, ok, "context.Canceled on Anthropic passthrough must not count as Ollama activity")
}

func TestGatewayService_AnthropicAPIKeyPassthrough_Non2xxRecordsOllamaActivity(t *testing.T) {
	deferred, activity := newDeferredActivityRecorder(t)
	// The default API key policy sends a 400 to error handling instead of retrying or failing over.
	upstream := &anthropicHTTPUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusBadRequest,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"type":"error","error":{"type":"invalid_request_error","message":"bad"}}`)),
		},
	}
	svc := newPrivateRuntimeFixture(
		&Options{Configured: true, PreserveContentType: true, ResponseReadLimit: 134217728}, Dependencies{Transport: upstream, Health: newPrivateHealthFixture(), Deferred: deferred}, nil,
	)
	ollama := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 604, Name: "ollama-400", Platform: capability.PlatformAnthropic, Type: capability.ProviderTypeAPIKey,
			Concurrency: 1,
			Credentials: map[string]any{"api_key": "k-ollama", "base_url": "https://ollama.com"},
			Extra:       map[string]any{"anthropic_passthrough": true},
			Status:      billing.StatusActive, Schedulable: true,
		},
	}
	rec := httptest.NewRecorder()
	c, _ := newPrivateHTTPFixture(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	_, _ = passthroughFixture(svc, context.Background(), c, ollama, []byte(`{"model":"x"}`), "x", "x", false, time.Now())

	require.NoError(t, deferred.StopContext(context.Background()))
	_, ok := activity.Load(int64(604))
	require.True(t, ok, "Anthropic passthrough non-2xx on Ollama provider must record activity via handleErrorResponse")
}

func newPrivateRuntimeFixture(options *Options, deps Dependencies, _ any) *Runtime {
	value := Options{ResponseReadLimit: 128 * 1024 * 1024}
	if options != nil {
		value = *options
	}
	return NewRuntime(deps, value)
}

func newPrivateHealthFixture() *provideradapter.UpstreamHealth {
	return gatewaytestkit.NewHealthObserver(gatewaytestkit.HealthInput{})
}

// passthroughFixture builds the input and runs credential validation and the upstream exchange.
func passthroughFixture(runtime *Runtime, ctx context.Context, output HTTPBoundary, target *gatewayprovider.ExecutionProvider, body []byte, model, original string, stream bool, started time.Time) (*forwardcore.Result, error) {
	return runtime.passthrough(ctx, output, target, forwardcore.APIKeyInput{Body: body, RequestModel: model, OriginalModel: original, RequestStream: stream, StartTime: started})
}

func newAnthropicAPIKeyProviderForTest() *gatewayprovider.ExecutionProvider {
	return &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 201,
			Name:        "anthropic-apikey-pass-test",
			Platform:    capability.PlatformAnthropic,
			Type:        capability.ProviderTypeAPIKey,
			Concurrency: 1,
			Credentials: map[string]any{
				"api_key":  "upstream-anthropic-key",
				"base_url": "https://api.anthropic.com",
			},
			Extra: map[string]any{
				"anthropic_passthrough": true,
			},
			Status:      billing.StatusActive,
			Schedulable: true,
		},
	}
}

func (u *anthropicHTTPUpstreamRecorder) Do(req *http.Request, proxyURL string, providerID int64, providerConcurrency int) (*http.Response, error) {
	u.lastReq = req
	if req != nil && req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		u.lastBody = b
		_ = req.Body.Close()
		req.Body = io.NopCloser(bytes.NewReader(b))
	}
	if u.err != nil {
		return nil, u.err
	}
	return u.resp, nil
}

func (u *anthropicHTTPUpstreamRecorder) DoWithTLS(req *http.Request, proxyURL string, providerID int64, providerConcurrency int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, providerID, providerConcurrency)
}

func (r *deferredActivityRepository) BatchUpdateLastUsed(_ context.Context, updates map[int64]time.Time) error {
	for id, ts := range updates {
		r.updates.Store(id, ts)
	}
	return nil
}

func newDeferredActivityRecorder(t *testing.T) (*providercore.DeferredService, *sync.Map) {
	t.Helper()
	wheel := timingwheel.New()
	repo := &deferredActivityRepository{}
	svc := providercore.NewDeferredService(repo, wheel, providercore.DeferredOptions{Interval: time.Second, Now: time.Now, Observe: log.Printf})
	t.Cleanup(func() { require.NoError(t, svc.StopContext(context.Background())) })
	return svc, &repo.updates
}
