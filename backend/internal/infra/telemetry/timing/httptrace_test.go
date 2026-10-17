package timing

import (
	"context"
	"errors"
	"net/http/httptrace"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestHTTPTraceRecordsIntoUsageCollector invokes transport hooks for one exchange.
// The shared snapshot must retain real zero values and successful write milestones.
func TestHTTPTraceRecordsIntoUsageCollector(t *testing.T) {
	ctx, stages := WithStages(t.Context(), time.Now(), nil)
	trace := httptrace.ContextClientTrace(BeginHTTPTrace(ctx))
	trace.GetConn("upstream.test")
	trace.GotConn(httptrace.GotConnInfo{Reused: true})
	trace.WroteRequest(httptrace.WroteRequestInfo{})
	trace.GotFirstResponseByte()
	snapshot := stages.Snapshot()
	for _, key := range []string{"upstream_get_conn_ms", "upstream_got_conn_ms", "upstream_wrote_request_ms", "upstream_first_response_byte_ms"} {
		require.Contains(t, snapshot, key)
	}
	require.Equal(t, int64(1), snapshot["upstream_connection_reused"])
	require.Equal(t, int64(1), snapshot["upstream_attempt_count"])
}

// TestHTTPTraceRejectsLateAttemptCallbacks sends a delayed acquired-connection event
// from a failed provider after replacement. No timestamp or reuse flag may cross attempts.
func TestHTTPTraceRejectsLateAttemptCallbacks(t *testing.T) {
	ctx, stages := WithStages(t.Context(), time.Now(), nil)
	previous := httptrace.ContextClientTrace(BeginHTTPTrace(ctx))
	previous.GetConn("old.test")
	next := httptrace.ContextClientTrace(BeginHTTPTrace(ctx))
	next.GetConn("new.test")
	previous.GotConn(httptrace.GotConnInfo{Reused: true})
	previous.GotFirstResponseByte()
	snapshot := stages.Snapshot()
	require.NotContains(t, snapshot, "upstream_got_conn_ms")
	require.NotContains(t, snapshot, "upstream_first_response_byte_ms")
	require.NotContains(t, snapshot, "upstream_connection_reused")
	next.GotConn(httptrace.GotConnInfo{})
	require.Contains(t, stages.Snapshot(), "upstream_got_conn_ms")
	require.Equal(t, int64(2), stages.Snapshot()["upstream_attempt_count"])
}

// TestHTTPTraceConnectionRecoveryKeepsOneSequence replaces a broken reused connection
// within one exchange. Successful stage offsets belong to the replacement connection,
// while counters and the observed write error retain evidence of the recovery.
func TestHTTPTraceConnectionRecoveryKeepsOneSequence(t *testing.T) {
	ctx, stages := WithStages(t.Context(), time.Now(), nil)
	trace := httptrace.ContextClientTrace(BeginHTTPTrace(ctx))
	trace.GetConn("upstream.test")
	trace.GotConn(httptrace.GotConnInfo{Reused: true})
	trace.WroteRequest(httptrace.WroteRequestInfo{Err: errors.New("closed connection")})
	require.NotContains(t, stages.Snapshot(), "upstream_wrote_request_ms")
	trace.GetConn("upstream.test")
	require.NotContains(t, stages.Snapshot(), "upstream_got_conn_ms")
	trace.GotConn(httptrace.GotConnInfo{})
	trace.WroteRequest(httptrace.WroteRequestInfo{})
	snapshot := stages.Snapshot()
	require.Equal(t, int64(2), snapshot["upstream_get_conn_count"])
	require.Equal(t, int64(1), snapshot["upstream_wrote_request_error"])
	require.Contains(t, snapshot, "upstream_wrote_request_ms")
	require.Contains(t, snapshot, "upstream_connection_reused")
	require.Zero(t, snapshot["upstream_connection_reused"])
}

// TestHTTPTraceLeavesUnobservedRequestsAlone excludes auxiliary requests that have
// no usage collector. Beginning such a request must not install another timing model.
func TestHTTPTraceLeavesUnobservedRequestsAlone(t *testing.T) {
	require.Nil(t, httptrace.ContextClientTrace(BeginHTTPTrace(context.Background())))
}
