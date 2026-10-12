package openai

// OAuthTokenResponse represents the token response from OpenAI OAuth.
type OAuthTokenResponse struct {
	AccessToken  string `json:"access_token"`
	IDToken      string `json:"id_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token,omitempty"`
	Scope        string `json:"scope,omitempty"`
}

// OAuthIDTokenClaims represents the claims from OpenAI ID Token.
type OAuthIDTokenClaims struct {
	// Standard claims
	Sub           string   `json:"sub"`
	Email         string   `json:"email"`
	EmailVerified bool     `json:"email_verified"`
	Iss           string   `json:"iss"`
	Aud           []string `json:"aud"` // OpenAI returns aud as an array
	Exp           int64    `json:"exp"`
	Iat           int64    `json:"iat"`

	// OpenAI specific claims (nested under https://api.openai.com/auth)
	OpenAIAuth *OAuthAuthClaims `json:"https://api.openai.com/auth,omitempty"`
}

// OAuthAuthClaims represents the OpenAI specific auth claims.
type OAuthAuthClaims struct {
	ChatGPTAccountID string                   `json:"chatgpt_account_id"`
	ChatGPTUserID    string                   `json:"chatgpt_user_id"`
	ChatGPTPlanType  string                   `json:"chatgpt_plan_type"`
	UserID           string                   `json:"user_id"`
	POID             string                   `json:"poid"` // organization ID in access_token JWT
	Organizations    []OAuthOrganizationClaim `json:"organizations"`
}

// OAuthOrganizationClaim represents an organization in the ID Token.
type OAuthOrganizationClaim struct {
	ID        string `json:"id"`
	Role      string `json:"role"`
	Title     string `json:"title"`
	IsDefault bool   `json:"is_default"`
}

// OAuthUserInfo represents user information extracted from ID Token claims.
type OAuthUserInfo struct {
	Email            string
	ChatGPTAccountID string
	ChatGPTUserID    string
	PlanType         string
	UserID           string
	OrganizationID   string
	Organizations    []OAuthOrganizationClaim
}

// ChatGPTAccountInfo holds account data returned by chatgpt.com/backend-api/accounts/check.
type ChatGPTAccountInfo struct {
	PlanType string
	Email    string
	// ProviderID uses account.account_id, or the accounts map key when that field is absent.
	// Callers use it to distinguish personal accounts from workspaces in the same response.
	ProviderID            string
	SubscriptionExpiresAt string // entitlement.expires_at (RFC3339)
}

// PATWhoamiResponse holds user and account information returned by the PAT identity query.
type PATWhoamiResponse struct {
	Email                   string `json:"email"`
	ChatGPTUserID           string `json:"chatgpt_user_id"`
	ChatGPTAccountID        string `json:"chatgpt_account_id"`
	ChatGPTPlanType         string `json:"chatgpt_plan_type"`
	ChatGPTAccountIsFedRAMP *bool  `json:"chatgpt_account_is_fedramp"`
}

// GetUserInfo extracts user info from ID Token claims.
func (c *OAuthIDTokenClaims) GetUserInfo() *OAuthUserInfo {
	info := &OAuthUserInfo{
		Email: c.Email,
	}

	if c.OpenAIAuth != nil {
		info.ChatGPTAccountID = c.OpenAIAuth.ChatGPTAccountID
		info.ChatGPTUserID = c.OpenAIAuth.ChatGPTUserID
		info.PlanType = c.OpenAIAuth.ChatGPTPlanType
		info.UserID = c.OpenAIAuth.UserID
		info.Organizations = c.OpenAIAuth.Organizations

		// Prefer the default organization.
		for _, org := range c.OpenAIAuth.Organizations {
			if org.IsDefault {
				info.OrganizationID = org.ID
				break
			}
		}
		// Use the first organization when no default has an ID.
		if info.OrganizationID == "" && len(c.OpenAIAuth.Organizations) > 0 {
			info.OrganizationID = c.OpenAIAuth.Organizations[0].ID
		}
	}

	return info
}
