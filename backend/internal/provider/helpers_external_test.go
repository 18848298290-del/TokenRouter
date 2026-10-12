package provider_test

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// sparkShadowValidatingGroupRepoStub implements batched group existence checks
// so group validation exercises ExistsByIDs.
type sparkShadowValidatingGroupRepoStub struct {
	routing.GroupRepository
	existing map[int64]bool
}

// shadowGroupsFixture adapts routing groups to provider management references.
type shadowGroupsFixture struct{ routing.GroupRepository }

// sparkShadowRepoStub stores providers, group bindings, and incrementing IDs for shadow creation tests.
// Embedded AdminStore methods outside this fixture's scope remain unimplemented.
type sparkShadowRepoStub struct {
	providercore.AdminStore
	providersByID map[int64]*providercore.Record
	nextID        int64
	providers     map[int64]*providercore.Record
	groupsOf      map[int64][]int64 // providerID → []groupIDs
}

// providerServiceTestRepo provides in-memory records for provider management tests.
type providerServiceTestRepo struct {
	providercore.AdminStore
	mu          sync.Mutex
	providers   map[int64]*providercore.Record
	updates     map[int64][]map[string]any
	bulkUpdates []providercore.ProviderBulkUpdate
}

// cnProviderTestCredentials supplies custom endpoints to check that saving retains the requested URLs.
func cnProviderTestCredentials(platform, mode, protocol string) map[string]any {
	credentials := map[string]any{
		"api_key":       "sk-test",
		"provider_mode": mode,
		"api_protocol":  protocol,
		"base_url":      "https://relay.example.test/v1",
	}
	if protocol == providercore.APIProtocolAdaptive {
		urls := map[string]any{
			providercore.APIProtocolChatCompletions: "https://relay.example.test/v1",
			providercore.APIProtocolAnthropic:       "https://relay.example.test/anthropic",
		}
		if platform != capability.PlatformZhipu {
			urls[providercore.APIProtocolResponses] = "https://relay.example.test/responses"
		}
		credentials["api_base_urls"] = urls
	}
	return credentials
}

// newProviderEditorForTest injects time, fingerprint seed generation, and platform credential validation.
func newProviderEditorForTest(repo providercore.AdminStore, groupPorts ...providercore.AdminGroups) *providercore.Admin {
	var groups providercore.AdminGroups
	if len(groupPorts) > 0 {
		groups = groupPorts[0]
	}
	quota, _ := repo.(providercore.ProviderQuotaResetter)
	duplicates, _ := repo.(providercore.DuplicateStore)
	return providercore.NewAdmin(repo, providercore.AdminOptions{Duplicates: duplicates, Groups: groups, Quotas: quota, ShadowModels: provideradapter.DefaultSparkShadowModels, Creation: providercore.CreationOptions{Now: time.Now, LoadLocation: time.LoadLocation, NewSeed: uuid.NewString}, Credentials: provideradapter.CreateCredentialHooks(nil, nil)})
}

func (s *sparkShadowValidatingGroupRepoStub) ExistsByIDs(_ context.Context, ids []int64) (map[int64]bool, error) {
	out := make(map[int64]bool, len(ids))
	for _, id := range ids {
		out[id] = s.existing[id]
	}
	return out, nil
}

func (g shadowGroupsFixture) GetGroup(ctx context.Context, id int64) (*providercore.GroupReference, error) {
	v, err := g.GetByID(ctx, id)
	return shadowGroupReferenceFixture(v), err
}

func (g shadowGroupsFixture) ActiveGroups(ctx context.Context, platform string) ([]providercore.GroupReference, error) {
	rows, err := g.ListActive(ctx)
	if rows == nil {
		return nil, err
	}
	out := make([]providercore.GroupReference, len(rows))
	for i := range rows {
		out[i] = *shadowGroupReferenceFixture(&rows[i])
	}
	return out, err
}

func (g shadowGroupsFixture) ValidateGroups(ctx context.Context, ids []int64) error {
	return routing.ValidateGroupIDs(ctx, g.GroupRepository, ids)
}

func shadowGroupReferenceFixture(v *routing.Group) *providercore.GroupReference {
	if v == nil {
		return nil
	}
	return &providercore.GroupReference{ID: v.ID, Name: v.Name, RequireOAuthOnly: v.RequireOAuthOnly}
}

func newSparkShadowRepoStub() *sparkShadowRepoStub {
	return &sparkShadowRepoStub{
		nextID:        0,
		providers:     make(map[int64]*providercore.Record),
		groupsOf:      make(map[int64][]int64),
		providersByID: make(map[int64]*providercore.Record),
	}
}

func (s *sparkShadowRepoStub) Create(_ context.Context, provider *providercore.Record) error {
	s.nextID++
	provider.ID = s.nextID
	cp := *providercore.CloneRecord(provider)
	s.providers[provider.ID] = &cp
	s.providersByID[provider.ID] = &cp
	return nil
}

func (s *sparkShadowRepoStub) GetByID(_ context.Context, id int64) (*providercore.Record, error) {
	acc, ok := s.providers[id]
	if !ok {
		return nil, providercore.ErrProviderNotFound
	}
	return providercore.CloneRecord(acc), nil
}

func (s *sparkShadowRepoStub) ListShadowsByParent(_ context.Context, parentID int64) ([]*providercore.Record, error) {
	var result []*providercore.Record
	for _, acc := range s.providers {
		if acc.ParentProviderID != nil && *acc.ParentProviderID == parentID && acc.QuotaDimension == providercore.QuotaDimensionSpark {
			cp := *providercore.CloneRecord(acc)
			result = append(result, &cp)
		}
	}
	return result, nil
}

func (s *sparkShadowRepoStub) BindGroups(_ context.Context, providerID int64, groupIDs []int64) error {
	s.groupsOf[providerID] = append(s.groupsOf[providerID], groupIDs...)
	return nil
}

func (s *sparkShadowRepoStub) ListSchedulableByGroupID(_ context.Context, groupID int64) ([]providercore.Record, error) {
	var result []providercore.Record
	for accID, groups := range s.groupsOf {
		if slices.Contains(groups, groupID) {
			if acc, ok := s.providers[accID]; ok {
				result = append(result, *acc)
			}
		}
	}
	return result, nil
}

// ExistsByID reports whether the fixture contains the provider.
func (s *sparkShadowRepoStub) ExistsByID(_ context.Context, id int64) (bool, error) {
	_, ok := s.providers[id]
	return ok, nil
}

func (s *sparkShadowRepoStub) Update(_ context.Context, provider *providercore.Record) error {
	if _, ok := s.providers[provider.ID]; !ok {
		return providercore.ErrProviderNotFound
	}
	cp := *providercore.CloneRecord(provider)
	s.providers[provider.ID] = &cp
	s.providersByID[provider.ID] = &cp
	return nil
}

func (s *sparkShadowRepoStub) Delete(_ context.Context, id int64) error {
	delete(s.providers, id)
	delete(s.providersByID, id)
	return nil
}

func (s *sparkShadowRepoStub) BatchUpdateLastUsed(_ context.Context, _ map[int64]time.Time) error {
	return nil
}

func (s *sparkShadowRepoStub) ListByGroup(_ context.Context, _ int64) ([]providercore.Record, error) {
	return nil, nil
}

func (s *sparkShadowRepoStub) ListWithFilters(_ context.Context, _ pagination.PaginationParams, _, _, _, _ string, _ int64, _ string) ([]providercore.Record, *pagination.PaginationResult, error) {
	return nil, nil, nil
}

// GetByIDs returns existing fixture records in request order.
func (s *sparkShadowRepoStub) GetByIDs(_ context.Context, ids []int64) ([]*providercore.Record, error) {
	var out []*providercore.Record
	for _, id := range ids {
		if value, ok := s.providersByID[id]; ok {
			out = append(out, providercore.CloneRecord(value))
		}
	}
	return out, nil
}

// BulkUpdate reports no affected rows; the management use case performs shadow synchronization.
func (*sparkShadowRepoStub) BulkUpdate(context.Context, []int64, providercore.ProviderBulkUpdate) (int64, error) {
	return 0, nil
}

func (*sparkShadowRepoStub) ResetQuotaUsedAndClearRateLimitCooldown(context.Context, int64) error {
	return nil
}

func (r *providerServiceTestRepo) Create(_ context.Context, provider *providercore.Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.providers == nil {
		r.providers = make(map[int64]*providercore.Record)
	}
	if provider.ID == 0 {
		provider.ID = int64(len(r.providers) + 1)
	}
	r.providers[provider.ID] = providercore.CloneRecord(provider)
	return nil
}

func (r *providerServiceTestRepo) Update(_ context.Context, provider *providercore.Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.providers == nil {
		r.providers = make(map[int64]*providercore.Record)
	}
	r.providers[provider.ID] = providercore.CloneRecord(provider)
	return nil
}

func (r *providerServiceTestRepo) BulkUpdate(_ context.Context, ids []int64, updates providercore.ProviderBulkUpdate) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bulkUpdates = append(r.bulkUpdates, updates)
	return int64(len(ids)), nil
}

func (r *providerServiceTestRepo) GetByID(_ context.Context, id int64) (*providercore.Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	provider := r.providers[id]
	if provider == nil {
		return nil, providercore.ErrProviderNotFound
	}
	clone := *provider
	clone.Credentials = providercore.CRSMergeMap(nil, provider.Credentials)
	clone.Extra = providercore.CRSMergeMap(nil, provider.Extra)
	clone.LoadLocation = time.LoadLocation
	return &clone, nil
}

func (r *providerServiceTestRepo) GetByIDs(_ context.Context, ids []int64) ([]*providercore.Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]*providercore.Record, 0, len(ids))
	for _, id := range ids {
		if provider := r.providers[id]; provider != nil {
			result = append(result, provider)
		}
	}
	return result, nil
}

func (r *providerServiceTestRepo) UpdateExtra(_ context.Context, id int64, updates map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	provider := r.providers[id]
	if provider == nil {
		return providercore.ErrProviderNotFound
	}
	if provider.Extra == nil {
		provider.Extra = make(map[string]any)
	}
	maps.Copy(provider.Extra, updates)
	if r.updates == nil {
		r.updates = make(map[int64][]map[string]any)
	}
	r.updates[id] = append(r.updates[id], updates)
	return nil
}

func (r *providerServiceTestRepo) FindByExtraField(_ context.Context, key string, value any) ([]providercore.Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]providercore.Record, 0)
	for _, provider := range r.providers {
		if provider.Extra != nil && provider.Extra[key] == value {
			result = append(result, *provider)
		}
	}
	return result, nil
}

func ollamaUsageProvider(id int64) *providercore.Record {
	return &providercore.Record{
		ID: id, Name: fmt.Sprintf("ollama-%d", id), Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey,
		Credentials: map[string]any{"base_url": "https://ollama.com", "api_key": fmt.Sprintf("key-%d", id)},
		Extra:       map[string]any{}, Status: billing.StatusActive, Schedulable: true, Concurrency: 1,
	}
}
