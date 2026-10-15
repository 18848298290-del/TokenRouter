package provider

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	core "github.com/TokenFlux/TokenRouter/internal/batchimage"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/vertex"
)

// vertex.go submits Vertex image batches through managed GCS resources and
// handles their results and cleanup.
// Vertex image batches upload input to a managed GCS object and submit its URI
// with an output prefix derived from the same batch identity. The job request
// names key as the instance identifier, keeping result rows attributable to
// their submitted custom IDs. A successful upload remains upstream when later
// job creation fails.
//
// Cleanup recomputes managed references from the provider's current options
// and the stored batch ID. Input deletion requires the exact generated object;
// output deletion requires the generated trailing-slash prefix, allowing its
// result descendants. Polling retains output references across upstream and
// stored metadata shapes, while downloads sort listed objects and return the
// upstream client's lazy combined reader for caller-owned consumption.

// Vertex defaults supply polling cadence, the global job location, and the
// managed path template used to distinguish environments and batch identities.
const (
	DefaultVertexBatchRequeueAfter = 30 * time.Second
	DefaultVertexBatchLocation     = "global"
	DefaultVertexManagedGCSPrefix  = "batch-image/{env}/{batch_id}"
)

var (
	// These assertions keep the Vertex adapter, job client, and GCS store
	// compatible with batch execution and managed-object exchange requirements.
	_ BatchImageProvider     = (*VertexBatchImageProvider)(nil)
	_ VertexBatchClient      = (*VertexBatchHTTPClient)(nil)
	_ VertexBatchObjectStore = (*VertexGCSObjectStore)(nil)
)

// VertexBatchImageProviderOptions supplies endpoint identity and managed
// storage settings retained by the batch adapter.
type VertexBatchImageProviderOptions struct {
	// Enabled is retained as configuration metadata. Adapter operations check
	// the provider's batch protocol or credentials; execution is not gated by this
	// field, so callers apply their enablement policy.
	Enabled bool // retained adapter enablement setting
	// ProjectID takes precedence during Submit when nonblank. Empty values
	// use the provider's project credential or parsed service-account project, and
	// a missing resolved project fails after input upload.
	ProjectID string // preferred job project identity
	// Location is normalized to global when blank during construction.
	// Submit prefers its nonblank value; a provider-specific location is considered
	// only if the retained option is empty.
	Location string // preferred job location identity
	// ManagedGCSBucket supplies the bucket for both generated references.
	// ManagedRefs rejects an empty or URI-shaped value, and cleanup compares stored
	// paths against this current bucket setting.
	ManagedGCSBucket string // managed batch storage bucket
	// ManagedGCSPrefix is normalized or defaulted before path substitution.
	// ManagedRefs requires the generated result to contain the batch ID so its
	// input and output resources retain job-specific identity.
	ManagedGCSPrefix string // managed per-job path template
	Environment      string // managed path environment segment
	// InputRetentionHours and OutputRetentionHours retain submitted configuration
	// values. Cleanup scheduling uses the core cleanup options, while this adapter
	// receives a resource target when deletion is requested.
	InputRetentionHours  int // hours
	OutputRetentionHours int // hours
	// BatchPredictionBaseURL selects the default job client's base when no
	// client is injected. A supplied client determines its own request endpoint.
	BatchPredictionBaseURL string // optional job API base
	// GCSBaseURL selects the default object store's base when no store is
	// injected. A supplied store determines its own object request endpoint.
	GCSBaseURL string // optional object API base
}

// VertexBatchImageProvider executes image batches through Vertex jobs and
// managed GCS resources using service-account tokens.
type VertexBatchImageProvider struct {
	opts VertexBatchImageProviderOptions // normalized retained provider options
	// client is retained directly and reused across batch operations.
	// Callers configure an injected client before sharing the provider and keep it
	// available while stored jobs use it.
	client VertexBatchClient // Vertex batch job client
	// objectStore is retained directly for uploads, listing, reads, and
	// deletion. Callers keep an injected store available for the complete lifetime
	// of streams opened through it.
	objectStore VertexBatchObjectStore // managed GCS object adapter
	// tokenCache is optional and passed to the credential coordinator.
	// A nil cache performs direct token exchange, while a supplied cache participates
	// in that coordinator's token and refresh-lock rules.
	tokenCache GeminiTokenCache // optional service-account token cache
}

// VertexManagedRefs ties one batch's input object and output prefix to its
// generated managed storage path.
type VertexManagedRefs struct {
	// Prefix, InputURI, and OutputPrefixURI come from the same batch identity
	// and managed configuration. Cleanup recomputes those values before comparing
	// stored resource paths with its permitted deletion targets.
	Prefix          string // generated relative batch prefix
	InputURI        string // gs:// input object URI
	OutputPrefixURI string // gs:// output prefix URI
}

// VertexBatchClient exchanges asynchronous job resources with caller-supplied
// Vertex bearer tokens.
type VertexBatchClient = vertex.VertexBatchClient

// VertexBatchObjectStore uploads, lists, reads, and deletes managed batch
// objects through GCS.
type VertexBatchObjectStore = vertex.VertexBatchObjectStore

// VertexCreateBatchPredictionJobRequest carries endpoint identity and input,
// output, and instance settings for job submission.
type VertexCreateBatchPredictionJobRequest = vertex.VertexCreateBatchPredictionJobRequest

// VertexBatchInputConfig names the input encoding and GCS sources for a batch
// prediction job.
type VertexBatchInputConfig = vertex.VertexBatchInputConfig

// VertexBatchGCSSource carries the GCS object URIs submitted as batch input.
type VertexBatchGCSSource = vertex.VertexBatchGCSSource

// VertexBatchOutputConfig names the result encoding and GCS destination for
// batch predictions.
type VertexBatchOutputConfig = vertex.VertexBatchOutputConfig

// VertexBatchGCSDestination carries the GCS prefix where a batch writes its
// result objects.
type VertexBatchGCSDestination = vertex.VertexBatchGCSDestination

// VertexBatchInstanceConfig identifies the input field used to correlate
// batch request and result rows.
type VertexBatchInstanceConfig = vertex.VertexBatchInstanceConfig

// VertexBatchPredictionJob carries the server job identity, state, errors,
// and output configuration.
type VertexBatchPredictionJob = vertex.VertexBatchPredictionJob

// VertexBatchJobError carries symbolic status and diagnostic details returned
// for a Vertex batch.
type VertexBatchJobError = vertex.VertexBatchJobError

// VertexBatchHTTPClient implements bearer-authenticated job exchanges through
// the upstream Vertex HTTP wrapper.
type VertexBatchHTTPClient = vertex.VertexBatchHTTPClient

// VertexGCSObjectStore implements managed batch object exchanges through the
// upstream GCS HTTP wrapper.
type VertexGCSObjectStore = vertex.VertexGCSObjectStore

// VertexAPIError carries upstream HTTP status and diagnostic details before
// application error mapping.
type VertexAPIError = vertex.VertexAPIError

// NewVertexBatchImageProvider retains normalized options and supplied
// operation clients. Nil clients select default Vertex and GCS HTTP wrappers;
// a nil token cache leaves token exchange to the credential coordinator's
// uncached path.
func NewVertexBatchImageProvider(opts VertexBatchImageProviderOptions, client VertexBatchClient, objectStore VertexBatchObjectStore, tokenCache GeminiTokenCache) *VertexBatchImageProvider {
	opts = NormalizeVertexBatchImageProviderOptions(opts)
	if client == nil {
		client = NewVertexBatchHTTPClient(opts.BatchPredictionBaseURL, nil)
	}
	if objectStore == nil {
		objectStore = NewVertexGCSObjectStore(opts.GCSBaseURL, nil)
	}
	return &VertexBatchImageProvider{
		opts:        opts,
		client:      client,
		objectStore: objectStore,
		tokenCache:  tokenCache,
	}
}

// NormalizeVertexBatchImageProviderOptions trims endpoint and managed-path
// settings and supplies global location, the standard prefix template, and
// default environment when blank. It retains enablement and retention values
// as supplied and returns the normalized value copy.
func NormalizeVertexBatchImageProviderOptions(opts VertexBatchImageProviderOptions) VertexBatchImageProviderOptions {
	opts.ProjectID = strings.TrimSpace(opts.ProjectID)
	opts.Location = strings.TrimSpace(opts.Location)
	if opts.Location == "" {
		opts.Location = DefaultVertexBatchLocation
	}
	opts.ManagedGCSBucket = strings.Trim(strings.TrimSpace(opts.ManagedGCSBucket), "/")
	opts.ManagedGCSPrefix = strings.Trim(strings.TrimSpace(opts.ManagedGCSPrefix), "/")
	if opts.ManagedGCSPrefix == "" {
		opts.ManagedGCSPrefix = DefaultVertexManagedGCSPrefix
	}
	opts.Environment = strings.TrimSpace(opts.Environment)
	if opts.Environment == "" {
		opts.Environment = "default"
	}
	opts.BatchPredictionBaseURL = strings.TrimRight(strings.TrimSpace(opts.BatchPredictionBaseURL), "/")
	opts.GCSBaseURL = strings.TrimRight(strings.TrimSpace(opts.GCSBaseURL), "/")
	return opts
}

// Name returns the Vertex identity used for batch registry lookup.
func (p *VertexBatchImageProvider) Name() string {
	return core.BatchImageProviderVertex
}

// SupportsProvider reports whether a Gemini service-account record has
// parseable required key fields. This credential check performs no token
// exchange; Submit separately checks the provider's configured batch protocol
// before creating resources.
func (p *VertexBatchImageProvider) SupportsProvider(provider *Provider) bool {
	if provider == nil || provider.Platform != PlatformGemini || provider.Type != ProviderTypeServiceAccount {
		return false
	}
	// A parsing failure makes this credential shape unavailable to selection.
	// Operational validation later returns the batch missing-service-account reason,
	// so this eligibility predicate can discard the parser diagnostic.
	_, err := provideradapter.ParseVertexServiceAccountKey(provider)
	return err == nil
}

// Submit uploads managed Vertex JSONL and creates a batch prediction job.
// It requires the enabled batch route, service-account fields, and managed
// bucket; empty input model or batch ID can come from the supplied job.
//
// Managed references join input and output to one batch prefix, and key is
// selected as the instance identifier for result matching. Upload precedes
// project resolution and job creation, so later failures leave that input
// upstream. A successful response needs a job name and returns it with both
// managed references for persistence, polling, and cleanup.
func (p *VertexBatchImageProvider) Submit(ctx context.Context, job *core.BatchImageJob, provider *Provider, input core.BatchImageInput) (*core.BatchProviderJob, error) {
	if _, enabled := resolveBatchProtocol(provider); !enabled {
		return nil, core.ErrBatchImageProviderUnsupportedProvider
	}
	if err := p.ValidateProvider(provider); err != nil {
		return nil, err
	}
	if strings.TrimSpace(p.opts.ManagedGCSBucket) == "" {
		return nil, VertexProviderError("VERTEX_MANAGED_GCS_BUCKET_MISSING", "Vertex managed GCS bucket is not configured", nil)
	}
	if input.BatchID == "" && job != nil {
		input.BatchID = job.BatchID
	}
	if input.Model == "" && job != nil {
		input.Model = job.Model
	}

	jsonl, err := BuildVertexBatchJSONL(input)
	if err != nil {
		return nil, err
	}
	refs, err := p.ManagedRefs(input.BatchID)
	if err != nil {
		return nil, err
	}

	AccessToken, err := p.AccessToken(ctx, provider)
	if err != nil {
		return nil, MapVertexClientError(err)
	}
	// Upload the encoded items to their managed input before creating the job
	// that references them. A later project or creation failure leaves that object
	// upstream because this adapter performs no compensation.
	if err := p.objectStore.UploadJSONL(ctx, AccessToken, refs.InputURI, bytes.NewReader(jsonl)); err != nil {
		return nil, VertexProviderError("VERTEX_GCS_UPLOAD_FAILED", "Vertex managed GCS upload failed", nil)
	}

	projectID := strings.TrimSpace(p.opts.ProjectID)
	// A nonblank configured project takes precedence over provider credentials.
	// When absent, ask the provider for its explicit project or parsed service-account
	// project so the submitted endpoint has one resolved identity.
	if projectID == "" {
		projectID = provider.VertexProjectID(vertex.ServiceAccountProjectID)
	}
	if projectID == "" {
		return nil, VertexProviderError("VERTEX_PROJECT_ID_MISSING", "Vertex project id is not configured", nil)
	}
	location := strings.TrimSpace(p.opts.Location)
	if location == "" {
		location = provider.VertexLocation(input.Model)
	}

	req := VertexCreateBatchPredictionJobRequest{
		ProjectID:    projectID,
		Location:     location,
		DisplayName:  VertexBatchDisplayName(input),
		Model:        NormalizeVertexBatchModelPath(input.Model),
		InputConfig:  VertexBatchInputConfig{InstancesFormat: "jsonl", GCSSource: VertexBatchGCSSource{URIs: []string{refs.InputURI}}},
		OutputConfig: VertexBatchOutputConfig{PredictionsFormat: "jsonl", GCSDestination: VertexBatchGCSDestination{OutputURIPrefix: refs.OutputPrefixURI}},
		// Use the submitted key field as the instance identifier so Vertex result
		// rows retain each custom ID needed by the batch indexer to match output
		// with input items.
		InstanceConfig: &VertexBatchInstanceConfig{KeyField: "key"},
	}
	created, err := p.client.CreateBatchPredictionJob(ctx, AccessToken, req)
	if err != nil {
		return nil, MapVertexClientError(err)
	}
	if created == nil || strings.TrimSpace(created.Name) == "" {
		return nil, VertexProviderError("VERTEX_INVALID_RESPONSE", "Vertex batch response is missing job name", nil)
	}
	return &core.BatchProviderJob{
		ProviderJobName:   created.Name,
		ProviderInputRef:  refs.InputURI,
		ProviderOutputRef: refs.OutputPrefixURI,
		RawState:          created.State,
	}, nil
}

// Get polls the stored Vertex job with the bound service-account token and
// maps its observed state. Credentials and job name are checked before the
// exchange; nil server responses produce the fixed invalid-response error.
//
// The returned output reference prefers the server's output prefix, then the
// job's stored provider reference, then its stored GCS output URI. This order
// retains a usable identity when one representation is absent and attaches it
// to the mapped observation for later indexing and download, including when
// the state itself is not yet successful.
func (p *VertexBatchImageProvider) Get(ctx context.Context, job *core.BatchImageJob, provider *Provider) (*core.BatchProviderStatus, error) {
	if err := p.ValidateProvider(provider); err != nil {
		return nil, err
	}
	jobName := core.BatchImageProviderJobName(job)
	if jobName == "" {
		return nil, core.ErrBatchImageProviderMissingJobName
	}
	AccessToken, err := p.AccessToken(ctx, provider)
	if err != nil {
		return nil, MapVertexClientError(err)
	}
	vertexJob, err := p.client.GetBatchPredictionJob(ctx, AccessToken, jobName)
	if err != nil {
		return nil, MapVertexClientError(err)
	}
	if vertexJob == nil {
		return nil, VertexProviderError("VERTEX_INVALID_RESPONSE", "Vertex batch response is empty", nil)
	}
	status := MapVertexBatchState(vertexJob)
	outputRef := strings.TrimSpace(vertexJob.OutputConfig.GCSDestination.OutputURIPrefix)
	// A server observation can omit its output destination. Retain the stored
	// provider reference first, then the older GCS field, so later indexing keeps
	// the job's known output identity.
	if outputRef == "" {
		outputRef = core.BatchImageProviderOutputRef(job)
	}
	if outputRef == "" && job != nil && job.GCSOutputURI != nil {
		outputRef = strings.TrimSpace(*job.GCSOutputURI)
	}
	status.ProviderOutputRef = outputRef
	return status, nil
}

// Cancel requests cancellation of the job's stored Vertex name using the
// service-account token. Missing credentials or job name fail before the
// exchange, and mapped client errors return to the caller for lifecycle handling.
func (p *VertexBatchImageProvider) Cancel(ctx context.Context, job *core.BatchImageJob, provider *Provider) error {
	if err := p.ValidateProvider(provider); err != nil {
		return err
	}
	jobName := core.BatchImageProviderJobName(job)
	if jobName == "" {
		return core.ErrBatchImageProviderMissingJobName
	}
	AccessToken, err := p.AccessToken(ctx, provider)
	if err != nil {
		return MapVertexClientError(err)
	}
	return MapVertexClientError(p.client.CancelBatchPredictionJob(ctx, AccessToken, jobName))
}

// OpenResult lists the job's stored output prefix and returns a lazy JSONL
// reader over lexically sorted object URIs. The GCS URI is used when the provider
// reference is absent; missing objects return a typed error.
//
// Sorting mutates the returned listing slice, which the combined reader retains.
// Callers close that reader, serialize reads with closing, and use nonempty
// read buffers to retain inter-object separators. An object-open error occurs
// during reading after its index advances; treat the combined read as failed,
// since another Read would proceed to the following object.
func (p *VertexBatchImageProvider) OpenResult(ctx context.Context, job *core.BatchImageJob, provider *Provider) (io.ReadCloser, string, error) {
	if err := p.ValidateProvider(provider); err != nil {
		return nil, "", err
	}
	outputRef := core.BatchImageProviderOutputRef(job)
	// A stored job can retain its output only in GCSOutputURI. Use that
	// representation when the preferred provider reference is absent so result
	// listing still addresses the bound output.
	if outputRef == "" && job != nil && job.GCSOutputURI != nil {
		outputRef = strings.TrimSpace(*job.GCSOutputURI)
	}
	if outputRef == "" {
		return nil, "", core.ErrBatchImageProviderMissingResultRef
	}
	AccessToken, err := p.AccessToken(ctx, provider)
	if err != nil {
		return nil, "", MapVertexClientError(err)
	}
	objects, err := p.objectStore.ListJSONLObjects(ctx, AccessToken, outputRef)
	if err != nil {
		return nil, "", VertexProviderError("VERTEX_GCS_LIST_FAILED", "Vertex managed GCS list failed", nil)
	}
	// Sort object URIs before the lazy combined reader borrows this listing.
	// Deterministic order makes indexing and download consume the same object
	// sequence while that reader inserts separators between object streams.
	sort.Strings(objects)
	if len(objects) == 0 {
		return nil, "", VertexProviderError("VERTEX_RESULT_OBJECTS_MISSING", "Vertex result objects are missing", nil)
	}
	return vertex.NewCombinedJSONLReadCloser(ctx, AccessToken, objects, p.objectStore), "application/jsonl", nil
}

// Cleanup validates the service-account record, obtains its token, and deletes
// the selected managed resources. Stored provider references take precedence;
// absent values use the job's GCS input or output URI before the path checks.
//
// Each deletion recomputes the permitted managed path from current options and
// batch identity. All-target cleanup deletes input before output and stops on
// the first error, so earlier deletion can survive a later failure. Unknown
// targets return the target error after credentials and token acquisition have
// succeeded, and retained references allow retries.
func (p *VertexBatchImageProvider) Cleanup(ctx context.Context, job *core.BatchImageJob, provider *Provider, target core.CleanupTarget) error {
	if err := p.ValidateProvider(provider); err != nil {
		return err
	}
	AccessToken, err := p.AccessToken(ctx, provider)
	if err != nil {
		return MapVertexClientError(err)
	}
	inputRef := core.BatchImageProviderInputRef(job)
	outputRef := core.BatchImageProviderOutputRef(job)
	// Stored provider references take precedence over the GCS fields.
	// Fill absent references from those fields before applying managed-path checks,
	// so cleanup can recognize jobs using either stored representation.
	if job != nil {
		if inputRef == "" && job.GCSInputURI != nil {
			inputRef = strings.TrimSpace(*job.GCSInputURI)
		}
		if outputRef == "" && job.GCSOutputURI != nil {
			outputRef = strings.TrimSpace(*job.GCSOutputURI)
		}
	}

	switch target {
	case core.CleanupTargetInput:
		return p.DeleteManagedInput(ctx, AccessToken, job, inputRef)
	case core.CleanupTargetOutput:
		return p.DeleteManagedOutput(ctx, AccessToken, job, outputRef)
	// Delete the managed input before the output prefix.
	// Stop on the input error so the caller handles partial cleanup using the same
	// stored references before attempting the remaining target.
	case core.CleanupTargetAll:
		if err := p.DeleteManagedInput(ctx, AccessToken, job, inputRef); err != nil {
			return err
		}
		return p.DeleteManagedOutput(ctx, AccessToken, job, outputRef)
	default:
		return core.ErrUnsupportedCleanupTarget
	}
}

// ValidateProvider accepts Gemini service-account records with parseable
// required credential fields. Wrong platform or type returns the unsupported
// error; missing or malformed key input returns the missing-service-account
// application error before token exchange.
func (p *VertexBatchImageProvider) ValidateProvider(provider *Provider) error {
	if provider == nil || provider.Platform != PlatformGemini || provider.Type != ProviderTypeServiceAccount {
		return core.ErrBatchImageProviderUnsupportedProvider
	}
	// MapVertexClientError recognizes the shared missing-service-account error
	// and passes it through. Return that error for missing or malformed input so
	// later caller handling retains the batch validation reason.
	if _, err := provideradapter.ParseVertexServiceAccountKey(provider); err != nil {
		return core.ErrBatchImageProviderMissingServiceAccount
	}
	return nil
}

// AccessToken delegates service-account authentication to the provider
// credential coordinator. It parses credentials before cache lookup, uses the
// record's loaded proxy for exchange, and performs direct exchange when the
// adapter has no token cache.
func (p *VertexBatchImageProvider) AccessToken(ctx context.Context, provider *Provider) (string, error) {
	return provideradapter.VertexServiceAccountAccessToken(ctx, p.tokenCache, provider)
}

// DeleteManagedInput skips a blank reference and otherwise requires the exact
// managed input URI for the job. A mismatched path returns the unsafe-cleanup
// error before object deletion; client failures are mapped.
func (p *VertexBatchImageProvider) DeleteManagedInput(ctx context.Context, accessToken string, job *core.BatchImageJob, uri string) error {
	if strings.TrimSpace(uri) == "" {
		return nil
	}
	// Compare the stored URI with the generated input object before deleting it.
	// A changed bucket or job prefix fails this check, preventing cleanup from
	// using an arbitrary stored input path.
	if !p.IsSafeManagedInput(job, uri) {
		return core.ErrBatchImageProviderUnsafeCleanupPath
	}
	return MapVertexClientError(p.objectStore.DeleteObject(ctx, accessToken, uri))
}

// DeleteManagedOutput skips a blank reference and otherwise requires the
// job's managed output prefix or a string beneath it. Unsafe paths fail before
// prefix deletion; client failures are mapped and partial deletion can require
// retry.
func (p *VertexBatchImageProvider) DeleteManagedOutput(ctx context.Context, accessToken string, job *core.BatchImageJob, uri string) error {
	if strings.TrimSpace(uri) == "" {
		return nil
	}
	// Compare the stored URI with the generated trailing-slash output prefix
	// before deleting a prefix. That prefix scopes cleanup to this job's managed
	// output location and its literal descendants.
	if !p.IsSafeManagedOutput(job, uri) {
		return core.ErrBatchImageProviderUnsafeCleanupPath
	}
	return MapVertexClientError(p.objectStore.DeletePrefix(ctx, accessToken, uri))
}

// IsSafeManagedInput compares the trimmed URI with the generated input object
// for a nonnil job and batch ID. Invalid identity or managed configuration
// returns false before cleanup can select a deletion target.
func (p *VertexBatchImageProvider) IsSafeManagedInput(job *core.BatchImageJob, uri string) bool {
	if job == nil || strings.TrimSpace(job.BatchID) == "" {
		return false
	}
	// Reference derivation validates batch identity and managed configuration.
	// A failure produces an unsafe decision here, so DeleteManagedInput returns
	// its cleanup-path error before asking the object store to delete anything.
	refs, err := p.ManagedRefs(job.BatchID)
	return err == nil && strings.TrimSpace(uri) == refs.InputURI
}

// IsSafeManagedOutput checks the trimmed URI against the generated
// trailing-slash output prefix for the job. Invalid identity or managed
// configuration returns false; prefix matching admits that prefix and its
// literal descendants.
func (p *VertexBatchImageProvider) IsSafeManagedOutput(job *core.BatchImageJob, uri string) bool {
	if job == nil || strings.TrimSpace(job.BatchID) == "" {
		return false
	}
	// Reference derivation validates batch identity and managed configuration.
	// A failure produces an unsafe decision here, so DeleteManagedOutput rejects
	// its prefix before asking the object store to delete any matching objects.
	refs, err := p.ManagedRefs(job.BatchID)
	return err == nil && strings.HasPrefix(strings.TrimSpace(uri), refs.OutputPrefixURI)
}

// ManagedRefs returns input and output URIs under one managed prefix for a
// valid batch-shaped ID. It trims the configured bucket, rejects missing or
// URI-shaped bucket input, and requires the generated prefix to contain the
// batch ID.
//
// Both resources use that prefix: input names requests.jsonl, while output
// ends with a slash so cleanup can select its descendants. The same derivation
// is used when validating stored cleanup references; changing current bucket,
// environment, or prefix options can make older job paths fail that comparison.
func (p *VertexBatchImageProvider) ManagedRefs(batchID string) (VertexManagedRefs, error) {
	batchID = strings.TrimSpace(batchID)
	if !core.IsValidBatchImageID(batchID) {
		return VertexManagedRefs{}, core.BatchImageProviderInputError("valid batch_id is required")
	}
	bucket := strings.Trim(strings.TrimSpace(p.opts.ManagedGCSBucket), "/")
	if bucket == "" || strings.Contains(bucket, "://") {
		return VertexManagedRefs{}, VertexProviderError("VERTEX_MANAGED_GCS_BUCKET_MISSING", "Vertex managed GCS bucket is not configured", nil)
	}
	prefix := BuildVertexManagedGCSPrefix(p.opts.ManagedGCSPrefix, p.opts.Environment, batchID)
	// Require the batch identity in the generated prefix before deriving input
	// and output references. A template omitting it could assign different jobs
	// the same managed resource location.
	if !strings.Contains(prefix, batchID) {
		return VertexManagedRefs{}, core.BatchImageProviderInputError("managed GCS prefix must contain batch_id")
	}
	base := "gs://" + bucket + "/" + strings.Trim(prefix, "/")
	return VertexManagedRefs{
		Prefix:          strings.Trim(prefix, "/"),
		InputURI:        base + "/input/requests.jsonl",
		OutputPrefixURI: base + "/output/",
	}, nil
}

// BuildVertexManagedGCSPrefix substitutes sanitized environment and batch
// segments into the managed template and trims surrounding slashes. A blank
// template selects the standard template; this helper leaves batch-ID inclusion
// validation to ManagedRefs.
func BuildVertexManagedGCSPrefix(template, env, batchID string) string {
	template = strings.Trim(strings.TrimSpace(template), "/")
	if template == "" {
		template = DefaultVertexManagedGCSPrefix
	}
	env = SanitizeVertexGCSPathSegment(env)
	batchID = SanitizeVertexGCSPathSegment(batchID)
	// Substitute sanitized segments into the template before deriving GCS URIs.
	// The caller validates batch-ID inclusion afterward, because this helper can
	// also process templates containing no placeholders.
	prefix := strings.ReplaceAll(template, "{env}", env)
	prefix = strings.ReplaceAll(prefix, "{batch_id}", batchID)
	return strings.Trim(prefix, "/")
}

// SanitizeVertexGCSPathSegment keeps ASCII letters, digits, hyphens,
// underscores, and dots, replacing other runes with hyphens. Blank input selects
// default; different input spellings can produce the same segment, so ManagedRefs
// also validates batch identity.
func SanitizeVertexGCSPathSegment(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return "default"
	}
	var b strings.Builder
	for _, r := range v {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			_, _ = b.WriteRune(r)
		case r == '-', r == '_', r == '.':
			_, _ = b.WriteRune(r)
		// Replace each unsupported rune with a hyphen while retaining allowed ASCII
		// path characters. This transformation can merge spellings, so generated paths
		// still need the caller's batch-identity validation.
		default:
			_ = b.WriteByte('-')
		}
	}
	return b.String()
}

// VertexBatchDisplayName selects a trimmed explicit display name, then the
// trimmed batch ID with a tokenrouter prefix, then a fixed default. The
// result labels the submitted Vertex job for upstream identification.
func VertexBatchDisplayName(input core.BatchImageInput) string {
	if v := strings.TrimSpace(input.DisplayName); v != "" {
		return v
	}
	if v := strings.TrimSpace(input.BatchID); v != "" {
		return "tokenrouter-" + v
	}
	return "tokenrouter-image-batch"
}

// MapVertexBatchState maps a nonnil Vertex job into a processor observation
// while retaining its trimmed raw state. Known terminal states set Done and
// clear the polling suggestion; pending, queued, running, and paused states
// remain available for polling.
//
// Unknown states stay running unless a nonempty error message establishes
// failure. Error Status overrides generated codes and Message supplies trimmed
// diagnostics, so a status alone can add an error code without ending an
// unknown-state observation. ProviderProcessor chooses its next lifecycle
// action from InternalState; Done records terminal recognition in the result.
func MapVertexBatchState(job *VertexBatchPredictionJob) *core.BatchProviderStatus {
	state := strings.TrimSpace(job.State)
	status := &core.BatchProviderStatus{
		RawState:              state,
		InternalState:         core.BatchProviderStateRunning,
		SuggestedRequeueAfter: DefaultVertexBatchRequeueAfter,
	}
	switch strings.ToUpper(state) {
	case "JOB_STATE_PENDING", "JOB_STATE_QUEUED":
		status.InternalState = core.BatchProviderStateQueued
	case "JOB_STATE_RUNNING", "JOB_STATE_PAUSED":
		status.InternalState = core.BatchProviderStateRunning
	case "JOB_STATE_SUCCEEDED":
		status.InternalState = core.BatchProviderStateSucceeded
		status.Done = true
		status.SuggestedRequeueAfter = 0
	case "JOB_STATE_FAILED":
		status.InternalState = core.BatchProviderStateFailed
		status.Done = true
		status.ErrorCode = "VERTEX_BATCH_FAILED"
		status.SuggestedRequeueAfter = 0
	case "JOB_STATE_CANCELLED":
		status.InternalState = core.BatchProviderStateCancelled
		status.Done = true
		status.ErrorCode = "VERTEX_BATCH_CANCELLED"
		status.SuggestedRequeueAfter = 0
	case "JOB_STATE_EXPIRED":
		status.InternalState = core.BatchProviderStateExpired
		status.Done = true
		status.ErrorCode = "VERTEX_BATCH_EXPIRED"
		status.SuggestedRequeueAfter = 0
	// Unknown state names retain polling unless a meaningful error message is
	// present. An upstream Status alone can supply a diagnostic code while leaving
	// that observation running.
	default:
		if job.Error != nil && strings.TrimSpace(job.Error.Message) != "" {
			status.InternalState = core.BatchProviderStateFailed
			status.Done = true
			status.ErrorCode = "VERTEX_BATCH_FAILED"
			status.SuggestedRequeueAfter = 0
		}
	}
	// Use the upstream symbolic Status when present after choosing the internal
	// state. Keeping code selection separate lets diagnostics accompany an
	// observation whose unknown state still needs polling.
	if job.Error != nil {
		if code := strings.TrimSpace(job.Error.Status); code != "" {
			status.ErrorCode = code
		}
		status.ErrorMessage = strings.TrimSpace(job.Error.Message)
	}
	return status
}

// VertexProviderError creates a bad-gateway error using caller-safe reason and
// message text. A nonnil cause is attached and appears in Error diagnostics;
// callers select public messages separately from any sensitive cause.
func VertexProviderError(reason, message string, cause error) error {
	err := infraerrors.New(infraerrors.CategoryBadGateway, reason, message)
	if cause != nil {
		return err.WithCause(cause)
	}
	return err
}

// MapVertexClientError preserves recognized batch validation errors and maps
// Vertex HTTP statuses to fixed application messages. Nil returns nil.
// Recognized API failures discard upstream diagnostics; other failures retain
// their cause, which can appear in Error while the application message stays
// fixed.
func MapVertexClientError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, core.ErrBatchImageProviderMissingServiceAccount) ||
		errors.Is(err, core.ErrBatchImageProviderMissingJobName) ||
		errors.Is(err, core.ErrBatchImageProviderMissingResultRef) ||
		errors.Is(err, core.ErrBatchImageProviderUnsafeCleanupPath) ||
		errors.Is(err, core.ErrUnsupportedCleanupTarget) {
		return err
	}
	// Map recognized HTTP failures to fixed public messages and omit their
	// upstream diagnostic cause. Those diagnostics can contain credential material,
	// as the permission-error test demonstrates with its injected private key.
	if apiErr, ok := errors.AsType[*VertexAPIError](err); ok {
		switch apiErr.StatusCode {
		case http.StatusUnauthorized:
			return VertexProviderError("VERTEX_AUTH_FAILED", "Vertex authentication failed", nil)
		case http.StatusForbidden:
			return VertexProviderError("VERTEX_PERMISSION_DENIED", "Vertex permission denied", nil)
		case http.StatusTooManyRequests:
			return VertexProviderError("VERTEX_RATE_LIMITED", "Vertex rate limit exceeded", nil)
		case http.StatusNotFound:
			return VertexProviderError("VERTEX_BATCH_NOT_FOUND", "Vertex batch resource was not found", nil)
		default:
			return VertexProviderError("VERTEX_INVALID_RESPONSE", "Vertex API request failed", nil)
		}
	}
	// Retain other failures as diagnostic causes with a fixed application message.
	// BatchImageError reads that Message; callers exposing Error text also expose
	// the attached cause through ApplicationError's diagnostic formatting.
	return VertexProviderError("VERTEX_INVALID_RESPONSE", "Vertex API request failed", err)
}

// NormalizeVertexBatchModelPath delegates trimmed model resource selection to
// the upstream client. Publisher and project resource paths pass through; other
// names receive the Google publisher-model prefix, including an empty supplied
// name.
func NormalizeVertexBatchModelPath(model string) string {
	return vertex.NormalizeVertexBatchModelPath(model)
}

// NewVertexBatchHTTPClient constructs the upstream job wrapper over its
// normalized base URL and borrowed HTTP client. Blank base selects Google job
// endpoints during requests; nil client selects the shared batch HTTP client.
func NewVertexBatchHTTPClient(baseURL string, client *http.Client) *VertexBatchHTTPClient {
	return vertex.NewVertexBatchHTTPClient(baseURL, client)
}

// NewVertexGCSObjectStore constructs the upstream object wrapper over its
// normalized GCS base and borrowed HTTP client. Blank base selects
// storage.googleapis.com, while nil client selects the shared batch HTTP client.
func NewVertexGCSObjectStore(baseURL string, client *http.Client) *VertexGCSObjectStore {
	return vertex.NewVertexGCSObjectStore(baseURL, client)
}

// BuildVertexBatchJSONL converts references and normalizes MIME labels before
// delegating Vertex JSONL encoding. The conversion supplies model, keys,
// prompts, and reference sources; response MIME type, aspect ratio, image size,
// and metadata have no encoding here. The encoder validates a nonblank model
// and trimmed keys and prompts, rejects duplicate keys or ambiguous sources,
// and reads borrowed inline bytes synchronously.
func BuildVertexBatchJSONL(input core.BatchImageInput) ([]byte, error) {
	// Carry each custom ID and ordered reference sources to the upstream encoder
	// while normalizing MIME labels. Inline byte slices remain borrowed until that
	// synchronous serialization has finished.
	value := vertex.BatchJSONLInput{Model: input.Model, Items: make([]vertex.BatchJSONLItem, len(input.Items))}
	for i, item := range input.Items {
		value.Items[i] = vertex.BatchJSONLItem{CustomID: item.CustomID, Prompt: item.Prompt, ReferenceImages: make([]vertex.BatchReference, len(item.ReferenceImages))}
		for j, ref := range item.ReferenceImages {
			value.Items[i].ReferenceImages[j] = vertex.BatchReference{MimeType: core.NormalizeBatchImageReferenceMimeType(ref.MimeType), Data: ref.Data, FileURI: ref.FileURI}
		}
	}
	return vertex.BuildVertexBatchJSONL(value, core.BatchImageProviderInputError)
}
