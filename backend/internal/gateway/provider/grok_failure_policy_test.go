package provider

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
)

// TestClassifyGrokUpstreamFailure_FreeUsage supplies Free exhaustion code,
// Chinese text, and token-pair text under different HTTP statuses.
// Classification permits cooldown/failover with the probe duration and leaves
// model blocking false.
func TestClassifyGrokUpstreamFailure_FreeUsage(t *testing.T) {
	cases := []struct {
		name   string // upstream failure scenario label
		status int    // upstream response status code
		body   string // upstream failure payload text
	}{
		{
			name:   "code free-usage-exhausted",
			status: http.StatusTooManyRequests,
			body:   `{"error":{"code":"subscription:free-usage-exhausted","message":"You've used all the included free usage for model grok-4.5. Usage resets over a rolling 24-hour window."}}`,
		},
		{
			name:   "chinese body without 429",
			status: http.StatusBadRequest,
			body:   `{"error":{"message":"模型额度用完，请稍后再试"}}`,
		},
		{
			name:   "token pair with free marker",
			status: http.StatusOK,
			body:   `{"error":{"message":"free usage tokens (actual / limit): 2000000 / 2000000 for model grok-4.5"}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := grok.ClassifyGrokUpstreamFailure(tc.status, []byte(tc.body), "grok-4.5")
			require.Equal(t, grok.GrokFailureFreeUsage, d.Class)
			require.True(t, d.ShouldCooldown)
			require.True(t, d.ShouldFailover)
			require.False(t, d.BlockModel, "free-usage must not soft-block models")
			require.Equal(t, grok.GrokFreeUsageProbeCooldown, d.Cooldown)
		})
	}
}

// TestClassifyGrokUpstreamFailure_EmptyUpstream supplies empty-output text
// under 502 for a Grok model. Classification selects EmptyUpstream, enables
// cooldown/failover and model blocking, and records a four-minute duration.
func TestClassifyGrokUpstreamFailure_EmptyUpstream(t *testing.T) {
	d := grok.ClassifyGrokUpstreamFailure(http.StatusBadGateway, []byte(`empty model output: no content/tool_calls`), "grok-4.5")
	require.Equal(t, grok.GrokFailureEmptyUpstream, d.Class)
	require.True(t, d.ShouldCooldown)
	require.True(t, d.ShouldFailover)
	require.True(t, d.BlockModel)
	require.Equal(t, 4*time.Minute, d.Cooldown)
}

// TestClassifyGrokUpstreamFailure_ModelCapacityUsesShortCooldown supplies
// model-capacity text under 429 for Grok. Classification identifies capacity,
// chooses a one-minute cooldown, and leaves model blocking disabled.
func TestClassifyGrokUpstreamFailure_ModelCapacityUsesShortCooldown(t *testing.T) {
	d := grok.ClassifyGrokUpstreamFailure(http.StatusTooManyRequests,
		[]byte(`{"error":{"message":"The model is currently at capacity due to high demand"}}`), "grok-4.6")
	require.Equal(t, grok.GrokFailureModelCapacity, d.Class)
	require.Equal(t, time.Minute, d.Cooldown)
	require.False(t, d.BlockModel)
}

// TestClassifyGrokUpstreamFailure_Billing supplies the spending-limit marker
// under HTTP 403. Classification selects billing and enables both provider
// cooldown and failover for that observed body.
func TestClassifyGrokUpstreamFailure_Billing(t *testing.T) {
	d := grok.ClassifyGrokUpstreamFailure(http.StatusForbidden, []byte(`{"code":"personal-team-blocked:spending-limit","error":"spending limit reached"}`), "")
	require.Equal(t, grok.GrokFailureBilling, d.Class)
	require.True(t, d.ShouldCooldown)
	require.True(t, d.ShouldFailover)
}

// TestClassifyGrokUpstreamFailure_GrokSubscriptionRequiredIsBilling reports
// missing credits/subscription under HTTP 402 for Grok. Classification chooses
// billing with cooldown and failover rather than a model-capacity condition.
func TestClassifyGrokUpstreamFailure_GrokSubscriptionRequiredIsBilling(t *testing.T) {
	d := grok.ClassifyGrokUpstreamFailure(http.StatusPaymentRequired,
		[]byte(`{"error":{"message":"You have run out of credits or need a Grok subscription"}}`), "grok-4.6")
	require.Equal(t, grok.GrokFailureBilling, d.Class)
	require.True(t, d.ShouldFailover)
	require.True(t, d.ShouldCooldown)
}

// TestGrokRetryableOnSameProvider_CapacityAndRateLimit compares model
// capacity, ordinary rate limits, billing, pool Free exhaustion, invalid
// capacity text, and another platform. Only the recognized Grok capacity case
// enables the tested ordinary same-provider replay.
func TestGrokRetryableOnSameProvider_CapacityAndRateLimit(t *testing.T) {
	provider := &ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 9105, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth}}
	require.True(t, GrokRetryableOnSameProvider(provider, http.StatusTooManyRequests,
		[]byte(`{"error":{"message":"The model is currently at capacity due to high demand"}}`)))
	require.False(t, GrokRetryableOnSameProvider(provider, http.StatusTooManyRequests,
		[]byte(`{"error":{"message":"rate limit exceeded"}}`)))
	require.False(t, GrokRetryableOnSameProvider(provider, http.StatusPaymentRequired,
		[]byte(`{"error":{"message":"You have run out of credits or need a Grok subscription"}}`)))
	poolProvider := &ExecutionProvider{Record: providercore.Record{
		LoadLocation: time.LoadLocation, ID: 9108, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth,
		Credentials: map[string]any{"pool_mode": true},
	}}
	require.False(t, GrokRetryableOnSameProvider(poolProvider, http.StatusTooManyRequests,
		[]byte(`{"error":{"code":"subscription:free-usage-exhausted"}}`)),
		"pool free-usage must fail over instead of retrying the exhausted provider")
	require.False(t, GrokRetryableOnSameProvider(provider, http.StatusBadRequest,
		[]byte(`{"error":{"message":"capacity field is invalid"}}`)))
	nonGrok := &ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 9106, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}}
	require.False(t, GrokRetryableOnSameProvider(nonGrok, http.StatusTooManyRequests,
		[]byte(`{"error":{"message":"model at capacity"}}`)))
}

// TestShouldMarkGrokTeamModelRateLimit_ExcludesCapacity compares capacity,
// ordinary rate-limit text, Free exhaustion, and invalid input. Team model
// limit marking includes quota evidence while excluding capacity and unrelated
// request errors.
func TestShouldMarkGrokTeamModelRateLimit_ExcludesCapacity(t *testing.T) {
	require.False(t, grok.ShouldMarkGrokTeamModelRateLimit(http.StatusTooManyRequests,
		[]byte(`{"error":{"message":"The model is currently at capacity due to high demand"}}`)))
	require.True(t, grok.ShouldMarkGrokTeamModelRateLimit(http.StatusTooManyRequests,
		[]byte(`{"error":{"message":"rate limit exceeded"}}`)))
	require.True(t, grok.ShouldMarkGrokTeamModelRateLimit(http.StatusBadRequest,
		[]byte(`{"error":{"code":"subscription:free-usage-exhausted"}}`)))
	require.False(t, grok.ShouldMarkGrokTeamModelRateLimit(http.StatusBadRequest,
		[]byte(`{"error":{"message":"invalid request"}}`)))
}

// TestGrokSameProviderRetryMetadata_CapacityDeadline compares capacity with
// ordinary OAuth rate-limit text. Capacity receives 500 milliseconds, a
// measured thirty-second deadline, and one retry; ordinary rate limit receives
// empty replay metadata.
func TestGrokSameProviderRetryMetadata_CapacityDeadline(t *testing.T) {
	provider := &ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 9107, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth}}
	retryable, delay, deadline, retryMax := GrokSameProviderRetryMetadata(provider, http.StatusTooManyRequests,
		[]byte(`{"error":{"message":"model capacity exceeded"}}`))
	require.True(t, retryable)
	require.Equal(t, 500*time.Millisecond, delay)
	require.WithinDuration(t, time.Now().Add(30*time.Second), deadline, 2*time.Second)
	require.Equal(t, 1, retryMax)

	retryable, delay, deadline, retryMax = GrokSameProviderRetryMetadata(provider, http.StatusTooManyRequests,
		[]byte(`{"error":{"message":"rate limit exceeded"}}`))
	require.False(t, retryable)
	require.Zero(t, delay)
	require.True(t, deadline.IsZero())
	require.Zero(t, retryMax)
}

// TestClassifyGrokUpstreamFailure_ValidationNoCool supplies an invalid
// tool-schema request error under HTTP 400 for classification. The result
// remains None, with both cooldown and provider failover disabled.
func TestClassifyGrokUpstreamFailure_ValidationNoCool(t *testing.T) {
	d := grok.ClassifyGrokUpstreamFailure(http.StatusBadRequest, []byte(`{"error":{"message":"invalid tool schema"}}`), "")
	require.Equal(t, grok.GrokFailureNone, d.Class)
	require.False(t, d.ShouldCooldown)
	require.False(t, d.ShouldFailover)
}

// TestClassifyGrokUpstreamFailure_FreeUsageWinsOver5xx reports the
// Free-exhaustion marker for a Grok model under HTTP 502. Body evidence
// selects FreeUsage and excludes the ordinary server-failure class.
func TestClassifyGrokUpstreamFailure_FreeUsageWinsOver5xx(t *testing.T) {
	// A proxy may report quota exhaustion as 502; the body classification must take precedence.
	d := grok.ClassifyGrokUpstreamFailure(http.StatusBadGateway, []byte(`subscription:free-usage-exhausted for model grok-4.3`), "grok-4.3")
	require.Equal(t, grok.GrokFailureFreeUsage, d.Class)
	require.NotEqual(t, grok.GrokFailureServer, d.Class)
}

// TestClassifyGrokUpstreamFailure_CompatibilityDoesNotCooldown supplies
// compaction decoding failures under HTTP 422 in nested and flat error forms.
// Classification permits compatibility failover while leaving cooldown flag
// and duration empty.
func TestClassifyGrokUpstreamFailure_CompatibilityDoesNotCooldown(t *testing.T) {
	cases := []string{
		`{"error":{"message":"Could not decode the compaction blob. Ensure it is unmodified from the compact response"}}`,
		`{"code":"compaction_decode_error","message":"invalid response history"}`,
	}
	for _, body := range cases {
		d := grok.ClassifyGrokUpstreamFailure(http.StatusUnprocessableEntity, []byte(body), "grok-4.6")
		require.Equal(t, grok.GrokFailureCompatibility, d.Class, body)
		require.True(t, d.ShouldFailover, body)
		require.False(t, d.ShouldCooldown, body)
		require.Zero(t, d.Cooldown, body)
	}
}

// TestClassifyGrokUpstreamFailure_CompatibilityRequiresClientError supplies
// compaction-related body text under HTTP 500 and 502 statuses. Classification
// remains outside the Compatibility class and retains the ordinary server
// cooldown flag.
func TestClassifyGrokUpstreamFailure_CompatibilityRequiresClientError(t *testing.T) {
	body := []byte(`{"error":{"message":"upstream failed while handling the compaction blob"}}`)
	for _, status := range []int{http.StatusBadGateway, http.StatusInternalServerError} {
		d := grok.ClassifyGrokUpstreamFailure(status, body, "grok-4.6")
		require.NotEqual(t, grok.GrokFailureCompatibility, d.Class)
		require.True(t, d.ShouldCooldown)
	}
}

// TestClassifyGrokUpstreamFailure_GenericShapeErrorDoesNotFailover supplies a
// generic untagged content-shape error under HTTP 400. It is not classified as
// compaction compatibility and does not permit provider failover.
func TestClassifyGrokUpstreamFailure_GenericShapeErrorDoesNotFailover(t *testing.T) {
	d := grok.ClassifyGrokUpstreamFailure(http.StatusBadRequest,
		[]byte(`{"error":{"message":"data did not match any variant of the untagged enum content"}}`), "grok-4.6")
	require.NotEqual(t, grok.GrokFailureCompatibility, d.Class)
	require.False(t, d.ShouldFailover)
}

// TestShouldFailoverGrokUpstreamError_FreeUsageBody supplies Free-exhaustion
// evidence under HTTP 400 to response policy. That body permits provider
// switching even though the generic client status alone would not qualify.
func TestShouldFailoverGrokUpstreamError_FreeUsageBody(t *testing.T) {
	body := []byte(`{"error":{"code":"subscription:free-usage-exhausted","message":"free usage exhausted"}}`)
	require.True(t, ShouldFailoverGrokResponse(http.StatusBadRequest, body))
}

// TestShouldFailoverGrokUpstreamError_CompatibilityBody supplies a
// compaction-decoding rejection under HTTP 422 to Grok response policy. That
// compatibility body permits switching for the tested client-error status.
func TestShouldFailoverGrokUpstreamError_CompatibilityBody(t *testing.T) {
	body := []byte(`{"error":{"message":"Could not decode the compaction blob"}}`)
	require.True(t, ShouldFailoverGrokResponse(http.StatusUnprocessableEntity, body))
}

// TestShouldFailoverGrokUpstreamError_ContentPolicyStillNoFailover supplies
// sensitive-content rejection under HTTP 403. Response policy excludes this
// request-content failure from provider switching despite the status
// ordinarily qualifying.
func TestShouldFailoverGrokUpstreamError_ContentPolicyStillNoFailover(t *testing.T) {
	body := []byte(`{"error":{"code":"new_sensitive","message":"text is sensitive"}}`)
	require.False(t, ShouldFailoverGrokResponse(http.StatusForbidden, body))
}

// TestShouldFailoverGrokUpstreamError405IsGrokOnly compares Grok response
// policy with the generic upstream status predicate for 405. Grok permits
// switching, while the generic status predicate does not.
func TestShouldFailoverGrokUpstreamError405IsGrokOnly(t *testing.T) {
	require.True(t, ShouldFailoverGrokResponse(http.StatusMethodNotAllowed, nil),
		"Grok 405 应触发切号，使粘性会话可以迁移到支持该端点的提供商")
	require.False(t, ShouldFailoverUpstreamStatus(http.StatusMethodNotAllowed),
		"通用 OpenAI 错误策略不应因 Grok 的端点能力差异扩大切号范围")
}

// TestShouldFailoverGrokUpstreamErrorExistingCodesStillWork supplies
// status-only cases spanning authentication, payment, method, rate, server,
// success, and client failures. Grok policy matches the tested switch-eligible
// and rejected status sets.
func TestShouldFailoverGrokUpstreamErrorExistingCodesStillWork(t *testing.T) {
	for _, code := range []int{401, 402, 403, 405, 429, 500, 502, 503, 504, 529} {
		require.True(t, ShouldFailoverGrokResponse(code, nil), "状态码 %d 应触发 Grok 切号", code)
	}
	for _, code := range []int{200, 201, 400, 404, 408, 422} {
		require.False(t, ShouldFailoverGrokResponse(code, nil), "状态码 %d 不应触发 Grok 切号", code)
	}
}

// TestGrokStreamIdleFailoverError constructs a three-minute Grok idle failure.
// Assertions cover 502, empty_upstream body, output-write recovery flag,
// request scope, and one same-provider retry before the measured deadline.
func TestGrokStreamIdleFailoverError(t *testing.T) {
	provider := &ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth}}
	err := GrokStreamIdleFailure(provider, 180*time.Second)
	require.NotNil(t, err)
	require.Equal(t, 502, err.StatusCode)
	require.True(t, err.SafeToFailoverAfterWrite)
	require.True(t, err.RetryableOnSameProvider)
	require.True(t, err.RequestScopedTransient)
	require.Equal(t, 1, err.SameProviderRetryMax)
	require.Contains(t, string(err.ResponseBody), "empty_upstream")
	require.WithinDuration(t, time.Now().Add(180*time.Second), err.SameProviderRetryDeadline, 2*time.Second)
}

// TestGrokStreamIdleFailoverErrorRequiresGrokProvider constructs the idle
// failure for an OpenAI target. The result retains request-scoped transient
// classification while same-provider replay eligibility is false.
func TestGrokStreamIdleFailoverErrorRequiresGrokProvider(t *testing.T) {
	openAI := &ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 2, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}}
	err := GrokStreamIdleFailure(openAI, time.Second)
	require.False(t, err.RetryableOnSameProvider)
	require.True(t, err.RequestScopedTransient)
}

// TestGrokStructuredErrorCandidatesDoNotShadowTopLevelMessages supplies nested
// error types beside top-level encrypted or compaction rejection messages.
// Both codec checks still recognize their corresponding top-level rejection
// evidence.
func TestGrokStructuredErrorCandidatesDoNotShadowTopLevelMessages(t *testing.T) {
	shadowedInvalidEncrypted := []byte(`{"code":"invalid-argument","error":{"type":"invalid_request_error"},"message":"Could not decrypt encrypted_content because it was modified"}`)
	require.True(t, GrokBodyCodec().IsGrokInvalidEncryptedContentResponse(http.StatusBadRequest, shadowedInvalidEncrypted))
	require.True(t, GrokBodyCodec().IsGrokCompactionReplayDecodeError(http.StatusBadRequest, []byte(`{"error":{"type":"invalid_request_error"},"message":"could not decode compaction history"}`)))
}

// TestGrokDecoderCompatibility422FailsOverWithoutCooldown supplies multiple
// 422 decoder rejection forms and unrelated validation controls. Compatible
// decoder errors permit failover with no cooldown, while other client messages
// or a 400 status do not qualify.
func TestGrokDecoderCompatibility422FailsOverWithoutCooldown(t *testing.T) {
	body := []byte(`{"detail":"data did not match any variant of untagged enum ModelInput at input[3]"}`)
	require.True(t, grok.IsGrokDecoderCompatibilityError(http.StatusUnprocessableEntity, body))
	require.True(t, grok.IsGrokDecoderCompatibilityError(http.StatusUnprocessableEntity, []byte(`{"message":"could not decode ModelInput at input.3"}`)))
	require.True(t, grok.IsGrokDecoderCompatibilityError(http.StatusUnprocessableEntity, []byte(`{"error":{"type":"invalid_request_error"},"message":"could not deserialize ModelInput at input[3]"}`)))
	require.True(t, grok.IsGrokDecoderCompatibilityError(http.StatusUnprocessableEntity, []byte(`{"error":"Failed to deserialize the JSON body into the target type: messages[1]: data did not match any variant of untagged enum Content at line 1 column 6577"}`)))
	require.True(t, ShouldFailoverGrokResponse(http.StatusUnprocessableEntity, body))
	decision := grok.ClassifyGrokUpstreamFailure(http.StatusUnprocessableEntity, body, "grok-4.5")
	require.False(t, decision.ShouldCooldown)
	require.Equal(t, grok.GrokFailureNone, decision.Class)

	require.False(t, grok.IsGrokDecoderCompatibilityError(http.StatusUnprocessableEntity, []byte(`{"error":{"message":"invalid user parameter"}}`)))
	require.False(t, grok.IsGrokDecoderCompatibilityError(http.StatusUnprocessableEntity, []byte(`{"error":"messages[1].content is required"}`)))
	require.False(t, grok.IsGrokDecoderCompatibilityError(http.StatusBadRequest, body))
}
