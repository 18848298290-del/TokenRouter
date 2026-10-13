package batchimage_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/batchimage"
	batchimageprovider "github.com/TokenFlux/TokenRouter/internal/batchimage/provider"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
)

// Provider result rows can use different identity and inline-image spellings
// or omit expected custom IDs. The parser and index fixtures protect
// metadata-only storage, custom-ID reconciliation and stage progression,
// while transient polling failures receive another visit and confirmed
// cancellation returns held funds.

// TestParseBatchImageResultLine_SuccessShapes parses compatible custom-ID and
// candidate shapes, including competing envelopes and malformed parts. It
// checks counts, first MIME, extensions and physical line metadata while the
// parsed value omits encoded payload content.
func TestParseBatchImageResultLine_SuccessShapes(t *testing.T) {
	tests := []struct {
		name      string // scenario display name
		line      string // provider JSON input
		wantID    string // expected custom identity
		wantMime  string // expected image MIME
		wantExt   string // expected filename extension
		wantCount int    // expected image count
	}{
		{
			name:   "gemini_inlineData",
			line:   `{"key":"cover_001","response":{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"` + batchImageTestData + `"}}]}}]}}`,
			wantID: "cover_001", wantMime: "image/png", wantExt: "png", wantCount: 1,
		},
		{
			name:   "snake_case_inline_data",
			line:   `{"custom_id":"cover_002","response":{"candidates":[{"content":{"parts":[{"inline_data":{"mime_type":"image/jpeg","data":"` + batchImageTestData + `"}}]}}]}}`,
			wantID: "cover_002", wantMime: "image/jpeg", wantExt: "jpg", wantCount: 1,
		},
		{
			name:   "vertex_top_level_response",
			line:   `{"customId":"cover_003","response":{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/webp","data":"` + batchImageTestData + `"}}]}}]}}`,
			wantID: "cover_003", wantMime: "image/webp", wantExt: "webp", wantCount: 1,
		},
		{
			name:   "top_level_candidates",
			line:   `{"request":{"key":"cover_004"},"candidates":[{"content":{"parts":[{"inline_data":{"mime_type":"image/png","data":"` + batchImageTestData + `"}},{"inlineData":{"mimeType":"image/png","data":"` + batchImageTestData + `"}}]}}]}`,
			wantID: "cover_004", wantMime: "image/png", wantExt: "png", wantCount: 2,
		},
		{
			name: "response_images_take_priority",
			line: `{"key":"cover_005","response":{"candidates":[{"content":{"parts":[
				{"inlineData":{"mimeType":" Image/JPEG ","data":" invalid base64 "}},
				{"inline_data":{"mime_type":"image/webp","data":"c2Vjb25k"}}
			]}}]},"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"dGhpcmQ="}}]}}]}`,
			wantID: "cover_005", wantMime: "Image/JPEG", wantExt: "jpg", wantCount: 2,
		},
		{
			name: "empty_response_falls_back_to_top_level",
			line: `{"key":"cover_006","response":{"candidates":[{"content":{"parts":[{"text":"none"}]}}]},
				"candidates":[{"content":{"parts":[{"inline_data":{"mime_type":"image/png","data":"Zmlyc3Q="}}]}}]}`,
			wantID: "cover_006", wantMime: "image/png", wantExt: "png", wantCount: 1,
		},
		{
			name: "skip_empty_and_non_image_parts",
			line: `{"key":"cover_007","candidates":[null,{}, {"content":{"parts":[
				null,{"text":"none"},{"inlineData":{"mimeType":"text/plain","data":"dGV4dA=="}},
				{"inlineData":{"mimeType":"image/png","data":" \t "}},
				{"inlineData":{},"inline_data":{"mime_type":"image/png","data":"c2tpcA=="}},
				{"inlineData":{"mimeType":" ","mime_type":" image/webp ","data":"cGljdHVyZQ=="}}
			]}}]}`,
			wantID: "cover_007", wantMime: "image/webp", wantExt: "webp", wantCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := batchimage.ParseBatchImageResultLine([]byte(tt.line), 7)
			require.NoError(t, err)
			require.Equal(t, tt.wantID, got.CustomID)
			require.Equal(t, batchimage.BatchImageParsedStatusSucceeded, got.Status)
			require.Equal(t, tt.wantMime, got.MimeType)
			require.Equal(t, tt.wantExt, got.FileExtension)
			require.Equal(t, tt.wantCount, got.ImageCount)
			require.Equal(t, 7, got.SourceLineNumber)
			require.NotContains(t, fmt.Sprintf("%+v", got), batchImageTestData)
		})
	}
}

// TestParseBatchImageResultLine_FailureShapes supplies status, error, quota
// and text-only result rows. The parser returns failed outcomes with the
// expected stable category, protecting the item diagnostics later exposed by
// public listings.
func TestParseBatchImageResultLine_FailureShapes(t *testing.T) {
	tests := []struct {
		name     string // scenario display name
		line     string // provider JSON input
		wantCode string // expected failure category
	}{
		{name: "status_row", line: `{"key":"cover_001","status":{"code":3,"message":"invalid argument: bad prompt"}}`, wantCode: "INVALID_ARGUMENT"},
		{name: "error_row", line: `{"key":"cover_002","error":{"code":"SAFETY","message":"blocked by safety policy"}}`, wantCode: "SAFETY_BLOCKED"},
		{name: "quota_row", line: `{"key":"cover_003","error":{"code":"RESOURCE_EXHAUSTED","message":"quota exceeded"}}`, wantCode: "PROVIDER_RATE_LIMITED"},
		{name: "empty_image_output", line: `{"key":"cover_004","response":{"candidates":[{"content":{"parts":[{"text":"no image"}]}}]}}`, wantCode: "EMPTY_IMAGE_OUTPUT"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := batchimage.ParseBatchImageResultLine([]byte(tt.line), 1)
			require.NoError(t, err)
			require.Equal(t, batchimage.BatchImageParsedStatusFailed, got.Status)
			require.Equal(t, tt.wantCode, got.ErrorCode)
		})
	}
}

// TestParseBatchImageResultLine_RejectsMissingCustomIDAndDoesNotLeakData
// submits an image-bearing row with no usable identity. It checks the
// indexing parse category and confirms the diagnostic omits the encoded
// sample included in that input.
func TestParseBatchImageResultLine_RejectsMissingCustomIDAndDoesNotLeakData(t *testing.T) {
	_, err := batchimage.ParseBatchImageResultLine([]byte(`{"response":{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"`+batchImageTestData+`"}}]}}]}}`), 3)
	require.ErrorIs(t, err, batchimage.ErrBatchImageIndexParseFailed)
	require.NotContains(t, err.Error(), batchImageTestData)
}

// TestBatchImageResultIndexer_WritesCountsAndReplacesItems indexes a
// successful and failed row, then changes output to an unknown identity. It
// checks replacement counts, metadata-only rows and missing failures for the
// previously stored custom IDs.
func TestBatchImageResultIndexer_WritesCountsAndReplacesItems(t *testing.T) {
	output := `{"key":"ok","response":{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"` + batchImageTestData + `"}}]}}]}}` + "\n" + `{"key":"bad","error":{"code":"SAFETY","message":"blocked by safety policy"}}` + "\n"
	repo := newFakeBatchImageRepository()
	outputRef := "files/output"
	job := &batchimage.BatchImageJob{BatchID: "imgbatch_index", ProviderOutputRef: &outputRef}
	platform := &fakeProcessorProvider{result: output}

	result, err := (&batchimage.ResultIndexer{Repo: repo, Observe: resultObserve}).Index(context.Background(), job, batchimageprovider.BindProvider(platform, providercore.CloneRecord(&providercore.Record{})))
	require.NoError(t, err)
	require.True(t, platform.openResultCalled)
	require.Equal(t, 1, result.SuccessCount)
	require.Equal(t, 1, result.FailCount)
	require.Equal(t, 2, result.TotalCount)
	require.Equal(t, 1, repo.replaceCalls)
	require.Len(t, repo.items[job.BatchID], 2)
	require.Equal(t, batchimage.BatchImageItemStatusSuccess, repo.items[job.BatchID][0].Status)
	require.Equal(t, batchimage.BatchImageItemStatusFailed, repo.items[job.BatchID][1].Status)
	require.Equal(t, batchimage.BatchImageCounts{SuccessCount: 1, FailCount: 1}, repo.counts[job.BatchID])
	require.NotContains(t, fmt.Sprintf("%+v", repo.items[job.BatchID]), batchImageTestData)

	// Reindex against existing custom IDs: discard unknown "ok2" and record missing
	// "ok" and "bad" as PROVIDER_RESULT_MISSING failures.
	platform.result = `{"key":"ok2","response":{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/webp","data":"` + batchImageTestData + `"}}]}}]}}` + "\n"
	result, err = (&batchimage.ResultIndexer{Repo: repo, Observe: resultObserve}).Index(context.Background(), job, batchimageprovider.BindProvider(platform, providercore.CloneRecord(&providercore.Record{})))
	require.NoError(t, err)
	require.Equal(t, 2, result.TotalCount)
	require.Equal(t, 0, result.SuccessCount)
	require.Equal(t, 2, result.FailCount)
	require.Len(t, repo.items[job.BatchID], 2)
	gotIDs := []string{repo.items[job.BatchID][0].CustomID, repo.items[job.BatchID][1].CustomID}
	require.ElementsMatch(t, []string{"ok", "bad"}, gotIDs)
	for _, item := range repo.items[job.BatchID] {
		require.Equal(t, batchimage.BatchImageItemStatusFailed, item.Status)
		require.Equal(t, "PROVIDER_RESULT_MISSING", batchimage.BatchImageDerefString(item.ErrorCode))
	}
}

// TestBatchImageResultIndexer_ReconcilesMissingAndUnknownCustomIDs seeds
// three submission placeholders and returns one unknown extra provider row.
// The index retains only expected IDs, records the omitted result as failure
// and reconciles totals to the submitted count.
func TestBatchImageResultIndexer_ReconcilesMissingAndUnknownCustomIDs(t *testing.T) {
	repo := newFakeBatchImageRepository()
	outputRef := "files/output"
	job := &batchimage.BatchImageJob{BatchID: "imgbatch_reconcile", ProviderOutputRef: &outputRef, ItemCount: 3}
	// Seed pending items as the submission flow does.
	require.NoError(t, repo.BulkCreateBatchImageItems(context.Background(), []batchimage.CreateBatchImageItemParams{
		{JobID: job.BatchID, CustomID: "a", Status: batchimage.BatchImageItemStatusPending},
		{JobID: job.BatchID, CustomID: "b", Status: batchimage.BatchImageItemStatusPending},
		{JobID: job.BatchID, CustomID: "c", Status: batchimage.BatchImageItemStatusPending},
	}))
	// Provider output succeeds for a, fails for b, omits c, and adds unknown x.
	output := strings.Join([]string{
		`{"key":"a","response":{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"` + batchImageTestData + `"}}]}}]}}`,
		`{"key":"b","error":{"code":"SAFETY","message":"blocked"}}`,
		`{"key":"x","error":{"code":"UNKNOWN","message":"not ours"}}`,
	}, "\n") + "\n"
	platform := &fakeProcessorProvider{result: output}

	result, err := (&batchimage.ResultIndexer{Repo: repo, Observe: resultObserve}).Index(context.Background(), job, batchimageprovider.BindProvider(platform, providercore.CloneRecord(&providercore.Record{})))
	require.NoError(t, err)
	require.Equal(t, 3, result.TotalCount)
	require.Equal(t, 1, result.SuccessCount)
	require.Equal(t, 2, result.FailCount)
	require.Len(t, repo.items[job.BatchID], 3)
	byID := make(map[string]batchimage.CreateBatchImageItemParams)
	for _, item := range repo.items[job.BatchID] {
		byID[item.CustomID] = item
	}
	require.NotContains(t, byID, "x")
	require.Equal(t, batchimage.BatchImageItemStatusSuccess, byID["a"].Status)
	require.Equal(t, batchimage.BatchImageItemStatusFailed, byID["b"].Status)
	require.Equal(t, batchimage.BatchImageItemStatusFailed, byID["c"].Status)
	require.Equal(t, "PROVIDER_RESULT_MISSING", batchimage.BatchImageDerefString(byID["c"].ErrorCode))
	// Reconciliation restores the success+fail == item_count settlement invariant.
	require.Equal(t, job.ItemCount, result.SuccessCount+result.FailCount)
}

// TestBatchImageResultIndexer_EmptyInvalidAndDuplicateOutput feeds empty,
// malformed and duplicate-identity output into a fresh repository. Each
// expected indexing error leaves result storage empty, protecting full-scan
// validation before any replacement is published.
func TestBatchImageResultIndexer_EmptyInvalidAndDuplicateOutput(t *testing.T) {
	tests := []struct {
		name string // scenario display name
		body string // provider result input
		want error  // expected observable result
	}{
		{name: "empty", body: "\n", want: batchimage.ErrBatchImageIndexNoResultLines},
		{name: "invalid_json", body: "{bad-json}\n", want: batchimage.ErrBatchImageIndexParseFailed},
		{name: "duplicate_custom_id", body: `{"key":"dup","error":{"message":"one"}}` + "\n" + `{"key":"dup","error":{"message":"two"}}` + "\n", want: batchimage.ErrBatchImageDuplicateCustomID},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newFakeBatchImageRepository()
			_, err := (&batchimage.ResultIndexer{Repo: repo, Observe: resultObserve}).Index(context.Background(), &batchimage.BatchImageJob{BatchID: "imgbatch_bad"}, batchimageprovider.BindProvider(&fakeProcessorProvider{result: tt.body}, providercore.CloneRecord(&providercore.Record{})))
			require.ErrorIs(t, err, tt.want)
			require.Empty(t, repo.items["imgbatch_bad"])
		})
	}
}

// TestBatchImageProviderProcessor_ValidationAndTerminalCases uses terminal
// jobs and missing backend, provider or upstream-name fixtures. It checks
// terminal completion without polling and the specific validation errors
// required before provider execution can resume.
func TestBatchImageProviderProcessor_ValidationAndTerminalCases(t *testing.T) {
	ctx := context.Background()
	providerID := int64(10)
	providerJob := "providers/job"

	t.Run("terminal job returns without platform call", func(t *testing.T) {
		repo := newFakeBatchImageRepository()
		repo.jobs["imgbatch_done"] = &batchimage.BatchImageJob{BatchID: "imgbatch_done", Status: batchimage.BatchImageJobStatusFailed}
		platform := &fakeProcessorProvider{}
		got, err := newBatchProcessorFixture(repo, batchimage.NewRegistry[batchimageprovider.BatchImageProvider](platform), &fakeBatchImageProviderResolver{provider: &providercore.Record{}}, nil, nil, nil, 0).Process(ctx, "imgbatch_done")
		require.NoError(t, err)
		require.True(t, got.Terminal)
		require.False(t, platform.getCalled)
	})

	t.Run("missing platform", func(t *testing.T) {
		repo := newFakeBatchImageRepository()
		repo.jobs["imgbatch_missing_provider"] = &batchimage.BatchImageJob{BatchID: "imgbatch_missing_provider", Status: batchimage.BatchImageJobStatusSubmitted, Platform: "missing", ProviderID: &providerID, ProviderJobName: &providerJob}
		_, err := newBatchProcessorFixture(repo, batchimage.NewRegistry[batchimageprovider.BatchImageProvider](), &fakeBatchImageProviderResolver{provider: &providercore.Record{}}, nil, nil, nil, 0).Process(ctx, "imgbatch_missing_provider")
		require.ErrorIs(t, err, batchimage.ErrBatchImageUnsupportedProvider)
	})

	t.Run("missing provider id", func(t *testing.T) {
		repo := newFakeBatchImageRepository()
		repo.jobs["imgbatch_missing_provider"] = &batchimage.BatchImageJob{BatchID: "imgbatch_missing_provider", Status: batchimage.BatchImageJobStatusSubmitted, Platform: "fake", ProviderJobName: &providerJob}
		_, err := newBatchProcessorFixture(repo, batchimage.NewRegistry[batchimageprovider.BatchImageProvider](&fakeProcessorProvider{}), &fakeBatchImageProviderResolver{provider: &providercore.Record{}}, nil, nil, nil, 0).Process(ctx, "imgbatch_missing_provider")
		require.ErrorIs(t, err, batchimage.ErrBatchImageMissingProviderID)
	})

	t.Run("missing platform job name", func(t *testing.T) {
		repo := newFakeBatchImageRepository()
		repo.jobs["imgbatch_missing_name"] = &batchimage.BatchImageJob{BatchID: "imgbatch_missing_name", Status: batchimage.BatchImageJobStatusSubmitted, Platform: "fake", ProviderID: &providerID}
		_, err := newBatchProcessorFixture(repo, batchimage.NewRegistry[batchimageprovider.BatchImageProvider](&fakeProcessorProvider{}), &fakeBatchImageProviderResolver{provider: &providercore.Record{}}, nil, nil, nil, 0).Process(ctx, "imgbatch_missing_name")
		require.ErrorIs(t, err, batchimage.ErrBatchImageMissingProviderJobName)
	})
}

// TestBatchImageProviderProcessor_StatusFlow polls queued, running,
// successful, failed and cancelled provider observations. It checks retry
// intervals, stored state edges, output-reference persistence and the release
// action used after confirmed cancellation.
func TestBatchImageProviderProcessor_StatusFlow(t *testing.T) {
	ctx := context.Background()
	providerID := int64(10)
	providerJob := "providers/job"
	newJob := func(status string) *batchimage.BatchImageJob {
		return &batchimage.BatchImageJob{BatchID: "imgbatch_flow", Status: status, Platform: "fake", ProviderID: &providerID, ProviderJobName: &providerJob}
	}

	t.Run("running status updates and requeues", func(t *testing.T) {
		repo := newFakeBatchImageRepository()
		repo.jobs["imgbatch_flow"] = newJob(batchimage.BatchImageJobStatusSubmitted)
		platform := &fakeProcessorProvider{status: &batchimage.BatchProviderStatus{InternalState: batchimage.BatchProviderStateRunning, RawState: "RUNNING", SuggestedRequeueAfter: 12 * time.Second}}
		got, err := newTestBatchImageProcessor(repo, platform).Process(ctx, "imgbatch_flow")
		require.NoError(t, err)
		require.False(t, got.Terminal)
		require.Equal(t, 12*time.Second, got.RequeueAfter)
		require.Equal(t, batchimage.BatchImageJobStatusRunning, repo.jobs["imgbatch_flow"].Status)
	})

	t.Run("queued status requeues", func(t *testing.T) {
		repo := newFakeBatchImageRepository()
		repo.jobs["imgbatch_flow"] = newJob(batchimage.BatchImageJobStatusSubmitted)
		platform := &fakeProcessorProvider{status: &batchimage.BatchProviderStatus{InternalState: batchimage.BatchProviderStateQueued}}
		got, err := newTestBatchImageProcessor(repo, platform).Process(ctx, "imgbatch_flow")
		require.NoError(t, err)
		require.False(t, got.Terminal)
		require.Equal(t, batchimage.DefaultBatchImageProcessorRequeue, got.RequeueAfter)
		require.Equal(t, batchimage.BatchImageJobStatusSubmitted, repo.jobs["imgbatch_flow"].Status)
	})

	t.Run("transient platform get error requeues", func(t *testing.T) {
		repo := newFakeBatchImageRepository()
		repo.jobs["imgbatch_flow"] = newJob(batchimage.BatchImageJobStatusSubmitted)
		platform := &fakeProcessorProvider{getErr: errors.New("temporary upstream failure")}
		got, err := newTestBatchImageProcessor(repo, platform).Process(ctx, "imgbatch_flow")
		require.NoError(t, err)
		require.False(t, got.Terminal)
		require.Equal(t, time.Minute, got.RequeueAfter)
	})

	t.Run("succeeded indexes and settles from submitted", func(t *testing.T) {
		repo := newFakeBatchImageRepository()
		repo.jobs["imgbatch_flow"] = newJob(batchimage.BatchImageJobStatusSubmitted)
		platform := &fakeProcessorProvider{
			status: &batchimage.BatchProviderStatus{InternalState: batchimage.BatchProviderStateSucceeded, RawState: "SUCCEEDED", ProviderOutputRef: "files/output"},
			result: `{"key":"ok","response":{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"` + batchImageTestData + `"}}]}}]}}` + "\n",
		}
		got, err := newTestBatchImageProcessor(repo, platform).Process(ctx, "imgbatch_flow")
		require.NoError(t, err)
		require.False(t, got.Terminal)
		require.Equal(t, time.Millisecond, got.RequeueAfter)
		require.Equal(t, batchimage.BatchImageJobStatusSettling, repo.jobs["imgbatch_flow"].Status)
		require.Equal(t, "files/output", batchimage.BatchImageDerefString(repo.jobs["imgbatch_flow"].ProviderOutputRef))
		require.Equal(t, []string{batchimage.BatchImageJobStatusIndexing, batchimage.BatchImageJobStatusSettling}, repo.transitions["imgbatch_flow"])
		require.Equal(t, batchimage.BatchImageCounts{SuccessCount: 1}, repo.counts["imgbatch_flow"])
	})

	t.Run("failed platform marks job failed", func(t *testing.T) {
		repo := newFakeBatchImageRepository()
		repo.jobs["imgbatch_flow"] = newJob(batchimage.BatchImageJobStatusRunning)
		platform := &fakeProcessorProvider{status: &batchimage.BatchProviderStatus{InternalState: batchimage.BatchProviderStateFailed, RawState: "FAILED", ErrorCode: "BAD_PROMPT", ErrorMessage: "bad prompt"}}
		got, err := newTestBatchImageProcessor(repo, platform).Process(ctx, "imgbatch_flow")
		require.NoError(t, err)
		require.True(t, got.Terminal)
		require.Equal(t, batchimage.BatchImageJobStatusFailed, repo.jobs["imgbatch_flow"].Status)
		require.Equal(t, "BAD_PROMPT", batchimage.BatchImageDerefString(repo.jobs["imgbatch_flow"].LastErrorCode))
	})

	t.Run("cancelled platform marks job cancelled", func(t *testing.T) {
		repo := newFakeBatchImageRepository()
		repo.jobs["imgbatch_flow"] = newJob(batchimage.BatchImageJobStatusRunning)
		apiKeyID := int64(22)
		holdAmount := 0.5
		repo.jobs["imgbatch_flow"].UserID = 11
		repo.jobs["imgbatch_flow"].APIKeyID = &apiKeyID
		repo.jobs["imgbatch_flow"].EstimatedCost = holdAmount
		repo.jobs["imgbatch_flow"].HoldAmount = &holdAmount
		platform := &fakeProcessorProvider{status: &batchimage.BatchProviderStatus{InternalState: batchimage.BatchProviderStateCancelled, RawState: "CANCELLED"}}
		processor := newTestBatchImageProcessor(repo, platform)
		billing := &fakeBatchImageBillingRepo{}
		processor.Funding = nativeTaskFundingFixture(billing)
		got, err := processor.Process(ctx, "imgbatch_flow")
		require.NoError(t, err)
		require.True(t, got.Terminal)
		require.Equal(t, batchimage.BatchImageJobStatusCancelled, repo.jobs["imgbatch_flow"].Status)
		require.Len(t, billing.releases, 1)
		require.Equal(t, batchimage.BatchImageReleaseRequestID("imgbatch_flow"), billing.releases[0].RequestID)
	})
}

// TestCanTransitionBatchImageJob_PR5DirectIndexing checks status edges for a
// provider already complete at the first observation. Submission may enter
// indexing or failure directly, and indexing retains its failure transition.
func TestCanTransitionBatchImageJob_PR5DirectIndexing(t *testing.T) {
	require.True(t, batchimage.CanTransitionBatchImageJob(batchimage.BatchImageJobStatusSubmitted, batchimage.BatchImageJobStatusIndexing))
	require.True(t, batchimage.CanTransitionBatchImageJob(batchimage.BatchImageJobStatusSubmitted, batchimage.BatchImageJobStatusFailed))
	require.True(t, batchimage.CanTransitionBatchImageJob(batchimage.BatchImageJobStatusIndexing, batchimage.BatchImageJobStatusFailed))
}

// newTestBatchImageProcessor binds a fake polling backend to the supplied
// repository and an explicit indexer. The fixture uses production
// ResultAccess resolution while leaving funding absent unless a test injects
// its store.
func newTestBatchImageProcessor(repo *fakeBatchImageRepository, platform *fakeProcessorProvider) *batchimage.ProviderProcessor {
	return newBatchProcessorFixture(repo,
		batchimage.NewRegistry[batchimageprovider.BatchImageProvider](platform),
		&fakeBatchImageProviderResolver{provider: &providercore.Record{}},
		&batchimage.ResultIndexer{Repo: repo, Observe: resultObserve}, nil, nil, 0)
}
