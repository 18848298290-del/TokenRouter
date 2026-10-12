package provider

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// ImageToolCooldown records a confirmed image-tool failure. The caller determines whether the failure
// qualifies.
type ImageToolCooldown struct {
	Store interface {
		SetModelRateLimit(context.Context, int64, string, time.Time, ...string) error
	}
	Settings func(context.Context) (*provider.OpenAIImagesOAuthUnavailableCooldownSettings, error)
}

func (s *ImageToolCooldown) Apply(ctx context.Context, value *provider.Record) {
	if s == nil || s.Store == nil || value == nil || value.Platform != capability.PlatformOpenAI {
		return
	}
	stateCtx, cancel := ProviderStateContext(ctx)
	defer cancel()
	cooldown := openai.OpenAIImagesOAuthUnavailableDefaultCooldown
	if s.Settings != nil {
		settings, err := s.Settings(stateCtx)
		if err != nil {
			logging.LegacyPrintf("service.openai_gateway", "[OpenAI] Images OAuth tool cooldown setting read failed error=%v", err)
		} else {
			cooldown = time.Duration(settings.CooldownMinutes) * time.Minute
		}
	}
	resetAt := time.Now().Add(cooldown)
	if err := s.Store.SetModelRateLimit(stateCtx, value.ID, provider.OpenAIImageGenerationRateLimitKey, resetAt, openai.OpenAIImagesOAuthUnavailableReason); err != nil {
		logging.LegacyPrintf("service.openai_gateway", "[OpenAI] Images OAuth tool cooldown write failed provider_id=%d error=%v", value.ID, err)
		return
	}
	logging.LegacyPrintf("service.openai_gateway", "[OpenAI] Images OAuth tool unavailable provider_id=%d reset_in=%s", value.ID, time.Until(resetAt).Truncate(time.Second))
}
