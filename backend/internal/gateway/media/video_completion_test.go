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

// TestVideoStatusUsesActualLongDurationAndSavedResolution merges a custom creation
// label with completed duration evidence. Missing completion duration uses the
// saved duration, and failed tasks never produce a billable completion.
func TestVideoStatusUsesActualLongDurationAndSavedResolution(t *testing.T) {
	pending := &GrokVideoPendingBilling{Model: "custom-video", VideoResolution: "custom-resolution", VideoDurationSeconds: 30}
	result := ExtractGrokVideoBillingFromStatusBody([]byte(`{"status":"done","video":{"url":"https://example.test/video.mp4","duration":60}}`), pending, "task", "")
	require.NotNil(t, result)
	require.Equal(t, 60, result.VideoDurationSeconds)
	require.Equal(t, "custom-resolution", result.VideoResolution)
	result = ExtractGrokVideoBillingFromStatusBody([]byte(`{"status":"done","video":{"url":"https://example.test/video.mp4"}}`), pending, "task", "")
	require.Equal(t, 30, result.VideoDurationSeconds)
	require.Nil(t, ExtractGrokVideoBillingFromStatusBody([]byte(`{"status":"failed"}`), pending, "task", ""))
}
