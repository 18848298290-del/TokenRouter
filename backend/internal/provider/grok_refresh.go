package provider

import (
	"context"
	"errors"
	"hash/fnv"
	"strings"
	"time"
)

// grok_refresh.go selects Grok credentials for timed refresh and builds their
// replacement credential maps.
// Grok token maintenance needs a renewable OAuth credential before expiry
// timing can select it. Missing access token or unparseable expiry requests
// refresh. For an otherwise usable token, the base window is raised to at least
// one hour before a stable provider-ID offset of zero through 179 seconds is
// subtracted. The jitter helper also enforces its thirty-minute lower bound.
//
// The refresher returns a credential update for its persistence caller. Newly
// exchanged fields override stored values, omitted metadata is inherited
// through MergeCredentials, and a nonblank stored base URL is restored after
// the exchange builder supplies its default. This keeps custom transport
// selection paired with renewed credentials. Eligibility and timing remain
// separate methods, so callers apply platform/type admission before using the
// timing helper.

const (
	// GrokTokenRefreshSkew is the base window for refreshing tokens before
	// expiry.
	// Refreshing ahead of requests helps keep providers usable on
	// token-cache misses.
	GrokTokenRefreshSkew = time.Hour

	// GrokTokenRefreshJitterMax bounds the stable offset subtracted from
	// each provider's refresh window.
	// The offset spreads refreshes for providers imported together.
	GrokTokenRefreshJitterMax = 3 * time.Minute

	// GrokTokenRefreshSkewMin is the thirty-minute lower window applied
	// after
	// subtracting the stable provider-ID offset.
	GrokTokenRefreshSkewMin = 30 * time.Minute
)

// GrokTokenRefresher selects renewable Grok records and assembles exchanged
// credential updates.
type GrokTokenRefresher struct {
	// grokOAuthService is retained by construction and required only for
	// Refresh.
	// Timing and cache-key decisions remain usable before the exchange
	// dependency
	// is configured.
	grokOAuthService GrokRefreshTokenService // retained exchange and credential builder
}

// GrokRefreshTokenService supplies token exchange and credential assembly for
// refresh callers.
type GrokRefreshTokenService interface {
	// RefreshProviderToken exchanges a supplied Grok OAuth record and
	// returns
	// token information. The refresh caller passes its context, propagates
	// exchange errors, and leaves credential construction and persistence
	// as later
	// steps.
	RefreshProviderToken(context.Context, *Record) (*GrokTokenInfo, error)
	// BuildProviderCredentials assembles token information into credential
	// update
	// keys for the refresh caller. The caller merges omitted stored
	// metadata and
	// restores a custom endpoint before handing the update to persistence.
	BuildProviderCredentials(*GrokTokenInfo) map[string]any
}

// NewGrokTokenRefresher retains the OAuth exchange dependency for later
// credential renewal. Nil permits eligibility, timing, and key decisions;
// Refresh reports that missing dependency when an exchange is requested.
func NewGrokTokenRefresher(grokOAuthService GrokRefreshTokenService) *GrokTokenRefresher {
	return &GrokTokenRefresher{grokOAuthService: grokOAuthService}
}

// CacheKey returns the provider-ID Grok token key used for shared refresh
// locking and caching. Nil records select the zero-ID key; other provider
// metadata does not contribute to this identity.
func (r *GrokTokenRefresher) CacheKey(provider *Record) string {
	return GrokTokenCacheKey(provider)
}

// CanRefresh reports whether a nonnil Grok OAuth record has a nonblank refresh
// token. This admission check leaves expiry timing to NeedsRefresh, whose
// direct callers apply platform and type qualification separately.
func (r *GrokTokenRefresher) CanRefresh(provider *Record) bool {
	return provider != nil && provider.Platform == PlatformGrok && provider.Type == ProviderTypeOAuth &&
		strings.TrimSpace(provider.GetGrokRefreshToken()) != ""
}

// NeedsRefresh reports whether renewable credentials need token exchange under
// the chosen time window. Nil records or blank refresh tokens return false,
// while missing access token or unparseable expiry requests refresh.
//
// For a valid expiry, the supplied window is raised to one hour and then
// adjusted by stable provider-ID jitter before the strict remaining-time
// comparison. Call CanRefresh for platform/type admission; this timing helper
// does not repeat that check. A token exactly at the adjusted window remains
// outside the strict refresh condition.
func (r *GrokTokenRefresher) NeedsRefresh(provider *Record, refreshWindow time.Duration) bool {
	if provider == nil || strings.TrimSpace(provider.GetGrokRefreshToken()) == "" {
		return false
	}
	if strings.TrimSpace(provider.GetGrokAccessToken()) == "" {
		return true
	}
	expiresAt := provider.GetCredentialAsTime("expires_at")
	if expiresAt == nil {
		return true
	}
	// Caller windows below one hour use the maintenance policy floor.
	if refreshWindow < GrokTokenRefreshSkew {
		refreshWindow = GrokTokenRefreshSkew
	}
	// A stable provider-ID hash spreads refresh times reproducibly.
	refreshWindow = GrokTokenRefreshWindowWithJitter(provider.ID, refreshWindow)
	return time.Until(*expiresAt) < refreshWindow
}

// GrokTokenRefreshWindowWithJitter subtracts a stable whole-second provider-ID
// offset below three minutes and clamps the result to thirty minutes.
// Nonpositive IDs and windows at or below that minimum pass through unchanged.
func GrokTokenRefreshWindowWithJitter(providerID int64, refreshWindow time.Duration) time.Duration {
	if providerID <= 0 || refreshWindow <= GrokTokenRefreshSkewMin {
		return refreshWindow
	}
	h := fnv.New32a()
	var b [8]byte
	id := uint64(providerID)
	// Encode all eight provider-ID bytes least-significant first, giving
	// FNV
	// one fixed-width representation for the stable timing calculation.
	for i := range 8 {
		b[i] = byte(id >> (8 * i))
	}
	// FNV accepts every input byte and returns no error. Discard the write
	// result and use its digest to choose repeatable provider-specific
	// timing.
	_, _ = h.Write(b[:])
	// Jitter is measured in whole seconds and excludes the upper bound.
	jitter := time.Duration(h.Sum32()%uint32(GrokTokenRefreshJitterMax/time.Second)) * time.Second
	out := refreshWindow - jitter
	if out < GrokTokenRefreshSkewMin {
		return GrokTokenRefreshSkewMin
	}
	return out
}

// Refresh exchanges a nonnil provider's credentials and returns their merged
// update map for separate persistence. Missing exchange dependency returns an
// error, and exchange failures leave the caller to decide recovery.
//
// The builder's returned token fields override existing values, while absent
// keys inherit stored metadata through MergeCredentials. A nonblank stored base
// URL is then restored in trimmed form so token renewal keeps the selected
// custom transport. Nested inherited values follow the merge helper's shallow
// ownership rules; callers stabilize shared inputs and own later persistence.
func (r *GrokTokenRefresher) Refresh(ctx context.Context, provider *Record) (map[string]any, error) {
	if r == nil || r.grokOAuthService == nil {
		return nil, errors.New("grok oauth service is not configured")
	}
	tokenInfo, err := r.grokOAuthService.RefreshProviderToken(ctx, provider)
	if err != nil {
		return nil, err
	}
	newCredentials := r.grokOAuthService.BuildProviderCredentials(tokenInfo)
	// Keep omitted stored metadata while exchanged fields replace supplied
	// values; restore the operator's custom endpoint after the builder's
	// default
	// participates in that merge.
	newCredentials = MergeCredentials(provider.Credentials, newCredentials)
	if baseURL := strings.TrimSpace(provider.GetCredential("base_url")); baseURL != "" {
		newCredentials["base_url"] = baseURL
	}
	return newCredentials, nil
}
