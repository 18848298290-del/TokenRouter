//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// Combined provider reports must agree with separate model and endpoint readers
// on recorded cost sources and observed durations. PostgreSQL fixtures compare
// overlapping windows, query counts, JIT restoration, and prefixed-model
// merging; the capture wrapper records each query for shape inspection while the
// surrounding test transaction owns cleanup.

// sqlCall records one delegated query for count and execution-plan inspection.
type sqlCall struct {
	Query string `json:"query"` // captured executed SQL text
	// Args retains the caller's argument slice for later EXPLAIN execution; the
	// test keeps referenced values stable while inspecting this captured query.
	Args []any `json:"-"`
}

// countingSQL captures query calls while delegating SQL execution to its fixture
// executor.
type countingSQL struct {
	// sqlExecutor and calls share the fixture's serial access. QueryContext appends
	// to calls without synchronization and retains arguments for later plan reads.
	sqlExecutor           // wrapped fixture SQL executor
	calls       []sqlCall // captured ordered query invocations
}

// TestUsageBatchQueryShape records multiple actors and compares four-ID versus
// eight-ID report calls. It requires one query per raw batch/ranking read and
// logs captured outer ranking plans.
func TestUsageBatchQueryShape(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	writer := NewUsageLogRepositoryWithSQL(client, tx, timezone.NewCalendar(time.Local))
	provider := mustCreateProvider(t, client, &providercore.Record{Name: "test-query-shape"})
	ids := []int64{}
	keys := []int64{}
	for i := range 8 {
		u := mustCreateUser(t, client, &identity.User{Email: fmt.Sprintf("test-query-%d@test.local", i)})
		key := mustCreateApiKey(t, client, &apikey.APIKey{UserID: u.ID, Key: fmt.Sprintf("sk-test-query-%d", i), Name: "k"})
		ids = append(ids, u.ID)
		keys = append(keys, key.ID)
		for range 4 {
			_, e := writer.Create(ctx, &usage.UsageLog{UserID: u.ID, APIKeyID: key.ID, ProviderID: provider.ID, Model: "lifecycle-test", InputTokens: 10, OutputTokens: 5, TotalCost: 0.1, ActualCost: 0.1, CreatedAt: time.Now().Add(-time.Hour)})
			require.NoError(t, e)
		}
	}
	counter := &countingSQL{sqlExecutor: tx}
	repo := NewUsageLogRepositoryWithSQL(client, counter, timezone.NewCalendar(time.Local))
	start, end := time.Now().Add(-2*time.Hour), time.Now()
	results := map[string]any{}
	for _, n := range []int{4, 8} {
		counter.calls = nil
		_, e := repo.GetBatchUserUsageStats(ctx, ids[:n], start, end)
		require.NoError(t, e)
		results[fmt.Sprintf("batch_users_%d", n)] = len(counter.calls)
		counter.calls = nil
		_, e = repo.GetBatchAPIKeyUsageStats(ctx, keys[:n], start, end)
		require.NoError(t, e)
		results[fmt.Sprintf("batch_keys_%d", n)] = len(counter.calls)
	}
	counter.calls = nil
	_, e := repo.GetUserSpendingRanking(ctx, start, end, 8)
	require.NoError(t, e)
	results["user_ranking"] = len(counter.calls)
	plans := []any{}
	for _, call := range counter.calls {
		// Explain the captured statement on the same transaction so its fixture rows
		// and bound values remain available for query-shape inspection.
		rows, e := tx.QueryContext(ctx, "EXPLAIN (FORMAT JSON) "+call.Query, call.Args...)
		require.NoError(t, e)
		for rows.Next() {
			var raw []byte
			require.NoError(t, rows.Scan(&raw))
			plans = append(plans, json.RawMessage(raw))
		}
		// Close each explained result before the next statement reuses the
		// transaction; any cleanup error fails the query-shape fixture rather than
		// entering its log.
		require.NoError(t, rows.Close())
	}
	results["ranking_explain"] = plans
	raw, e := json.MarshalIndent(results, "", "  ")
	require.NoError(t, e)
	for _, key := range []string{"batch_users_4", "batch_users_8", "batch_keys_4", "batch_keys_8", "user_ranking"} {
		require.Equal(t, 1, results[key], key)
	}
	t.Logf("TEST_QUERY_SHAPE=%s", raw)
}

// TestProviderWindowPairMatchesSingles supplies boundary, overlapping, empty,
// reversed, and future provider windows. It checks every paired summary equals
// separate reads, including recorded provider bases, multipliers, and cache
// tokens.
func (s *UsageLogRepoSuite) TestProviderWindowPairMatchesSingles() {
	u := mustCreateUser(s.T(), s.client, &identity.User{Email: "window-pair@test.local"})
	k := mustCreateApiKey(s.T(), s.client, &apikey.APIKey{UserID: u.ID, Key: "window-pair"})
	p := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "window-pair"})
	empty := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "empty-window-pair"})
	now := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	for i, at := range []time.Time{now.Add(-8 * 24 * time.Hour), now.Add(-7 * 24 * time.Hour), now.Add(-6 * 24 * time.Hour), now.Add(-5 * time.Hour), now.Add(-4 * time.Hour), now.Add(time.Hour)} {
		log := s.createUsageLog(u, k, p, 10+i, 20+i, float64(i), at)
		_, err := s.tx.ExecContext(s.ctx, "UPDATE usage_logs SET provider_stats_cost=2,provider_rate_multiplier=3,total_cost=4,cache_creation_tokens=5,cache_read_tokens=6 WHERE id=$1", log.ID)
		s.Require().NoError(err)
	}
	first, second := now.Add(-5*time.Hour), now.Add(-7*24*time.Hour)
	for _, id := range []int64{p.ID, empty.ID} {
		for _, starts := range [][2]time.Time{{first, second}, {second, first}, {first, first}, {now.Add(2 * time.Hour), now.Add(3 * time.Hour)}} {
			wantA, err := s.repo.GetProviderWindowStats(s.ctx, id, starts[0])
			s.Require().NoError(err)
			wantB, err := s.repo.GetProviderWindowStats(s.ctx, id, starts[1])
			s.Require().NoError(err)
			a, b, err := s.repo.GetProviderWindowStatsPair(s.ctx, id, starts[0], starts[1])
			s.Require().NoError(err)
			s.Require().Equal(wantA, a)
			s.Require().Equal(wantB, b)
		}
	}
}

// TestProviderReportRestoresConnectionJIT limits the shared pool to one
// connection and reads an empty provider report. It checks JIT before/after
// matches and the returned request total is zero.
func TestProviderReportRestoresConnectionJIT(t *testing.T) {
	limit := integrationDB.Stats().MaxOpenConnections
	// Force before/report/after reads through one pool connection so a leaked JIT
	// setting cannot be hidden by acquiring a different connection for the
	// assertion.
	integrationDB.SetMaxOpenConns(1)
	// Restore the shared pool limit after this scenario, including assertion
	// failure, so later integration tests do not inherit its single-connection
	// restriction.
	t.Cleanup(func() { integrationDB.SetMaxOpenConns(limit) })
	client := testEntClient(t)
	ctx := context.Background()
	p := mustCreateProvider(t, client, &providercore.Record{Name: "report-jit"})
	var before, after string
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SHOW jit").Scan(&before))
	repo := NewUsageLogRepositoryWithSQL(client, integrationDB, timezone.NewCalendar(time.UTC))
	result, err := repo.GetProviderUsageStats(ctx, p.ID, time.Now().Add(-time.Hour), time.Now())
	require.NoError(t, err)
	require.Zero(t, result.Summary.TotalRequests)
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SHOW jit").Scan(&after))
	require.Equal(t, before, after)
}

// TestProviderReportCombinedResults supplies unequal provider/base/user costs,
// blank endpoints, and one null duration. It compares grouped models/endpoints
// to separate readers and checks totals plus observed-duration averaging.
func (s *UsageLogRepoSuite) TestProviderReportCombinedResults() {
	u := mustCreateUser(s.T(), s.client, &identity.User{Email: "provider-report@test.local"})
	k := mustCreateApiKey(s.T(), s.client, &apikey.APIKey{UserID: u.ID, Key: "provider-report"})
	p := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "provider-report"})
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(72 * time.Hour)
	for i := range 4 {
		log := s.createUsageLog(u, k, p, 10+i, 20+i, float64(i), start.Add(time.Duration(i)*12*time.Hour))
		_, err := s.tx.ExecContext(s.ctx, `UPDATE usage_logs SET model=$1,inbound_endpoint=$2,upstream_endpoint=$3,
			provider_stats_cost=2,provider_rate_multiplier=3,total_cost=4,cache_creation_tokens=5,cache_read_tokens=6,
			duration_ms=CASE WHEN $4=0 THEN NULL ELSE $4*100 END WHERE id=$5`, fmt.Sprintf("model-%d", i%2), []string{"", " /v1/messages "}[i%2], []string{" /v1/responses ", ""}[i%2], i, log.ID)
		s.Require().NoError(err)
	}
	wantModels, err := s.repo.GetModelStatsWithFilters(s.ctx, start, end, 0, 0, p.ID, 0, nil, nil, nil)
	s.Require().NoError(err)
	wantInbound, err := s.repo.GetEndpointStatsWithFilters(s.ctx, start, end, 0, 0, p.ID, 0, "", nil, nil, nil)
	s.Require().NoError(err)
	wantUpstream, err := s.repo.GetUpstreamEndpointStatsWithFilters(s.ctx, start, end, 0, 0, p.ID, 0, "", nil, nil, nil)
	s.Require().NoError(err)
	got, err := s.repo.GetProviderUsageStats(s.ctx, p.ID, start, end)
	s.Require().NoError(err)
	s.Require().ElementsMatch(wantModels, got.Models)
	s.Require().ElementsMatch(wantInbound, got.Endpoints)
	s.Require().ElementsMatch(wantUpstream, got.UpstreamEndpoints)
	s.Require().EqualValues(4, got.Summary.TotalRequests)
	s.Require().EqualValues(24, got.Summary.TotalCost)
	s.Require().EqualValues(6, got.Summary.TotalUserCost)
	s.Require().EqualValues(16, got.Summary.TotalStandardCost)
	s.Require().EqualValues(200, got.Summary.AvgDurationMs)
}

// TestUsageLog_GetStatsWithFilters_AggregatesAndEndpoints records three scoped
// rows with separate cache and endpoint evidence. It checks summary token/charge
// sums and populated inbound, upstream, and path detail lists.
func TestUsageLog_GetStatsWithFilters_AggregatesAndEndpoints(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	repo := NewUsageLogRepositoryWithSQL(client, tx, timezone.NewCalendar(time.Local))

	user := mustCreateUser(t, client, &identity.User{Email: "stats@test.com"})
	apiKey := mustCreateApiKey(t, client, &apikey.APIKey{UserID: user.ID, Key: "sk-stats-1", Name: "k"})
	provider := mustCreateProvider(t, client, &providercore.Record{Name: "acc-stats"})

	now := time.Now().UTC()
	inboundEndpoint := "/v1/messages"
	upstreamEndpoint := "/v1/responses"
	for range 3 {
		_, err := repo.Create(ctx, &usage.UsageLog{
			UserID: user.ID, APIKeyID: apiKey.ID, ProviderID: provider.ID,
			Model: "claude-3", InputTokens: 2, OutputTokens: 3,
			CacheCreationTokens: 4, CacheReadTokens: 5,
			TotalCost: 0.5, ActualCost: 0.4, CreatedAt: now,
			InboundEndpoint: &inboundEndpoint, UpstreamEndpoint: &upstreamEndpoint,
		})
		require.NoError(t, err)
	}

	start := now.Add(-1 * time.Hour)
	end := now.Add(1 * time.Hour)
	// Scope by the fixture user because other tests commit usage logs into this
	// shared database. Otherwise their records, including zero-token failures,
	// would affect TotalRequests.
	stats, err := repo.GetStatsWithFilters(ctx, usage.UsageLogFilters{UserID: user.ID, StartTime: &start, EndTime: &end})
	require.NoError(t, err)
	require.Equal(t, int64(3), stats.TotalRequests)
	require.Equal(t, int64(6), stats.TotalInputTokens)
	require.Equal(t, int64(9), stats.TotalOutputTokens)
	require.Equal(t, int64(27), stats.TotalCacheTokens)
	require.Equal(t, int64(12), stats.TotalCacheCreationTokens)
	require.Equal(t, int64(15), stats.TotalCacheReadTokens)
	// Allow numeric-to-float conversion differences in the three-row charge sum.
	require.InDelta(t, 1.2, stats.TotalActualCost, 1e-9)
	require.NotEmpty(t, stats.Endpoints)
	require.NotEmpty(t, stats.UpstreamEndpoints)
	require.NotEmpty(t, stats.EndpointPaths)
}

// TestUsageLog_GetModelStats_MergesCompositePrefix records two display names
// sharing one internal requested model. It checks one grouped result with both
// requests and combined input/output token totals.
func TestUsageLog_GetModelStats_MergesCompositePrefix(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	repo := NewUsageLogRepositoryWithSQL(client, tx, timezone.NewCalendar(time.Local))

	user := mustCreateUser(t, client, &identity.User{Email: "model-stats-composite@test.com"})
	apiKey := mustCreateApiKey(t, client, &apikey.APIKey{UserID: user.ID, Key: "sk-model-stats-composite", Name: "k"})
	provider := mustCreateProvider(t, client, &providercore.Record{Name: "acc-model-stats-composite"})
	now := time.Now().UTC()

	for _, requestedModel := range []string{"gpt-5.6-sol", "GPT/gpt-5.6-sol"} {
		_, err := repo.Create(ctx, &usage.UsageLog{
			UserID: user.ID, APIKeyID: apiKey.ID, ProviderID: provider.ID,
			Model: "gpt-5.6-sol", RequestedModel: requestedModel,
			InputTokens: 10, OutputTokens: 5, TotalCost: 0.1, ActualCost: 0.1,
			CreatedAt: now,
		})
		require.NoError(t, err)
	}

	stats, err := repo.GetModelStatsWithFilters(
		ctx,
		now.Add(-time.Hour),
		now.Add(time.Hour),
		user.ID,
		0,
		0,
		0,
		nil,
		nil,
		nil,
	)
	require.NoError(t, err)
	require.Len(t, stats, 1)
	require.Equal(t, "gpt-5.6-sol", stats[0].Model)
	require.Equal(t, int64(2), stats[0].Requests)
	require.Equal(t, int64(30), stats[0].TotalTokens)
}

// QueryContext records the SQL text and supplied argument slice before
// delegation. Calls and arguments remain shared with the test's capture storage;
// callers serialize execution and keep captured values stable for later
// inspection.
func (s *countingSQL) QueryContext(ctx context.Context, q string, a ...any) (*sql.Rows, error) {
	// Keep the executed query and argument references available after delegation;
	// the caller uses this serial fixture capture for counts and EXPLAIN
	// statements.
	s.calls = append(s.calls, sqlCall{q, a})
	return s.sqlExecutor.QueryContext(ctx, q, a...)
}
