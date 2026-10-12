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

// snapshotGroupBindingReader returns one group and counts full reads for
// override assertions.
type snapshotGroupBindingReader struct {
	Groups                // unused group reads panic
	group  *routing.Group // configured override group
	calls  int            // full lookup count
}

// GetByID increments the fixture's full-read count and returns its configured
// group pointer.
func (r *snapshotGroupBindingReader) GetByID(context.Context, int64) (*routing.Group, error) {
	r.calls++
	return r.group, nil
}

// TestSnapshotNativeBindingPreservesGroupOverrides omits request group context
// while configuring scheduling snapshots for each selector. Each reads the full
// group once and applies its Top-K override to effective settings.
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
