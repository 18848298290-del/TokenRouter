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

// openAIAuthPolicyProviderRepo counts authentication-policy provider writes
// for stream fixtures.
type openAIAuthPolicyProviderRepo struct {
	// ExecutionProviderStore supplies the remaining methods; unimplemented
	// calls expose an unexpected fixture path while these overrides count selected writes.
	gatewayprovider.ExecutionProviderStore

	tempCalls     int // temporary provider-write count
	setErrorCalls int // persistent provider-error write count
}

// openAIAuthPolicy403Counter returns configured forbidden-count observations
// for health policy fixtures.
type openAIAuthPolicy403Counter struct {
	// counts supplies sequential forbidden observations. Increment consumes its
	// first value, while an exhausted list uses one and Reset leaves the list intact.
	counts []int64 // remaining configured forbidden counts
}

// capacityShedProviderRepoStub records temporary cooldown calls for
// request-capacity fixtures.
type capacityShedProviderRepoStub struct {
	// ExecutionProviderStore supplies the remaining methods; unimplemented
	// calls expose an unexpected fixture path while these overrides count selected writes.
	gatewayprovider.ExecutionProviderStore

	tempUnschedCalls int // temporary provider-cooldown call count
}

// capacityRetryStore adapts the capacity fixture provider into core cooldown
// lookup.
type capacityRetryStore struct {
	// capacityShedProviderRepoStub supplies the same counted cooldown methods;
	// GetByID adapts only its provider representation for the core retry service.
	*capacityShedProviderRepoStub
}

// oauth429RateLimitRepo captures provider and model rate-limit writes from
// quota fixtures.
type oauth429RateLimitRepo struct {
	// HealthStoreBase supplies the ordinary fixture health operations while the
	// overrides retain the rate-limit calls and their observed reset values.
	gatewaytestkit.HealthStoreBase
	// setRateLimitedCalls and lastRateLimitedUntil describe the same latest
	// provider-rate callback. SetRateLimited increments the count before recording
	// the supplied deadline; no rate persistence is emulated beyond that observation.
	setRateLimitedCalls  int       // provider-rate persistence call count
	lastRateLimitedUntil time.Time // last provider-rate reset deadline
	// setModelRateLimitCalls, lastModelRateLimitKey and lastModelRateLimitedUntil
	// describe the latest model callback together. Tests compare its scope and
	// deadline against provider-level calls to distinguish the selected quota rule.
	setModelRateLimitCalls    int       // model-rate persistence call count
	lastModelRateLimitKey     string    // last model-rate scope key
	lastModelRateLimitedUntil time.Time // last model-rate reset deadline
}

// rateLimit429ProviderRepoStub captures provider identity and reset time for
// HTTP 429 fixtures.
type rateLimit429ProviderRepoStub struct {
	// HealthStoreBase supplies the ordinary fixture health operations while the
	// overrides retain the rate-limit calls and their observed reset values.
	gatewaytestkit.HealthStoreBase
	// rateLimitCalls, lastRateLimitID and lastRateLimitReset describe each latest
	// provider callback together. The override records supplied values for
	// deadline assertions and returns success without storing a durable block.
	rateLimitCalls     int       // provider-rate persistence call count
	lastRateLimitID    int64     // last rate-limited provider identifier
	lastRateLimitReset time.Time // last provider-rate reset timestamp
}

// TestOpenAIHandleErrorResponsePassthrough_InvalidRequest400PassesThrough
// supplies a structured parameter rejection to OAuth passthrough output. It
// checks HTTP 400 and the exact client JSON containing code and parameter
// identity.
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

// wsFixtureModelBlocked checks the model-transient state after one canonical
// scheduling-model resolution. A nil provider returns false; the fixture
// service supplies the block state and time used to observe that model key.
func wsFixtureModelBlocked(s *wsExecutionFixture, value *gatewayprovider.ExecutionProvider, model string) bool {
	if value == nil {
		return false
	}
	key := providercore.NormalizeTransientModel(gatewayprovider.ExecutionModelPolicy(value).CanonicalSchedulingModel(model))
	return s.Output.Health.ModelTransient.IsBlocked(value.Record.ID, key, time.Now())
}

// wsFixtureProviderBlocked checks credential-aware provider blocking for
// OpenAI or Grok fixture targets. Missing or other-platform providers return
// false; the runtime reader uses the current execution credential identity to
// evaluate any stored block.
func wsFixtureProviderBlocked(s *wsExecutionFixture, value *gatewayprovider.ExecutionProvider) bool {
	if value == nil || (value.Record.Platform != capability.PlatformOpenAI && value.Record.Platform != capability.PlatformGrok) {
		return false
	}
	return s.Output.Health.Runtime.Blocked(value.Record.ID, func() string { return providercore.RefreshCredentialIdentity(gatewayprovider.ExecutionRecord(value)) })
}

// wsFixtureBlockProvider publishes a runtime block for an OpenAI or Grok
// fixture provider. Missing or other-platform targets are ignored, and the
// caller supplies the deadline and reason used by later diagnostic checks.
func wsFixtureBlockProvider(s *wsExecutionFixture, value *gatewayprovider.ExecutionProvider, until time.Time, reason string) {
	if value == nil || (value.Record.Platform != capability.PlatformOpenAI && value.Record.Platform != capability.PlatformGrok) {
		return
	}
	s.Output.Health.Runtime.Block(value.Record.ID, until, reason)
}

// wsFixtureRetry429 checks OAuth-like 429 recovery against fixture runtime
// state and response evidence. This delegated admission is stateful: an
// eligible transient observation can start the provider's runtime retry window.
func wsFixtureRetry429(s *wsExecutionFixture, value *gatewayprovider.ExecutionProvider, headers http.Header, body []byte) bool {
	return provideradapter.CanRetryOpenAI429(s.Output.Health.Runtime, gatewayprovider.ExecutionRecord(value), headers, body)
}

// wsFixtureRequestBlocked reports combined provider or model blocking in the
// fixture states.
func wsFixtureRequestBlocked(s *wsExecutionFixture, value *gatewayprovider.ExecutionProvider, model string) bool {
	return wsFixtureProviderBlocked(s, value) || wsFixtureModelBlocked(s, value, model)
}

// SetTempUnschedulable counts a temporary provider write and returns fixture
// success.
func (r *openAIAuthPolicyProviderRepo) SetTempUnschedulable(context.Context, int64, time.Time, string) error {
	r.tempCalls++
	return nil
}

// SetError counts a persistent provider-error write and returns fixture
// success.
func (r *openAIAuthPolicyProviderRepo) SetError(context.Context, int64, string) error {
	r.setErrorCalls++
	return nil
}

// IncrementOpenAI403Count consumes the first configured count, or returns one
// when none remain. It ignores request identity and expiry inputs, letting
// these fixtures control the observation rather than emulate persistent
// counter storage.
func (s *openAIAuthPolicy403Counter) IncrementOpenAI403Count(context.Context, int64, int) (int64, error) {
	if len(s.counts) == 0 {
		return 1, nil
	}
	count := s.counts[0]
	s.counts = s.counts[1:]
	return count, nil
}

// ResetOpenAI403Count returns success without clearing the fixture's remaining
// configured counts.
func (*openAIAuthPolicy403Counter) ResetOpenAI403Count(context.Context, int64) error {
	return nil
}

// TestOpenAIHTTPAccessStateBadRequestDoesNotDisableProvider mentions account
// disabling inside an unknown-parameter HTTP 400. It checks false stop
// decision, no error-store call and no provider runtime block.
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

// TestOpenAIStreamEchoedAccessStateMessageDoesNotDisableOrFailover supplies
// request-validation wording inside a failed Response. It checks no
// access-state/failover classification, default terminal status and no
// provider-error or block observation.
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

// TestOpenAIHTTPAccessStateTrustsStructuredCode returns
// organization-deactivated code in HTTP 400. It checks recognized provider
// failure, one error-store write and a runtime block under that structured
// evidence.
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

// TestOpenAIHTTPAuthMessagesUseExistingStatusPolicies compares refreshable
// OAuth 401 and counter-driven 403 messages. It checks temporary cooldown
// calls and absence of permanent error-store writes for the listed fixtures.
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

// TestOpenAICapacityFailoverCarriesSafeTerminalResponse supplies
// server-overload evidence inside HTTP 400. It checks capacity classification,
// HTTP 503 client presentation and retained message without exposing the
// upstream classification code as text.
func TestOpenAICapacityFailoverCarriesSafeTerminalResponse(t *testing.T) {
	message := "Our servers are currently overloaded. Please try again later."
	body := []byte(`{"error":{"code":"server_is_overloaded","message":"` + message + `"}}`)
	err := gatewayprovider.NewOpenAIUpstreamFailure(http.StatusBadRequest, nil, body, message, false)

	require.True(t, gatewayprovider.IsOpenAICapacityShed(err))
	require.Equal(t, http.StatusServiceUnavailable, err.ClientStatusCode)
	require.Equal(t, message, err.ClientMessage)
	require.NotContains(t, err.ClientMessage, "server_is_overloaded")
}

// TestOpenAIStreamSemanticStatusesPreservedAcrossTerminalShapes enumerates
// authentication, permission, rate and overload forms. It checks inferred
// status and bare-error recovery predicate for each listed terminal payload.
func TestOpenAIStreamSemanticStatusesPreservedAcrossTerminalShapes(t *testing.T) {
	tests := []struct {
		name         string // fixture case name
		body         string // upstream terminal JSON payload
		status       int    // expected inferred HTTP status
		wantFailover bool   // expected bare-error recovery predicate
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

// TestOpenAIStreamBareErrorUsesSemanticFailover supplies a bare rate-limit
// error with recognized type/code. It checks that the stream predicate admits
// this semantic failure for provider recovery.
func TestOpenAIStreamBareErrorUsesSemanticFailover(t *testing.T) {
	payload := []byte(`{"error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"slow down"}}`)
	require.True(t, openai.OpenAIStreamErrorEventShouldFailover(payload, "slow down"))
}

// TestOpenAIStream403FailoverRequiresStructuredProviderCredentialSignal
// compares ordinary permission with access-state and authentication evidence.
// It checks HTTP 403 inference and both terminal predicates for every listed
// fixture.
func TestOpenAIStream403FailoverRequiresStructuredProviderCredentialSignal(t *testing.T) {
	tests := []struct {
		name    string // fixture case name
		payload string // upstream terminal JSON payload
		want    bool   // expected terminal failover verdict
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

// TestOpenAIStream403PostOutputProviderSideEffectsIgnoreRequestPermissionErrors
// supplies a request-specific permission terminal. It checks retained HTTP 403
// status, false scheduling stop, no error-store write and no runtime block.
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

// TestOpenAIStream403ExplicitCredentialAuthAppliesProviderSideEffects supplies
// structured invalid-key HTTP 403 evidence. It checks status, scheduling stop,
// one error-store write and a provider block for that credential failure.
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

// TestOpenAIStreamAccessStateAppliesProviderHealthBeforeFailover supplies
// suspended-workspace evidence to setup-token health. It checks semantic HTTP
// 403, scheduling stop and a runtime block after terminal effects.
func TestOpenAIStreamAccessStateAppliesProviderHealthBeforeFailover(t *testing.T) {
	svc := newWSFixture(wsFixtureInputs{})
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 919, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeSetupToken}}
	payload := []byte(`{"type":"response.failed","response":{"error":{"code":"workspace_suspended","message":"workspace is suspended"}}}`)

	status, disabled := svc.Output.TerminalProviderEffects(nil, provider, payload, "workspace is suspended", nil)

	require.Equal(t, http.StatusForbidden, status)
	require.True(t, disabled)
	require.True(t, wsFixtureProviderBlocked(svc, provider))
}

// TestOpenAIStreamOAuthLike429GetsDeadlineWithoutImmediateRuntimeBlock
// compares OAuth and setup-token rate-limit terminals. It checks same-provider
// retry with a deadline and absence of immediate runtime block for these
// fixtures.
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

// SetTempUnschedulable counts temporary cooldown attempts in the capacity
// fixture store.
func (r *capacityShedProviderRepoStub) SetTempUnschedulable(_ context.Context, _ int64, _ time.Time, _ string) error {
	r.tempUnschedCalls++
	return nil
}

// GetByID constructs an OpenAI OAuth fixture for the supplied provider
// identifier.
func (r *capacityShedProviderRepoStub) GetByID(_ context.Context, id int64) (*gatewayprovider.ExecutionProvider, error) {
	return &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: id, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}}, nil
}

// TestStreamFailedEventCapacityShedRetriesOnSameProvider compares
// overload/slow-down codes with a generic server failure outside pool mode. It
// checks capacity detection and retry eligibility only for the listed capacity
// fixtures.
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

// TestOpenAIHTTPCapacityShedIsRequestScopedForOAuthProviders classifies
// request overload and passes its metadata to cooldown/health. It checks
// transient same-provider eligibility with no recorded provider
// temporary-unscheduling call.
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

// TestOpenAIStreamErrorFrameDoesNotStartClientOutput enumerates retryable
// errors, metadata, empty/content deltas and DONE. It checks each listed
// semantic-output verdict independently of any actual writer commitment.
func TestOpenAIStreamErrorFrameDoesNotStartClientOutput(t *testing.T) {
	cases := []struct {
		data      string // upstream stream-data payload
		eventType string // declared SSE event type
		want      bool   // expected semantic client-output verdict
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

// TestSanitizeOpenAICapacityShedErrorCodeForClient enumerates overload and
// ordinary error payloads plus invalid JSON. It checks the change flag and
// exact code fragments, including retention of the listed noncapacity inputs.
func TestSanitizeOpenAICapacityShedErrorCodeForClient(t *testing.T) {
	cases := []struct {
		name        string // fixture case name
		payload     string // upstream terminal JSON payload
		wantChanged bool   // expected adopted code rewrite
		wantContain string // expected output payload fragment
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

// GetByID converts the capacity fixture execution provider into its core
// record.
func (s capacityRetryStore) GetByID(ctx context.Context, id int64) (*providercore.Record, error) {
	value, err := s.capacityShedProviderRepoStub.GetByID(ctx, id)
	return gatewayprovider.ExecutionRecord(value), err
}

// TestApplyOpenAIStreamFailedErrorPassthroughRule_UsesProvidedPlatform
// installs a Grok-specific context-window presentation rule. It checks
// successful matching and HTTP 400 when the caller passes Grok as the event
// platform.
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

// TestNonStreamingTerminalFailureFailover_NilProviderProposesNothing supplies
// capacity terminal evidence without a selected provider. It checks that
// buffered terminal classification returns no provider recovery proposal for
// that missing target.
func TestNonStreamingTerminalFailureFailover_NilProviderProposesNothing(t *testing.T) {
	c, _ := newNonStreamingFailoverContext(t)
	svc := newNonStreamingFailoverService()
	payload := []byte(`{"type":"response.failed","error":{"message":"Selected model is at capacity. Please try a different model."}}`)

	require.Nil(t, svc.nonStreamingTerminalFailure(
		c, newNonStreamingSSEResponse(), nil, false, "response.failed", payload,
		"Selected model is at capacity. Please try a different model."))
}

// SetRateLimited counts provider-rate persistence and records its last reset
// deadline.
func (r *oauth429RateLimitRepo) SetRateLimited(_ context.Context, _ int64, until time.Time) error {
	r.setRateLimitedCalls++
	r.lastRateLimitedUntil = until
	return nil
}

// SetModelRateLimit counts model-rate persistence and records scope and reset
// deadline.
func (r *oauth429RateLimitRepo) SetModelRateLimit(_ context.Context, _ int64, scope string, until time.Time, _ ...string) error {
	r.setModelRateLimitCalls++
	r.lastModelRateLimitKey = scope
	r.lastModelRateLimitedUntil = until
	return nil
}

// TestOpenAI429FastPath_KeepsOAuthProviderSchedulableDuringRetryWindow
// compares OAuth, setup-token, API-key and Grok retry inputs. It checks
// credential-specific retry deadlines and API-key blocking/persistence
// alongside no immediate OAuth block.
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

// TestOpenAI429FastPath_BlocksOAuthImmediatelyWhenSevenDayQuotaIsExhausted
// supplies exhausted seven-day quota headers. It checks a temporary runtime
// block and persisted reset beyond six days, with false permanent-stop
// decision and no local retry eligibility.
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

// TestOpenAI429FastPath_RetriesOAuthWhenNoQuotaSignalExists supplies
// Retry-After with no exhausted quota evidence. It checks OAuth retry
// eligibility and absence of a provider runtime block for that fixture.
func TestOpenAI429FastPath_RetriesOAuthWhenNoQuotaSignalExists(t *testing.T) {
	svc := newResponsesFixture(responsesFixtureInputs{})
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 424, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}}
	headers := http.Header{"Retry-After": []string{"1"}}

	require.True(t, provideradapter.CanRetryOpenAI429(svc.Output.Health.Runtime, provider.View(), headers, []byte(`{"error":{"type":"rate_limit_error","message":"try again"}}`)))
	require.False(t, httpFixtureRuntimeBlocked(svc, provider))
}

// TestOpenAIStream429IgnoresSuccessfulQuotaSnapshotHeaders expires the local
// retry window then supplies semantic 429 with HTTP 200 quota headers. It
// checks short-lived blocking that expires under the fixture clock rather than
// inheriting a week-long window.
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

// TestOpenAI429FastPath_SparkQuotaOnlyBlocksSparkModel supplies exhausted
// seven-day quota for Spark model execution. It checks model-only persistence
// and reset, no provider-rate write and no provider-wide runtime block.
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

// TestOpenAIStreamFailover_Spark429KeepsModelScope builds failover from a
// Spark rate-limit event and exhausted quota headers. It checks HTTP 429 and
// model-scoped recording with no provider-wide rate or runtime block.
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

// TestOpenAI429FastPath_OpenCodeGoUsageLimitUsesMessageResetDuration supplies
// the fixture four-hour-fifty-nine-minute reset message. It checks provider
// identity, bounded reset timestamp and runtime blocking after the rate-limit
// write.
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

// TestOpenAIRuntimeBlock_AppliesToOpenAIAPIKeyWhenRateLimitServiceStopsScheduling
// directly publishes a permanent runtime stop for an OpenAI key. It checks
// that the fixture runtime recognizes that provider block.
func TestOpenAIRuntimeBlock_AppliesToOpenAIAPIKeyWhenRateLimitServiceStopsScheduling(t *testing.T) {
	svc := newResponsesFixture(responsesFixtureInputs{})
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 44, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}

	svc.Output.Health.Runtime.BlockProviderScheduling(provider.View(), time.Time{}, "custom_error_code")

	require.True(t, httpFixtureRuntimeBlocked(svc, provider))
}

// TestOpenAIRuntimeBlock_DoesNotApplyToOtherPlatforms directly publishes a
// stop for the Gemini fixture target. It checks that the OpenAI runtime block
// helper does not retain a block for that platform.
func TestOpenAIRuntimeBlock_DoesNotApplyToOtherPlatforms(t *testing.T) {
	svc := newResponsesFixture(responsesFixtureInputs{})
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 45, Platform: capability.PlatformGemini, Type: capability.ProviderTypeOAuth}}

	svc.Output.Health.Runtime.BlockProviderScheduling(provider.View(), time.Time{}, "custom_error_code")

	require.False(t, httpFixtureRuntimeBlocked(svc, provider))
}

// TestOpenAIRuntimeBlocker_IgnoresNonOpenAIFromRateLimitService observes a
// Gemini forbidden failure through shared health. It checks true stop
// classification while the OpenAI-specific runtime block predicate remains
// false.
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

// TestOpenAIPoolModeRetryable5xx_DoesNotCreateModelTransientBlock repeats a
// listed pool-retryable HTTP 524 for one model. It checks false
// scheduling-stop decisions and no provider or model transient block after
// those observations.
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

// TestOpenAIPoolModeNonRetryable5xx_DoesNotCreateModelTransientBlock repeats
// HTTP 503 outside the fixture pool status list. It checks false stop
// decisions and no provider or model block for this pool-policy case.
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

// TestOpenAINonPoolAPIKey5xx_StillCreatesModelTransientBlock repeats HTTP 504
// for a nonpool API key. It checks false stop decisions and a true block
// predicate for either the provider or requested model after two observations.
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

// TestOpenAIModelNotFound_DoesNotRuntimeBlockWholeProvider supplies structured
// model-not-found evidence with a known model. It checks scheduling stop and
// one model-limit record, with no provider-wide temporary or runtime block.
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

// TestOpenAIModelTempUnschedulable_DoesNotRuntimeBlockWholeProvider matches an
// endpoint-not-found rule for a known model. It checks the recorded model
// scope and absence of provider-wide persistence and runtime blocking.
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

// TestOpenAIModelTempUnschedulable_WriteFailureDoesNotRuntimeBlockWholeProvider
// fails the model-limit persistence call after rule matching. It checks stop
// classification and recorded attempt without substituting a provider-wide
// cooldown or runtime block.
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

// TestOpenAIOAuth429_MatchingModelTempRuleAvoidsProviderRuntimeBlock matches a
// model-quota rule for OAuth. It checks model-scoped persistence and stop
// classification with no provider-wide runtime block in this fixture.
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

// TestOpenAIOAuth429_NonmatchingModelTempRuleKeepsProviderRuntimeBlock
// supplies generic rate limiting that misses a model rule. Its actual
// assertions check retry eligibility, no immediate runtime block and no
// model-limit write.
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

// TestOpenAITempUnschedulable_UnknownModelKeepsProviderRuntimeBlock matches
// temporary-error policy without a model identity. It checks provider-wide
// temporary persistence and runtime blocking rather than a model-limit record.
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

// expireRuntimeRetryForTest installs one test-clock runtime state across
// output, transport and credential recovery services. It seeds the selected
// provider's retry window in the past, restores the clock's ordinary reading
// and returns the controllable clock; callers run this setup before
// overlapping execution.
func expireRuntimeRetryForTest(s *OpenAIResponsesExecutor, id int64) *httpRuntimeClock {
	clock := &httpRuntimeClock{}
	state := providercore.NewRuntimeBlockState(clock.Now)
	s.Output.Health.Runtime = state
	s.Output.GrokHealth.Runtime = state
	s.Requests.Failure.Health.Runtime = state
	s.Text.Credentials.Runtime = state
	s.Text.Credentials.Recovery.Runtime = state
	// Seed a retry window under the past fixture timestamp, then restore ordinary
	// clock reads so terminal handling observes it after the seeded deadline has passed.
	clock.Set(time.Now().Add(-providercore.RuntimeRetryWindow - time.Second))
	state.RetryWindowActive(id)
	clock.nanos.Store(0)
	return clock
}

// SetRateLimited records the latest provider identifier and rate-reset
// timestamp with its call count.
func (r *rateLimit429ProviderRepoStub) SetRateLimited(_ context.Context, id int64, resetAt time.Time) error {
	r.rateLimitCalls++
	r.lastRateLimitID = id
	r.lastRateLimitReset = resetAt
	return nil
}

// TestHandleOpenAITransientError_BlocksOnlyRequestedModel repeats transient
// failure for one known model. It checks no provider-wide block, a block for
// that model and an unblocked different model in the fixture.
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

// TestHandleOpenAITransientError_TransientStatusesUseModelScope repeats each
// listed transient status for a known model. It checks unchanged stop
// decisions, no whole-provider block and a retained requested-model block.
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

// TestHandleOpenAITransientError_529RemainsOverloadOnly supplies HTTP 529 with
// overload text to transient classification. It checks that this fixture
// status is excluded from the ordinary transient-provider failure predicate.
func TestHandleOpenAITransientError_529RemainsOverloadOnly(t *testing.T) {
	require.False(t, gatewayprovider.IsTransientProviderFailure(529, []byte(`{"error":{"message":"overloaded"}}`)))
}

// TestHandleOpenAITransientError_CanonicalModelIsNotMappedTwice configures
// chained model mappings and observes failure under the first mapped model. It
// checks that model blocking is visible by client alias and cleared by
// successful schedule feedback.
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

// TestHandleOpenAITransientError_DoesNotBlockParameter400 supplies a
// parameter-error HTTP 400 for a known model. It checks false stop and absence
// of provider and model runtime blocks for that input rejection.
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

// TestHandleOpenAITransientError_HardDisableStillBlocksWholeProvider directly
// publishes a provider-wide runtime block with a finite deadline. It checks
// request blocking for both listed models under that same provider
// observation.
func TestHandleOpenAITransientError_HardDisableStillBlocksWholeProvider(t *testing.T) {
	svc := newWSFixture(wsFixtureInputs{})
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 5106, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}

	wsFixtureBlockProvider(svc, provider, time.Now().Add(time.Minute), "upstream_disable")

	require.True(t, wsFixtureRequestBlocked(svc, provider, "gpt-5.5"))
	require.True(t, wsFixtureRequestBlocked(svc, provider, "gpt-5.6-sol"))
}
