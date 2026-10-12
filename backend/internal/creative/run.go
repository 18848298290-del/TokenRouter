package creative

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

const (
	// Creative Studio image operations.
	CreativeOperationGenerate = "generate"
	CreativeOperationEdit     = "edit"
	CreativeOperationInpaint  = "inpaint"

	// Provisioning phases are persisted after each step so recovery can resume or release a partial task.
	CreativeProvisioningPhaseCreated        = "created"
	CreativeProvisioningPhaseHoldReserved   = "hold_reserved"
	CreativeProvisioningPhaseTransientSaved = "transient_saved"
	CreativeProvisioningPhaseEnqueued       = "enqueued"
	CreativeProvisioningPhaseFailed         = "failed"
	CreativeProvisioningPhaseComplete       = "complete"

	// Task states, including provider completion and pending funding recovery.
	CreativeRunStatusQueued            = "queued"
	CreativeRunStatusRunning           = "running"
	CreativeRunStatusProviderSucceeded = "provider_succeeded"
	CreativeRunStatusSettlementPending = "settlement_pending"
	CreativeRunStatusReleasePending    = "release_pending"
	CreativeRunStatusSucceeded         = "succeeded"
	CreativeRunStatusFailed            = "failed"
	CreativeRunStatusCancelled         = "cancelled"
	CreativeRunStatusResultLost        = "result_lost"

	// Per-output delivery states.
	CreativeRunOutputStatusPending   = "pending"
	CreativeRunOutputStatusSucceeded = "succeeded"
	CreativeRunOutputStatusFailed    = "failed"
	CreativeRunOutputStatusLost      = "lost"
	CreativeRunOutputStatusAcked     = "acked"

	// CreativeManagedBy marks hidden Creative Studio execution keys in api_keys.managed_by.
	CreativeManagedBy = "creative_studio"

	// CreativeWorkspaceHeader carries the browser workspace UUID.
	CreativeWorkspaceHeader = "X-Creative-Workspace-ID"

	CreativeRunOutboxProvision CreativeRunOutboxOperation = "provision"
	CreativeRunOutboxSettle    CreativeRunOutboxOperation = "settle"
	CreativeRunOutboxRelease   CreativeRunOutboxOperation = "release"

	CreativeRunOutboxPending   CreativeRunOutboxStatus = "pending"
	CreativeRunOutboxLeased    CreativeRunOutboxStatus = "leased"
	CreativeRunOutboxDone      CreativeRunOutboxStatus = "done"
	CreativeRunOutboxCancelled CreativeRunOutboxStatus = "cancelled"
)

var (
	// Workers distinguish missing, unavailable, and corrupt data; storage outages must remain retryable.
	ErrCreativeTransientNotFound    = errors.New("creative transient data is missing or expired")
	ErrCreativeTransientUnavailable = errors.New("creative transient store is unavailable")
	ErrCreativeTransientCorrupt     = errors.New("creative transient data is corrupt")
	ErrCreativeDisabled             = infraerrors.New(infraerrors.CategoryNotFound, "CREATIVE_DISABLED", "creative studio is disabled")
	ErrCreativeRunNotFound          = infraerrors.New(infraerrors.CategoryNotFound, "CREATIVE_RUN_NOT_FOUND", "creative run not found")
	ErrCreativeRunExists            = infraerrors.New(infraerrors.CategoryConflict, "CREATIVE_RUN_EXISTS", "creative run already exists")
	ErrCreativeOutputExists         = infraerrors.New(infraerrors.CategoryConflict, "CREATIVE_OUTPUT_EXISTS", "creative run output already exists")
	ErrCreativeWorkspaceRequired    = infraerrors.New(infraerrors.CategoryBadRequest, "CREATIVE_WORKSPACE_REQUIRED", "creative workspace id is required")
	ErrCreativeWorkspaceInvalid     = infraerrors.New(infraerrors.CategoryBadRequest, "CREATIVE_WORKSPACE_INVALID", "creative workspace id is invalid")

	ErrCreativeInvalidTransition    = infraerrors.New(infraerrors.CategoryBadRequest, "CREATIVE_INVALID_TRANSITION", "invalid creative run status transition")
	ErrCreativeInvalidParams        = infraerrors.New(infraerrors.CategoryBadRequest, "CREATIVE_INVALID_PARAMS", "invalid creative run parameters")
	ErrCreativePromptTooLong        = infraerrors.New(infraerrors.CategoryBadRequest, "CREATIVE_PROMPT_TOO_LONG", "creative prompt is too long")
	ErrCreativeInvalidMime          = infraerrors.New(infraerrors.CategoryBadRequest, "CREATIVE_INVALID_MIME", "creative source image mime type is invalid")
	ErrCreativeAssetTooLarge        = infraerrors.New(infraerrors.Category(413), "CREATIVE_ASSET_TOO_LARGE", "creative asset is too large")
	ErrCreativeInputTooLarge        = infraerrors.New(infraerrors.Category(413), "CREATIVE_INPUT_TOO_LARGE", "creative total input is too large")
	ErrCreativeMaskRequired         = infraerrors.New(infraerrors.CategoryBadRequest, "CREATIVE_MASK_REQUIRED", "creative inpaint requires source image and png mask")
	ErrCreativeMaskSizeMismatch     = infraerrors.New(infraerrors.CategoryBadRequest, "CREATIVE_MASK_SIZE_MISMATCH", "creative mask dimensions must match the first source image")
	ErrCreativeOperationUnsupported = infraerrors.New(infraerrors.CategoryBadRequest, "CREATIVE_OPERATION_UNSUPPORTED", "creative operation is not supported by this group")
	ErrCreativeInvalidModel         = infraerrors.New(infraerrors.CategoryBadRequest, "CREATIVE_INVALID_MODEL", "creative model is not available in this group")
	ErrCreativeGroupForbidden       = infraerrors.New(infraerrors.CategoryForbidden, "CREATIVE_GROUP_FORBIDDEN", "creative studio is not available for this group")
	ErrCreativeGroupImageDisabled   = infraerrors.New(infraerrors.CategoryForbidden, "CREATIVE_GROUP_IMAGE_DISABLED", "image generation is disabled for this group")

	ErrCreativeRunIdempotencyConflict = infraerrors.New(infraerrors.CategoryConflict, "CREATIVE_IDEMPOTENCY_CONFLICT", "idempotency key reused with different creative request")
	ErrCreativeOutputNotFound         = infraerrors.New(infraerrors.CategoryNotFound, "CREATIVE_OUTPUT_NOT_FOUND", "creative run output not found")
	ErrCreativeOutputNotReady         = infraerrors.New(infraerrors.CategoryConflict, "CREATIVE_OUTPUT_NOT_READY", "creative run output is not ready")
	ErrCreativeOutputExpired          = infraerrors.New(infraerrors.Category(410), "CREATIVE_OUTPUT_EXPIRED", "creative run output has expired")
	ErrCreativeResultLost             = infraerrors.New(infraerrors.Category(410), "CREATIVE_RESULT_LOST", "creative run result has been lost")
	ErrCreativeTransientFailed        = infraerrors.New(infraerrors.CategoryBadGateway, "CREATIVE_TRANSIENT_FAILED", "creative transient store operation failed")

	ErrCreativeBillingHoldFailed     = infraerrors.New(infraerrors.CategoryBadGateway, "CREATIVE_BILLING_HOLD_FAILED", "creative balance hold failed")
	ErrCreativeInsufficientBalance   = infraerrors.New(infraerrors.Category(402), "CREATIVE_INSUFFICIENT_BALANCE", "insufficient balance for creative run")
	ErrCreativeSettlementBillingFail = infraerrors.New(infraerrors.CategoryBadGateway, "CREATIVE_SETTLEMENT_BILLING_FAILED", "creative settlement billing failed")
)

// CreativeRunScope binds a user to a browser workspace and is required for user-facing task access.
type CreativeRunScope struct {
	UserID      int64
	WorkspaceID string
}

// CreativeRun contains persistent asynchronous task metadata.
// Keep plaintext prompts, image and mask bytes, and raw provider responses out of this persistent type.
type CreativeRun struct {
	ID     int64
	RunID  string
	UserID int64
	// WorkspaceID is absent for legacy tasks, which user-facing workspace queries hide.
	WorkspaceID          *string
	GroupID              int64
	APIKeyID             int64
	ProviderID           *int64
	Platform             string
	Model                string
	RequestedModel       string
	Operation            string
	RequestedOutputCount int
	ImageSize            string
	AspectRatio          string
	ResponseMIMEType     string

	PromptHash         string
	RequestFingerprint string
	IdempotencyKey     *string

	Status string

	EstimatedCost               float64
	HoldAmount                  *float64
	ActualCost                  *float64
	BalanceHoldAmount           float64
	SubscriptionHoldAllocations []billing.BillingAllocation
	// AllowanceReserved records an allowance reservation, as in batch_image_jobs.allowance_reserved.
	AllowanceReserved          bool
	BaseUnitPrice              float64
	SubscriptionRateMultiplier float64
	BalanceRateMultiplier      float64
	PlanGroupRateEnabled       bool

	ErrorCode           *string
	ErrorMessage        *string
	ReleaseTargetStatus string
	AttemptCount        int
	ProvisioningPhase   string
	// SettlementAttemptCount and ReleaseAttemptCount track funding recovery separately from execution retries.
	SettlementAttemptCount   int
	ReleaseAttemptCount      int
	ProviderResultRecordedAt *time.Time
	NextReconcileAt          *time.Time
	LastReconcileError       *string

	Version int64

	CreatedAt   time.Time
	UpdatedAt   time.Time
	StartedAt   *time.Time
	CompletedAt *time.Time
	CancelledAt *time.Time
}

// CreativeRunOutput contains output metadata; image bytes are kept in temporary storage.
type CreativeRunOutput struct {
	ID                 int64
	RunID              string
	OutputIndex        int
	Status             string
	MimeType           *string
	ByteSize           *int64
	TransientExpiresAt *time.Time
	AckedAt            *time.Time
	ErrorCode          *string
	ErrorMessage       *string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// CreateCreativeRunParams supplies metadata for a task and its initial output rows.
type CreateCreativeRunParams struct {
	Platform                   string
	RunID                      string
	UserID                     int64
	WorkspaceID                string
	GroupID                    int64
	APIKeyID                   int64
	Model                      string
	RequestedModel             string
	Operation                  string
	RequestedOutputCount       int
	ImageSize                  string
	AspectRatio                string
	ResponseMIMEType           string
	PromptHash                 string
	RequestFingerprint         string
	IdempotencyKey             *string
	EstimatedCost              float64
	HoldAmount                 float64
	BaseUnitPrice              float64
	SubscriptionRateMultiplier float64
	BalanceRateMultiplier      float64
	PlanGroupRateEnabled       bool
}

// CreativeRunTransitionOptions supplies optional error details, release target, and transition time.
type CreativeRunTransitionOptions struct {
	ErrorCode           *string
	ErrorMessage        *string
	ReleaseTargetStatus string
	Now                 *time.Time
}

// CreativeRunOutboxOperation identifies a recoverable background action.
type CreativeRunOutboxOperation string

// CreativeRunOutboxStatus is the processing state of an outbox record.
type CreativeRunOutboxStatus string

// CreativeRunOutbox is a durable record of background recovery work.
type CreativeRunOutbox struct {
	ID           int64
	RunID        string
	Operation    CreativeRunOutboxOperation
	Status       CreativeRunOutboxStatus
	AvailableAt  time.Time
	LeaseToken   string
	LeaseUntil   *time.Time
	AttemptCount int
	LastError    string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// CreativeRunOutboxRepository persists and leases recovery actions.
type CreativeRunOutboxRepository interface {
	Ensure(ctx context.Context, runID string, operation CreativeRunOutboxOperation, availableAt time.Time) error
	Claim(ctx context.Context, workerID string, limit int, lease time.Duration) ([]CreativeRunOutbox, error)
	Complete(ctx context.Context, id int64, leaseToken string) error
	Retry(ctx context.Context, id int64, leaseToken string, availableAt time.Time, lastError string) error
	Cancel(ctx context.Context, id int64, leaseToken string) error
}

// CreativeRunFilter selects and paginates user-facing task lists.
type CreativeRunFilter struct {
	Status string
	Limit  int
	Offset int
}

// CreativeRunOutputBatchReader optionally fetches output metadata in bulk to avoid one query per task.
type CreativeRunOutputBatchReader interface {
	ListCreativeRunOutputsForRuns(ctx context.Context, runIDs []string) (map[string][]*CreativeRunOutput, error)
}

// CreativeRunAllowanceMarker persists the allowance reservation marker used during funding recovery.
type CreativeRunAllowanceMarker interface {
	SetCreativeRunAllowanceReserved(ctx context.Context, runID string, reserved bool) error
}

// CreativeRunRepository persists task metadata and state transitions.
type CreativeRunRepository interface {
	CreateCreativeRun(ctx context.Context, params CreateCreativeRunParams) (*CreativeRun, error)
	GetCreativeRunByRunID(ctx context.Context, runID string) (*CreativeRun, error)
	GetCreativeRunByRunIDForOwner(ctx context.Context, scope CreativeRunScope, runID string) (*CreativeRun, error)
	GetCreativeRunByIdempotencyKey(ctx context.Context, scope CreativeRunScope, key string) (*CreativeRun, error)
	ListCreativeRunsForOwner(ctx context.Context, scope CreativeRunScope, filter CreativeRunFilter) ([]*CreativeRun, error)
	// TransitionCreativeRunStatus checks CanTransitionCreativeRun and uses optimistic version locking.
	TransitionCreativeRunStatus(ctx context.Context, runID, toStatus string, opts CreativeRunTransitionOptions) error
	// MarkCreativeRunRunning enters running or updates the provider of an already running task.
	MarkCreativeRunRunning(ctx context.Context, runID string, providerID int64, now time.Time) error
	// SetCreativeRunExecution records the selected provider and platform before the upstream call.
	SetCreativeRunExecution(ctx context.Context, runID string, providerID int64, platform string, now time.Time) error
	// MarkCreativeRunSucceeded records cost and completes running, provider_succeeded, or
	// settlement_pending tasks.
	MarkCreativeRunSucceeded(ctx context.Context, runID string, actualCost float64, now time.Time) error
	// UpdateCreativeRunOutput updates output metadata while leaving acknowledged rows unchanged.
	UpdateCreativeRunOutput(ctx context.Context, runID string, outputIndex int, status, mimeType string, byteSize int64, transientExpiresAt *time.Time, errorCode, errorMessage string) error
	GetCreativeRunOutput(ctx context.Context, runID string, outputIndex int) (*CreativeRunOutput, error)
	ListCreativeRunOutputs(ctx context.Context, runID string) ([]*CreativeRunOutput, error)
	// MarkCreativeRunOutputAcked records client receipt; repeated acknowledgment succeeds.
	MarkCreativeRunOutputAcked(ctx context.Context, runID string, outputIndex int, now time.Time) error
	// ListCreativeRunsDueForTransientCleanup selects terminal tasks completed at or before cutoff.
	ListCreativeRunsDueForTransientCleanup(ctx context.Context, cutoff time.Time, limit int) ([]*CreativeRun, error)
	// IncrementCreativeRunAttempt atomically increments the execution attempt count, then reads the
	// current count.
	IncrementCreativeRunAttempt(ctx context.Context, runID string) (int, error)
	// IncrementCreativeRunSettlementAttempt atomically increments the settlement attempt count, then reads
	// the current count.
	IncrementCreativeRunSettlementAttempt(ctx context.Context, runID string) (int, error)
	// IncrementCreativeRunReleaseAttempt atomically increments the release attempt count, then reads the
	// current count.
	IncrementCreativeRunReleaseAttempt(ctx context.Context, runID string) (int, error)
	// SetCreativeRunProvisioningPhase persists the last completed provisioning phase.
	SetCreativeRunProvisioningPhase(ctx context.Context, runID, phase string) error
	// MarkCreativeRunProviderSucceeded records provider completion for settlement recovery.
	MarkCreativeRunProviderSucceeded(ctx context.Context, runID string, providerID int64, now time.Time) error
	// SetCreativeRunReconcileError records the latest recovery error and next retry time.
	SetCreativeRunReconcileError(ctx context.Context, runID, message string, next time.Time) error
}

// CreativeRunPayload is the execution payload stored temporarily in Redis.
// Plaintext prompts and media belong in temporary storage and execution memory, never persistent task metadata.
// Sources and Mask are excluded from JSON; their bytes are stored under separate Redis input and mask keys.
type CreativeRunPayload struct {
	RunID              string `json:"run_id"`
	UserID             int64  `json:"user_id"`
	GroupID            int64  `json:"group_id"`
	APIKeyID           int64  `json:"api_key_id"`
	Model              string `json:"model"`
	Operation          string `json:"operation"`
	Prompt             string `json:"prompt"`
	ImageSize          string `json:"image_size"`
	AspectRatio        string `json:"aspect_ratio"`
	Background         string `json:"background,omitempty"`
	ThinkingLevel      string `json:"thinking_level,omitempty"`
	Quality            string `json:"quality,omitempty"`
	SourceCount        int    `json:"source_count"`
	HasMask            bool   `json:"has_mask"`
	RequestFingerprint string `json:"request_fingerprint"`

	// The worker fills Sources and Mask from temporary storage before execution.
	Sources []CreativeInputImage `json:"-"`
	Mask    *CreativeInputImage  `json:"-"`
}

// CreativeInputImage contains an uploaded source image or mask and its MIME type.
type CreativeInputImage struct {
	Bytes []byte
	Mime  string
}

// CreateCreativeRunParamsPublic contains CreateRun input assembled from multipart fields by the handler.
type CreateCreativeRunParamsPublic struct {
	GroupID       int64
	Model         string
	Operation     string
	Prompt        string
	SourceImages  []CreativeInputImage
	Mask          *CreativeInputImage
	ImageSize     string
	AspectRatio   string
	Quality       string
	Background    string
	ThinkingLevel string
	OutputCount   int
}

// CreativeRunPublic is the client-facing task representation; timestamps are Unix seconds.
type CreativeRunPublic struct {
	ID                   string                    `json:"id"`
	Status               string                    `json:"status"`
	Model                string                    `json:"model"`
	RequestedModel       string                    `json:"requested_model"`
	Operation            string                    `json:"operation"`
	RequestedOutputCount int                       `json:"requested_output_count"`
	ImageSize            string                    `json:"image_size"`
	AspectRatio          string                    `json:"aspect_ratio"`
	OutputFormat         string                    `json:"output_format"`
	GroupID              int64                     `json:"group_id"`
	EstimatedCost        float64                   `json:"estimated_cost"`
	HoldAmount           float64                   `json:"hold_amount"`
	ActualCost           *float64                  `json:"actual_cost,omitempty"`
	ErrorCode            string                    `json:"error_code,omitempty"`
	ErrorMessage         string                    `json:"error_message,omitempty"`
	Outputs              []CreativeRunOutputPublic `json:"outputs,omitempty"`
	CreatedAt            int64                     `json:"created_at"`
	StartedAt            *int64                    `json:"started_at,omitempty"`
	CompletedAt          *int64                    `json:"completed_at,omitempty"`
	CancelledAt          *int64                    `json:"cancelled_at,omitempty"`
	// IdempotentReplay reports whether the response reuses a task for the supplied idempotency key.
	IdempotentReplay bool `json:"idempotent_replay,omitempty"`
}

// CreativeRunOutputPublic exposes output metadata with timestamps in Unix seconds.
type CreativeRunOutputPublic struct {
	Index              int    `json:"output_index"`
	Status             string `json:"status"`
	MimeType           string `json:"mime_type,omitempty"`
	ByteSize           int64  `json:"byte_size,omitempty"`
	TransientExpiresAt *int64 `json:"transient_expires_at,omitempty"`
	AckedAt            *int64 `json:"acked_at,omitempty"`
	ErrorCode          string `json:"error_code,omitempty"`
	ErrorMessage       string `json:"error_message,omitempty"`
}

// CreativeModelPublic is an available group/model entry returned by ListModels.
type CreativeModelPublic struct {
	GroupID            int64                 `json:"group_id"`
	GroupName          string                `json:"group_name"`
	Model              string                `json:"model"`
	Operations         []string              `json:"operations"`
	ImageSizes         []string              `json:"image_sizes"`
	AspectRatios       []string              `json:"aspect_ratios"`
	Qualities          []string              `json:"qualities"`
	OutputFormats      []string              `json:"output_formats"`
	OutputCompression  *CreativeNumericRange `json:"output_compression"`
	BackgroundOptions  []string              `json:"background_options"`
	ThinkingLevels     []string              `json:"thinking_levels"`
	MaxOutputCount     int                   `json:"max_output_count"`
	MaxReferenceImages int                   `json:"max_reference_images"`
	Price512           float64               `json:"price_512"`
	Price1K            float64               `json:"price_1k"`
	Price2K            float64               `json:"price_2k"`
	Price4K            float64               `json:"price_4k"`
}

// CreativeNumericRange defines inclusive bounds and a step for a numeric parameter.
type CreativeNumericRange struct {
	Min  int `json:"min"`
	Max  int `json:"max"`
	Step int `json:"step"`
}

// CreativeModelsResponse is the ListModels response body.
type CreativeModelsResponse struct {
	Data []CreativeModelPublic `json:"data"`
}

// CreativeCapabilitiesResponse describes server upload limits in bytes and prompt limits in Unicode code
// points.
type CreativeCapabilitiesResponse struct {
	MaxPromptChars     int      `json:"max_prompt_chars"`
	MaxAssetBytes      int64    `json:"max_asset_bytes"`
	MaxTotalInputBytes int64    `json:"max_total_input_bytes"`
	MaxMaskBytes       int64    `json:"max_mask_bytes"`
	AllowedMIMETypes   []string `json:"allowed_mime_types"`
}

// CreativeListRunsResponse is a page of tasks with an indication of possible further results.
type CreativeListRunsResponse struct {
	Data    []*CreativeRunPublic `json:"data"`
	HasMore bool                 `json:"has_more"`
}

// NormalizeCreativeWorkspaceID parses a nonzero workspace UUID and returns its canonical lowercase form.
func NormalizeCreativeWorkspaceID(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ErrCreativeWorkspaceRequired
	}
	parsed, err := uuid.Parse(raw)
	if err != nil || parsed == uuid.Nil {
		return "", ErrCreativeWorkspaceInvalid
	}
	return strings.ToLower(parsed.String()), nil
}

// NormalizeCreativeRunScope requires a positive user ID and normalizes the workspace UUID.
func NormalizeCreativeRunScope(scope CreativeRunScope) (CreativeRunScope, error) {
	if scope.UserID <= 0 {
		return CreativeRunScope{}, ErrCreativeRunNotFound
	}
	normalizedWorkspaceID, err := NormalizeCreativeWorkspaceID(scope.WorkspaceID)
	if err != nil {
		return CreativeRunScope{}, err
	}
	scope.WorkspaceID = normalizedWorkspaceID
	return scope, nil
}

// NewCreativeRunID returns crun_ followed by 16 random bytes encoded as hexadecimal.
func NewCreativeRunID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "crun_" + hex.EncodeToString(b[:]), nil
}

// IsValidCreativeRunID checks for a crun_ prefix and nonempty suffix; it does not validate hexadecimal
// encoding.
func IsValidCreativeRunID(runID string) bool {
	return len(runID) > len("crun_") && runID[:len("crun_")] == "crun_"
}

// IsTerminalCreativeRunStatus reports whether task execution has ended.
func IsTerminalCreativeRunStatus(status string) bool {
	switch status {
	case CreativeRunStatusSucceeded, CreativeRunStatusFailed, CreativeRunStatusCancelled, CreativeRunStatusResultLost:
		return true
	default:
		return false
	}
}

// IsCreativeRunSettlementPending reports whether settlement or release recovery is pending.
func IsCreativeRunSettlementPending(status string) bool {
	return status == CreativeRunStatusProviderSucceeded ||
		status == CreativeRunStatusSettlementPending ||
		status == CreativeRunStatusReleasePending
}

// CanTransitionCreativeRun checks allowed status pairs, including provider and funding recovery stages.
// The sole transition out of a terminal state is succeeded to result_lost.
func CanTransitionCreativeRun(from, to string) bool {
	if from == "" || to == "" {
		return false
	}
	if IsTerminalCreativeRunStatus(from) {
		// A successful task can lose its temporary result after completion.
		return from == CreativeRunStatusSucceeded && to == CreativeRunStatusResultLost
	}
	if to == CreativeRunStatusFailed {
		return true
	}
	switch from {
	case CreativeRunStatusQueued:
		// A queued task can lose its payload before it ever reaches running.
		return to == CreativeRunStatusRunning || to == CreativeRunStatusCancelled || to == CreativeRunStatusResultLost || to == CreativeRunStatusFailed || to == CreativeRunStatusReleasePending
	case CreativeRunStatusRunning:
		switch to {
		case CreativeRunStatusProviderSucceeded, CreativeRunStatusSettlementPending, CreativeRunStatusSucceeded, CreativeRunStatusCancelled, CreativeRunStatusResultLost, CreativeRunStatusReleasePending:
			return true
		default:
			return false
		}
	case CreativeRunStatusProviderSucceeded:
		return to == CreativeRunStatusSettlementPending || to == CreativeRunStatusSucceeded || to == CreativeRunStatusCancelled || to == CreativeRunStatusResultLost
	case CreativeRunStatusSettlementPending:
		return to == CreativeRunStatusSucceeded || to == CreativeRunStatusCancelled || to == CreativeRunStatusResultLost
	case CreativeRunStatusReleasePending:
		return to == CreativeRunStatusFailed || to == CreativeRunStatusCancelled || to == CreativeRunStatusResultLost
	default:
		return false
	}
}

// CreativeRunToPublic converts metadata for the client, using estimated cost when no hold amount is recorded.
func CreativeRunToPublic(run *CreativeRun, outputs []*CreativeRunOutput) *CreativeRunPublic {
	if run == nil {
		return nil
	}
	holdAmount := run.EstimatedCost
	if run.HoldAmount != nil {
		holdAmount = *run.HoldAmount
	}
	out := &CreativeRunPublic{
		ID:                   run.RunID,
		Status:               run.Status,
		Model:                run.Model,
		RequestedModel:       run.RequestedModel,
		Operation:            run.Operation,
		RequestedOutputCount: run.RequestedOutputCount,
		ImageSize:            run.ImageSize,
		AspectRatio:          run.AspectRatio,
		OutputFormat:         OutputFormatFromMIME(run.ResponseMIMEType),
		GroupID:              run.GroupID,
		EstimatedCost:        run.EstimatedCost,
		HoldAmount:           holdAmount,
		ActualCost:           run.ActualCost,
		ErrorCode:            CreativeDerefString(run.ErrorCode),
		ErrorMessage:         CreativeDerefString(run.ErrorMessage),
		CreatedAt:            run.CreatedAt.Unix(),
		StartedAt:            CreativeUnixPtr(run.StartedAt),
		CompletedAt:          CreativeUnixPtr(run.CompletedAt),
		CancelledAt:          CreativeUnixPtr(run.CancelledAt),
	}
	if len(outputs) > 0 {
		out.Outputs = make([]CreativeRunOutputPublic, 0, len(outputs))
		for _, output := range outputs {
			out.Outputs = append(out.Outputs, CreativeRunOutputToPublic(output))
		}
	}
	return out
}

// CreativeRunOutputToPublic converts output metadata and timestamps for the client.
func CreativeRunOutputToPublic(output *CreativeRunOutput) CreativeRunOutputPublic {
	if output == nil {
		return CreativeRunOutputPublic{}
	}
	out := CreativeRunOutputPublic{
		Index:              output.OutputIndex,
		Status:             output.Status,
		MimeType:           CreativeDerefString(output.MimeType),
		ByteSize:           CreativeDerefInt64(output.ByteSize),
		TransientExpiresAt: CreativeUnixPtr(output.TransientExpiresAt),
		AckedAt:            CreativeUnixPtr(output.AckedAt),
		ErrorCode:          CreativeDerefString(output.ErrorCode),
		ErrorMessage:       CreativeDerefString(output.ErrorMessage),
	}
	return out
}

func CreativeDerefString(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func CreativeDerefInt64(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

func CreativeUnixPtr(t *time.Time) *int64 {
	if t == nil {
		return nil
	}
	v := t.Unix()
	return &v
}

func CreativeStringPtr(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

func OutputFormatFromMIME(mime string) string {
	switch strings.ToLower(strings.TrimSpace(mime)) {
	case "image/jpeg", "image/jpg":
		return "jpeg"
	case "image/webp":
		return "webp"
	default:
		return "png"
	}
}
