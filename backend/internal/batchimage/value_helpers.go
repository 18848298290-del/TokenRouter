package batchimage

import (
	"strings"
)

func BatchImageDerefString(v *string) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(*v)
}

func BatchImageStringPtr(v string) *string {
	return &v
}

func BatchImageOptionalStringPtr(v string) *string {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	return &v
}

func TruncateBatchImageMessage(message string, limit int) string {
	message = strings.TrimSpace(message)
	if limit <= 0 || len(message) <= limit {
		return message
	}
	return message[:limit]
}

// BatchImageRequestedModel returns the client model, using the upstream model for older jobs.
func BatchImageRequestedModel(job *BatchImageJob) string {
	if job == nil {
		return ""
	}
	if requestedModel := strings.TrimSpace(job.RequestedModel); requestedModel != "" {
		return requestedModel
	}
	return job.Model
}

// BatchImageInternalModel returns the submission routing model, using the upstream model for older jobs.
func BatchImageInternalModel(job *BatchImageJob) string {
	if job == nil {
		return ""
	}
	if internalModel := strings.TrimSpace(job.InternalModel); internalModel != "" {
		return internalModel
	}
	return strings.TrimSpace(job.Model)
}

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

func BatchImageGCSRef(platform, ref string) string {
	if platform == BatchImageProviderVertex && strings.HasPrefix(strings.TrimSpace(ref), "gs://") {
		return strings.TrimSpace(ref)
	}
	return ""
}
