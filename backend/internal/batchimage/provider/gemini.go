package provider

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	core "github.com/TokenFlux/TokenRouter/internal/batchimage"
	gemininative "github.com/TokenFlux/TokenRouter/internal/upstream/gemini"
)

// Gemini image batches submit uploaded JSONL by file resource name and keep
// that name with the provider job for subsequent polling and cleanup. Submission
// validates the batch protocol and API-key credential shape before uploading,
// then creates the asynchronous job from the returned file. A failed creation
// can leave the successful upload available upstream.
//
// Polling interprets server state together with its output representation.
// Succeeded inline responses are rejected by the file-based indexing path, and
// a succeeded job missing its result file becomes a failed observation.
// Supported output spellings resolve to one stored reference, while result
// reading transfers the downloaded stream to the caller and cleanup uses
// references already held by the job.

// DefaultGeminiBatchRequeueAfter supplies the polling suggestion on every
// Gemini observation, including terminal states. ProviderProcessor reads this
// interval when InternalState calls for another queued or running check.
const DefaultGeminiBatchRequeueAfter = 30 * time.Second

var (
	// These assertions keep the Gemini adapter and HTTP client compatible with
	// the batch execution and API exchange interfaces used by this package.
	_ BatchImageProvider = (*GeminiAPIBatchImageProvider)(nil)
	_ GeminiBatchClient  = (*GeminiBatchHTTPClient)(nil)
)

// GeminiBatchClient exchanges uploaded files and asynchronous Gemini batches
// using supplied API-key credentials.
type GeminiBatchClient interface {
	// UploadJSONL reads JSONL from the supplied reader and uploads a file resource.
	// The caller retains reader ownership and manages the stored file's later
	// cleanup; the returned name is used when creating its batch.
	UploadJSONL(ctx context.Context, apiKey string, displayName string, r io.Reader) (*GeminiUploadedFile, error)
	// CreateBatch submits the named uploaded file to the supplied model using its
	// API key. The returned job name identifies later polling and cancellation;
	// callers manage file and job lifetimes.
	CreateBatch(ctx context.Context, apiKey string, model string, fileName string, displayName string) (*GeminiBatchJob, error)
	// GetBatch reads the named asynchronous job with the supplied API key.
	// Its response describes one upstream observation; the provider adapter maps
	// that state and the job processor decides further polling.
	GetBatch(ctx context.Context, apiKey string, batchName string) (*GeminiBatchJob, error)
	// CancelBatch requests cancellation of the named upstream batch with the
	// supplied API key. Successful request completion leaves terminal-state
	// confirmation to later job observations and the processor's lifecycle rules.
	CancelBatch(ctx context.Context, apiKey string, batchName string) error
	// DownloadFile opens the named result file and returns its content type.
	// On success the caller owns the returned stream and closes it after consumption;
	// authentication and download failures return as errors.
	DownloadFile(ctx context.Context, apiKey string, fileName string) (io.ReadCloser, string, error)
	// DeleteFile removes the named uploaded or result file with the supplied
	// API key. The provider adapter chooses stored job references for cleanup
	// and maps returned client errors into application failures.
	DeleteFile(ctx context.Context, apiKey string, fileName string) error
}

// GeminiUploadedFile identifies the upstream file resource returned by a JSONL
// upload.
type GeminiUploadedFile = gemininative.GeminiUploadedFile

// GeminiBatchJob carries the upstream job name, state, errors, and result
// representations.
type GeminiBatchJob = gemininative.GeminiBatchJob

// GeminiBatchDest carries alternative upstream spellings for a batch output
// file reference.
type GeminiBatchDest = gemininative.GeminiBatchDest

// GeminiBatchResponse carries file or inline result forms returned by the
// upstream batch.
type GeminiBatchResponse = gemininative.GeminiBatchResponse

// GeminiBatchError carries the upstream symbolic status and diagnostic details
// for a batch.
type GeminiBatchError = gemininative.GeminiBatchError

// GeminiAPIBatchImageProvider executes file-based image batches through a
// Gemini API-key client.
type GeminiAPIBatchImageProvider struct {
	// client is retained by the constructor and reused for file and job
	// operations. Callers keep an injected client's configuration stable while the
	// provider is shared across batch work.
	client GeminiBatchClient // Gemini batch exchange client
}

// GeminiBatchHTTPClient implements API-key file and batch exchanges through
// the upstream Gemini client.
type GeminiBatchHTTPClient = gemininative.GeminiBatchHTTPClient

// GeminiAPIError records upstream HTTP status and diagnostic details before
// public error mapping.
type GeminiAPIError = gemininative.GeminiAPIError

// NewGeminiAPIBatchImageProvider retains the supplied client for batch
// operations. A nil client constructs the default Gemini HTTP wrapper;
// callers configure any supplied client before sharing the provider.
func NewGeminiAPIBatchImageProvider(client GeminiBatchClient) *GeminiAPIBatchImageProvider {
	if client == nil {
		client = NewGeminiBatchHTTPClient("", nil)
	}
	return &GeminiAPIBatchImageProvider{client: client}
}

// Name returns the Gemini API identity used for batch registry lookup.
func (p *GeminiAPIBatchImageProvider) Name() string {
	return core.BatchImageProviderGeminiAPI
}

// SupportsProvider reports whether a record is a Gemini API-key provider with
// a nonblank secret. It checks credential shape; Submit separately checks
// configured batch protocol eligibility before creating upstream resources.
func (p *GeminiAPIBatchImageProvider) SupportsProvider(provider *Provider) bool {
	return provider != nil &&
		provider.Platform == PlatformGemini &&
		provider.Type == ProviderTypeAPIKey &&
		batchImageProviderAPIKey(provider) != ""
}

// Submit uploads Gemini JSONL and creates a batch from its returned file name.
// The provider needs the enabled batch route and a nonblank API key; empty
// input batch ID or model is filled from a supplied job before encoding.
//
// The JSONL upload precedes job creation, and each response needs its resource
// name before the next phase can proceed. Creation errors leave a successful
// upload upstream because this adapter performs no compensation. A successful
// result carries the job name, input file reference, and raw state for local
// persistence and subsequent polling.
func (p *GeminiAPIBatchImageProvider) Submit(ctx context.Context, job *core.BatchImageJob, provider *Provider, input core.BatchImageInput) (*core.BatchProviderJob, error) {
	if _, enabled := resolveBatchProtocol(provider); !enabled {
		return nil, core.ErrBatchImageProviderUnsupportedProvider
	}
	if provider == nil || provider.Platform != PlatformGemini || provider.Type != ProviderTypeAPIKey {
		return nil, core.ErrBatchImageProviderUnsupportedProvider
	}
	apiKey := batchImageProviderAPIKey(provider)
	if apiKey == "" {
		return nil, core.ErrBatchImageProviderMissingAPIKey
	}
	if input.BatchID == "" && job != nil {
		input.BatchID = job.BatchID
	}
	if input.Model == "" && job != nil {
		input.Model = job.Model
	}

	jsonl, err := BuildGeminiBatchJSONL(input)
	if err != nil {
		return nil, err
	}

	displayName := strings.TrimSpace(input.DisplayName)
	if displayName == "" {
		displayName = strings.TrimSpace(input.BatchID)
	}

	// Upload before creation because the batch API accepts a file resource name.
	// If later creation fails, this adapter cannot return a successful job binding
	// and leaves that uploaded file upstream.
	uploaded, err := p.client.UploadJSONL(ctx, apiKey, displayName, bytes.NewReader(jsonl))
	if err != nil {
		return nil, MapGeminiClientError(err)
	}
	if uploaded == nil || strings.TrimSpace(uploaded.Name) == "" {
		return nil, GeminiProviderError("GEMINI_INVALID_RESPONSE", "Gemini upload response is missing file name", nil)
	}

	// Use the upload's returned file name to bind this batch to its encoded
	// items. A valid created job name is required before local processing can
	// retain that binding for polling.
	batch, err := p.client.CreateBatch(ctx, apiKey, input.Model, uploaded.Name, displayName)
	if err != nil {
		return nil, MapGeminiClientError(err)
	}
	if batch == nil || strings.TrimSpace(batch.Name) == "" {
		return nil, GeminiProviderError("GEMINI_INVALID_RESPONSE", "Gemini batch response is missing job name", nil)
	}

	return &core.BatchProviderJob{
		ProviderJobName:  batch.Name,
		ProviderInputRef: uploaded.Name,
		RawState:         batch.State,
	}, nil
}

// Get reads the stored provider job with its Gemini API-key credentials and
// maps the server observation. Missing job names or credentials fail before
// the client call, while nil responses produce a fixed invalid-response error.
//
// A succeeded observation must supply a file result for indexing. Inline
// results return the unsupported-shape error; a missing file turns that
// observation into a done failure. Supported file references attach to the
// mapped status, and existing job polling uses credential checks independently
// of submission's batch-route gate.
func (p *GeminiAPIBatchImageProvider) Get(ctx context.Context, job *core.BatchImageJob, provider *Provider) (*core.BatchProviderStatus, error) {
	if provider == nil || provider.Platform != PlatformGemini || provider.Type != ProviderTypeAPIKey {
		return nil, core.ErrBatchImageProviderUnsupportedProvider
	}
	apiKey := batchImageProviderAPIKey(provider)
	if apiKey == "" {
		return nil, core.ErrBatchImageProviderMissingAPIKey
	}
	jobName := core.BatchImageProviderJobName(job)
	if jobName == "" {
		return nil, core.ErrBatchImageProviderMissingJobName
	}

	batch, err := p.client.GetBatch(ctx, apiKey, jobName)
	if err != nil {
		return nil, MapGeminiClientError(err)
	}
	if batch == nil {
		return nil, GeminiProviderError("GEMINI_INVALID_RESPONSE", "Gemini batch response is empty", nil)
	}

	status := MapGeminiBatchState(batch)
	if status.InternalState == core.BatchProviderStateSucceeded {
		// The indexer reads JSONL from a stored file reference. Reject inline success
		// results before attaching output metadata, since treating them as a file would
		// lose the result representation needed for indexing.
		if GeminiBatchHasInlineResults(batch) {
			return nil, core.ErrBatchImageProviderInlineResultUnsupported
		}
		outputRef := GeminiBatchOutputRef(batch)
		// A successful state alone supplies no indexable result. Convert missing-file
		// success into a done failure so the processor handles funding release rather
		// than advancing with an absent result reference.
		if outputRef == "" {
			status.InternalState = core.BatchProviderStateFailed
			status.Done = true
			status.ErrorCode = "GEMINI_RESULT_FILE_MISSING"
			status.ErrorMessage = "Gemini batch succeeded without a result file reference"
		}
		status.ProviderOutputRef = outputRef
	}
	return status, nil
}

// Cancel requests cancellation of the stored Gemini batch name using its
// API-key provider. Missing credentials or job name fail before the call;
// client errors are mapped and later polling establishes the terminal state.
func (p *GeminiAPIBatchImageProvider) Cancel(ctx context.Context, job *core.BatchImageJob, provider *Provider) error {
	if provider == nil || provider.Platform != PlatformGemini || provider.Type != ProviderTypeAPIKey {
		return core.ErrBatchImageProviderUnsupportedProvider
	}
	apiKey := batchImageProviderAPIKey(provider)
	if apiKey == "" {
		return core.ErrBatchImageProviderMissingAPIKey
	}
	jobName := core.BatchImageProviderJobName(job)
	if jobName == "" {
		return core.ErrBatchImageProviderMissingJobName
	}
	return MapGeminiClientError(p.client.CancelBatch(ctx, apiKey, jobName))
}

// OpenResult downloads the stored Gemini output reference with the bound
// API-key provider and maps client failures. Callers close a successful reader
// after consuming its bytes; missing credentials or references fail before
// download.
func (p *GeminiAPIBatchImageProvider) OpenResult(ctx context.Context, job *core.BatchImageJob, provider *Provider) (io.ReadCloser, string, error) {
	if provider == nil || provider.Platform != PlatformGemini || provider.Type != ProviderTypeAPIKey {
		return nil, "", core.ErrBatchImageProviderUnsupportedProvider
	}
	apiKey := batchImageProviderAPIKey(provider)
	if apiKey == "" {
		return nil, "", core.ErrBatchImageProviderMissingAPIKey
	}
	outputRef := core.BatchImageProviderOutputRef(job)
	if outputRef == "" {
		return nil, "", core.ErrBatchImageProviderMissingResultRef
	}
	r, contentType, err := p.client.DownloadFile(ctx, apiKey, outputRef)
	return r, contentType, MapGeminiClientError(err)
}

// Cleanup deletes the job's stored Gemini input, output, or both file
// references using its API-key provider. Blank references are no-ops, and an
// unknown target returns the cleanup-target error after credential validation.
//
// All-target cleanup deletes input before output and stops on the first error.
// If output deletion fails, the earlier input deletion remains applied. The
// stored references remain available to retry that partial cleanup; remote
// deletion and local deletion markers are separate use-case steps.
func (p *GeminiAPIBatchImageProvider) Cleanup(ctx context.Context, job *core.BatchImageJob, provider *Provider, target core.CleanupTarget) error {
	if provider == nil || provider.Platform != PlatformGemini || provider.Type != ProviderTypeAPIKey {
		return core.ErrBatchImageProviderUnsupportedProvider
	}
	apiKey := batchImageProviderAPIKey(provider)
	if apiKey == "" {
		return core.ErrBatchImageProviderMissingAPIKey
	}

	switch target {
	case core.CleanupTargetInput:
		return p.DeleteGeminiFileIfPresent(ctx, apiKey, core.BatchImageProviderInputRef(job))
	case core.CleanupTargetOutput:
		return p.DeleteGeminiFileIfPresent(ctx, apiKey, core.BatchImageProviderOutputRef(job))
	// Delete the stored input before output for all-target cleanup.
	// Stop after an input error so the caller sees a partial cleanup outcome and
	// can retry using the retained references.
	case core.CleanupTargetAll:
		if err := p.DeleteGeminiFileIfPresent(ctx, apiKey, core.BatchImageProviderInputRef(job)); err != nil {
			return err
		}
		return p.DeleteGeminiFileIfPresent(ctx, apiKey, core.BatchImageProviderOutputRef(job))
	default:
		return core.ErrUnsupportedCleanupTarget
	}
}

// DeleteGeminiFileIfPresent skips an absent or whitespace-only reference and
// maps errors from deleting a present file. It passes the supplied nonblank
// name to the client, leaving resource-name normalization to that implementation.
func (p *GeminiAPIBatchImageProvider) DeleteGeminiFileIfPresent(ctx context.Context, apiKey, fileName string) error {
	if strings.TrimSpace(fileName) == "" {
		return nil
	}
	return MapGeminiClientError(p.client.DeleteFile(ctx, apiKey, fileName))
}

// BuildGeminiBatchJSONL converts batch input references and normalizes their
// MIME labels before delegating JSONL encoding. The conversion supplies model,
// keys, prompts, and reference sources; response MIME type, aspect ratio, image
// size, and metadata have no encoding here. The encoder validates a nonblank
// model and trimmed keys and prompts, rejects duplicate normalized keys or
// ambiguous image sources, and reads inline byte slices during the call.
func BuildGeminiBatchJSONL(input core.BatchImageInput) ([]byte, error) {
	// Normalize MIME labels while carrying each custom ID, prompt, and reference
	// source into the upstream encoder. Inline bytes remain borrowed until that
	// synchronous encoder finishes base64 serialization.
	value := gemininative.BatchJSONLInput{Model: input.Model, Items: make([]gemininative.BatchJSONLItem, len(input.Items))}
	for i, item := range input.Items {
		value.Items[i] = gemininative.BatchJSONLItem{CustomID: item.CustomID, Prompt: item.Prompt, ReferenceImages: make([]gemininative.BatchReference, len(item.ReferenceImages))}
		for j, ref := range item.ReferenceImages {
			value.Items[i].ReferenceImages[j] = gemininative.BatchReference{MimeType: core.NormalizeBatchImageReferenceMimeType(ref.MimeType), Data: ref.Data, FileURI: ref.FileURI}
		}
	}
	return gemininative.BuildGeminiBatchJSONL(value, core.BatchImageProviderInputError)
}

// MapGeminiBatchState converts a nonnil Gemini job into a processor observation
// while retaining its trimmed raw state. Known terminal states set Done;
// unknown states remain running unless a nonempty error code or message
// establishes failure.
//
// Upstream error Code overrides generated terminal codes, while Status fills
// an otherwise empty code and Message is trimmed into diagnostics. Every
// observation retains the default polling suggestion, including terminal states;
// ProviderProcessor chooses continued polling from InternalState, while Done
// records terminal recognition in the returned observation.
func MapGeminiBatchState(batch *GeminiBatchJob) *core.BatchProviderStatus {
	state := strings.TrimSpace(batch.State)
	normalized := strings.ToUpper(state)
	status := &core.BatchProviderStatus{
		RawState:              state,
		InternalState:         core.BatchProviderStateRunning,
		SuggestedRequeueAfter: DefaultGeminiBatchRequeueAfter,
	}

	switch normalized {
	case "JOB_STATE_PENDING", "JOB_STATE_QUEUED":
		status.InternalState = core.BatchProviderStateQueued
	case "JOB_STATE_RUNNING":
		status.InternalState = core.BatchProviderStateRunning
	case "JOB_STATE_SUCCEEDED":
		status.InternalState = core.BatchProviderStateSucceeded
		status.Done = true
	case "JOB_STATE_FAILED":
		status.InternalState = core.BatchProviderStateFailed
		status.Done = true
		status.ErrorCode = "GEMINI_BATCH_FAILED"
	case "JOB_STATE_CANCELLED":
		status.InternalState = core.BatchProviderStateCancelled
		status.Done = true
		status.ErrorCode = "GEMINI_BATCH_CANCELLED"
	case "JOB_STATE_EXPIRED":
		status.InternalState = core.BatchProviderStateExpired
		status.Done = true
		status.ErrorCode = "GEMINI_BATCH_EXPIRED"
	// Unknown states keep polling until the response supplies a meaningful error
	// code or message. Error Status alone supplies diagnostics but does not make
	// this unknown-state branch terminal.
	default:
		if batch.Error != nil && (strings.TrimSpace(batch.Error.Message) != "" || strings.TrimSpace(batch.Error.Code) != "") {
			status.InternalState = core.BatchProviderStateFailed
			status.Done = true
			status.ErrorCode = "GEMINI_BATCH_FAILED"
		}
	}

	// Retain the provider's symbolic code when present; otherwise Status fills
	// an empty code. This ordering keeps generated known-state failure codes unless
	// an upstream Code replaces them.
	if batch.Error != nil {
		if code := strings.TrimSpace(batch.Error.Code); code != "" {
			status.ErrorCode = code
		} else if status.ErrorCode == "" && strings.TrimSpace(batch.Error.Status) != "" {
			status.ErrorCode = strings.TrimSpace(batch.Error.Status)
		}
		status.ErrorMessage = strings.TrimSpace(batch.Error.Message)
	}
	return status
}

// GeminiBatchOutputRef selects a trimmed output file name from a batch
// response, or empty when none is present. Destination camelCase and snake_case
// names precede response-file variants, giving one deterministic reference to
// the indexer when multiple spellings exist.
//
// Each empty or whitespace-only value lets the next spelling participate.
// The selection retains the chosen resource name rather than combining fields,
// so subsequent download and indexing use one provider result identity.
func GeminiBatchOutputRef(batch *GeminiBatchJob) string {
	if batch == nil {
		return ""
	}
	// Read destination spellings before response-file spellings so mixed upstream
	// shapes select a single deterministic file reference for later downloading and
	// result indexing.
	if batch.Dest != nil {
		if v := strings.TrimSpace(batch.Dest.FileName); v != "" {
			return v
		}
		if v := strings.TrimSpace(batch.Dest.FileNameSnake); v != "" {
			return v
		}
	}
	if batch.Response != nil {
		if v := strings.TrimSpace(batch.Response.ResponsesFile); v != "" {
			return v
		}
		if v := strings.TrimSpace(batch.Response.ResponsesFileSnake); v != "" {
			return v
		}
	}
	return ""
}

// GeminiBatchHasInlineResults reports whether either response inline-results
// slice contains entries. Nil jobs, absent response objects, and empty slices
// return false; Get uses this check when a succeeded job needs file-based
// indexing.
func GeminiBatchHasInlineResults(batch *GeminiBatchJob) bool {
	return batch != nil &&
		batch.Response != nil &&
		(len(batch.Response.InlinedResponses) > 0 || len(batch.Response.InlinedResponsesAlt) > 0)
}

// GeminiProviderError creates a bad-gateway application error from caller-safe
// reason and message text. A nonnil cause is attached by the upstream helper;
// callers choose public text before using this constructor.
func GeminiProviderError(reason, message string, cause error) error {
	return gemininative.ProviderError(reason, message, cause)
}

// MapGeminiClientError delegates fixed-message mapping of Gemini client
// failures. Nil returns nil; authentication, rate-limit, missing-resource,
// and generic failures discard upstream diagnostics before becoming application
// errors exposed by batch use cases.
func MapGeminiClientError(err error) error { return gemininative.MapClientError(err) }

// NewGeminiBatchHTTPClient constructs the upstream wrapper with the batch
// missing-key error. Blank base selects the default Gemini endpoint and nil
// client selects the shared batch HTTP client; supplied client configuration
// remains borrowed.
func NewGeminiBatchHTTPClient(baseURL string, client *http.Client) *GeminiBatchHTTPClient {
	return gemininative.NewGeminiBatchHTTPClient(baseURL, client, core.ErrBatchImageProviderMissingAPIKey)
}
