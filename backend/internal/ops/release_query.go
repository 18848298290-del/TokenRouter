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

const (
	updateCacheTTL        = 1200
	githubRepo            = "TokenFlux/TokenRouter"
	maxRollbackVersions   = 3
	rollbackFetchPageSize = 15
)

type ReleaseQueryClient interface {
	FetchLatestRelease(context.Context, string) (*GitHubRelease, error)
	FetchRecentReleases(context.Context, string, int) ([]*GitHubRelease, error)
}
type ReleaseQuery struct {
	cache                     UpdateCache
	githubClient              ReleaseQueryClient
	currentVersion, buildType string
}

type GitHubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

// RollbackVersion describes a stable release eligible for rollback.
type RollbackVersion struct {
	Version     string `json:"version"` // Version without the v prefix, such as 0.1.146.
	PublishedAt string `json:"published_at"`
	HTMLURL     string `json:"html_url"`
}

// GitHubRelease represents GitHub API response.
type GitHubRelease struct {
	TagName     string        `json:"tag_name"`
	Name        string        `json:"name"`
	Body        string        `json:"body"`
	PublishedAt string        `json:"published_at"`
	HTMLURL     string        `json:"html_url"`
	Draft       bool          `json:"draft"`
	Prerelease  bool          `json:"prerelease"`
	Assets      []GitHubAsset `json:"assets"`
}

// Asset represents a release asset.
type Asset struct {
	Name        string `json:"name"`
	DownloadURL string `json:"download_url"`
	Size        int64  `json:"size"`
}

// ReleaseInfo contains GitHub release details.
type ReleaseInfo struct {
	Name        string  `json:"name"`
	Body        string  `json:"body"`
	PublishedAt string  `json:"published_at"`
	HTMLURL     string  `json:"html_url"`
	Assets      []Asset `json:"assets,omitempty"`
}

// UpdateInfo contains update information.
type UpdateInfo struct {
	CurrentVersion string       `json:"current_version"`
	LatestVersion  string       `json:"latest_version"`
	HasUpdate      bool         `json:"has_update"`
	ReleaseInfo    *ReleaseInfo `json:"release_info,omitempty"`
	Cached         bool         `json:"cached"`
	Warning        string       `json:"warning,omitempty"`
	BuildType      string       `json:"build_type"` // "source" or "release"
}

// UpdateCache defines cache operations for update service.
type UpdateCache interface {
	GetUpdateInfo(ctx context.Context) (string, error)
	SetUpdateInfo(ctx context.Context, data string, ttl time.Duration) error
}

// @project-doc docs/operations/ops_monitoring_and_alerting.md#ops_release_and_maintenance
func NewReleaseQuery(cache UpdateCache, client ReleaseQueryClient, version, buildType string) *ReleaseQuery {
	return &ReleaseQuery{cache, client, version, buildType}
}

func parseVersion(v string) [3]int {
	v = strings.TrimPrefix(v, "v")
	if idx := strings.IndexByte(v, '-'); idx != -1 {
		v = v[:idx]
	}
	parts := strings.Split(v, ".")
	result := [3]int{0, 0, 0}
	for i := 0; i < len(parts) && i < 3; i++ {
		if parsed, err := strconv.Atoi(parts[i]); err == nil {
			result[i] = parsed
		}
	}
	return result
}

// compareVersions compares numeric major, minor, and patch components, ignoring prerelease suffixes.
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

func (s *ReleaseQuery) SaveToCache(ctx context.Context, info *UpdateInfo) {
	cacheData := struct {
		Latest      string       `json:"latest"`
		ReleaseInfo *ReleaseInfo `json:"release_info"`
		Timestamp   int64        `json:"timestamp"`
	}{
		Latest:      info.LatestVersion,
		ReleaseInfo: info.ReleaseInfo,
		Timestamp:   time.Now().Unix(),
	}

	data, _ := json.Marshal(cacheData)
	_ = s.cache.SetUpdateInfo(ctx, string(data), time.Duration(updateCacheTTL)*time.Second)
}

func (s *ReleaseQuery) GetFromCache(ctx context.Context) (*UpdateInfo, error) {
	data, err := s.cache.GetUpdateInfo(ctx)
	if err != nil {
		return nil, err
	}

	var cached struct {
		Latest      string       `json:"latest"`
		ReleaseInfo *ReleaseInfo `json:"release_info"`
		Timestamp   int64        `json:"timestamp"`
	}
	if err := json.Unmarshal([]byte(data), &cached); err != nil {
		return nil, err
	}

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

// normalizeRollbackVersion accepts v?MAJOR.MINOR.PATCH for downloads and displayed commands.
// The restricted format supports version ordering and excludes shell metacharacters from copied commands.
func normalizeRollbackVersion(raw string) (string, bool) {
	version := strings.TrimSpace(raw)
	version = strings.TrimPrefix(version, "v")
	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		return "", false
	}
	for _, part := range parts {
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

// NormalizeRollbackVersion exposes the same validation used for rollback candidates.
func NormalizeRollbackVersion(raw string) (string, bool) { return normalizeRollbackVersion(raw) }

// FetchRollbackCandidates fetches recent releases and retains stable versions older than the current version.
func (s *ReleaseQuery) FetchRollbackCandidates(ctx context.Context) ([]*GitHubRelease, error) {
	releases, err := s.githubClient.FetchRecentReleases(ctx, githubRepo, rollbackFetchPageSize)
	if err != nil {
		return nil, err
	}

	currentVersion, ok := normalizeRollbackVersion(s.currentVersion)
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
		// Only stable versions strictly older than the current version are eligible.
		if compareVersions(v, currentVersion) >= 0 {
			continue
		}
		seen[v] = true
		candidates = append(candidates, r)
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		left, _ := normalizeRollbackVersion(candidates[i].TagName)
		right, _ := normalizeRollbackVersion(candidates[j].TagName)
		return compareVersions(
			left,
			right,
		) > 0
	})

	if len(candidates) > maxRollbackVersions {
		candidates = candidates[:maxRollbackVersions]
	}
	return candidates, nil
}

// ListRollbackVersions returns recent eligible stable releases, newest first.
// Drafts, prereleases, and nonstandard version strings are excluded.
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

// CheckUpdate checks for available updates.
func (s *ReleaseQuery) CheckUpdate(ctx context.Context, force bool) (*UpdateInfo, error) {
	// Try cache first
	if !force {
		if cached, err := s.GetFromCache(ctx); err == nil && cached != nil {
			return cached, nil
		}
	}

	// Fetch from GitHub
	info, err := s.FetchLatestRelease(ctx)
	if err != nil {
		// Return cached on error
		if cached, cacheErr := s.GetFromCache(ctx); cacheErr == nil && cached != nil {
			cached.Warning = "Using cached data: " + err.Error()
			return cached, nil
		}
		return &UpdateInfo{
			CurrentVersion: s.currentVersion,
			LatestVersion:  s.currentVersion,
			HasUpdate:      false,
			Warning:        err.Error(),
			BuildType:      s.buildType,
		}, nil
	}

	// Cache result
	s.SaveToCache(ctx, info)
	return info, nil
}
