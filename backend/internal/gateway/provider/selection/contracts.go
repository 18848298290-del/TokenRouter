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

// Providers supplies candidate queries and full execution targets to selection.
type Providers interface {
	GetByID(context.Context, int64) (*gatewayadapter.ExecutionProvider, error)
	ListSchedulableByPlatform(context.Context, string) ([]gatewayadapter.ExecutionProvider, error)
	ListSchedulableByGroupIDAndPlatform(context.Context, int64, string) ([]gatewayadapter.ExecutionProvider, error)
	ListSchedulableUngroupedByPlatform(context.Context, string) ([]gatewayadapter.ExecutionProvider, error)
	ListSchedulableByPlatforms(context.Context, []string) ([]gatewayadapter.ExecutionProvider, error)
	ListSchedulableByGroupIDAndPlatforms(context.Context, int64, []string) ([]gatewayadapter.ExecutionProvider, error)
	ListSchedulableUngroupedByPlatforms(context.Context, []string) ([]gatewayadapter.ExecutionProvider, error)
}

// Groups supports lightweight and full group reads at their respective call sites.
type Groups interface {
	GetByID(context.Context, int64) (*routing.Group, error)
	GetByIDLite(context.Context, int64) (*routing.Group, error)
}

// Snapshots supplies provider snapshots; implementations decode cached records.
type Snapshots interface {
	GetProvider(context.Context, int64) (*provider.Record, error)
	ListProviders(context.Context, *int64, string, bool) ([]provider.Record, bool, error)
}

// Reads groups provider, group, and snapshot data sources.
type Reads struct {
	Providers Providers
	Groups    Groups
	Snapshot  Snapshots
}

// Shared contains app-owned instances reused by basic and advanced selection.
type Shared struct {
	Cache         schedulercore.StickyCache
	Concurrency   *schedulercore.ConcurrencyService
	Health        *provideradapter.UpstreamHealth
	GroupPolicies *routing.PricingConfigService
	Parameters    *schedulercore.Parameters
	Feedback      *schedulercore.RuntimeStats
}

// GenericDependencies supplies window-cost, RPM, session, and free-quota checks.
type GenericDependencies struct {
	Reads
	Shared
	Window                  *billing.WindowCostGuard
	WindowPrefetchAvailable bool
	RPM                     schedulercore.RPMCache
	Sessions                schedulercore.SessionLimitCache
	FreeQuota               *provider.FreeQuotaGate
	SetProviderError        func(context.Context, int64, string) error
}

// CompatibleDependencies supplies response ownership, health state, and quota observations for compatible selection.
type CompatibleDependencies struct {
	Generic *Generic
	Gemini  *Gemini
	Reads
	Shared
	Responses            session.OpenAIWSStateStore
	QuotaSettings        *provider.QuotaSettingsCache
	RuntimeBlocks        *provider.RuntimeBlockState
	ModelTransient       *provider.ModelTransientState
	ProxyCircuit         *egress.ProxyStreamCircuit
	StickyStats          *schedulercore.StickyStats
	FreeQuota            *provider.FreeQuotaGate
	NewAdvancedFreeQuota func() *provider.FreeQuotaGate
}

// GeminiDependencies supplies shared selection resources and Gemini quota prechecks.
type GeminiDependencies struct {
	Reads
	Shared
	QuotaPrecheck *provider.GeminiPrecheck
}

// Options contains startup configuration. The app distinguishes unset values from explicit zero values.
type Options struct {
	Scheduling        schedulercore.FlowOptions
	DebugRouting      bool
	StickyTTL         time.Duration
	ResponseTTL       time.Duration
	ReadLegacySticky  bool
	WriteLegacySticky bool
}

// DefaultOptions returns defaults for omitted process configuration.
func DefaultOptions() Options {
	return Options{
		Scheduling: schedulercore.FlowOptions{StickySessionMaxWaiting: 3, StickySessionWaitTimeout: 45 * time.Second, FallbackWaitTimeout: 30 * time.Second, FallbackMaxWaiting: 100, LoadBatchEnabled: true},

		StickyTTL:         time.Hour,
		ResponseTTL:       time.Hour,
		ReadLegacySticky:  true,
		WriteLegacySticky: true,
	}
}
