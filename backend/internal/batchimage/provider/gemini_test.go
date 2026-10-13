package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/batchimage"
	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	testassert "github.com/TokenFlux/TokenRouter/internal/testutil/assertion"
)

// Gemini fixtures record file and batch calls while supplying chosen responses
// or client failures. Tests inspect JSONL item identity and references, returned
// provider resource metadata, state mapping, and public messages under the
// specific response shapes and error cases they construct.

// fakeGeminiBatchClient records sequential Gemini operations and supplies
// chosen responses or failures for provider tests.
type fakeGeminiBatchClient struct {
	// calls is mutated by fake methods in invocation order for test assertions.
	// Tests use this fixture sequentially; concurrent operations would require
	// synchronizing this slice and the remaining observations.
	calls []string // observed sequential client operations
	// uploaded is returned directly when present after uploadErr is checked.
	// A nil value selects the fixed uploaded-file response; tests leave the chosen
	// object stable during provider use.
	uploaded *GeminiUploadedFile // configured upload file response
	// created is returned directly when present after createErr is checked.
	// A nil value selects the fixed pending-job response; tests leave the chosen
	// object stable during provider use.
	created *GeminiBatchJob // configured batch creation response
	// got is returned directly after getErr is checked, including when nil.
	// Tests keep that configured observation stable while the provider interprets
	// its selected response shape.
	got *GeminiBatchJob // configured polling job observation
	// uploadErr, createErr, getErr, cancelErr, downloadErr, and deleteErr supply
	// their matching fake operation failures. Each operation records its available
	// observations before returning its configured error.
	uploadErr      error  // injected upload operation error
	createErr      error  // injected creation operation error
	getErr         error  // injected polling operation error
	cancelErr      error  // injected cancellation operation error
	downloadErr    error  // injected download operation error
	deleteErr      error  // injected deletion operation error
	uploadedJSONL  []byte // collected upload input bytes
	createdFile    string // observed creation input file
	cancelledBatch string // observed cancellation job name
	downloadedFile string // observed downloaded file name
	downloadBody   string // configured result body text
	// downloadContentType supplies the result MIME label when nonempty.
	// DownloadFile replaces an empty value with application/octet-stream for its
	// new fixture reader.
	downloadContentType string   // configured download MIME label
	deletedFiles        []string // observed deletion file names
}

// TestBatchImageProviderRegistry_ReturnsGeminiAPI resolves the Gemini adapter
// through ordinary and required registry lookup. Both return the same adapter,
// while an unknown registry name returns the invalid-provider error.
func TestBatchImageProviderRegistry_ReturnsGeminiAPI(t *testing.T) {
	registry := newBatchProviderRegistryForTest()
	platform, ok := registry.Get(batchimage.BatchImageProviderGeminiAPI)
	require.True(t, ok)
	require.Equal(t, batchimage.BatchImageProviderGeminiAPI, platform.Name())

	must, err := registry.MustGet(batchimage.BatchImageProviderGeminiAPI)
	require.NoError(t, err)
	require.Same(t, platform, must)

	_, err = registry.MustGet("unknown_provider")
	require.ErrorIs(t, err, batchimage.ErrBatchImageInvalidProvider)
}

// TestGeminiProvider_SupportsOnlyGeminiAPIKeyWithSecret compares credential
// shapes across supported and unsupported records. A Gemini API key with a
// secret qualifies, while blank keys, OAuth, another platform, and nil fail.
func TestGeminiProvider_SupportsOnlyGeminiAPIKeyWithSecret(t *testing.T) {
	platform := NewGeminiAPIBatchImageProvider(&fakeGeminiBatchClient{})

	require.True(t, platform.SupportsProvider(geminiAPIKeyProvider("sk-gemini")))
	require.False(t, platform.SupportsProvider(&providercore.Record{Platform: capability.PlatformGemini, Type: capability.ProviderTypeAPIKey, Credentials: map[string]any{}}))
	require.False(t, platform.SupportsProvider(&providercore.Record{Platform: capability.PlatformGemini, Type: capability.ProviderTypeOAuth, Credentials: map[string]any{"api_key": "sk"}}))
	require.False(t, platform.SupportsProvider(&providercore.Record{Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey, Credentials: map[string]any{"api_key": "sk"}}))
	require.False(t, platform.SupportsProvider(nil))
}

// TestGeminiProvider_MissingAPIKeyRejected submits a Gemini API-key record
// whose secret is absent. The provider returns the missing-key application
// error before the fake client can upload its input.
func TestGeminiProvider_MissingAPIKeyRejected(t *testing.T) {
	platform := NewGeminiAPIBatchImageProvider(&fakeGeminiBatchClient{})
	_, err := platform.Submit(context.Background(), nil, &providercore.Record{Platform: capability.PlatformGemini, Type: capability.ProviderTypeAPIKey}, validGeminiBatchInput())
	require.ErrorIs(t, err, batchimage.ErrBatchImageProviderMissingAPIKey)
}

// TestBuildGeminiBatchJSONL_WritesValidLinesAndPreservesCustomID encodes two
// items with different keys and prompts. Each decoded line retains its expected
// key and prompt and requests text plus image output.
func TestBuildGeminiBatchJSONL_WritesValidLinesAndPreservesCustomID(t *testing.T) {
	input := validGeminiBatchInput()
	input.Items = append(input.Items, batchimage.BatchImageInputItem{CustomID: "cover_002", Prompt: "Second prompt"})

	jsonl, err := BuildGeminiBatchJSONL(input)
	require.NoError(t, err)

	lines := strings.Split(strings.TrimSpace(string(jsonl)), "\n")
	require.Len(t, lines, 2)
	requireJSONLLine(t, lines[0], "cover_001", "A clean product hero image")
	requireJSONLLine(t, lines[1], "cover_002", "Second prompt")
}

// TestBuildGeminiBatchJSONL_RejectsDuplicateCustomIDs appends a second item
// using the first key. Encoding returns the provider-input error, protecting
// result matching from competing requests with the same custom ID.
func TestBuildGeminiBatchJSONL_RejectsDuplicateCustomIDs(t *testing.T) {
	input := validGeminiBatchInput()
	input.Items = append(input.Items, batchimage.BatchImageInputItem{CustomID: "cover_001", Prompt: "Duplicate"})

	_, err := BuildGeminiBatchJSONL(input)
	require.ErrorIs(t, err, batchimage.ErrBatchImageProviderInvalidInput)
}

// TestBuildGeminiBatchJSONL_RejectsEmptyPrompt replaces an item's prompt with
// whitespace. Encoding returns the provider-input error after normalization,
// preventing submission of the tested empty image-generation request.
func TestBuildGeminiBatchJSONL_RejectsEmptyPrompt(t *testing.T) {
	input := validGeminiBatchInput()
	input.Items[0].Prompt = " "

	_, err := BuildGeminiBatchJSONL(input)
	require.ErrorIs(t, err, batchimage.ErrBatchImageProviderInvalidInput)
}

// TestBuildGeminiBatchJSONL_WritesReferenceImages encodes inline WebP bytes
// and a JPEG file URI. The decoded request keeps prompt-first part order,
// expected MIME labels, base64 bytes, and the supplied file URI.
func TestBuildGeminiBatchJSONL_WritesReferenceImages(t *testing.T) {
	input := validGeminiBatchInput()
	input.Items[0].ReferenceImages = []batchimage.BatchImageReference{
		{MimeType: "image/webp", Data: []byte("webp-bytes")},
		{MimeType: "image/jpeg", FileURI: "gs://bucket/refs/style.jpg"},
	}

	jsonl, err := BuildGeminiBatchJSONL(input)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(jsonl)), "\n")
	require.Len(t, lines, 1)

	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &got))
	request := testassert.MustType[map[string]any](got["request"])
	contents := testassert.MustType[[]any](request["contents"])
	parts := testassert.MustType[[]any](testassert.MustType[map[string]any](contents[0])["parts"])
	require.Len(t, parts, 3)
	require.Equal(t, "A clean product hero image", testassert.MustType[map[string]any](parts[0])["text"])
	inlineData := testassert.MustType[map[string]any](testassert.MustType[map[string]any](parts[1])["inlineData"])
	require.Equal(t, "image/webp", inlineData["mimeType"])
	require.Equal(t, "d2VicC1ieXRlcw==", inlineData["data"])
	fileData := testassert.MustType[map[string]any](testassert.MustType[map[string]any](parts[2])["fileData"])
	require.Equal(t, "image/jpeg", fileData["mimeType"])
	require.Equal(t, "gs://bucket/refs/style.jpg", fileData["fileUri"])
}

// TestGeminiProvider_SubmitUploadsJSONLThenCreatesBatch supplies successful
// file and job responses. It checks upload-before-create call order, returned
// resource names, an absent output reference, and omission of the API key
// from uploaded JSONL.
func TestGeminiProvider_SubmitUploadsJSONLThenCreatesBatch(t *testing.T) {
	client := &fakeGeminiBatchClient{
		uploaded: &GeminiUploadedFile{Name: "files/input-jsonl"},
		created:  &GeminiBatchJob{Name: "batches/job-123", State: "JOB_STATE_PENDING"},
	}
	platform := NewGeminiAPIBatchImageProvider(client)

	got, err := platform.Submit(context.Background(), &batchimage.BatchImageJob{BatchID: "imgbatch_123", Model: "gemini-3.1-flash-image"}, geminiAPIKeyProvider("sk-secret"), validGeminiBatchInput())
	require.NoError(t, err)
	require.Equal(t, []string{"upload", "create"}, client.calls)
	require.Equal(t, "files/input-jsonl", got.ProviderInputRef)
	require.Equal(t, "batches/job-123", got.ProviderJobName)
	require.Empty(t, got.ProviderOutputRef)
	require.NotContains(t, got.ProviderInputRef, "A clean product hero image")
	require.NotContains(t, string(client.uploadedJSONL), "sk-secret")
}

// TestGeminiProvider_GetMapsStates supplies running and supported terminal job
// observations. The returned internal state, Done flag, output file, and error
// code match each constructed case, with the test secret absent from messages.
func TestGeminiProvider_GetMapsStates(t *testing.T) {
	tests := []struct {
		name      string                                // case identification for subtests
		job       *GeminiBatchJob                       // configured upstream job observation
		wantState batchimage.BatchProviderInternalState // expected processor internal state
		wantDone  bool                                  // expected terminal observation flag
		wantRef   string                                // expected output file reference
		wantCode  string                                // expected public failure code
	}{
		{name: "running", job: &GeminiBatchJob{Name: "batches/1", State: "JOB_STATE_RUNNING"}, wantState: batchimage.BatchProviderStateRunning},
		{name: "succeeded_dest_fileName", job: &GeminiBatchJob{Name: "batches/1", State: "JOB_STATE_SUCCEEDED", Dest: &GeminiBatchDest{FileName: "files/out"}}, wantState: batchimage.BatchProviderStateSucceeded, wantDone: true, wantRef: "files/out"},
		{name: "failed", job: &GeminiBatchJob{Name: "batches/1", State: "JOB_STATE_FAILED", Error: &GeminiBatchError{Code: "BAD_PROMPT", Message: "bad prompt"}}, wantState: batchimage.BatchProviderStateFailed, wantDone: true, wantCode: "BAD_PROMPT"},
		{name: "cancelled", job: &GeminiBatchJob{Name: "batches/1", State: "JOB_STATE_CANCELLED"}, wantState: batchimage.BatchProviderStateCancelled, wantDone: true, wantCode: "GEMINI_BATCH_CANCELLED"},
		{name: "expired", job: &GeminiBatchJob{Name: "batches/1", State: "JOB_STATE_EXPIRED"}, wantState: batchimage.BatchProviderStateExpired, wantDone: true, wantCode: "GEMINI_BATCH_EXPIRED"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			platform := NewGeminiAPIBatchImageProvider(&fakeGeminiBatchClient{got: tt.job})
			got, err := platform.Get(context.Background(), jobWithProviderName("batches/1"), geminiAPIKeyProvider("sk-secret"))
			require.NoError(t, err)
			require.Equal(t, tt.wantState, got.InternalState)
			require.Equal(t, tt.wantDone, got.Done)
			require.Equal(t, tt.wantRef, got.ProviderOutputRef)
			require.Equal(t, tt.wantCode, got.ErrorCode)
			require.NotContains(t, got.ErrorMessage, "sk-secret")
		})
	}
}

// TestGeminiProvider_GetExtractsResponsesFileReference supplies a succeeded job
// whose result name appears under response.responsesFile. Polling keeps
// successful state and returns that reference for file-based indexing.
func TestGeminiProvider_GetExtractsResponsesFileReference(t *testing.T) {
	platform := NewGeminiAPIBatchImageProvider(&fakeGeminiBatchClient{
		got: &GeminiBatchJob{
			Name:     "batches/1",
			State:    "JOB_STATE_SUCCEEDED",
			Response: &GeminiBatchResponse{ResponsesFile: "files/responses-jsonl"},
		},
	})

	got, err := platform.Get(context.Background(), jobWithProviderName("batches/1"), geminiAPIKeyProvider("sk-secret"))
	require.NoError(t, err)
	require.Equal(t, batchimage.BatchProviderStateSucceeded, got.InternalState)
	require.Equal(t, "files/responses-jsonl", got.ProviderOutputRef)
}

// TestGeminiProvider_GetRejectsInlineResultShape supplies succeeded inline
// responses. Polling returns the inline-result-unsupported application error,
// protecting the processor's requirement for a downloadable JSONL result file.
func TestGeminiProvider_GetRejectsInlineResultShape(t *testing.T) {
	platform := NewGeminiAPIBatchImageProvider(&fakeGeminiBatchClient{
		got: &GeminiBatchJob{
			Name:     "batches/1",
			State:    "JOB_STATE_SUCCEEDED",
			Response: &GeminiBatchResponse{InlinedResponses: []any{map[string]any{"response": "large"}}},
		},
	})

	_, err := platform.Get(context.Background(), jobWithProviderName("batches/1"), geminiAPIKeyProvider("sk-secret"))
	require.ErrorIs(t, err, batchimage.ErrBatchImageProviderInlineResultUnsupported)
}

// TestGeminiProvider_OpenResultStreamsResultFile opens a stored output reference
// through a fake download. Reading the returned stream finds the expected bytes
// and MIME type, while the fake observes that exact file name.
func TestGeminiProvider_OpenResultStreamsResultFile(t *testing.T) {
	client := &fakeGeminiBatchClient{downloadBody: "line1\n", downloadContentType: "application/jsonl"}
	platform := NewGeminiAPIBatchImageProvider(client)

	outputRef := "files/output-jsonl"
	r, contentType, err := platform.OpenResult(context.Background(), &batchimage.BatchImageJob{ProviderOutputRef: &outputRef}, geminiAPIKeyProvider("sk-secret"))
	require.NoError(t, err)
	defer func() { require.NoError(t, r.Close()) }()

	body, err := io.ReadAll(r)
	require.NoError(t, err)
	require.Equal(t, "line1\n", string(body))
	require.Equal(t, "application/jsonl", contentType)
	require.Equal(t, "files/output-jsonl", client.downloadedFile)
}

// TestGeminiProvider_CancelCallsClient cancels a job with a stored provider name.
// The call succeeds and the fake records that name, checking cancellation is
// directed at the bound upstream batch.
func TestGeminiProvider_CancelCallsClient(t *testing.T) {
	client := &fakeGeminiBatchClient{}
	platform := NewGeminiAPIBatchImageProvider(client)

	require.NoError(t, platform.Cancel(context.Background(), jobWithProviderName("batches/1"), geminiAPIKeyProvider("sk-secret")))
	require.Equal(t, "batches/1", client.cancelledBatch)
}

// TestGeminiProvider_CleanupDeletesRefsOnlyWhenPresent cleans both populated
// references and then an empty job. The fake records input before output and
// receives no additional deletion from the empty-reference cleanup.
func TestGeminiProvider_CleanupDeletesRefsOnlyWhenPresent(t *testing.T) {
	inputRef := "files/input"
	outputRef := "files/output"
	client := &fakeGeminiBatchClient{}
	platform := NewGeminiAPIBatchImageProvider(client)

	err := platform.Cleanup(context.Background(), &batchimage.BatchImageJob{ProviderInputRef: &inputRef, ProviderOutputRef: &outputRef}, geminiAPIKeyProvider("sk-secret"), batchimage.CleanupTargetAll)
	require.NoError(t, err)
	require.Equal(t, []string{"files/input", "files/output"}, client.deletedFiles)

	err = platform.Cleanup(context.Background(), &batchimage.BatchImageJob{}, geminiAPIKeyProvider("sk-secret"), batchimage.CleanupTargetAll)
	require.NoError(t, err)
	require.Equal(t, []string{"files/input", "files/output"}, client.deletedFiles)
}

// TestGeminiProvider_ErrorsDoNotExposeAPIKey injects a 401 upload error containing
// the supplied secret. Submission returns the authentication reason with fixed
// text whose error string omits that API key.
func TestGeminiProvider_ErrorsDoNotExposeAPIKey(t *testing.T) {
	apiKey := "sk-top-secret"
	client := &fakeGeminiBatchClient{uploadErr: &GeminiAPIError{StatusCode: 401, Message: "upstream body should be hidden " + apiKey}}
	platform := NewGeminiAPIBatchImageProvider(client)

	_, err := platform.Submit(context.Background(), nil, geminiAPIKeyProvider(apiKey), validGeminiBatchInput())
	require.Error(t, err)
	require.Equal(t, "GEMINI_AUTH_FAILED", apperror.Reason(err))
	require.NotContains(t, err.Error(), apiKey)
}

// TestGeminiProvider_MetadataDoesNotStoreImageBytesOrBase64 submits the fixture
// prompt with fixed file and job resources. Returned metadata omits the tested
// base64 markers and prompt text, keeping those resource references distinct
// from encoded input content.
func TestGeminiProvider_MetadataDoesNotStoreImageBytesOrBase64(t *testing.T) {
	client := &fakeGeminiBatchClient{
		uploaded: &GeminiUploadedFile{Name: "files/input-jsonl"},
		created:  &GeminiBatchJob{Name: "batches/job-123", State: "JOB_STATE_PENDING"},
	}
	platform := NewGeminiAPIBatchImageProvider(client)

	got, err := platform.Submit(context.Background(), nil, geminiAPIKeyProvider("sk-secret"), validGeminiBatchInput())
	require.NoError(t, err)
	require.NotContains(t, got.ProviderJobName, "base64")
	require.NotContains(t, got.ProviderInputRef, "base64")
	require.NotContains(t, got.ProviderOutputRef, "base64")
	require.NotContains(t, got.ProviderJobName+got.ProviderInputRef+got.ProviderOutputRef, "iVBOR")
	require.NotContains(t, got.ProviderJobName+got.ProviderInputRef+got.ProviderOutputRef, "A clean product hero image")
}

// requireJSONLLine decodes one Gemini line and checks its key, prompt, and
// text-plus-image modalities. It marks itself as a testing helper so failed
// assertions identify the test that requested that line check.
func requireJSONLLine(t *testing.T, line, wantKey, wantPrompt string) {
	t.Helper()
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(line), &got))
	require.Equal(t, wantKey, got["key"])
	request := testassert.MustType[map[string]any](got["request"])
	config := testassert.MustType[map[string]any](request["generationConfig"])
	require.Equal(t, []any{"TEXT", "IMAGE"}, config["responseModalities"])
	contents := testassert.MustType[[]any](request["contents"])
	parts := testassert.MustType[[]any](testassert.MustType[map[string]any](contents[0])["parts"])
	require.Equal(t, wantPrompt, testassert.MustType[map[string]any](parts[0])["text"])
}

// validGeminiBatchInput returns one valid keyed Gemini image-request fixture.
func validGeminiBatchInput() batchimage.BatchImageInput {
	return batchimage.BatchImageInput{
		BatchID:     "imgbatch_123",
		Model:       "gemini-3.1-flash-image",
		DisplayName: "test batch",
		Items: []batchimage.BatchImageInputItem{{
			CustomID: "cover_001",
			Prompt:   "A clean product hero image",
		}},
	}
}

// geminiAPIKeyProvider returns a Gemini record with the supplied API-key
// credential for tests.
func geminiAPIKeyProvider(apiKey string) *providercore.Record {
	return &providercore.Record{
		Platform:    capability.PlatformGemini,
		Type:        capability.ProviderTypeAPIKey,
		Credentials: map[string]any{"api_key": apiKey},
	}
}

// jobWithProviderName returns fixture job metadata pointing to the supplied
// provider name.
func jobWithProviderName(name string) *batchimage.BatchImageJob {
	return &batchimage.BatchImageJob{ProviderJobName: &name}
}

// UploadJSONL rejects a blank API key, then records the call and collected
// input bytes. An injected error takes precedence over a supplied file response;
// absent responses select a fixed file fixture, and reader errors are ignored.
func (f *fakeGeminiBatchClient) UploadJSONL(_ context.Context, apiKey string, _ string, r io.Reader) (*GeminiUploadedFile, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, errors.New("missing api key")
	}
	f.calls = append(f.calls, "upload")
	// Capture whatever input bytes the reader supplies for assertions, even when
	// reading fails. This fake's injected uploadErr controls the simulated operation
	// result, so reader errors are deliberately discarded.
	f.uploadedJSONL, _ = io.ReadAll(r)
	if f.uploadErr != nil {
		return nil, f.uploadErr
	}
	// A configured response exposes the shape chosen by its test.
	// When absent, use the valid fixed resource so tests focused on later phases
	// can submit through this fake.
	if f.uploaded != nil {
		return f.uploaded, nil
	}
	return &GeminiUploadedFile{Name: "files/input-jsonl"}, nil
}

// CreateBatch records the call and input file name before returning its
// injected error or chosen response. An absent chosen response supplies the
// fixed pending job used by ordinary submission tests.
func (f *fakeGeminiBatchClient) CreateBatch(_ context.Context, _ string, _ string, fileName string, _ string) (*GeminiBatchJob, error) {
	f.calls = append(f.calls, "create")
	f.createdFile = fileName
	if f.createErr != nil {
		return nil, f.createErr
	}
	// A configured creation response exposes the test's selected job shape.
	// When absent, use a pending named job so ordinary submission cases proceed
	// with valid upstream identity.
	if f.created != nil {
		return f.created, nil
	}
	return &GeminiBatchJob{Name: "batches/job-123", State: "JOB_STATE_PENDING"}, nil
}

// GetBatch records polling and returns its injected error or chosen observation.
// A nil chosen observation returns nil successfully, letting provider validation
// exercise an empty-response case when a test configures it.
func (f *fakeGeminiBatchClient) GetBatch(_ context.Context, _ string, _ string) (*GeminiBatchJob, error) {
	f.calls = append(f.calls, "get")
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.got, nil
}

// CancelBatch records cancellation and the requested batch name before
// returning the configured error. A nil error reports successful fake request
// completion; it does not mutate the configured polling response.
func (f *fakeGeminiBatchClient) CancelBatch(_ context.Context, _ string, batchName string) error {
	f.calls = append(f.calls, "cancel")
	f.cancelledBatch = batchName
	return f.cancelErr
}

// DownloadFile records the requested file and returns its injected error when
// present. Otherwise it opens a new caller-closed body from downloadBody,
// using application/octet-stream when the configured content type is empty.
func (f *fakeGeminiBatchClient) DownloadFile(_ context.Context, _ string, fileName string) (io.ReadCloser, string, error) {
	f.calls = append(f.calls, "download")
	f.downloadedFile = fileName
	if f.downloadErr != nil {
		return nil, "", f.downloadErr
	}
	contentType := f.downloadContentType
	// An empty fixture MIME label selects application/octet-stream so successful
	// fake downloads still provide a usable content-type observation with their
	// configured response body bytes.
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	return io.NopCloser(bytes.NewBufferString(f.downloadBody)), contentType, nil
}

// DeleteFile records a deletion call and appends its supplied file name before
// returning the configured error. Failed fake deletions remain visible in the
// call and reference observations for assertions.
func (f *fakeGeminiBatchClient) DeleteFile(_ context.Context, _ string, fileName string) error {
	f.calls = append(f.calls, "delete")
	f.deletedFiles = append(f.deletedFiles, fileName)
	return f.deleteErr
}
