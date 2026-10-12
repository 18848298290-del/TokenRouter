package provider

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// Token sources distinguish a newer stored credential version from an
// unversioned or current request snapshot. A configured reader supplies one
// repository observation, and the table checks the stale flag across missing
// inputs, failed reads and each tested version ordering.

// tokenVersionReader supplies one configured record or error for token-version
// comparisons.
type tokenVersionReader struct {
	// value and err supply one repository observation to GetByID. The record is
	// returned by reference, and table cases keep it stable during the comparison.
	value *Record // configured repository record pointer
	err   error   // configured repository read error
}

// TestCheckTokenVersion compares request snapshots with configured repository
// observations, including absent records and lookup errors. The assertions
// protect the stale flag for unversioned, equal, newer and older stored
// versions.
func TestCheckTokenVersion(t *testing.T) {
	tests := []struct {
		name           string  // version comparison case label
		provider       *Record // captured request provider record
		latestProvider *Record // configured repository provider observation
		repoErr        error   // injected repository lookup failure
		expectedStale  bool    // expected newer stored version
	}{
		{
			name:           "nil_provider",
			provider:       nil,
			latestProvider: nil,
			expectedStale:  false,
		},
		{
			name: "no_version_in_provider_but_db_has_version",
			provider: &Record{
				ID:          1,
				Credentials: map[string]any{},
			},
			latestProvider: &Record{
				ID:          1,
				Credentials: map[string]any{"_token_version": int64(100)},
			},
			expectedStale: true, // A database version supersedes an unversioned snapshot.
		},
		{
			name: "both_no_version",
			provider: &Record{
				ID:          1,
				Credentials: map[string]any{},
			},
			latestProvider: &Record{
				ID:          1,
				Credentials: map[string]any{},
			},
			expectedStale: false, // Unversioned records are treated as current.
		},
		{
			name: "same_version",
			provider: &Record{
				ID:          1,
				Credentials: map[string]any{"_token_version": int64(100)},
			},
			latestProvider: &Record{
				ID:          1,
				Credentials: map[string]any{"_token_version": int64(100)},
			},
			expectedStale: false,
		},
		{
			name: "current_version_newer",
			provider: &Record{
				ID:          1,
				Credentials: map[string]any{"_token_version": int64(200)},
			},
			latestProvider: &Record{
				ID:          1,
				Credentials: map[string]any{"_token_version": int64(100)},
			},
			expectedStale: false,
		},
		{
			name: "current_version_older_stale",
			provider: &Record{
				ID:          1,
				Credentials: map[string]any{"_token_version": int64(100)},
			},
			latestProvider: &Record{
				ID:          1,
				Credentials: map[string]any{"_token_version": int64(200)},
			},
			expectedStale: true, // The database version is newer.
		},
		{
			name: "repo_error",
			provider: &Record{
				ID:          1,
				Credentials: map[string]any{"_token_version": int64(100)},
			},
			latestProvider: nil,
			repoErr:        errors.New("db error"),
			expectedStale:  false, // Lookup failures permit use of the current token.
		},
		{
			name: "repo_returns_nil",
			provider: &Record{
				ID:          1,
				Credentials: map[string]any{"_token_version": int64(100)},
			},
			latestProvider: nil,
			repoErr:        nil,
			expectedStale:  false, // A missing record permits use of the current token.
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// The fixture supplies a single repository observation, and the assertion
			// checks the stale decision rather than coordinating any later cache write.
			repo := tokenVersionReader{value: tt.latestProvider, err: tt.repoErr}
			_, isStale := CheckTokenVersion(context.Background(), tt.provider, repo)
			require.Equal(t, tt.expectedStale, isStale)
		})
	}
}

// TestCheckTokenVersion_NilRepo supplies a versioned request record with no
// repository dependency. The stale flag stays false, allowing token sources to
// continue using the captured credentials.
func TestCheckTokenVersion_NilRepo(t *testing.T) {
	provider := &Record{
		ID:          1,
		Credentials: map[string]any{"_token_version": int64(100)},
	}
	_, isStale := CheckTokenVersion(context.Background(), provider, nil)
	require.False(t, isStale) // A missing repository permits use of the current token.
}

// GetByID returns the configured record pointer and error for any requested
// ID. Context and identity are ignored by this reader, and tests keep the
// borrowed observation stable during each comparison.
func (r tokenVersionReader) GetByID(context.Context, int64) (*Record, error) {
	return r.value, r.err
}
