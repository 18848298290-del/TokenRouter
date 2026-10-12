package anthropic

// Claude authorization exchanges return credentials together with optional
// account and organization identities. Provider authorization converts the
// relative token lifetime into an expiry time and copies the identity objects
// when they are present in the upstream JSON response.

// OAuthTokenResponse holds credentials and identities returned by Claude authorization.
type OAuthTokenResponse struct {
	AccessToken  string `json:"access_token"`            // inference access credential
	TokenType    string `json:"token_type"`              // returned authorization scheme
	ExpiresIn    int64  `json:"expires_in"`              // seconds
	RefreshToken string `json:"refresh_token,omitempty"` // optional refresh credential
	Scope        string `json:"scope,omitempty"`         // granted OAuth scopes
	// Organization is optional; ClaudeAuthorization copies its UUID into token
	// information when the response supplies a nonempty organization identifier.
	Organization *OAuthOrgInfo `json:"organization,omitempty"`
	// Provider decodes the upstream account object; ClaudeAuthorization copies
	// its account UUID and email when this optional object is present.
	Provider *OAuthProviderInfo `json:"account,omitempty"`
}

// OAuthOrgInfo identifies the organization returned by Claude's token endpoint.
type OAuthOrgInfo struct {
	UUID string `json:"uuid"` // upstream organization identifier
}

// OAuthProviderInfo identifies the upstream account returned by Claude authorization.
type OAuthProviderInfo struct {
	UUID         string `json:"uuid"`          // upstream account identifier
	EmailAddress string `json:"email_address"` // upstream account email
}
