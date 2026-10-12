//go:build integration

package billing_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	billingpg "github.com/TokenFlux/TokenRouter/internal/billing/postgres"
	"github.com/TokenFlux/TokenRouter/internal/creative"
	creativepg "github.com/TokenFlux/TokenRouter/internal/creative/postgres"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

type failingProjection struct{ billingpg.TaskProjection }

// TestProviderOutcomeRollbackAndDeliveryLost checks atomic outcome and outbox writes using output metadata.
func TestProviderOutcomeRollbackAndDeliveryLost(t *testing.T) {
	ctx := context.Background()
	client := committedEntitlementClient(t)
	user := mustCreateUser(t, client, &identity.User{Email: "test-" + uuid.NewString() + "@example.com", Balance: 10})
	group := mustCreateGroup(t, client, &routing.Group{Name: "test-outcome-" + uuid.NewString()})
	key := mustCreateApiKey(t, client, &apikey.APIKey{UserID: user.ID, Key: "sk-test-" + uuid.NewString(), Name: "test"})
	repo := creativepg.NewCreativeRunRepository(client)
	id := "crun_" + uuid.NewString()
	_, err := repo.CreateCreativeRun(ctx, creative.CreateCreativeRunParams{RunID: id, UserID: user.ID, GroupID: group.ID, APIKeyID: key.ID, Model: "image", RequestedModel: "image", Operation: creative.CreativeOperationGenerate, RequestedOutputCount: 1, ImageSize: "1K", ResponseMIMEType: "image/png", PromptHash: "hash", RequestFingerprint: "fingerprint"})
	require.NoError(t, err)
	require.NoError(t, repo.MarkCreativeRunRunning(ctx, id, 0, time.Now()))
	outcomes, ok := repo.(creative.ProviderOutcomeStore)
	require.True(t, ok)
	_, err = integrationDB.ExecContext(ctx, `CREATE FUNCTION test_reject_outcome() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test outcome audit failure'; END $$;
 CREATE TRIGGER test_reject_outcome BEFORE INSERT ON creative_run_outbox FOR EACH ROW EXECUTE FUNCTION test_reject_outcome();`)
	require.NoError(t, err)
	cleanup := func() {
		_, err := integrationDB.ExecContext(context.Background(), `DROP TRIGGER IF EXISTS test_reject_outcome ON creative_run_outbox; DROP FUNCTION IF EXISTS test_reject_outcome();`)
		require.NoError(t, err)
	}
	t.Cleanup(cleanup)
	now := time.Now()
	expires := now.Add(time.Minute)
	mime := "image/png"
	size := int64(3)
	code := creative.OutputDeliveryPending
	metadata := []creative.CreativeRunOutput{{OutputIndex: 0, Status: creative.CreativeRunOutputStatusSucceeded, MimeType: &mime, ByteSize: &size, TransientExpiresAt: &expires, ErrorCode: &code}}
	err = outcomes.RecordProviderOutcome(ctx, id, 0, metadata, now)
	require.ErrorContains(t, err, "test outcome audit failure")
	run, err := repo.GetCreativeRunByRunID(ctx, id)
	require.NoError(t, err)
	require.Nil(t, run.ProviderResultRecordedAt)
	require.Equal(t, creative.CreativeRunStatusRunning, run.Status)
	output, err := repo.GetCreativeRunOutput(ctx, id, 0)
	require.NoError(t, err)
	require.Equal(t, creative.CreativeRunOutputStatusPending, output.Status)
	cleanup()
	require.NoError(t, outcomes.RecordProviderOutcome(ctx, id, 0, metadata, now))
	require.NoError(t, outcomes.RecordProviderOutcome(ctx, id, 0, metadata, now))
	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM creative_run_outbox WHERE run_id=$1 AND operation='settle'`, id).Scan(&count))
	require.Equal(t, 1, count)
	require.NoError(t, outcomes.CompleteProviderOutcome(ctx, id, 0.2, true, time.Now()))
	run, err = repo.GetCreativeRunByRunID(ctx, id)
	require.NoError(t, err)
	require.Equal(t, creative.CreativeRunStatusResultLost, run.Status)
	require.NotNil(t, run.ActualCost)
	require.InDelta(t, 0.2, *run.ActualCost, 1e-10)
}

func (p failingProjection) SaveReservation(ctx context.Context, balance float64, alloc []billing.BillingAllocation, hold, estimated float64) error {
	if err := p.TaskProjection.SaveReservation(ctx, balance, alloc, hold, estimated); err != nil {
		return err
	}
	return errors.New("test projection failure")
}

// TestTaskFundingProjectionRollback checks that a failed task write rolls back balances, the task snapshot, and the deduplication claim.
func TestTaskFundingProjectionRollback(t *testing.T) {
	ctx := context.Background()
	client := committedEntitlementClient(t)
	user := mustCreateUser(t, client, &identity.User{Email: "test-funds-" + uuid.NewString() + "@example.com", Balance: 10})
	group := mustCreateGroup(t, client, &routing.Group{Name: "test-funds-" + uuid.NewString()})
	key := mustCreateApiKey(t, client, &apikey.APIKey{UserID: user.ID, Key: "sk-test-funds-" + uuid.NewString(), Name: "test"})
	repo := creativepg.NewCreativeRunRepository(client)
	id := "crun_" + uuid.NewString()
	_, err := repo.CreateCreativeRun(ctx, creative.CreateCreativeRunParams{RunID: id, UserID: user.ID, GroupID: group.ID, APIKeyID: key.ID, Model: "image", RequestedModel: "image", Operation: creative.CreativeOperationGenerate, RequestedOutputCount: 1, ImageSize: "1K", ResponseMIMEType: "image/png", PromptHash: "hash", RequestFingerprint: "fingerprint", EstimatedCost: 0.2})
	require.NoError(t, err)
	store := billingpg.NewSettlementStore(integrationDB, timezone.NewCalendar(time.Local),

		nil, billingpg.TaskProjectionFactories{creative.FundingScope: func(tx *sql.Tx, ref billing.TaskReference) billingpg.TaskProjection {
			return failingProjection{creativepg.NewFundingParticipant(tx, ref.ID)}
		}})
	cmd := &billing.TaskFundsCommand{Task: creative.FundingReference(id), UserID: user.ID, APIKeyID: key.ID, RequestID: "creative_hold:" + id, HoldAmount: 0.2}
	_, err = store.Reserve(ctx, cmd)
	require.ErrorContains(t, err, "test projection failure")
	var balance, frozen float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance,COALESCE(frozen_balance,0) FROM users WHERE id=$1`, user.ID).Scan(&balance, &frozen))
	require.Equal(t, 10.0, balance)
	require.Zero(t, frozen)
	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_billing_dedup WHERE request_id=$1`, cmd.RequestID).Scan(&count))
	require.Zero(t, count)
	good := billingpg.NewSettlementStore(integrationDB, timezone.NewCalendar(time.Local),

		nil, billingpg.TaskProjectionFactories{creative.FundingScope: func(tx *sql.Tx, ref billing.TaskReference) billingpg.TaskProjection {
			return creativepg.NewFundingParticipant(tx, ref.ID)
		}})
	funds := billing.NewFunds(good)
	result, err := funds.Reserve(ctx, cmd)
	require.NoError(t, err)
	require.True(t, result.Applied, fmt.Sprint(result))
}

// TestNativeCreativeFundingReplay checks that replaying capture for the same task charges it once.
func TestNativeCreativeFundingReplay(t *testing.T) {
	ctx := context.Background()
	client := committedEntitlementClient(t)
	user := mustCreateUser(t, client, &identity.User{Email: "test-native-" + uuid.NewString() + "@example.com", Balance: 10})
	group := mustCreateGroup(t, client, &routing.Group{Name: "test-native-" + uuid.NewString()})
	key := mustCreateApiKey(t, client, &apikey.APIKey{UserID: user.ID, Key: "sk-test-native-" + uuid.NewString(), Name: "native"})
	repo := creativepg.NewCreativeRunRepository(client)
	run, err := repo.CreateCreativeRun(ctx, creative.CreateCreativeRunParams{RunID: "crun_" + uuid.NewString(), UserID: user.ID, GroupID: group.ID, APIKeyID: key.ID, Model: "image", RequestedModel: "image", Operation: creative.CreativeOperationGenerate, RequestedOutputCount: 1, ImageSize: "1K", ResponseMIMEType: "image/png", PromptHash: "hash", RequestFingerprint: "fingerprint", EstimatedCost: 0.2, HoldAmount: 0.2, BaseUnitPrice: 0.2, SubscriptionRateMultiplier: 1, BalanceRateMultiplier: 1, PlanGroupRateEnabled: true})
	require.NoError(t, err)
	store := billingpg.NewSettlementStore(integrationDB, timezone.NewCalendar(time.Local),

		nil, billingpg.TaskProjectionFactories{creative.FundingScope: func(tx *sql.Tx, ref billing.TaskReference) billingpg.TaskProjection {
			return creativepg.NewFundingParticipant(tx, ref.ID)
		}})
	funding := creative.Funding{Store: billing.NewFunds(store)}
	require.NoError(t, funding.Reserve(ctx, run))
	first, err := funding.Capture(ctx, run, 1)
	require.NoError(t, err)
	require.True(t, first.Applied)
	replay, err := funding.Capture(ctx, run, 1)
	require.NoError(t, err)
	require.False(t, replay.Applied)
	var balance, frozen float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance,COALESCE(frozen_balance,0) FROM users WHERE id=$1`, user.ID).Scan(&balance, &frozen))
	require.InDelta(t, 9.8, balance, 1e-10)
	require.Zero(t, frozen)
	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_billing_dedup WHERE request_id=$1`, creative.CreativeCaptureRequestID(run.RunID)).Scan(&count))
	require.Equal(t, 1, count)
}
