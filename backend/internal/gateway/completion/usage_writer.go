package completion

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
)

// Background usage contributes model and financial aliases to a request summary
// that the entrypoint may already have finished. Matching capture updates therefore
// leave its timing and terminal state intact. A different task ID instead receives
// a related completion record through the bound observer, keeping polling traffic
// separate from the task whose measured work is being logged.
//
// Usage persistence follows the funds attempt. Best-effort rejection triggers one
// synchronous Create call, using a fresh detached context when the earlier budget
// has expired. These writes keep the same analysis identity and never invoke
// settlement again. Missing logs skip both persistence and summary supplementation;
// configured write failures are reported through the recorder observer.

// WriteUsage supplements request metadata and attempts analysis persistence.
// Missing Logs or row returns immediately. ClientModel can override the row's
// requested display name, and matching request capture receives provider/model
// updates and aliases while keeping entrypoint timing and outcome.
//
// A nonmatching ID produces a completion record through RequestRecords. Storage
// runs under detached fifteen-second contexts; best-effort errors cause a single
// synchronous retry, with a renewed context when that budget has expired. Callers
// own the row during these updates and use SnapshotLogWriter when repositories
// may mutate their arguments. Write errors are observed and do not resubmit funds.
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
		// A matched capture may already be failed or cancelled; supplement its model
		// and aliases while retaining the entrypoint's terminal state, timing, and status.
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
			// A task or otherwise unmatched ID needs its own summary; submitting this
			// record leaves the polling request's lifecycle and financial aliases unchanged.
			s.requestRecords(record)
		}
	}
	usageCtx, cancel := detachedBillingContext(ctx)
	defer cancel()

	if writer, ok := s.logs.(BestEffortLogWriter); ok {
		if err := writer.CreateBestEffort(usageCtx, usageLog); err != nil {
			s.printf(logKey, "Create usage log failed: %v", err)
			// Rejected analysis writes retry from the same row after the funds outcome
			// is already known; this path performs persistence alone and can also fail.
			fallbackCtx := usageCtx
			if usageCtx.Err() != nil {
				// An expired best-effort budget would reject the retry immediately; a fresh
				// detached timeout lets synchronous persistence make one additional attempt.
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

	// A synchronous-only repository has no queue recovery path; report its failure
	// while leaving the earlier funds outcome and captured analysis identity intact.
	if _, err := s.logs.Create(usageCtx, usageLog); err != nil {
		s.printf(logKey, "Create usage log failed: %v", err)
	}
}

// applyProviderStatsCost attaches a provider base amount independently of user
// charges. A configured price resolver takes precedence over the separate statistics
// source, and no source leaves the existing field unchanged.
//
// The requested model substitutes for absent upstream evidence, with Qoder asking
// the resolver to prefer its requested alias. ImageCount determines request units
// when positive, otherwise one is used. The returned optional value distinguishes
// no statistics price from an explicitly zero provider expense; settlement later
// multiplies that selected base by the executing provider's rate.
func (s *Recorder) applyProviderStatsCost(ctx context.Context, row *UsageLog, providerID, groupID int64, upstream, requested, mapped string, tokens UsageTokens) {
	if upstream == "" {
		upstream = requested
	}
	count := 1
	if row.ImageCount > 0 {
		count = row.ImageCount
	}
	source := s.stats
	// The shared resolver combines current provider rules and catalogue pricing;
	// use the separate statistics binding only when that resolver is unavailable.
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
