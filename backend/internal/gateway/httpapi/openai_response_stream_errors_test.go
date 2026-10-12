package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	gatewaytestkit "github.com/TokenFlux/TokenRouter/internal/gateway/testkit"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

type openAIAuthPolicyProviderRepo struct {
	gatewayprovider.ExecutionProviderStore

	tempCalls     int
	setErrorCalls int
}

type openAIAuthPolicy403Counter struct {
	counts []int64
}

type capacityShedProviderRepoStub struct {
	gatewayprovider.ExecutionProviderStore
	// Embedding the interface makes unexpected calls to unimplemented methods panic.

	tempUnschedCalls int
}

// Cooldown uses the same store stub after conversion reads the fixture.
type capacityRetryStore struct{ *capacityShedProviderRepoStub }

type oauth429RateLimitRepo struct {
	gatewaytestkit.HealthStoreBase
	setRateLimitedCalls       int
	lastRateLimitedUntil      time.Time
	setModelRateLimitCalls    int
	lastModelRateLimitKey     string
	lastModelRateLimitedUntil time.Time
}

type rateLimit429ProviderRepoStub struct {
	gatewaytestkit.HealthStoreBase
	rateLimitCalls     int
	lastRateLimitID    int64
	lastRateLimitReset time.Time
}

func TestOpenAIHandleErrorResponsePassthrough_InvalidRequest400PassesThrough(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	svc := newResponseOutputForTest(OpenAIResponseOptions{})
	respBody := []byte(`{"error":{"message":"Invalid property name in input arguments","type":"invalid_request_error","param":"input[35].arguments","code":"property_name_above_max_length"}}`)
	resp := &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     http.Header{"Content-Type": {"application/json"}},
	}
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 12, Name: "openai", Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}}

	err := svc.PassthroughError(context.Background(), resp, c, provider, []byte(`{"model":"gpt-5.5"}`), respBody)

	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.JSONEq(t, string(respBody), rec.Body.String())
	require.Contains(t, rec.Body.String(), "property_name_above_max_length")
	require.Contains(t, rec.Body.String(), "input[35].arguments")
}

// wsFixtureModelBlocked reads model blocking through the provider health interface.
func wsFixtureModelBlocked(s *wsExecutionFixture, value *gatewayprovider.ExecutionProvider, model string) bool {
	if value == nil {
		return false
	}
	key := providercore.NormalizeTransientModel(gatewayprovider.ExecutionModelPolicy(value).CanonicalSchedulingModel(model))
	return s.Output.Health.ModelTransient.IsBlocked(value.Record.ID, key, time.Now())
}

func wsFixtureProviderBlocked(s *wsExecutionFixture, value *gatewayprovider.ExecutionProvider) bool {
	if value == nil || (value.Record.Platform != capability.PlatformOpenAI && value.Record.Platform != capability.PlatformGrok) {
		return false
	}
	return s.Output.Health.Runtime.Blocked(value.Record.ID, func() string { return providercore.RefreshCredentialIdentity(gatewayprovider.ExecutionRecord(value)) })
}

func wsFixtureBlockProvider(s *wsExecutionFixture, value *gatewayprovider.ExecutionProvider, until time.Time, reason string) {
	if value == nil || (value.Record.Platform != capability.PlatformOpenAI && value.Record.Platform != capability.PlatformGrok) {
		return
	}
	s.Output.Health.Runtime.Block(value.Record.ID, until, reason)
}

func wsFixtureRetry429(s *wsExecutionFixture, value *gatewayprovider.ExecutionProvider, headers http.Header, body []byte) bool {
	return provideradapter.CanRetryOpenAI429(s.Output.Health.Runtime, gatewayprovider.ExecutionRecord(value), headers, body)
}

func wsFixtureRequestBlocked(s *wsExecutionFixture, value *gatewayprovider.ExecutionProvider, model string) bool {
	return wsFixtureProviderBlocked(s, value) || wsFixtureModelBlocked(s, value, model)
}

func (r *openAIAuthPolicyProviderRepo) SetTempUnschedulable(context.Context, int64, time.Time, string) error {
	r.tempCalls++
	return nil
}

func (r *openAIAuthPolicyProviderRepo) SetError(context.Context, int64, string) error {
	r.setErrorCalls++
	return nil
}

func (s *openAIAuthPolicy403Counter) IncrementOpenAI403Count(context.Context, int64, int) (int64, error) {
	if len(s.counts) == 0 {
		return 1, nil
	}
	count := s.counts[0]
	s.counts = s.counts[1:]
	return count, nil
}

func (*openAIAuthPolicy403Counter) ResetOpenAI403Count(context.Context, int64) error {
	return nil
}

func TestOpenAIHTTPAccessStateBadRequestDoesNotDisableProvider(t *testing.T) {
	repo := &openAIStream403ProviderRepo{}
	svc := newWSFixture(wsFixtureInputs{health: newUpstreamHealthForTest(repo, nil, nil, providercore.HealthOptions{}, nil)})
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 925, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, Status: billing.StatusActive, Schedulable: true}}
	body := []byte(`{"error":{"code":"unknown_parameter","message":"Unknown parameter: account disabled"}}`)

	disabled := gatewayprovider.ApplyOpenAIResponseHealth(context.Background(), svc.Output.Health, provider, http.StatusBadRequest, nil, body, false).StopScheduling

	require.False(t, disabled)
	require.Zero(t, repo.setErrorCalls)
	require.False(t, wsFixtureProviderBlocked(svc, provider))
}

func TestOpenAIStreamEchoedAccessStateMessageDoesNotDisableOrFailover(t *testing.T) {
	repo := &openAIStream403ProviderRepo{}
	svc := newWSFixture(wsFixtureInputs{health: newUpstreamHealthForTest(repo, nil, nil, providercore.HealthOptions{}, nil)})
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 926, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, Status: billing.StatusActive, Schedulable: true}}
	payload := []byte(`{"type":"response.failed","response":{"error":{"type":"invalid_request_error","code":"unknown_parameter","message":"Unknown parameter: account disabled"}}}`)
	message := openai.ExtractOpenAISSEErrorMessage(payload)

	require.False(t, gatewayprovider.IsOpenAIUpstreamAccessStateError(message, payload))
	require.False(t, openai.OpenAIStreamFailedEventShouldFailover(payload, message))
	status, disabled := svc.Output.TerminalProviderEffects(nil, provider, payload, message, nil)
	require.Equal(t, http.StatusBadGateway, status)
	require.False(t, disabled)
	require.Zero(t, repo.setErrorCalls)
	require.False(t, wsFixtureProviderBlocked(svc, provider))
}

func TestOpenAIHTTPAccessStateTrustsStructuredCode(t *testing.T) {
	repo := &openAIStream403ProviderRepo{}
	svc := newWSFixture(wsFixtureInputs{health: newUpstreamHealthForTest(repo, nil, nil, providercore.HealthOptions{}, nil)})
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 930, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, Status: billing.StatusActive, Schedulable: true}}
	body := []byte(`{"error":{"code":"organization_deactivated","message":"request rejected"}}`)

	require.True(t, gatewayprovider.IsOpenAIHTTPUpstreamAccessStateError(http.StatusBadRequest, "", body))
	require.True(t, gatewayprovider.ShouldFailoverOpenAIResponse(http.StatusBadRequest, "", body))
	require.True(t, gatewayprovider.ApplyOpenAIResponseHealth(context.Background(), svc.Output.Health, provider, http.StatusBadRequest, nil, body, false).StopScheduling)
	require.Equal(t, 1, repo.setErrorCalls)
	require.True(t, wsFixtureProviderBlocked(svc, provider))
}

func TestOpenAIHTTPAuthMessagesUseExistingStatusPolicies(t *testing.T) {
	t.Run("oauth 401 remains recoverable", func(t *testing.T) {
		repo := &openAIAuthPolicyProviderRepo{}
		rateLimits := newUpstreamHealthForTest(repo, &wsFixtureOptions{}, nil, providercore.HealthOptions{}, nil)

		svc := newWSFixture(wsFixtureInputs{health: rateLimits})
		provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 931, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, Status: billing.StatusActive, Schedulable: true,
			Credentials: map[string]any{"refresh_token": "refreshable"},
		}}
		body := []byte(`{"error":{"message":"provider is disabled"}}`)

		require.False(t, gatewayprovider.IsOpenAIHTTPUpstreamAccessStateError(http.StatusUnauthorized, "", body))
		require.True(t, gatewayprovider.ApplyOpenAIResponseHealth(context.Background(), svc.Output.Health, provider, http.StatusUnauthorized, nil, body, false).StopScheduling)
		require.Zero(t, repo.setErrorCalls)
		require.Equal(t, 1, repo.tempCalls)
	})

	t.Run("403 uses counter cooldown", func(t *testing.T) {
		repo := &openAIAuthPolicyProviderRepo{}
		counter := &openAIAuthPolicy403Counter{counts: []int64{1}}
		var svc *wsExecutionFixture

		rateLimits := newUpstreamHealthForTest(repo, &wsFixtureOptions{}, nil, providercore.HealthOptions{ForbiddenCounter: counter, Block: func(v *providercore.Record, until time.Time, reason string) {
			wsFixtureBlockProvider(svc, gatewayprovider.NewExecutionProvider(v), until, reason)
		}}, nil)

		svc = newWSFixture(wsFixtureInputs{health: rateLimits})
		rateLimits.Limits.RetryOpenAI = func(v *providercore.Record, h http.Header, body []byte) bool {
			return wsFixtureRetry429(svc, gatewayprovider.NewExecutionProvider(v), h, body)
		}

		provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 932, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, Status: billing.StatusActive, Schedulable: true}}
		body := []byte(`{"error":{"message":"workspace has been suspended"}}`)

		require.False(t, gatewayprovider.IsOpenAIHTTPUpstreamAccessStateError(http.StatusForbidden, "", body))
		require.True(t, gatewayprovider.ApplyOpenAIResponseHealth(context.Background(), svc.Output.Health, provider, http.StatusForbidden, nil, body, false).StopScheduling)
		require.Zero(t, repo.setErrorCalls)
		require.Equal(t, 1, repo.tempCalls)
	})
}

func TestOpenAICapacityFailoverCarriesSafeTerminalResponse(t *testing.T) {
	message := "Our servers are currently overloaded. Please try again later."
	body := []byte(`{"error":{"code":"server_is_overloaded","message":"` + message + `"}}`)
	err := gatewayprovider.NewOpenAIUpstreamFailure(http.StatusBadRequest, nil, body, message, false)

	require.True(t, gatewayprovider.IsOpenAICapacityShed(err))
	require.Equal(t, http.StatusServiceUnavailable, err.ClientStatusCode)
	require.Equal(t, message, err.ClientMessage)
	require.NotContains(t, err.ClientMessage, "server_is_overloaded")
}

func TestOpenAIStreamSemanticStatusesPreservedAcrossTerminalShapes(t *testing.T) {
	tests := []struct {
		name         string
		body         string
		status       int
		wantFailover bool
	}{
		{"unauthorized", `{"type":"error","error":{"type":"authentication_error","code":"invalid_api_key","message":"unauthorized"}}`, http.StatusUnauthorized, true},
		{"forbidden", `{"type":"response.failed","response":{"error":{"type":"permission_error","message":"forbidden"}}}`, http.StatusForbidden, false},
		{"rate_limit", `{"error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"slow down"}}`, http.StatusTooManyRequests, true},
		{"overload_529", `{"type":"error","error":{"status_code":529,"code":"overloaded","message":"overloaded"}}`, 529, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := []byte(tt.body)
			message := openai.ExtractOpenAISSEErrorMessage(payload)
			require.Equal(t, tt.status, openai.OpenAIStreamFailureStatus(payload, message))
			require.Equal(t, tt.wantFailover, openai.OpenAIStreamErrorEventShouldFailover(payload, message))
		})
	}
}

func TestOpenAIStreamBareErrorUsesSemanticFailover(t *testing.T) {
	payload := []byte(`{"error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"slow down"}}`)
	require.True(t, openai.OpenAIStreamErrorEventShouldFailover(payload, "slow down"))
}

func TestOpenAIStream403FailoverRequiresStructuredProviderCredentialSignal(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    bool
	}{
		{
			name:    "ordinary permission error",
			payload: `{"type":"response.failed","response":{"error":{"type":"permission_error","code":"forbidden","message":"access denied for this request"}}}`,
		},
		{
			name:    "explicit 403 request status",
			payload: `{"type":"error","error":{"type":"permission_error","code":"forbidden","status_code":403,"message":"forbidden content"}}`,
		},
		{
			name:    "structured access state",
			payload: `{"type":"response.failed","response":{"error":{"code":"workspace_suspended","message":"workspace is suspended"}}}`,
			want:    true,
		},
		{
			name:    "explicit credential auth code",
			payload: `{"type":"error","error":{"type":"permission_error","code":"invalid_api_key","status_code":403,"message":"credential rejected"}}`,
			want:    true,
		},
		{
			name:    "explicit authentication type",
			payload: `{"type":"error","error":{"type":"authentication_error","status_code":403,"message":"credential rejected"}}`,
			want:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := []byte(tt.payload)
			message := openai.ExtractOpenAISSEErrorMessage(payload)
			require.Equal(t, http.StatusForbidden, openai.OpenAIStreamFailureStatus(payload, message))
			require.Equal(t, tt.want, openai.OpenAIStreamFailedEventShouldFailover(payload, message))
			require.Equal(t, tt.want, openai.OpenAIStreamErrorEventShouldFailover(payload, message))
		})
	}
}

func TestOpenAIStream403PostOutputProviderSideEffectsIgnoreRequestPermissionErrors(t *testing.T) {
	repo := &openAIStream403ProviderRepo{}
	rateLimits := newUpstreamHealthForTest(repo, nil, nil, providercore.HealthOptions{}, nil)

	svc := newWSFixture(wsFixtureInputs{health: rateLimits})
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 918, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}}
	payload := []byte(`{"type":"error","error":{"type":"permission_error","code":"forbidden","status_code":403,"message":"access denied for this request"}}`)

	status, disabled := svc.Output.TerminalProviderEffects(nil, provider, payload, "access denied for this request", nil)

	require.Equal(t, http.StatusForbidden, status)
	require.False(t, disabled)
	require.Zero(t, repo.setErrorCalls)
	require.False(t, wsFixtureProviderBlocked(svc, provider))
}

func TestOpenAIStream403ExplicitCredentialAuthAppliesProviderSideEffects(t *testing.T) {
	repo := &openAIStream403ProviderRepo{}
	rateLimits := newUpstreamHealthForTest(repo, nil, nil, providercore.HealthOptions{}, nil)

	svc := newWSFixture(wsFixtureInputs{health: rateLimits})
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 917, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}}
	payload := []byte(`{"type":"error","error":{"type":"permission_error","code":"invalid_api_key","status_code":403,"message":"credential rejected"}}`)

	status, disabled := svc.Output.TerminalProviderEffects(nil, provider, payload, "credential rejected", nil)

	require.Equal(t, http.StatusForbidden, status)
	require.True(t, disabled)
	require.Equal(t, 1, repo.setErrorCalls)
	require.True(t, wsFixtureProviderBlocked(svc, provider))
}

func TestOpenAIStreamAccessStateAppliesProviderHealthBeforeFailover(t *testing.T) {
	svc := newWSFixture(wsFixtureInputs{})
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 919, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeSetupToken}}
	payload := []byte(`{"type":"response.failed","response":{"error":{"code":"workspace_suspended","message":"workspace is suspended"}}}`)

	status, disabled := svc.Output.TerminalProviderEffects(nil, provider, payload, "workspace is suspended", nil)

	require.Equal(t, http.StatusForbidden, status)
	require.True(t, disabled)
	require.True(t, wsFixtureProviderBlocked(svc, provider))
}

func TestOpenAIStreamOAuthLike429GetsDeadlineWithoutImmediateRuntimeBlock(t *testing.T) {
	for _, providerType := range []string{capability.ProviderTypeOAuth, capability.ProviderTypeSetupToken} {
		t.Run(providerType, func(t *testing.T) {
			svc := newWSFixture(wsFixtureInputs{})
			provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 920, Platform: capability.PlatformOpenAI, Type: providerType}}
			payload := []byte(`{"type":"error","error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"slow down"}}`)
			status, disabled := svc.Output.TerminalProviderEffects(nil, provider, payload, "slow down", nil)
			err := (gatewayprovider.OpenAIFailoverPolicy{Health: svc.Output.Health}).NewProviderFailure(provider, status, nil, payload, "slow down", disabled, false)

			require.Equal(t, http.StatusTooManyRequests, status)
			require.False(t, disabled)
			require.True(t, err.RetryableOnSameProvider)
			require.False(t, err.SameProviderRetryDeadline.IsZero())
			require.False(t, wsFixtureProviderBlocked(svc, provider))
		})
	}
}

func (r *capacityShedProviderRepoStub) SetTempUnschedulable(_ context.Context, _ int64, _ time.Time, _ string) error {
	r.tempUnschedCalls++
	return nil
}

func (r *capacityShedProviderRepoStub) GetByID(_ context.Context, id int64) (*gatewayprovider.ExecutionProvider, error) {
	return &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: id, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}}, nil
}

// TestStreamFailedEventCapacityShedRetriesOnSameProvider checks bounded local retries for non-pool providers
// because switching does not resolve capacity shedding.
func TestStreamFailedEventCapacityShedRetriesOnSameProvider(t *testing.T) {
	nonPool := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}}

	for _, code := range []string{"server_is_overloaded", "slow_down"} {
		payload := []byte(`{"type":"response.failed","response":{"error":{"code":"` + code + `"}}}`)
		require.True(t, openai.IsOpenAIUpstreamCapacityShedEvent(payload), code)
		require.True(t, gatewayprovider.OpenAIStreamFailureRetryable(nonPool, payload, "overloaded"), code)
	}

	// Non-capacity failed events do not request same-provider retries outside pool mode.
	other := []byte(`{"type":"response.failed","response":{"error":{"code":"server_error"}}}`)
	require.False(t, openai.IsOpenAIUpstreamCapacityShedEvent(other))
	require.False(t, gatewayprovider.OpenAIStreamFailureRetryable(nonPool, other, "boom"))
}

func TestOpenAIHTTPCapacityShedIsRequestScopedForOAuthProviders(t *testing.T) {
	payload := []byte(`{"error":{"type":"server_error","message":"Our servers are currently overloaded. Please try again later."}}`)
	failoverErr := gatewayprovider.NewOpenAIUpstreamFailure(
		http.StatusBadRequest,
		http.Header{"X-Request-Id": []string{"rid-http-capacity"}},
		payload,
		"Our servers are currently overloaded. Please try again later.",
		false,
	)

	require.True(t, failoverErr.RetryableOnSameProvider)
	require.True(t, failoverErr.RequestScopedTransient)

	repo := &capacityShedProviderRepoStub{}
	providercore.NewRetryCooldown(capacityRetryStore{repo}, providercore.RetryCooldownOptions{}).Apply(context.Background(), providercore.RetryCooldownInput{ProviderID: 1, Status: failoverErr.StatusCode, Retryable: failoverErr.RetryableOnSameProvider, RequestScopedTransient: failoverErr.RequestScopedTransient})
	require.Zero(t, repo.tempUnschedCalls)

	healthObserver := newHTTPHealthFixture(repo, &responsesFixtureOptions{}, nil, providercore.HealthOptions{}, nil)

	gateway := newResponsesFixture(responsesFixtureInputs{health: healthObserver})
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}}
	require.False(t, gatewayprovider.ApplyOpenAIResponseHealth(context.Background(), gateway.Output.Health, provider, http.StatusBadRequest, nil, payload, false, "gpt-5").StopScheduling)
	require.Zero(t, repo.tempUnschedCalls)
}

// TestOpenAIStreamErrorFrameDoesNotStartClientOutput checks buffering of capacity error frames until
// response.failed.
// Flushing error early sets clientOutputStarted and prevents pre-output failover.
func TestOpenAIStreamErrorFrameDoesNotStartClientOutput(t *testing.T) {
	cases := []struct {
		data      string
		eventType string
		want      bool
	}{
		{`{"type":"error","error":{"code":"server_is_overloaded","message":"overloaded"}}`, "error", false},
		{`{"type":"error","error":{"code":"slow_down","message":"slow down"}}`, "error", false},
		{`{"type":"error","error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"limited"}}`, "error", false},
		// Non-retryable frames retain upstream error details and pass through.
		{`{"type":"error","error":{"type":"invalid_request_error","code":"content_policy_violation","message":"blocked"}}`, "error", true},
		{`{"type":"response.failed","response":{"error":{"code":"server_is_overloaded"}}}`, "response.failed", false},
		{`{"type":"response.created","response":{"id":"resp_1"}}`, "response.created", false},
		{`{"type":"response.in_progress","response":{"id":"resp_1"}}`, "response.in_progress", false},
		{`{"type":"response.output_item.added","item":{"type":"reasoning","summary":[]}}`, "response.output_item.added", false},
		{`{"type":"response.output_item.added","item":{"type":"reasoning","encrypted_content":"ciphertext"}}`, "response.output_item.added", true},
		{`{"type":"response.reasoning_summary_part.added","part":{"type":"summary_text","text":""}}`, "response.reasoning_summary_part.added", false},
		{`{"type":"response.reasoning_summary_part.added","part":{"type":"summary_text","text":"thinking"}}`, "response.reasoning_summary_part.added", true},
		{`{"type":"response.content_part.added","part":{"type":"output_text","text":""}}`, "response.content_part.added", false},
		{`{"type":"response.output_text.delta","delta":"hi"}`, "response.output_text.delta", true},
		{`[DONE]`, "", true},
	}
	for _, tc := range cases {
		require.Equal(t, tc.want, openai.OpenAIStreamDataStartsClientOutput(tc.data, tc.eventType), "data=%s type=%s", tc.data, tc.eventType)
	}
}

// TestSanitizeOpenAICapacityShedErrorCodeForClient checks capacity-code rewriting and preservation of other
// codes.
// Clients use codes such as rate_limit_exceeded to interpret retry delays.
func TestSanitizeOpenAICapacityShedErrorCodeForClient(t *testing.T) {
	cases := []struct {
		name        string
		payload     string
		wantChanged bool
		wantContain string
	}{
		{
			name:        "failed事件嵌套code改写",
			payload:     `{"type":"response.failed","response":{"error":{"code":"server_is_overloaded","message":"overloaded"}}}`,
			wantChanged: true,
			wantContain: `"code":"server_error"`,
		},
		{
			name:        "error帧裸code改写",
			payload:     `{"type":"error","error":{"code":"slow_down","message":"slow down"}}`,
			wantChanged: true,
			wantContain: `"code":"server_error"`,
		},
		{
			name:        "failed事件只有过载文案时补充code",
			payload:     `{"type":"response.failed","response":{"error":{"message":"Our servers are currently overloaded. Please try again later."}}}`,
			wantChanged: true,
			wantContain: `"code":"server_error"`,
		},
		{
			name:        "error帧只有过载文案时补充code",
			payload:     `{"type":"error","error":{"message":"Server is overloaded. Please try again later."}}`,
			wantChanged: true,
			wantContain: `"code":"server_error"`,
		},
		{
			name:        "rate_limit不改写",
			payload:     `{"type":"response.failed","response":{"error":{"code":"rate_limit_exceeded","message":"try again in 3s"}}}`,
			wantChanged: false,
			wantContain: `"code":"rate_limit_exceeded"`,
		},
		{
			name:        "普通server_error不改写",
			payload:     `{"type":"response.failed","response":{"error":{"code":"server_error","message":"boom"}}}`,
			wantChanged: false,
			wantContain: `"code":"server_error"`,
		},
		{
			name:        "非JSON不改写",
			payload:     `not-json`,
			wantChanged: false,
			wantContain: `not-json`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, changed := openai.SanitizeOpenAICapacityShedErrorCodeForClient([]byte(tc.payload))
			require.Equal(t, tc.wantChanged, changed)
			require.Contains(t, string(out), tc.wantContain)
			if changed {
				require.NotContains(t, string(out), "server_is_overloaded")
				require.NotContains(t, string(out), "slow_down")
			}
		})
	}
}

func (s capacityRetryStore) GetByID(ctx context.Context, id int64) (*providercore.Record, error) {
	value, err := s.capacityShedProviderRepoStub.GetByID(ctx, id)
	return gatewayprovider.ExecutionRecord(value), err
}

func TestApplyOpenAIStreamFailedErrorPassthroughRule_UsesProvidedPlatform(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	bindStatusCodePassthroughRule(c, capability.PlatformGrok, http.StatusBadRequest, "context_length_exceeded", http.StatusBadRequest)
	payload := []byte(`{"type":"response.failed","response":{"status":"failed","error":{"code":"context_length_exceeded","type":"invalid_request_error","message":"input exceeds the context window"}}}`)

	status, _, _, matched := ApplyOpenAIStreamFailedErrorRule(
		c,
		capability.PlatformGrok,
		payload,
		"input exceeds the context window",
	)

	require.True(t, matched)
	require.Equal(t, http.StatusBadRequest, status)
}

func TestNonStreamingTerminalFailureFailover_NilProviderProposesNothing(t *testing.T) {
	c, _ := newNonStreamingFailoverContext(t)
	svc := newNonStreamingFailoverService()
	payload := []byte(`{"type":"response.failed","error":{"message":"Selected model is at capacity. Please try a different model."}}`)

	require.Nil(t, svc.nonStreamingTerminalFailure(
		c, newNonStreamingSSEResponse(), nil, false, "response.failed", payload,
		"Selected model is at capacity. Please try a different model."))
}

func (r *oauth429RateLimitRepo) SetRateLimited(_ context.Context, _ int64, until time.Time) error {
	r.setRateLimitedCalls++
	r.lastRateLimitedUntil = until
	return nil
}

func (r *oauth429RateLimitRepo) SetModelRateLimit(_ context.Context, _ int64, scope string, until time.Time, _ ...string) error {
	r.setModelRateLimitCalls++
	r.lastModelRateLimitKey = scope
	r.lastModelRateLimitedUntil = until
	return nil
}

func TestOpenAI429FastPath_KeepsOAuthProviderSchedulableDuringRetryWindow(t *testing.T) {
	repo := &oauth429RateLimitRepo{}
	var svc *OpenAIResponsesExecutor

	rateLimits := newHTTPHealthFixture(repo, &responsesFixtureOptions{}, nil, providercore.HealthOptions{Block: func(v *providercore.Record, until time.Time, reason string) {
		svc.Output.Health.Runtime.BlockProviderScheduling(v, until, reason)
	}}, nil)

	svc = newResponsesFixture(responsesFixtureInputs{health: rateLimits})
	rateLimits.Limits.RetryOpenAI = func(v *providercore.Record, h http.Header, body []byte) bool {
		return provideradapter.CanRetryOpenAI429(svc.Output.Health.Runtime, v, h, body)
	}

	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 42, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}}
	apiKeyProvider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 43, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}
	setupTokenProvider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 44, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeSetupToken}}
	grokOAuthProvider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 45, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth}}

	shouldDisable := gatewayprovider.ApplyOpenAIResponseHealth(context.Background(), svc.Output.Health, provider, http.StatusTooManyRequests, http.Header{}, nil, false).StopScheduling
	apiKeyShouldDisable := gatewayprovider.ApplyOpenAIResponseHealth(context.Background(), svc.Output.Health, apiKeyProvider, http.StatusTooManyRequests, http.Header{}, nil, false).StopScheduling

	require.False(t, shouldDisable)
	require.False(t, apiKeyShouldDisable)
	require.False(t, httpFixtureRuntimeBlocked(svc, provider))
	require.True(t, httpFixtureRuntimeBlocked(svc, apiKeyProvider), "API-key 429 keeps the existing scheduler cooldown behavior")
	require.Equal(t, 1, repo.setRateLimitedCalls, "only the API-key 429 should persist a scheduler block")
	require.True(t, provideradapter.CanRetryOpenAI429(svc.Output.Health.Runtime, provider.View(), nil, nil))
	require.True(t, provideradapter.CanRetryOpenAI429(svc.Output.Health.Runtime, setupTokenProvider.View(), nil, nil))
	require.False(t, provideradapter.CanRetryOpenAI429(svc.Output.Health.Runtime, apiKeyProvider.View(), nil, nil))
	require.False(t, provideradapter.CanRetryOpenAI429(svc.Output.Health.Runtime, grokOAuthProvider.View(), nil, nil))
	require.WithinDuration(t, time.Now().Add(providercore.RuntimeRetryWindow), svc.Output.Health.RetryDeadline(provider.View()), time.Second)
	require.WithinDuration(t, time.Now().Add(providercore.RuntimeRetryWindow), svc.Output.Health.RetryDeadline(setupTokenProvider.View()), time.Second)
}

func TestOpenAI429FastPath_BlocksOAuthImmediatelyWhenSevenDayQuotaIsExhausted(t *testing.T) {
	repo := &oauth429RateLimitRepo{}
	var svc *OpenAIResponsesExecutor

	rateLimits := newHTTPHealthFixture(repo, &responsesFixtureOptions{}, nil, providercore.HealthOptions{Block: func(v *providercore.Record, until time.Time, reason string) {
		svc.Output.Health.Runtime.BlockProviderScheduling(v, until, reason)
	}}, nil)

	svc = newResponsesFixture(responsesFixtureInputs{health: rateLimits})
	rateLimits.Limits.RetryOpenAI = func(v *providercore.Record, h http.Header, body []byte) bool {
		return provideradapter.CanRetryOpenAI429(svc.Output.Health.Runtime, v, h, body)
	}

	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 423, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}}
	headers := http.Header{}
	headers.Set("x-codex-primary-used-percent", "100")
	headers.Set("x-codex-primary-reset-after-seconds", "604800")
	headers.Set("x-codex-primary-window-minutes", "10080")
	headers.Set("x-codex-secondary-used-percent", "20")
	headers.Set("x-codex-secondary-reset-after-seconds", "3600")
	headers.Set("x-codex-secondary-window-minutes", "300")

	shouldDisable := gatewayprovider.ApplyOpenAIResponseHealth(context.Background(), svc.Output.Health, provider, http.StatusTooManyRequests, headers, []byte(`{"error":{"type":"rate_limit_error","code":"rate_limit_exceeded"}}`), false).StopScheduling

	require.False(t, shouldDisable)
	require.True(t, httpFixtureRuntimeBlocked(svc, provider))
	require.Equal(t, 1, repo.setRateLimitedCalls)
	require.Greater(t, time.Until(repo.lastRateLimitedUntil), 6*24*time.Hour)
	require.False(t, provideradapter.CanRetryOpenAI429(svc.Output.Health.Runtime, provider.View(), headers, nil))
}

func TestOpenAI429FastPath_RetriesOAuthWhenNoQuotaSignalExists(t *testing.T) {
	svc := newResponsesFixture(responsesFixtureInputs{})
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 424, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}}
	headers := http.Header{"Retry-After": []string{"1"}}

	require.True(t, provideradapter.CanRetryOpenAI429(svc.Output.Health.Runtime, provider.View(), headers, []byte(`{"error":{"type":"rate_limit_error","message":"try again"}}`)))
	require.False(t, httpFixtureRuntimeBlocked(svc, provider))
}

func TestOpenAIStream429IgnoresSuccessfulQuotaSnapshotHeaders(t *testing.T) {
	repo := &oauth429RateLimitRepo{}
	var svc *OpenAIResponsesExecutor

	rateLimits := newHTTPHealthFixture(repo, &responsesFixtureOptions{}, nil, providercore.HealthOptions{Block: func(v *providercore.Record, until time.Time, reason string) {
		svc.Output.Health.Runtime.BlockProviderScheduling(v, until, reason)
	}}, nil)

	svc = newResponsesFixture(responsesFixtureInputs{health: rateLimits})
	rateLimits.Limits.RetryOpenAI = func(v *providercore.Record, h http.Header, body []byte) bool {
		return provideradapter.CanRetryOpenAI429(svc.Output.Health.Runtime, v, h, body)
	}

	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 421, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}}
	clock := expireRuntimeRetryForTest(svc, provider.Record.ID)
	headers := http.Header{}
	headers.Set("x-codex-primary-used-percent", "37")
	headers.Set("x-codex-primary-reset-after-seconds", "604800")
	headers.Set("x-codex-primary-window-minutes", "10080")
	payload := []byte(`{"type":"error","error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"slow down"}}`)

	status, disabled := svc.Output.TerminalProviderEffects(nil, provider, payload, "slow down", headers)

	require.Equal(t, http.StatusTooManyRequests, status)
	require.False(t, disabled)
	require.True(t, httpFixtureRuntimeBlocked(svc, provider))
	clock.Set(time.Now().Add(time.Minute))
	require.False(t, httpFixtureRuntimeBlocked(svc, provider), "流内 429 不得继承七天快照")
	if !repo.lastRateLimitedUntil.IsZero() {
		require.Less(t, time.Until(repo.lastRateLimitedUntil), time.Minute)
	}
}

func TestOpenAI429FastPath_SparkQuotaOnlyBlocksSparkModel(t *testing.T) {
	repo := &oauth429RateLimitRepo{}
	var svc *OpenAIResponsesExecutor

	rateLimits := newHTTPHealthFixture(repo, &responsesFixtureOptions{}, nil, providercore.HealthOptions{Block: func(v *providercore.Record, until time.Time, reason string) {
		svc.Output.Health.Runtime.BlockProviderScheduling(v, until, reason)
	}}, nil)

	svc = newResponsesFixture(responsesFixtureInputs{health: rateLimits})
	rateLimits.Limits.RetryOpenAI = func(v *providercore.Record, h http.Header, body []byte) bool {
		return provideradapter.CanRetryOpenAI429(svc.Output.Health.Runtime, v, h, body)
	}

	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 425, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}}
	headers := http.Header{}
	headers.Set("x-codex-primary-used-percent", "100")
	headers.Set("x-codex-primary-reset-after-seconds", "604800")
	headers.Set("x-codex-primary-window-minutes", "10080")
	headers.Set("x-codex-secondary-used-percent", "20")
	headers.Set("x-codex-secondary-reset-after-seconds", "3600")
	headers.Set("x-codex-secondary-window-minutes", "300")

	shouldDisable := gatewayprovider.ApplyOpenAIResponseHealth(context.Background(), svc.Output.Health, provider, http.StatusTooManyRequests, headers, []byte(`{"error":{"type":"rate_limit_error","code":"rate_limit_exceeded"}}`), false, "gpt-5.3-codex-spark").StopScheduling

	require.False(t, shouldDisable)
	require.False(t, httpFixtureRuntimeBlocked(svc, provider))
	require.Zero(t, repo.setRateLimitedCalls)
	require.Equal(t, 1, repo.setModelRateLimitCalls)
	require.Equal(t, "gpt-5.3-codex-spark", repo.lastModelRateLimitKey)
	require.Greater(t, time.Until(repo.lastModelRateLimitedUntil), 6*24*time.Hour)
}

func TestOpenAIStreamFailover_Spark429KeepsModelScope(t *testing.T) {
	repo := &oauth429RateLimitRepo{}
	var svc *OpenAIResponsesExecutor

	rateLimits := newHTTPHealthFixture(repo, &responsesFixtureOptions{}, nil, providercore.HealthOptions{Block: func(v *providercore.Record, until time.Time, reason string) {
		svc.Output.Health.Runtime.BlockProviderScheduling(v, until, reason)
	}}, nil)

	svc = newResponsesFixture(responsesFixtureInputs{health: rateLimits})
	rateLimits.Limits.RetryOpenAI = func(v *providercore.Record, h http.Header, body []byte) bool {
		return provideradapter.CanRetryOpenAI429(svc.Output.Health.Runtime, v, h, body)
	}

	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 432, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}}
	headers := http.Header{}
	headers.Set("x-codex-primary-used-percent", "100")
	headers.Set("x-codex-primary-reset-after-seconds", "604800")
	headers.Set("x-codex-primary-window-minutes", "10080")
	headers.Set("x-codex-secondary-used-percent", "20")
	headers.Set("x-codex-secondary-reset-after-seconds", "3600")
	headers.Set("x-codex-secondary-window-minutes", "300")
	payload := []byte(`{"type":"error","error":{"type":"rate_limit_error","code":"rate_limit_exceeded"}}`)

	failoverErr := svc.Output.NewStreamFailureWithModel(
		nil, provider, false, "", payload, "quota exhausted", "gpt-5.3-codex-spark", headers,
	)

	require.Equal(t, http.StatusTooManyRequests, failoverErr.StatusCode)
	require.Zero(t, repo.setRateLimitedCalls)
	require.Equal(t, 1, repo.setModelRateLimitCalls)
	require.Equal(t, "gpt-5.3-codex-spark", repo.lastModelRateLimitKey)
	require.False(t, httpFixtureRuntimeBlocked(svc, provider))
}

func TestOpenAI429FastPath_OpenCodeGoUsageLimitUsesMessageResetDuration(t *testing.T) {
	repo := &rateLimit429ProviderRepoStub{}
	var svc *OpenAIResponsesExecutor

	healthObserver := newHTTPHealthFixture(repo, &responsesFixtureOptions{}, nil, providercore.HealthOptions{Block: func(v *providercore.Record, until time.Time, reason string) {
		svc.Output.Health.Runtime.BlockProviderScheduling(v, until, reason)
	}}, nil)

	svc = newResponsesFixture(responsesFixtureInputs{health: healthObserver})
	healthObserver.Limits.RetryOpenAI = func(v *providercore.Record, h http.Header, body []byte) bool {
		return provideradapter.CanRetryOpenAI429(svc.Output.Health.Runtime, v, h, body)
	}

	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 44, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}
	body := []byte(`{"type":"error","error":{"type":"GoUsageLimitError","message":"5-hour usage limit reached. Resets in 4hr 59min. To continue using this model now, enable usage from your available balance: https://opencode.ai/workspace/wrk_test/go"},"metadata":{"workspace":"wrk_test","limitName":"5 hour"}}`)

	before := time.Now()
	shouldDisable := gatewayprovider.ApplyOpenAIResponseHealth(context.Background(), svc.Output.Health, provider, http.StatusTooManyRequests, http.Header{}, body, false).StopScheduling
	after := time.Now()

	require.False(t, shouldDisable)
	require.Equal(t, 1, repo.rateLimitCalls)
	require.Equal(t, provider.Record.ID, repo.lastRateLimitID)
	expectedResetAfter := 4*time.Hour + 59*time.Minute
	require.False(t, repo.lastRateLimitReset.Before(before.Add(expectedResetAfter-time.Second)))
	require.False(t, repo.lastRateLimitReset.After(after.Add(expectedResetAfter)))
	require.True(t, httpFixtureRuntimeBlocked(svc, provider))
}

func TestOpenAIRuntimeBlock_AppliesToOpenAIAPIKeyWhenRateLimitServiceStopsScheduling(t *testing.T) {
	svc := newResponsesFixture(responsesFixtureInputs{})
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 44, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}

	svc.Output.Health.Runtime.BlockProviderScheduling(provider.View(), time.Time{}, "custom_error_code")

	require.True(t, httpFixtureRuntimeBlocked(svc, provider))
}

func TestOpenAIRuntimeBlock_DoesNotApplyToOtherPlatforms(t *testing.T) {
	svc := newResponsesFixture(responsesFixtureInputs{})
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 45, Platform: capability.PlatformGemini, Type: capability.ProviderTypeOAuth}}

	svc.Output.Health.Runtime.BlockProviderScheduling(provider.View(), time.Time{}, "custom_error_code")

	require.False(t, httpFixtureRuntimeBlocked(svc, provider))
}

func TestOpenAIRuntimeBlocker_IgnoresNonOpenAIFromRateLimitService(t *testing.T) {
	gateway := newResponsesFixture(responsesFixtureInputs{})
	repo := &gatewaytestkit.HealthStoreRecorder{}

	healthObserver := newHTTPHealthFixture(repo, &responsesFixtureOptions{}, nil, providercore.HealthOptions{Block: func(v *providercore.Record, until time.Time, reason string) {
		gateway.Output.Health.Runtime.BlockProviderScheduling(v, until, reason)
	}}, nil)
	healthObserver.Limits.RetryOpenAI = func(v *providercore.Record, h http.Header, body []byte) bool {
		return provideradapter.CanRetryOpenAI429(gateway.Output.Health.Runtime, v, h, body)
	}

	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 45, Platform: capability.PlatformGemini, Type: capability.ProviderTypeOAuth}}

	shouldDisable := gatewayprovider.ApplyExecutionHealth(context.Background(), healthObserver, provider, gatewayprovider.HealthObservationFromContext(context.Background(), http.StatusForbidden, http.Header{}, []byte("forbidden"), nil)).StopScheduling

	require.True(t, shouldDisable)
	require.False(t, httpFixtureRuntimeBlocked(gateway, provider))
}

func TestOpenAIPoolModeRetryable5xx_DoesNotCreateModelTransientBlock(t *testing.T) {
	repo := &gatewaytestkit.ErrorPolicyStore{}
	healthObserver := newHTTPHealthFixture(repo, &responsesFixtureOptions{}, nil, providercore.HealthOptions{}, nil)

	gateway := newResponsesFixture(responsesFixtureInputs{health: healthObserver})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 47,
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeAPIKey,
			Credentials: map[string]any{
				"pool_mode":                    true,
				"pool_mode_retry_status_codes": []any{float64(524)},
			},
		},
	}

	for range 2 {
		shouldDisable := gatewayprovider.ApplyOpenAIResponseHealth(context.Background(), gateway.Output.Health, provider, 524, http.Header{}, []byte(`{"error":{"message":"upstream timeout"}}`), false, "gpt-5.4").StopScheduling
		require.False(t, shouldDisable)
	}

	require.False(t, (httpFixtureRuntimeBlocked(gateway, provider) || gateway.Output.Health.ModelTransient.IsBlocked(provider.Record.ID, providercore.NormalizeTransientModel(gatewayprovider.ExecutionModelPolicy(provider).CanonicalSchedulingModel("gpt-5.4")), time.Now())))
}

func TestOpenAIPoolModeNonRetryable5xx_DoesNotCreateModelTransientBlock(t *testing.T) {
	repo := &gatewaytestkit.ErrorPolicyStore{}
	healthObserver := newHTTPHealthFixture(repo, &responsesFixtureOptions{}, nil, providercore.HealthOptions{}, nil)

	gateway := newResponsesFixture(responsesFixtureInputs{health: healthObserver})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 48,
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeAPIKey,
			Credentials: map[string]any{
				"pool_mode":                    true,
				"pool_mode_retry_status_codes": []any{float64(http.StatusGatewayTimeout)},
			},
		},
	}

	for range 2 {
		shouldDisable := gatewayprovider.ApplyOpenAIResponseHealth(context.Background(), gateway.Output.Health, provider, http.StatusServiceUnavailable, http.Header{}, []byte(`{"error":{"message":"upstream unavailable"}}`), false, "gpt-5.4").StopScheduling
		require.False(t, shouldDisable)
	}

	require.False(t, (httpFixtureRuntimeBlocked(gateway, provider) || gateway.Output.Health.ModelTransient.IsBlocked(provider.Record.ID, providercore.NormalizeTransientModel(gatewayprovider.ExecutionModelPolicy(provider).CanonicalSchedulingModel("gpt-5.4")), time.Now())))
}

func TestOpenAINonPoolAPIKey5xx_StillCreatesModelTransientBlock(t *testing.T) {
	repo := &gatewaytestkit.ErrorPolicyStore{}
	healthObserver := newHTTPHealthFixture(repo, &responsesFixtureOptions{}, nil, providercore.HealthOptions{}, nil)

	gateway := newResponsesFixture(responsesFixtureInputs{health: healthObserver})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 49,
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeAPIKey,
		},
	}

	for range 2 {
		shouldDisable := gatewayprovider.ApplyOpenAIResponseHealth(context.Background(), gateway.Output.Health, provider, http.StatusGatewayTimeout, http.Header{}, []byte(`{"error":{"message":"upstream timeout"}}`), false, "gpt-5.4").StopScheduling
		require.False(t, shouldDisable)
	}

	require.True(t, (httpFixtureRuntimeBlocked(gateway, provider) || gateway.Output.Health.ModelTransient.IsBlocked(provider.Record.ID, providercore.NormalizeTransientModel(gatewayprovider.ExecutionModelPolicy(provider).CanonicalSchedulingModel("gpt-5.4")), time.Now())))
}

func TestOpenAIModelNotFound_DoesNotRuntimeBlockWholeProvider(t *testing.T) {
	repo := &gatewaytestkit.ModelHealthStore{}
	svc := newResponsesFixture(responsesFixtureInputs{health: newHTTPHealthFixture(repo, nil, nil, providercore.HealthOptions{}, nil)})
	provider := gatewaytestkit.ModelNotFoundProvider()

	shouldDisable := gatewayprovider.ApplyOpenAIResponseHealth(context.Background(), svc.Output.Health, provider, http.StatusNotFound, http.Header{}, []byte(`{"error":{"code":"model_not_found","message":"model not found"}}`), false, "gpt-5.4").StopScheduling

	require.True(t, shouldDisable)
	require.False(t, httpFixtureRuntimeBlocked(svc, provider))
	require.Zero(t, repo.TempCalls)
	require.Len(t, repo.ModelRateLimitCalls, 1)
}

func TestOpenAIModelTempUnschedulable_DoesNotRuntimeBlockWholeProvider(t *testing.T) {
	repo := &gatewaytestkit.ModelHealthStore{}
	svc := newResponsesFixture(responsesFixtureInputs{health: newHTTPHealthFixture(repo, nil, nil, providercore.HealthOptions{}, nil)})
	provider := gatewaytestkit.ModelNotFoundProvider()

	shouldDisable := gatewayprovider.ApplyOpenAIResponseHealth(context.Background(), svc.Output.Health, provider, http.StatusNotFound, http.Header{}, []byte(`{"error":{"message":"endpoint not found"}}`), false, "gpt-5.4").StopScheduling

	require.True(t, shouldDisable)
	require.False(t, httpFixtureRuntimeBlocked(svc, provider))
	require.Zero(t, repo.TempCalls)
	require.Len(t, repo.ModelRateLimitCalls, 1)
	require.Equal(t, "gpt-5.4", repo.ModelRateLimitCalls[0].Scope)
}

func TestOpenAIModelTempUnschedulable_WriteFailureDoesNotRuntimeBlockWholeProvider(t *testing.T) {
	repo := &gatewaytestkit.ModelHealthStore{ModelRateLimitErr: errors.New("write failed")}
	svc := newResponsesFixture(responsesFixtureInputs{health: newHTTPHealthFixture(repo, nil, nil, providercore.HealthOptions{}, nil)})
	provider := gatewaytestkit.ModelNotFoundProvider()

	shouldDisable := gatewayprovider.ApplyOpenAIResponseHealth(context.Background(), svc.Output.Health, provider, http.StatusNotFound, http.Header{}, []byte(`{"error":{"message":"endpoint not found"}}`), false, "gpt-5.4").StopScheduling

	require.True(t, shouldDisable)
	require.False(t, httpFixtureRuntimeBlocked(svc, provider))
	require.Zero(t, repo.TempCalls)
	require.Len(t, repo.ModelRateLimitCalls, 1)
}

func TestOpenAIOAuth429_MatchingModelTempRuleAvoidsProviderRuntimeBlock(t *testing.T) {
	repo := &gatewaytestkit.ModelHealthStore{}
	svc := newResponsesFixture(responsesFixtureInputs{health: newHTTPHealthFixture(repo, nil, nil, providercore.HealthOptions{}, nil)})
	provider := gatewaytestkit.ModelNotFoundProvider()
	provider.Record.Type = capability.ProviderTypeOAuth
	provider.Record.Credentials["temp_unschedulable_rules"] = []any{
		map[string]any{
			"error_code":       float64(http.StatusTooManyRequests),
			"keywords":         []any{"model quota"},
			"duration_minutes": float64(10),
		},
	}

	shouldDisable := gatewayprovider.ApplyOpenAIResponseHealth(context.Background(), svc.Output.Health, provider, http.StatusTooManyRequests, http.Header{}, []byte(`{"error":{"message":"model quota exhausted"}}`), false, "gpt-5.4").StopScheduling

	require.True(t, shouldDisable)
	require.False(t, httpFixtureRuntimeBlocked(svc, provider))
	require.Len(t, repo.ModelRateLimitCalls, 1)
	require.Equal(t, "gpt-5.4", repo.ModelRateLimitCalls[0].Scope)
}

func TestOpenAIOAuth429_NonmatchingModelTempRuleKeepsProviderRuntimeBlock(t *testing.T) {
	repo := &gatewaytestkit.ModelHealthStore{}
	svc := newResponsesFixture(responsesFixtureInputs{health: newHTTPHealthFixture(repo, nil, nil, providercore.HealthOptions{}, nil)})
	provider := gatewaytestkit.ModelNotFoundProvider()
	provider.Record.Type = capability.ProviderTypeOAuth
	provider.Record.Credentials["temp_unschedulable_rules"] = []any{
		map[string]any{
			"error_code":       float64(http.StatusTooManyRequests),
			"keywords":         []any{"different marker"},
			"duration_minutes": float64(10),
		},
	}

	shouldDisable := gatewayprovider.ApplyOpenAIResponseHealth(context.Background(), svc.Output.Health, provider, http.StatusTooManyRequests, http.Header{}, []byte(`{"error":{"message":"global rate limit"}}`), false, "gpt-5.4").StopScheduling

	require.False(t, shouldDisable)
	require.False(t, httpFixtureRuntimeBlocked(svc, provider))
	require.True(t, provideradapter.CanRetryOpenAI429(svc.Output.Health.Runtime, provider.View(), nil, nil))
	require.Empty(t, repo.ModelRateLimitCalls)
}

func TestOpenAITempUnschedulable_UnknownModelKeepsProviderRuntimeBlock(t *testing.T) {
	repo := &gatewaytestkit.ModelHealthStore{}
	svc := newResponsesFixture(responsesFixtureInputs{health: newHTTPHealthFixture(repo, nil, nil, providercore.HealthOptions{}, nil)})
	provider := gatewaytestkit.ModelNotFoundProvider()

	shouldDisable := gatewayprovider.ApplyOpenAIResponseHealth(context.Background(), svc.Output.Health, provider, http.StatusNotFound, http.Header{}, []byte(`{"error":{"message":"endpoint not found"}}`), false).StopScheduling

	require.True(t, shouldDisable)
	require.True(t, httpFixtureRuntimeBlocked(svc, provider))
	require.Equal(t, 1, repo.TempCalls)
	require.Empty(t, repo.ModelRateLimitCalls)
}

func expireRuntimeRetryForTest(s *OpenAIResponsesExecutor, id int64) *httpRuntimeClock {
	clock := &httpRuntimeClock{}
	state := providercore.NewRuntimeBlockState(clock.Now)
	s.Output.Health.Runtime = state
	s.Output.GrokHealth.Runtime = state
	s.Requests.Failure.Health.Runtime = state
	s.Text.Credentials.Runtime = state
	s.Text.Credentials.Recovery.Runtime = state
	clock.Set(time.Now().Add(-providercore.RuntimeRetryWindow - time.Second))
	state.RetryWindowActive(id)
	clock.nanos.Store(0)
	return clock
}

func (r *rateLimit429ProviderRepoStub) SetRateLimited(_ context.Context, id int64, resetAt time.Time) error {
	r.rateLimitCalls++
	r.lastRateLimitID = id
	r.lastRateLimitReset = resetAt
	return nil
}

func TestHandleOpenAITransientError_BlocksOnlyRequestedModel(t *testing.T) {
	svc := newWSFixture(wsFixtureInputs{})
	setWSFixtureHealth(svc, newUpstreamHealthForTest(transientCooldownProviderRepo{}, &wsFixtureOptions{}, nil, providercore.HealthOptions{}, nil))

	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 5105,
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeAPIKey,
		},
	}

	firstShouldDisable := gatewayprovider.ApplyOpenAIResponseHealth(context.Background(), svc.Output.Health, provider, http.StatusBadGateway, http.Header{}, []byte(`{"error":{"message":"Upstream request failed","type":"upstream_error"}}`), false, "gpt-5.5").StopScheduling
	secondShouldDisable := gatewayprovider.ApplyOpenAIResponseHealth(context.Background(), svc.Output.Health, provider, http.StatusBadGateway, http.Header{}, []byte(`{"error":{"message":"Upstream request failed","type":"upstream_error"}}`), false, "gpt-5.5").StopScheduling

	require.False(t, firstShouldDisable)
	require.False(t, secondShouldDisable)
	require.False(t, wsFixtureProviderBlocked(svc, provider))
	require.True(t, wsFixtureModelBlocked(svc, provider, "gpt-5.5"))
	require.False(t, wsFixtureModelBlocked(svc, provider, "gpt-5.6-terra"))
}

func TestHandleOpenAITransientError_TransientStatusesUseModelScope(t *testing.T) {
	for _, statusCode := range []int{http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout, 520, 521, 522, 523, 524} {
		t.Run(http.StatusText(statusCode), func(t *testing.T) {
			svc := newWSFixture(wsFixtureInputs{})
			setWSFixtureHealth(svc, newUpstreamHealthForTest(transientCooldownProviderRepo{}, &wsFixtureOptions{}, nil, providercore.HealthOptions{}, nil))

			provider := &gatewayprovider.ExecutionProvider{
				Record: providercore.Record{
					LoadLocation: time.LoadLocation, ID: int64(5100 + statusCode),
					Platform: capability.PlatformOpenAI,
					Type:     capability.ProviderTypeAPIKey,
				},
			}

			firstShouldDisable := gatewayprovider.ApplyOpenAIResponseHealth(context.Background(), svc.Output.Health, provider, statusCode, http.Header{}, []byte(`{"error":{"message":"temporary upstream failure"}}`), false, "gpt-5.5").StopScheduling
			secondShouldDisable := gatewayprovider.ApplyOpenAIResponseHealth(context.Background(), svc.Output.Health, provider, statusCode, http.Header{}, []byte(`{"error":{"message":"temporary upstream failure"}}`), false, "gpt-5.5").StopScheduling

			require.False(t, firstShouldDisable)
			require.False(t, secondShouldDisable)
			require.False(t, wsFixtureProviderBlocked(svc, provider), "status %d must not block the whole provider", statusCode)
			require.True(t, wsFixtureModelBlocked(svc, provider, "gpt-5.5"), "status %d should block the failing model", statusCode)
		})
	}
}

func TestHandleOpenAITransientError_529RemainsOverloadOnly(t *testing.T) {
	require.False(t, gatewayprovider.IsTransientProviderFailure(529, []byte(`{"error":{"message":"overloaded"}}`)))
}

func TestHandleOpenAITransientError_CanonicalModelIsNotMappedTwice(t *testing.T) {
	svc := newWSFixture(wsFixtureInputs{})
	setWSFixtureHealth(svc, newUpstreamHealthForTest(transientCooldownProviderRepo{}, &wsFixtureOptions{}, nil, providercore.HealthOptions{}, nil))

	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 5107,
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeAPIKey,
			Credentials: map[string]any{
				"model_mapping": map[string]any{
					"public-alias": "upstream-a",
					"upstream-a":   "upstream-b",
				},
			},
		},
	}
	canonicalModel := gatewayprovider.ExecutionModelPolicy(provider).Mapped("public-alias")
	require.Equal(t, "upstream-a", canonicalModel)

	for range 2 {
		gatewayprovider.ApplyOpenAIResponseHealth(context.Background(), svc.Output.Health, provider, http.StatusBadGateway, http.Header{}, []byte(`{"error":{"message":"temporary upstream failure"}}`), false, canonicalModel)
	}

	require.True(t, wsFixtureModelBlocked(svc, provider, "public-alias"))
	svc.choices.ReportOpenAIProviderScheduleResult(provider, canonicalModel, true, nil)
	require.False(t, wsFixtureModelBlocked(svc, provider, "public-alias"))
}

func TestHandleOpenAITransientError_DoesNotBlockParameter400(t *testing.T) {
	svc := newWSFixture(wsFixtureInputs{})
	setWSFixtureHealth(svc, newUpstreamHealthForTest(transientCooldownProviderRepo{}, &wsFixtureOptions{}, nil, providercore.HealthOptions{}, nil))

	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 5103,
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeAPIKey,
		},
	}

	shouldDisable := gatewayprovider.ApplyOpenAIResponseHealth(context.Background(), svc.Output.Health, provider, http.StatusBadRequest, http.Header{}, []byte(`{"error":{"message":"Invalid type for input[0].arguments"}}`), false, "gpt-5.5").StopScheduling

	require.False(t, shouldDisable)
	require.False(t, wsFixtureProviderBlocked(svc, provider))
	require.False(t, wsFixtureModelBlocked(svc, provider, "gpt-5.5"))
}

func TestHandleOpenAITransientError_HardDisableStillBlocksWholeProvider(t *testing.T) {
	svc := newWSFixture(wsFixtureInputs{})
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 5106, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}

	wsFixtureBlockProvider(svc, provider, time.Now().Add(time.Minute), "upstream_disable")

	require.True(t, wsFixtureRequestBlocked(svc, provider, "gpt-5.5"))
	require.True(t, wsFixtureRequestBlocked(svc, provider, "gpt-5.6-sol"))
}
