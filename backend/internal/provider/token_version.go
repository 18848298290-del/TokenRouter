package provider

import (
	"context"
)

// token_version.go compares captured and stored token versions to detect
// provider credentials superseded by refresh.
// An OAuth request can retain a provider snapshot after another refresh has
// stored a newer access token. Cache invalidation removes an existing token
// entry, but a request still holding the older record could subsequently fill
// that entry again. Comparing _token_version with a fresh repository
// observation lets token sources detect that already recorded rotation before
// deciding which access token to return.
//
// The comparison permits current credentials when storage cannot be read,
// keeping a cache miss usable during repository failures. Versions are ordered
// integers through GetCredentialAsInt64: missing or unparseable values become
// zero, while supported float64 values convert directly to int64. The returned
// record remains the repository's object. This observation and a caller's later
// cache write are separate operations, so an intervening rotation remains possible.

// CheckTokenVersion returns the repository observation and reports whether its
// token version exceeds the supplied snapshot's version. A nil provider or
// repository, a read error, or a nil row returns nil and false, permitting
// callers to continue with their current credentials.
//
// Missing, nil, unsupported and unparseable _token_version values read as
// zero. Supported float64 values convert directly to int64, so callers keep
// that representation integer-valued, finite and within range for meaningful
// version ordering. Equal versions and an older database version return false;
// two unversioned records also remain usable. Only the first optional observer
// is called for stale observations.
// Callers borrow the returned record and decide whether to return its token or
// populate a cache; this read does not coordinate a subsequent cache write.
func CheckTokenVersion(ctx context.Context, provider *Record, repo RefreshRepository, observers ...func(string, ...any)) (latestProvider *Record, isStale bool) {
	debug := func(string, ...any) {}
	if len(observers) > 0 && observers[0] != nil {
		debug = observers[0]
	}

	if provider == nil || repo == nil {
		return nil, false
	}

	currentVersion := provider.GetCredentialAsInt64("_token_version")

	latestProvider, err := repo.GetByID(ctx, provider.ID)
	if err != nil || latestProvider == nil {
		// A failed or missing repository observation supplies no replacement token;
		// token sources continue from their request snapshot to keep this read path available.
		return nil, false
	}

	latestVersion := latestProvider.GetCredentialAsInt64("_token_version")

	// A recorded version distinguishes a completed refresh from an unversioned
	// request snapshot, so token sources choose the returned row for that observation.
	if currentVersion == 0 && latestVersion > 0 {
		debug("token_version_stale_no_current_version",
			"provider_id", provider.ID,
			"latest_version", latestVersion)
		return latestProvider, true
	}

	// Two unversioned observations provide no ordering evidence, so the caller
	// keeps its supplied token even when storage returned a different record.
	if currentVersion == 0 && latestVersion == 0 {
		return latestProvider, false
	}

	// A newer stored version identifies credentials rotated after this snapshot;
	// callers use that row while leaving cache publication to their own path.
	if latestVersion > currentVersion {
		debug("token_version_stale",
			"provider_id", provider.ID,
			"current_version", currentVersion,
			"latest_version", latestVersion)
		return latestProvider, true
	}

	return latestProvider, false
}
