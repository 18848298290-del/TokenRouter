//go:build integration

package provider_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/providergroup"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	billingpostgres "github.com/TokenFlux/TokenRouter/internal/billing/postgres"
	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/infra/postgres"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	providerpostgres "github.com/TokenFlux/TokenRouter/internal/provider/postgres"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/scheduler/rediscache/codec"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// Provider persistence connects configuration and health changes with scheduler
// events and immediately published snapshots. Rollback-owned PostgreSQL suite
// cases compare stored records, group memberships, eligibility queries, and the
// snapshot recorder used by each write path.
//
// Conditional Grok writes additionally exercise stale credential observations,
// a forced outbox failure inside one SQL statement, and caller cancellation
// immediately after that statement succeeds. The SQL wrappers inject those
// conditions at the execution point, while a standalone committed-row test
// checks rollback through a fresh read. Snapshot assertions inspect the context
// received by publication; the fixture records its error state without modeling
// a complete scheduler cache.

// ProviderRepoSuite shares transaction-owned persistence fixtures across
// provider repository assertion methods.
type ProviderRepoSuite struct {
	suite.Suite // test assertions and runner
	// ctx, client, repo, and sql belong to the transaction created by SetupTest.
	// testEntTx registers rollback so persisted fixture changes disappear after
	// the current suite method finishes.
	ctx    context.Context                    // current test query context
	client *dbent.Client                      // transaction-bound Ent client
	repo   *providerpostgres.ProviderStore    // provider store under test
	sql    postgres.Executor                  // transaction-bound SQL executor
	cache  scheduler.SnapshotPublicationCache // optional publication assertion cache
}

// schedulerCacheRecorder records publication calls and latest provider pointers
// for synchronous assertions.
type schedulerCacheRecorder struct {
	// setProviders and providers retain the published record pointers for
	// synchronous assertions. They share those records, so tests inspect them
	// after publication rather than modeling independent cache ownership. Fixture
	// callers serialize access to the recorder because it has no synchronization.
	setProviders []*providercore.Record         // records in publication order
	deleteIDs    []int64                        // IDs in deletion order
	providers    map[int64]*providercore.Record // latest fixture record per ID
	setCtxErr    error                          // latest publication context error
}

// failAtomicSchedulerOutboxSQLExecutor injects a PostgreSQL constraint
// violation into combined mutation/event statements.
type failAtomicSchedulerOutboxSQLExecutor struct {
	postgres.Executor // delegated fixture SQL executor
}

// cancelAfterAtomicMutationSQLExecutor cancels a caller after a combined
// database mutation/event statement succeeds.
type cancelAfterAtomicMutationSQLExecutor struct {
	postgres.Executor // delegated fixture SQL executor
	// cancel ends the caller context after a successful combined mutation/event
	// statement; the fixture provides it before executing the selected SQL path.
	cancel context.CancelFunc
}

// SetProvider unwraps and records the published provider and the publication
// context's error. Successful nonnil records also update the fixture map;
// pointers remain shared and callers inspect them after the synchronous
// publisher completes.
func (s *schedulerCacheRecorder) SetProvider(ctx context.Context, value scheduler.SnapshotProvider) error {
	provider, err := codec.RecordValue(value)
	if err != nil {
		return err
	}
	s.setCtxErr = ctx.Err()
	s.setProviders = append(s.setProviders, provider)
	if s.providers == nil {
		s.providers = make(map[int64]*providercore.Record)
	}
	if provider != nil {
		s.providers[provider.ID] = provider
	}
	return nil
}

// ExecContext delegates SQL after replacing the final argument of an atomic
// mutation/event statement with nil. That argument is event_type in the
// exercised statement, whose NOT NULL violation lets PostgreSQL prove that the
// earlier mutation also fails.
func (e *failAtomicSchedulerOutboxSQLExecutor) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if strings.Contains(query, "WITH updated AS") && strings.Contains(query, "INSERT INTO scheduler_outbox") && len(args) > 0 {
		args = append([]any(nil), args...)
		// Replace the event argument on a copied slice so the caller keeps its
		// arguments while the combined statement fails its NOT NULL constraint.
		args[len(args)-1] = nil
	}
	return e.Executor.ExecContext(ctx, query, args...)
}

// ExecContext executes SQL, then cancels after a successful combined
// mutation/event statement. It returns the original result and error, allowing
// the test to observe publication's context after the caller context has been
// canceled.
func (e *cancelAfterAtomicMutationSQLExecutor) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	result, err := e.Executor.ExecContext(ctx, query, args...)
	if err == nil && strings.Contains(query, "WITH updated AS") && strings.Contains(query, "INSERT INTO scheduler_outbox") {
		e.cancel()
	}
	return result, err
}

// DeleteProvider records the removal ID and deletes any fixture-map entry. It
// succeeds for absent IDs; callers inspect the accumulated removals after the
// synchronous publication operation.
func (s *schedulerCacheRecorder) DeleteProvider(ctx context.Context, providerID int64) error {
	s.deleteIDs = append(s.deleteIDs, providerID)
	if s.providers != nil {
		delete(s.providers, providerID)
	}
	return nil
}

// SetupTest binds the suite's Ent client and SQL executor to a fresh
// rollback-owned transaction. It resets the cache to nil and creates the
// provider store with the same transaction for each case.
func (s *ProviderRepoSuite) SetupTest() {
	s.ctx = context.Background()
	tx := testEntTx(s.T())
	s.client = tx.Client()
	s.sql = tx
	s.cache = nil
	s.repo = newProviderStoreContract(s.client, tx, nil)
}

// TestProviderRepoSuite runs provider persistence and scheduler-publication
// cases with independent PostgreSQL transactions. The suite compares
// configuration, health state, membership queries, event rows, and recorded
// snapshot effects for each tested path.
func TestProviderRepoSuite(t *testing.T) {
	suite.Run(t, new(ProviderRepoSuite))
}

// TestCreate inserts an OAuth provider through the store and reads it back. The
// returned database identity must be nonzero and the stored name must match the
// input.
func (s *ProviderRepoSuite) TestCreate() {
	provider := &providercore.Record{
		Name:        "test-create",
		Platform:    capability.PlatformAnthropic,
		Type:        capability.ProviderTypeOAuth,
		Status:      billing.StatusActive,
		Credentials: map[string]any{},
		Extra:       map[string]any{},
		Concurrency: 3,
		Priority:    50,
		Schedulable: true,
	}

	err := s.repo.Create(s.ctx, provider)
	s.Require().NoError(err, "Create")
	s.Require().NotZero(provider.ID, "expected ID to be set")

	got, err := s.repo.GetByID(s.ctx, provider.ID)
	s.Require().NoError(err, "GetByID")
	s.Require().Equal("test-create", got.Name)
}

// TestGetByID_NotFound asks the store for an ID absent from the transaction
// fixture. The read returns an error rather than a fabricated provider record.
func (s *ProviderRepoSuite) TestGetByID_NotFound() {
	_, err := s.repo.GetByID(s.ctx, 999999)
	s.Require().Error(err, "expected error for non-existent ID")
}

// TestUpdate creates a provider, changes its name, and saves it through the
// store. Reading the same provider ID must return the requested replacement
// name.
func (s *ProviderRepoSuite) TestUpdate() {
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "original"})

	provider.Name = "updated"
	err := s.repo.Update(s.ctx, provider)
	s.Require().NoError(err, "Update")

	got, err := s.repo.GetByID(s.ctx, provider.ID)
	s.Require().NoError(err, "GetByID after update")
	s.Require().Equal("updated", got.Name)
}

// TestUpdate_SyncSchedulerSnapshotOnDisabled saves a disabled status while a
// snapshot recorder is attached. One recorded publication identifies the
// updated provider and exposes the disabled status.
func (s *ProviderRepoSuite) TestUpdate_SyncSchedulerSnapshotOnDisabled() {
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "sync-update", Status: billing.StatusActive, Schedulable: true})
	cacheRecorder := &schedulerCacheRecorder{}
	s.cache = cacheRecorder
	s.repo.SetEvents(providerPublicationEvents(s.repo, cacheRecorder))

	provider.Status = billing.StatusDisabled
	err := s.repo.Update(s.ctx, provider)
	s.Require().NoError(err, "Update")

	s.Require().Len(cacheRecorder.setProviders, 1)
	s.Require().Equal(provider.ID, cacheRecorder.setProviders[0].ID)
	s.Require().Equal(billing.StatusDisabled, cacheRecorder.setProviders[0].Status)
}

// TestUpdate_SyncSchedulerSnapshotOnCredentialsChange changes the target in a
// model-mapping credential. The single recorded snapshot uses the provider's ID
// and contains the updated mapping target.
func (s *ProviderRepoSuite) TestUpdate_SyncSchedulerSnapshotOnCredentialsChange() {
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{
		Name:        "sync-credentials-update",
		Status:      billing.StatusActive,
		Schedulable: true,
		Credentials: map[string]any{
			"model_mapping": map[string]any{
				"gpt-5": "gpt-5.1",
			},
		},
	})
	cacheRecorder := &schedulerCacheRecorder{}
	s.cache = cacheRecorder
	s.repo.SetEvents(providerPublicationEvents(s.repo, cacheRecorder))

	provider.Credentials = map[string]any{
		"model_mapping": map[string]any{
			"gpt-5": "gpt-5.2",
		},
	}
	err := s.repo.Update(s.ctx, provider)
	s.Require().NoError(err, "Update")

	s.Require().Len(cacheRecorder.setProviders, 1)
	s.Require().Equal(provider.ID, cacheRecorder.setProviders[0].ID)
	mapping, ok := cacheRecorder.setProviders[0].Credentials["model_mapping"].(map[string]any)
	s.Require().True(ok)
	s.Require().Equal("gpt-5.2", mapping["gpt-5"])
}

// TestUpdateCredentials_SyncsSnapshotAndDurableOutbox replaces an access token
// after isolating the transaction's outbox assertions. The recorder receives
// the new token and one provider-change event exists in the database.
func (s *ProviderRepoSuite) TestUpdateCredentials_SyncsSnapshotAndDurableOutbox() {
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{
		Name:        "sync-refresh-credentials",
		Status:      billing.StatusActive,
		Schedulable: true,
		Credentials: map[string]any{"access_token": "old-token"},
	})
	cacheRecorder := &schedulerCacheRecorder{}
	s.cache = cacheRecorder
	s.repo.SetEvents(providerPublicationEvents(s.repo, cacheRecorder))
	// Clear existing event rows inside the suite transaction so the next count
	// belongs to this operation; fixture rollback restores the shared outbox afterward.
	_, err := s.sql.ExecContext(s.ctx, "TRUNCATE scheduler_outbox")
	s.Require().NoError(err)

	s.Require().NoError(s.repo.UpdateCredentials(s.ctx, provider.ID, map[string]any{"access_token": "new-token"}))

	s.Require().Len(cacheRecorder.setProviders, 1)
	s.Require().Equal("new-token", cacheRecorder.setProviders[0].GetCredential("access_token"))
	var outboxCount int
	err = postgres.ScanSingleRow(
		s.ctx,
		s.sql,
		"SELECT COUNT(*) FROM scheduler_outbox WHERE event_type = $1 AND provider_id = $2",
		[]any{scheduler.SchedulerOutboxEventProviderChanged, provider.ID},
		&outboxCount,
	)
	s.Require().NoError(err)
	s.Require().Equal(1, outboxCount)
}

// TestDelete removes a provider previously inserted into the transaction
// fixture. A subsequent lookup of its assigned ID returns an error through the
// repository.
func (s *ProviderRepoSuite) TestDelete() {
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "to-delete"})

	err := s.repo.Delete(s.ctx, provider.ID)
	s.Require().NoError(err, "Delete")

	_, err = s.repo.GetByID(s.ctx, provider.ID)
	s.Require().Error(err, "expected error after delete")
}

// TestDelete_RemovesSchedulerProviderSnapshot seeds a fixture cache entry
// before deleting its provider. The recorder receives that removal ID and its
// provider map no longer contains the entry.
func (s *ProviderRepoSuite) TestDelete_RemovesSchedulerProviderSnapshot() {
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "to-delete-cache"})
	cacheRecorder := &schedulerCacheRecorder{
		providers: map[int64]*providercore.Record{
			provider.ID: {
				ID:          provider.ID,
				Name:        provider.Name,
				Status:      billing.StatusActive,
				Schedulable: true,
			},
		},
	}
	s.cache = cacheRecorder
	s.repo.SetEvents(providerPublicationEvents(s.repo, cacheRecorder))

	err := s.repo.Delete(s.ctx, provider.ID)
	s.Require().NoError(err, "Delete")

	s.Require().Equal([]int64{provider.ID}, cacheRecorder.deleteIDs)
	s.Require().NotContains(cacheRecorder.providers, provider.ID)
}

// TestDelete_WithGroupBindings persists a provider/group membership before
// deleting the provider. The transaction's membership query returns zero rows
// for that provider after deletion.
func (s *ProviderRepoSuite) TestDelete_WithGroupBindings() {
	group := mustCreateGroup(s.T(), s.client, &routing.Group{Name: "g-del"})
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "acc-del"})
	mustBindProviderToGroup(s.T(), s.client, provider.ID, group.ID)

	err := s.repo.Delete(s.ctx, provider.ID)
	s.Require().NoError(err, "Delete should cascade remove bindings")

	count, err := s.client.ProviderGroup.Query().Where(providergroup.ProviderIDEQ(provider.ID)).Count(s.ctx)
	s.Require().NoError(err)
	s.Require().Zero(count, "expected bindings to be removed")
}

// TestList inserts two providers before requesting a page that fits both
// fixtures. The list contains two records and the reported total also equals
// two.
func (s *ProviderRepoSuite) TestList() {
	mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "acc1"})
	mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "acc2"})

	providers, page, err := s.repo.ListWithFilters(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 10}, "", "", "", "", 0, "")
	s.Require().NoError(err, "List")
	s.Require().Len(providers, 2)
	s.Require().Equal(int64(2), page.Total)
}

// TestListOAuthRefreshCandidatePage_GrokCursorAndExclusions mixes valid Grok
// OAuth rows with incompatible, paused, blank-token, cooldown, and
// other-platform fixtures. Two pages return the three expected IDs in order,
// with no repeated ID.
func (s *ProviderRepoSuite) TestListOAuthRefreshCandidatePage_GrokCursorAndExclusions() {
	now := time.Now().UTC()
	valid1 := mustCreateProvider(s.T(), s.client, &providercore.Record{
		Name:     "grok-oauth-page-1",
		Platform: capability.PlatformGrok,
		Type:     capability.ProviderTypeOAuth,
		Status:   billing.StatusActive,
		Credentials: map[string]any{
			"access_token":  "access-1",
			"refresh_token": "refresh-1",
			"expires_at":    now.Add(30 * time.Minute).Format(time.RFC3339),
		},
	})
	// A manually unschedulable OAuth provider must not consume refresh page capacity, even with a refresh token.
	unschedulable := mustCreateProvider(s.T(), s.client, &providercore.Record{
		Name:        "grok-oauth-unschedulable-excluded",
		Platform:    capability.PlatformGrok,
		Type:        capability.ProviderTypeOAuth,
		Status:      billing.StatusActive,
		Credentials: map[string]any{"refresh_token": "refresh-unschedulable"},
	})
	s.Require().NoError(s.client.Provider.UpdateOneID(unschedulable.ID).SetSchedulable(false).Exec(s.ctx))
	mustCreateProvider(s.T(), s.client, &providercore.Record{
		Name:     "grok-api-key-excluded",
		Platform: capability.PlatformGrok,
		Type:     capability.ProviderTypeAPIKey,
		Status:   billing.StatusActive,
		Credentials: map[string]any{
			"api_key":       "api-key",
			"refresh_token": "must-not-make-api-key-eligible",
		},
	})
	valid2 := mustCreateProvider(s.T(), s.client, &providercore.Record{
		Name:        "grok-oauth-page-2",
		Platform:    capability.PlatformGrok,
		Type:        capability.ProviderTypeOAuth,
		Status:      billing.StatusActive,
		Credentials: map[string]any{"refresh_token": "refresh-2"},
	})
	mustCreateProvider(s.T(), s.client, &providercore.Record{
		Name:        "grok-oauth-blank-refresh-excluded",
		Platform:    capability.PlatformGrok,
		Type:        capability.ProviderTypeOAuth,
		Status:      billing.StatusActive,
		Credentials: map[string]any{"refresh_token": "   "},
	})
	valid3 := mustCreateProvider(s.T(), s.client, &providercore.Record{
		Name:        "grok-oauth-page-3",
		Platform:    capability.PlatformGrok,
		Type:        capability.ProviderTypeOAuth,
		Status:      billing.StatusActive,
		Credentials: map[string]any{"refresh_token": "refresh-3"},
	})
	cooldown := mustCreateProvider(s.T(), s.client, &providercore.Record{
		Name:        "grok-oauth-retry-cooldown-excluded",
		Platform:    capability.PlatformGrok,
		Type:        capability.ProviderTypeOAuth,
		Status:      billing.StatusActive,
		Credentials: map[string]any{"refresh_token": "refresh-cooldown"},
	})
	s.Require().NoError(s.repo.SetTempUnschedulable(s.ctx, cooldown.ID, now.Add(10*time.Minute), "token refresh retry exhausted: timeout"))
	mustCreateProvider(s.T(), s.client, &providercore.Record{
		Name:        "openai-oauth-excluded",
		Platform:    capability.PlatformOpenAI,
		Type:        capability.ProviderTypeOAuth,
		Status:      billing.StatusActive,
		Credentials: map[string]any{"refresh_token": "refresh-openai"},
	})

	options := providercore.OAuthRefreshPageOptions{
		Platforms:            []string{capability.PlatformGrok},
		Limit:                2,
		ActiveOnly:           true,
		RequireRefreshToken:  true,
		ExcludeRetryCooldown: true,
	}
	firstPage, err := s.repo.ListOAuthRefreshCandidatePage(s.ctx, options)
	s.Require().NoError(err)
	first := firstPage.Providers
	s.Require().Len(first, 2)
	s.Require().Equal([]int64{valid1.ID, valid2.ID}, []int64{first[0].ID, first[1].ID})
	s.Require().NotContains([]int64{first[0].ID, first[1].ID}, unschedulable.ID)

	options.AfterID = first[len(first)-1].ID
	secondPage, err := s.repo.ListOAuthRefreshCandidatePage(s.ctx, options)
	s.Require().NoError(err)
	second := secondPage.Providers
	s.Require().Len(second, 1)
	s.Require().Equal(valid3.ID, second[0].ID)
	s.Require().NotContains([]int64{first[0].ID, first[1].ID}, second[0].ID)
}

// TestListWithFilters creates separate fixtures for configuration, effective
// runtime status, group, search, and privacy filters. Each case compares
// returned rows with the page total and checks the corresponding expected
// attributes.
func (s *ProviderRepoSuite) TestListWithFilters() {
	tests := []struct {
		name        string                     // filter scenario label
		setup       func(client *dbent.Client) // per-scenario fixture creation callback
		platform    string                     // requested provider platform
		accType     string                     // requested credential provider type
		status      string                     // requested effective provider status
		search      string                     // requested provider-name substring
		groupID     int64                      // requested group-membership filter value
		privacyMode string                     // requested privacy-mode filter value
		wantCount   int                        // expected matching provider count
		// validate runs after the shared row/count assertions when configured; nil
		// leaves that scenario covered by its shared assertions alone.
		validate func(providers []providercore.Record) // optional returned-record assertion callback
	}{
		{
			name: "filter_by_platform",
			setup: func(client *dbent.Client) {
				mustCreateProvider(s.T(), client, &providercore.Record{Name: "a1", Platform: capability.PlatformAnthropic})
				mustCreateProvider(s.T(), client, &providercore.Record{Name: "a2", Platform: capability.PlatformOpenAI})
			},
			platform:  capability.PlatformOpenAI,
			wantCount: 1,
			validate: func(providers []providercore.Record) {
				s.Require().Equal(capability.PlatformOpenAI, providers[0].Platform)
			},
		},
		{
			name: "filter_by_type",
			setup: func(client *dbent.Client) {
				mustCreateProvider(s.T(), client, &providercore.Record{Name: "t1", Type: capability.ProviderTypeOAuth})
				mustCreateProvider(s.T(), client, &providercore.Record{Name: "t2", Type: capability.ProviderTypeAPIKey})
			},
			accType:   capability.ProviderTypeAPIKey,
			wantCount: 1,
			validate: func(providers []providercore.Record) {
				s.Require().Equal(capability.ProviderTypeAPIKey, providers[0].Type)
			},
		},
		{
			name: "filter_by_status",
			setup: func(client *dbent.Client) {
				mustCreateProvider(s.T(), client, &providercore.Record{Name: "s1", Status: billing.StatusActive})
				mustCreateProvider(s.T(), client, &providercore.Record{Name: "s2", Status: billing.StatusDisabled})
			},
			status:    billing.StatusDisabled,
			wantCount: 1,
			validate: func(providers []providercore.Record) {
				s.Require().Equal(billing.StatusDisabled, providers[0].Status)
			},
		},
		{
			name: "filter_by_status_active_excludes_runtime_blocked_providers",
			setup: func(client *dbent.Client) {
				mustCreateProvider(s.T(), client, &providercore.Record{Name: "active-normal", Status: billing.StatusActive})
				rateLimited := mustCreateProvider(s.T(), client, &providercore.Record{Name: "active-rate-limited", Status: billing.StatusActive})
				err := client.Provider.UpdateOneID(rateLimited.ID).
					SetRateLimitResetAt(time.Now().Add(10 * time.Minute)).
					Exec(context.Background())
				s.Require().NoError(err)
				tempUnsched := mustCreateProvider(s.T(), client, &providercore.Record{Name: "active-temp-unsched", Status: billing.StatusActive})
				err = client.Provider.UpdateOneID(tempUnsched.ID).
					SetTempUnschedulableUntil(time.Now().Add(15 * time.Minute)).
					Exec(context.Background())
				s.Require().NoError(err)
				unsched := mustCreateProvider(s.T(), client, &providercore.Record{Name: "active-unsched", Status: billing.StatusActive})
				err = client.Provider.UpdateOneID(unsched.ID).
					SetSchedulable(false).
					Exec(context.Background())
				s.Require().NoError(err)
			},
			status:    billing.StatusActive,
			wantCount: 1,
			validate: func(providers []providercore.Record) {
				s.Require().Equal("active-normal", providers[0].Name)
			},
		},
		{
			name: "filter_by_status_unschedulable_excludes_rate_limited_and_temp_unschedulable",
			setup: func(client *dbent.Client) {
				mustCreateProvider(s.T(), client, &providercore.Record{Name: "active-normal", Status: billing.StatusActive, Schedulable: true})
				unsched := mustCreateProvider(s.T(), client, &providercore.Record{Name: "active-unsched", Status: billing.StatusActive})
				err := client.Provider.UpdateOneID(unsched.ID).
					SetSchedulable(false).
					Exec(context.Background())
				s.Require().NoError(err)
				rateLimited := mustCreateProvider(s.T(), client, &providercore.Record{Name: "active-rate-limited", Status: billing.StatusActive})
				err = client.Provider.UpdateOneID(rateLimited.ID).
					SetSchedulable(false).
					SetRateLimitResetAt(time.Now().Add(10 * time.Minute)).
					Exec(context.Background())
				s.Require().NoError(err)
				tempUnsched := mustCreateProvider(s.T(), client, &providercore.Record{Name: "active-temp-unsched", Status: billing.StatusActive})
				err = client.Provider.UpdateOneID(tempUnsched.ID).
					SetSchedulable(false).
					SetTempUnschedulableUntil(time.Now().Add(15 * time.Minute)).
					Exec(context.Background())
				s.Require().NoError(err)
			},
			status:    "unschedulable",
			wantCount: 1,
			validate: func(providers []providercore.Record) {
				s.Require().Equal("active-unsched", providers[0].Name)
			},
		},
		{
			name: "filter_by_status_rate_limited_excludes_temp_unschedulable",
			setup: func(client *dbent.Client) {
				rateLimited := mustCreateProvider(s.T(), client, &providercore.Record{Name: "active-rate-limited", Status: billing.StatusActive})
				err := client.Provider.UpdateOneID(rateLimited.ID).
					SetRateLimitResetAt(time.Now().Add(10 * time.Minute)).
					Exec(context.Background())
				s.Require().NoError(err)
				tempUnsched := mustCreateProvider(s.T(), client, &providercore.Record{Name: "active-temp-unsched", Status: billing.StatusActive})
				err = client.Provider.UpdateOneID(tempUnsched.ID).
					SetRateLimitResetAt(time.Now().Add(20 * time.Minute)).
					SetTempUnschedulableUntil(time.Now().Add(15 * time.Minute)).
					Exec(context.Background())
				s.Require().NoError(err)
			},
			status:    "rate_limited",
			wantCount: 1,
			validate: func(providers []providercore.Record) {
				s.Require().Equal("active-rate-limited", providers[0].Name)
			},
		},
		{
			name: "filter_by_status_temp_unschedulable_excludes_manually_unschedulable",
			setup: func(client *dbent.Client) {
				tempUnsched := mustCreateProvider(s.T(), client, &providercore.Record{Name: "active-temp-unsched", Status: billing.StatusActive, Schedulable: true})
				err := client.Provider.UpdateOneID(tempUnsched.ID).
					SetTempUnschedulableUntil(time.Now().Add(15 * time.Minute)).
					Exec(context.Background())
				s.Require().NoError(err)
				unsched := mustCreateProvider(s.T(), client, &providercore.Record{Name: "active-unsched", Status: billing.StatusActive})
				err = client.Provider.UpdateOneID(unsched.ID).
					SetSchedulable(false).
					Exec(context.Background())
				s.Require().NoError(err)
			},
			status:    "temp_unschedulable",
			wantCount: 1,
			validate: func(providers []providercore.Record) {
				s.Require().Equal("active-temp-unsched", providers[0].Name)
			},
		},
		{
			name: "filter_by_search",
			setup: func(client *dbent.Client) {
				mustCreateProvider(s.T(), client, &providercore.Record{Name: "alpha-provider"})
				mustCreateProvider(s.T(), client, &providercore.Record{Name: "beta-provider"})
			},
			search:    "alpha",
			wantCount: 1,
			validate: func(providers []providercore.Record) {
				s.Require().Contains(providers[0].Name, "alpha")
			},
		},
		{
			name: "filter_by_ungrouped",
			setup: func(client *dbent.Client) {
				group := mustCreateGroup(s.T(), client, &routing.Group{Name: "g-ungrouped"})
				grouped := mustCreateProvider(s.T(), client, &providercore.Record{Name: "grouped-provider"})
				mustCreateProvider(s.T(), client, &providercore.Record{Name: "ungrouped-provider"})
				mustBindProviderToGroup(s.T(), client, grouped.ID, group.ID)
			},
			groupID:   providercore.ProviderListGroupUngrouped,
			wantCount: 1,
			validate: func(providers []providercore.Record) {
				s.Require().Equal("ungrouped-provider", providers[0].Name)
				s.Require().Empty(providers[0].GroupIDs)
			},
		},
		{
			name: "filter_by_privacy_mode",
			setup: func(client *dbent.Client) {
				mustCreateProvider(s.T(), client, &providercore.Record{Name: "privacy-ok", Extra: map[string]any{"privacy_mode": openai.PrivacyModeTrainingOff}})
				mustCreateProvider(s.T(), client, &providercore.Record{Name: "privacy-fail", Extra: map[string]any{"privacy_mode": openai.PrivacyModeFailed}})
			},
			privacyMode: openai.PrivacyModeTrainingOff,
			wantCount:   1,
			validate: func(providers []providercore.Record) {
				s.Require().Equal("privacy-ok", providers[0].Name)
			},
		},
		{
			name: "filter_by_privacy_mode_unset",
			setup: func(client *dbent.Client) {
				mustCreateProvider(s.T(), client, &providercore.Record{Name: "privacy-unset", Extra: nil})
				mustCreateProvider(s.T(), client, &providercore.Record{Name: "privacy-empty", Extra: map[string]any{"privacy_mode": ""}})
				mustCreateProvider(s.T(), client, &providercore.Record{Name: "privacy-set", Extra: map[string]any{"privacy_mode": openai.PrivacyModeTrainingOff}})
			},
			privacyMode: providercore.ProviderPrivacyModeUnsetFilter,
			wantCount:   2,
			validate: func(providers []providercore.Record) {
				names := []string{providers[0].Name, providers[1].Name}
				s.ElementsMatch([]string{"privacy-unset", "privacy-empty"}, names)
			},
		},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			// Each filter case gets its own rollback-owned transaction so its
			// configured providers and runtime windows cannot affect the next case's results.
			tx := testEntTx(s.T())
			client := tx.Client()
			repo := newProviderStoreContract(client, tx, nil)
			ctx := context.Background()

			tt.setup(client)

			providers, page, err := repo.ListWithFilters(ctx, pagination.PaginationParams{Page: 1, PageSize: 10}, tt.platform, tt.accType, tt.status, tt.search, tt.groupID, tt.privacyMode)
			s.Require().NoError(err)
			s.Require().Len(providers, tt.wantCount)
			// All fixture matches fit within this requested page, so compare
			// total with the list length to detect inconsistent filter predicates.
			s.Require().NotNil(page)
			s.Require().Equal(int64(tt.wantCount), page.Total, "total must match items on single page")
			if tt.validate != nil {
				tt.validate(providers)
			}
		})
	}
}

// TestListByGroup binds two providers with different global priorities to one
// group. Both are returned, and the lower numeric priority provider appears
// first in the result.
func (s *ProviderRepoSuite) TestListByGroup() {
	group := mustCreateGroup(s.T(), s.client, &routing.Group{Name: "g-list"})
	acc1 := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "a1", Status: billing.StatusActive, Priority: 2})
	acc2 := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "a2", Status: billing.StatusActive, Priority: 1})
	mustBindProviderToGroup(s.T(), s.client, acc1.ID, group.ID)
	mustBindProviderToGroup(s.T(), s.client, acc2.ID, group.ID)

	providers, err := s.repo.ListByGroup(s.ctx, group.ID)
	s.Require().NoError(err, "ListByGroup")
	s.Require().Len(providers, 2)
	// Group lists use each provider's global priority.
	s.Require().Equal(acc2.ID, providers[0].ID, "expected acc2 first (provider priority=1)")
}

// TestListActive inserts active and disabled providers before querying active
// records. The result contains exactly the named active fixture from the
// transaction.
func (s *ProviderRepoSuite) TestListActive() {
	mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "active1", Status: billing.StatusActive})
	mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "inactive1", Status: billing.StatusDisabled})

	providers, err := s.repo.ListActive(s.ctx)
	s.Require().NoError(err, "ListActive")
	s.Require().Len(providers, 1)
	s.Require().Equal("active1", providers[0].Name)
}

// TestListByPlatform creates providers on Anthropic and OpenAI before
// requesting the Anthropic platform. Exactly one provider is returned and its
// stored platform matches the request.
func (s *ProviderRepoSuite) TestListByPlatform() {
	mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "p1", Platform: capability.PlatformAnthropic, Status: billing.StatusActive})
	mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "p2", Platform: capability.PlatformOpenAI, Status: billing.StatusActive})

	providers, err := s.repo.ListByPlatform(s.ctx, capability.PlatformAnthropic)
	s.Require().NoError(err, "ListByPlatform")
	s.Require().Len(providers, 1)
	s.Require().Equal(capability.PlatformAnthropic, providers[0].Platform)
}

// TestPreload_And_VirtualFields associates a provider with a proxy and group
// before reading it individually and through listing. Both paths expose the
// proxy and group identifiers populated from the fixture relationships.
func (s *ProviderRepoSuite) TestPreload_And_VirtualFields() {
	proxy := mustCreateProxy(s.T(), s.client, &egress.Proxy{Name: "p1"})
	group := mustCreateGroup(s.T(), s.client, &routing.Group{Name: "g1"})

	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{
		Name:    "acc1",
		ProxyID: &proxy.ID,
	})
	mustBindProviderToGroup(s.T(), s.client, provider.ID, group.ID)

	got, err := s.repo.GetByID(s.ctx, provider.ID)
	s.Require().NoError(err, "GetByID")
	s.Require().NotNil(got.Proxy, "expected Proxy preload")
	s.Require().Equal(proxy.ID, got.Proxy.ID)
	s.Require().Len(got.GroupIDs, 1, "expected GroupIDs to be populated")
	s.Require().Equal(group.ID, got.GroupIDs[0])
	s.Require().Len(got.Groups, 1, "expected Groups to be populated")
	s.Require().Equal(group.ID, got.Groups[0].ID)

	providers, page, err := s.repo.ListWithFilters(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 10}, "", "", "", "acc", 0, "")
	s.Require().NoError(err, "ListWithFilters")
	s.Require().Equal(int64(1), page.Total)
	s.Require().Len(providers, 1)
	s.Require().NotNil(providers[0].Proxy, "expected Proxy preload in list")
	s.Require().Equal(proxy.ID, providers[0].Proxy.ID)
	s.Require().Len(providers[0].GroupIDs, 1, "expected GroupIDs in list")
	s.Require().Equal(group.ID, providers[0].GroupIDs[0])
}

// TestGroupBinding_And_BindGroups adds one membership, removes it, and replaces
// the set with two groups. Reads after each operation report the expected
// membership count and initial group ID.
func (s *ProviderRepoSuite) TestGroupBinding_And_BindGroups() {
	g1 := mustCreateGroup(s.T(), s.client, &routing.Group{Name: "g1"})
	g2 := mustCreateGroup(s.T(), s.client, &routing.Group{Name: "g2"})
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "acc"})

	s.Require().NoError(s.repo.AddToGroup(s.ctx, provider.ID, g1.ID), "AddToGroup")
	groups, err := s.repo.GetGroups(s.ctx, provider.ID)
	s.Require().NoError(err, "GetGroups")
	s.Require().Len(groups, 1, "expected 1 group")
	s.Require().Equal(g1.ID, groups[0].ID)

	s.Require().NoError(s.repo.RemoveFromGroup(s.ctx, provider.ID, g1.ID), "RemoveFromGroup")
	groups, err = s.repo.GetGroups(s.ctx, provider.ID)
	s.Require().NoError(err, "GetGroups after remove")
	s.Require().Empty(groups, "expected 0 groups after remove")

	s.Require().NoError(s.repo.BindGroups(s.ctx, provider.ID, []int64{g1.ID, g2.ID}), "BindGroups")
	groups, err = s.repo.GetGroups(s.ctx, provider.ID)
	s.Require().NoError(err, "GetGroups after bind")
	s.Require().Len(groups, 2, "expected 2 groups after bind")
}

// TestBindGroups_EmptyList creates an existing group membership before passing
// an empty replacement set. The repository succeeds and the following group
// read returns no memberships.
func (s *ProviderRepoSuite) TestBindGroups_EmptyList() {
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "acc-empty"})
	group := mustCreateGroup(s.T(), s.client, &routing.Group{Name: "g-empty"})
	mustBindProviderToGroup(s.T(), s.client, provider.ID, group.ID)

	s.Require().NoError(s.repo.BindGroups(s.ctx, provider.ID, []int64{}), "BindGroups empty")

	groups, err := s.repo.GetGroups(s.ctx, provider.ID)
	s.Require().NoError(err)
	s.Require().Empty(groups, "expected 0 groups after binding empty list")
}

// TestListSchedulable creates an ordinary provider and another with a future
// overload deadline. The schedulable result contains the ordinary fixture ID
// and excludes the overloaded provider.
func (s *ProviderRepoSuite) TestListSchedulable() {
	now := time.Now()
	group := mustCreateGroup(s.T(), s.client, &routing.Group{Name: "g-sched"})

	okAcc := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "ok", Schedulable: true})
	mustBindProviderToGroup(s.T(), s.client, okAcc.ID, group.ID)

	future := now.Add(10 * time.Minute)
	overloaded := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "over", Schedulable: true, OverloadUntil: &future})
	mustBindProviderToGroup(s.T(), s.client, overloaded.ID, group.ID)

	sched, err := s.repo.ListSchedulable(s.ctx)
	s.Require().NoError(err, "ListSchedulable")
	ids := idsOfProviders(sched)
	s.Require().Contains(ids, okAcc.ID)
	s.Require().NotContains(ids, overloaded.ID)
}

// TestListSchedulableByGroupID_TimeBoundaries_And_StatusUpdates binds ordinary,
// overloaded, and rate-limited providers to one group. Initially one is
// eligible, and clearing the rate limit increases the candidate count to two.
func (s *ProviderRepoSuite) TestListSchedulableByGroupID_TimeBoundaries_And_StatusUpdates() {
	now := time.Now()
	group := mustCreateGroup(s.T(), s.client, &routing.Group{Name: "g-sched"})

	okAcc := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "ok", Schedulable: true})
	mustBindProviderToGroup(s.T(), s.client, okAcc.ID, group.ID)

	future := now.Add(10 * time.Minute)
	overloaded := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "over", Schedulable: true, OverloadUntil: &future})
	mustBindProviderToGroup(s.T(), s.client, overloaded.ID, group.ID)

	rateLimited := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "rl", Schedulable: true})
	mustBindProviderToGroup(s.T(), s.client, rateLimited.ID, group.ID)
	s.Require().NoError(s.repo.SetRateLimited(s.ctx, rateLimited.ID, now.Add(10*time.Minute)), "SetRateLimited")

	s.Require().NoError(s.repo.SetError(s.ctx, overloaded.ID, "boom"), "SetError")

	sched, err := s.repo.ListSchedulableByGroupID(s.ctx, group.ID)
	s.Require().NoError(err, "ListSchedulableByGroupID")
	s.Require().Len(sched, 1, "expected only ok provider schedulable")
	s.Require().Equal(okAcc.ID, sched[0].ID)

	s.Require().NoError(s.repo.ClearRateLimit(s.ctx, rateLimited.ID), "ClearRateLimit")
	sched2, err := s.repo.ListSchedulableByGroupID(s.ctx, group.ID)
	s.Require().NoError(err, "ListSchedulableByGroupID after ClearRateLimit")
	s.Require().Len(sched2, 2, "expected 2 schedulable providers after ClearRateLimit")
}

// TestListSchedulableCapacityByGroupIDs requests repeated and invalid group IDs
// with one shared provider and one overloaded provider. Returned capacity rows
// are group-ordered, exclude overload, and carry the shared provider's
// concurrency and RPM data.
func (s *ProviderRepoSuite) TestListSchedulableCapacityByGroupIDs() {
	now := time.Now()
	group1 := mustCreateGroup(s.T(), s.client, &routing.Group{Name: "g-capacity-1"})
	group2 := mustCreateGroup(s.T(), s.client, &routing.Group{Name: "g-capacity-2"})
	shared := mustCreateProvider(s.T(), s.client, &providercore.Record{
		Name:        "shared-capacity",
		Platform:    capability.PlatformOpenAI,
		Schedulable: true,
		Concurrency: 3,
		Extra:       map[string]any{"base_rpm": 12},
	})
	mustBindProviderToGroup(s.T(), s.client, shared.ID, group1.ID)
	mustBindProviderToGroup(s.T(), s.client, shared.ID, group2.ID)

	future := now.Add(10 * time.Minute)
	overloaded := mustCreateProvider(s.T(), s.client, &providercore.Record{
		Name:          "overloaded-capacity",
		Schedulable:   true,
		OverloadUntil: &future,
	})
	mustBindProviderToGroup(s.T(), s.client, overloaded.ID, group1.ID)

	rows, err := s.repo.ListSchedulableCapacityByGroupIDs(s.ctx, []int64{group2.ID, group1.ID, group2.ID, 0})
	s.Require().NoError(err)
	s.Require().Len(rows, 2)
	s.Require().Equal(group1.ID, rows[0].GroupID)
	s.Require().Equal(group2.ID, rows[1].GroupID)
	for _, row := range rows {
		s.Require().Equal(shared.ID, row.ProviderID)
		s.Require().Equal(capability.PlatformOpenAI, row.Platform)
		s.Require().Equal(3, row.Concurrency)
		s.Require().Equal(float64(12), row.Extra["base_rpm"])
	}
}

// TestListSchedulableByPlatform creates schedulable Anthropic and OpenAI
// providers before requesting Anthropic candidates. The query returns exactly
// one row whose platform matches the requested value.
func (s *ProviderRepoSuite) TestListSchedulableByPlatform() {
	mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "a1", Platform: capability.PlatformAnthropic, Schedulable: true})
	mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "a2", Platform: capability.PlatformOpenAI, Schedulable: true})

	providers, err := s.repo.ListSchedulableByPlatform(s.ctx, capability.PlatformAnthropic)
	s.Require().NoError(err)
	s.Require().Len(providers, 1)
	s.Require().Equal(capability.PlatformAnthropic, providers[0].Platform)
}

// TestListSchedulableByGroupIDAndPlatform binds providers from two platforms to
// one group. Requesting Anthropic candidates returns exactly the matching bound
// fixture with its expected ID.
func (s *ProviderRepoSuite) TestListSchedulableByGroupIDAndPlatform() {
	group := mustCreateGroup(s.T(), s.client, &routing.Group{Name: "g-sp"})
	a1 := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "a1", Platform: capability.PlatformAnthropic, Schedulable: true})
	a2 := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "a2", Platform: capability.PlatformOpenAI, Schedulable: true})
	mustBindProviderToGroup(s.T(), s.client, a1.ID, group.ID)
	mustBindProviderToGroup(s.T(), s.client, a2.ID, group.ID)

	providers, err := s.repo.ListSchedulableByGroupIDAndPlatform(s.ctx, group.ID, capability.PlatformAnthropic)
	s.Require().NoError(err)
	s.Require().Len(providers, 1)
	s.Require().Equal(a1.ID, providers[0].ID)
}

// TestSetSchedulable disables scheduling on an active provider while snapshot
// recording is enabled. A read exposes false, and one recorded publication
// identifies that provider.
func (s *ProviderRepoSuite) TestSetSchedulable() {
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "acc-sched", Schedulable: true})
	cacheRecorder := &schedulerCacheRecorder{}
	s.cache = cacheRecorder
	s.repo.SetEvents(providerPublicationEvents(s.repo, cacheRecorder))

	s.Require().NoError(s.repo.SetSchedulable(s.ctx, provider.ID, false))

	got, err := s.repo.GetByID(s.ctx, provider.ID)
	s.Require().NoError(err)
	s.Require().False(got.Schedulable)
	s.Require().Len(cacheRecorder.setProviders, 1)
	s.Require().Equal(provider.ID, cacheRecorder.setProviders[0].ID)
}

// TestBulkUpdate_SyncSchedulerSnapshotOnDisabled applies disabled status to two
// providers with a snapshot recorder attached. The affected-row count is two
// and the recorded publications include both provider IDs.
func (s *ProviderRepoSuite) TestBulkUpdate_SyncSchedulerSnapshotOnDisabled() {
	provider1 := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "bulk-1", Status: billing.StatusActive, Schedulable: true})
	provider2 := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "bulk-2", Status: billing.StatusActive, Schedulable: true})
	cacheRecorder := &schedulerCacheRecorder{}
	s.cache = cacheRecorder
	s.repo.SetEvents(providerPublicationEvents(s.repo, cacheRecorder))

	disabled := billing.StatusDisabled
	rows, err := s.repo.BulkUpdate(s.ctx, []int64{provider1.ID, provider2.ID}, providercore.ProviderBulkUpdate{
		Status: &disabled,
	})
	s.Require().NoError(err)
	s.Require().Equal(int64(2), rows)

	s.Require().Len(cacheRecorder.setProviders, 2)
	ids := map[int64]struct{}{}
	for _, acc := range cacheRecorder.setProviders {
		ids[acc.ID] = struct{}{}
	}
	s.Require().Contains(ids, provider1.ID)
	s.Require().Contains(ids, provider2.ID)
}

// TestSetOverloaded writes a fixed overload deadline to an active provider.
// Storage and its single recorded snapshot expose that deadline within the
// asserted one-second tolerance.
func (s *ProviderRepoSuite) TestSetOverloaded() {
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "acc-over"})
	until := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	cacheRecorder := &schedulerCacheRecorder{}
	s.cache = cacheRecorder
	s.repo.SetEvents(providerPublicationEvents(s.repo, cacheRecorder))

	s.Require().NoError(s.repo.SetOverloaded(s.ctx, provider.ID, until))

	got, err := s.repo.GetByID(s.ctx, provider.ID)
	s.Require().NoError(err)
	s.Require().NotNil(got.OverloadUntil)
	// Snapshot publication rereads storage, so both deadline comparisons allow
	// the same one-second tolerance for their persisted timestamp representation.
	s.Require().WithinDuration(until, *got.OverloadUntil, time.Second)
	s.Require().Len(cacheRecorder.setProviders, 1)
	s.Require().Equal(provider.ID, cacheRecorder.setProviders[0].ID)
	s.Require().NotNil(cacheRecorder.setProviders[0].OverloadUntil)
	s.Require().WithinDuration(until, *cacheRecorder.setProviders[0].OverloadUntil, time.Second)
}

// TestSetRateLimited writes a fixed rate-limit reset deadline through the
// repository. The read contains both rate-limited and reset timestamps, with
// reset matching within one second.
func (s *ProviderRepoSuite) TestSetRateLimited() {
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "acc-rl"})
	resetAt := time.Date(2025, 6, 15, 14, 0, 0, 0, time.UTC)

	s.Require().NoError(s.repo.SetRateLimited(s.ctx, provider.ID, resetAt))

	got, err := s.repo.GetByID(s.ctx, provider.ID)
	s.Require().NoError(err)
	s.Require().NotNil(got.RateLimitedAt)
	s.Require().NotNil(got.RateLimitResetAt)
	// One-second tolerance checks the requested deadline after persistence; this
	// case does not require storage to retain finer timestamp precision.
	s.Require().WithinDuration(resetAt, *got.RateLimitResetAt, time.Second)
}

// TestSetRateLimitedIfLaterDoesNotShortenReset writes a later deadline followed
// by an earlier proposal. Storage keeps the later reset, and the second of two
// recorded publications retains that deadline.
func (s *ProviderRepoSuite) TestSetRateLimitedIfLaterDoesNotShortenReset() {
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "acc-rl-monotonic"})
	// Distinct whole-second deadlines separate reset ordering from timestamp
	// precision in the persisted and published window comparisons that follow.
	later := time.Now().Add(30 * time.Minute).UTC().Truncate(time.Second)
	earlier := time.Now().Add(5 * time.Minute).UTC().Truncate(time.Second)
	cacheRecorder := &schedulerCacheRecorder{}
	s.cache = cacheRecorder
	s.repo.SetEvents(providerPublicationEvents(s.repo, cacheRecorder))

	s.Require().NoError(s.repo.SetRateLimitedIfLater(s.ctx, provider.ID, later))
	s.Require().NoError(s.repo.SetRateLimitedIfLater(s.ctx, provider.ID, earlier))

	got, err := s.repo.GetByID(s.ctx, provider.ID)
	s.Require().NoError(err)
	s.Require().NotNil(got.RateLimitResetAt)
	s.Require().WithinDuration(later, *got.RateLimitResetAt, time.Second)
	s.Require().Len(cacheRecorder.setProviders, 2)
	s.Require().NotNil(cacheRecorder.setProviders[1].RateLimitResetAt)
	s.Require().WithinDuration(later, *cacheRecorder.setProviders[1].RateLimitResetAt, time.Second)
}

// TestClearRateLimitIfObservedProtectsRearmed429Generation clears one observed
// Grok window, then installs a shorter new window. The stale clear is rejected,
// and changing the provider to API key also rejects an otherwise matching
// timestamp pair.
func (s *ProviderRepoSuite) TestClearRateLimitIfObservedProtectsRearmed429Generation() {
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{
		Name:     "acc-rl-conditional-clear",
		Platform: capability.PlatformGrok,
		Type:     capability.ProviderTypeOAuth,
	})
	// Different reset instants make the old timestamp pair identifiable after
	// rearming, separating generation ownership from each deadline's ordering.
	firstReset := time.Now().Add(30 * time.Minute).UTC().Truncate(time.Second)
	rearmedReset := time.Now().Add(5 * time.Minute).UTC().Truncate(time.Second)

	s.Require().NoError(s.repo.SetRateLimitedIfLater(s.ctx, provider.ID, firstReset))
	staleGeneration, err := s.repo.GetByID(s.ctx, provider.ID)
	s.Require().NoError(err)
	s.Require().NotNil(staleGeneration.RateLimitedAt)
	s.Require().NotNil(staleGeneration.RateLimitResetAt)
	cleared, err := s.repo.ClearRateLimitIfObserved(s.ctx, provider.ID, *staleGeneration.RateLimitedAt, *staleGeneration.RateLimitResetAt)
	s.Require().NoError(err)
	s.Require().True(cleared)

	// Rearm the cleared provider with a shorter deadline, making the old
	// timestamp pair stale even though its former reset was later than this one.
	s.Require().NoError(s.repo.SetRateLimitedIfLater(s.ctx, provider.ID, rearmedReset))
	cleared, err = s.repo.ClearRateLimitIfObserved(s.ctx, provider.ID, *staleGeneration.RateLimitedAt, *staleGeneration.RateLimitResetAt)
	s.Require().NoError(err)
	s.Require().False(cleared)

	got, err := s.repo.GetByID(s.ctx, provider.ID)
	s.Require().NoError(err)
	s.Require().NotNil(got.RateLimitedAt)
	s.Require().NotNil(got.RateLimitResetAt)
	s.Require().WithinDuration(rearmedReset, *got.RateLimitResetAt, time.Second)

	// Change provider type after observing the current window; its timestamps
	// still match, so rejection here isolates the OAuth eligibility requirement.
	_, err = s.client.Provider.UpdateOneID(provider.ID).
		SetType(capability.ProviderTypeAPIKey).
		Save(s.ctx)
	s.Require().NoError(err)
	cleared, err = s.repo.ClearRateLimitIfObserved(s.ctx, provider.ID, *got.RateLimitedAt, *got.RateLimitResetAt)
	s.Require().NoError(err)
	s.Require().False(cleared)

	retyped, err := s.repo.GetByID(s.ctx, provider.ID)
	s.Require().NoError(err)
	s.Require().Equal(capability.ProviderTypeAPIKey, retyped.Type)
	s.Require().NotNil(retyped.RateLimitedAt)
	s.Require().NotNil(retyped.RateLimitResetAt)
	s.Require().WithinDuration(rearmedReset, *retyped.RateLimitResetAt, time.Second)
}

// TestClearRateLimit installs overload and rate-limit windows before clearing
// provider rate limits. A subsequent read contains no rate-limited, reset, or
// overload timestamp for that provider.
func (s *ProviderRepoSuite) TestClearRateLimit() {
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "acc-clear"})
	until := time.Now().Add(1 * time.Hour)
	s.Require().NoError(s.repo.SetOverloaded(s.ctx, provider.ID, until))
	s.Require().NoError(s.repo.SetRateLimited(s.ctx, provider.ID, until))

	s.Require().NoError(s.repo.ClearRateLimit(s.ctx, provider.ID))

	got, err := s.repo.GetByID(s.ctx, provider.ID)
	s.Require().NoError(err)
	s.Require().Nil(got.RateLimitedAt)
	s.Require().Nil(got.RateLimitResetAt)
	s.Require().Nil(got.OverloadUntil)
}

// TestResetQuotaUsedAndClearRateLimitCooldownPreservesOtherRuntimeState resets
// quota counters and the provider rate limit while other runtime blocks are
// present. Overload, temporary suspension, and model limits remain, with a
// pending event and one publication.
func (s *ProviderRepoSuite) TestResetQuotaUsedAndClearRateLimitCooldownPreservesOtherRuntimeState() {
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{
		Name: "acc-reset-quota-cooldown",
		Extra: map[string]any{
			"quota_used":        12.5,
			"quota_daily_used":  5.0,
			"quota_weekly_used": 9.0,
			"model_rate_limits": map[string]any{
				"claude-sonnet-4-5": map[string]any{"rate_limit_reset_at": "2026-09-01T10:00:00Z"},
			},
		},
	})
	until := time.Now().Add(1 * time.Hour)
	s.Require().NoError(s.repo.SetOverloaded(s.ctx, provider.ID, until))
	s.Require().NoError(s.repo.SetRateLimited(s.ctx, provider.ID, until))
	s.Require().NoError(s.repo.SetTempUnschedulable(s.ctx, provider.ID, until, "preserve-me"))

	cacheRecorder := &schedulerCacheRecorder{}
	s.cache = cacheRecorder
	s.repo.SetEvents(providerPublicationEvents(s.repo, cacheRecorder))

	s.Require().NoError(newProviderUsageContract(s.sql, s.repo, s.cache).ResetQuotaUsedAndClearRateLimitCooldown(s.ctx, provider.ID))

	got, err := s.repo.GetByID(s.ctx, provider.ID)
	s.Require().NoError(err)
	s.Require().Nil(got.RateLimitedAt)
	s.Require().Nil(got.RateLimitResetAt)
	s.Require().NotNil(got.OverloadUntil)
	// Allow one second of timestamp difference when checking that quota reset
	// retains the preexisting overload deadline as an independent runtime block.
	s.Require().WithinDuration(until, *got.OverloadUntil, time.Second)
	s.Require().NotNil(got.TempUnschedulableUntil)
	// This comparison permits one second of timestamp difference while checking
	// that the earlier suspension deadline survives the requested operation.
	s.Require().WithinDuration(until, *got.TempUnschedulableUntil, time.Second)
	s.Require().Equal("preserve-me", got.TempUnschedulableReason)
	s.Require().Contains(got.Extra, "model_rate_limits")
	s.Require().Equal(float64(0), got.Extra["quota_used"])
	s.Require().Equal(float64(0), got.Extra["quota_daily_used"])
	s.Require().Equal(float64(0), got.Extra["quota_weekly_used"])

	var pendingEventExists bool
	s.Require().NoError(postgres.ScanSingleRow(s.ctx, s.sql, `
		SELECT EXISTS (
			SELECT 1 FROM scheduler_outbox
			WHERE event_type = $1 AND provider_id = $2 AND dedup_key IS NOT NULL
		)`, []any{scheduler.SchedulerOutboxEventProviderChanged, provider.ID}, &pendingEventExists))
	s.Require().True(pendingEventExists)
	s.Require().Len(cacheRecorder.setProviders, 1)
	s.Require().Equal(provider.ID, cacheRecorder.setProviders[0].ID)
}

// TestTempUnschedulableFieldsLoadedByGetByIDAndGetByIDs stores a temporary
// window and structured reason on one of two providers. Both read forms expose
// them in requested order, and clearing updates storage and the recorded
// snapshot.
func (s *ProviderRepoSuite) TestTempUnschedulableFieldsLoadedByGetByIDAndGetByIDs() {
	acc1 := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "acc-temp-1"})
	acc2 := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "acc-temp-2"})

	// Whole-second fixture deadlines make the individual and batch reads comparable.
	until := time.Now().Add(15 * time.Minute).UTC().Truncate(time.Second)
	reason := `{"rule":"429","matched_keyword":"too many requests"}`
	s.Require().NoError(s.repo.SetTempUnschedulable(s.ctx, acc1.ID, until, reason))

	gotByID, err := s.repo.GetByID(s.ctx, acc1.ID)
	s.Require().NoError(err)
	s.Require().NotNil(gotByID.TempUnschedulableUntil)
	s.Require().WithinDuration(until, *gotByID.TempUnschedulableUntil, time.Second)
	s.Require().Equal(reason, gotByID.TempUnschedulableReason)

	gotByIDs, err := s.repo.GetByIDs(s.ctx, []int64{acc2.ID, acc1.ID})
	s.Require().NoError(err)
	s.Require().Len(gotByIDs, 2)
	s.Require().Equal(acc2.ID, gotByIDs[0].ID)
	s.Require().Nil(gotByIDs[0].TempUnschedulableUntil)
	s.Require().Equal("", gotByIDs[0].TempUnschedulableReason)
	s.Require().Equal(acc1.ID, gotByIDs[1].ID)
	s.Require().NotNil(gotByIDs[1].TempUnschedulableUntil)
	s.Require().WithinDuration(until, *gotByIDs[1].TempUnschedulableUntil, time.Second)
	s.Require().Equal(reason, gotByIDs[1].TempUnschedulableReason)

	cacheRecorder := &schedulerCacheRecorder{}
	s.cache = cacheRecorder
	s.repo.SetEvents(providerPublicationEvents(s.repo, cacheRecorder))

	s.Require().NoError(s.repo.ClearTempUnschedulable(s.ctx, acc1.ID))
	cleared, err := s.repo.GetByID(s.ctx, acc1.ID)
	s.Require().NoError(err)
	s.Require().Nil(cleared.TempUnschedulableUntil)
	s.Require().Equal("", cleared.TempUnschedulableReason)
	s.Require().Len(cacheRecorder.setProviders, 1)
	s.Require().Equal(acc1.ID, cacheRecorder.setProviders[0].ID)
	s.Require().Nil(cacheRecorder.setProviders[0].TempUnschedulableUntil)
	s.Require().Equal("", cacheRecorder.setProviders[0].TempUnschedulableReason)
}

// TestSetTempUnschedulableSkipsOutboxWhenWindowDoesNotExtend writes a
// suspension then proposes an earlier deadline with another reason. The
// deadline and first reason remain, while event and publication counts stay at
// one.
func (s *ProviderRepoSuite) TestSetTempUnschedulableSkipsOutboxWhenWindowDoesNotExtend() {
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "acc-temp-noop"})
	cacheRecorder := &schedulerCacheRecorder{}
	s.cache = cacheRecorder
	s.repo.SetEvents(providerPublicationEvents(s.repo, cacheRecorder))

	// Clear existing event rows inside the suite transaction so the next count
	// belongs to this operation; fixture rollback restores the shared outbox afterward.
	_, err := s.sql.ExecContext(s.ctx, "TRUNCATE scheduler_outbox")
	s.Require().NoError(err)

	// Fix the initial window at whole-second precision before comparing extensions.
	until := time.Now().Add(30 * time.Minute).UTC().Truncate(time.Second)
	s.Require().NoError(s.repo.SetTempUnschedulable(s.ctx, provider.ID, until, "first"))

	var count int
	err = postgres.ScanSingleRow(s.ctx, s.sql, "SELECT COUNT(*) FROM scheduler_outbox", nil, &count)
	s.Require().NoError(err)
	s.Require().Equal(1, count)
	s.Require().Len(cacheRecorder.setProviders, 1)

	s.Require().NoError(s.repo.SetTempUnschedulable(s.ctx, provider.ID, until.Add(-5*time.Minute), "older"))

	err = postgres.ScanSingleRow(s.ctx, s.sql, "SELECT COUNT(*) FROM scheduler_outbox", nil, &count)
	s.Require().NoError(err)
	s.Require().Equal(1, count)
	s.Require().Len(cacheRecorder.setProviders, 1)

	got, err := s.repo.GetByID(s.ctx, provider.ID)
	s.Require().NoError(err)
	s.Require().Equal("first", got.TempUnschedulableReason)
	s.Require().NotNil(got.TempUnschedulableUntil)
	// This comparison permits one second of timestamp difference while checking
	// that the earlier suspension deadline survives the requested operation.
	s.Require().WithinDuration(until, *got.TempUnschedulableUntil, time.Second)
}

// TestClearModelRateLimits_SyncsSchedulerSnapshot stores a per-model rate-limit
// entry before invoking cleanup. The following database read and single
// recorded snapshot both omit the model-rate-limit key.
func (s *ProviderRepoSuite) TestClearModelRateLimits_SyncsSchedulerSnapshot() {
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{
		Name: "acc-clear-model-rate",
		Extra: map[string]any{
			"model_rate_limits": map[string]any{
				"claude-sonnet-4-5": map[string]any{
					"rate_limit_reset_at": "2026-06-03T10:00:00Z",
				},
			},
		},
	})
	cacheRecorder := &schedulerCacheRecorder{}
	s.cache = cacheRecorder
	s.repo.SetEvents(providerPublicationEvents(s.repo, cacheRecorder))

	s.Require().NoError(s.repo.ClearModelRateLimits(s.ctx, provider.ID))

	got, err := s.repo.GetByID(s.ctx, provider.ID)
	s.Require().NoError(err)
	s.Require().NotContains(got.Extra, "model_rate_limits")
	s.Require().Len(cacheRecorder.setProviders, 1)
	s.Require().Equal(provider.ID, cacheRecorder.setProviders[0].ID)
	s.Require().NotContains(cacheRecorder.setProviders[0].Extra, "model_rate_limits")
}

// TestUpdateLastUsed creates a provider with no activity timestamp and invokes
// activity persistence. Reading that provider afterward exposes a nonnil
// LastUsedAt value from the repository.
func (s *ProviderRepoSuite) TestUpdateLastUsed() {
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "acc-used"})
	s.Require().Nil(provider.LastUsedAt)

	s.Require().NoError(s.repo.UpdateLastUsed(s.ctx, provider.ID))

	got, err := s.repo.GetByID(s.ctx, provider.ID)
	s.Require().NoError(err)
	s.Require().NotNil(got.LastUsedAt)
}

// TestSetError marks an active provider errored while snapshot recording is
// enabled. Storage and the publication expose error status and disabled
// scheduling, and one provider-change event exists.
func (s *ProviderRepoSuite) TestSetError() {
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "acc-err", Status: billing.StatusActive, Schedulable: true})
	cacheRecorder := &schedulerCacheRecorder{}
	s.cache = cacheRecorder
	s.repo.SetEvents(providerPublicationEvents(s.repo, cacheRecorder))
	// Clear existing event rows inside the suite transaction so the next count
	// belongs to this operation; fixture rollback restores the shared outbox afterward.
	_, err := s.sql.ExecContext(s.ctx, "TRUNCATE scheduler_outbox")
	s.Require().NoError(err)

	s.Require().NoError(s.repo.SetError(s.ctx, provider.ID, "something went wrong"))

	got, err := s.repo.GetByID(s.ctx, provider.ID)
	s.Require().NoError(err)
	s.Require().Equal(providercore.StatusError, got.Status)
	s.Require().Equal("something went wrong", got.ErrorMessage)
	s.Require().False(got.Schedulable)
	s.Require().Len(cacheRecorder.setProviders, 1)
	s.Require().Equal(provider.ID, cacheRecorder.setProviders[0].ID)
	s.Require().Equal(providercore.StatusError, cacheRecorder.setProviders[0].Status)
	s.Require().False(cacheRecorder.setProviders[0].Schedulable)

	var outboxCount int
	err = postgres.ScanSingleRow(
		s.ctx,
		s.sql,
		"SELECT COUNT(*) FROM scheduler_outbox WHERE event_type = $1 AND provider_id = $2",
		[]any{scheduler.SchedulerOutboxEventProviderChanged, provider.ID},
		&outboxCount,
	)
	s.Require().NoError(err)
	s.Require().Equal(1, outboxCount)
}

// TestSetGrokOAuthErrorIfCredentialsUnchanged_AppliesAndSyncsSchedulerState
// reconciles an active Grok provider with matching credentials and no refresh
// token. The write applies, unschedules storage, publishes the errored record,
// and inserts one event.
func (s *ProviderRepoSuite) TestSetGrokOAuthErrorIfCredentialsUnchanged_AppliesAndSyncsSchedulerState() {
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{
		Name:        "grok-conditional-error-applied",
		Platform:    capability.PlatformGrok,
		Type:        capability.ProviderTypeOAuth,
		Status:      billing.StatusActive,
		Schedulable: true,
		Credentials: map[string]any{"access_token": "observed", "_token_version": int64(7)},
	})
	observed, err := s.repo.GetByID(s.ctx, provider.ID)
	s.Require().NoError(err)
	cacheRecorder := &schedulerCacheRecorder{}
	s.cache = cacheRecorder
	s.repo.SetEvents(providerPublicationEvents(s.repo, cacheRecorder))
	// Clear existing event rows inside the suite transaction so the next count
	// belongs to this operation; fixture rollback restores the shared outbox afterward.
	_, err = s.sql.ExecContext(s.ctx, "TRUNCATE scheduler_outbox")
	s.Require().NoError(err)

	applied, err := s.repo.SetGrokOAuthErrorIfCredentialsUnchanged(
		s.ctx,
		provider.ID,
		observed.Credentials,
		"missing refresh token",
	)

	s.Require().NoError(err)
	s.Require().True(applied)
	got, err := s.repo.GetByID(s.ctx, provider.ID)
	s.Require().NoError(err)
	s.Require().Equal(providercore.StatusError, got.Status)
	s.Require().False(got.Schedulable)
	s.Require().Equal("missing refresh token", got.ErrorMessage)
	s.Require().Len(cacheRecorder.setProviders, 1)
	s.Require().Equal(providercore.StatusError, cacheRecorder.setProviders[0].Status)

	var outboxCount int
	err = postgres.ScanSingleRow(
		s.ctx,
		s.sql,
		"SELECT COUNT(*) FROM scheduler_outbox WHERE event_type = $1 AND provider_id = $2",
		[]any{scheduler.SchedulerOutboxEventProviderChanged, provider.ID},
		&outboxCount,
	)
	s.Require().NoError(err)
	s.Require().Equal(1, outboxCount)
}

// TestSetGrokOAuthErrorIfCredentialsUnchanged_SkipsConcurrentReauthorization
// observes credentials before sequentially rotating them through the
// repository. Applying the old reconciliation observation returns false,
// retains fresh credentials, and adds no publication or event.
func (s *ProviderRepoSuite) TestSetGrokOAuthErrorIfCredentialsUnchanged_SkipsConcurrentReauthorization() {
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{
		Name:        "grok-conditional-error-reauthorized",
		Platform:    capability.PlatformGrok,
		Type:        capability.ProviderTypeOAuth,
		Status:      billing.StatusActive,
		Schedulable: true,
		Credentials: map[string]any{"access_token": "observed", "_token_version": int64(7)},
	})
	observed, err := s.repo.GetByID(s.ctx, provider.ID)
	s.Require().NoError(err)
	s.Require().NoError(s.repo.UpdateCredentials(s.ctx, provider.ID, map[string]any{
		"access_token":   "fresh-access",
		"refresh_token":  "fresh-refresh",
		"expires_at":     time.Now().UTC().Add(4 * time.Hour).Format(time.RFC3339),
		"_token_version": int64(8),
	}))
	cacheRecorder := &schedulerCacheRecorder{}
	s.cache = cacheRecorder
	s.repo.SetEvents(providerPublicationEvents(s.repo, cacheRecorder))
	// Clear existing event rows inside the suite transaction so the next count
	// belongs to this operation; fixture rollback restores the shared outbox afterward.
	_, err = s.sql.ExecContext(s.ctx, "TRUNCATE scheduler_outbox")
	s.Require().NoError(err)

	applied, err := s.repo.SetGrokOAuthErrorIfCredentialsUnchanged(
		s.ctx,
		provider.ID,
		observed.Credentials,
		"stale reconciliation",
	)

	s.Require().NoError(err)
	s.Require().False(applied)
	got, err := s.repo.GetByID(s.ctx, provider.ID)
	s.Require().NoError(err)
	s.Require().Equal(billing.StatusActive, got.Status)
	s.Require().True(got.Schedulable)
	s.Require().Equal("fresh-refresh", got.GetGrokRefreshToken())
	s.Require().Empty(cacheRecorder.setProviders, "a lost compare-and-set race must not rewrite the scheduler snapshot")

	var outboxCount int
	err = postgres.ScanSingleRow(
		s.ctx,
		s.sql,
		"SELECT COUNT(*) FROM scheduler_outbox WHERE event_type = $1 AND provider_id = $2",
		[]any{scheduler.SchedulerOutboxEventProviderChanged, provider.ID},
		&outboxCount,
	)
	s.Require().NoError(err)
	s.Require().Zero(outboxCount, "a lost compare-and-set race must not enqueue a stale provider change")
}

// TestUpdateGrokOAuthCredentialsIfUnchanged_AppliesAndPublishesSchedulerState
// rotates a matching Grok credential observation. Storage and the recorded
// publication expose the replacement refresh token, publication's context is
// active, and one event exists.
func (s *ProviderRepoSuite) TestUpdateGrokOAuthCredentialsIfUnchanged_AppliesAndPublishesSchedulerState() {
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{
		Name:        "grok-refresh-success-cas-applied",
		Platform:    capability.PlatformGrok,
		Type:        capability.ProviderTypeOAuth,
		Status:      billing.StatusActive,
		Schedulable: true,
		Credentials: map[string]any{
			"access_token":   "attempted-access",
			"refresh_token":  "attempted-refresh",
			"_token_version": int64(10),
		},
	})
	observed, err := s.repo.GetByID(s.ctx, provider.ID)
	s.Require().NoError(err)
	cacheRecorder := &schedulerCacheRecorder{}
	s.cache = cacheRecorder
	s.repo.SetEvents(providerPublicationEvents(s.repo, cacheRecorder))
	// Clear existing event rows inside the suite transaction so the next count
	// belongs to this operation; fixture rollback restores the shared outbox afterward.
	_, err = s.sql.ExecContext(s.ctx, "TRUNCATE scheduler_outbox")
	s.Require().NoError(err)

	applied, err := s.repo.UpdateGrokOAuthCredentialsIfUnchanged(
		s.ctx,
		provider.ID,
		observed.Credentials,
		observed.ProxyID,
		map[string]any{
			"access_token":   "rotated-access",
			"refresh_token":  "rotated-refresh",
			"_token_version": int64(11),
		},
	)

	s.Require().NoError(err)
	s.Require().True(applied)
	got, err := s.repo.GetByID(s.ctx, provider.ID)
	s.Require().NoError(err)
	s.Require().Equal("rotated-refresh", got.GetGrokRefreshToken())
	s.Require().Len(cacheRecorder.setProviders, 1)
	s.Require().Equal("rotated-refresh", cacheRecorder.setProviders[0].GetGrokRefreshToken())
	s.Require().NoError(cacheRecorder.setCtxErr)

	var outboxCount int
	err = postgres.ScanSingleRow(
		s.ctx,
		s.sql,
		"SELECT COUNT(*) FROM scheduler_outbox WHERE event_type = $1 AND provider_id = $2",
		[]any{scheduler.SchedulerOutboxEventProviderChanged, provider.ID},
		&outboxCount,
	)
	s.Require().NoError(err)
	s.Require().Equal(1, outboxCount)
}

// TestUpdateGrokOAuthCredentialsIfUnchanged_SkipsConcurrentReauthorization
// captures one credential version then sequentially stores administrator
// credentials. The old rotation returns false, retains the administrator token,
// and produces no recorded publication or event.
func (s *ProviderRepoSuite) TestUpdateGrokOAuthCredentialsIfUnchanged_SkipsConcurrentReauthorization() {
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{
		Name:        "grok-refresh-success-cas-reauthorized",
		Platform:    capability.PlatformGrok,
		Type:        capability.ProviderTypeOAuth,
		Status:      billing.StatusActive,
		Schedulable: true,
		Credentials: map[string]any{
			"access_token":   "attempted-access",
			"refresh_token":  "attempted-refresh",
			"_token_version": int64(20),
		},
	})
	observed, err := s.repo.GetByID(s.ctx, provider.ID)
	s.Require().NoError(err)
	s.Require().NoError(s.repo.UpdateCredentials(s.ctx, provider.ID, map[string]any{
		"access_token":   "reauthorized-access",
		"refresh_token":  "reauthorized-refresh",
		"_token_version": int64(21),
	}))
	cacheRecorder := &schedulerCacheRecorder{}
	s.cache = cacheRecorder
	s.repo.SetEvents(providerPublicationEvents(s.repo, cacheRecorder))
	// Clear existing event rows inside the suite transaction so the next count
	// belongs to this operation; fixture rollback restores the shared outbox afterward.
	_, err = s.sql.ExecContext(s.ctx, "TRUNCATE scheduler_outbox")
	s.Require().NoError(err)

	applied, err := s.repo.UpdateGrokOAuthCredentialsIfUnchanged(
		s.ctx,
		provider.ID,
		observed.Credentials,
		observed.ProxyID,
		map[string]any{
			"access_token":   "provider-access",
			"refresh_token":  "provider-refresh",
			"_token_version": int64(22),
		},
	)

	s.Require().NoError(err)
	s.Require().False(applied)
	got, err := s.repo.GetByID(s.ctx, provider.ID)
	s.Require().NoError(err)
	s.Require().Equal("reauthorized-refresh", got.GetGrokRefreshToken())
	s.Require().Empty(cacheRecorder.setProviders)

	var outboxCount int
	err = postgres.ScanSingleRow(
		s.ctx,
		s.sql,
		"SELECT COUNT(*) FROM scheduler_outbox WHERE event_type = $1 AND provider_id = $2",
		[]any{scheduler.SchedulerOutboxEventProviderChanged, provider.ID},
		&outboxCount,
	)
	s.Require().NoError(err)
	s.Require().Zero(outboxCount)
}

// TestGrokOAuthConditionalMutation_DetachesBoundedSnapshotSync injects
// cancellation immediately after the combined Grok mutation/event statement
// succeeds. The operation reports applied and one publication receives a
// context with no cancellation error.
func (s *ProviderRepoSuite) TestGrokOAuthConditionalMutation_DetachesBoundedSnapshotSync() {
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{
		Name:        "grok-conditional-detached-sync",
		Platform:    capability.PlatformGrok,
		Type:        capability.ProviderTypeOAuth,
		Status:      billing.StatusActive,
		Schedulable: true,
		Credentials: map[string]any{"access_token": "observed"},
	})
	observed, err := s.repo.GetByID(s.ctx, provider.ID)
	s.Require().NoError(err)
	ctx, cancel := context.WithCancel(context.Background())
	cacheRecorder := &schedulerCacheRecorder{}
	repo := newProviderStoreContract(s.client, &cancelAfterAtomicMutationSQLExecutor{
		Executor: s.sql,
		cancel:   cancel,
	}, cacheRecorder)

	applied, err := repo.SetGrokOAuthErrorIfCredentialsUnchanged(
		ctx,
		provider.ID,
		observed.Credentials,
		"missing refresh token",
	)

	s.Require().NoError(err)
	s.Require().True(applied)
	s.Require().ErrorIs(ctx.Err(), context.Canceled)
	s.Require().Len(cacheRecorder.setProviders, 1)
	s.Require().NoError(cacheRecorder.setCtxErr, "immediate scheduler propagation must use a bounded detached context")
}

// TestGrokOAuthConditionalMutationRollsBackWhenOutboxInsertFails injects a NOT
// NULL violation into the combined Grok mutation/event statement. A fresh
// committed-row read retains active scheduling and empty error state, with no
// event row.
func TestGrokOAuthConditionalMutationRollsBackWhenOutboxInsertFails(t *testing.T) {
	client := testEntClient(t)
	provider := mustCreateProvider(t, client, &providercore.Record{
		Name:        "grok-conditional-atomic-outbox-failure",
		Platform:    capability.PlatformGrok,
		Type:        capability.ProviderTypeOAuth,
		Status:      billing.StatusActive,
		Schedulable: true,
		Credentials: map[string]any{"access_token": "observed"},
	})
	// Remove the event records as well as the committed provider fixture; outbox
	// rows carry this provider ID independently of the row deleted afterward.
	// Cleanup errors are discarded, so failed attempts can leave committed rows.
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM scheduler_outbox WHERE provider_id = $1", provider.ID)
		_ = client.Provider.DeleteOneID(provider.ID).Exec(context.Background())
	})
	repo := newProviderStoreContract(client, &failAtomicSchedulerOutboxSQLExecutor{Executor: integrationDB}, nil)

	applied, err := repo.SetGrokOAuthErrorIfCredentialsUnchanged(
		context.Background(),
		provider.ID,
		provider.Credentials,
		"missing refresh token",
	)

	require.Error(t, err)
	require.False(t, applied)
	got, readErr := repo.GetByID(context.Background(), provider.ID)
	require.NoError(t, readErr)
	require.Equal(t, billing.StatusActive, got.Status)
	require.True(t, got.Schedulable)
	require.Empty(t, got.ErrorMessage)
	var outboxCount int
	require.NoError(t, integrationDB.QueryRowContext(
		context.Background(),
		"SELECT COUNT(*) FROM scheduler_outbox WHERE provider_id = $1",
		provider.ID,
	).Scan(&outboxCount))
	require.Zero(t, outboxCount)
}

// TestUpdateErrorStatusUnschedulesProvider saves an error-status record whose
// input scheduling flag is true. Reading storage returns the error message and
// false scheduling, applying the store's error-status rule.
func (s *ProviderRepoSuite) TestUpdateErrorStatusUnschedulesProvider() {
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "acc-update-err", Status: billing.StatusActive, Schedulable: true})
	provider.Status = providercore.StatusError
	provider.ErrorMessage = "token revoked"
	provider.Schedulable = true

	s.Require().NoError(s.repo.Update(s.ctx, provider))

	got, err := s.repo.GetByID(s.ctx, provider.ID)
	s.Require().NoError(err)
	s.Require().Equal(providercore.StatusError, got.Status)
	s.Require().Equal("token revoked", got.ErrorMessage)
	s.Require().False(got.Schedulable)
}

// TestClearError_SyncSchedulerSnapshotOnRecovery clears a provider's stored
// error while snapshot recording is enabled. Storage returns active status and
// empty error text, and its single publication is active.
func (s *ProviderRepoSuite) TestClearError_SyncSchedulerSnapshotOnRecovery() {
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{
		Name:         "acc-clear-err",
		Status:       providercore.StatusError,
		ErrorMessage: "temporary error",
	})
	cacheRecorder := &schedulerCacheRecorder{}
	s.cache = cacheRecorder
	s.repo.SetEvents(providerPublicationEvents(s.repo, cacheRecorder))

	s.Require().NoError(s.repo.ClearError(s.ctx, provider.ID))

	got, err := s.repo.GetByID(s.ctx, provider.ID)
	s.Require().NoError(err)
	s.Require().Equal(billing.StatusActive, got.Status)
	s.Require().Empty(got.ErrorMessage)
	s.Require().Len(cacheRecorder.setProviders, 1)
	s.Require().Equal(provider.ID, cacheRecorder.setProviders[0].ID)
	s.Require().Equal(billing.StatusActive, cacheRecorder.setProviders[0].Status)
}

// TestUpdateSessionWindow supplies start, end, and active status for a
// provider's session window. The subsequent record has both window timestamps
// populated and the requested window status.
func (s *ProviderRepoSuite) TestUpdateSessionWindow() {
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "acc-win"})
	start := time.Date(2025, 6, 15, 10, 0, 0, 0, time.UTC)
	end := time.Date(2025, 6, 15, 15, 0, 0, 0, time.UTC)

	s.Require().NoError(s.repo.UpdateSessionWindow(s.ctx, provider.ID, &start, &end, "active"))

	got, err := s.repo.GetByID(s.ctx, provider.ID)
	s.Require().NoError(err)
	s.Require().NotNil(got.SessionWindowStart)
	s.Require().NotNil(got.SessionWindowEnd)
	s.Require().Equal("active", got.SessionWindowStatus)
}

// TestUpdateExtra_MergesFields starts with one custom Extra field and adds
// another through the repository. The resulting record exposes both the
// existing value and the new value.
func (s *ProviderRepoSuite) TestUpdateExtra_MergesFields() {
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{
		Name:  "acc-extra",
		Extra: map[string]any{"a": "1"},
	})
	s.Require().NoError(s.repo.UpdateExtra(s.ctx, provider.ID, map[string]any{"b": "2"}), "UpdateExtra")

	got, err := s.repo.GetByID(s.ctx, provider.ID)
	s.Require().NoError(err, "GetByID")
	s.Require().Equal("1", got.Extra["a"])
	s.Require().Equal("2", got.Extra["b"])
}

// TestUpdateExtra_EmptyUpdates passes an empty Extra patch for an existing
// provider. The repository completes without returning an error; this case
// makes no additional storage or event assertion.
func (s *ProviderRepoSuite) TestUpdateExtra_EmptyUpdates() {
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "acc-extra-empty"})
	s.Require().NoError(s.repo.UpdateExtra(s.ctx, provider.ID, map[string]any{}))
}

// TestUpdateExtra_NilExtra starts with the creation fixture's empty Extra map
// and adds one key. Reading the provider returns the supplied value under that
// key.
func (s *ProviderRepoSuite) TestUpdateExtra_NilExtra() {
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "acc-nil-extra", Extra: nil})
	s.Require().NoError(s.repo.UpdateExtra(s.ctx, provider.ID, map[string]any{"key": "val"}))

	got, err := s.repo.GetByID(s.ctx, provider.ID)
	s.Require().NoError(err)
	s.Require().Equal("val", got.Extra["key"])
}

// TestUpdateExtra_SchedulerNeutralSkipsOutboxAndSyncsFreshSnapshot seeds stale
// disabled cache data before writing observational usage fields. Storage
// retains those observations, event count stays zero, and publication replaces
// the cache entry with current active data.
func (s *ProviderRepoSuite) TestUpdateExtra_SchedulerNeutralSkipsOutboxAndSyncsFreshSnapshot() {
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{
		Name:     "acc-extra-neutral",
		Platform: capability.PlatformOpenAI,
		Extra:    map[string]any{"codex_usage_updated_at": "old"},
	})
	cacheRecorder := &schedulerCacheRecorder{
		providers: map[int64]*providercore.Record{
			provider.ID: {
				ID:       provider.ID,
				Platform: provider.Platform,
				Status:   billing.StatusDisabled,
				Extra: map[string]any{
					"codex_usage_updated_at": "old",
				},
			},
		},
	}
	s.cache = cacheRecorder
	s.repo.SetEvents(providerPublicationEvents(s.repo, cacheRecorder))

	updates := map[string]any{
		"codex_usage_updated_at":     "2026-03-11T10:00:00Z",
		"codex_5h_used_percent":      88.5,
		"session_window_utilization": 0.42,
	}
	s.Require().NoError(s.repo.UpdateExtra(s.ctx, provider.ID, updates))

	got, err := s.repo.GetByID(s.ctx, provider.ID)
	s.Require().NoError(err)
	s.Require().Equal("2026-03-11T10:00:00Z", got.Extra["codex_usage_updated_at"])
	s.Require().Equal(88.5, got.Extra["codex_5h_used_percent"])
	s.Require().Equal(0.42, got.Extra["session_window_utilization"])

	var outboxCount int
	s.Require().NoError(postgres.ScanSingleRow(s.ctx, s.sql, "SELECT COUNT(*) FROM scheduler_outbox WHERE provider_id = $1", []any{provider.ID}, &outboxCount))
	s.Require().Zero(outboxCount)
	s.Require().Len(cacheRecorder.setProviders, 1)
	s.Require().NotNil(cacheRecorder.providers[provider.ID])
	s.Require().Equal(billing.StatusActive, cacheRecorder.providers[provider.ID].Status)
	s.Require().Equal("2026-03-11T10:00:00Z", cacheRecorder.providers[provider.ID].Extra["codex_usage_updated_at"])
}

// TestUpdateExtra_ExhaustedCodexSnapshotSyncsSchedulerCache writes a fully used
// Codex window as observational Extra. The recorder receives active provider
// data with the usage percentage, while the isolated outbox count stays zero.
func (s *ProviderRepoSuite) TestUpdateExtra_ExhaustedCodexSnapshotSyncsSchedulerCache() {
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{
		Name:     "acc-extra-codex-exhausted",
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Extra:    map[string]any{},
	})
	cacheRecorder := &schedulerCacheRecorder{}
	s.cache = cacheRecorder
	s.repo.SetEvents(providerPublicationEvents(s.repo, cacheRecorder))
	// Clear existing event rows inside the suite transaction so the next count
	// belongs to this operation; fixture rollback restores the shared outbox afterward.
	_, err := s.sql.ExecContext(s.ctx, "TRUNCATE scheduler_outbox")
	s.Require().NoError(err)

	s.Require().NoError(s.repo.UpdateExtra(s.ctx, provider.ID, map[string]any{
		"codex_7d_used_percent":        100.0,
		"codex_7d_reset_at":            "2026-03-12T13:00:00Z",
		"codex_7d_reset_after_seconds": 86400,
	}))

	var count int
	err = postgres.ScanSingleRow(s.ctx, s.sql, "SELECT COUNT(*) FROM scheduler_outbox", nil, &count)
	s.Require().NoError(err)
	s.Require().Equal(0, count)
	s.Require().Len(cacheRecorder.setProviders, 1)
	s.Require().Equal(provider.ID, cacheRecorder.setProviders[0].ID)
	s.Require().Equal(billing.StatusActive, cacheRecorder.setProviders[0].Status)
	s.Require().Equal(100.0, cacheRecorder.setProviders[0].Extra["codex_7d_used_percent"])
}

// TestUpdateExtra_SchedulerRelevantStillEnqueuesOutbox combines
// mixed-scheduling configuration with an observational timestamp in one patch.
// The transaction contains one scheduling event after the repository accepts
// those fields.
func (s *ProviderRepoSuite) TestUpdateExtra_SchedulerRelevantStillEnqueuesOutbox() {
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{
		Name:     "acc-extra-mixed",
		Platform: capability.PlatformAntigravity,
		Extra:    map[string]any{},
	})
	// Clear existing event rows inside the suite transaction so the next count
	// belongs to this operation; fixture rollback restores the shared outbox afterward.
	_, err := s.sql.ExecContext(s.ctx, "TRUNCATE scheduler_outbox")
	s.Require().NoError(err)

	s.Require().NoError(s.repo.UpdateExtra(s.ctx, provider.ID, map[string]any{
		"mixed_scheduling":       true,
		"codex_usage_updated_at": "2026-03-11T10:00:00Z",
	}))

	var count int
	err = postgres.ScanSingleRow(s.ctx, s.sql, "SELECT COUNT(*) FROM scheduler_outbox", nil, &count)
	s.Require().NoError(err)
	s.Require().Equal(1, count)
}

// TestGetByCRSAccountID persists a regular provider with a CRS identifier in
// Extra. Looking up that identifier returns a nonnil record with the expected
// fixture name.
func (s *ProviderRepoSuite) TestGetByCRSAccountID() {
	crsID := "crs-12345"
	mustCreateProvider(s.T(), s.client, &providercore.Record{
		Name:  "acc-crs",
		Extra: map[string]any{"crs_account_id": crsID},
	})

	got, err := s.repo.GetByCRSAccountID(s.ctx, crsID)
	s.Require().NoError(err)
	s.Require().NotNil(got)
	s.Require().Equal("acc-crs", got.Name)
}

// TestGetByCRSAccountID_NotFound requests a CRS identifier absent from the
// fixture records. The repository returns a nil provider and no error for the
// missing mapping.
func (s *ProviderRepoSuite) TestGetByCRSAccountID_NotFound() {
	got, err := s.repo.GetByCRSAccountID(s.ctx, "non-existent")
	s.Require().NoError(err)
	s.Require().Nil(got)
}

// TestGetByCRSAccountID_EmptyString supplies an empty CRS identifier to the
// repository lookup. The read returns a nil provider and no error for that
// input.
func (s *ProviderRepoSuite) TestGetByCRSAccountID_EmptyString() {
	got, err := s.repo.GetByCRSAccountID(s.ctx, "")
	s.Require().NoError(err)
	s.Require().Nil(got)
}

// TestGetByCRSAccountID_ExcludesSparkShadow stores a CRS identifier on a Spark
// shadow associated with a parent. Lookup returns nil, keeping that shadow
// outside regular-provider CRS matching.
func (s *ProviderRepoSuite) TestGetByCRSAccountID_ExcludesSparkShadow() {
	crsID := "crs-shadow-only-99"
	parent := mustCreateProvider(s.T(), s.client, &providercore.Record{
		Name: "crs-mother", Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth,
	})
	mustCreateProvider(s.T(), s.client, &providercore.Record{
		Name: "crs-shadow", Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth,
		ParentProviderID: &parent.ID,
		QuotaDimension:   providercore.QuotaDimensionSpark,
		Extra:            map[string]any{"crs_account_id": crsID},
	})

	got, err := s.repo.GetByCRSAccountID(s.ctx, crsID)
	s.Require().NoError(err)
	s.Require().Nil(got, "spark 影子即便带 crs_account_id 也不应被 CRS 命中")
}

// TestListCRSAccountIDs_ExcludesSparkShadow assigns a CRS identifier to a
// persisted Spark shadow. The returned identifier map omits that shadow's value
// from the synchronization candidates.
func (s *ProviderRepoSuite) TestListCRSAccountIDs_ExcludesSparkShadow() {
	parent := mustCreateProvider(s.T(), s.client, &providercore.Record{
		Name: "crs-list-mother", Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth,
	})
	shadowCRSID := "crs-list-shadow-77"
	mustCreateProvider(s.T(), s.client, &providercore.Record{
		Name: "crs-list-shadow", Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth,
		ParentProviderID: &parent.ID,
		QuotaDimension:   providercore.QuotaDimensionSpark,
		Extra:            map[string]any{"crs_account_id": shadowCRSID},
	})

	ids, err := s.repo.ListCRSAccountIDs(s.ctx)
	s.Require().NoError(err)
	_, ok := ids[shadowCRSID]
	s.Require().False(ok, "影子的 crs_account_id 不应进入 CRS 映射")
}

// TestBulkUpdate assigns the same new priority to two providers through one
// bulk request. It reports at least one affected row, and both provider reads
// expose priority ninety-nine.
func (s *ProviderRepoSuite) TestBulkUpdate() {
	a1 := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "bulk1", Priority: 1})
	a2 := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "bulk2", Priority: 1})

	newPriority := 99
	affected, err := s.repo.BulkUpdate(s.ctx, []int64{a1.ID, a2.ID}, providercore.ProviderBulkUpdate{
		Priority: &newPriority,
	})
	s.Require().NoError(err)
	s.Require().GreaterOrEqual(affected, int64(1), "expected at least one affected row")

	// These comparisons discard lookup errors and dereference the returned
	// records; a nil result fails at the corresponding priority check.
	got1, _ := s.repo.GetByID(s.ctx, a1.ID)
	got2, _ := s.repo.GetByID(s.ctx, a2.ID)
	s.Require().Equal(99, got1.Priority)
	s.Require().Equal(99, got2.Priority)
}

// TestBulkUpdate_MergeCredentials supplies one new credential key to a provider
// holding another custom key. Reading the record exposes both the existing
// value and the added value.
func (s *ProviderRepoSuite) TestBulkUpdate_MergeCredentials() {
	a1 := mustCreateProvider(s.T(), s.client, &providercore.Record{
		Name:        "bulk-cred",
		Credentials: map[string]any{"existing": "value"},
	})

	_, err := s.repo.BulkUpdate(s.ctx, []int64{a1.ID}, providercore.ProviderBulkUpdate{
		Credentials: map[string]any{"new_key": "new_value"},
	})
	s.Require().NoError(err)

	// The value comparison needs a returned record; this case discards the
	// lookup error and fails at the later dereference when that record is nil.
	got, _ := s.repo.GetByID(s.ctx, a1.ID)
	s.Require().Equal("value", got.Credentials["existing"])
	s.Require().Equal("new_value", got.Credentials["new_key"])
}

// TestBulkUpdate_MergeExtra supplies one new Extra key to a provider holding
// another custom key. Reading the record exposes the existing value alongside
// the added value.
func (s *ProviderRepoSuite) TestBulkUpdate_MergeExtra() {
	a1 := mustCreateProvider(s.T(), s.client, &providercore.Record{
		Name:  "bulk-extra",
		Extra: map[string]any{"existing": "val"},
	})

	_, err := s.repo.BulkUpdate(s.ctx, []int64{a1.ID}, providercore.ProviderBulkUpdate{
		Extra: map[string]any{"new_key": "new_val"},
	})
	s.Require().NoError(err)

	// The value comparison needs a returned record; this case discards the
	// lookup error and fails at the later dereference when that record is nil.
	got, _ := s.repo.GetByID(s.ctx, a1.ID)
	s.Require().Equal("val", got.Extra["existing"])
	s.Require().Equal("new_val", got.Extra["new_key"])
}

// TestBulkUpdate_EmptyIDs submits an empty provider-ID list and an empty bulk
// request. The repository returns zero affected rows and no error for that
// input.
func (s *ProviderRepoSuite) TestBulkUpdate_EmptyIDs() {
	affected, err := s.repo.BulkUpdate(s.ctx, []int64{}, providercore.ProviderBulkUpdate{})
	s.Require().NoError(err)
	s.Require().Zero(affected)
}

// TestBulkUpdate_EmptyUpdates supplies an existing provider ID with an empty
// bulk request. The repository returns zero affected rows and no error for that
// input.
func (s *ProviderRepoSuite) TestBulkUpdate_EmptyUpdates() {
	a1 := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "bulk-empty"})

	affected, err := s.repo.BulkUpdate(s.ctx, []int64{a1.ID}, providercore.ProviderBulkUpdate{})
	s.Require().NoError(err)
	s.Require().Zero(affected)
}

// idsOfProviders returns provider database identifiers in the supplied record
// order.
func idsOfProviders(providers []providercore.Record) []int64 {
	out := make([]int64, 0, len(providers))
	for i := range providers {
		out = append(out, providers[i].ID)
	}
	return out
}

// newProviderUsageContract constructs usage storage with provider-change
// enqueueing and snapshot synchronization callbacks. The supplied executor
// carries event writes alongside usage operations, and store supplies the
// records read during immediate publication.
func newProviderUsageContract(exec postgres.Executor, store *providerpostgres.ProviderStore, cache scheduler.SnapshotPublicationCache) *billingpostgres.ProviderUsageStore {
	events := providerPublicationEvents(store, cache)
	return billingpostgres.NewProviderUsageStore(exec, billingpostgres.ProviderUsageOptions{
		Changed: func(ctx context.Context, id int64) error {
			return events.Write(ctx, exec, providerpostgres.ProviderChanged, &id, nil, nil)
		},
		Sync: events.SyncOne,
	})
}
