//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/usage"
	"github.com/TokenFlux/TokenRouter/migrations"
)

// TestUsageRequestIDConditionUsesIndexes checks results and plans for sparse matches and external ID collisions.
func TestUsageRequestIDConditionUsesIndexes(t *testing.T) {
	ctx := t.Context()
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	// Temporary tables retain production indexes and isolate lookup candidates from other test records.
	_, err = tx.ExecContext(ctx, `
CREATE TEMP TABLE usage_logs (LIKE public.usage_logs INCLUDING ALL) ON COMMIT DROP;
CREATE TEMP TABLE request_records (LIKE public.request_records INCLUDING ALL) ON COMMIT DROP;
CREATE TEMP TABLE ops_error_logs (LIKE public.ops_error_logs INCLUDING ALL) ON COMMIT DROP;
CREATE TEMP TABLE ops_system_logs (LIKE public.ops_system_logs INCLUDING ALL) ON COMMIT DROP;
INSERT INTO usage_logs(user_id,billing_user_id,api_key_id,provider_id,request_id,upstream_request_id,model)
SELECT 1,1,1,1,'request-'||g,'upstream-'||g,'model' FROM generate_series(1,20000) g;
UPDATE usage_logs SET upstream_request_id='request-1' WHERE request_id='request-2';
INSERT INTO usage_logs(user_id,billing_user_id,api_key_id,provider_id,request_id,upstream_request_id,model)
VALUES(1,1,1,1,NULL,'legacy-upstream','model');
INSERT INTO request_records(request_id,started_at,updated_at,revision,record)
VALUES('request-1',now(),now(),1,'{"request_id":"request-1","state":"completed"}');
ANALYZE usage_logs;
ANALYZE request_records;
ANALYZE ops_error_logs;
ANALYZE ops_system_logs;`)
	require.NoError(t, err)
	for _, test := range []struct {
		search string
		want   string
	}{
		{"request-1", "request-1"},
		{"upstream-3", "request-3"},
		{"legacy-upstream", "legacy"},
		{"missing", ""},
	} {
		t.Run(test.search, func(t *testing.T) {
			query := "SELECT COALESCE(ul.request_id,'legacy') FROM usage_logs ul WHERE ul.api_key_id=$1 AND " + usageRequestIDCondition("ul.", 2)
			rows, err := tx.QueryContext(ctx, query, 1, test.search)
			require.NoError(t, err)
			var got []string
			for rows.Next() {
				var id string
				require.NoError(t, rows.Scan(&id))
				got = append(got, id)
			}
			require.NoError(t, rows.Err())
			require.NoError(t, rows.Close())
			if test.want == "" {
				require.Empty(t, got)
			} else {
				require.Equal(t, []string{test.want}, got)
			}
			var raw []byte
			require.NoError(t, tx.QueryRowContext(ctx, "EXPLAIN (ANALYZE, FORMAT JSON) "+query, 1, test.search).Scan(&raw))
			type planNode struct {
				Type     string     `json:"Node Type"`
				Relation string     `json:"Relation Name"`
				Plans    []planNode `json:"Plans"`
			}
			var plans []struct {
				Plan planNode `json:"Plan"`
			}
			require.NoError(t, json.Unmarshal(raw, &plans))
			seen := false
			var inspect func(planNode)
			inspect = func(node planNode) {
				if node.Relation == "usage_logs" {
					seen = true
					require.NotEqual(t, "Seq Scan", node.Type, "%s", raw)
				}
				for _, child := range node.Plans {
					inspect(child)
				}
			}
			inspect(plans[0].Plan)
			require.True(t, seen)
		})
	}
}

// TestResponseModelMigrationPreservesHistory recreates the old columns in a transaction and checks historical NULL values.
func (s *UsageLogRepoSuite) TestResponseModelMigrationPreservesHistory() {
	user := mustCreateUser(s.T(), s.client, &identity.User{Email: "response-history@test.local"})
	key := mustCreateApiKey(s.T(), s.client, &apikey.APIKey{UserID: user.ID, Key: "response-history", Name: "history"})
	upstream := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "response-history"})
	log := s.createUsageLog(user, key, upstream, 1, 1, 0, time.Now())
	_, err := s.tx.ExecContext(s.ctx, "ALTER TABLE usage_logs DROP COLUMN upstream_response_model, DROP COLUMN upstream_model_mismatch")
	s.Require().NoError(err)
	migration, err := migrations.FS.ReadFile("285_usage_upstream_response_model.sql")
	s.Require().NoError(err)
	for range 2 {
		_, err = s.tx.ExecContext(s.ctx, string(migration))
		s.Require().NoError(err)
	}
	loaded, err := s.repo.GetByID(s.ctx, log.ID)
	s.Require().NoError(err)
	s.Nil(loaded.UpstreamResponseModel)
	s.Nil(loaded.UpstreamModelMismatch)
}

func TestUsageLog_ListWithFilters_ResolvesSoftDeletedUser(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	repo := NewUsageLogRepositoryWithSQL(client, tx, timezone.NewCalendar(time.Local))

	// Create one log for an active user and one for a user to be soft-deleted.
	active := mustCreateUser(t, client, &identity.User{Email: "active-listfilter@test.com"})
	deleted := mustCreateUser(t, client, &identity.User{Email: "deleted-listfilter@test.com"})
	apiKey := mustCreateApiKey(t, client, &apikey.APIKey{UserID: deleted.ID, Key: "sk-del-1", Name: "k"})
	apiKey2 := mustCreateApiKey(t, client, &apikey.APIKey{UserID: active.ID, Key: "sk-act-1", Name: "k"})
	provider := mustCreateProvider(t, client, &providercore.Record{Name: "acc-listfilter"})

	now := time.Now().UTC()
	for _, u := range []struct {
		uid int64
		kid int64
	}{{deleted.ID, apiKey.ID}, {active.ID, apiKey2.ID}} {
		_, err := repo.Create(ctx, &usage.UsageLog{
			UserID: u.uid, APIKeyID: u.kid, ProviderID: provider.ID,
			Model: "claude-3", InputTokens: 1, OutputTokens: 1,
			TotalCost: 0.1, ActualCost: 0.1, CreatedAt: now,
		})
		require.NoError(t, err)
	}

	// The SoftDeleteMixin hook turns deletion into an update of deleted_at.
	require.NoError(t, client.User.DeleteOneID(deleted.ID).Exec(ctx))

	logs, _, err := repo.ListWithFilters(ctx, pagination.PaginationParams{Page: 1, PageSize: 50},
		usage.UsageLogFilters{ExactTotal: true})
	require.NoError(t, err)

	byUser := map[int64]usage.UsageLog{}
	for _, l := range logs {
		byUser[l.UserID] = l
	}

	// Historical log hydration includes the deleted user's email and deletion time.
	delLog, ok := byUser[deleted.ID]
	require.True(t, ok, "deleted user's usage log must still be listed")
	require.NotNil(t, delLog.User, "deleted user identity must resolve")
	require.Equal(t, "deleted-listfilter@test.com", delLog.User.Email)
	require.NotNil(t, delLog.User.DeletedAt, "DeletedAt must be set for soft-deleted user")

	// Active users have a nil DeletedAt.
	actLog := byUser[active.ID]
	require.NotNil(t, actLog.User)
	require.Nil(t, actLog.User.DeletedAt)
}

func (s *UsageLogRepoSuite) TestListWithFilters_SortByModelAsc() {
	user := mustCreateUser(s.T(), s.client, &identity.User{Email: "usage-sort@example.com"})
	apiKey := mustCreateApiKey(s.T(), s.client, &apikey.APIKey{UserID: user.ID, Key: "sk-usage-sort", Name: "k"})
	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "usage-sort-provider"})

	first := &usage.UsageLog{
		UserID:         user.ID,
		APIKeyID:       apiKey.ID,
		ProviderID:     provider.ID,
		RequestID:      uuid.New().String(),
		Model:          "z-model",
		RequestedModel: "z-model",
		InputTokens:    10,
		OutputTokens:   20,
		TotalCost:      0.5,
		ActualCost:     0.5,
		CreatedAt:      time.Now(),
	}
	_, err := s.repo.Create(s.ctx, first)
	s.Require().NoError(err)

	second := &usage.UsageLog{
		UserID:         user.ID,
		APIKeyID:       apiKey.ID,
		ProviderID:     provider.ID,
		RequestID:      uuid.New().String(),
		Model:          "a-model",
		RequestedModel: "a-model",
		InputTokens:    10,
		OutputTokens:   20,
		TotalCost:      0.5,
		ActualCost:     0.5,
		CreatedAt:      time.Now().Add(time.Second),
	}
	_, err = s.repo.Create(s.ctx, second)
	s.Require().NoError(err)

	logs, _, err := s.repo.ListWithFilters(s.ctx, pagination.PaginationParams{
		Page:      1,
		PageSize:  10,
		SortBy:    "model",
		SortOrder: "asc",
	}, usage.UsageLogFilters{UserID: user.ID})
	s.Require().NoError(err)
	s.Require().Len(logs, 2)
	s.Require().Equal("a-model", logs[0].RequestedModel)
	s.Require().Equal("z-model", logs[1].RequestedModel)
}
