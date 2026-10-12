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

func TestClassifyGrokUpstreamFailure_FreeUsage(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
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

func TestClassifyGrokUpstreamFailure_EmptyUpstream(t *testing.T) {
	d := grok.ClassifyGrokUpstreamFailure(http.StatusBadGateway, []byte(`empty model output: no content/tool_calls`), "grok-4.5")
	require.Equal(t, grok.GrokFailureEmptyUpstream, d.Class)
	require.True(t, d.ShouldCooldown)
	require.True(t, d.ShouldFailover)
	require.True(t, d.BlockModel)
	require.Equal(t, 4*time.Minute, d.Cooldown)
}

func TestClassifyGrokUpstreamFailure_ModelCapacityUsesShortCooldown(t *testing.T) {
	d := grok.ClassifyGrokUpstreamFailure(http.StatusTooManyRequests,
		[]byte(`{"error":{"message":"The model is currently at capacity due to high demand"}}`), "grok-4.6")
	require.Equal(t, grok.GrokFailureModelCapacity, d.Class)
	require.Equal(t, time.Minute, d.Cooldown)
	require.False(t, d.BlockModel)
}

func TestClassifyGrokUpstreamFailure_Billing(t *testing.T) {
	d := grok.ClassifyGrokUpstreamFailure(http.StatusForbidden, []byte(`{"code":"personal-team-blocked:spending-limit","error":"spending limit reached"}`), "")
	require.Equal(t, grok.GrokFailureBilling, d.Class)
	require.True(t, d.ShouldCooldown)
	require.True(t, d.ShouldFailover)
}

func TestClassifyGrokUpstreamFailure_GrokSubscriptionRequiredIsBilling(t *testing.T) {
	d := grok.ClassifyGrokUpstreamFailure(http.StatusPaymentRequired,
		[]byte(`{"error":{"message":"You have run out of credits or need a Grok subscription"}}`), "grok-4.6")
	require.Equal(t, grok.GrokFailureBilling, d.Class)
	require.True(t, d.ShouldFailover)
	require.True(t, d.ShouldCooldown)
}

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

func TestClassifyGrokUpstreamFailure_ValidationNoCool(t *testing.T) {
	d := grok.ClassifyGrokUpstreamFailure(http.StatusBadRequest, []byte(`{"error":{"message":"invalid tool schema"}}`), "")
	require.Equal(t, grok.GrokFailureNone, d.Class)
	require.False(t, d.ShouldCooldown)
	require.False(t, d.ShouldFailover)
}

func TestClassifyGrokUpstreamFailure_FreeUsageWinsOver5xx(t *testing.T) {
	// A proxy may report quota exhaustion as 502; the body classification must take precedence.
	d := grok.ClassifyGrokUpstreamFailure(http.StatusBadGateway, []byte(`subscription:free-usage-exhausted for model grok-4.3`), "grok-4.3")
	require.Equal(t, grok.GrokFailureFreeUsage, d.Class)
	require.NotEqual(t, grok.GrokFailureServer, d.Class)
}

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

func TestClassifyGrokUpstreamFailure_CompatibilityRequiresClientError(t *testing.T) {
	body := []byte(`{"error":{"message":"upstream failed while handling the compaction blob"}}`)
	for _, status := range []int{http.StatusBadGateway, http.StatusInternalServerError} {
		d := grok.ClassifyGrokUpstreamFailure(status, body, "grok-4.6")
		require.NotEqual(t, grok.GrokFailureCompatibility, d.Class)
		require.True(t, d.ShouldCooldown)
	}
}

func TestClassifyGrokUpstreamFailure_GenericShapeErrorDoesNotFailover(t *testing.T) {
	d := grok.ClassifyGrokUpstreamFailure(http.StatusBadRequest,
		[]byte(`{"error":{"message":"data did not match any variant of the untagged enum content"}}`), "grok-4.6")
	require.NotEqual(t, grok.GrokFailureCompatibility, d.Class)
	require.False(t, d.ShouldFailover)
}

func TestShouldFailoverGrokUpstreamError_FreeUsageBody(t *testing.T) {
	body := []byte(`{"error":{"code":"subscription:free-usage-exhausted","message":"free usage exhausted"}}`)
	require.True(t, ShouldFailoverGrokResponse(http.StatusBadRequest, body))
}

func TestShouldFailoverGrokUpstreamError_CompatibilityBody(t *testing.T) {
	body := []byte(`{"error":{"message":"Could not decode the compaction blob"}}`)
	require.True(t, ShouldFailoverGrokResponse(http.StatusUnprocessableEntity, body))
}

func TestShouldFailoverGrokUpstreamError_ContentPolicyStillNoFailover(t *testing.T) {
	body := []byte(`{"error":{"code":"new_sensitive","message":"text is sensitive"}}`)
	require.False(t, ShouldFailoverGrokResponse(http.StatusForbidden, body))
}

func TestShouldFailoverGrokUpstreamError405IsGrokOnly(t *testing.T) {
	require.True(t, ShouldFailoverGrokResponse(http.StatusMethodNotAllowed, nil),
		"Grok 405 应触发切号，使粘性会话可以迁移到支持该端点的提供商")
	require.False(t, ShouldFailoverUpstreamStatus(http.StatusMethodNotAllowed),
		"通用 OpenAI 错误策略不应因 Grok 的端点能力差异扩大切号范围")
}

func TestShouldFailoverGrokUpstreamErrorExistingCodesStillWork(t *testing.T) {
	for _, code := range []int{401, 402, 403, 405, 429, 500, 502, 503, 504, 529} {
		require.True(t, ShouldFailoverGrokResponse(code, nil), "状态码 %d 应触发 Grok 切号", code)
	}
	for _, code := range []int{200, 201, 400, 404, 408, 422} {
		require.False(t, ShouldFailoverGrokResponse(code, nil), "状态码 %d 不应触发 Grok 切号", code)
	}
}

// TestGrokStreamIdleFailoverError checks retry metadata for an idle Grok stream.
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

func TestGrokStreamIdleFailoverErrorRequiresGrokProvider(t *testing.T) {
	openAI := &ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 2, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}}
	err := GrokStreamIdleFailure(openAI, time.Second)
	require.False(t, err.RetryableOnSameProvider)
	require.True(t, err.RequestScopedTransient)
}

func TestGrokStructuredErrorCandidatesDoNotShadowTopLevelMessages(t *testing.T) {
	shadowedInvalidEncrypted := []byte(`{"code":"invalid-argument","error":{"type":"invalid_request_error"},"message":"Could not decrypt encrypted_content because it was modified"}`)
	require.True(t, GrokBodyCodec().IsGrokInvalidEncryptedContentResponse(http.StatusBadRequest, shadowedInvalidEncrypted))
	require.True(t, GrokBodyCodec().IsGrokCompactionReplayDecodeError(http.StatusBadRequest, []byte(`{"error":{"type":"invalid_request_error"},"message":"could not decode compaction history"}`)))
}

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
