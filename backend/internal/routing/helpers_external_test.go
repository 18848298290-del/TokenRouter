package routing_test

import (
	"context"
	"log/slog"

	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
)

// External group-administration tests need repository values that can be
// edited by the service while their stored fixtures remain inspectable. The
// adapters clone group values around reads and writes, and write-back carries
// repository-assigned fields into the caller's working group even when the
// repository returns an error.
//
// The constructor supplies an empty price store and reads model policy from a
// cloned group, filling default client protocols only when the stored
// selection is nil. Optional duplicate, key, ordering, provider,
// invalidation, and weight dependencies select the paths a test exercises.
// Its mutation callback executes directly in the supplied context, so it
// reproduces administrative call flow and supplies no database transaction or
// rollback.

// groupAdminPricingStore supplies an empty pricing snapshot for independent
// group-policy reads.
type groupAdminPricingStore struct {
	// PricingConfigRepository supplies unused methods through a nil embedded
	// interface. ListAll is implemented for cache reads; an unexpected other
	// method call would panic.
	routing.PricingConfigRepository // unused repository method placeholders
}

// groupPortFixture clones group values around the repository methods used by
// external tests.
type groupPortFixture struct {
	// GroupRepository remains owned by the test. The adapter passes contexts
	// through and isolates supported group collections through
	// routing.CloneGroup at each implemented exchange.
	routing.GroupRepository // wrapped administrative group repository
}

// groupDuplicatePortFixture clones duplicate lookup results and creation
// write-back for external tests.
type groupDuplicatePortFixture struct {
	// GroupDuplicateRepository receives a cloned write value and may populate
	// its fields. The adapter copies those fields back to the caller before
	// returning any repository error.
	routing.GroupDuplicateRepository // wrapped duplication fixture repository
}

// groupRepoStubForAdmin captures supplied administration arguments and
// configured group read results.
type groupRepoStubForAdmin struct {
	// created and updated retain pointers supplied by the group adapter. getByID
	// is the configured read value; the adapter clones it before service code
	// changes its fields.
	created *routing.
		Group // recorded group creation argument
	updated *routing.
		Group // recorded group update argument
	getByID *routing.
		Group // supplied group read result
	// getErr makes both ID readers return a nil group and the supplied error,
	// taking precedence over getByID when the test injects failure.
	getErr error // injected group read error

	listWithFiltersCalls    int                         // recorded filtered listing count
	listWithFiltersParams   pagination.PaginationParams // captured filtered page options
	listWithFiltersPlatform string                      // captured platform filter text
	listWithFiltersStatus   string                      // captured status filter text
	listWithFiltersSearch   string                      // captured search filter text
	// listWithFiltersIsExclusive retains the caller's pointer for later
	// assertions; tests keep that pointed value stable until the listing request
	// has been inspected.
	listWithFiltersIsExclusive *bool // captured optional exclusivity filter
	// listWithFiltersGroups is returned directly. Callers observe these fixture
	// rows through the surrounding service or adapter and configure their
	// contents before use.
	listWithFiltersGroups []routing.Group // supplied filtered listing rows
	// listWithFiltersResult overrides synthesized pagination when nonnil.
	// Otherwise ListWithFilters derives the total from listWithFiltersGroups and
	// copies page values from the request.
	listWithFiltersResult *pagination.PaginationResult // optional supplied page metadata
	// listWithFiltersErr suppresses rows and pagination after query arguments
	// have been recorded, allowing assertions to inspect a failed listing's
	// request.
	listWithFiltersErr      error // injected filtered listing error
	groupSortOrderLockCalls int   // recorded ordering lock requests
}

// ListAll returns an empty successful pricing snapshot for group-policy
// fixtures.
func (groupAdminPricingStore) ListAll(context.Context) ([]routing.PricingConfig, error) {
	return nil, nil
}

// newGroupAdminForTest constructs administration using the basic repository,
// duplicate, and pricing invalidation fixtures. Other dependencies use
// newGroupAdminPortsForTest defaults, including direct mutation callbacks and
// default scheduler weights.
func newGroupAdminForTest(repo routing.GroupRepository, duplicate routing.GroupDuplicateRepository, pricingConfigs routing.GroupPricingInvalidator) *routing.GroupAdmin {
	return newGroupAdminPortsForTest(repo, duplicate, pricingConfigs, nil, nil, nil, nil)
}

// newGroupAdminPortsForTest constructs administration with the supplied
// repository and optional dependency fixtures. It uses the first key reader,
// wraps a supplied duplicate reader, and executes mutation callbacks directly
// in the caller's context.
//
// Model rules come from cloned group reads with default client protocols
// supplied for nil selections; an explicit empty selection stays empty. The
// price store is empty, default model suggestions are fixed, and supplied
// weights replace the scheduler defaults before validation.
//
// Supply the first key reader when deletion invalidates bound credentials,
// and a provider reader for OAuth-filtered membership copies. Repositories
// and callback inputs remain owned by the test.
func newGroupAdminPortsForTest(repo routing.GroupRepository, duplicate routing.GroupDuplicateRepository, pricingConfigs routing.GroupPricingInvalidator, sortOrder routing.GroupSortOrderRepository, providers routing.GroupProviders, invalidator routing.GroupAdminInvalidator, weights *policy.ConfigScoreWeights, keyReaders ...routing.GroupKeyReader) *routing.GroupAdmin {
	var keys routing.GroupKeyReader
	if len(keyReaders) > 0 {
		keys = keyReaders[0]
	}
	var duplicates routing.GroupDuplicateRepository
	if duplicate != nil {
		duplicates = groupDuplicatePortFixture{duplicate}
	}
	modelPolicies := routing.NewPricingConfigService(groupAdminPricingStore{}, nil, routing.PricingConfigOptions{ReadGroup: func(ctx context.Context, id int64) (*routing.Group, error) {
		group, err := repo.GetByIDLite(ctx, id)
		if group != nil {
			group = routing.CloneGroup(group)
			// Supply text defaults only for an omitted selection, preserving an
			// allocated empty slice so the fixture can still exercise closed
			// client admission through the policy reader.
			if group.AllowedProtocols == nil {
				group.AllowedProtocols = capability.DefaultGroupClientProtocols("")
			}
		}
		return group, err
	}})
	return routing.NewGroupAdmin(groupPortFixture{repo}, duplicates, sortOrder, providers, keys, invalidator, pricingConfigs, routing.GroupAdminOptions{
		DefaultModels: func(string) []string { return []string{"claude-sonnet-4-6", "gpt-5.4"} },
		ModelResolver: routing.RequestableResolver{
			GroupPolicies: modelPolicies,
			Warn:          slog.Warn,
		},
		GlobalWeights: func(ctx context.Context) (policy.ScoreWeights, error) {
			defaults := scheduler.DefaultAdminSettingsDefaults()
			if weights != nil {
				defaults.Weights = *weights
			}
			return scheduler.LoadValidationWeights(ctx, nil, defaults)
		},
		Mutate: func(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) },
	})
}

// testGroupsFixture returns a new group slice using routing.CloneGroup for
// every element. Nil input stays nil, and the supported mutable group fields
// follow that copier's isolation rules.
func testGroupsFixture(values []routing.Group) []routing.Group {
	if values == nil {
		return nil
	}
	out := make([]routing.Group, len(values))
	for i := range values {
		out[i] = *routing.CloneGroup(&values[i])
	}
	return out
}

// Create submits a cloned working group to the wrapped repository and copies
// its resulting fields into the caller's group before returning the error.
// Supply a nonnil group and a repository that accepts that disposable working
// value.
//
// Repository-populated fields therefore reach the caller even when the write
// reports failure. A second clone supplies caller-visible fields under
// routing.CloneGroup's copying rules. The repository controls any retention
// of its working argument.
func (r groupPortFixture) Create(ctx context.Context, value *routing.Group) error {
	old := routing.CloneGroup(value)
	err := r.GroupRepository.Create(ctx, old)
	*value = *routing.CloneGroup(old)
	return err
}

// Update submits a cloned working group to the wrapped repository and copies
// its resulting fields into the caller's group before returning the error.
// Supply a nonnil group; the wrapped repository can change the disposable
// argument while handling the attempted update.
//
// Write-back includes fields populated by a failing repository call. The
// caller receives a further clone under routing.CloneGroup's copying rules
// for later service or test inspection. The repository controls any retention
// of its working argument.
func (r groupPortFixture) Update(ctx context.Context, value *routing.Group) error {
	old := routing.CloneGroup(value)
	err := r.GroupRepository.Update(ctx, old)
	*value = *routing.CloneGroup(old)
	return err
}

// GetByID returns routing.CloneGroup of the wrapped full-read result with its
// accompanying error. Nil results stay nil, and recognized mutable group
// fields are detached by that copier.
func (r groupPortFixture) GetByID(ctx context.Context, id int64) (*routing.Group, error) {
	value, err := r.GroupRepository.GetByID(ctx, id)
	return routing.CloneGroup(value), err
}

// GetByIDLite returns routing.CloneGroup of the wrapped lightweight result
// with its accompanying error. Nil results stay nil, while any supplied error
// remains visible to the service's model-policy reader.
func (r groupPortFixture) GetByIDLite(ctx context.Context, id int64) (*routing.Group, error) {
	value, err := r.GroupRepository.GetByIDLite(ctx, id)
	return routing.CloneGroup(value), err
}

// List clones every group in the wrapped repository's page and returns its
// pagination pointer and error directly. The group values follow
// routing.CloneGroup; page metadata remains the repository's supplied object.
func (r groupPortFixture) List(ctx context.Context, params pagination.PaginationParams) ([]routing.Group, *pagination.PaginationResult, error) {
	v, p, e := r.GroupRepository.List(ctx, params)
	return testGroupsFixture(v), p, e
}

// ListWithFilters forwards all filters and clones the returned group page.
// Pagination metadata and the error pass through directly, so the adapter
// changes group-value ownership while retaining listing observations.
func (r groupPortFixture) ListWithFilters(ctx context.Context, params pagination.PaginationParams, platform, status, search string, isExclusive *bool) ([]routing.Group, *pagination.PaginationResult, error) {
	v, p, e := r.GroupRepository.ListWithFilters(ctx, params, platform, status, search, isExclusive)
	return testGroupsFixture(v), p, e
}

// ListActive clones every group returned by the wrapped active listing. It
// preserves a nil slice and returns the repository error alongside any rows
// the repository supplied.
func (r groupPortFixture) ListActive(ctx context.Context) ([]routing.Group, error) {
	v, e := r.GroupRepository.ListActive(ctx)
	return testGroupsFixture(v), e
}

// FindByDuplicateOperationID clones the wrapped duplicate lookup result and
// returns the same error. Missing operations remain nil, and supported
// mutable group fields receive routing.CloneGroup's copied storage.
func (p groupDuplicatePortFixture) FindByDuplicateOperationID(ctx context.Context, id string) (*routing.Group, error) {
	value, err := p.GroupDuplicateRepository.FindByDuplicateOperationID(ctx, id)
	return routing.CloneGroup(value), err
}

// CreateFromSource submits a cloned duplicate and source ID to the wrapped
// repository, then writes the result into the caller's group before returning
// the error. Supply a nonnil group; the repository can populate its disposable
// argument during the creation attempt.
//
// Assigned identities and other populated fields therefore survive a failing
// call through this adapter. The caller receives a further clone under
// routing.CloneGroup's copying rules for recovery or inspection. The
// repository controls any retention of its working argument.
func (p groupDuplicatePortFixture) CreateFromSource(ctx context.Context, value *routing.Group, id int64) error {
	copy := routing.CloneGroup(value)
	err := p.GroupDuplicateRepository.CreateFromSource(ctx, copy, id)
	*value = *routing.CloneGroup(copy)
	return err
}

// Create retains the supplied group pointer for later assertions and succeeds.
// It stores the caller's working object directly, so callers keep its fields
// stable while inspecting the captured creation argument.
func (s *groupRepoStubForAdmin) Create(_ context.Context, g *routing.Group) error {
	s.created = g
	return nil
}

// Update retains the supplied group pointer for later assertions and succeeds.
// It stores the caller's working object directly, so callers keep its fields
// stable while inspecting the captured update argument.
func (s *groupRepoStubForAdmin) Update(_ context.Context, g *routing.Group) error {
	s.updated = g
	return nil
}

// GetByID returns the configured group pointer unless getErr is set. It
// ignores the requested ID, and callers receive the same object retained in
// the fixture.
func (s *groupRepoStubForAdmin) GetByID(_ context.Context, _ int64) (*routing.Group, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.getByID, nil
}

// GetByIDLite returns the configured group pointer unless getErr is set. It
// ignores the requested ID and supplies the same fixture value as the full
// reader.
func (s *groupRepoStubForAdmin) GetByIDLite(_ context.Context, _ int64) (*routing.Group, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.getByID, nil
}

// Delete panics when administration tests request direct group deletion. The
// fixture has no implementation for this repository path, so invoking it
// exposes an unexpected dependency call immediately.
func (s *groupRepoStubForAdmin) Delete(_ context.Context, _ int64) error {
	panic("unexpected Delete call")
}

// DeleteCascade panics when create or update fixtures receive a cascade
// deletion. The fixture has no implementation for this repository path, so
// invoking it exposes an unexpected dependency call immediately.
func (s *groupRepoStubForAdmin) DeleteCascade(_ context.Context, _ int64) ([]int64, error) {
	panic("unexpected DeleteCascade call")
}

// List panics when administration requests an unfiltered paginated listing.
// The fixture has no implementation for this repository path, so invoking it
// exposes an unexpected dependency call immediately.
func (s *groupRepoStubForAdmin) List(_ context.Context, _ pagination.PaginationParams) ([]routing.Group, *pagination.PaginationResult, error) {
	panic("unexpected List call")
}

// ListWithFilters records its pagination and filter inputs before returning
// the configured rows. An injected error suppresses rows and pagination;
// absent result metadata is synthesized from the row count and request page
// values.
func (s *groupRepoStubForAdmin) ListWithFilters(_ context.Context, params pagination.PaginationParams, platform, status, search string, isExclusive *bool) ([]routing.Group, *pagination.PaginationResult, error) {
	s.listWithFiltersCalls++
	s.listWithFiltersParams = params
	s.listWithFiltersPlatform = platform
	s.listWithFiltersStatus = status
	s.listWithFiltersSearch = search
	s.listWithFiltersIsExclusive = isExclusive

	if s.listWithFiltersErr != nil {
		return nil, nil, s.listWithFiltersErr
	}

	result := s.listWithFiltersResult
	if result == nil {
		result = &pagination.PaginationResult{
			Total:    int64(len(s.listWithFiltersGroups)),
			Page:     params.Page,
			PageSize: params.PageSize,
		}
	}

	return s.listWithFiltersGroups, result, nil
}

// ListActive panics when administration requests all active groups through
// this fixture. The fixture has no implementation for this repository path,
// so invoking it exposes an unexpected dependency call immediately.
func (s *groupRepoStubForAdmin) ListActive(_ context.Context) ([]routing.Group, error) {
	panic("unexpected ListActive call")
}

// ListActiveByPlatform panics when administration requests active groups
// filtered by platform. The fixture has no implementation for this repository
// path, so invoking it exposes an unexpected dependency call immediately.
func (s *groupRepoStubForAdmin) ListActiveByPlatform(_ context.Context, _ string) ([]routing.Group, error) {
	panic("unexpected ListActiveByPlatform call")
}

// ListActiveByPlatformLite panics when administration requests lightweight
// active groups filtered by platform. The fixture has no implementation for
// this repository path, so invoking it exposes an unexpected dependency call
// immediately.
func (s *groupRepoStubForAdmin) ListActiveByPlatformLite(_ context.Context, _ string) ([]routing.Group, error) {
	panic("unexpected ListActiveByPlatformLite call")
}

// ExistsByName panics when administration requests a group-name existence
// check. The fixture has no implementation for this repository path, so
// invoking it exposes an unexpected dependency call immediately.
func (s *groupRepoStubForAdmin) ExistsByName(_ context.Context, _ string) (bool, error) {
	panic("unexpected ExistsByName call")
}

// GetProviderCount panics when administration requests separate
// provider-count hydration. The fixture has no implementation for this
// repository path, so invoking it exposes an unexpected dependency call
// immediately.
func (s *groupRepoStubForAdmin) GetProviderCount(_ context.Context, _ int64) (int64, int64, error) {
	panic("unexpected GetProviderCount call")
}

// DeleteProviderGroupsByGroupID panics when the base fixture receives
// membership replacement cleanup. The fixture has no implementation for this
// repository path, so invoking it exposes an unexpected dependency call
// immediately.
func (s *groupRepoStubForAdmin) DeleteProviderGroupsByGroupID(_ context.Context, _ int64) (int64, error) {
	panic("unexpected DeleteProviderGroupsByGroupID call")
}

// BindProvidersToGroup panics when the base fixture receives new provider
// membership binding. The fixture has no implementation for this repository
// path, so invoking it exposes an unexpected dependency call immediately.
func (s *groupRepoStubForAdmin) BindProvidersToGroup(_ context.Context, _ int64, _ []int64) error {
	panic("unexpected BindProvidersToGroup call")
}

// GetProviderIDsByGroupIDs panics when the base fixture receives source
// membership lookup. The fixture has no implementation for this repository
// path, so invoking it exposes an unexpected dependency call immediately.
func (s *groupRepoStubForAdmin) GetProviderIDsByGroupIDs(_ context.Context, _ []int64) ([]int64, error) {
	panic("unexpected GetProviderIDsByGroupIDs call")
}

// UpdateSortOrders accepts ordering writes successfully and retains no update
// arguments.
func (s *groupRepoStubForAdmin) UpdateSortOrders(_ context.Context, _ []routing.GroupSortOrderUpdate) error {
	return nil
}

// LockGroupSortOrder counts each ordering-lock request and returns successful
// acquisition.
func (s *groupRepoStubForAdmin) LockGroupSortOrder(_ context.Context) error {
	s.groupSortOrderLockCalls++
	return nil
}
