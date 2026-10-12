package requeststate

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestResponseToolsSessionUpdateKeepsActiveTurn publishes active names, mutates
// the caller map, and concurrently replaces session names. Active output must
// retain its copied name while the session slot receives the new name and a
// separate request stays empty.
func TestResponseToolsSessionUpdateKeepsActiveTurn(t *testing.T) {
	state := &ResponseTools{}
	input := map[string]string{"alias": "active"}
	state.SetCodexNames(false, input)
	input["alias"] = "caller changed"
	var tasks sync.WaitGroup
	// Concurrent session publications exercise the separate slot while every task
	// reads the earlier immutable active map; wait for all publications before
	// checking the final session.
	for range 32 {
		tasks.Go(func() {
			state.SetCodexNames(true, map[string]string{"alias": "next"})
			require.Equal(t, "active", state.CodexNames(false)["alias"])
		})
	}
	tasks.Wait()
	require.Equal(t, "active", state.CodexNames(false)["alias"])
	require.Equal(t, "next", state.CodexNames(true)["alias"])
	other := &ResponseTools{}
	require.Nil(t, other.CodexNames(false))
}
