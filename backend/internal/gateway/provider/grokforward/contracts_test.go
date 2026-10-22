package grokforward

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestNewFailureRetryEvidence combines response retry guidance with health-only
// reuse decisions. Request-scoped handling requires response-derived 429 evidence,
// and captured headers remain independent while error bytes keep their ownership.
func TestNewFailureRetryEvidence(t *testing.T) {
	for _, tc := range []struct {
		name       string // retry evidence scenario
		status     int    // upstream response status
		retry      bool   // response retry evidence
		health     bool   // health reuse decision
		wantReuse  bool   // expected provider reuse
		wantScoped bool   // expected request-local handling
	}{
		{name: "health-only-429", status: 429, health: true, wantReuse: true},
		{name: "response-429", status: 429, retry: true, wantReuse: true, wantScoped: true},
		{name: "response-503", status: 503, retry: true, wantReuse: true},
		{name: "no-retry", status: 429},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deadline := time.Unix(100, 0)
			retry := Retry{Retryable: tc.retry, Delay: time.Second, Deadline: deadline, Max: 4}
			response := &http.Response{StatusCode: tc.status, Header: http.Header{"X-Test": {"first", "second"}}}
			body := []byte("error")
			failure := newFailure(response, body, retry, tc.health)
			require.Equal(t, tc.status, failure.StatusCode)
			require.Equal(t, tc.wantReuse, failure.RetryableOnSameProvider)
			require.Equal(t, tc.wantScoped, failure.RequestScopedTransient)
			require.Equal(t, time.Second, failure.SameProviderRetryDelay)
			require.Equal(t, deadline, failure.SameProviderRetryDeadline)
			require.Equal(t, 4, failure.SameProviderRetryMax)
			// Mutate both input containers after capture to distinguish the header
			// snapshot from the intentionally borrowed error bytes retained by the failure.
			response.Header["X-Test"][0] = "changed"
			body[0] = 'E'
			require.Equal(t, []string{"first", "second"}, failure.ResponseHeaders["X-Test"])
			require.Equal(t, "Error", string(failure.ResponseBody))
		})
	}
}
