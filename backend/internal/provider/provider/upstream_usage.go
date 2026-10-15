package provider

import (
	"context"
	"strings"
	"sync"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/deepseek"
	"github.com/TokenFlux/TokenRouter/internal/upstream/kimi"
	"github.com/TokenFlux/TokenRouter/internal/upstream/usagecontract"
	"github.com/TokenFlux/TokenRouter/internal/upstream/usageprovider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/zhipu"
)

// upstream_usage.go registers usage adapters and dispatches provider usage
// queries through their prepared requests.
// Native usage queries select a named adapter and bind its technical request
// before execution. The registry installs the seven catalogued adapters for
// fixed platform or relay protocols, while provider services own shared query
// lifecycle, caching, and identity rechecks around this execution layer.

// UpstreamUsageExecutionOptions supplies availability and record-to-request
// callbacks for native usage queries.
type UpstreamUsageExecutionOptions struct {
	// Available optionally checks query readiness. Its absence makes Available
	// return false without invoking root or request callbacks.
	Available func() bool
	// BaseURL resolves a record's usage root. Configure it before BaseURL is
	// called; that method performs no nil-callback check.
	BaseURL func(*provider.Record) string
	// Request builds a query snapshot for a supported adapter. Query requires this
	// callback and propagates its preparation errors before execution.
	Request func(*provider.Record, provider.UpstreamUsageQueryConfig) (*usagecontract.Request, error)
}

// UpstreamUsageExecution holds shared native adapters and constructs their
// technical usage requests.
// @project-doc docs/interfaces/upstream_usage.md#native_usage_adapters
type UpstreamUsageExecution struct {
	// Options supplies callbacks read by query entry points. Keep callback
	// configuration stable while queries run; the registry mutex protects adapters
	// alone.
	Options UpstreamUsageExecutionOptions
	// mu protects adapters during registration and lookup. Each query retains the
	// retrieved shared adapter after unlocking, so replacement affects later
	// lookups.
	mu       sync.RWMutex                     // adapter registry access lock
	adapters map[string]usagecontract.Adapter // adapters indexed by trimmed name
}

// NewUpstreamUsageExecution creates a registry containing the catalogued native
// usage adapters. Callers supply BaseURL and Request callbacks for query
// preparation; a missing Available callback leaves the execution unavailable.
func NewUpstreamUsageExecution(options UpstreamUsageExecutionOptions) *UpstreamUsageExecution {
	source := &UpstreamUsageExecution{Options: options, adapters: make(map[string]usagecontract.Adapter)}
	factories := map[string]func() usagecontract.Adapter{
		provider.UpstreamUsageAdapterSub2API:         func() usagecontract.Adapter { return &usageprovider.Sub2APIUsageAdapter{} },
		provider.UpstreamUsageAdapterNewAPI:          func() usagecontract.Adapter { return &usageprovider.NewAPIUsageAdapter{} },
		provider.UpstreamUsageAdapterZivv:            func() usagecontract.Adapter { return &usageprovider.ZivvUsageAdapter{} },
		provider.UpstreamUsageAdapterKimiCoding:      func() usagecontract.Adapter { return &kimi.KimiCodingUsageAdapter{} },
		provider.UpstreamUsageAdapterKimiBalance:     func() usagecontract.Adapter { return &kimi.KimiBalanceUsageAdapter{} },
		provider.UpstreamUsageAdapterZhipuCoding:     func() usagecontract.Adapter { return &zhipu.ZhipuCodingUsageAdapter{} },
		provider.UpstreamUsageAdapterDeepseekBalance: func() usagecontract.Adapter { return &deepseek.DeepseekBalanceUsageAdapter{} },
	}
	for _, spec := range provider.UpstreamUsageAdapterCatalog() {
		source.RegisterAdapter(factories[spec.Name]())
	}
	return source
}

// RegisterAdapter adds or replaces an adapter under its trimmed name while
// holding the registry lock. Nil receivers, nil adapters, and blank names are
// ignored; registered adapter objects remain shared with queries.
func (s *UpstreamUsageExecution) RegisterAdapter(adapter usagecontract.Adapter) {
	if s == nil || adapter == nil || strings.TrimSpace(adapter.Name()) == "" {
		return
	}
	// Publish replacement entries under the registry lock shared by lookups. An
	// existing query can finish using the adapter it already retrieved.
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.adapters == nil {
		s.adapters = make(map[string]usagecontract.Adapter)
	}
	s.adapters[strings.TrimSpace(adapter.Name())] = adapter
}

// adapter returns the shared adapter registered under the exact supplied name.
// A missing entry returns nil, and the read lock protects lookup against
// replacement by RegisterAdapter.
func (s *UpstreamUsageExecution) adapter(name string) usagecontract.Adapter {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.adapters[name]
}

// Available reports whether the configured availability callback permits usage
// queries. Nil receivers and missing callbacks return false; callers can check
// this before invoking required request and root callbacks.
func (s *UpstreamUsageExecution) Available() bool {
	return s != nil && s.Options.Available != nil && s.Options.Available()
}

// Supports reports whether an exact adapter name is registered.
func (s *UpstreamUsageExecution) Supports(name string) bool { return s.adapter(name) != nil }

// BaseURL calls the configured resolver for the provider's usage-query root.
// Configure Options.BaseURL before calling it; the record and result are
// forwarded without additional validation at this entry.
func (s *UpstreamUsageExecution) BaseURL(value *provider.Record) string {
	return s.Options.BaseURL(value)
}

// Query builds and executes a request through the exact configured adapter
// name. Missing adapters return the unsupported error before request
// construction; caller-supplied Request errors and adapter errors propagate to
// provider query orchestration.
func (s *UpstreamUsageExecution) Query(ctx context.Context, value *provider.Record, config provider.UpstreamUsageQueryConfig) (*provider.UpstreamUsageInfo, error) {
	adapter := s.adapter(config.Adapter)
	if adapter == nil {
		return nil, provider.ErrUpstreamUsageUnsupported
	}
	input, err := s.Options.Request(value, config)
	if err != nil {
		return nil, err
	}
	return adapter.Query(ctx, input)
}
