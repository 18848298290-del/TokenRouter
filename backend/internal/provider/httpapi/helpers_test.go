package httpapi

import (
	"context"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

const deprecatedLongContextBillingExtraKey = "openai_long_context_billing_enabled"

type grokImportAdminService struct {
	*managementMutationFixture
	mu     sync.Mutex
	nextID int64
}

// managementCreateFixture records creation inputs; unused embedded operations remain unimplemented.
type managementCreateFixture struct {
	ProviderManagement
	mu                sync.Mutex
	createdProviders  []*providercore.CreateProviderInput
	createProviderErr error
}

// managementListFixture supplies list and scoring candidates and records pagination and call counts.
type managementListFixture struct {
	ProviderManagement
	providers                                                               []providercore.Record
	providerSchedulerScoreFilterProviders                                   []providercore.Record
	openAISchedulerScorePoolProviders                                       []providercore.Record
	schedulerScoreFilterCalls, openAISchedulerScorePoolCalls, getGroupCalls int
	openAISchedulerScorePoolGroupIDs                                        []int64
	lastListProviders                                                       struct {
		platform, providerType, status, search, privacyMode, sortBy, sortOrder string
		groupID                                                                int64
		calls                                                                  int
	}
}

type availableModelsAdminService struct {
	*managementMutationFixture
	provider providercore.Record
}

// managementMutationFixture records configuration, credential, and invalidation inputs in its own state.
type managementMutationFixture struct {
	managementCreateFixture
	providers                   []providercore.Record
	updateProviderInput         *providercore.UpdateProviderInput
	updateProviderErr           error
	updateExtraCalls            []map[string]any
	clearProviderErrorIDs       []int64
	lastBulkUpdateProviderInput *providercore.BulkUpdateProvidersInput
	bulkUpdateProviderErr       error
}

func newGrokImportAdminService() *grokImportAdminService {
	return &grokImportAdminService{
		managementMutationFixture: newManagementMutationFixture(),
		nextID:                    500,
	}
}

func (s *grokImportAdminService) CreateProvider(_ context.Context, input *providercore.CreateProviderInput) (*providercore.Record, error) {
	s.mu.Lock()
	s.nextID++
	id := s.nextID
	s.mu.Unlock()
	return &providercore.Record{
		ID:          id,
		Name:        input.Name,
		Platform:    input.Platform,
		Type:        input.Type,
		Credentials: input.Credentials,
		Extra:       input.Extra,
		ProxyID:     input.ProxyID,
		Concurrency: input.Concurrency,
		Status:      billing.StatusActive,
		Schedulable: true,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}, nil
}

// newManagedRefreshFixture creates a refresh coordinator using the test's platform exchange functions.
func newManagedRefreshFixture(source interface {
	providercore.ManagedCredentialStore
	providercore.ManagedCredentialPrivacy
}, exchange *providercore.ManualCredentialExchange,
) *providercore.ManagedRefreshService {
	return providercore.NewManagedRefreshService(providercore.ManagedRefreshOptions{
		Store: source, Privacy: source, CacheKey: provideradapter.ManagedRefreshCacheKey,
		Coordinate: func(ctx context.Context, v *providercore.Record, _ string, apply func(context.Context, *providercore.Record) (*providercore.Record, string, error)) (*providercore.Record, string, error) {
			return apply(ctx, v)
		},
		Exchange: func(ctx context.Context, v *providercore.Record) (providercore.ManagedRefreshObservation, error) {
			credentials, missing, err := exchange.Refresh(ctx, v)
			return providercore.ManagedRefreshObservation{Credentials: credentials, ProjectIDMissing: missing}, err
		},
	})
}

func (s *managementCreateFixture) CreateProvider(_ context.Context, input *providercore.CreateProviderInput) (*providercore.Record, error) {
	s.mu.Lock()
	s.createdProviders = append(s.createdProviders, input)
	s.mu.Unlock()
	if s.createProviderErr != nil {
		return nil, s.createProviderErr
	}
	return &providercore.Record{ID: 300, Name: input.Name, Status: providercore.StatusActive}, nil
}

func (s *managementCreateFixture) ForceOpenAIPrivacy(context.Context, *providercore.Record) string {
	return ""
}

func (s *managementCreateFixture) ForceAntigravityPrivacy(context.Context, *providercore.Record) string {
	return ""
}

// newManagementCreateFixtureHandler connects management, batch creation, and response presentation.
func newManagementCreateFixtureHandler(source *managementCreateFixture) *ManagementHandler {
	presenter := NewRuntimePresenter(providercore.NewRuntimeStatusReader(providercore.RuntimeStatusOptions{}), source, nil)
	batch := providercore.NewManagementBatch(source, nil, providercore.ManagementCreationOptions{Privacy: source})
	return NewManagementHandler(source, ManagementOptions{Presenter: presenter, Privacy: source, Batch: batch})
}

func (s *managementListFixture) ListProviders(ctx context.Context, page, pageSize int, platform, providerType, status, search string, groupID int64, privacyMode string, sortBy, sortOrder string) ([]providercore.Record, int64, error) {
	s.lastListProviders.platform = platform
	s.lastListProviders.providerType = providerType
	s.lastListProviders.status = status
	s.lastListProviders.search = search
	s.lastListProviders.groupID = groupID
	s.lastListProviders.privacyMode = privacyMode
	s.lastListProviders.sortBy = sortBy
	s.lastListProviders.sortOrder = sortOrder
	s.lastListProviders.calls++
	providers := s.providers
	total := len(providers)
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = total
	}
	start := (page - 1) * pageSize
	if start >= total {
		return []providercore.Record{}, int64(total), nil
	}
	end := min(start+pageSize, total)
	return providers[start:end], int64(total), nil
}

func (s *managementListFixture) ListProvidersForSchedulerScoreFilter(_ context.Context, platform, providerType, status, search string, groupID int64, privacyMode string) ([]providercore.Record, error) {
	s.schedulerScoreFilterCalls++
	if s.providerSchedulerScoreFilterProviders != nil {
		return s.providerSchedulerScoreFilterProviders, nil
	}
	return s.providers, nil
}

func (s *managementListFixture) ListSchedulableProvidersForAdvancedSchedulerScore(_ context.Context, groupID *int64, platform string) ([]providercore.Record, error) {
	s.openAISchedulerScorePoolCalls++
	if groupID != nil {
		s.openAISchedulerScorePoolGroupIDs = append(s.openAISchedulerScorePoolGroupIDs, *groupID)
	}
	providers := s.openAISchedulerScorePoolProviders
	if providers == nil {
		providers = s.providers
	}
	out := make([]providercore.Record, 0, len(providers))
	for _, provider := range providers {
		if (platform != "" && provider.Platform != platform) || !provider.IsSchedulable() {
			continue
		}
		if groupID == nil {
			if len(provider.ProviderGroups) == 0 && len(provider.GroupIDs) == 0 {
				out = append(out, provider)
			}
			continue
		}
		for _, providerGroup := range provider.ProviderGroups {
			if providerGroup.GroupID == *groupID {
				out = append(out, provider)
				break
			}
		}
	}
	return out, nil
}

func newManagementListFixture() *managementListFixture {
	now := time.Now().UTC()
	return &managementListFixture{providers: []providercore.Record{{ID: 3, Name: "provider", Platform: providercore.PlatformAnthropic, Type: providercore.ProviderTypeOAuth, Status: providercore.StatusActive, CreatedAt: now, UpdatedAt: now}}}
}

func setupProviderMutationContractRouter(adminSvc *managementMutationFixture) *gin.Engine {
	router := gin.New()
	providerHandler := newMutationHandler(adminSvc, nil)
	router.POST("/api/v1/admin/providers", providerHandler.Create)
	router.PUT("/api/v1/admin/providers/:id", providerHandler.Update)
	router.POST("/api/v1/admin/providers/bulk-update", providerHandler.BulkUpdate)
	return router
}

func (s *availableModelsAdminService) GetProvider(_ context.Context, id int64) (*providercore.Record, error) {
	if s.provider.ID == id {
		acc := s.provider
		return &acc, nil
	}
	return s.managementMutationFixture.GetProvider(context.Background(), id)
}

func (s *managementMutationFixture) GetProvider(ctx context.Context, id int64) (*providercore.Record, error) {
	for i := range s.providers {
		if s.providers[i].ID == id {
			provider := s.providers[i]
			return &provider, nil
		}
	}
	provider := providercore.Record{ID: id, Name: "provider", Platform: capability.PlatformAnthropic, Type: capability.ProviderTypeOAuth, Status: billing.StatusActive}
	return &provider, nil
}

func (s *managementMutationFixture) GetProvidersByIDs(ctx context.Context, ids []int64) ([]*providercore.Record, error) {
	out := make([]*providercore.Record, 0, len(ids))
	for _, id := range ids {
		found := false
		for i := range s.providers {
			if s.providers[i].ID == id {
				provider := s.providers[i]
				out = append(out, &provider)
				found = true
				break
			}
		}
		if found {
			continue
		}
		provider := providercore.Record{ID: id, Name: "provider", Status: billing.StatusActive}
		out = append(out, &provider)
	}
	return out, nil
}

func (s *managementMutationFixture) UpdateProvider(ctx context.Context, id int64, input *providercore.UpdateProviderInput) (*providercore.Record, error) {
	// Check the expected credential version under the lock; omitted expectations allow an ordinary update.
	if input.ExpectedCredentials != nil {
		for i := range s.providers {
			if s.providers[i].ID == id && !providercore.MatchesCredentialVersion(&s.providers[i], *input.ExpectedCredentials) {
				return nil, providercore.ErrRefreshProviderStateChanged
			}
		}
	}

	if s.updateProviderErr != nil {
		return nil, s.updateProviderErr
	}
	s.updateProviderInput = input
	for i := range s.providers {
		if s.providers[i].ID == id {
			if input.Credentials != nil {
				if input.PatchCredentials {
					s.providers[i].Credentials = providercore.MergeCredentials(s.providers[i].Credentials, input.Credentials)
				} else {
					s.providers[i].Credentials = input.Credentials
				}
			}
			provider := s.providers[i]
			return &provider, nil
		}
	}
	provider := providercore.Record{ID: id, Name: input.Name, Platform: capability.PlatformAnthropic, Type: input.Type, Status: billing.StatusActive, Credentials: input.Credentials}
	return &provider, nil
}

func (s *managementMutationFixture) UpdateProviderExtra(ctx context.Context, id int64, updates map[string]any) error {
	s.updateExtraCalls = append(s.updateExtraCalls, updates)
	return nil
}

func (s *managementMutationFixture) ClearProviderError(ctx context.Context, id int64) (*providercore.Record, error) {
	s.clearProviderErrorIDs = append(s.clearProviderErrorIDs, id)
	provider := providercore.Record{ID: id, Name: "provider", Platform: capability.PlatformAnthropic, Type: capability.ProviderTypeOAuth, Status: billing.StatusActive}
	return &provider, nil
}

func (s *managementMutationFixture) BulkUpdateProviders(ctx context.Context, input *providercore.BulkUpdateProvidersInput) (*providercore.BulkUpdateProvidersResult, error) {
	s.lastBulkUpdateProviderInput = input
	if s.bulkUpdateProviderErr != nil {
		return nil, s.bulkUpdateProviderErr
	}
	return &providercore.BulkUpdateProvidersResult{Success: len(input.ProviderIDs), Failed: 0, SuccessIDs: input.ProviderIDs}, nil
}

func (s *managementMutationFixture) EnsureOpenAIPrivacy(ctx context.Context, provider *providercore.Record) string {
	return ""
}

func (s *managementMutationFixture) EnsureAntigravityPrivacy(ctx context.Context, provider *providercore.Record) string {
	return ""
}

func newManagementMutationFixture() *managementMutationFixture { return &managementMutationFixture{} }

// newMutationHandler connects management operations and response presentation.
func newMutationHandler(source *managementMutationFixture, invalidator providercore.TokenCacheInvalidator) *ManagementHandler {
	options := providercore.ManagedRefreshOptions{Store: source, Privacy: source}
	if invalidator != nil {
		options.Invalidate = invalidator.InvalidateToken
	}
	managed := providercore.NewManagedRefreshService(options)
	presenter := NewRuntimePresenter(providercore.NewRuntimeStatusReader(providercore.RuntimeStatusOptions{}), source, nil)
	batch := providercore.NewManagementBatch(source, managed, providercore.ManagementCreationOptions{Privacy: source})
	return NewManagementHandler(source, ManagementOptions{Managed: managed, Presenter: presenter, RuntimePresenter: presenter, Privacy: source, Batch: batch})
}
