package httpx

import (
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// TestLegacyAndCategoryErrorCompatibility
// constructs matching application errors across standard and custom codes.
// Equality, wrapping identity, mapped bodies, copied metadata, nil defaults, and
// a generic error's retained cause must match the fixture's asserted representations.
func TestLegacyAndCategoryErrorCompatibility(t *testing.T) {
	for _, code := range []int{200, 400, 401, 403, 404, 409, 418, 429, 499, 500, 502, 503, 504, 529} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			old := apperror.New(apperror.Category(code), "stable_reason", "safe message").WithMetadata(map[string]string{"scope": "read"})
			fresh := apperror.New(apperror.Category(code), "stable_reason", "safe message").WithMetadata(map[string]string{"scope": "read"})
			if old.Error() != fresh.Error() || !errors.Is(old, fresh) || !errors.Is(fresh, old) {
				t.Fatal("error identity changed")
			}
			wrapped := fmt.Errorf("outer: %w", fresh)
			var target *apperror.ApplicationError
			if !errors.As(wrapped, &target) || target != fresh {
				t.Fatal("legacy errors.As must retain the same entity")
			}
			oldCode, oldBody := ToHTTP(old)
			newCode, newBody := ToHTTP(wrapped)
			if oldCode != code || newCode != code || !reflect.DeepEqual(oldBody, newBody) || ErrorCode(wrapped) != code {
				t.Fatal("HTTP projection changed")
			}
			newBody.Metadata["scope"] = "write"
			if fresh.Metadata["scope"] != "read" {
				t.Fatal("HTTP metadata must be copied")
			}
		})
	}
	if ErrorCode(nil) != 200 || apperror.Reason(nil) != "" || apperror.Message(nil) != "" {
		t.Fatal("nil compatibility changed")
	}
	cause := errors.New("private database detail")
	converted := apperror.FromError(cause)
	_, body := ToHTTP(converted)
	if body.Message != "internal error" || !errors.Is(converted, cause) {
		t.Fatal("generic error must preserve its cause and safe response")
	}
}

// TestToHTTP_Legacy maps a nil interface and a forbidden application error.
// Both the numeric status and complete returned Status value must match their
// expected success or structured-error representation in the supplied cases.
func TestToHTTP_Legacy(t *testing.T) {
	tests := []struct {
		name           string          // mapping case for subtest output
		err            error           // supplied nil or application error
		wantStatusCode int             // expected numeric HTTP status
		wantBody       apperror.Status // expected serialized application status
	}{
		{
			name:           "nil_error",
			err:            nil,
			wantStatusCode: http.StatusOK,
			wantBody:       apperror.Status{Code: int32(http.StatusOK)},
		},
		{
			name:           "application_error",
			err:            apperror.Forbidden("FORBIDDEN", "no access"),
			wantStatusCode: http.StatusForbidden,
			wantBody: apperror.Status{
				Code:    int32(http.StatusForbidden),
				Reason:  "FORBIDDEN",
				Message: "no access",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, body := ToHTTP(tt.err)
			require.Equal(t, tt.wantStatusCode, code)
			require.Equal(t, tt.wantBody, body)
		})
	}
}

// TestToHTTP_MetadataDeepCopy_Legacy
// mutates input metadata and then the mapped error's entity metadata.
// The response metadata must retain its earlier value after both changes,
// protecting the independent string-map copy returned by ToHTTP.
func TestToHTTP_MetadataDeepCopy_Legacy(t *testing.T) {
	md := map[string]string{"k": "v"}
	appErr := apperror.BadRequest("BAD_REQUEST", "invalid").WithMetadata(md)

	code, body := ToHTTP(appErr)
	require.Equal(t, http.StatusBadRequest, code)
	require.Equal(t, "v", body.Metadata["k"])

	md["k"] = "changed"
	require.Equal(t, "v", body.Metadata["k"])

	appErr.Metadata["k"] = "changed-again"
	require.Equal(t, "v", body.Metadata["k"])
}
