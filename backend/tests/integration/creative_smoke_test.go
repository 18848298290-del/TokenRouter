package integration

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	billingcore "github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
	billingtestkit "github.com/TokenFlux/TokenRouter/internal/billing/testkit"
	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/creative"
	creativeprovider "github.com/TokenFlux/TokenRouter/internal/creative/provider"
	creativeredis "github.com/TokenFlux/TokenRouter/internal/creative/rediscache"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// This smoke test uses miniredis and repository and executor stubs to cover creation,
// enqueueing, reservation, execution, settlement, retrieval, acknowledgement, and expiry after
// acknowledgement.
// Public, Results, and the worker share the production Redis queue and transient store implementations.
// PostgreSQL transaction behavior belongs to the Docker-backed repository integration tests.

// smokeFakeRunRepo stores the run and output state needed by this smoke test in memory.
type smokeFakeRunRepo struct {
	runs    map[string]*creative.CreativeRun
	outputs map[string][]*creative.CreativeRunOutput
}

// smokeFakeBillingRepo counts capture and release calls.
type smokeFakeBillingRepo struct {
	captureN int
	releaseN int
}

// smokeFakeExecutor returns a fixed image output.
type smokeFakeExecutor struct{}

// smokeFakeManagedKeyRepo stores a managed key with a fixed ID.
type smokeFakeManagedKeyRepo struct{ key *apikey.APIKey }

type smokeFakeUserRepo struct{}

type smokeFakeGroupRepo struct{}

type smokeFakeProviderRepo struct{}

type smokeFakeRateRepo struct{}

type smokeCreativeSettingReader struct{}

func newSmokeFakeRunRepo() *smokeFakeRunRepo {
	return &smokeFakeRunRepo{
		runs:    make(map[string]*creative.CreativeRun),
		outputs: make(map[string][]*creative.CreativeRunOutput),
	}
}

func (r *smokeFakeRunRepo) CreateCreativeRun(ctx context.Context, params creative.CreateCreativeRunParams) (*creative.CreativeRun, error) {
	workspaceID := params.WorkspaceID
	run := &creative.CreativeRun{
		RunID:                params.RunID,
		UserID:               params.UserID,
		WorkspaceID:          &workspaceID,
		GroupID:              params.GroupID,
		APIKeyID:             params.APIKeyID,
		Model:                params.Model,
		Platform:             params.Platform,
		Operation:            params.Operation,
		RequestedOutputCount: params.RequestedOutputCount,
		ImageSize:            params.ImageSize,
		Status:               creative.CreativeRunStatusQueued,
		EstimatedCost:        params.EstimatedCost,
		HoldAmount:           &params.HoldAmount,
		BaseUnitPrice:        params.BaseUnitPrice,
	}
	r.runs[run.RunID] = run
	outputs := make([]*creative.CreativeRunOutput, 0, params.RequestedOutputCount)
	for index := range params.RequestedOutputCount {
		outputs = append(outputs, &creative.CreativeRunOutput{RunID: run.RunID, OutputIndex: index, Status: creative.CreativeRunOutputStatusPending})
	}
	r.outputs[run.RunID] = outputs
	return run, nil
}

func (r *smokeFakeRunRepo) GetCreativeRunByRunID(ctx context.Context, runID string) (*creative.CreativeRun, error) {
	run, ok := r.runs[runID]
	if !ok {
		return nil, creative.ErrCreativeRunNotFound
	}
	return run, nil
}

func (r *smokeFakeRunRepo) GetCreativeRunByRunIDForOwner(ctx context.Context, scope creative.CreativeRunScope, runID string) (*creative.CreativeRun, error) {
	run, err := r.GetCreativeRunByRunID(ctx, runID)
	if err != nil {
		return nil, err
	}
	if run.UserID != scope.UserID || run.WorkspaceID == nil || *run.WorkspaceID != scope.WorkspaceID {
		return nil, creative.ErrCreativeRunNotFound
	}
	return run, nil
}

func (r *smokeFakeRunRepo) GetCreativeRunByIdempotencyKey(ctx context.Context, scope creative.CreativeRunScope, key string) (*creative.CreativeRun, error) {
	return nil, creative.ErrCreativeRunNotFound
}

func (r *smokeFakeRunRepo) ListCreativeRunsForOwner(ctx context.Context, scope creative.CreativeRunScope, filter creative.CreativeRunFilter) ([]*creative.CreativeRun, error) {
	return nil, nil
}

func (r *smokeFakeRunRepo) TransitionCreativeRunStatus(ctx context.Context, runID, toStatus string, opts creative.CreativeRunTransitionOptions) error {
	run, ok := r.runs[runID]
	if !ok {
		return creative.ErrCreativeRunNotFound
	}
	if !creative.CanTransitionCreativeRun(run.Status, toStatus) {
		return creative.ErrCreativeInvalidTransition
	}
	run.Status = toStatus
	if opts.ReleaseTargetStatus != "" {
		run.ReleaseTargetStatus = opts.ReleaseTargetStatus
	}
	return nil
}

func (r *smokeFakeRunRepo) MarkCreativeRunRunning(ctx context.Context, runID string, providerID int64, now time.Time) error {
	run, ok := r.runs[runID]
	if !ok {
		return creative.ErrCreativeRunNotFound
	}
	if run.Status == creative.CreativeRunStatusRunning {
		return nil
	}
	if !creative.CanTransitionCreativeRun(run.Status, creative.CreativeRunStatusRunning) {
		return creative.ErrCreativeInvalidTransition
	}
	run.Status = creative.CreativeRunStatusRunning
	if providerID > 0 {
		run.ProviderID = &providerID
	}
	run.StartedAt = &now
	return nil
}

func (r *smokeFakeRunRepo) SetCreativeRunExecution(ctx context.Context, runID string, providerID int64, provider string, now time.Time) error {
	run, ok := r.runs[runID]
	if !ok {
		return creative.ErrCreativeRunNotFound
	}
	if providerID > 0 {
		run.ProviderID = &providerID
		run.Platform = provider
	}
	return nil
}

func (r *smokeFakeRunRepo) MarkCreativeRunSucceeded(ctx context.Context, runID string, actualCost float64, now time.Time) error {
	run, ok := r.runs[runID]
	if !ok {
		return creative.ErrCreativeRunNotFound
	}
	if run.Status != creative.CreativeRunStatusRunning && run.Status != creative.CreativeRunStatusProviderSucceeded && run.Status != creative.CreativeRunStatusSettlementPending {
		return creative.ErrCreativeInvalidTransition
	}
	run.Status = creative.CreativeRunStatusSucceeded
	run.ActualCost = &actualCost
	run.CompletedAt = &now
	return nil
}

func (r *smokeFakeRunRepo) UpdateCreativeRunOutput(ctx context.Context, runID string, outputIndex int, status, mimeType string, byteSize int64, transientExpiresAt *time.Time, errorCode, errorMessage string) error {
	for _, output := range r.outputs[runID] {
		if output.OutputIndex == outputIndex {
			if output.Status == creative.CreativeRunOutputStatusAcked {
				return nil
			}
			output.Status = status
			output.MimeType = &mimeType
			output.ByteSize = &byteSize
			output.TransientExpiresAt = transientExpiresAt
			output.ErrorCode = &errorCode
			output.ErrorMessage = &errorMessage
			return nil
		}
	}
	return creative.ErrCreativeOutputNotFound
}

func (r *smokeFakeRunRepo) GetCreativeRunOutput(ctx context.Context, runID string, outputIndex int) (*creative.CreativeRunOutput, error) {
	for _, output := range r.outputs[runID] {
		if output.OutputIndex == outputIndex {
			return output, nil
		}
	}
	return nil, creative.ErrCreativeOutputNotFound
}

func (r *smokeFakeRunRepo) ListCreativeRunOutputs(ctx context.Context, runID string) ([]*creative.CreativeRunOutput, error) {
	return r.outputs[runID], nil
}

func (r *smokeFakeRunRepo) MarkCreativeRunOutputAcked(ctx context.Context, runID string, outputIndex int, now time.Time) error {
	output, err := r.GetCreativeRunOutput(ctx, runID, outputIndex)
	if err != nil {
		return err
	}
	if output.Status == creative.CreativeRunOutputStatusAcked {
		return nil
	}
	if output.Status != creative.CreativeRunOutputStatusSucceeded {
		return creative.ErrCreativeOutputNotReady
	}
	output.Status = creative.CreativeRunOutputStatusAcked
	output.AckedAt = &now
	return nil
}

func (r *smokeFakeRunRepo) ListCreativeRunsDueForTransientCleanup(ctx context.Context, cutoff time.Time, limit int) ([]*creative.CreativeRun, error) {
	return nil, nil
}

func (r *smokeFakeRunRepo) IncrementCreativeRunAttempt(ctx context.Context, runID string) (int, error) {
	run, ok := r.runs[runID]
	if !ok {
		return 0, creative.ErrCreativeRunNotFound
	}
	run.AttemptCount++
	return run.AttemptCount, nil
}

func (r *smokeFakeRunRepo) IncrementCreativeRunSettlementAttempt(ctx context.Context, runID string) (int, error) {
	run, ok := r.runs[runID]
	if !ok {
		return 0, creative.ErrCreativeRunNotFound
	}
	run.SettlementAttemptCount++
	return run.SettlementAttemptCount, nil
}

func (r *smokeFakeRunRepo) IncrementCreativeRunReleaseAttempt(ctx context.Context, runID string) (int, error) {
	run, ok := r.runs[runID]
	if !ok {
		return 0, creative.ErrCreativeRunNotFound
	}
	run.ReleaseAttemptCount++
	return run.ReleaseAttemptCount, nil
}

func (r *smokeFakeRunRepo) SetCreativeRunProvisioningPhase(ctx context.Context, runID, phase string) error {
	run, ok := r.runs[runID]
	if !ok {
		return creative.ErrCreativeRunNotFound
	}
	run.ProvisioningPhase = phase
	return nil
}

func (r *smokeFakeRunRepo) MarkCreativeRunProviderSucceeded(ctx context.Context, runID string, providerID int64, now time.Time) error {
	run, ok := r.runs[runID]
	if !ok {
		return creative.ErrCreativeRunNotFound
	}
	if providerID > 0 {
		run.ProviderID = &providerID
	}
	run.ProviderResultRecordedAt = &now
	if run.Status == creative.CreativeRunStatusRunning {
		run.Status = creative.CreativeRunStatusProviderSucceeded
	}
	return nil
}

func (r *smokeFakeRunRepo) SetCreativeRunReconcileError(ctx context.Context, runID, message string, next time.Time) error {
	run, ok := r.runs[runID]
	if !ok {
		return creative.ErrCreativeRunNotFound
	}
	if message == "" {
		run.LastReconcileError = nil
	} else {
		run.LastReconcileError = &message
	}
	if next.IsZero() {
		run.NextReconcileAt = nil
	} else {
		run.NextReconcileAt = &next
	}
	return nil
}

func (r *smokeFakeBillingRepo) Reserve(ctx context.Context, cmd *billingcore.TaskFundsCommand) (*billingcore.TaskFundsResult, error) {
	return &billingcore.TaskFundsResult{Applied: true, HoldAmountUSD: cmd.HoldAmount, EstimatedAmountUSD: cmd.HoldAmount, BalanceAmountUSD: cmd.HoldAmount}, nil
}

func (r *smokeFakeBillingRepo) Capture(ctx context.Context, cmd *billingcore.TaskFundsCommand) (*billingcore.TaskFundsResult, error) {
	r.captureN++
	return &billingcore.TaskFundsResult{Applied: true, ActualAmountUSD: cmd.ActualBaseAmountUSD}, nil
}

func (r *smokeFakeBillingRepo) Release(ctx context.Context, cmd *billingcore.TaskFundsCommand) (*billingcore.TaskFundsResult, error) {
	r.releaseN++
	return &billingcore.TaskFundsResult{Applied: true}, nil
}

func (e *smokeFakeExecutor) Prepare(ctx context.Context, run creative.CreativeRun) (*creative.CreativeExecution, error) {
	return &creative.CreativeExecution{
		ProviderID:    55,
		Platform:      creative.PlatformGemini,
		Target:        e,
		UpstreamModel: run.Model,
		ReleaseFunc:   func() {},
	}, nil
}

func (e *smokeFakeExecutor) Execute(ctx context.Context, run creative.CreativeRun, payload creative.CreativeRunPayload) (*creative.CreativeExecuteResult, error) {
	return &creative.CreativeExecuteResult{
		Outputs:    []creative.CreativeOutput{{Index: 0, Bytes: []byte("smoke-image-bytes"), Mime: "image/png"}},
		ProviderID: 55,
	}, nil
}

func (e *smokeFakeExecutor) IsRetryable(err error) bool { return false }

func (r *smokeFakeManagedKeyRepo) GetManagedKeyByUserAndGroup(ctx context.Context, userID, groupID int64, managedBy string) (*apikey.APIKey, error) {
	if r.key != nil {
		return r.key, nil
	}
	return nil, apikey.ErrAPIKeyNotFound
}

func (r *smokeFakeManagedKeyRepo) CreateManagedKey(ctx context.Context, key *apikey.APIKey) error {
	key.ID = 900
	r.key = key
	return nil
}

func (r *smokeFakeUserRepo) GetByID(ctx context.Context, id int64) (creative.UserAccess, error) {
	return &identity.User{ID: id}, nil
}

func (r *smokeFakeGroupRepo) GetByIDLite(ctx context.Context, id int64) (*creative.GroupView, error) {
	return &creative.GroupView{ID: id, Name: "Smoke Group", Active: true, AllowImageGeneration: true, RateMultiplier: 1, Operations: map[string][]string{creative.PlatformGemini: {creative.CreativeOperationGenerate, creative.CreativeOperationEdit}}}, nil
}

func (r *smokeFakeGroupRepo) ListActive(context.Context) ([]creative.GroupView, error) {
	return nil, nil
}

func (r *smokeFakeProviderRepo) ListSchedulableByGroupIDAndPlatform(context.Context, int64, string) ([]creative.CatalogProvider, error) {
	return []creative.CatalogProvider{creativeprovider.CatalogProvider(&provider.Record{ID: 55, Platform: "gemini", Type: "apikey", Status: billingcore.StatusActive, Schedulable: true, Credentials: map[string]any{"model_mapping": map[string]any{"gemini-3.1-flash-image": "gemini-3.1-flash-image"}}})}, nil
}

func (r *smokeFakeRateRepo) GetByUserAndGroup(ctx context.Context, userID, groupID int64) (*float64, error) {
	return nil, nil
}

func (smokeCreativeSettingReader) IsCreativeEnabled(context.Context) bool { return true }

func (smokeCreativeSettingReader) GetCreativeModelSettings(context.Context) []creative.CreativeModelSetting {
	return []creative.CreativeModelSetting{{
		GroupID:    12,
		Model:      "gemini-3.1-flash-image",
		Operations: []string{creative.CreativeOperationGenerate, creative.CreativeOperationEdit},
	}}
}

// TestCreativeFullChainSmoke exercises the creative workflow with production Redis adapters and miniredis.
func TestCreativeFullChainSmoke(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	cfg := &config.Config{
		Creative: config.CreativeConfig{
			Enabled:                 true,
			QueueEnabled:            true,
			TransientTTLSeconds:     1800,
			MaxAssetBytes:           33554432,
			MaxTotalInputBytes:      67108864,
			MaxPromptChars:          8000,
			DefaultResponseMimeType: "image/png",
			DefaultImageSize:        "1K",
			ExecuteTimeoutSeconds:   300,
			MaxExecuteAttempts:      3,
			JobLockTTLSeconds:       300,
			StaleActiveAfterSeconds: 600,
			DelayedMoveLimit:        100,
			RecoverLimit:            100,
		},
		Default: config.DefaultConfig{APIKeyPrefix: "sk-"},
	}

	repo := newSmokeFakeRunRepo()
	billing := &smokeFakeBillingRepo{}
	queue := creativeredis.NewCreativeQueue(client, &creativeredis.QueueOptions{
		InflightKeyPrefix:  cfg.Creative.InflightKeyPrefix,
		InflightTTLSeconds: cfg.Creative.InflightTTLSeconds,
		JobLockTTLSeconds:  cfg.Creative.JobLockTTLSeconds,
		LockKeyPrefix:      cfg.Creative.LockKeyPrefix,
		QueueActiveKey:     cfg.Creative.QueueActiveKey,
		QueueDelayedKey:    cfg.Creative.QueueDelayedKey,
		QueueReadyKey:      cfg.Creative.QueueReadyKey,
	})
	store := creativeredis.NewCreativeTransientStore(client, &creativeredis.TransientOptions{TransientTTLSeconds: cfg.Creative.TransientTTLSeconds})
	results := &creative.Results{Repo: repo, TransientStore: store, Queue: queue, Funding: creative.Funding{Store: billing}, TransientTTL: time.Duration(cfg.Creative.TransientTTLSeconds) * time.Second}
	managed := apikey.ManagedKeys{Store: &smokeFakeManagedKeyRepo{}, Prefix: cfg.Default.APIKeyPrefix, ManagedBy: creative.CreativeManagedBy, NamePrefix: "creative-studio"}
	price := 0.02
	prices := billingtestkit.SharedPriceResolver(billingtestkit.Calculator(nil, nil), 12, pricing.DefaultBillingSettings(), []routing.ModelPricingEntry{{Models: []string{"*"}, BillingMode: routing.BillingModeImage, PerRequestPrice: &price}})
	svc := &creative.Public{
		Repo: repo, UserRepo: &smokeFakeUserRepo{}, ProviderRepo: &smokeFakeProviderRepo{}, GroupRepo: &smokeFakeGroupRepo{}, UserGroupRateRepo: &smokeFakeRateRepo{}, Queue: queue, TransientStore: store, Results: results, Settings: smokeCreativeSettingReader{}, UserNotFound: identity.ErrUserNotFound,
		Options: creative.PublicOptions{Enabled: cfg.Creative.Enabled, MaxAssetBytes: cfg.Creative.MaxAssetBytes, MaxTotalInputBytes: cfg.Creative.MaxTotalInputBytes, MaxPromptChars: cfg.Creative.MaxPromptChars, DefaultImageSize: cfg.Creative.DefaultImageSize},
		EnsureKey: func(ctx context.Context, u, g int64) (int64, error) {
			key, err := managed.Ensure(ctx, u, g)
			if err != nil {
				return 0, err
			}
			return key.ID, nil
		},
		ImageUnitPrice: func(ctx context.Context, g *creative.GroupView, m, size string) (float64, bool) {
			price, err := prices.ResolveImageUnitPrice(ctx, billingcore.PricingInput{Model: m, GroupID: &g.ID}, size)
			return price, err == nil
		},
		SubscriptionMultiplier: func(context.Context, int64, *creative.GroupView, float64) (float64, bool) { return 0, false },
	}
	worker := creative.NewCreativeRunWorker(queue, repo, store, &smokeFakeExecutor{}, results, creative.CreativeWorkerOptions{ReserveBlockTimeout: time.Second, JobLockTTL: time.Minute, LockConflictDelay: time.Millisecond, ErrorRetryDelay: time.Millisecond, ErrorBackoff: time.Millisecond, StaleActiveAfter: time.Minute, MaxAttempts: 3}, creative.WorkerPorts{})
	ctx := context.Background()

	// Create a queued run with a source image and prompt, including estimation and reservation.
	png := smokeTestPNG(t)
	scope := creative.CreativeRunScope{UserID: 7, WorkspaceID: "11111111-1111-4111-8111-111111111111"}
	created, err := svc.CreateRun(ctx, scope, creative.CreateCreativeRunParamsPublic{
		GroupID:      12,
		Model:        "gemini-3.1-flash-image",
		Operation:    creative.CreativeOperationGenerate,
		Prompt:       "smoke prompt",
		SourceImages: []creative.CreativeInputImage{{Bytes: png, Mime: "image/png"}},
		ImageSize:    "1K",
	}, "smoke-idem-key")
	require.NoError(t, err)
	require.Equal(t, creative.CreativeRunStatusQueued, created.Status)
	runID := created.ID
	require.Equal(t, creative.PlatformGemini, repo.runs[runID].Platform)

	// Reserve from the production queue, execute the fake provider, and capture the charge.
	require.NoError(t, worker.RunOnce(ctx))
	require.Equal(t, creative.CreativeRunStatusSucceeded, repo.runs[runID].Status)
	require.Equal(t, 1, billing.captureN)
	require.Equal(t, creative.CreativeRunOutputStatusSucceeded, repo.outputs[runID][0].Status)

	// Retrieve the generated output bytes.
	content, err := svc.GetOutputContent(ctx, scope, runID, 0)
	require.NoError(t, err)
	require.Equal(t, []byte("smoke-image-bytes"), content.Content)
	require.Equal(t, "image/png", content.ContentType)

	// Acknowledgement deletes the transient output.
	require.NoError(t, svc.AckOutput(ctx, scope, runID, 0))
	require.Equal(t, creative.CreativeRunOutputStatusAcked, repo.outputs[runID][0].Status)
	_, err = store.LoadOutput(ctx, runID, 0)
	require.Error(t, err, "ack 后临时输出必须已删除")

	// Reading an acknowledged output returns ErrCreativeOutputExpired.
	_, err = svc.GetOutputContent(ctx, scope, runID, 0)
	require.ErrorIs(t, err, creative.ErrCreativeOutputExpired)
}

// smokeTestPNG generates a valid 2-by-2 PNG fixture.
func smokeTestPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

// RecordProviderOutcome stores output snapshots in memory. PostgreSQL tests cover transactional rollback.
func (r *smokeFakeRunRepo) RecordProviderOutcome(ctx context.Context, id string, providerID int64, outputs []creative.CreativeRunOutput, now time.Time) error {
	run, err := r.GetCreativeRunByRunID(ctx, id)
	if err != nil {
		return err
	}
	if run.ProviderResultRecordedAt != nil {
		return nil
	}
	snapshots := make([]*creative.CreativeRunOutput, len(outputs))
	for i := range outputs {
		value := outputs[i]
		snapshots[i] = &value
	}
	r.outputs[id] = snapshots
	return r.MarkCreativeRunProviderSucceeded(ctx, id, providerID, now)
}

func (r *smokeFakeRunRepo) CompleteProviderOutcome(ctx context.Context, id string, cost float64, lost bool, now time.Time) error {
	if err := r.MarkCreativeRunSucceeded(ctx, id, cost, now); err != nil {
		return err
	}
	if lost {
		r.runs[id].Status = creative.CreativeRunStatusResultLost
	}
	return nil
}
