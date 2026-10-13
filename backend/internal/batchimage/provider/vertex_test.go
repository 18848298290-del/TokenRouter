package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/batchimage"
	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	testassert "github.com/TokenFlux/TokenRouter/internal/testutil/assertion"
	"github.com/TokenFlux/TokenRouter/internal/upstream/vertex"
)

// Vertex fixtures supply a cached access token and chosen job or object-store
// responses, keeping provider tests focused on managed references and result
// handling. Assertions inspect JSONL identity, submitted request shape, observed
// state, sorted combined output, and deletion decisions for the constructed
// stored paths.

// fakeVertexBatchClient records submitted requests and cancellation names
// while supplying chosen job responses or errors.
type fakeVertexBatchClient struct {
	// created is returned directly after createErr is checked.
	// A nil value supplies a fixed pending job; tests keep the chosen observation
	// stable while the provider reads it.
	created *VertexBatchPredictionJob // configured job creation response
	// got is returned directly after getErr is checked, including when nil.
	// Tests keep the selected observation stable during polling so its chosen
	// state and output fields stay consistent.
	got *VertexBatchPredictionJob // configured polling job observation
	// createErr, getErr, and cancelErr select their matching fake operation
	// failures. Creation and cancellation retain their observed inputs before
	// returning configured errors, allowing assertions to inspect attempted work.
	createErr error // injected job creation error
	getErr    error // injected job polling error
	cancelErr error // injected job cancellation error
	// createdReq stores the request by value after token validation.
	// Its nested reference fields remain shared with that request for the later
	// serialization assertions.
	createdReq    VertexCreateBatchPredictionJobRequest // last captured creation request
	cancelledName string                                // last captured cancellation name
}

// fakeVertexObjectStore records managed-object operations and supplies
// configured JSONL listings, bodies, and failures.
type fakeVertexObjectStore struct {
	// uploadURI and uploadedJSONL retain the attempted managed upload before
	// uploadErr is returned. UploadJSONL collects the reader's available bytes for
	// assertions even if reading reports an error.
	uploadURI     string // last captured upload URI
	uploadedJSONL []byte // last collected upload bytes
	uploadErr     error  // injected object upload error
	// listed supplies fixture object names in their original order.
	// ListJSONLObjects returns a filtered new slice so the provider can sort its
	// result independently of this configured list.
	listed []string // configured result object names
	// objects supplies body text keyed by requested URI. OpenObject reads it
	// when opening a new stream, and absent entries produce empty JSONL bodies for
	// fixture assertions.
	objects map[string]string // configured object body lookup
	// listErr, openErr, and deleteErr select listing, opening, and deletion
	// outcomes. Listing and opening errors precede successful results, while
	// deletion errors follow recording the requested cleanup reference.
	listErr         error    // injected object listing error
	openErr         error    // injected object opening error
	deleteErr       error    // injected object deletion error
	deletedObjects  []string // observed object deletion URIs
	deletedPrefixes []string // observed prefix deletion URIs
}

// fakeGeminiTokenCache supplies one configured access token for provider
// fixtures that bypass actual token exchange.
type fakeGeminiTokenCache struct {
	// token is returned unchanged when its trimmed value is nonempty.
	// Provider fixtures supply a known token here; blank or whitespace-only
	// configuration makes GetAccessToken report an error.
	token string // configured shared access token
}

// TestBatchImageProviderRegistry_ReturnsVertex resolves the Vertex adapter by
// its registered batch identity. The returned adapter reports that same name,
// checking registry construction includes the supported platform.
func TestBatchImageProviderRegistry_ReturnsVertex(t *testing.T) {
	registry := newBatchProviderRegistryForTest()
	platform, ok := registry.Get(batchimage.BatchImageProviderVertex)
	require.True(t, ok)
	require.Equal(t, batchimage.BatchImageProviderVertex, platform.Name())
}

// TestVertexProvider_SupportsOnlyGeminiServiceAccount compares platform and
// credential shapes. A complete Gemini service-account fixture qualifies, while
// API keys, OAuth, another platform, and absent service-account fields fail.
func TestVertexProvider_SupportsOnlyGeminiServiceAccount(t *testing.T) {
	platform := newTestVertexProvider(&fakeVertexBatchClient{}, &fakeVertexObjectStore{})

	require.True(t, platform.SupportsProvider(vertexServiceAccount()))
	require.False(t, platform.SupportsProvider(&providercore.Record{Platform: capability.PlatformGemini, Type: capability.ProviderTypeAPIKey, Credentials: map[string]any{"api_key": "sk"}}))
	require.False(t, platform.SupportsProvider(&providercore.Record{Platform: capability.PlatformGemini, Type: capability.ProviderTypeOAuth, Credentials: map[string]any{"access_token": "tok"}}))
	require.False(t, platform.SupportsProvider(&providercore.Record{Platform: capability.PlatformAnthropic, Type: capability.ProviderTypeServiceAccount, Credentials: vertexServiceAccount().Credentials}))
	require.False(t, platform.SupportsProvider(&providercore.Record{Platform: capability.PlatformGemini, Type: capability.ProviderTypeServiceAccount, Credentials: map[string]any{}}))
}

// TestVertexProvider_MissingServiceAccountRejected submits an empty Gemini
// service-account record. The provider returns the missing-service-account
// error, protecting submission from attempting object or job operations with
// that malformed credential shape.
func TestVertexProvider_MissingServiceAccountRejected(t *testing.T) {
	platform := newTestVertexProvider(&fakeVertexBatchClient{}, &fakeVertexObjectStore{})
	_, err := platform.Submit(context.Background(), nil, &providercore.Record{Platform: capability.PlatformGemini, Type: capability.ProviderTypeServiceAccount, Credentials: map[string]any{}}, validVertexBatchInput())
	require.ErrorIs(t, err, batchimage.ErrBatchImageProviderMissingServiceAccount)
}

// TestVertexProvider_MissingManagedGCSBucketRejected supplies valid credential
// fields and no managed bucket. Submission returns the missing-bucket reason
// before the fake store can upload input for the job.
func TestVertexProvider_MissingManagedGCSBucketRejected(t *testing.T) {
	platform := NewVertexBatchImageProvider(VertexBatchImageProviderOptions{ProjectID: "proj", Environment: "test"}, &fakeVertexBatchClient{}, &fakeVertexObjectStore{}, &fakeGeminiTokenCache{token: "token"})
	_, err := platform.Submit(context.Background(), nil, vertexServiceAccount(), validVertexBatchInput())
	require.Error(t, err)
	require.Equal(t, "VERTEX_MANAGED_GCS_BUCKET_MISSING", apperror.Reason(err))
}

// TestBuildVertexBatchJSONL_WritesValidLinesAndPreservesCustomID encodes two
// items with distinct custom IDs and prompts. Decoded lines retain both
// identities, user-role contents, and the requested text-plus-image modalities.
func TestBuildVertexBatchJSONL_WritesValidLinesAndPreservesCustomID(t *testing.T) {
	input := validVertexBatchInput()
	input.Items = append(input.Items, batchimage.BatchImageInputItem{CustomID: "cover_002", Prompt: "Second prompt"})

	jsonl, err := BuildVertexBatchJSONL(input)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(jsonl)), "\n")
	require.Len(t, lines, 2)
	requireVertexJSONLLine(t, lines[0], "cover_001", "A clean product hero image")
	requireVertexJSONLLine(t, lines[1], "cover_002", "Second prompt")
}

// TestBuildVertexBatchJSONL_RejectsDuplicateCustomIDs appends another request
// with the first item's key. Encoding returns the provider-input error,
// protecting result matching from duplicate request identities within the batch.
func TestBuildVertexBatchJSONL_RejectsDuplicateCustomIDs(t *testing.T) {
	input := validVertexBatchInput()
	input.Items = append(input.Items, batchimage.BatchImageInputItem{CustomID: "cover_001", Prompt: "Duplicate"})
	_, err := BuildVertexBatchJSONL(input)
	require.ErrorIs(t, err, batchimage.ErrBatchImageProviderInvalidInput)
}

// TestBuildVertexBatchJSONL_RejectsEmptyPrompt changes one prompt to whitespace.
// Encoding returns the provider-input error after trimming, preventing the
// tested empty request from becoming a Vertex JSONL line.
func TestBuildVertexBatchJSONL_RejectsEmptyPrompt(t *testing.T) {
	input := validVertexBatchInput()
	input.Items[0].Prompt = " "
	_, err := BuildVertexBatchJSONL(input)
	require.ErrorIs(t, err, batchimage.ErrBatchImageProviderInvalidInput)
}

// TestBuildVertexBatchJSONL_WritesReferenceImages encodes inline PNG bytes and
// a JPEG GCS URI. The decoded parts retain prompt-first order, expected MIME
// labels, base64 content, and the supplied file reference.
func TestBuildVertexBatchJSONL_WritesReferenceImages(t *testing.T) {
	input := validVertexBatchInput()
	input.Items[0].ReferenceImages = []batchimage.BatchImageReference{
		{MimeType: "image/png", Data: []byte("png-bytes")},
		{MimeType: "image/jpeg", FileURI: "gs://bucket/refs/style.jpg"},
	}

	jsonl, err := BuildVertexBatchJSONL(input)
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
	require.Equal(t, "image/png", inlineData["mimeType"])
	require.Equal(t, "cG5nLWJ5dGVz", inlineData["data"])
	fileData := testassert.MustType[map[string]any](testassert.MustType[map[string]any](parts[2])["fileData"])
	require.Equal(t, "image/jpeg", fileData["mimeType"])
	require.Equal(t, "gs://bucket/refs/style.jpg", fileData["fileUri"])
}

// TestNormalizeVertexBatchModelPath compares a bare model with publisher and
// project resources. The bare name gains the Google publisher prefix, while
// the two supplied resource paths retain their expected forms.
func TestNormalizeVertexBatchModelPath(t *testing.T) {
	require.Equal(t, "publishers/google/models/gemini-3.1-flash-image", NormalizeVertexBatchModelPath("gemini-3.1-flash-image"))
	require.Equal(t, "publishers/google/models/gemini-2.5-flash-image", NormalizeVertexBatchModelPath("publishers/google/models/gemini-2.5-flash-image"))
	require.Equal(t, "projects/p/locations/global/models/m", NormalizeVertexBatchModelPath("projects/p/locations/global/models/m"))
}

// TestBuildVertexBatchPredictionJobsEndpoint constructs global and regional
// job endpoints. The global location uses the shared API host, while the
// regional location appears in both its host and resource path.
func TestBuildVertexBatchPredictionJobsEndpoint(t *testing.T) {
	global, err := vertex.BuildVertexBatchPredictionJobsEndpoint("", "my-project", "global")
	require.NoError(t, err)
	require.Equal(t, "https://aiplatform.googleapis.com/v1/projects/my-project/locations/global/batchPredictionJobs", global)

	regional, err := vertex.BuildVertexBatchPredictionJobsEndpoint("", "my-project", "asia-northeast1")
	require.NoError(t, err)
	require.Equal(t, "https://asia-northeast1-aiplatform.googleapis.com/v1/projects/my-project/locations/asia-northeast1/batchPredictionJobs", regional)
}

// TestVertexProvider_SubmitUploadsJSONLAndCreatesBatchPredictionJob uses
// successful upload and creation fixtures. It checks managed references,
// JSONL formats, result key selection, and omission of the tested credential
// and unrelated request fields from submitted metadata.
func TestVertexProvider_SubmitUploadsJSONLAndCreatesBatchPredictionJob(t *testing.T) {
	vertexClient := &fakeVertexBatchClient{created: &VertexBatchPredictionJob{Name: "projects/proj/locations/global/batchPredictionJobs/job-1", State: "JOB_STATE_PENDING"}}
	store := &fakeVertexObjectStore{}
	platform := newTestVertexProvider(vertexClient, store)

	got, err := platform.Submit(context.Background(), &batchimage.BatchImageJob{BatchID: "imgbatch_abc123", Model: "gemini-3.1-flash-image"}, vertexServiceAccount(), validVertexBatchInput())
	require.NoError(t, err)

	require.Equal(t, "gs://managed-bucket/batch-image/test/imgbatch_abc123/input/requests.jsonl", store.uploadURI)
	require.Equal(t, "projects/proj/locations/global/batchPredictionJobs/job-1", got.ProviderJobName)
	require.Equal(t, store.uploadURI, got.ProviderInputRef)
	require.Equal(t, "gs://managed-bucket/batch-image/test/imgbatch_abc123/output/", got.ProviderOutputRef)
	require.Equal(t, "jsonl", vertexClient.createdReq.InputConfig.InstancesFormat)
	require.Equal(t, "jsonl", vertexClient.createdReq.OutputConfig.PredictionsFormat)
	require.Equal(t, got.ProviderOutputRef, vertexClient.createdReq.OutputConfig.GCSDestination.OutputURIPrefix)
	require.Equal(t, "key", vertexClient.createdReq.InstanceConfig.KeyField)
	require.NotContains(t, string(vertexClient.createdPayloadForAssert(t)), "serviceAccount")
	require.NotContains(t, string(vertexClient.createdPayloadForAssert(t)), "encryptionSpec")
	require.NotContains(t, got.ProviderInputRef+got.ProviderOutputRef+got.ProviderJobName, "A clean product hero image")
	require.NotContains(t, string(store.uploadedJSONL), "private_key")
}

// TestVertexProvider_GetMapsStates supplies pending, queued, running, and
// supported terminal observations. Polling returns each expected internal state,
// Done flag, error code, and the supplied output prefix for that response.
func TestVertexProvider_GetMapsStates(t *testing.T) {
	tests := []struct {
		name      string                                // case identification for subtests
		state     string                                // configured upstream state spelling
		err       *VertexBatchJobError                  // configured upstream failure details
		wantState batchimage.BatchProviderInternalState // expected processor internal state
		wantDone  bool                                  // expected terminal observation flag
		wantCode  string                                // expected public failure code
	}{
		{name: "pending", state: "JOB_STATE_PENDING", wantState: batchimage.BatchProviderStateQueued},
		{name: "queued", state: "JOB_STATE_QUEUED", wantState: batchimage.BatchProviderStateQueued},
		{name: "running", state: "JOB_STATE_RUNNING", wantState: batchimage.BatchProviderStateRunning},
		{name: "succeeded", state: "JOB_STATE_SUCCEEDED", wantState: batchimage.BatchProviderStateSucceeded, wantDone: true},
		{name: "failed", state: "JOB_STATE_FAILED", err: &VertexBatchJobError{Status: "INVALID_ARGUMENT", Message: "bad request"}, wantState: batchimage.BatchProviderStateFailed, wantDone: true, wantCode: "INVALID_ARGUMENT"},
		{name: "cancelled", state: "JOB_STATE_CANCELLED", wantState: batchimage.BatchProviderStateCancelled, wantDone: true, wantCode: "VERTEX_BATCH_CANCELLED"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output := "gs://managed-bucket/batch-image/test/imgbatch_abc123/output/"
			platform := newTestVertexProvider(&fakeVertexBatchClient{got: &VertexBatchPredictionJob{
				Name:         "projects/proj/locations/global/batchPredictionJobs/job-1",
				State:        tt.state,
				Error:        tt.err,
				OutputConfig: VertexBatchOutputConfig{GCSDestination: VertexBatchGCSDestination{OutputURIPrefix: output}},
			}}, &fakeVertexObjectStore{})
			got, err := platform.Get(context.Background(), vertexJobWithName("projects/proj/locations/global/batchPredictionJobs/job-1"), vertexServiceAccount())
			require.NoError(t, err)
			require.Equal(t, tt.wantState, got.InternalState)
			require.Equal(t, tt.wantDone, got.Done)
			require.Equal(t, output, got.ProviderOutputRef)
			require.Equal(t, tt.wantCode, got.ErrorCode)
		})
	}
}

// TestVertexProvider_OpenResultReturnsCombinedJSONLStream lists result objects
// out of lexical order. Reading returns them in sorted order with the combined
// reader's separating newline, including beside already terminated object text.
func TestVertexProvider_OpenResultReturnsCombinedJSONLStream(t *testing.T) {
	output := "gs://managed-bucket/batch-image/test/imgbatch_abc123/output/"
	store := &fakeVertexObjectStore{
		// Return object names in reverse lexical order so reading can expose the
		// provider's sorting decision. Both bodies already end in newlines, making
		// the combined reader's extra separator observable.
		listed: []string{
			output + "predictions_2.jsonl",
			output + "predictions_1.jsonl",
		},
		objects: map[string]string{
			output + "predictions_1.jsonl": `{"key":"1"}` + "\n",
			output + "predictions_2.jsonl": `{"key":"2"}` + "\n",
		},
	}
	platform := newTestVertexProvider(&fakeVertexBatchClient{}, store)
	r, contentType, err := platform.OpenResult(context.Background(), &batchimage.BatchImageJob{ProviderOutputRef: &output}, vertexServiceAccount())
	require.NoError(t, err)
	defer func() { require.NoError(t, r.Close()) }()

	body, err := io.ReadAll(r)
	require.NoError(t, err)
	require.Equal(t, "application/jsonl", contentType)
	require.Equal(t, "{\"key\":\"1\"}\n\n{\"key\":\"2\"}\n", string(body))
}

// TestVertexProvider_OpenResultMissingObjectsReturnsTypedError supplies a
// stored output prefix with an empty object listing. The provider returns the
// missing-result-objects reason before exposing a combined stream.
func TestVertexProvider_OpenResultMissingObjectsReturnsTypedError(t *testing.T) {
	output := "gs://managed-bucket/batch-image/test/imgbatch_abc123/output/"
	platform := newTestVertexProvider(&fakeVertexBatchClient{}, &fakeVertexObjectStore{})
	_, _, err := platform.OpenResult(context.Background(), &batchimage.BatchImageJob{ProviderOutputRef: &output}, vertexServiceAccount())
	require.Error(t, err)
	require.Equal(t, "VERTEX_RESULT_OBJECTS_MISSING", apperror.Reason(err))
}

// TestVertexProvider_CancelCallsClient cancels a job with a stored Vertex
// resource name. The operation succeeds and the fake records that name,
// checking the request addresses the bound upstream job.
func TestVertexProvider_CancelCallsClient(t *testing.T) {
	vertexClient := &fakeVertexBatchClient{}
	platform := newTestVertexProvider(vertexClient, &fakeVertexObjectStore{})

	err := platform.Cancel(context.Background(), vertexJobWithName("projects/proj/locations/global/batchPredictionJobs/job-1"), vertexServiceAccount())
	require.NoError(t, err)
	require.Equal(t, "projects/proj/locations/global/batchPredictionJobs/job-1", vertexClient.cancelledName)
}

// TestVertexProvider_CleanupDeletesOnlyManagedPaths cleans both generated
// input and output references for one batch. The store observes the exact input
// object deletion and the corresponding managed output-prefix deletion.
func TestVertexProvider_CleanupDeletesOnlyManagedPaths(t *testing.T) {
	input := "gs://managed-bucket/batch-image/test/imgbatch_abc123/input/requests.jsonl"
	output := "gs://managed-bucket/batch-image/test/imgbatch_abc123/output/"
	store := &fakeVertexObjectStore{}
	platform := newTestVertexProvider(&fakeVertexBatchClient{}, store)

	err := platform.Cleanup(context.Background(), &batchimage.BatchImageJob{BatchID: "imgbatch_abc123", ProviderInputRef: &input, ProviderOutputRef: &output}, vertexServiceAccount(), batchimage.CleanupTargetAll)
	require.NoError(t, err)
	require.Equal(t, []string{input}, store.deletedObjects)
	require.Equal(t, []string{output}, store.deletedPrefixes)
}

// TestVertexProvider_CleanupRejectsUnsafePath supplies an input reference from
// another bucket. Cleanup returns the unsafe-path error after comparing it with
// the generated managed object for the stored batch ID.
func TestVertexProvider_CleanupRejectsUnsafePath(t *testing.T) {
	input := "gs://other-bucket/batch-image/test/imgbatch_abc123/input/requests.jsonl"
	platform := newTestVertexProvider(&fakeVertexBatchClient{}, &fakeVertexObjectStore{})

	err := platform.Cleanup(context.Background(), &batchimage.BatchImageJob{BatchID: "imgbatch_abc123", ProviderInputRef: &input}, vertexServiceAccount(), batchimage.CleanupTargetInput)
	require.ErrorIs(t, err, batchimage.ErrBatchImageProviderUnsafeCleanupPath)
}

// TestVertexProvider_ErrorsDoNotExposeServiceAccountSecrets injects a 403
// creation error containing private-key text. Submission returns the permission
// reason with fixed error text that omits that key and the account email.
func TestVertexProvider_ErrorsDoNotExposeServiceAccountSecrets(t *testing.T) {
	privateKey := "-----BEGIN PRIVATE KEY-----secret-----END PRIVATE KEY-----"
	provider := vertexServiceAccount()
	provider.Credentials["service_account_json"] = map[string]any{
		"type":         "service_account",
		"project_id":   "proj",
		"private_key":  privateKey,
		"client_email": "svc@proj.iam.gserviceaccount.com",
	}
	platform := newTestVertexProvider(&fakeVertexBatchClient{createErr: &VertexAPIError{StatusCode: 403, Message: "do not expose " + privateKey}}, &fakeVertexObjectStore{})

	_, err := platform.Submit(context.Background(), nil, provider, validVertexBatchInput())
	require.Error(t, err)
	require.Equal(t, "VERTEX_PERMISSION_DENIED", apperror.Reason(err))
	require.NotContains(t, err.Error(), privateKey)
	require.NotContains(t, err.Error(), "svc@proj")
}

// TestVertexProvider_MetadataDoesNotStoreImageBytesOrBase64 submits the fixture
// prompt with fixed managed and job references. Returned metadata omits the
// tested base64 markers and prompt, keeping resource identity separate from
// that input text.
func TestVertexProvider_MetadataDoesNotStoreImageBytesOrBase64(t *testing.T) {
	vertexClient := &fakeVertexBatchClient{created: &VertexBatchPredictionJob{Name: "projects/proj/locations/global/batchPredictionJobs/job-1", State: "JOB_STATE_PENDING"}}
	platform := newTestVertexProvider(vertexClient, &fakeVertexObjectStore{})

	got, err := platform.Submit(context.Background(), nil, vertexServiceAccount(), validVertexBatchInput())
	require.NoError(t, err)
	metadata := got.ProviderJobName + got.ProviderInputRef + got.ProviderOutputRef
	require.NotContains(t, metadata, "iVBOR")
	require.NotContains(t, metadata, "base64")
	require.NotContains(t, metadata, "A clean product hero image")
}

// validVertexBatchInput returns one valid keyed Vertex image-request fixture.
func validVertexBatchInput() batchimage.BatchImageInput {
	return batchimage.BatchImageInput{
		BatchID:     "imgbatch_abc123",
		Model:       "gemini-3.1-flash-image",
		DisplayName: "test vertex batch",
		Items: []batchimage.BatchImageInputItem{{
			CustomID: "cover_001",
			Prompt:   "A clean product hero image",
		}},
	}
}

// requireVertexJSONLLine decodes one request and checks its key, user role,
// prompt, and text-plus-image modalities. It marks itself as a helper so
// assertion failures identify the invoking test's requested line check.
func requireVertexJSONLLine(t *testing.T, line, wantKey, wantPrompt string) {
	t.Helper()
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(line), &got))
	require.Equal(t, wantKey, got["key"])
	request := testassert.MustType[map[string]any](got["request"])
	contents := testassert.MustType[[]any](request["contents"])
	require.Equal(t, "user", testassert.MustType[map[string]any](contents[0])["role"])
	parts := testassert.MustType[[]any](testassert.MustType[map[string]any](contents[0])["parts"])
	require.Equal(t, wantPrompt, testassert.MustType[map[string]any](parts[0])["text"])
	config := testassert.MustType[map[string]any](request["generationConfig"])
	require.Equal(t, []any{"TEXT", "IMAGE"}, config["responseModalities"])
}

// newTestVertexProvider binds supplied fakes to fixed project and managed-path
// options. Its token cache supplies a fixture token, so supported credential
// parsing can proceed without a signed exchange or external request.
func newTestVertexProvider(client *fakeVertexBatchClient, store *fakeVertexObjectStore) *VertexBatchImageProvider {
	return NewVertexBatchImageProvider(VertexBatchImageProviderOptions{
		ProjectID:        "proj",
		Location:         "global",
		ManagedGCSBucket: "managed-bucket",
		ManagedGCSPrefix: "batch-image/{env}/{batch_id}",
		Environment:      "test",
	}, client, store, &fakeGeminiTokenCache{token: "ya29.test-token"})
}

// vertexServiceAccount returns a Gemini record with required service-account
// fields for credential parsing fixtures.
func vertexServiceAccount() *providercore.Record {
	return &providercore.Record{
		Platform: capability.PlatformGemini,
		Type:     capability.ProviderTypeServiceAccount,
		Credentials: map[string]any{
			"service_account_json": map[string]any{
				"type":         "service_account",
				"project_id":   "proj",
				"private_key":  "-----BEGIN PRIVATE KEY-----\nabc\n-----END PRIVATE KEY-----\n",
				"client_email": "svc@proj.iam.gserviceaccount.com",
			},
		},
	}
}

// vertexJobWithName returns fixture job metadata pointing to the supplied
// Vertex resource name.
func vertexJobWithName(name string) *batchimage.BatchImageJob {
	return &batchimage.BatchImageJob{ProviderJobName: &name}
}

// CreateBatchPredictionJob rejects a blank token, then records the supplied
// request before checking its injected error. A chosen response is returned
// directly; nil selects a fixed named pending job for submission tests.
func (f *fakeVertexBatchClient) CreateBatchPredictionJob(_ context.Context, accessToken string, req VertexCreateBatchPredictionJobRequest) (*VertexBatchPredictionJob, error) {
	if strings.TrimSpace(accessToken) == "" {
		return nil, errors.New("missing token")
	}
	f.createdReq = req
	if f.createErr != nil {
		return nil, f.createErr
	}
	// A supplied response exposes the exact shape selected by its test.
	// When absent, use a named pending job so ordinary submission cases receive
	// valid upstream identity from the fake.
	if f.created != nil {
		return f.created, nil
	}
	return &VertexBatchPredictionJob{Name: "projects/proj/locations/global/batchPredictionJobs/job-1", State: "JOB_STATE_PENDING"}, nil
}

// GetBatchPredictionJob returns its injected error or configured observation.
// A nil observation succeeds with nil, letting the provider validate an
// empty upstream response when a test chooses that shape.
func (f *fakeVertexBatchClient) GetBatchPredictionJob(_ context.Context, _ string, _ string) (*VertexBatchPredictionJob, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.got, nil
}

// CancelBatchPredictionJob records the requested resource name before
// returning its configured error. A nil error reports successful fake request
// completion; the configured polling response remains available for independent
// state observations.
func (f *fakeVertexBatchClient) CancelBatchPredictionJob(_ context.Context, _ string, name string) error {
	f.cancelledName = name
	return f.cancelErr
}

// createdPayloadForAssert serializes the recorded creation request for JSON
// field assertions. It marks the helper and fails the test on encoding errors,
// then returns the complete encoded payload bytes.
func (f *fakeVertexBatchClient) createdPayloadForAssert(t *testing.T) []byte {
	t.Helper()
	b, err := json.Marshal(f.createdReq)
	require.NoError(t, err)
	return b
}

// UploadJSONL records the target URI and all collected input bytes, then
// returns its configured error. Reader errors are discarded by this fake;
// tests use the injected upload error to select its outcome.
func (f *fakeVertexObjectStore) UploadJSONL(_ context.Context, _ string, uri string, r io.Reader) error {
	f.uploadURI = uri
	// Capture available request bytes for assertions even when a reader reports
	// failure. The injected uploadErr selects the fake's operation outcome, so the
	// reader error is discarded after capture.
	f.uploadedJSONL, _ = io.ReadAll(r)
	return f.uploadErr
}

// ListJSONLObjects returns a new slice of configured names ending in .jsonl
// in their supplied order. A configured listing error takes precedence;
// the fake's prefix argument does not filter those fixture names.
func (f *fakeVertexObjectStore) ListJSONLObjects(_ context.Context, _ string, _ string) ([]string, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]string, 0, len(f.listed))
	for _, item := range f.listed {
		if strings.HasSuffix(item, ".jsonl") {
			out = append(out, item)
		}
	}
	return out, nil
}

// OpenObject returns a new caller-closed JSONL reader for the configured URI.
// An injected error takes precedence; an absent map entry supplies an empty
// body with the same application/jsonl MIME label.
func (f *fakeVertexObjectStore) OpenObject(_ context.Context, _ string, uri string) (io.ReadCloser, string, error) {
	if f.openErr != nil {
		return nil, "", f.openErr
	}
	return io.NopCloser(bytes.NewBufferString(f.objects[uri])), "application/jsonl", nil
}

// DeleteObject records the requested URI before returning its configured
// deletion error. Failed fake deletions remain in deletedObjects for assertions
// that inspect which stored resource cleanup attempted.
func (f *fakeVertexObjectStore) DeleteObject(_ context.Context, _ string, uri string) error {
	f.deletedObjects = append(f.deletedObjects, uri)
	return f.deleteErr
}

// DeletePrefix records the requested prefix before returning its configured
// deletion error. Failed fake deletions remain in deletedPrefixes for assertions
// that inspect the managed output scope cleanup attempted.
func (f *fakeVertexObjectStore) DeletePrefix(_ context.Context, _ string, uri string) error {
	f.deletedPrefixes = append(f.deletedPrefixes, uri)
	return f.deleteErr
}

// GetAccessToken returns the configured token for any requested cache key.
// A whitespace-only token returns a missing-token error; tests configure a
// nonblank token to satisfy the credential coordinator's cache lookup.
func (f *fakeGeminiTokenCache) GetAccessToken(context.Context, string) (string, error) {
	if strings.TrimSpace(f.token) == "" {
		return "", errors.New("missing token")
	}
	return f.token, nil
}

// SetAccessToken accepts cache writes and returns success without updating the
// fixture token. Provider tests use the configured lookup value; this fake
// does not model token publication after exchange.
func (f *fakeGeminiTokenCache) SetAccessToken(context.Context, string, string, time.Duration) error {
	return nil
}

// DeleteAccessToken accepts invalidation and returns success while retaining
// the configured fixture token. The fake supports provider setup that expects
// cache methods, with lookup outcomes selected directly by each test.
func (f *fakeGeminiTokenCache) DeleteAccessToken(context.Context, string) error {
	return nil
}

// AcquireRefreshLock reports an unavailable lock and no error for every key.
// Provider fixtures normally return their cached token before this method is
// needed; the fake does not install Redis coordination state.
func (f *fakeGeminiTokenCache) AcquireRefreshLock(context.Context, string, time.Duration) (bool, error) {
	return false, nil
}

// ReleaseRefreshLock accepts a refresh-lock release and returns success.
// The fixture supplies lock contention through AcquireRefreshLock and keeps no
// stored lock state, so release has no additional observable effect.
func (f *fakeGeminiTokenCache) ReleaseRefreshLock(context.Context, string) error {
	return nil
}
