package batchimage

import (
	"strings"
	"time"
)

// A public batch identifies the client model and mapped lifecycle state while
// internal provider resources remain in the stored job. Item failures expose
// a stable category, optional origin and scrubbed message, so result listings
// can distinguish provider failures from indexing problems.

// The public message byte cap bounds diagnostics included in item responses
// after internal resource markers have been screened.
const maxBatchImagePublicErrorChars = 500

// BatchImageJobToPublic renders client-visible job state or returns nil for a
// nil job. Absent hold snapshots use EstimatedCost; optional scalar pointers
// remain shared with the job while timestamp pointers receive new Unix-second
// values.
func BatchImageJobToPublic(job *BatchImageJob) *BatchImagePublicBatch {
	if job == nil {
		return nil
	}
	holdAmount := job.EstimatedCost
	if job.HoldAmount != nil {
		holdAmount = *job.HoldAmount
	}
	return &BatchImagePublicBatch{
		ID:              job.BatchID,
		Object:          "image.batch",
		TaskName:        BatchImagePublicTaskName(job),
		ParentBatchID:   job.ParentBatchID,
		Status:          PublicBatchImageStatus(job.Status),
		Model:           BatchImageRequestedModel(job),
		Platform:        job.Platform,
		ItemCount:       job.ItemCount,
		SuccessCount:    job.SuccessCount,
		FailCount:       job.FailCount,
		EstimatedCost:   job.EstimatedCost,
		HoldAmount:      holdAmount,
		ActualCost:      job.ActualCost,
		CreatedAt:       job.CreatedAt.Unix(),
		SubmittedAt:     BatchImageUnixPtr(job.SubmittedAt),
		SettledAt:       BatchImageUnixPtr(job.SettledAt),
		DownloadedAt:    BatchImageUnixPtr(job.DownloadedAt),
		OutputDeletedAt: BatchImageUnixPtr(job.OutputDeletedAt),
	}
}

// BatchImageItemToPublic renders a nonnil indexed item as pending, succeeded
// or failed. Optional prompt, MIME and extension pointers remain attached to
// the stored metadata. Failed and cancelled internal items receive a new
// public error with a screened message and inferred origin.
func BatchImageItemToPublic(item *BatchImageItem) BatchImagePublicItem {
	out := BatchImagePublicItem{
		CustomID:      item.CustomID,
		Status:        "failed",
		PromptPreview: item.PromptPreview,
		MimeType:      item.MimeType,
		FileExtension: item.FileExtension,
		ImageCount:    item.ImageCount,
	}
	if item.Status == BatchImageItemStatusPending {
		out.Status = "pending"
		return out
	}
	if item.Status == BatchImageItemStatusSuccess {
		out.Status = "succeeded"
		return out
	}
	out.Error = &BatchImagePublicError{
		Code:    BatchImageDerefString(item.ErrorCode),
		Message: SanitizeBatchImagePublicMessage(BatchImageDerefString(item.ErrorMessage)),
		Source:  BatchImageItemErrorSource(item),
	}
	return out
}

// BatchImageUnixPtr returns fresh pointer storage containing a present
// timestamp's Unix seconds. Nil timestamps remain nil, preserving absent
// public lifecycle times while each converted value has independent scalar
// storage.
func BatchImageUnixPtr(t *time.Time) *int64 {
	if t == nil {
		return nil
	}
	v := t.Unix()
	return &v
}

// BatchImageItemErrorSource classifies a recorded failure origin for public
// rendering. Nil items or an absent ErrorCode return empty text. When that
// pointer is present, a nonblank source object takes provider precedence;
// otherwise known provider and indexing categories select an origin, and
// unrecognized codes return empty text.
func BatchImageItemErrorSource(item *BatchImageItem) string {
	if item == nil || item.ErrorCode == nil {
		return ""
	}
	code := strings.TrimSpace(*item.ErrorCode)
	if BatchImageDerefString(item.ProviderSourceObject) != "" {
		return "provider"
	}
	switch code {
	case "EMPTY_IMAGE_OUTPUT", "PROVIDER_ITEM_FAILED":
		return "provider"
	case "INDEX_OUTPUT_MISSING", "INDEX_PARSE_FAILED", "DUPLICATE_CUSTOM_ID_IN_OUTPUT":
		return "system"
	default:
		return ""
	}
}

// PublicBatchImageStatus maps persisted stages into the public batch
// lifecycle. Creation, uploading and submission all become queued; indexing
// becomes processing_results, and an unrecognized status passes through for
// the caller.
func PublicBatchImageStatus(status string) string {
	switch status {
	case BatchImageJobStatusCreated, BatchImageJobStatusUploading, BatchImageJobStatusSubmitted:
		return "queued"
	case BatchImageJobStatusRunning:
		return "running"
	case BatchImageJobStatusIndexing:
		return "processing_results"
	case BatchImageJobStatusSettling:
		return "settling"
	case BatchImageJobStatusCompleted:
		return "completed"
	case BatchImageJobStatusFailed:
		return "failed"
	case BatchImageJobStatusCancelled:
		return "cancelled"
	case BatchImageJobStatusOutputDeleted:
		return "output_deleted"
	default:
		return status
	}
}

// BatchImagePublicTaskName returns a trimmed saved title, or empty text for
// nil jobs. Jobs with no saved title receive a formatted creation time
// through DefaultBatchImageTaskName for client display.
func BatchImagePublicTaskName(job *BatchImageJob) string {
	if job == nil {
		return ""
	}
	if strings.TrimSpace(job.TaskName) != "" {
		return strings.TrimSpace(job.TaskName)
	}
	return DefaultBatchImageTaskName(job.CreatedAt)
}

// DefaultBatchImageTaskName formats the supplied local time as a default
// batch title. A zero time selects time.Now, so callers requiring a fixed
// title provide the intended creation time.
func DefaultBatchImageTaskName(now time.Time) string {
	if now.IsZero() {
		now = time.Now()
	}
	return now.Format("2006-01-02 15:04:05")
}

// SanitizeBatchImagePublicMessage trims diagnostics and replaces text
// containing gs://, files/ or projects/ with a generic upstream failure.
// Remaining text is capped at 500 bytes; this marker screen does not validate
// arbitrary secret content.
func SanitizeBatchImagePublicMessage(message string) string {
	message = strings.TrimSpace(message)
	// Provider errors can embed internal resource names; replacing the entire
	// message prevents those path fragments from reaching public item responses.
	for _, marker := range []string{"gs://", "files/", "projects/"} {
		if strings.Contains(message, marker) {
			message = "upstream platform operation failed"
			break
		}
	}
	if len(message) > maxBatchImagePublicErrorChars {
		// Public diagnostics retain at most 500 bytes, even when the stored
		// provider error contains a longer message.
		message = message[:maxBatchImagePublicErrorChars]
	}
	return message
}
