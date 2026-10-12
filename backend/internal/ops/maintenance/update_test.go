package maintenance

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/ops"
	opsadapter "github.com/TokenFlux/TokenRouter/internal/ops/provider"
)

// updateServiceCacheStub retains update JSON for sequential release-query fixture calls.
type updateServiceCacheStub struct {
	data string // stored fixture update JSON
}

// updateServiceGitHubClientStub supplies selected release responses for maintenance eligibility tests.
type updateServiceGitHubClientStub struct {
	release *ops.GitHubRelease // latest fixture release metadata
	// recentReleases and recentErr are returned together for candidate enumeration.
	// Tests configure them before query calls to choose eligible release data or an enumeration failure.
	recentReleases []*ops.GitHubRelease // recent fixture release records
	recentErr      error                // injected candidate enumeration failure
}

// GetUpdateInfo returns fixture update JSON or a cache-miss error for empty data.
// The release query consumes that miss by fetching its configured client response;
// this fixture does not consult the supplied context or an external cache.
func (s *updateServiceCacheStub) GetUpdateInfo(context.Context) (string, error) {
	if s.data == "" {
		return "", errors.New("cache miss")
	}
	return s.data, nil
}

// SetUpdateInfo replaces the sequential fixture's cached JSON and returns success.
// Context and TTL are unused because these eligibility tests inspect query results
// without modelling cache expiry or concurrent cache generations.
func (s *updateServiceCacheStub) SetUpdateInfo(_ context.Context, data string, _ time.Duration) error {
	s.data = data
	return nil
}

// FetchLatestRelease returns the configured latest release without remote network access.
func (s *updateServiceGitHubClientStub) FetchLatestRelease(context.Context, string) (*ops.GitHubRelease, error) {
	return s.release, nil
}

// FetchRecentReleases returns the supplied candidate fixture records and enumeration error.
func (s *updateServiceGitHubClientStub) FetchRecentReleases(context.Context, string, int) ([]*ops.GitHubRelease, error) {
	return s.recentReleases, s.recentErr
}

// DownloadFile panics when eligibility tests unexpectedly begin binary download work.
func (s *updateServiceGitHubClientStub) DownloadFile(context.Context, string, string, int64) error {
	panic("DownloadFile should not be called when no update is available")
}

// FetchChecksumFile panics when these eligibility fixtures unexpectedly request checksum downloads.
func (s *updateServiceGitHubClientStub) FetchChecksumFile(context.Context, string) ([]byte, error) {
	panic("FetchChecksumFile should not be called when no update is available")
}

// TestUpdateServicePerformUpdateNoUpdateReturnsSentinel supplies a latest release
// matching the running version. PerformUpdate must return the no-update sentinel,
// keeping this eligibility result distinguishable from installer failures.
func TestUpdateServicePerformUpdateNoUpdateReturnsSentinel(t *testing.T) {
	svc := newMaintenanceUpdateForTest(
		&updateServiceCacheStub{},
		&updateServiceGitHubClientStub{
			release: &ops.GitHubRelease{
				TagName: "v0.1.132",
				Name:    "v0.1.132",
			},
		},
		"0.1.132",
		"release",
	)

	err := svc.PerformUpdate(context.Background())

	require.Error(t, err)
	require.True(t, errors.Is(err, ErrNoUpdateAvailable))
	require.ErrorIs(t, err, ErrNoUpdateAvailable)
}

// newRollbackTestService builds a release-query fixture using the selected current
// version and recent release list. The real installer remains bound so accepted
// targets can reach platform asset selection while rejected targets stop earlier.
func newRollbackTestService(current string, releases []*ops.GitHubRelease) *UpdateService {
	return newMaintenanceUpdateForTest(
		&updateServiceCacheStub{},
		&updateServiceGitHubClientStub{recentReleases: releases},
		current,
		"release",
	)
}

// TestUpdateServiceRollbackToVersionRejectsDisallowedTargets supplies current,
// newer, unknown, malformed, and out-of-window targets against recent stable fixtures.
// Every disallowed selection must return the version-rejection sentinel before installer download paths can run.
func TestUpdateServiceRollbackToVersionRejectsDisallowedTargets(t *testing.T) {
	releases := []*ops.GitHubRelease{
		{TagName: "v0.1.148"},
		{TagName: "v0.1.147"},
		{TagName: "v0.1.146"},
		{TagName: "v0.1.145"},
		{TagName: "v0.1.144"},
		{TagName: "v0.1.143"},
		{TagName: "v0.1.142"},
	}
	svc := newRollbackTestService("0.1.147", releases)

	for _, target := range []string{
		"",         // Empty version
		"0.1.147",  // Current version
		"v0.1.147", // Current version with prefix
		"0.1.148",  // Newer version
		"0.1.142",  // Outside the three most recent versions
		"9.9.9",    // Unknown version
		"0.1.146;$(touch /tmp/tokenrouter-pwned)", // Invalid shell characters
	} {
		err := svc.RollbackToVersion(context.Background(), target)
		require.ErrorIs(t, err, ErrRollbackVersionNotAllowed, "target %q should be rejected", target)
	}
}

// TestUpdateServiceRollbackToVersionAcceptsVPrefix requests a permitted earlier
// release using its v prefix and no compatible assets. The failure must come
// from platform asset selection, proving version eligibility was accepted before installation.
func TestUpdateServiceRollbackToVersionAcceptsVPrefix(t *testing.T) {
	// An allowed release with no asset for this platform must fail during asset selection.
	releases := []*ops.GitHubRelease{
		{TagName: "v0.1.147"},
		{TagName: "v0.1.146"},
	}
	svc := newRollbackTestService("0.1.147", releases)

	err := svc.RollbackToVersion(context.Background(), "v0.1.146")

	require.Error(t, err)
	require.NotErrorIs(t, err, ErrRollbackVersionNotAllowed)
	require.Contains(t, err.Error(), "no compatible release found")
}

// newMaintenanceUpdateForTest binds the actual release query and binary installer
// to supplied cache and GitHub fixtures. The caller selects running version and
// build kind so tests exercise their eligibility decisions before any forbidden download callback.
func newMaintenanceUpdateForTest(cache ops.UpdateCache, client *updateServiceGitHubClientStub, version, buildType string) *UpdateService {
	return NewUpdateService(ops.NewReleaseQuery(cache, client, version, buildType), opsadapter.NewBinaryInstaller(client, nil))
}
