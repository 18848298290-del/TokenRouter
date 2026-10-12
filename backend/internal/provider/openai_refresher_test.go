package provider

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// The background OpenAI refresher separates provider eligibility from its
// decision to exchange a near-expiry token. These cases check refresh-token
// presence, exclusion of credential shadows and the supported OpenAI OAuth
// type while leaving actual exchanges and persistence to other tests.

// TestOpenAITokenRefresher_NeedsRefresh_SkipsProviderWithoutRefreshToken
// compares near-expiry OAuth records with and without a refresh token. Only
// the record with a refresh token requests exchange inside the supplied
// five-minute window.
func TestOpenAITokenRefresher_NeedsRefresh_SkipsProviderWithoutRefreshToken(t *testing.T) {
	refresher := &OpenAITokenRefresher{}
	expiresAt := time.Now().Add(time.Minute).UTC().Format(time.RFC3339)

	withoutRT := &Record{
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"access_token": "access-token",
			"expires_at":   expiresAt,
		},
	}
	require.False(t, refresher.NeedsRefresh(withoutRT, 5*time.Minute))

	withRT := &Record{
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"access_token":  "access-token",
			"refresh_token": "refresh-token",
			"expires_at":    expiresAt,
		},
	}
	require.True(t, refresher.NeedsRefresh(withRT, 5*time.Minute))
}

// TestOpenAITokenRefresherSkipsShadow compares a credential shadow with a
// regular OpenAI OAuth record. CanRefresh rejects the shadow despite its
// matching platform and type, while the regular record remains eligible.
func TestOpenAITokenRefresherSkipsShadow(t *testing.T) {
	pid := int64(100)
	r := &OpenAITokenRefresher{}
	// Credential shadows are excluded even when their platform and type match.
	require.False(t, r.CanRefresh(&Record{ID: 200, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, ParentProviderID: &pid}))
	// A regular OpenAI OAuth provider is eligible; NeedsRefresh separately checks its refresh token.
	require.True(t, r.CanRefresh(&Record{ID: 100, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, Credentials: map[string]any{"refresh_token": "RT"}}))
}

// TestOpenAITokenRefresher_CanRefresh supplies OpenAI OAuth and API-key
// records through the eligibility table. CanRefresh accepts the OAuth type and
// rejects the API-key type, independently of expiry evaluation.
func TestOpenAITokenRefresher_CanRefresh(t *testing.T) {
	refresher := &OpenAITokenRefresher{}

	tests := []struct {
		name     string // refresher eligibility case label
		platform string // supplied provider platform value
		accType  string // supplied provider authentication type
		want     bool   // expected provider eligibility result
	}{
		{
			name:     "openai oauth - can refresh",
			platform: capability.PlatformOpenAI,
			accType:  capability.ProviderTypeOAuth,
			want:     true,
		},
		{
			name:     "openai apikey - cannot refresh",
			platform: capability.PlatformOpenAI,
			accType:  capability.ProviderTypeAPIKey,
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &Record{
				Platform: tt.platform,
				Type:     tt.accType,
			}
			require.Equal(t, tt.want, refresher.CanRefresh(provider))
		})
	}
}
