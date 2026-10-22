package routing

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// group_probe_runner_test.go checks attempt identity, timing, retries, and round admission.
// A configured probe model can differ from the execution model returned by
// provider selection. The mapped executor checks the selected target and
// forwarded prompt while the result assertion retains the administrator's
// requested model in history.
//
// Round tests expose claim entry to hold one run active while a second tick
// arrives, then release and join the first run. Retry callbacks supply
// controlled failures, success, timeout, and parent cancellation so
// assertions can distinguish the initial attempt from additional retries and
// inspect each child's deadline. These callbacks classify their own results;
// the cancellation case returns failure and checks the number of calls that
// follow it.

// mappedProbeExecutor checks selected probe arguments through the owning
// test's assertions.
type mappedProbeExecutor struct {
	t *testing.T // owning probe assertion test
}

// TestGroupProbeAttemptUsesMappedTargetAndRecordsRequestedModel selects a
// different execution model for the configured request. Executor assertions
// check forwarded arguments, while the returned result retains the request
// name and selected provider ID.
func TestGroupProbeAttemptUsesMappedTargetAndRecordsRequestedModel(t *testing.T) {
	runner := NewGroupAvailabilityProbeRunnerService(nil, mappedProbeExecutor{t: t}, GroupProbeOptions{})
	result := runner.runProbeAttempt(context.Background(), GroupAvailabilityProbeDueGroup{GroupID: 59}, GroupAvailabilityProbeConfig{
		ModelID: "gemini-3.8-flash", Prompt: "hi", UserAgent: "probe-client", Protocol: protocol.ProtocolOpenAIChatCompletions,
	})
	require.True(t, result.Success)
	require.Equal(t, "gemini-3.8-flash", result.ModelID)
	require.Equal(t, int64(3678), *result.ProviderID)
}

// TestGroupAvailabilityProbeRunDueClaimsOnlyRunnableBatch executes an empty
// due-group round through the repository fixture. It checks one claim call
// and a claim limit equal to the default worker count.
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

// TestGroupAvailabilityProbeRunDueSkipsOverlappingRun blocks the first round
// inside ClaimDue before invoking another round. The overlapping call makes
// no extra claim, and releasing the fixture lets the first round finish.
func TestGroupAvailabilityProbeRunDueSkipsOverlappingRun(t *testing.T) {
	claimStarted := make(chan struct{})
	releaseClaim := make(chan struct{})
	released := false
	// Release a blocked claim if the test exits before normal completion, so an
	// assertion failure does not leave its runner goroutine waiting on the gate.
	defer func() {
		if !released {
			close(releaseClaim)
		}
	}()

	repo := &groupAvailabilityProbeRunnerRepoStub{claimStarted: claimStarted, releaseClaim: releaseClaim}
	runner := &GroupAvailabilityProbeRunnerService{repo: repo, instanceID: "test-instance"}
	firstDone := make(chan struct{})
	// Signal completion when the admitted round returns so the test can join its
	// goroutine after releasing the repository gate.
	go func() {
		defer close(firstDone)
		runner.runDue()
	}()

	// Establish claim entry before issuing the overlapping tick. The test
	// deadline turns a round that never reaches the repository into a bounded
	// failure.
	select {
	case <-claimStarted:
	case <-time.After(time.Second):
		t.Fatal("first run did not reach ClaimDue")
	}

	// Keep the first claim blocked while issuing another tick, so the single
	// recorded claim demonstrates that overlapping rounds are rejected before
	// reaching the repository.
	runner.runDue()
	if calls, _ := repo.claimSnapshot(); calls != 1 {
		t.Fatalf("ClaimDue() calls during overlap = %d, want 1", calls)
	}

	// Release the admitted claim and mark the gate closed before deferred
	// cleanup runs, preventing a second close during test teardown.
	close(releaseClaim)
	released = true
	// Join after releasing the gate so the claim can return. The test deadline
	// reports a round that remains unfinished instead of leaving this assertion
	// blocked.
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("first run did not finish after releasing ClaimDue")
	}
}

// TestRunGroupAvailabilityProbeAttemptsStopsAfterSuccess fails two callbacks
// before the third succeeds with retries still available. Each callback has a
// deadline, and success ends the sequence at three attempts.
func TestRunGroupAvailabilityProbeAttemptsStopsAfterSuccess(t *testing.T) {
	attempts := 0
	// A positive timeout makes each attempt's deadline observable.
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

// TestRunGroupAvailabilityProbeAttemptsUsesRetriesAfterInitialAttempt returns
// failure on every callback with three retries configured. It checks four
// total attempts and a failed final result.
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

// TestRunGroupAvailabilityProbeAttemptsRetriesAfterAttemptTimeout waits for
// the first child deadline to expire and returns failure. The second callback
// receives an active context and succeeds after exactly one retry.
func TestRunGroupAvailabilityProbeAttemptsRetriesAfterAttemptTimeout(t *testing.T) {
	attempts := 0
	// A short attempt budget expires the deliberately blocked callback promptly.
	result := runGroupAvailabilityProbeAttempts(context.Background(), 1, 5*time.Millisecond, func(ctx context.Context) *GroupAvailabilityProbeResult {
		attempts++
		if attempts == 1 {
			// Make the first attempt consume its own deadline so the next callback
			// proves that retries receive a fresh active child context.
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

// TestRunGroupAvailabilityProbeAttemptsStopsAfterParentCancellation cancels
// the parent during the first callback and returns a failed result. The
// assertion checks that no additional retry callback starts.
func TestRunGroupAvailabilityProbeAttemptsStopsAfterParentCancellation(t *testing.T) {
	// Give the callback control over its parent lifetime so canceling during
	// failure can be observed before the helper admits another retry.
	ctx, cancel := context.WithCancel(context.Background())
	attempts := 0
	runGroupAvailabilityProbeAttempts(ctx, 3, 0, func(context.Context) *GroupAvailabilityProbeResult {
		attempts++
		// Cancel the parent before returning failure, making the helper's
		// post-attempt cancellation check responsible for stopping the remaining
		// configured retries.
		cancel()
		return &GroupAvailabilityProbeResult{Status: GroupAvailabilityProbeStatusFailed, Success: false}
	})

	if attempts != 1 {
		t.Fatalf("runGroupAvailabilityProbeAttempts() attempts = %d, want 1", attempts)
	}
}

// Select asserts the configured group and request model, then returns
// provider 3678 with a different mapped model. Those checks make selection
// input observable inside the owning test.
func (e mappedProbeExecutor) Select(_ context.Context, due GroupAvailabilityProbeDueGroup, model string) (GroupProbeTarget, error) {
	require.Equal(e.t, int64(59), due.GroupID)
	require.Equal(e.t, "gemini-3.8-flash", model)
	require.Equal(e.t, protocol.ProtocolOpenAIChatCompletions, due.Config.Protocol)
	return GroupProbeTarget{GroupID: due.GroupID, ProviderID: 3678, ModelID: "gemini-3.8-flash-tiered", ProtocolRoute: provider.TestProtocolRoute{ClientProtocol: due.Config.Protocol, UpstreamProtocol: protocol.ProtocolGeminiGenerateContent}}, nil
}

// Test asserts the selected provider, mapped model, prompt, and User-Agent
// before returning successful status. Timing fields remain zero because this
// executor verifies argument forwarding rather than measured execution
// duration.
func (e mappedProbeExecutor) Test(_ context.Context, target GroupProbeTarget, prompt, userAgent string) (*ProbeExecutionResult, error) {
	require.Equal(e.t, int64(59), target.GroupID)
	require.Equal(e.t, int64(3678), target.ProviderID)
	require.Equal(e.t, "gemini-3.8-flash-tiered", target.ModelID)
	require.Equal(e.t, provider.TestProtocolRoute{ClientProtocol: protocol.ProtocolOpenAIChatCompletions, UpstreamProtocol: protocol.ProtocolGeminiGenerateContent}, target.ProtocolRoute)
	require.Equal(e.t, "hi", prompt)
	require.Equal(e.t, "probe-client", userAgent)
	return &ProbeExecutionResult{Status: GroupAvailabilityProbeStatusSuccess}, nil
}
