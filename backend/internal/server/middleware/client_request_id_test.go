package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
)

// A caller's correlation header is separate from the local diagnostic identity
// returned by gateway middleware. These cases exercise missing, valid, unsafe,
// and oversized identifiers while checking which values reach the request
// context and response headers.

// TestClientRequestID_GeneratesWhenMissing supplies spoofed internal headers
// without a caller correlation ID. The request gains a local ID, the spoofed
// headers are stripped, and both internal response names agree.
func TestClientRequestID_GeneratesWhenMissing(t *testing.T) {
	r := gin.New()
	r.Use(ClientRequestID())
	r.GET("/t", func(c *gin.Context) {
		v := c.Request.Context().Value(telemetry.ClientRequestID)
		require.NotNil(t, v)
		id, ok := v.(string)
		require.True(t, ok)
		require.NotEmpty(t, id)
		require.Empty(t, c.Request.Header.Get(clientRequestIDHeader))
		require.Empty(t, c.Request.Header.Get(internalRequestIDHeader))
		require.Empty(t, c.Request.Header.Get(legacyInternalRequestIDHeader))
		c.Status(http.StatusOK)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/t", nil)
	req.Header.Set(internalRequestIDHeader, "spoofed-internal-request-id")
	req.Header.Set(legacyInternalRequestIDHeader, "spoofed-legacy-id")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.NotEmpty(t, w.Header().Get("X-Client-Request-Id"))
	require.NotEqual(t, "spoofed-internal-request-id", w.Header().Get(internalRequestIDHeader))
	require.Equal(t, w.Header().Get(internalRequestIDHeader), w.Header().Get(legacyInternalRequestIDHeader))
}

// TestClientRequestIDSeparatesInternalAndParentIDs sends a valid caller
// correlation header through the gateway middleware. The context and response
// retain that parent while the local UUID identifies the request independently.
func TestClientRequestIDSeparatesInternalAndParentIDs(t *testing.T) {
	var internalID string
	r := gin.New()
	r.Use(ClientRequestID())
	r.POST("/t", func(c *gin.Context) {
		id, ok := c.Request.Context().Value(telemetry.ClientRequestID).(string)
		require.True(t, ok)
		require.NotEqual(t, "upstream-request-123", id)
		require.Len(t, id, 36)
		internalID = id
		parentID, ok := c.Request.Context().Value(telemetry.ParentClientRequestID).(string)
		require.True(t, ok)
		require.Equal(t, "upstream-request-123", parentID)
		require.Equal(t, parentID, c.Request.Header.Get(clientRequestIDHeader))
		require.Equal(t, parentID, c.Writer.Header().Get(clientRequestIDHeader))
		require.Empty(t, c.Request.Header.Get(internalRequestIDHeader))
		require.Empty(t, c.Request.Header.Get(legacyInternalRequestIDHeader))
		require.Equal(t, id, c.Writer.Header().Get(internalRequestIDHeader))
		c.Status(http.StatusOK)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/t", nil)
	req.Header.Set(clientRequestIDHeader, "upstream-request-123")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "upstream-request-123", w.Header().Get(clientRequestIDHeader))
	require.Equal(t, internalID, w.Header().Get(internalRequestIDHeader))
}

// TestClientRequestIDRejectsUnsafeIncomingHeader sends a correlation ID
// containing embedded spaces. The parent is absent from context and the
// response uses the local UUID, while downstream code can still read the
// incoming header.
func TestClientRequestIDRejectsUnsafeIncomingHeader(t *testing.T) {
	r := gin.New()
	r.Use(ClientRequestID())
	r.GET("/t", func(c *gin.Context) {
		id, ok := c.Request.Context().Value(telemetry.ClientRequestID).(string)
		require.True(t, ok)
		require.Len(t, id, 36)
		_, parentOK := c.Request.Context().Value(telemetry.ParentClientRequestID).(string)
		require.False(t, parentOK)
		require.Equal(t, "request id with spaces", c.Request.Header.Get(clientRequestIDHeader))
		require.Equal(t, id, c.Writer.Header().Get(clientRequestIDHeader))
		c.Status(http.StatusOK)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/t", nil)
	req.Header.Set(clientRequestIDHeader, "request id with spaces")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
}

// TestClientRequestID_PreservesExisting starts with a valid ID in the request
// context. The middleware reuses that ID and returns it in the
// caller-correlation response header when no parent was supplied.
func TestClientRequestID_PreservesExisting(t *testing.T) {
	r := gin.New()
	r.Use(ClientRequestID())
	r.GET("/t", func(c *gin.Context) {
		id, ok := c.Request.Context().Value(telemetry.ClientRequestID).(string)
		require.True(t, ok)
		require.Equal(t, "keep", id)
		c.Status(http.StatusOK)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/t", nil)
	req = req.WithContext(context.WithValue(req.Context(), telemetry.ClientRequestID, "keep"))
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "keep", w.Header().Get(clientRequestIDHeader))
}

// TestClientRequestID_ReplacesOversizedExistingID starts with an internal
// context value exceeding the persisted byte limit. The handler and response
// receive the same newly generated UUID after that value is rejected.
func TestClientRequestID_ReplacesOversizedExistingID(t *testing.T) {
	r := gin.New()
	r.Use(ClientRequestID())
	r.GET("/t", func(c *gin.Context) {
		id, ok := c.Request.Context().Value(telemetry.ClientRequestID).(string)
		require.True(t, ok)
		require.Len(t, id, 36)
		c.String(http.StatusOK, id)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/t", nil)
	req = req.WithContext(context.WithValue(req.Context(), telemetry.ClientRequestID, strings.Repeat("x", 200)))
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, w.Body.String(), w.Header().Get(clientRequestIDHeader))
}
