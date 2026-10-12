package ws

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestTurnCommitPrecedesNextAdmission blocks a concurrent response.create while
// terminal delivery holds the lifecycle lock. Successful delivery admits that
// waiter, and its reservation rejects another overlapping request.
func TestTurnCommitPrecedesNextAdmission(t *testing.T) {
	lifecycle := NewTurnLifecycle(true)
	lifecycle.BeginTerminalWrite()
	started := make(chan struct{})
	admitted := make(chan bool, 1)
	// Start admission while the terminal writer owns the mutex, then observe
	// whether its result arrives before the writer releases that lock.
	go func() { close(started); admitted <- lifecycle.BeginResponseCreate(nil) }()
	<-started
	select {
	case <-admitted:
		t.Fatal("next turn admitted before terminal commit")
	// Allow a short scheduling window for the competing admission result.
	case <-time.After(10 * time.Millisecond):
	}
	lifecycle.FinishTerminalWrite(true, nil)
	require.True(t, <-admitted)
	require.False(t, lifecycle.BeginResponseCreate(nil))
}

// TestTurnQueueFreezesInputAndPreservesOrder mutates supplied bytes and a tier
// after enqueueing two turns. Pop retains the first snapshot's asserted time,
// body, and tier, then returns FIFO order and the empty-source marker.
func TestTurnQueueFreezesInputAndPreservesOrder(t *testing.T) {
	queue := NewTurnPayloadQueue()
	tier := "priority"
	body := []byte(`{"model":"first"}`)
	started := time.Now()
	queue.Push(TurnPayload{StartedAt: started, RequestBody: body, OriginalModel: "first", ServiceTier: &tier})
	body[0] = '!'
	tier = "flex"
	queue.Push(TurnPayload{OriginalModel: "second"})
	first := queue.Pop()
	require.Equal(t, started, first.StartedAt)
	require.Equal(t, []byte(`{"model":"first"}`), first.RequestBody)
	require.Equal(t, "priority", *first.ServiceTier)
	require.Equal(t, "second", queue.Pop().OriginalModel)
	require.Equal(t, "passthrough_missing", queue.Pop().Source)
}
