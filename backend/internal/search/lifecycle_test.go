package search

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Search shutdown closes admission while accepted work remains registered. This
// scenario waits with a short context, then completes the retained operation
// twice and waits again, exposing permanent stop and idempotent completion
// behavior together.

// TestSearchStopWaitsAndRejectsNewWork stops a group with one unfinished
// operation using a short deadline. Admission stays closed after timeout;
// repeated completion allows a later stop wait to succeed.
func TestSearchStopWaitsAndRejectsNewWork(t *testing.T) {
	group := NewWorkGroup()
	done, err := group.Begin()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	require.ErrorIs(t, group.Stop(ctx), context.DeadlineExceeded)
	_, err = group.Begin()
	require.Error(t, err)
	// Invoke the same completion twice after admission closed; its once guard must
	// keep the stopped group's count and close decision valid.
	done()
	done()
	require.NoError(t, group.Stop(context.Background()))
}
