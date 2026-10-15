package provider

// admin_inputs.go defines creation, edit, and bulk-update inputs and the
// per-provider outcomes returned by administration.
// Provider administration distinguishes omitted settings from requested zero or
// empty values. Creation inputs select defaults, single edits describe field
// replacement or internal patches, and bulk inputs carry shared updates plus
// per-provider outcomes so HTTP callers can report partial membership failures.

// CreateProviderInput carries configuration submitted for creation of a
// provider record.
type CreateProviderInput struct {
	Name string // administrator provider display name
	// Notes supplies the optional administrator note. Creation trims its text and
	// stores nil when that text becomes empty.
	Notes    *string
	Platform string // upstream platform identifier
	Type     string // provider authentication type
	// Credentials supplies platform authentication and protocol settings. Creation
	// can normalize or replace this map and may retain nested input values.
	Credentials map[string]any
	// Extra supplies provider configuration. CreateProvider copies its outer map
	// before normalization; nested values can remain shared with this input.
	Extra map[string]any
	// ProxyID selects the upstream proxy. Nil selects direct operation; creation
	// retains the supplied pointer in the constructed record.
	ProxyID *int64
	// Concurrency requests provider slots. Grok OAuth creation replaces
	// nonpositive values with one; other platform/type combinations retain the
	// supplied value.
	Concurrency int
	Priority    int // global provider scheduling priority
	// RateMultiplier selects the provider cost multiplier. Nil leaves the storage
	// default, zero is accepted, and negative values fail creation.
	RateMultiplier *float64
	// LoadFactor overrides the load denominator. Creation leaves nonpositive
	// values unset and rejects positive values above ten thousand.
	LoadFactor *int
	// GroupIDs requests memberships bound after provider creation. Empty input
	// creates an ungrouped provider; the slice is read during binding.
	GroupIDs []int64
	// ExpiresAt supplies Unix seconds for expiration. Nil and nonpositive values
	// leave expiration unset; positive values become a new time pointer.
	ExpiresAt *int64
	// AutoPauseOnExpired controls expiration-triggered pausing. Nil selects true
	// during creation; a supplied boolean selects its own value.
	AutoPauseOnExpired *bool
}

// ShadowOptions selects independent scheduling and membership settings for a
// credential shadow.
type ShadowOptions struct {
	// Name selects the shadow display label after trimming. Empty text uses the
	// parent's name followed by the Spark suffix.
	Name string
	// Priority selects the shadow's global scheduling priority. Nonpositive input
	// inherits the parent's current priority during shadow construction.
	Priority int
	// Concurrency selects the shadow's provider slots. Nonpositive input inherits
	// the parent's current concurrency during shadow construction.
	Concurrency int
	// GroupIDs selects shadow memberships. Empty input inherits parent
	// memberships; supplied groups are validated when the optional group reader is
	// configured.
	GroupIDs []int64
}

// UpdateProviderInput selects configuration fields for a single provider edit.
type UpdateProviderInput struct {
	// PatchCredentials prepares a merged credential validation view. A nonnil
	// Credentials map also supplies raw key-merge intent to the locked writer;
	// nil supplies no raw patch. HTTP JSON cannot set this flag.
	PatchCredentials bool `json:"-"` // internal credential merge selection
	// PatchExtra prepares a merged Extra validation view. A nonnil Extra map also
	// supplies raw key-merge intent to the locked writer; nil supplies no raw
	// patch. HTTP JSON cannot set this flag.
	PatchExtra bool `json:"-"` // internal Extra merge selection
	// ExpectedCredentials requires the refresh identity to match during validation
	// and persistence. Internal callers supply it; HTTP JSON cannot set this value.
	ExpectedCredentials *CredentialVersion `json:"-"` // internal refresh identity requirement
	// Name replaces the display name when nonempty. An empty string leaves the
	// stored name selected by the configuration writer.
	Name string
	// Notes replaces the administrator note when supplied. Trimmed empty text
	// clears the note; nil leaves the existing note unchanged.
	Notes *string
	// Type selects authentication conversion when nonempty. Shadows retain their
	// saved type, and parents with shadows reject a different non-OAuth type.
	Type string
	// Credentials selects replacement settings or an internal key patch. Ordinary
	// empty input skips replacement; an empty shadow map clears model mapping
	// when PatchCredentials is false.
	Credentials map[string]any
	// Extra selects configuration replacement or a key patch. Without PatchExtra,
	// nil skips replacement, an empty map clears configuration, and
	// deprecated-only input is ignored. Configuration-aware storage retains
	// dedicated usage and maintenance fields.
	Extra map[string]any
	// ProxyID selects a parent proxy change. Zero clears it, nil leaves it
	// unchanged, and ordinary shadow edits retain the inherited proxy.
	ProxyID *int64
	// Concurrency selects a slot change when nonnil. Zero is a supplied value,
	// with Grok OAuth nonpositive normalization still applied.
	Concurrency *int
	// Priority selects a scheduling-priority change when nonnil. A pointer to zero
	// writes zero; nil leaves the stored priority unchanged.
	Priority *int
	// RateMultiplier selects a cost multiplier when supplied. Zero is accepted,
	// negative values fail, and nil leaves the stored multiplier unchanged.
	RateMultiplier *float64
	// LoadFactor selects a denominator change when supplied. Nonpositive values
	// clear the override, and values above ten thousand are rejected.
	LoadFactor *int
	// Status selects a persisted status when nonempty. An empty string leaves the
	// configuration writer's latest stored status unchanged.
	Status string
	// GroupIDs selects replacement memberships after configuration is saved. Nil
	// retains memberships; a pointer to an empty slice removes every binding.
	GroupIDs *[]int64
	// ExpiresAt supplies Unix seconds when nonnil. Nonpositive values clear
	// expiration, and a positive value selects a new expiration time.
	ExpiresAt *int64
	// AutoPauseOnExpired selects the expiration pausing switch when nonnil. A
	// pointer to false disables the switch; nil retains it.
	AutoPauseOnExpired *bool
}

// BulkUpdateProvidersInput selects provider targets and shared configuration
// changes for a batch.
type BulkUpdateProvidersInput struct {
	// ProviderIDs supplies explicit batch targets. Resolving Filters replaces an
	// empty slice before later validation, and outcomes follow the resulting ID
	// sequence.
	ProviderIDs []int64
	// Filters selects the matching provider list when ProviderIDs is empty.
	// Explicit IDs take precedence, and a nil filter leaves empty targets.
	Filters *BulkUpdateProviderFilters
	// Name supplies the shared display-name change when nonempty. An empty string
	// omits the column from the bulk storage patch.
	Name string
	// ProxyID selects a shared proxy change when supplied. Zero clears proxies;
	// including a shadow rejects the batch before storage is called.
	ProxyID *int64
	// Concurrency selects a shared slot change when supplied. Bulk storage
	// receives zero directly; this path does not apply Grok's creation default.
	Concurrency *int
	// Priority selects a shared priority change when supplied. Zero is passed to
	// storage, and nil omits the priority column.
	Priority *int
	// RateMultiplier selects a shared cost multiplier when supplied. Negative
	// values fail validation; zero is passed to storage as a valid value.
	RateMultiplier *float64
	// LoadFactor selects a positive denominator through this administrative path.
	// Nonpositive values produce a nil patch and retain the stored override.
	LoadFactor *int
	// Status selects a shared persisted status when nonempty. Empty input omits
	// that column from the bulk configuration write.
	Status string
	// Schedulable selects a shared scheduling switch when nonnil. False is a
	// supplied change; nil omits the scheduling column.
	Schedulable *bool
	// GroupIDs selects replacement memberships for each target after configuration
	// is saved. Nil skips bindings; an empty selection clears them.
	GroupIDs *[]int64
	// Credentials supplies keys merged into each target's latest stored
	// credentials. Normalization can modify or replace this map before target
	// validation finishes; shadows and unsupported protocol combinations fail
	// before the shared write.
	Credentials map[string]any
	// Extra supplies keys merged into each target's latest stored settings.
	// Managed snapshots and deprecated keys are removed before preparing the
	// storage patch.
	Extra map[string]any
}

// BulkUpdateProviderFilters identifies the provider list used when explicit IDs
// are absent.
type BulkUpdateProviderFilters struct {
	Platform string // upstream platform filter
	Type     string // provider authentication filter
	Status   string // persisted provider status filter
	// Group is empty for every provider or ungrouped for providers without
	// memberships. Other values are trimmed and parsed as decimal group IDs.
	Group       string
	Search      string // administrator provider text search
	PrivacyMode string // stored privacy mode filter
}

// BulkUpdateProviderResult records the membership outcome for one requested
// provider.
type BulkUpdateProviderResult struct {
	ProviderID int64  `json:"provider_id"`     // requested local provider identifier
	Success    bool   `json:"success"`         // membership stage successful outcome
	Error      string `json:"error,omitempty"` // failed membership error text
}

// BulkUpdateProvidersResult summarizes ordered outcomes after the shared
// configuration write.
type BulkUpdateProvidersResult struct {
	// Success and Failed count Results by outcome, while SuccessIDs and FailedIDs
	// retain each outcome's provider identifiers. Results follows the requested
	// target order.
	Success    int                        `json:"success"`     // successful target outcome count
	Failed     int                        `json:"failed"`      // failed target outcome count
	SuccessIDs []int64                    `json:"success_ids"` // successful target provider identifiers
	FailedIDs  []int64                    `json:"failed_ids"`  // failed target provider identifiers
	Results    []BulkUpdateProviderResult `json:"results"`     // input-ordered individual target outcomes
}

// ProviderBulkUpdate carries selected scalar changes and JSON patches into bulk
// storage.
type ProviderBulkUpdate struct {
	// ProtocolUpdates contains each provider's validated protocol keys. Bulk
	// SQL merges these values after the common credential patch within the same
	// statement.
	ProtocolUpdates map[int64]map[string]any // validated provider protocol patches

	// Name, ProxyID, Concurrency, Priority, RateMultiplier, LoadFactor, Status,
	// and Schedulable are scalar selections: nil omits each column. Credentials
	// and Extra merge supplied keys instead of replacing whole stored objects.
	Name *string // selected provider display name
	// ProxyID changes the stored proxy when nonnil. Zero clears the association;
	// nil leaves the existing proxy selection unchanged.
	ProxyID        *int64
	Concurrency    *int     // selected provider request slots
	Priority       *int     // selected global scheduling priority
	RateMultiplier *float64 // selected provider cost multiplier
	// LoadFactor changes the storage denominator when nonnil. Storage clears
	// nonpositive values, although Admin's bulk path omits that request.
	LoadFactor  *int
	Status      *string        // selected persisted provider status
	Schedulable *bool          // selected provider scheduling switch
	Credentials map[string]any // shared credential key updates
	Extra       map[string]any // shared configuration key updates
	// EnsureCodexFingerprintSeed requests stored-seed retention or generation
	// during the SQL merge when the Extra patch enables fingerprint
	// convergence.
	EnsureCodexFingerprintSeed bool // stored fingerprint seed preparation
}
