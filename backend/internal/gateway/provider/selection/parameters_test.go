package selection

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/routing"
	schedulercore "github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
	schedulerredis "github.com/TokenFlux/TokenRouter/internal/scheduler/rediscache"
)

type snapshotGroupBindingReader struct {
	Groups
	group *routing.Group
	calls int
}

func (r *snapshotGroupBindingReader) GetByID(context.Context, int64) (*routing.Group, error) {
	r.calls++
	return r.group, nil
}

// TestSnapshotNativeBindingPreservesGroupOverrides checks group settings and overrides when request context lacks a group.
func TestSnapshotNativeBindingPreservesGroupOverrides(t *testing.T) {
	for _, platform := range []string{"gateway", "gemini", "openai"} {
		t.Run(platform, func(t *testing.T) {
			topK := 17
			group := &routing.Group{
				ID: 71, SchedulerType: routing.GroupSchedulerTypeAdvanced,
				AdvancedSchedulerOverrides: routing.GroupAdvancedSchedulerOverrides{LBTopK: &topK},
			}
			reader := &snapshotGroupBindingReader{group: group}
			snapshot := schedulercore.NewSnapshotService(nil, nil, nil, nil, nil, schedulercore.SnapshotBindings{})
			var read func(context.Context, *int64) policy.EffectiveSettings
			switch platform {
			case "gateway":
				gateway := newGenericSelectionForTest(GenericDependencies{
					Reads:  Reads{Groups: reader, Snapshot: schedulerredis.NewSnapshotReader(snapshot)},
					Shared: Shared{},
				}, nil)

				read = gateway.advancedSchedulerEffectiveSettingsForRequest
			case "gemini":
				gateway := newGeminiSelectionForTest(
					GeminiDependencies{
						Reads:  Reads{Groups: reader, Snapshot: schedulerredis.NewSnapshotReader(snapshot)},
						Shared: Shared{},
					}, nil)

				read = gateway.advancedSchedulerEffectiveSettingsForRequest
			case "openai":
				gateway := newCompatibleSelectionForTest(CompatibleDependencies{
					Reads:  Reads{Groups: reader, Snapshot: schedulerredis.NewSnapshotReader(snapshot)},
					Shared: Shared{},
				}, nil)

				read = gateway.advancedSchedulerEffectiveSettingsForRequest
			}
			settings := read(context.Background(), &group.ID)
			require.Equal(t, topK, settings.TopK)
			require.Equal(t, 1, reader.calls)
		})
	}
}
