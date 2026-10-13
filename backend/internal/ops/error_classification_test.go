package ops

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestIsKnownOpsErrorType supplies recognized type labels and unknown or
// serialized-nil strings. It checks membership decisions used to keep
// normalization within the stored error-type vocabulary.
func TestIsKnownOpsErrorType(t *testing.T) {
	known := []string{
		"invalid_request_error",
		"authentication_error",
		"rate_limit_error",
		"billing_error",
		"subscription_error",
		"upstream_error",
		"overloaded_error",
		"api_error",
		"not_found_error",
		"forbidden_error",
	}
	for _, k := range known {
		require.True(t, isKnownOpsErrorType(k), "expected known: %s", k)
	}

	unknown := []string{"<nil>", "null", "", "random_error", "some_new_type", "<nil>\u003e"}
	for _, u := range unknown {
		require.False(t, isKnownOpsErrorType(u), "expected unknown: %q", u)
	}
}

// TestNormalizeOpsErrorType combines known, unknown, and empty types with
// funding/subscription codes. It checks code-based defaults and that a
// recognized type wins when its code suggests another classification.
func TestNormalizeOpsErrorType(t *testing.T) {
	tests := []struct {
		name    string // normalization case display name
		errType string // supplied observed type label
		code    string // supplied response classification code
		want    string // expected stored type label
	}{
		{"known invalid_request_error", "invalid_request_error", "", "invalid_request_error"},
		{"known rate_limit_error", "rate_limit_error", "", "rate_limit_error"},
		{"known upstream_error", "upstream_error", "", "upstream_error"},

		{"nil literal from upstream", "<nil>", "", "api_error"},
		{"null string", "null", "", "api_error"},
		{"random string", "something_weird", "", "api_error"},

		{"nil with INSUFFICIENT_BALANCE code", "<nil>", "INSUFFICIENT_BALANCE", "billing_error"},
		{"nil with USAGE_LIMIT_EXCEEDED code", "<nil>", "USAGE_LIMIT_EXCEEDED", "subscription_error"},

		{"empty type with balance code", "", "INSUFFICIENT_BALANCE", "billing_error"},
		{"empty type with subscription code", "", "SUBSCRIPTION_NOT_FOUND", "subscription_error"},
		{"empty type no code", "", "", "api_error"},

		{"known type overrides conflicting code", "rate_limit_error", "INSUFFICIENT_BALANCE", "rate_limit_error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizeErrorType(tt.errType, tt.code)
			require.Equal(t, tt.want, got)
		})
	}
}

// TestNativeAccountErrorsKeepTheirClassification passes native account wording
// to the local recognition helpers. It checks inactive-user text identifies
// client authentication and insufficient-account-balance text identifies a
// local business limit.
func TestNativeAccountErrorsKeepTheirClassification(t *testing.T) {
	require.True(t, isOpsClientAuthError("", "user account is not active"))
	require.True(t, isOpsLocalBusinessLimitError("", "insufficient account balance"))
}
