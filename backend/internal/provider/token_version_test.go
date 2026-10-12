package provider

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// tokenVersionReader supplies the database record for token version checks.
type tokenVersionReader struct {
	value *Record
	err   error
}

func TestCheckTokenVersion(t *testing.T) {
	tests := []struct {
		name           string
		provider       *Record
		latestProvider *Record
		repoErr        error
		expectedStale  bool
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
			// Compare the supplied snapshot against the simulated database record.
			repo := tokenVersionReader{value: tt.latestProvider, err: tt.repoErr}
			_, isStale := CheckTokenVersion(context.Background(), tt.provider, repo)
			require.Equal(t, tt.expectedStale, isStale)
		})
	}
}

func TestCheckTokenVersion_NilRepo(t *testing.T) {
	provider := &Record{
		ID:          1,
		Credentials: map[string]any{"_token_version": int64(100)},
	}
	_, isStale := CheckTokenVersion(context.Background(), provider, nil)
	require.False(t, isStale) // A missing repository permits use of the current token.
}

func (r tokenVersionReader) GetByID(context.Context, int64) (*Record, error) {
	return r.value, r.err
}
