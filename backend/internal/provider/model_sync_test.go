package provider

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestModelSyncOwnsInflightQueryAndInput checks input isolation and shutdown of in-flight queries.
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
