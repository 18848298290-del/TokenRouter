package app

import (
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/gateway"
	"github.com/TokenFlux/TokenRouter/internal/gateway/admission"
	"github.com/TokenFlux/TokenRouter/internal/gateway/errorpolicy"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/gateway/httpapi/textattempt"
	"github.com/TokenFlux/TokenRouter/internal/gateway/promptpolicy"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/provider/selection"
	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
	"github.com/TokenFlux/TokenRouter/internal/gateway/telemetry"
	textflow "github.com/TokenFlux/TokenRouter/internal/gateway/text"
	"github.com/TokenFlux/TokenRouter/internal/moderation"
	openaiwire "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/upstream/gemini"
)

// messageHTTPBindings holds module interfaces and stateless HTTP helpers.
type messageHTTPBindings struct {
	options     gatewayhttp.MessagesHTTPOptions
	bindings    gatewayhttp.MessagesBindings
	prompt      *promptpolicy.Service
	concurrency *gatewayhttp.ConcurrencyHelper
}

// provideMessageHTTPBindings binds Messages HTTP dependencies; executors are constructed separately.
func provideMessageHTTPBindings(
	planner *gatewayprovider.RoutePlanner,
	cache session.GatewayCache,
	shared *schedulerSharedState,
	funding *admission.FundingAdmission,
	rules *errorpolicy.ErrorPassthroughService,
	moderationService *moderation.ContentModerationService,
	settings *gateway.RuntimeSettings,
	prompts *promptpolicy.Service,
	concurrency *scheduler.ConcurrencyService,
	cfg *config.Config, choices *selection.Generic,
	keys *apikey.APIKeyService, subscriptions *billing.SubscriptionService,
) *messageHTTPBindings {
	options := gatewayhttp.MessagesHTTPOptions{MaxSwitches: 10, MaxGeminiSwitches: 3}
	ping := time.Duration(0)
	if cfg != nil {
		options.MaxBodyBytes = cfg.Gateway.MaxBodySize
		ping = time.Duration(cfg.Concurrency.PingInterval) * time.Second
		if cfg.Gateway.MaxProviderSwitches > 0 {
			options.MaxSwitches = cfg.Gateway.MaxProviderSwitches
		}
		if cfg.Gateway.MaxProviderSwitchesGemini > 0 {
			options.MaxGeminiSwitches = cfg.Gateway.MaxProviderSwitchesGemini
		}
	}
	var moderationPort gatewayhttp.ModerationPort
	if moderationService != nil {
		moderationPort = moderationService
	}
	bindings := gatewayhttp.MessagesBindings{
		PlanRoute:           planner.PlanKey,
		ClientGroupFallback: provideClientGroupFallbackResolver(keys, funding, subscriptions, cache),
		ClientVersions:      settings.GetClaudeCodeVersionBounds, Funding: funding, Moderation: moderationPort, Errors: rules,
		IsolateSession: messageSessionIsolation(cache), CachedSession: choices.GetCachedSessionProviderID,
		ObserveCompatibility: func(log *zap.Logger) {
			gatewayhttp.LogCompatibilityFallback(log, func() gatewayhttp.CompatibilityLogSnapshot {
				return gatewayCompatibilitySnapshot(shared)
			})
		},
	}
	return &messageHTTPBindings{options: options, bindings: bindings, prompt: prompts, concurrency: gatewayhttp.NewConcurrencyHelper(concurrency, gatewayhttp.SSEPingFormatClaude, ping)}
}

func provideMessagesHTTP(
	shared *messageHTTPBindings,
	runtime *textattempt.Runtime,
	activity *gatewayRequestActivity,
) *gatewayhttp.MessagesHandler {
	result := gatewayhttp.NewBoundMessagesHandler(
		shared.options,
		shared.bindings,
		shared.prompt,
		shared.concurrency,
		textflow.NewMessagesExecutor(
			runtime,
			textflow.MessageOptions{
				MaxSwitches:            shared.options.MaxSwitches,
				CompletePartialFailure: true,
				Observe:                telemetry.Failover,
			},
			textflow.MessageOptions{
				MaxSwitches: shared.options.MaxGeminiSwitches,
				Observe:     telemetry.Failover,
			},
		),
	)
	result.BindRequestActivity(activity.Enter)
	return result
}

func provideCompatibleTextHTTP(
	shared *messageHTTPBindings,
	runtime *textattempt.Runtime,
	activity *gatewayRequestActivity,
) *gatewayhttp.CompatibleTextHandler {
	result := gatewayhttp.NewBoundCompatibleTextHandler(
		shared.options,
		shared.bindings,
		openaiwire.ReplaceModelInBody,
		shared.prompt,
		shared.concurrency,
		textflow.NewMessagesExecutor(
			runtime,
			textflow.MessageOptions{
				MaxSwitches:           shared.options.MaxSwitches,
				StopOnCanceledContext: true,
				Observe:               telemetry.Failover,
			},
			textflow.MessageOptions{
				MaxSwitches:           shared.options.MaxGeminiSwitches,
				StopOnCanceledContext: true,
				Observe:               telemetry.Failover,
			},
		),
	)
	result.BindRequestActivity(activity.Enter)
	return result
}

func provideGeminiNativeHTTP(
	shared *messageHTTPBindings,
	digest *session.DigestSessionStore,
	runtime *textattempt.Runtime,
	activity *gatewayRequestActivity, choices *selection.Generic,
) *gatewayhttp.GeminiNativeHandler {
	result := gatewayhttp.NewBoundGeminiNativeHandler(
		gatewayhttp.GeminiNativeOptions{
			MaxSwitches: shared.options.MaxGeminiSwitches,
		},
		shared.bindings,
		gatewayhttp.GeminiHTTPBindings{
			SafeModelSegment: gemini.IsSafeGeminiModelPathSegment,
			FindSession:      messageDigestFind(digest),
			BindSticky:       choices.BindStickySession,
		},
		shared.prompt,
		shared.concurrency,
		func() string { return uuid.New().String() },
		textflow.NewMessagesExecutor(
			runtime,
			textflow.MessageOptions{
				MaxSwitches: shared.options.MaxGeminiSwitches,
				Observe:     telemetry.Failover,
			},
			textflow.MessageOptions{
				MaxSwitches: shared.options.MaxGeminiSwitches,
				Observe:     telemetry.Failover,
			},
		),
	)
	result.BindRequestActivity(activity.Enter)
	return result
}
