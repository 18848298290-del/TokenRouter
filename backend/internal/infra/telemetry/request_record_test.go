package telemetry

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

// TestNormalizeRequestRecordRemovesUnstorableText covers NULs, invalid UTF-8, and multibyte truncation.
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

// TestUpdateRequestForIDChecksCaptureIdentity checks that a mismatched task ID cannot update the parent.
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

// TestRecordRelatedRequestFinishesTerminalStates checks completion times for failed and canceled records too.
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
