package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Update cache entries contain the remote version, release metadata, and write
// time, while each ReleaseQuery supplies its own running version and build
// label. Cache hits recompute numeric update eligibility so instances using the
// shared cache can report different update flags. Force bypasses the initial
// cache read, but a failed remote fetch can still select cached data with a
// warning.
//
// Update comparison tolerates missing or malformed numeric components as zero
// and drops hyphen suffixes. Rollback selection applies a stricter three-part
// numeric spelling before filtering drafts, prereleases, duplicates, and
// versions that are not older. Numeric ordering selects the newest three
// candidates from one fetched page. Cache expiry and JSON decoding do not
// validate metadata completeness, so an available version alone can accompany
// absent installer inputs.

// Release queries share one repository target, a cached-response lifetime in
// seconds, and fixed fetch/result limits for selecting rollback candidates.
const (
	updateCacheTTL        = 1200                    // cached release lifetime in seconds
	githubRepo            = "TokenFlux/TokenRouter" // queried GitHub owner/repository identity
	maxRollbackVersions   = 3                       // maximum displayed rollback candidate count
	rollbackFetchPageSize = 15                      // requested recent release page count
)

// ReleaseQueryClient supplies latest and recent GitHub release records for
// version selection.
type ReleaseQueryClient interface {
	// FetchLatestRelease reads the supplied owner/repository under context and
	// reports errors to ReleaseQuery. Implementations supply a nonnil release on
	// success because the query dereferences its tag and asset metadata.
	FetchLatestRelease(context.Context, string) (*GitHubRelease, error)
	// FetchRecentReleases reads one requested release page under context and
	// returns records or an error. ReleaseQuery filters nil records, flags,
	// version spellings, and duplicates itself; retained candidate pointers remain
	// shared with these records.
	FetchRecentReleases(context.Context, string, int) ([]*GitHubRelease, error)
}

// ReleaseQuery compares releases for its running version and selects bounded
// rollback candidates.
type ReleaseQuery struct {
	// cache is the borrowed serialized-release store used by SaveToCache and
	// GetFromCache. Callers provide it before cache operations; CheckUpdate treats
	// its errors as an unavailable cache.
	cache UpdateCache
	// githubClient is the supplied metadata reader retained by NewReleaseQuery.
	// Latest-release success supplies a nonnil record, while rollback filtering
	// permits nil entries in a recent list.
	githubClient ReleaseQueryClient
	// currentVersion and buildType are copied from construction and returned in
	// every update view. They remain supplied strings while each query computes
	// eligibility and cache-origin flags.
	//
	// currentVersion uses permissive comparison for updates, but invalid strict
	// rollback spelling yields no candidates after a successful fetch. buildType
	// is the supplied provenance label; selection does not validate it or use it
	// to disable release queries.
	// currentVersion: running version text. buildType: supplied provenance label.
	currentVersion, buildType string
}

// GitHubAsset carries one GitHub asset record before local download metadata
// conversion.
type GitHubAsset struct {
	Name               string `json:"name"`                 // GitHub release asset name
	BrowserDownloadURL string `json:"browser_download_url"` // GitHub browser asset URL
	Size               int64  `json:"size"`                 // reported asset byte size
}

// RollbackVersion contains display metadata for a candidate selected by
// rollback validation.
type RollbackVersion struct {
	// Version is supplied by ListRollbackVersions after strict validation removes
	// an optional lowercase v prefix. It identifies an older stable numeric
	// release eligible at that query time.
	Version     string `json:"version"`
	PublishedAt string `json:"published_at"` // GitHub publication timestamp text
	HTMLURL     string `json:"html_url"`     // release browser page URL
}

// GitHubRelease carries decoded GitHub release metadata and its reported flags.
type GitHubRelease struct {
	TagName     string `json:"tag_name"`     // GitHub release tag text
	Name        string `json:"name"`         // release display name text
	Body        string `json:"body"`         // release note body text
	PublishedAt string `json:"published_at"` // GitHub publication timestamp text
	HTMLURL     string `json:"html_url"`     // release browser page URL
	Draft       bool   `json:"draft"`        // reported draft release flag
	Prerelease  bool   `json:"prerelease"`   // reported prerelease eligibility flag
	// Assets is the supplied GitHub metadata slice. FetchRollbackCandidates
	// retains it through release pointers, so callers keep these records stable
	// while using shared candidate results.
	Assets []GitHubAsset `json:"assets"`
}

// Asset contains local JSON download metadata copied from a release asset.
type Asset struct {
	Name        string `json:"name"`         // release asset name
	DownloadURL string `json:"download_url"` // release asset download URL
	Size        int64  `json:"size"`         // reported asset byte size
}

// ReleaseInfo carries release description and local asset metadata for an
// update view.
type ReleaseInfo struct {
	Name        string `json:"name"`         // release display name text
	Body        string `json:"body"`         // release note body text
	PublishedAt string `json:"published_at"` // GitHub publication timestamp text
	HTMLURL     string `json:"html_url"`     // release browser page URL
	// Assets is copied into local-shaped records by FetchLatestRelease and
	// allocated again by cache decoding. Callers keep the list stable while
	// SaveToCache serializes its containing release.
	Assets []Asset `json:"assets,omitempty"`
}

// UpdateInfo combines running build identity, compared latest version,
// metadata, and query diagnostics.
type UpdateInfo struct {
	CurrentVersion string `json:"current_version"` // supplied running version text
	// LatestVersion uses cached or latest-API version text when available.
	// CheckUpdate returns CurrentVersion here after a remote failure with no
	// usable cache, making HasUpdate false.
	LatestVersion string `json:"latest_version"`
	// HasUpdate records permissive numeric ordering of CurrentVersion and
	// LatestVersion. It does not validate complete release metadata or a
	// platform-compatible asset before reporting an available version.
	HasUpdate bool `json:"has_update"`
	// ReleaseInfo contains fetched or cached metadata when present. Cache decoding
	// permits its absence, and CheckUpdate leaves it nil in a remote-failure view
	// without usable cache.
	ReleaseInfo *ReleaseInfo `json:"release_info,omitempty"`
	Cached      bool         `json:"cached"` // cache-derived update view flag
	// Warning reports a remote-fetch failure in CheckUpdate, optionally prefixed
	// when cached data is returned. SaveToCache does not store this diagnostic,
	// and a normal cache hit starts with it empty.
	Warning   string `json:"warning,omitempty"`
	BuildType string `json:"build_type"` // supplied build provenance label
}

// UpdateCache stores serialized latest-release data for update reads across
// running builds.
type UpdateCache interface {
	// GetUpdateInfo returns the stored serialized release response under context
	// or a cache error. ReleaseQuery decodes its JSON and checks timestamp age;
	// CheckUpdate uses a failed read as an unavailable cache.
	GetUpdateInfo(ctx context.Context) (string, error)
	// SetUpdateInfo replaces the stored serialized response using ttl as the cache
	// lifetime and reports any write error. ReleaseQuery supplies its
	// twenty-minute duration but suppresses this error because caching is best
	// effort.
	SetUpdateInfo(ctx context.Context, data string, ttl time.Duration) error
}

// NewReleaseQuery retains the cache and metadata client with supplied running
// version and build label, performing no fetch or normalization. Callers
// provide usable dependencies before querying; cache reads recompute
// eligibility for this retained version.
// @project-doc docs/operations/ops_monitoring_and_alerting.md#ops_release_and_maintenance
func NewReleaseQuery(cache UpdateCache, client ReleaseQueryClient, version, buildType string) *ReleaseQuery {
	return &ReleaseQuery{cache, client, version, buildType}
}

// parseVersion returns up to three numeric components for permissive update
// ordering after removing one lowercase v prefix and a hyphen suffix. Missing,
// malformed, or overflowing components remain zero; additional components are
// ignored and no validation error is returned.
func parseVersion(v string) [3]int {
	// Update ordering compares the numeric release base. Remove a lowercase v and
	// hyphen suffix here; strict rollback selection uses its separate validator
	// before numeric comparison.
	v = strings.TrimPrefix(v, "v")
	if idx := strings.IndexByte(v, '-'); idx != -1 {
		v = v[:idx]
	}
	parts := strings.Split(v, ".")
	result := [3]int{0, 0, 0}
	// Only the first three release components participate; omitted parts retain
	// zero.
	for i := 0; i < len(parts) && i < 3; i++ {
		// Malformed or overflowing components keep the initialized zero. Update
		// comparison therefore produces numeric ordering even when the original text
		// could not be fully parsed.
		if parsed, err := strconv.Atoi(parts[i]); err == nil {
			result[i] = parsed
		}
	}
	return result
}

// compareVersions returns minus one, zero, or one for numeric current/latest
// ordering in major/minor/patch sequence. Both inputs use permissive
// parseVersion, so discarded suffixes and zero-valued invalid components affect
// equality without rejecting either text.
func compareVersions(current, latest string) int {
	currentParts := parseVersion(current)
	latestParts := parseVersion(latest)

	for i := range 3 {
		if currentParts[i] < latestParts[i] {
			return -1
		}
		if currentParts[i] > latestParts[i] {
			return 1
		}
	}
	return 0
}

// SaveToCache encodes latest version, release metadata, and a Unix-second write
// time, then attempts a twenty-minute cache write. Supply nonnil info/cache and
// keep metadata stable during encoding. Running version, build label, update
// flag, and warning are recomputed or omitted on reading; encoding/write errors
// are suppressed.
func (s *ReleaseQuery) SaveToCache(ctx context.Context, info *UpdateInfo) {
	cacheData := struct {
		Latest string `json:"latest"` // cached latest version text
		// ReleaseInfo is the supplied metadata pointer saved in cache JSON. Encoding
		// reads its asset list during this call; callers leave that data stable until
		// encoding returns.
		ReleaseInfo *ReleaseInfo `json:"release_info"`
		Timestamp   int64        `json:"timestamp"` // Unix seconds
	}{
		Latest:      info.LatestVersion,
		ReleaseInfo: info.ReleaseInfo,
		Timestamp:   time.Now().Unix(),
	}

	// The fetched view is already available to its caller. Serialize only remote
	// release data and attempt caching, ignoring encoding/storage failure instead
	// of failing that view.
	data, _ := json.Marshal(cacheData)
	// Convert cache lifetime seconds to the backend duration.
	_ = s.cache.SetUpdateInfo(ctx, string(data), time.Duration(updateCacheTTL)*time.Second)
}

// GetFromCache decodes a stored release response and combines it with this
// query running version and build label. Cache and JSON errors are returned.
// Age is checked with direct signed Unix-second subtraction, expiring only a
// difference above 1,200; equality and future timestamps can pass, and
// timestamp origin/range is not validated.
//
// Latest text and release metadata are accepted after decoding without a
// completeness check. Missing or null ReleaseInfo can therefore accompany
// HasUpdate true when permissive comparison orders latest above current. The
// returned view marks Cached and recomputes its update flag using the retained
// running version; callers inspect metadata before treating a version
// comparison as installable assets.
func (s *ReleaseQuery) GetFromCache(ctx context.Context) (*UpdateInfo, error) {
	data, err := s.cache.GetUpdateInfo(ctx)
	if err != nil {
		return nil, err
	}

	var cached struct {
		Latest string `json:"latest"` // cached latest version text
		// ReleaseInfo is decoded metadata for this cache hit. Missing or null values
		// remain nil, while the update flag is computed independently from latest
		// version text.
		ReleaseInfo *ReleaseInfo `json:"release_info"`
		Timestamp   int64        `json:"timestamp"` // Unix seconds
	}
	if err := json.Unmarshal([]byte(data), &cached); err != nil {
		return nil, err
	}

	// The stored write time supplies a second age check beside backend expiry.
	// Direct signed subtraction accepts equality/future times and has no
	// timestamp-range validation before this comparison.
	if time.Now().Unix()-cached.Timestamp > updateCacheTTL {
		return nil, fmt.Errorf("cache expired")
	}

	return &UpdateInfo{
		CurrentVersion: s.currentVersion,
		LatestVersion:  cached.Latest,
		HasUpdate:      compareVersions(s.currentVersion, cached.Latest) < 0,
		ReleaseInfo:    cached.ReleaseInfo,
		Cached:         true,
		BuildType:      s.buildType,
	}, nil
}

// FetchLatestRelease reads the fixed GitHub repository under context and
// returns a fresh local update view on success. The client supplies a nonnil
// release. It removes one lowercase v prefix, copies all assets without
// platform filtering, and compares the remaining tag permissively against this
// running version.
func (s *ReleaseQuery) FetchLatestRelease(ctx context.Context) (*UpdateInfo, error) {
	release, err := s.githubClient.FetchLatestRelease(ctx, githubRepo)
	if err != nil {
		return nil, err
	}

	latestVersion := strings.TrimPrefix(release.TagName, "v")

	assets := make([]Asset, len(release.Assets))
	for i, a := range release.Assets {
		assets[i] = Asset{
			Name:        a.Name,
			DownloadURL: a.BrowserDownloadURL,
			Size:        a.Size,
		}
	}

	return &UpdateInfo{
		CurrentVersion: s.currentVersion,
		LatestVersion:  latestVersion,
		HasUpdate:      compareVersions(s.currentVersion, latestVersion) < 0,
		ReleaseInfo: &ReleaseInfo{
			Name:        release.Name,
			Body:        release.Body,
			PublishedAt: release.PublishedAt,
			HTMLURL:     release.HTMLURL,
			Assets:      assets,
		},
		Cached:    false,
		BuildType: s.buildType,
	}, nil
}

// normalizeRollbackVersion returns canonical three-component numeric text and
// true after trimming surrounding space and one lowercase v prefix. Empty
// parts, multi-digit leading zeros, nondigits, suffixes, metadata, or integer
// overflow return empty text and false. The restricted spelling supports
// candidate deduplication and copied download commands.
func normalizeRollbackVersion(raw string) (string, bool) {
	version := strings.TrimSpace(raw)
	version = strings.TrimPrefix(version, "v")
	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		return "", false
	}
	for _, part := range parts {
		// A single zero is valid, while multi-digit leading zeros are rejected.
		// Canonical text keeps candidate deduplication by version spelling aligned
		// with numeric ordering.
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return "", false
		}
		for _, ch := range part {
			if ch < '0' || ch > '9' {
				return "", false
			}
		}
		if _, err := strconv.Atoi(part); err != nil {
			return "", false
		}
	}
	return strings.Join(parts, "."), true
}

// NormalizeRollbackVersion exposes strict rollback spelling validation for
// query and maintenance callers. It accepts an optional lowercase v prefix
// around three canonical numeric parts, returning empty text and false for
// unusable targets.
func NormalizeRollbackVersion(raw string) (string, bool) { return normalizeRollbackVersion(raw) }

// FetchRollbackCandidates reads one recent fifteen-record page, then returns up
// to three stable releases numerically older than the running version. Fetch
// errors are returned before current-version validation; invalid strict current
// spelling yields an empty slice after a successful fetch.
//
// Nil records, drafts, prereleases, noncanonical tags, current/newer versions,
// and repeated normalized versions are discarded. The first retained duplicate
// supplies metadata. Remaining pointers are sorted newest first by numeric
// version, then capped; publication dates do not select their order. The
// returned slice is new but retained release records and their asset slices
// remain shared with the client result, so callers treat them as stable.
func (s *ReleaseQuery) FetchRollbackCandidates(ctx context.Context) ([]*GitHubRelease, error) {
	releases, err := s.githubClient.FetchRecentReleases(ctx, githubRepo, rollbackFetchPageSize)
	if err != nil {
		return nil, err
	}

	currentVersion, ok := normalizeRollbackVersion(s.currentVersion)
	// Recent metadata has already been fetched. An invalid running version
	// supplies no stable rollback ordering, so return an empty list after the
	// successful read.
	if !ok {
		return []*GitHubRelease{}, nil
	}

	seen := make(map[string]bool, len(releases))
	candidates := make([]*GitHubRelease, 0, maxRollbackVersions)
	for _, r := range releases {
		if r == nil || r.Draft || r.Prerelease {
			continue
		}
		v, valid := normalizeRollbackVersion(r.TagName)
		if !valid || seen[v] {
			continue
		}
		// The current and newer versions are not rollback targets. Mark a normalized
		// version seen only after its record passes every eligibility check.
		if compareVersions(v, currentVersion) >= 0 {
			continue
		}
		seen[v] = true
		candidates = append(candidates, r)
	}

	// Numeric ordering chooses the newest eligible older releases regardless of
	// response order or publication date. Validation above makes the repeated
	// normalization results usable for this comparator.
	sort.SliceStable(candidates, func(i, j int) bool {
		left, _ := normalizeRollbackVersion(candidates[i].TagName)
		right, _ := normalizeRollbackVersion(candidates[j].TagName)
		return compareVersions(
			left,
			right,
		) > 0
	})

	// Return the newest eligible candidates within the three-version display cap.
	if len(candidates) > maxRollbackVersions {
		candidates = candidates[:maxRollbackVersions]
	}
	return candidates, nil
}

// ListRollbackVersions returns new display values for validated candidates in
// newest numeric order and propagates fetch errors. It strips the accepted v
// prefix again while copying publication and browser-link text; it does not
// perform local backup restoration or download.
func (s *ReleaseQuery) ListRollbackVersions(ctx context.Context) ([]RollbackVersion, error) {
	releases, err := s.FetchRollbackCandidates(ctx)
	if err != nil {
		return nil, err
	}

	versions := make([]RollbackVersion, 0, len(releases))
	for _, r := range releases {
		version, _ := normalizeRollbackVersion(r.TagName)
		versions = append(versions, RollbackVersion{
			Version:     version,
			PublishedAt: r.PublishedAt,
			HTMLURL:     r.HTMLURL,
		})
	}
	return versions, nil
}

// CheckUpdate returns a nonnil update view and nil error for cache or
// remote-read failures, carrying remote diagnostics in Warning. Force skips the
// first cache read, then performs a latest-release fetch; it does not prevent a
// later cached response after that fetch fails.
//
// A valid initial cache hit is returned directly without warning or remote
// work. Remote failure tries the cache again and adds a warning; without usable
// cache, latest is set to current, HasUpdate is false, and ReleaseInfo remains
// nil. A successful remote fetch is cached best effort. Cached metadata
// completeness is not validated, so the update flag alone does not establish
// available installer inputs.
func (s *ReleaseQuery) CheckUpdate(ctx context.Context, force bool) (*UpdateInfo, error) {
	// A non-forced read can finish from cache before contacting GitHub. Cache
	// decoding, expiry, or backend errors are ignored here so the remote attempt
	// can proceed.
	if !force {
		if cached, err := s.GetFromCache(ctx); err == nil && cached != nil {
			return cached, nil
		}
	}

	info, err := s.FetchLatestRelease(ctx)
	if err != nil {
		// Remote failure reopens cache selection even for a forced refresh. A usable
		// cached view carries the remote diagnostic while keeping its cached version
		// and metadata.
		if cached, cacheErr := s.GetFromCache(ctx); cacheErr == nil && cached != nil {
			cached.Warning = "Using cached data: " + err.Error()
			return cached, nil
		}
		// No usable cache leaves the running version as latest and an absent metadata
		// pointer. Return that diagnostic view successfully, with the fetch error
		// carried in Warning.
		return &UpdateInfo{
			CurrentVersion: s.currentVersion,
			LatestVersion:  s.currentVersion,
			HasUpdate:      false,
			Warning:        err.Error(),
			BuildType:      s.buildType,
		}, nil
	}

	// Keep the successful fresh view available even when cache storage fails. The
	// saved subset allows other running builds to recompute their own update flags
	// later.
	s.SaveToCache(ctx, info)
	return info, nil
}
