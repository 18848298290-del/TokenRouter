package batchimage_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/batchimage"
	batchimageprovider "github.com/TokenFlux/TokenRouter/internal/batchimage/provider"
	billingcore "github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// Output deletion authorizes the owner and records remote cleanup before
// completed resources become unavailable to public downloads. These fixtures exercise completed output
// deletion, repeated deletion, state and owner rejection, provider absence
// and scrubbed failure diagnostics; separate cases inspect terminal input
// retention and expired output scans.

// TestBatchImageCleanupService_DeleteOutputsForOwner exercises
// owner-authorized deletion, repeated markers and rejected states through a
// recorded provider. It checks public output_deleted responses, stored
// timestamps and distinct sanitized transient or unsafe-path diagnostics.
func TestBatchImageCleanupService_DeleteOutputsForOwner(t *testing.T) {
	ctx := context.Background()

	t.Run("deletes completed output and returns public dto", func(t *testing.T) {
		svc, repo, platform := newTestBatchImageCleanupService()

		got, err := svc.DeleteOutputsForOwner(ctx, testBatchImageOwner(), "imgbatch_cleanup")
		require.NoError(t, err)
		require.Equal(t, "output_deleted", got.Status)
		require.NotNil(t, got.OutputDeletedAt)
		require.Equal(t, []batchimage.CleanupTarget{batchimage.CleanupTargetOutput}, platform.cleanupTargets)
		require.NotNil(t, repo.jobs["imgbatch_cleanup"].OutputDeletedAt)
		require.Equal(t, batchimage.BatchImageJobStatusOutputDeleted, repo.jobs["imgbatch_cleanup"].Status)
		body := mustBatchImageJSON(t, got)
		requireBatchImagePublicJSONHasNoInternals(t, body)
	})

	t.Run("repeated delete is idempotent", func(t *testing.T) {
		svc, repo, platform := newTestBatchImageCleanupService()
		deletedAt := time.Now()
		repo.jobs["imgbatch_cleanup"].Status = batchimage.BatchImageJobStatusOutputDeleted
		repo.jobs["imgbatch_cleanup"].OutputDeletedAt = &deletedAt

		got, err := svc.DeleteOutputsForOwner(ctx, testBatchImageOwner(), "imgbatch_cleanup")
		require.NoError(t, err)
		require.Equal(t, "output_deleted", got.Status)
		require.Empty(t, platform.cleanupTargets)
	})

	t.Run("not completed returns not ready", func(t *testing.T) {
		svc, repo, _ := newTestBatchImageCleanupService()
		repo.jobs["imgbatch_cleanup"].Status = batchimage.BatchImageJobStatusRunning

		_, err := svc.DeleteOutputsForOwner(ctx, testBatchImageOwner(), "imgbatch_cleanup")
		require.ErrorIs(t, err, batchimage.ErrBatchImageOutputDeleteNotReady)
	})

	t.Run("non owner returns not found", func(t *testing.T) {
		svc, _, _ := newTestBatchImageCleanupService()
		_, err := svc.DeleteOutputsForOwner(ctx, batchimage.BatchImageOwner{UserID: 11, APIKeyID: 999}, "imgbatch_cleanup")
		require.ErrorIs(t, err, batchimage.ErrBatchImageJobNotFound)
	})

	t.Run("platform not found is success", func(t *testing.T) {
		svc, repo, platform := newTestBatchImageCleanupService()
		platform.cleanupErr = apperror.New(404, "PROVIDER_NOT_FOUND", "platform file not found: gs://hidden")

		got, err := svc.DeleteOutputsForOwner(ctx, testBatchImageOwner(), "imgbatch_cleanup")
		require.NoError(t, err)
		require.Equal(t, "output_deleted", got.Status)
		require.NotNil(t, repo.jobs["imgbatch_cleanup"].OutputDeletedAt)
	})

	t.Run("platform transient error is sanitized and records failure", func(t *testing.T) {
		svc, repo, platform := newTestBatchImageCleanupService()
		platform.cleanupErr = errors.New("temporary cleanup failed for gs://secret-output")

		_, err := svc.DeleteOutputsForOwner(ctx, testBatchImageOwner(), "imgbatch_cleanup")
		require.ErrorIs(t, err, batchimage.ErrBatchImageProviderCleanupFailed)
		require.Equal(t, "BATCH_IMAGE_PROVIDER_CLEANUP_FAILED", apperror.Reason(err))
		require.NotContains(t, apperror.Message(err), "gs://")
		require.Equal(t, "BATCH_IMAGE_PROVIDER_CLEANUP_FAILED", batchimage.BatchImageDerefString(repo.jobs["imgbatch_cleanup"].LastErrorCode))
		require.Equal(t, "upstream platform operation failed", batchimage.BatchImageDerefString(repo.jobs["imgbatch_cleanup"].LastErrorMessage))
	})

	t.Run("unsafe cleanup path is not swallowed", func(t *testing.T) {
		svc, repo, platform := newTestBatchImageCleanupService()
		platform.cleanupErr = batchimage.ErrBatchImageProviderUnsafeCleanupPath

		_, err := svc.DeleteOutputsForOwner(ctx, testBatchImageOwner(), "imgbatch_cleanup")
		require.ErrorIs(t, err, batchimage.ErrBatchImageCleanupUnsafePath)
		require.Equal(t, "BATCH_IMAGE_CLEANUP_UNSAFE_PATH", batchimage.BatchImageDerefString(repo.jobs["imgbatch_cleanup"].LastErrorCode))
	})
}

// TestBatchImageCleanupService_InputOutputAndWorker cleans terminal inputs
// and outputs before running a bounded retention scan. It checks repeated
// input cleanup, retained failed status and exclusion of unfinished or
// future-expiry outputs.
func TestBatchImageCleanupService_InputOutputAndWorker(t *testing.T) {
	ctx := context.Background()
	now := time.Now()

	t.Run("input cleanup marks input only", func(t *testing.T) {
		svc, repo, platform := newTestBatchImageCleanupService()

		err := svc.CleanupInput(ctx, "imgbatch_cleanup")
		require.NoError(t, err)
		require.Equal(t, []batchimage.CleanupTarget{batchimage.CleanupTargetInput}, platform.cleanupTargets)
		require.NotNil(t, repo.jobs["imgbatch_cleanup"].InputDeletedAt)
		require.Equal(t, batchimage.BatchImageJobStatusCompleted, repo.jobs["imgbatch_cleanup"].Status)

		err = svc.CleanupInput(ctx, "imgbatch_cleanup")
		require.NoError(t, err)
		require.Len(t, platform.cleanupTargets, 1)
	})

	t.Run("output cleanup for failed job keeps status", func(t *testing.T) {
		svc, repo, _ := newTestBatchImageCleanupService()
		repo.jobs["imgbatch_cleanup"].Status = batchimage.BatchImageJobStatusFailed

		err := svc.CleanupOutput(ctx, "imgbatch_cleanup", "ttl")
		require.NoError(t, err)
		require.Equal(t, batchimage.BatchImageJobStatusFailed, repo.jobs["imgbatch_cleanup"].Status)
		require.NotNil(t, repo.jobs["imgbatch_cleanup"].OutputDeletedAt)
	})

	t.Run("worker processes due jobs and continues after failure", func(t *testing.T) {
		svc, repo, platform := newTestBatchImageCleanupService()
		platform.cleanupErr = nil
		old := now.Add(-48 * time.Hour)
		expired := now.Add(-time.Minute)
		future := now.Add(time.Hour)
		repo.jobs["imgbatch_cleanup"].FinishedAt = &old
		repo.jobs["imgbatch_cleanup"].OutputExpiresAt = &expired
		repo.jobs["imgbatch_running"] = cleanupTestJob("imgbatch_running", batchimage.BatchImageJobStatusRunning)
		repo.jobs["imgbatch_running"].FinishedAt = &old
		repo.jobs["imgbatch_running"].OutputExpiresAt = &expired
		repo.jobs["imgbatch_future"] = cleanupTestJob("imgbatch_future", batchimage.BatchImageJobStatusCompleted)
		repo.jobs["imgbatch_future"].FinishedAt = &old
		repo.jobs["imgbatch_future"].OutputExpiresAt = &future

		result, err := svc.RunOnce(ctx, now)
		require.NoError(t, err)
		require.Equal(t, 2, result.InputCleaned)
		require.Equal(t, 1, result.OutputCleaned)
		require.Equal(t, batchimage.BatchImageJobStatusRunning, repo.jobs["imgbatch_running"].Status)
		require.Nil(t, repo.jobs["imgbatch_future"].OutputDeletedAt)
		require.NotContains(t, strings.Join(repo.events["imgbatch_running"], ","), "cleanup")
	})
}

// TestBatchImageDownloadAfterOutputDeletedReturnsGone marks a completed
// fixture as output_deleted before item and ZIP requests. Both paths return
// the deleted-output category and produce no usable download result for that
// state.
func TestBatchImageDownloadAfterOutputDeletedReturnsGone(t *testing.T) {
	svc, repo, _ := newTestBatchImageDownloadService()
	now := time.Now()
	repo.jobs["imgbatch_download"].Status = batchimage.BatchImageJobStatusOutputDeleted
	repo.jobs["imgbatch_download"].OutputDeletedAt = &now

	stream, err := svc.OpenItemContent(context.Background(), testBatchImageOwner(), "imgbatch_download", "cover/../001", 0)
	require.Nil(t, stream)
	require.ErrorIs(t, err, batchimage.ErrBatchImageOutputDeleted)

	var out strings.Builder
	result, err := svc.StreamZip(context.Background(), testBatchImageOwner(), "imgbatch_download", batchimage.BatchImageZipOptions{}, &out)
	require.Nil(t, result)
	require.ErrorIs(t, err, batchimage.ErrBatchImageOutputDeleted)
}

// newTestBatchImageCleanupService constructs a completed cleanup job with a
// recording Gemini backend and fixed retention options. The returned
// repository and provider remain shared with the service so cases can inject
// state and cleanup errors.
func newTestBatchImageCleanupService() (*batchimage.Cleanup, *fakeBatchImageRepository, *publicBatchImageProvider) {
	repo := newFakeBatchImageRepository()
	repo.jobs["imgbatch_cleanup"] = cleanupTestJob("imgbatch_cleanup", batchimage.BatchImageJobStatusCompleted)
	platform := &publicBatchImageProvider{name: batchimage.BatchImageProviderGeminiAPI}
	providerID := int64(101)
	svc := newBatchCleanupFixture(repo, batchimage.NewRegistry[batchimageprovider.BatchImageProvider](platform), &resultProviderFixture{provider: &provider.Record{ID: providerID, Platform: capability.PlatformGemini, Type: capability.ProviderTypeAPIKey, Status: billingcore.StatusActive, Schedulable: true}}, &config.Config{BatchImage: config.BatchImageConfig{CleanupBatchSize: 10, InputRetentionAfterTerminalHours: 24}})
	return svc, repo, platform
}

// cleanupTestJob creates an old job with owner, provider references and
// terminal-time snapshots for the supplied status. Tests mutate the returned
// record directly before running cleanup or inspecting its saved deletion
// markers.
func cleanupTestJob(batchID, status string) *batchimage.BatchImageJob {
	apiKeyID := int64(22)
	providerID := int64(101)
	now := time.Now().Add(-48 * time.Hour)
	return &batchimage.BatchImageJob{
		BatchID:           batchID,
		UserID:            11,
		APIKeyID:          &apiKeyID,
		ProviderID:        &providerID,
		Platform:          batchimage.BatchImageProviderGeminiAPI,
		Model:             "gemini-2.5-flash-image",
		Status:            status,
		ProviderJobName:   batchimage.BatchImageStringPtr("providers/internal/job"),
		ProviderInputRef:  batchimage.BatchImageStringPtr("files/internal/input"),
		ProviderOutputRef: batchimage.BatchImageStringPtr("files/internal/output"),
		ItemCount:         1,
		SuccessCount:      1,
		CreatedAt:         now,
		UpdatedAt:         now,
		FinishedAt:        &now,
		SettledAt:         &now,
	}
}

// mustBatchImageJSON serializes a public fixture value and fails the calling
// test if encoding fails. It returns the JSON text used by shared assertions
// to screen provider references and credential field names.
func mustBatchImageJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}
