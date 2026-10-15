package provider

import (
	"strings"
	"time"

	wire "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// openai_token_values.go builds OpenAI credential maps from OAuth exchanges
// and validated PAT metadata.
// OAuth exchanges, imported access tokens and PAT validation return different
// combinations of OpenAI metadata. Credential updates always include
// access_token, while absent optional response fields are omitted so merging
// can retain stored configuration. Positive OAuth expiry becomes an RFC3339
// credential string, and refresh-token whitespace is used to decide whether a
// replacement exists.
//
// PAT updates normalize authentication mode and remove OAuth refresh state
// after assembly. Canonical PAT metadata includes the FedRAMP boolean even
// when false, allowing a later merge to clear a previous true value; other
// authentication results include that flag only when true. Token information
// also carries privacy results for callers, while the credential builder
// selects the stored credential keys and leaves those response-only fields
// outside its map.

// OpenAIAuthURLResult pairs an upstream authorization URL with the stored PKCE
// session identity.
type OpenAIAuthURLResult struct {
	AuthURL   string `json:"auth_url"`   // generated upstream authorization URL
	SessionID string `json:"session_id"` // stored PKCE session identity
}

// OpenAIExchangeCodeInput supplies a code, session proof and optional
// exchange-parameter overrides.
type OpenAIExchangeCodeInput struct {
	SessionID string // stored authorization session identity
	Code      string // returned upstream authorization code
	// State supplies the proof compared with the stored session before exchange.
	// An empty value or a byte mismatch is rejected before calling the upstream client.
	State string
	// RedirectURI overrides the session redirect when nonempty. An empty value
	// retains the redirect selected when the authorization URL was generated.
	RedirectURI string
	// ProxyID selects a proxy lookup when supplied. A found result overrides
	// the session URL; an absent result retains that URL, and errors abort exchange.
	ProxyID *int64
	// TLSFingerprintRouterID selects exchange transport options. Nil maps to zero,
	// and the adapter ignores nonpositive IDs and unavailable or disabled routers.
	TLSFingerprintRouterID *int64
}

// OpenAITokenInfo carries authorization credentials and account metadata for
// response and persistence consumers.
type OpenAITokenInfo struct {
	AccessToken string `json:"access_token"` // issued or supplied access token
	// RefreshToken is omitted from credential updates when blank after trimming,
	// allowing merge callers to retain their previously stored refresh token.
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token,omitempty"` // optional account metadata JWT
	// ExpiresIn and ExpiresAt describe OAuth expiry as a duration and absolute
	// timestamp. PAT validation leaves both zero, and PAT credential normalization
	// removes expiry fields even if metadata supplied a positive timestamp.
	ExpiresIn int64 `json:"expires_in"` // seconds
	ExpiresAt int64 `json:"expires_at"` // Unix-seconds
	// ClientID identifies the OAuth client used for exchange. Credential assembly
	// trims it and omits an empty value so merges can retain stored client identity.
	ClientID         string `json:"client_id,omitempty"`
	AuthMode         string `json:"auth_mode,omitempty"`          // selected credential authentication mode
	Email            string `json:"email,omitempty"`              // reported account email address
	ChatGPTAccountID string `json:"chatgpt_account_id,omitempty"` // reported ChatGPT account identity
	ChatGPTUserID    string `json:"chatgpt_user_id,omitempty"`    // reported ChatGPT user identity
	// ChatGPTAccountFedRAMP is validated by PAT whoami. Canonical PAT updates
	// include false; other modes omit false so an existing flag can remain during merging.
	ChatGPTAccountFedRAMP bool   `json:"chatgpt_account_is_fedramp,omitempty"`
	OrganizationID        string `json:"organization_id,omitempty"`         // reported organization account identity
	PlanType              string `json:"plan_type,omitempty"`               // selected account subscription plan
	SubscriptionExpiresAt string `json:"subscription_expires_at,omitempty"` // upstream subscription expiry text
	PrivacyMode           string `json:"privacy_mode,omitempty"`            // reported training configuration result
}

// BuildOpenAIProviderCredentials returns a new credential update map from
// nonnil tokenInfo, always including access_token. Optional absent metadata is
// omitted so MergeCredentials can retain stored values; positive ExpiresAt is
// formatted as RFC3339.
//
// A nonblank refresh token is included as supplied, while client IDs are
// trimmed. Canonical PAT mode writes both authentication markers, Bearer type
// and the FedRAMP boolean, including false; other modes include the FedRAMP
// flag only when true. PAT normalization then removes OAuth-only refresh and
// expiry fields. Callers own any later merge and persistence, and privacy
// results remain on tokenInfo for their separate consumers.
func BuildOpenAIProviderCredentials(tokenInfo *OpenAITokenInfo) map[string]any {
	creds := map[string]any{
		"access_token": tokenInfo.AccessToken,
	}
	// Encode absolute expiry with the RFC3339 layout used by credential readers.
	if tokenInfo.ExpiresAt > 0 {
		creds["expires_at"] = time.Unix(tokenInfo.ExpiresAt, 0).Format(time.RFC3339)
	}
	// OAuth responses may omit a replacement refresh token; excluding that key
	// lets MergeCredentials retain the existing renewable credential after successful exchange.
	if strings.TrimSpace(tokenInfo.RefreshToken) != "" {
		creds["refresh_token"] = tokenInfo.RefreshToken
	}

	if tokenInfo.IDToken != "" {
		creds["id_token"] = tokenInfo.IDToken
	}
	if tokenInfo.Email != "" {
		creds["email"] = tokenInfo.Email
	}
	if tokenInfo.ChatGPTAccountID != "" {
		creds["chatgpt_account_id"] = tokenInfo.ChatGPTAccountID
	}
	if tokenInfo.ChatGPTUserID != "" {
		creds["chatgpt_user_id"] = tokenInfo.ChatGPTUserID
	}
	if tokenInfo.OrganizationID != "" {
		creds["organization_id"] = tokenInfo.OrganizationID
	}
	if tokenInfo.PlanType != "" {
		creds["plan_type"] = tokenInfo.PlanType
	}
	if tokenInfo.SubscriptionExpiresAt != "" {
		creds["subscription_expires_at"] = tokenInfo.SubscriptionExpiresAt
	}
	if strings.TrimSpace(tokenInfo.ClientID) != "" {
		creds["client_id"] = strings.TrimSpace(tokenInfo.ClientID)
	}
	// Canonical PAT metadata supplies an authoritative boolean, including false,
	// while other results add only a true flag and permit an omitted value to survive merging.
	if tokenInfo.AuthMode == OpenAIAuthModePersonalAccessToken {
		creds[OpenAIAuthModeCredentialKey] = OpenAIAuthModePersonalAccessToken
		creds[OpenAIAuthModeLegacyCredentialKey] = "personal_access_token"
		creds["token_type"] = "Bearer"
		creds["chatgpt_account_is_fedramp"] = tokenInfo.ChatGPTAccountFedRAMP
	} else if tokenInfo.ChatGPTAccountFedRAMP {
		creds["chatgpt_account_is_fedramp"] = true
	}

	return NormalizeOpenAIPersonalAccessTokenCredentials(nil, tokenInfo, creds)
}

// OpenAIPATTokenInfo returns PAT authorization metadata from a validated
// whoami response, trimming its identity strings and retaining accessToken as
// supplied. Supply nonnil whoami with a nonnil FedRAMP field, as enforced by
// ValidatePATWhoami; OAuth refresh and expiry fields stay zero.
func OpenAIPATTokenInfo(accessToken string, whoami *wire.PATWhoamiResponse) *OpenAITokenInfo {
	return &OpenAITokenInfo{
		AccessToken:           accessToken,
		AuthMode:              OpenAIAuthModePersonalAccessToken,
		Email:                 strings.TrimSpace(whoami.Email),
		ChatGPTAccountID:      strings.TrimSpace(whoami.ChatGPTAccountID),
		ChatGPTUserID:         strings.TrimSpace(whoami.ChatGPTUserID),
		ChatGPTAccountFedRAMP: *whoami.ChatGPTAccountIsFedRAMP,
		PlanType:              strings.TrimSpace(whoami.ChatGPTPlanType),
	}
}
