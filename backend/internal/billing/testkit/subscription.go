package testkit

import (
	"context"
	"strconv"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
)

// Subscription fixtures keep grant records by ID and a separate owner/plan
// timeline index used by renewal tests. Mutating a record through Seed,
// Create, Update or Restore rebuilds that index, ordering starts and creation
// times while latest selection independently chooses maximum expiry. Explicit
// seeded IDs do not advance the generated-ID counter, so tests avoid
// collisions when mixing manual and automatic IDs.
//
// Stored inputs and most read results use shallow copies, sharing nested
// plan/user views, quota pointers and timestamps. Restore returns its stored
// outer pointer as well, so direct result edits reach that fixture record.
// The fixture ignores contexts and has no map synchronization or transaction
// rollback. Base repository methods panic when a test leaves an
// operation unconfigured; concrete implementations cover only their selected
// in-memory operations rather than PostgreSQL filtering and persistence rules.

// SubscriptionRepositoryNoop panics on repository operations that a fixture
// leaves unconfigured.
type SubscriptionRepositoryNoop struct{}

// SubscriptionRepository stores shallow grant copies and rebuilds owner/plan
// timeline indexes for tests.
type SubscriptionRepository struct {
	// SubscriptionRepositoryNoop supplies panic behavior for methods not
	// overridden below. Tests requiring deletion, quota mutation or additional
	// page/group queries embed another implementation rather than silently
	// receiving no-op success.
	SubscriptionRepositoryNoop

	// nextID advances only when Seed or Create receives a zero record ID.
	// Explicit IDs do not reserve this counter, so callers avoid collisions that
	// would overwrite ByID entries.
	nextID int64 // next generated fixture identifier
	// ByID and byUserPlan hold current records and their derived timeline lookup
	// together. Repository mutators rebuild the index after replacement; direct
	// fixture edits need RebuildIndex before owner/plan reads.
	// ByID exposes mutable fixture records, all of which must be nonnil before
	// rebuilding. Stored copies share nested references; direct
	// map/owner/plan/time changes need RebuildIndex before timeline reads.
	ByID map[int64]*billing.UserSubscription // stored fixture grant pointers
	// byUserPlan mirrors ByID using exact owner/plan keys and start/creation
	// ordering. RebuildIndex replaces it; exact timestamp ties have no stable
	// order or ID tiebreaker under the map traversal and swaps.
	byUserPlan map[string][]int64 // ordered owner-plan record identifiers
	// CreateCalls increments for each nonnil Create input, including replacement
	// of an existing ID. Nil creates return success without changing this count;
	// fixture access is unsynchronized.
	CreateCalls int // successful nonnil create invocation count
}

// Create panics on every attempted base-fixture insertion. Tests override it
// to store a new grant; the base method changes no supplied record and never
// returns a successful creation result.
func (SubscriptionRepositoryNoop) Create(context.Context, *billing.UserSubscription) error {
	panic("unexpected Create call")
}

// GetByID panics on every base-fixture record lookup. An embedding fixture
// overrides it to expose stored grants; this method reads no rows and supplies
// no not-found result to its caller.
func (SubscriptionRepositoryNoop) GetByID(context.Context, int64) (*billing.UserSubscription, error) {
	panic("unexpected GetByID call")
}

// GetByIDIncludeDeleted panics on every base-fixture restoration read. A
// concrete fixture must supply its stored/deleted record behavior; this base
// method neither reads deletion state nor produces a grant view.
func (SubscriptionRepositoryNoop) GetByIDIncludeDeleted(context.Context, int64) (*billing.UserSubscription, error) {
	panic("unexpected GetByIDIncludeDeleted call")
}

// GetByUserIDAndGroupID panics for every attempted legacy owner/group lookup.
// Fixtures using that additional method override it themselves; the base
// implementation performs no selection or group coverage validation.
func (SubscriptionRepositoryNoop) GetByUserIDAndGroupID(context.Context, int64, int64) (*billing.UserSubscription, error) {
	panic("unexpected GetByUserIDAndGroupID call")
}

// GetActiveByUserIDAndGroupID panics for every base-fixture active owner/group
// lookup. It computes neither time eligibility nor quota coverage, so tests
// requiring that operation supply a concrete override.
func (SubscriptionRepositoryNoop) GetActiveByUserIDAndGroupID(context.Context, int64, int64) (*billing.UserSubscription, error) {
	panic("unexpected GetActiveByUserIDAndGroupID call")
}

// GetLatestByUserIDAndPlanID panics on every attempted base-fixture renewal
// lookup. A concrete fixture implements lifetime ordering when required; this
// method reads no timeline and returns no latest grant.
func (SubscriptionRepositoryNoop) GetLatestByUserIDAndPlanID(context.Context, int64, int64) (*billing.UserSubscription, error) {
	panic("unexpected GetLatestByUserIDAndPlanID call")
}

// Update panics on every attempted base-fixture grant replacement. It changes
// no supplied fields or stored record, so tests that expect persisted timeline
// edits override this repository operation.
func (SubscriptionRepositoryNoop) Update(context.Context, *billing.UserSubscription) error {
	panic("unexpected Update call")
}

// Delete panics on every attempted base-fixture grant removal. It sets no
// deletion marker and removes no record, allowing revocation fixtures to
// detect an unconfigured deletion before claiming success.
func (SubscriptionRepositoryNoop) Delete(context.Context, int64) error {
	panic("unexpected Delete call")
}

// Restore panics on every attempted base-fixture restoration. It clears no
// deletion marker or stored label, so restoration scenarios need an override
// that supplies those observable changes.
func (SubscriptionRepositoryNoop) Restore(context.Context, int64, string) (*billing.UserSubscription, error) {
	panic("unexpected Restore call")
}

// ListByUserID panics on every attempted base-fixture owner listing. It
// returns neither an empty slice nor seeded rows, ensuring tests configure
// this query before exercising a successful list path.
func (SubscriptionRepositoryNoop) ListByUserID(context.Context, int64) ([]billing.UserSubscription, error) {
	panic("unexpected ListByUserID call")
}

// ListByUserIDAndPlanID panics on every attempted base-fixture timeline query.
// It supplies no chain ordering or records, so renewal and shift scenarios
// implement the owner/plan read they require.
func (SubscriptionRepositoryNoop) ListByUserIDAndPlanID(context.Context, int64, int64) ([]billing.UserSubscription, error) {
	panic("unexpected ListByUserIDAndPlanID call")
}

// ListActiveByUserID panics on every attempted base-fixture active listing. It
// evaluates no lifetime or status predicate, so a consuming scenario provides
// its selected active-record behavior through an override.
func (SubscriptionRepositoryNoop) ListActiveByUserID(context.Context, int64) ([]billing.UserSubscription, error) {
	panic("unexpected ListActiveByUserID call")
}

// ListByGroupID panics on every attempted base-fixture group page query. It
// returns no rows or pagination metadata, leaving this additional legacy
// operation to scenarios that configure it.
func (SubscriptionRepositoryNoop) ListByGroupID(context.Context, int64, pagination.PaginationParams) ([]billing.UserSubscription, *pagination.PaginationResult, error) {
	panic("unexpected ListByGroupID call")
}

// ListByPlanID panics on every attempted base-fixture plan page query. It
// produces no row count or response page, so administrator page scenarios
// require a concrete repository override.
func (SubscriptionRepositoryNoop) ListByPlanID(context.Context, int64, pagination.PaginationParams) ([]billing.UserSubscription, *pagination.PaginationResult, error) {
	panic("unexpected ListByPlanID call")
}

// List panics on every attempted base-fixture administrator list request. It
// applies none of the supplied filters or sort inputs and returns no page
// metadata, exposing an unconfigured query path.
func (SubscriptionRepositoryNoop) List(context.Context, pagination.PaginationParams, *int64, *int64, string, string, string, string) ([]billing.UserSubscription, *pagination.PaginationResult, error) {
	panic("unexpected List call")
}

// ListBySourceOrderID panics on every attempted base-fixture payment-source
// query. It cannot establish prior issuance for an order, so payment replay
// tests provide a concrete linked-grant listing.
func (SubscriptionRepositoryNoop) ListBySourceOrderID(context.Context, int64) ([]billing.UserSubscription, error) {
	panic("unexpected ListBySourceOrderID call")
}

// ExistsByUserIDAndGroupID panics for every attempted legacy owner/group
// existence check. It supplies no boolean observation, requiring tests using
// that additional method to configure their own result.
func (SubscriptionRepositoryNoop) ExistsByUserIDAndGroupID(context.Context, int64, int64) (bool, error) {
	panic("unexpected ExistsByUserIDAndGroupID call")
}

// ExtendExpiry panics on every attempted base-fixture deadline write. It
// changes no record lifetime, so tests needing persisted expiry changes
// override this operation rather than assuming a completed extension.
func (SubscriptionRepositoryNoop) ExtendExpiry(context.Context, int64, time.Time) error {
	panic("unexpected ExtendExpiry call")
}

// UpdateStatus panics on every attempted base-fixture label write. It changes
// no stored or effective status, allowing scenarios to detect a missing
// status-mutation implementation before continuing their assertions.
func (SubscriptionRepositoryNoop) UpdateStatus(context.Context, int64, string) error {
	panic("unexpected UpdateStatus call")
}

// UpdateNotes panics on every attempted base-fixture reason write. It retains
// all fixture state and returns no successful update, so scenarios editing
// history text supply a concrete override.
func (SubscriptionRepositoryNoop) UpdateNotes(context.Context, int64, string) error {
	panic("unexpected UpdateNotes call")
}

// ActivateWindows panics on every attempted base-fixture anchor activation. It
// examines no selected flags and writes no timestamps, so first-window
// scenarios configure their intended activation behavior separately.
func (SubscriptionRepositoryNoop) ActivateWindows(context.Context, int64, time.Time, billing.SubscriptionWindowActivation) error {
	panic("unexpected ActivateWindows call")
}

// ResetUsageWindows panics on every attempted base-fixture multi-period reset.
// It clears no counter or anchor, requiring quota-maintenance scenarios to
// provide the persistence behavior they assert afterward.
func (SubscriptionRepositoryNoop) ResetUsageWindows(context.Context, int64, bool, bool, bool, time.Time) error {
	panic("unexpected ResetUsageWindows call")
}

// ResetDailyUsage panics on every attempted base-fixture conditional daily
// reset. It compares no expected timestamp and changes no quota, so tests
// needing daily maintenance supply their own repository override.
func (SubscriptionRepositoryNoop) ResetDailyUsage(context.Context, int64, *time.Time, time.Time) error {
	panic("unexpected ResetDailyUsage call")
}

// ResetWeeklyUsage panics on every attempted base-fixture conditional weekly
// reset. It evaluates no stale-anchor condition and clears no usage, exposing
// an unconfigured maintenance call in the scenario.
func (SubscriptionRepositoryNoop) ResetWeeklyUsage(context.Context, int64, *time.Time, time.Time) error {
	panic("unexpected ResetWeeklyUsage call")
}

// ResetMonthlyUsage panics on every attempted base-fixture conditional monthly
// reset. It changes no anchor or consumption, so tests exercising monthly
// persistence configure their intended conflict and success results.
func (SubscriptionRepositoryNoop) ResetMonthlyUsage(context.Context, int64, *time.Time, time.Time) error {
	panic("unexpected ResetMonthlyUsage call")
}

// IncrementUsage panics on every attempted base-fixture usage addition. It
// changes no period counters and validates no amount, making an unexpected
// grant-charge write visible to the test immediately.
func (SubscriptionRepositoryNoop) IncrementUsage(context.Context, int64, float64) error {
	panic("unexpected IncrementUsage call")
}

// BatchUpdateExpiredStatus panics on every attempted base-fixture expiry scan.
// It returns no affected count and changes no labels, so maintenance scenarios
// provide their own bulk-status operation.
func (SubscriptionRepositoryNoop) BatchUpdateExpiredStatus(context.Context) (int64, error) {
	panic("unexpected BatchUpdateExpiredStatus call")
}

// NewSubscriptionRepository initializes empty grant and timeline maps with
// automatic IDs starting at one. The fixture has no synchronization or context
// cancellation; tests own its lifetime and avoid conflicting explicit IDs.
func NewSubscriptionRepository() *SubscriptionRepository {
	return &SubscriptionRepository{
		nextID:     1,
		ByID:       make(map[int64]*billing.UserSubscription),
		byUserPlan: make(map[string][]int64),
	}
}

// key encodes an exact owner/plan pair as signed decimal IDs separated by a
// colon for byUserPlan. The separator distinguishes pairs such as
// one/twenty-three and twelve/three; no alias expansion or ID validation
// occurs.
func (s *SubscriptionRepository) key(userID, planID int64) string {
	return strconv.FormatInt(userID, 10) + ":" + strconv.FormatInt(planID, 10)
}

// RebuildIndex replaces the owner/plan index from nonnil ByID records, then
// orders each timeline by start and creation time ascending. Exact timestamp
// ties have no stable order or ID tiebreaker, because map traversal and
// indirect swaps can reorder them. Call after direct fixture changes affecting
// membership or ordering.
//
// The rebuilt sequence drives ListByUserIDAndPlanID, while latest selection
// independently compares expiry. Index slices contain ByID map keys, so later
// reads obtain each current stored record rather than an indexed copy. This
// mutation is unsynchronized; test callers quiesce concurrent access and keep
// every indexed entry present.
func (s *SubscriptionRepository) RebuildIndex() {
	// Replace the entire derived index before rebuilding it from current record
	// ownership; stale pair membership must disappear when a fixture record moves
	// to another owner or plan.
	s.byUserPlan = make(map[string][]int64)
	for id, sub := range s.ByID {
		key := s.key(sub.UserID, sub.PlanID)
		s.byUserPlan[key] = append(s.byUserPlan[key], id)
	}
	// Map traversal supplies no timeline order. Sort starts and creation times in
	// each index slice so chain tests read a consistent chronological relation
	// for unequal timestamps.
	for key := range s.byUserPlan {
		ids := s.byUserPlan[key]
		for i := range ids {
			for j := i + 1; j < len(ids); j++ {
				left := s.ByID[ids[i]]
				right := s.ByID[ids[j]]
				if right.StartsAt.Before(left.StartsAt) ||
					(right.StartsAt.Equal(left.StartsAt) && right.CreatedAt.Before(left.CreatedAt)) {
					ids[i], ids[j] = ids[j], ids[i]
				}
			}
		}
		s.byUserPlan[key] = ids
	}
}

// Seed shallowly stores a nonnil input and rebuilds timeline indexing. Zero ID
// consumes the next counter; an explicit ID can replace a row and does not
// advance that counter. The input's ID itself remains unchanged.
func (s *SubscriptionRepository) Seed(sub *billing.UserSubscription) {
	if sub == nil {
		return
	}
	cp := *sub
	if cp.ID == 0 {
		cp.ID = s.nextID
		s.nextID++
	}
	s.ByID[cp.ID] = &cp
	// Rebuild membership and timeline order after replacing the stored record, so
	// later owner/plan reads use these newly stored scalar fields.
	s.RebuildIndex()
}

// Create shallowly stores a nonnil grant, writes its chosen ID back to input
// and rebuilds timelines. It counts each nonnil invocation, accepts nil as
// success, and overwrites existing IDs without uniqueness or database-default
// validation.
func (s *SubscriptionRepository) Create(_ context.Context, sub *billing.UserSubscription) error {
	if sub == nil {
		return nil
	}
	s.CreateCalls++
	cp := *sub
	if cp.ID == 0 {
		cp.ID = s.nextID
		s.nextID++
	}
	sub.ID = cp.ID
	s.ByID[cp.ID] = &cp
	// Rebuild membership and timeline order after replacing the stored record, so
	// later owner/plan reads use these newly stored scalar fields.
	s.RebuildIndex()
	return nil
}

// GetByID returns a shallow copy of stored grant data or
// subscription-not-found for missing/nil entries. It includes deleted rows and
// keeps nested pointers shared; scalar result changes require Update to reach
// the stored outer record.
func (s *SubscriptionRepository) GetByID(_ context.Context, id int64) (*billing.UserSubscription, error) {
	sub := s.ByID[id]
	if sub == nil {
		return nil, billing.ErrSubscriptionNotFound
	}
	cp := *sub
	return &cp, nil
}

// GetByIDIncludeDeleted returns a shallow stored copy or
// subscription-not-found for missing/nil entries. It retains the stored label
// and deleted marker, with nested references shared, using the same unfiltered
// record access as GetByID.
func (s *SubscriptionRepository) GetByIDIncludeDeleted(_ context.Context, id int64) (*billing.UserSubscription, error) {
	sub := s.ByID[id]
	if sub == nil {
		return nil, billing.ErrSubscriptionNotFound
	}
	cp := *sub
	return &cp, nil
}

// GetLatestByUserIDAndPlanID chooses maximum expiry, then maximum creation
// time, from the indexed owner/plan sequence. Empty membership returns
// subscription-not-found; deleted and ineffective records remain eligible in
// this fixture, and the result is a shallow copy.
//
// The timeline index itself is sorted by start and creation time, so this
// method compares expiry independently rather than taking its final entry.
// Exact expiry/creation ties keep the first indexed candidate, and no ID
// tiebreaker is added. Direct ByID edits require a rebuilt, intact index
// before this read.
func (s *SubscriptionRepository) GetLatestByUserIDAndPlanID(_ context.Context, userID, planID int64) (*billing.UserSubscription, error) {
	ids := s.byUserPlan[s.key(userID, planID)]
	if len(ids) == 0 {
		return nil, billing.ErrSubscriptionNotFound
	}
	latest := s.ByID[ids[0]]
	for _, id := range ids[1:] {
		candidate := s.ByID[id]
		if candidate.ExpiresAt.After(latest.ExpiresAt) ||
			(candidate.ExpiresAt.Equal(latest.ExpiresAt) && candidate.CreatedAt.After(latest.CreatedAt)) {
			latest = candidate
		}
	}
	cp := *latest
	return &cp, nil
}

// Update shallowly replaces the record keyed by a nonnil input's ID and
// rebuilds indexing. It accepts nil as success and can create a missing ID;
// nil fields replace fixture values, differing from PostgreSQL nillable-setter
// behavior.
func (s *SubscriptionRepository) Update(_ context.Context, sub *billing.UserSubscription) error {
	if sub == nil {
		return nil
	}
	cp := *sub
	s.ByID[cp.ID] = &cp
	// Rebuild membership and timeline order after replacing the stored record, so
	// later owner/plan reads use these newly stored scalar fields.
	s.RebuildIndex()
	return nil
}

// Restore replaces a stored grant with a shallow copy having the supplied
// label, nil deletion marker and current UTC update time. Missing/nil entries
// return not-found; the returned pointer is also the stored record, so
// subsequent direct edits affect fixture state.
func (s *SubscriptionRepository) Restore(_ context.Context, subscriptionID int64, restoredStatus string) (*billing.UserSubscription, error) {
	sub := s.ByID[subscriptionID]
	if sub == nil {
		return nil, billing.ErrSubscriptionNotFound
	}
	cp := *sub
	cp.Status = restoredStatus
	cp.DeletedAt = nil
	cp.UpdatedAt = time.Now().UTC()
	s.ByID[subscriptionID] = &cp
	// Rebuild membership and timeline order after replacing the stored record, so
	// later owner/plan reads use these newly stored scalar fields.
	s.RebuildIndex()
	return &cp, nil
}

// ListByUserID returns shallow values for matching owners in map iteration
// order, including deleted records. An empty match produces an allocated
// slice; nested references remain shared and the fixture ignores context
// cancellation.
func (s *SubscriptionRepository) ListByUserID(_ context.Context, userID int64) ([]billing.UserSubscription, error) {
	out := make([]billing.UserSubscription, 0)
	for _, sub := range s.ByID {
		if sub.UserID == userID {
			out = append(out, *sub)
		}
	}
	return out, nil
}

// ListByUserIDAndPlanID returns shallow values in rebuilt start/creation
// timeline order for the exact pair. Empty membership yields an allocated
// slice; indexed rows must remain present, and deleted records are not
// filtered here.
func (s *SubscriptionRepository) ListByUserIDAndPlanID(_ context.Context, userID, planID int64) ([]billing.UserSubscription, error) {
	ids := s.byUserPlan[s.key(userID, planID)]
	out := make([]billing.UserSubscription, 0, len(ids))
	for _, id := range ids {
		out = append(out, *s.ByID[id])
	}
	return out, nil
}

// ListActiveByUserID returns matching-owner shallow values whose derived
// status is active at one wall-clock observation. Map iteration defines order;
// EffectiveStatus determines deletion/time/suspension behavior, which can
// differ from PostgreSQL's stored-label filter.
func (s *SubscriptionRepository) ListActiveByUserID(_ context.Context, userID int64) ([]billing.UserSubscription, error) {
	now := time.Now()
	out := make([]billing.UserSubscription, 0)
	for _, sub := range s.ByID {
		if sub.UserID == userID && sub.EffectiveStatus(now) == billing.SubscriptionStatusActive {
			out = append(out, *sub)
		}
	}
	return out, nil
}

// ListBySourceOrderID returns shallow values with a nonnil source-order
// pointer equal to the supplied ID. It includes deleted records in map
// iteration order, retains nested sharing and produces an allocated empty
// slice for no matches.
func (s *SubscriptionRepository) ListBySourceOrderID(_ context.Context, sourceOrderID int64) ([]billing.UserSubscription, error) {
	out := make([]billing.UserSubscription, 0)
	for _, sub := range s.ByID {
		if sub.SourceOrderID != nil && *sub.SourceOrderID == sourceOrderID {
			out = append(out, *sub)
		}
	}
	return out, nil
}
