package lifecycle

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Restart scheduling has both a response-send delay and a close-versus-callback
// race. Platform fixtures check admission and cancellation, while a blocked
// callback lets the test observe that Close stays pending until callback work
// returns and releases its completion slot.

// TestRestarterPlatformDelayAndClose submits repeated requests across Linux,
// Darwin, and Windows. The callback must wait past the short response window,
// run once for Linux, and remain suppressed after pending work is closed.
func TestRestarterPlatformDelayAndClose(t *testing.T) {
	for _, platform := range []string{"linux", "darwin", "windows"} {
		t.Run(platform, func(t *testing.T) {
			var calls atomic.Int32
			called := make(chan struct{}, 2)
			// Buffer callback announcements so scheduling tests never hold up callback
			// completion while probing the response window or waiting for Close.
			r := NewRestarter(platform, func() { calls.Add(1); called <- struct{}{} })
			defer r.Close()
			for range 8 {
				require.NoError(t, r.RequestRestart())
			}
			// Probe well before the scheduled 600-millisecond delay. An early callback
			// would remove the response-send window even if its final count stayed one.
			select {
			case <-called:
				t.Fatal("未保留响应发送窗口")
			case <-time.After(100 * time.Millisecond):
			}
			if platform == "linux" {
				select {
				case <-called:
				case <-time.After(time.Second):
					t.Fatal("没有请求关闭")
				}
			}
			r.Close()
			require.NoError(t, r.RequestRestart())
			expected := int32(0)
			if platform == "linux" {
				expected = 1
			}
			require.Equal(t, expected, calls.Load())
		})
	}
	r := NewRestarter("linux", func() { t.Error("关闭后仍发出重启") })
	require.NoError(t, r.RequestRestart())
	r.Close()
	// Wait beyond the pending timer's delay after Close succeeds, exposing a
	// cancelled timer that might still invoke the fixture callback unexpectedly.
	time.Sleep(650 * time.Millisecond)
}

// TestRestarterCloseWaitsForCallback blocks an already entered restart
// callback. Close must remain pending during that block and finish after the
// callback is released, protecting resources used by active restart work.
func TestRestarterCloseWaitsForCallback(t *testing.T) {
	entered, release, closed := make(chan struct{}), make(chan struct{}), make(chan struct{})
	// Announce timer callback entry before waiting on release. This keeps its
	// completion slot occupied until the test has observed a pending Close.
	r := NewRestarter("linux", func() { close(entered); <-release })
	require.NoError(t, r.RequestRestart())
	// Observe callback entry before launching Close so this case exercises an
	// already fired timer and its completion wait, rather than successful timer cancellation.
	<-entered
	go func() { r.Close(); close(closed) }()
	select {
	case <-closed:
		t.Fatal("回调尚未完成")
	case <-time.After(20 * time.Millisecond):
	}
	// Release callback work after checking the pending close. Its completion
	// decrement then allows Close to publish its finished signal to the test.
	close(release)
	<-closed
}
