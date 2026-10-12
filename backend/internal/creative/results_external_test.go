package creative_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/creative"
	testassert "github.com/TokenFlux/TokenRouter/internal/testutil/assertion"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// creativeFakeUsageLogRepo collects usage logs passed to Create.
type creativeFakeUsageLogRepo struct {
	usage.UsageLogRepository
	logs []*usage.UsageLog
}

func TestCreativeSucceedRunIdempotentSettlement(t *testing.T) {
	svc := newCreativeTestService()
	ctx := context.Background()
	usageRepo := &creativeFakeUsageLogRepo{}
	bindCreativeUsageFixture(svc.Results, usageRepo)
	repo := testassert.MustType[*creativeFakeRunRepo](svc.Repo)
	billing := testassert.MustType[*creativeFakeBillingRepo](creativeFixtureBilling(svc))

	runID := "crun_settleidempotent1"
	providerID := int64(55)
	repo.runs[runID] = &creative.CreativeRun{
		RunID:                runID,
		UserID:               7,
		WorkspaceID:          creativeStringValuePtr(testCreativeWorkspaceID),
		GroupID:              12,
		APIKeyID:             900,
		ProviderID:           &providerID,
		Model:                "gemini-3.1-flash-image",
		Operation:            creative.CreativeOperationGenerate,
		RequestedOutputCount: 1,
		Status:               creative.CreativeRunStatusRunning,
		EstimatedCost:        0.02,
		BaseUnitPrice:        0.02,
	}
	repo.outputs[runID] = []*creative.CreativeRunOutput{
		{RunID: runID, OutputIndex: 0, Status: creative.CreativeRunOutputStatusPending},
	}
	results := []creative.ProviderOutput{{Index: 0, Success: true, Bytes: []byte("img"), Mime: "image/png"}}

	first, err := svc.Results.SucceedRun(ctx, runID, providerID, results)
	require.NoError(t, err)
	require.Equal(t, creative.CreativeRunStatusSucceeded, first.Status)

	second, err := svc.Results.SucceedRun(ctx, runID, providerID, results)
	require.NoError(t, err)
	require.Equal(t, creative.CreativeRunStatusSucceeded, second.Status)

	// Repeated completion captures funds and logs usage once with stable billing identifiers.
	require.Equal(t, 1, billing.captureN)
	require.Equal(t, []string{"creative_capture:" + runID}, billing.captureIDs)
	require.Len(t, usageRepo.logs, 1)
	require.Len(t, usageRepo.logs[0].RequestID, 36)
	require.Equal(t, "creative_settle:"+runID, usageRepo.logs[0].BillingKey)
	require.Equal(t, "image", stringValue(usageRepo.logs[0].BillingMode))
	require.Equal(t, 1, usageRepo.logs[0].ImageCount)
	// Repeated settlement keeps a single succeeded output row.
	outputs := repo.outputs[runID]
	require.Len(t, outputs, 1)
	require.Equal(t, creative.CreativeRunOutputStatusSucceeded, outputs[0].Status)
}

// TestCreativeSucceedRunRequiresTransientOutput checks rejection of empty output and billable loss after
// save failures.
func TestCreativeSucceedRunRequiresTransientOutput(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*creative.Public)
		bytes     []byte
	}{
		{
			name: "transient store missing",
			configure: func(svc *creative.Public) {
				bindCreativeTransientFixture(svc, nil)
			},
		},
		{
			name: "save output failed",
			configure: func(svc *creative.Public) {
				testassert.MustType[*creativeFakeTransient](svc.TransientStore).saveOutputErr = errors.New("redis unavailable")
			},
		},
		{
			name:  "empty output",
			bytes: []byte{},
			configure: func(svc *creative.Public) {
				// The default transient fake verifies that empty bytes are rejected before a write.
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			svc := newCreativeTestService()
			repo := testassert.MustType[*creativeFakeRunRepo](svc.Repo)
			providerID := int64(55)
			runID := "crun_transientrequired"
			repo.runs[runID] = &creative.CreativeRun{
				RunID:                runID,
				UserID:               7,
				WorkspaceID:          creativeStringValuePtr(testCreativeWorkspaceID),
				GroupID:              12,
				APIKeyID:             900,
				ProviderID:           &providerID,
				Model:                "gemini-3.1-flash-image",
				Operation:            creative.CreativeOperationGenerate,
				RequestedOutputCount: 1,
				Status:               creative.CreativeRunStatusRunning,
				EstimatedCost:        0.02,
				BaseUnitPrice:        0.02,
			}
			repo.outputs[runID] = []*creative.CreativeRunOutput{{
				RunID: runID, OutputIndex: 0, Status: creative.CreativeRunOutputStatusPending,
			}}
			test.configure(svc)

			outputBytes := test.bytes
			if outputBytes == nil {
				outputBytes = []byte("image")
			}
			_, err := svc.Results.SucceedRun(context.Background(), runID, providerID, []creative.ProviderOutput{{
				Index: 0, Success: true, Bytes: outputBytes, Mime: "image/png",
			}})

			billingRepo, ok := creativeFixtureBilling(svc).(*creativeFakeBillingRepo)
			require.True(t, ok)
			if test.name == "empty output" {
				require.ErrorIs(t, err, creative.ErrCreativeTransientFailed)
				require.Equal(t, creative.CreativeRunStatusRunning, repo.runs[runID].Status)
				require.Equal(t, creative.CreativeRunOutputStatusPending, repo.outputs[runID][0].Status)
				require.Zero(t, billingRepo.captureN)
			} else {
				// Confirmed generation with undeliverable output is billed once and recorded as result_lost.
				require.NoError(t, err)
				require.Equal(t, creative.CreativeRunStatusResultLost, repo.runs[runID].Status)
				require.Equal(t, 1, billingRepo.captureN)
			}
		})
	}
}

func (r *creativeFakeUsageLogRepo) Create(ctx context.Context, log *usage.UsageLog) (bool, error) {
	r.logs = append(r.logs, log)
	return true, nil
}

func stringValue(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
