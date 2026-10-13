package batchimage

import (
	"fmt"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// Provider operations read names and resource references from nullable
// persisted job fields. These accessors trim stored text and turn absent
// fields into empty strings, while provider input failures keep their
// diagnostic cause under a stable application error.

// Provider errors distinguish unsupported credentials, missing resource
// references, malformed inputs and unsafe cleanup targets for public error
// mapping.
var (
	ErrBatchImageProviderUnsupportedProvider     = apperror.New(apperror.CategoryBadRequest, "BATCH_IMAGE_PROVIDER_UNSUPPORTED_PROVIDER", "batch image platform does not support this provider")
	ErrBatchImageProviderMissingAPIKey           = apperror.New(apperror.CategoryBadRequest, "BATCH_IMAGE_PROVIDER_MISSING_API_KEY", "batch image platform provider is missing api key")
	ErrBatchImageProviderMissingServiceAccount   = apperror.New(apperror.CategoryBadRequest, "BATCH_IMAGE_PROVIDER_MISSING_SERVICE_ACCOUNT", "batch image platform provider is missing service account credentials")
	ErrBatchImageProviderMissingJobName          = apperror.New(apperror.CategoryBadRequest, "BATCH_IMAGE_PROVIDER_MISSING_JOB_NAME", "batch image platform job name is missing")
	ErrBatchImageProviderMissingResultRef        = apperror.New(apperror.CategoryBadRequest, "BATCH_IMAGE_PROVIDER_MISSING_RESULT_REF", "batch image platform result reference is missing")
	ErrBatchImageProviderInlineResultUnsupported = apperror.New(apperror.CategoryBadRequest, "GEMINI_INLINE_BATCH_RESULT_UNSUPPORTED", "Gemini inline batch result is not supported")
	ErrBatchImageProviderInvalidInput            = apperror.New(apperror.CategoryBadRequest, "BATCH_IMAGE_PROVIDER_INVALID_INPUT", "invalid batch image platform input")
	ErrBatchImageProviderUnsafeCleanupPath       = apperror.New(apperror.CategoryBadRequest, "VERTEX_UNSAFE_CLEANUP_PATH", "unsafe batch image cleanup path")
	ErrUnsupportedCleanupTarget                  = apperror.New(apperror.CategoryBadRequest, "BATCH_IMAGE_PROVIDER_UNSUPPORTED_CLEANUP_TARGET", "unsupported batch image cleanup target")
)

// BatchImageProviderJobName returns the trimmed upstream job name retained by
// a batch. A nil job or absent name produces an empty string for provider
// validation to reject.
func BatchImageProviderJobName(job *BatchImageJob) string {
	if job == nil || job.ProviderJobName == nil {
		return ""
	}
	return strings.TrimSpace(*job.ProviderJobName)
}

// BatchImageProviderInputRef returns the trimmed provider input reference
// retained by a batch. A nil job or absent input field produces an empty
// string for cleanup or submission handling.
func BatchImageProviderInputRef(job *BatchImageJob) string {
	if job == nil || job.ProviderInputRef == nil {
		return ""
	}
	return strings.TrimSpace(*job.ProviderInputRef)
}

// BatchImageProviderOutputRef returns the trimmed provider output reference
// retained by a batch. A nil job or absent output field produces an empty
// string for result validation to reject.
func BatchImageProviderOutputRef(job *BatchImageJob) string {
	if job == nil || job.ProviderOutputRef == nil {
		return ""
	}
	return strings.TrimSpace(*job.ProviderOutputRef)
}

// BatchImageProviderInputError attaches a formatted cause to the provider
// input error category.
func BatchImageProviderInputError(format string, args ...any) error {
	return ErrBatchImageProviderInvalidInput.WithCause(fmt.Errorf(format, args...))
}
