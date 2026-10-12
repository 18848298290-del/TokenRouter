package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	gatewaytestkit "github.com/TokenFlux/TokenRouter/internal/gateway/testkit"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// openAITransportProviderRepoStub records temporary scheduling blocks for transport-health tests.
type openAITransportProviderRepoStub struct {
	tempUnschedCalls []tempUnschedCall
}

// tempUnschedCall captures a provider scheduling-block write.
type tempUnschedCall struct {
	providerID int64
	until      time.Time
	reason     string
}

// TestClassifyUpstreamTransportError checks persistent and transient transport classifications.
// The classification distinguishes provider-blocking failures from transient retry candidates.
func TestClassifyUpstreamTransportError(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		persistent bool
	}{
		// Configuration, credential, and routing failures are classified as persistent.
		{"socks5 proxy credential rejected", errors.New(`Post "https://chatgpt.com/backend-api/codex/responses": socks connect tcp 85.255.176.68:12324->chatgpt.com:443: username/password authentication failed`), true},
		{"proxy connection refused", errors.New(`proxyconnect tcp: dial tcp 1.2.3.4:1080: connect: connection refused`), true},
		{"no route to host", errors.New(`dial tcp 1.2.3.4:443: connect: no route to host`), true},
		{"dns resolution failure", errors.New(`dial tcp: lookup proxy.example.com: no such host`), true},
		{"network unreachable", errors.New(`dial tcp 1.2.3.4:443: connect: network is unreachable`), true},

		// Transient transport failures permit switching while leaving the provider schedulable.
		{"client timeout", errors.New(`Post "https://chatgpt.com/...": context deadline exceeded (Client.Timeout exceeded while awaiting headers)`), false},
		{"i/o timeout", errors.New(`dial tcp 1.2.3.4:443: i/o timeout`), false},
		{"connection reset by peer", errors.New(`read tcp 10.0.0.1:5->2.2.2.2:443: read: connection reset by peer`), false},
		{"unexpected eof", errors.New(`unexpected EOF`), false},
		{"broken pipe", errors.New(`write tcp 10.0.0.1:5->2.2.2.2:443: write: broken pipe`), false},

		{"nil error", nil, false},

		// Typed cases cover net.OpError wrapping syscall errors.
		{
			"ECONNREFUSED via net.OpError",
			&net.OpError{
				Op:  "dial",
				Net: "tcp",
				Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED},
			},
			true,
		},
		// Bare syscall errors exercise the same errors.Is classification.
		{"ECONNREFUSED bare", syscall.ECONNREFUSED, true},
		{"EHOSTUNREACH bare", syscall.EHOSTUNREACH, true},
		{"ENETUNREACH bare", syscall.ENETUNREACH, true},

		// A DNS not-found error is classified as persistent.
		{
			"DNS not found (IsNotFound=true)",
			&net.DNSError{Err: "no such host", Name: "proxy.example.com", IsNotFound: true},
			true,
		},
		// This timeout has IsNotFound=false and remains transient.
		{
			"DNS timeout (IsNotFound=false)",
			&net.DNSError{Err: "i/o timeout", Name: "proxy.example.com", IsTimeout: true},
			false,
		},

		// Client cancellation is not a persistent transport failure.
		{"context.Canceled", context.Canceled, false},
		// A deadline expiry is not a persistent transport failure.
		{"context.DeadlineExceeded", context.DeadlineExceeded, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := httpclient.ClassifyTransportFailure(tc.err).Persistent
			if got != tc.persistent {
				t.Fatalf("httpclient.ClassifyTransportFailure(%v).Persistent = %v, want %v", tc.err, got, tc.persistent)
			}
		})
	}
}

func (r *openAITransportProviderRepoStub) SetTempUnschedulable(_ context.Context, id int64, until time.Time, reason string) error {
	r.tempUnschedCalls = append(r.tempUnschedCalls, tempUnschedCall{providerID: id, until: until, reason: reason})
	return nil
}

func newOpenAITransportErrTestContext() (*gin.Context, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	return c, rec
}

func TestHandleOpenAIUpstreamTransportError_PersistentEvictsAndFailsOver(t *testing.T) {
	repo := &openAITransportProviderRepoStub{}
	svc := transportHealthFixture(repo, nil)
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 4627, Name: "proxy-expired", Platform: capability.PlatformOpenAI}}
	c, rec := newOpenAITransportErrTestContext()

	before := time.Now()
	retErr := svc.Failure.Handle(context.Background(), c, provider,
		errors.New(`Post "https://chatgpt.com/backend-api/codex/responses": socks connect tcp 85.255.176.68:12324->chatgpt.com:443: username/password authentication failed`), false)
	after := time.Now()

	var failoverErr *forwardcore.UpstreamFailoverError
	require.True(t, errors.As(retErr, &failoverErr), "persistent error must return *UpstreamFailoverError")
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.Len(t, repo.tempUnschedCalls, 1)
	require.Equal(t, int64(4627), repo.tempUnschedCalls[0].providerID)
	require.Contains(t, repo.tempUnschedCalls[0].reason, "authentication failed")
	require.True(t, repo.tempUnschedCalls[0].until.After(before.Add((10*time.Minute)-time.Second)))
	require.True(t, repo.tempUnschedCalls[0].until.Before(after.Add((10*time.Minute)+time.Second)))
	require.True(t, svc.Failure.Health.Runtime.Blocked(provider.Record.ID, nil))
	require.Equal(t, 0, rec.Body.Len())
}

func TestHandleOpenAIUpstreamTransportError_TransientFailsOverWithoutEviction(t *testing.T) {
	repo := &openAITransportProviderRepoStub{}
	svc := transportHealthFixture(repo, nil)
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 99, Name: "flaky", Platform: capability.PlatformOpenAI}}
	c, rec := newOpenAITransportErrTestContext()

	err := svc.Failure.Handle(context.Background(), c, provider,
		errors.New(`Post "https://chatgpt.com/...": context deadline exceeded (Client.Timeout exceeded while awaiting headers)`), false)

	var failoverErr *forwardcore.UpstreamFailoverError
	require.True(t, errors.As(err, &failoverErr), "transient error must return *UpstreamFailoverError")
	require.Empty(t, repo.tempUnschedCalls)
	require.False(t, svc.Failure.Health.Runtime.Blocked(provider.Record.ID, nil))
	require.Equal(t, 0, rec.Body.Len())
}

func TestHandleOpenAIUpstreamTransportError_ContextCanceledNoFailover(t *testing.T) {
	repo := &openAITransportProviderRepoStub{}
	svc := transportHealthFixture(repo, nil)
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 77, Name: "healthy", Platform: capability.PlatformOpenAI}}
	c, rec := newOpenAITransportErrTestContext()

	err := svc.Failure.Handle(context.Background(), c, provider, context.Canceled, false)

	var failoverErr *forwardcore.UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr), "context.Canceled must not return *UpstreamFailoverError")
	require.Empty(t, repo.tempUnschedCalls)
	require.False(t, svc.Failure.Health.Runtime.Blocked(provider.Record.ID, nil))
	require.Equal(t, 0, rec.Body.Len())
}

func TestHandleOpenAIUpstreamTransportError_WrappedContextCanceledNoFailover(t *testing.T) {
	repo := &openAITransportProviderRepoStub{}
	svc := transportHealthFixture(repo, nil)
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 78, Name: "healthy2", Platform: capability.PlatformOpenAI}}
	c, _ := newOpenAITransportErrTestContext()

	err := svc.Failure.Handle(context.Background(), c, provider, fmt.Errorf("http request failed: %w", context.Canceled), false)

	var failoverErr *forwardcore.UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr), "wrapped context.Canceled must not return *UpstreamFailoverError")
	require.Empty(t, repo.tempUnschedCalls)
	require.False(t, svc.Failure.Health.Runtime.Blocked(provider.Record.ID, nil))
}

// TestHandleOpenAIUpstreamTransportError_RecordsOllamaActivityOnly checks activity recording for Ollama Cloud targets.
func TestHandleOpenAIUpstreamTransportError_RecordsOllamaActivityOnly(t *testing.T) {
	deferred, activity := gatewaytestkit.DeferredActivityRecorder(t)
	svc := transportHealthFixture(&openAITransportProviderRepoStub{}, deferred)
	ollama := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 501, Name: "ollama-cloud", Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey,
			Credentials: map[string]any{"api_key": "k-ollama", "base_url": "https://ollama.com"},
		},
	}
	other := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 502, Name: "openai-official", Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey,
			Credentials: map[string]any{"api_key": "k-openai", "base_url": "https://api.openai.com"},
		},
	}
	c, _ := newOpenAITransportErrTestContext()

	_ = svc.Failure.Handle(context.Background(), c, ollama, errors.New("connection reset"), false)
	_ = svc.Failure.Handle(context.Background(), c, other, errors.New("connection reset"), false)

	require.NoError(t, deferred.StopContext(context.Background()))
	_, ok := activity.Load(int64(501))
	require.True(t, ok, "Ollama Cloud transport error must schedule last_used activity")
	_, ok = activity.Load(int64(502))
	require.False(t, ok, "non-Ollama transport error must not schedule Ollama activity")
}

// TestHandleOpenAIUpstreamTransportError_ContextCanceledSkipsOllamaActivity checks that cancellation skips
// activity recording.
func TestHandleOpenAIUpstreamTransportError_ContextCanceledSkipsOllamaActivity(t *testing.T) {
	deferred, activity := gatewaytestkit.DeferredActivityRecorder(t)
	svc := transportHealthFixture(&openAITransportProviderRepoStub{}, deferred)
	ollama := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 503, Name: "ollama-canceled", Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey,
			Credentials: map[string]any{"api_key": "k-ollama", "base_url": "https://ollama.com"},
		},
	}
	c, _ := newOpenAITransportErrTestContext()

	err := svc.Failure.Handle(context.Background(), c, ollama, context.Canceled, false)

	require.ErrorIs(t, err, context.Canceled)
	require.NoError(t, deferred.StopContext(context.Background()))
	_, ok := activity.Load(int64(503))
	require.False(t, ok, "context.Canceled is client disconnect before a fault; do not count as Ollama activity")
}

// TestHandleOpenAIProviderUpstreamError_RecordsOllamaActivityOnly checks Ollama Cloud activity on non-2xx responses.
func TestHandleOpenAIProviderUpstreamError_RecordsOllamaActivityOnly(t *testing.T) {
	deferred, activity := gatewaytestkit.DeferredActivityRecorder(t)
	svc := transportHealthFixture(nil, deferred)
	ollama := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 504, Name: "ollama-429", Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey,
			Credentials: map[string]any{"api_key": "k-ollama", "base_url": "https://ollama.com"},
		},
	}
	other := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 505, Name: "openai-429", Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey,
			Credentials: map[string]any{"api_key": "k-openai", "base_url": "https://api.openai.com"},
		},
	}

	_ = gatewayprovider.ApplyOpenAIResponseHealth(context.Background(), svc.Output.Health, ollama, http.StatusTooManyRequests, http.Header{}, []byte(`{"error":{"message":"rate"}}`), false, "gpt-test").StopScheduling
	_ = gatewayprovider.ApplyOpenAIResponseHealth(context.Background(), svc.Output.Health, other, http.StatusTooManyRequests, http.Header{}, []byte(`{"error":{"message":"rate"}}`), false, "gpt-test").StopScheduling

	require.NoError(t, deferred.StopContext(context.Background()))
	_, ok := activity.Load(int64(504))
	require.True(t, ok, "Ollama Cloud non-2xx must schedule last_used activity")
	_, ok = activity.Load(int64(505))
	require.False(t, ok, "non-Ollama non-2xx must not schedule Ollama activity")
}

// transportHealthFixture supplies shared runtime block state and deferred activity recording.
func transportHealthFixture(store *openAITransportProviderRepoStub, deferred *providercore.DeferredService) *GrokExecutor {
	state := providercore.NewRuntimeBlockState(time.Now)
	health := &provideradapter.TransportHealth{Runtime: state, Deferred: deferred}
	if store != nil {
		health.Store = store
	}
	return &GrokExecutor{Failure: &UpstreamTransportFailure{Health: health}, Output: &OpenAIResponseOutput{Health: &provideradapter.OpenAIResponseHealth{Runtime: state, Deferred: deferred}}}
}
