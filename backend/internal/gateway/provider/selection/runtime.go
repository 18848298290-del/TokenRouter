package selection

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/egress"
	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	schedulercore "github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// Provider selection combines persistent group data with application-owned
// caches and scheduling observations. Constructors bind those instances to
// generic, compatible, Gemini, and diagnostic entry points; compatible runtimes
// also create omitted process-local health stores and sticky counters for their
// own use.

// Generic holds readers, eligibility rules, and concurrency resources for provider selection.
type Generic struct {
	options             Options                           // startup scheduling options
	providerRepo        Providers                         // persisted provider reader
	groupRepo           Groups                            // admitted group reader
	schedulerSnapshot   Snapshots                         // decoded scheduling snapshots
	cache               schedulercore.StickyCache         // group session bindings
	concurrencyService  *schedulercore.ConcurrencyService // provider slot service
	healthObserver      *provideradapter.UpstreamHealth   // scheduling health observations
	groupPolicies       *routing.PricingConfigService     // group model policy
	schedulerParameters *schedulercore.Parameters         // shared effective parameters
	// advancedProviderStats uses injected feedback when present. The optional
	// lazy getter is unsynchronized, so its first initialization precedes concurrent selection.
	advancedProviderStats   *schedulercore.RuntimeStats                // advanced performance feedback
	freeQuotaGate           *provider.FreeQuotaGate                    // basic Grok quota gate
	window                  *billing.WindowCostGuard                   // Anthropic cost-window gate
	windowPrefetchAvailable bool                                       // cost-window batching capability
	rpmCache                schedulercore.RPMCache                     // Anthropic minute observations
	sessionLimitCache       schedulercore.SessionLimitCache            // Anthropic session registration
	setProviderError        func(context.Context, int64, string) error // selection-error persistence callback
}

// Compatible supplies platform eligibility rules and shared state for compatible requests.
type Compatible struct {
	generic             *Generic                                             // Anthropic eligibility checks
	gemini              *Gemini                                              // Gemini quota checks
	options             Options                                              // startup compatible options
	providerRepo        Providers                                            // database recheck reader
	schedulerSnapshot   Snapshots                                            // cached candidate reader
	schedulingGroups    func(context.Context, int64) (*routing.Group, error) // full scheduling-group reader
	cache               schedulercore.StickyCache                            // compatible session bindings
	concurrencyService  *schedulercore.ConcurrencyService                    // shared provider slots
	healthObserver      *provideradapter.UpstreamHealth                      // provider health feedback
	groupPolicies       *routing.PricingConfigService                        // compatible model policy
	schedulerParameters *schedulercore.Parameters                            // shared scoring parameters
	// openaiProviderStats uses injected feedback when available; pickerOnce
	// creates omitted feedback before publishing the compatible picker's shared statistics.
	openaiProviderStats *schedulercore.RuntimeStats  // shared performance observations
	quotaSettings       *provider.QuotaSettingsCache // quota auto-pause settings
	// freeQuotaGate serves basic selection, while newFreeQuotaGate constructs the
	// separately cached gate that the advanced picker publishes on its first quota check.
	freeQuotaGate      *provider.FreeQuotaGate        // basic Free OAuth gate
	newFreeQuotaGate   func() *provider.FreeQuotaGate // advanced gate factory
	runtime            *provider.RuntimeBlockState    // credential failure state
	modelTransient     *provider.ModelTransientState  // provider-model transient blocks
	proxyCircuit       *egress.ProxyStreamCircuit     // stream-quarantine observations
	proxyFailOpenLogAt atomic.Int64                   // warning time in Unix nanoseconds
	responseState      session.OpenAIWSStateStore     // response affinity store
	stickyMetrics      *schedulercore.StickyStats     // hash-migration observation counters
	// pickerOnce publishes picker after its statistics are available. Selection,
	// metrics, and diagnostics then reuse this picker for the lifetime of the compatible selector.
	pickerOnce sync.Once    // picker publication barrier
	picker     pickerEngine // shared advanced selector
}

// Gemini holds dependencies for slot-free Gemini and mixed-pool selection.
type Gemini struct {
	options             Options                   // startup Gemini options
	providerRepo        Providers                 // Gemini provider reads
	groupRepo           Groups                    // admitted group reads
	schedulerSnapshot   Snapshots                 // Gemini candidate snapshots
	cache               schedulercore.StickyCache // Gemini session bindings
	schedulerParameters *schedulercore.Parameters // effective Gemini settings
	// advancedProviderStats uses injected feedback or an unsynchronized lazy
	// allocation. Callers complete that optional first initialization before concurrent Gemini selection.
	advancedProviderStats *schedulercore.RuntimeStats // shared provider observations
	quotaPrecheck         *provider.GeminiPrecheck    // local quota eligibility
}

// DiagnosticSource supplies records used by the scheduler to build diagnostic results.
type DiagnosticSource interface {
	// GetProvider reads a full execution target for the diagnostic provider
	// identifier.
	GetProvider(context.Context, int64) (*gatewayadapter.ExecutionProvider, error)
	// GetGroup reads the full scheduling group used to explain effective settings.
	GetGroup(context.Context, int64) (*routing.Group, error)
	// ListProvidersForSchedulerScoreFilter reads the unpaged diagnostic pool using
	// provider and group filters.
	ListProvidersForSchedulerScoreFilter(context.Context, string, string, string, string, int64, string) ([]gatewayadapter.ExecutionProvider, error)
	// ListSchedulableProvidersForAdvancedSchedulerScore reads the candidate pool
	// used by advanced scoring diagnostics.
	ListSchedulableProvidersForAdvancedSchedulerScore(context.Context, *int64, string) ([]gatewayadapter.ExecutionProvider, error)
}

// Diagnostics shares selection parameters, feedback, and eligibility rules for read-only diagnostics.
type Diagnostics struct {
	source              DiagnosticSource                  // diagnostic record reader
	concurrencyService  *schedulercore.ConcurrencyService // shared provider slots
	feedback            *schedulercore.RuntimeStats       // production performance observations
	schedulerParameters *schedulercore.Parameters         // shared scoring parameters
	gatewayService      *Generic                          // generic diagnostic eligibility
	openAIGateway       *Compatible                       // compatible diagnostic eligibility
}

// NewGeneric binds the supplied reader and scheduling instances for generic
// selection. Omitted Window installs a default cost guard; startup options are
// stored as supplied, so callers apply configuration defaults before
// construction.
// @project-doc docs/architecture/provider_scheduling_and_cache.md#advanced_scheduler_selection
func NewGeneric(deps GenericDependencies, options Options) *Generic {
	if deps.Window == nil {
		deps.Window = defaultWindowCostGuard()
	}
	return &Generic{
		options:           options,
		providerRepo:      deps.Providers,
		groupRepo:         deps.Groups,
		schedulerSnapshot: deps.Snapshot,
		cache:             deps.Cache,

		concurrencyService:  deps.Concurrency,
		healthObserver:      deps.Health,
		groupPolicies:       deps.GroupPolicies,
		schedulerParameters: deps.Parameters,

		advancedProviderStats:   deps.Feedback,
		freeQuotaGate:           deps.FreeQuota,
		window:                  deps.Window,
		windowPrefetchAvailable: deps.WindowPrefetchAvailable,

		rpmCache:          deps.RPM,
		sessionLimitCache: deps.Sessions,
		setProviderError:  deps.SetProviderError,
	}
}

// NewCompatible binds the shared services used by compatible selection and
// diagnostic callers. Omitted credential blocks, model cooldowns, proxy
// circuit, and sticky counters receive local defaults; the advanced picker is
// initialized on demand.
func NewCompatible(deps CompatibleDependencies, options Options) *Compatible {
	if deps.RuntimeBlocks == nil {
		deps.RuntimeBlocks = provider.NewRuntimeBlockState(time.Now)
	}
	if deps.ModelTransient == nil {
		deps.ModelTransient = provider.NewModelTransientState(0)
	}
	if deps.ProxyCircuit == nil {
		deps.ProxyCircuit = egress.NewProxyStreamCircuit(egress.DefaultProxyStreamCircuitSettings())
	}
	if deps.StickyStats == nil {
		deps.StickyStats = &schedulercore.StickyStats{}
	}
	var groups func(context.Context, int64) (*routing.Group, error)
	if deps.Groups != nil {
		groups = deps.Groups.GetByID
	}
	return &Compatible{
		generic: deps.Generic, gemini: deps.Gemini,
		options:           options,
		providerRepo:      deps.Providers,
		schedulerSnapshot: deps.Snapshot,
		schedulingGroups:  groups,
		cache:             deps.Cache,

		concurrencyService:  deps.Concurrency,
		healthObserver:      deps.Health,
		groupPolicies:       deps.GroupPolicies,
		schedulerParameters: deps.Parameters,

		openaiProviderStats: deps.Feedback,
		quotaSettings:       deps.QuotaSettings,
		freeQuotaGate:       deps.FreeQuota,
		newFreeQuotaGate:    deps.NewAdvancedFreeQuota,

		runtime:        deps.RuntimeBlocks,
		modelTransient: deps.ModelTransient,
		proxyCircuit:   deps.ProxyCircuit,
		responseState:  deps.Responses,
		stickyMetrics:  deps.StickyStats,
	}
}

// NewGemini binds supplied provider, group, cache, quota, feedback, and
// parameter instances for Gemini selection. Options remain the caller's startup
// values, and provider choice uses the shared slot-free Gemini scheduling core.
func NewGemini(deps GeminiDependencies, options Options) *Gemini {
	return &Gemini{
		options:           options,
		providerRepo:      deps.Providers,
		groupRepo:         deps.Groups,
		schedulerSnapshot: deps.Snapshot,
		cache:             deps.Cache,

		schedulerParameters:   deps.Parameters,
		advancedProviderStats: deps.Feedback,
		quotaPrecheck:         deps.QuotaPrecheck,
	}
}

// NewDiagnostics binds read-only scoring inputs to the same scheduling services
// used by production selectors. The provided generic and compatible selectors
// supply platform eligibility, while shared parameters and feedback determine
// diagnostic scores.
func NewDiagnostics(source DiagnosticSource, shared Shared, generic *Generic, compatible *Compatible) *Diagnostics {
	return &Diagnostics{
		source:              source,
		concurrencyService:  shared.Concurrency,
		feedback:            shared.Feedback,
		schedulerParameters: shared.Parameters,

		gatewayService: generic,
		openAIGateway:  compatible,
	}
}
