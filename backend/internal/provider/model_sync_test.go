package provider

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A live model adapter may mutate its input or delay returning after shutdown
// cancellation. A blocked fetch makes those effects observable, checking that
// the caller's credentials stay isolated and that stop rejects another query
// while retaining its first timeout result.

// TestModelSyncOwnsInflightQueryAndInput blocks a fetch after mutating its
// copied credential map, then times out shutdown. The original token stays
// intact, later queries fail as stopped, and the released query sees
// cancellation.
func TestModelSyncOwnsInflightQueryAndInput(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	completed := make(chan error, 1)
	core := NewModelSyncService(func(ctx context.Context, v *Record) ([]string, error) {
		v.Credentials["access_token"] = "changed-by-adapter"
		close(entered)
		<-release
		return nil, ctx.Err()
	})
	input := &Record{ID: 1, Credentials: map[string]any{"access_token": "original"}}
	// Keep the adapter in flight until stop has exhausted its budget. This
	// exposes both copied input ownership and the canceled context observed
	// when the deliberately blocked adapter finally returns.
	go func() { _, err := core.Fetch(context.Background(), input); completed <- err }()
	<-entered
	require.Equal(t, "original", input.Credentials["access_token"])
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	stopErr := core.StopContext(ctx)
	require.ErrorIs(t, stopErr, context.DeadlineExceeded)
	_, err := core.Fetch(context.Background(), input)
	require.ErrorIs(t, err, ErrRefreshStopped)
	close(release)
	require.ErrorIs(t, <-completed, context.Canceled)
	require.Equal(t, stopErr, core.StopContext(context.Background()))
}
