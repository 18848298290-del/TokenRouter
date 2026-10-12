package messageforward

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/egress"
	egressprovider "github.com/TokenFlux/TokenRouter/internal/egress/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway"
	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/searchtools"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/upstream/anthropic"
)

// Dependencies supplies credentials, transport, and policies shared by preparation and execution.
// Dynamic settings are read during request handling.
type Dependencies struct {
	Attributes    *routing.ModelAttributeService
	Credentials   *provider.MessageCredentialSource
	Fingerprint   *anthropic.RequestFingerprint
	Transport     httpclient.UpstreamTransport
	Health        *provideradapter.UpstreamHealth
	TLS           *egressprovider.TLSProfiles
	Settings      *gateway.RuntimeSettings
	Prices        *billing.PriceResolver
	Search        *searchtools.Emulator
	Enter         func() (func(), error)
	Debug         DebugObserver
	ProviderState ProviderState
	Deferred      *provider.DeferredService
	GroupPolicies BedrockGroupPolicies
}

// BedrockGroupPolicies reads group feature settings during upstream request preparation.
type BedrockGroupPolicies interface {
	GetGroupPolicy(context.Context, int64) (*routing.GroupPolicyView, error)
}

// ProviderState records temporary provider unscheduling.
type ProviderState interface {
	SetTempUnschedulable(context.Context, int64, time.Time, string) error
}

// DebugObserver receives request snapshots; implementations own their files and log resources.
type DebugObserver interface {
	Snapshot(string, http.Header, []byte, map[string]string)
	Capture(*http.Request, []byte, *gatewayadapter.ExecutionProvider, string, bool, bool) string
}

// Runtime holds Messages dependencies. Each attempt keeps its own state, credentials, and response.
type Runtime struct {
	dependencies Dependencies
	options      Options
}

func NewRuntime(dependencies Dependencies, options Options) *Runtime {
	options.URLValidation.AllowedHosts = slices.Clone(options.URLValidation.AllowedHosts)
	return &Runtime{dependencies: dependencies, options: options}
}

// checkBeta saves the Messages beta policy result, including an empty filter set.
func (r *Runtime) checkBeta(ctx context.Context, state *AttemptState, target *gatewayadapter.ExecutionProvider, header, model string) error {
	result := r.evaluateBeta(ctx, target, header, model)
	if result.BlockErr != nil {
		return result.BlockErr
	}
	state.BetaEvaluated = true
	state.BetaFilters = result.FilterSet
	if state.BetaFilters == nil {
		state.BetaFilters = map[string]struct{}{}
	}
	return nil
}

// betaFilters reuses an evaluated beta policy result or reads settings when it has not been evaluated.
func (r *Runtime) betaFilters(ctx context.Context, state *AttemptState, target *gatewayadapter.ExecutionProvider, model string) map[string]struct{} {
	if state.BetaEvaluated {
		return state.BetaFilters
	}
	return r.evaluateBeta(ctx, target, "", model).FilterSet
}

func (r *Runtime) evaluateBeta(ctx context.Context, target *gatewayadapter.ExecutionProvider, header, model string) anthropic.BetaPolicyResult {
	if r.dependencies.Settings == nil {
		return anthropic.BetaPolicyResult{}
	}
	settings, err := r.dependencies.Settings.GetBetaPolicySettings(ctx)
	if err != nil || settings == nil {
		return anthropic.BetaPolicyResult{}
	}
	return anthropic.EvaluateBetaPolicy(gatewayadapter.AnthropicBetaPolicy(settings), header, target.View().IsOAuth(), target.View().IsBedrock(), model)
}

// validateBaseURL applies URL format validation or allowlist validation according to configuration.
func (r *Runtime) validateBaseURL(raw string) (string, error) {
	var normalized string
	var err error
	if r.options.Configured && !r.options.URLAllowlistEnabled {
		normalized, err = egress.ValidateURLFormat(raw, r.options.AllowInsecureHTTP)
	} else {
		policy := r.options.URLValidation
		policy.RequireAllowlist = true
		normalized, err = egress.ValidateHTTPSURL(raw, policy)
	}
	if err != nil {
		return "", fmt.Errorf("invalid base_url: %w", err)
	}
	return normalized, nil
}
