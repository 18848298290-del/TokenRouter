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

// anthropicHTTPUpstreamRecorder captures request state before returning
// configured transport results.
type anthropicHTTPUpstreamRecorder struct {
	// lastReq retains the exchanged request and lastBody captures its read
	// bytes. resp is handed to the executor for response cleanup when err
	// is nil; a configured err returns no response while leaving request
	// evidence recorded.
	lastReq  *http.Request  // last borrowed HTTP request
	lastBody []byte         // last captured request bytes
	resp     *http.Response // configured upstream response pointer
	err      error          // configured upstream exchange error
}

// deferredActivityRepository records concurrent batched provider activity
// updates for assertions.
type deferredActivityRepository struct {
	// updates is written by the deferred service and read by assertions
	// after StopContext drains the service; sync.Map coordinates those
	// accesses.
	updates sync.Map // provider activity observation map
}

// TestGatewayService_AnthropicAPIKeyPassthrough_ForwardDirect_NonStreamingSuccess
// returns a successful native Anthropic body with cache usage variants. The
// client body remains exact while completion observations normalize cached
// tokens and both cache creation buckets.
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

// TestGatewayService_AnthropicAPIKeyPassthrough_ForwardDirect_InvalidTokenType
// invokes API key passthrough with an OAuth provider token. The call returns no
// result and a credential-kind error identifying that passthrough requires an
// API key.
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

// TestGatewayService_AnthropicAPIKeyPassthrough_ForwardDirect_UpstreamRequestError
// fails the transport before response output for an API key provider. The
// result is nil, failover requests another provider, and the fixture writer
// remains unstarted.
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
	// The handler owns failover after transport errors, so this attempt
	// leaves the response unwritten.
	require.False(t, c.Writer.Written())
}

// TestGatewayService_AnthropicAPIKeyPassthrough_ForwardDirect_EmptyResponseBody
// returns HTTP success with a nil upstream body. Passthrough returns no
// completion result and an error reporting the empty response required by the
// exchange reader.
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

// TestGatewayService_AnthropicAPIKeyPassthrough_TransportErrorRecordsOllamaActivity
// compares an Ollama provider and another API key provider after transport
// failure. Draining deferred writes records activity for the eligible Ollama
// provider and omits the other.
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

// TestGatewayService_AnthropicAPIKeyPassthrough_ContextCanceledSkipsOllamaActivity
// fails an eligible Ollama exchange with context.Canceled. After deferred
// service drain, the activity recorder contains no update for that cancelled
// provider request.
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

// TestGatewayService_AnthropicAPIKeyPassthrough_Non2xxRecordsOllamaActivity
// returns a terminal invalid-request response for an eligible Ollama provider.
// Error handling schedules activity, which is visible after the deferred
// service has been drained.
func TestGatewayService_AnthropicAPIKeyPassthrough_Non2xxRecordsOllamaActivity(t *testing.T) {
	deferred, activity := newDeferredActivityRecorder(t)
	// The default API key policy sends a 400 to error handling instead of
	// retrying or failing over.
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

// newPrivateRuntimeFixture retains test dependencies and selects a default 128
// MiB response cap. Supplied options replace that complete default value, so
// tests install any read limit they need.
func newPrivateRuntimeFixture(options *Options, deps Dependencies, _ any) *Runtime {
	value := Options{ResponseReadLimit: 128 * 1024 * 1024}
	if options != nil {
		value = *options
	}
	return NewRuntime(deps, value)
}

// newPrivateHealthFixture constructs a health observer using default internal
// test options.
func newPrivateHealthFixture() *provideradapter.UpstreamHealth {
	return gatewaytestkit.NewHealthObserver(gatewaytestkit.HealthInput{})
}

// passthroughFixture supplies model, body, stream mode, and timing to native
// execution. The runtime validates API key credentials and returns the selected
// path observations or failure for fixture assertions.
func passthroughFixture(runtime *Runtime, ctx context.Context, output HTTPBoundary, target *gatewayprovider.ExecutionProvider, body []byte, model, original string, stream bool, started time.Time) (*forwardcore.Result, error) {
	return runtime.passthrough(ctx, output, target, forwardcore.APIKeyInput{Body: body, RequestModel: model, OriginalModel: original, RequestStream: stream, StartTime: started})
}

// newAnthropicAPIKeyProviderForTest creates a schedulable Anthropic API key
// passthrough target.
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

// Do captures the borrowed request and rebuilds its body from available fixture
// bytes. Configured errors return no response; otherwise the supplied response
// pointer is returned for execution and closure.
func (u *anthropicHTTPUpstreamRecorder) Do(req *http.Request, proxyURL string, providerID int64, providerConcurrency int) (*http.Response, error) {
	u.lastReq = req
	if req != nil && req.Body != nil {
		// Capture available fixture bytes and rebuild the request
		// reader so later wire assertions can reread the body after
		// this stub inspection.
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

// DoWithTLS delegates recording to Do and ignores the fixture TLS profile.
func (u *anthropicHTTPUpstreamRecorder) DoWithTLS(req *http.Request, proxyURL string, providerID int64, providerConcurrency int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, providerID, providerConcurrency)
}

// BatchUpdateLastUsed stores provider activity times in the concurrent fixture
// observation map.
func (r *deferredActivityRepository) BatchUpdateLastUsed(_ context.Context, updates map[int64]time.Time) error {
	for id, ts := range updates {
		r.updates.Store(id, ts)
	}
	return nil
}

// newDeferredActivityRecorder creates a timed provider activity service and its
// synchronized recorder. The returned map is observed after draining, and test
// cleanup stops the service when the test ends.
func newDeferredActivityRecorder(t *testing.T) (*providercore.DeferredService, *sync.Map) {
	t.Helper()
	wheel := timingwheel.New()
	repo := &deferredActivityRepository{}
	svc := providercore.NewDeferredService(repo, wheel, providercore.DeferredOptions{Interval: time.Second, Now: time.Now, Observe: log.Printf})
	// Register cleanup to drain the deferred service even when an assertion
	// ends early, keeping queued fixture work within the test resource
	// lifetime.
	t.Cleanup(func() { require.NoError(t, svc.StopContext(context.Background())) })
	return svc, &repo.updates
}
