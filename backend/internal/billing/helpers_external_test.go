package billing_test

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
	"github.com/TokenFlux/TokenRouter/internal/billing/testkit"
	"github.com/TokenFlux/TokenRouter/internal/gateway/provider/modelidentity"
	"github.com/TokenFlux/TokenRouter/internal/modelcatalog/provider"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// External billing tests program lookup values and capture attempted
// redeem mutations while pricing fixtures supply catalog and settings
// inputs. The redeem fixture can retain mutation observations even
// when it returns an injected failure, so its state represents
// call evidence rather than transactional database behavior.

// redeemRepoStub programs redeem administration reads and records attempted
// mutation arguments.
type redeemRepoStub struct {
	// getErrByID entries take precedence over codesByID in GetByID.
	// Presence in the error map selects that response even when its
	// error value is nil, returning no code for that lookup.
	getErrByID map[int64]error // programmed lookup error entries
	// codesByID returns stored pointers directly on configured reads.
	// Update installs a shallow copy in this map after recording
	// the original input pointer, so nested members can remain shared
	// with test input.
	codesByID     map[int64]*billing.RedeemCode // programmed code record pointers
	deleteErrByID map[int64]error               // programmed deletion error entries
	updateErr     error                         // programmed update failure
	// updatedCodes, deletedIDs, and lockedGetIDs record attempted calls
	// before their configured result is returned. Update and Delete
	// observations remain present on injected errors; locking reads
	// record the requested identity without acquiring a database lock.
	updatedCodes []*billing.RedeemCode // captured original update pointers
	deletedIDs   []int64               // captured deletion attempt identities
	lockedGetIDs []int64               // captured locking-read request identities

	// batchUpdateIDs, batchUpdateFields, batchUpdateResult, batchUpdateErr,
	// and batchUpdateCalled describe the latest bulk call. IDs are
	// copied; field members remain shared through the struct copy.
	// An error wins over the result, while zero configured result
	// selects the ID count.
	batchUpdateIDs    []int64                             // copied bulk-update request identities
	batchUpdateFields billing.RedeemCodeBatchUpdateFields // captured bulk-update field values
	batchUpdateResult int64                               // programmed nonzero updated count
	batchUpdateErr    error                               // programmed bulk-update failure
	batchUpdateCalled bool                                // recorded bulk-update invocation flag
}

// settingsPrices supplies fixture billing settings and matches configured
// model-price cards.
type settingsPrices struct {
	// settings is retained by value in the fixture. The settings
	// reader calls BillingSettings.Clone to detach its optional price
	// pointers before returning the effective values to the resolver.
	settings pricing.BillingSettings // configured effective billing settings
	// cards retains the supplied slice header and underlying entries.
	// Mutating those entries affects this fixture; MatchPriceCard returns
	// a cloned matching card for the resolver's selected-price reads.
	cards []routing.ModelPricingEntry // shared configured price-card slice
}

// catalogFixture supplies model-price records for a retained catalog service snapshot.
type catalogFixture struct {
	// pricingData supplies model records to NewServiceFromSnapshot.
	// The service retains this map and its record pointers as owned
	// snapshot input after construction.
	pricingData map[string]*pricing.CatalogModelPricing
}

// Create panics on unexpected single-code insertion. This fixture
// programs existing or synthesized ID lookups and captures their
// administration writes, so creation callers require another
// repository fixture that supports insertion.
func (s *redeemRepoStub) Create(ctx context.Context, code *billing.RedeemCode) error {
	panic("unexpected Create call")
}

// CreateBatch panics on unexpected bulk insertion. The fixture's
// bulk operation records updates to supplied identities; it does
// not create an initial collection of codes or simulate generated
// redemption credentials for callers.
func (s *redeemRepoStub) CreateBatch(ctx context.Context, codes []billing.RedeemCode) error {
	panic("unexpected CreateBatch call")
}

// GetByID selects a programmed error-map entry first, then a
// stored code pointer, otherwise a new ID-only record. An existing
// error-map key can return nil code with nil error; stored code
// values are returned as supplied, including nil.
func (s *redeemRepoStub) GetByID(ctx context.Context, id int64) (*billing.RedeemCode, error) {
	if s.getErrByID != nil {
		if err, ok := s.getErrByID[id]; ok {
			return nil, err
		}
	}
	if s.codesByID != nil {
		if code, ok := s.codesByID[id]; ok {
			return code, nil
		}
	}
	return &billing.RedeemCode{ID: id}, nil
}

// GetByIDForUpdate records the requested identity and delegates
// the response to GetByID. It acquires no database lock and
// retains the observation even when the delegated lookup returns
// an injected error or nil record.
func (s *redeemRepoStub) GetByIDForUpdate(ctx context.Context, id int64) (*billing.RedeemCode, error) {
	s.lockedGetIDs = append(s.lockedGetIDs, id)
	return s.GetByID(ctx, id)
}

// GetByCode panics on an unexpected credential lookup. This
// fixture programs records by numeric ID for administration tests;
// a caller exercising a redemption credential needs a repository
// fixture with code-string lookup behavior.
func (s *redeemRepoStub) GetByCode(ctx context.Context, code string) (*billing.RedeemCode, error) {
	panic("unexpected GetByCode call")
}

// GetByCodeForUpdate panics on unexpected credential-based locking
// lookup. Lock-request observations in this fixture apply to
// GetByIDForUpdate, so credential redemption tests supply a
// repository fixture that supports their distinct lookup path.
func (s *redeemRepoStub) GetByCodeForUpdate(ctx context.Context, code string) (*billing.RedeemCode, error) {
	panic("unexpected GetByCodeForUpdate call")
}

// Update records the original code pointer and installs a shallow
// copy in codesByID before returning updateErr. Pass a nonnil
// code; captured pointers and copied nested members remain shared.
// An injected failure leaves both observations in this test fixture.
func (s *redeemRepoStub) Update(ctx context.Context, code *billing.RedeemCode) error {
	// Capture the original input for call assertions and retain a
	// shallow stored copy before returning the injected error, so
	// tests can inspect attempted writes even on their failure path.
	s.updatedCodes = append(s.updatedCodes, code)
	if s.codesByID == nil {
		s.codesByID = make(map[int64]*billing.RedeemCode)
	}
	cloned := *code
	s.codesByID[code.ID] = &cloned
	return s.updateErr
}

// BatchUpdate captures IDs, fields, and invocation state before
// returning its programmed result. IDs are copied but pointer
// members of fields remain shared; batchUpdateErr returns zero
// and wins over a nonzero result, otherwise zero configuration
// uses the supplied ID count.
func (s *redeemRepoStub) BatchUpdate(ctx context.Context, ids []int64, fields billing.RedeemCodeBatchUpdateFields) (int64, error) {
	s.batchUpdateCalled = true
	s.batchUpdateIDs = append([]int64(nil), ids...)
	s.batchUpdateFields = fields
	if s.batchUpdateErr != nil {
		return 0, s.batchUpdateErr
	}
	if s.batchUpdateResult != 0 {
		return s.batchUpdateResult, nil
	}
	return int64(len(ids)), nil
}

// Delete records the attempted identity and returns its programmed
// error-map value or nil. It retains the configured code map
// and the attempt observation, including on failure; this fixture
// does not emulate row removal or rollback.
func (s *redeemRepoStub) Delete(ctx context.Context, id int64) error {
	s.deletedIDs = append(s.deletedIDs, id)
	if s.deleteErrByID != nil {
		if err, ok := s.deleteErrByID[id]; ok {
			return err
		}
	}
	return nil
}

// Use panics on an unexpected redemption-state mutation. The
// fixture observes code administration and bulk field changes;
// a caller exercising entitlement use needs a repository fixture
// that simulates its use-state and consistency rules.
func (s *redeemRepoStub) Use(ctx context.Context, id, userID int64) error {
	panic("unexpected Use call")
}

// CreateUsage accepts any receipt input and returns nil without
// retaining a usage record. It supplies a successful interface
// placeholder for the surrounding tests, so later reads cannot
// observe a receipt created through this fixture.
func (s *redeemRepoStub) CreateUsage(ctx context.Context, usage *billing.RedeemCodeUsage) error {
	return nil
}

// GetUsageByRedeemCodeAndUser returns no receipt and no error for
// every code/user pair. The fixture retains no usage records,
// allowing its callers to observe an absent receipt independently
// of configured code lookup and mutation results.
func (s *redeemRepoStub) GetUsageByRedeemCodeAndUser(ctx context.Context, redeemCodeID, userID int64) (*billing.RedeemCodeUsage, error) {
	return nil, nil
}

// List panics on an unexpected unfiltered pagination request.
// This fixture returns programmed individual ID lookups and
// records mutations; tests exercising list results require a
// repository fixture with collection and pagination behavior.
func (s *redeemRepoStub) List(ctx context.Context, params pagination.PaginationParams) ([]billing.RedeemCode, *pagination.PaginationResult, error) {
	panic("unexpected List call")
}

// ListWithFilters panics on an unexpected filtered listing request.
// The fixture does not interpret type, status, search, or page
// inputs, so callers testing filtered collections supply a
// repository fixture with those query results.
func (s *redeemRepoStub) ListWithFilters(ctx context.Context, params pagination.PaginationParams, codeType, status, search string) ([]billing.RedeemCode, *pagination.PaginationResult, error) {
	panic("unexpected ListWithFilters call")
}

// ListByUserPaginated panics on an unexpected per-user list request.
// The fixture does not retain user-indexed code collections or
// page totals, so user-history tests need their own list-capable
// repository fixture.
func (s *redeemRepoStub) ListByUserPaginated(ctx context.Context, userID int64, params pagination.PaginationParams, codeType string) ([]billing.RedeemCode, *pagination.PaginationResult, error) {
	panic("unexpected ListByUserPaginated call")
}

// SumPositiveBalanceByUser panics on an unexpected user aggregate
// query. Programmed ID records and mutation observations do not
// constitute a simulated redemption ledger, so callers testing
// that balance sum require an aggregate-capable repository fixture.
func (s *redeemRepoStub) SumPositiveBalanceByUser(ctx context.Context, userID int64) (float64, error) {
	panic("unexpected SumPositiveBalanceByUser call")
}

// newCalculator constructs a calculator using the supplied catalog
// and no explicit price map. A nil catalog selects testkit's
// historical and media baseline fixtures; a supplied catalog
// provides its existing price data to the calculator.
func newCalculator(catalog *provider.Service) *billing.Calculator {
	return newCalculatorWithPrices(catalog, nil)
}

// newCalculatorWithPrices delegates calculator construction to
// testkit with optional explicit prices. When both inputs are
// nil, testkit supplies its baseline; supplied prices fill missing
// catalog names rather than replacing existing catalog entries.
func newCalculatorWithPrices(catalog *provider.Service, prices map[string]*pricing.ModelPricing) *billing.Calculator {
	return testkit.Calculator(catalog, prices)
}

// GetEffectiveBillingSettings returns the fixture settings with
// optional price pointers copied by Clone. Context and group
// identity are ignored, so tests choose one effective settings
// value while callers can modify those returned price pointers.
func (s *settingsPrices) GetEffectiveBillingSettings(context.Context, int64) pricing.BillingSettings {
	return s.settings.Clone()
}

// GetEffectiveConfigModelPricing selects a matching fixture card
// for model using MatchPriceCard and returns its clone. Context
// and group ID do not change the search; absence of an effective
// exact or wildcard match returns nil.
func (s *settingsPrices) GetEffectiveConfigModelPricing(_ context.Context, _ int64, model string) *pricing.ModelPricingEntry {
	return pricing.MatchPriceCard(s.cards, model)
}

// settingsResolver constructs a resolver and returns its retained
// settings/card fixture for test control. The source keeps supplied
// card slices and optional settings pointers, while its read
// methods apply their own cloning before returning selected values.
func settingsResolver(calculator *billing.Calculator, settings pricing.BillingSettings, cards []routing.ModelPricingEntry) (*billing.PriceResolver, *settingsPrices) {
	source := &settingsPrices{settings: settings, cards: cards}
	return billing.NewPriceResolver(source, calculator, nil, nil), source
}

// newCatalogFixture constructs a retained catalog service from
// the fixture price map with model-identity candidate support.
// Complete price-map population before construction and leave the map
// and its entries unchanged while the service reads them. The constructor
// retains that snapshot storage and starts no remote updates.
func newCatalogFixture(fixture catalogFixture) *provider.Service {
	return provider.NewServiceFromSnapshot(provider.Options{
		ModelLookupCandidates: modelidentity.CandidatesFactory,
	}, nil, provider.Snapshot{Data: fixture.pricingData})
}

// testPtrFloat64 returns separately addressed numeric fixture storage for the
// supplied value.
func testPtrFloat64(v float64) *float64 { return &v }

// ptrTime returns a pointer to a copied time value for fixtures.
func ptrTime(t time.Time) *time.Time { return &t }

// revokeSubscriptionFixture returns an active UTC subscription
// starting two hours before its clock observation and expiring
// twenty-four hours after it. All three window starts share the
// same UTC midnight pointer; callers configure quotas and usage
// for the revocation scenario they need.
func revokeSubscriptionFixture() *billing.UserSubscription {
	now := time.Now().UTC()
	// Use UTC midnight so the synthetic active subscription has a
	// current daily-window start even during the first clock hour,
	// letting revocation tests choose their usage state without an
	// incidental stale-day reset.
	windowStart := timezone.NewCalendar(now.Location()).StartOfDay(now)
	return &billing.UserSubscription{
		ID:                 1,
		UserID:             7,
		PlanID:             10,
		StartsAt:           now.Add(-2 * time.Hour),
		ExpiresAt:          now.Add(24 * time.Hour),
		Status:             billing.SubscriptionStatusActive,
		DailyWindowStart:   &windowStart,
		WeeklyWindowStart:  &windowStart,
		MonthlyWindowStart: &windowStart,
	}
}

// quotaPointer returns numeric quota fixture storage for the supplied value.
func quotaPointer(value float64) *float64 {
	return &value
}
