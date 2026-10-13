package ops

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// updateServiceCacheStub supplies one mutable serialized release value for
// query fixtures.
type updateServiceCacheStub struct {
	// data is the latest value written by SetUpdateInfo, with empty text
	// interpreted as a cache miss. The fixture ignores expiration and is used
	// sequentially.
	data string
}

// updateServiceGitHubClientStub returns supplied metadata records for
// deterministic release-query scenarios.
type updateServiceGitHubClientStub struct {
	// release is returned unchanged by FetchLatestRelease with nil error,
	// including nil when unset. Tests that invoke latest lookup supply a usable
	// record for its dereferencing consumer.
	release *GitHubRelease
	// recentReleases is the supplied slice returned by recent-page lookup without
	// copying records or their assets. Candidate queries retain some of those same
	// pointers.
	recentReleases []*GitHubRelease
	// recentErr is returned alongside recentReleases, even when that slice is
	// nonempty. The query discards records when this configured error is nonnil.
	recentErr error
}

// GetUpdateInfo returns fixture data or a cache-miss error when it is empty.
// Context and cache expiration are not modeled here, leaving JSON age checking
// to the query under test.
func (s *updateServiceCacheStub) GetUpdateInfo(context.Context) (string, error) {
	if s.data == "" {
		return "", errors.New("cache miss")
	}
	return s.data, nil
}

// SetUpdateInfo replaces the fixture data and returns nil while ignoring
// context and ttl. Subsequent reads see the supplied text until another fixture
// write changes it; no expiration worker runs.
func (s *updateServiceCacheStub) SetUpdateInfo(_ context.Context, data string, _ time.Duration) error {
	s.data = data
	return nil
}

// FetchLatestRelease returns the supplied release pointer and nil error,
// ignoring repository and context. It performs no network read or copying;
// unset fixtures can return nil and are unsuitable for latest-result
// dereferencing.
func (s *updateServiceGitHubClientStub) FetchLatestRelease(context.Context, string) (*GitHubRelease, error) {
	return s.release, nil
}

// FetchRecentReleases returns the supplied recent slice and configured error
// while ignoring repository, page count, and context. Records remain shared, so
// tests keep their fixture metadata stable during candidate selection.
func (s *updateServiceGitHubClientStub) FetchRecentReleases(context.Context, string, int) ([]*GitHubRelease, error) {
	return s.recentReleases, s.recentErr
}

// DownloadFile panics unconditionally to reject asset transfers through this
// release-query fixture. The method implements no download behavior; metadata
// query services never call it, and installation tests need a downloader
// fixture.
func (s *updateServiceGitHubClientStub) DownloadFile(context.Context, string, string, int64) error {
	panic("DownloadFile should not be called when no update is available")
}

// FetchChecksumFile panics unconditionally when asked for checksum bytes
// through this metadata fixture. Query construction supplies only metadata
// methods, and callers use a checksum-capable fixture when exercising
// installation rather than release selection.
func (s *updateServiceGitHubClientStub) FetchChecksumFile(context.Context, string) ([]byte, error) {
	panic("FetchChecksumFile should not be called when no update is available")
}

// newRollbackTestService retains the supplied current version and recent
// release pointers in a ReleaseQuery with an empty cache and release build
// label. It performs no request; tests keep those borrowed records stable while
// querying.
func newRollbackTestService(current string, releases []*GitHubRelease) *ReleaseQuery {
	return NewReleaseQuery(
		&updateServiceCacheStub{},
		&updateServiceGitHubClientStub{recentReleases: releases},
		current,
		"release",
	)
}

// TestUpdateServiceListRollbackVersionsFiltersAndCaps supplies current/newer
// versions, a prerelease, a draft, duplicates, and more eligible history than
// the display cap. It checks the newest three older stable normalized versions
// remain.
func TestUpdateServiceListRollbackVersionsFiltersAndCaps(t *testing.T) {
	releases := []*GitHubRelease{
		{TagName: "v0.1.148", PublishedAt: "2026-07-09T00:00:00Z"},                       // Exclude versions newer than the current version.
		{TagName: "v0.1.147", PublishedAt: "2026-07-08T00:00:00Z"},                       // Exclude the current version.
		{TagName: "v0.1.146-rc1", PublishedAt: "2026-07-07T12:00:00Z", Prerelease: true}, // Exclude prereleases.
		{TagName: "v0.1.146", PublishedAt: "2026-07-07T00:00:00Z"},
		{TagName: "v0.1.145", PublishedAt: "2026-07-06T00:00:00Z", Draft: true}, // Exclude drafts.
		{TagName: "v0.1.144", PublishedAt: "2026-07-05T00:00:00Z"},
		{TagName: "v0.1.144", PublishedAt: "2026-07-05T00:00:00Z"}, // Exclude duplicate versions.
		{TagName: "v0.1.143", PublishedAt: "2026-07-04T00:00:00Z"},
		{TagName: "v0.1.142", PublishedAt: "2026-07-03T00:00:00Z"}, // Exclude entries beyond the three-version limit.
	}
	svc := newRollbackTestService("0.1.147", releases)

	versions, err := svc.ListRollbackVersions(context.Background())

	require.NoError(t, err)
	require.Len(t, versions, 3)
	require.Equal(t, "0.1.146", versions[0].Version)
	require.Equal(t, "0.1.144", versions[1].Version)
	require.Equal(t, "0.1.143", versions[2].Version)
}

// TestUpdateServiceListRollbackVersionsSortsUnorderedInput supplies three
// eligible older releases in mixed numeric order. It checks display values are
// ordered newest first independently of the fixture response sequence.
func TestUpdateServiceListRollbackVersionsSortsUnorderedInput(t *testing.T) {
	releases := []*GitHubRelease{
		{TagName: "v0.1.144"},
		{TagName: "v0.1.146"},
		{TagName: "v0.1.145"},
	}
	svc := newRollbackTestService("0.1.147", releases)

	versions, err := svc.ListRollbackVersions(context.Background())

	require.NoError(t, err)
	require.Len(t, versions, 3)
	require.Equal(t, "0.1.146", versions[0].Version)
	require.Equal(t, "0.1.145", versions[1].Version)
	require.Equal(t, "0.1.144", versions[2].Version)
}

// TestUpdateServiceListRollbackVersionsRejectsNonSemverTags supplies a numeric
// release alongside shell-like text, an alternate prefix, leading zeros, and
// build metadata. It checks strict rollback spelling accepts only the listed
// canonical numeric version.
func TestUpdateServiceListRollbackVersionsRejectsNonSemverTags(t *testing.T) {
	releases := []*GitHubRelease{
		{TagName: "v0.1.146"},
		{TagName: "v0.1.145;$(touch /tmp/tokenrouter-pwned)"},
		{TagName: "release-0.1.144"},
		{TagName: "v0.1.0143"},
		{TagName: "v0.1.142+meta"},
	}
	svc := newRollbackTestService("0.1.147", releases)

	versions, err := svc.ListRollbackVersions(context.Background())

	require.NoError(t, err)
	require.Equal(t, []RollbackVersion{{Version: "0.1.146"}}, versions)
}

// TestUpdateServiceListRollbackVersionsEmptyWhenNoneOlder supplies the running
// version and a newer release as its entire recent page. It checks candidate
// display returns an empty result with no error.
func TestUpdateServiceListRollbackVersionsEmptyWhenNoneOlder(t *testing.T) {
	releases := []*GitHubRelease{
		{TagName: "v0.1.147"},
		{TagName: "v0.1.148"},
	}
	svc := newRollbackTestService("0.1.147", releases)

	versions, err := svc.ListRollbackVersions(context.Background())

	require.NoError(t, err)
	require.Empty(t, versions)
}

// TestUpdateServiceListRollbackVersionsPropagatesFetchError injects a
// recent-release reader error for a valid running version. It checks the
// display query returns that failure text instead of a successful candidate
// list.
func TestUpdateServiceListRollbackVersionsPropagatesFetchError(t *testing.T) {
	svc := NewReleaseQuery(
		&updateServiceCacheStub{},
		&updateServiceGitHubClientStub{recentErr: errors.New("github unavailable")},
		"0.1.147",
		"release",
	)

	_, err := svc.ListRollbackVersions(context.Background())

	require.Error(t, err)
	require.Contains(t, err.Error(), "github unavailable")
}
