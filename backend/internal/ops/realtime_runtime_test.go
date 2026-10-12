package ops

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestOpsWSShutdownCancelsIdleTimer checks that the idle timer cannot delay shutdown or restart sampling afterward.
func TestOpsWSShutdownCancelsIdleTimer(t *testing.T) {
	rt := NewRealtimeRuntime()

	rt.scheduleQPSWSIdleStop()
	finished := make(chan struct{})
	go func() { rt.Stop(); close(finished) }()
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

// TestRealtimePayloadRequestIsolation checks that returned payloads are copies that callers can modify independently.
func TestRealtimePayloadRequestIsolation(t *testing.T) {
	r := NewRealtimeRuntime()
	r.cache.payload.Store([]byte(`{"type":"qps_update"}`))
	first := r.Payload()
	first[0] = 'x'
	require.Equal(t, byte('{'), r.Payload()[0])
	r.Stop()
}
