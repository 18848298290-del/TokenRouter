package provider

import (
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

func TestBuildClaudeProviderCredentials_Full(t *testing.T) {
	tokenInfo := &ClaudeTokenInfo{
		AccessToken:  "at-123",
		TokenType:    "Bearer",
		ExpiresIn:    3600,
		ExpiresAt:    1700000000,
		RefreshToken: "rt-456",
		Scope:        "openid",
	}

	creds := BuildClaudeProviderCredentials(tokenInfo)

	require.Equal(t, "at-123", creds["access_token"])
	require.Equal(t, "Bearer", creds["token_type"])
	require.Equal(t, "3600", creds["expires_in"])
	require.Equal(t, "1700000000", creds["expires_at"])
	require.Equal(t, "rt-456", creds["refresh_token"])
	require.Equal(t, "openid", creds["scope"])
}

func TestBuildClaudeProviderCredentials_Minimal(t *testing.T) {
	tokenInfo := &ClaudeTokenInfo{
		AccessToken: "at-789",
		TokenType:   "Bearer",
		ExpiresIn:   7200,
		ExpiresAt:   1700003600,
	}

	creds := BuildClaudeProviderCredentials(tokenInfo)

	require.Equal(t, "at-789", creds["access_token"])
	require.Equal(t, "Bearer", creds["token_type"])
	require.Equal(t, "7200", creds["expires_in"])
	require.Equal(t, "1700003600", creds["expires_at"])
	_, hasRefresh := creds["refresh_token"]
	_, hasScope := creds["scope"]
	require.False(t, hasRefresh, "refresh_token should not be set when empty")
	require.False(t, hasScope, "scope should not be set when empty")
}

func TestClaudeTokenRefresher_NeedsRefresh(t *testing.T) {
	refresher := &ClaudeTokenRefresher{}
	refreshWindow := 30 * time.Minute

	tests := []struct {
		name        string
		credentials map[string]any
		wantRefresh bool
	}{
		{
			name: "expires_at as string - expired",
			credentials: map[string]any{
				"expires_at": "1000", // 1970-01-01 00:16:40 UTC; expired.
			},
			wantRefresh: true,
		},
		{
			name: "expires_at as float64 - expired",
			credentials: map[string]any{
				"expires_at": float64(1000), // Numeric timestamp; expired.
			},
			wantRefresh: true,
		},
		{
			name: "expires_at as RFC3339 - expired",
			credentials: map[string]any{
				"expires_at": "1970-01-01T00:00:00Z", // RFC3339 timestamp; expired.
			},
			wantRefresh: true,
		},
		{
			name: "expires_at as string - far future",
			credentials: map[string]any{
				"expires_at": "9999999999", // Far-future timestamp.
			},
			wantRefresh: false,
		},
		{
			name: "expires_at as float64 - far future",
			credentials: map[string]any{
				"expires_at": float64(9999999999), // Numeric far-future timestamp.
			},
			wantRefresh: false,
		},
		{
			name: "expires_at as RFC3339 - far future",
			credentials: map[string]any{
				"expires_at": "2099-12-31T23:59:59Z", // RFC3339 far-future timestamp.
			},
			wantRefresh: false,
		},
		{
			name:        "expires_at missing",
			credentials: map[string]any{},
			wantRefresh: false,
		},
		{
			name: "expires_at is nil",
			credentials: map[string]any{
				"expires_at": nil,
			},
			wantRefresh: false,
		},
		{
			name: "expires_at is invalid string",
			credentials: map[string]any{
				"expires_at": "invalid",
			},
			wantRefresh: false,
		},
		{
			name:        "credentials is nil",
			credentials: nil,
			wantRefresh: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &Record{
				Platform:    capability.PlatformAnthropic,
				Type:        capability.ProviderTypeOAuth,
				Credentials: tt.credentials,
			}

			got := refresher.NeedsRefresh(provider, refreshWindow)
			require.Equal(t, tt.wantRefresh, got)
		})
	}
}

func TestClaudeTokenRefresher_NeedsRefresh_WithinWindow(t *testing.T) {
	refresher := &ClaudeTokenRefresher{}
	refreshWindow := 30 * time.Minute

	// Expire in 15 minutes, within the refresh window.
	expiresAt := time.Now().Add(15 * time.Minute).Unix()

	tests := []struct {
		name        string
		credentials map[string]any
	}{
		{
			name: "string type - within refresh window",
			credentials: map[string]any{
				"expires_at": strconv.FormatInt(expiresAt, 10),
			},
		},
		{
			name: "float64 type - within refresh window",
			credentials: map[string]any{
				"expires_at": float64(expiresAt),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &Record{
				Platform:    capability.PlatformAnthropic,
				Type:        capability.ProviderTypeOAuth,
				Credentials: tt.credentials,
			}

			got := refresher.NeedsRefresh(provider, refreshWindow)
			require.True(t, got, "should need refresh when within window")
		})
	}
}

func TestClaudeTokenRefresher_NeedsRefresh_OutsideWindow(t *testing.T) {
	refresher := &ClaudeTokenRefresher{}
	refreshWindow := 30 * time.Minute

	// Expire in one hour, outside the refresh window.
	expiresAt := time.Now().Add(1 * time.Hour).Unix()

	tests := []struct {
		name        string
		credentials map[string]any
	}{
		{
			name: "string type - outside refresh window",
			credentials: map[string]any{
				"expires_at": strconv.FormatInt(expiresAt, 10),
			},
		},
		{
			name: "float64 type - outside refresh window",
			credentials: map[string]any{
				"expires_at": float64(expiresAt),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &Record{
				Platform:    capability.PlatformAnthropic,
				Type:        capability.ProviderTypeOAuth,
				Credentials: tt.credentials,
			}

			got := refresher.NeedsRefresh(provider, refreshWindow)
			require.False(t, got, "should not need refresh when outside window")
		})
	}
}

func TestClaudeTokenRefresher_CanRefresh(t *testing.T) {
	refresher := &ClaudeTokenRefresher{}

	tests := []struct {
		name     string
		platform string
		accType  string
		want     bool
	}{
		{
			name:     "anthropic oauth - can refresh",
			platform: capability.PlatformAnthropic,
			accType:  capability.ProviderTypeOAuth,
			want:     true,
		},
		{
			name:     "anthropic setup-token - can refresh",
			platform: capability.PlatformAnthropic,
			accType:  capability.ProviderTypeSetupToken,
			want:     true,
		},
		{
			name:     "anthropic api-key - cannot refresh",
			platform: capability.PlatformAnthropic,
			accType:  capability.ProviderTypeAPIKey,
			want:     false,
		},
		{
			name:     "openai oauth - cannot refresh",
			platform: capability.PlatformOpenAI,
			accType:  capability.ProviderTypeOAuth,
			want:     false,
		},
		{
			name:     "gemini oauth - cannot refresh",
			platform: capability.PlatformGemini,
			accType:  capability.ProviderTypeOAuth,
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &Record{
				Platform: tt.platform,
				Type:     tt.accType,
			}

			got := refresher.CanRefresh(provider)
			require.Equal(t, tt.want, got)
		})
	}
}
