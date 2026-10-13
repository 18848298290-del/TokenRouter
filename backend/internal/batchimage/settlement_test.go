package batchimage_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/batchimage"
	batchimageprovider "github.com/TokenFlux/TokenRouter/internal/batchimage/provider"
	billingcore "github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/config"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// Settlement can resume after capture or refund succeeds but a later job
// transition fails. These in-memory funding and repository fixtures exercise
// stable action identities, submitted pricing, outcome validation, bounded
// retries and usage snapshots, including mixed subscription and wallet
// allocations and preserved output expiry.

// resultUsageRepository records attempted usage writes and supplies the
// configured repository response.
type resultUsageRepository struct {
	usage.UsageLogRepository // unused usage repository methods

	inserted   bool            // configured new usage result
	err        error           // injected usage write failure
	calls      int             // usage insertion invocation count
	lastLog    *usage.UsageLog // last submitted usage pointer
	lastCtxErr error           // last observed context error
}

// TestBatchImageSettlementOutputExpiration completes tasks with configured
// output retention and an existing expiry. It checks the newly scheduled
// deadline while preserving an expiry already stored before the settlement
// visit.
func TestBatchImageSettlementOutputExpiration(t *testing.T) {
	repo := newFakeBatchImageRepository()
	job := testSettlingBatchImageJob("imgbatch_expire")
	repo.jobs[job.BatchID] = job
	billing := &fakeBatchImageBillingRepo{}
	svc := newBatchSettlementFixture(repo,
		billing, nil, &fakeBatchImagePricingResolver{unitPrice: 0.25}, nil, &config.Config{BatchImage: config.BatchImageConfig{OutputRetentionAfterTerminalHours: 5}})

	_, err := svc.Settle(context.Background(), job.BatchID)
	require.NoError(t, err)
	require.NotNil(t, repo.jobs[job.BatchID].OutputExpiresAt)
	require.WithinDuration(t, time.Now().Add(5*time.Hour), *repo.jobs[job.BatchID].OutputExpiresAt, time.Minute)

	existing := time.Now().Add(time.Hour)
	second := testSettlingBatchImageJob("imgbatch_keep_expire")
	second.OutputExpiresAt = &existing
	repo.jobs[second.BatchID] = second
	_, err = svc.Settle(context.Background(), second.BatchID)
	require.NoError(t, err)
	require.Equal(t, existing, *repo.jobs[second.BatchID].OutputExpiresAt)
}

// TestBatchImageSettlementService_SettlesAndChargesSuccessfulImagesOnly
// settles mixed successful and failed item counts with retained model and
// session snapshots. It checks the captured charge, completed metadata and
// usage mapping while recorded commands omit input content.
func TestBatchImageSettlementService_SettlesAndChargesSuccessfulImagesOnly(t *testing.T) {
	repo := newFakeBatchImageRepository()
	job := testSettlingBatchImageJob("imgbatch_settle")
	job.SuccessCount = 3
	job.FailCount = 2
	job.ItemCount = 5
	job.RequestedModel = "batch-image-alias"
	job.InternalModel = "gemini-internal-image"
	job.SessionID = batchimage.BatchImageStringPtr("batch-settlement-session")
	repo.jobs[job.BatchID] = job
	billing := &fakeBatchImageBillingRepo{}
	usageLogs := &resultUsageRepository{}
	svc := newBatchSettlementFixture(repo, billing,
		usageLogs, &fakeBatchImagePricingResolver{unitPrice: 0.25}, nil, nil)

	result, err := svc.Settle(context.Background(), job.BatchID)
	require.NoError(t, err)
	require.Equal(t, 0.75, result.ActualCost)
	require.Equal(t, batchimage.BatchImageCaptureRequestID(job.BatchID), result.RequestID)
	require.False(t, result.AlreadySettled)
	require.Equal(t, batchimage.BatchImageJobStatusCompleted, repo.jobs[job.BatchID].Status)
	require.NotNil(t, repo.jobs[job.BatchID].ActualCost)
	require.Equal(t, 0.75, *repo.jobs[job.BatchID].ActualCost)
	require.NotEmpty(t, batchimage.BatchImageDerefString(repo.jobs[job.BatchID].ManifestHash))
	require.NotNil(t, repo.jobs[job.BatchID].SettledAt)
	require.Equal(t, "batch-settlement-session", batchimage.BatchImageDerefString(usageLogs.lastLog.SessionID))
	require.Equal(t, "gemini-internal-image", usageLogs.lastLog.Model)
	require.Equal(t, "batch-image-alias", usageLogs.lastLog.RequestedModel)
	require.NotNil(t, usageLogs.lastLog.UpstreamModel)
	require.Equal(t, "gemini-image", *usageLogs.lastLog.UpstreamModel)
	require.NotNil(t, usageLogs.lastLog.ModelMappingChain)
	require.Equal(t, "batch-image-alias→gemini-internal-image→gemini-image", *usageLogs.lastLog.ModelMappingChain)
	require.Len(t, billing.captures, 1)
	require.Equal(t, int64(321), billing.captures[0].APIKeyID)
	require.Equal(t, job.UserID, billing.captures[0].UserID)
	require.Equal(t, job.BatchID, billing.captures[0].Task.ID)
	require.Equal(t, 0.75, billing.captures[0].ActualAmount)
	require.Equal(t, 1.25, billing.captures[0].HoldAmount)
	require.NotContains(t, fmt.Sprintf("%+v", billing.captures[0]), batchImageTestData)
	require.NotContains(t, fmt.Sprintf("%+v", billing.captures[0]), "gs://")
	require.NotContains(t, fmt.Sprintf("%+v", billing.captures[0]), "prompt")
}

// TestBatchImageSettlementService_RecordsSubscriptionFirstBilling captures a
// task with recorded subscription and wallet reservations. It checks charged
// source amounts, the usage allocation count and the subscription identity
// chosen for mixed-funding reporting.
func TestBatchImageSettlementService_RecordsSubscriptionFirstBilling(t *testing.T) {
	repo := newFakeBatchImageRepository()
	job := testSettlingBatchImageJob("imgbatch_subscription_settle")
	job.SuccessCount = 3
	job.FailCount = 2
	job.ItemCount = 5
	subscriptionID := int64(77)
	planID := int64(88)
	job.SubscriptionHoldAllocations = []billingcore.BillingAllocation{
		{
			Type:           billingcore.BillingAllocationTypeSubscription,
			AmountUSD:      0.6,
			SubscriptionID: &subscriptionID,
			PlanID:         &planID,
		},
	}
	job.BalanceHoldAmount = 0.65
	repo.jobs[job.BatchID] = job
	usageLogs := &resultUsageRepository{}
	svc := newBatchSettlementFixture(repo, &fakeBatchImageBillingRepo{},
		usageLogs, &fakeBatchImagePricingResolver{unitPrice: 0.25}, nil, nil)

	result, err := svc.Settle(context.Background(), job.BatchID)

	require.NoError(t, err)
	require.InDelta(t, 0.6, result.SubscriptionAmountUSD, 0.000001)
	require.InDelta(t, 0.15, result.BalanceAmountUSD, 0.000001)
	require.NotNil(t, usageLogs.lastLog)
	require.Equal(t, usage.BillingTypeSubscription, usageLogs.lastLog.BillingType)
	require.NotNil(t, usageLogs.lastLog.SubscriptionID)
	require.Equal(t, subscriptionID, *usageLogs.lastLog.SubscriptionID)
	require.InDelta(t, 0.6, usageLogs.lastLog.SubscriptionAmountUSD, 0.000001)
	require.InDelta(t, 0.15, usageLogs.lastLog.BalanceAmountUSD, 0.000001)
	require.Len(t, usageLogs.lastLog.BillingAllocations, 2)
}

// TestBuildBatchImageHoldCommandKeepsTeamBillingSnapshot builds a command
// whose submitting actor differs from its team payer. The command retains
// payer, actor and team identities so funding and member usage can address
// separate users.
func TestBuildBatchImageHoldCommandKeepsTeamBillingSnapshot(t *testing.T) {
	job := testSettlingBatchImageJob("imgbatch_team_snapshot")
	teamID := int64(77)
	job.UserID = 23
	job.BillingUserID = 11
	job.TeamID = &teamID

	cmd, err := batchimage.BuildHoldCommand(job, batchimage.BatchImageCaptureRequestID(job.BatchID), 0.5, "payload-hash")
	require.NoError(t, err)
	require.Equal(t, int64(11), cmd.UserID)
	require.Equal(t, int64(23), cmd.ActorUserID)
	require.NotNil(t, cmd.TeamID)
	require.Equal(t, teamID, *cmd.TeamID)
}

// TestBatchImageBalanceHoldKeepsAPIKeyBillingSnapshot reserves, captures and
// releases a version-three job in preferred-subscription mode. Each recorded
// command keeps the submitted funding mode and selected subscription identity
// across those operations.
func TestBatchImageBalanceHoldKeepsAPIKeyBillingSnapshot(t *testing.T) {
	job := testSettlingBatchImageJob("imgbatch_api_key_billing_snapshot")
	preferredSubscriptionID := int64(89)
	job.BillingMode = apikey.APIKeyBillingModeSubscription
	job.PreferredSubscriptionID = &preferredSubscriptionID
	job.PricingSnapshotVersion = 3
	billing := &fakeBatchImageBillingRepo{}

	require.NoError(t, (batchimage.Funding{Store: billing}).Reserve(context.Background(), job, nil, "reserve-hash"))
	_, err := (batchimage.Funding{Store: billing}).Capture(context.Background(), job, 0.5, "capture-hash")
	require.NoError(t, err)
	require.NoError(t, (batchimage.Funding{Store: billing}).Release(context.Background(), job, "release-hash"))

	commands := []*billingcore.TaskFundsCommand{
		billing.reserves[0],
		billing.captures[0],
		billing.releases[0],
	}
	for _, command := range commands {
		require.Equal(t, apikey.APIKeyBillingModeSubscription, command.APIKeyBillingMode)
		require.NotNil(t, command.PreferredSubscriptionID)
		require.Equal(t, preferredSubscriptionID, *command.PreferredSubscriptionID)
	}
}

// TestBatchImageSettlementService_ZeroSuccessCanComplete settles a task whose
// indexed items all failed. It checks completed state and one zero-cost
// capture command, protecting reservation reconciliation even when no
// successful item is billable.
func TestBatchImageSettlementService_ZeroSuccessCanComplete(t *testing.T) {
	repo := newFakeBatchImageRepository()
	job := testSettlingBatchImageJob("imgbatch_zero")
	job.SuccessCount = 0
	job.FailCount = 4
	job.ItemCount = 4
	repo.jobs[job.BatchID] = job
	billing := &fakeBatchImageBillingRepo{}
	svc := newBatchSettlementFixture(repo, billing, nil, &fakeBatchImagePricingResolver{unitPrice: 0.25}, nil, nil)

	result, err := svc.Settle(context.Background(), job.BatchID)
	require.NoError(t, err)
	require.Equal(t, 0.0, result.ActualCost)
	require.Equal(t, batchimage.BatchImageJobStatusCompleted, repo.jobs[job.BatchID].Status)
	require.Len(t, billing.captures, 1)
	require.Equal(t, 0.0, billing.captures[0].ActualAmount)
}

// TestBatchImageSettlementService_CompletedJobReturnsAlreadySettledWithoutBilling
// starts with a completed record carrying a saved actual cost. Settlement
// reports AlreadySettled and that cost while the funding fixture records no
// capture attempt.
func TestBatchImageSettlementService_CompletedJobReturnsAlreadySettledWithoutBilling(t *testing.T) {
	repo := newFakeBatchImageRepository()
	job := testSettlingBatchImageJob("imgbatch_done")
	job.Status = batchimage.BatchImageJobStatusCompleted
	cost := 0.5
	job.ActualCost = &cost
	repo.jobs[job.BatchID] = job
	billing := &fakeBatchImageBillingRepo{}
	svc := newBatchSettlementFixture(repo, billing, nil, &fakeBatchImagePricingResolver{unitPrice: 0.25}, nil, nil)

	result, err := svc.Settle(context.Background(), job.BatchID)
	require.NoError(t, err)
	require.True(t, result.AlreadySettled)
	require.Equal(t, 0.5, result.ActualCost)
	require.Empty(t, billing.captures)
}

// TestBatchImageSettlementService_IdempotentAfterBillingCrash marks the
// capture identity as already applied while the job still waits for
// settlement. The visit replays capture and completes the record with its
// expected charge.
func TestBatchImageSettlementService_IdempotentAfterBillingCrash(t *testing.T) {
	repo := newFakeBatchImageRepository()
	job := testSettlingBatchImageJob("imgbatch_crash")
	repo.jobs[job.BatchID] = job
	billing := &fakeBatchImageBillingRepo{alreadyApplied: map[string]bool{batchimage.BatchImageCaptureRequestID(job.BatchID): true}}
	svc := newBatchSettlementFixture(repo, billing, nil, &fakeBatchImagePricingResolver{unitPrice: 0.25}, nil, nil)

	result, err := svc.Settle(context.Background(), job.BatchID)
	require.NoError(t, err)
	require.Equal(t, 0.5, result.ActualCost)
	require.Equal(t, batchimage.BatchImageJobStatusCompleted, repo.jobs[job.BatchID].Status)
	require.Len(t, billing.captures, 1)
}

// TestBatchImageSettlementService_ValidationErrors injects invalid stages,
// counts, missing identities, missing prices and conflicting manifests. The
// expected categories return before capture, and the task remains outside
// completed state.
func TestBatchImageSettlementService_ValidationErrors(t *testing.T) {
	tests := []struct {
		name    string                          // scenario display name
		mutate  func(*batchimage.BatchImageJob) // scenario setup mutation
		pricing batchimage.ImagePricer          // injected image pricer
		want    error                           // expected observable result
	}{
		{name: "invalid_status", mutate: func(j *batchimage.BatchImageJob) { j.Status = batchimage.BatchImageJobStatusRunning }, want: batchimage.ErrBatchImageSettlementInvalidStatus},
		{name: "negative_success_count", mutate: func(j *batchimage.BatchImageJob) { j.SuccessCount = -1 }, want: batchimage.ErrBatchImageSettlementInvalidCounts},
		{name: "negative_fail_count", mutate: func(j *batchimage.BatchImageJob) { j.FailCount = -1 }, want: batchimage.ErrBatchImageSettlementInvalidCounts},
		{name: "counts_exceed_item_count", mutate: func(j *batchimage.BatchImageJob) { j.SuccessCount = 2; j.FailCount = 2; j.ItemCount = 3 }, want: batchimage.ErrBatchImageSettlementInvalidCounts},
		{name: "missing_api_key", mutate: func(j *batchimage.BatchImageJob) { j.APIKeyID = nil }, want: batchimage.ErrBatchImageSettlementMissingAPIKeyID},
		{name: "missing_provider", mutate: func(j *batchimage.BatchImageJob) { j.ProviderID = nil }, want: batchimage.ErrBatchImageSettlementMissingProviderID},
		{name: "pricing_missing", pricing: &fakeBatchImagePricingResolver{err: batchimage.ErrBatchImageSettlementPricingMissing}, want: batchimage.ErrBatchImageSettlementPricingMissing},
		{name: "manifest_conflict", mutate: func(j *batchimage.BatchImageJob) { v := "different"; j.ManifestHash = &v }, want: batchimage.ErrBatchImageSettlementManifestConflict},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newFakeBatchImageRepository()
			job := testSettlingBatchImageJob("imgbatch_" + tt.name)
			if tt.mutate != nil {
				tt.mutate(job)
			}
			repo.jobs[job.BatchID] = job
			pricing := tt.pricing
			if pricing == nil {
				pricing = &fakeBatchImagePricingResolver{unitPrice: 0.25}
			}
			billing := &fakeBatchImageBillingRepo{}
			svc := newBatchSettlementFixture(repo, billing, nil, pricing, nil, nil)

			_, err := svc.Settle(context.Background(), job.BatchID)
			require.ErrorIs(t, err, tt.want)
			require.Empty(t, billing.captures)
			require.NotEqual(t, batchimage.BatchImageJobStatusCompleted, repo.jobs[job.BatchID].Status)
		})
	}
}

// TestBatchImageSettlementService_CostExceedingHoldDoesNotCharge prices
// successful items above a legacy job's reservation. It checks the over-hold
// error, retained settling state and saved diagnostic before any funding
// capture can occur.
func TestBatchImageSettlementService_CostExceedingHoldDoesNotCharge(t *testing.T) {
	repo := newFakeBatchImageRepository()
	job := testSettlingBatchImageJob("imgbatch_cost_over_hold")
	job.SuccessCount = 2
	job.FailCount = 0
	job.ItemCount = 2
	holdAmount := 0.5
	job.HoldAmount = &holdAmount
	job.EstimatedCost = holdAmount
	repo.jobs[job.BatchID] = job
	billing := &fakeBatchImageBillingRepo{}
	svc := newBatchSettlementFixture(repo, billing, nil, &fakeBatchImagePricingResolver{unitPrice: 0.50}, nil, nil)

	_, err := svc.Settle(context.Background(), job.BatchID)
	require.ErrorIs(t, err, batchimage.ErrBatchImageSettlementCostExceedsHold)
	require.Empty(t, billing.captures)
	require.Equal(t, batchimage.BatchImageJobStatusSettling, repo.jobs[job.BatchID].Status)
	require.Equal(t, "SETTLEMENT_COST_EXCEEDS_HOLD", batchimage.BatchImageDerefString(repo.jobs[job.BatchID].LastErrorCode))
}

// TestBatchImageSettlementService_UsesSubmittedPricingSnapshot supplies a higher
// live quote beside a stored version-one price snapshot. The charge
// and capture use the submitted unit price and held total from that record.
func TestBatchImageSettlementService_UsesSubmittedPricingSnapshot(t *testing.T) {
	repo := newFakeBatchImageRepository()
	job := testSettlingBatchImageJob("imgbatch_snapshot")
	job.SuccessCount = 2
	job.FailCount = 0
	job.ItemCount = 2
	job.PricingSnapshotVersion = 1
	job.BaseUnitPrice = 0.25
	job.GroupRateMultiplier = 1
	job.ProviderRateMultiplier = 1
	job.BatchDiscountMultiplier = 1
	job.HoldMultiplier = 1.1
	job.BillableUnitPrice = 0.25
	job.HoldUnitPrice = 0.275
	holdAmount := 0.55
	job.HoldAmount = &holdAmount
	job.EstimatedCost = 0.5
	repo.jobs[job.BatchID] = job
	billing := &fakeBatchImageBillingRepo{}
	svc := newBatchSettlementFixture(repo, billing, nil, &fakeBatchImagePricingResolver{unitPrice: 0.50}, nil, nil)

	result, err := svc.Settle(context.Background(), job.BatchID)
	require.NoError(t, err)
	require.InDelta(t, 0.5, result.ActualCost, 1e-12)
	require.Len(t, billing.captures, 1)
	require.InDelta(t, 0.5, billing.captures[0].ActualAmount, 1e-12)
	require.InDelta(t, 0.55, billing.captures[0].HoldAmount, 1e-12)
}

// TestBatchImageSettlementService_BillingFailureLeavesSettlingAndRecordsError
// injects a capture timeout into a settling task. The service retains
// settling state, records its funding diagnostic and returns the
// billing-failed category for another queue visit.
func TestBatchImageSettlementService_BillingFailureLeavesSettlingAndRecordsError(t *testing.T) {
	repo := newFakeBatchImageRepository()
	job := testSettlingBatchImageJob("imgbatch_billing_fail")
	repo.jobs[job.BatchID] = job
	billing := &fakeBatchImageBillingRepo{err: errors.New("temporary billing timeout with gs://hidden-output")}
	svc := newBatchSettlementFixture(repo, billing, nil, &fakeBatchImagePricingResolver{unitPrice: 0.25}, nil, nil)

	_, err := svc.Settle(context.Background(), job.BatchID)
	require.ErrorIs(t, err, batchimage.ErrBatchImageSettlementBillingFailed)
	require.Equal(t, batchimage.BatchImageJobStatusSettling, repo.jobs[job.BatchID].Status)
	require.Equal(t, "SETTLEMENT_BILLING_FAILED", batchimage.BatchImageDerefString(repo.jobs[job.BatchID].LastErrorCode))
	require.Contains(t, batchimage.BatchImageDerefString(repo.jobs[job.BatchID].LastErrorMessage), "temporary billing timeout")
	require.NotNil(t, billing.captures[0])
}

// TestBatchImagePipelineProcessor_SettlesQueuedSettlingJob passes a settling
// record through the combined processor. It checks one capture, completed
// state and a terminal queue result, protecting the handoff from indexing to
// funding.
func TestBatchImagePipelineProcessor_SettlesQueuedSettlingJob(t *testing.T) {
	repo := newFakeBatchImageRepository()
	job := testSettlingBatchImageJob("imgbatch_pipeline")
	repo.jobs[job.BatchID] = job
	billing := &fakeBatchImageBillingRepo{}
	settlement := newBatchSettlementFixture(repo, billing, nil, &fakeBatchImagePricingResolver{unitPrice: 0.25}, nil, nil)
	processor := &batchimage.PipelineProcessor{
		ProviderProcessor: newBatchProcessorFixture(repo, batchimage.NewRegistry[batchimageprovider.BatchImageProvider](&fakeProcessorProvider{}), &fakeBatchImageProviderResolver{provider: &providercore.Record{}}, nil, nil, nil, 0),
		SettlementService: settlement,
	}

	result, err := processor.Process(context.Background(), job.BatchID)
	require.NoError(t, err)
	require.True(t, result.Terminal)
	require.Equal(t, batchimage.BatchImageJobStatusCompleted, repo.jobs[job.BatchID].Status)
	require.Len(t, billing.captures, 1)
}

// TestBatchImagePipelineProcessor_RequeuesTransientSettlementFailure returns
// a temporary capture error through the combined processor. The task stays
// settling, and the processor returns the default settlement revisit delay
// with a nonterminal result.
func TestBatchImagePipelineProcessor_RequeuesTransientSettlementFailure(t *testing.T) {
	repo := newFakeBatchImageRepository()
	job := testSettlingBatchImageJob("imgbatch_pipeline_retry")
	repo.jobs[job.BatchID] = job
	settlement := newBatchSettlementFixture(repo, &fakeBatchImageBillingRepo{err: errors.New("temporary")}, nil, &fakeBatchImagePricingResolver{unitPrice: 0.25}, nil, nil)
	processor := &batchimage.PipelineProcessor{
		ProviderProcessor: newBatchProcessorFixture(repo, batchimage.NewRegistry[batchimageprovider.BatchImageProvider](&fakeProcessorProvider{}), &fakeBatchImageProviderResolver{provider: &providercore.Record{}}, nil, nil, nil, 0),
		SettlementService: settlement,
	}

	result, err := processor.Process(context.Background(), job.BatchID)
	require.NoError(t, err)
	require.False(t, result.Terminal)
	require.Equal(t, batchimage.BatchImageSettlementRetryDelay, result.RequeueAfter)
	require.Equal(t, batchimage.BatchImageJobStatusSettling, repo.jobs[job.BatchID].Status)
}

// TestBatchImagePipelineProcessor_FailsAndReleasesAfterSettlementRetryLimit
// injects the final permitted capture failure before retry exhaustion. The
// task becomes failed, its reservation is released and the combined processor
// returns a terminal queue result.
func TestBatchImagePipelineProcessor_FailsAndReleasesAfterSettlementRetryLimit(t *testing.T) {
	repo := newFakeBatchImageRepository()
	job := testSettlingBatchImageJob("imgbatch_pipeline_retry_exhausted")
	job.RetryCount = batchimage.BatchImageSettlementMaxRetries - 1
	repo.jobs[job.BatchID] = job
	billing := &fakeBatchImageBillingRepo{captureErr: errors.New("temporary billing timeout")}
	settlement := newBatchSettlementFixture(repo, billing, nil, &fakeBatchImagePricingResolver{unitPrice: 0.25}, nil, nil)
	processor := &batchimage.PipelineProcessor{
		ProviderProcessor: newBatchProcessorFixture(repo, batchimage.NewRegistry[batchimageprovider.BatchImageProvider](&fakeProcessorProvider{}), &fakeBatchImageProviderResolver{provider: &providercore.Record{}}, nil, nil, nil, 0),
		SettlementService: settlement,
	}

	result, err := processor.Process(context.Background(), job.BatchID)
	require.NoError(t, err)
	require.True(t, result.Terminal)
	require.Equal(t, batchimage.BatchImageJobStatusFailed, repo.jobs[job.BatchID].Status)
	require.Equal(t, "SETTLEMENT_BILLING_RETRY_EXHAUSTED", batchimage.BatchImageDerefString(repo.jobs[job.BatchID].LastErrorCode))
	require.Len(t, billing.captures, 1)
	require.Len(t, billing.releases, 1)
	require.Equal(t, batchimage.BatchImageReleaseRequestID(job.BatchID), billing.releases[0].RequestID)
}

// TestBatchImageSettlementRetryExhaustedReleaseIsIdempotentAfterTransitionFailure
// fails the status transition after the exhausted task's refund. A later
// visit retries the same release identity, records only one applied funding
// action and then fails the task.
func TestBatchImageSettlementRetryExhaustedReleaseIsIdempotentAfterTransitionFailure(t *testing.T) {
	repo := newFakeBatchImageRepository()
	job := testSettlingBatchImageJob("imgbatch_retry_exhausted_transition_fail")
	job.RetryCount = batchimage.BatchImageSettlementMaxRetries
	job.LastErrorCode = batchimage.BatchImageStringPtr("SETTLEMENT_BILLING_FAILED")
	repo.jobs[job.BatchID] = job
	repo.transitionErr = errors.New("temporary transition failure")
	billing := &fakeBatchImageBillingRepo{}
	svc := newBatchSettlementFixture(repo, billing, nil, &fakeBatchImagePricingResolver{unitPrice: 0.25}, nil, nil)

	_, err := svc.Settle(context.Background(), job.BatchID)
	require.ErrorContains(t, err, "temporary transition failure")
	require.Equal(t, batchimage.BatchImageJobStatusSettling, repo.jobs[job.BatchID].Status)
	require.Len(t, billing.releases, 1)
	require.Len(t, billing.seen, 1)

	repo.transitionErr = nil
	_, err = svc.Settle(context.Background(), job.BatchID)
	require.ErrorIs(t, err, batchimage.ErrBatchImageSettlementBillingFailed)
	require.Equal(t, batchimage.BatchImageJobStatusFailed, repo.jobs[job.BatchID].Status)
	require.Len(t, billing.releases, 2)
	require.Equal(t, billing.releases[0].RequestID, billing.releases[1].RequestID)
	require.Len(t, billing.seen, 1)
}

// TestBatchImageSettlementService_CostExceedsHoldExhaustsAndReleases repeats
// an over-hold validation failure until the limit. It checks no capture, one
// refund and the original RequestHash shared by terminal release paths after
// failure.
func TestBatchImageSettlementService_CostExceedsHoldExhaustsAndReleases(t *testing.T) {
	repo := newFakeBatchImageRepository()
	job := testSettlingBatchImageJob("imgbatch_over_hold_exhausted")
	job.SuccessCount = 2
	job.FailCount = 0
	job.ItemCount = 2
	holdAmount := 0.5
	job.HoldAmount = &holdAmount
	job.EstimatedCost = holdAmount
	requestHash := "request-hash-over-hold"
	job.RequestHash = &requestHash
	repo.jobs[job.BatchID] = job
	billing := &fakeBatchImageBillingRepo{}
	svc := newBatchSettlementFixture(repo, billing, nil, &fakeBatchImagePricingResolver{unitPrice: 0.50}, nil,

		nil)

	// Earlier failures keep this task settling so the same held reservation is
	// available when the persisted retry count eventually reaches its limit.
	for range batchimage.BatchImageSettlementMaxRetries - 1 {
		_, err := svc.Settle(context.Background(), job.BatchID)
		require.ErrorIs(t, err, batchimage.ErrBatchImageSettlementCostExceedsHold)
		require.Equal(t, batchimage.BatchImageJobStatusSettling, repo.jobs[job.BatchID].Status)
	}
	// This visit crosses the persisted retry limit; refund must precede terminal
	// failure so exhausting the budget cannot strand the task reservation.
	_, err := svc.Settle(context.Background(), job.BatchID)
	require.ErrorIs(t, err, batchimage.ErrBatchImageSettlementBillingFailed)
	require.Equal(t, batchimage.BatchImageJobStatusFailed, repo.jobs[job.BatchID].Status)
	require.Empty(t, billing.captures)
	require.Len(t, billing.releases, 1)
	require.Equal(t, batchimage.BatchImageReleaseRequestID(job.BatchID), billing.releases[0].RequestID)
	// Release uses RequestHash across processor, Cancel, and recovery because the
	// shared request ID rejects differing payload fingerprints.
	require.Equal(t, requestHash, billing.releases[0].RequestPayloadHash)
}

// TestBatchImageSettlementService_InvalidCountsExhaustsAndReleases repeats
// inconsistent indexed counts until settlement exhaustion. The final visit
// fails the task and releases funds using the original request fingerprint
// while capture remains unused.
func TestBatchImageSettlementService_InvalidCountsExhaustsAndReleases(t *testing.T) {
	repo := newFakeBatchImageRepository()
	job := testSettlingBatchImageJob("imgbatch_bad_counts_exhausted")
	job.SuccessCount = 2
	job.FailCount = 2
	job.ItemCount = 3
	requestHash := "request-hash-bad-counts"
	job.RequestHash = &requestHash
	repo.jobs[job.BatchID] = job
	billing := &fakeBatchImageBillingRepo{}
	svc := newBatchSettlementFixture(repo, billing, nil, &fakeBatchImagePricingResolver{unitPrice: 0.25}, nil, nil)

	for range batchimage.BatchImageSettlementMaxRetries - 1 {
		_, err := svc.Settle(context.Background(), job.BatchID)
		require.ErrorIs(t, err, batchimage.ErrBatchImageSettlementInvalidCounts)
	}
	_, err := svc.Settle(context.Background(), job.BatchID)
	require.ErrorIs(t, err, batchimage.ErrBatchImageSettlementBillingFailed)
	require.Equal(t, batchimage.BatchImageJobStatusFailed, repo.jobs[job.BatchID].Status)
	require.Empty(t, billing.captures)
	require.Len(t, billing.releases, 1)
	require.Equal(t, requestHash, billing.releases[0].RequestPayloadHash)
}

// TestReleaseBatchImageBalanceHold_TreatsFingerprintConflictAsReleased
// injects the historical release fingerprint-conflict category from funding.
// The wrapper records a release attempt and returns successfully, protecting
// the compatibility handling for that existing action identity.
func TestReleaseBatchImageBalanceHold_TreatsFingerprintConflictAsReleased(t *testing.T) {
	job := testSettlingBatchImageJob("imgbatch_release_conflict")
	// Inject the conflict category directly. This fixture records the attempted
	// release but does not simulate a prior committed funding transaction.
	billing := &fakeBatchImageBillingRepo{releaseErr: billingcore.ErrUsageBillingRequestConflict}
	err := (batchimage.Funding{Store: billing}).Release(context.Background(), job, "request-hash")
	require.NoError(t, err)
	require.Len(t, billing.releases, 1)
}

// TestBatchImageSettlementManifestHash changes job timestamps and then
// successful counts before recomputing the manifest. It checks timestamp
// independence and count sensitivity, and confirms the returned digest
// remains distinct from sample input content.
func TestBatchImageSettlementManifestHash(t *testing.T) {
	job := testSettlingBatchImageJob("imgbatch_hash")
	first := batchimage.BuildBatchImageSettlementManifestHash(job)
	job.CreatedAt = job.CreatedAt.AddDate(0, 0, 1)
	job.UpdatedAt = job.UpdatedAt.AddDate(0, 0, 1)
	require.Equal(t, first, batchimage.BuildBatchImageSettlementManifestHash(job))

	job.SuccessCount++
	require.NotEqual(t, first, batchimage.BuildBatchImageSettlementManifestHash(job))

	job.SuccessCount--
	promptOrBase64 := first + " prompt " + batchImageTestData
	require.NotContains(t, batchimage.BuildBatchImageSettlementManifestHash(job), promptOrBase64)
}

// TestBatchImageSettlementBillingRequestIDs settles one task twice and
// another task once. It checks two distinct batch-derived capture identities
// and two deduplication entries, protecting completed replay and separation
// between jobs.
func TestBatchImageSettlementBillingRequestIDs(t *testing.T) {
	repo := newFakeBatchImageRepository()
	first := testSettlingBatchImageJob("imgbatch_unique_1")
	second := testSettlingBatchImageJob("imgbatch_unique_2")
	repo.jobs[first.BatchID] = first
	repo.jobs[second.BatchID] = second
	billing := &fakeBatchImageBillingRepo{}
	svc := newBatchSettlementFixture(repo, billing, nil, &fakeBatchImagePricingResolver{unitPrice: 0.25}, nil, nil)

	_, err := svc.Settle(context.Background(), first.BatchID)
	require.NoError(t, err)
	_, err = svc.Settle(context.Background(), first.BatchID)
	require.NoError(t, err)
	_, err = svc.Settle(context.Background(), second.BatchID)
	require.NoError(t, err)

	require.Len(t, billing.captures, 2)
	require.Equal(t, batchimage.BatchImageCaptureRequestID(first.BatchID), billing.captures[0].RequestID)
	require.Equal(t, batchimage.BatchImageCaptureRequestID(second.BatchID), billing.captures[1].RequestID)
	require.NotEqual(t, billing.captures[0].RequestID, billing.captures[1].RequestID)
	require.Len(t, billing.seen, 2)
}

// testSettlingBatchImageJob builds a legacy settling task with fixed owner,
// provider references, item counts and a funded estimate. Tests mutate its
// snapshots directly to exercise validation, pricing or recovery outcomes.
func testSettlingBatchImageJob(batchID string) *batchimage.BatchImageJob {
	apiKeyID := int64(321)
	providerID := int64(654)
	providerJobName := "providers/job"
	outputRef := "files/output"
	holdAmount := 1.25
	holdID := batchimage.BatchImageHoldRequestID(batchID)
	return &batchimage.BatchImageJob{
		BatchID:           batchID,
		UserID:            123,
		APIKeyID:          &apiKeyID,
		ProviderID:        &providerID,
		Platform:          batchimage.BatchImageProviderGeminiAPI,
		Model:             "gemini-image",
		Status:            batchimage.BatchImageJobStatusSettling,
		ProviderJobName:   &providerJobName,
		ProviderOutputRef: &outputRef,
		ItemCount:         3,
		SuccessCount:      2,
		FailCount:         1,
		EstimatedCost:     holdAmount,
		HoldAmount:        &holdAmount,
		HoldID:            &holdID,
	}
}

// Create records the usage pointer, invocation count and current context
// error before returning configured insertion and error values. The fixture
// retains the supplied record for assertions after the recorder callback
// runs.
func (s *resultUsageRepository) Create(ctx context.Context, log *usage.UsageLog) (bool, error) {
	s.calls++
	s.lastLog = log
	s.lastCtxErr = ctx.Err()
	return s.inserted, s.err
}
