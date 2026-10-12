package openai_ws_v2

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// MetricsSnapshot counts usage parsing failures.
type MetricsSnapshot struct {
	UsageParseFailureTotal int64
}

type delayedReadFrameConn struct {
	base       FrameConn
	firstDelay time.Duration
	once       sync.Once
}
type readStartSpyFrameConn struct {
	base      FrameConn
	started   chan struct{}
	startOnce sync.Once
}
type closeSpyFrameConn struct {
	closeCalls atomic.Int32
}
type eofReplacementFrameConn struct {
	FrameConn
	err error
}
type cancelJoinProbeFrameConn struct {
	readStarted  chan struct{}
	readCanceled chan struct{}
	allowReturn  chan struct{}
	readReturned chan struct{}
	startOnce    sync.Once
	cancelOnce   sync.Once
	returnOnce   sync.Once
}

// errorOnWriteFrameConn fails every write to test first-frame failure handling.
type errorOnWriteFrameConn struct{}

// SnapshotMetrics returns the current passthrough usage-parse failure count.
func SnapshotMetrics() MetricsSnapshot {
	return MetricsSnapshot{
		UsageParseFailureTotal: passthroughUsageParseFailureTotal.Load(),
	}
}

func TestRunClientToUpstream_ErrorPaths(t *testing.T) {
	t.Parallel()

	t.Run("read client eof", func(t *testing.T) {
		t.Parallel()

		exitCh := make(chan relayExitSignal, 1)
		runClientToUpstream(
			context.Background(),
			newPassthroughTestFrameConn(nil, true),
			nil,
			func(_ coderws.MessageType, _ []byte) error { return nil },
			func() {},
			nil,
			nil,
			exitCh,
		)
		sig := <-exitCh
		require.Equal(t, "read_client", sig.stage)
		require.True(t, sig.graceful)
	})

	t.Run("write upstream failed", func(t *testing.T) {
		t.Parallel()

		exitCh := make(chan relayExitSignal, 1)
		runClientToUpstream(
			context.Background(),
			newPassthroughTestFrameConn([]passthroughTestFrame{
				{msgType: coderws.MessageText, payload: []byte(`{"x":1}`)},
			}, true),
			nil,
			func(_ coderws.MessageType, _ []byte) error { return errors.New("boom") },
			func() {},
			nil,
			nil,
			exitCh,
		)
		sig := <-exitCh
		require.Equal(t, "write_upstream", sig.stage)
		require.False(t, sig.graceful)
	})

	t.Run("forwarded counter and trace callback", func(t *testing.T) {
		t.Parallel()

		exitCh := make(chan relayExitSignal, 1)
		forwarded := &atomic.Int64{}
		traces := make([]RelayTraceEvent, 0, 2)
		runClientToUpstream(
			context.Background(),
			newPassthroughTestFrameConn([]passthroughTestFrame{
				{msgType: coderws.MessageText, payload: []byte(`{"x":1}`)},
			}, true),
			nil,
			func(_ coderws.MessageType, _ []byte) error { return nil },
			func() {},
			forwarded,
			func(event RelayTraceEvent) {
				traces = append(traces, event)
			},
			exitCh,
		)
		sig := <-exitCh
		require.Equal(t, "read_client", sig.stage)
		require.Equal(t, int64(1), forwarded.Load())
		require.NotEmpty(t, traces)
	})
}

func TestRunUpstreamToClient_ErrorAndDropPaths(t *testing.T) {
	t.Parallel()

	t.Run("read upstream eof", func(t *testing.T) {
		t.Parallel()

		exitCh := make(chan relayExitSignal, 1)
		drop := &atomic.Bool{}
		drop.Store(false)
		runUpstreamToClient(
			context.Background(),
			newPassthroughTestFrameConn(nil, true),
			func(_ coderws.MessageType, _ []byte) error { return nil },
			time.Now(),
			time.Now,
			&relayState{},
			nil,
			nil,
			nil,
			nil,
			nil,
			nil,
			nil,
			drop,
			nil,
			nil,
			func() {},
			nil,
			exitCh,
		)
		sig := <-exitCh
		require.Equal(t, "read_upstream", sig.stage)
		require.True(t, sig.graceful)
	})

	t.Run("write client failed", func(t *testing.T) {
		t.Parallel()

		exitCh := make(chan relayExitSignal, 1)
		drop := &atomic.Bool{}
		drop.Store(false)
		runUpstreamToClient(
			context.Background(),
			newPassthroughTestFrameConn([]passthroughTestFrame{
				{msgType: coderws.MessageText, payload: []byte(`{"type":"response.output_text.delta","delta":"x"}`)},
			}, true),
			func(_ coderws.MessageType, _ []byte) error { return errors.New("write failed") },
			time.Now(),
			time.Now,
			&relayState{},
			nil,
			nil,
			nil,
			nil,
			nil,
			nil,
			nil,
			drop,
			nil,
			nil,
			func() {},
			nil,
			exitCh,
		)
		sig := <-exitCh
		require.Equal(t, "write_client", sig.stage)
	})

	t.Run("drop downstream and stop on terminal", func(t *testing.T) {
		t.Parallel()

		exitCh := make(chan relayExitSignal, 1)
		drop := &atomic.Bool{}
		drop.Store(true)
		dropped := &atomic.Int64{}
		runUpstreamToClient(
			context.Background(),
			newPassthroughTestFrameConn([]passthroughTestFrame{
				{
					msgType: coderws.MessageText,
					payload: []byte(`{"type":"response.completed","response":{"id":"resp_drop","usage":{"input_tokens":1,"output_tokens":1}}}`),
				},
			}, true),
			func(_ coderws.MessageType, _ []byte) error { return nil },
			time.Now(),
			time.Now,
			&relayState{},
			nil,
			nil,
			nil,
			nil,
			nil,
			nil,
			nil,
			drop,
			nil,
			dropped,
			func() {},
			nil,
			exitCh,
		)
		sig := <-exitCh
		require.Equal(t, "drain_terminal", sig.stage)
		require.True(t, sig.graceful)
		require.Equal(t, int64(1), dropped.Load())
	})
}

func TestRunIdleWatchdog_NoTimeoutWhenDisabled(t *testing.T) {
	t.Parallel()

	exitCh := make(chan relayExitSignal, 1)
	lastActivity := &atomic.Int64{}
	lastActivity.Store(time.Now().UnixNano())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go runIdleWatchdog(ctx, time.Now, 0, lastActivity, nil, exitCh)
	select {
	case <-exitCh:
		t.Fatal("unexpected idle timeout signal")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestHelperFunctionsCoverage(t *testing.T) {
	t.Parallel()

	require.Equal(t, "text", relayMessageTypeString(coderws.MessageText))
	require.Equal(t, "binary", relayMessageTypeString(coderws.MessageBinary))
	require.Contains(t, relayMessageTypeString(coderws.MessageType(99)), "unknown(")

	require.Equal(t, "", relayErrorString(nil))
	require.Equal(t, "x", relayErrorString(errors.New("x")))

	require.True(t, isDisconnectError(io.EOF))
	require.True(t, isDisconnectError(net.ErrClosed))
	require.True(t, isDisconnectError(context.Canceled))
	require.True(t, isDisconnectError(coderws.CloseError{Code: coderws.StatusGoingAway}))
	require.True(t, isDisconnectError(errors.New("broken pipe")))
	require.False(t, isDisconnectError(errors.New("unrelated")))

	require.True(t, isTokenEvent("response.output_text.delta"))
	require.True(t, isTokenEvent("response.output_audio.delta"))
	require.False(t, isTokenEvent("response.completed"))
	require.False(t, isTokenEvent(""))
	require.False(t, isTokenEvent("response.created"))

	require.Equal(t, 2*time.Second, minDuration(2*time.Second, 5*time.Second))
	require.Equal(t, 2*time.Second, minDuration(5*time.Second, 2*time.Second))
	require.Equal(t, 5*time.Second, minDuration(0, 5*time.Second))
	require.Equal(t, 2*time.Second, minDuration(2*time.Second, 0))

	ch := make(chan relayExitSignal, 1)
	ch <- relayExitSignal{stage: "ok"}
	sig, ok := waitRelayExit(ch, 10*time.Millisecond)
	require.True(t, ok)
	require.Equal(t, "ok", sig.stage)
	ch <- relayExitSignal{stage: "ok2"}
	sig, ok = waitRelayExit(ch, 0)
	require.True(t, ok)
	require.Equal(t, "ok2", sig.stage)
	_, ok = waitRelayExit(ch, 10*time.Millisecond)
	require.False(t, ok)

	n, ok := parseUsageIntField(gjson.Get(`{"n":3}`, "n"), true)
	require.True(t, ok)
	require.Equal(t, 3, n)
	_, ok = parseUsageIntField(gjson.Get(`{"n":"x"}`, "n"), true)
	require.False(t, ok)
	n, ok = parseUsageIntField(gjson.Result{}, false)
	require.True(t, ok)
	require.Equal(t, 0, n)
	_, ok = parseUsageIntField(gjson.Result{}, true)
	require.False(t, ok)
}

func TestParseUsageAndEnrichCoverage(t *testing.T) {
	t.Parallel()

	state := &relayState{}
	parseUsageAndAccumulate(state, []byte(`{"type":"response.completed","response":{"usage":{"input_tokens":"bad"}}}`), "response.completed", nil)
	require.Equal(t, 0, state.usage.InputTokens)

	parseUsageAndAccumulate(
		state,
		[]byte(`{"type":"response.completed","response":{"usage":{"input_tokens":9,"output_tokens":"bad","input_tokens_details":{"cached_tokens":2}}}}`),
		"response.completed",
		nil,
	)
	require.Equal(t, 0, state.usage.InputTokens, "部分字段解析失败时不应累加 usage")
	require.Equal(t, 0, state.usage.OutputTokens)
	require.Equal(t, 0, state.usage.CacheReadInputTokens)

	parseUsageAndAccumulate(
		state,
		[]byte(`{"type":"response.completed","response":{"usage":{"input_tokens_details":{"cached_tokens":2}}}}`),
		"response.completed",
		nil,
	)
	require.Equal(t, 0, state.usage.InputTokens, "必填 usage 字段缺失时不应累加 usage")
	require.Equal(t, 0, state.usage.OutputTokens)
	require.Equal(t, 0, state.usage.CacheReadInputTokens)

	parseUsageAndAccumulate(state, []byte(`{"type":"response.completed","response":{"usage":{"input_tokens":2,"output_tokens":1,"input_tokens_details":{"cached_tokens":1,"cache_write_tokens":4},"output_tokens_details":{"image_tokens":3}}}}`), "response.completed", nil)
	finalizeRelayTurnUsage(state)
	require.Equal(t, 2, state.usage.InputTokens)
	require.Equal(t, 1, state.usage.OutputTokens)
	require.Equal(t, 1, state.usage.CacheReadInputTokens)
	require.Equal(t, 4, state.usage.CacheCreationInputTokens)
	require.Equal(t, 3, state.usage.ImageOutputTokens)

	result := &RelayResult{}
	enrichResult(result, state, 5*time.Millisecond)
	require.Equal(t, state.usage.InputTokens, result.Usage.InputTokens)
	require.Equal(t, state.usage.CacheCreationInputTokens, result.Usage.CacheCreationInputTokens)
	require.Equal(t, state.usage.ImageOutputTokens, result.Usage.ImageOutputTokens)
	require.Equal(t, 5*time.Millisecond, result.Duration)
	parseUsageAndAccumulate(state, []byte(`{"type":"response.in_progress","response":{"usage":{"input_tokens":9}}}`), "response.in_progress", nil)
	require.Equal(t, 2, state.usage.InputTokens)
	enrichResult(nil, state, 0)
}

func TestParseUsageAndAccumulateIncludesIndependentReasoningTokens(t *testing.T) {
	t.Parallel()

	state := &relayState{}
	got := parseUsageAndAccumulate(
		state,
		[]byte(`{"type":"response.completed","response":{"usage":{"input_tokens":32,"output_tokens":9,"total_tokens":151,"output_tokens_details":{"reasoning_tokens":110}}}}`),
		"response.completed",
		nil,
	)
	require.Equal(t, 32, got.InputTokens)
	require.Equal(t, 119, got.OutputTokens)

	state = &relayState{}
	got = parseUsageAndAccumulate(
		state,
		[]byte(`{"type":"response.completed","response":{"usage":{"input_tokens":32,"output_tokens":119,"total_tokens":151,"output_tokens_details":{"reasoning_tokens":110}}}}`),
		"response.completed",
		nil,
	)
	require.Equal(t, 119, got.OutputTokens, "inclusive Responses output must not double-count reasoning")
}

func TestParseUsageAndAccumulateAcceptsChatUsageAliases(t *testing.T) {
	t.Parallel()

	state := &relayState{}
	got := parseUsageAndAccumulate(
		state,
		[]byte(`{"type":"response.done","response":{"usage":{"prompt_tokens":12,"completion_tokens":6,"prompt_tokens_details":{"cached_tokens":4},"completion_tokens_details":{"image_tokens":2}}}}`),
		"response.done",
		nil,
	)
	finalizeRelayTurnUsage(state)
	require.Equal(t, 12, got.InputTokens)
	require.Equal(t, 6, got.OutputTokens)
	require.Equal(t, 4, got.CacheReadInputTokens)
	require.Equal(t, 2, got.ImageOutputTokens)
	require.Equal(t, got, state.usage)
}

func TestRelayUsageTerminalWithoutUsageKeepsFallback(t *testing.T) {
	t.Parallel()

	state := &relayState{}
	parseUsageAndAccumulate(state, []byte(`{"type":"response.in_progress","usage":{"input_tokens":9,"output_tokens":4}}`), "response.in_progress", nil)
	parseUsageAndAccumulate(state, []byte(`{"type":"response.completed","response":{"id":"resp_1"}}`), "response.completed", nil)

	turnUsage := finalizeRelayTurnUsage(state)
	require.Equal(t, Usage{InputTokens: 9, OutputTokens: 4}, turnUsage)
	require.Equal(t, turnUsage, state.usage)
}

func TestRelayUsageTerminalZeroKeepsFallback(t *testing.T) {
	t.Parallel()

	state := &relayState{}
	parseUsageAndAccumulate(state, []byte(`{"type":"response.in_progress","usage":{"input_tokens":9,"output_tokens":4}}`), "response.in_progress", nil)
	parseUsageAndAccumulate(state, []byte(`{"type":"response.completed","usage":{"input_tokens":0,"output_tokens":0}}`), "response.completed", nil)

	require.Equal(t, Usage{InputTokens: 9, OutputTokens: 4}, finalizeRelayTurnUsage(state))
	require.Equal(t, Usage{InputTokens: 9, OutputTokens: 4}, state.usage)
}

func TestRelayUsageTerminalNonZeroReplacesFallbackAsWhole(t *testing.T) {
	t.Parallel()

	state := &relayState{}
	parseUsageAndAccumulate(state, []byte(`{"type":"response.in_progress","usage":{"input_tokens":9,"output_tokens":4,"input_tokens_details":{"cached_tokens":2}}}`), "response.in_progress", nil)
	parseUsageAndAccumulate(state, []byte(`{"type":"response.completed","usage":{"input_tokens":3,"output_tokens":0,"input_tokens_details":{"cached_tokens":0}}}`), "response.completed", nil)

	require.Equal(t, Usage{InputTokens: 3}, finalizeRelayTurnUsage(state))
	require.Equal(t, Usage{InputTokens: 3}, state.usage)
}

func TestObserveUpstreamMessageBareErrorClearsTurnStateAndFinalizesUsageOnce(t *testing.T) {
	t.Parallel()

	now := time.Unix(200, 0)
	state := &relayState{}
	state.setPendingTurnStartedAt(now.Add(-2 * time.Second))
	timing := openAIWSRelayGetOrInitTurnTiming(state, "resp_active", now.Add(-time.Second))
	require.NotNil(t, timing)
	state.setPendingTurnStartedAt(now.Add(-500 * time.Millisecond))
	parseUsageAndAccumulate(state, []byte(`{"type":"response.in_progress","usage":{"input_tokens":9,"output_tokens":4}}`), "response.in_progress", nil)

	observed := observeUpstreamMessage(
		state,
		[]byte(`{"type":"error","usage":{"input_tokens":3,"output_tokens":1},"error":{"message":"failed"}}`),
		now.Add(-3*time.Second),
		func() time.Time { return now },
		nil,
		nil,
	)

	require.False(t, observed.terminal, "bare error settlement is deferred in case response.failed follows")
	require.Equal(t, "error", observed.eventType)
	observed = finalizePendingBareError(state, now)
	require.True(t, observed.terminal)
	require.Equal(t, Usage{InputTokens: 3, OutputTokens: 1}, observed.usage)
	require.Equal(t, observed.usage, state.usage)
	require.Equal(t, Usage{}, state.turnUsage)
	require.Nil(t, state.activeTurn)
	require.Empty(t, state.turnTimingByID)
	// The error is bound to the active turn id, so settlement must not consume a
	// later pending start that belongs to the next turn.
	require.Equal(t, now.Add(-500*time.Millisecond), state.consumePendingTurnStartedAt())

	// Re-observing and settling a later terminal without usage must not re-add the prior turn.
	observeUpstreamMessage(state, []byte(`{"type":"error","error":{"message":"again"}}`), now, func() time.Time { return now }, nil, nil)
	finalizePendingBareError(state, now)
	require.Equal(t, Usage{InputTokens: 3, OutputTokens: 1}, state.usage)
}

func TestObserveUpstreamMessageErrorThenFailedSettlesUsageOnce(t *testing.T) {
	t.Parallel()

	now := time.Unix(300, 0)
	state := &relayState{}
	state.setPendingTurnStartedAt(now.Add(-time.Second))
	openAIWSRelayGetOrInitTurnTiming(state, "resp_1", now)
	parseUsageAndAccumulate(state, []byte(`{"type":"response.in_progress","usage":{"input_tokens":9,"output_tokens":4}}`), "response.in_progress", nil)

	errorObserved := observeUpstreamMessage(
		state,
		[]byte(`{"type":"error","error":{"code":"server_is_overloaded","message":"overloaded"}}`),
		now,
		func() time.Time { return now },
		nil,
		nil,
	)
	require.False(t, errorObserved.terminal)
	require.NotNil(t, state.pendingBareError)
	require.Equal(t, Usage{}, state.usage)

	failedObserved := observeUpstreamMessage(
		state,
		[]byte(`{"type":"response.failed","response":{"id":"resp_1","usage":{"input_tokens":7,"output_tokens":2}}}`),
		now,
		func() time.Time { return now },
		nil,
		nil,
	)
	require.True(t, failedObserved.terminal)
	require.Equal(t, Usage{InputTokens: 7, OutputTokens: 2}, failedObserved.usage)
	require.Equal(t, failedObserved.usage, state.usage)
	require.Nil(t, state.pendingBareError)
}

func TestObserveUpstreamMessageBareErrorBeforeNextCompletedKeepsBothTurns(t *testing.T) {
	t.Parallel()

	now := time.Unix(400, 0)
	state := &relayState{}
	parseUsageAndAccumulate(state, []byte(`{"type":"response.in_progress","usage":{"input_tokens":5,"output_tokens":1}}`), "response.in_progress", nil)
	bare := observeUpstreamMessage(
		state,
		[]byte(`{"type":"error","error":{"message":"first turn failed"}}`),
		now,
		func() time.Time { return now },
		nil,
		nil,
	)
	require.False(t, bare.terminal)
	bare = finalizePendingBareError(state, now)
	require.True(t, bare.terminal)
	require.Equal(t, Usage{InputTokens: 5, OutputTokens: 1}, bare.usage)

	completed := observeUpstreamMessage(
		state,
		[]byte(`{"type":"response.completed","response":{"id":"resp_next","usage":{"input_tokens":3,"output_tokens":2}}}`),
		now,
		func() time.Time { return now },
		nil,
		nil,
	)
	require.True(t, completed.terminal)
	require.Equal(t, Usage{InputTokens: 3, OutputTokens: 2}, completed.usage)
	require.Equal(t, Usage{InputTokens: 8, OutputTokens: 3}, state.usage)
}

func TestOpenAICacheCreationTokensFromUsageNestedZeroWins(t *testing.T) {
	t.Parallel()

	usage := gjson.Parse(`{"input_tokens_details":{"cache_write_tokens":0},"cache_creation_input_tokens":19}`)
	require.Zero(t, openAICacheCreationTokensFromUsage(usage))
}

func TestEmitTurnCompleteCoverage(t *testing.T) {
	t.Parallel()

	// Nonterminal events do not complete a turn.
	called := 0
	emitTurnComplete(func(turn RelayTurnResult) {
		called++
	}, &relayState{requestModel: "gpt-5"}, observedUpstreamEvent{
		terminal:   false,
		eventType:  "response.output_text.delta",
		responseID: "resp_ignored",
		usage:      Usage{InputTokens: 1},
	})
	require.Equal(t, 0, called)

	// A non-error terminal event requires a response ID to complete a turn.
	emitTurnComplete(func(turn RelayTurnResult) {
		called++
	}, &relayState{requestModel: "gpt-5"}, observedUpstreamEvent{
		terminal:  true,
		eventType: "response.completed",
	})
	require.Equal(t, 0, called)

	// Bare error legitimately has no response_id; it must still settle the turn
	// or a later completed turn would suppress the adapter's aggregate fallback.
	var bareError RelayTurnResult
	emitTurnComplete(func(turn RelayTurnResult) {
		called++
		bareError = turn
	}, &relayState{requestModel: "gpt-5"}, observedUpstreamEvent{
		terminal:  true,
		eventType: "error",
		usage:     Usage{InputTokens: 4, OutputTokens: 1},
	})
	require.Equal(t, 1, called)
	require.Empty(t, bareError.RequestID)
	require.Equal(t, "error", bareError.TerminalEventType)
	require.Equal(t, Usage{InputTokens: 4, OutputTokens: 1}, bareError.Usage)

	// A terminal event with a response ID completes a turn; nil state leaves its request model empty.
	var got RelayTurnResult
	emitTurnComplete(func(turn RelayTurnResult) {
		called++
		got = turn
	}, nil, observedUpstreamEvent{
		terminal:   true,
		eventType:  "response.completed",
		responseID: "resp_emit",
		usage:      Usage{InputTokens: 2, OutputTokens: 3},
	})
	require.Equal(t, 2, called)
	require.Equal(t, "resp_emit", got.RequestID)
	require.Equal(t, "response.completed", got.TerminalEventType)
	require.Equal(t, 2, got.Usage.InputTokens)
	require.Equal(t, 3, got.Usage.OutputTokens)
	require.Equal(t, "", got.RequestModel)
}

func TestIsDisconnectErrorCoverage_CloseStatusesAndMessageBranches(t *testing.T) {
	t.Parallel()

	require.True(t, isDisconnectError(coderws.CloseError{Code: coderws.StatusNormalClosure}))
	require.True(t, isDisconnectError(coderws.CloseError{Code: coderws.StatusNoStatusRcvd}))
	require.True(t, isDisconnectError(coderws.CloseError{Code: coderws.StatusAbnormalClosure}))
	require.True(t, isDisconnectError(errors.New("connection reset by peer")))
	require.False(t, isDisconnectError(errors.New("   ")))
}

func TestIsTokenEventCoverageBranches(t *testing.T) {
	t.Parallel()

	require.False(t, isTokenEvent("response.in_progress"))
	require.False(t, isTokenEvent("response.output_item.added"))
	require.True(t, isTokenEvent("response.output_audio.delta"))
	require.True(t, isTokenEvent("response.function_call_arguments.delta"))
	require.True(t, isTokenEvent("response.reasoning_summary_text.delta"))
	require.True(t, isTokenEvent("response.output_text.done"))
	require.True(t, isTokenEvent("response.function_call_arguments.done"))
	require.False(t, isTokenEvent("response.output"))
	require.False(t, isTokenEvent("response.output_audio.done"))
	require.False(t, isTokenEvent("response.content_part.done"))
	require.False(t, isTokenEvent("response.output_item.done"))
	require.False(t, isTokenEvent("response.output_text.annotation.added"))
	require.False(t, isTokenEvent("response.done"))
}

func TestTerminalAndTokenEventSetsAreDisjoint(t *testing.T) {
	t.Parallel()

	for _, eventType := range []string{
		"response.completed",
		"response.done",
		"response.failed",
		"response.incomplete",
		"response.cancelled",
		"response.canceled",
	} {
		require.True(t, isTerminalEvent(eventType), eventType)
		require.False(t, isTokenEvent(eventType), eventType)
	}
}

func TestShouldParseUsageTerminalEvents(t *testing.T) {
	t.Parallel()

	for _, eventType := range []string{
		"response.completed",
		"response.done",
		"response.failed",
		"response.incomplete",
		"response.cancelled",
		"response.canceled",
	} {
		require.True(t, shouldParseUsage(eventType), eventType)
	}
	require.False(t, shouldParseUsage("response.output_text.delta"))
	require.False(t, shouldParseUsage(""))
}

func TestRelayTurnTimingHelpersCoverage(t *testing.T) {
	t.Parallel()

	now := time.Unix(100, 0)
	require.Nil(t, openAIWSRelayGetOrInitTurnTiming(nil, "resp_nil", now))
	_, ok := openAIWSRelayDeleteTurnTiming(nil, "resp_nil")
	require.False(t, ok)

	state := &relayState{}
	timing := openAIWSRelayGetOrInitTurnTiming(state, "resp_a", now)
	require.NotNil(t, timing)
	require.Equal(t, now, timing.startAt)

	// Repeated lookup keeps the initial start time.
	timing2 := openAIWSRelayGetOrInitTurnTiming(state, "resp_a", now.Add(5*time.Second))
	require.NotNil(t, timing2)
	require.Equal(t, now, timing2.startAt)

	deleted, ok := openAIWSRelayDeleteTurnTiming(state, "resp_a")
	require.True(t, ok)
	require.Equal(t, now, deleted.startAt)

	_, ok = openAIWSRelayDeleteTurnTiming(state, "resp_a")
	require.False(t, ok)
}

func TestObserveUpstreamMessage_ResponseIDFallbackPolicy(t *testing.T) {
	t.Parallel()

	state := &relayState{requestModel: "gpt-5"}
	startAt := time.Unix(0, 0)
	now := startAt
	nowFn := func() time.Time {
		now = now.Add(5 * time.Millisecond)
		return now
	}

	// A nonterminal top-level ID identifies the event, not the response.
	observed := observeUpstreamMessage(
		state,
		[]byte(`{"type":"response.output_text.delta","id":"evt_123","delta":"hi"}`),
		startAt,
		nowFn,
		nil,
		nil,
	)
	require.False(t, observed.terminal)
	require.Equal(t, "", observed.responseID)

	// Terminal events may use a top-level ID when the response ID is absent.
	observed = observeUpstreamMessage(
		state,
		[]byte(`{"type":"response.completed","id":"resp_fallback","response":{"usage":{"input_tokens":1,"output_tokens":1}}}`),
		startAt,
		nowFn,
		nil,
		nil,
	)
	require.True(t, observed.terminal)
	require.Equal(t, "resp_fallback", observed.responseID)
}

func TestObserveUpstreamMessage_ResponseServiceTierOnlyFromTerminalEvents(t *testing.T) {
	t.Parallel()

	state := &relayState{requestModel: "gpt-5.6-sol"}
	startAt := time.Unix(0, 0)
	now := startAt
	nowFn := func() time.Time {
		now = now.Add(5 * time.Millisecond)
		return now
	}

	created := observeUpstreamMessage(
		state,
		[]byte(`{"type":"response.created","response":{"id":"resp_1","model":"gpt-5.6-sol","service_tier":"priority"}}`),
		startAt,
		nowFn,
		nil,
		nil,
	)
	require.False(t, created.terminal)
	require.Equal(t, "", created.responseServiceTier)

	completed := observeUpstreamMessage(
		state,
		[]byte(`{"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.6-sol","service_tier":"default","usage":{"input_tokens":1,"output_tokens":2}}}`),
		startAt,
		nowFn,
		nil,
		nil,
	)
	require.True(t, completed.terminal)
	require.Equal(t, "default", completed.responseServiceTier, "终止事件档位应覆盖早期回显的 priority")

	var turn RelayTurnResult
	emitTurnComplete(func(result RelayTurnResult) { turn = result }, state, completed)
	require.Equal(t, "default", turn.ResponseServiceTier)

	var result RelayResult
	enrichResult(&result, state, now.Sub(startAt))
	require.Equal(t, "default", result.ResponseServiceTier)

	// A later turn without a tier must not inherit the previous turn's value.
	observeUpstreamMessage(state, []byte(`{"type":"response.created","response":{"id":"resp_2","model":"gpt-5.6-sol"}}`), startAt, nowFn, nil, nil)
	second := observeUpstreamMessage(
		state,
		[]byte(`{"type":"response.completed","response":{"id":"resp_2","model":"gpt-5.6-sol","usage":{"input_tokens":3,"output_tokens":4}}}`),
		startAt,
		nowFn,
		nil,
		nil,
	)
	require.Equal(t, "", second.responseServiceTier)
}

// TestRelayResponseModelsAreIsolated checks per-turn model tracking and terminal model corrections.
func TestRelayResponseModelsAreIsolated(t *testing.T) {
	state := &relayState{requestModel: "sent"}
	start := time.Now()
	observe := func(body string) observedUpstreamEvent {
		return observeUpstreamMessage(state, []byte(body), start, time.Now, nil, nil)
	}
	observe(`{"type":"response.created","response":{"id":"a","model":"early-a"}}`)
	observe(`{"type":"response.created","response":{"id":"b","model":"model-b"}}`)
	a := observe(`{"type":"response.completed","response":{"id":"a","model":"final-a"}}`)
	b := observe(`{"type":"response.completed","response":{"id":"b"}}`)
	require.Equal(t, "final-a", a.responseModel)
	require.Equal(t, "model-b", b.responseModel)
	var turn RelayTurnResult
	emitTurnComplete(func(value RelayTurnResult) { turn = value }, state, a)
	require.Equal(t, "final-a", turn.UpstreamResponseModel)
	observe(`{"type":"response.created","response":{"id":"c"}}`)
	c := observe(`{"type":"response.completed","response":{"id":"c"}}`)
	require.Empty(t, c.responseModel)
}

func (c *eofReplacementFrameConn) ReadFrame(ctx context.Context) (coderws.MessageType, []byte, error) {
	msgType, payload, err := c.FrameConn.ReadFrame(ctx)
	if errors.Is(err, io.EOF) {
		return msgType, payload, c.err
	}
	return msgType, payload, err
}

func (c *delayedReadFrameConn) ReadFrame(ctx context.Context) (coderws.MessageType, []byte, error) {
	if c == nil || c.base == nil {
		return coderws.MessageText, nil, io.EOF
	}
	c.once.Do(func() {
		if c.firstDelay > 0 {
			timer := time.NewTimer(c.firstDelay)
			defer timer.Stop()
			select {
			case <-ctx.Done():
			case <-timer.C:
			}
		}
	})
	return c.base.ReadFrame(ctx)
}

func (c *delayedReadFrameConn) WriteFrame(ctx context.Context, msgType coderws.MessageType, payload []byte) error {
	if c == nil || c.base == nil {
		return io.EOF
	}
	return c.base.WriteFrame(ctx, msgType, payload)
}

func (c *delayedReadFrameConn) Close() error {
	if c == nil || c.base == nil {
		return nil
	}
	return c.base.Close()
}

func (c *readStartSpyFrameConn) ReadFrame(ctx context.Context) (coderws.MessageType, []byte, error) {
	c.startOnce.Do(func() { close(c.started) })
	return c.base.ReadFrame(ctx)
}

func (c *readStartSpyFrameConn) WriteFrame(ctx context.Context, msgType coderws.MessageType, payload []byte) error {
	return c.base.WriteFrame(ctx, msgType, payload)
}

func (c *readStartSpyFrameConn) Close() error {
	return c.base.Close()
}

func (c *closeSpyFrameConn) ReadFrame(ctx context.Context) (coderws.MessageType, []byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	<-ctx.Done()
	return coderws.MessageText, nil, ctx.Err()
}

func (c *closeSpyFrameConn) WriteFrame(ctx context.Context, _ coderws.MessageType, _ []byte) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func (c *closeSpyFrameConn) Close() error {
	if c != nil {
		c.closeCalls.Add(1)
	}
	return nil
}

func (c *closeSpyFrameConn) CloseCalls() int32 {
	if c == nil {
		return 0
	}
	return c.closeCalls.Load()
}

func newCancelJoinProbeFrameConn() *cancelJoinProbeFrameConn {
	return &cancelJoinProbeFrameConn{
		readStarted:  make(chan struct{}),
		readCanceled: make(chan struct{}),
		allowReturn:  make(chan struct{}),
		readReturned: make(chan struct{}),
	}
}

func (c *cancelJoinProbeFrameConn) ReadFrame(ctx context.Context) (coderws.MessageType, []byte, error) {
	c.startOnce.Do(func() { close(c.readStarted) })
	<-ctx.Done()
	c.cancelOnce.Do(func() { close(c.readCanceled) })
	<-c.allowReturn
	c.returnOnce.Do(func() { close(c.readReturned) })
	return coderws.MessageText, nil, ctx.Err()
}

func (c *cancelJoinProbeFrameConn) WriteFrame(ctx context.Context, _ coderws.MessageType, _ []byte) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
func (c *cancelJoinProbeFrameConn) Close() error { return nil }
func TestRelay_BasicRelayAndUsage(t *testing.T) {
	t.Parallel()

	clientConn := newPassthroughTestFrameConn(nil, false)
	upstreamConn := newPassthroughTestFrameConn([]passthroughTestFrame{
		{
			msgType: coderws.MessageText,
			payload: []byte(`{"type":"response.completed","response":{"id":"resp_123","usage":{"input_tokens":7,"output_tokens":3,"input_tokens_details":{"cached_tokens":2}}}}`),
		},
	}, true)

	firstPayload := []byte(`{"type":"response.create","model":"gpt-5.3-codex","input":[{"type":"input_text","text":"hello"}]}`)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result, relayExit := Relay(ctx, clientConn, upstreamConn, firstPayload, RelayOptions{})
	require.Nil(t, relayExit)
	require.Equal(t, "gpt-5.3-codex", result.RequestModel)
	require.Equal(t, "resp_123", result.RequestID)
	require.Equal(t, "response.completed", result.TerminalEventType)
	require.Equal(t, 7, result.Usage.InputTokens)
	require.Equal(t, 3, result.Usage.OutputTokens)
	require.Equal(t, 2, result.Usage.CacheReadInputTokens)
	require.Nil(t, result.FirstTokenMs)
	require.Equal(t, int64(1), result.ClientToUpstreamFrames)
	require.Equal(t, int64(1), result.UpstreamToClientFrames)
	require.Equal(t, int64(0), result.DroppedDownstreamFrames)

	upstreamWrites := upstreamConn.Writes()
	require.Len(t, upstreamWrites, 1)
	require.Equal(t, coderws.MessageText, upstreamWrites[0].msgType)
	require.JSONEq(t, string(firstPayload), string(upstreamWrites[0].payload))

	clientWrites := clientConn.Writes()
	require.Len(t, clientWrites, 1)
	require.Equal(t, coderws.MessageText, clientWrites[0].msgType)
	require.JSONEq(t, `{"type":"response.completed","response":{"id":"resp_123","usage":{"input_tokens":7,"output_tokens":3,"input_tokens_details":{"cached_tokens":2}}}}`, string(clientWrites[0].payload))
}

func TestRelay_FunctionCallOutputBytesPreserved(t *testing.T) {
	t.Parallel()

	clientConn := newPassthroughTestFrameConn(nil, false)
	upstreamConn := newPassthroughTestFrameConn([]passthroughTestFrame{
		{
			msgType: coderws.MessageText,
			payload: []byte(`{"type":"response.completed","response":{"id":"resp_func","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}, true)

	firstPayload := []byte(`{"type":"response.create","model":"gpt-5.3-codex","input":[{"type":"function_call_output","call_id":"call_abc123","output":"{\"ok\":true}"}]}`)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, relayExit := Relay(ctx, clientConn, upstreamConn, firstPayload, RelayOptions{})
	require.Nil(t, relayExit)

	upstreamWrites := upstreamConn.Writes()
	require.Len(t, upstreamWrites, 1)
	require.Equal(t, coderws.MessageText, upstreamWrites[0].msgType)
	require.Equal(t, firstPayload, upstreamWrites[0].payload)
}

func TestRelay_UpstreamDisconnect(t *testing.T) {
	t.Parallel()

	// The upstream closes before declaring a response; the client sends no additional frames.
	clientConn := newPassthroughTestFrameConn(nil, false)
	upstreamConn := newPassthroughTestFrameConn(nil, true)

	firstPayload := []byte(`{"type":"response.create","model":"gpt-4o","input":[]}`)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result, relayExit := Relay(ctx, clientConn, upstreamConn, firstPayload, RelayOptions{})
	// EOF is graceful here because no upstream response ID established an active turn.
	require.Nil(t, relayExit, "上游 EOF 应被视为 graceful disconnect")
	require.Equal(t, "gpt-4o", result.RequestModel)
}

func TestRelay_UpstreamNormalCloseBeforeTerminalIsFailure(t *testing.T) {
	t.Parallel()

	clientConn := newPassthroughTestFrameConn(nil, false)
	upstreamConn := &eofReplacementFrameConn{
		FrameConn: newPassthroughTestFrameConn([]passthroughTestFrame{
			{
				msgType: coderws.MessageText,
				payload: []byte(`{"type":"response.created","response":{"id":"resp_incomplete","status":"in_progress"}}`),
			},
			{
				msgType: coderws.MessageText,
				payload: []byte(`{"type":"response.in_progress","response":{"id":"resp_incomplete","status":"in_progress"}}`),
			},
		}, true),
		err: coderws.CloseError{Code: coderws.StatusNormalClosure},
	}

	firstPayload := []byte(`{"type":"response.create","model":"gpt-5.6-sol","input":[]}`)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result, relayExit := Relay(ctx, clientConn, upstreamConn, firstPayload, RelayOptions{})
	require.NotNil(t, relayExit)
	require.Equal(t, "read_upstream", relayExit.Stage)
	require.False(t, relayExit.Graceful)
	require.True(t, relayExit.WroteDownstream)
	require.ErrorContains(t, relayExit.Err, "upstream websocket closed before terminal event")
	require.Empty(t, result.TerminalEventType)
	require.Len(t, clientConn.Writes(), 2)
}

func TestRelay_ClientDisconnect(t *testing.T) {
	t.Parallel()

	// The client closes immediately while the upstream read waits for cancellation.
	clientConn := newPassthroughTestFrameConn(nil, true)
	upstreamConn := newPassthroughTestFrameConn(nil, false)

	firstPayload := []byte(`{"type":"response.create","model":"gpt-4o","input":[]}`)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result, relayExit := Relay(ctx, clientConn, upstreamConn, firstPayload, RelayOptions{})
	require.NotNil(t, relayExit, "客户端 EOF 应返回可观测的中断状态")
	require.Equal(t, "client_disconnected", relayExit.Stage)
	require.Equal(t, "gpt-4o", result.RequestModel)
}

func TestRelay_ClientDisconnect_DrainCapturesLateUsage(t *testing.T) {
	t.Parallel()

	clientConn := newPassthroughTestFrameConn(nil, true)
	upstreamBase := newPassthroughTestFrameConn([]passthroughTestFrame{
		{
			msgType: coderws.MessageText,
			payload: []byte(`{"type":"response.completed","response":{"id":"resp_drain","usage":{"input_tokens":6,"output_tokens":4,"input_tokens_details":{"cached_tokens":1}}}}`),
		},
	}, true)
	upstreamConn := &delayedReadFrameConn{
		base:       upstreamBase,
		firstDelay: 80 * time.Millisecond,
	}

	firstPayload := []byte(`{"type":"response.create","model":"gpt-4o","input":[]}`)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result, relayExit := Relay(ctx, clientConn, upstreamConn, firstPayload, RelayOptions{
		UpstreamDrainTimeout: 400 * time.Millisecond,
	})
	require.NotNil(t, relayExit)
	require.Equal(t, "client_disconnected", relayExit.Stage)
	require.Equal(t, "resp_drain", result.RequestID)
	require.Equal(t, "response.completed", result.TerminalEventType)
	require.Equal(t, 6, result.Usage.InputTokens)
	require.Equal(t, 4, result.Usage.OutputTokens)
	require.Equal(t, 1, result.Usage.CacheReadInputTokens)
	require.Equal(t, int64(1), result.ClientToUpstreamFrames)
	require.Equal(t, int64(0), result.UpstreamToClientFrames)
	require.Equal(t, int64(1), result.DroppedDownstreamFrames)
}

func TestRelay_IdleTimeout(t *testing.T) {
	t.Parallel()

	// Neither peer sends more frames, so the relay reaches its idle timeout.
	clientConn := newPassthroughTestFrameConn(nil, false)
	upstreamConn := newPassthroughTestFrameConn(nil, false)

	firstPayload := []byte(`{"type":"response.create","model":"gpt-4o","input":[]}`)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Advance the clock after initialization to trigger the watchdog quickly.
	now := time.Now()
	callCount := 0
	nowFn := func() time.Time {
		callCount++
		if callCount <= 5 {
			return now
		}
		return now.Add(time.Hour)
	}

	result, relayExit := Relay(ctx, clientConn, upstreamConn, firstPayload, RelayOptions{
		IdleTimeout: 2 * time.Second,
		Now:         nowFn,
	})
	require.NotNil(t, relayExit, "应因 idle timeout 退出")
	require.Equal(t, "idle_timeout", relayExit.Stage)
	require.Equal(t, "gpt-4o", result.RequestModel)
}

func TestRelay_IdleTimeoutDoesNotCloseClientOnError(t *testing.T) {
	t.Parallel()

	clientConn := &closeSpyFrameConn{}
	upstreamConn := &closeSpyFrameConn{}

	firstPayload := []byte(`{"type":"response.create","model":"gpt-4o","input":[]}`)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	now := time.Now()
	callCount := 0
	nowFn := func() time.Time {
		callCount++
		if callCount <= 5 {
			return now
		}
		return now.Add(time.Hour)
	}

	_, relayExit := Relay(ctx, clientConn, upstreamConn, firstPayload, RelayOptions{
		IdleTimeout: 2 * time.Second,
		Now:         nowFn,
	})
	require.NotNil(t, relayExit, "应因 idle timeout 退出")
	require.Equal(t, "idle_timeout", relayExit.Stage)
	require.Zero(t, clientConn.CloseCalls(), "错误路径不应提前关闭客户端连接，交给上层决定 close code")
	require.GreaterOrEqual(t, upstreamConn.CloseCalls(), int32(1))
}

func TestRelay_JoinsUpstreamReaderBeforeReturning(t *testing.T) {
	t.Parallel()

	clientConn := &closeSpyFrameConn{}
	upstreamConn := newCancelJoinProbeFrameConn()
	ctx, cancel := context.WithCancel(context.Background())
	resultCh := make(chan *RelayExit, 1)

	go func() {
		_, relayExit := Relay(
			ctx,
			clientConn,
			upstreamConn,
			[]byte(`{"type":"response.create","model":"gpt-4o","input":[]}`),
			RelayOptions{},
		)
		resultCh <- relayExit
	}()

	select {
	case <-upstreamConn.readStarted:
	case <-time.After(time.Second):
		t.Fatal("upstream reader did not start")
	}
	cancel()
	select {
	case <-upstreamConn.readCanceled:
	case <-time.After(time.Second):
		t.Fatal("upstream reader did not observe relay cancellation")
	}
	select {
	case <-resultCh:
		t.Fatal("Relay returned before the upstream reader exited")
	case <-time.After(50 * time.Millisecond):
	}

	close(upstreamConn.allowReturn)
	select {
	case relayExit := <-resultCh:
		require.NotNil(t, relayExit)
		require.ErrorIs(t, relayExit.Err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("Relay did not return after the upstream reader exited")
	}
	select {
	case <-upstreamConn.readReturned:
	default:
		t.Fatal("Relay returned before the upstream reader completion signal")
	}
	require.Zero(t, clientConn.CloseCalls(), "错误路径不应提前关闭客户端连接")
}

func TestRelay_NilConnections(t *testing.T) {
	t.Parallel()

	firstPayload := []byte(`{"type":"response.create","model":"gpt-4o","input":[]}`)
	ctx := context.Background()

	t.Run("nil client conn", func(t *testing.T) {
		upstreamConn := newPassthroughTestFrameConn(nil, true)
		_, relayExit := Relay(ctx, nil, upstreamConn, firstPayload, RelayOptions{})
		require.NotNil(t, relayExit)
		require.Equal(t, "relay_init", relayExit.Stage)
		require.Contains(t, relayExit.Err.Error(), "nil")
	})

	t.Run("nil upstream conn", func(t *testing.T) {
		clientConn := newPassthroughTestFrameConn(nil, true)
		_, relayExit := Relay(ctx, clientConn, nil, firstPayload, RelayOptions{})
		require.NotNil(t, relayExit)
		require.Equal(t, "relay_init", relayExit.Stage)
		require.Contains(t, relayExit.Err.Error(), "nil")
	})
}

func TestRelay_MultipleUpstreamMessages(t *testing.T) {
	t.Parallel()

	// Relay deltas and a completion frame, including the terminal usage.
	clientConn := newPassthroughTestFrameConn(nil, false)
	upstreamConn := newPassthroughTestFrameConn([]passthroughTestFrame{
		{
			msgType: coderws.MessageText,
			payload: []byte(`{"type":"response.output_text.delta","delta":"Hello"}`),
		},
		{
			msgType: coderws.MessageText,
			payload: []byte(`{"type":"response.output_text.delta","delta":" world"}`),
		},
		{
			msgType: coderws.MessageText,
			payload: []byte(`{"type":"response.completed","response":{"id":"resp_multi","usage":{"input_tokens":10,"output_tokens":5,"input_tokens_details":{"cached_tokens":3}}}}`),
		},
	}, true)

	firstPayload := []byte(`{"type":"response.create","model":"gpt-4o","input":[{"type":"input_text","text":"hi"}]}`)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result, relayExit := Relay(ctx, clientConn, upstreamConn, firstPayload, RelayOptions{})
	require.Nil(t, relayExit)
	require.Equal(t, "resp_multi", result.RequestID)
	require.Equal(t, "response.completed", result.TerminalEventType)
	require.Equal(t, 10, result.Usage.InputTokens)
	require.Equal(t, 5, result.Usage.OutputTokens)
	require.Equal(t, 3, result.Usage.CacheReadInputTokens)
	require.NotNil(t, result.FirstTokenMs)

	clientWrites := clientConn.Writes()
	require.Len(t, clientWrites, 3)
}

func TestRelay_OnTurnComplete_PerTerminalEvent(t *testing.T) {
	t.Parallel()

	clientConn := newPassthroughTestFrameConn(nil, false)
	upstreamConn := newPassthroughTestFrameConn([]passthroughTestFrame{
		{
			msgType: coderws.MessageText,
			payload: []byte(`{"type":"response.completed","response":{"id":"resp_turn_1","usage":{"input_tokens":2,"output_tokens":1}}}`),
		},
		{
			msgType: coderws.MessageText,
			payload: []byte(`{"type":"response.failed","response":{"id":"resp_turn_2","usage":{"input_tokens":3,"output_tokens":4}}}`),
		},
	}, true)

	firstPayload := []byte(`{"type":"response.create","model":"gpt-5.3-codex","input":[]}`)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	turns := make([]RelayTurnResult, 0, 2)
	result, relayExit := Relay(ctx, clientConn, upstreamConn, firstPayload, RelayOptions{
		OnTurnComplete: func(turn RelayTurnResult) {
			turns = append(turns, turn)
		},
	})
	require.Nil(t, relayExit)
	require.Len(t, turns, 2)
	require.Equal(t, "resp_turn_1", turns[0].RequestID)
	require.Equal(t, "response.completed", turns[0].TerminalEventType)
	require.Equal(t, 2, turns[0].Usage.InputTokens)
	require.Equal(t, 1, turns[0].Usage.OutputTokens)
	require.Equal(t, "resp_turn_2", turns[1].RequestID)
	require.Equal(t, "response.failed", turns[1].TerminalEventType)
	require.Equal(t, 3, turns[1].Usage.InputTokens)
	require.Equal(t, 4, turns[1].Usage.OutputTokens)
	require.Equal(t, 5, result.Usage.InputTokens)
	require.Equal(t, 5, result.Usage.OutputTokens)
}

func TestRelay_OnTurnComplete_BareErrorWithoutIDBeforeLaterCompleted(t *testing.T) {
	t.Parallel()

	clientConn := newPassthroughTestFrameConn(nil, false)
	upstreamConn := newPassthroughTestFrameConn([]passthroughTestFrame{
		{
			msgType: coderws.MessageText,
			payload: []byte(`{"type":"error","usage":{"input_tokens":5,"output_tokens":1},"error":{"message":"first turn failed"}}`),
		},
		{
			msgType: coderws.MessageText,
			payload: []byte(`{"type":"response.completed","response":{"id":"resp_next","usage":{"input_tokens":3,"output_tokens":2}}}`),
		},
	}, true)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	turns := make([]RelayTurnResult, 0, 2)
	result, relayExit := Relay(
		ctx,
		clientConn,
		upstreamConn,
		[]byte(`{"type":"response.create","model":"gpt-5.6-sol","input":[]}`),
		RelayOptions{OnTurnComplete: func(turn RelayTurnResult) { turns = append(turns, turn) }},
	)

	require.Nil(t, relayExit)
	require.Len(t, turns, 2)
	require.Equal(t, "error", turns[0].TerminalEventType)
	require.Empty(t, turns[0].RequestID)
	require.Equal(t, Usage{InputTokens: 5, OutputTokens: 1}, turns[0].Usage)
	require.Equal(t, "response.completed", turns[1].TerminalEventType)
	require.Equal(t, "resp_next", turns[1].RequestID)
	require.Equal(t, Usage{InputTokens: 3, OutputTokens: 2}, turns[1].Usage)
	require.Equal(t, Usage{InputTokens: 8, OutputTokens: 3}, result.Usage)
}

func TestRelay_OnTurnComplete_AuxiliaryFrameDoesNotSettleBareErrorBeforeFailed(t *testing.T) {
	t.Parallel()

	clientConn := newPassthroughTestFrameConn(nil, false)
	upstreamConn := newPassthroughTestFrameConn([]passthroughTestFrame{
		{msgType: coderws.MessageText, payload: []byte(`{"type":"error","error":{"code":"server_error","message":"failed"}}`)},
		{msgType: coderws.MessageText, payload: []byte(`{"type":"rate_limits.updated","rate_limits":[{"name":"requests","remaining":1}]}`)},
		{msgType: coderws.MessageText, payload: []byte(`{"type":"response.failed","response":{"id":"resp_authoritative","usage":{"input_tokens":7,"output_tokens":2},"error":{"code":"server_error","message":"failed"}}}`)},
	}, true)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	turns := make([]RelayTurnResult, 0, 1)
	result, relayExit := Relay(ctx, clientConn, upstreamConn, []byte(`{"type":"response.create","model":"gpt-5.6-sol","input":[]}`), RelayOptions{
		OnTurnComplete: func(turn RelayTurnResult) { turns = append(turns, turn) },
	})

	require.Nil(t, relayExit)
	require.Len(t, turns, 1)
	require.Equal(t, "response.failed", turns[0].TerminalEventType)
	require.Equal(t, "resp_authoritative", turns[0].RequestID)
	require.Equal(t, Usage{InputTokens: 7, OutputTokens: 2}, turns[0].Usage)
	require.Equal(t, turns[0].Usage, result.Usage)
}

func TestRelay_OnTurnComplete_UsesCurrentResponseCreateModel(t *testing.T) {
	t.Parallel()

	errorEvent := []byte(`{"type":"error","error":{"message":"This request may pose a cybersecurity risk."}}`)
	clientConn := newPassthroughTestFrameConn(nil, false)
	upstreamConn := newPassthroughTestFrameConn([]passthroughTestFrame{
		{msgType: coderws.MessageText, payload: errorEvent},
	}, true)
	firstPayload := []byte(`{"type":"response.create","model":"gpt-5.3-codex","input":[]}`)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var gotEventType string
	var gotPayload []byte
	_, _ = Relay(ctx, clientConn, upstreamConn, firstPayload, RelayOptions{
		OnUpstreamEvent: func(eventType string, payload []byte) {
			gotEventType = eventType
			gotPayload = append([]byte(nil), payload...)
		},
	})

	require.Equal(t, "error", gotEventType)
	require.JSONEq(t, string(errorEvent), string(gotPayload))
}

func TestRelay_OnUpstreamError_ReportsFailedEvent(t *testing.T) {
	t.Parallel()

	failedEvent := []byte(`{"type":"response.failed","response":{"id":"resp_failed","error":{"message":"This request may pose a cybersecurity risk."}}}`)
	clientConn := newPassthroughTestFrameConn(nil, false)
	upstreamConn := newPassthroughTestFrameConn([]passthroughTestFrame{
		{msgType: coderws.MessageText, payload: failedEvent},
	}, true)
	firstPayload := []byte(`{"type":"response.create","model":"gpt-5.3-codex","input":[]}`)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var gotEventType string
	var gotPayload []byte
	_, _ = Relay(ctx, clientConn, upstreamConn, firstPayload, RelayOptions{
		OnUpstreamEvent: func(eventType string, payload []byte) {
			gotEventType = eventType
			gotPayload = append([]byte(nil), payload...)
		},
	})

	require.Equal(t, "response.failed", gotEventType)
	require.JSONEq(t, string(failedEvent), string(gotPayload))
}

func TestRelay_OnTurnComplete_ProvidesTurnMetrics(t *testing.T) {
	t.Parallel()

	clientConn := newPassthroughTestFrameConn(nil, false)
	upstreamConn := newPassthroughTestFrameConn([]passthroughTestFrame{
		{
			msgType: coderws.MessageText,
			payload: []byte(`{"type":"response.output_text.delta","response_id":"resp_metric","delta":"hi"}`),
		},
		{
			msgType: coderws.MessageText,
			payload: []byte(`{"type":"response.completed","response":{"id":"resp_metric","usage":{"input_tokens":2,"output_tokens":1}}}`),
		},
	}, true)

	firstPayload := []byte(`{"type":"response.create","model":"gpt-5.3-codex","input":[]}`)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	base := time.Unix(0, 0)
	var nowTick atomic.Int64
	nowFn := func() time.Time {
		step := nowTick.Add(1)
		return base.Add(time.Duration(step) * 5 * time.Millisecond)
	}

	var turn RelayTurnResult
	result, relayExit := Relay(ctx, clientConn, upstreamConn, firstPayload, RelayOptions{
		Now: nowFn,
		OnTurnComplete: func(current RelayTurnResult) {
			turn = current
		},
	})
	require.Nil(t, relayExit)
	require.Equal(t, "resp_metric", turn.RequestID)
	require.Equal(t, "response.completed", turn.TerminalEventType)
	require.NotNil(t, turn.FirstTokenMs)
	require.GreaterOrEqual(t, *turn.FirstTokenMs, 0)
	require.Greater(t, turn.Duration.Milliseconds(), int64(0))
	require.NotNil(t, result.FirstTokenMs)
	require.Greater(t, result.Duration.Milliseconds(), int64(0))
}

func TestRelay_OnTurnCompleteUsesExplicitFirstTurnStart(t *testing.T) {
	t.Parallel()

	clientConn := newPassthroughTestFrameConn(nil, false)
	upstreamConn := newPassthroughTestFrameConn([]passthroughTestFrame{
		{
			msgType: coderws.MessageText,
			payload: []byte(`{"type":"response.completed","response":{"id":"resp_initial_boundary","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}, true)
	startedAt := time.Date(2026, time.August, 17, 9, 59, 59, 0, time.UTC)
	var turn RelayTurnResult
	_, relayExit := Relay(context.Background(), clientConn, upstreamConn,
		[]byte(`{"type":"response.create","model":"gpt-5.3-codex","input":[]}`), RelayOptions{
			FirstTurnStartedAt: startedAt,
			Now:                func() time.Time { return startedAt.Add(time.Second) },
			OnTurnComplete:     func(current RelayTurnResult) { turn = current },
		})
	require.Nil(t, relayExit)
	require.Equal(t, startedAt, turn.StartedAt)
}

func TestRelay_OnTurnCompleteUsesSubsequentResponseCreateStart(t *testing.T) {
	t.Parallel()

	clientConn := newPassthroughTestFrameConn(nil, false)
	upstreamConn := newPassthroughTestFrameConn(nil, false)
	firstStartedAt := time.Date(2026, time.August, 17, 9, 0, 0, 0, time.UTC)
	secondStartedAt := time.Date(2026, time.August, 17, 9, 59, 59, 0, time.UTC)
	secondResponseAt := secondStartedAt.Add(time.Second)
	var now atomic.Int64
	now.Store(firstStartedAt.UnixNano())
	turns := make(chan RelayTurnResult, 2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = Relay(ctx, clientConn, upstreamConn,
			[]byte(`{"type":"response.create","model":"gpt-5.3-codex","input":[]}`), RelayOptions{
				Now: func() time.Time { return time.Unix(0, now.Load()).UTC() },
				OnTurnComplete: func(current RelayTurnResult) {
					turns <- current
				},
			})
	}()
	require.Eventually(t, func() bool { return len(upstreamConn.Writes()) == 1 }, time.Second, time.Millisecond)
	upstreamConn.readCh <- passthroughTestFrame{
		msgType: coderws.MessageText,
		payload: []byte(`{"type":"response.completed","response":{"id":"resp_first","usage":{"input_tokens":1,"output_tokens":1}}}`),
	}
	select {
	case <-turns:
	case <-time.After(time.Second):
		t.Fatal("first turn did not complete")
	}

	now.Store(secondStartedAt.UnixNano())
	clientConn.readCh <- passthroughTestFrame{
		msgType: coderws.MessageText,
		payload: []byte(`{"type":"response.create","model":"gpt-5.3-codex","input":[]}`),
	}
	require.Eventually(t, func() bool { return len(upstreamConn.Writes()) == 2 }, time.Second, time.Millisecond)
	now.Store(secondResponseAt.UnixNano())
	upstreamConn.readCh <- passthroughTestFrame{
		msgType: coderws.MessageText,
		payload: []byte(`{"type":"response.completed","response":{"id":"resp_second","usage":{"input_tokens":1,"output_tokens":1}}}`),
	}
	var secondTurn RelayTurnResult
	select {
	case secondTurn = <-turns:
	case <-time.After(time.Second):
		t.Fatal("second turn did not complete")
	}
	require.Equal(t, secondStartedAt, secondTurn.StartedAt)

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("relay did not stop after cancellation")
	}
}

func TestRelay_BinaryFramePassthrough(t *testing.T) {
	t.Parallel()

	// Binary frames pass through unchanged and contribute no usage.
	binaryPayload := []byte{0x00, 0x01, 0x02, 0x03}
	clientConn := newPassthroughTestFrameConn(nil, false)
	upstreamConn := newPassthroughTestFrameConn([]passthroughTestFrame{
		{
			msgType: coderws.MessageBinary,
			payload: binaryPayload,
		},
	}, true)

	firstPayload := []byte(`{"type":"response.create","model":"gpt-4o","input":[]}`)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result, relayExit := Relay(ctx, clientConn, upstreamConn, firstPayload, RelayOptions{})
	require.Nil(t, relayExit)
	require.Equal(t, 0, result.Usage.InputTokens)

	clientWrites := clientConn.Writes()
	require.Len(t, clientWrites, 1)
	require.Equal(t, coderws.MessageBinary, clientWrites[0].msgType)
	require.Equal(t, binaryPayload, clientWrites[0].payload)
}

func TestRelay_BinaryJSONFrameSkipsObservation(t *testing.T) {
	t.Parallel()

	clientConn := newPassthroughTestFrameConn(nil, false)
	upstreamConn := newPassthroughTestFrameConn([]passthroughTestFrame{
		{
			msgType: coderws.MessageBinary,
			payload: []byte(`{"type":"response.completed","response":{"id":"resp_binary","usage":{"input_tokens":7,"output_tokens":3}}}`),
		},
	}, true)

	firstPayload := []byte(`{"type":"response.create","model":"gpt-4o","input":[]}`)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result, relayExit := Relay(ctx, clientConn, upstreamConn, firstPayload, RelayOptions{})
	require.Nil(t, relayExit)
	require.Equal(t, 0, result.Usage.InputTokens)
	require.Equal(t, "", result.RequestID)
	require.Equal(t, "", result.TerminalEventType)

	clientWrites := clientConn.Writes()
	require.Len(t, clientWrites, 1)
	require.Equal(t, coderws.MessageBinary, clientWrites[0].msgType)
}

func TestRelay_UpstreamErrorEventPassthroughRaw(t *testing.T) {
	t.Parallel()

	clientConn := newPassthroughTestFrameConn(nil, false)
	errorEvent := []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"No tool call found"}}`)
	upstreamConn := newPassthroughTestFrameConn([]passthroughTestFrame{
		{
			msgType: coderws.MessageText,
			payload: errorEvent,
		},
	}, true)

	firstPayload := []byte(`{"type":"response.create","model":"gpt-4o","input":[]}`)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, relayExit := Relay(ctx, clientConn, upstreamConn, firstPayload, RelayOptions{})
	require.Nil(t, relayExit)

	clientWrites := clientConn.Writes()
	require.Len(t, clientWrites, 1)
	require.Equal(t, coderws.MessageText, clientWrites[0].msgType)
	require.Equal(t, errorEvent, clientWrites[0].payload)
}

func TestRelay_PreservesFirstMessageType(t *testing.T) {
	t.Parallel()

	clientConn := newPassthroughTestFrameConn(nil, false)
	upstreamConn := newPassthroughTestFrameConn(nil, true)

	firstPayload := []byte(`{"type":"response.create","model":"gpt-4o","input":[]}`)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, relayExit := Relay(ctx, clientConn, upstreamConn, firstPayload, RelayOptions{
		FirstMessageType: coderws.MessageBinary,
	})
	require.Nil(t, relayExit)

	upstreamWrites := upstreamConn.Writes()
	require.Len(t, upstreamWrites, 1)
	require.Equal(t, coderws.MessageBinary, upstreamWrites[0].msgType)
	require.Equal(t, firstPayload, upstreamWrites[0].payload)
}

func TestRelay_UsageParseFailureDoesNotBlockRelay(t *testing.T) {
	baseline := SnapshotMetrics().UsageParseFailureTotal

	// The JSON is valid but usage has the wrong type; forwarding must continue.
	clientConn := newPassthroughTestFrameConn(nil, false)
	upstreamConn := newPassthroughTestFrameConn([]passthroughTestFrame{
		{
			msgType: coderws.MessageText,
			payload: []byte(`{"type":"response.completed","response":{"id":"resp_bad","usage":"not_an_object"}}`),
		},
	}, true)

	firstPayload := []byte(`{"type":"response.create","model":"gpt-4o","input":[]}`)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result, relayExit := Relay(ctx, clientConn, upstreamConn, firstPayload, RelayOptions{})
	require.Nil(t, relayExit)
	// Invalid usage contributes zero tokens while the terminal event remains observable.
	require.Equal(t, 0, result.Usage.InputTokens)
	require.Equal(t, "response.completed", result.TerminalEventType)

	clientWrites := clientConn.Writes()
	require.Len(t, clientWrites, 1)
	require.GreaterOrEqual(t, SnapshotMetrics().UsageParseFailureTotal, baseline+1)
}

func TestRelay_WriteUpstreamFirstMessageFails(t *testing.T) {
	t.Parallel()

	upstreamConn := newPassthroughTestFrameConn(nil, true)
	_ = upstreamConn.Close()

	// Use the failing writer for the relay's first upstream frame.
	errConn := &errorOnWriteFrameConn{}
	clientConn := newPassthroughTestFrameConn(nil, false)

	firstPayload := []byte(`{"type":"response.create","model":"gpt-4o","input":[]}`)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, relayExit := Relay(ctx, clientConn, errConn, firstPayload, RelayOptions{})
	require.NotNil(t, relayExit)
	require.Equal(t, "write_upstream", relayExit.Stage)
}

func TestRelay_ContextCanceled(t *testing.T) {
	t.Parallel()

	clientConn := newPassthroughTestFrameConn(nil, false)
	upstreamConn := newPassthroughTestFrameConn(nil, false)

	firstPayload := []byte(`{"type":"response.create","model":"gpt-4o","input":[]}`)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, relayExit := Relay(ctx, clientConn, upstreamConn, firstPayload, RelayOptions{})
	// Cancellation prevents the first upstream write.
	require.NotNil(t, relayExit)
}

func TestRelay_DownstreamPreambleStartsClientReader(t *testing.T) {
	clientBase := newPassthroughTestFrameConn(nil, false)
	clientConn := &readStartSpyFrameConn{base: clientBase, started: make(chan struct{})}
	upstreamConn := newPassthroughTestFrameConn(nil, false)
	resultCh := make(chan *RelayExit, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() {
		_, relayExit := Relay(
			ctx,
			clientConn,
			upstreamConn,
			[]byte(`{"type":"response.create","model":"gpt-5.1"}`),
			RelayOptions{
				StartClientAfterFirstDownstream: true,
			},
		)
		resultCh <- relayExit
	}()

	upstreamConn.readCh <- passthroughTestFrame{
		msgType: coderws.MessageText,
		payload: []byte(`{"type":"response.created","response":{"id":"resp_semantic_gate"}}`),
	}
	require.Eventually(t, func() bool { return len(clientBase.Writes()) == 1 }, time.Second, 10*time.Millisecond)
	select {
	case <-clientConn.started:
	case <-time.After(time.Second):
		t.Fatal("response.created did not start the client reader")
	}

	upstreamConn.readCh <- passthroughTestFrame{
		msgType: coderws.MessageText,
		payload: []byte(`{"type":"response.completed","response":{"id":"resp_semantic_gate","usage":{"input_tokens":1,"output_tokens":1}}}`),
	}
	_ = upstreamConn.Close()
	select {
	case relayExit := <-resultCh:
		require.Nil(t, relayExit)
	case <-time.After(time.Second):
		t.Fatal("relay did not finish after terminal event and upstream close")
	}
}

func TestRelay_TraceEvents_ContainsLifecycleStages(t *testing.T) {
	t.Parallel()

	clientConn := newPassthroughTestFrameConn(nil, false)
	upstreamConn := newPassthroughTestFrameConn([]passthroughTestFrame{
		{
			msgType: coderws.MessageText,
			payload: []byte(`{"type":"response.completed","response":{"id":"resp_trace","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}, true)

	firstPayload := []byte(`{"type":"response.create","model":"gpt-4o","input":[]}`)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	stages := make([]string, 0, 8)
	var stagesMu sync.Mutex
	_, relayExit := Relay(ctx, clientConn, upstreamConn, firstPayload, RelayOptions{
		OnTrace: func(event RelayTraceEvent) {
			stagesMu.Lock()
			stages = append(stages, event.Stage)
			stagesMu.Unlock()
		},
	})
	require.Nil(t, relayExit)
	stagesMu.Lock()
	capturedStages := append([]string(nil), stages...)
	stagesMu.Unlock()
	require.Contains(t, capturedStages, "relay_start")
	require.Contains(t, capturedStages, "write_first_message_ok")
	require.Contains(t, capturedStages, "first_exit")
	require.Contains(t, capturedStages, "relay_complete")
}

func TestRelay_TraceEvents_IdleTimeout(t *testing.T) {
	t.Parallel()

	clientConn := newPassthroughTestFrameConn(nil, false)
	upstreamConn := newPassthroughTestFrameConn(nil, false)

	firstPayload := []byte(`{"type":"response.create","model":"gpt-4o","input":[]}`)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	now := time.Now()
	callCount := 0
	nowFn := func() time.Time {
		callCount++
		if callCount <= 5 {
			return now
		}
		return now.Add(time.Hour)
	}

	stages := make([]string, 0, 8)
	var stagesMu sync.Mutex
	_, relayExit := Relay(ctx, clientConn, upstreamConn, firstPayload, RelayOptions{
		IdleTimeout: 2 * time.Second,
		Now:         nowFn,
		OnTrace: func(event RelayTraceEvent) {
			stagesMu.Lock()
			stages = append(stages, event.Stage)
			stagesMu.Unlock()
		},
	})
	require.NotNil(t, relayExit)
	require.Equal(t, "idle_timeout", relayExit.Stage)
	stagesMu.Lock()
	capturedStages := append([]string(nil), stages...)
	stagesMu.Unlock()
	require.Contains(t, capturedStages, "idle_timeout_triggered")
	require.Contains(t, capturedStages, "relay_exit")
}

func (c *errorOnWriteFrameConn) ReadFrame(ctx context.Context) (coderws.MessageType, []byte, error) {
	<-ctx.Done()
	return coderws.MessageText, nil, ctx.Err()
}

func (c *errorOnWriteFrameConn) WriteFrame(_ context.Context, _ coderws.MessageType, _ []byte) error {
	return errors.New("write failed: connection refused")
}

func (c *errorOnWriteFrameConn) Close() error {
	return nil
}

func TestRelay_NoSemanticOutputTerminalSequence_FirstTokenMsNil(t *testing.T) {
	t.Parallel()

	for _, terminalEvent := range []string{"response.completed", "response.done"} {
		t.Run(terminalEvent, func(t *testing.T) {
			t.Parallel()

			clientConn := newPassthroughTestFrameConn(nil, false)
			upstreamConn := newPassthroughTestFrameConn([]passthroughTestFrame{
				{
					msgType: coderws.MessageText,
					payload: []byte(`{"type":"response.created","response":{"id":"resp_no_output"}}`),
				},
				{
					msgType: coderws.MessageText,
					payload: []byte(`{"type":"response.in_progress","response":{"id":"resp_no_output"}}`),
				},
				{
					msgType: coderws.MessageText,
					payload: []byte(`{"type":"response.content_part.done","response_id":"resp_no_output"}`),
				},
				{
					msgType: coderws.MessageText,
					payload: []byte(`{"type":"response.output_item.done","response_id":"resp_no_output"}`),
				},
				{
					msgType: coderws.MessageText,
					payload: []byte(`{"type":"` + terminalEvent + `","response":{"id":"resp_no_output","usage":{"input_tokens":2,"output_tokens":0}}}`),
				},
			}, true)

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			var turn RelayTurnResult
			result, relayExit := Relay(
				ctx,
				clientConn,
				upstreamConn,
				[]byte(`{"type":"response.create","model":"gpt-5.3-codex","input":[]}`),
				RelayOptions{OnTurnComplete: func(current RelayTurnResult) { turn = current }},
			)

			require.Nil(t, relayExit)
			require.Equal(t, terminalEvent, turn.TerminalEventType)
			require.Nil(t, turn.FirstTokenMs)
			require.Equal(t, terminalEvent, result.TerminalEventType)
			require.Nil(t, result.FirstTokenMs)
			require.Equal(t, int64(5), result.UpstreamToClientFrames)
		})
	}
}

func TestRelay_NoDeltaOutputDoneEvent_RecordsFirstTokenBeforeTerminal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		donePayload string
	}{
		{
			name:        "output text done",
			donePayload: `{"type":"response.output_text.done","response_id":"resp_done","text":"hello"}`,
		},
		{
			name:        "function call arguments done",
			donePayload: `{"type":"response.function_call_arguments.done","response_id":"resp_done","arguments":"{\"city\":\"Paris\"}"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			clientConn := newPassthroughTestFrameConn(nil, false)
			upstreamConn := newPassthroughTestFrameConn([]passthroughTestFrame{
				{msgType: coderws.MessageText, payload: []byte(`{"type":"response.created","response":{"id":"resp_done"}}`)},
				{msgType: coderws.MessageText, payload: []byte(tt.donePayload)},
				{msgType: coderws.MessageText, payload: []byte(`{"type":"response.completed","response":{"id":"resp_done","usage":{"input_tokens":2,"output_tokens":1}}}`)},
			}, true)

			base := time.Unix(0, 0)
			var nowTick atomic.Int64
			nowFn := func() time.Time {
				return base.Add(time.Duration(nowTick.Add(1)) * 10 * time.Millisecond)
			}
			var turn RelayTurnResult
			result, relayExit := Relay(
				context.Background(),
				clientConn,
				upstreamConn,
				[]byte(`{"type":"response.create","model":"gpt-5.3-codex","input":[]}`),
				RelayOptions{
					Now:            nowFn,
					OnTurnComplete: func(current RelayTurnResult) { turn = current },
				},
			)

			require.Nil(t, relayExit)
			require.NotNil(t, turn.FirstTokenMs)
			require.Less(t, int64(*turn.FirstTokenMs), turn.Duration.Milliseconds())
			require.NotNil(t, result.FirstTokenMs)
			require.Less(t, int64(*result.FirstTokenMs), result.Duration.Milliseconds())
		})
	}
}

func TestRelay_OnTurnComplete_RealOpenAIStream_FirstTokenMs(t *testing.T) {
	t.Parallel()

	clientConn := newPassthroughTestFrameConn(nil, false)
	upstreamConn := newPassthroughTestFrameConn([]passthroughTestFrame{
		{
			msgType: coderws.MessageText,
			payload: []byte(`{"type":"response.created","response":{"id":"resp_real"}}`),
		},
		{
			msgType: coderws.MessageText,
			payload: []byte(`{"type":"response.output_text.delta","delta":"He"}`),
		},
		{
			msgType: coderws.MessageText,
			payload: []byte(`{"type":"response.output_text.delta","delta":"llo"}`),
		},
		{
			msgType: coderws.MessageText,
			payload: []byte(`{"type":"response.output_text.delta","delta":" world"}`),
		},
		{
			msgType: coderws.MessageText,
			payload: []byte(`{"type":"response.completed","response":{"id":"resp_real","usage":{"input_tokens":2,"output_tokens":3}}}`),
		},
	}, true)

	firstPayload := []byte(`{"type":"response.create","model":"gpt-5.3-codex","input":[]}`)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	base := time.Unix(0, 0)
	var nowTick atomic.Int64
	nowFn := func() time.Time {
		step := nowTick.Add(1)
		return base.Add(time.Duration(step) * 10 * time.Millisecond)
	}

	var turn RelayTurnResult
	result, relayExit := Relay(ctx, clientConn, upstreamConn, firstPayload, RelayOptions{
		Now: nowFn,
		OnTurnComplete: func(current RelayTurnResult) {
			turn = current
		},
	})
	require.Nil(t, relayExit)
	require.Equal(t, "resp_real", turn.RequestID)
	require.Equal(t, "response.completed", turn.TerminalEventType)

	require.NotNil(t, turn.FirstTokenMs, "per-turn FirstTokenMs must be captured for real OpenAI streams")
	require.Greater(t, turn.Duration.Milliseconds(), int64(0))

	require.Less(t,
		int64(*turn.FirstTokenMs),
		turn.Duration.Milliseconds(),
		"per-turn FirstTokenMs (%dms) should be strictly less than Duration (%dms); "+
			"equality indicates the bug where first_token is mistakenly stamped on the terminal event",
		*turn.FirstTokenMs, turn.Duration.Milliseconds(),
	)

	require.NotNil(t, result.FirstTokenMs)
	require.Greater(t, *result.FirstTokenMs, 0)
}
