package media

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestGrokVideoRequestStateSeparatesStatusFromBilling checks that terminal state alone does not establish billable output.
func TestGrokVideoRequestStateSeparatesStatusFromBilling(t *testing.T) {
	for _, test := range []struct {
		body     string
		state    string
		billable bool
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
