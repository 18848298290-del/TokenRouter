package provider

import (
	"strconv"
	"strings"
	"time"
)

func CanRefreshQoder(provider *Record) bool {
	return provider != nil && provider.Platform == PlatformQoder && provider.Type == ProviderTypeCosy
}

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
		return time.Until(*expiresAt) < 15*time.Minute
	}
	// Qoder RateLimitResetAt also represents monthly quota and agent limits.
	// Those scheduling signals do not establish that a token needs refreshing.
	return false
}

func QoderTokenCacheKey(provider *Record) string {
	if provider == nil {
		return "qoder:provider:0"
	}
	return "qoder:provider:" + strconv.FormatInt(provider.ID, 10)
}

func MergeQoderRefreshCredentials(oldCredentials, newCredentials map[string]any, site, refreshMode string, expiresAt time.Time) map[string]any {
	newCredentials = MergeCredentials(oldCredentials, newCredentials)
	newCredentials["site"] = site
	newCredentials["refresh_mode"] = refreshMode
	if site == "cn" {
		// Remove legacy machine-token fields after merging to use the CN site's identity fields.
		delete(newCredentials, "machine_token")
		delete(newCredentials, "machine_type")
	}
	if !expiresAt.IsZero() {
		newCredentials["expires_at"] = expiresAt.UTC().Format(time.RFC3339)
	}
	refreshToken := strings.TrimSpace(qoderOldRefreshToken(oldCredentials))
	if strings.TrimSpace(stringFromCredentialValue(newCredentials["refresh_token"])) == "" {
		if refreshToken != "" {
			newCredentials["refresh_token"] = refreshToken
		}
	}
	// When the exchange supplies no expiry, discard the stored expiry.
	// Retaining an old expires_at would make NeedsRefreshQoder treat freshly refreshed credentials
	// as near expiry and could trigger repeated refreshes.
	if expiresAt.IsZero() {
		delete(newCredentials, "expires_at")
	}
	return newCredentials
}

func stringFromCredentialValue(value any) string {
	switch v := value.(type) {
	case string:
		return v
	default:
		return ""
	}
}

func qoderOldRefreshToken(v map[string]any) string {
	return stringFromCredentialValue(v["refresh_token"])
}

// NeedsRefreshQoderAfterFailure compares the current credentials with those used by a failed request.
// Matching failed credentials can refresh regardless of expiry; rotated credentials skip another refresh.
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

// QoderRefreshCredentialsHash hashes identity fields used to detect competing credential updates.
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
	auth := make(map[string]any, len(keys))
	for _, key := range keys {
		if value, ok := credentials[key]; ok {
			auth[key] = value
		}
	}
	return QoderCredentialsHash(auth)
}
