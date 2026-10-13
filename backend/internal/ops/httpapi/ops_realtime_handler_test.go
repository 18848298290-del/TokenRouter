package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// TestParseOpsRealtimeWindow parses a five-minute alias and an invalid value
// through the realtime window helper. It checks the accepted duration and
// canonical label, then rejects the unsupported value.
func TestParseOpsRealtimeWindow(t *testing.T) {
	dur, label, ok := parseOpsRealtimeWindow("5m")
	require.True(t, ok)
	require.Equal(t, 5*time.Minute, dur)
	require.Equal(t, "5min", label)

	_, _, ok = parseOpsRealtimeWindow("invalid")
	require.False(t, ok)
}

// TestIsOpsRealtimeRequestCanceled exercises direct cancellation, the database
// diagnostic, and a canceled request with a generic error. It also checks a nil
// error and an unrelated failure with no request remain unrecognized.
func TestIsOpsRealtimeRequestCanceled(t *testing.T) {
	require.False(t, isOpsRealtimeRequestCanceled(nil, nil))
	require.True(t, isOpsRealtimeRequestCanceled(nil, context.Canceled))
	require.True(t, isOpsRealtimeRequestCanceled(nil, errors.New("pq: canceling statement due to user request")))

	// Use a generic failure with an already canceled request to exercise
	// context-based classification when no cancellation error wrapper is present.
	requestCtx, cancel := context.WithCancel(context.Background())
	cancel()
	c := &gin.Context{Request: httptest.NewRequest(http.MethodGet, "/api/v1/admin/ops/concurrency", nil).WithContext(requestCtx)}
	require.True(t, isOpsRealtimeRequestCanceled(c, errors.New("query failed")))
	require.False(t, isOpsRealtimeRequestCanceled(&gin.Context{}, errors.New("query failed")))
}
