package grok

// Grok authorization can exchange browser OAuth credentials directly or convert
// a temporary web SSO token into Build OAuth tokens. The wire values keep that
// temporary login input separate from the access/refresh credentials returned
// for provider storage, and manual callback parsing records its source form.

// TokenResponse holds access, refresh, and identity values returned by xAI OAuth.
type TokenResponse struct {
	AccessToken  string `json:"access_token"`            // upstream access credential
	RefreshToken string `json:"refresh_token,omitempty"` // optional refresh credential
	IDToken      string `json:"id_token,omitempty"`      // optional identity token
	TokenType    string `json:"token_type,omitempty"`    // returned authorization scheme
	ExpiresIn    int64  `json:"expires_in,omitempty"`    // seconds
	Scope        string `json:"scope,omitempty"`         // granted OAuth scopes
}

// PasswordLoginResult holds account email and temporary credentials for SSO conversion.
type PasswordLoginResult struct {
	Email string `json:"email,omitempty"` // login account email
	// SSOToken supplies temporary password-login input to ConvertSSOToBuild.
	// GrokAuthorization returns exchanged OAuth credentials to its caller for
	// storage; its returned token information omits this SSO value.
	SSOToken string `json:"sso_token"`
}

// AuthorizationInput records parsed callback values and their source-form indicator.
type AuthorizationInput struct {
	Code  string // parsed authorization code
	State string // parsed callback state
	// RequiresState marks code extracted from a callback URL or query string
	// by ParseAuthorizationInput. Bare code leaves it false; setting the flag
	// does not itself validate or require a supplied state value.
	RequiresState bool
}
