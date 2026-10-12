package batchimage_test

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/batchimage"
	batchimageprovider "github.com/TokenFlux/TokenRouter/internal/batchimage/provider"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/gateway/completion"
	"github.com/TokenFlux/TokenRouter/internal/gateway/modeltrace"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/pkg/querycache"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

const batchImageTestData = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJ"

var (
	_ batchimage.BatchImageDownloadLimiter = (*fakeBatchImageDownloadLimiter)(nil)
	_ batchimage.BatchImageDownloadPermit  = (*fakeBatchImageDownloadPermit)(nil)

	_ batchProvidersFixtureSource           = (*publicBatchImageProviderRepo)(nil)
	_ batchimage.BatchImageQueue            = (*publicBatchImageQueue)(nil)
	_ batchimageprovider.BatchImageProvider = (*publicBatchImageProvider)(nil)

	_ batchGroupFixtureSource                      = (*publicBatchImageGroupRepo)(nil)
	_ batchimage.BatchImageUserGroupRateRepository = (*publicBatchImageUserGroupRateRepo)(nil)

	_ batchimage.FundingStore = (*fakeBatchImageBillingRepo)(nil)
	_ batchimage.ImagePricer  = (*fakeBatchImagePricingResolver)(nil)
)

type fakeBatchImageDownloadLimiter struct {
	acquireCount int
	releaseCount int
	deny         bool
}

type fakeBatchImageDownloadPermit struct {
	once    bool
	release func()
}

type fakeBatchImageProviderResolver struct {
	provider *providercore.Record
	err      error
}

type fakeProcessorProvider struct {
	status *batchimage.BatchProviderStatus
	getErr error
	result string

	getCalled        bool
	openResultCalled bool
}

// batchProvidersFixtureSource supplies records to production candidate filtering and funding code.
type batchProvidersFixtureSource interface {
	GetByID(context.Context, int64) (*providercore.Record, error)
	ListSchedulableByPlatform(context.Context, string) ([]providercore.Record, error)
	ListSchedulableByGroupIDAndPlatform(context.Context, int64, string) ([]providercore.Record, error)
}

type batchGroupFixtureSource interface {
	GetByIDLite(context.Context, int64) (*batchimage.GroupView, error)
}

type batchProviderFixture struct {
	source   batchProvidersFixtureSource
	registry *batchimage.Registry[batchimageprovider.BatchImageProvider]
}

type batchGroupReader struct{ source batchGroupFixtureSource }

type publicBatchImageProviderRepo struct {
	providers []providercore.Record
}

type publicBatchImageQueue struct {
	enqueued []string
	err      error
}

type publicBatchImageGroupRepo struct {
	groups map[int64]*batchimage.GroupView
}

type publicBatchImageUserGroupRateRepo struct {
	rates map[int64]*float64
}

type publicBatchImageProvider struct {
	name           string
	submits        []batchimage.BatchImageInput
	submitErr      error
	cancelCount    int
	cancelErr      error
	result         string
	cleanupTargets []batchimage.CleanupTarget
	cleanupErr     error
}

type fakeBatchImageRepository struct {
	jobs          map[string]*batchimage.BatchImageJob
	items         map[string][]batchimage.CreateBatchImageItemParams
	counts        map[string]batchimage.BatchImageCounts
	transitions   map[string][]string
	events        map[string][]string
	transitionErr error
	replaceCalls  int
}

// resultProviderFixture returns the bound provider for download and cleanup tests.
type resultProviderFixture struct{ provider *providercore.Record }

type fakeBatchImagePricingResolver struct {
	unitPrice     float64
	missingModels map[string]bool
	err           error
	models        []string
}

type fakeBatchImageBillingRepo struct {
	usableSubscription *billing.UserSubscription
	subscriptionErr    error
	reserves           []*billing.TaskFundsCommand
	captures           []*billing.TaskFundsCommand
	releases           []*billing.TaskFundsCommand
	seen               map[string]struct{}
	alreadyApplied     map[string]bool

	err        error
	reserveErr error
	captureErr error
	releaseErr error
}

type fakeBatchImageQueue struct {
	reserved     batchimage.ReservedBatchImageJob
	lockAcquired bool
	acked        []string
	requeued     []fakeBatchImageRequeue
	releaseCount int
}

type fakeBatchImageRequeue struct {
	batchID string
	delay   time.Duration
}

type fakeBatchImageLock struct {
	release func()
	queue   *fakeBatchImageQueue
}

type fakeBatchImageProcessor struct {
	result    batchimage.BatchImageProcessResult
	err       error
	processed []string
}

func newTestBatchImageDownloadService() (*batchimage.Download, *fakeBatchImageRepository, *fakeBatchImageDownloadLimiter) {
	repo := newFakeBatchImageRepository()
	apiKeyID := int64(22)
	providerID := int64(101)
	repo.jobs["imgbatch_download"] = &batchimage.BatchImageJob{
		BatchID:           "imgbatch_download",
		UserID:            11,
		APIKeyID:          &apiKeyID,
		ProviderID:        &providerID,
		Platform:          batchimage.BatchImageProviderGeminiAPI,
		Model:             "gemini-2.5-flash-image",
		Status:            batchimage.BatchImageJobStatusCompleted,
		ProviderJobName:   batchimage.BatchImageStringPtr("providers/internal/job"),
		ProviderOutputRef: batchimage.BatchImageStringPtr("gs://bucket/internal/output.jsonl"),
		ItemCount:         3,
		SuccessCount:      2,
		FailCount:         1,
		CreatedAt:         time.Now(),
	}
	mime := "image/png"
	ext := "png"
	webp := "image/webp"
	webpExt := "webp"
	code := "SAFETY_BLOCKED"
	msg := "blocked in gs://bucket/internal/output.jsonl"
	repo.items["imgbatch_download"] = []batchimage.CreateBatchImageItemParams{
		{JobID: "imgbatch_download", CustomID: "cover/../001", Status: batchimage.BatchImageItemStatusSuccess, MimeType: &mime, FileExtension: &ext, ImageCount: 2},
		{JobID: "imgbatch_download", CustomID: "bad", Status: batchimage.BatchImageItemStatusFailed, ErrorCode: &code, ErrorMessage: &msg},
		{JobID: "imgbatch_download", CustomID: "ok_2", Status: batchimage.BatchImageItemStatusSuccess, MimeType: &webp, FileExtension: &webpExt, ImageCount: 1},
	}
	platform := &publicBatchImageProvider{name: batchimage.BatchImageProviderGeminiAPI, result: batchImageDownloadResultJSONL()}
	limiter := &fakeBatchImageDownloadLimiter{}
	svc := newBatchDownloadFixture(repo, batchimage.NewRegistry[batchimageprovider.BatchImageProvider](platform), &resultProviderFixture{provider: &providercore.Record{ID: providerID, Platform: capability.PlatformGemini, Type: capability.ProviderTypeAPIKey, Status: billing.StatusActive, Schedulable: true}}, limiter, &config.Config{BatchImage: config.BatchImageConfig{MaxDownloadItemsZip: 10, MaxDownloadDurationSeconds: 60}})
	return svc, repo, limiter
}

func batchImageDownloadResultJSONL() string {
	return strings.Join([]string{
		`{"key":"cover/../001","response":{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"Zmlyc3Q="}},{"inlineData":{"mimeType":"image/jpeg","data":"c2Vjb25k"}}]}}]}}`,
		`{"key":"bad","error":{"code":"SAFETY","message":"blocked"}}`,
		`{"key":"ok_2","candidates":[{"content":{"parts":[{"inline_data":{"mime_type":"image/webp","data":"dGhpcmQ="}}]}}]}`,
	}, "\n") + "\n"
}

func readZipFiles(t *testing.T, data []byte) map[string][]byte {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	require.NoError(t, err)
	out := make(map[string][]byte, len(reader.File))
	for _, file := range reader.File {
		rc, err := file.Open()
		require.NoError(t, err)
		body, err := io.ReadAll(rc)
		require.NoError(t, err)
		require.NoError(t, rc.Close())
		out[file.Name] = body
	}
	return out
}

func mapValues(in map[string][]byte) [][]byte {
	out := make([][]byte, 0, len(in))
	for _, value := range in {
		out = append(out, value)
	}
	return out
}

func (l *fakeBatchImageDownloadLimiter) Acquire(context.Context, string, string) (batchimage.BatchImageDownloadPermit, error) {
	l.acquireCount++
	if l.deny {
		return nil, batchimage.ErrBatchImageDownloadLimited
	}
	return &fakeBatchImageDownloadPermit{release: func() { l.releaseCount++ }}, nil
}

func (p *fakeBatchImageDownloadPermit) Release(context.Context) error {
	if p.once {
		return nil
	}
	p.once = true
	if p.release != nil {
		p.release()
	}
	return nil
}

// newBatchProcessorFixture wires the repository, result indexer, and funding operations.
func newBatchProcessorFixture(repo batchimage.BatchImageRepository, registry *batchimage.Registry[batchimageprovider.BatchImageProvider], providers batchimageprovider.ResultProviders, indexer *batchimage.ResultIndexer, funds batchimage.FundingStore, auth apikey.APIKeyAuthCacheInvalidator, delay time.Duration) *batchimage.ProviderProcessor {
	core := &batchimage.ProviderProcessor{Repo: repo, Funding: nativeTaskFundingFixture(funds), DefaultRequeue: delay, Indexer: indexer}
	if registry != nil && providers != nil {
		core.ResolveProvider = (batchimageprovider.ResultAccess{Registry: registry, Providers: providers}).Process
	}
	if auth != nil {
		core.InvalidateAuth = auth.InvalidateAuthCacheByUserID
	}
	return core
}

func newBatchSettlementFixture(repo batchimage.BatchImageRepository, funds batchimage.FundingStore, logs usage.UsageLogRepository, prices batchimage.ImagePricer, auth apikey.APIKeyAuthCacheInvalidator, cfg *config.Config) *batchimage.Settlement {
	core := &batchimage.Settlement{Repo: repo, Funding: nativeTaskFundingFixture(funds)}
	if cfg != nil {
		core.Retention = time.Duration(cfg.BatchImage.OutputRetentionAfterTerminalHours) * time.Hour
	}
	if prices != nil {
		core.Quote = func(ctx context.Context, model string, group *int64, size string) (float64, error) {
			return prices.BatchImageUnitPrice(ctx, batchimage.BatchImagePriceInput{Model: model, GroupID: group, ImageSize: size})
		}
	}
	if auth != nil {
		core.InvalidateAuth = auth.InvalidateAuthCacheByUserID
	}
	if logs != nil {
		core.RecordUsage = func(ctx context.Context, row *usage.UsageLog) {
			completion.NewRecorder(completion.Dependencies{Logs: completion.SnapshotLogWriter(logs), Observe: func(component, message string) { logging.LegacyPrintf(component, "%s", message) }}, completion.RecorderOptions{}).WriteUsage(ctx, querycache.Clone(row), "service.batch_image_settlement")
		}
	}
	return core
}

// nativeTaskFundingFixture binds a test store to batch funding operations.
func nativeTaskFundingFixture(store batchimage.FundingStore) batchimage.Funding {
	return batchimage.Funding{Store: store}
}

func (r *fakeBatchImageProviderResolver) GetByID(context.Context, int64) (*providercore.Record, error) {
	if r.err != nil {
		return nil, r.err
	}
	return r.provider, nil
}

func (p *fakeProcessorProvider) Name() string { return "fake" }

func (p *fakeProcessorProvider) SupportsProvider(*providercore.Record) bool {
	return true
}

func (p *fakeProcessorProvider) Submit(context.Context, *batchimage.BatchImageJob, *providercore.Record, batchimage.BatchImageInput) (*batchimage.BatchProviderJob, error) {
	panic("Submit must not be called by PR5 processor")
}

func (p *fakeProcessorProvider) Get(context.Context, *batchimage.BatchImageJob, *providercore.Record) (*batchimage.BatchProviderStatus, error) {
	p.getCalled = true
	if p.getErr != nil {
		return nil, p.getErr
	}
	if p.status == nil {
		return &batchimage.BatchProviderStatus{InternalState: batchimage.BatchProviderStateQueued}, nil
	}
	return p.status, nil
}

func (p *fakeProcessorProvider) Cancel(context.Context, *batchimage.BatchImageJob, *providercore.Record) error {
	return nil
}

func (p *fakeProcessorProvider) OpenResult(context.Context, *batchimage.BatchImageJob, *providercore.Record) (io.ReadCloser, string, error) {
	p.openResultCalled = true
	return io.NopCloser(strings.NewReader(p.result)), "application/jsonl", nil
}

func (p *fakeProcessorProvider) Cleanup(context.Context, *batchimage.BatchImageJob, *providercore.Record, batchimage.CleanupTarget) error {
	return nil
}

func resultObserve(event string, values ...any) {
	logging.LegacyPrintf("service.batch_image", "%s %v", event, values)
}

func (r *batchProviderFixture) project(value *providercore.Record) *batchimage.Candidate {
	return (&batchimageprovider.Candidates{Registry: r.registry, ObserveModel: modeltrace.RegisterStage}).Project(providercore.CloneRecord(value))
}

func (r *batchProviderFixture) GetByID(ctx context.Context, id int64) (*batchimage.Candidate, error) {
	v, err := r.source.GetByID(ctx, id)
	return r.project(v), err
}

func (r *batchProviderFixture) values(rows []providercore.Record) []batchimage.Candidate {
	out := make([]batchimage.Candidate, len(rows))
	for i := range rows {
		out[i] = *r.project(&rows[i])
	}
	return out
}

func (r *batchProviderFixture) ListSchedulableByPlatform(ctx context.Context, p string) ([]batchimage.Candidate, error) {
	v, err := r.source.ListSchedulableByPlatform(ctx, p)
	return r.values(v), err
}

func (r *batchProviderFixture) ListSchedulableByGroupIDAndPlatform(ctx context.Context, id int64, p string) ([]batchimage.Candidate, error) {
	v, err := r.source.ListSchedulableByGroupIDAndPlatform(ctx, id, p)
	return r.values(v), err
}

func (r batchGroupReader) GetByIDLite(ctx context.Context, id int64) (*batchimage.GroupView, error) {
	v, err := r.source.GetByIDLite(ctx, id)
	return batchGroupProjection(v), err
}

func batchGroupProjection(v *batchimage.GroupView) *batchimage.GroupView { return v }

func taskFixtureBilling(core *batchimage.Public) batchimage.FundingStore { return core.Funding.Store }

// newBatchPublicFixture wires public operations to test dependencies and runtime options.
func newBatchPublicFixture(repo batchimage.BatchImageRepository, providers batchProvidersFixtureSource, pricingConfigs *routing.PricingConfigService, groups batchGroupFixtureSource, rates batchimage.BatchImageUserGroupRateRepository, queue batchimage.BatchImageQueue, registry *batchimage.Registry[batchimageprovider.BatchImageProvider], prices batchimage.ImagePricer, funds batchimage.FundingStore, auth apikey.APIKeyAuthCacheInvalidator, cfg *config.Config) *batchimage.Public {
	core := &batchimage.Public{Repo: repo, UserGroupRateRepo: rates, Queue: queue, Pricing: prices, Funding: nativeTaskFundingFixture(funds), Observe: resultObserve}
	if providers != nil {
		core.ProviderRepo = &batchProviderFixture{source: providers, registry: registry}
	}
	if groups == nil {
		groups = &publicBatchImageGroupRepo{groups: map[int64]*batchimage.GroupView{7: {ID: 7, AllowBatchImageGeneration: true, RateMultiplier: 1, BatchImageDiscountMultiplier: 0.5, BatchImageHoldMultiplier: 0.6}}}
	}
	core.GroupRepo = batchGroupReader{groups}
	if pricingConfigs != nil {
		core.PricingConfigService = pricingConfigs
	}
	if cfg != nil {
		c := cfg.BatchImage
		core.Options = batchimage.PublicOptions{Enabled: c.Enabled, StaleActiveAfterSeconds: c.StaleActiveAfterSeconds, MaxItemsPerJobDefault: c.MaxItemsPerJobDefault, MaxOutputImagesPerJob: c.MaxOutputImagesPerJob, MaxOutputImagesPerItem: c.MaxOutputImagesPerItem, MaxPromptCharsPerItem: c.MaxPromptCharsPerItem, MaxReferenceImagesPerJob: c.MaxReferenceImagesPerJob, MaxReferenceInlineBytesPerJob: c.MaxReferenceInlineBytesPerJob, DefaultResponseMimeType: c.DefaultResponseMimeType, DefaultImageSize: c.DefaultImageSize}
	}
	core.ProviderExists = func(name string) bool {
		value, ok := registry.Get(name)
		return ok && value != nil
	}
	if auth != nil {
		core.InvalidateAuth = auth.InvalidateAuthCacheByUserID
	}
	core.ClientModel = func(ctx context.Context) string {
		v, _ := ctx.Value(telemetry.ClientModel).(string)
		return v
	}
	core.WithModelTrace = func(ctx context.Context, m routing.GroupMappingResult, requested string) routing.GroupMappingResult {
		return modeltrace.WithGroupRedirect(m, ctx, requested)
	}
	core.RegisterModel = modeltrace.RegisterStage
	core.AutoSubscription = func(ctx context.Context, userID int64, groupID *int64) *billing.UserSubscription {
		reader, _ := taskFixtureBilling(core).(completion.SubscriptionReader)
		return completion.ResolveSubscription(ctx, nil, reader, userID, groupID)
	}
	core.PreferredSubscription = func(ctx context.Context, userID, id int64, groupID *int64) *billing.UserSubscription {
		reader, _ := taskFixtureBilling(core).(billing.PreferredSubscriptionReader)
		return billing.ResolvePreferredSubscription(ctx, reader, userID, id, groupID)
	}
	core.SubscriptionMultiplier = func(ctx context.Context, owner batchimage.BatchImageOwner, group *batchimage.GroupView, fallback float64, sub *billing.UserSubscription) float64 {
		var projected *completion.GroupSnapshot
		if group != nil {
			projected = &completion.GroupSnapshot{ID: group.ID, RateMultiplier: group.RateMultiplier}
		}
		return completion.ResolveUsageRateMultiplier(ctx, owner.EffectiveBillingUserID(), owner.GroupID, projected, fallback, sub, nil)
	}
	return core
}

func validBatchImageSubmitRequest() batchimage.BatchImageSubmitRequest {
	return batchimage.BatchImageSubmitRequest{
		Model:            "gemini-2.5-flash-image",
		Platform:         batchimage.BatchImageProviderGeminiAPI,
		ResponseMimeType: "image/png",
		AspectRatio:      "1:1",
		ImageSize:        "1K",
		Metadata:         map[string]string{"project": "campaign-a", "secret": strings.Repeat("x", 300)},
		Items: []batchimage.BatchImageSubmitItem{
			{CustomID: "cover_001", Prompt: "hero"},
			{CustomID: "cover_002", Prompt: "clean"},
		},
	}
}

func testBatchImageProvider(id int64, providerType string) providercore.Record {
	return providercore.Record{
		ID:            id,
		Platform:      capability.PlatformGemini,
		Type:          providerType,
		Status:        billing.StatusActive,
		Schedulable:   true,
		Priority:      int(id),
		Credentials:   map[string]any{"api_key": "test-secret"},
		Concurrency:   1,
		RateLimitedAt: nil,
	}
}

func (r *publicBatchImageProviderRepo) GetByID(_ context.Context, id int64) (*providercore.Record, error) {
	for i := range r.providers {
		if r.providers[i].ID == id {
			return &r.providers[i], nil
		}
	}
	return nil, errors.New("provider not found")
}

func (r *publicBatchImageProviderRepo) ListSchedulableByPlatform(_ context.Context, platform string) ([]providercore.Record, error) {
	out := make([]providercore.Record, 0, len(r.providers))
	for _, provider := range r.providers {
		if provider.Platform == platform {
			out = append(out, provider)
		}
	}
	return out, nil
}

func (r *publicBatchImageProviderRepo) ListSchedulableByGroupIDAndPlatform(ctx context.Context, _ int64, platform string) ([]providercore.Record, error) {
	return r.ListSchedulableByPlatform(ctx, platform)
}

func (q *publicBatchImageQueue) Enqueue(_ context.Context, batchID string) error {
	if q.err != nil {
		return q.err
	}
	if slices.Contains(q.enqueued, batchID) {
		return batchimage.ErrBatchImageAlreadyQueued
	}
	q.enqueued = append(q.enqueued, batchID)
	return nil
}

func (q *publicBatchImageQueue) Reserve(context.Context, time.Duration) (batchimage.ReservedBatchImageJob, error) {
	return batchimage.ReservedBatchImageJob{}, batchimage.ErrBatchImageQueueEmpty
}

func (q *publicBatchImageQueue) RequeueAfter(context.Context, string, time.Duration) error {
	return nil
}

func (q *publicBatchImageQueue) Ack(context.Context, string) error {
	return nil
}

func (q *publicBatchImageQueue) Heartbeat(context.Context, string) error {
	return nil
}

func (q *publicBatchImageQueue) MoveDueDelayedToReady(context.Context, int) (int, error) {
	return 0, nil
}

func (q *publicBatchImageQueue) RecoverStaleActive(context.Context, time.Duration, int) (int, error) {
	return 0, nil
}

func (q *publicBatchImageQueue) TryAcquireJobLock(context.Context, string, time.Duration) (batchimage.BatchImageJobLock, bool, error) {
	return nil, false, nil
}

func (r *publicBatchImageGroupRepo) GetByIDLite(_ context.Context, id int64) (*batchimage.GroupView, error) {
	if r != nil && r.groups != nil {
		if group, ok := r.groups[id]; ok {
			return group, nil
		}
	}
	return nil, routing.ErrGroupNotFound
}

func (r *publicBatchImageUserGroupRateRepo) GetByUserAndGroup(_ context.Context, _ int64, groupID int64) (*float64, error) {
	if r != nil && r.rates != nil {
		return r.rates[groupID], nil
	}
	return nil, nil
}

func (p *publicBatchImageProvider) Name() string { return p.name }

func (p *publicBatchImageProvider) SupportsProvider(*providercore.Record) bool { return true }

func (p *publicBatchImageProvider) Submit(_ context.Context, _ *batchimage.BatchImageJob, _ *providercore.Record, input batchimage.BatchImageInput) (*batchimage.BatchProviderJob, error) {
	p.submits = append(p.submits, input)
	if p.submitErr != nil {
		return nil, p.submitErr
	}
	return &batchimage.BatchProviderJob{
		ProviderJobName:   "providers/" + p.name + "/job",
		ProviderInputRef:  "files/" + p.name + "/input",
		ProviderOutputRef: "files/" + p.name + "/output",
	}, nil
}

func (p *publicBatchImageProvider) Get(context.Context, *batchimage.BatchImageJob, *providercore.Record) (*batchimage.BatchProviderStatus, error) {
	return &batchimage.BatchProviderStatus{InternalState: batchimage.BatchProviderStateQueued}, nil
}

func (p *publicBatchImageProvider) Cancel(context.Context, *batchimage.BatchImageJob, *providercore.Record) error {
	p.cancelCount++
	return p.cancelErr
}

func (p *publicBatchImageProvider) OpenResult(context.Context, *batchimage.BatchImageJob, *providercore.Record) (io.ReadCloser, string, error) {
	return io.NopCloser(strings.NewReader(p.result)), "application/jsonl", nil
}

func (p *publicBatchImageProvider) Cleanup(_ context.Context, _ *batchimage.BatchImageJob, _ *providercore.Record, target batchimage.CleanupTarget) error {
	p.cleanupTargets = append(p.cleanupTargets, target)
	return p.cleanupErr
}

func newFakeBatchImageRepository() *fakeBatchImageRepository {
	return &fakeBatchImageRepository{
		jobs:        make(map[string]*batchimage.BatchImageJob),
		items:       make(map[string][]batchimage.CreateBatchImageItemParams),
		counts:      make(map[string]batchimage.BatchImageCounts),
		transitions: make(map[string][]string),
		events:      make(map[string][]string),
	}
}

func (r *fakeBatchImageRepository) CreateBatchImageJob(_ context.Context, params batchimage.CreateBatchImageJobParams) (*batchimage.BatchImageJob, error) {
	job := &batchimage.BatchImageJob{
		BatchID:                     params.BatchID,
		UserID:                      params.UserID,
		APIKeyID:                    params.APIKeyID,
		ProviderID:                  params.ProviderID,
		GroupID:                     params.GroupID,
		Status:                      params.Status,
		Platform:                    params.Platform,
		Model:                       params.Model,
		RequestedModel:              params.RequestedModel,
		InternalModel:               params.InternalModel,
		TaskName:                    params.TaskName,
		ProviderJobName:             params.ProviderJobName,
		ItemCount:                   params.ItemCount,
		EstimatedCost:               params.EstimatedCost,
		HoldAmount:                  params.HoldAmount,
		BalanceHoldAmount:           params.BalanceHoldAmount,
		SubscriptionHoldAllocations: cloneResultAllocations(params.SubscriptionHoldAllocations),
		SubscriptionRateMultiplier:  params.SubscriptionRateMultiplier,
		BalanceRateMultiplier:       params.BalanceRateMultiplier,
		PlanGroupRateEnabled:        params.PlanGroupRateEnabled,
		HoldID:                      params.HoldID,
		BaseUnitPrice:               params.BaseUnitPrice,
		GroupRateMultiplier:         params.GroupRateMultiplier,
		ProviderRateMultiplier:      params.ProviderRateMultiplier,
		BatchDiscountMultiplier:     params.BatchDiscountMultiplier,
		HoldMultiplier:              params.HoldMultiplier,
		BillableUnitPrice:           params.BillableUnitPrice,
		HoldUnitPrice:               params.HoldUnitPrice,
		PricingSnapshotVersion:      params.PricingSnapshotVersion,
		Currency:                    params.Currency,
		IdempotencyKey:              params.IdempotencyKey,
		RequestHash:                 params.RequestHash,
		SessionID:                   params.SessionID,
		CreatedAt:                   time.Now(),
	}
	r.jobs[job.BatchID] = job
	return job, nil
}

func (r *fakeBatchImageRepository) GetBatchImageJobByBatchID(_ context.Context, batchID string) (*batchimage.BatchImageJob, error) {
	job, ok := r.jobs[batchID]
	if !ok {
		return nil, batchimage.ErrBatchImageJobNotFound
	}
	return job, nil
}

func (r *fakeBatchImageRepository) GetBatchImageJobByIdempotencyKey(_ context.Context, userID, apiKeyID int64, key string) (*batchimage.BatchImageJob, error) {
	for _, job := range r.jobs {
		if job.UserID == userID && job.APIKeyID != nil && *job.APIKeyID == apiKeyID && batchimage.BatchImageDerefString(job.IdempotencyKey) == key {
			return job, nil
		}
	}
	return nil, batchimage.ErrBatchImageJobNotFound
}

func (r *fakeBatchImageRepository) GetBatchImageJobByBatchIDForOwner(_ context.Context, userID, apiKeyID int64, batchID string) (*batchimage.BatchImageJob, error) {
	job, ok := r.jobs[batchID]
	if !ok || job.UserID != userID || job.APIKeyID == nil || *job.APIKeyID != apiKeyID {
		return nil, batchimage.ErrBatchImageJobNotFound
	}
	return job, nil
}

func (r *fakeBatchImageRepository) ListBatchImageJobsForOwner(_ context.Context, userID, apiKeyID int64, filter batchimage.BatchImageJobFilter) ([]*batchimage.BatchImageJob, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	offset := max(filter.Offset, 0)
	var jobs []*batchimage.BatchImageJob
	for _, job := range r.jobs {
		if job.UserID != userID || job.APIKeyID == nil || *job.APIKeyID != apiKeyID {
			continue
		}
		if filter.Status != "" && job.Status != filter.Status {
			continue
		}
		if filter.TaskNameLike != "" && !strings.Contains(strings.ToLower(job.TaskName), strings.ToLower(filter.TaskNameLike)) {
			continue
		}
		if filter.ExcludeDeleted && job.UserDeletedAt != nil {
			continue
		}
		if filter.Downloaded != nil {
			downloaded := job.DownloadedAt != nil
			if downloaded != *filter.Downloaded {
				continue
			}
		}
		if filter.CreatedAfter != nil && job.CreatedAt.Before(*filter.CreatedAfter) {
			continue
		}
		if filter.CreatedBefore != nil && !job.CreatedAt.Before(*filter.CreatedBefore) {
			continue
		}
		if offset > 0 {
			offset--
			continue
		}
		jobs = append(jobs, job)
		if len(jobs) >= limit {
			break
		}
	}
	return jobs, nil
}

func (r *fakeBatchImageRepository) GetBatchImageJobByID(_ context.Context, id int64) (*batchimage.BatchImageJob, error) {
	for _, job := range r.jobs {
		if job.ID == id {
			return job, nil
		}
	}
	return nil, batchimage.ErrBatchImageJobNotFound
}

func (r *fakeBatchImageRepository) TransitionBatchImageJobStatus(_ context.Context, batchID, toStatus string, opts batchimage.BatchImageTransitionOptions) error {
	job, ok := r.jobs[batchID]
	if !ok {
		return batchimage.ErrBatchImageJobNotFound
	}
	if !batchimage.CanTransitionBatchImageJob(job.Status, toStatus) {
		return batchimage.ErrBatchImageInvalidTransition
	}
	if r.transitionErr != nil {
		return r.transitionErr
	}
	job.Status = toStatus
	job.LastErrorCode = opts.ErrorCode
	job.LastErrorMessage = opts.ErrorMessage
	r.transitions[batchID] = append(r.transitions[batchID], toStatus)
	if opts.EventType != "" {
		r.events[batchID] = append(r.events[batchID], opts.EventType)
	}
	return nil
}

func (r *fakeBatchImageRepository) TouchBatchImageJobSubmitting(_ context.Context, batchID string) error {
	job, ok := r.jobs[batchID]
	if !ok {
		return batchimage.ErrBatchImageJobNotFound
	}
	if job.Status == batchimage.BatchImageJobStatusCreated || job.Status == batchimage.BatchImageJobStatusUploading {
		job.UpdatedAt = time.Now()
	}
	return nil
}

func (r *fakeBatchImageRepository) FailStaleUnsubmittedBatchImageJob(_ context.Context, batchID string, cutoff time.Time, code, message string) (bool, error) {
	job, ok := r.jobs[batchID]
	if !ok {
		return false, batchimage.ErrBatchImageJobNotFound
	}
	if job.Status != batchimage.BatchImageJobStatusCreated && job.Status != batchimage.BatchImageJobStatusUploading {
		return false, nil
	}
	if batchimage.BatchImageDerefString(job.ProviderJobName) != "" || job.UpdatedAt.After(cutoff) {
		return false, nil
	}
	job.Status = batchimage.BatchImageJobStatusFailed
	job.LastErrorCode = batchimage.BatchImageStringPtr(code)
	job.LastErrorMessage = batchimage.BatchImageStringPtr(message)
	job.UpdatedAt = time.Now()
	r.transitions[batchID] = append(r.transitions[batchID], batchimage.BatchImageJobStatusFailed)
	r.events[batchID] = append(r.events[batchID], "billing_hold_recovery_failed_unsubmitted")
	return true, nil
}

func (r *fakeBatchImageRepository) UpdateBatchImageJobProviderOutputRef(_ context.Context, batchID, providerOutputRef string) error {
	job, ok := r.jobs[batchID]
	if !ok {
		return batchimage.ErrBatchImageJobNotFound
	}
	job.ProviderOutputRef = &providerOutputRef
	return nil
}

func (r *fakeBatchImageRepository) UpdateBatchImageJobProviderSubmit(_ context.Context, params batchimage.UpdateBatchImageJobProviderSubmitParams) error {
	job, ok := r.jobs[params.BatchID]
	if !ok {
		return batchimage.ErrBatchImageJobNotFound
	}
	if !batchimage.CanTransitionBatchImageJob(job.Status, batchimage.BatchImageJobStatusSubmitted) {
		return batchimage.ErrBatchImageInvalidTransition
	}
	job.Status = batchimage.BatchImageJobStatusSubmitted
	job.ProviderJobName = batchimage.BatchImageOptionalStringPtr(params.ProviderJobName)
	job.ProviderInputRef = batchimage.BatchImageOptionalStringPtr(params.ProviderInputRef)
	job.ProviderOutputRef = batchimage.BatchImageOptionalStringPtr(params.ProviderOutputRef)
	job.GCSInputURI = batchimage.BatchImageOptionalStringPtr(params.GCSInputURI)
	job.GCSOutputURI = batchimage.BatchImageOptionalStringPtr(params.GCSOutputURI)
	now := time.Now()
	job.SubmittedAt = &now
	r.transitions[params.BatchID] = append(r.transitions[params.BatchID], batchimage.BatchImageJobStatusSubmitted)
	r.events[params.BatchID] = append(r.events[params.BatchID], "provider_submitted")
	return nil
}

func (r *fakeBatchImageRepository) RecordBatchImageJobSubmitFailure(_ context.Context, batchID, code, message string, markFailed bool) error {
	job, ok := r.jobs[batchID]
	if !ok {
		return batchimage.ErrBatchImageJobNotFound
	}
	if markFailed {
		job.Status = batchimage.BatchImageJobStatusFailed
	}
	job.LastErrorCode = batchimage.BatchImageOptionalStringPtr(code)
	job.LastErrorMessage = batchimage.BatchImageOptionalStringPtr(message)
	eventType := "submit_failed"
	if !markFailed {
		eventType = "queue_failed"
	}
	r.events[batchID] = append(r.events[batchID], eventType)
	return nil
}

func (r *fakeBatchImageRepository) MarkBatchImageJobSettled(_ context.Context, params batchimage.MarkBatchImageJobSettledParams) error {
	job, ok := r.jobs[params.BatchID]
	if !ok {
		return batchimage.ErrBatchImageJobNotFound
	}
	if job.Status != batchimage.BatchImageJobStatusSettling {
		if job.Status == batchimage.BatchImageJobStatusCompleted {
			return batchimage.ErrBatchImageAlreadySettled
		}
		return batchimage.ErrBatchImageSettlementInvalidStatus
	}
	if batchimage.BatchImageDerefString(job.ManifestHash) != "" && batchimage.BatchImageDerefString(job.ManifestHash) != params.ManifestHash {
		return batchimage.ErrBatchImageSettlementManifestConflict
	}
	now := time.Now()
	job.Status = batchimage.BatchImageJobStatusCompleted
	job.ActualCost = &params.ActualCost
	job.ManifestHash = &params.ManifestHash
	job.SettledAt = &now
	if job.OutputExpiresAt == nil && params.OutputExpiresAt != nil {
		job.OutputExpiresAt = params.OutputExpiresAt
	}
	r.transitions[params.BatchID] = append(r.transitions[params.BatchID], batchimage.BatchImageJobStatusCompleted)
	r.events[params.BatchID] = append(r.events[params.BatchID], "settlement_completed")
	return nil
}

func (r *fakeBatchImageRepository) SetBatchImageJobSettlementFailed(_ context.Context, batchID, code, message string) (int, error) {
	job, ok := r.jobs[batchID]
	if !ok {
		return 0, batchimage.ErrBatchImageJobNotFound
	}
	job.LastErrorCode = batchimage.BatchImageStringPtr(code)
	job.LastErrorMessage = batchimage.BatchImageOptionalStringPtr(message)
	job.RetryCount++
	r.events[batchID] = append(r.events[batchID], "settlement_failed")
	return job.RetryCount, nil
}

func (r *fakeBatchImageRepository) CreateBatchImageItem(_ context.Context, params batchimage.CreateBatchImageItemParams) (*batchimage.BatchImageItem, error) {
	r.items[params.JobID] = append(r.items[params.JobID], params)
	return &batchimage.BatchImageItem{JobID: params.JobID, CustomID: params.CustomID, Status: params.Status}, nil
}

func (r *fakeBatchImageRepository) BulkCreateBatchImageItems(ctx context.Context, params []batchimage.CreateBatchImageItemParams) error {
	for _, param := range params {
		if _, err := r.CreateBatchImageItem(ctx, param); err != nil {
			return err
		}
	}
	return nil
}

func (r *fakeBatchImageRepository) ReplaceBatchImageItemsForJob(_ context.Context, batchID string, items []batchimage.CreateBatchImageItemParams, counts batchimage.BatchImageCounts) error {
	// Registered jobs can rebuild items only while indexing.
	// Unregistered jobs constructed directly by tests can also write items.
	if job, ok := r.jobs[batchID]; ok && job.Status != batchimage.BatchImageJobStatusIndexing {
		return batchimage.ErrBatchImageIndexStateConflict
	}
	r.replaceCalls++
	copied := append([]batchimage.CreateBatchImageItemParams(nil), items...)
	for idx := range copied {
		copied[idx].JobID = batchID
	}
	r.items[batchID] = copied
	r.counts[batchID] = counts
	if job, ok := r.jobs[batchID]; ok {
		job.SuccessCount = counts.SuccessCount
		job.FailCount = counts.FailCount
		job.ItemCount = len(copied)
	}
	return nil
}

func (r *fakeBatchImageRepository) ListBatchImageItems(_ context.Context, batchID string, filter batchimage.BatchImageItemFilter) ([]*batchimage.BatchImageItem, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	offset := max(filter.Offset, 0)
	var result []*batchimage.BatchImageItem
	for _, item := range r.items[batchID] {
		if filter.Status != "" && item.Status != filter.Status {
			continue
		}
		if offset > 0 {
			offset--
			continue
		}
		result = append(result, &batchimage.BatchImageItem{
			JobID:                item.JobID,
			CustomID:             item.CustomID,
			Status:               item.Status,
			RequestHash:          item.RequestHash,
			PromptPreview:        item.PromptPreview,
			ProviderSourceObject: item.ProviderSourceObject,
			SourceLineNumber:     item.SourceLineNumber,
			SourceByteOffset:     item.SourceByteOffset,
			SourceByteLength:     item.SourceByteLength,
			MimeType:             item.MimeType,
			FileExtension:        item.FileExtension,
			ImageCount:           item.ImageCount,
			ErrorCode:            item.ErrorCode,
			ErrorMessage:         item.ErrorMessage,
			BilledAmount:         item.BilledAmount,
			IndexedAt:            item.IndexedAt,
		})
		if len(result) >= limit {
			break
		}
	}
	return result, nil
}

func (r *fakeBatchImageRepository) ListBatchImageItemsForOwner(ctx context.Context, userID, apiKeyID int64, batchID string, filter batchimage.BatchImageItemFilter) ([]*batchimage.BatchImageItem, error) {
	if _, err := r.GetBatchImageJobByBatchIDForOwner(ctx, userID, apiKeyID, batchID); err != nil {
		return nil, err
	}
	return r.ListBatchImageItems(ctx, batchID, filter)
}

func (r *fakeBatchImageRepository) GetBatchImageJobForDownload(ctx context.Context, userID, apiKeyID int64, batchID string) (*batchimage.BatchImageJob, error) {
	return r.GetBatchImageJobByBatchIDForOwner(ctx, userID, apiKeyID, batchID)
}

func (r *fakeBatchImageRepository) GetBatchImageItemForDownload(_ context.Context, batchID, customID string) (*batchimage.BatchImageItem, error) {
	for _, item := range r.items[batchID] {
		if item.CustomID != customID {
			continue
		}
		return &batchimage.BatchImageItem{
			JobID:                item.JobID,
			CustomID:             item.CustomID,
			Status:               item.Status,
			RequestHash:          item.RequestHash,
			PromptPreview:        item.PromptPreview,
			ProviderSourceObject: item.ProviderSourceObject,
			SourceLineNumber:     item.SourceLineNumber,
			SourceByteOffset:     item.SourceByteOffset,
			SourceByteLength:     item.SourceByteLength,
			MimeType:             item.MimeType,
			FileExtension:        item.FileExtension,
			ImageCount:           item.ImageCount,
			ErrorCode:            item.ErrorCode,
			ErrorMessage:         item.ErrorMessage,
			BilledAmount:         item.BilledAmount,
			IndexedAt:            item.IndexedAt,
		}, nil
	}
	return nil, batchimage.ErrBatchImageItemNotFound
}

func (r *fakeBatchImageRepository) ListBatchImageItemsForDownload(ctx context.Context, batchID string, status string, limit int) ([]*batchimage.BatchImageItem, error) {
	return r.ListBatchImageItems(ctx, batchID, batchimage.BatchImageItemFilter{Status: status, Limit: limit})
}

func (r *fakeBatchImageRepository) ListBatchImageJobsDueForInputCleanup(_ context.Context, cutoff time.Time, limit int) ([]*batchimage.BatchImageJob, error) {
	if limit <= 0 {
		limit = 100
	}
	var jobs []*batchimage.BatchImageJob
	for _, job := range r.jobs {
		if job.InputDeletedAt != nil || batchimage.BatchImageDerefString(job.ProviderInputRef) == "" || !batchimage.IsTerminalBatchImageJobStatus(job.Status) {
			continue
		}
		at := job.FinishedAt
		if at == nil {
			at = job.SettledAt
		}
		if at == nil {
			at = &job.UpdatedAt
		}
		if at != nil && at.After(cutoff) {
			continue
		}
		jobs = append(jobs, job)
		if len(jobs) >= limit {
			break
		}
	}
	return jobs, nil
}

func (r *fakeBatchImageRepository) ListBatchImageJobsDueForOutputCleanup(_ context.Context, now time.Time, limit int) ([]*batchimage.BatchImageJob, error) {
	if limit <= 0 {
		limit = 100
	}
	var jobs []*batchimage.BatchImageJob
	for _, job := range r.jobs {
		if job.OutputDeletedAt != nil || batchimage.BatchImageDerefString(job.ProviderOutputRef) == "" || job.Status != batchimage.BatchImageJobStatusCompleted || job.OutputExpiresAt == nil || job.OutputExpiresAt.After(now) {
			continue
		}
		jobs = append(jobs, job)
		if len(jobs) >= limit {
			break
		}
	}
	return jobs, nil
}

func (r *fakeBatchImageRepository) ListStaleUnsubmittedBatchImageJobs(_ context.Context, cutoff time.Time, limit int) ([]*batchimage.BatchImageJob, error) {
	if limit <= 0 {
		limit = 100
	}
	jobs := make([]*batchimage.BatchImageJob, 0, limit)
	for _, job := range r.jobs {
		if len(jobs) >= limit {
			break
		}
		if job.Status != batchimage.BatchImageJobStatusCreated && job.Status != batchimage.BatchImageJobStatusUploading {
			continue
		}
		if batchimage.BatchImageDerefString(job.ProviderJobName) != "" {
			continue
		}
		holdAmount := job.EstimatedCost
		if job.HoldAmount != nil {
			holdAmount = *job.HoldAmount
		}
		if holdAmount <= 0 || job.UpdatedAt.After(cutoff) {
			continue
		}
		jobs = append(jobs, job)
	}
	return jobs, nil
}

func (r *fakeBatchImageRepository) MarkBatchImageInputDeleted(_ context.Context, batchID string, deletedAt time.Time) error {
	job, ok := r.jobs[batchID]
	if !ok {
		return batchimage.ErrBatchImageJobNotFound
	}
	if job.InputDeletedAt == nil {
		job.InputDeletedAt = &deletedAt
	}
	r.events[batchID] = append(r.events[batchID], "input_cleanup_completed")
	return nil
}

func (r *fakeBatchImageRepository) MarkBatchImageOutputDeleted(_ context.Context, batchID string, deletedAt time.Time) error {
	job, ok := r.jobs[batchID]
	if !ok {
		return batchimage.ErrBatchImageJobNotFound
	}
	if job.OutputDeletedAt == nil {
		job.OutputDeletedAt = &deletedAt
	}
	if job.Status == batchimage.BatchImageJobStatusCompleted {
		job.Status = batchimage.BatchImageJobStatusOutputDeleted
	}
	r.events[batchID] = append(r.events[batchID], "output_cleanup_completed")
	return nil
}

func (r *fakeBatchImageRepository) MarkBatchImageDownloaded(_ context.Context, batchID string, downloadedAt time.Time) error {
	job, ok := r.jobs[batchID]
	if !ok {
		return batchimage.ErrBatchImageJobNotFound
	}
	if job.DownloadedAt == nil {
		job.DownloadedAt = &downloadedAt
	}
	r.events[batchID] = append(r.events[batchID], "download_completed")
	return nil
}

func (r *fakeBatchImageRepository) MarkBatchImageJobUserDeleted(_ context.Context, userID, apiKeyID int64, batchID string, deletedAt time.Time) error {
	job, ok := r.jobs[batchID]
	if !ok || job.UserID != userID || job.APIKeyID == nil || *job.APIKeyID != apiKeyID {
		return batchimage.ErrBatchImageJobNotFound
	}
	if !batchimage.IsBatchImageProcessorDoneStatus(job.Status) {
		return batchimage.ErrBatchImageRecordDeleteNotReady
	}
	if job.UserDeletedAt == nil {
		job.UserDeletedAt = &deletedAt
	}
	r.events[batchID] = append(r.events[batchID], "user_record_deleted")
	return nil
}

func (r *fakeBatchImageRepository) SetBatchImageOutputExpiresAt(_ context.Context, batchID string, expiresAt time.Time) error {
	job, ok := r.jobs[batchID]
	if !ok {
		return batchimage.ErrBatchImageJobNotFound
	}
	if job.OutputExpiresAt == nil {
		job.OutputExpiresAt = &expiresAt
	}
	return nil
}

func (r *fakeBatchImageRepository) RecordBatchImageCleanupFailure(_ context.Context, batchID, code, message string) error {
	job, ok := r.jobs[batchID]
	if !ok {
		return batchimage.ErrBatchImageJobNotFound
	}
	job.LastErrorCode = batchimage.BatchImageStringPtr(code)
	job.LastErrorMessage = batchimage.BatchImageOptionalStringPtr(message)
	r.events[batchID] = append(r.events[batchID], "output_cleanup_failed")
	return nil
}

func (r *fakeBatchImageRepository) AppendBatchImageEvent(_ context.Context, batchID, eventType string, _ any) error {
	r.events[batchID] = append(r.events[batchID], eventType)
	return nil
}

func testBatchImageOwner() batchimage.BatchImageOwner {
	groupID := int64(7)
	return batchimage.BatchImageOwner{UserID: 11, APIKeyID: 22, GroupID: &groupID}
}

func requireBatchImagePublicJSONHasNoInternals(t *testing.T, body string) {
	t.Helper()
	for _, forbidden := range []string{
		"provider_job_name",
		"provider_input_ref",
		"provider_output_ref",
		"gcs_input_uri",
		"gcs_output_uri",
		"provider_id",
		"service_account",
		"api_key",
		"download_url",
		"providers/",
		"files/",
		"gs://",
	} {
		require.NotContains(t, body, forbidden)
	}
}

func (r *resultProviderFixture) GetByID(context.Context, int64) (*providercore.Record, error) {
	return r.provider, nil
}

func newBatchDownloadFixture(repo batchimage.BatchImageRepository, registry *batchimage.Registry[batchimageprovider.BatchImageProvider], providers batchimageprovider.ResultProviders, limiter batchimage.BatchImageDownloadLimiter, cfg *config.Config) *batchimage.Download {
	return &batchimage.Download{Repo: repo, Limiter: limiter, ResolveProvider: (batchimageprovider.ResultAccess{Registry: registry, Providers: providers}).Download, Options: batchimage.DownloadOptions{MaxItems: cfg.BatchImage.MaxDownloadItemsZip, MaxBytes: cfg.BatchImage.MaxDownloadBytesPerRequest, Duration: time.Duration(cfg.BatchImage.MaxDownloadDurationSeconds) * time.Second}}
}

func newBatchCleanupFixture(repo batchimage.BatchImageRepository, registry *batchimage.Registry[batchimageprovider.BatchImageProvider], providers batchimageprovider.ResultProviders, cfg *config.Config) *batchimage.Cleanup {
	return &batchimage.Cleanup{Repo: repo, ResolveProvider: (batchimageprovider.ResultAccess{Registry: registry, Providers: providers}).Cleanup, Options: batchimage.CleanupOptions{InputRetention: time.Duration(cfg.BatchImage.InputRetentionAfterTerminalHours) * time.Hour, Interval: time.Duration(cfg.BatchImage.CleanupIntervalMinutes) * time.Minute, BatchSize: cfg.BatchImage.CleanupBatchSize}}
}

// cloneResultAllocations copies stored allocations through the funding module helper.
func cloneResultAllocations(values []billing.BillingAllocation) []billing.BillingAllocation {
	if len(values) == 0 {
		return nil
	}
	out := make([]billing.BillingAllocation, len(values))
	for i, value := range values {
		out[i] = billing.CloneBillingAllocation(value, value.AmountUSD)
	}
	return out
}

func (r *fakeBatchImagePricingResolver) BatchImageUnitPrice(_ context.Context, input batchimage.BatchImagePriceInput) (float64, error) {
	if input.Model != "" {
		r.models = append(r.models, input.Model)
	}
	if r.err != nil {
		return 0, r.err
	}
	if input.Model != "" && r.missingModels[input.Model] {
		return 0, batchimage.ErrBatchImageSettlementPricingMissing
	}
	return r.unitPrice, nil
}

func (r *fakeBatchImageBillingRepo) Reserve(_ context.Context, cmd *billing.TaskFundsCommand) (*billing.TaskFundsResult, error) {
	if r.reserveErr != nil {
		r.reserves = append(r.reserves, cmd)
		return nil, r.reserveErr
	}
	result, err := r.applyHold(cmd, &r.reserves)
	if err == nil && result != nil {
		result.BalanceAmountUSD = cmd.HoldAmount
		result.HoldAmountUSD = cmd.HoldAmount
		result.EstimatedAmountUSD = cmd.HoldAmount
		if cmd.PricingSnapshotVersion >= 2 {
			result.EstimatedAmountUSD = cmd.HoldAmount * cmd.SettlementRateScale
		}
		if cmd.HoldAmount > 0 {
			result.BillingAllocations = []billing.BillingAllocation{{Type: billing.BillingAllocationTypeBalance, AmountUSD: cmd.HoldAmount}}
		}
	}
	return result, err
}

func (r *fakeBatchImageBillingRepo) Capture(_ context.Context, cmd *billing.TaskFundsCommand) (*billing.TaskFundsResult, error) {
	if r.captureErr != nil {
		r.captures = append(r.captures, cmd)
		return nil, r.captureErr
	}
	result, err := r.applyHold(cmd, &r.captures)
	if err != nil || result == nil {
		return result, err
	}
	plan, err := billing.PlanTaskCapture(cmd)
	if err != nil {
		return nil, err
	}
	cmd.ActualAmount = plan.ActualAmountUSD
	result.SubscriptionAmountUSD = plan.SubscriptionAmountUSD
	result.BalanceAmountUSD = plan.BalanceAmountUSD
	result.ActualAmountUSD = plan.ActualAmountUSD
	result.BillingAllocations = plan.BillingAllocations
	return result, nil
}

func (r *fakeBatchImageBillingRepo) Release(_ context.Context, cmd *billing.TaskFundsCommand) (*billing.TaskFundsResult, error) {
	if r.releaseErr != nil {
		r.releases = append(r.releases, cmd)
		return nil, r.releaseErr
	}
	return r.applyHold(cmd, &r.releases)
}

func (r *fakeBatchImageBillingRepo) applyHold(cmd *billing.TaskFundsCommand, calls *[]*billing.TaskFundsCommand) (*billing.TaskFundsResult, error) {
	if r.seen == nil {
		r.seen = make(map[string]struct{})
	}
	if r.err != nil {
		*calls = append(*calls, cmd)
		return nil, r.err
	}
	if cmd != nil {
		cmd.Normalize()
		if _, ok := r.seen[cmd.RequestID]; ok || r.alreadyApplied[cmd.RequestID] {
			*calls = append(*calls, cmd)
			return &billing.TaskFundsResult{Applied: false}, nil
		}
		r.seen[cmd.RequestID] = struct{}{}
	}
	*calls = append(*calls, cmd)
	return &billing.TaskFundsResult{Applied: true}, nil
}

func (r *fakeBatchImageBillingRepo) ResolveUsableSubscriptionForGroup(context.Context, int64, int64) (*billing.UserSubscription, error) {
	return r.usableSubscription, r.subscriptionErr
}

func newFakeBatchImageQueue(batchID string) *fakeBatchImageQueue {
	return &fakeBatchImageQueue{
		reserved:     batchimage.ReservedBatchImageJob{BatchID: batchID},
		lockAcquired: true,
	}
}

func (q *fakeBatchImageQueue) Enqueue(context.Context, string) error {
	return nil
}

func (q *fakeBatchImageQueue) Reserve(context.Context, time.Duration) (batchimage.ReservedBatchImageJob, error) {
	return q.reserved, nil
}

func (q *fakeBatchImageQueue) RequeueAfter(_ context.Context, batchID string, delay time.Duration) error {
	q.requeued = append(q.requeued, fakeBatchImageRequeue{batchID: batchID, delay: delay})
	return nil
}

func (q *fakeBatchImageQueue) Ack(_ context.Context, batchID string) error {
	q.acked = append(q.acked, batchID)
	return nil
}

func (q *fakeBatchImageQueue) Heartbeat(context.Context, string) error {
	return nil
}

func (q *fakeBatchImageQueue) MoveDueDelayedToReady(context.Context, int) (int, error) {
	return 0, nil
}

func (q *fakeBatchImageQueue) RecoverStaleActive(context.Context, time.Duration, int) (int, error) {
	return 0, nil
}

func (q *fakeBatchImageQueue) TryAcquireJobLock(context.Context, string, time.Duration) (batchimage.BatchImageJobLock, bool, error) {
	if !q.lockAcquired {
		return nil, false, nil
	}
	return fakeBatchImageLock{release: func() { q.releaseCount++ }, queue: q}, true, nil
}

func (l fakeBatchImageLock) Release(context.Context) error {
	if l.release != nil {
		l.release()
	}
	return nil
}

func (p *fakeBatchImageProcessor) Process(_ context.Context, batchID string) (batchimage.BatchImageProcessResult, error) {
	p.processed = append(p.processed, batchID)
	return p.result, p.err
}

// Heartbeat updates queue activity through the held job lock.
func (l fakeBatchImageLock) Heartbeat(ctx context.Context) error {
	return l.queue.Heartbeat(ctx, l.queue.reserved.BatchID)
}

func (l fakeBatchImageLock) Ack(ctx context.Context) error {
	return l.queue.Ack(ctx, l.queue.reserved.BatchID)
}

func (l fakeBatchImageLock) RequeueAfter(ctx context.Context, d time.Duration) error {
	return l.queue.RequeueAfter(ctx, l.queue.reserved.BatchID, d)
}
