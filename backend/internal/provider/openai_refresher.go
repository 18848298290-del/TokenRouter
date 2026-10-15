package provider

import (
	"context"
	"strings"
	"time"
)

// openai_refresher.go adapts OpenAI authorization to background credential
// refresh eligibility and update assembly.
// Background refresh uses the same provider-ID cache identity as request-side
// OpenAI token retrieval. Shadows retain credentials with their parent and are
// excluded from adapter eligibility. PATs and records lacking a nonblank
// refresh token do not request expiry-based exchange.
//
// A missing or invalid expiry becomes a refresh signal only while the record
// is rate-limited, avoiding exchange solely because expiry metadata is absent.
// Successful authorization metadata is assembled into a fresh update map,
// filled from stored keys that the response omitted, then normalized for PAT
// mode. That ordering retains account configuration while removing OAuth-only
// state inherited during the merge; versioning and persistence remain with the
// shared coordinator.

// OpenAIRefreshTokenService obtains authorization metadata for a
// caller-supplied OpenAI provider record.
type OpenAIRefreshTokenService interface {
	// RefreshProviderToken obtains metadata according to the record's authentication
	// mode. The caller supplies a stable nonnil record and owns subsequent merging
	// and persistence of the returned token information.
	RefreshProviderToken(context.Context, *Record) (*OpenAITokenInfo, error)
}

// OpenAITokenRefresher adapts authorization metadata to the shared
// coordinator's credential-update format.
type OpenAITokenRefresher struct {
	// Authorization is borrowed and required for Refresh calls. Its shared
	// service determines exchange, PAT validation and metadata enrichment behavior.
	Authorization OpenAIRefreshTokenService
}

// CacheKey returns the provider-ID OpenAI token identity also used for refresh
// locking. Supply a nonnil record for the intended credential owner; this
// method passes its ID through without resolving shadows or validating
// provider eligibility.
func (r *OpenAITokenRefresher) CacheKey(provider *Record) string {
	return OpenAITokenCacheKey(provider)
}

// CanRefresh reports OpenAI OAuth eligibility after rejecting credential
// shadows. Supply a nonnil record; this classification does not inspect
// refresh-token presence or expiry, which NeedsRefresh evaluates separately
// before a background exchange.
func (r *OpenAITokenRefresher) CanRefresh(provider *Record) bool {
	if provider.IsCredentialShadow() {
		return false
	}
	return provider.Platform == PlatformOpenAI && provider.Type == ProviderTypeOAuth
}

// NeedsRefresh selects whether credentials should be exchanged within
// refreshWindow after excluding PATs and records without a nonblank refresh
// token. Parsed expiry uses a strict less-than comparison with that window.
//
// Missing or invalid expiry uses the record's current rate-limit state as the
// refresh signal, so metadata gaps alone do not request an exchange. Platform
// and shadow eligibility remain with CanRefresh, which the coordinator
// evaluates separately. The supplied record remains an observation throughout
// this decision, and no exchange or state mutation occurs here.
func (r *OpenAITokenRefresher) NeedsRefresh(provider *Record, refreshWindow time.Duration) bool {
	if provider.IsOpenAIPersonalAccessToken() {
		return false
	}
	if strings.TrimSpace(provider.GetOpenAIRefreshToken()) == "" {
		return false
	}
	expiresAt := provider.GetCredentialAsTime("expires_at")
	// Background eligibility treats absent expiry differently from request-side
	// refresh admission; an active rate-limit observation supplies the extra exchange signal.
	if expiresAt == nil {
		return provider.IsRateLimited()
	}

	return time.Until(*expiresAt) < refreshWindow
}

// Refresh returns merged credential updates after the configured authorization
// service succeeds. The supplied record stays the source of omitted fields,
// and nested inherited values remain shared.
//
// BuildOpenAIProviderCredentials writes returned access-token and present
// metadata fields into a new map. MergeCredentials fills keys omitted by that
// response before PAT normalization removes OAuth-only state, including state
// inherited from the record. This order retains configuration while preventing
// a PAT from acquiring refresh behavior through stale fields. The coordinator
// owns versioning and persistence; callers provide a stable record and
// callable Authorization.
func (r *OpenAITokenRefresher) Refresh(ctx context.Context, provider *Record) (map[string]any, error) {
	tokenInfo, err := r.Authorization.RefreshProviderToken(ctx, provider)
	if err != nil {
		return nil, err
	}

	// Missing response keys inherit stored configuration before PAT cleanup removes
	// OAuth-only fields, preventing stale refresh metadata from surviving that merge.
	newCredentials := BuildOpenAIProviderCredentials(tokenInfo)
	newCredentials = MergeCredentials(provider.Credentials, newCredentials)
	newCredentials = NormalizeOpenAIPersonalAccessTokenCredentials(provider, tokenInfo, newCredentials)

	return newCredentials, nil
}
