package media

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestGrokVideoRequestStateSeparatesStatusFromBilling supplies active,
// terminal, unknown and malformed status bodies. It checks summary state
// independently of billability, including done with a URL versus done
// without output evidence.
func TestGrokVideoRequestStateSeparatesStatusFromBilling(t *testing.T) {
	for _, test := range []struct {
		body     string // supplied task status JSON
		state    string // expected normalized request-summary state
		billable bool   // expected completed-output billing eligibility
	}{
		{`{"status":"pending"}`, "running", false},
		{`{"status":"done","video":{"url":"https://example.test/video.mp4"}}`, "completed", true},
		{`{"status":"done"}`, "completed", false},
		{`{"status":"failed"}`, "failed", false},
		{`{"status":"expired"}`, "failed", false},
		{`{"status":"cancelled"}`, "canceled", false},
		{`{"status":"canceled"}`, "canceled", false},
		{`{"status":"unknown"}`, "", false},
		{`{}`, "", false},
		{`{"status":"done"`, "", false},
	} {
		t.Run(test.body, func(t *testing.T) {
			require.Equal(t, test.state, GrokVideoRequestState([]byte(test.body)))
			require.Equal(t, test.billable, IsGrokVideoStatusBillable([]byte(test.body)))
		})
	}
}
