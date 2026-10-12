package session

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestCodexTurnOriginsExpiryBoundary reads ownership at its exact deadline and one
// nanosecond later. The deadline remains valid; the later read rejects ownership
// and removes the stored entry.
func TestCodexTurnOriginsExpiryBoundary(t *testing.T) {
	now := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	origins := NewCodexTurnOrigins(func() time.Time { return now })
	origins.Record("key-session", 42, time.Minute)
	now = now.Add(time.Minute)
	id, ok := origins.Owner("key-session")
	require.True(t, ok, "原边界在到期时刻本身仍有效")
	require.Equal(t, int64(42), id)
	now = now.Add(time.Nanosecond)
	_, ok = origins.Owner("key-session")
	require.False(t, ok)
	_, retained := origins.origins.Load("key-session")
	require.False(t, retained, "读侧清除已过期来源")
}

// TestCodexTurnOriginsSweepsOn256thWrite leaves a negative-TTL record unqueried while
// adding live records. It remains stored through 255 writes and is removed by the
// 256th write's sweep, while that triggering record remains readable.
func TestCodexTurnOriginsSweepsOn256thWrite(t *testing.T) {
	now := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	origins := NewCodexTurnOrigins(func() time.Time { return now })
	origins.Record("expired", 1, -time.Second)
	for i := range 254 {
		origins.Record(fmt.Sprint(i), 2, time.Hour)
	}
	_, exists := origins.origins.Load("expired")
	require.True(t, exists, "清扫频率保持每256次写入")
	origins.Record("last", 3, time.Hour)
	_, exists = origins.origins.Load("expired")
	require.False(t, exists)
	id, ok := origins.Owner("last")
	require.True(t, ok)
	require.Equal(t, int64(3), id)
}

// TestCodexTurnOriginsConcurrentRequestsStayIsolated repeats ownership writes and
// reads for thirty-two API Key seeds concurrently. Equal session text under those
// keys continues to return each seed's own provider.
func TestCodexTurnOriginsConcurrentRequestsStayIsolated(t *testing.T) {
	origins := NewCodexTurnOrigins(time.Now)
	var wg sync.WaitGroup
	// Use equal session text with separate key prefixes so overlapping reads
	// and writes can reveal accidental ownership sharing between API Keys.
	for i := int64(1); i <= 32; i++ {
		wg.Go(func() {
			seed := fmt.Sprintf("%d\x00same-session", i)
			for range 16 {
				origins.Record(seed, i, time.Hour)
				id, ok := origins.Owner(seed)
				if !ok || id != i {
					t.Errorf("来源跨请求污染：want=%d got=%d found=%v", i, id, ok)
					return
				}
			}
		})
	}
	wg.Wait()
}
