package batchimage

import (
	"context"
	"time"
)

// contracts.go defines batch submission inputs, provider observations, and
// public response values.
// Batch submission expands each requested repetition into a separate custom
// ID before a provider receives it. These input and public response values
// carry the request, normalized provider observation and client-visible
// totals; persisted resource references remain on the internal job.

// Provider-state labels drive polling decisions, while cleanup-target labels
// select the internal resources passed to provider deletion.
const (
	BatchProviderStateQueued    BatchProviderInternalState = "queued"    // upstream work awaits execution
	BatchProviderStateRunning   BatchProviderInternalState = "running"   // upstream work is executing
	BatchProviderStateSucceeded BatchProviderInternalState = "succeeded" // upstream results are available
	BatchProviderStateFailed    BatchProviderInternalState = "failed"    // upstream execution failed
	BatchProviderStateCancelled BatchProviderInternalState = "cancelled" // upstream cancellation was observed
	BatchProviderStateExpired   BatchProviderInternalState = "expired"   // upstream completion window expired

	// Cleanup targets choose the submitted input resources, generated output
	// resources or all provider-owned resources for the stored job.
	CleanupTargetInput  CleanupTarget = "input"  // remove submitted input resources
	CleanupTargetOutput CleanupTarget = "output" // remove generated output resources
	CleanupTargetAll    CleanupTarget = "all"    // remove all job resources
)

// BatchImageInput contains expanded requests and output preferences for one
// provider submission.
type BatchImageInput struct {
	BatchID     string // public batch identifier
	Model       string // final upstream model
	DisplayName string // provider display label
	// Items contains already expanded custom IDs. Providers read its prompts and
	// reference data during Submit, so callers keep these shared slices and byte
	// buffers stable.
	Items []BatchImageInputItem // expanded item requests

	ResponseMimeType string // requested output MIME
	AspectRatio      string // requested image proportions
	ImageSize        string // requested output size

	Metadata map[string]string // bounded request labels
}

// BatchImageInputItem identifies one expanded request and its generation
// inputs.
type BatchImageInputItem struct {
	CustomID string // submitted item identity
	Prompt   string // generation prompt text

	ReferenceImages []BatchImageReference // reference image inputs
}

// BatchImageReference carries inline bytes or a GCS reference for a provider
// input.
type BatchImageReference struct {
	ID       string // reference source label
	Type     string // reference image role
	MimeType string // image MIME type
	// Data and FileURI represent alternative image sources. Submit validation
	// accepts exactly one source, then the provider receives bytes or the
	// validated gs:// URI.
	Data    []byte // decoded image bytes
	FileURI string // GCS source URI
}

// BatchProviderJob reports the internal resources created by a provider
// submission.
type BatchProviderJob struct {
	ProviderJobName   string // internal provider job
	ProviderInputRef  string // internal input reference
	ProviderOutputRef string // internal output reference
	RawState          string // original provider state
}

// BatchProviderInternalState classifies an upstream observation for local job
// progression.
type BatchProviderInternalState string

// BatchProviderStatus combines an upstream observation with local polling and
// failure information.
type BatchProviderStatus struct {
	RawState string // original provider state

	InternalState BatchProviderInternalState // normalized provider state
	// Done is the provider completion hint carried with the observation.
	// ProviderProcessor selects its next persisted stage from InternalState, so
	// that state decides progression even when the hint differs.
	Done bool // provider completion hint

	ProviderOutputRef string // internal output reference

	ErrorCode    string // stable failure category
	ErrorMessage string // failure diagnostic text
	// SuggestedRequeueAfter supplies a positive backend polling interval when
	// available. ProviderProcessor.RequeueDelay uses its configured interval or
	// default when this suggestion is absent or nonpositive.
	SuggestedRequeueAfter time.Duration // suggested polling delay
}

// CleanupTarget selects which persisted provider resources a cleanup
// operation removes.
type CleanupTarget string

// BatchImageUserGroupRateRepository reads a payer-specific multiplier for one
// routing group.
type BatchImageUserGroupRateRepository interface {
	// GetByUserAndGroup reads the optional payer-specific rate for a routing
	// group. A nil rate leaves the group wallet multiplier in effect, while a
	// read error prevents creation of a pricing snapshot.
	GetByUserAndGroup(ctx context.Context, userID, groupID int64) (*float64, error)
}

// BatchImageSubmitRequest is the public request before normalization and
// repetition expansion.
type BatchImageSubmitRequest struct {
	Model            string                 `json:"model"`              // client requested model
	TaskName         string                 `json:"task_name"`          // user visible title
	ParentBatchID    string                 `json:"parent_batch_id"`    // root related batch
	Platform         string                 `json:"platform"`           // batch execution backend
	Items            []BatchImageSubmitItem `json:"items"`              // unexpanded prompt item requests
	ResponseMimeType string                 `json:"response_mime_type"` // requested output MIME
	AspectRatio      string                 `json:"aspect_ratio"`       // requested image proportions
	ImageSize        string                 `json:"image_size"`         // requested output size
	Metadata         map[string]string      `json:"metadata"`           // bounded request labels
	// SessionID records the client session for usage reporting. Its JSON
	// exclusion keeps session changes out of the normalized request hash used by
	// idempotent submission.
	SessionID *string `json:"-"` // optional client session
}

// BatchImageSubmitItem supplies one prompt and its desired number of
// independent requests.
type BatchImageSubmitItem struct {
	CustomID        string                     `json:"custom_id"`                  // submitted item identity
	Prompt          string                     `json:"prompt"`                     // generation prompt text
	OutputCount     int                        `json:"output_count,omitempty"`     // requested repetition count
	ReferenceImages []BatchImageReferenceInput `json:"reference_images,omitempty"` // reference image inputs
}

// BatchImageReferenceInput decodes public image bytes or carries an internal
// GCS URI.
type BatchImageReferenceInput struct {
	ID       string `json:"id,omitempty"`   // reference source label
	Type     string `json:"type,omitempty"` // reference image role
	MimeType string `json:"mime_type"`      // image MIME type
	// Data and FileURI carry alternative public inputs.
	// NormalizeBatchImageReferenceInputs validates that one source is present,
	// and the request keeps its decoded bytes for submission.
	Data    []byte `json:"data,omitempty"`     // decoded image bytes
	FileURI string `json:"file_uri,omitempty"` // GCS source URI
}

// BatchImageOwner identifies the submitting actor, payer and selected funding
// source.
type BatchImageOwner struct {
	UserID int64 // submitting user identity
	// BillingUserID supplies a separate payer for team requests.
	// EffectiveBillingUserID uses UserID when this override is absent or
	// nonpositive, so funding still has a payer.
	BillingUserID int64  // funding payer identity
	TeamID        *int64 // optional originating team
	APIKeyID      int64  // submitting API key
	GroupID       *int64 // selected routing group
	// BillingMode and PreferredSubscriptionID are frozen into the submitted job.
	// Later reserve, capture and release use those values after the API key
	// configuration can change.
	BillingMode             string // submitted funding mode
	PreferredSubscriptionID *int64 // submitted preferred subscription
}

// BatchImagePricingSnapshot retains submission rates and estimated funding
// totals for later execution.
type BatchImagePricingSnapshot struct {
	BaseUnitPrice       float64 // USD base image price
	GroupRateMultiplier float64 // estimated group multiplier
	// SubscriptionRateMultiplier and BalanceRateMultiplier are separate funding
	// inputs. Versioned commands combine them with provider and hold scaling;
	// GroupRateMultiplier supplies the displayed estimate selected for this owner.
	SubscriptionRateMultiplier float64 // subscription group multiplier
	BalanceRateMultiplier      float64 // wallet group multiplier
	// PlanGroupRateEnabled lets billing select a subscription plan's group rate.
	// BuildHoldCommand negates this flag when constructing its disable-plan-rate
	// option.
	PlanGroupRateEnabled    bool    // plan rate selection
	ProviderRateMultiplier  float64 // provider price multiplier
	BatchDiscountMultiplier float64 // settlement discount multiplier
	HoldMultiplier          float64 // reservation price multiplier
	// BillableUnitPrice and HoldUnitPrice combine BaseUnitPrice, GroupRateMultiplier
	// and ProviderRateMultiplier with their respective discount or hold scale.
	// Their totals use the expanded request count before reservation.
	BillableUnitPrice float64 // USD estimated item charge
	HoldUnitPrice     float64 // USD estimated item reservation
	EstimatedCost     float64 // USD estimated charge
	HoldAmount        float64 // USD reserved total
}

// BatchImagePublicBatch exposes client-visible task state, counts and funding
// totals.
type BatchImagePublicBatch struct {
	ID       string `json:"id"`        // public batch identifier
	Object   string `json:"object"`    // public resource discriminator
	TaskName string `json:"task_name"` // user visible title
	// ParentBatchID retains the internal job's optional root-related batch
	// pointer during public conversion. Consumers treat that pointed-to
	// identifier as immutable while they render this response.
	ParentBatchID *string `json:"parent_batch_id,omitempty"` // root related batch
	Status        string  `json:"status"`                    // public job lifecycle stage
	Model         string  `json:"model"`                     // client requested model
	Platform      string  `json:"platform"`                  // batch execution backend
	ItemCount     int     `json:"item_count"`                // expanded request count
	SuccessCount  int     `json:"success_count"`             // successful item count
	FailCount     int     `json:"fail_count"`                // failed item count
	EstimatedCost float64 `json:"estimated_cost"`            // USD estimated charge
	HoldAmount    float64 `json:"hold_amount"`               // USD reserved total
	// ActualCost retains the stored job's captured USD pointer during public
	// conversion. Nil represents an unset charge, and consumers keep a present
	// pointed-to amount immutable while rendering.
	ActualCost      *float64 `json:"actual_cost"`                 // USD captured charge
	CreatedAt       int64    `json:"created_at"`                  // job creation Unix seconds
	SubmittedAt     *int64   `json:"submitted_at"`                // provider submission Unix seconds
	SettledAt       *int64   `json:"settled_at"`                  // successful settlement Unix seconds
	DownloadedAt    *int64   `json:"downloaded_at,omitempty"`     // first download Unix seconds
	OutputDeletedAt *int64   `json:"output_deleted_at,omitempty"` // output deletion Unix seconds
}

// BatchImagePublicItem exposes the indexed outcome and download metadata for
// one custom ID.
type BatchImagePublicItem struct {
	CustomID string `json:"custom_id"` // submitted item identity
	Status   string `json:"status"`    // public item outcome label
	// PromptPreview, MimeType and FileExtension retain the stored item's string
	// pointers during BatchImageItemToPublic conversion. Response consumers keep
	// those values immutable while rendering because later metadata readers share
	// them.
	PromptPreview *string `json:"prompt_preview,omitempty"` // stored prompt preview
	MimeType      *string `json:"mime_type"`                // image MIME type
	FileExtension *string `json:"file_extension"`           // image filename extension
	ImageCount    int     `json:"image_count"`              // returned image parts
	// Error carries the sanitized diagnostic for failed or cancelled internal
	// items. Pending and successful items retain nil here, matching the public
	// outcome produced by BatchImageItemToPublic.
	Error *BatchImagePublicError `json:"error"` // optional public failure detail
}

// BatchImagePublicError carries a sanitized diagnostic and its optional
// public origin.
type BatchImagePublicError struct {
	Code    string `json:"code"`             // public failure code
	Message string `json:"message"`          // public diagnostic text
	Source  string `json:"source,omitempty"` // public failure origin
}

// BatchImagePublicItemsResponse returns one item page and its full-page
// indicator.
type BatchImagePublicItemsResponse struct {
	Object  string                 `json:"object"`   // public resource discriminator
	Data    []BatchImagePublicItem `json:"data"`     // public item outcome page
	HasMore bool                   `json:"has_more"` // full page indicator
}

// BatchImagePublicListResponse returns one owner-scoped batch page and its
// full-page indicator.
type BatchImagePublicListResponse struct {
	Object  string                   `json:"object"`   // public resource discriminator
	Data    []*BatchImagePublicBatch `json:"data"`     // public batch resource page
	HasMore bool                     `json:"has_more"` // full page indicator
}

// BatchImagePublicModel identifies a requestable image model and its batch
// backend.
type BatchImagePublicModel struct {
	ID       string `json:"id"`       // requestable client model
	Object   string `json:"object"`   // public resource discriminator
	Platform string `json:"platform"` // batch execution backend
}

// BatchImagePublicModelsResponse returns the priced models available to this
// owner.
type BatchImagePublicModelsResponse struct {
	Object string                  `json:"object"` // public resource discriminator
	Data   []BatchImagePublicModel `json:"data"`   // priced client model choices
}

// BatchImageJobsQuery carries owner-list filters before cursor and time
// normalization.
type BatchImageJobsQuery struct {
	Status     string // public job status filter
	TaskName   string // user visible title
	Downloaded string // download acknowledgement filter
	From       string // creation lower bound input
	To         string // creation upper bound input
	Limit      int    // requested page size
	Cursor     string // decimal page offset
}

// BatchImageItemsQuery carries status and pagination inputs for one item
// listing.
type BatchImageItemsQuery struct {
	Status string // public item outcome filter
	Limit  int    // requested page size
	Cursor string // decimal page offset
}

// EffectiveBillingUserID returns the positive payer override when one was
// supplied for this owner. Personal requests and older callers with no
// separate payer use UserID for funding operations.
func (o BatchImageOwner) EffectiveBillingUserID() int64 {
	if o.BillingUserID > 0 {
		return o.BillingUserID
	}
	return o.UserID
}
