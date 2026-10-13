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

// An evaluated beta policy can produce an empty removal set, which differs from
// an attempt that has not evaluated the policy. Preparation stores both the
// evaluation flag and filter map, and subsequent request building reuses that
// attempt result when the flag is set.
//
// Runtime construction clones the URL allowlist slice, while injected services
// and attempt policy maps remain borrowed references. Request-time readers
// decide beta and forwarding policy as the request is prepared. Base URL
// validation selects format checks only when configuration disables
// allowlisting; otherwise it requires the configured host policy before an
// upstream builder receives the normalized URL.

// Dependencies supplies shared preparation and exchange services borrowed by
// the runtime.
type Dependencies struct {
	// Attributes, Credentials, Transport, TLS, Settings, Prices, and Search
	// retain the injected service references. The application keeps them
	// usable until synchronous preparation and forwarding finish.
	//
	// final model attribute reader
	Attributes *routing.ModelAttributeService
	// selected provider credential source
	Credentials *provider.MessageCredentialSource
	// Fingerprint is optional; request preparation skips
	// fingerprint-derived metadata and header input when this service is
	// absent.
	//
	// provider client identity reader
	Fingerprint *anthropic.RequestFingerprint
	// upstream HTTP exchange transport
	Transport httpclient.UpstreamTransport
	// Health is optional; error decisions remain available through provider
	// policy when persistence observation is unavailable.
	//
	// upstream provider health observer
	Health *provideradapter.UpstreamHealth
	// provider TLS profile resolver
	TLS *egressprovider.TLSProfiles
	// request-time gateway settings reader
	Settings *gateway.RuntimeSettings
	// Anthropic fast-mode price resolver
	Prices *billing.PriceResolver
	Search *searchtools.Emulator // shared web search emulator
	// Enter is optional and supplies a release callback held through
	// executor response cleanup when admission succeeds.
	//
	// application attempt admission callback
	Enter func() (func(), error)
	// Debug is optional; preparation calls its methods synchronously and
	// the observer owns retained snapshot resources.
	Debug DebugObserver // request evidence observation adapter
	// ProviderState is optional; persistent transport failures attempt
	// scheduling exclusion only when this adapter is installed.
	ProviderState ProviderState // temporary scheduling persistence adapter
	// Deferred is optional and receives eligible Ollama activity updates;
	// the application owns flushing the queued writes.
	//
	// batched provider activity service
	Deferred *provider.DeferredService
	// GroupPolicies is optional; PrepareBedrockCompatibility uses it when a
	// group ID accompanies the selected provider.
	GroupPolicies BedrockGroupPolicies // Bedrock group policy reader
}

// BedrockGroupPolicies reads group policy for optional Bedrock compatibility
// preparation.
type BedrockGroupPolicies interface {
	// GetGroupPolicy returns the requested group policy or a lookup error.
	// PrepareBedrockCompatibility keeps the supplied body when lookup fails
	// or the policy is absent, so implementations may report unavailable
	// settings.
	GetGroupPolicy(context.Context, int64) (*routing.GroupPolicyView, error)
}

// ProviderState persists temporary scheduling exclusion after persistent
// transport failures.
type ProviderState interface {
	// SetTempUnschedulable stores the provider exclusion deadline and
	// diagnostic reason. Callers supply the write context, and transport
	// handling logs errors while returning the attempt failure to its
	// caller.
	SetTempUnschedulable(context.Context, int64, time.Time, string) error
}

// DebugObserver records request evidence through application-owned diagnostic
// resources.
type DebugObserver interface {
	// Snapshot records labeled request headers, body bytes, and diagnostic
	// fields. Calls are synchronous with preparation; implementations
	// retain any copies needed after the request buffers or headers change.
	Snapshot(string, http.Header, []byte, map[string]string)
	// Capture records a prepared request and returns its mimic diagnostic
	// text. The caller stores that text for eligible OAuth attempts;
	// implementations own any snapshot files and retained data.
	Capture(*http.Request, []byte, *gatewayadapter.ExecutionProvider, string, bool, bool) string
}

// Runtime retains injected services and startup options for Messages attempts.
type Runtime struct {
	// dependencies retains the injected references; attempts borrow them
	// while the application manages their initialization and shutdown.
	dependencies Dependencies // shared request service references
	// options stores startup choices; NewRuntime clones its allowlist slice
	// before any request builder can read that configuration.
	options Options // startup forwarding option values
}

// NewRuntime retains the supplied dependencies and startup options for Messages
// forwarding. It clones URLValidation.AllowedHosts; other referenced services,
// callbacks, and option values retain their supplied ownership.
func NewRuntime(dependencies Dependencies, options Options) *Runtime {
	options.URLValidation.AllowedHosts = slices.Clone(options.URLValidation.AllowedHosts)
	return &Runtime{dependencies: dependencies, options: options}
}

// checkBeta stores a successful beta evaluation for this attempt and returns a
// blocking error when policy rejects the request. A blocked evaluation leaves
// the attempt state unchanged.
//
// A successful empty filter set still sets BetaEvaluated and stores a nonnil
// map. Later request builders use the evaluation flag to distinguish that
// result from an unqueried policy, so a filtered-empty decision is retained
// instead of rereading dynamic settings during the same preparation path.
func (r *Runtime) checkBeta(ctx context.Context, state *AttemptState, target *gatewayadapter.ExecutionProvider, header, model string) error {
	result := r.evaluateBeta(ctx, target, header, model)
	if result.BlockErr != nil {
		return result.BlockErr
	}
	// Publish the evaluation flag together with its filter map before
	// builders query it, retaining successful empty results within this
	// synchronous attempt.
	state.BetaEvaluated = true
	state.BetaFilters = result.FilterSet
	if state.BetaFilters == nil {
		state.BetaFilters = map[string]struct{}{}
	}
	return nil
}

// betaFilters returns the saved removal set after checkBeta has evaluated the
// attempt. Otherwise it evaluates settings with an empty incoming header and
// returns that result without updating state.
func (r *Runtime) betaFilters(ctx context.Context, state *AttemptState, target *gatewayadapter.ExecutionProvider, model string) map[string]struct{} {
	if state.BetaEvaluated {
		return state.BetaFilters
	}
	// When preparation has no saved evaluation, query with an empty
	// incoming header because this callback supplies the removal set
	// independently of header assembly.
	return r.evaluateBeta(ctx, target, "", model).FilterSet
}

// evaluateBeta reads current settings and evaluates header policy for the
// selected provider and model. Missing settings or a failed read produce an
// empty policy result for request preparation.
func (r *Runtime) evaluateBeta(ctx context.Context, target *gatewayadapter.ExecutionProvider, header, model string) anthropic.BetaPolicyResult {
	if r.dependencies.Settings == nil {
		return anthropic.BetaPolicyResult{}
	}
	settings, err := r.dependencies.Settings.GetBetaPolicySettings(ctx)
	// Unavailable dynamic settings leave the beta result empty so
	// preparation can use the upstream builder defaults after a failed
	// settings read.
	if err != nil || settings == nil {
		return anthropic.BetaPolicyResult{}
	}
	return anthropic.EvaluateBetaPolicy(gatewayadapter.AnthropicBetaPolicy(settings), header, target.View().IsOAuth(), target.View().IsBedrock(), model)
}

// validateBaseURL returns a normalized upstream URL allowed by the startup
// policy. Format-only validation applies to configured disabled allowlisting;
// all other cases require the URLValidation allowlist and return wrapped
// failures.
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
