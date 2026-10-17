package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

// Request-record tests exercise storage-safe text, exact capture identity,
// and related request completion across lifecycle states. Assertions check
// normalized bytes and retained caller inputs separately, then require child
// records to inherit parent identity and receive terminal finish times when
// appropriate.

// TestNormalizeRequestRecordRemovesUnstorableText supplies NULs, invalid
// UTF-8, oversized multibyte text, and invalid external IDs. It requires
// storable normalized fields and unchanged selected input fields and attempts
// after copying.
func TestNormalizeRequestRecordRemovesUnstorableText(t *testing.T) {
	input := RequestRecord{
		RequestID: "local-id", Path: "/invalid\x00path", Model: "模型\x00" + strings.Repeat("模", 100),
		Method: "GE\x00T", Platform: "open\xffai\x00", ErrorCode: "http\x00_404",
		Aliases:  []RequestAlias{{Kind: "call\x00er", Value: "valid"}, {Kind: "caller", Value: "bad\x00id"}},
		Attempts: []RequestAttempt{{Outcome: "fail\x00ed", RequestID: "bad\x00id"}},
	}
	record := NormalizeRequestRecord(input)
	require.Equal(t, "/invalidpath", record.Path)
	require.Equal(t, "GET", record.Method)
	require.Equal(t, "openai", record.Platform)
	require.Equal(t, "http_404", record.ErrorCode)
	require.LessOrEqual(t, len(record.Model), 200)
	require.True(t, utf8.ValidString(record.Model))
	require.Equal(t, []RequestAlias{{Kind: "caller", Value: "valid"}}, record.Aliases)
	require.Equal(t, []RequestAttempt{{Outcome: "failed"}}, record.Attempts)
	body, err := json.Marshal(record)
	require.NoError(t, err)
	require.NotContains(t, string(body), `\u0000`)
	require.Equal(t, "/invalid\x00path", input.Path)
	require.Equal(t, "fail\x00ed", input.Attempts[0].Outcome)
}

// TestUpdateRequestForIDChecksCaptureIdentity attempts updates without a
// capture and with mismatched and matching IDs. It requires false results and
// an untouched callback until the exact parent identity matches.
func TestUpdateRequestForIDChecksCaptureIdentity(t *testing.T) {
	ctx := WithRequestCapture(t.Context(), RequestRecord{RequestID: "parent"}, nil)
	called := false
	update := func(*RequestRecord) { called = true }
	require.False(t, UpdateRequestForID(context.Background(), "parent", update))
	require.False(t, UpdateRequestForID(ctx, "child", update))
	require.False(t, called)
	require.True(t, UpdateRequestForID(ctx, "parent", update))
	require.True(t, called)
}

// TestRecordRelatedRequestFinishesTerminalStates publishes running,
// completed, failed, and canceled children through a sampled observer. It
// requires inherited parent/key identity and a finish time for each terminal
// state, with running left unfinished.
func TestRecordRelatedRequestFinishesTerminalStates(t *testing.T) {
	for _, state := range []string{"running", "completed", "failed", "canceled"} {
		t.Run(state, func(t *testing.T) {
			var latest RequestRecord
			ctx := WithRequestCapture(t.Context(), RequestRecord{RequestID: "parent", APIKeyID: 1}, func(record RequestRecord) {
				if record.ParentRequestID != "" {
					latest = record
				}
			})
			RecordRelatedRequest(ctx, RequestRecord{RequestID: "task", State: state, StartedAt: time.Now().Add(-time.Minute)})
			require.Equal(t, "parent", latest.ParentRequestID)
			require.Equal(t, int64(1), latest.APIKeyID)
			if state == "running" {
				require.Nil(t, latest.FinishedAt)
			} else {
				require.NotNil(t, latest.FinishedAt)
			}
		})
	}
}

// TestMergeRequestRecordRetainsSparseTimingStages combines an earlier admission
// breakdown with a later transport milestone. Neither input map is mutated,
// and measured zero remains available after sparse completion enrichment.
func TestMergeRequestRecordRetainsSparseTimingStages(t *testing.T) {
	now := time.Now()
	before := RequestRecord{UpdatedAt: now, FailureTimings: map[string]int64{"request_body_read_ms": 120, "provider_slot_wait_ms": 0}}
	after := RequestRecord{UpdatedAt: now.Add(time.Second), FailureTimings: map[string]int64{"failed_at_ms": 500}}
	got := MergeRequestRecord(before, after)
	require.Equal(t, int64(120), got.FailureTimings["request_body_read_ms"])
	require.Contains(t, got.FailureTimings, "provider_slot_wait_ms")
	require.Equal(t, int64(500), got.FailureTimings["failed_at_ms"])
	got.FailureTimings["provider_slot_wait_ms"] = 99
	require.Zero(t, before.FailureTimings["provider_slot_wait_ms"])
	require.Len(t, after.FailureTimings, 1)
}

// TestWithoutRequestCapturePreservesRequestContext masks model diagnostics for
// delivery downloads. Their cancellation, deadline, and request identity remain
// shared while header and failure observations cannot alter the parent capture.
func TestWithoutRequestCapturePreservesRequestContext(t *testing.T) {
	base, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	base = context.WithValue(base, RequestID, "parent")
	var saved RequestRecord
	base = WithRequestCapture(base, RequestRecord{RequestID: "parent"}, func(r RequestRecord) { saved = r })
	base = WithRequestProvider(base, 7, "X-Custom")
	hidden := WithoutRequestCapture(base)
	require.Equal(t, "parent", RequestIDValue(hidden))
	want, _ := base.Deadline()
	got, _ := hidden.Deadline()
	require.Equal(t, want, got)
	dispatch := BeginRequestAttempt(hidden, 0)
	ObserveRequestResult(dispatch, RequestAttemptFromContext(dispatch), errors.New("failed"))
	require.Empty(t, saved.Attempts)
	cancel()
	require.ErrorIs(t, hidden.Err(), context.Canceled)
}
