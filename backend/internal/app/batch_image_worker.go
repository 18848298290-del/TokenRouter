package app

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/batchimage"
	batchprovider "github.com/TokenFlux/TokenRouter/internal/batchimage/provider"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/gateway/completion"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/pkg/querycache"
	providerpostgres "github.com/TokenFlux/TokenRouter/internal/provider/postgres"
	"github.com/TokenFlux/TokenRouter/internal/requestlog"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// batchPricingGroups reads group fields and billing settings used to quote batch tasks.
type batchPricingGroups struct {
	source   routing.GroupRepository
	settings *routing.PricingConfigService
}

func (r batchPricingGroups) GetByIDLite(ctx context.Context, id int64) (*batchimage.GroupView, error) {
	value, err := r.source.GetByIDLite(ctx, id)
	if value == nil {
		return nil, err
	}
	settings := r.settings.GetEffectiveBillingSettings(ctx, id)
	return &batchimage.GroupView{ID: value.ID, AllowBatchImageGeneration: value.AllowBatchImageGeneration, RateMultiplier: value.RateMultiplier, BatchImageDiscountMultiplier: settings.BatchImageDiscountMultiplier, BatchImageHoldMultiplier: settings.BatchImageHoldMultiplier}, err
}

func provideBatchPricing(resolver *billing.PriceResolver, groups routing.GroupRepository, configs *routing.PricingConfigService) *batchimage.Pricing {
	return &batchimage.Pricing{Resolver: resolver, GroupRepo: batchPricingGroups{groups, configs}}
}

func batchVertexOptions(cfg *config.Config) batchprovider.VertexBatchImageProviderOptions {
	if cfg == nil {
		return batchprovider.VertexBatchImageProviderOptions{}
	}
	return batchprovider.VertexBatchImageProviderOptions{
		Enabled:                cfg.BatchImage.VertexEnabled,
		ProjectID:              cfg.BatchImage.VertexProjectID,
		Location:               cfg.BatchImage.VertexLocation,
		ManagedGCSBucket:       cfg.BatchImage.VertexManagedGCSBucket,
		ManagedGCSPrefix:       cfg.BatchImage.VertexManagedGCSPrefix,
		Environment:            cfg.Log.Environment,
		InputRetentionHours:    cfg.BatchImage.VertexInputRetentionHours,
		OutputRetentionHours:   cfg.BatchImage.VertexOutputRetentionHours,
		BatchPredictionBaseURL: cfg.BatchImage.VertexBatchPredictionBaseURL,
		GCSBaseURL:             cfg.BatchImage.VertexGCSBaseURL,
	}
}

// provideBatchRuntime binds batch image funding, processing, settlement, recovery, and worker loops.
func provideBatchRuntime(requests *requestlog.Service, repo batchimage.BatchImageRepository, providers *providerpostgres.ProviderStore, queue batchimage.BatchImageQueue, funds *billing.Funds, logs usage.UsageLogRepository, pricing *batchimage.Pricing, auth apikey.APIKeyAuthCacheInvalidator, cfg *config.Config, registry *batchimage.Registry[batchprovider.BatchImageProvider]) *batchimage.Runtime {
	funding := batchimage.Funding{Store: funds, Observe: creativeObserve}
	processor := &batchimage.ProviderProcessor{Repo: repo, Funding: funding, Observe: creativeObserve, ResolveProvider: (batchprovider.ResultAccess{Registry: registry, Providers: providers}).Process}
	settlement := &batchimage.Settlement{Repo: repo, Funding: funding, Observe: creativeObserve}
	if cfg != nil {
		settlement.Retention = time.Duration(cfg.BatchImage.OutputRetentionAfterTerminalHours) * time.Hour
	}
	if pricing != nil {
		settlement.Quote = func(ctx context.Context, model string, group *int64, size string) (float64, error) {
			return pricing.BatchImageUnitPrice(ctx, batchimage.BatchImagePriceInput{Model: model, GroupID: group, ImageSize: size})
		}
	}
	if logs != nil {
		recorder := completion.NewRecorder(completion.Dependencies{RequestRecords: requests.Observe, Logs: completion.SnapshotLogWriter(logs), Observe: func(component, message string) { logging.LegacyPrintf(component, "%s", message) }}, completion.RecorderOptions{})
		settlement.RecordUsage = func(ctx context.Context, row *usage.UsageLog) {
			recorder.WriteUsage(ctx, querycache.Clone(row), "service.batch_image_settlement")
		}
	}
	options := batchWorkerOptions(cfg)
	options.Observe = creativeObserve
	recovery := &batchimage.BillingRecovery{Repo: repo, Funding: funding, Queue: queue, StaleAfter: options.StaleActiveAfter, Limit: options.RecoverLimit, Observe: creativeObserve}
	if auth != nil {
		processor.InvalidateAuth = auth.InvalidateAuthCacheByUserID
		settlement.InvalidateAuth = auth.InvalidateAuthCacheByUserID
		recovery.InvalidateAuth = auth.InvalidateAuthCacheByUserID
	}
	worker := batchimage.NewBatchImageWorker(queue, &batchimage.PipelineProcessor{ProviderProcessor: processor, SettlementService: settlement}, options)
	return batchimage.NewWorkerRuntime(worker, recovery, cfg != nil && cfg.BatchImage.QueueEnabled)
}

// batchWorkerOptions reads startup configuration; the batch image module supplies defaults and validation.
func batchWorkerOptions(cfg *config.Config) batchimage.BatchImageWorkerOptions {
	if cfg == nil {
		return batchimage.NormalizeBatchImageWorkerOptions(batchimage.BatchImageWorkerOptions{})
	}
	return batchimage.NormalizeBatchImageWorkerOptions(batchimage.BatchImageWorkerOptions{
		JobLockTTL:          time.Duration(cfg.BatchImage.JobLockTTLSeconds) * time.Second,
		LockConflictDelay:   time.Duration(cfg.BatchImage.LockConflictDelaySeconds) * time.Second,
		DefaultRequeueDelay: time.Duration(cfg.BatchImage.DefaultRequeueDelaySeconds) * time.Second,
		ErrorRetryDelay:     time.Duration(cfg.BatchImage.ErrorRetryDelaySeconds) * time.Second,
		DelayedPollInterval: time.Duration(cfg.BatchImage.DelayedMoverIntervalSeconds) * time.Second,
		RecoveryInterval:    time.Duration(cfg.BatchImage.RecoveryIntervalSeconds) * time.Second,
		StaleActiveAfter:    time.Duration(cfg.BatchImage.StaleActiveAfterSeconds) * time.Second,
		DelayedMoveLimit:    cfg.BatchImage.DelayedMoveLimit,
		RecoverLimit:        cfg.BatchImage.RecoverLimit,
	})
}
