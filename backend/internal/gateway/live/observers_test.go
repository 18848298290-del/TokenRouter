package live

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestObserverStopCancelsAndWaitsForEnteredTask checks that Stop waits for cleanup and rejects new observers.
func TestObserverStopCancelsAndWaitsForEnteredTask(t *testing.T) {
	var mu sync.Mutex
	var stopped bool
	var cancels map[string]context.CancelFunc
	var wg sync.WaitGroup
	state := ObserverState{Mutex: &mu, Stopped: &stopped, Cancels: &cancels, Wait: &wg}
	run, finish, ok := state.Begin("observer")
	require.True(t, ok)
	stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	stopDone := make(chan error, 1)
	go func() { stopDone <- state.Stop(stopCtx) }()
	select {
	case <-run.Done():
	case <-stopCtx.Done():
		t.Fatal("observer context not cancelled")
	}
	select {
	case <-stopDone:
		t.Fatal("stop completed before observer returned")
	default:
	}
	_, _, accepted := state.Begin("late")
	require.False(t, accepted)
	finish()
	require.NoError(t, <-stopDone)
	require.NoError(t, state.Stop(context.Background()))
}
