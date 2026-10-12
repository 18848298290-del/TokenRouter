package batchimage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

const (
	BatchImageProviderGeminiAPI = "gemini_api"
	BatchImageProviderVertex    = "vertex"

	BatchImageJobStatusCreated       = "created"
	BatchImageJobStatusUploading     = "uploading"
	BatchImageJobStatusSubmitted     = "submitted"
	BatchImageJobStatusRunning       = "running"
	BatchImageJobStatusIndexing      = "indexing"
	BatchImageJobStatusSettling      = "settling"
	BatchImageJobStatusCompleted     = "completed"
	BatchImageJobStatusFailed        = "failed"
	BatchImageJobStatusCancelled     = "cancelled"
	BatchImageJobStatusOutputDeleted = "output_deleted"

	BatchImageItemStatusPending   = "pending"
	BatchImageItemStatusSuccess   = "success"
	BatchImageItemStatusFailed    = "failed"
	BatchImageItemStatusCancelled = "cancelled"
)

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

type BatchImageJob struct {
	ID            int64
	BatchID       string
	UserID        int64
	BillingUserID int64
	TeamID        *int64
	APIKeyID      *int64
	ProviderID    *int64
	GroupID       *int64
	// BillingMode and PreferredSubscriptionID snapshot the API key funding source at submission.
	BillingMode             string
	PreferredSubscriptionID *int64
	Platform                string
	// Model, RequestedModel, and InternalModel identify the upstream, client, and internal routing models.
	Model             string
	RequestedModel    string
	InternalModel     string
	TaskName          string
	ParentBatchID     *string
	Status            string
	ProviderJobName   *string
	ProviderInputRef  *string
	ProviderOutputRef *string
	GCSInputURI       *string
	GCSOutputURI      *string

	ItemCount      int
	SuccessCount   int
	FailCount      int
	CancelledCount int

	EstimatedCost float64
	HoldAmount    *float64
	ActualCost    *float64
	// BalanceHoldAmount and SubscriptionHoldAllocations record the funds reserved at submission.
	BalanceHoldAmount           float64
	SubscriptionHoldAllocations []billing.BillingAllocation
	SubscriptionRateMultiplier  float64
	BalanceRateMultiplier       float64
	PlanGroupRateEnabled        bool
	// AllowanceReserved reports whether the estimate counts toward key and team-member quotas.
	AllowanceReserved       bool
	BaseUnitPrice           float64
	GroupRateMultiplier     float64
	ProviderRateMultiplier  float64
	BatchDiscountMultiplier float64
	HoldMultiplier          float64
	BillableUnitPrice       float64
	HoldUnitPrice           float64
	PricingSnapshotVersion  int
	Currency                string
	HoldID                  *string

	IdempotencyKey *string
	RequestHash    *string
	ManifestHash   *string
	SessionID      *string

	RetryCount int
	Version    int

	OutputExpiresAt *time.Time
	InputDeletedAt  *time.Time
	OutputDeletedAt *time.Time
	DownloadedAt    *time.Time
	UserDeletedAt   *time.Time

	LastErrorCode    *string
	LastErrorMessage *string

	CreatedAt   time.Time
	UpdatedAt   time.Time
	SubmittedAt *time.Time
	StartedAt   *time.Time
	FinishedAt  *time.Time
	SettledAt   *time.Time
}

type CreateBatchImageJobParams struct {
	BatchID                 string
	UserID                  int64
	BillingUserID           int64
	TeamID                  *int64
	APIKeyID                *int64
	ProviderID              *int64
	GroupID                 *int64
	BillingMode             string
	PreferredSubscriptionID *int64
	Platform                string
	// Model, RequestedModel, and InternalModel identify the upstream, client, and internal routing models.
	Model             string
	RequestedModel    string
	InternalModel     string
	TaskName          string
	ParentBatchID     *string
	Status            string
	ProviderJobName   *string
	ProviderInputRef  *string
	ProviderOutputRef *string
	GCSInputURI       *string
	GCSOutputURI      *string

	ItemCount      int
	SuccessCount   int
	FailCount      int
	CancelledCount int

	EstimatedCost               float64
	HoldAmount                  *float64
	ActualCost                  *float64
	BalanceHoldAmount           float64
	SubscriptionHoldAllocations []billing.BillingAllocation
	SubscriptionRateMultiplier  float64
	BalanceRateMultiplier       float64
	PlanGroupRateEnabled        bool
	BaseUnitPrice               float64
	GroupRateMultiplier         float64
	ProviderRateMultiplier      float64
	BatchDiscountMultiplier     float64
	HoldMultiplier              float64
	BillableUnitPrice           float64
	HoldUnitPrice               float64
	PricingSnapshotVersion      int
	Currency                    string
	HoldID                      *string

	IdempotencyKey *string
	RequestHash    *string
	ManifestHash   *string
	SessionID      *string

	RetryCount int

	OutputExpiresAt *time.Time
}

type BatchImageItem struct {
	ID                   int64
	JobID                string
	CustomID             string
	Status               string
	RequestHash          *string
	PromptPreview        *string
	ProviderSourceObject *string
	SourceLineNumber     *int
	SourceByteOffset     *int64
	SourceByteLength     *int64
	MimeType             *string
	FileExtension        *string
	ImageCount           int
	ErrorCode            *string
	ErrorMessage         *string
	BilledAmount         *float64
	CreatedAt            time.Time
	IndexedAt            *time.Time
}

type CreateBatchImageItemParams struct {
	JobID                string
	CustomID             string
	Status               string
	RequestHash          *string
	PromptPreview        *string
	ProviderSourceObject *string
	SourceLineNumber     *int
	SourceByteOffset     *int64
	SourceByteLength     *int64
	MimeType             *string
	FileExtension        *string
	ImageCount           int
	ErrorCode            *string
	ErrorMessage         *string
	BilledAmount         *float64
	IndexedAt            *time.Time
}

type BatchImageItemFilter struct {
	Status string
	Limit  int
	Offset int
}

type BatchImageJobFilter struct {
	Status         string
	TaskNameLike   string
	Downloaded     *bool
	CreatedAfter   *time.Time
	CreatedBefore  *time.Time
	ExcludeDeleted bool
	Limit          int
	Offset         int
}

type BatchImageCounts struct {
	SuccessCount int
	FailCount    int
}

type UpdateBatchImageJobProviderSubmitParams struct {
	BatchID           string
	ProviderJobName   string
	ProviderInputRef  string
	ProviderOutputRef string
	GCSInputURI       string
	GCSOutputURI      string
	EventPayload      any
}

type BatchImageTransitionOptions struct {
	EventType    string
	EventPayload any
	ErrorCode    *string
	ErrorMessage *string
	Now          *time.Time
}

type MarkBatchImageJobSettledParams struct {
	BatchID         string
	ActualCost      float64
	ManifestHash    string
	EventPayload    any
	Now             *time.Time
	OutputExpiresAt *time.Time
}

type BatchImageRepository interface {
	CreateBatchImageJob(ctx context.Context, params CreateBatchImageJobParams) (*BatchImageJob, error)
	GetBatchImageJobByBatchID(ctx context.Context, batchID string) (*BatchImageJob, error)
	GetBatchImageJobByIdempotencyKey(ctx context.Context, userID, apiKeyID int64, key string) (*BatchImageJob, error)
	GetBatchImageJobByBatchIDForOwner(ctx context.Context, userID, apiKeyID int64, batchID string) (*BatchImageJob, error)
	GetBatchImageJobByID(ctx context.Context, id int64) (*BatchImageJob, error)
	ListBatchImageJobsForOwner(ctx context.Context, userID, apiKeyID int64, filter BatchImageJobFilter) ([]*BatchImageJob, error)
	TransitionBatchImageJobStatus(ctx context.Context, batchID, toStatus string, opts BatchImageTransitionOptions) error
	// TouchBatchImageJobSubmitting refreshes updated_at for created/uploading jobs
	// so recovery can distinguish a slow submission from an abandoned job.
	TouchBatchImageJobSubmitting(ctx context.Context, batchID string) error
	// FailStaleUnsubmittedBatchImageJob atomically fails a created/uploading job with
	// an empty provider_job_name and updated_at before cutoff.
	// A false result means the stale condition no longer holds; callers must not release funds.
	FailStaleUnsubmittedBatchImageJob(ctx context.Context, batchID string, cutoff time.Time, code, message string) (bool, error)
	UpdateBatchImageJobProviderOutputRef(ctx context.Context, batchID, providerOutputRef string) error
	UpdateBatchImageJobProviderSubmit(ctx context.Context, params UpdateBatchImageJobProviderSubmitParams) error
	RecordBatchImageJobSubmitFailure(ctx context.Context, batchID, code, message string, markFailed bool) error
	MarkBatchImageJobSettled(ctx context.Context, params MarkBatchImageJobSettledParams) error
	SetBatchImageJobSettlementFailed(ctx context.Context, batchID, code, message string) (int, error)
	CreateBatchImageItem(ctx context.Context, params CreateBatchImageItemParams) (*BatchImageItem, error)
	BulkCreateBatchImageItems(ctx context.Context, params []CreateBatchImageItemParams) error
	ReplaceBatchImageItemsForJob(ctx context.Context, batchID string, items []CreateBatchImageItemParams, counts BatchImageCounts) error
	ListBatchImageItems(ctx context.Context, batchID string, filter BatchImageItemFilter) ([]*BatchImageItem, error)
	ListBatchImageItemsForOwner(ctx context.Context, userID, apiKeyID int64, batchID string, filter BatchImageItemFilter) ([]*BatchImageItem, error)
	GetBatchImageJobForDownload(ctx context.Context, userID, apiKeyID int64, batchID string) (*BatchImageJob, error)
	GetBatchImageItemForDownload(ctx context.Context, batchID, customID string) (*BatchImageItem, error)
	ListBatchImageItemsForDownload(ctx context.Context, batchID string, status string, limit int) ([]*BatchImageItem, error)
	ListBatchImageJobsDueForInputCleanup(ctx context.Context, cutoff time.Time, limit int) ([]*BatchImageJob, error)
	ListBatchImageJobsDueForOutputCleanup(ctx context.Context, now time.Time, limit int) ([]*BatchImageJob, error)
	ListStaleUnsubmittedBatchImageJobs(ctx context.Context, cutoff time.Time, limit int) ([]*BatchImageJob, error)
	MarkBatchImageInputDeleted(ctx context.Context, batchID string, deletedAt time.Time) error
	MarkBatchImageOutputDeleted(ctx context.Context, batchID string, deletedAt time.Time) error
	MarkBatchImageDownloaded(ctx context.Context, batchID string, downloadedAt time.Time) error
	MarkBatchImageJobUserDeleted(ctx context.Context, userID, apiKeyID int64, batchID string, deletedAt time.Time) error
	SetBatchImageOutputExpiresAt(ctx context.Context, batchID string, expiresAt time.Time) error
	RecordBatchImageCleanupFailure(ctx context.Context, batchID, code, message string) error
	AppendBatchImageEvent(ctx context.Context, batchID, eventType string, payload any) error
}

func NewBatchImageID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "imgbatch_" + hex.EncodeToString(b[:]), nil
}

func IsSupportedBatchImageProvider(platform string) bool {
	switch platform {
	case BatchImageProviderGeminiAPI, BatchImageProviderVertex:
		return true
	default:
		return false
	}
}

func IsTerminalBatchImageJobStatus(status string) bool {
	switch status {
	case BatchImageJobStatusCompleted, BatchImageJobStatusFailed, BatchImageJobStatusCancelled, BatchImageJobStatusOutputDeleted:
		return true
	default:
		return false
	}
}

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
