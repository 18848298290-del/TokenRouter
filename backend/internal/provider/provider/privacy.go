package provider

import (
	"log/slog"

	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// Privacy queries use platform authorization clients while provider services
// track cancellation and conditional persistence. Antigravity privacy is
// available from its authorization options, and OpenAI privacy is installed
// when a client factory is supplied; administration logs use the same provider
// privacy options.

// PrivacyOptions supplies platform privacy calls and administrative log
// callbacks to the provider service. A nil factory leaves the OpenAI callback
// unset; Antigravity remains configured, and the owning service handles
// lifecycle and identity-checked writes.
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
