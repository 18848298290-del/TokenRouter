package provider

import (
	"context"
	"sync"

	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// ProbeTasks shares an unpersisted-provider mutex across tests and model previews; the coordinator claims
// persisted providers.
type ProbeTasks struct {
	Coordinator *provider.OpenAITaskCoordinator
	Options     provider.OpenAITaskOptions
	fallback    sync.Mutex
}

func (p *ProbeTasks) Ensure(ctx context.Context, value *provider.Record, expected string) error {
	options := p.Options
	options.FallbackMutex = &p.fallback
	return p.Coordinator.Ensure(ctx, options, value, expected)
}
