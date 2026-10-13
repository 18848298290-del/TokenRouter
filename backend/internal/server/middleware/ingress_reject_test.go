package middleware

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Rejection recording uses canonical client buckets and runs after the handler
// has attached its marker. The tests inspect a recorder installed for one
// request and compare IPv6, mapped IPv4, and malformed address normalization.

// ingressRejectRecorderStub captures call count and the last client bucket for
// assertions.
type ingressRejectRecorderStub struct {
	// mu protects calls and clientIP together. RecordIngressReject updates both
	// under the lock, and tests take that lock before inspecting their matching
	// observation.
	mu       sync.Mutex // captured observation mutex
	calls    int        // recorder invocation count
	clientIP string     // last normalized client bucket
}

// TestNormalizeIngressRejectIP supplies IPv6, mapped IPv4, and invalid address
// text. The results protect /64 grouping, IPv4 canonicalization, and the fixed
// sentinel for malformed input.
func TestNormalizeIngressRejectIP(t *testing.T) {
	require.Equal(t, "2001:db8:abcd:1234::", normalizeIngressRejectIP("2001:db8:abcd:1234:ffff::1"))
	require.Equal(t, "192.0.2.4", normalizeIngressRejectIP("::ffff:192.0.2.4"))
	require.Equal(t, "0.0.0.0", normalizeIngressRejectIP("not-an-ip"))
}

// TestLoggerRecordsIngressRejectOnce attaches an invalid-key marker while
// serving one request through Logger. The installed recorder observes one call
// with the masked IPv6 client bucket.
func TestLoggerRecordsIngressRejectOnce(t *testing.T) {
	recorder := &ingressRejectRecorderStub{}
	// Install the recorder for this request and disable it during cleanup. Shared
	// recorder state therefore cannot retain this fixture for subsequent tests.
	SetIngressRejectRecorder(recorder)
	t.Cleanup(func() { SetIngressRejectRecorder(nil) })
	router := gin.New()
	router.Use(Logger())
	router.GET("/v1/messages", func(c *gin.Context) {
		MarkIngressRejected(c, IngressRejectInvalidAPIKey)
		c.Status(http.StatusUnauthorized)
	})
	request := httptest.NewRequest(http.MethodGet, "/v1/messages", nil)
	request.RemoteAddr = "[2001:db8:abcd:1234:ffff::1]:1234"
	router.ServeHTTP(httptest.NewRecorder(), request)
	recorder.mu.Lock()
	require.Equal(t, 1, recorder.calls)
	require.Equal(t, "2001:db8:abcd:1234::", recorder.clientIP)
	recorder.mu.Unlock()
}

// TestNormalizeIngressRejectIPGroupsIPv6By64 compares two addresses within the
// same IPv6 network. Both normalize to the same /64 bucket despite differing
// host bits.
func TestNormalizeIngressRejectIPGroupsIPv6By64(t *testing.T) {
	require.Equal(t, "2001:db8:abcd:1234::", normalizeIngressRejectIP("2001:db8:abcd:1234:1111::1"))
	require.Equal(t, normalizeIngressRejectIP("2001:db8:abcd:1234:1111::1"), normalizeIngressRejectIP("2001:db8:abcd:1234:ffff::2"))
}

// RecordIngressReject captures a matching call count and client bucket under
// the fixture mutex. Other aggregate arguments are ignored; assertions take the
// same lock before reading the captured pair.
func (r *ingressRejectRecorderStub) RecordIngressReject(_, _, _, clientIP string, _, _ int64) {
	// Update the count and last bucket together so a reader cannot pair one call's
	// count with another call's still-unpublished client value.
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	r.clientIP = clientIP
}
