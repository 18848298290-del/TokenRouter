package routing

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// mappedProbeExecutor checks that execution uses the selected model while history records the configured
// request model.
type mappedProbeExecutor struct {
	t *testing.T
}

func TestGroupProbeAttemptUsesMappedTargetAndRecordsRequestedModel(t *testing.T) {
	runner := NewGroupAvailabilityProbeRunnerService(nil, mappedProbeExecutor{t: t}, GroupProbeOptions{})
	result := runner.runProbeAttempt(context.Background(), GroupAvailabilityProbeDueGroup{GroupID: 59}, GroupAvailabilityProbeConfig{
		ModelID: "gemini-3.8-flash", Prompt: "hi", UserAgent: "probe-client",
	})
	require.True(t, result.Success)
	require.Equal(t, "gemini-3.8-flash", result.ModelID)
	require.Equal(t, int64(3678), *result.ProviderID)
}

func TestGroupAvailabilityProbeRunDueClaimsOnlyRunnableBatch(t *testing.T) {
	repo := &groupAvailabilityProbeRunnerRepoStub{}
	runner := &GroupAvailabilityProbeRunnerService{repo: repo, instanceID: "test-instance"}

	runner.runDue()

	calls, limit := repo.claimSnapshot()
	if calls != 1 {
		t.Fatalf("ClaimDue() calls = %d, want 1", calls)
	}
	if limit != groupAvailabilityProbeDefaultMaxWorkers {
		t.Fatalf("ClaimDue() limit = %d, want %d", limit, groupAvailabilityProbeDefaultMaxWorkers)
	}
}

func TestGroupAvailabilityProbeRunDueSkipsOverlappingRun(t *testing.T) {
	claimStarted := make(chan struct{})
	releaseClaim := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(releaseClaim)
		}
	}()

	repo := &groupAvailabilityProbeRunnerRepoStub{claimStarted: claimStarted, releaseClaim: releaseClaim}
	runner := &GroupAvailabilityProbeRunnerService{repo: repo, instanceID: "test-instance"}
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		runner.runDue()
	}()

	select {
	case <-claimStarted:
	case <-time.After(time.Second):
		t.Fatal("first run did not reach ClaimDue")
	}

	// A second call during an active round skips claiming; the instance limit bounds workers.
	runner.runDue()
	if calls, _ := repo.claimSnapshot(); calls != 1 {
		t.Fatalf("ClaimDue() calls during overlap = %d, want 1", calls)
	}

	close(releaseClaim)
	released = true
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("first run did not finish after releasing ClaimDue")
	}
}

func TestRunGroupAvailabilityProbeAttemptsStopsAfterSuccess(t *testing.T) {
	attempts := 0
	result := runGroupAvailabilityProbeAttempts(context.Background(), 3, time.Second, func(ctx context.Context) *GroupAvailabilityProbeResult {
		attempts++
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("each probe attempt must have its own deadline")
		}
		if attempts < 3 {
			return &GroupAvailabilityProbeResult{Status: GroupAvailabilityProbeStatusFailed, Success: false}
		}
		return &GroupAvailabilityProbeResult{Status: GroupAvailabilityProbeStatusSuccess, Success: true}
	})

	if result == nil || !result.Success {
		t.Fatalf("runGroupAvailabilityProbeAttempts() result = %+v, want success", result)
	}
	if attempts != 3 {
		t.Fatalf("runGroupAvailabilityProbeAttempts() attempts = %d, want 3", attempts)
	}
}

func TestRunGroupAvailabilityProbeAttemptsUsesRetriesAfterInitialAttempt(t *testing.T) {
	attempts := 0
	result := runGroupAvailabilityProbeAttempts(context.Background(), 3, 0, func(context.Context) *GroupAvailabilityProbeResult {
		attempts++
		return &GroupAvailabilityProbeResult{
			Status:       GroupAvailabilityProbeStatusFailed,
			Success:      false,
			ErrorMessage: "temporary failure",
		}
	})

	if result == nil || result.Success {
		t.Fatalf("runGroupAvailabilityProbeAttempts() result = %+v, want failure", result)
	}
	if attempts != 4 {
		t.Fatalf("runGroupAvailabilityProbeAttempts() attempts = %d, want 4", attempts)
	}
}

func TestRunGroupAvailabilityProbeAttemptsRetriesAfterAttemptTimeout(t *testing.T) {
	attempts := 0
	result := runGroupAvailabilityProbeAttempts(context.Background(), 1, 5*time.Millisecond, func(ctx context.Context) *GroupAvailabilityProbeResult {
		attempts++
		if attempts == 1 {
			<-ctx.Done()
			return &GroupAvailabilityProbeResult{
				Status:       GroupAvailabilityProbeStatusFailed,
				Success:      false,
				ErrorMessage: ctx.Err().Error(),
			}
		}
		if ctx.Err() != nil {
			t.Fatalf("retry context error = %v, want active context", ctx.Err())
		}
		return &GroupAvailabilityProbeResult{Status: GroupAvailabilityProbeStatusSuccess, Success: true}
	})

	if result == nil || !result.Success {
		t.Fatalf("runGroupAvailabilityProbeAttempts() result = %+v, want success", result)
	}
	if attempts != 2 {
		t.Fatalf("runGroupAvailabilityProbeAttempts() attempts = %d, want 2", attempts)
	}
}

func TestRunGroupAvailabilityProbeAttemptsStopsAfterParentCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	attempts := 0
	runGroupAvailabilityProbeAttempts(ctx, 3, 0, func(context.Context) *GroupAvailabilityProbeResult {
		attempts++
		cancel()
		return &GroupAvailabilityProbeResult{Status: GroupAvailabilityProbeStatusFailed, Success: false}
	})

	if attempts != 1 {
		t.Fatalf("runGroupAvailabilityProbeAttempts() attempts = %d, want 1", attempts)
	}
}

func (e mappedProbeExecutor) Select(_ context.Context, due GroupAvailabilityProbeDueGroup, model string) (GroupProbeTarget, error) {
	require.Equal(e.t, int64(59), due.GroupID)
	require.Equal(e.t, "gemini-3.8-flash", model)
	return GroupProbeTarget{ProviderID: 3678, ModelID: "gemini-3.8-flash-tiered"}, nil
}

func (e mappedProbeExecutor) Test(_ context.Context, id int64, model, prompt, userAgent string) (*ProbeExecutionResult, error) {
	require.Equal(e.t, int64(3678), id)
	require.Equal(e.t, "gemini-3.8-flash-tiered", model)
	require.Equal(e.t, "hi", prompt)
	require.Equal(e.t, "probe-client", userAgent)
	return &ProbeExecutionResult{Status: GroupAvailabilityProbeStatusSuccess}, nil
}
