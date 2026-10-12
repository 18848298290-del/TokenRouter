package ws

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestTurnCommitPrecedesNextAdmission checks that the next turn waits until the terminal write commits.
func TestTurnCommitPrecedesNextAdmission(t *testing.T) {
	lifecycle := NewTurnLifecycle(true)
	lifecycle.BeginTerminalWrite()
	started := make(chan struct{})
	admitted := make(chan bool, 1)
	go func() { close(started); admitted <- lifecycle.BeginResponseCreate(nil) }()
	<-started
	select {
	case <-admitted:
		t.Fatal("next turn admitted before terminal commit")
	case <-time.After(10 * time.Millisecond):
	}
	lifecycle.FinishTerminalWrite(true, nil)
	require.True(t, <-admitted)
	require.False(t, lifecycle.BeginResponseCreate(nil))
}

// TestTurnQueueFreezesInputAndPreservesOrder checks that reused input buffers cannot change stored timing or model metadata.
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
