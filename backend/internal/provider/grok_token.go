package provider

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// grok_token.go acquires Grok request tokens while checking refresh results
// against selected provider and proxy identity.
// A Grok request carries a selected provider and proxy identity while token
// refresh and cache state can advance independently. Request token reads first
// require schedulable OAuth identity and complete selected token inputs. Cached
// text is accepted when its trimmed value equals the trimmed selected access
// token and that record remains outside the refresh window.
//
// Refresh results and newer repository versions are checked against the
// selected proxy before they replace the token. Lock waiting requires a changed
// valid database token or increased token version; cache text alone cannot
// establish that rotation. Policy can retain selected credentials after refresh
// errors, with an observed expired timestamp rejected. Manual tests use a
// separate path that bypasses scheduling gates and can retain an initially
// valid token on exchange error or lock contention.

// Grok request budgets bound exchange and shared-lock waiting, while cache
// skew ends cache reuse before the selected credential expires.
const (
	GrokTokenCacheSkew          = 5 * time.Minute
	GrokRequestRefreshTimeout   = 8 * time.Second
	GrokRefreshLockWaitTimeout  = 2 * time.Second
	GrokRefreshLockPollInterval = 25 * time.Millisecond
)

// Grok token errors distinguish missing renewal inputs, expired access,
// unavailable refresh execution, and a missing selected proxy for recovery.
var (
	ErrGrokOAuthRefreshNotConfigured = errors.New("grok oauth refresh is not configured")
	ErrGrokOAuthRefreshTokenMissing  = errors.New("grok oauth refresh token is missing")
	ErrGrokOAuthAccessTokenMissing   = errors.New("grok oauth access token is missing")
	ErrGrokOAuthAccessTokenExpired   = errors.New("grok oauth access token is expired")
	ErrGrokOAuthConfiguredProxyMiss  = errors.New("grok oauth configured proxy is missing")
)

// GrokTokenSource selects request credentials across cache, refresh, and
// repository observations.
type GrokTokenSource struct {
	// Cache is auxiliary request storage. Nil skips cache/version
	// selection,
	// and cache failures leave selected record handling available for the
	// caller's token decision.
	Cache AccessTokenCache // optional shared access-token cache
	// Repository supplies token-version comparisons and authoritative
	// lock-wait
	// reads. Missing or failed version reads permit current credentials;
	// the
	// bounded wait needs a successful changed row to return a token.
	Repository RefreshRepository // token-version and lock-wait reader
	// Policy chooses whether refresh errors abort and whether lock
	// contention
	// waits for changed storage. The remaining access-token and
	// observed-expiry
	// checks still apply after continuing.
	Policy ProviderRefreshPolicy // refresh error and contention choices
	// Refresh is required when the request path selects renewal, but manual
	// tests
	// may retain an initially valid token when it is absent. It returns
	// coordinated lock or provider observations.
	// coordinated credential exchange callback
	Refresh func(context.Context, *Record, time.Duration) (*OAuthRefreshResult, error)
}

// GrokStoredAccessToken reads the credential selected by a nonnil record's
// type. OAuth/setup-token and API Key require their token key; Bedrock returns
// an empty success, while service accounts and unsupported types return errors.
func GrokStoredAccessToken(value *Record) (string, error) {
	switch value.Type {
	case capability.ProviderTypeOAuth:
		token := value.GetGrokAccessToken()
		if token == "" {
			return "", errors.New("grok access_token not found in credentials")
		}
		return token, nil
	case capability.ProviderTypeSetupToken:
		token := value.GetCredential("access_token")
		if token == "" {
			return "", errors.New("access_token not found in credentials")
		}
		return token, nil
	case capability.ProviderTypeAPIKey:
		token := value.GetCredential("api_key")
		if token == "" {
			return "", errors.New("api_key not found in credentials")
		}
		return token, nil
	case capability.ProviderTypeBedrock:
		return "", nil
	case capability.ProviderTypeServiceAccount:
		return "", fmt.Errorf("unsupported service account platform: %s", value.Platform)
	default:
		return "", fmt.Errorf("unsupported provider type: %s", value.Type)
	}
}

// GetAccessToken returns request credentials for a schedulable Grok OAuth
// provider with its selected proxy identity. A missing selected access or
// refresh token fails even before cache reuse; a cached token needs matching
// trimmed text and expiry outside the refresh window. The selected-record
// return path uses credential-reader text; cache hits and lock-wait rotations
// return trimmed text.
//
// Renewal uses the request timeout and configured error/contention policy.
// Continuing after a refresh error can retain unknown expiry, while an observed
// expired timestamp is rejected. Returned refreshed or newer-version records
// are checked against the selected proxy and scheduling eligibility.
// Version-read failures permit the current snapshot, and cache writes are best
// effort; the repository read and later publication are separate observations
// rather than one atomic rotation.
// @project-doc docs/interfaces/grok_upstream.md#grok_account_contract
func (p *GrokTokenSource) GetAccessToken(ctx context.Context, provider *Record) (string, error) {
	if provider == nil {
		return "", errors.New("provider is nil")
	}
	if provider.Platform != PlatformGrok || provider.Type != ProviderTypeOAuth {
		return "", errors.New("not a grok oauth provider")
	}
	selectedProxyID := CloneGrokProxyID(provider.ProxyID)
	if eligibilityErr := GrokOAuthRequestProviderEligibilityError(provider); eligibilityErr != nil {
		return "", WithGrokCredentialFailureSnapshot(eligibilityErr, provider)
	}

	expiresAt := provider.GetCredentialAsTime("expires_at")
	providerAccessToken := strings.TrimSpace(provider.GetGrokAccessToken())
	if providerAccessToken == "" {
		return "", WithGrokCredentialFailureSnapshot(ErrGrokOAuthAccessTokenMissing, provider)
	}
	if strings.TrimSpace(provider.GetGrokRefreshToken()) == "" {
		return "", WithGrokCredentialFailureSnapshot(ErrGrokOAuthRefreshTokenMissing, provider)
	}
	cacheKey := GrokTokenCacheKey(provider)
	// Cache failures leave the selected stored credential path available.
	// Matching text and expiry prevent an unrelated cached observation from
	// replacing the provider chosen for this request.
	if p.Cache != nil {
		if token, err := p.Cache.GetAccessToken(ctx, cacheKey); err == nil {
			cachedToken := strings.TrimSpace(token)
			if cachedToken != "" && providerAccessToken != "" && cachedToken == providerAccessToken &&
				expiresAt != nil && time.Until(*expiresAt) > GrokTokenRefreshSkew {
				return cachedToken, nil
			}
		}
	}

	needsRefresh := expiresAt == nil || time.Until(*expiresAt) <= GrokTokenRefreshSkew
	if needsRefresh {
		if p.Refresh == nil {
			return "", ErrGrokOAuthRefreshNotConfigured
		}
		refreshCtx, cancel := context.WithTimeout(ctx, GrokRequestRefreshTimeout)
		defer cancel()
		result, err := p.Refresh(WithRefreshRequestPath(refreshCtx), provider, GrokTokenRefreshSkew)
		// Policy can keep the selected token after exchange failure.
		// The later
		// access and observed-expiry checks still determine whether it
		// can be
		// returned to this request.
		if err != nil {
			if p.Policy.OnRefreshError == ProviderRefreshErrorReturn {
				return "", err
			}
		} else if result != nil && result.LockHeld {
			// Contention follows policy: wait for an authoritative
			// rotation or keep
			// the selected token only while its observed expiry
			// remains valid.
			if p.Policy.OnLockHeld == ProviderLockHeldWaitForCache {
				token, waitErr := p.WaitForRefreshedToken(refreshCtx, provider, cacheKey)
				return token, WithGrokCredentialFailureSnapshot(waitErr, provider)
			}
			if expiresAt == nil || !time.Now().Before(*expiresAt) {
				return "", WithGrokCredentialFailureSnapshot(ErrGrokOAuthAccessTokenExpired, provider)
			}
		} else if result != nil && result.Provider != nil {
			// A refreshed row can carry another transport choice;
			// validate it against
			// the selected proxy before replacing this attempt's
			// credential source.
			if eligibilityErr := GrokOAuthRequestProviderEligibilityError(result.Provider); eligibilityErr != nil {
				return "", WithGrokCredentialFailureSnapshot(eligibilityErr, result.Provider)
			}
			if !GrokCredentialProxyIDsEqual(result.Provider.ProxyID, selectedProxyID) {
				return "", WithGrokCredentialFailureSnapshot(ErrRefreshProviderStateChanged, result.Provider)
			}
			provider = result.Provider
			expiresAt = provider.GetCredentialAsTime("expires_at")
		}
	}

	accessToken := provider.GetGrokAccessToken()
	if strings.TrimSpace(accessToken) == "" {
		return "", WithGrokCredentialFailureSnapshot(ErrGrokOAuthAccessTokenMissing, provider)
	}
	if expiresAt != nil && !time.Now().Before(*expiresAt) {
		return "", WithGrokCredentialFailureSnapshot(ErrGrokOAuthAccessTokenExpired, provider)
	}

	if p.Cache != nil {
		// Observe committed version advances before auxiliary cache
		// publication;
		// an unreadable version leaves the current snapshot available,
		// while a
		// newer row must pass this request's proxy and expiry checks.
		latestProvider, isStale := CheckTokenVersion(ctx, provider, p.Repository)
		if isStale && latestProvider != nil {
			if eligibilityErr := GrokOAuthRequestProviderEligibilityError(latestProvider); eligibilityErr != nil {
				return "", WithGrokCredentialFailureSnapshot(eligibilityErr, latestProvider)
			}
			if !GrokCredentialProxyIDsEqual(latestProvider.ProxyID, selectedProxyID) {
				return "", WithGrokCredentialFailureSnapshot(ErrRefreshProviderStateChanged, latestProvider)
			}
			accessToken = latestProvider.GetGrokAccessToken()
			if strings.TrimSpace(accessToken) == "" {
				return "", WithGrokCredentialFailureSnapshot(ErrGrokOAuthAccessTokenMissing, latestProvider)
			}
			latestExpiry := latestProvider.GetCredentialAsTime("expires_at")
			if latestExpiry == nil || !time.Now().Before(*latestExpiry) {
				return "", WithGrokCredentialFailureSnapshot(ErrGrokOAuthAccessTokenExpired, latestProvider)
			}
		} else {
			// Cache lifetime ends before observed expiry when the
			// skew fits; shorter
			// valid lifetimes use their remaining duration instead.
			ttl := 30 * time.Minute
			if expiresAt != nil {
				until := time.Until(*expiresAt)
				switch {
				case until > GrokTokenCacheSkew:
					ttl = until - GrokTokenCacheSkew
				case until > 0:
					ttl = until
				default:
					ttl = time.Minute
				}
			}
			// A cache write failure leaves the selected access
			// token returnable;
			// this auxiliary publication follows a separate version
			// observation and
			// can still race with later rotation.
			_ = p.Cache.SetAccessToken(ctx, cacheKey, accessToken, ttl)
		}
	}

	return accessToken, nil
}

// GetAccessTokenForManualTest obtains Grok OAuth credentials for an
// administrator probe while bypassing request scheduling gates. The initial
// record still needs its configured proxy loaded and a renewable token;
// sufficiently distant valid access returns directly.
//
// Missing exchange dependency, refresh failure, or lock contention can retain
// an initially valid token. Otherwise the refreshed record supplies access text
// and any observed expiry is checked before return. This path does not use
// request cache/version selection or apply its selected-proxy comparison after
// exchange; the coordinator and probe caller own their separate identity
// obligations.
func (p *GrokTokenSource) GetAccessTokenForManualTest(ctx context.Context, provider *Record) (string, error) {
	if provider == nil {
		return "", errors.New("provider is nil")
	}
	if provider.Platform != PlatformGrok || provider.Type != ProviderTypeOAuth {
		return "", errors.New("not a grok oauth provider")
	}
	if provider.ProxyID != nil && provider.Proxy == nil {
		return "", ErrGrokOAuthConfiguredProxyMiss
	}
	if strings.TrimSpace(provider.GetGrokRefreshToken()) == "" {
		return "", ErrGrokOAuthRefreshTokenMissing
	}

	accessToken := strings.TrimSpace(provider.GetGrokAccessToken())
	expiresAt := provider.GetCredentialAsTime("expires_at")
	tokenValid := accessToken != "" && expiresAt != nil && time.Now().Before(*expiresAt)
	if accessToken != "" && expiresAt != nil && time.Until(*expiresAt) > GrokTokenRefreshSkew {
		return accessToken, nil
	}

	// An administrator can still probe valid selected credentials when
	// renewal
	// is unavailable; use that initial validity observation before
	// reporting
	// missing exchange configuration.
	if p.Refresh == nil {
		if tokenValid {
			return accessToken, nil
		}
		return "", ErrGrokOAuthRefreshNotConfigured
	}

	// Keep this refresh outside request-path classification so
	// RefreshIfNeeded does not
	// reapply the scheduling gates that manual tests are intended to
	// bypass.
	refreshCtx, cancel := context.WithTimeout(ctx, GrokRequestRefreshTimeout)
	defer cancel()
	result, err := p.Refresh(refreshCtx, provider, GrokTokenRefreshSkew)
	if err != nil {
		// Use the pre-exchange validity decision when renewal fails.
		// This branch
		// returns its captured token before the final expiry check used
		// after a
		// successful exchange, even if that token expires during the
		// wait.
		if tokenValid {
			return accessToken, nil
		}
		return "", err
	}
	// An independent owner already holds renewal. The manual probe can keep
	// its initially valid token, while an unusable one reports contention
	// for
	// a later retry.
	if result != nil && result.LockHeld {
		if tokenValid {
			return accessToken, nil
		}
		return "", errors.New("token refresh is already in progress on another worker; retry in a few seconds")
	}
	if result != nil && result.Provider != nil {
		provider = result.Provider
	}

	accessToken = strings.TrimSpace(provider.GetGrokAccessToken())
	if accessToken == "" {
		return "", ErrGrokOAuthAccessTokenMissing
	}
	if latestExpiry := provider.GetCredentialAsTime("expires_at"); latestExpiry != nil && !time.Now().Before(*latestExpiry) {
		return "", ErrGrokOAuthAccessTokenExpired
	}
	return accessToken, nil
}

// WaitForRefreshedToken polls with a two-second context and returns a changed,
// valid, eligible database token for the selected proxy. Change means different
// token text or an increased positive token version, so a version advance can
// qualify the same text.
//
// Cache observations support best-effort correction after that authoritative
// success; they cannot provide success by themselves. The context bound relies
// on cache and repository callbacks honoring that context. When polling reaches
// its wait select, caller cancellation returns its error. Callers supply a
// nonnil record and context.
//
// Timeout without a successful nonnil row read returns
// ErrRefreshProviderRereadFailed with the last read or deadline diagnostic.
// Timeout after any successful nonnil row read returns
// ErrRefreshProviderStateChanged, even when that row never supplied usable
// replacement credentials. That classification tells recovery to leave a
// possibly unfinished rotation available for later requests.
func (p *GrokTokenSource) WaitForRefreshedToken(ctx context.Context, provider *Record, cacheKey string) (string, error) {
	waitCtx, cancel := context.WithTimeout(ctx, GrokRefreshLockWaitTimeout)
	defer cancel()

	initialToken := strings.TrimSpace(provider.GetGrokAccessToken())
	initialVersion := provider.GetCredentialAsInt64("_token_version")
	selectedProxyID := CloneGrokProxyID(provider.ProxyID)
	sawAuthoritativeState := false
	var lastProviderReadErr error
	ticker := time.NewTicker(GrokRefreshLockPollInterval)
	defer ticker.Stop()

	for {
		cachedToken := ""
		if p.Cache != nil {
			// Cache read failure leaves database polling active
			// because shared cache
			// text cannot establish that the lock owner committed
			// changed identity.
			if token, err := p.Cache.GetAccessToken(waitCtx, cacheKey); err == nil {
				cachedToken = strings.TrimSpace(token)
			}
		}

		if p.Repository != nil {
			latest, err := p.Repository.GetByID(waitCtx, provider.ID)
			// Retain read failure as timeout evidence while
			// polling; a later
			// successful changed row can still establish the
			// completed renewal.
			if err != nil {
				lastProviderReadErr = err
			} else if latest == nil {
				return "", ErrRefreshProviderStateChanged
			} else {
				sawAuthoritativeState = true
				if eligibilityErr := GrokOAuthRequestProviderEligibilityError(latest); eligibilityErr != nil {
					return "", WithGrokCredentialFailureSnapshot(eligibilityErr, latest)
				}
				if !GrokCredentialProxyIDsEqual(latest.ProxyID, selectedProxyID) {
					return "", WithGrokCredentialFailureSnapshot(ErrRefreshProviderStateChanged, latest)
				}
				token := strings.TrimSpace(latest.GetGrokAccessToken())
				version := latest.GetCredentialAsInt64("_token_version")
				expiresAt := latest.GetCredentialAsTime("expires_at")
				changed := token != initialToken || (version > 0 && version > initialVersion)
				valid := expiresAt != nil && time.Now().Before(*expiresAt)
				if token != "" && changed && valid {
					// Changed valid database credentials
					// establish success. Correct a
					// conflicting cached token as auxiliary
					// work; its write failure
					// cannot undo that authoritative
					// observation.
					if cachedToken != "" && cachedToken != token {
						ttl := time.Until(*expiresAt)
						if ttl > GrokTokenCacheSkew {
							ttl -= GrokTokenCacheSkew
						}
						_ = p.Cache.SetAccessToken(waitCtx, cacheKey, token, ttl)
					}
					return token, nil
				}
			}
		}

		// Read before sleeping so a completed rotation is visible
		// immediately;
		// the ticker spaces later attempts after an unchanged or failed
		// observation.
		select {
		case <-waitCtx.Done():
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			if !sawAuthoritativeState {
				if lastProviderReadErr == nil {
					lastProviderReadErr = waitCtx.Err()
				}
				return "", fmt.Errorf("%w: %v", ErrRefreshProviderRereadFailed, lastProviderReadErr)
			}
			// The refresh owner may commit just after this bounded
			// wait ends.
			// Report changed state so callers avoid isolating
			// credentials still being refreshed.
			return "", ErrRefreshProviderStateChanged
		case <-ticker.C:
		}
	}
}

// GrokOAuthRequestProviderEligibilityError rejects missing, non-OAuth, or
// unschedulable Grok records and unloaded configured proxies. Nil success means
// this scheduling/proxy gate passed; token completeness and expiry are checked
// by token callers.
func GrokOAuthRequestProviderEligibilityError(provider *Record) error {
	if provider == nil || !provider.IsGrokOAuth() || !provider.IsSchedulable() {
		return ErrRefreshProviderStateChanged
	}
	if provider.ProxyID != nil && provider.Proxy == nil {
		return ErrGrokOAuthConfiguredProxyMiss
	}
	return nil
}

// CloneGrokProxyID returns a separate pointer containing the optional proxy
// identity. Nil stays nil, keeping absence distinct from a populated zero while
// later comparisons remain independent of the original pointer.
func CloneGrokProxyID(proxyID *int64) *int64 {
	if proxyID == nil {
		return nil
	}
	value := *proxyID
	return &value
}

// InvalidateToken deletes this provider's cached access token when receiver,
// cache, and record exist. Missing dependencies are a successful skip; a
// configured deletion returns its cache error to the caller.
func (p *GrokTokenSource) InvalidateToken(ctx context.Context, provider *Record) error {
	if p == nil || p.Cache == nil || provider == nil {
		return nil
	}
	return p.Cache.DeleteAccessToken(ctx, GrokTokenCacheKey(provider))
}

// GrokTokenCacheKey returns a decimal provider-ID key for token cache and
// refresh coordination. Nil and zero-ID records share the zero key; account
// email and other credential metadata do not affect it.
func GrokTokenCacheKey(provider *Record) string {
	if provider == nil {
		return "grok:provider:0"
	}
	return "grok:provider:" + strconv.FormatInt(provider.ID, 10)
}
