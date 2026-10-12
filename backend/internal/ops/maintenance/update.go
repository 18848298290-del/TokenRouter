package maintenance

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/ops"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// Installing a selected version depends on available update or rollback candidates.
// The service delegates release selection to the shared query object, then passes
// only the selected asset metadata to its installer. Local previous-binary rollback
// is a separate installer operation that performs no release-candidate lookup.

// Update errors distinguish an already-current installation from a requested version
// outside the allowed rollback candidate set before any installer work is started.
var (
	ErrNoUpdateAvailable         = infraerrors.Conflict("ALREADY_UP_TO_DATE", "no update available; current version is latest")
	ErrRollbackVersionNotAllowed = infraerrors.BadRequest("ROLLBACK_VERSION_NOT_ALLOWED", "version is not in the allowed rollback list")
)

type (
	// Asset carries the release download metadata consumed by the binary installer.
	Asset = ops.Asset
	// GitHubRelease carries candidate tags and assets returned by release query selection.
	GitHubRelease = ops.GitHubRelease
)

// ReleaseQueries provides current update information and eligible rollback release selection.
type ReleaseQueries interface {
	// CheckUpdate returns a nonnil update view on success, including ReleaseInfo
	// when HasUpdate is true. Force controls initial cache use; the shared query
	// implementation can still return cached information after a remote fetch failure.
	CheckUpdate(context.Context, bool) (*ops.UpdateInfo, error)
	// ListRollbackVersions returns display metadata for the allowed rollback choices.
	// Selection belongs to the query implementation, keeping the UI's choices and
	// RollbackToVersion's candidate lookup aligned with that same release policy.
	ListRollbackVersions(context.Context) ([]ops.RollbackVersion, error)
	// FetchRollbackCandidates returns eligible nonnil release records for rollback lookup.
	// The shared query filters stable versions older than the running version and
	// bounds its newest-first result to the allowed recent candidate count.
	FetchRollbackCandidates(context.Context) ([]*ops.GitHubRelease, error)
}

// Installer applies selected binary assets or restores the locally retained previous executable.
type Installer interface {
	// Apply installs an appropriate binary from the supplied release assets under ctx.
	// The technical installer owns download, optional checksum verification, extraction,
	// replacement, and compensation; errors return before maintenance reports completion.
	//
	// The binary adapter checks cancellation before replacing the executable, then
	// completes replacement or attempts restoration of its backup on failure. Success
	// means the selected binary is in place, while an error can include a failed
	// compensation outcome that the caller must inspect before requesting restart.
	Apply(context.Context, []ops.Asset) error
	// Rollback restores the installer-held previous executable and returns any failure.
	// It has no context parameter, so maintenance checks cancellation before invoking
	// it and leaves an already started local replacement to the installer.
	Rollback() error
}

// UpdateService checks release eligibility and delegates installation to the injected installer.
type UpdateService struct {
	// ReleaseQueries is the supplied shared query instance. Its embedded methods
	// provide the same candidate and update reads used by installation decisions here.
	ReleaseQueries
	installer Installer // selected binary installation executor
}

// NewUpdateService retains the supplied nonnil release-query and installer
// dependencies without starting maintenance work. Release queries remain embedded,
// so callers also use that instance for update and rollback display information.
func NewUpdateService(query ReleaseQueries, installer Installer) *UpdateService {
	return &UpdateService{query, installer}
}

// Rollback delegates previous-executable restoration to the retained installer.
// It does not select a release version or accept cancellation itself; callers
// use the maintenance operation wrapper for admission and its pre-call cancellation check.
func (s *UpdateService) Rollback() error { return s.installer.Rollback() }

// PerformUpdate requests forced update information and installs its selected
// release assets when HasUpdate is true. A current installation returns
// ErrNoUpdateAvailable before installer work; successful query results supply the
// nonnil view and release metadata required by this call.
func (s *UpdateService) PerformUpdate(ctx context.Context) error {
	info, err := s.CheckUpdate(ctx, true)
	if err != nil {
		return err
	}

	if !info.HasUpdate {
		return ErrNoUpdateAvailable
	}

	return s.installer.Apply(ctx, info.ReleaseInfo.Assets)
}

// RollbackToVersion installs assets from a requested normalized version only when
// that version appears in the query's current rollback candidates. Invalid or
// unmatched targets return ErrRollbackVersionNotAllowed before installation; supported
// prefixes and version validation come from ops.NormalizeRollbackVersion.
func (s *UpdateService) RollbackToVersion(ctx context.Context, version string) error {
	target, ok := normalizeRollbackVersion(version)
	if !ok {
		return ErrRollbackVersionNotAllowed
	}

	releases, err := s.fetchRollbackCandidates(ctx)
	if err != nil {
		return err
	}

	var match *GitHubRelease
	for _, r := range releases {
		// Candidate tags use the same normalizer as the requested target. An unusable
		// tag produces an empty candidate and cannot match the already validated nonempty target.
		candidateVersion, _ := normalizeRollbackVersion(r.TagName)
		if candidateVersion == target {
			match = r
			break
		}
	}
	if match == nil {
		return ErrRollbackVersionNotAllowed
	}

	assets := make([]Asset, len(match.Assets))
	for i, a := range match.Assets {
		assets[i] = Asset{
			Name:        a.Name,
			DownloadURL: a.BrowserDownloadURL,
			Size:        a.Size,
		}
	}

	return s.installer.Apply(ctx, assets)
}

// fetchRollbackCandidates returns the retained query instance's allowed release records.
func (s *UpdateService) fetchRollbackCandidates(ctx context.Context) ([]*GitHubRelease, error) {
	return s.FetchRollbackCandidates(ctx)
}

// normalizeRollbackVersion delegates rollback spelling and validation to the Ops query rule.
// Callers use its boolean result to reject malformed requested versions, and
// compare accepted values with candidates normalized by that same function.
func normalizeRollbackVersion(raw string) (string, bool) { return ops.NormalizeRollbackVersion(raw) }
