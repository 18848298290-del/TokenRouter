package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	httpapitestkit "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi/testkit"
	"github.com/TokenFlux/TokenRouter/internal/gateway/tierpolicy"
)

// TestWriteOpenAIFastPolicyBlockedResponse_AfterKeepaliveCommit waits for a
// Compact heartbeat before a fast-tier rejection. It checks HTTP 200 and one
// response.failed event carrying the permission error and message.
func TestWriteOpenAIFastPolicyBlockedResponse_AfterKeepaliveCommit(t *testing.T) {
	c, rec := newCompactBridgeTestContext(t, true)
	// Start the heartbeat and retain cleanup for the test; wait for commitment
	// before checking how the subsequent policy rejection ends the stream.
	stop := StartOpenAICompactSSEKeepalive(c, keepaliveTestInterval)
	defer stop()
	waitForKeepaliveBeats()
	WriteFastPolicyBlockedResponse(c, &tierpolicy.BlockedError{Message: "tier blocked"})

	require.Equal(t, http.StatusOK, rec.Code)
	events := httpapitestkit.ParseCompactSSE(t, stripKeepaliveComments(rec.Body.String()))
	require.Len(t, events, 1)
	require.Equal(t, "response.failed", events[0][0])
	require.Equal(t, "permission_error", gjson.Get(events[0][1], "response.error.code").String())
	require.Contains(t, gjson.Get(events[0][1], "response.error.message").String(), "tier blocked")
}

// TestWriteOpenAIFastPolicyBlockedResponse_BeforeKeepaliveCommit rejects
// fast-tier use before the delayed Compact heartbeat. It checks HTTP 403 and
// the permission_error field in the resulting JSON envelope.
func TestWriteOpenAIFastPolicyBlockedResponse_BeforeKeepaliveCommit(t *testing.T) {
	c, rec := newCompactBridgeTestContext(t, true)
	// Delay the heartbeat beyond this assertion window and stop it on return
	// so the rejection can exercise the still-uncommitted JSON output path.
	stop := StartOpenAICompactSSEKeepalive(c, time.Hour)
	defer stop()
	WriteFastPolicyBlockedResponse(c, &tierpolicy.BlockedError{Message: "tier blocked"})

	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Equal(t, "permission_error", gjson.Get(rec.Body.String(), "error.type").String())
}

// TestWriteOpenAIFastPolicyBlockedResponseMarksBusinessLimited writes a
// fast-tier rejection into an empty response. It checks HTTP 403 and the
// request's local-policy business-limitation marker and reason.
func TestWriteOpenAIFastPolicyBlockedResponseMarksBusinessLimited(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	WriteFastPolicyBlockedResponse(c, &tierpolicy.BlockedError{Message: "custom fast policy block"})

	require.Equal(t, http.StatusForbidden, rec.Code)
	require.True(t, HasOpsClientBusinessLimited(c))
	reason, ok := c.Get(OpsClientBusinessLimitedReasonKey)
	require.True(t, ok)
	require.Equal(t, OpsClientBusinessLimitedReasonLocalPolicyDenied, reason)
}

// TestWriteOpenAIPassthroughErrorHeaders_StrictRetryAfter replaces an existing
// delay header with the listed numeric and date candidates. It checks
// retention of valid values and removal of invalid or expired ones.
func TestWriteOpenAIPassthroughErrorHeaders_StrictRetryAfter(t *testing.T) {
	now := time.Now().UTC()
	tests := []struct {
		name string // fixture case name
		raw  string // upstream delay value
		want bool   // expected header acceptance
	}{
		{name: "positive delay seconds", raw: "17", want: true},
		{name: "fractional delay", raw: "1.5"},
		{name: "scientific notation", raw: "1e3"},
		{name: "explicit plus sign", raw: "+17"},
		{name: "zero", raw: "0"},
		{name: "negative delay", raw: "-1"},
		{name: "uint64 overflow", raw: "18446744073709551616"},
		{name: "future http date", raw: now.Add(time.Hour).Format(http.TimeFormat), want: true},
		{name: "past http date", raw: now.Add(-time.Hour).Format(http.TimeFormat)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dst := http.Header{"Retry-After": []string{"stale"}}
			WriteForwardPassthroughErrorHeaders(dst, http.Header{"Retry-After": []string{tt.raw}})
			if tt.want {
				require.Equal(t, tt.raw, dst.Get("Retry-After"))
			} else {
				require.Empty(t, dst.Get("Retry-After"))
			}
		})
	}
}
