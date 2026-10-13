package provider

import (
	"strconv"
	"strings"
	"time"
)

// Qoder PAT sessions and refresh-token sessions have different maintenance
// eligibility. Periodic refresh excludes PAT and requires an observed expiry
// within fifteen minutes, while failed-request coordination can refresh the
// same authentication identity regardless of expiry and ignores unrelated model
// mapping changes.
//
// A refreshed credential map inherits omitted metadata through
// MergeCredentials, retaining shared nested values from the old record. Site
// and mode then control cleanup: CN drops legacy machine fields, an omitted new
// expiry drops the old deadline, and an omitted refresh token can retain the
// previous usable token. The caller persists this returned map through the
// refresh coordinator.

// CanRefreshQoder reports whether a nonnil record has Qoder platform and COSY
// type.
func CanRefreshQoder(provider *Record) bool {
	return provider != nil && provider.Platform == PlatformQoder && provider.Type == ProviderTypeCosy
}

// NeedsRefreshQoder reports whether a Qoder COSY token is within the fixed
// fifteen-minute expiry window. The duration argument is ignored; PAT records,
// absent refresh tokens, and missing expiry return false, while an already
// expired token qualifies.
func NeedsRefreshQoder(provider *Record, _ time.Duration) bool {
	if !CanRefreshQoder(provider) {
		return false
	}
	if strings.TrimSpace(provider.GetCredential("pat")) != "" {
		return false
	}
	if strings.TrimSpace(provider.GetCredential("refresh_token")) == "" {
		return false
	}
	if expiresAt := provider.GetCredentialAsTime("expires_at"); expiresAt != nil {
		// Refresh within the fixed fifteen-minute expiry window.
		return time.Until(*expiresAt) < 15*time.Minute
	}
	// Qoder RateLimitResetAt also represents monthly quota and agent
	// limits.
	// Those scheduling signals do not establish that a token needs
	// refreshing.
	return false
}

// QoderTokenCacheKey returns the provider-ID key shared by Qoder token caching
// and refresh coordination. Nil uses ID zero; identity metadata such as UID or
// PAT does not alter this key.
func QoderTokenCacheKey(provider *Record) string {
	if provider == nil {
		return "qoder:provider:0"
	}
	return "qoder:provider:" + strconv.FormatInt(provider.ID, 10)
}

// MergeQoderRefreshCredentials mutates and returns the supplied new-credential
// map, allocating one when nil and inheriting omitted old keys. Inherited
// nested values stay shared with oldCredentials, so callers keep them stable
// during this merge and copy them before later mutation.
//
// The selected site and refresh mode replace inherited metadata. CN removes
// legacy machine token/type fields; a new expiry is formatted in UTC, and an
// absent expiry removes the old deadline. A blank replacement refresh token can
// retain the trimmed old token so omission does not break the next exchange.
func MergeQoderRefreshCredentials(oldCredentials, newCredentials map[string]any, site, refreshMode string, expiresAt time.Time) map[string]any {
	newCredentials = MergeCredentials(oldCredentials, newCredentials)
	newCredentials["site"] = site
	newCredentials["refresh_mode"] = refreshMode
	if site == "cn" {
		// CN exchanges supply their current identity fields; remove
		// inherited
		// legacy machine token/type values so later session preparation
		// sees
		// the site-specific form.
		delete(newCredentials, "machine_token")
		delete(newCredentials, "machine_type")
	}
	// Encode the exchanged deadline as UTC text for shared credential
	// readers.
	if !expiresAt.IsZero() {
		newCredentials["expires_at"] = expiresAt.UTC().Format(time.RFC3339)
	}
	refreshToken := strings.TrimSpace(qoderOldRefreshToken(oldCredentials))
	if strings.TrimSpace(stringFromCredentialValue(newCredentials["refresh_token"])) == "" {
		// Keep a usable previous refresh token when the exchange omits
		// or
		// blanks it, so the following refresh still has an input token.
		if refreshToken != "" {
			newCredentials["refresh_token"] = refreshToken
		}
	}
	// An exchange with no new expiry cannot renew the old deadline; remove
	// it so freshly exchanged credentials do not immediately reenter the
	// near-expiry path in NeedsRefreshQoder.
	if expiresAt.IsZero() {
		delete(newCredentials, "expires_at")
	}
	return newCredentials
}

// stringFromCredentialValue returns a credential string exactly as stored.
// Unsupported types produce an empty string so refresh-token checks can treat
// them as missing rather than stringify arbitrary values.
func stringFromCredentialValue(value any) string {
	switch v := value.(type) {
	case string:
		return v
	default:
		return ""
	}
}

// qoderOldRefreshToken reads the previous refresh token through string-only
// credential conversion.
func qoderOldRefreshToken(v map[string]any) string {
	return stringFromCredentialValue(v["refresh_token"])
}

// NeedsRefreshQoderAfterFailure reports refresh eligibility for the failed
// authentication identity when a hash is supplied. It requires Qoder COSY and a
// nonblank refresh token; a matching hash bypasses expiry checks, while an
// empty hash delegates to NeedsRefreshQoder.
func NeedsRefreshQoderAfterFailure(value *Record, failedCredentials string, ttl time.Duration) bool {
	if !CanRefreshQoder(value) {
		return false
	}
	if strings.TrimSpace(value.GetCredential("refresh_token")) == "" {
		return false
	}
	if failedCredentials != "" {
		return QoderRefreshCredentialsHash(value.Credentials) == failedCredentials
	}
	return NeedsRefreshQoder(value, ttl)
}

// QoderRefreshCredentialsHash returns the JSON digest of selected
// authentication, site, and organization values for failed-request refresh
// coordination. Callers keep the credentials stable while these values are
// borrowed for hashing. With authentication fields present, expiry and model
// mapping changes leave that selected identity unchanged.
//
// An empty source map hashes nil, while a nonempty source with no selected keys
// hashes an empty object. That distinction follows QoderCredentialsHash and
// remains observable even though unrelated keys are omitted. Only the listed
// authentication keys contribute values to the filtered map.
func QoderRefreshCredentialsHash(credentials map[string]any) string {
	if len(credentials) == 0 {
		return QoderCredentialsHash(nil)
	}
	keys := []string{
		"pat",
		"security_oauth_token",
		"refresh_token",
		"machine_id",
		"machine_token",
		"machine_type",
		"uid",
		"aid",
		"organization_id",
		"organization_name",
		"name",
		"user_type",
		"site",
		"refresh_mode",
	}
	// Filter authentication keys before hashing so an expiry or
	// model-mapping
	// edit does not separate the failed request from its otherwise
	// unchanged
	// refresh identity.
	auth := make(map[string]any, len(keys))
	for _, key := range keys {
		if value, ok := credentials[key]; ok {
			auth[key] = value
		}
	}
	return QoderCredentialsHash(auth)
}
