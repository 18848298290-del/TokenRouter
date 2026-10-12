package provider

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	acct "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/antigravity"
	"github.com/TokenFlux/TokenRouter/internal/upstream/gemini"
)

// Antigravity 429 responses may carry model metadata or only a reset delay,
// while 503 capacity responses leave recovery with the retry loop. These cases
// exercise antigravity_error_observation.go and check recorded model keys,
// provider-wide writes, and caller retry or switch decisions.

// antigravityErrorStoreFixture records serial health writes made by the model
// observer.
type antigravityErrorStoreFixture struct {
	// AntigravityHealthStore leaves unconfigured operations on a nil interface.
	// These observer cases use the model-window method below; other inherited
	// methods panic when reached.
	acct.AntigravityHealthStore         // unused health-store substitute
	rateCalls                   []int64 // provider-wide write identities
	modelRateLimitCalls         []struct {
		modelKey string /* stored model-window identity */
	} // recorded model-limit writes
}

// TestHandleUpstreamError_429_ModelRateLimit supplies explicit model metadata and
// a fifteen-second reset delay. It checks a handled switch decision and the
// same parsed model recorded in the health store.
func TestHandleUpstreamError_429_ModelRateLimit(t *testing.T) {
	repo := &antigravityErrorStoreFixture{}
	svc := newAntigravityErrorFixture(repo)
	provider := &acct.Record{ID: 1, Name: "acc-1", Platform: capability.PlatformAntigravity}

	body := []byte(`{
		"error": {
			"status": "RESOURCE_EXHAUSTED",
			"details": [
				{"@type": "type.googleapis.com/google.rpc.ErrorInfo", "metadata": {"model": "claude-sonnet-4-5"}, "reason": "RATE_LIMIT_EXCEEDED"},
				{"@type": "type.googleapis.com/google.rpc.RetryInfo", "retryDelay": "15s"}
			]
		}
	}`)

	result := svc.Observe(AntigravityErrorInput{Context: context.Background(), Prefix: "[test]", Provider: provider, Status: http.StatusTooManyRequests, Headers: http.Header{}, Body: body, RequestedModel: "claude-sonnet-4-5"})

	require.NotNil(t, result)
	require.True(t, result.Handled)
	require.NotNil(t, result.SwitchError)
	require.Equal(t, "claude-sonnet-4-5", result.SwitchError.RateLimitedModel)
	require.Len(t, repo.modelRateLimitCalls, 1)
	require.Equal(t, "claude-sonnet-4-5", repo.modelRateLimitCalls[0].modelKey)
}

// TestHandleUpstreamError_429_NonModelRateLimit supplies quota timing without model
// classification metadata. It checks that the request model receives a window
// while the observer returns no immediate retry or switch result.
func TestHandleUpstreamError_429_NonModelRateLimit(t *testing.T) {
	repo := &antigravityErrorStoreFixture{}
	svc := newAntigravityErrorFixture(repo)
	provider := &acct.Record{ID: 2, Name: "acc-2", Platform: capability.PlatformAntigravity}

	// This response has a reset delay but no RATE_LIMIT_EXCEEDED reason.
	body := buildGeminiRateLimitBody("5s")

	result := svc.Observe(AntigravityErrorInput{Context: context.Background(), Prefix: "[test]", Provider: provider, Status: http.StatusTooManyRequests, Headers: http.Header{}, Body: body, RequestedModel: "claude-sonnet-4-5"})

	require.Nil(t, result)
	require.Len(t, repo.modelRateLimitCalls, 1)
	require.Equal(t, "claude-sonnet-4-5", repo.modelRateLimitCalls[0].modelKey)
}

// TestHandleUpstreamError_429_NonModelRateLimit_UsesMappedModelKey uses an Opus
// request without saved mappings or a thinking override. It checks that generic
// 429 handling records that unchanged final model spelling.
func TestHandleUpstreamError_429_NonModelRateLimit_UsesMappedModelKey(t *testing.T) {
	repo := &antigravityErrorStoreFixture{}
	svc := newAntigravityErrorFixture(repo)
	provider := &acct.Record{ID: 20, Name: "acc-20", Platform: capability.PlatformAntigravity}

	body := buildGeminiRateLimitBody("5s")

	result := svc.Observe(AntigravityErrorInput{Context: context.Background(), Prefix: "[test]", Provider: provider, Status: http.StatusTooManyRequests, Headers: http.Header{}, Body: body, RequestedModel: "claude-opus-4-6"})

	require.Nil(t, result)
	require.Len(t, repo.modelRateLimitCalls, 1)
	require.Equal(t, "claude-opus-4-6", repo.modelRateLimitCalls[0].modelKey)
}

// TestHandleUpstreamError_503_ModelCapacityExhausted supplies shared-capacity
// metadata with a model and retry delay. It checks a handled result with no
// observer retry, switch request, or model-limit write.
func TestHandleUpstreamError_503_ModelCapacityExhausted(t *testing.T) {
	repo := &antigravityErrorStoreFixture{}
	svc := newAntigravityErrorFixture(repo)
	provider := &acct.Record{ID: 3, Name: "acc-3", Platform: capability.PlatformAntigravity}

	body := []byte(`{
		"error": {
			"status": "UNAVAILABLE",
			"details": [
				{"@type": "type.googleapis.com/google.rpc.ErrorInfo", "metadata": {"model": "gemini-3-pro-high"}, "reason": "MODEL_CAPACITY_EXHAUSTED"},
				{"@type": "type.googleapis.com/google.rpc.RetryInfo", "retryDelay": "30s"}
			]
		}
	}`)

	result := svc.Observe(AntigravityErrorInput{Context: context.Background(), Prefix: "[test]", Provider: provider, Status: http.StatusServiceUnavailable, Headers: http.Header{}, Body: body, RequestedModel: "gemini-3-pro-high"})

	// The observer consumes the error without recording a model limit or
	// requesting a switch.
	require.NotNil(t, result)
	require.True(t, result.Handled)
	require.False(t, result.ShouldRetry, "MODEL_CAPACITY_EXHAUSTED should not trigger retry from handleModelRateLimit path")
	require.Nil(t, result.SwitchError, "MODEL_CAPACITY_EXHAUSTED should not trigger provider switch")
	require.Empty(t, repo.modelRateLimitCalls, "MODEL_CAPACITY_EXHAUSTED should not set model rate limit")
}

// TestHandleUpstreamError_503_NonModelRateLimit supplies a generic
// service-unavailable
// response with no model window. It checks that the observer produces no result
// and writes neither a provider-wide nor model-specific limit.
func TestHandleUpstreamError_503_NonModelRateLimit(t *testing.T) {
	repo := &antigravityErrorStoreFixture{}
	svc := newAntigravityErrorFixture(repo)
	provider := &acct.Record{ID: 4, Name: "acc-4", Platform: capability.PlatformAntigravity}

	body := []byte(`{
		"error": {
			"status": "UNAVAILABLE",
			"message": "Service temporarily unavailable",
			"details": [
				{"@type": "type.googleapis.com/google.rpc.ErrorInfo", "reason": "SERVICE_UNAVAILABLE"}
			]
		}
	}`)

	result := svc.Observe(AntigravityErrorInput{Context: context.Background(), Prefix: "[test]", Provider: provider, Status: http.StatusServiceUnavailable, Headers: http.Header{}, Body: body, RequestedModel: "gemini-3-pro-high"})

	require.Nil(t, result)
	require.Empty(t, repo.modelRateLimitCalls, "503 non-model rate limit should not trigger model rate limit")
	require.Empty(t, repo.rateCalls, "503 non-model rate limit should not trigger provider rate limit")
}

// TestHandleUpstreamError_503_EmptyBody supplies an empty JSON object for a failed
// service response. It checks that missing model metadata produces no retry
// result and leaves both recorded limit collections empty.
func TestHandleUpstreamError_503_EmptyBody(t *testing.T) {
	repo := &antigravityErrorStoreFixture{}
	svc := newAntigravityErrorFixture(repo)
	provider := &acct.Record{ID: 5, Name: "acc-5", Platform: capability.PlatformAntigravity}

	body := []byte(`{}`)

	result := svc.Observe(AntigravityErrorInput{Context: context.Background(), Prefix: "[test]", Provider: provider, Status: http.StatusServiceUnavailable, Headers: http.Header{}, Body: body, RequestedModel: "gemini-3-pro-high"})

	require.Nil(t, result)
	require.Empty(t, repo.modelRateLimitCalls)
	require.Empty(t, repo.rateCalls)
}

// buildGeminiRateLimitBody embeds the supplied delay in generic quota-reset
// metadata.
func buildGeminiRateLimitBody(delay string) []byte {
	return []byte(fmt.Sprintf(`{"error":{"message":"too many requests","details":[{"metadata":{"quotaResetDelay":%q}}]}}`, delay))
}

// SetModelRateLimit records the supplied model key for later serial assertions.
func (s *antigravityErrorStoreFixture) SetModelRateLimit(_ context.Context, _ int64, key string, _ time.Time, _ ...string) error {
	s.modelRateLimitCalls = append(s.modelRateLimitCalls, struct {
		modelKey string /* recorded model-window key */
	}{key})
	return nil
}

// SetRateLimited records the provider ID for each provider-wide write assertion.
func (s *antigravityErrorStoreFixture) SetRateLimited(_ context.Context, id int64, _ time.Time) error {
	s.rateCalls = append(s.rateCalls, id)
	return nil
}

// newAntigravityErrorFixture binds model-window storage and platform reset parsing.
// Body logging is disabled and the platform default supplies missing reset
// timing. The fixture uses no general health observer or snapshot publisher.
func newAntigravityErrorFixture(store *antigravityErrorStoreFixture) *AntigravityErrorObserver {
	return &AntigravityErrorObserver{
		Health:         &acct.AntigravityHealth{Store: store, ModelKeys: AntigravityModelLimitKeys, Logf: func(string, ...any) {}},
		SetRateLimited: store.SetRateLimited, LogConfig: func() (bool, int) { return false, 0 },
		ResetTime:       func(body []byte) *int64 { return gemini.ParseGeminiRateLimitResetTime(body, nil) },
		DefaultDuration: func() time.Duration { return antigravity.AntigravityDefaultRateLimitDuration },
	}
}
