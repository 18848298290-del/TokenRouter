//go:build integration

package postgres

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/settings/preaggregation"
	teamcore "github.com/TokenFlux/TokenRouter/internal/team"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// Usage repository scenarios exercise the migrated PostgreSQL schema through
// rollback-isolated suite transactions and committed pool-backed batches.
// Persisted requests keep database identity, diagnostic fields, monetary
// measures, and display associations aligned across writes, filtered lists,
// dashboards, and reports.
//
// Queue fixtures explicitly separate full admission queues, cancellation while
// queued, and worker results; some replace the real consumer with a controlled
// acknowledgement and therefore test coordination rather than persistence.
// Aggregation fixtures compare selected raw and bucket metrics, including late
// records and current team ownership. Other cases establish the actual
// assertions for zero input, bounded ranges, model grouping, provider expense,
// and nullable response evidence; they do not imply untested
// financial-settlement guarantees.

// TestUsageLogRepoSuite runs repository scenarios against migrated PostgreSQL
// with UsageLogRepoSuite's rollback fixtures. The suite provides shared setup so
// each method's assertions observe its own transactional writes.
func TestUsageLogRepoSuite(t *testing.T) {
	suite.Run(t, new(UsageLogRepoSuite))
}

// TestCreate supplies a charged usage row without a request ID to the
// transactional store. It checks Create succeeds and populates a nonzero
// database row identity.
func (s *UsageLogRepoSuite) TestCreate() {
	user := mustCreateUser(s.T(), s.client, &identity.User{Email: "create@test.com"})
	apiKey := mustCreateApiKey(s.T(), s.client, &apikey.APIKey{UserID: user.ID, Key: "sk-create", Name: "k"})
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "acc-create"})

	log := &usage.UsageLog{
		UserID:       user.ID,
		APIKeyID:     apiKey.ID,
		ProviderID:   provider.ID,
		Model:        "claude-3",
		InputTokens:  10,
		OutputTokens: 20,
		TotalCost:    0.5,
		ActualCost:   0.4,
	}

	_, err := s.repo.Create(s.ctx, log)
	s.Require().NoError(err, "Create")
	s.Require().NotZero(log.ID)
}

// TestUsageLogRepositoryCreate_BatchPathConcurrent submits sixteen distinct
// pool-backed requests concurrently. It checks each caller reports insertion
// with a nonzero ID and the database contains sixteen key-scoped rows.
func TestUsageLogRepositoryCreate_BatchPathConcurrent(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageLogRepositoryWithSQL(client, integrationDB, timezone.NewCalendar(time.Local))

	user := mustCreateUser(t, client, &identity.User{Email: fmt.Sprintf("usage-batch-%d@example.com", time.Now().UnixNano())})
	apiKey := mustCreateApiKey(t, client, &apikey.APIKey{UserID: user.ID, Key: "sk-usage-batch-" + uuid.NewString(), Name: "k"})
	provider := mustCreateProvider(t, client, &providercore.Record{Name: "acc-usage-batch-" + uuid.NewString()})

	const total = 16
	results := make([]bool, total)
	errs := make([]error, total)
	logs := make([]*usage.UsageLog, total)

	// Every caller owns its result slice slot and row. Join all submissions before
	// inspecting insertion flags or querying the committed batch record count.
	var wg sync.WaitGroup
	wg.Add(total)
	for i := range total {
		logs[i] = &usage.UsageLog{
			UserID:       user.ID,
			APIKeyID:     apiKey.ID,
			ProviderID:   provider.ID,
			RequestID:    uuid.NewString(),
			Model:        "claude-3",
			InputTokens:  10 + i,
			OutputTokens: 20 + i,
			TotalCost:    0.5,
			ActualCost:   0.5,
			CreatedAt:    time.Now().UTC(),
		}
		go func() {
			defer wg.Done()
			results[i], errs[i] = repo.Create(ctx, logs[i])
		}()
	}
	wg.Wait()

	for i := range total {
		require.NoError(t, errs[i])
		require.True(t, results[i])
		require.NotZero(t, logs[i].ID)
	}

	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM usage_logs WHERE api_key_id = $1", apiKey.ID).Scan(&count))
	require.Equal(t, total, count)
}

// TestUsageLogRepositoryCreate_BatchPathDuplicateRequestID writes the same
// request/key pair twice through the pool path. It checks first insertion,
// duplicate recovery to the same row ID, and one stored record.
func TestUsageLogRepositoryCreate_BatchPathDuplicateRequestID(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageLogRepositoryWithSQL(client, integrationDB, timezone.NewCalendar(time.Local))

	user := mustCreateUser(t, client, &identity.User{Email: fmt.Sprintf("usage-dup-%d@example.com", time.Now().UnixNano())})
	apiKey := mustCreateApiKey(t, client, &apikey.APIKey{UserID: user.ID, Key: "sk-usage-dup-" + uuid.NewString(), Name: "k"})
	provider := mustCreateProvider(t, client, &providercore.Record{Name: "acc-usage-dup-" + uuid.NewString()})
	requestID := uuid.NewString()

	log1 := &usage.UsageLog{
		UserID:       user.ID,
		APIKeyID:     apiKey.ID,
		ProviderID:   provider.ID,
		RequestID:    requestID,
		Model:        "claude-3",
		InputTokens:  10,
		OutputTokens: 20,
		TotalCost:    0.5,
		ActualCost:   0.5,
		CreatedAt:    time.Now().UTC(),
	}
	log2 := &usage.UsageLog{
		UserID:       user.ID,
		APIKeyID:     apiKey.ID,
		ProviderID:   provider.ID,
		RequestID:    requestID,
		Model:        "claude-3",
		InputTokens:  10,
		OutputTokens: 20,
		TotalCost:    0.5,
		ActualCost:   0.5,
		CreatedAt:    time.Now().UTC(),
	}

	inserted1, err1 := repo.Create(ctx, log1)
	inserted2, err2 := repo.Create(ctx, log2)
	require.NoError(t, err1)
	require.NoError(t, err2)
	require.True(t, inserted1)
	require.False(t, inserted2)
	require.Equal(t, log1.ID, log2.ID)

	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM usage_logs WHERE request_id = $1 AND api_key_id = $2", requestID, apiKey.ID).Scan(&count))
	require.Equal(t, 1, count)
}

// TestUsageLogRepositoryFlushCreateBatch_DeduplicatesSameKeyInMemory places
// eight differing payloads under one request/key identity. It checks one caller
// reports insertion, every caller receives the same row ID, and one row
// persists.
func TestUsageLogRepositoryFlushCreateBatch_DeduplicatesSameKeyInMemory(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageLogRepositoryWithSQL(client, integrationDB, timezone.NewCalendar(time.Local))

	user := mustCreateUser(t, client, &identity.User{Email: fmt.Sprintf("usage-batch-memdup-%d@example.com", time.Now().UnixNano())})
	apiKey := mustCreateApiKey(t, client, &apikey.APIKey{UserID: user.ID, Key: "sk-usage-batch-memdup-" + uuid.NewString(), Name: "k"})
	provider := mustCreateProvider(t, client, &providercore.Record{Name: "acc-usage-batch-memdup-" + uuid.NewString()})
	requestID := uuid.NewString()

	const total = 8
	batch := make([]usageLogCreateRequest, 0, total)
	logs := make([]*usage.UsageLog, 0, total)

	for i := range total {
		log := &usage.UsageLog{
			UserID:       user.ID,
			APIKeyID:     apiKey.ID,
			ProviderID:   provider.ID,
			RequestID:    requestID,
			Model:        "claude-3",
			InputTokens:  10 + i,
			OutputTokens: 20 + i,
			TotalCost:    0.5,
			ActualCost:   0.5,
			CreatedAt:    time.Now().UTC(),
		}
		logs = append(logs, log)
		batch = append(batch, usageLogCreateRequest{
			log:      log,
			prepared: prepareUsageLogInsert(log),
			resultCh: make(chan usageLogCreateResult, 1),
		})
	}

	repo.flushCreateBatch(integrationDB, batch)

	insertedCount := 0
	var firstID int64
	for idx, req := range batch {
		res := <-req.resultCh
		require.NoError(t, res.err)
		if res.inserted {
			insertedCount++
		}
		require.NotZero(t, logs[idx].ID)
		if idx == 0 {
			firstID = logs[idx].ID
		} else {
			require.Equal(t, firstID, logs[idx].ID)
		}
	}

	require.Equal(t, 1, insertedCount)

	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM usage_logs WHERE request_id = $1 AND api_key_id = $2", requestID, apiKey.ID).Scan(&count))
	require.Equal(t, 1, count)
}

// TestUsageLogRepositoryCreateBestEffort_BatchPathDuplicateRequestID submits the
// same request/key pair twice to best-effort Create. It checks both calls
// succeed and one matching row is eventually stored.
func TestUsageLogRepositoryCreateBestEffort_BatchPathDuplicateRequestID(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageLogRepositoryWithSQL(client, integrationDB, timezone.NewCalendar(time.Local))

	user := mustCreateUser(t, client, &identity.User{Email: fmt.Sprintf("usage-best-effort-dup-%d@example.com", time.Now().UnixNano())})
	apiKey := mustCreateApiKey(t, client, &apikey.APIKey{UserID: user.ID, Key: "sk-usage-best-effort-dup-" + uuid.NewString(), Name: "k"})
	provider := mustCreateProvider(t, client, &providercore.Record{Name: "acc-usage-best-effort-dup-" + uuid.NewString()})
	requestID := uuid.NewString()

	log1 := &usage.UsageLog{
		UserID:       user.ID,
		APIKeyID:     apiKey.ID,
		ProviderID:   provider.ID,
		RequestID:    requestID,
		Model:        "claude-3",
		InputTokens:  10,
		OutputTokens: 20,
		TotalCost:    0.5,
		ActualCost:   0.5,
		CreatedAt:    time.Now().UTC(),
	}
	log2 := &usage.UsageLog{
		UserID:       user.ID,
		APIKeyID:     apiKey.ID,
		ProviderID:   provider.ID,
		RequestID:    requestID,
		Model:        "claude-3",
		InputTokens:  10,
		OutputTokens: 20,
		TotalCost:    0.5,
		ActualCost:   0.5,
		CreatedAt:    time.Now().UTC(),
	}

	require.NoError(t, repo.CreateBestEffort(ctx, log1))
	require.NoError(t, repo.CreateBestEffort(ctx, log2))

	// Poll the committed table so this assertion observes persistence, with a
	// three-second budget and twenty-millisecond checks after successful writes.
	require.Eventually(t, func() bool {
		var count int
		err := integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM usage_logs WHERE request_id = $1 AND api_key_id = $2", requestID, apiKey.ID).Scan(&count)
		return err == nil && count == 1
	}, 3*time.Second, 20*time.Millisecond)
}

// TestUsageLogRepositoryCreateBestEffort_QueueFullBlocksUntilCtxDeadline fills a
// controlled acknowledgement queue with no consumer. It checks the call waits
// near its deadline before returning a Dropped error.
func TestUsageLogRepositoryCreateBestEffort_QueueFullBlocksUntilCtxDeadline(t *testing.T) {
	// Prefill a queue with no worker so admission cannot proceed; the returned
	// disposition must come from the caller deadline rather than database
	// execution.
	client := testEntClient(t)
	repo := NewUsageLogRepositoryWithSQL(client, integrationDB, timezone.NewCalendar(time.Local))
	repo.bestEffortBatchCh = make(chan usageLogBestEffortRequest, 1)
	repo.bestEffortBatchCh <- usageLogBestEffortRequest{}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := repo.CreateBestEffort(ctx, &usage.UsageLog{
		UserID:       1,
		APIKeyID:     2,
		ProviderID:   3,
		RequestID:    uuid.NewString(),
		Model:        "claude-3",
		InputTokens:  10,
		OutputTokens: 20,
		TotalCost:    0.5,
		ActualCost:   0.5,
		CreatedAt:    time.Now().UTC(),
	})

	require.Error(t, err)
	require.True(t, usage.IsUsageLogCreateDropped(err))
	// Allow a fifty-millisecond margin around the configured admission deadline.
	require.GreaterOrEqual(t, time.Since(start), 150*time.Millisecond)
}

// TestUsageLogRepositoryCreateBestEffort_QueueFullWaitsForDrain fills a
// controlled queue and drains it after a delay. It checks the blocked submission
// succeeds after its synthetic acknowledgement, without asserting database
// persistence.
func TestUsageLogRepositoryCreateBestEffort_QueueFullWaitsForDrain(t *testing.T) {
	// A controlled consumer removes the placeholder and acknowledges the submitted
	// request, isolating queue-space recovery from real database persistence.
	client := testEntClient(t)
	repo := NewUsageLogRepositoryWithSQL(client, integrationDB, timezone.NewCalendar(time.Local))
	repo.bestEffortBatchCh = make(chan usageLogBestEffortRequest, 1)
	repo.bestEffortBatchCh <- usageLogBestEffortRequest{}

	// Delay the synthetic drain until the submitting caller has encountered the
	// full queue, then acknowledge its request to finish the admission scenario.
	go func() {
		time.Sleep(100 * time.Millisecond)
		// Remove the placeholder before receiving the waiting request, creating queue
		// space that must release its blocked send and preserve its acknowledgement.
		<-repo.bestEffortBatchCh
		req := <-repo.bestEffortBatchCh
		sendUsageLogBestEffortResult(req.resultCh, nil)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	err := repo.CreateBestEffort(ctx, &usage.UsageLog{
		UserID:       1,
		APIKeyID:     2,
		ProviderID:   3,
		RequestID:    uuid.NewString(),
		Model:        "claude-3",
		InputTokens:  10,
		OutputTokens: 20,
		TotalCost:    0.5,
		ActualCost:   0.5,
		CreatedAt:    time.Now().UTC(),
	})

	require.NoError(t, err)
}

// TestUsageLogRepositoryCreate_BatchPathCanceledContextMarksNotPersisted
// supplies an already canceled context to pool-backed Create. It checks false
// insertion and a confirmed-unwritten marker in the returned error.
func TestUsageLogRepositoryCreate_BatchPathCanceledContextMarksNotPersisted(t *testing.T) {
	client := testEntClient(t)
	repo := NewUsageLogRepositoryWithSQL(client, integrationDB, timezone.NewCalendar(time.Local))

	user := mustCreateUser(t, client, &identity.User{Email: fmt.Sprintf("usage-cancel-%d@example.com", time.Now().UnixNano())})
	apiKey := mustCreateApiKey(t, client, &apikey.APIKey{UserID: user.ID, Key: "sk-usage-cancel-" + uuid.NewString(), Name: "k"})
	provider := mustCreateProvider(t, client, &providercore.Record{Name: "acc-usage-cancel-" + uuid.NewString()})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	inserted, err := repo.Create(ctx, &usage.UsageLog{
		UserID:       user.ID,
		APIKeyID:     apiKey.ID,
		ProviderID:   provider.ID,
		RequestID:    uuid.NewString(),
		Model:        "claude-3",
		InputTokens:  10,
		OutputTokens: 20,
		TotalCost:    0.5,
		ActualCost:   0.5,
		CreatedAt:    time.Now().UTC(),
	})

	require.False(t, inserted)
	require.Error(t, err)
	require.True(t, usage.IsUsageLogCreateNotPersisted(err))
}

// TestUsageLogRepositoryCreate_BatchPathQueueFullMarksNotPersisted fills a
// controlled result-aware queue until the caller deadline. It checks admission
// waits, insertion remains false, and the error carries confirmed-nonpersistence
// disposition.
func TestUsageLogRepositoryCreate_BatchPathQueueFullMarksNotPersisted(t *testing.T) {
	// Hold result-aware admission behind a prefilled queue with no consumer, making
	// the deadline reject the request before any worker can claim processing.
	client := testEntClient(t)
	repo := NewUsageLogRepositoryWithSQL(client, integrationDB, timezone.NewCalendar(time.Local))
	repo.createBatchCh = make(chan usageLogCreateRequest, 1)
	repo.createBatchCh <- usageLogCreateRequest{}

	user := mustCreateUser(t, client, &identity.User{Email: fmt.Sprintf("usage-create-full-%d@example.com", time.Now().UnixNano())})
	apiKey := mustCreateApiKey(t, client, &apikey.APIKey{UserID: user.ID, Key: "sk-usage-create-full-" + uuid.NewString(), Name: "k"})
	provider := mustCreateProvider(t, client, &providercore.Record{Name: "acc-usage-create-full-" + uuid.NewString()})

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	inserted, err := repo.Create(ctx, &usage.UsageLog{
		UserID:       user.ID,
		APIKeyID:     apiKey.ID,
		ProviderID:   provider.ID,
		RequestID:    uuid.NewString(),
		Model:        "claude-3",
		InputTokens:  10,
		OutputTokens: 20,
		TotalCost:    0.5,
		ActualCost:   0.5,
		CreatedAt:    time.Now().UTC(),
	})

	require.False(t, inserted)
	require.Error(t, err)
	require.True(t, usage.IsUsageLogCreateNotPersisted(err))
	// Allow a fifty-millisecond margin around the configured admission deadline.
	require.GreaterOrEqual(t, time.Since(start), 150*time.Millisecond)
}

// TestUsageLogRepositoryCreate_BatchPathCanceledAfterQueueMarksNotPersisted
// holds an admitted request before worker processing. It cancels the caller,
// checks the confirmed-unwritten error, then acknowledges the captured request.
func TestUsageLogRepositoryCreate_BatchPathCanceledAfterQueueMarksNotPersisted(t *testing.T) {
	client := testEntClient(t)
	repo := NewUsageLogRepositoryWithSQL(client, integrationDB, timezone.NewCalendar(time.Local))
	repo.createBatchCh = make(chan usageLogCreateRequest, 1)

	user := mustCreateUser(t, client, &identity.User{Email: fmt.Sprintf("usage-cancel-queued-%d@example.com", time.Now().UnixNano())})
	apiKey := mustCreateApiKey(t, client, &apikey.APIKey{UserID: user.ID, Key: "sk-usage-cancel-queued-" + uuid.NewString(), Name: "k"})
	provider := mustCreateProvider(t, client, &providercore.Record{Name: "acc-usage-cancel-queued-" + uuid.NewString()})

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)

	// Submit on a separate goroutine so the test can capture admission, cancel
	// before processing, and join the caller through errCh.
	go func() {
		_, err := repo.createBatched(ctx, &usage.UsageLog{
			UserID:       user.ID,
			APIKeyID:     apiKey.ID,
			ProviderID:   provider.ID,
			RequestID:    uuid.NewString(),
			Model:        "claude-3",
			InputTokens:  10,
			OutputTokens: 20,
			TotalCost:    0.5,
			ActualCost:   0.5,
			CreatedAt:    time.Now().UTC(),
		})
		errCh <- err
	}()

	// Receive but do not claim processing. Cancellation can then win the queued
	// state transition, making the returned disposition independent of SQL timing.
	req := <-repo.createBatchCh
	require.NotNil(t, req.shared)
	cancel()

	err := <-errCh
	require.Error(t, err)
	require.True(t, usage.IsUsageLogCreateNotPersisted(err))
	// Finish the captured request after the caller leaves so the fixture has no
	// unresolved worker acknowledgement when its database cleanup begins.
	completeUsageLogCreateRequest(req, usageLogCreateResult{inserted: false, err: usage.MarkUsageLogCreateNotPersisted(context.Canceled)})
}

// TestUsageLogRepositoryFlushCreateBatch_CanceledRequestReturnsNotPersisted
// passes a request already marked canceled to direct flushing. It checks the
// result reports no insertion and carries the confirmed-unwritten disposition.
func TestUsageLogRepositoryFlushCreateBatch_CanceledRequestReturnsNotPersisted(t *testing.T) {
	client := testEntClient(t)
	repo := NewUsageLogRepositoryWithSQL(client, integrationDB, timezone.NewCalendar(time.Local))

	user := mustCreateUser(t, client, &identity.User{Email: fmt.Sprintf("usage-flush-cancel-%d@example.com", time.Now().UnixNano())})
	apiKey := mustCreateApiKey(t, client, &apikey.APIKey{UserID: user.ID, Key: "sk-usage-flush-cancel-" + uuid.NewString(), Name: "k"})
	provider := mustCreateProvider(t, client, &providercore.Record{Name: "acc-usage-flush-cancel-" + uuid.NewString()})

	log := &usage.UsageLog{
		UserID:       user.ID,
		APIKeyID:     apiKey.ID,
		ProviderID:   provider.ID,
		RequestID:    uuid.NewString(),
		Model:        "claude-3",
		InputTokens:  10,
		OutputTokens: 20,
		TotalCost:    0.5,
		ActualCost:   0.5,
		CreatedAt:    time.Now().UTC(),
	}
	req := usageLogCreateRequest{
		log:      log,
		prepared: prepareUsageLogInsert(log),
		shared:   &usageLogCreateShared{},
		resultCh: make(chan usageLogCreateResult, 1),
	}
	// Mark queued state canceled before flushing so the worker cannot claim this
	// request for SQL processing and must acknowledge confirmed nonpersistence.
	req.shared.state.Store(usageLogCreateStateCanceled)

	repo.flushCreateBatch(integrationDB, []usageLogCreateRequest{req})

	res := <-req.resultCh
	require.False(t, res.inserted)
	require.Error(t, res.err)
	require.True(t, usage.IsUsageLogCreateNotPersisted(res.err))
}

// TestGetByID creates one charged fixture and reads it by database identity. It
// checks the row ID and input-token count survive the persistence/readback
// sequence.
func (s *UsageLogRepoSuite) TestGetByID() {
	user := mustCreateUser(s.T(), s.client, &identity.User{Email: "getbyid@test.com"})
	apiKey := mustCreateApiKey(s.T(), s.client, &apikey.APIKey{UserID: user.ID, Key: "sk-getbyid", Name: "k"})
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "acc-getbyid"})

	log := s.createUsageLog(user, apiKey, provider, 10, 20, 0.5, time.Now())

	got, err := s.repo.GetByID(s.ctx, log.ID)
	s.Require().NoError(err, "GetByID")
	s.Require().Equal(log.ID, got.ID)
	s.Require().Equal(10, got.InputTokens)
}

// TestGetByID_NotFound queries a database identity absent from the fixture
// transaction. It checks GetByID returns an error, without asserting a more
// specific error value.
func (s *UsageLogRepoSuite) TestGetByID_NotFound() {
	_, err := s.repo.GetByID(s.ctx, 999999)
	s.Require().Error(err, "expected error for non-existent ID")
}

// TestGetByID_ReturnsProviderRateMultiplier persists a nonnil provider
// multiplier beside unequal base/user costs. It checks readback retains a nonnil
// multiplier with the supplied value.
func (s *UsageLogRepoSuite) TestGetByID_ReturnsProviderRateMultiplier() {
	user := mustCreateUser(s.T(), s.client, &identity.User{Email: "getbyid-mult@test.com"})
	apiKey := mustCreateApiKey(s.T(), s.client, &apikey.APIKey{UserID: user.ID, Key: "sk-getbyid-mult", Name: "k"})
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "acc-getbyid-mult"})

	m := 0.5
	log := &usage.UsageLog{
		UserID:                 user.ID,
		APIKeyID:               apiKey.ID,
		ProviderID:             provider.ID,
		RequestID:              uuid.New().String(),
		Model:                  "claude-3",
		InputTokens:            10,
		OutputTokens:           20,
		TotalCost:              1.0,
		ActualCost:             2.0,
		ProviderRateMultiplier: &m,
		CreatedAt: timezone.NewCalendar(time.Local).
			Today().Add(2 * time.Hour),
	}
	_, err := s.repo.Create(s.ctx, log)
	s.Require().NoError(err)

	got, err := s.repo.GetByID(s.ctx, log.ID)
	s.Require().NoError(err)
	s.Require().NotNil(got.ProviderRateMultiplier)
	// Use a relative tolerance for the numeric multiplier's float64 readback.
	s.Require().InEpsilon(0.5, *got.ProviderRateMultiplier, 0.0001)
}

// TestGetByID_ReturnsOpenAIWSMode records legacy WebSocket evidence with a
// charged row. It checks reading the persisted row retains the true OpenAIWSMode
// flag.
func (s *UsageLogRepoSuite) TestGetByID_ReturnsOpenAIWSMode() {
	user := mustCreateUser(s.T(), s.client, &identity.User{Email: "getbyid-ws@test.com"})
	apiKey := mustCreateApiKey(s.T(), s.client, &apikey.APIKey{UserID: user.ID, Key: "sk-getbyid-ws", Name: "k"})
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "acc-getbyid-ws"})

	log := &usage.UsageLog{
		UserID:       user.ID,
		APIKeyID:     apiKey.ID,
		ProviderID:   provider.ID,
		RequestID:    uuid.New().String(),
		Model:        "gpt-5.3-codex",
		InputTokens:  10,
		OutputTokens: 20,
		TotalCost:    1.0,
		ActualCost:   1.0,
		OpenAIWSMode: true,
		CreatedAt: timezone.NewCalendar(time.Local).
			Today().Add(3 * time.Hour),
	}
	_, err := s.repo.Create(s.ctx, log)
	s.Require().NoError(err)

	got, err := s.repo.GetByID(s.ctx, log.ID)
	s.Require().NoError(err)
	s.Require().True(got.OpenAIWSMode)
}

// TestGetByID_ReturnsRequestTypeAndLegacyFallback records explicit WebSocket
// type with inconsistent legacy flags. It checks readback uses that type and
// normalizes both legacy transport flags to true.
func (s *UsageLogRepoSuite) TestGetByID_ReturnsRequestTypeAndLegacyFallback() {
	user := mustCreateUser(s.T(), s.client, &identity.User{Email: "getbyid-request-type@test.com"})
	apiKey := mustCreateApiKey(s.T(), s.client, &apikey.APIKey{UserID: user.ID, Key: "sk-getbyid-request-type", Name: "k"})
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "acc-getbyid-request-type"})

	log := &usage.UsageLog{
		UserID:       user.ID,
		APIKeyID:     apiKey.ID,
		ProviderID:   provider.ID,
		RequestID:    uuid.New().String(),
		Model:        "gpt-5.3-codex",
		RequestType:  usage.RequestTypeWSV2,
		Stream:       true,
		OpenAIWSMode: false,
		InputTokens:  10,
		OutputTokens: 20,
		TotalCost:    1.0,
		ActualCost:   1.0,
		CreatedAt: timezone.NewCalendar(time.Local).
			Today().Add(4 * time.Hour),
	}
	_, err := s.repo.Create(s.ctx, log)
	s.Require().NoError(err)

	got, err := s.repo.GetByID(s.ctx, log.ID)
	s.Require().NoError(err)
	s.Require().Equal(usage.RequestTypeWSV2, got.RequestType)
	s.Require().True(got.Stream)
	s.Require().True(got.OpenAIWSMode)
}

// TestDelete creates one analytical usage record and deletes its database
// identity. It checks deletion succeeds and a subsequent GetByID query returns
// an error.
func (s *UsageLogRepoSuite) TestDelete() {
	user := mustCreateUser(s.T(), s.client, &identity.User{Email: "delete@test.com"})
	apiKey := mustCreateApiKey(s.T(), s.client, &apikey.APIKey{UserID: user.ID, Key: "sk-delete", Name: "k"})
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "acc-delete"})

	log := s.createUsageLog(user, apiKey, provider, 10, 20, 0.5, time.Now())

	err := s.repo.Delete(s.ctx, log.ID)
	s.Require().NoError(err, "Delete")

	_, err = s.repo.GetByID(s.ctx, log.ID)
	s.Require().Error(err, "expected error after delete")
}

// TestListByUser records two requests for one actor in the suite transaction. It
// checks the user listing contains both rows and reports an exact total of two.
func (s *UsageLogRepoSuite) TestListByUser() {
	user := mustCreateUser(s.T(), s.client, &identity.User{Email: "listbyuser@test.com"})
	apiKey := mustCreateApiKey(s.T(), s.client, &apikey.APIKey{UserID: user.ID, Key: "sk-listbyuser", Name: "k"})
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "acc-listbyuser"})

	s.createUsageLog(user, apiKey, provider, 10, 20, 0.5, time.Now())
	s.createUsageLog(user, apiKey, provider, 15, 25, 0.6, time.Now())

	logs, page, err := s.repo.ListByUser(s.ctx, user.ID, pagination.PaginationParams{Page: 1, PageSize: 10})
	s.Require().NoError(err, "ListByUser")
	s.Require().Len(logs, 2)
	s.Require().Equal(int64(2), page.Total)
}

// TestListByAPIKey records two requests under one key in the suite transaction.
// It checks the key listing returns both rows with an exact total of two.
func (s *UsageLogRepoSuite) TestListByAPIKey() {
	user := mustCreateUser(s.T(), s.client, &identity.User{Email: "listbyapikey@test.com"})
	apiKey := mustCreateApiKey(s.T(), s.client, &apikey.APIKey{UserID: user.ID, Key: "sk-listbyapikey", Name: "k"})
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "acc-listbyapikey"})

	s.createUsageLog(user, apiKey, provider, 10, 20, 0.5, time.Now())
	s.createUsageLog(user, apiKey, provider, 15, 25, 0.6, time.Now())

	logs, page, err := s.repo.ListByAPIKey(s.ctx, apiKey.ID, pagination.PaginationParams{Page: 1, PageSize: 10})
	s.Require().NoError(err, "ListByAPIKey")
	s.Require().Len(logs, 2)
	s.Require().Equal(int64(2), page.Total)
}

// TestListByProvider records one request through the selected execution
// provider. It checks the provider listing returns that row and reports an exact
// total of one.
func (s *UsageLogRepoSuite) TestListByProvider() {
	user := mustCreateUser(s.T(), s.client, &identity.User{Email: "listbyprovider@test.com"})
	apiKey := mustCreateApiKey(s.T(), s.client, &apikey.APIKey{UserID: user.ID, Key: "sk-listbyprovider", Name: "k"})
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "acc-listbyprovider"})

	s.createUsageLog(user, apiKey, provider, 10, 20, 0.5, time.Now())

	logs, page, err := s.repo.ListByProvider(s.ctx, provider.ID, pagination.PaginationParams{Page: 1, PageSize: 10})
	s.Require().NoError(err, "ListByProvider")
	s.Require().Len(logs, 1)
	s.Require().Equal(int64(1), page.Total)
}

// TestGetUserStats records two actor requests inside a fixed bounded interval.
// It checks request count and separate input/output token sums from the raw
// summary.
func (s *UsageLogRepoSuite) TestGetUserStats() {
	user := mustCreateUser(s.T(), s.client, &identity.User{Email: "userstats@test.com"})
	apiKey := mustCreateApiKey(s.T(), s.client, &apikey.APIKey{UserID: user.ID, Key: "sk-userstats", Name: "k"})
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "acc-userstats"})

	base := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	s.createUsageLog(user, apiKey, provider, 10, 20, 0.5, base)
	s.createUsageLog(user, apiKey, provider, 15, 25, 0.6, base.Add(1*time.Hour))

	startTime := base.Add(-1 * time.Hour)
	endTime := base.Add(2 * time.Hour)
	stats, err := s.repo.GetUserStats(s.ctx, user.ID, startTime, endTime)
	s.Require().NoError(err, "GetUserStats")
	s.Require().Equal(int64(2), stats.TotalRequests)
	s.Require().Equal(int64(25), stats.InputTokens)
	s.Require().Equal(int64(45), stats.OutputTokens)
}

// TestListWithFilters selects one fixture actor after recording its usage. It
// checks the filtered listing contains one row and the counted page reports one
// match.
func (s *UsageLogRepoSuite) TestListWithFilters() {
	user := mustCreateUser(s.T(), s.client, &identity.User{Email: "filters@test.com"})
	apiKey := mustCreateApiKey(s.T(), s.client, &apikey.APIKey{UserID: user.ID, Key: "sk-filters", Name: "k"})
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "acc-filters"})

	s.createUsageLog(user, apiKey, provider, 10, 20, 0.5, time.Now())

	filters := usage.UsageLogFilters{UserID: user.ID}
	logs, page, err := s.repo.ListWithFilters(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 10}, filters)
	s.Require().NoError(err, "ListWithFilters")
	s.Require().Len(logs, 1)
	s.Require().Equal(int64(1), page.Total)
}

// TestDashboardStats_TodayTotalsAndPerformance adds users, key/provider states,
// current/older usage, and rollups beside a baseline dashboard. It checks
// selected count/cost/token increments and raw recent-rate parity.
func (s *UsageLogRepoSuite) TestDashboardStats_TodayTotalsAndPerformance() {
	now := time.Now().UTC()
	todayStart := truncateToDayUTC(now)
	baseStats, err := s.repo.GetDashboardStats(s.ctx)
	s.Require().NoError(err, "GetDashboardStats base")

	userToday := mustCreateUser(s.T(), s.client, &identity.User{
		Email:     "today@example.com",
		CreatedAt: testMaxTime(todayStart.Add(10*time.Second), now.Add(-10*time.Second)),
		UpdatedAt: now,
	})
	userOld := mustCreateUser(s.T(), s.client, &identity.User{
		Email:     "old@example.com",
		CreatedAt: todayStart.Add(-24 * time.Hour),
		UpdatedAt: todayStart.Add(-24 * time.Hour),
	})

	group := mustCreateGroup(s.T(), s.client, &routing.Group{Name: "g-ul"})
	apiKey1 := mustCreateApiKey(s.T(), s.client, &apikey.APIKey{UserID: userToday.ID, Key: "sk-ul-1", Name: "ul1"})
	mustCreateApiKey(s.T(), s.client, &apikey.APIKey{UserID: userOld.ID, Key: "sk-ul-2", Name: "ul2", Status: billing.StatusDisabled})

	resetAt := now.Add(10 * time.Minute)
	accNormal := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "a-normal", Schedulable: true})
	mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "a-error", Status: providercore.StatusError, Schedulable: true})
	mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "a-rl", RateLimitedAt: &now, RateLimitResetAt: &resetAt, Schedulable: true})
	mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "a-ov", OverloadUntil: &resetAt, Schedulable: true})

	d1, d2, d3 := 100, 200, 300
	logToday := &usage.UsageLog{
		UserID:              userToday.ID,
		APIKeyID:            apiKey1.ID,
		ProviderID:          accNormal.ID,
		Model:               "claude-3",
		GroupID:             &group.ID,
		InputTokens:         10,
		OutputTokens:        20,
		CacheCreationTokens: 3,
		CacheReadTokens:     4,
		TotalCost:           1.5,
		ActualCost:          1.2,
		DurationMs:          &d1,
		CreatedAt:           testMaxTime(todayStart.Add(2*time.Minute), now.Add(-2*time.Minute)),
	}
	_, err = s.repo.Create(s.ctx, logToday)
	s.Require().NoError(err, "Create logToday")

	logOld := &usage.UsageLog{
		UserID:       userOld.ID,
		APIKeyID:     apiKey1.ID,
		ProviderID:   accNormal.ID,
		Model:        "claude-3",
		InputTokens:  5,
		OutputTokens: 6,
		TotalCost:    0.7,
		ActualCost:   0.7,
		DurationMs:   &d2,
		CreatedAt:    todayStart.Add(-1 * time.Hour),
	}
	_, err = s.repo.Create(s.ctx, logOld)
	s.Require().NoError(err, "Create logOld")

	logPerf := &usage.UsageLog{
		UserID:       userToday.ID,
		APIKeyID:     apiKey1.ID,
		ProviderID:   accNormal.ID,
		Model:        "claude-3",
		InputTokens:  1,
		OutputTokens: 2,
		TotalCost:    0.1,
		ActualCost:   0.1,
		DurationMs:   &d3,
		CreatedAt:    now.Add(-30 * time.Second),
	}
	_, err = s.repo.Create(s.ctx, logPerf)
	s.Require().NoError(err, "Create logPerf")

	aggRepo := newDashboardAggregationRepositoryWithSQL(s.tx)
	aggStart := todayStart.Add(-2 * time.Hour)
	aggEnd := now.Add(2 * time.Minute)
	s.Require().NoError(aggRepo.AggregateRange(s.ctx, aggStart, aggEnd), "AggregateRange")

	stats, err := s.repo.GetDashboardStats(s.ctx)
	s.Require().NoError(err, "GetDashboardStats")

	s.Require().Equal(baseStats.TotalUsers+2, stats.TotalUsers, "TotalUsers mismatch")
	s.Require().Equal(baseStats.TodayNewUsers+1, stats.TodayNewUsers, "TodayNewUsers mismatch")
	s.Require().Equal(baseStats.ActiveUsers+1, stats.ActiveUsers, "ActiveUsers mismatch")
	s.Require().Equal(baseStats.TotalAPIKeys+2, stats.TotalAPIKeys, "TotalAPIKeys mismatch")
	s.Require().Equal(baseStats.ActiveAPIKeys+1, stats.ActiveAPIKeys, "ActiveAPIKeys mismatch")
	s.Require().Equal(baseStats.TotalProviders+4, stats.TotalProviders, "TotalProviders mismatch")
	s.Require().Equal(baseStats.ErrorProviders+1, stats.ErrorProviders, "ErrorProviders mismatch")
	s.Require().Equal(baseStats.RateLimitProviders+1, stats.RateLimitProviders, "RateLimitProviders mismatch")
	s.Require().Equal(baseStats.OverloadProviders+1, stats.OverloadProviders, "OverloadProviders mismatch")

	s.Require().Equal(baseStats.TotalRequests+3, stats.TotalRequests, "TotalRequests mismatch")
	s.Require().Equal(baseStats.TotalInputTokens+int64(16), stats.TotalInputTokens, "TotalInputTokens mismatch")
	s.Require().Equal(baseStats.TotalOutputTokens+int64(28), stats.TotalOutputTokens, "TotalOutputTokens mismatch")
	s.Require().Equal(baseStats.TotalCacheCreationTokens+int64(3), stats.TotalCacheCreationTokens, "TotalCacheCreationTokens mismatch")
	s.Require().Equal(baseStats.TotalCacheReadTokens+int64(4), stats.TotalCacheReadTokens, "TotalCacheReadTokens mismatch")
	s.Require().Equal(baseStats.TotalTokens+int64(51), stats.TotalTokens, "TotalTokens mismatch")
	s.Require().Equal(baseStats.TotalCost+2.3, stats.TotalCost, "TotalCost mismatch")
	s.Require().Equal(baseStats.TotalActualCost+2.0, stats.TotalActualCost, "TotalActualCost mismatch")
	// These rows omit provider-specific bases/multipliers, so provider expense uses
	// their base costs and remains distinct from the lower actual user charges.
	s.Require().Equal(baseStats.TotalProviderCost+2.3, stats.TotalProviderCost, "TotalProviderCost mismatch")
	s.Require().GreaterOrEqual(stats.TodayRequests, int64(1), "expected TodayRequests >= 1")
	s.Require().GreaterOrEqual(stats.TodayCost, 0.0, "expected TodayCost >= 0")
	s.Require().GreaterOrEqual(stats.TodayProviderCost, 0.0, "expected TodayProviderCost >= 0")

	wantRpm, wantTpm, err := s.repo.getPerformanceStats(s.ctx, 0, false)
	s.Require().NoError(err, "getPerformanceStats")
	s.Require().Equal(wantRpm, stats.Rpm, "Rpm mismatch")
	s.Require().Equal(wantTpm, stats.Tpm, "Tpm mismatch")
}

// TestDashboardStatsWithRange_Fallback records one row outside and two rows
// inside the requested raw span. It checks bounded totals, separate monetary
// measures, and the all-request duration average.
func (s *UsageLogRepoSuite) TestDashboardStatsWithRange_Fallback() {
	now := time.Now().UTC()
	todayStart := truncateToDayUTC(now)
	rangeStart := todayStart.Add(-24 * time.Hour)
	rangeEnd := now.Add(1 * time.Second)

	user1 := mustCreateUser(s.T(), s.client, &identity.User{Email: "range-u1@test.com"})
	user2 := mustCreateUser(s.T(), s.client, &identity.User{Email: "range-u2@test.com"})
	apiKey1 := mustCreateApiKey(s.T(), s.client, &apikey.APIKey{UserID: user1.ID, Key: "sk-range-1", Name: "k1"})
	apiKey2 := mustCreateApiKey(s.T(), s.client, &apikey.APIKey{UserID: user2.ID, Key: "sk-range-2", Name: "k2"})
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "acc-range"})

	d1, d2, d3 := 100, 200, 300
	logOutside := &usage.UsageLog{
		UserID:       user1.ID,
		APIKeyID:     apiKey1.ID,
		ProviderID:   provider.ID,
		Model:        "claude-3",
		InputTokens:  7,
		OutputTokens: 8,
		TotalCost:    0.8,
		ActualCost:   0.7,
		DurationMs:   &d3,
		CreatedAt:    rangeStart.Add(-1 * time.Hour),
	}
	_, err := s.repo.Create(s.ctx, logOutside)
	s.Require().NoError(err)

	logRange := &usage.UsageLog{
		UserID:              user1.ID,
		APIKeyID:            apiKey1.ID,
		ProviderID:          provider.ID,
		Model:               "claude-3",
		InputTokens:         10,
		OutputTokens:        20,
		CacheCreationTokens: 1,
		CacheReadTokens:     2,
		TotalCost:           1.0,
		ActualCost:          0.9,
		DurationMs:          &d1,
		CreatedAt:           rangeStart.Add(2 * time.Hour),
	}
	_, err = s.repo.Create(s.ctx, logRange)
	s.Require().NoError(err)

	logToday := &usage.UsageLog{
		UserID:          user2.ID,
		APIKeyID:        apiKey2.ID,
		ProviderID:      provider.ID,
		Model:           "claude-3",
		InputTokens:     5,
		OutputTokens:    6,
		CacheReadTokens: 1,
		TotalCost:       0.5,
		ActualCost:      0.5,
		DurationMs:      &d2,
		CreatedAt:       now,
	}
	_, err = s.repo.Create(s.ctx, logToday)
	s.Require().NoError(err)

	stats, err := s.repo.GetDashboardStatsWithRange(s.ctx, rangeStart, rangeEnd)
	s.Require().NoError(err)
	s.Require().Equal(int64(2), stats.TotalRequests)
	s.Require().Equal(int64(15), stats.TotalInputTokens)
	s.Require().Equal(int64(26), stats.TotalOutputTokens)
	s.Require().Equal(int64(1), stats.TotalCacheCreationTokens)
	s.Require().Equal(int64(3), stats.TotalCacheReadTokens)
	s.Require().Equal(int64(45), stats.TotalTokens)
	s.Require().Equal(1.5, stats.TotalCost)
	s.Require().Equal(1.4, stats.TotalActualCost)
	// Absent provider bases and multipliers make provider expense equal the fixture
	// base costs. Use relative float tolerance for the returned duration average.
	s.Require().Equal(1.5, stats.TotalProviderCost)
	s.Require().InEpsilon(150.0, stats.AverageDurationMs, 0.0001)
}

// TestGetUserDashboardStats records one actor request under one key in the suite
// transaction. It checks the dashboard reports one key and one retained request.
func (s *UsageLogRepoSuite) TestGetUserDashboardStats() {
	user := mustCreateUser(s.T(), s.client, &identity.User{Email: "userdash@test.com"})
	apiKey := mustCreateApiKey(s.T(), s.client, &apikey.APIKey{UserID: user.ID, Key: "sk-userdash", Name: "k"})
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "acc-userdash"})

	s.createUsageLog(user, apiKey, provider, 10, 20, 0.5, time.Now())

	stats, err := s.repo.GetUserDashboardStats(s.ctx, user.ID)
	s.Require().NoError(err, "GetUserDashboardStats")
	s.Require().Equal(int64(1), stats.TotalAPIKeys)
	s.Require().Equal(int64(1), stats.TotalRequests)
}

// TestGetUserDashboardStatsIncludesOwnedTeamWithoutDuplicates records
// owner/member personal and team-key usage. It checks owner visibility counts
// team rows once and the member's actor view retains its two requests.
func (s *UsageLogRepoSuite) TestGetUserDashboardStatsIncludesOwnedTeamWithoutDuplicates() {
	owner := mustCreateUser(s.T(), s.client, &identity.User{Email: "userdash-owner@test.com"})
	member := mustCreateUser(s.T(), s.client, &identity.User{Email: "userdash-member@test.com"})
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "acc-userdash-team"})

	team, err := s.client.Team.Create().
		SetName("仪表盘范围测试团队").
		SetStatus("active").
		SetMemberLimit(10).
		Save(s.ctx)
	s.Require().NoError(err)
	teamID := team.ID
	_, err = s.client.TeamMembership.Create().
		SetTeamID(teamID).
		SetUserID(owner.ID).
		SetRole(teamcore.TeamRoleOwner).
		Save(s.ctx)
	s.Require().NoError(err)
	_, err = s.client.TeamMembership.Create().
		SetTeamID(teamID).
		SetUserID(member.ID).
		SetRole(teamcore.TeamRoleMember).
		Save(s.ctx)
	s.Require().NoError(err)

	ownerPersonalKey := mustCreateApiKey(s.T(), s.client, &apikey.APIKey{UserID: owner.ID, Key: "sk-userdash-owner-personal"})
	ownerTeamKey := mustCreateApiKey(s.T(), s.client, &apikey.APIKey{UserID: owner.ID, TeamID: &teamID, Key: "sk-userdash-owner-team"})
	memberTeamKey := mustCreateApiKey(s.T(), s.client, &apikey.APIKey{UserID: member.ID, TeamID: &teamID, Key: "sk-userdash-member-team"})
	memberPersonalKey := mustCreateApiKey(s.T(), s.client, &apikey.APIKey{UserID: member.ID, Key: "sk-userdash-member-personal"})

	// Fixed timestamps test all-time totals; today's totals depend on the test
	// date.
	createdAt := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	// Rows cover the owner, a team member, the owner's team key, and a member's
	// personal key.
	logs := []*usage.UsageLog{
		{
			UserID: owner.ID, BillingUserID: owner.ID, APIKeyID: ownerPersonalKey.ID, ProviderID: provider.ID,
			RequestID: uuid.NewString(), Model: "dashboard-scope", InputTokens: 10, OutputTokens: 1,
			TotalCost: 0.1, ActualCost: 0.1, CreatedAt: createdAt,
		},
		{
			UserID: member.ID, BillingUserID: owner.ID, TeamID: &teamID, APIKeyID: memberTeamKey.ID, ProviderID: provider.ID,
			RequestID: uuid.NewString(), Model: "dashboard-scope", InputTokens: 20, OutputTokens: 2,
			TotalCost: 0.2, ActualCost: 0.2, CreatedAt: createdAt,
		},
		{
			UserID: owner.ID, BillingUserID: owner.ID, TeamID: &teamID, APIKeyID: ownerTeamKey.ID, ProviderID: provider.ID,
			RequestID: uuid.NewString(), Model: "dashboard-scope", InputTokens: 30, OutputTokens: 3,
			TotalCost: 0.3, ActualCost: 0.3, CreatedAt: createdAt,
		},
		{
			UserID: member.ID, BillingUserID: member.ID, APIKeyID: memberPersonalKey.ID, ProviderID: provider.ID,
			RequestID: uuid.NewString(), Model: "dashboard-scope", InputTokens: 40, OutputTokens: 4,
			TotalCost: 0.4, ActualCost: 0.4, CreatedAt: createdAt,
		},
	}
	for _, log := range logs {
		_, err = s.repo.Create(s.ctx, log)
		s.Require().NoError(err)
	}

	ownerStats, err := s.repo.GetUserDashboardStats(s.ctx, owner.ID)
	s.Require().NoError(err)
	s.Require().Equal(int64(3), ownerStats.TotalAPIKeys)
	s.Require().Equal(int64(3), ownerStats.ActiveAPIKeys)
	s.Require().Equal(int64(3), ownerStats.TotalRequests)
	s.Require().Equal(int64(60), ownerStats.TotalInputTokens)
	s.Require().Equal(int64(6), ownerStats.TotalOutputTokens)
	s.Require().Equal(int64(66), ownerStats.TotalTokens)
	// Allow float summation differences in the owner's three retained row costs.
	s.Require().InDelta(0.6, ownerStats.TotalCost, 0.000001)

	memberStats, err := s.repo.GetUserDashboardStats(s.ctx, member.ID)
	s.Require().NoError(err)
	s.Require().Equal(int64(2), memberStats.TotalAPIKeys)
	s.Require().Equal(int64(2), memberStats.TotalRequests)
	s.Require().Equal(int64(60), memberStats.TotalInputTokens)
	s.Require().Equal(int64(6), memberStats.TotalOutputTokens)
}

// TestGetProviderTodayStats records current-day provider usage with nonunit and
// zero multipliers. It checks requests, cache-inclusive tokens, and distinct
// provider/base/user cost sums.
func (s *UsageLogRepoSuite) TestGetProviderTodayStats() {
	user := mustCreateUser(s.T(), s.client, &identity.User{Email: "acctoday@test.com"})
	apiKey := mustCreateApiKey(s.T(), s.client, &apikey.APIKey{UserID: user.ID, Key: "sk-acctoday", Name: "k"})
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "acc-today"})

	createdAt := timezone.NewCalendar(time.Local).
		Today().Add(1 * time.Hour)

	m1 := 1.5
	m2 := 0.0
	_, err := s.repo.Create(s.ctx, &usage.UsageLog{
		UserID:                 user.ID,
		APIKeyID:               apiKey.ID,
		ProviderID:             provider.ID,
		RequestID:              uuid.New().String(),
		Model:                  "claude-3",
		InputTokens:            10,
		OutputTokens:           20,
		TotalCost:              1.0,
		ActualCost:             2.0,
		ProviderRateMultiplier: &m1,
		CreatedAt:              createdAt,
	})
	s.Require().NoError(err)
	_, err = s.repo.Create(s.ctx, &usage.UsageLog{
		UserID:                 user.ID,
		APIKeyID:               apiKey.ID,
		ProviderID:             provider.ID,
		RequestID:              uuid.New().String(),
		Model:                  "claude-3",
		InputTokens:            5,
		OutputTokens:           5,
		TotalCost:              0.5,
		ActualCost:             1.0,
		ProviderRateMultiplier: &m2,
		CreatedAt:              createdAt,
	})
	s.Require().NoError(err)

	stats, err := s.repo.GetProviderTodayStats(s.ctx, provider.ID)
	s.Require().NoError(err, "GetProviderTodayStats")
	s.Require().Equal(int64(2), stats.Requests)
	s.Require().Equal(int64(40), stats.Tokens)
	// The zero multiplier removes the second row from provider expense while both
	// base costs and user charges remain. Apply one relative float tolerance to
	// these three independently summed monetary measures.
	s.Require().InEpsilon(1.5, stats.Cost, 0.0001)
	s.Require().InEpsilon(1.5, stats.StandardCost, 0.0001)
	s.Require().InEpsilon(3.0, stats.UserCost, 0.0001)
}

// TestDashboardAggregationConsistency records two completed hours and builds
// dashboard plus analytics rollups. It checks selected hourly/daily metrics,
// then adds a late row and checks recomputed hourly totals.
func (s *UsageLogRepoSuite) TestDashboardAggregationConsistency() {
	now := time.Now().UTC().Truncate(time.Second)
	// Pick completed UTC hours on one date so dashboard day totals can be compared
	// with their hourly fixtures before the later recomputation scenario.
	dayStart := truncateToDayUTC(now)
	hour1 := dayStart.Add(2 * time.Hour) // 02:00 UTC
	hour2 := dayStart.Add(3 * time.Hour) // 03:00 UTC
	// If the second fixture hour is still live, move both hours to yesterday so
	// aggregation assertions inspect complete hours regardless of the current test
	// time.
	if now.Before(hour2.Add(time.Hour)) {
		dayStart = dayStart.Add(-24 * time.Hour)
		hour1 = dayStart.Add(2 * time.Hour)
		hour2 = dayStart.Add(3 * time.Hour)
	}

	user1 := mustCreateUser(s.T(), s.client, &identity.User{Email: "agg-u1@test.com"})
	user2 := mustCreateUser(s.T(), s.client, &identity.User{Email: "agg-u2@test.com"})
	apiKey1 := mustCreateApiKey(s.T(), s.client, &apikey.APIKey{UserID: user1.ID, Key: "sk-agg-1", Name: "k1"})
	apiKey2 := mustCreateApiKey(s.T(), s.client, &apikey.APIKey{UserID: user2.ID, Key: "sk-agg-2", Name: "k2"})
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "acc-agg"})

	d1, d2, d3 := 100, 200, 150
	log1 := &usage.UsageLog{
		UserID:              user1.ID,
		APIKeyID:            apiKey1.ID,
		ProviderID:          provider.ID,
		Model:               "claude-3",
		InputTokens:         10,
		OutputTokens:        20,
		CacheCreationTokens: 2,
		CacheReadTokens:     1,
		TotalCost:           1.0,
		ActualCost:          0.9,
		DurationMs:          &d1,
		CreatedAt:           hour1.Add(5 * time.Minute),
	}
	_, err := s.repo.Create(s.ctx, log1)
	s.Require().NoError(err)

	log2 := &usage.UsageLog{
		UserID:       user1.ID,
		APIKeyID:     apiKey1.ID,
		ProviderID:   provider.ID,
		Model:        "claude-3",
		InputTokens:  5,
		OutputTokens: 5,
		TotalCost:    0.5,
		ActualCost:   0.5,
		DurationMs:   &d2,
		CreatedAt:    hour1.Add(20 * time.Minute),
	}
	_, err = s.repo.Create(s.ctx, log2)
	s.Require().NoError(err)

	log3 := &usage.UsageLog{
		UserID:       user2.ID,
		APIKeyID:     apiKey2.ID,
		ProviderID:   provider.ID,
		Model:        "claude-3",
		InputTokens:  7,
		OutputTokens: 8,
		TotalCost:    0.7,
		ActualCost:   0.7,
		DurationMs:   &d3,
		CreatedAt:    hour2.Add(10 * time.Minute),
	}
	_, err = s.repo.Create(s.ctx, log3)
	s.Require().NoError(err)

	aggRepo := newDashboardAggregationRepositoryWithSQL(s.tx)
	aggStart := hour1.Add(-5 * time.Minute)
	aggEnd := hour2.Add(time.Hour) // Include the entire second hour.
	s.Require().NoError(aggRepo.AggregateRange(s.ctx, aggStart, aggEnd))
	s.Require().NoError(aggRepo.AggregateUsageAnalyticsRange(s.ctx, aggStart, aggEnd))

	// hourlyRow holds the selected dashboard bucket metrics asserted by this
	// fixture.
	type hourlyRow struct {
		totalRequests       int64   // requests in selected bucket
		inputTokens         int64   // recorded input token sum
		outputTokens        int64   // recorded output token sum
		cacheCreationTokens int64   // recorded cache-creation token sum
		cacheReadTokens     int64   // recorded cache-read token sum
		totalCost           float64 // base costs in USD
		actualCost          float64 // user charges in USD
		totalDurationMs     int64   // milliseconds
		activeUsers         int64   // distinct bucket actor count
	}
	fetchHourly := func(bucketStart time.Time) hourlyRow {
		var row hourlyRow
		err := scanSingleRow(s.ctx, s.tx, `
			SELECT total_requests, input_tokens, output_tokens, cache_creation_tokens, cache_read_tokens,
			       total_cost, actual_cost, total_duration_ms, active_users
			FROM usage_dashboard_hourly
			WHERE bucket_start = $1
		`, []any{bucketStart}, &row.totalRequests, &row.inputTokens, &row.outputTokens,
			&row.cacheCreationTokens, &row.cacheReadTokens, &row.totalCost, &row.actualCost,
			&row.totalDurationMs, &row.activeUsers,
		)
		s.Require().NoError(err)
		return row
	}

	hour1Row := fetchHourly(hour1)
	s.Require().Equal(int64(2), hour1Row.totalRequests)
	s.Require().Equal(int64(15), hour1Row.inputTokens)
	s.Require().Equal(int64(25), hour1Row.outputTokens)
	s.Require().Equal(int64(2), hour1Row.cacheCreationTokens)
	s.Require().Equal(int64(1), hour1Row.cacheReadTokens)
	s.Require().Equal(1.5, hour1Row.totalCost)
	s.Require().Equal(1.4, hour1Row.actualCost)
	s.Require().Equal(int64(300), hour1Row.totalDurationMs)
	s.Require().Equal(int64(1), hour1Row.activeUsers)

	hour2Row := fetchHourly(hour2)
	s.Require().Equal(int64(1), hour2Row.totalRequests)
	s.Require().Equal(int64(7), hour2Row.inputTokens)
	s.Require().Equal(int64(8), hour2Row.outputTokens)
	s.Require().Equal(int64(0), hour2Row.cacheCreationTokens)
	s.Require().Equal(int64(0), hour2Row.cacheReadTokens)
	s.Require().Equal(0.7, hour2Row.totalCost)
	s.Require().Equal(0.7, hour2Row.actualCost)
	s.Require().Equal(int64(150), hour2Row.totalDurationMs)
	s.Require().Equal(int64(1), hour2Row.activeUsers)

	var daily struct {
		totalRequests       int64   // requests in selected bucket
		inputTokens         int64   // recorded input token sum
		outputTokens        int64   // recorded output token sum
		cacheCreationTokens int64   // recorded cache-creation token sum
		cacheReadTokens     int64   // recorded cache-read token sum
		totalCost           float64 // base costs in USD
		actualCost          float64 // user charges in USD
		totalDurationMs     int64   // milliseconds
		activeUsers         int64   // distinct bucket actor count
	}
	err = scanSingleRow(s.ctx, s.tx, `
		SELECT total_requests, input_tokens, output_tokens, cache_creation_tokens, cache_read_tokens,
		       total_cost, actual_cost, total_duration_ms, active_users
		FROM usage_dashboard_daily
		WHERE bucket_date = $1::date
	`, []any{dayStart}, &daily.totalRequests, &daily.inputTokens, &daily.outputTokens,
		&daily.cacheCreationTokens, &daily.cacheReadTokens, &daily.totalCost, &daily.actualCost,
		&daily.totalDurationMs, &daily.activeUsers,
	)
	s.Require().NoError(err)
	s.Require().Equal(int64(3), daily.totalRequests)
	s.Require().Equal(int64(22), daily.inputTokens)
	s.Require().Equal(int64(33), daily.outputTokens)
	s.Require().Equal(int64(2), daily.cacheCreationTokens)
	s.Require().Equal(int64(1), daily.cacheReadTokens)
	s.Require().Equal(2.2, daily.totalCost)
	s.Require().Equal(2.1, daily.actualCost)
	s.Require().Equal(int64(450), daily.totalDurationMs)
	s.Require().Equal(int64(2), daily.activeUsers)

	// Read the selected actor's first hourly range from analytics and compare its
	// request, token, duration, and cost sums to the corresponding fixture inputs.
	var analyticsRequests, analyticsInput, analyticsOutput, analyticsDuration int64
	var analyticsTotalCost, analyticsActualCost float64
	err = scanSingleRow(s.ctx, s.tx, `
		SELECT COALESCE(SUM(total_requests), 0), COALESCE(SUM(input_tokens), 0),
		       COALESCE(SUM(output_tokens), 0), COALESCE(SUM(total_duration_ms), 0),
		       COALESCE(SUM(total_cost), 0), COALESCE(SUM(actual_cost), 0)
		FROM usage_analytics_hourly
		WHERE user_id = $1 AND bucket_start >= $2 AND bucket_start < $3
	`, []any{user1.ID, hour1, hour2}, &analyticsRequests, &analyticsInput, &analyticsOutput,
		&analyticsDuration, &analyticsTotalCost, &analyticsActualCost)
	s.Require().NoError(err)
	s.Require().Equal(int64(2), analyticsRequests)
	s.Require().Equal(int64(15), analyticsInput)
	s.Require().Equal(int64(25), analyticsOutput)
	s.Require().Equal(int64(300), analyticsDuration)
	// Allow numeric-to-float conversion differences in both initial bucket costs.
	s.Require().InDelta(1.5, analyticsTotalCost, 0.000001)
	s.Require().InDelta(1.4, analyticsActualCost, 0.000001)

	// Insert a late row into an already computed hour, then rebuild that range; its
	// increased metrics must reflect all retained rows in the bucket.
	lateDuration := 50
	_, err = s.repo.Create(s.ctx, &usage.UsageLog{
		UserID: user1.ID, APIKeyID: apiKey1.ID, ProviderID: provider.ID,
		RequestID: uuid.NewString(), Model: "claude-3", InputTokens: 3, OutputTokens: 4,
		TotalCost: 0.2, ActualCost: 0.15, DurationMs: &lateDuration,
		CreatedAt: hour1.Add(45 * time.Minute),
	})
	s.Require().NoError(err)
	s.Require().NoError(aggRepo.AggregateUsageAnalyticsRange(s.ctx, hour1, hour1.Add(time.Hour)))

	err = scanSingleRow(s.ctx, s.tx, `
		SELECT COALESCE(SUM(total_requests), 0), COALESCE(SUM(input_tokens), 0),
		       COALESCE(SUM(output_tokens), 0), COALESCE(SUM(total_duration_ms), 0),
		       COALESCE(SUM(total_cost), 0), COALESCE(SUM(actual_cost), 0)
		FROM usage_analytics_hourly
		WHERE user_id = $1 AND bucket_start = $2
	`, []any{user1.ID, hour1}, &analyticsRequests, &analyticsInput, &analyticsOutput,
		&analyticsDuration, &analyticsTotalCost, &analyticsActualCost)
	s.Require().NoError(err)
	s.Require().Equal(int64(3), analyticsRequests)
	s.Require().Equal(int64(18), analyticsInput)
	s.Require().Equal(int64(29), analyticsOutput)
	s.Require().Equal(int64(350), analyticsDuration)
	// Apply the same absolute float tolerance to the rebuilt bucket's cost sums.
	s.Require().InDelta(1.7, analyticsTotalCost, 0.000001)
	s.Require().InDelta(1.55, analyticsActualCost, 0.000001)
}

// TestUsageAnalyticsQueriesExecuteOnPostgreSQL establishes covered
// Honolulu-window buckets with a fixed Calendar. It checks actual trend,
// summary, batch cost, and group queries execute and return the expected
// selected metrics.
func (s *UsageLogRepoSuite) TestUsageAnalyticsQueriesExecuteOnPostgreSQL() {
	loc, err := time.LoadLocation("Pacific/Honolulu")
	s.Require().NoError(err)
	now := time.Date(2026, time.October, 5, 22, 0, 0, 0, time.UTC)
	calendar := timezone.NewCalendarWithClock(loc, func() time.Time { return now })
	s.repo = NewUsageLogRepositoryWithSQL(s.client, s.tx, calendar)

	todayStart := calendar.Today().UTC()
	yesterdayStart := todayStart.AddDate(0, 0, -1)
	createdAt := todayStart.Add(90 * time.Minute)
	user := mustCreateUser(s.T(), s.client, &identity.User{Email: "analytics-query@test.com"})
	apiKey := mustCreateApiKey(s.T(), s.client, &apikey.APIKey{UserID: user.ID, Key: "sk-analytics-query", Name: "analytics"})
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "analytics-query-provider"})
	group := mustCreateGroup(s.T(), s.client, &routing.Group{Name: "analytics-query-group"})
	groupID := group.ID
	_, err = s.repo.Create(s.ctx, &usage.UsageLog{
		UserID: user.ID, APIKeyID: apiKey.ID, ProviderID: provider.ID, GroupID: &groupID,
		RequestID: uuid.NewString(), Model: "claude-3", RequestedModel: "claude-3",
		InputTokens: 10, OutputTokens: 20, TotalCost: 1, ActualCost: 0.8,
		CreatedAt: createdAt,
	})
	s.Require().NoError(err)

	aggRepo := NewAggregationStoreWithSQL(s.tx, calendar)
	s.Require().NoError(aggRepo.AggregateUsageAnalyticsRange(s.ctx, yesterdayStart, now))
	// Publish fixture coverage after bucket construction completes, allowing the
	// following analytics reads to select those known rows with matching watermark
	// metadata.
	_, err = s.tx.ExecContext(s.ctx, `
		UPDATE usage_analytics_aggregation_state
		SET live_watermark = $1, coverage_start = $2, backfill_cursor = $2,
		    source_oldest_at = $3, phase = 'idle'
		WHERE id = 1
	`, now, yesterdayStart, createdAt)
	s.Require().NoError(err)
	s.repo.preAggregation = preaggregation.NewPreAggregationSettingsService(nil, &preaggregation.Options{
		Usage: preaggregation.UsageOptions{Enabled: true, IntervalSeconds: 60},
	})

	trend, ok, err := s.repo.getUsageTrendFromAnalytics(s.ctx, todayStart, now, "hour", UsageLogFilters{})
	s.Require().NoError(err)
	s.Require().True(ok)
	s.Require().NotEmpty(trend)

	stats, ok, err := s.repo.getUsageStatsFromAnalytics(s.ctx, UsageLogFilters{StartTime: &todayStart, EndTime: &now})
	s.Require().NoError(err)
	s.Require().True(ok)
	s.Require().Equal(int64(1), stats.TotalRequests)

	// Use the same absolute float tolerance for key, actor, and group charge
	// readbacks below; each represents the fixture's decimal amount.
	keyStats, ok, err := s.repo.getBatchAPIKeyUsageStatsFromAnalytics(s.ctx, []int64{apiKey.ID}, todayStart, now)
	s.Require().NoError(err)
	s.Require().True(ok)
	s.Require().InDelta(0.8, keyStats[apiKey.ID].TodayActualCost, 0.000001)

	userStats, ok, err := s.repo.getBatchUserUsageStatsFromAnalytics(s.ctx, []int64{user.ID}, todayStart, now)
	s.Require().NoError(err)
	s.Require().True(ok)
	s.Require().InDelta(0.8, userStats[user.ID].TodayActualCost, 0.000001)

	groups, ok, err := s.repo.getAllGroupUsageSummaryFromAnalytics(s.ctx, todayStart)
	s.Require().NoError(err)
	s.Require().True(ok)
	foundGroup := false
	for _, item := range groups {
		if item.GroupID == group.ID {
			foundGroup = true
			s.Require().InDelta(0.8, item.TodayCost, 0.000001)
			break
		}
	}
	s.Require().True(foundGroup)
}

// TestGetBatchUserUsageStats records two actors and requests a zero-bound batch
// summary. It checks both requested map entries exist, without asserting their
// monetary values.
func (s *UsageLogRepoSuite) TestGetBatchUserUsageStats() {
	user1 := mustCreateUser(s.T(), s.client, &identity.User{Email: "batch1@test.com"})
	user2 := mustCreateUser(s.T(), s.client, &identity.User{Email: "batch2@test.com"})
	apiKey1 := mustCreateApiKey(s.T(), s.client, &apikey.APIKey{UserID: user1.ID, Key: "sk-batch1", Name: "k"})
	apiKey2 := mustCreateApiKey(s.T(), s.client, &apikey.APIKey{UserID: user2.ID, Key: "sk-batch2", Name: "k"})
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "acc-batch"})

	s.createUsageLog(user1, apiKey1, provider, 10, 20, 0.5, time.Now())
	s.createUsageLog(user2, apiKey2, provider, 15, 25, 0.6, time.Now())

	stats, err := s.repo.GetBatchUserUsageStats(s.ctx, []int64{user1.ID, user2.ID}, time.Time{}, time.Time{})
	s.Require().NoError(err, "GetBatchUserUsageStats")
	s.Require().Len(stats, 2)
	s.Require().NotNil(stats[user1.ID])
	s.Require().NotNil(stats[user2.ID])
}

// TestGetBatchUserUsageStats_Empty supplies no actor IDs with zero time bounds.
// It checks the batch reader succeeds with an empty result rather than querying
// fixture usage.
func (s *UsageLogRepoSuite) TestGetBatchUserUsageStats_Empty() {
	stats, err := s.repo.GetBatchUserUsageStats(s.ctx, []int64{}, time.Time{}, time.Time{})
	s.Require().NoError(err)
	s.Require().Empty(stats)
}

// TestGetBatchApiKeyUsageStats records usage for two keys and requests
// zero-bound summaries. It checks two result entries are returned, without
// asserting each decoded charge.
func (s *UsageLogRepoSuite) TestGetBatchApiKeyUsageStats() {
	user := mustCreateUser(s.T(), s.client, &identity.User{Email: "batchkey@test.com"})
	apiKey1 := mustCreateApiKey(s.T(), s.client, &apikey.APIKey{UserID: user.ID, Key: "sk-batchkey1", Name: "k1"})
	apiKey2 := mustCreateApiKey(s.T(), s.client, &apikey.APIKey{UserID: user.ID, Key: "sk-batchkey2", Name: "k2"})
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "acc-batchkey"})

	s.createUsageLog(user, apiKey1, provider, 10, 20, 0.5, time.Now())
	s.createUsageLog(user, apiKey2, provider, 15, 25, 0.6, time.Now())

	stats, err := s.repo.GetBatchAPIKeyUsageStats(s.ctx, []int64{apiKey1.ID, apiKey2.ID}, time.Time{}, time.Time{})
	s.Require().NoError(err, "GetBatchAPIKeyUsageStats")
	s.Require().Len(stats, 2)
}

// TestGetBatchApiKeyUsageStats_Empty supplies an empty key-ID list with zero
// bounds. It checks the batch reader returns no error and an empty result.
func (s *UsageLogRepoSuite) TestGetBatchApiKeyUsageStats_Empty() {
	stats, err := s.repo.GetBatchAPIKeyUsageStats(s.ctx, []int64{}, time.Time{}, time.Time{})
	s.Require().NoError(err)
	s.Require().Empty(stats)
}

// TestGetGlobalStats records two rows in a fixed half-open query span. It checks
// global request count and separate input/output token totals for that selected
// window.
func (s *UsageLogRepoSuite) TestGetGlobalStats() {
	user := mustCreateUser(s.T(), s.client, &identity.User{Email: "global@test.com"})
	apiKey := mustCreateApiKey(s.T(), s.client, &apikey.APIKey{UserID: user.ID, Key: "sk-global", Name: "k"})
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "acc-global"})

	base := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	s.createUsageLog(user, apiKey, provider, 10, 20, 0.5, base)
	s.createUsageLog(user, apiKey, provider, 15, 25, 0.6, base.Add(1*time.Hour))

	stats, err := s.repo.GetGlobalStats(s.ctx, base.Add(-1*time.Hour), base.Add(2*time.Hour))
	s.Require().NoError(err, "GetGlobalStats")
	s.Require().Equal(int64(2), stats.TotalRequests)
	s.Require().Equal(int64(25), stats.TotalInputTokens)
	s.Require().Equal(int64(45), stats.TotalOutputTokens)
}

// TestListByUserAndTimeRange records two rows inside a bounded actor span and
// one earlier row. It checks the selected listing returns exactly the two
// in-range records.
func (s *UsageLogRepoSuite) TestListByUserAndTimeRange() {
	user := mustCreateUser(s.T(), s.client, &identity.User{Email: "timerange@test.com"})
	apiKey := mustCreateApiKey(s.T(), s.client, &apikey.APIKey{UserID: user.ID, Key: "sk-timerange", Name: "k"})
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "acc-timerange"})

	base := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	s.createUsageLog(user, apiKey, provider, 10, 20, 0.5, base)
	s.createUsageLog(user, apiKey, provider, 15, 25, 0.6, base.Add(1*time.Hour))
	s.createUsageLog(user, apiKey, provider, 20, 30, 0.7, base.Add(-24*time.Hour)) // outside range

	startTime := base.Add(-1 * time.Hour)
	endTime := base.Add(2 * time.Hour)
	logs, _, err := s.repo.ListByUserAndTimeRange(s.ctx, user.ID, startTime, endTime)
	s.Require().NoError(err, "ListByUserAndTimeRange")
	s.Require().Len(logs, 2)
}

// TestListByAPIKeyAndTimeRange records two rows inside a bounded key span and
// one earlier row. It checks the returned listing contains two matching usage
// records.
func (s *UsageLogRepoSuite) TestListByAPIKeyAndTimeRange() {
	user := mustCreateUser(s.T(), s.client, &identity.User{Email: "keytimerange@test.com"})
	apiKey := mustCreateApiKey(s.T(), s.client, &apikey.APIKey{UserID: user.ID, Key: "sk-keytimerange", Name: "k"})
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "acc-keytimerange"})

	base := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	s.createUsageLog(user, apiKey, provider, 10, 20, 0.5, base)
	s.createUsageLog(user, apiKey, provider, 15, 25, 0.6, base.Add(30*time.Minute))
	s.createUsageLog(user, apiKey, provider, 20, 30, 0.7, base.Add(-24*time.Hour)) // outside range

	startTime := base.Add(-1 * time.Hour)
	endTime := base.Add(2 * time.Hour)
	logs, _, err := s.repo.ListByAPIKeyAndTimeRange(s.ctx, apiKey.ID, startTime, endTime)
	s.Require().NoError(err, "ListByAPIKeyAndTimeRange")
	s.Require().Len(logs, 2)
}

// TestListByProviderAndTimeRange records two rows inside a bounded provider span
// and one earlier row. It checks the selected provider listing returns exactly
// two records.
func (s *UsageLogRepoSuite) TestListByProviderAndTimeRange() {
	user := mustCreateUser(s.T(), s.client, &identity.User{Email: "acctimerange@test.com"})
	apiKey := mustCreateApiKey(s.T(), s.client, &apikey.APIKey{UserID: user.ID, Key: "sk-acctimerange", Name: "k"})
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "acc-acctimerange"})

	base := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	s.createUsageLog(user, apiKey, provider, 10, 20, 0.5, base)
	s.createUsageLog(user, apiKey, provider, 15, 25, 0.6, base.Add(45*time.Minute))
	s.createUsageLog(user, apiKey, provider, 20, 30, 0.7, base.Add(-24*time.Hour)) // outside range

	startTime := base.Add(-1 * time.Hour)
	endTime := base.Add(2 * time.Hour)
	logs, _, err := s.repo.ListByProviderAndTimeRange(s.ctx, provider.ID, startTime, endTime)
	s.Require().NoError(err, "ListByProviderAndTimeRange")
	s.Require().Len(logs, 2)
}

// TestListByModelAndTimeRange records two matching stored models and one
// different model in the selected span. It checks the model equality listing
// returns two rows.
func (s *UsageLogRepoSuite) TestListByModelAndTimeRange() {
	user := mustCreateUser(s.T(), s.client, &identity.User{Email: "modeltimerange@test.com"})
	apiKey := mustCreateApiKey(s.T(), s.client, &apikey.APIKey{UserID: user.ID, Key: "sk-modeltimerange", Name: "k"})
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "acc-modeltimerange"})

	base := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)

	log1 := &usage.UsageLog{
		UserID:       user.ID,
		APIKeyID:     apiKey.ID,
		ProviderID:   provider.ID,
		Model:        "claude-3-opus",
		InputTokens:  10,
		OutputTokens: 20,
		TotalCost:    0.5,
		ActualCost:   0.5,
		CreatedAt:    base,
	}
	_, err := s.repo.Create(s.ctx, log1)
	s.Require().NoError(err)

	log2 := &usage.UsageLog{
		UserID:       user.ID,
		APIKeyID:     apiKey.ID,
		ProviderID:   provider.ID,
		Model:        "claude-3-opus",
		InputTokens:  15,
		OutputTokens: 25,
		TotalCost:    0.6,
		ActualCost:   0.6,
		CreatedAt:    base.Add(30 * time.Minute),
	}
	_, err = s.repo.Create(s.ctx, log2)
	s.Require().NoError(err)

	log3 := &usage.UsageLog{
		UserID:       user.ID,
		APIKeyID:     apiKey.ID,
		ProviderID:   provider.ID,
		Model:        "claude-3-sonnet",
		InputTokens:  20,
		OutputTokens: 30,
		TotalCost:    0.7,
		ActualCost:   0.7,
		CreatedAt:    base.Add(1 * time.Hour),
	}
	_, err = s.repo.Create(s.ctx, log3)
	s.Require().NoError(err)

	startTime := base.Add(-1 * time.Hour)
	endTime := base.Add(2 * time.Hour)
	logs, _, err := s.repo.ListByModelAndTimeRange(s.ctx, "claude-3-opus", startTime, endTime)
	s.Require().NoError(err, "ListByModelAndTimeRange")
	s.Require().Len(logs, 2)
}

// TestGetProviderWindowStats records two requests after the lower bound and one
// older request. It checks request count and input/output token sums for the
// open-ended provider window.
func (s *UsageLogRepoSuite) TestGetProviderWindowStats() {
	user := mustCreateUser(s.T(), s.client, &identity.User{Email: "windowstats@test.com"})
	apiKey := mustCreateApiKey(s.T(), s.client, &apikey.APIKey{UserID: user.ID, Key: "sk-windowstats", Name: "k"})
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "acc-windowstats"})

	now := time.Now()
	windowStart := now.Add(-10 * time.Minute)

	s.createUsageLog(user, apiKey, provider, 10, 20, 0.5, now.Add(-5*time.Minute))
	s.createUsageLog(user, apiKey, provider, 15, 25, 0.6, now.Add(-3*time.Minute))
	s.createUsageLog(user, apiKey, provider, 20, 30, 0.7, now.Add(-30*time.Minute)) // outside window

	stats, err := s.repo.GetProviderWindowStats(s.ctx, provider.ID, windowStart)
	s.Require().NoError(err, "GetProviderWindowStats")
	s.Require().Equal(int64(2), stats.Requests)
	s.Require().Equal(int64(70), stats.Tokens) // (10+20) + (15+25)
}

// TestGetUserUsageTrendByUserID places actor usage on two UTC fixture dates. It
// checks day-granularity query returns two populated buckets, without asserting
// each bucket's numeric totals.
func (s *UsageLogRepoSuite) TestGetUserUsageTrendByUserID() {
	user := mustCreateUser(s.T(), s.client, &identity.User{Email: "usertrend@test.com"})
	apiKey := mustCreateApiKey(s.T(), s.client, &apikey.APIKey{UserID: user.ID, Key: "sk-usertrend", Name: "k"})
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "acc-usertrend"})

	base := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	s.createUsageLog(user, apiKey, provider, 10, 20, 0.5, base)
	s.createUsageLog(user, apiKey, provider, 15, 25, 0.6, base.Add(1*time.Hour))
	s.createUsageLog(user, apiKey, provider, 20, 30, 0.7, base.Add(24*time.Hour)) // next day

	startTime := base.Add(-1 * time.Hour)
	endTime := base.Add(48 * time.Hour)
	trend, err := s.repo.GetUserUsageTrendByUserID(s.ctx, user.ID, startTime, endTime, "day")
	s.Require().NoError(err, "GetUserUsageTrendByUserID")
	s.Require().Len(trend, 2) // 2 different days
}

// TestGetUserUsageTrendByUserID_HourlyGranularity records requests in three
// distinct fixture hours. It checks hour-granularity actor trends contain three
// populated buckets in the selected interval.
func (s *UsageLogRepoSuite) TestGetUserUsageTrendByUserID_HourlyGranularity() {
	user := mustCreateUser(s.T(), s.client, &identity.User{Email: "usertrendhourly@test.com"})
	apiKey := mustCreateApiKey(s.T(), s.client, &apikey.APIKey{UserID: user.ID, Key: "sk-usertrendhourly", Name: "k"})
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "acc-usertrendhourly"})

	base := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	s.createUsageLog(user, apiKey, provider, 10, 20, 0.5, base)
	s.createUsageLog(user, apiKey, provider, 15, 25, 0.6, base.Add(1*time.Hour))
	s.createUsageLog(user, apiKey, provider, 20, 30, 0.7, base.Add(2*time.Hour))

	startTime := base.Add(-1 * time.Hour)
	endTime := base.Add(3 * time.Hour)
	trend, err := s.repo.GetUserUsageTrendByUserID(s.ctx, user.ID, startTime, endTime, "hour")
	s.Require().NoError(err, "GetUserUsageTrendByUserID hourly")
	s.Require().Len(trend, 3) // 3 different hours
}

// TestGetUserModelStats records two internal model identities with unequal token
// totals. It checks two groups are returned and the larger token model occupies
// the first position.
func (s *UsageLogRepoSuite) TestGetUserModelStats() {
	user := mustCreateUser(s.T(), s.client, &identity.User{Email: "modelstats@test.com"})
	apiKey := mustCreateApiKey(s.T(), s.client, &apikey.APIKey{UserID: user.ID, Key: "sk-modelstats", Name: "k"})
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "acc-modelstats"})

	base := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)

	log1 := &usage.UsageLog{
		UserID:       user.ID,
		APIKeyID:     apiKey.ID,
		ProviderID:   provider.ID,
		Model:        "claude-3-opus",
		InputTokens:  100,
		OutputTokens: 200,
		TotalCost:    0.5,
		ActualCost:   0.5,
		CreatedAt:    base,
	}
	_, err := s.repo.Create(s.ctx, log1)
	s.Require().NoError(err)

	log2 := &usage.UsageLog{
		UserID:       user.ID,
		APIKeyID:     apiKey.ID,
		ProviderID:   provider.ID,
		Model:        "claude-3-sonnet",
		InputTokens:  50,
		OutputTokens: 100,
		TotalCost:    0.2,
		ActualCost:   0.2,
		CreatedAt:    base.Add(1 * time.Hour),
	}
	_, err = s.repo.Create(s.ctx, log2)
	s.Require().NoError(err)

	startTime := base.Add(-1 * time.Hour)
	endTime := base.Add(2 * time.Hour)
	stats, err := s.repo.GetUserModelStats(s.ctx, user.ID, startTime, endTime)
	s.Require().NoError(err, "GetUserModelStats")
	s.Require().Len(stats, 2)

	s.Require().Equal("claude-3-opus", stats[0].Model)
	s.Require().Equal(int64(300), stats[0].TotalTokens)
}

// TestGetUsageTrendWithFilters records usage on two dates under one actor/key
// pair. It checks actor, key, and combined base selectors each return two
// populated daily buckets.
func (s *UsageLogRepoSuite) TestGetUsageTrendWithFilters() {
	user := mustCreateUser(s.T(), s.client, &identity.User{Email: "trendfilters@test.com"})
	apiKey := mustCreateApiKey(s.T(), s.client, &apikey.APIKey{UserID: user.ID, Key: "sk-trendfilters", Name: "k"})
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "acc-trendfilters"})

	base := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	s.createUsageLog(user, apiKey, provider, 10, 20, 0.5, base)
	s.createUsageLog(user, apiKey, provider, 15, 25, 0.6, base.Add(24*time.Hour))

	startTime := base.Add(-1 * time.Hour)
	endTime := base.Add(48 * time.Hour)

	trend, err := s.repo.GetUsageTrendWithFilters(s.ctx, startTime, endTime, "day", user.ID, 0, 0, 0, "", nil, nil, nil)
	s.Require().NoError(err, "GetUsageTrendWithFilters user filter")
	s.Require().Len(trend, 2)

	trend, err = s.repo.GetUsageTrendWithFilters(s.ctx, startTime, endTime, "day", 0, apiKey.ID, 0, 0, "", nil, nil, nil)
	s.Require().NoError(err, "GetUsageTrendWithFilters apiKey filter")
	s.Require().Len(trend, 2)

	trend, err = s.repo.GetUsageTrendWithFilters(s.ctx, startTime, endTime, "day", user.ID, apiKey.ID, 0, 0, "", nil, nil, nil)
	s.Require().NoError(err, "GetUsageTrendWithFilters both filters")
	s.Require().Len(trend, 2)
}

// TestGetUsageTrendWithFilters_HourlyGranularity records two actor requests in
// separate fixture hours. It checks the selected hour-granularity query returns
// two populated trend buckets.
func (s *UsageLogRepoSuite) TestGetUsageTrendWithFilters_HourlyGranularity() {
	user := mustCreateUser(s.T(), s.client, &identity.User{Email: "trendfilters-h@test.com"})
	apiKey := mustCreateApiKey(s.T(), s.client, &apikey.APIKey{UserID: user.ID, Key: "sk-trendfilters-h", Name: "k"})
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "acc-trendfilters-h"})

	base := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	s.createUsageLog(user, apiKey, provider, 10, 20, 0.5, base)
	s.createUsageLog(user, apiKey, provider, 15, 25, 0.6, base.Add(1*time.Hour))

	startTime := base.Add(-1 * time.Hour)
	endTime := base.Add(3 * time.Hour)

	trend, err := s.repo.GetUsageTrendWithFilters(s.ctx, startTime, endTime, "hour", user.ID, 0, 0, 0, "", nil, nil, nil)
	s.Require().NoError(err, "GetUsageTrendWithFilters hourly")
	s.Require().Len(trend, 2)
}

// TestGetModelStatsWithFilters records two models with separate provider, base,
// and user costs. It checks actor/key/provider selectors and distinct monetary
// columns under provider filtering.
func (s *UsageLogRepoSuite) TestGetModelStatsWithFilters() {
	user := mustCreateUser(s.T(), s.client, &identity.User{Email: "modelfilters@test.com"})
	apiKey := mustCreateApiKey(s.T(), s.client, &apikey.APIKey{UserID: user.ID, Key: "sk-modelfilters", Name: "k"})
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "acc-modelfilters"})

	base := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	providerRate1, providerStatsCost1 := 2.0, 0.4
	providerRate2, providerStatsCost2 := 1.5, 0.1

	log1 := &usage.UsageLog{
		UserID:                 user.ID,
		APIKeyID:               apiKey.ID,
		ProviderID:             provider.ID,
		Model:                  "claude-3-opus",
		InputTokens:            100,
		OutputTokens:           200,
		TotalCost:              0.5,
		ActualCost:             25,
		ProviderRateMultiplier: &providerRate1,
		ProviderStatsCost:      &providerStatsCost1,
		CreatedAt:              base,
	}
	_, err := s.repo.Create(s.ctx, log1)
	s.Require().NoError(err)

	log2 := &usage.UsageLog{
		UserID:                 user.ID,
		APIKeyID:               apiKey.ID,
		ProviderID:             provider.ID,
		Model:                  "claude-3-sonnet",
		InputTokens:            50,
		OutputTokens:           100,
		TotalCost:              0.2,
		ActualCost:             10,
		ProviderRateMultiplier: &providerRate2,
		ProviderStatsCost:      &providerStatsCost2,
		CreatedAt:              base.Add(1 * time.Hour),
	}
	_, err = s.repo.Create(s.ctx, log2)
	s.Require().NoError(err)

	startTime := base.Add(-1 * time.Hour)
	endTime := base.Add(2 * time.Hour)

	stats, err := s.repo.GetModelStatsWithFilters(s.ctx, startTime, endTime, user.ID, 0, 0, 0, nil, nil, nil)
	s.Require().NoError(err, "GetModelStatsWithFilters user filter")
	s.Require().Len(stats, 2)

	stats, err = s.repo.GetModelStatsWithFilters(s.ctx, startTime, endTime, 0, apiKey.ID, 0, 0, nil, nil, nil)
	s.Require().NoError(err, "GetModelStatsWithFilters apiKey filter")
	s.Require().Len(stats, 2)

	// Filtering by provider keeps user charges, provider costs, and standard costs
	// distinct.
	stats, err = s.repo.GetModelStatsWithFilters(s.ctx, startTime, endTime, 0, 0, provider.ID, 0, nil, nil, nil)
	s.Require().NoError(err, "GetModelStatsWithFilters provider filter")
	s.Require().Len(stats, 2)
	// Allow numeric-to-float conversion differences across the separately decoded
	// user, provider, and base amounts.
	s.Require().InDelta(25, stats[0].ActualCost, 0.000001)
	s.Require().InDelta(0.8, stats[0].ProviderCost, 0.000001)
	s.Require().InDelta(0.5, stats[0].Cost, 0.000001)
}

// TestGetGeminiUsageTotalsBatchUsesProviderCost supplies one Pro and one Flash
// model with larger user charges. It checks recorded provider-cost
// bases/multipliers and one request in each category.
func (s *UsageLogRepoSuite) TestGetGeminiUsageTotalsBatchUsesProviderCost() {
	user := mustCreateUser(s.T(), s.client, &identity.User{Email: "gemini-cost@test.com"})
	apiKey := mustCreateApiKey(s.T(), s.client, &apikey.APIKey{UserID: user.ID, Key: "sk-gemini-cost", Name: "k"})
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "acc-gemini-cost"})
	base := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	proRate, proStatsCost := 2.0, 0.4
	flashRate, flashStatsCost := 1.5, 0.1

	// User charges exceed provider costs in these rows, making cost-source mistakes
	// observable.
	logs := []*usage.UsageLog{
		{
			UserID:                 user.ID,
			APIKeyID:               apiKey.ID,
			ProviderID:             provider.ID,
			Model:                  "gemini-2.5-pro",
			InputTokens:            100,
			OutputTokens:           200,
			TotalCost:              0.5,
			ActualCost:             25,
			ProviderRateMultiplier: &proRate,
			ProviderStatsCost:      &proStatsCost,
			CreatedAt:              base,
		},
		{
			UserID:                 user.ID,
			APIKeyID:               apiKey.ID,
			ProviderID:             provider.ID,
			Model:                  "gemini-2.5-flash",
			InputTokens:            50,
			OutputTokens:           100,
			TotalCost:              0.2,
			ActualCost:             10,
			ProviderRateMultiplier: &flashRate,
			ProviderStatsCost:      &flashStatsCost,
			CreatedAt:              base.Add(time.Minute),
		},
	}
	for _, log := range logs {
		_, err := s.repo.Create(s.ctx, log)
		s.Require().NoError(err)
	}

	totals, err := s.repo.GetGeminiUsageTotalsBatch(s.ctx, []int64{provider.ID}, base.Add(-time.Hour), base.Add(time.Hour))

	s.Require().NoError(err)
	// Use absolute float tolerance for both multiplied provider-cost readbacks.
	s.Require().InDelta(0.8, totals[provider.ID].ProCost, 0.000001)
	s.Require().InDelta(0.15, totals[provider.ID].FlashCost, 0.000001)
	s.Require().Equal(int64(1), totals[provider.ID].ProRequests)
	s.Require().Equal(int64(1), totals[provider.ID].FlashRequests)
}

// TestGetProviderUsageStats records two provider models on two fixture dates. It
// checks two history rows, combined requests/tokens, and two model groups in the
// report.
func (s *UsageLogRepoSuite) TestGetProviderUsageStats() {
	user := mustCreateUser(s.T(), s.client, &identity.User{Email: "accstats@test.com"})
	apiKey := mustCreateApiKey(s.T(), s.client, &apikey.APIKey{UserID: user.ID, Key: "sk-accstats", Name: "k"})
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "acc-accstats"})

	base := time.Date(2025, 1, 15, 0, 0, 0, 0, time.UTC)

	log1 := &usage.UsageLog{
		UserID:       user.ID,
		APIKeyID:     apiKey.ID,
		ProviderID:   provider.ID,
		Model:        "claude-3-opus",
		InputTokens:  100,
		OutputTokens: 200,
		TotalCost:    0.5,
		ActualCost:   0.4,
		CreatedAt:    base.Add(12 * time.Hour),
	}
	_, err := s.repo.Create(s.ctx, log1)
	s.Require().NoError(err)

	log2 := &usage.UsageLog{
		UserID:       user.ID,
		APIKeyID:     apiKey.ID,
		ProviderID:   provider.ID,
		Model:        "claude-3-sonnet",
		InputTokens:  50,
		OutputTokens: 100,
		TotalCost:    0.2,
		ActualCost:   0.15,
		CreatedAt:    base.Add(36 * time.Hour), // next day
	}
	_, err = s.repo.Create(s.ctx, log2)
	s.Require().NoError(err)

	startTime := base
	endTime := base.Add(72 * time.Hour)

	resp, err := s.repo.GetProviderUsageStats(s.ctx, provider.ID, startTime, endTime)
	s.Require().NoError(err, "GetProviderUsageStats")

	s.Require().Len(resp.History, 2, "expected 2 days of history")
	s.Require().Equal(int64(2), resp.Summary.TotalRequests)
	s.Require().Equal(int64(450), resp.Summary.TotalTokens)
	s.Require().Len(resp.Models, 2)
}

// TestGetProviderUsageStats_EmptyRange queries a provider with no selected usage
// records. It checks history is empty and the report's request total remains
// zero.
func (s *UsageLogRepoSuite) TestGetProviderUsageStats_EmptyRange() {
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "acc-emptystats"})

	base := time.Date(2025, 1, 15, 0, 0, 0, 0, time.UTC)
	startTime := base
	endTime := base.Add(72 * time.Hour)

	resp, err := s.repo.GetProviderUsageStats(s.ctx, provider.ID, startTime, endTime)
	s.Require().NoError(err, "GetProviderUsageStats empty")

	s.Require().Len(resp.History, 0)
	s.Require().Equal(int64(0), resp.Summary.TotalRequests)
}

// TestGetUserUsageTrend records two payers across two fixture dates with
// different token totals. It checks the selected top-user daily trend returns at
// least two rows.
func (s *UsageLogRepoSuite) TestGetUserUsageTrend() {
	user1 := mustCreateUser(s.T(), s.client, &identity.User{Email: "usertrend1@test.com"})
	user2 := mustCreateUser(s.T(), s.client, &identity.User{Email: "usertrend2@test.com"})
	apiKey1 := mustCreateApiKey(s.T(), s.client, &apikey.APIKey{UserID: user1.ID, Key: "sk-usertrend1", Name: "k1"})
	apiKey2 := mustCreateApiKey(s.T(), s.client, &apikey.APIKey{UserID: user2.ID, Key: "sk-usertrend2", Name: "k2"})
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "acc-usertrends"})

	base := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	s.createUsageLog(user1, apiKey1, provider, 100, 200, 1.0, base)
	s.createUsageLog(user2, apiKey2, provider, 50, 100, 0.5, base)
	s.createUsageLog(user1, apiKey1, provider, 100, 200, 1.0, base.Add(24*time.Hour))

	startTime := base.Add(-1 * time.Hour)
	endTime := base.Add(48 * time.Hour)

	trend, err := s.repo.GetUserUsageTrend(s.ctx, startTime, endTime, "day", 10)
	s.Require().NoError(err, "GetUserUsageTrend")
	s.Require().GreaterOrEqual(len(trend), 2)
}

// TestGetAPIKeyUsageTrend records two keys across two fixture dates with
// different token totals. It checks the selected top-key daily trend returns at
// least two rows.
func (s *UsageLogRepoSuite) TestGetAPIKeyUsageTrend() {
	user := mustCreateUser(s.T(), s.client, &identity.User{Email: "keytrend@test.com"})
	apiKey1 := mustCreateApiKey(s.T(), s.client, &apikey.APIKey{UserID: user.ID, Key: "sk-keytrend1", Name: "k1"})
	apiKey2 := mustCreateApiKey(s.T(), s.client, &apikey.APIKey{UserID: user.ID, Key: "sk-keytrend2", Name: "k2"})
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "acc-keytrends"})

	base := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	s.createUsageLog(user, apiKey1, provider, 100, 200, 1.0, base)
	s.createUsageLog(user, apiKey2, provider, 50, 100, 0.5, base)
	s.createUsageLog(user, apiKey1, provider, 100, 200, 1.0, base.Add(24*time.Hour))

	startTime := base.Add(-1 * time.Hour)
	endTime := base.Add(48 * time.Hour)

	trend, err := s.repo.GetAPIKeyUsageTrend(s.ctx, startTime, endTime, "day", 10)
	s.Require().NoError(err, "GetAPIKeyUsageTrend")
	s.Require().GreaterOrEqual(len(trend), 2)
}

// TestGetAPIKeyUsageTrend_HourlyGranularity records two requests under one key
// in separate fixture hours. It checks the selected top-key query returns two
// populated hourly rows.
func (s *UsageLogRepoSuite) TestGetAPIKeyUsageTrend_HourlyGranularity() {
	user := mustCreateUser(s.T(), s.client, &identity.User{Email: "keytrendh@test.com"})
	apiKey := mustCreateApiKey(s.T(), s.client, &apikey.APIKey{UserID: user.ID, Key: "sk-keytrendh", Name: "k"})
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "acc-keytrendh"})

	base := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	s.createUsageLog(user, apiKey, provider, 100, 200, 1.0, base)
	s.createUsageLog(user, apiKey, provider, 50, 100, 0.5, base.Add(1*time.Hour))

	startTime := base.Add(-1 * time.Hour)
	endTime := base.Add(3 * time.Hour)

	trend, err := s.repo.GetAPIKeyUsageTrend(s.ctx, startTime, endTime, "hour", 10)
	s.Require().NoError(err, "GetAPIKeyUsageTrend hourly")
	s.Require().Len(trend, 2)
}

// TestListWithFilters_ApiKeyFilter records one request and selects its key ID.
// It checks the listing contains one row with an exact page total of one.
func (s *UsageLogRepoSuite) TestListWithFilters_ApiKeyFilter() {
	user := mustCreateUser(s.T(), s.client, &identity.User{Email: "filterskey@test.com"})
	apiKey := mustCreateApiKey(s.T(), s.client, &apikey.APIKey{UserID: user.ID, Key: "sk-filterskey", Name: "k"})
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "acc-filterskey"})

	s.createUsageLog(user, apiKey, provider, 10, 20, 0.5, time.Now())

	filters := usage.UsageLogFilters{APIKeyID: apiKey.ID}
	logs, page, err := s.repo.ListWithFilters(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 10}, filters)
	s.Require().NoError(err, "ListWithFilters apiKey")
	s.Require().Len(logs, 1)
	s.Require().Equal(int64(1), page.Total)
}

// TestListWithFilters_TimeRange records two selected rows and one older row
// before the query span. It checks filtered listing length and the observed
// fast-page total both equal two.
func (s *UsageLogRepoSuite) TestListWithFilters_TimeRange() {
	user := mustCreateUser(s.T(), s.client, &identity.User{Email: "filterstime@test.com"})
	apiKey := mustCreateApiKey(s.T(), s.client, &apikey.APIKey{UserID: user.ID, Key: "sk-filterstime", Name: "k"})
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "acc-filterstime"})

	base := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	s.createUsageLog(user, apiKey, provider, 10, 20, 0.5, base)
	s.createUsageLog(user, apiKey, provider, 15, 25, 0.6, base.Add(1*time.Hour))
	s.createUsageLog(user, apiKey, provider, 20, 30, 0.7, base.Add(-24*time.Hour)) // outside range

	startTime := base.Add(-1 * time.Hour)
	endTime := base.Add(2 * time.Hour)
	filters := usage.UsageLogFilters{StartTime: &startTime, EndTime: &endTime}
	logs, page, err := s.repo.ListWithFilters(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 10}, filters)
	s.Require().NoError(err, "ListWithFilters time range")
	s.Require().Len(logs, 2)
	s.Require().Equal(int64(2), page.Total)
}

// TestListWithFilters_CombinedFilters combines actor, key, and bounded
// timestamps over two requests. It checks both matching rows remain in the page
// and its exact total is two.
func (s *UsageLogRepoSuite) TestListWithFilters_CombinedFilters() {
	user := mustCreateUser(s.T(), s.client, &identity.User{Email: "filterscombined@test.com"})
	apiKey := mustCreateApiKey(s.T(), s.client, &apikey.APIKey{UserID: user.ID, Key: "sk-filterscombined", Name: "k"})
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "acc-filterscombined"})

	base := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	s.createUsageLog(user, apiKey, provider, 10, 20, 0.5, base)
	s.createUsageLog(user, apiKey, provider, 15, 25, 0.6, base.Add(1*time.Hour))

	startTime := base.Add(-1 * time.Hour)
	endTime := base.Add(2 * time.Hour)
	filters := usage.UsageLogFilters{
		UserID:    user.ID,
		APIKeyID:  apiKey.ID,
		StartTime: &startTime,
		EndTime:   &endTime,
	}
	logs, page, err := s.repo.ListWithFilters(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 10}, filters)
	s.Require().NoError(err, "ListWithFilters combined")
	s.Require().Len(logs, 2)
	s.Require().Equal(int64(2), page.Total)
}

// TestResponseModelRoundTrip records absent, matching, and differing nullable
// response-model evidence through direct suite writes. It checks both diagnostic
// fields equal their supplied values after readback.
func (s *UsageLogRepoSuite) TestResponseModelRoundTrip() {
	user := mustCreateUser(s.T(), s.client, &identity.User{Email: "response-model@test.local"})
	key := mustCreateApiKey(s.T(), s.client, &apikey.APIKey{UserID: user.ID, Key: "response-model-key", Name: "model"})
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "response-model"})
	for i, known := range []bool{false, true, true} {
		log := &usage.UsageLog{UserID: user.ID, APIKeyID: key.ID, ProviderID: provider.ID, RequestID: fmt.Sprintf("response-model-%d", i), Model: "sent", CreatedAt: time.Now()}
		if known {
			model := "runtime-version"
			mismatch := i == 2
			if !mismatch {
				model = "sent"
			}
			log.UpstreamResponseModel = &model
			log.UpstreamModelMismatch = &mismatch
		}
		_, err := s.repo.Create(s.ctx, log)
		s.Require().NoError(err)
		loaded, err := s.repo.GetByID(s.ctx, log.ID)
		s.Require().NoError(err)
		s.Equal(log.UpstreamResponseModel, loaded.UpstreamResponseModel)
		s.Equal(log.UpstreamModelMismatch, loaded.UpstreamModelMismatch)
	}
}

// truncateToDayUTC returns UTC midnight for the date containing the supplied
// instant.
func truncateToDayUTC(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// testMaxTime returns the later instant, choosing b when both compare equal.
func testMaxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
