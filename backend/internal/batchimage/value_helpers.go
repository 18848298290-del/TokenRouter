package batchimage

import (
	"strings"
)

// value_helpers.go resolves stored model identities and normalizes nullable
// text and image reference values.
// Nullable stored text uses trimmed empty values when a job has no recorded
// field. Model accessors retain the client and internal routing identities
// when present and use the upstream snapshot for older jobs, keeping public
// rendering and usage logging compatible.

// BatchImageDerefString reads and trims a nullable stored string value. Nil
// pointers return an empty string, which callers use to test whether a job
// reference or fingerprint is present.
func BatchImageDerefString(v *string) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(*v)
}

// BatchImageStringPtr returns pointer storage containing the supplied string
// unchanged.
func BatchImageStringPtr(v string) *string {
	return &v
}

// BatchImageOptionalStringPtr trims a string and returns pointer storage for
// a present value. Empty or whitespace-only input returns nil, matching
// nullable repository fields produced by submission and indexing.
func BatchImageOptionalStringPtr(v string) *string {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	return &v
}

// TruncateBatchImageMessage trims text and caps its byte length when limit is
// positive. Nonpositive limits retain all trimmed text; byte slicing can end
// inside a multibyte UTF-8 character.
func TruncateBatchImageMessage(message string, limit int) string {
	message = strings.TrimSpace(message)
	if limit <= 0 || len(message) <= limit {
		return message
	}
	// This limit applies to stored diagnostic bytes; slicing retains the
	// requested prefix even when UTF-8 ends partially.
	return message[:limit]
}

// BatchImageRequestedModel returns the trimmed client model snapshot or an
// empty string for nil input. Older jobs lacking that snapshot expose their
// upstream Model value as stored.
func BatchImageRequestedModel(job *BatchImageJob) string {
	if job == nil {
		return ""
	}
	if requestedModel := strings.TrimSpace(job.RequestedModel); requestedModel != "" {
		return requestedModel
	}
	return job.Model
}

// BatchImageInternalModel returns the trimmed internal routing snapshot or an
// empty string for nil input. Older jobs lacking that snapshot use the
// trimmed upstream model for usage reporting.
func BatchImageInternalModel(job *BatchImageJob) string {
	if job == nil {
		return ""
	}
	if internalModel := strings.TrimSpace(job.InternalModel); internalModel != "" {
		return internalModel
	}
	return strings.TrimSpace(job.Model)
}

// NormalizeBatchImageReferenceMimeType canonicalizes supported PNG, JPEG and
// WebP input MIME types. Case and surrounding whitespace are normalized,
// image/jpg becomes image/jpeg, and unsupported types return an empty string.
func NormalizeBatchImageReferenceMimeType(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "image/jpeg", "image/jpg":
		return "image/jpeg"
	case "image/png":
		return "image/png"
	case "image/webp":
		return "image/webp"
	default:
		return ""
	}
}

// BatchImageGCSRef returns a trimmed gs:// reference for the Vertex backend.
// Other backends and references with a different prefix produce an empty
// string for nullable GCS job fields.
func BatchImageGCSRef(platform, ref string) string {
	if platform == BatchImageProviderVertex && strings.HasPrefix(strings.TrimSpace(ref), "gs://") {
		return strings.TrimSpace(ref)
	}
	return ""
}
