package testkit

import (
	"context"
	"log/slog"
	"time"

	pricingprovider "github.com/TokenFlux/TokenRouter/internal/billing/provider"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// pricing_config_repository.go supplies callback-based pricing repository
// stubs and configured service fixtures.
// Combined rows let tests exercise production price-cache and group-policy reads
// with controlled results. NewConfigServiceFixture uses ListAllFn for both
// inputs; direct stub calls expose the remaining callbacks and their documented
// defaults.

// PricingConfigRepositoryStub supplies per-operation callbacks and default
// results for pricing-service tests. Its methods require a nonnil stub
// receiver.
type PricingConfigRepositoryStub struct {
	// ListAllFn, GetGroupPlatformsFn, CreateFn, GetByIDFn, UpdateFn, DeleteFn,
	// ListFn, ExistsByNameFn, ExistsByNameExcludingFn, GetGroupIDsFn, SetGroupIDsFn,
	// GetPricingConfigIDByGroupIDFn, GetGroupsInOtherPricingConfigsFn,
	// ListModelPricingFn, CreateModelPricingFn, UpdateModelPricingFn,
	// DeleteModelPricingFn, and ReplaceModelPricingFn run synchronously when set,
	// using their matching method's supplied arguments. Each nil field selects
	// that method's documented default; state retained by callbacks belongs to
	// the test.
	// complete fixture row read callback
	ListAllFn func(ctx context.Context) ([]Configuration, error)
	// fixture group-platform read callback
	GetGroupPlatformsFn func(ctx context.Context, groupIDs []int64) (map[int64]string, error)
	// configuration insertion result callback
	CreateFn func(ctx context.Context, pricingConfig *Configuration) error
	// configuration identity read callback
	GetByIDFn func(ctx context.Context, id int64) (*Configuration, error)
	// configuration replacement result callback
	UpdateFn func(ctx context.Context, pricingConfig *Configuration) error
	// configuration deletion result callback
	DeleteFn func(ctx context.Context, id int64) error
	// filtered fixture page callback
	ListFn func(ctx context.Context, params pagination.PaginationParams, status, search string) ([]Configuration, *pagination.PaginationResult, error)
	// configuration name existence callback
	ExistsByNameFn func(ctx context.Context, name string) (bool, error)
	// other configuration name callback
	ExistsByNameExcludingFn func(ctx context.Context, name string, excludeID int64) (bool, error)
	// configuration group association callback
	GetGroupIDsFn func(ctx context.Context, pricingConfigID int64) ([]int64, error)
	// group association replacement callback
	SetGroupIDsFn func(ctx context.Context, pricingConfigID int64, groupIDs []int64) error
	// group configuration identity callback
	GetPricingConfigIDByGroupIDFn func(ctx context.Context, groupID int64) (int64, error)
	// group association conflict callback
	GetGroupsInOtherPricingConfigsFn func(ctx context.Context, pricingConfigID int64, groupIDs []int64) ([]int64, error)
	// configuration model-price read callback
	ListModelPricingFn func(ctx context.Context, pricingConfigID int64) ([]routing.ModelPricingEntry, error)
	// model-price insertion result callback
	CreateModelPricingFn func(ctx context.Context, pricing *routing.ModelPricingEntry) error
	// model-price replacement result callback
	UpdateModelPricingFn func(ctx context.Context, pricing *routing.ModelPricingEntry) error
	// model-price deletion result callback
	DeleteModelPricingFn func(ctx context.Context, id int64) error
	// complete model-price replacement callback
	ReplaceModelPricingFn func(ctx context.Context, pricingConfigID int64, pricingList []routing.ModelPricingEntry) error
}

// Create invokes CreateFn with the supplied fixture when configured and returns
// its error. Otherwise it reports success without changing the fixture or
// maintaining any repository state for later reads.
func (m *PricingConfigRepositoryStub) Create(ctx context.Context, pricingConfig *Configuration) error {
	if m.CreateFn != nil {
		return m.CreateFn(ctx, pricingConfig)
	}
	return nil
}

// GetByID invokes GetByIDFn when configured and returns the callback's value
// and error directly. Without a callback it returns ErrPricingConfigNotFound,
// so successful fixture reads require test-supplied results.
func (m *PricingConfigRepositoryStub) GetByID(ctx context.Context, id int64) (*Configuration, error) {
	if m.GetByIDFn != nil {
		return m.GetByIDFn(ctx, id)
	}
	return nil, routing.ErrPricingConfigNotFound
}

// Update invokes UpdateFn with the supplied fixture when configured and returns
// its error. Otherwise it reports success without persisting values, and later
// reads continue to use their configured callbacks.
func (m *PricingConfigRepositoryStub) Update(ctx context.Context, pricingConfig *Configuration) error {
	if m.UpdateFn != nil {
		return m.UpdateFn(ctx, pricingConfig)
	}
	return nil
}

// Delete invokes DeleteFn with the supplied ID when configured and returns its
// error. Otherwise it reports success without removing fixture values, so tests
// model subsequent reads through their callbacks.
func (m *PricingConfigRepositoryStub) Delete(ctx context.Context, id int64) error {
	if m.DeleteFn != nil {
		return m.DeleteFn(ctx, id)
	}
	return nil
}

// List invokes ListFn with pagination, status, and search inputs when
// configured. Otherwise it returns nil rows and pagination without an error,
// requiring tests that inspect pages to supply their result.
func (m *PricingConfigRepositoryStub) List(ctx context.Context, params pagination.PaginationParams, status, search string) ([]Configuration, *pagination.PaginationResult, error) {
	if m.ListFn != nil {
		return m.ListFn(ctx, params, status, search)
	}
	return nil, nil, nil
}

// ListAll invokes ListAllFn when configured and returns its fixture slice and
// error directly. Otherwise it returns nil rows without an error, allowing
// service cache construction from an empty fixture set.
func (m *PricingConfigRepositoryStub) ListAll(ctx context.Context) ([]Configuration, error) {
	if m.ListAllFn != nil {
		return m.ListAllFn(ctx)
	}
	return nil, nil
}

// ExistsByName invokes ExistsByNameFn for the supplied name when configured and
// returns its result. Otherwise it returns false without an error, so tests
// supply a callback to reproduce existing names.
func (m *PricingConfigRepositoryStub) ExistsByName(ctx context.Context, name string) (bool, error) {
	if m.ExistsByNameFn != nil {
		return m.ExistsByNameFn(ctx, name)
	}
	return false, nil
}

// ExistsByNameExcluding invokes its callback with the name and excluded ID when
// configured. Otherwise it returns false without an error, so tests supply
// results for conflicting configuration names.
func (m *PricingConfigRepositoryStub) ExistsByNameExcluding(ctx context.Context, name string, excludeID int64) (bool, error) {
	if m.ExistsByNameExcludingFn != nil {
		return m.ExistsByNameExcludingFn(ctx, name, excludeID)
	}
	return false, nil
}

// GetGroupIDs invokes GetGroupIDsFn for the configuration ID when configured
// and returns its slice directly. Otherwise it returns nil without an error,
// modeling no group associations for that read.
func (m *PricingConfigRepositoryStub) GetGroupIDs(ctx context.Context, pricingConfigID int64) ([]int64, error) {
	if m.GetGroupIDsFn != nil {
		return m.GetGroupIDsFn(ctx, pricingConfigID)
	}
	return nil, nil
}

// SetGroupIDs invokes SetGroupIDsFn with the configuration ID and supplied
// groups when configured. Otherwise it reports success without storing links,
// so subsequent association reads depend on their own callbacks.
func (m *PricingConfigRepositoryStub) SetGroupIDs(ctx context.Context, pricingConfigID int64, groupIDs []int64) error {
	if m.SetGroupIDsFn != nil {
		return m.SetGroupIDsFn(ctx, pricingConfigID, groupIDs)
	}
	return nil
}

// GetPricingConfigIDByGroupID invokes its callback for the supplied group ID
// when configured. Otherwise it returns zero without an error, modeling a group
// with no associated pricing configuration.
func (m *PricingConfigRepositoryStub) GetPricingConfigIDByGroupID(ctx context.Context, groupID int64) (int64, error) {
	if m.GetPricingConfigIDByGroupIDFn != nil {
		return m.GetPricingConfigIDByGroupIDFn(ctx, groupID)
	}
	return 0, nil
}

// GetGroupsInOtherPricingConfigs invokes its callback with the current
// configuration and requested groups when configured. Otherwise it returns nil
// without an error, modeling no detected association conflicts.
func (m *PricingConfigRepositoryStub) GetGroupsInOtherPricingConfigs(ctx context.Context, pricingConfigID int64, groupIDs []int64) ([]int64, error) {
	if m.GetGroupsInOtherPricingConfigsFn != nil {
		return m.GetGroupsInOtherPricingConfigsFn(ctx, pricingConfigID, groupIDs)
	}
	return nil, nil
}

// GetGroupPlatforms invokes GetGroupPlatformsFn with requested group IDs when
// configured and returns its map directly. Otherwise it returns nil without an
// error, leaving fixture platform metadata absent.
func (m *PricingConfigRepositoryStub) GetGroupPlatforms(ctx context.Context, groupIDs []int64) (map[int64]string, error) {
	if m.GetGroupPlatformsFn != nil {
		return m.GetGroupPlatformsFn(ctx, groupIDs)
	}
	return nil, nil
}

// ListModelPricing invokes ListModelPricingFn for the configuration ID when
// configured and returns its slice directly. Otherwise it returns nil without
// an error, modeling an empty model-price list.
func (m *PricingConfigRepositoryStub) ListModelPricing(ctx context.Context, pricingConfigID int64) ([]routing.ModelPricingEntry, error) {
	if m.ListModelPricingFn != nil {
		return m.ListModelPricingFn(ctx, pricingConfigID)
	}
	return nil, nil
}

// CreateModelPricing invokes CreateModelPricingFn with the supplied entry when
// configured and returns its error. Otherwise it reports success without
// assigning identity or storing a price for later fixture reads.
func (m *PricingConfigRepositoryStub) CreateModelPricing(ctx context.Context, pricing *routing.ModelPricingEntry) error {
	if m.CreateModelPricingFn != nil {
		return m.CreateModelPricingFn(ctx, pricing)
	}
	return nil
}

// UpdateModelPricing invokes UpdateModelPricingFn with the supplied entry when
// configured and returns its error. Otherwise it reports success without
// changing fixture storage, so read results remain callback-defined.
func (m *PricingConfigRepositoryStub) UpdateModelPricing(ctx context.Context, pricing *routing.ModelPricingEntry) error {
	if m.UpdateModelPricingFn != nil {
		return m.UpdateModelPricingFn(ctx, pricing)
	}
	return nil
}

// DeleteModelPricing invokes DeleteModelPricingFn for the supplied entry ID
// when configured and returns its error. Otherwise it reports success without
// removing entries from any callback's model-price results.
func (m *PricingConfigRepositoryStub) DeleteModelPricing(ctx context.Context, id int64) error {
	if m.DeleteModelPricingFn != nil {
		return m.DeleteModelPricingFn(ctx, id)
	}
	return nil
}

// ReplaceModelPricing invokes ReplaceModelPricingFn with the configuration and
// replacement list when configured. Otherwise it reports success without
// retaining that list, so tests define later pricing reads through their
// callbacks.
func (m *PricingConfigRepositoryStub) ReplaceModelPricing(ctx context.Context, pricingConfigID int64, pricingList []routing.ModelPricingEntry) error {
	if m.ReplaceModelPricingFn != nil {
		return m.ReplaceModelPricingFn(ctx, pricingConfigID, pricingList)
	}
	return nil
}

// NewConfigServiceFixture builds pricing and group reads from a nonnil
// combined-row stub with warning logging, the current clock, and pricing
// timezone loading.
// Tests configure ListAllFn to supply those reads. Service administration needs
// a production repository because the read adapter's other repository methods
// reach its nil embedded repository.
func NewConfigServiceFixture(repo *PricingConfigRepositoryStub) *routing.PricingConfigService {
	return NewPricingConfigService(repo, nil, routing.PricingConfigOptions{Warn: slog.Warn, Now: time.Now, LoadLocation: pricingprovider.LoadPricingLocation})
}

// StandardPricingConfigRepository supplies one configuration and platform map
// through fixed read callbacks. Configuration collections and the map remain
// captured by reference; tests keep them stable while service reads use those
// inputs.
func StandardPricingConfigRepository(ch Configuration, groupPlatforms map[int64]string) *PricingConfigRepositoryStub {
	return &PricingConfigRepositoryStub{
		ListAllFn: func(_ context.Context) ([]Configuration, error) {
			return []Configuration{ch}, nil
		},
		GetGroupPlatformsFn: func(_ context.Context, _ []int64) (map[int64]string, error) {
			return groupPlatforms, nil
		},
	}
}
