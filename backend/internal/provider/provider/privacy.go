package provider

import (
	"log/slog"

	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// PrivacyOptions binds upstream privacy calls; the provider service owns lifecycle and conditional writes.
func PrivacyOptions(factory openai.PrivacyClientFactory, endpoints openai.PrivacyEndpoints) provider.PrivacyOptions {
	antigravity := provider.AntigravityAuthorization{Options: AntigravityAuthorizationOptions(nil)}
	options := provider.PrivacyOptions{
		Antigravity: antigravity.SetPrivacy,
		Warn:        slog.Warn,
		Info:        slog.Info,
		AdminObserve: func(format string, args ...any) {
			logging.LegacyPrintf("service.admin", format, args...)
		},
	}
	if factory != nil {
		authorization := OpenAIAuthorizationOptions(&OpenAIAuthorizationDependencies{
			PrivacyFactory: factory, PrivacyEndpoints: endpoints,
		})
		options.OpenAI = authorization.DisableTraining
	}
	return options
}
