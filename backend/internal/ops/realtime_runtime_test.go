package ops

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestOpsWSShutdownCancelsIdleTimer schedules the default idle stop before
// final runtime shutdown with no active sample loop. It checks Stop returns
// within one second, later idle scheduling keeps its timer nil, and repeated
// Stop completes.
func TestOpsWSShutdownCancelsIdleTimer(t *testing.T) {
	rt := NewRealtimeRuntime()

	rt.scheduleQPSWSIdleStop()
	finished := make(chan struct{})
	// Close finished only after runtime Stop returns. The select observes
	// completed shutdown rather than a state flag published before timer or
	// sampler work joined.
	go func() { rt.Stop(); close(finished) }()
	// A one-second observation budget exposes waiting for the uncancelled
	// thirty-second idle delay. Completion proceeds to the separate assertion that
	// later idle scheduling stays closed.
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("关闭仍等待三十秒空闲定时器")
	}
	rt.scheduleQPSWSIdleStop()
	rt.qpsWSIdleStopMu.Lock()
	require.Nil(t, rt.qpsWSIdleStopTimer)
	rt.qpsWSIdleStopMu.Unlock()
	rt.Stop()
}

// TestRealtimePayloadRequestIsolation publishes nonempty cached JSON and
// mutates the first Payload result. It checks another read retains the original
// first byte, protecting separate returned byte storage for readers.
func TestRealtimePayloadRequestIsolation(t *testing.T) {
	r := NewRealtimeRuntime()
	r.cache.payload.Store([]byte(`{"type":"qps_update"}`))
	first := r.Payload()
	first[0] = 'x'
	require.Equal(t, byte('{'), r.Payload()[0])
	r.Stop()
}
