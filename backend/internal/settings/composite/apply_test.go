package composite

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestApplicationsUseOneCommittedSnapshot checks that every application receives the same persisted snapshot.
func TestApplicationsUseOneCommittedSnapshot(t *testing.T) {
	reads := 0
	var order []string
	value := &Snapshot{SiteName: "persisted", CreativeWorkerCount: 7}
	changes := ApplicationsForUpdate(func(context.Context) (*Snapshot, error) { reads++; return value, nil }, []Application{
		{Module: "gateway", Apply: func(_ context.Context, s *Snapshot) error {
			require.Same(t, value, s)
			order = append(order, "gateway")
			return nil
		}},
		{Module: "site", Apply: func(_ context.Context, s *Snapshot) error {
			require.Equal(t, "persisted", s.SiteName)
			order = append(order, "site")
			return nil
		}},
		{Module: "creative", Apply: func(_ context.Context, s *Snapshot) error {
			require.Equal(t, 7, s.CreativeWorkerCount)
			order = append(order, "creative")
			return nil
		}},
	})
	for _, change := range changes {
		require.NoError(t, change.Apply(context.Background()))
	}
	require.Equal(t, 1, reads)
	require.Equal(t, []string{"gateway", "site", "creative"}, order)
}

// TestApplicationsDoNotPublishAfterReadFailure checks that a failed read returning nil skips module application.
func TestApplicationsDoNotPublishAfterReadFailure(t *testing.T) {
	failed := errors.New("read failed")
	applied := false
	changes := ApplicationsForUpdate(func(context.Context) (*Snapshot, error) { return nil, failed }, []Application{{Module: "site", Apply: func(context.Context, *Snapshot) error { applied = true; return nil }}})
	require.ErrorIs(t, changes[0].Apply(context.Background()), failed)
	require.NoError(t, changes[1].Apply(context.Background()))
	require.False(t, applied)
}
