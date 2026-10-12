package provider

import (
	"context"
)

// CheckTokenVersion compares the provider token version with the current database record.
// Callers use the returned record when isStale is true, preventing an old request snapshot
// from repopulating the cache after an asynchronous refresh invalidates it.
//
// Results:
//   - latestProvider is the database record, or nil when the lookup cannot be performed or fails.
//   - isStale reports a newer database token version; false allows the caller to use its current record.
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
		// Lookup failure leaves the caller free to use its current token.
		return nil, false
	}

	latestVersion := latestProvider.GetCredentialAsInt64("_token_version")

	// A positive database version supersedes a snapshot with no version.
	// The caller must use the refreshed record before caching.
	if currentVersion == 0 && latestVersion > 0 {
		debug("token_version_stale_no_current_version",
			"provider_id", provider.ID,
			"latest_version", latestVersion)
		return latestProvider, true
	}

	// Unversioned records are treated as current.
	if currentVersion == 0 && latestVersion == 0 {
		return latestProvider, false
	}

	// A newer database version supersedes the caller's snapshot.
	if latestVersion > currentVersion {
		debug("token_version_stale",
			"provider_id", provider.ID,
			"current_version", currentVersion,
			"latest_version", latestVersion)
		return latestProvider, true
	}

	return latestProvider, false
}
