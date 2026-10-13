package provider

import (
	"context"
	"time"
)

// Codex session import keeps administrator settings separate from normalized
// credential identity and token expiry. Parsed entries carry their source
// index into item outcomes, while request-level settings select create
// defaults or optional existing-provider updates; decoded JWT metadata
// contributes hints without authenticating the imported account.

// codexImportClockSkewSeconds permits recently elapsed token expiry during
// import validation; explicit and JWT paths apply this grace with their own
// comparison boundaries.
const codexImportClockSkewSeconds int64 = 120

// CodexImportProviders supplies provider creation and updates after import
// normalization and matching.
type CodexImportProviders interface {
	// CreateProvider persists normalized creation input and reports its record.
	// Import treats nil successful record as accepted creation with row ID zero;
	// returned errors remain item failures rather than aborting the whole batch.
	CreateProvider(context.Context, *CreateProviderInput) (*Record, error)
	// UpdateProvider persists the matched row update. Import accepts nil
	// successful data using the prior row ID, while nonnil returned records
	// update its in-memory identity index for later entries.
	UpdateProvider(context.Context, int64, *UpdateProviderInput) (*Record, error)
}

// CodexImportOptions binds time, OAuth client identity, key validation, and
// token invalidation.
type CodexImportOptions struct {
	// Now supplies import metadata and expiration decisions. Construction fills
	// nil with time.Now, while direct normalization also defines that same
	// fallback.
	Now           func() time.Time
	OAuthClientID string // renewal OAuth client identifier
	// ValidatePrivateKey is required for Agent Identity entries before import
	// succeeds. Missing validation rejects their private key rather than
	// accepting unchecked signing material.
	ValidatePrivateKey func(string) error
	// Invalidate optionally follows successful existing-provider updates. Its
	// error is discarded and does not reverse the accepted persistence outcome.
	Invalidate func(context.Context, *Record) error
}

// CodexImporter retains provider operations, archive lookup, and captured
// import dependencies.
type CodexImporter struct {
	// providers, archive, and options remain retained during import processing.
	// The operations perform independent row writes, while archive provides the
	// initial matching population and options supplies validation and clock
	// callbacks.
	providers CodexImportProviders // configured provider mutation operations
	archive   *Archive             // stored-provider matching population reader
	options   CodexImportOptions   // captured import construction settings
}

// CodexOrganizationClaim holds decoded organization hints used to select
// imported account metadata.
type CodexOrganizationClaim struct {
	ID    string `json:"id"`    // upstream organization identifier hint
	Role  string `json:"role"`  // upstream organization role hint
	Title string `json:"title"` // upstream organization title hint
	// upstream default organization flag
	IsDefault bool `json:"is_default"`
}

// CodexSessionImportRequest combines raw credential content and
// administrator-selected provider settings.
type CodexSessionImportRequest struct {
	// primary raw credential content
	// Content and Contents are processed in that order after blank inputs are
	// omitted. Parsing flattens their values into one sequential index population
	// before Import reports entry-specific results.
	Content string `json:"content"`
	// additional raw credential contents
	Contents []string `json:"contents"`
	// selected provider name base
	Name string `json:"name"`
	// optional provider administrator notes
	Notes *string `json:"notes"`
	// GroupIDs is passed to creation as supplied. Nonempty groups replace
	// existing membership through a copied update slice; empty input retains
	// existing groups.
	GroupIDs []int64 `json:"group_ids"`
	// optional selected egress proxy
	ProxyID *int64 `json:"proxy_id"`
	// Concurrency and Priority use supplied pointers for updates. Nil retains
	// existing values there, while creation chooses three and fifty respectively.
	// optional provider request capacity
	Concurrency *int `json:"concurrency"`
	// optional scheduler priority update
	Priority *int `json:"priority"`
	// optional provider billing multiplier
	RateMultiplier *float64 `json:"rate_multiplier"`
	// optional scheduler capacity override
	LoadFactor *int   `json:"load_factor"`
	ExpiresAt  *int64 `json:"expires_at"` // Unix seconds
	// optional provider expiry pause
	AutoPauseOnExpired *bool `json:"auto_pause_on_expired"`
	// CredentialExtras supplies administrator metadata after normalized token
	// fields. SanitizeCodexImportCredentialExtras removes reserved identity and
	// token keys before Import merges supported values.
	CredentialExtras map[string]any `json:"credential_extras"`
	// Extra supplies initial metadata for creation and updates. Normalized
	// entry metadata wins on colliding keys, and supported containers are copied
	// before provider operations consume them.
	Extra map[string]any `json:"extra"`
	// UpdateExisting defaults to true. False skips matching-row updates but
	// still permits creation for that normalized entry through the import flow.
	UpdateExisting *bool `json:"update_existing"`
}

// CodexSessionImportResult counts item outcomes and preserves their warnings
// and failure diagnostics.
type CodexSessionImportResult struct {
	// Total counts supplied entries; Created, Updated, Skipped, and Failed
	// partition outcomes when Import completes. An initial population-read error
	// returns Total with no processed item outcomes, while Warnings can accompany
	// any later action.
	Total int `json:"total"` // input entry count
	// accepted provider creation count
	Created int `json:"created"`
	// accepted existing-row update count
	Updated int `json:"updated"`
	// same-batch duplicate entry count
	Skipped int `json:"skipped"`
	// failed import entry count
	Failed int `json:"failed"`
	// per-entry action result list
	Items []CodexSessionImportItem `json:"items,omitempty"`
	// per-entry import warning messages
	Warnings []CodexSessionImportMessage `json:"warnings,omitempty"`
	// per-entry import failure messages
	Errors []CodexSessionImportMessage `json:"errors,omitempty"`
}

// CodexSessionImportItem identifies one processed entry and its resulting
// provider action.
type CodexSessionImportItem struct {
	Index int `json:"index"` // source entry index
	// selected provider display name
	Name string `json:"name,omitempty"`
	// created, updated, skipped, or failed
	Action string `json:"action"`
	// resulting stored row identifier
	ProviderID int64 `json:"provider_id,omitempty"`
	// item action diagnostic text
	Message string `json:"message,omitempty"`
}

// CodexSessionImportMessage associates an import diagnostic with its source
// entry and name.
type CodexSessionImportMessage struct {
	Index   int    `json:"index"`          // source entry index
	Name    string `json:"name,omitempty"` // selected provider display name
	Message string `json:"message"`        // entry diagnostic message text
}

// CodexImportEntry carries one flattened source value and its caller-visible
// index.
type CodexImportEntry struct {
	Index int // caller-visible source entry index
	Value any // raw token or decoded value
}

// CodexImportProvider combines normalized credentials, identity hints, and
// entry-specific import diagnostics.
type CodexImportProvider struct {
	Name            string // selected provider display name
	AccessToken     string `json:"-"` // normalized OAuth access token
	RefreshToken    string `json:"-"` // optional OAuth renewal credential
	IDToken         string `json:"-"` // optional metadata identity token
	Email           string // selected account email hint
	ProviderID      string // upstream ChatGPT account identifier
	UserID          string // upstream ChatGPT user identifier
	PlanType        string // selected account plan hint
	Organization    string // selected organization identifier hint
	AgentRuntimeID  string // registered signing runtime identifier
	AgentPrivateKey string `json:"-"` // validator-accepted runtime signing material
	AgentTaskID     string // optional signed task identifier
	AgentFedRAMP    bool   // upstream FedRAMP account flag
	IsAgentIdentity bool   // selected signing credential mode
	// normalized credential value map
	Credentials    map[string]any `json:"-"`
	Extra          map[string]any // entry import display metadata
	TokenExpiresAt *time.Time     // observed token expiry time
	IdentityKeys   []string       // ordered record matching keys
	WarningTexts   []string       // entry normalization warning messages
}

// CodexJWTClaims holds unauthenticated decoded token payload hints for import
// metadata.
type CodexJWTClaims struct {
	// decoded subject identity hint
	Sub string `json:"sub"`
	// decoded email identity hint
	Email string `json:"email"`
	Exp   int64  `json:"exp"` // Unix seconds
	Iat   int64  `json:"iat"` // Unix seconds
	// optional ChatGPT claim hints
	OpenAIAuth *CodexJWTOpenAIClaims `json:"https://api.openai.com/auth,omitempty"`
}

// CodexJWTOpenAIClaims carries optional ChatGPT and organization identities
// from decoded JWT metadata.
type CodexJWTOpenAIClaims struct {
	// decoded ChatGPT account identity
	ChatGPTAccountID string `json:"chatgpt_account_id"`
	// decoded ChatGPT user identity
	ChatGPTUserID string `json:"chatgpt_user_id"`
	// decoded ChatGPT plan hint
	ChatGPTPlanType string `json:"chatgpt_plan_type"`
	// alternate decoded user identity
	UserID string `json:"user_id"`
	// decoded organization identity hint
	POID string `json:"poid"`
	// decoded organization hint list
	Organizations []CodexOrganizationClaim `json:"organizations"`
}

// NewCodexImporter retains provider/archive dependencies and copies options,
// defaulting Now to time.Now. Configure reached key validator and optional
// invalidator before imports; retain those services and keep request values
// stable through processing.
func NewCodexImporter(providers CodexImportProviders, archive *Archive, options CodexImportOptions) *CodexImporter {
	if options.Now == nil {
		options.Now = time.Now
	}
	return &CodexImporter{providers, archive, options}
}
