package openai

// OpenAI authorization exchanges return token credentials separately from the
// account and organization metadata carried by decoded claims or identity
// endpoints. Those account identifiers need to stay distinct from a local
// provider record ID when callers assemble persisted credential information.
//
// GetUserInfo copies claim strings into a new value and selects an organization
// from the ordered claim list. The first default flag wins, with an empty
// selected ID falling back to the first organization's ID; the organization
// slice remains shared with the decoded claims. These representations expose
// asserted metadata and perform no signature, issuer, audience, or expiry
// verification themselves. Account-check and PAT values retain their own
// returned identities for authorization's subsequent reconciliation.

// OAuthTokenResponse holds credentials and relative lifetime returned by
// OpenAI authorization.
type OAuthTokenResponse struct {
	AccessToken  string `json:"access_token"`            // upstream access credential
	IDToken      string `json:"id_token"`                // returned identity token
	TokenType    string `json:"token_type"`              // returned authorization scheme
	ExpiresIn    int64  `json:"expires_in"`              // seconds
	RefreshToken string `json:"refresh_token,omitempty"` // optional refresh credential
	Scope        string `json:"scope,omitempty"`         // granted OAuth scopes
}

// OAuthIDTokenClaims represents the claims from OpenAI ID Token.
type OAuthIDTokenClaims struct {
	Sub           string `json:"sub"`            // token subject identifier
	Email         string `json:"email"`          // asserted account email
	EmailVerified bool   `json:"email_verified"` // asserted email verification
	Iss           string `json:"iss"`            // asserted token issuer
	// Aud decodes a JSON audience array in this claim schema. A scalar
	// string cannot decode into this slice through the standard JSON decoder.
	Aud []string `json:"aud"`
	Exp int64    `json:"exp"` // Unix seconds
	Iat int64    `json:"iat"` // Unix seconds

	// OpenAIAuth holds the optional namespaced account claims. GetUserInfo
	// copies their fields when present and otherwise returns email with empty
	// account metadata.
	OpenAIAuth *OAuthAuthClaims `json:"https://api.openai.com/auth,omitempty"`
}

// OAuthAuthClaims holds namespaced account and organization assertions decoded from tokens.
type OAuthAuthClaims struct {
	ChatGPTAccountID string `json:"chatgpt_account_id"` // upstream account identifier
	ChatGPTUserID    string `json:"chatgpt_user_id"`    // upstream ChatGPT user
	ChatGPTPlanType  string `json:"chatgpt_plan_type"`  // asserted account plan
	UserID           string `json:"user_id"`            // upstream user identifier
	POID             string `json:"poid"`               // access-token account selector
	// Organizations retains the ordered claim list. GetUserInfo shares this
	// slice with its returned metadata, so callers copy it before independent mutable use.
	Organizations []OAuthOrganizationClaim `json:"organizations"`
}

// OAuthOrganizationClaim represents an organization in the ID Token.
type OAuthOrganizationClaim struct {
	ID    string `json:"id"`    // upstream organization identifier
	Role  string `json:"role"`  // asserted organization role
	Title string `json:"title"` // asserted organization title
	// IsDefault selects the first flagged organization in GetUserInfo.
	// If that entry's ID is empty, selection falls back to the first list entry's ID.
	IsDefault bool `json:"is_default"`
}

// OAuthUserInfo represents user information extracted from ID Token claims.
type OAuthUserInfo struct {
	Email            string // asserted account email
	ChatGPTAccountID string // upstream account identifier
	ChatGPTUserID    string // upstream ChatGPT user
	PlanType         string // asserted account plan
	UserID           string // upstream user identifier
	OrganizationID   string // selected organization identifier
	// Organizations shares the decoded claim slice returned by GetUserInfo.
	// Copy the slice before editing organization entries independently of those claims.
	Organizations []OAuthOrganizationClaim
}

// ChatGPTAccountInfo holds account identity and subscription data returned by
// chatgpt.com/backend-api/accounts/check.
type ChatGPTAccountInfo struct {
	PlanType string // returned account plan
	Email    string // returned account email
	// ProviderID uses a nonblank nested account.account_id string, otherwise
	// the trimmed accounts-map key. Authorization compares this source account
	// identity with token metadata before combining personal plan and expiry information.
	ProviderID            string
	SubscriptionExpiresAt string // raw entitlement expiry
}

// PATWhoamiResponse holds user and account information returned by the PAT identity query.
type PATWhoamiResponse struct {
	Email            string `json:"email"`              // returned account email
	ChatGPTUserID    string `json:"chatgpt_user_id"`    // upstream ChatGPT user
	ChatGPTAccountID string `json:"chatgpt_account_id"` // upstream account identifier
	ChatGPTPlanType  string `json:"chatgpt_plan_type"`  // returned account plan
	// ChatGPTAccountIsFedRAMP distinguishes missing metadata from an explicit
	// false account flag; JSON null and omission both leave the pointer nil.
	ChatGPTAccountIsFedRAMP *bool `json:"chatgpt_account_is_fedramp"`
}

// GetUserInfo returns newly allocated user metadata from a nonnil decoded-claims
// receiver. Email is copied independently of optional OpenAIAuth; absent
// namespaced claims leave the remaining account fields at their zero values.
//
// Organization selection stops at the first default flag, then uses the first
// list entry's ID if that selected ID is empty. Organizations shares the claim
// slice, so callers copy its entries before independent mutable use. This getter
// performs metadata extraction and leaves token verification to the owning
// authorization flow.
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

		// The first default flag owns selection even if its ID is empty.
		// Stop there so another default entry cannot silently replace that ordered claim choice.
		for _, org := range c.OpenAIAuth.Organizations {
			if org.IsDefault {
				info.OrganizationID = org.ID
				break
			}
		}
		// An empty selected ID falls back to the first list entry's ID.
		// This does not rescan later default flags or require the fallback ID to be nonempty.
		if info.OrganizationID == "" && len(c.OpenAIAuth.Organizations) > 0 {
			info.OrganizationID = c.OpenAIAuth.Organizations[0].ID
		}
	}

	return info
}
