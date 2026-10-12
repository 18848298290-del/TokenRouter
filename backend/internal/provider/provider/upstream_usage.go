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

type UpstreamUsageExecutionOptions struct {
	Available func() bool
	BaseURL   func(*provider.Record) string
	Request   func(*provider.Record, provider.UpstreamUsageQueryConfig) (*usagecontract.Request, error)
}

// UpstreamUsageExecution holds usage adapters and builds their requests.
// The provider core owns query caching, identity checks, and lifecycle.
// @project-doc docs/interfaces/upstream_usage.md#native_usage_adapters
type UpstreamUsageExecution struct {
	Options  UpstreamUsageExecutionOptions
	mu       sync.RWMutex
	adapters map[string]usagecontract.Adapter
}

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

func (s *UpstreamUsageExecution) RegisterAdapter(adapter usagecontract.Adapter) {
	if s == nil || adapter == nil || strings.TrimSpace(adapter.Name()) == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.adapters == nil {
		s.adapters = make(map[string]usagecontract.Adapter)
	}
	s.adapters[strings.TrimSpace(adapter.Name())] = adapter
}

func (s *UpstreamUsageExecution) adapter(name string) usagecontract.Adapter {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.adapters[name]
}

func (s *UpstreamUsageExecution) Available() bool {
	return s != nil && s.Options.Available != nil && s.Options.Available()
}
func (s *UpstreamUsageExecution) Supports(name string) bool { return s.adapter(name) != nil }
func (s *UpstreamUsageExecution) BaseURL(value *provider.Record) string {
	return s.Options.BaseURL(value)
}

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
