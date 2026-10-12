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

type redeemRepoStub struct {
	getErrByID    map[int64]error
	codesByID     map[int64]*billing.RedeemCode
	deleteErrByID map[int64]error
	updateErr     error
	updatedCodes  []*billing.RedeemCode
	deletedIDs    []int64
	lockedGetIDs  []int64

	batchUpdateIDs    []int64
	batchUpdateFields billing.RedeemCodeBatchUpdateFields
	batchUpdateResult int64
	batchUpdateErr    error
	batchUpdateCalled bool
}

// settingsPrices holds settings and price cards for billing tests.
type settingsPrices struct {
	settings pricing.BillingSettings
	cards    []routing.ModelPricingEntry
}

// catalogFixture holds model prices supplied by a test.
type catalogFixture struct {
	pricingData map[string]*pricing.CatalogModelPricing
}

func (s *redeemRepoStub) Create(ctx context.Context, code *billing.RedeemCode) error {
	panic("unexpected Create call")
}

func (s *redeemRepoStub) CreateBatch(ctx context.Context, codes []billing.RedeemCode) error {
	panic("unexpected CreateBatch call")
}

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

func (s *redeemRepoStub) GetByIDForUpdate(ctx context.Context, id int64) (*billing.RedeemCode, error) {
	s.lockedGetIDs = append(s.lockedGetIDs, id)
	return s.GetByID(ctx, id)
}

func (s *redeemRepoStub) GetByCode(ctx context.Context, code string) (*billing.RedeemCode, error) {
	panic("unexpected GetByCode call")
}

func (s *redeemRepoStub) GetByCodeForUpdate(ctx context.Context, code string) (*billing.RedeemCode, error) {
	panic("unexpected GetByCodeForUpdate call")
}

func (s *redeemRepoStub) Update(ctx context.Context, code *billing.RedeemCode) error {
	s.updatedCodes = append(s.updatedCodes, code)
	if s.codesByID == nil {
		s.codesByID = make(map[int64]*billing.RedeemCode)
	}
	cloned := *code
	s.codesByID[code.ID] = &cloned
	return s.updateErr
}

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

func (s *redeemRepoStub) Delete(ctx context.Context, id int64) error {
	s.deletedIDs = append(s.deletedIDs, id)
	if s.deleteErrByID != nil {
		if err, ok := s.deleteErrByID[id]; ok {
			return err
		}
	}
	return nil
}

func (s *redeemRepoStub) Use(ctx context.Context, id, userID int64) error {
	panic("unexpected Use call")
}

func (s *redeemRepoStub) CreateUsage(ctx context.Context, usage *billing.RedeemCodeUsage) error {
	return nil
}

func (s *redeemRepoStub) GetUsageByRedeemCodeAndUser(ctx context.Context, redeemCodeID, userID int64) (*billing.RedeemCodeUsage, error) {
	return nil, nil
}

func (s *redeemRepoStub) List(ctx context.Context, params pagination.PaginationParams) ([]billing.RedeemCode, *pagination.PaginationResult, error) {
	panic("unexpected List call")
}

func (s *redeemRepoStub) ListWithFilters(ctx context.Context, params pagination.PaginationParams, codeType, status, search string) ([]billing.RedeemCode, *pagination.PaginationResult, error) {
	panic("unexpected ListWithFilters call")
}

func (s *redeemRepoStub) ListByUserPaginated(ctx context.Context, userID int64, params pagination.PaginationParams, codeType string) ([]billing.RedeemCode, *pagination.PaginationResult, error) {
	panic("unexpected ListByUserPaginated call")
}

func (s *redeemRepoStub) SumPositiveBalanceByUser(ctx context.Context, userID int64) (float64, error) {
	panic("unexpected SumPositiveBalanceByUser call")
}

// newCalculator constructs a calculator with the test catalog.
func newCalculator(catalog *provider.Service) *billing.Calculator {
	return newCalculatorWithPrices(catalog, nil)
}

func newCalculatorWithPrices(catalog *provider.Service, prices map[string]*pricing.ModelPricing) *billing.Calculator {
	return testkit.Calculator(catalog, prices)
}

func (s *settingsPrices) GetEffectiveBillingSettings(context.Context, int64) pricing.BillingSettings {
	return s.settings.Clone()
}

func (s *settingsPrices) GetEffectiveConfigModelPricing(_ context.Context, _ int64, model string) *pricing.ModelPricingEntry {
	return pricing.MatchPriceCard(s.cards, model)
}

func settingsResolver(calculator *billing.Calculator, settings pricing.BillingSettings, cards []routing.ModelPricingEntry) (*billing.PriceResolver, *settingsPrices) {
	source := &settingsPrices{settings: settings, cards: cards}
	return billing.NewPriceResolver(source, calculator, nil, nil), source
}

func newCatalogFixture(fixture catalogFixture) *provider.Service {
	return provider.NewServiceFromSnapshot(provider.Options{
		ModelLookupCandidates: modelidentity.CandidatesFactory,
	}, nil, provider.Snapshot{Data: fixture.pricingData})
}

// testPtrFloat64 returns a pointer to the supplied value.
func testPtrFloat64(v float64) *float64 { return &v }

func ptrTime(t time.Time) *time.Time { return &t }

func revokeSubscriptionFixture() *billing.UserSubscription {
	now := time.Now().UTC()
	// Start at today's midnight so the fixture remains in the current daily window during the first hour of the day.
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

func quotaPointer(value float64) *float64 {
	return &value
}
