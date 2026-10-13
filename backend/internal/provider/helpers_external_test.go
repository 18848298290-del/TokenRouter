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

// Spark fixtures clone records for direct, batch, and child lookups, while
// group queries return shallow values with borrowed references.
// providerServiceTestRepo copies supported credential maps for single reads and
// exposes stored pointers for batch reads, so tests distinguish prospective
// edits from changes to retained records or recorded patch history.

// sparkShadowValidatingGroupRepoStub supplies existence results for selected
// routing group identifiers.
type sparkShadowValidatingGroupRepoStub struct {
	routing.GroupRepository                // unused routing repository methods
	existing                map[int64]bool // configured group existence values
}

// shadowGroupsFixture converts routing groups into provider administration
// group references.
type shadowGroupsFixture struct {
	routing.GroupRepository // underlying routing fixture reads
}

// sparkShadowRepoStub retains shadow records, membership selections, and
// generated fixture identifiers.
type sparkShadowRepoStub struct {
	providercore.AdminStore // unused administrative store methods
	// providers and providersByID share each stored clone address. Create and
	// Update write both indexes, while Delete removes both; nextID supplies
	// identities and groupsOf independently appends memberships.
	providersByID map[int64]*providercore.Record // batch lookup record index
	nextID        int64                          // last assigned fixture identifier
	providers     map[int64]*providercore.Record // primary fixture record index
	groupsOf      map[int64][]int64              // appended provider group selections
}

// providerServiceTestRepo records administrative configuration operations under
// a fixture mutex.
type providerServiceTestRepo struct {
	providercore.AdminStore // unused administrative store methods
	// mu protects providers, updates, and bulkUpdates while fixture methods read
	// or mutate them. Some methods return retained pointers, so that lock does not
	// protect caller access after return.
	mu        sync.Mutex                     // fixture data mutation lock
	providers map[int64]*providercore.Record // retained provider record pointers
	// updates records submitted Extra patch maps directly. Tests keep those maps
	// stable when inspecting write history after the update returns.
	updates map[int64][]map[string]any
	// bulkUpdates records patch values with their referenced maps retained. Tests
	// inspect submitted updates rather than treating this history as an
	// independent clone.
	bulkUpdates []providercore.ProviderBulkUpdate
}

// cnProviderTestCredentials builds CN credential fixtures with a custom relay
// endpoint. Adaptive mode supplies protocol-specific addresses and excludes
// Responses for Zhipu so tests use platform-supported legacy selections.
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

// newProviderEditorForTest binds provider administration with clocks, seeds,
// and credential validation. It uses the first optional group dependency and
// discovers quota or duplicate support from the supplied fixture store.
func newProviderEditorForTest(repo providercore.AdminStore, groupPorts ...providercore.AdminGroups) *providercore.Admin {
	var groups providercore.AdminGroups
	if len(groupPorts) > 0 {
		groups = groupPorts[0]
	}
	quota, _ := repo.(providercore.ProviderQuotaResetter)
	duplicates, _ := repo.(providercore.DuplicateStore)
	return providercore.NewAdmin(repo, providercore.AdminOptions{Duplicates: duplicates, Groups: groups, Quotas: quota, ShadowModels: provideradapter.DefaultSparkShadowModels, Creation: providercore.CreationOptions{Now: time.Now, LoadLocation: time.LoadLocation, NewSeed: uuid.NewString}, Credentials: provideradapter.CreateCredentialHooks(nil, nil)})
}

// ExistsByIDs returns each requested fixture group's configured existence value
// in a new map.
func (s *sparkShadowValidatingGroupRepoStub) ExistsByIDs(_ context.Context, ids []int64) (map[int64]bool, error) {
	out := make(map[int64]bool, len(ids))
	for _, id := range ids {
		out[id] = s.existing[id]
	}
	return out, nil
}

// GetGroup converts the underlying routing lookup into a provider group
// reference. A nil routing record remains nil, and the lookup error is returned
// alongside the converted result.
func (g shadowGroupsFixture) GetGroup(ctx context.Context, id int64) (*providercore.GroupReference, error) {
	v, err := g.GetByID(ctx, id)
	return shadowGroupReferenceFixture(v), err
}

// ActiveGroups converts every active routing fixture group and ignores the
// platform argument. A nil slice stays nil, and the underlying error accompanies
// any available converted records.
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

// ValidateGroups delegates membership validation to routing using the embedded
// fixture repository.
func (g shadowGroupsFixture) ValidateGroups(ctx context.Context, ids []int64) error {
	return routing.ValidateGroupIDs(ctx, g.GroupRepository, ids)
}

// shadowGroupReferenceFixture converts routing identity and OAuth policy to a
// provider reference. Nil input returns nil; available input produces a new
// reference with the fields used by management.
func shadowGroupReferenceFixture(v *routing.Group) *providercore.GroupReference {
	if v == nil {
		return nil
	}
	return &providercore.GroupReference{ID: v.ID, Name: v.Name, RequireOAuthOnly: v.RequireOAuthOnly}
}

// newSparkShadowRepoStub creates empty shadow record indexes and membership
// storage for tests.
func newSparkShadowRepoStub() *sparkShadowRepoStub {
	return &sparkShadowRepoStub{
		nextID:        0,
		providers:     make(map[int64]*providercore.Record),
		groupsOf:      make(map[int64][]int64),
		providersByID: make(map[int64]*providercore.Record),
	}
}

// Create assigns the next fixture ID and stores one supported-container clone
// in both indexes. The assigned ID changes the supplied record, while stored
// JSON containers are isolated by CloneRecord.
func (s *sparkShadowRepoStub) Create(_ context.Context, provider *providercore.Record) error {
	s.nextID++
	provider.ID = s.nextID
	cp := *providercore.CloneRecord(provider)
	// Both lookup paths point at this one saved clone. Update and Delete keep
	// the indexes aligned so shadow tests observe the same provider through
	// either read path.
	s.providers[provider.ID] = &cp
	s.providersByID[provider.ID] = &cp
	return nil
}

// GetByID reads a supported-container record clone for an existing fixture ID.
// Missing IDs return ErrProviderNotFound, and callers can mutate copied JSON
// containers without changing the retained fixture record.
func (s *sparkShadowRepoStub) GetByID(_ context.Context, id int64) (*providercore.Record, error) {
	acc, ok := s.providers[id]
	if !ok {
		return nil, providercore.ErrProviderNotFound
	}
	return providercore.CloneRecord(acc), nil
}

// ListShadowsByParent returns cloned Spark children of the selected parent. The
// parent link and Spark quota dimension must both match; an empty match leaves
// the returned slice nil.
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

// BindGroups appends the submitted memberships to this fixture's retained
// selection. It does not model replacement, so tests using it inspect added
// bindings rather than production removal behavior.
func (s *sparkShadowRepoStub) BindGroups(_ context.Context, providerID int64, groupIDs []int64) error {
	s.groupsOf[providerID] = append(s.groupsOf[providerID], groupIDs...)
	return nil
}

// ListSchedulableByGroupID reads records associated with the fixture group and
// performs no runtime eligibility filtering. Returned records are value copies
// with their referenced fields still shared with fixture storage.
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

// ExistsByID reports whether the fixture's primary record index contains the
// provider.
func (s *sparkShadowRepoStub) ExistsByID(_ context.Context, id int64) (bool, error) {
	_, ok := s.providers[id]
	return ok, nil
}

// Update replaces both fixture indexes with a supported-container clone of an
// existing provider. Missing records return ErrProviderNotFound, and the two
// indexes share the same saved clone address.
func (s *sparkShadowRepoStub) Update(_ context.Context, provider *providercore.Record) error {
	if _, ok := s.providers[provider.ID]; !ok {
		return providercore.ErrProviderNotFound
	}
	cp := *providercore.CloneRecord(provider)
	// Both lookup paths point at this one saved clone. Update and Delete keep
	// the indexes aligned so shadow tests observe the same provider through
	// either read path.
	s.providers[provider.ID] = &cp
	s.providersByID[provider.ID] = &cp
	return nil
}

// Delete removes the provider identifier from both retained shadow fixture
// indexes.
func (s *sparkShadowRepoStub) Delete(_ context.Context, id int64) error {
	// Remove both indexes together in this fixture path so a batch lookup cannot
	// return a provider that a direct lookup already regards as deleted.
	delete(s.providers, id)
	delete(s.providersByID, id)
	return nil
}

// BatchUpdateLastUsed accepts test activity writes without changing retained
// fixture records.
func (s *sparkShadowRepoStub) BatchUpdateLastUsed(_ context.Context, _ map[int64]time.Time) error {
	return nil
}

// ListByGroup returns no provider records for this shadow fixture's unused list
// path.
func (s *sparkShadowRepoStub) ListByGroup(_ context.Context, _ int64) ([]providercore.Record, error) {
	return nil, nil
}

// ListWithFilters supplies no page or pagination result for this fixture's
// unused list path.
func (s *sparkShadowRepoStub) ListWithFilters(_ context.Context, _ pagination.PaginationParams, _, _, _, _ string, _ int64, _ string) ([]providercore.Record, *pagination.PaginationResult, error) {
	return nil, nil, nil
}

// GetByIDs returns clones for existing fixture identifiers in the supplied
// request order. Missing IDs are skipped, and repeated IDs produce repeated
// copied records for batch tests.
func (s *sparkShadowRepoStub) GetByIDs(_ context.Context, ids []int64) ([]*providercore.Record, error) {
	var out []*providercore.Record
	for _, id := range ids {
		if value, ok := s.providersByID[id]; ok {
			out = append(out, providercore.CloneRecord(value))
		}
	}
	return out, nil
}

// BulkUpdate reports zero affected rows and leaves fixture records unchanged.
// Administrative proxy propagation still uses individual writes, allowing tests
// to observe that separate shadow synchronization path.
func (*sparkShadowRepoStub) BulkUpdate(context.Context, []int64, providercore.ProviderBulkUpdate) (int64, error) {
	return 0, nil
}

// ResetQuotaUsedAndClearRateLimitCooldown accepts the quota reset used by
// parent eligibility tests.
func (*sparkShadowRepoStub) ResetQuotaUsedAndClearRateLimitCooldown(context.Context, int64) error {
	return nil
}

// Create stores a supported-container clone under the fixture mutex and assigns
// an ID when absent. The caller receives that identity, while an explicitly
// supplied ID selects its own map entry.
func (r *providerServiceTestRepo) Create(_ context.Context, provider *providercore.Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.providers == nil {
		r.providers = make(map[int64]*providercore.Record)
	}
	// Choose and save the fixture identity under the record-map mutex so
	// another retained-map mutation cannot intervene between ID selection and
	// installation.
	if provider.ID == 0 {
		provider.ID = int64(len(r.providers) + 1)
	}
	r.providers[provider.ID] = providercore.CloneRecord(provider)
	return nil
}

// Update stores a supported-container record clone under the fixture mutex. It
// allocates the record map when needed and models full replacement, rather than
// a configuration-aware latest-row merge.
func (r *providerServiceTestRepo) Update(_ context.Context, provider *providercore.Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.providers == nil {
		r.providers = make(map[int64]*providercore.Record)
	}
	r.providers[provider.ID] = providercore.CloneRecord(provider)
	return nil
}

// BulkUpdate records the submitted patch under the fixture mutex and reports
// the requested ID count. The patch's maps remain shared, and no target record
// mutation is simulated.
func (r *providerServiceTestRepo) BulkUpdate(_ context.Context, ids []int64, updates providercore.ProviderBulkUpdate) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Append this patch while holding the history lock so concurrent fixture
	// submissions retain complete entries for later batch-update assertions.
	r.bulkUpdates = append(r.bulkUpdates, updates)
	return int64(len(ids)), nil
}

// GetByID copies a fixture record and its supported credential and Extra
// containers under the mutex. Other pointer fields remain borrowed, missing IDs
// return ErrProviderNotFound, and the clone receives time-zone lookup.
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

// GetByIDs reads existing fixture pointers in requested order under the mutex.
// Returned records remain shared after unlock, so tests coordinate mutations
// rather than treating them as independent snapshots.
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

// UpdateExtra merges submitted keys and records the same update map under the
// mutex. Missing records fail; nested values and the recorded map remain shared
// with the caller after submission.
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
	// Merge the retained configuration and record its submitted patch under one
	// fixture lock so history assertions observe the same operation that
	// changed these settings.
	maps.Copy(provider.Extra, updates)
	if r.updates == nil {
		r.updates = make(map[int64][]map[string]any)
	}
	r.updates[id] = append(r.updates[id], updates)
	return nil
}

// FindByExtraField selects fixture records by direct interface-value equality
// under the mutex. The supplied value needs to support that comparison, and
// returned value copies retain referenced provider fields.
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

// ollamaUsageProvider creates a schedulable OpenAI API Key fixture at the
// Ollama endpoint.
func ollamaUsageProvider(id int64) *providercore.Record {
	return &providercore.Record{
		ID: id, Name: fmt.Sprintf("ollama-%d", id), Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey,
		Credentials: map[string]any{"base_url": "https://ollama.com", "api_key": fmt.Sprintf("key-%d", id)},
		Extra:       map[string]any{}, Status: billing.StatusActive, Schedulable: true, Concurrency: 1,
	}
}
