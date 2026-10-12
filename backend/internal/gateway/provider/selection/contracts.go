package selection

import (
	"context"
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

// Provider choice needs candidate records, the admitted group's settings, and
// shared runtime observations. The application supplies these readers and
// services to each selector; startup options are passed as values so app
// configuration can distinguish omitted settings from deliberate zeros.

// Providers supplies candidate queries and full execution targets to selection.
type Providers interface {
	// GetByID reads the full execution provider for a persisted identifier.
	// Selection callers validate its group, model, and runtime eligibility after
	// the read before handing the target to execution.
	GetByID(context.Context, int64) (*gatewayadapter.ExecutionProvider, error)
	// ListSchedulableByPlatform reads schedulable provider records for one
	// requested platform.
	ListSchedulableByPlatform(context.Context, string) ([]gatewayadapter.ExecutionProvider, error)
	// ListSchedulableByGroupIDAndPlatform reads schedulable members of a group for
	// one platform.
	ListSchedulableByGroupIDAndPlatform(context.Context, int64, string) ([]gatewayadapter.ExecutionProvider, error)
	// ListSchedulableUngroupedByPlatform reads schedulable providers with no group
	// membership on one platform.
	ListSchedulableUngroupedByPlatform(context.Context, string) ([]gatewayadapter.ExecutionProvider, error)
	// ListSchedulableByPlatforms reads schedulable provider records across the
	// supplied platform set.
	ListSchedulableByPlatforms(context.Context, []string) ([]gatewayadapter.ExecutionProvider, error)
	// ListSchedulableByGroupIDAndPlatforms reads schedulable group members across
	// the supplied platform set.
	ListSchedulableByGroupIDAndPlatforms(context.Context, int64, []string) ([]gatewayadapter.ExecutionProvider, error)
	// ListSchedulableUngroupedByPlatforms reads schedulable ungrouped records
	// across the supplied platform set.
	ListSchedulableUngroupedByPlatforms(context.Context, []string) ([]gatewayadapter.ExecutionProvider, error)
}

// Groups supports lightweight and full group reads at their respective call sites.
type Groups interface {
	// GetByID reads a full group record for scheduling settings and overrides.
	GetByID(context.Context, int64) (*routing.Group, error)
	// GetByIDLite reads the lightweight group data used to validate selection
	// inputs.
	GetByIDLite(context.Context, int64) (*routing.Group, error)
}

// Snapshots supplies provider snapshots; implementations decode cached records.
type Snapshots interface {
	// GetProvider decodes the full cached provider record for the supplied
	// identifier.
	GetProvider(context.Context, int64) (*provider.Record, error)
	// ListProviders reads the group's candidate snapshot for platform and forcing
	// mode. The boolean carries mixed-scheduling information from the source, and
	// read errors propagate to the selector using that snapshot.
	ListProviders(context.Context, *int64, string, bool) ([]provider.Record, bool, error)
}

// Reads groups provider, group, and snapshot data sources.
type Reads struct {
	Providers Providers // persisted provider reads
	Groups    Groups    // admitted group reads
	Snapshot  Snapshots // cached provider reads
}

// Shared contains app-owned instances reused by basic and advanced selection.
type Shared struct {
	// Cache, Concurrency, Health, GroupPolicies, Parameters, and Feedback retain
	// the supplied application instances. Constructors bind those same objects
	// so selector and diagnostic calls can observe the application's shared state.
	Cache         schedulercore.StickyCache         // group session bindings
	Concurrency   *schedulercore.ConcurrencyService // provider slot accounting
	Health        *provideradapter.UpstreamHealth   // observed scheduling thresholds
	GroupPolicies *routing.PricingConfigService     // group model policies
	Parameters    *schedulercore.Parameters         // effective scheduling settings
	Feedback      *schedulercore.RuntimeStats       // provider performance observations
}

// GenericDependencies supplies window-cost, RPM, session, and free-quota checks.
type GenericDependencies struct {
	Reads  // provider and group readers
	Shared // application scheduling services
	// Window supplies Anthropic cost-window eligibility. NewGeneric constructs a
	// default guard when no application-owned instance is supplied here.
	Window                  *billing.WindowCostGuard
	WindowPrefetchAvailable bool                                       // cost-window batch capability
	RPM                     schedulercore.RPMCache                     // Anthropic minute counters
	Sessions                schedulercore.SessionLimitCache            // Anthropic session capacity
	FreeQuota               *provider.FreeQuotaGate                    // Grok token soft gate
	SetProviderError        func(context.Context, int64, string) error // selection failure persistence
}

// CompatibleDependencies supplies response ownership, health state, and quota observations for compatible selection.
type CompatibleDependencies struct {
	Generic       *Generic                     // Anthropic runtime eligibility
	Gemini        *Gemini                      // Gemini quota eligibility
	Reads                                      // provider and group readers
	Shared                                     // application scheduling services
	Responses     session.OpenAIWSStateStore   // response provider bindings
	QuotaSettings *provider.QuotaSettingsCache // quota auto-pause settings
	// RuntimeBlocks supplies credential failure observations. NewCompatible creates
	// a process-local store when this shared dependency is omitted.
	RuntimeBlocks *provider.RuntimeBlockState
	// ModelTransient supplies provider-model cooldown observations. NewCompatible
	// constructs a bounded local store when this dependency is omitted.
	ModelTransient *provider.ModelTransientState
	// ProxyCircuit supplies stream-quarantine observations. NewCompatible creates
	// a circuit using egress defaults when no instance is supplied.
	ProxyCircuit *egress.ProxyStreamCircuit
	// StickyStats holds hash-migration observations. NewCompatible allocates a
	// selector-local counter set when the application omits a shared instance.
	StickyStats *schedulercore.StickyStats
	// FreeQuota and NewAdvancedFreeQuota serve different selection caches: basic
	// calls share FreeQuota, while each advanced picker publishes a gate constructed by the factory.
	FreeQuota            *provider.FreeQuotaGate        // basic Grok soft gate
	NewAdvancedFreeQuota func() *provider.FreeQuotaGate // advanced gate construction
}

// GeminiDependencies supplies shared selection resources and Gemini quota prechecks.
type GeminiDependencies struct {
	Reads                                  // provider and group readers
	Shared                                 // application scheduling services
	QuotaPrecheck *provider.GeminiPrecheck // local Gemini quota checks
}

// Options contains startup configuration. The app distinguishes unset values from explicit zero values.
type Options struct {
	Scheduling   schedulercore.FlowOptions // selection and wait policy
	DebugRouting bool                      // model routing diagnostics
	// StickyTTL and ResponseTTL feed SessionStickyTTL and OpenAIHTTPResponseStickyTTL
	// respectively. Those compatible helpers use a positive supplied duration or
	// one hour when the corresponding configured value is nonpositive.
	StickyTTL         time.Duration // session binding lifetime
	ResponseTTL       time.Duration // response binding lifetime
	ReadLegacySticky  bool          // legacy hash lookup
	WriteLegacySticky bool          // legacy hash dual write
}

// DefaultOptions returns startup defaults for callers that omit process
// configuration. Bindings last one hour, legacy sticky reads and writes are
// enabled, and load-batch selection uses bounded sticky and fallback waits.
func DefaultOptions() Options {
	return Options{
		Scheduling: schedulercore.FlowOptions{StickySessionMaxWaiting: 3, StickySessionWaitTimeout: 45 * time.Second, FallbackWaitTimeout: 30 * time.Second, FallbackMaxWaiting: 100, LoadBatchEnabled: true},

		StickyTTL:         time.Hour,
		ResponseTTL:       time.Hour,
		ReadLegacySticky:  true,
		WriteLegacySticky: true,
	}
}
