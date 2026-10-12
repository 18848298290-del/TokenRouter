package selection

import (
	"context"

	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
)

// geminiSelector keeps a per-call lookup so returned providers retain their corresponding read snapshots.
func (s *Gemini) geminiSelector() (*scheduler.GeminiSelector, *projectionScope) {
	scope := &projectionScope{providers: map[uint64]*gatewayadapter.ExecutionProvider{}, groups: map[uint64]*routing.Group{}}
	ports := scheduler.GeminiSelectionPorts{
		Resolve: func(ctx context.Context, id *int64) (string, bool, bool, *scheduler.FlowGroup, error) {
			platform, mixed, forced, group, err := s.resolvePlatformAndSchedulingMode(ctx, id)
			return platform, mixed, forced, scope.group(group), err
		},
		WithGroup: func(ctx context.Context, group *scheduler.FlowGroup) context.Context {
			return requeststate.WithGroup(ctx, scope.oldGroup(group))
		},
		Effective: s.advancedSchedulerEffectiveSettingsForRequest,
		Sticky: func(ctx context.Context, id *int64, hash, key, model string, excluded map[int64]struct{}, platform string, mixed bool) *scheduler.FlowProvider {
			return scope.provider(s.tryStickySessionHit(ctx, id, hash, key, model, excluded, platform, mixed))
		},
		List: func(ctx context.Context, id *int64, platform string, forced bool) ([]scheduler.FlowProvider, error) {
			values, err := s.listSchedulableProvidersOnce(ctx, id, platform, forced)
			return scope.values(values), err
		},
		Eligible: func(ctx context.Context, values []scheduler.FlowProvider, model string, excluded map[int64]struct{}, platform string, mixed bool) []*scheduler.FlowProvider {
			return scope.pointers(s.eligibleGeminiProviders(ctx, scope.oldValues(values), model, excluded, platform, mixed))
		},
		Advanced: func(ctx context.Context, id *int64, hash, key string, values []*scheduler.FlowProvider, settings policy.EffectiveSettings) *scheduler.FlowProvider {
			var targets []*gatewayadapter.ExecutionProvider
			if values != nil {
				targets = make([]*gatewayadapter.ExecutionProvider, len(values))
				for i, v := range values {
					targets[i] = scope.oldProvider(v)
				}
			}
			return scope.provider(s.selectAdvancedGeminiProvider(ctx, id, hash, key, targets, settings))
		},
		Unsupported: func(ctx context.Context, values []scheduler.FlowProvider, model, platform string, excluded map[int64]struct{}, mixed bool) error {
			return s.groupModelUnsupportedErrorIfApplicable(ctx, scope.oldValues(values), model, platform, excluded, mixed)
		},
		Hydrate: func(ctx context.Context, value *scheduler.FlowProvider) (*scheduler.FlowProvider, error) {
			out, err := s.hydrateSelectedProvider(ctx, scope.oldProvider(value))
			return scope.provider(out), err
		},
	}
	return scheduler.NewGeminiSelector(ports, s.cache), scope
}
