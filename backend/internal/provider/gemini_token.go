package provider

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
)

// gemini_token.go resolves Gemini access tokens through caches, coordinated
// refresh, and optional project discovery.
// Gemini OAuth tokens use a project-based cache key when credentials contain a
// project, and a row-based key otherwise. Service-account records bypass OAuth
// handling and require Vertex exchange; OAuth cache hits return before stored
// expiry is inspected.
//
// An optional coordinator handles rotation and lock contention according to
// request policy. Project discovery runs only for the literal
// auto_detect_project_id value true and can return the token even when
// discovery fails or its callback is absent. A discovered project changes
// local credentials before best-effort persistence, while the cache key
// remains the one selected at lookup entry. Repository version observation
// then prevents publication of an already stale snapshot; callers synchronize
// their mutable record throughout this work.

// GeminiTokenRefreshSkew and GeminiTokenCacheSkew govern pre-expiry request
// refresh and the lifetime margin used for OAuth cache publication.
const (
	GeminiTokenRefreshSkew = 3 * time.Minute
	GeminiTokenCacheSkew   = 5 * time.Minute
)

// GeminiTokenOptions supplies OAuth lookup, project repair, and Vertex
// exchange dependencies.
type GeminiTokenOptions struct {
	// Debug and Warn optionally observe token lookup and lock failures. The
	// nil-safe wrappers keep credential selection callable when either callback
	// is missing.
	// Debug: token selection diagnostics; Warn: cache admission warnings
	Debug, Warn func(string, ...any)
	// Cache optionally supplies tokens and refresh admission. Nil skips cache
	// lookup and publication while configured refresh and stored credentials
	// remain available.
	Cache AccessTokenCache
	// Repository optionally checks completed rotation before cache publication.
	// Nil or failed reads let CheckTokenVersion retain this request observation.
	Repository RefreshRepository
	// Policy selects an error or stored token after refresh failure. Its lock
	// rule may perform one immediate peer cache reread before credential fallback.
	Policy ProviderRefreshPolicy
	// Refresh optionally invokes coordinated rotation. A nonnil successful
	// result either reports LockHeld or supplies the replacement record consumed
	// below.
	Refresh func(context.Context, *Record, time.Duration) (*OAuthRefreshResult, error)
	// Vertex is required for service-account input, which returns directly
	// before OAuth cache lookup and optional project discovery can run.
	Vertex func(context.Context, *Record) (string, error)
	// Project optionally discovers a missing opted-in project and tier. Its
	// absence returns the selected access token before version checking or cache
	// publication.
	Project func(context.Context, string, string) (string, string, error)
	// ResolveProxy optionally selects discovery transport for a configured
	// ProxyID.
	// Missing resolver or false availability leaves the discovery proxy URL empty.
	ResolveProxy func(context.Context, int64) (string, bool)
	// Persist is required after successful project discovery. Its error is
	// discarded, so local credentials remain changed even when durable storage
	// fails.
	Persist func(context.Context, *Record, map[string]any) error
	// Logf is required when project discovery fails. That diagnostic branch
	// returns the usable token and leaves the missing project unresolved.
	Logf func(string, ...any)
}

// GeminiTokenSource selects request credentials and optionally repairs missing
// OAuth projects.
type GeminiTokenSource struct {
	// Options copies callback values and borrows shared services. Configure it
	// before lookups and synchronize subsequent changes with credential consumers.
	Options GeminiTokenOptions
}

// GetAccessToken delegates Gemini credential selection using the captured
// options. A stable record and reached callback dependencies are required;
// GetGeminiAccessToken defines refresh failure, project repair, and cache
// ownership rules.
func (s *GeminiTokenSource) GetAccessToken(ctx context.Context, value *Record) (string, error) {
	return GetGeminiAccessToken(ctx, value, s.Options)
}

// GeminiOAuthTokenCacheKey selects the trimmed project namespace or the
// provider-row fallback. Pass a nonnil record; equal project IDs
// address the same cache key across different provider rows.
func GeminiOAuthTokenCacheKey(provider *Record) string {
	projectID := strings.TrimSpace(provider.GetCredential("project_id"))
	if projectID != "" {
		return "gemini:" + projectID
	}
	return "gemini:provider:" + strconv.FormatInt(provider.ID, 10)
}

// GetGeminiAccessToken returns Gemini OAuth or delegated service-account
// credentials. Invalid records fail; service accounts require Vertex, and
// refresh success requires a nonnil result with a usable replacement record
// when LockHeld is false.
//
// Cache hits precede expiry checks. Policy can tolerate refresh failure or
// reread a peer token on lock contention; this Gemini path adds no sleep.
// Opted-in project discovery may mutate local credentials and discard
// persistence errors, using the key captured before repair. Newer repository
// versions replace the selected token and suppress cache writes. Callers
// synchronize record mutation and provide Persist and Logf when discovery
// reaches those branches.
func GetGeminiAccessToken(ctx context.Context, provider *Record, options GeminiTokenOptions) (string, error) {
	if provider == nil {
		return "", errors.New("provider is nil")
	}
	if provider.Platform != PlatformGemini || (provider.Type != ProviderTypeOAuth && provider.Type != ProviderTypeServiceAccount) {
		return "", errors.New("not a gemini oauth or service account")
	}
	if provider.Type == ProviderTypeServiceAccount {
		return options.Vertex(ctx, provider)
	}

	cacheKey := GeminiOAuthTokenCacheKey(provider)

	// Blank entries and cache read errors select the record path. A populated
	// cache token wins before expiry or optional project discovery is inspected.
	if options.Cache != nil {
		if token, err := options.Cache.GetAccessToken(ctx, cacheKey); err == nil && strings.TrimSpace(token) != "" {
			return token, nil
		}
	}

	// Missing expiry and near expiry both request optional coordinator refresh.
	// Tolerated errors keep the record token, while strict policy stops this
	// lookup.
	expiresAt := provider.GetCredentialAsTime("expires_at")
	needsRefresh := expiresAt == nil || time.Until(*expiresAt) <= GeminiTokenRefreshSkew

	if needsRefresh && options.Refresh != nil {
		result, err := options.Refresh(ctx, provider, GeminiTokenRefreshSkew)
		if err != nil {
			if options.Policy.OnRefreshError == ProviderRefreshErrorReturn {
				return "", err
			}
		} else if result.LockHeld {
			if options.Policy.OnLockHeld == ProviderLockHeldWaitForCache && options.Cache != nil {
				if token, cacheErr := options.Cache.GetAccessToken(ctx, cacheKey); cacheErr == nil && strings.TrimSpace(token) != "" {
					return token, nil
				}
			}
			options.debug("gemini_token_lock_held_use_old", "provider_id", provider.ID)
		} else {
			provider = result.Provider
			expiresAt = provider.GetCredentialAsTime("expires_at")
		}
	} else if needsRefresh && options.Cache != nil {
		// Without an exchange callback, cache admission still encloses lookup and
		// publication. Granted locks are released on return; lock failures retain
		// credential fallback.
		locked, lockErr := options.Cache.AcquireRefreshLock(ctx, cacheKey, 30*time.Second)
		if lockErr == nil && locked {
			defer func() { _ = options.Cache.ReleaseRefreshLock(ctx, cacheKey) }()
		} else if lockErr != nil {
			options.warn("gemini_token_lock_failed", "provider_id", provider.ID, "error", lockErr)
		}
	}

	accessToken := provider.GetCredential("access_token")
	if strings.TrimSpace(accessToken) == "" {
		return "", errors.New("access_token not found in credentials")
	}

	// Only the literal true credential enables discovery. Failed or absent
	// discovery returns the selected token early, bypassing later version reads
	// and cache writes.
	projectID := strings.TrimSpace(provider.GetCredential("project_id"))
	autoDetectProjectID := provider.GetCredential("auto_detect_project_id") == "true"

	if projectID == "" && autoDetectProjectID {
		if options.Project == nil {
			return accessToken, nil
		}

		// Project lookup may use a resolved selected proxy. Missing resolver or
		// unavailable lookup leaves an empty URL for the discovery transport
		// callback.
		var proxyURL string
		if provider.ProxyID != nil && options.ResolveProxy != nil {
			if resolved, ok := options.ResolveProxy(ctx, *provider.ProxyID); ok {
				proxyURL = resolved
			}
		}
		detected, tierID, err := options.Project(ctx, accessToken, proxyURL)
		if err != nil {
			options.Logf("[GeminiTokenProvider] Auto-detect project_id failed: %v, fallback to AI Studio API mode", err)
			return accessToken, nil
		}
		detected = strings.TrimSpace(detected)
		tierID = strings.TrimSpace(tierID)
		if detected != "" {
			if provider.Credentials == nil {
				provider.Credentials = make(map[string]any)
			}
			provider.Credentials["project_id"] = detected
			if tierID != "" {
				provider.Credentials["tier_id"] = tierID
			}
			// Persist after the local project and tier assignment. A storage error
			// is discarded, retaining that repaired local record for this request.
			_ = options.Persist(ctx, provider, provider.Credentials)
		}
	}

	// A newer repository version supplies its token and skips cache publication
	// from this snapshot; the observation does not lock subsequent cache writes.
	if options.Cache != nil {
		latestProvider, isStale := CheckTokenVersion(ctx, provider, options.Repository, options.Debug)
		if isStale && latestProvider != nil {
			options.debug("gemini_token_version_stale_use_latest", "provider_id", provider.ID)
			accessToken = latestProvider.GetCredential("access_token")
			if strings.TrimSpace(accessToken) == "" {
				return "", errors.New("access_token not found after version check")
			}
		} else {
			// Unknown expiry selects the default lifetime. Known expiry subtracts the
			// cache margin where possible, then uses positive lifetime or a short
			// expired fallback.
			ttl := 30 * time.Minute
			if expiresAt != nil {
				until := time.Until(*expiresAt)
				switch {
				case until > GeminiTokenCacheSkew:
					ttl = until - GeminiTokenCacheSkew
				case until > 0:
					ttl = until
				default:
					ttl = time.Minute
				}
			}
			// Cache write errors leave the chosen token available
			// to upstream execution, even when the next request
			// will need another credential lookup.
			_ = options.Cache.SetAccessToken(ctx, cacheKey, accessToken, ttl)
		}
	}

	return accessToken, nil
}

// debug emits Gemini token diagnostics through the optional Debug callback.
// Nil logging leaves token selection available and contributes no additional
// error to the caller result.
func (o GeminiTokenOptions) debug(message string, args ...any) {
	if o.Debug != nil {
		o.Debug(message, args...)
	}
}

// warn emits Gemini cache-lock diagnostics through the optional Warn callback.
// A missing logger discards these observations while the stored-credential
// path continues under its existing request policy.
func (o GeminiTokenOptions) warn(message string, args ...any) {
	if o.Warn != nil {
		o.Warn(message, args...)
	}
}
