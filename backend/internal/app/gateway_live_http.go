package app

import (
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/app/lifecycle"
	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/gateway/admission"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/provider/selection"
	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
	"github.com/TokenFlux/TokenRouter/internal/moderation"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai/liveattestation"
)

// provideLiveExecution shares credentials, WebSocket dialing, and background task tracking with other execution
// paths.
func provideLiveExecution(cfg *config.Config, auxiliary *gatewayhttp.OpenAIAuxiliary, cache session.GatewayCache, concurrency *scheduler.ConcurrencyService, choices *selection.Compatible, routes *gatewayadapter.RoutePlanner, recorders GatewayCompletionRecorders, tasks *lifecycle.Tasks, manager *lifecycle.Manager, grok *gatewayhttp.GrokExecutor) *gatewayhttp.OpenAILiveExecutor {
	out := &gatewayhttp.OpenAILiveExecutor{
		Options:  gatewayhttp.OpenAILiveOptions{MaxSessionDuration: time.Hour, ObserverRetryInterval: time.Second},
		Requests: auxiliary.Requests, Selection: choices, Routes: routes, Usage: recorders.OpenAI,
		Dialer: grok.Dialer, Attestation: liveattestation.NewProvider(), Background: tasks.Go,
	}
	out.Store, _ = cache.(session.LiveCallStore)
	if concurrency != nil {
		out.Leases = concurrency.LiveLeases()
	}
	if cfg != nil {
		if cfg.Gateway.Live.MaxSessionDurationSeconds > 0 {
			out.Options.MaxSessionDuration = time.Duration(cfg.Gateway.Live.MaxSessionDurationSeconds) * time.Second
		}
		if strings.TrimSpace(cfg.JWT.Secret) != "" {
			out.AttestationCipher = openai.NewLiveAttestationCipher(cfg.JWT.Secret)
		}
	}
	manager.Register(lifecycle.Hook{Name: "OpenAILiveObservers", StartOrder: 995, StopOrder: 5, Stop: out.StopLiveObservers})
	return out
}

// provideLiveHTTP shares funding, concurrency, and moderation services.
func provideLiveHTTP(source *gatewayhttp.OpenAILiveExecutor, funding *admission.FundingAdmission, concurrency *scheduler.ConcurrencyService, moderator *moderation.ContentModerationService, activity *gatewayRequestActivity) *gatewayhttp.LiveHandler {
	ports := gatewayhttp.LivePorts{Slots: concurrency}
	if source != nil {
		ports.Execution = source
	}
	if funding != nil {
		ports.Funding = funding
	}
	if moderator != nil {
		ports.Moderation = moderator
	}
	result := gatewayhttp.NewLiveHandler(ports)
	if activity != nil {
		result.BindRequestActivity(activity.Enter)
	}
	return result
}
