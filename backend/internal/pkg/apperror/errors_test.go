package apperror

import (
	stderrors "errors"
	"fmt"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestApplicationError_Basics supplies direct and wrapped application errors
// with differing messages and reasons. Status fields must match constructor
// inputs, and errors.Is must compare category and reason while ignoring
// message.
func TestApplicationError_Basics(t *testing.T) {
	tests := []struct {
		name    string            // subtest display name
		err     *ApplicationError // supplied error fixture
		want    Status            // expected public status
		wantIs  bool              // expected identity comparison result
		target  error             // identity comparison target
		wrapped error             // wrapped lookup fixture
	}{
		{
			name: "new",
			err:  New(400, "BAD_REQUEST", "invalid input"),
			want: Status{
				Code:    400,
				Reason:  "BAD_REQUEST",
				Message: "invalid input",
			},
		},
		{
			name:   "is_matches_code_and_reason",
			err:    New(401, "UNAUTHORIZED", "nope"),
			want:   Status{Code: 401, Reason: "UNAUTHORIZED", Message: "nope"},
			target: New(401, "UNAUTHORIZED", "ignored message"),
			wantIs: true,
		},
		{
			name:   "is_does_not_match_reason",
			err:    New(401, "UNAUTHORIZED", "nope"),
			want:   Status{Code: 401, Reason: "UNAUTHORIZED", Message: "nope"},
			target: New(401, "DIFFERENT", "ignored message"),
			wantIs: false,
		},
		{
			name:    "from_error_unwraps_wrapped_application_error",
			err:     New(404, "NOT_FOUND", "missing"),
			wrapped: fmt.Errorf("wrap: %w", New(404, "NOT_FOUND", "missing")),
			want: Status{
				Code:    404,
				Reason:  "NOT_FOUND",
				Message: "missing",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.err != nil {
				require.Equal(t, tt.want, tt.err.Status)
			}

			if tt.target != nil {
				require.Equal(t, tt.wantIs, stderrors.Is(tt.err, tt.target))
			}

			if tt.wrapped != nil {
				got := FromError(tt.wrapped)
				require.Equal(t, tt.want, got.Status)
			}
		})
	}
}

// TestApplicationError_WithMetadataDeepCopy attaches a populated map and a nil
// map to an error. Mutating the supplied populated map must leave the error
// metadata unchanged, while nil stays nil.
func TestApplicationError_WithMetadataDeepCopy(t *testing.T) {
	tests := []struct {
		name string            // subtest display name
		md   map[string]string // supplied metadata fixture
	}{
		{name: "non_nil", md: map[string]string{"a": "1"}},
		{name: "nil", md: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			appErr := BadRequest("BAD_REQUEST", "invalid input").WithMetadata(tt.md)

			if tt.md == nil {
				require.Nil(t, appErr.Metadata)
				return
			}

			tt.md["a"] = "changed"
			require.Equal(t, "1", appErr.Metadata["a"])
		})
	}
}

// TestFromError_Generic supplies plain and wrapped errors that have no
// application status. Each result must expose the generic public description
// and retain the supplied error as its cause.
func TestFromError_Generic(t *testing.T) {
	tests := []struct {
		name       string // subtest display name
		err        error  // supplied error fixture
		wantCode   int32  // expected numeric category
		wantReason string // expected reason identifier
		wantMsg    string // expected public message
	}{
		{
			name:       "plain_error",
			err:        stderrors.New("boom"),
			wantCode:   UnknownCode,
			wantReason: UnknownReason,
			wantMsg:    UnknownMessage,
		},
		{
			name:       "wrapped_plain_error",
			err:        fmt.Errorf("wrap: %w", io.EOF),
			wantCode:   UnknownCode,
			wantReason: UnknownReason,
			wantMsg:    UnknownMessage,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FromError(tt.err)
			require.Equal(t, tt.wantCode, got.Code)
			require.Equal(t, tt.wantReason, got.Reason)
			require.Equal(t, tt.wantMsg, got.Message)
			require.Equal(t, tt.err, got.Unwrap())
		})
	}
}
