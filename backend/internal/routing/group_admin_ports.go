package routing

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
)

// Group administration reads provider identity and Key membership through
// injected readers before writing group settings. Transaction and cache
// callbacks are supplied by the application, so the administrative service can
// validate saved configuration without constructing storage or request-runtime
// services.

// GroupProvider contains the identity and authentication type needed when
// copying providers.
type GroupProvider struct {
	ID       int64  // saved provider row ID
	Platform string // saved provider platform name
	Type     string // provider credential type name
}

// GroupProviders supplies provider records and schedulable candidates to
// administrative group operations.
type GroupProviders interface {
	// GetByIDs reads provider identities and credential types for the supplied
	// IDs.
	GetByIDs(context.Context, []int64) ([]GroupProvider, error)
	// ListSchedulableByGroupID reads the group's provider candidates for
	// administrative model resolution.
	ListSchedulableByGroupID(context.Context, int64) ([]CatalogueProvider, error)
}

// GroupKeyReader lists bound Key strings for post-deletion authentication
// invalidation.
type GroupKeyReader interface {
	// ListKeysByGroupID returns Key strings currently bound to the specified
	// group.
	ListKeysByGroupID(context.Context, int64) ([]string, error)
}

// GroupAdminInvalidator expires authentication snapshots by group or individual
// Key string.
type GroupAdminInvalidator interface {
	GroupAuthInvalidator // group snapshot invalidation operations
	// InvalidateAuthCacheByKey expires one Key authentication snapshot by its
	// stored credential string. Deletion callers capture these strings before
	// associations disappear and invoke invalidation after cascade removal
	// succeeds.
	InvalidateAuthCacheByKey(context.Context, string)
}

// GroupPricingInvalidator refreshes shared prices when their storage or
// associations change.
type GroupPricingInvalidator interface {
	// InvalidateCache expires shared price lookups after persisted configuration
	// or membership changes. PricingConfigService also rebuilds synchronously and
	// observes failures through its warning callback, so callers invoke it after
	// storage writes succeed.
	InvalidateCache()
}

// GroupAdminOptions supplies routing reads, validation settings, and the group
// mutation transaction.
type GroupAdminOptions struct {
	ModelResolver RequestableResolver // configured request-model candidate resolver
	// DefaultModels is invoked for nonpositive group IDs with an empty platform
	// string. Configure this callback before requesting suggestions for a group
	// that has not been saved.
	DefaultModels func(string) []string // new-group model suggestion reader
	// GlobalWeights is invoked after local override validation when at least one
	// weight is overridden. Its result is merged before effective weight
	// validation.
	GlobalWeights func(context.Context) (policy.ScoreWeights, error) // global scoring weight reader
	// Mutate supplies the operation context used for settings and membership
	// writes. Configure rollback for any repository error; GroupStore.Mutate
	// reuses an existing context transaction and leaves its completion to the
	// enclosing owner. Configured UpdateGroup authentication invalidation follows
	// a successful callback return.
	Mutate func(context.Context, func(context.Context) error) error // group mutation transaction callback
}

// GroupAdmin validates and persists group configuration using supplied stores
// and callbacks.
type GroupAdmin struct {
	groupRepo GroupRepository // group configuration and membership store
	// groupDuplicateRepo is required when duplication or keyed recovery is
	// requested. Ordinary group creation and updates use groupRepo and the Mutate
	// callback.
	groupDuplicateRepo GroupDuplicateRepository // optional atomic duplication store
	// groupSortOrderRepo serializes automatic tail allocation inside the mutation
	// transaction. Explicit sort positions bypass this reader, and nil leaves
	// allocation without that repository lock.
	groupSortOrderRepo GroupSortOrderRepository // optional creation ordering lock
	providerRepo       GroupProviders           // provider copying and catalog reader
	apiKeyRepo         GroupKeyReader           // bound Key string reader
	// authCacheInvalidator expires snapshots after successful updates or deletion.
	// Deletion also requires apiKeyRepo to read bound strings before the group is
	// removed.
	authCacheInvalidator          GroupAdminInvalidator   // optional authentication snapshot invalidator
	pricingConfigCacheInvalidator GroupPricingInvalidator // retained pricing invalidation interface
	options                       GroupAdminOptions       // administrative callbacks and model resolver
}

// NewGroupAdmin retains group repositories, provider readers, invalidators, and
// administrative callbacks. Construction performs no reads; callers configure
// dependencies for the operations they use, including Mutate for creation and
// updates and GlobalWeights when overriding scoring weights.
func NewGroupAdmin(repo GroupRepository, duplicate GroupDuplicateRepository, sortOrder GroupSortOrderRepository, providers GroupProviders, keys GroupKeyReader, invalidator GroupAdminInvalidator, pricingConfigs GroupPricingInvalidator, options GroupAdminOptions) *GroupAdmin {
	return &GroupAdmin{groupRepo: repo, groupDuplicateRepo: duplicate, groupSortOrderRepo: sortOrder, providerRepo: providers, apiKeyRepo: keys, authCacheInvalidator: invalidator, pricingConfigCacheInvalidator: pricingConfigs, options: options}
}

// ValidateAdvancedOverrides checks sparse scheduler fields and then the
// effective weights when a weight override exists. Invalid local fields fail
// before any global read; nonweight overrides need no GlobalWeights callback or
// settings lookup.
func (s *GroupAdmin) ValidateAdvancedOverrides(ctx context.Context, overrides GroupAdvancedSchedulerOverrides) error {
	if err := policy.ValidateGroupOverrides(overrides); err != nil {
		return err
	}
	if !policy.HasWeightOverrides(overrides) {
		return nil
	}
	weights, err := s.options.GlobalWeights(ctx)
	if err != nil {
		return err
	}
	return policy.ValidateEffectiveWeights(policy.ApplyGroupWeightOverrides(weights, overrides))
}
