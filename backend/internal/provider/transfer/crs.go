package transfer

import "github.com/TokenFlux/TokenRouter/internal/egress"

// CRS exports retain upstream account collection names even though
// TokenRouter stores providers. Each collection selects the destination
// platform and authentication type in CRSSync, and string source IDs become
// synchronization metadata used to find existing root providers.
//
// The response shapes retain plaintext credential and Extra maps for the
// importing service to inspect and normalize. Claude's authType chooses OAuth
// or setup-token, while other collections establish their types directly.
// Source activation and status jointly determine destination status, selected
// proxy data can be matched or created, and source priorities are bounded
// before provider configuration is written.

// CRSExportResponse holds the upstream export outcome and its six provider
// collections for synchronization.
type CRSExportResponse struct {
	// Success controls CRSClient export acceptance. On false, Message supplies
	// the preferred failure text, then Error, then the client's unknown-error message
	// when both text fields are empty.
	Success bool   `json:"success"` // upstream export success flag
	Error   string `json:"error"`   // alternate export failure message
	Message string `json:"message"` // preferred export failure message
	// Data holds decoded source collections for CRSSync; nil and
	// empty collections both contribute no source entries, and their legacy JSON
	// names select the destination platform and authentication type.
	Data struct {
		// ExportedAt: source export timestamp text
		ExportedAt string `json:"exportedAt"`
		// ClaudeProviders: Anthropic OAuth/setup-token source entries
		ClaudeProviders []CRSClaudeProvider `json:"claudeAccounts"`
		// ClaudeConsoleProviders: Anthropic API Key source entries
		ClaudeConsoleProviders []CRSConsoleProvider `json:"claudeConsoleAccounts"`
		// OpenAIOAuthProviders: OpenAI OAuth source entries
		OpenAIOAuthProviders []CRSOpenAIOAuthProvider `json:"openaiOAuthAccounts"`
		// OpenAIResponsesProviders: OpenAI API Key source entries
		OpenAIResponsesProviders []CRSOpenAIResponsesProvider `json:"openaiResponsesAccounts"`
		// GeminiOAuthProviders: Gemini OAuth source entries
		GeminiOAuthProviders []CRSGeminiOAuthProvider `json:"geminiOAuthAccounts"`
		// GeminiAPIKeyProviders: Gemini API Key source entries
		GeminiAPIKeyProviders []CRSGeminiAPIKeyProvider `json:"geminiApiKeyAccounts"`
	} `json:"data"` // exported provider source data
}

// CRSProxy uses the egress import representation and its JSON field names.
type CRSProxy = egress.CRSProxySpec

// CRSClaudeProvider holds one CRS Anthropic OAuth or setup-token entry for
// provider synchronization.
type CRSClaudeProvider struct {
	Kind string `json:"kind"` // source provider category name
	// ID remains a CRS string identity and becomes crs_account_id for root
	// lookup; TokenRouter assigns its own numeric ID when creating a provider.
	ID string `json:"id"` // source string provider identifier
	// Name is trimmed by CRSDefaultName; blank source names become CRS followed
	// by the source ID, keeping the imported entry identifiable in management.
	Name string `json:"name"` // source display name text
	// Description retains the source annotation for decoding. CRSSync leaves it
	// unused: creation supplies no Notes value, and updates retain the
	// destination's stored notes.
	Description string `json:"description"` // source provider note text
	// Platform retains the upstream hint for decoding. CRSSync chooses the
	// destination platform from this collection and ignores the hint when
	// constructing provider configuration.
	Platform string `json:"platform"` // source platform hint text
	// AuthType is trimmed and defaults to oauth when blank. CRSSync accepts
	// oauth or setup-token for this collection and skips unsupported source
	// authentication types.
	AuthType string `json:"authType"` // source authentication hint text
	// IsActive and Status determine the imported status: false activation yields
	// inactive; otherwise trimmed Status equal to error ignoring case yields
	// error, and other values yield active. Schedulable supplies a separate
	// configuration switch.
	IsActive    bool `json:"isActive"`    // source activation flag value
	Schedulable bool `json:"schedulable"` // source scheduling switch value
	// Priority is accepted from one through one hundred by CRSClampPriority.
	// Values outside that range, including the omitted zero value, become fifty.
	Priority int    `json:"priority"` // source global priority value
	Status   string `json:"status"`   // source stored status text
	// Proxy contributes a new selection when SyncProxies is enabled and egress
	// can match or create it. A nil result selects no proxy for creation;
	// updates retain the existing proxy.
	Proxy *CRSProxy `json:"proxy"` // optional source proxy specification
	// Credentials and Extra hold mutable decoded objects.
	// CRSSanitizeCredentialsMap drops nil credential values and copies supported
	// containers; CRSSync merges Extra through shallow temporary maps before
	// cloning supported containers into the destination record.
	Credentials map[string]any `json:"credentials"` // plaintext source credential object
	Extra       map[string]any `json:"extra"`       // source extension configuration object
}

// CRSConsoleProvider holds one CRS Anthropic API Key entry for provider
// synchronization.
type CRSConsoleProvider struct {
	Kind string `json:"kind"` // source provider category name
	// ID remains a CRS string identity and becomes crs_account_id for root
	// lookup; TokenRouter assigns its own numeric ID when creating a provider.
	ID string `json:"id"` // source string provider identifier
	// Name is trimmed by CRSDefaultName; blank source names become CRS followed
	// by the source ID, keeping the imported entry identifiable in management.
	Name string `json:"name"` // source display name text
	// Description retains the source annotation for decoding. CRSSync leaves it
	// unused: creation supplies no Notes value, and updates retain the
	// destination's stored notes.
	Description string `json:"description"` // source provider note text
	// Platform retains the upstream hint for decoding. CRSSync chooses the
	// destination platform from this collection and ignores the hint when
	// constructing provider configuration.
	Platform string `json:"platform"` // source platform hint text
	// IsActive and Status determine the imported status: false activation yields
	// inactive; otherwise trimmed Status equal to error ignoring case yields
	// error, and other values yield active. Schedulable supplies a separate
	// configuration switch.
	IsActive    bool `json:"isActive"`    // source activation flag value
	Schedulable bool `json:"schedulable"` // source scheduling switch value
	// Priority is accepted from one through one hundred by CRSClampPriority.
	// Values outside that range, including the omitted zero value, become fifty.
	Priority int    `json:"priority"` // source global priority value
	Status   string `json:"status"`   // source stored status text
	// MaxConcurrentTasks supplies destination concurrency when positive. Zero or
	// negative values use the console importer's default capacity of three
	// simultaneous requests.
	// MaxConcurrentTasks: source simultaneous task capacity
	MaxConcurrentTasks int `json:"maxConcurrentTasks"`
	// Proxy contributes a new selection when SyncProxies is enabled and egress
	// can match or create it. A nil result selects no proxy for creation;
	// updates retain the existing proxy.
	Proxy *CRSProxy `json:"proxy"` // optional source proxy specification
	// Credentials retains decoded plaintext secrets for CRSSync, which validates
	// the required API key and sanitizes the supplied map before writing
	// provider configuration.
	Credentials map[string]any `json:"credentials"` // plaintext source credential object
}

// CRSOpenAIResponsesProvider holds one CRS OpenAI API Key entry for provider
// synchronization.
type CRSOpenAIResponsesProvider struct {
	Kind string `json:"kind"` // source provider category name
	// ID remains a CRS string identity and becomes crs_account_id for root
	// lookup; TokenRouter assigns its own numeric ID when creating a provider.
	ID string `json:"id"` // source string provider identifier
	// Name is trimmed by CRSDefaultName; blank source names become CRS followed
	// by the source ID, keeping the imported entry identifiable in management.
	Name string `json:"name"` // source display name text
	// Description retains the source annotation for decoding. CRSSync leaves it
	// unused: creation supplies no Notes value, and updates retain the
	// destination's stored notes.
	Description string `json:"description"` // source provider note text
	// Platform retains the upstream hint for decoding. CRSSync chooses the
	// destination platform from this collection and ignores the hint when
	// constructing provider configuration.
	Platform string `json:"platform"` // source platform hint text
	// IsActive and Status determine the imported status: false activation yields
	// inactive; otherwise trimmed Status equal to error ignoring case yields
	// error, and other values yield active. Schedulable supplies a separate
	// configuration switch.
	IsActive    bool `json:"isActive"`    // source activation flag value
	Schedulable bool `json:"schedulable"` // source scheduling switch value
	// Priority is accepted from one through one hundred by CRSClampPriority.
	// Values outside that range, including the omitted zero value, become fifty.
	Priority int    `json:"priority"` // source global priority value
	Status   string `json:"status"`   // source stored status text
	// Proxy contributes a new selection when SyncProxies is enabled and egress
	// can match or create it. A nil result selects no proxy for creation;
	// updates retain the existing proxy.
	Proxy *CRSProxy `json:"proxy"` // optional source proxy specification
	// Credentials and Extra hold mutable decoded objects.
	// CRSSanitizeCredentialsMap drops nil credential values and copies supported
	// containers; CRSSync merges Extra through shallow temporary maps before
	// cloning supported containers into the destination record.
	Credentials map[string]any `json:"credentials"` // plaintext source credential object
	Extra       map[string]any `json:"extra"`       // source extension configuration object
}

// CRSOpenAIOAuthProvider holds one CRS OpenAI OAuth entry for provider
// synchronization.
type CRSOpenAIOAuthProvider struct {
	Kind string `json:"kind"` // source provider category name
	// ID remains a CRS string identity and becomes crs_account_id for root
	// lookup; TokenRouter assigns its own numeric ID when creating a provider.
	ID string `json:"id"` // source string provider identifier
	// Name is trimmed by CRSDefaultName; blank source names become CRS followed
	// by the source ID, keeping the imported entry identifiable in management.
	Name string `json:"name"` // source display name text
	// Description retains the source annotation for decoding. CRSSync leaves it
	// unused: creation supplies no Notes value, and updates retain the
	// destination's stored notes.
	Description string `json:"description"` // source provider note text
	// Platform retains the upstream hint for decoding. CRSSync chooses the
	// destination platform from this collection and ignores the hint when
	// constructing provider configuration.
	Platform string `json:"platform"` // source platform hint text
	// AuthType retains the source hint for decoding. CRSSync establishes OAuth
	// from this collection and ignores that hint when building the destination
	// provider.
	AuthType string `json:"authType"` // source authentication hint text
	// IsActive and Status determine the imported status: false activation yields
	// inactive; otherwise trimmed Status equal to error ignoring case yields
	// error, and other values yield active. Schedulable supplies a separate
	// configuration switch.
	IsActive    bool `json:"isActive"`    // source activation flag value
	Schedulable bool `json:"schedulable"` // source scheduling switch value
	// Priority is accepted from one through one hundred by CRSClampPriority.
	// Values outside that range, including the omitted zero value, become fifty.
	Priority int    `json:"priority"` // source global priority value
	Status   string `json:"status"`   // source stored status text
	// Proxy contributes a new selection when SyncProxies is enabled and egress
	// can match or create it. A nil result selects no proxy for creation;
	// updates retain the existing proxy.
	Proxy *CRSProxy `json:"proxy"` // optional source proxy specification
	// Credentials and Extra hold mutable decoded objects.
	// CRSSanitizeCredentialsMap drops nil credential values and copies supported
	// containers; CRSSync merges Extra through shallow temporary maps before
	// cloning supported containers into the destination record.
	Credentials map[string]any `json:"credentials"` // plaintext source credential object
	Extra       map[string]any `json:"extra"`       // source extension configuration object
}

// CRSGeminiOAuthProvider holds one CRS Gemini OAuth entry for provider
// synchronization.
type CRSGeminiOAuthProvider struct {
	Kind string `json:"kind"` // source provider category name
	// ID remains a CRS string identity and becomes crs_account_id for root
	// lookup; TokenRouter assigns its own numeric ID when creating a provider.
	ID string `json:"id"` // source string provider identifier
	// Name is trimmed by CRSDefaultName; blank source names become CRS followed
	// by the source ID, keeping the imported entry identifiable in management.
	Name string `json:"name"` // source display name text
	// Description retains the source annotation for decoding. CRSSync leaves it
	// unused: creation supplies no Notes value, and updates retain the
	// destination's stored notes.
	Description string `json:"description"` // source provider note text
	// Platform retains the upstream hint for decoding. CRSSync chooses the
	// destination platform from this collection and ignores the hint when
	// constructing provider configuration.
	Platform string `json:"platform"` // source platform hint text
	// AuthType retains the source hint for decoding. CRSSync establishes OAuth
	// from this collection and ignores that hint when building the destination
	// provider.
	AuthType string `json:"authType"` // source authentication hint text
	// IsActive and Status determine the imported status: false activation yields
	// inactive; otherwise trimmed Status equal to error ignoring case yields
	// error, and other values yield active. Schedulable supplies a separate
	// configuration switch.
	IsActive    bool `json:"isActive"`    // source activation flag value
	Schedulable bool `json:"schedulable"` // source scheduling switch value
	// Priority is accepted from one through one hundred by CRSClampPriority.
	// Values outside that range, including the omitted zero value, become fifty.
	Priority int    `json:"priority"` // source global priority value
	Status   string `json:"status"`   // source stored status text
	// Proxy contributes a new selection when SyncProxies is enabled and egress
	// can match or create it. A nil result selects no proxy for creation;
	// updates retain the existing proxy.
	Proxy *CRSProxy `json:"proxy"` // optional source proxy specification
	// Credentials and Extra hold mutable decoded objects.
	// CRSSanitizeCredentialsMap drops nil credential values and copies supported
	// containers; CRSSync merges Extra through shallow temporary maps before
	// cloning supported containers into the destination record.
	Credentials map[string]any `json:"credentials"` // plaintext source credential object
	Extra       map[string]any `json:"extra"`       // source extension configuration object
}

// CRSGeminiAPIKeyProvider holds one CRS Gemini API Key entry for provider
// synchronization.
type CRSGeminiAPIKeyProvider struct {
	Kind string `json:"kind"` // source provider category name
	// ID remains a CRS string identity and becomes crs_account_id for root
	// lookup; TokenRouter assigns its own numeric ID when creating a provider.
	ID string `json:"id"` // source string provider identifier
	// Name is trimmed by CRSDefaultName; blank source names become CRS followed
	// by the source ID, keeping the imported entry identifiable in management.
	Name string `json:"name"` // source display name text
	// Description retains the source annotation for decoding. CRSSync leaves it
	// unused: creation supplies no Notes value, and updates retain the
	// destination's stored notes.
	Description string `json:"description"` // source provider note text
	// Platform retains the upstream hint for decoding. CRSSync chooses the
	// destination platform from this collection and ignores the hint when
	// constructing provider configuration.
	Platform string `json:"platform"` // source platform hint text
	// IsActive and Status determine the imported status: false activation yields
	// inactive; otherwise trimmed Status equal to error ignoring case yields
	// error, and other values yield active. Schedulable supplies a separate
	// configuration switch.
	IsActive    bool `json:"isActive"`    // source activation flag value
	Schedulable bool `json:"schedulable"` // source scheduling switch value
	// Priority is accepted from one through one hundred by CRSClampPriority.
	// Values outside that range, including the omitted zero value, become fifty.
	Priority int    `json:"priority"` // source global priority value
	Status   string `json:"status"`   // source stored status text
	// Proxy contributes a new selection when SyncProxies is enabled and egress
	// can match or create it. A nil result selects no proxy for creation;
	// updates retain the existing proxy.
	Proxy *CRSProxy `json:"proxy"` // optional source proxy specification
	// Credentials and Extra hold mutable decoded objects.
	// CRSSanitizeCredentialsMap drops nil credential values and copies supported
	// containers; CRSSync merges Extra through shallow temporary maps before
	// cloning supported containers into the destination record.
	Credentials map[string]any `json:"credentials"` // plaintext source credential object
	Extra       map[string]any `json:"extra"`       // source extension configuration object
}
