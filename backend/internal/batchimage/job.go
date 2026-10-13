package batchimage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// A batch job keeps its selected provider, model identities and funding
// snapshots while request authentication and live configuration continue
// changing. The expanded custom IDs identify provider result lines; each item
// records result metadata and failure details for later listing and
// downloads. PostgreSQL owns these records, and queue reservations carry
// their public batch IDs.
//
// Transitions allow polling to skip directly from submitted into indexing
// when the first observation is already complete. Settling precedes captured
// completion, failed or cancelled tasks can release holds, and output
// deletion remains possible after a terminal outcome. Repository operations
// expose the conditional status checks needed by submission, indexing,
// recovery and settlement so a stale worker can observe a conflict and retry
// the current state.

// Backend and status labels identify persisted execution choices and
// lifecycle outcomes used by public rendering, processing and guarded
// repository transitions.
const (
	BatchImageProviderGeminiAPI = "gemini_api" // Gemini file batch backend
	BatchImageProviderVertex    = "vertex"     // Vertex prediction batch backend

	// Persisted job statuses control guarded progression from saved submission
	// through indexed capture, terminal failure and completed output deletion.
	BatchImageJobStatusCreated       = "created"        // job saved before funding
	BatchImageJobStatusUploading     = "uploading"      // upstream submission in progress
	BatchImageJobStatusSubmitted     = "submitted"      // upstream job reference saved
	BatchImageJobStatusRunning       = "running"        // upstream execution was observed
	BatchImageJobStatusIndexing      = "indexing"       // provider results being reconciled
	BatchImageJobStatusSettling      = "settling"       // indexed counts awaiting capture
	BatchImageJobStatusCompleted     = "completed"      // funding and completion saved
	BatchImageJobStatusFailed        = "failed"         // job failed during progression
	BatchImageJobStatusCancelled     = "cancelled"      // job cancellation became terminal
	BatchImageJobStatusOutputDeleted = "output_deleted" // retained output was deleted

	// Stored item statuses distinguish submission placeholders from indexed
	// image success, item failure and recorded cancellation when listing or
	// downloading results.
	BatchImageItemStatusPending   = "pending"   // submitted result awaits indexing
	BatchImageItemStatusSuccess   = "success"   // provider row contains images
	BatchImageItemStatusFailed    = "failed"    // indexed row reports item failure
	BatchImageItemStatusCancelled = "cancelled" // item records cancellation outcome
)

// Batch errors provide shared application categories for missing resources,
// invalid lifecycle operations, submission constraints, settlement failures,
// downloads and cleanup.
var (
	ErrBatchImageJobNotFound = billing.ErrTaskNotFound
	ErrBatchImageJobExists   = infraerrors.New(infraerrors.CategoryConflict, "BATCH_IMAGE_JOB_EXISTS", "batch image job already exists")
	ErrBatchImageItemExists  = infraerrors.New(infraerrors.CategoryConflict, "BATCH_IMAGE_ITEM_EXISTS", "batch image item already exists")

	ErrBatchImageInvalidTransition = infraerrors.New(infraerrors.CategoryBadRequest, "BATCH_IMAGE_INVALID_TRANSITION", "invalid batch image job status transition")
	ErrBatchImageInvalidProvider   = infraerrors.New(infraerrors.CategoryBadRequest, "BATCH_IMAGE_INVALID_PROVIDER", "invalid batch image platform")

	ErrBatchImageMissingProviderJobName = infraerrors.New(infraerrors.CategoryBadRequest, "BATCH_IMAGE_MISSING_PROVIDER_JOB_NAME", "batch image platform job name is missing")
	ErrBatchImageMissingProviderID      = infraerrors.New(infraerrors.CategoryBadRequest, "BATCH_IMAGE_MISSING_PROVIDER_ID", "batch image provider id is missing")
	ErrBatchImageUnsupportedProvider    = infraerrors.New(infraerrors.CategoryBadRequest, "BATCH_IMAGE_UNSUPPORTED_PROVIDER", "unsupported batch image platform")
	ErrBatchImageIndexOutputMissing     = infraerrors.New(infraerrors.CategoryBadGateway, "BATCH_IMAGE_INDEX_OUTPUT_MISSING", "batch image platform output is missing")
	ErrBatchImageIndexParseFailed       = infraerrors.New(infraerrors.CategoryBadGateway, "BATCH_IMAGE_INDEX_PARSE_FAILED", "batch image platform output parse failed")
	ErrBatchImageIndexNoResultLines     = infraerrors.New(infraerrors.CategoryBadGateway, "BATCH_IMAGE_INDEX_NO_RESULT_LINES", "batch image platform output has no result lines")
	ErrBatchImageDuplicateCustomID      = infraerrors.New(infraerrors.CategoryBadGateway, "DUPLICATE_CUSTOM_ID_IN_OUTPUT", "batch image platform output contains duplicate custom id")
	ErrBatchImageIndexStateConflict     = infraerrors.New(infraerrors.CategoryConflict, "BATCH_IMAGE_INDEX_STATE_CONFLICT", "batch image job is no longer in indexing state")

	ErrBatchImageSettlementInvalidStatus     = infraerrors.New(infraerrors.CategoryBadRequest, "BATCH_IMAGE_SETTLEMENT_INVALID_STATUS", "batch image job is not ready for settlement")
	ErrBatchImageSettlementManifestConflict  = infraerrors.New(infraerrors.CategoryConflict, "BATCH_IMAGE_SETTLEMENT_MANIFEST_CONFLICT", "batch image settlement manifest hash conflict")
	ErrBatchImageSettlementPricingMissing    = billing.ErrImageTaskPricingMissing
	ErrBatchImageSettlementBillingFailed     = infraerrors.New(infraerrors.CategoryBadGateway, "BATCH_IMAGE_SETTLEMENT_BILLING_FAILED", "batch image settlement billing failed")
	ErrBatchImageAlreadySettled              = infraerrors.New(infraerrors.CategoryConflict, "BATCH_IMAGE_ALREADY_SETTLED", "batch image job is already settled")
	ErrBatchImageSettlementMissingAPIKeyID   = infraerrors.New(infraerrors.CategoryBadRequest, "BATCH_IMAGE_SETTLEMENT_MISSING_API_KEY_ID", "batch image settlement api key id is missing")
	ErrBatchImageSettlementMissingProviderID = infraerrors.New(infraerrors.CategoryBadRequest, "BATCH_IMAGE_SETTLEMENT_MISSING_PROVIDER_ID", "batch image settlement provider id is missing")
	ErrBatchImageSettlementInvalidCounts     = infraerrors.New(infraerrors.CategoryBadRequest, "BATCH_IMAGE_SETTLEMENT_INVALID_COUNTS", "batch image settlement counts are invalid")
	ErrBatchImageSettlementCostExceedsHold   = billing.ErrTaskSettlementCostExceedsHold
	ErrBatchImageBillingHoldFailed           = infraerrors.New(infraerrors.CategoryBadGateway, "BATCH_IMAGE_BILLING_HOLD_FAILED", "batch image balance hold failed")
	ErrBatchImageInsufficientBalance         = billing.ErrTaskInsufficientBalance

	ErrBatchImageDisabled                   = infraerrors.New(infraerrors.CategoryNotFound, "BATCH_IMAGE_DISABLED", "batch image API is disabled")
	ErrBatchImageGroupDisabled              = infraerrors.New(infraerrors.CategoryForbidden, "BATCH_IMAGE_GROUP_DISABLED", "batch image API is disabled for this group")
	ErrBatchImageInvalidModel               = infraerrors.New(infraerrors.CategoryBadRequest, "BATCH_IMAGE_INVALID_MODEL", "batch image model is required")
	ErrBatchImageNoProviderAvailable        = infraerrors.New(infraerrors.CategoryBadGateway, "BATCH_IMAGE_NO_PROVIDER_AVAILABLE", "no compatible batch image provider is available")
	ErrBatchImageInvalidItems               = infraerrors.New(infraerrors.CategoryBadRequest, "BATCH_IMAGE_INVALID_ITEMS", "batch image items are invalid")
	ErrBatchImageDuplicateCustomIDInRequest = infraerrors.New(infraerrors.CategoryBadRequest, "BATCH_IMAGE_DUPLICATE_CUSTOM_ID", "batch image custom ids must be unique")
	ErrBatchImagePromptTooLong              = infraerrors.New(infraerrors.CategoryBadRequest, "BATCH_IMAGE_PROMPT_TOO_LONG", "batch image prompt is too long")
	ErrBatchImageInvalidReferenceImage      = infraerrors.New(infraerrors.CategoryBadRequest, "BATCH_IMAGE_INVALID_REFERENCE_IMAGE", "batch image reference image is invalid")
	ErrBatchImageTooManyReferenceImages     = infraerrors.New(infraerrors.CategoryBadRequest, "BATCH_IMAGE_TOO_MANY_REFERENCE_IMAGES", "too many batch image reference images for this model")
	ErrBatchImageReferenceImagesTooLarge    = infraerrors.New(infraerrors.CategoryBadRequest, "BATCH_IMAGE_REFERENCE_IMAGES_TOO_LARGE", "batch image reference images are too large")
	ErrBatchImageTooManyOutputImages        = infraerrors.New(infraerrors.CategoryBadRequest, "BATCH_IMAGE_TOO_MANY_OUTPUT_IMAGES", "too many batch image output images")
	ErrBatchImageProviderSubmitFailed       = infraerrors.New(infraerrors.CategoryBadGateway, "BATCH_IMAGE_PROVIDER_SUBMIT_FAILED", "batch image platform submit failed")
	ErrBatchImageQueueFailed                = infraerrors.New(infraerrors.CategoryBadGateway, "BATCH_IMAGE_QUEUE_FAILED", "batch image queue failed")
	ErrBatchImageIdempotencyConflict        = infraerrors.New(infraerrors.CategoryConflict, "BATCH_IMAGE_IDEMPOTENCY_CONFLICT", "idempotency key reused with different batch image request")
	ErrBatchImageCancelFailed               = infraerrors.New(infraerrors.CategoryBadGateway, "BATCH_IMAGE_CANCEL_FAILED", "batch image cancel failed")
	ErrBatchImageVertexGCSBucketMissing     = infraerrors.New(infraerrors.CategoryBadGateway, "BATCH_IMAGE_VERTEX_GCS_BUCKET_MISSING", "Vertex managed GCS bucket is not configured")

	ErrBatchImageNotReady                 = infraerrors.New(infraerrors.CategoryConflict, "BATCH_IMAGE_NOT_READY", "batch image job is not completed")
	ErrBatchImageOutputDeleted            = infraerrors.New(infraerrors.Category(410), "BATCH_IMAGE_OUTPUT_DELETED", "batch image output has been deleted")
	ErrBatchImageItemNotFound             = infraerrors.New(infraerrors.CategoryNotFound, "BATCH_IMAGE_ITEM_NOT_FOUND", "batch image item not found")
	ErrBatchImageItemFailed               = infraerrors.New(infraerrors.CategoryConflict, "BATCH_IMAGE_ITEM_FAILED", "batch image item did not succeed")
	ErrBatchImageResultMissing            = infraerrors.New(infraerrors.CategoryInternalServer, "BATCH_IMAGE_RESULT_MISSING", "batch image result is missing")
	ErrBatchImageDownloadLimited          = infraerrors.New(infraerrors.CategoryTooManyRequests, "BATCH_IMAGE_DOWNLOAD_LIMITED", "too many batch image downloads")
	ErrBatchImageDownloadFailed           = infraerrors.New(infraerrors.CategoryInternalServer, "BATCH_IMAGE_DOWNLOAD_FAILED", "batch image download failed")
	ErrBatchImageDownloadTooLarge         = infraerrors.New(infraerrors.CategoryBadRequest, "BATCH_IMAGE_DOWNLOAD_TOO_LARGE", "batch image download is too large")
	ErrBatchImageItemImageIndexOutOfRange = infraerrors.New(infraerrors.CategoryBadRequest, "BATCH_IMAGE_ITEM_IMAGE_INDEX_OUT_OF_RANGE", "batch image item image index is out of range")
	ErrBatchImageZipTooManyItems          = infraerrors.New(infraerrors.CategoryBadRequest, "BATCH_IMAGE_ZIP_TOO_MANY_ITEMS", "batch image ZIP contains too many items; use single item downloads")
	ErrBatchImageOutputDeleteNotReady     = infraerrors.New(infraerrors.CategoryConflict, "BATCH_IMAGE_OUTPUT_DELETE_NOT_READY", "batch image output can only be deleted after completion")
	ErrBatchImageRecordDeleteNotReady     = infraerrors.New(infraerrors.CategoryConflict, "BATCH_IMAGE_RECORD_DELETE_NOT_READY", "batch image record can only be deleted after the job finishes")
	ErrBatchImageCleanupFailed            = infraerrors.New(infraerrors.CategoryBadGateway, "BATCH_IMAGE_CLEANUP_FAILED", "batch image cleanup failed")
	ErrBatchImageCleanupUnsafePath        = infraerrors.New(infraerrors.CategoryBadRequest, "BATCH_IMAGE_CLEANUP_UNSAFE_PATH", "batch image cleanup path is unsafe")
	ErrBatchImageProviderCleanupFailed    = infraerrors.New(infraerrors.CategoryBadGateway, "BATCH_IMAGE_PROVIDER_CLEANUP_FAILED", "batch image platform cleanup failed")
)

// BatchImageJob combines mutable persisted lifecycle state with submission-time
// model and pricing snapshots used by asynchronous work.
type BatchImageJob struct {
	ID            int64  // database record identity
	BatchID       string // public batch identifier
	UserID        int64  // submitting user identity
	BillingUserID int64  // funding payer identity
	TeamID        *int64 // optional originating team
	APIKeyID      *int64 // submitting API key
	ProviderID    *int64 // bound execution provider
	GroupID       *int64 // selected routing group
	// BillingMode and PreferredSubscriptionID retain the selected funding source
	// from submission. BuildHoldCommand uses this pair across reserve, capture
	// and release after live key settings change.
	BillingMode             string // submitted funding mode
	PreferredSubscriptionID *int64 // submitted preferred subscription
	Platform                string // batch execution backend
	// Model, RequestedModel and InternalModel retain upstream, client and
	// internal routing identities. Public rendering uses the client name, while
	// settlement reports internal and upstream names separately.
	Model             string  // final upstream model
	RequestedModel    string  // original client model
	InternalModel     string  // internal routing model
	TaskName          string  // user visible title
	ParentBatchID     *string // root related batch
	Status            string  // saved job lifecycle stage
	ProviderJobName   *string // internal provider job
	ProviderInputRef  *string // internal input reference
	ProviderOutputRef *string // internal output reference
	GCSInputURI       *string // Vertex input URI
	GCSOutputURI      *string // Vertex output URI

	ItemCount      int // expanded request count
	SuccessCount   int // successful item count
	FailCount      int // failed item count
	CancelledCount int // cancelled item count

	EstimatedCost float64 // USD estimated charge
	// HoldAmount records the reserved USD total when present. Funding and legacy
	// settlement use EstimatedCost as the compatible amount when this pointer
	// has no value.
	HoldAmount *float64 // USD reserved total
	ActualCost *float64 // USD captured charge
	// BalanceHoldAmount and SubscriptionHoldAllocations retain the committed
	// funding sources. Capture and release consume these amounts and copied
	// identity values to reconcile the original task reservation.
	BalanceHoldAmount           float64                     // USD reserved balance
	SubscriptionHoldAllocations []billing.BillingAllocation // reserved subscription funding
	// SubscriptionRateMultiplier and BalanceRateMultiplier are distinct source
	// inputs for newer funding commands. BuildHoldCommand scales both with provider
	// and hold multipliers, separately from the group rate used for estimates.
	SubscriptionRateMultiplier float64 // subscription group multiplier
	BalanceRateMultiplier      float64 // wallet group multiplier
	// PlanGroupRateEnabled permits billing to select a subscription plan's group
	// rate. BuildHoldCommand negates it for the newer snapshot command option.
	PlanGroupRateEnabled bool // plan rate selection
	// AllowanceReserved records whether key and member quotas include the hold.
	// Billing capture and release use this marker to remove unused reservation
	// accounting in their funding transaction.
	AllowanceReserved       bool    // quota reservation marker
	BaseUnitPrice           float64 // USD base image price
	GroupRateMultiplier     float64 // estimated group multiplier
	ProviderRateMultiplier  float64 // provider price multiplier
	BatchDiscountMultiplier float64 // settlement discount multiplier
	HoldMultiplier          float64 // reservation price multiplier
	BillableUnitPrice       float64 // USD estimated item charge
	HoldUnitPrice           float64 // USD estimated item reservation
	// PricingSnapshotVersion selects settlement calculation. Zero resolves a
	// live unit price, version one uses the stored billable price, and newer
	// versions pass base amounts and source rates to billing.
	PricingSnapshotVersion int     // stored pricing format
	Currency               string  // funding currency code
	HoldID                 *string // reservation action key

	IdempotencyKey *string // owner scoped retry key
	RequestHash    *string // normalized request fingerprint
	ManifestHash   *string // settlement result fingerprint
	SessionID      *string // optional client session

	RetryCount int // recorded settlement failures
	Version    int // repository update generation
	// OutputExpiresAt makes completed outputs eligible for the periodic cleanup
	// scan. Settlement fills it when absent and retains an already persisted
	// expiry during later completion attempts.
	OutputExpiresAt *time.Time // scheduled output expiry
	InputDeletedAt  *time.Time // first input deletion
	OutputDeletedAt *time.Time // first output deletion
	DownloadedAt    *time.Time // first download acknowledgement
	UserDeletedAt   *time.Time // first record hiding

	LastErrorCode    *string // latest diagnostic code
	LastErrorMessage *string // latest diagnostic message

	CreatedAt   time.Time  // job creation time
	UpdatedAt   time.Time  // latest persisted activity
	SubmittedAt *time.Time // provider submission time
	StartedAt   *time.Time // first running observation
	FinishedAt  *time.Time // first terminal transition
	SettledAt   *time.Time // successful settlement time
}

// CreateBatchImageJobParams supplies the initial job record and its
// submission-time funding and model snapshots.
type CreateBatchImageJobParams struct {
	BatchID       string // public batch identifier
	UserID        int64  // submitting user identity
	BillingUserID int64  // funding payer identity
	TeamID        *int64 // optional originating team
	APIKeyID      *int64 // submitting API key
	ProviderID    *int64 // bound execution provider
	GroupID       *int64 // selected routing group
	// BillingMode and PreferredSubscriptionID supply the funding source snapshot
	// written before reserve. The preferred subscription identity is copied by
	// Submit so later key edits do not replace it.
	BillingMode             string // submitted funding mode
	PreferredSubscriptionID *int64 // submitted preferred subscription
	Platform                string // batch execution backend
	// Model, RequestedModel and InternalModel are copied into the submitted
	// record together. Their separate stages let public listing and asynchronous
	// usage reporting retain the correct model identities.
	Model             string  // final upstream model
	RequestedModel    string  // original client model
	InternalModel     string  // internal routing model
	TaskName          string  // user visible title
	ParentBatchID     *string // root related batch
	Status            string  // initial job lifecycle stage
	ProviderJobName   *string // internal provider job
	ProviderInputRef  *string // internal input reference
	ProviderOutputRef *string // internal output reference
	GCSInputURI       *string // Vertex input URI
	GCSOutputURI      *string // Vertex output URI

	ItemCount      int // expanded request count
	SuccessCount   int // successful item count
	FailCount      int // failed item count
	CancelledCount int // cancelled item count

	EstimatedCost               float64                     // USD estimated charge
	HoldAmount                  *float64                    // USD reserved total
	ActualCost                  *float64                    // USD captured charge
	BalanceHoldAmount           float64                     // USD reserved balance
	SubscriptionHoldAllocations []billing.BillingAllocation // reserved subscription funding
	// SubscriptionRateMultiplier and BalanceRateMultiplier copy the separate
	// pricing-snapshot source rates into the new record. Newer funding commands
	// use those saved values after live group settings can change.
	SubscriptionRateMultiplier float64 // subscription group multiplier
	BalanceRateMultiplier      float64 // wallet group multiplier
	// PlanGroupRateEnabled supplies the submitted plan-rate selection flag.
	// BuildHoldCommand later converts it into the command's disable-plan-rate
	// option for newer snapshots.
	PlanGroupRateEnabled    bool    // plan rate selection
	BaseUnitPrice           float64 // USD base image price
	GroupRateMultiplier     float64 // estimated group multiplier
	ProviderRateMultiplier  float64 // provider price multiplier
	BatchDiscountMultiplier float64 // settlement discount multiplier
	HoldMultiplier          float64 // reservation price multiplier
	BillableUnitPrice       float64 // USD estimated item charge
	HoldUnitPrice           float64 // USD estimated item reservation
	PricingSnapshotVersion  int     // stored pricing format
	Currency                string  // funding currency code
	HoldID                  *string // reservation action key

	IdempotencyKey *string // owner scoped retry key
	RequestHash    *string // normalized request fingerprint
	ManifestHash   *string // settlement result fingerprint
	SessionID      *string // optional client session

	RetryCount int // recorded settlement failures

	OutputExpiresAt *time.Time // scheduled output expiry
}

// BatchImageItem records one submitted custom ID and its indexed provider
// result metadata.
type BatchImageItem struct {
	ID                   int64   // database record identity
	JobID                string  // owning batch identifier
	CustomID             string  // submitted item identity
	Status               string  // stored item outcome label
	RequestHash          *string // normalized request fingerprint
	PromptPreview        *string // stored prompt preview
	ProviderSourceObject *string // internal result source
	// SourceLineNumber, SourceByteOffset and SourceByteLength describe the
	// provider result location when supplied. Current indexing records line
	// numbers; download reads the result by matching CustomID.
	SourceLineNumber *int       // one based result line
	SourceByteOffset *int64     // result byte offset
	SourceByteLength *int64     // result byte length
	MimeType         *string    // image MIME type
	FileExtension    *string    // image filename extension
	ImageCount       int        // returned image parts
	ErrorCode        *string    // stable failure category
	ErrorMessage     *string    // failure diagnostic text
	BilledAmount     *float64   // optional USD item charge
	CreatedAt        time.Time  // job creation time
	IndexedAt        *time.Time // item indexing time
}

// CreateBatchImageItemParams supplies a pending item or one rebuilt indexed
// result.
type CreateBatchImageItemParams struct {
	JobID                string  // owning batch identifier
	CustomID             string  // submitted item identity
	Status               string  // initial or indexed outcome
	RequestHash          *string // normalized request fingerprint
	PromptPreview        *string // stored prompt preview
	ProviderSourceObject *string // internal result source
	// SourceLineNumber, SourceByteOffset and SourceByteLength retain optional
	// provider result positions during item creation. The current indexer
	// supplies line positions and leaves byte ranges absent.
	SourceLineNumber *int       // one based result line
	SourceByteOffset *int64     // result byte offset
	SourceByteLength *int64     // result byte length
	MimeType         *string    // image MIME type
	FileExtension    *string    // image filename extension
	ImageCount       int        // returned image parts
	ErrorCode        *string    // stable failure category
	ErrorMessage     *string    // failure diagnostic text
	BilledAmount     *float64   // optional USD item charge
	IndexedAt        *time.Time // item indexing time
}

// BatchImageItemFilter selects persisted outcomes and an offset-based item
// page.
type BatchImageItemFilter struct {
	Status string // stored item outcome filter
	Limit  int    // requested page size
	Offset int    // page start offset
}

// BatchImageJobFilter selects owner-visible jobs with optional status, title
// and creation filters.
type BatchImageJobFilter struct {
	Status         string     // stored job status filter
	TaskNameLike   string     // case insensitive title filter
	Downloaded     *bool      // download acknowledgement filter
	CreatedAfter   *time.Time // inclusive creation lower bound
	CreatedBefore  *time.Time // exclusive creation upper bound
	ExcludeDeleted bool       // hide deleted records
	Limit          int        // requested page size
	Offset         int        // page start offset
}

// BatchImageCounts records successful and failed custom IDs after result
// reconciliation.
type BatchImageCounts struct {
	SuccessCount int // successful item count
	FailCount    int // failed item count
}

// UpdateBatchImageJobProviderSubmitParams saves the resources returned after
// successful upstream submission.
type UpdateBatchImageJobProviderSubmitParams struct {
	BatchID           string // public batch identifier
	ProviderJobName   string // internal provider job
	ProviderInputRef  string // internal input reference
	ProviderOutputRef string // internal output reference
	GCSInputURI       string // Vertex input URI
	GCSOutputURI      string // Vertex output URI
	EventPayload      any    // audit event content
}

// BatchImageTransitionOptions supplies diagnostics, audit data and an
// optional transition time.
type BatchImageTransitionOptions struct {
	EventType    string // audit event label
	EventPayload any    // audit event content
	// ErrorCode and ErrorMessage supply failure diagnostics for a transition
	// into failed state. PostgreSQL retains existing diagnostics on other state
	// transitions even when these pointers are supplied.
	ErrorCode    *string    // stable failure category
	ErrorMessage *string    // failure diagnostic text
	Now          *time.Time // injected current time
}

// MarkBatchImageJobSettledParams stores a captured charge, result fingerprint
// and output retention time.
type MarkBatchImageJobSettledParams struct {
	BatchID         string     // public batch identifier
	ActualCost      float64    // USD captured charge
	ManifestHash    string     // settlement result fingerprint
	EventPayload    any        // audit event content
	Now             *time.Time // injected current time
	OutputExpiresAt *time.Time // scheduled output expiry
}

// BatchImageRepository stores lifecycle state, result indexes and
// owner-scoped batch access for asynchronous processing.
type BatchImageRepository interface {
	// CreateBatchImageJob persists the initial job and submission snapshots,
	// returning the created record. A duplicate batch identity fails; callers
	// reserve funds only after this record exists.
	CreateBatchImageJob(ctx context.Context, params CreateBatchImageJobParams) (*BatchImageJob, error)
	// GetBatchImageJobByBatchID reads the internal job for its public batch
	// identity. Missing records return ErrBatchImageJobNotFound; workers supply
	// an ID already reserved from the queue for processing.
	GetBatchImageJobByBatchID(ctx context.Context, batchID string) (*BatchImageJob, error)
	// GetBatchImageJobByIdempotencyKey finds a prior submission under its user,
	// API key and retry key. The caller compares RequestHash before replaying
	// the job or attempting its queue publication.
	GetBatchImageJobByIdempotencyKey(ctx context.Context, userID, apiKeyID int64, key string) (*BatchImageJob, error)
	// GetBatchImageJobByBatchIDForOwner reads a batch only when both user and
	// API key match its owner. Missing or mismatched ownership returns the same
	// not-found category to public operations.
	GetBatchImageJobByBatchIDForOwner(ctx context.Context, userID, apiKeyID int64, batchID string) (*BatchImageJob, error)
	// GetBatchImageJobByID reads a job using its numeric storage identity. It
	// returns ErrBatchImageJobNotFound when absent; this lookup supplies
	// internal callers rather than owner authorization during internal
	// maintenance.
	GetBatchImageJobByID(ctx context.Context, id int64) (*BatchImageJob, error)
	// ListBatchImageJobsForOwner returns jobs matching the user, API key and
	// requested filters. Limit and offset select the page, and ExcludeDeleted
	// removes records hidden through user deletion.
	ListBatchImageJobsForOwner(ctx context.Context, userID, apiKeyID int64, filter BatchImageJobFilter) ([]*BatchImageJob, error)
	// TransitionBatchImageJobStatus checks the current stored state before
	// applying its permitted next state and audit options. Invalid edges return
	// ErrBatchImageInvalidTransition; callers reread state when another
	// operation has progressed the job.
	TransitionBatchImageJobStatus(ctx context.Context, batchID, toStatus string, opts BatchImageTransitionOptions) error
	// TouchBatchImageJobSubmitting refreshes persisted activity while a created
	// or uploading job is submitting upstream. Later stages remain unchanged,
	// allowing stale-submit recovery to distinguish active submission from
	// abandoned work.
	TouchBatchImageJobSubmitting(ctx context.Context, batchID string) error
	// FailStaleUnsubmittedBatchImageJob conditionally fails a created or
	// uploading job with a NULL provider name and activity at or before cutoff.
	// False means callers retain funds; true permits release even when its later
	// audit append fails.
	FailStaleUnsubmittedBatchImageJob(ctx context.Context, batchID string, cutoff time.Time, code, message string) (bool, error)
	// UpdateBatchImageJobProviderOutputRef saves the internal output reference
	// reported by polling. Callers update their local snapshot after this write
	// succeeds so indexing can reopen the recorded provider result.
	UpdateBatchImageJobProviderOutputRef(ctx context.Context, batchID, providerOutputRef string) error
	// UpdateBatchImageJobProviderSubmit stores returned provider resources and
	// advances a permitted submission state to submitted. A status conflict
	// leaves the caller responsible for cancelling and cleaning the newly
	// created upstream job.
	UpdateBatchImageJobProviderSubmit(ctx context.Context, params UpdateBatchImageJobProviderSubmitParams) error
	// RecordBatchImageJobSubmitFailure saves diagnostics and a submission or
	// queue failure event. markFailed decides whether to fail the job; queue
	// publication errors keep a submitted upstream task available for retry.
	RecordBatchImageJobSubmitFailure(ctx context.Context, batchID, code, message string, markFailed bool) error
	// MarkBatchImageJobSettled marks a settling job completed with its captured
	// cost and matching manifest. Existing completion or a different manifest
	// returns a conflict; a present output expiry is retained.
	MarkBatchImageJobSettled(ctx context.Context, params MarkBatchImageJobSettledParams) error
	// SetBatchImageJobSettlementFailed saves the settlement diagnostic and
	// increments its persisted retry counter. The returned count can accompany
	// an event error because counter mutation and audit append are separate
	// writes.
	SetBatchImageJobSettlementFailed(ctx context.Context, batchID, code, message string) (int, error)
	// CreateBatchImageItem persists one pending or indexed item and returns its
	// metadata. Duplicate custom IDs within the owning batch return
	// ErrBatchImageItemExists for submission or indexing callers.
	CreateBatchImageItem(ctx context.Context, params CreateBatchImageItemParams) (*BatchImageItem, error)
	// BulkCreateBatchImageItems stores submission placeholders as one group of
	// item creations. Callers invoke it after reserving funds and release the
	// reservation if item persistence prevents upstream submission.
	BulkCreateBatchImageItems(ctx context.Context, params []CreateBatchImageItemParams) error
	// ReplaceBatchImageItemsForJob checks indexing state before replacing stored
	// results and their successful and failed counts. A later state returns
	// ErrBatchImageIndexStateConflict so stale indexing cannot replace settled
	// metadata.
	ReplaceBatchImageItemsForJob(ctx context.Context, batchID string, items []CreateBatchImageItemParams, counts BatchImageCounts) error
	// ListBatchImageItems returns the filtered item page for an internal batch
	// identity. The indexer uses successive offsets to collect expected IDs,
	// while public callers use the owner-checking variant.
	ListBatchImageItems(ctx context.Context, batchID string, filter BatchImageItemFilter) ([]*BatchImageItem, error)
	// ListBatchImageItemsForOwner checks user and API key ownership before
	// reading the filtered item page. A missing or mismatched job returns
	// ErrBatchImageJobNotFound rather than exposing its indexed results.
	ListBatchImageItemsForOwner(ctx context.Context, userID, apiKeyID int64, batchID string, filter BatchImageItemFilter) ([]*BatchImageItem, error)
	// GetBatchImageJobForDownload reads the batch after checking its user and
	// API key owner. Download checks the returned status separately and requires
	// completed output before acquiring a permit.
	GetBatchImageJobForDownload(ctx context.Context, userID, apiKeyID int64, batchID string) (*BatchImageJob, error)
	// GetBatchImageItemForDownload reads result metadata for one custom ID
	// inside an already authorized batch. Missing items return
	// ErrBatchImageItemNotFound; callers check success and requested image index
	// before streaming.
	GetBatchImageItemForDownload(ctx context.Context, batchID, customID string) (*BatchImageItem, error)
	// ListBatchImageItemsForDownload returns outcomes of the requested status
	// from an already authorized batch. ZIP callers request one extra successful
	// item to detect requests exceeding the configured item cap.
	ListBatchImageItemsForDownload(ctx context.Context, batchID string, status string, limit int) ([]*BatchImageItem, error)
	// ListBatchImageJobsDueForInputCleanup selects terminal jobs retaining
	// undeleted input references before the cutoff. Finish, settlement or
	// activity time determines eligibility, and limit bounds the cleanup batch
	// for provider deletion.
	ListBatchImageJobsDueForInputCleanup(ctx context.Context, cutoff time.Time, limit int) ([]*BatchImageJob, error)
	// ListBatchImageJobsDueForOutputCleanup selects completed jobs with present
	// output references and elapsed output expiry. Deleted outputs and
	// unfinished jobs are skipped, while limit bounds one cleanup scan.
	ListBatchImageJobsDueForOutputCleanup(ctx context.Context, now time.Time, limit int) ([]*BatchImageJob, error)
	// ListStaleUnsubmittedBatchImageJobs selects old created or uploading jobs
	// with no provider name and a positive held amount. Recovery rechecks the
	// same status and activity conditions before returning funds.
	ListStaleUnsubmittedBatchImageJobs(ctx context.Context, cutoff time.Time, limit int) ([]*BatchImageJob, error)
	// MarkBatchImageInputDeleted records the first successful input deletion and
	// its audit event. Callers perform provider cleanup first; repeated marking
	// retains the original deletion time on the job.
	MarkBatchImageInputDeleted(ctx context.Context, batchID string, deletedAt time.Time) error
	// MarkBatchImageOutputDeleted records the first output deletion after
	// provider cleanup succeeds. Completed jobs become output_deleted, while
	// failed and cancelled outcomes remain available with their output deletion
	// timestamp.
	MarkBatchImageOutputDeleted(ctx context.Context, batchID string, deletedAt time.Time) error
	// MarkBatchImageDownloaded records the first download acknowledgement and
	// appends its audit event. Public callers authorize the batch before
	// marking, and later acknowledgements retain the first timestamp.
	MarkBatchImageDownloaded(ctx context.Context, batchID string, downloadedAt time.Time) error
	// MarkBatchImageJobUserDeleted hides an owner-authorized terminal job while
	// keeping its metadata available to internal work. PostgreSQL rejects
	// settling and already hidden records with ErrBatchImageRecordDeleteNotReady
	// before appending the deletion event.
	MarkBatchImageJobUserDeleted(ctx context.Context, userID, apiKeyID int64, batchID string, deletedAt time.Time) error
	// SetBatchImageOutputExpiresAt supplies an output expiry only when the job
	// has none. Callers retain the chosen schedule across retries rather than
	// extending an already saved retention deadline.
	SetBatchImageOutputExpiresAt(ctx context.Context, batchID string, expiresAt time.Time) error
	// RecordBatchImageCleanupFailure saves the cleanup diagnostic and appends
	// its failure event. Callers sanitize provider messages before this write
	// and retain resource metadata for a later deletion attempt.
	RecordBatchImageCleanupFailure(ctx context.Context, batchID, code, message string) error
	// AppendBatchImageEvent persists an audit event for an internal batch
	// identity and supplied payload. State-changing callers decide whether an
	// event failure is fatal or merely observed after their main operation.
	AppendBatchImageEvent(ctx context.Context, batchID, eventType string, payload any) error
}

// NewBatchImageID returns a public batch identifier containing sixteen random
// bytes as hexadecimal.
func NewBatchImageID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "imgbatch_" + hex.EncodeToString(b[:]), nil
}

// IsSupportedBatchImageProvider reports whether the backend is Gemini API or
// Vertex.
func IsSupportedBatchImageProvider(platform string) bool {
	switch platform {
	case BatchImageProviderGeminiAPI, BatchImageProviderVertex:
		return true
	default:
		return false
	}
}

// IsTerminalBatchImageJobStatus reports whether generation and settlement
// reached a terminal job outcome.
func IsTerminalBatchImageJobStatus(status string) bool {
	switch status {
	case BatchImageJobStatusCompleted, BatchImageJobStatusFailed, BatchImageJobStatusCancelled, BatchImageJobStatusOutputDeleted:
		return true
	default:
		return false
	}
}

// CanTransitionBatchImageJob checks the permitted local status edge for
// repository updates. Terminal jobs can advance only to output_deleted;
// nonterminal failure is permitted, and repeated running observations can
// keep their current status.
func CanTransitionBatchImageJob(from, to string) bool {
	if from == "" || to == "" {
		return false
	}
	if IsTerminalBatchImageJobStatus(from) {
		return to == BatchImageJobStatusOutputDeleted &&
			from != BatchImageJobStatusOutputDeleted &&
			(from == BatchImageJobStatusCompleted || from == BatchImageJobStatusFailed || from == BatchImageJobStatusCancelled)
	}
	if to == BatchImageJobStatusFailed {
		return true
	}

	allowed := map[string]map[string]struct{}{
		BatchImageJobStatusCreated: {
			BatchImageJobStatusUploading: {},
			BatchImageJobStatusSubmitted: {},
			BatchImageJobStatusCancelled: {},
		},
		BatchImageJobStatusUploading: {
			BatchImageJobStatusSubmitted: {},
			BatchImageJobStatusCancelled: {},
		},
		BatchImageJobStatusSubmitted: {
			BatchImageJobStatusRunning:   {},
			BatchImageJobStatusIndexing:  {},
			BatchImageJobStatusFailed:    {},
			BatchImageJobStatusCancelled: {},
		},
		BatchImageJobStatusRunning: {
			BatchImageJobStatusRunning:   {},
			BatchImageJobStatusIndexing:  {},
			BatchImageJobStatusFailed:    {},
			BatchImageJobStatusCancelled: {},
		},
		BatchImageJobStatusIndexing: {
			BatchImageJobStatusSettling: {},
			BatchImageJobStatusFailed:   {},
		},
		BatchImageJobStatusSettling: {
			BatchImageJobStatusCompleted: {},
		},
	}
	_, ok := allowed[from][to]
	return ok
}
