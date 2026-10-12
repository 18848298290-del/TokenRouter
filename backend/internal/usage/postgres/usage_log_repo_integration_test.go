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

func TestUsageLogRepoSuite(t *testing.T) {
	suite.Run(t, new(UsageLogRepoSuite))
}

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

	require.Eventually(t, func() bool {
		var count int
		err := integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM usage_logs WHERE request_id = $1 AND api_key_id = $2", requestID, apiKey.ID).Scan(&count)
		return err == nil && count == 1
	}, 3*time.Second, 20*time.Millisecond)
}

func TestUsageLogRepositoryCreateBestEffort_QueueFullBlocksUntilCtxDeadline(t *testing.T) {
	// A full queue waits until the caller's deadline before marking the log dropped (issue #3656).
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
	require.GreaterOrEqual(t, time.Since(start), 150*time.Millisecond)
}

func TestUsageLogRepositoryCreateBestEffort_QueueFullWaitsForDrain(t *testing.T) {
	// The waiting log enters the queue after space becomes available.
	client := testEntClient(t)
	repo := NewUsageLogRepositoryWithSQL(client, integrationDB, timezone.NewCalendar(time.Local))
	repo.bestEffortBatchCh = make(chan usageLogBestEffortRequest, 1)
	repo.bestEffortBatchCh <- usageLogBestEffortRequest{}

	go func() {
		time.Sleep(100 * time.Millisecond)
		<-repo.bestEffortBatchCh // Remove the placeholder to release the blocked enqueue.
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

func TestUsageLogRepositoryCreate_BatchPathQueueFullMarksNotPersisted(t *testing.T) {
	// A full queue waits until the caller's deadline before marking the log not persisted (issue #3656).
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
	require.GreaterOrEqual(t, time.Since(start), 150*time.Millisecond)
}

func TestUsageLogRepositoryCreate_BatchPathCanceledAfterQueueMarksNotPersisted(t *testing.T) {
	client := testEntClient(t)
	repo := NewUsageLogRepositoryWithSQL(client, integrationDB, timezone.NewCalendar(time.Local))
	repo.createBatchCh = make(chan usageLogCreateRequest, 1)

	user := mustCreateUser(t, client, &identity.User{Email: fmt.Sprintf("usage-cancel-queued-%d@example.com", time.Now().UnixNano())})
	apiKey := mustCreateApiKey(t, client, &apikey.APIKey{UserID: user.ID, Key: "sk-usage-cancel-queued-" + uuid.NewString(), Name: "k"})
	provider := mustCreateProvider(t, client, &providercore.Record{Name: "acc-usage-cancel-queued-" + uuid.NewString()})

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)

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

	req := <-repo.createBatchCh
	require.NotNil(t, req.shared)
	cancel()

	err := <-errCh
	require.Error(t, err)
	require.True(t, usage.IsUsageLogCreateNotPersisted(err))
	completeUsageLogCreateRequest(req, usageLogCreateResult{inserted: false, err: usage.MarkUsageLogCreateNotPersisted(context.Canceled)})
}

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
	req.shared.state.Store(usageLogCreateStateCanceled)

	repo.flushCreateBatch(integrationDB, []usageLogCreateRequest{req})

	res := <-req.resultCh
	require.False(t, res.inserted)
	require.Error(t, res.err)
	require.True(t, usage.IsUsageLogCreateNotPersisted(res.err))
}

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

func (s *UsageLogRepoSuite) TestGetByID_NotFound() {
	_, err := s.repo.GetByID(s.ctx, 999999)
	s.Require().Error(err, "expected error for non-existent ID")
}

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
	s.Require().InEpsilon(0.5, *got.ProviderRateMultiplier, 0.0001)
}

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
	// provider_cost falls back to total_cost when provider_stats_cost is NULL
	s.Require().Equal(baseStats.TotalProviderCost+2.3, stats.TotalProviderCost, "TotalProviderCost mismatch")
	s.Require().GreaterOrEqual(stats.TodayRequests, int64(1), "expected TodayRequests >= 1")
	s.Require().GreaterOrEqual(stats.TodayCost, 0.0, "expected TodayCost >= 0")
	s.Require().GreaterOrEqual(stats.TodayProviderCost, 0.0, "expected TodayProviderCost >= 0")

	wantRpm, wantTpm, err := s.repo.getPerformanceStats(s.ctx, 0, false)
	s.Require().NoError(err, "getPerformanceStats")
	s.Require().Equal(wantRpm, stats.Rpm, "Rpm mismatch")
	s.Require().Equal(wantTpm, stats.Tpm, "Tpm mismatch")
}

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
	// provider_cost = COALESCE(provider_stats_cost, total_cost) * COALESCE(provider_rate_multiplier, 1) = total_cost
	s.Require().Equal(1.5, stats.TotalProviderCost)
	s.Require().InEpsilon(150.0, stats.AverageDurationMs, 0.0001)
}

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

// TestGetUserDashboardStatsIncludesOwnedTeamWithoutDuplicates checks owner-visible team usage without double counting.
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

	// Fixed timestamps test all-time totals; today's totals depend on the test date.
	createdAt := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	// Rows cover the owner, a team member, the owner's team key, and a member's personal key.
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
	s.Require().InDelta(0.6, ownerStats.TotalCost, 0.000001)

	memberStats, err := s.repo.GetUserDashboardStats(s.ctx, member.ID)
	s.Require().NoError(err)
	s.Require().Equal(int64(2), memberStats.TotalAPIKeys)
	s.Require().Equal(int64(2), memberStats.TotalRequests)
	s.Require().Equal(int64(60), memberStats.TotalInputTokens)
	s.Require().Equal(int64(6), memberStats.TotalOutputTokens)
}

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
	// provider cost = SUM(total_cost * provider_rate_multiplier)
	s.Require().InEpsilon(1.5, stats.Cost, 0.0001)
	// standard cost = SUM(total_cost)
	s.Require().InEpsilon(1.5, stats.StandardCost, 0.0001)
	// user cost = SUM(actual_cost)
	s.Require().InEpsilon(3.0, stats.UserCost, 0.0001)
}

func (s *UsageLogRepoSuite) TestDashboardAggregationConsistency() {
	now := time.Now().UTC().Truncate(time.Second)
	// Use two completed hours on the same UTC day so both can be aggregated.
	dayStart := truncateToDayUTC(now)
	hour1 := dayStart.Add(2 * time.Hour) // 02:00 UTC
	hour2 := dayStart.Add(3 * time.Hour) // 03:00 UTC
	// Use yesterday if today's second hour has not ended yet.
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

	type hourlyRow struct {
		totalRequests       int64
		inputTokens         int64
		outputTokens        int64
		cacheCreationTokens int64
		cacheReadTokens     int64
		totalCost           float64
		actualCost          float64
		totalDurationMs     int64
		activeUsers         int64
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
		totalRequests       int64
		inputTokens         int64
		outputTokens        int64
		cacheCreationTokens int64
		cacheReadTokens     int64
		totalCost           float64
		actualCost          float64
		totalDurationMs     int64
		activeUsers         int64
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

	// Hourly user totals must match raw records over the same range.
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
	s.Require().InDelta(1.5, analyticsTotalCost, 0.000001)
	s.Require().InDelta(1.4, analyticsActualCost, 0.000001)

	// Recomputing a completed hour replaces its aggregate to include late records.
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
	s.Require().InDelta(1.7, analyticsTotalCost, 0.000001)
	s.Require().InDelta(1.55, analyticsActualCost, 0.000001)
}

// TestUsageAnalyticsQueriesExecuteOnPostgreSQL checks PostgreSQL parameter typing for hourly, batch, and group queries.
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

func (s *UsageLogRepoSuite) TestGetBatchUserUsageStats_Empty() {
	stats, err := s.repo.GetBatchUserUsageStats(s.ctx, []int64{}, time.Time{}, time.Time{})
	s.Require().NoError(err)
	s.Require().Empty(stats)
}

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

func (s *UsageLogRepoSuite) TestGetBatchApiKeyUsageStats_Empty() {
	stats, err := s.repo.GetBatchAPIKeyUsageStats(s.ctx, []int64{}, time.Time{}, time.Time{})
	s.Require().NoError(err)
	s.Require().Empty(stats)
}

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

func (s *UsageLogRepoSuite) TestListByModelAndTimeRange() {
	user := mustCreateUser(s.T(), s.client, &identity.User{Email: "modeltimerange@test.com"})
	apiKey := mustCreateApiKey(s.T(), s.client, &apikey.APIKey{UserID: user.ID, Key: "sk-modeltimerange", Name: "k"})
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "acc-modeltimerange"})

	base := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)

	// Create logs with different models
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

func (s *UsageLogRepoSuite) TestGetUserModelStats() {
	user := mustCreateUser(s.T(), s.client, &identity.User{Email: "modelstats@test.com"})
	apiKey := mustCreateApiKey(s.T(), s.client, &apikey.APIKey{UserID: user.ID, Key: "sk-modelstats", Name: "k"})
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "acc-modelstats"})

	base := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)

	// Create logs with different models
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

	// Models are ordered by descending total tokens.
	s.Require().Equal("claude-3-opus", stats[0].Model)
	s.Require().Equal(int64(300), stats[0].TotalTokens)
}

func (s *UsageLogRepoSuite) TestGetUsageTrendWithFilters() {
	user := mustCreateUser(s.T(), s.client, &identity.User{Email: "trendfilters@test.com"})
	apiKey := mustCreateApiKey(s.T(), s.client, &apikey.APIKey{UserID: user.ID, Key: "sk-trendfilters", Name: "k"})
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "acc-trendfilters"})

	base := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	s.createUsageLog(user, apiKey, provider, 10, 20, 0.5, base)
	s.createUsageLog(user, apiKey, provider, 15, 25, 0.6, base.Add(24*time.Hour))

	startTime := base.Add(-1 * time.Hour)
	endTime := base.Add(48 * time.Hour)

	// Test with user filter
	trend, err := s.repo.GetUsageTrendWithFilters(s.ctx, startTime, endTime, "day", user.ID, 0, 0, 0, "", nil, nil, nil)
	s.Require().NoError(err, "GetUsageTrendWithFilters user filter")
	s.Require().Len(trend, 2)

	// Test with apiKey filter
	trend, err = s.repo.GetUsageTrendWithFilters(s.ctx, startTime, endTime, "day", 0, apiKey.ID, 0, 0, "", nil, nil, nil)
	s.Require().NoError(err, "GetUsageTrendWithFilters apiKey filter")
	s.Require().Len(trend, 2)

	// Test with both filters
	trend, err = s.repo.GetUsageTrendWithFilters(s.ctx, startTime, endTime, "day", user.ID, apiKey.ID, 0, 0, "", nil, nil, nil)
	s.Require().NoError(err, "GetUsageTrendWithFilters both filters")
	s.Require().Len(trend, 2)
}

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

	// Test with user filter
	stats, err := s.repo.GetModelStatsWithFilters(s.ctx, startTime, endTime, user.ID, 0, 0, 0, nil, nil, nil)
	s.Require().NoError(err, "GetModelStatsWithFilters user filter")
	s.Require().Len(stats, 2)

	// Test with apiKey filter
	stats, err = s.repo.GetModelStatsWithFilters(s.ctx, startTime, endTime, 0, apiKey.ID, 0, 0, nil, nil, nil)
	s.Require().NoError(err, "GetModelStatsWithFilters apiKey filter")
	s.Require().Len(stats, 2)

	// Filtering by provider keeps user charges, provider costs, and standard costs distinct.
	stats, err = s.repo.GetModelStatsWithFilters(s.ctx, startTime, endTime, 0, 0, provider.ID, 0, nil, nil, nil)
	s.Require().NoError(err, "GetModelStatsWithFilters provider filter")
	s.Require().Len(stats, 2)
	s.Require().InDelta(25, stats[0].ActualCost, 0.000001)
	s.Require().InDelta(0.8, stats[0].ProviderCost, 0.000001)
	s.Require().InDelta(0.5, stats[0].Cost, 0.000001)
}

func (s *UsageLogRepoSuite) TestGetGeminiUsageTotalsBatchUsesProviderCost() {
	user := mustCreateUser(s.T(), s.client, &identity.User{Email: "gemini-cost@test.com"})
	apiKey := mustCreateApiKey(s.T(), s.client, &apikey.APIKey{UserID: user.ID, Key: "sk-gemini-cost", Name: "k"})
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "acc-gemini-cost"})
	base := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	proRate, proStatsCost := 2.0, 0.4
	flashRate, flashStatsCost := 1.5, 0.1

	// User charges exceed provider costs in these rows, making cost-source mistakes observable.
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
	s.Require().InDelta(0.8, totals[provider.ID].ProCost, 0.000001)
	s.Require().InDelta(0.15, totals[provider.ID].FlashCost, 0.000001)
	s.Require().Equal(int64(1), totals[provider.ID].ProRequests)
	s.Require().Equal(int64(1), totals[provider.ID].FlashRequests)
}

func (s *UsageLogRepoSuite) TestGetProviderUsageStats() {
	user := mustCreateUser(s.T(), s.client, &identity.User{Email: "accstats@test.com"})
	apiKey := mustCreateApiKey(s.T(), s.client, &apikey.APIKey{UserID: user.ID, Key: "sk-accstats", Name: "k"})
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "acc-accstats"})

	base := time.Date(2025, 1, 15, 0, 0, 0, 0, time.UTC)

	// Create logs on different days
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

// TestResponseModelRoundTrip checks known response models alongside historical unknown values.
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

// truncateToDayUTC returns the start of the UTC day.
func truncateToDayUTC(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func testMaxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
