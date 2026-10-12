package creative_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/creative"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	testassert "github.com/TokenFlux/TokenRouter/internal/testutil/assertion"
)

func TestCreativeWorkerSuccessPath(t *testing.T) {
	f := newCreativeWorkerFixture()
	runID := "crun_workersuccess01"
	seedCreativeRun(f, runID, true)
	f.exec.onExecute = func(id string) {
		require.Equal(t, creative.PlatformGemini, f.repo.runs[id].Platform)
		require.Equal(t, int64(55), *f.repo.runs[id].ProviderID)
	}
	f.exec.result = &creative.CreativeExecuteResult{
		Outputs:    []creative.CreativeOutput{{Index: 0, Bytes: []byte("img"), Mime: "image/png"}},
		ProviderID: 55,
	}

	result, err := f.worker.Process(context.Background(), runID)
	require.NoError(t, err)
	require.True(t, result.Terminal)

	run := f.repo.runs[runID]
	require.Equal(t, creative.CreativeRunStatusSucceeded, run.Status)
	require.NotNil(t, run.ProviderID)
	require.Equal(t, int64(55), *run.ProviderID)
	require.Equal(t, 1, f.repo.setProviderN)
	require.NotNil(t, run.ActualCost)
	// A succeeded output has both metadata and readable temporary bytes.
	output := f.repo.outputs[runID][0]
	require.Equal(t, creative.CreativeRunOutputStatusSucceeded, output.Status)
	require.NotNil(t, output.TransientExpiresAt)
	data, err := f.store.LoadOutput(context.Background(), runID, 0)
	require.NoError(t, err)
	require.Equal(t, []byte("img"), data)
	// The fixture seeds the task directly, bypassing creation-time reservation; the worker captures funds.
	require.Equal(t, 0, f.billing.reserveN)
	require.Equal(t, 1, f.billing.captureN)
	require.Equal(t, 0, f.billing.releaseN)
}

// TestCreativeWorkerRecoversPersistedProviderOutput checks settlement recovery from saved outputs without
// another provider call.
func TestCreativeWorkerRecoversPersistedProviderOutput(t *testing.T) {
	f := newCreativeWorkerFixture()
	runID := "crun_workerrecover01"
	seedCreativeRun(f, runID, false)
	run := f.repo.runs[runID]
	run.Status = creative.CreativeRunStatusRunning
	providerID := int64(55)
	run.ProviderID = &providerID
	f.repo.outputs[runID][0].Status = creative.CreativeRunOutputStatusSucceeded
	f.store.outputs[runID+":0"] = []byte("img")

	result, err := f.worker.Process(context.Background(), runID)
	require.NoError(t, err)
	require.True(t, result.Terminal)
	require.Equal(t, 0, f.exec.calls)
	require.Equal(t, creative.CreativeRunStatusSucceeded, f.repo.runs[runID].Status)
	require.Equal(t, 1, f.billing.captureN)
}

// TestCreativeWorkerUserConcurrencyPending checks that unavailable user capacity leaves the task queued for
// a short retry.
func TestCreativeWorkerUserConcurrencyPending(t *testing.T) {
	f := newCreativeWorkerFixture()
	seedCreativeRun(f, "crun_worker_user_pending", true)
	userRepo := testassert.MustType[*creativeFakeUserRepo](testassert.MustType[creativeUserReader](f.service.UserRepo).source)
	userRepo.user.Concurrency = 1
	cache := &creativeUserCacheFixture{acquireResult: false}
	f.worker = newCreativeWorkerFixtureForSources(f.queue, f.repo, f.store, f.exec, f.service, f.worker.Options(), scheduler.NewConcurrencyService(cache, scheduler.Diagnostics{
		Logf:  logging.LegacyPrintf,
		Event: logging.Event,
	},
	))

	result, err := f.worker.Process(context.Background(), "crun_worker_user_pending")
	require.NoError(t, err)
	require.False(t, result.Terminal)
	require.Equal(t, creative.DefaultCreativeConcurrencyRequeueDelay, result.RequeueAfter)
	require.Equal(t, creative.CreativeRunStatusQueued, f.repo.runs["crun_worker_user_pending"].Status)
	require.Equal(t, 0, f.exec.calls)
}

// TestCreativeWorkerProviderConcurrencyPending checks that unavailable provider capacity leaves task status
// unchanged.
func TestCreativeWorkerProviderConcurrencyPending(t *testing.T) {
	f := newCreativeWorkerFixture()
	seedCreativeRun(f, "crun_worker_provider_pending", true)
	f.exec.execPrepareErr = creative.ErrCreativeExecutionPending

	result, err := f.worker.Process(context.Background(), "crun_worker_provider_pending")
	require.NoError(t, err)
	require.False(t, result.Terminal)
	require.Equal(t, creative.DefaultCreativeConcurrencyRequeueDelay, result.RequeueAfter)
	require.Equal(t, creative.CreativeRunStatusQueued, f.repo.runs["crun_worker_provider_pending"].Status)
}

// TestCreativeWorkerRetriesTransientOutputFailure checks billable delivery loss after save retries and a
// single provider call.
func TestCreativeWorkerRetriesTransientOutputFailure(t *testing.T) {
	f := newCreativeWorkerFixture()
	id := "crun_workeroutputretry1"
	seedCreativeRun(f, id, true)
	f.store.saveOutputErr = errors.New("redis unavailable")
	f.exec.result = &creative.CreativeExecuteResult{Outputs: []creative.CreativeOutput{{Index: 0, Bytes: []byte("img"), Mime: "image/png"}}, ProviderID: 55}
	result, err := f.worker.Process(context.Background(), id)
	require.NoError(t, err)
	require.True(t, result.Terminal)
	require.Equal(t, creative.CreativeRunStatusResultLost, f.repo.runs[id].Status)
	require.NotNil(t, f.repo.runs[id].ProviderResultRecordedAt)
	require.Equal(t, 1, f.billing.captureN)
	require.Zero(t, f.billing.releaseN)
	f.store.saveOutputErr = nil
	result, err = f.worker.Process(context.Background(), id)
	require.NoError(t, err)
	require.True(t, result.Terminal)
	require.Equal(t, 1, f.exec.calls)
	require.Equal(t, 1, f.billing.captureN)
}

func TestCreativeWorkerRetryableError(t *testing.T) {
	f := newCreativeWorkerFixture()
	runID := "crun_workerretry0001"
	seedCreativeRun(f, runID, true)
	f.exec.err = creative.CreativeHTTPStatusError(503, "upstream busy")

	// The first two retryable failures increment the attempt count and request requeueing.
	for attempt := 1; attempt <= 2; attempt++ {
		result, err := f.worker.Process(context.Background(), runID)
		require.NoError(t, err)
		require.False(t, result.Terminal)
		require.Greater(t, result.RequeueAfter, time.Duration(0))
		require.Equal(t, attempt, f.repo.runs[runID].AttemptCount)
	}
	require.Equal(t, 2, f.exec.calls)

	// The third failure exhausts attempts and releases the hold before terminal acknowledgment.
	result, err := f.worker.Process(context.Background(), runID)
	require.NoError(t, err)
	require.True(t, result.Terminal)
	run := f.repo.runs[runID]
	require.Equal(t, creative.CreativeRunStatusFailed, run.Status)
	require.NotNil(t, run.ErrorCode)
	require.Equal(t, 3, run.AttemptCount)
	require.Equal(t, 1, f.billing.releaseN)
}

// TestCreativeWorkerImageResultFailure checks that image download/decoding failures terminate after one
// provider call.
func TestCreativeWorkerImageResultFailure(t *testing.T) {
	for _, code := range []string{"IMAGE_DOWNLOAD_FAILED", "INVALID_IMAGE_RESPONSE"} {
		t.Run(code, func(t *testing.T) {
			f := newCreativeWorkerFixture()
			runID := "crun_image_result_failure"
			seedCreativeRun(f, runID, true)
			f.exec.err = creative.CreativeImageResultError(code, "creative image result failed")
			for range 2 {
				result, err := f.worker.Process(context.Background(), runID)
				require.NoError(t, err)
				require.True(t, result.Terminal)
				require.Zero(t, result.RequeueAfter)
			}
			run := f.repo.runs[runID]
			require.Equal(t, creative.CreativeRunStatusFailed, run.Status)
			require.Equal(t, code, *run.ErrorCode)
			require.Equal(t, 1, f.exec.calls)
			require.Zero(t, run.AttemptCount)
			require.Equal(t, 1, f.billing.releaseN)
		})
	}
}

func TestCreativeWorkerNonRetryableError(t *testing.T) {
	f := newCreativeWorkerFixture()
	runID := "crun_workernonretry01"
	seedCreativeRun(f, runID, true)
	f.exec.err = creative.CreativeNonRetryableError("grok platform does not support creative operation edit")

	result, err := f.worker.Process(context.Background(), runID)
	require.NoError(t, err)
	require.True(t, result.Terminal)
	require.Equal(t, 1, f.exec.calls)
	// Nonretryable failures release funds without incrementing the retry counter.
	run := f.repo.runs[runID]
	require.Equal(t, creative.CreativeRunStatusFailed, run.Status)
	require.Equal(t, 0, run.AttemptCount)
	require.Equal(t, 1, f.billing.releaseN)
}

// TestCreativeWorkerCancelRace checks that completed generation is billed while task status remains cancelled.
func TestCreativeWorkerCancelRace(t *testing.T) {
	f := newCreativeWorkerFixture()
	runID := "crun_workercancel001"
	seedCreativeRun(f, runID, true)
	f.exec.result = &creative.CreativeExecuteResult{
		Outputs:    []creative.CreativeOutput{{Index: 0, Bytes: []byte("img"), Mime: "image/png"}},
		ProviderID: 55,
	}
	// Simulate cancellation while the upstream request is running.
	f.exec.onExecute = func(runID string) {
		run := f.repo.runs[runID]
		run.Status = creative.CreativeRunStatusCancelled
	}

	result, err := f.worker.Process(context.Background(), runID)
	require.NoError(t, err)
	require.True(t, result.Terminal)

	run := f.repo.runs[runID]
	require.Equal(t, creative.CreativeRunStatusCancelled, run.Status)
	// The confirmed provider result is captured even though the task remains cancelled.
	require.Equal(t, 1, f.billing.captureN)
	require.Equal(t, 0, f.billing.releaseN)
	require.NotNil(t, run.ActualCost)
	output := f.repo.outputs[runID][0]
	require.Equal(t, creative.CreativeRunOutputStatusSucceeded, output.Status)
}

// TestCreativeWorkerPayloadLost checks result_lost and hold release when payload bytes are absent.
func TestCreativeWorkerPayloadLost(t *testing.T) {
	f := newCreativeWorkerFixture()
	runID := "crun_workerlost00001"
	seedCreativeRun(f, runID, false)

	result, err := f.worker.Process(context.Background(), runID)
	require.NoError(t, err)
	require.True(t, result.Terminal)
	require.Equal(t, 0, f.exec.calls)

	run := f.repo.runs[runID]
	require.Equal(t, creative.CreativeRunStatusResultLost, run.Status)
	// No provider call occurred, so the hold is released.
	require.Equal(t, 1, f.billing.releaseN)
	require.Equal(t, 0, f.billing.captureN)
}

// TestCreativeWorkerCancelBeforeExecute checks that a cancelled task is acknowledged without calling the
// provider.
func TestCreativeWorkerCancelBeforeExecute(t *testing.T) {
	f := newCreativeWorkerFixture()
	runID := "crun_workerprecancel1"
	seedCreativeRun(f, runID, true)
	f.repo.runs[runID].Status = creative.CreativeRunStatusCancelled

	result, err := f.worker.Process(context.Background(), runID)
	require.NoError(t, err)
	require.True(t, result.Terminal)
	require.Equal(t, 0, f.exec.calls)
	// An already terminal task does not trigger another release.
	require.Equal(t, 0, f.billing.releaseN)
}

// TestCreativeWorkerRunOnceAck covers reservation, locking, processing, acknowledgment, and lock release.
func TestCreativeWorkerRunOnceAck(t *testing.T) {
	f := newCreativeWorkerFixture()
	runID := "crun_workerrunonce001"
	seedCreativeRun(f, runID, true)
	f.queue.reserveBatch = []string{runID}
	f.exec.result = &creative.CreativeExecuteResult{
		Outputs:    []creative.CreativeOutput{{Index: 0, Bytes: []byte("img"), Mime: "image/png"}},
		ProviderID: 55,
	}

	require.NoError(t, f.worker.RunOnce(context.Background()))
	require.Equal(t, []string{runID}, f.queue.acked)
	require.Empty(t, f.queue.requeued)
	require.NotNil(t, f.queue.lastLock)
	require.True(t, f.queue.lastLock.released)
	require.Equal(t, creative.CreativeRunStatusSucceeded, f.repo.runs[runID].Status)
}

func TestCreativeOutputFailureMustNotInferAgain(t *testing.T) {
	f := newCreativeWorkerFixture()
	id := "crun_test_output_failure"
	seedCreativeRun(f, id, true)
	f.store.saveOutputErr = errors.New("redis unavailable")
	f.exec.result = &creative.CreativeExecuteResult{Outputs: []creative.CreativeOutput{{Index: 0, Bytes: []byte("img"), Mime: "image/png"}}, ProviderID: 55}
	_, err := f.worker.Process(context.Background(), id)
	require.NoError(t, err)
	f.store.saveOutputErr = nil
	_, err = f.worker.Process(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, 1, f.exec.calls, "platform 已成功，保存输出失败后的恢复不能再次推理")
}
