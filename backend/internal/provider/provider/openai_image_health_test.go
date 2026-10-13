package provider

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// imageHealthStore captures image model-limit writes and fails through
// unimplemented embedded health methods.
type imageHealthStore struct {
	// HealthStore leaves unimplemented health writes to the embedded interface.
	// Tests configure only the model-limit method exercised by image observations.
	providercore.HealthStore
	modelRateLimitCalls []imageHealthWrite // captured model cooldown writes
}

// imageHealthWrite records one provider model-limit mutation for image health
// assertions.
type imageHealthWrite struct {
	providerID int64     // provider receiving this cooldown
	scope      string    // model cooldown scope key
	resetAt    time.Time // image capability recovery deadline
	reason     string    // first supplied cooldown reason
}

// TestRateLimitService_HandleOpenAIImageRateLimit_ParsesTryAgainCooldown
// supplies an image-rate-limit message with a two-second retry instruction.
// Health must store that interval under the image capability key with the
// expected reason.
func TestRateLimitService_HandleOpenAIImageRateLimit_ParsesTryAgainCooldown(t *testing.T) {
	repo := &imageHealthStore{}
	svc := providercore.NewHealthService(repo, nil, providercore.HealthOptions{})
	provider := &providercore.Record{ID: 201, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}
	body := []byte(`{"error":{"type":"rate_limit_exceeded","message":"Rate limit reached for gpt-image-2-codex (for limit gpt-image) on input-images per min. Please try again in 2s."}}`)

	before := time.Now()
	handled := ObserveOpenAIImageRateLimit(context.Background(), svc, provider, http.StatusTooManyRequests, http.Header{}, body)

	require.True(t, handled)
	require.Len(t, repo.modelRateLimitCalls, 1)
	call := repo.modelRateLimitCalls[0]
	require.Equal(t, provider.ID, call.providerID)
	require.Equal(t, providercore.OpenAIImageGenerationRateLimitKey, call.scope)
	require.Equal(t, providercore.OpenAIImageRateLimitReason, call.reason)
	require.WithinDuration(t, before.Add(2*time.Second), call.resetAt, time.Second)
}

// TestRateLimitService_HandleOpenAIImageRateLimit_DefaultsToOneMinute supplies
// a recognized image limit without reset details. Health must store the bounded
// one-minute image cooldown with the expected scope and reason.
func TestRateLimitService_HandleOpenAIImageRateLimit_DefaultsToOneMinute(t *testing.T) {
	repo := &imageHealthStore{}
	svc := providercore.NewHealthService(repo, nil, providercore.HealthOptions{})
	provider := &providercore.Record{ID: 202, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}
	body := []byte(`{"error":{"type":"rate_limit_exceeded","message":"Rate limit reached for gpt-image-2-codex (for limit gpt-image) on input-images per min."}}`)

	before := time.Now()
	handled := ObserveOpenAIImageRateLimit(context.Background(), svc, provider, http.StatusTooManyRequests, http.Header{}, body)

	require.True(t, handled)
	require.Len(t, repo.modelRateLimitCalls, 1)
	call := repo.modelRateLimitCalls[0]
	require.Equal(t, providercore.OpenAIImageGenerationRateLimitKey, call.scope)
	require.Equal(t, providercore.OpenAIImageRateLimitReason, call.reason)
	require.WithinDuration(t, before.Add(time.Minute), call.resetAt, time.Second)
}

// TestRateLimitServiceHandleOpenAIImageCapabilityLoss_IgnoresGenericBadRequest
// supplies a malformed argument error unrelated to image capability. The
// observer must report unhandled and leave captured model-limit writes empty.
func TestRateLimitServiceHandleOpenAIImageCapabilityLoss_IgnoresGenericBadRequest(t *testing.T) {
	repo := &imageHealthStore{}
	svc := providercore.NewHealthService(repo, nil, providercore.HealthOptions{})
	provider := &providercore.Record{ID: 207, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}
	body := []byte(`{"error":{"message":"Invalid type for input[0].arguments"}}`)

	handled := ObserveOpenAIImageCapabilityLoss(context.Background(), svc, provider, http.StatusBadRequest, body)

	require.False(t, handled)
	require.Empty(t, repo.modelRateLimitCalls)
}

// TestRateLimitServiceHandleOpenAIImageCapabilityLoss_RespectsPlatformAndErrorCodePolicy
// supplies an image-tool denial to another platform and an excluding
// custom-code policy. Both cases must remain unhandled with no image cooldown
// writes.
func TestRateLimitServiceHandleOpenAIImageCapabilityLoss_RespectsPlatformAndErrorCodePolicy(t *testing.T) {
	body := []byte(`{"error":{"message":"Tool choice 'image_generation' not found in 'tools' parameter.","param":"tool_choice","type":"invalid_request_error"}}`)

	t.Run("non_openai_platform", func(t *testing.T) {
		repo := &imageHealthStore{}
		svc := providercore.NewHealthService(repo, nil, providercore.HealthOptions{})
		provider := &providercore.Record{ID: 208, Platform: capability.PlatformAnthropic, Type: capability.ProviderTypeOAuth}

		handled := ObserveOpenAIImageCapabilityLoss(context.Background(), svc, provider, http.StatusBadRequest, body)

		require.False(t, handled)
		require.Empty(t, repo.modelRateLimitCalls)
	})

	t.Run("custom_error_code_policy_excludes_400", func(t *testing.T) {
		repo := &imageHealthStore{}
		svc := providercore.NewHealthService(repo, nil, providercore.HealthOptions{})
		provider := &providercore.Record{
			ID:       209,
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeAPIKey,
			Credentials: map[string]any{
				"custom_error_codes_enabled": true,
				"custom_error_codes":         []any{float64(http.StatusTooManyRequests)},
			},
		}

		require.False(t, provider.ShouldHandleErrorCode(http.StatusBadRequest))
		handled := ObserveOpenAIImageCapabilityLoss(context.Background(), svc, provider, http.StatusBadRequest, body)

		require.False(t, handled)
		require.Empty(t, repo.modelRateLimitCalls)
	})
}

// SetModelRateLimit appends a captured provider, model scope, deadline, and
// first optional reason. Missing reasons leave an empty string; tests invoke it
// serially while assertions read the stored slice.
func (s *imageHealthStore) SetModelRateLimit(_ context.Context, id int64, scope string, resetAt time.Time, reasons ...string) error {
	call := imageHealthWrite{providerID: id, scope: scope, resetAt: resetAt}
	if len(reasons) > 0 {
		call.reason = reasons[0]
	}
	s.modelRateLimitCalls = append(s.modelRateLimitCalls, call)
	return nil
}
