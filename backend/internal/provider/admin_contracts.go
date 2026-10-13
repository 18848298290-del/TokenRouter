package provider

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
)

// Provider administration borrows application-owned storage and callbacks;
// NewAdmin copies the options value while retaining its references. Each
// operation uses its configured dependencies, with ConfigurationWriter providing
// locked field merging and plain Update adapters receiving prospective records.

// AdminStore supplies the record, configuration, membership, and health
// operations used by Admin.
type AdminStore interface {
	// UpdateExtra merges supplied Extra keys into the provider's current stored
	// settings.
	UpdateExtra(context.Context, int64, map[string]any) error
	// BulkUpdate applies selected scalar columns and merges JSON keys for the
	// requested IDs. Nil scalar pointers leave those columns unchanged, and
	// ProtocolUpdates supplies each provider's validated protocol settings.
	BulkUpdate(context.Context, []int64, ProviderBulkUpdate) (int64, error)
	// RevertProxyFallback restores a provider's saved original proxy selection and
	// clears its fallback origin. A provider lacking that saved origin returns
	// ErrProviderNotInFallback, allowing administration to report the unavailable
	// restoration.
	RevertProxyFallback(context.Context, int64) error
	// Update saves supplied provider configuration through the store's record
	// update path.
	Update(context.Context, *Record) error
	// FindByExtraField finds records matching one stored Extra value for duplicate
	// recovery.
	FindByExtraField(context.Context, string, any) ([]Record, error)
	// Create saves one provider row and fills its assigned ID into the supplied
	// record. The input is mutable during preparation, and callers use that
	// identity for subsequent membership writes.
	Create(context.Context, *Record) error
	// BindGroups replaces the provider's memberships with the supplied entire
	// selection. Nil or empty input clears existing bindings, and storage reports
	// failures from replacing that membership set.
	BindGroups(context.Context, int64, []int64) error
	// ListByGroup reads provider records belonging to the specified routing group.
	ListByGroup(context.Context, int64) ([]Record, error)
	// GetByID reads one provider using its local record identifier. A successful
	// result supplies a nonnil record because administrative edit and eligibility
	// paths dereference it after checking the error.
	GetByID(context.Context, int64) (*Record, error)
	// GetByIDs reads stored records for the supplied local provider identifiers.
	GetByIDs(context.Context, []int64) ([]*Record, error)
	// ListWithFilters applies administrative filters and sorting before returning
	// a page.
	ListWithFilters(context.Context, pagination.PaginationParams, string, string, string, string, int64, string) ([]Record, *pagination.PaginationResult, error)
	// ListAllWithFilters reads the complete matching provider set for score
	// diagnostics.
	ListAllWithFilters(context.Context, string, string, string, string, int64, string) ([]Record, error)
	// ListSchedulableByGroupIDAndPlatforms reads eligible group members from the
	// selected platforms.
	ListSchedulableByGroupIDAndPlatforms(context.Context, int64, []string) ([]Record, error)
	// ListSchedulableUngroupedByPlatforms reads eligible providers from the
	// selected platforms without memberships.
	ListSchedulableUngroupedByPlatforms(context.Context, []string) ([]Record, error)
	// ListSchedulableByGroupIDAndPlatform reads eligible group members for one
	// selected platform.
	ListSchedulableByGroupIDAndPlatform(context.Context, int64, string) ([]Record, error)
	// ListSchedulableUngroupedByPlatform reads eligible ungrouped providers for
	// one selected platform.
	ListSchedulableUngroupedByPlatform(context.Context, string) ([]Record, error)
	// ListShadowsByParent reads the parent's credential shadows. Returned entries
	// are nonnil because administration reads each child ID before deleting it;
	// an empty selection can return nil successfully.
	ListShadowsByParent(context.Context, int64) ([]*Record, error)
	// Delete removes the selected provider record through the administrative
	// store.
	Delete(context.Context, int64) error
	// ClearError removes the persisted error and restores active status. The
	// administrator's scheduling switch stays unchanged; Admin.ClearProviderError
	// clears cooldown fields through its later storage calls.
	ClearError(context.Context, int64) error
	// ClearRateLimit removes rate-limit timestamps and overload state for the
	// selected provider. This broad health reset is distinct from the combined
	// billing quota reset that retains unrelated overload state.
	ClearRateLimit(context.Context, int64) error
	// ClearAntigravityQuotaScopes removes the provider's persisted Antigravity
	// quota-scope observations.
	ClearAntigravityQuotaScopes(context.Context, int64) error
	// ClearModelRateLimits removes persisted model-specific rate-limit entries for
	// this provider.
	ClearModelRateLimits(context.Context, int64) error
	// ClearTempUnschedulable removes persisted temporary scheduling cooldown
	// fields for this provider.
	ClearTempUnschedulable(context.Context, int64) error
	// SetError saves error status and its supplied message for this provider.
	SetError(context.Context, int64, string) error
	// SetSchedulable saves the administrator-controlled scheduling switch for this
	// provider.
	SetSchedulable(context.Context, int64, bool) error
}

// ProviderQuotaResetter supplies billing's combined quota and cooldown reset
// operation.
type ProviderQuotaResetter interface {
	// ResetQuotaUsedAndClearRateLimitCooldown resets usage counters and the
	// quota-related scheduling cooldown together. Billing supplies this operation
	// so administration does not separately clear unrelated overload state during
	// a quota reset.
	ResetQuotaUsedAndClearRateLimitCooldown(context.Context, int64) error
}

type (
	// RuntimeUnblocker removes the process scheduling block after administrative
	// recovery.
	RuntimeUnblocker interface {
		// ClearProviderSchedulingBlock removes the provider's current process
		// scheduling block.
		ClearProviderSchedulingBlock(int64)
	}
	// AdminOptions binds storage extensions and callbacks for provider
	// administration.
	AdminOptions struct {
		// ShadowModels supplies the mapping installed on new Spark shadows.
		// Configure this callback before CreateShadow constructs a shadow's
		// credential settings.
		ShadowModels func() map[string]any
		// Duplicates saves copied providers with their memberships. Nil makes
		// DuplicateProvider return a configuration error after construction and
		// before submitting its write.
		Duplicates DuplicateStore
		// Groups validates selected IDs during edits and bulk updates. Nonempty
		// selections require it; shadow creation validates supplied groups when
		// this dependency is configured.
		Groups AdminGroups
		// Proxies optionally loads associations for credential validation. Nil
		// skips lookup, and failed lookups leave the prospective record's
		// existing association unchanged.
		Proxies     PrivacyProxyReader
		Creation    CreationOptions       // creation clock and fingerprint dependencies
		Credentials CreateCredentialHooks // platform credential preparation hooks
		// Background registers postcreation privacy work with the application. OpenAI
		// and Antigravity OAuth creation require it; Admin ignores the returned
		// admission boolean.
		Background func(string, func()) bool
		// Error records panics from postcreation privacy work. Configure it
		// together with Background so a recovered panic has a callable logger.
		Error func(string, ...any)
		// Privacy supplies postcreation OAuth privacy operations. A nil service
		// returns an empty observation, so an accepted background task can complete
		// while skipping the upstream privacy request.
		Privacy *PrivacyService
		// Quotas supplies the combined billing reset. Configure this dependency before
		// ResetProviderQuota reaches an eligible parent provider after its lookup.
		Quotas ProviderQuotaResetter
		// RuntimeBlocker optionally removes process scheduling blocks after storage
		// cleanup calls succeed. Nil skips that process-local action while storage
		// cleanup still runs.
		RuntimeBlocker RuntimeUnblocker
	}
)

// Admin applies management rules using application-owned storage and service
// dependencies.
type Admin struct {
	providerRepo AdminStore   // administrative record storage
	options      AdminOptions // retained administrative service dependencies
}

// GroupReference carries routing group identity and its OAuth membership
// policy.
type GroupReference struct {
	RequireOAuthOnly bool   // OAuth membership requirement
	ID               int64  // routing group identifier
	Name             string // administrator group label
}

// AdminGroups validates and reads routing groups for administrative membership
// changes.
type AdminGroups interface {
	// ActiveGroups reads active routing group references for administrative group
	// selection.
	ActiveGroups(context.Context, string) ([]GroupReference, error)
	// ValidateGroups checks that the supplied routing group identifiers can be
	// bound.
	ValidateGroups(context.Context, []int64) error
	// GetGroup reads one routing group's identity and OAuth-only membership
	// requirement.
	GetGroup(context.Context, int64) (*GroupReference, error)
}

// CreateCredentialHooks prepares and validates platform credentials before
// administrative persistence.
type CreateCredentialHooks struct {
	// Site and ValidateEdit are called by edits; Prepare and Validate serve
	// creation. Configure the callbacks used by each operation before invoking
	// Admin.
	//
	// Site reads the Qoder site before an edit so ValidateEdit can defer an
	// unchanged PAT exchange when the selected site changes.
	Site func(*Record) (string, error) // Qoder site resolution callback
	// ValidateEdit checks a prospective edit. Its boolean allows the Qoder
	// validator to defer PAT exchange under the site's compatibility rule.
	ValidateEdit func(context.Context, *Record, bool) error // prospective edit validation callback
	Prepare      func(*Record)                              // Qoder machine preparation callback
	Validate     func(context.Context, *Record) error       // precreation credential validation callback
}

// DuplicateStore saves a copied provider together with its group memberships.
type DuplicateStore interface {
	// CreateWithProviderGroups saves the copied provider and its memberships in
	// one transaction. The caller supplies fresh membership rows, and storage
	// fills the provider ID into the record and those rows before linking them.
	//
	// Generated identity may already be installed on the input when a later
	// membership or event write fails. Production storage commits transactions it
	// opens itself; an already transaction-bound store leaves commit or rollback
	// to its outer owner.
	CreateWithProviderGroups(context.Context, *Record, []GroupMembership) error
}

// ShadowProxyStore supplies shadow records and configuration writes for proxy
// propagation.
type ShadowProxyStore interface {
	// ListShadowsByParent reads the parent's credential shadows for synchronized
	// proxy changes.
	ListShadowsByParent(context.Context, int64) ([]*Record, error)
	// Update saves a shadow record through the supplied store's update path.
	Update(context.Context, *Record) error
}

// NewAdmin binds administrative storage and the supplied service callbacks. It
// copies the options value and retains its referenced dependencies; callers
// configure credential, creation, and background hooks before using paths that
// require them.
func NewAdmin(store AdminStore, options AdminOptions) *Admin {
	return &Admin{providerRepo: store, options: options}
}
