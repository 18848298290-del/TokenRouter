package provider

import (
	"context"
	"sync"

	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// Unpersisted Agent Identity records have no database claim for task
// registration. Tests and model previews share one ProbeTasks mutex for those
// records, while the task coordinator handles persisted provider claims; each
// Ensure call copies options before installing that mutex.

// ProbeTasks shares task-registration dependencies between provider tests and
// model previews.
type ProbeTasks struct {
	// Coordinator and Options define task registration for both tests and
	// previews. Ensure copies Options, replaces its fallback lock, and delegates
	// claim handling to Coordinator.
	Coordinator *provider.OpenAITaskCoordinator // task registration coordinator
	Options     provider.OpenAITaskOptions      // task registration dependencies
	// fallback serializes unpersisted records across Ensure calls. Keep the same
	// ProbeTasks instance shared by tests and previews that use this lock.
	fallback sync.Mutex
}

// Ensure asks the coordinator to establish or recover the record's Agent
// Identity task. It copies Options and supplies this instance's fallback mutex,
// so tests and previews for unpersisted records share the registration lock.
func (p *ProbeTasks) Ensure(ctx context.Context, value *provider.Record, expected string) error {
	options := p.Options
	options.FallbackMutex = &p.fallback
	return p.Coordinator.Ensure(ctx, options, value, expected)
}
