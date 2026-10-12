package provider

import (
	"context"
	"errors"
	"hash/fnv"
	"strings"
	"time"
)

const (
	// GrokTokenRefreshSkew is the base window for refreshing tokens before expiry.
	// Refreshing ahead of requests helps keep providers usable on token-cache misses.
	GrokTokenRefreshSkew = time.Hour

	// GrokTokenRefreshJitterMax bounds the stable offset subtracted from each provider's refresh window.
	// The offset spreads refreshes for providers imported together.
	GrokTokenRefreshJitterMax = 3 * time.Minute

	// GrokTokenRefreshSkewMin leaves time to finish a refresh after applying jitter.
	GrokTokenRefreshSkewMin = 30 * time.Minute
)

type GrokTokenRefresher struct {
	grokOAuthService GrokRefreshTokenService
}

// GrokRefreshTokenService refreshes tokens and converts the results into stored credentials.
type GrokRefreshTokenService interface {
	RefreshProviderToken(context.Context, *Record) (*GrokTokenInfo, error)
	BuildProviderCredentials(*GrokTokenInfo) map[string]any
}

func NewGrokTokenRefresher(grokOAuthService GrokRefreshTokenService) *GrokTokenRefresher {
	return &GrokTokenRefresher{grokOAuthService: grokOAuthService}
}

func (r *GrokTokenRefresher) CacheKey(provider *Record) string {
	return GrokTokenCacheKey(provider)
}

func (r *GrokTokenRefresher) CanRefresh(provider *Record) bool {
	return provider != nil && provider.Platform == PlatformGrok && provider.Type == ProviderTypeOAuth &&
		strings.TrimSpace(provider.GetGrokRefreshToken()) != ""
}

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
	if refreshWindow < GrokTokenRefreshSkew {
		refreshWindow = GrokTokenRefreshSkew
	}
	// A stable provider-ID hash spreads refresh times reproducibly.
	refreshWindow = GrokTokenRefreshWindowWithJitter(provider.ID, refreshWindow)
	return time.Until(*expiresAt) < refreshWindow
}

// GrokTokenRefreshWindowWithJitter subtracts a provider-specific offset in [0, GrokTokenRefreshJitterMax).
// Positive provider IDs with windows above GrokTokenRefreshSkewMin are clamped to that minimum; other inputs
// pass through.
func GrokTokenRefreshWindowWithJitter(providerID int64, refreshWindow time.Duration) time.Duration {
	if providerID <= 0 || refreshWindow <= GrokTokenRefreshSkewMin {
		return refreshWindow
	}
	h := fnv.New32a()
	var b [8]byte
	id := uint64(providerID)
	for i := range 8 {
		b[i] = byte(id >> (8 * i))
	}
	_, _ = h.Write(b[:])
	// Jitter is measured in whole seconds and excludes the upper bound.
	jitter := time.Duration(h.Sum32()%uint32(GrokTokenRefreshJitterMax/time.Second)) * time.Second
	out := refreshWindow - jitter
	if out < GrokTokenRefreshSkewMin {
		return GrokTokenRefreshSkewMin
	}
	return out
}

func (r *GrokTokenRefresher) Refresh(ctx context.Context, provider *Record) (map[string]any, error) {
	if r == nil || r.grokOAuthService == nil {
		return nil, errors.New("grok oauth service is not configured")
	}
	tokenInfo, err := r.grokOAuthService.RefreshProviderToken(ctx, provider)
	if err != nil {
		return nil, err
	}
	newCredentials := r.grokOAuthService.BuildProviderCredentials(tokenInfo)
	newCredentials = MergeCredentials(provider.Credentials, newCredentials)
	if baseURL := strings.TrimSpace(provider.GetCredential("base_url")); baseURL != "" {
		newCredentials["base_url"] = baseURL
	}
	return newCredentials, nil
}
