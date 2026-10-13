package app

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// Gemini quota display needs local per-model request, token, and provider-cost
// totals. The adapter narrows usage statistics into that shape and retains
// optional batch-total support only when the assembled usage repository
// implements the matching capability.

// providerGeminiUsageReader adapts local usage model statistics for Gemini
// quota display.
type providerGeminiUsageReader struct {
	source usage.UsageLogRepository // local model statistics
}

// providerGeminiUsageBatchReader adds the source's supported bulk totals to
// Gemini display reads.
type providerGeminiUsageBatchReader struct {
	// providerGeminiUsageReader and GeminiUsageTotalsBatchReader use the same
	// repository instance, with the latter exposed only after successful capability assertion during assembly.
	providerGeminiUsageReader             // individual model reads
	provider.GeminiUsageTotalsBatchReader // batched quota totals
}

// projectGeminiModelUsage converts per-model usage with provider cost and
// retains nil input. The result is a new slice of model, request, token, and
// cost values for the Gemini quota consumer.
func projectGeminiModelUsage(rows []usage.ModelStat) []provider.GeminiModelUsage {
	if rows == nil {
		return nil
	}
	values := make([]provider.GeminiModelUsage, len(rows))
	for i, row := range rows {
		values[i] = provider.GeminiModelUsage{Model: row.Model, Requests: row.Requests, TotalTokens: row.TotalTokens, ProviderCost: row.ProviderCost}
	}
	return values
}

// GetModelUsage requests local model statistics for one provider and supplied
// time range.
func (s providerGeminiUsageReader) GetModelUsage(ctx context.Context, id int64, start, end time.Time) ([]provider.GeminiModelUsage, error) {
	rows, err := s.source.GetModelStatsWithFilters(ctx, start, end, 0, 0, id, 0, nil, nil, nil)
	return projectGeminiModelUsage(rows), err
}

// newProviderGeminiUsageReader returns nil when no usage source is supplied.
// Otherwise it adds batch totals only when the source implements that
// interface, preserving capability detection for Gemini quota queries.
func newProviderGeminiUsageReader(source usage.UsageLogRepository) provider.GeminiQuotaUsageReader {
	if source == nil {
		return nil
	}
	reader := providerGeminiUsageReader{source: source}
	if batch, ok := source.(provider.GeminiUsageTotalsBatchReader); ok {
		return providerGeminiUsageBatchReader{reader, batch}
	}
	return reader
}
