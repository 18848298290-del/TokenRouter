package lifecycle

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestRestarterPlatformDelayAndClose checks one restart request and cancellation of pending callbacks on close.
func TestRestarterPlatformDelayAndClose(t *testing.T) {
	for _, platform := range []string{"linux", "darwin", "windows"} {
		t.Run(platform, func(t *testing.T) {
			var calls atomic.Int32
			called := make(chan struct{}, 2)
			r := NewRestarter(platform, func() { calls.Add(1); called <- struct{}{} })
			defer r.Close()
			for range 8 {
				require.NoError(t, r.RequestRestart())
			}
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
	time.Sleep(650 * time.Millisecond)
}

// TestRestarterCloseWaitsForCallback checks that Close waits for an active callback before releasing resources.
func TestRestarterCloseWaitsForCallback(t *testing.T) {
	entered, release, closed := make(chan struct{}), make(chan struct{}), make(chan struct{})
	r := NewRestarter("linux", func() { close(entered); <-release })
	require.NoError(t, r.RequestRestart())
	<-entered
	go func() { r.Close(); close(closed) }()
	select {
	case <-closed:
		t.Fatal("回调尚未完成")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	<-closed
}
