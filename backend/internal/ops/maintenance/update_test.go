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

type updateServiceCacheStub struct {
	data string
}

type updateServiceGitHubClientStub struct {
	release        *ops.GitHubRelease
	recentReleases []*ops.GitHubRelease
	recentErr      error
}

func (s *updateServiceCacheStub) GetUpdateInfo(context.Context) (string, error) {
	if s.data == "" {
		return "", errors.New("cache miss")
	}
	return s.data, nil
}

func (s *updateServiceCacheStub) SetUpdateInfo(_ context.Context, data string, _ time.Duration) error {
	s.data = data
	return nil
}

func (s *updateServiceGitHubClientStub) FetchLatestRelease(context.Context, string) (*ops.GitHubRelease, error) {
	return s.release, nil
}

func (s *updateServiceGitHubClientStub) FetchRecentReleases(context.Context, string, int) ([]*ops.GitHubRelease, error) {
	return s.recentReleases, s.recentErr
}

func (s *updateServiceGitHubClientStub) DownloadFile(context.Context, string, string, int64) error {
	panic("DownloadFile should not be called when no update is available")
}

func (s *updateServiceGitHubClientStub) FetchChecksumFile(context.Context, string) ([]byte, error) {
	panic("FetchChecksumFile should not be called when no update is available")
}

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

func newRollbackTestService(current string, releases []*ops.GitHubRelease) *UpdateService {
	return newMaintenanceUpdateForTest(
		&updateServiceCacheStub{},
		&updateServiceGitHubClientStub{recentReleases: releases},
		current,
		"release",
	)
}

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

// newMaintenanceUpdateForTest constructs release queries and an installer with test cache and GitHub clients.
func newMaintenanceUpdateForTest(cache ops.UpdateCache, client *updateServiceGitHubClientStub, version, buildType string) *UpdateService {
	return NewUpdateService(ops.NewReleaseQuery(cache, client, version, buildType), opsadapter.NewBinaryInstaller(client, nil))
}
