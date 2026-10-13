package provider

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// A confirmed OpenAI image-tool failure temporarily blocks the image capability
// under its model-limit key. The caller classifies the failure before invoking
// Apply; dynamic cooldown settings can change the duration, and a settings read
// error retains the platform default.

// ImageToolCooldown stores model-specific cooldowns for caller-classified
// OpenAI image-tool failures.
type ImageToolCooldown struct {
	// Store writes the image capability's recovery time. An absent store causes
	// Apply to skip the observation entirely.
	Store interface {
		// SetModelRateLimit stores the supplied model recovery time and optional
		// reason for a provider. An error leaves Apply to log the failed cooldown
		// write and return.
		SetModelRateLimit(context.Context, int64, string, time.Time, ...string) error
	}
	// Settings optionally reads the current cooldown duration and returns nonnil
	// settings on success. An absent callback or read error keeps the platform
	// default.
	Settings func(context.Context) (*provider.OpenAIImagesOAuthUnavailableCooldownSettings, error)
}

// Apply records an image-tool cooldown for an OpenAI record using a
// provider-state context. Missing receivers, stores, or records are skipped;
// settings and write failures are logged, and the platform duration applies
// when settings cannot be read.
func (s *ImageToolCooldown) Apply(ctx context.Context, value *provider.Record) {
	if s == nil || s.Store == nil || value == nil || value.Platform != capability.PlatformOpenAI {
		return
	}
	// Cooldown persistence gets the bounded provider-state budget even after
	// request cancellation, so a completed failure observation can still affect
	// the next image attempt.
	stateCtx, cancel := ProviderStateContext(ctx)
	defer cancel()
	cooldown := openai.OpenAIImagesOAuthUnavailableDefaultCooldown
	if s.Settings != nil {
		settings, err := s.Settings(stateCtx)
		// Keep the platform duration when settings cannot be read; losing a settings
		// lookup must not discard the caller's confirmed image-tool failure.
		if err != nil {
			logging.LegacyPrintf("service.openai_gateway", "[OpenAI] Images OAuth tool cooldown setting read failed error=%v", err)
		} else {
			// Settings minutes determine when image capability becomes eligible again.
			cooldown = time.Duration(settings.CooldownMinutes) * time.Minute
		}
	}
	resetAt := time.Now().Add(cooldown)
	// The model key limits the cooldown to image generation. A storage error is
	// logged because this observation hook cannot return a persistence failure.
	if err := s.Store.SetModelRateLimit(stateCtx, value.ID, provider.OpenAIImageGenerationRateLimitKey, resetAt, openai.OpenAIImagesOAuthUnavailableReason); err != nil {
		logging.LegacyPrintf("service.openai_gateway", "[OpenAI] Images OAuth tool cooldown write failed provider_id=%d error=%v", value.ID, err)
		return
	}
	logging.LegacyPrintf("service.openai_gateway", "[OpenAI] Images OAuth tool unavailable provider_id=%d reset_in=%s", value.ID, time.Until(resetAt).Truncate(time.Second))
}
