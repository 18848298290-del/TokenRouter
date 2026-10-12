package google

// Vertex service-account parsing requires project, email, and private-key text
// before token exchange. That parser overwrites the supplied token URI with
// Google's fixed endpoint, while the lower-level exchange reads the resulting
// URI as both assertion audience and token request destination.

// ServiceAccountKey holds project identity and signing material for Google token exchange.
type ServiceAccountKey struct {
	Type      string `json:"type"`       // credential format label
	ProjectID string `json:"project_id"` // credential project identifier
	// PrivateKeyID supplies an optional JWT kid. ExchangeServiceAccountToken
	// includes its supplied value when trimming establishes that it is nonblank.
	PrivateKeyID string `json:"private_key_id"`
	PrivateKey   string `json:"private_key"`  // PEM signing key
	ClientEmail  string `json:"client_email"` // assertion issuer email
	// TokenURI supplies the assertion audience and exchange URL to googleauth.
	// ParseVertexServiceAccountJSON replaces the credential's value with its fixed
	// Google endpoint before returning a key for the Vertex execution path.
	TokenURI string `json:"token_uri"`
}

// ServiceAccountTokenResponse holds token lifetime or diagnostic fields from Google exchange.
type ServiceAccountTokenResponse struct {
	AccessToken string `json:"access_token"` // exchanged access credential
	TokenType   string `json:"token_type"`   // returned authorization scheme
	// ExpiresIn supplies the returned lifetime in seconds. googleauth converts
	// it to a duration and substitutes one hour when that duration is nonpositive,
	// then subtracts its cache skew when the selected lifetime exceeds the skew.
	ExpiresIn int64 `json:"expires_in"`
	// Error and ErrorDesc carry exchange diagnostics. For non-success HTTP
	// status, googleauth prefers trimmed ErrorDesc, then trimmed Error, then
	// trimmed response text when neither field supplies a description.
	Error     string `json:"error"`             // upstream error label
	ErrorDesc string `json:"error_description"` // upstream error description
}
