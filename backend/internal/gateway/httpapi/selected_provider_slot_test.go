package httpapi

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// TestSelectedProviderSlotOwnsOnlyAcquiredResource supplies an already acquired
// selection with a release counter and no sticky service. The acquired hook
// runs once and repeated returned releases invoke underlying cleanup once.
func TestSelectedProviderSlotOwnsOnlyAcquiredResource(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	var released atomic.Int32
	acquired := 0
	hooks := ProviderSlotHooks{Acquired: func(*gin.Context) { acquired++ }, CapacityLimited: func(*gin.Context) { t.Fatal("unexpected capacity failure") }}
	started := false
	release, ok := AcquireSelectedProviderSlot(c, nil, "session", &SelectedProviderSlot{ProviderID: 12, Acquired: true, ReleaseFunc: func() { released.Add(1) }}, false, &started, zap.NewNop(), func(int, string, string, string) { t.Fatal("unexpected HTTP error") }, nil, nil, hooks)
	require.True(t, ok)
	require.Equal(t, 1, acquired)
	require.Zero(t, released.Load())
	release()
	release()
	require.EqualValues(t, 1, released.Load())
}

// TestSelectedProviderSlotMissingWaitPlanKeepsErrorShape supplies a selection
// with no acquired slot or wait plan. It requires the capacity observation and
// unchanged 503 error category, message and empty code.
func TestSelectedProviderSlotMissingWaitPlanKeepsErrorShape(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	limited := false
	status := 0
	kind, message := "", ""
	started := false
	release, ok := AcquireSelectedProviderSlot(c, nil, "", &SelectedProviderSlot{ProviderID: 12}, false, &started, zap.NewNop(), func(s int, k, code, m string) { status = s; kind = k; message = m; require.Empty(t, code) }, nil, nil, ProviderSlotHooks{Acquired: func(*gin.Context) { t.Fatal("unexpected acquired") }, CapacityLimited: func(*gin.Context) { limited = true }})
	require.False(t, ok)
	require.Nil(t, release)
	require.True(t, limited)
	require.Equal(t, 503, status)
	require.Equal(t, "api_error", kind)
	require.Equal(t, "No available providers", message)
}
