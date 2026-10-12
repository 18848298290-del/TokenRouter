package completion

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
)

func (s *Recorder) WriteUsage(ctx context.Context, usageLog *UsageLog, logKey string) {
	if s.logs == nil || usageLog == nil {
		return
	}
	applyClientModel(ctx, usageLog)
	if s.requestRecords != nil {
		now := time.Now().UTC()
		record := telemetry.RequestRecord{RequestID: usageLog.RequestID, StartedAt: usageLog.CreatedAt, UpdatedAt: now, FinishedAt: &now, State: "completed", UserID: usageLog.UserID, APIKeyID: usageLog.APIKeyID, ProviderID: usageLog.ProviderID, Model: usageLog.RequestedModel, Platform: usageLog.Platform}
		if record.StartedAt.IsZero() {
			record.StartedAt = now
		}
		if usageLog.TeamID != nil {
			record.TeamID = *usageLog.TeamID
		}
		if usageLog.InboundEndpoint != nil {
			record.Path = *usageLog.InboundEndpoint
		}
		if usageLog.DurationMs != nil {
			record.DurationMs = int64(*usageLog.DurationMs)
		}
		if usageLog.BillingKey != "" {
			record.Aliases = append(record.Aliases, telemetry.RequestAlias{Kind: "billing", Value: usageLog.BillingKey})
		}
		if usageLog.UpstreamRequestID != nil {
			record.Aliases = append(record.Aliases, telemetry.RequestAlias{Kind: "upstream", Value: *usageLog.UpstreamRequestID})
		}
		// Entrypoints own HTTP and WS-turn timing; background usage adds model and correlation data.
		if telemetry.UpdateRequestForID(ctx, record.RequestID, func(current *telemetry.RequestRecord) {
			if record.Model != "" {
				current.Model = record.Model
			}
			if record.ProviderID > 0 {
				current.ProviderID = record.ProviderID
			}
			if record.Platform != "" {
				current.Platform = record.Platform
			}
		}) {
			for _, alias := range record.Aliases {
				telemetry.AddRequestAlias(ctx, alias.Kind, alias.Value)
			}
		} else {
			s.requestRecords(record)
		}
	}
	usageCtx, cancel := detachedBillingContext(ctx)
	defer cancel()

	if writer, ok := s.logs.(BestEffortLogWriter); ok {
		if err := writer.CreateBestEffort(usageCtx, usageLog); err != nil {
			s.printf(logKey, "Create usage log failed: %v", err)
			// Retry failed best-effort writes synchronously so settled and reconcilable requests retain usage logs.
			// Failed settlements use ActualCost=0; deduplication handles billing keys and historical request IDs.
			fallbackCtx := usageCtx
			if usageCtx.Err() != nil {
				// Queue waiting exhausted the context; give the synchronous write a fresh timeout.
				var fallbackCancel context.CancelFunc
				fallbackCtx, fallbackCancel = detachedBillingContext(context.Background())
				defer fallbackCancel()
			}
			if _, syncErr := s.logs.Create(fallbackCtx, usageLog); syncErr != nil {
				s.printf(logKey, "Create usage log sync fallback failed: %v", syncErr)
			}
		}
		return
	}

	if _, err := s.logs.Create(usageCtx, usageLog); err != nil {
		s.printf(logKey, "Create usage log failed: %v", err)
	}
}

// applyProviderStatsCost resolves provider base costs independently of user charges.
func (s *Recorder) applyProviderStatsCost(ctx context.Context, row *UsageLog, providerID, groupID int64, upstream, requested, mapped string, tokens UsageTokens) {
	if upstream == "" {
		upstream = requested
	}
	count := 1
	if row.ImageCount > 0 {
		count = row.ImageCount
	}
	source := s.stats
	if s.resolver != nil {
		source = s.resolver
	}
	if source == nil {
		return
	}
	row.ProviderStatsCost = source.ResolveProviderStats(ctx, billing.ProviderStatsCostInput{
		PreferRequestedModel: row.Platform == "qoder", ProviderID: providerID, GroupID: groupID, UpstreamModel: upstream, RequestedModel: requested, MappedModel: mapped, Tokens: tokens, RequestCount: count, ServiceTier: stringValueOrEmpty(row.ServiceTier), ReasoningEffort: stringValueOrEmpty(row.ReasoningEffort),
	})
}
