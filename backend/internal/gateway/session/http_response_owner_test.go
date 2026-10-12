package session

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// ownerReaderProbe returns configured ownership and counts serial authorization reads.
type ownerReaderProbe struct {
	user, key int64 // stored response-owner user ID and stored response-owner API-key ID
	found     bool  // configured ownership presence result
	err       error // configured ownership lookup failure
	reads     int   // serial ownership lookup count
}

// GetHTTPResponseOwner counts the lookup and returns the configured ownership tuple.
func (p *ownerReaderProbe) GetHTTPResponseOwner(context.Context, int64, string) (int64, int64, bool, error) {
	p.reads++
	return p.user, p.key, p.found, p.err
}

// TestHTTPResponseOwnershipPreservesTenantAndLegacyKeys supplies modern user-owned
// records and legacy key-only records. Continuation accepts another key for the
// same user, rejects other users, and requires the legacy key to match.
func TestHTTPResponseOwnershipPreservesTenantAndLegacyKeys(t *testing.T) {
	for _, tc := range []struct {
		name string // named ownership comparison fixture
		// user and key configure the stored owner; requestUser and requestKey
		// identify the requester. A positive stored user selects user equality,
		// while a legacy nonpositive user selects key equality.
		user, key, requestUser, requestKey int64 // stored owner user ID, stored owner API-key ID, requester user identifier and requester API-key identifier
		// found controls whether the reader supplies an ownership record, while
		// want is the expected authorization decision for the configured IDs. A
		// missing record rejects continuation even when those IDs match.
		found, want bool // ownership record exists and expected continuation permission
	}{
		{"same-user-another-key", 1, 4, 1, 5, true, true},
		{"other-user-same-key", 1, 4, 2, 4, true, false},
		{"legacy-key-only", 0, 4, 2, 4, true, true},
		{"legacy-other-key", 0, 4, 2, 5, true, false},
		{"missing", 1, 4, 1, 4, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := &ownerReaderProbe{user: tc.user, key: tc.key, found: tc.found}
			ok, err := ValidateHTTPResponseOwner(context.Background(), func() HTTPResponseOwnerReader { return reader }, 7, "resp_1", tc.requestUser, tc.requestKey)
			require.NoError(t, err)
			require.Equal(t, tc.want, ok)
			require.Equal(t, 1, reader.reads)
		})
	}
}

// TestHTTPResponseOwnershipPreservesShortCircuitAndFailure supplies blank response
// IDs, invalid requester IDs, and a failing reader. Invalid inputs skip reader
// creation, while the valid lookup returns the configured storage error.
func TestHTTPResponseOwnershipPreservesShortCircuitAndFailure(t *testing.T) {
	reads := 0
	storeErr := errors.New("owner read unavailable")
	reader := &ownerReaderProbe{found: true, user: 1, key: 2, err: storeErr}
	get := func() HTTPResponseOwnerReader { reads++; return reader }
	for _, tc := range []struct {
		id        string // supplied continuation response ID
		user, key int64  // requester user identifier and requester API-key identifier
	}{{" ", 1, 2}, {"resp_1", 0, 2}, {"resp_1", 1, 0}} {
		ok, err := ValidateHTTPResponseOwner(context.Background(), get, 1, tc.id, tc.user, tc.key)
		require.NoError(t, err)
		require.False(t, ok)
	}
	require.Zero(t, reads)
	ok, err := ValidateHTTPResponseOwner(context.Background(), get, 1, "resp_1", 1, 2)
	require.ErrorIs(t, err, storeErr)
	require.False(t, ok)
	require.Equal(t, 1, reads)
}
