package postgres

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/creativerun"
	"github.com/TokenFlux/TokenRouter/ent/creativerunoutput"
	"github.com/TokenFlux/TokenRouter/internal/creative"
	postgresinfra "github.com/TokenFlux/TokenRouter/internal/infra/postgres"
	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// creativeRunRepository stores creative run metadata through Ent.
// State transitions using version checks reject updates when another writer advances the run.
type creativeRunRepository struct {
	client *dbent.Client
}

// translatePersistenceError maps missing records and conflicts to run errors.
func translatePersistenceError(err error, notFound, conflict *apperror.ApplicationError) error {
	if err == nil {
		return nil
	}
	if notFound != nil && (errors.Is(err, sql.ErrNoRows) || dbent.IsNotFound(err)) {
		return notFound.WithCause(err)
	}
	if conflict != nil && postgresinfra.IsUniqueConstraintViolation(err) {
		return conflict.WithCause(err)
	}
	return err
}

// NewCreativeRunRepository creates a creative run repository.
func NewCreativeRunRepository(client *dbent.Client) creative.CreativeRunRepository {
	return &creativeRunRepository{client: client}
}

func (r *creativeRunRepository) CreateCreativeRun(ctx context.Context, params creative.CreateCreativeRunParams) (*creative.CreativeRun, error) {
	if params.RunID == "" {
		runID, err := creative.NewCreativeRunID()
		if err != nil {
			return nil, err
		}
		params.RunID = runID
	}
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	builder := tx.CreativeRun.Create().
		SetRunID(params.RunID).
		SetUserID(params.UserID).
		SetGroupID(params.GroupID).
		SetAPIKeyID(params.APIKeyID).
		SetPlatform(params.Platform).
		SetModel(params.Model).
		SetRequestedModel(params.RequestedModel).
		SetOperation(params.Operation).
		SetRequestedOutputCount(params.RequestedOutputCount).
		SetImageSize(params.ImageSize).
		SetAspectRatio(params.AspectRatio).
		SetResponseMimeType(params.ResponseMIMEType).
		SetPromptHash(params.PromptHash).
		SetRequestFingerprint(params.RequestFingerprint).
		SetStatus(creative.CreativeRunStatusQueued).
		SetEstimatedCost(params.EstimatedCost).
		SetHoldAmount(params.HoldAmount).
		SetBaseUnitPrice(params.BaseUnitPrice).
		SetSubscriptionRateMultiplier(params.SubscriptionRateMultiplier).
		SetBalanceRateMultiplier(params.BalanceRateMultiplier).
		SetPlanGroupRateMultiplierEnabled(params.PlanGroupRateEnabled).
		SetProvisioningPhase(creative.CreativeProvisioningPhaseCreated)
	if params.WorkspaceID != "" {
		builder.SetWorkspaceID(params.WorkspaceID)
	}
	if params.IdempotencyKey != nil {
		builder.SetIdempotencyKey(*params.IdempotencyKey)
	}
	entity, err := builder.Save(ctx)
	if err != nil {
		return nil, translatePersistenceError(err, nil, creative.ErrCreativeRunExists)
	}
	// Create pending outputs in the run transaction so their metadata appears atomically.
	outputBuilders := make([]*dbent.CreativeRunOutputCreate, 0, params.RequestedOutputCount)
	for index := range params.RequestedOutputCount {
		outputBuilders = append(outputBuilders, tx.CreativeRunOutput.Create().
			SetRunID(params.RunID).
			SetOutputIndex(index).
			SetStatus(creative.CreativeRunOutputStatusPending))
	}
	if len(outputBuilders) > 0 {
		if err := tx.CreativeRunOutput.CreateBulk(outputBuilders...).Exec(ctx); err != nil {
			return nil, translatePersistenceError(err, nil, creative.ErrCreativeOutputExists)
		}
	}
	// Commit the provisioning outbox entry with the queued run so enqueue can be recovered.
	if _, err := tx.CreativeRunOutbox.Create().
		SetRunID(params.RunID).
		SetOperation(string(creative.CreativeRunOutboxProvision)).
		SetStatus(string(creative.CreativeRunOutboxPending)).
		Save(ctx); err != nil {
		return nil, translatePersistenceError(err, nil, creative.ErrCreativeRunExists)
	}
	if err := tx.Commit(); err != nil {
		return nil, translatePersistenceError(err, nil, creative.ErrCreativeRunExists)
	}
	return creativeRunEntityToService(entity), nil
}

func (r *creativeRunRepository) GetCreativeRunByRunID(ctx context.Context, runID string) (*creative.CreativeRun, error) {
	entity, err := r.client.CreativeRun.Query().
		Where(creativerun.RunIDEQ(runID)).
		Only(ctx)
	if err != nil {
		return nil, translatePersistenceError(err, creative.ErrCreativeRunNotFound, nil)
	}
	return creativeRunEntityToService(entity), nil
}

func (r *creativeRunRepository) GetCreativeRunByRunIDForOwner(ctx context.Context, scope creative.CreativeRunScope, runID string) (*creative.CreativeRun, error) {
	entity, err := r.client.CreativeRun.Query().
		Where(
			creativerun.RunIDEQ(runID),
			creativerun.UserIDEQ(scope.UserID),
			creativerun.WorkspaceIDEQ(scope.WorkspaceID),
		).
		Only(ctx)
	if err != nil {
		return nil, translatePersistenceError(err, creative.ErrCreativeRunNotFound, nil)
	}
	return creativeRunEntityToService(entity), nil
}

func (r *creativeRunRepository) GetCreativeRunByIdempotencyKey(ctx context.Context, scope creative.CreativeRunScope, key string) (*creative.CreativeRun, error) {
	entity, err := r.client.CreativeRun.Query().
		Where(
			creativerun.UserIDEQ(scope.UserID),
			creativerun.WorkspaceIDEQ(scope.WorkspaceID),
			creativerun.IdempotencyKeyEQ(key),
		).
		Order(dbent.Desc(creativerun.FieldID)).
		First(ctx)
	if err != nil {
		return nil, translatePersistenceError(err, creative.ErrCreativeRunNotFound, nil)
	}
	return creativeRunEntityToService(entity), nil
}

func (r *creativeRunRepository) ListCreativeRunsForOwner(ctx context.Context, scope creative.CreativeRunScope, filter creative.CreativeRunFilter) ([]*creative.CreativeRun, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if filter.Offset < 0 {
		filter.Offset = 0
	}
	query := r.client.CreativeRun.Query().
		Where(
			creativerun.UserIDEQ(scope.UserID),
			creativerun.WorkspaceIDEQ(scope.WorkspaceID),
		)
	if filter.Status != "" {
		if filter.Status == "active" {
			query = query.Where(creativerun.StatusIn(
				creative.CreativeRunStatusQueued,
				creative.CreativeRunStatusRunning,
				creative.CreativeRunStatusProviderSucceeded,
				creative.CreativeRunStatusSettlementPending,
			))
		} else {
			query = query.Where(creativerun.StatusEQ(filter.Status))
		}
	}
	entities, err := query.
		Order(dbent.Desc(creativerun.FieldCreatedAt), dbent.Desc(creativerun.FieldID)).
		Limit(limit).
		Offset(filter.Offset).
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*creative.CreativeRun, 0, len(entities))
	for _, entity := range entities {
		out = append(out, creativeRunEntityToService(entity))
	}
	return out, nil
}

// TransitionCreativeRunStatus validates the transition and updates with an optimistic version check.
func (r *creativeRunRepository) TransitionCreativeRunStatus(ctx context.Context, runID, toStatus string, opts creative.CreativeRunTransitionOptions) error {
	now := time.Now()
	if opts.Now != nil {
		now = *opts.Now
	}
	current, err := r.client.CreativeRun.Query().
		Where(creativerun.RunIDEQ(runID)).
		Only(ctx)
	if err != nil {
		return translatePersistenceError(err, creative.ErrCreativeRunNotFound, nil)
	}
	if !creative.CanTransitionCreativeRun(current.Status, toStatus) {
		return creative.ErrCreativeInvalidTransition
	}
	builder := r.client.CreativeRun.Update().
		Where(
			creativerun.RunIDEQ(runID),
			creativerun.VersionEQ(current.Version),
		).
		SetStatus(toStatus).
		SetVersion(current.Version + 1).
		SetUpdatedAt(now)
	if toStatus == creative.CreativeRunStatusRunning {
		builder.SetStartedAt(now)
	}
	if creative.IsTerminalCreativeRunStatus(toStatus) {
		builder.SetCompletedAt(now)
	}
	if toStatus == creative.CreativeRunStatusCancelled {
		builder.SetCancelledAt(now)
	}
	if toStatus == creative.CreativeRunStatusFailed {
		if opts.ErrorCode != nil {
			builder.SetErrorCode(*opts.ErrorCode)
		}
		if opts.ErrorMessage != nil {
			builder.SetErrorMessage(*opts.ErrorMessage)
		}
	}
	if toStatus == creative.CreativeRunStatusReleasePending {
		target := opts.ReleaseTargetStatus
		if target == "" {
			target = creative.CreativeRunStatusFailed
		}
		builder.SetReleaseTargetStatus(target)
		if opts.ErrorCode != nil {
			builder.SetErrorCode(*opts.ErrorCode)
		}
		if opts.ErrorMessage != nil {
			builder.SetErrorMessage(*opts.ErrorMessage)
		}
	}
	if toStatus == creative.CreativeRunStatusResultLost {
		if opts.ErrorCode != nil {
			builder.SetErrorCode(*opts.ErrorCode)
		}
		if opts.ErrorMessage != nil {
			builder.SetErrorMessage(*opts.ErrorMessage)
		}
	}
	affected, err := builder.Save(ctx)
	if err != nil {
		return err
	}
	if affected == 0 {
		// A version conflict means another writer advanced the run; the caller can reread its state.
		return creative.ErrCreativeInvalidTransition
	}
	return nil
}

// MarkCreativeRunRunning advances queued to running idempotently and records the provider.
func (r *creativeRunRepository) MarkCreativeRunRunning(ctx context.Context, runID string, providerID int64, now time.Time) error {
	current, err := r.client.CreativeRun.Query().
		Where(creativerun.RunIDEQ(runID)).
		Only(ctx)
	if err != nil {
		return translatePersistenceError(err, creative.ErrCreativeRunNotFound, nil)
	}
	if current.Status == creative.CreativeRunStatusRunning {
		// A retry of a running job succeeds and updates the provider when needed.
		if providerID > 0 && (current.ProviderID == nil || *current.ProviderID != providerID) {
			_, err := r.client.CreativeRun.Update().
				Where(creativerun.RunIDEQ(runID)).
				SetProviderID(providerID).
				SetUpdatedAt(now).
				Save(ctx)
			return err
		}
		return nil
	}
	if !creative.CanTransitionCreativeRun(current.Status, creative.CreativeRunStatusRunning) {
		return creative.ErrCreativeInvalidTransition
	}
	builder := r.client.CreativeRun.Update().
		Where(
			creativerun.RunIDEQ(runID),
			creativerun.VersionEQ(current.Version),
		).
		SetStatus(creative.CreativeRunStatusRunning).
		SetVersion(current.Version + 1).
		SetStartedAt(now).
		SetUpdatedAt(now)
	if providerID > 0 {
		builder.SetProviderID(providerID)
	}
	affected, err := builder.Save(ctx)
	if err != nil {
		return err
	}
	if affected == 0 {
		return creative.ErrCreativeInvalidTransition
	}
	return nil
}

// SetCreativeRunExecution saves the selected provider and platform.
func (r *creativeRunRepository) SetCreativeRunExecution(ctx context.Context, runID string, providerID int64, platform string, now time.Time) error {
	if providerID <= 0 {
		return nil
	}
	affected, err := r.client.CreativeRun.Update().
		Where(creativerun.RunIDEQ(runID)).
		SetPlatform(platform).
		SetProviderID(providerID).
		SetUpdatedAt(now).
		Save(ctx)
	if err != nil {
		return err
	}
	if affected == 0 {
		return creative.ErrCreativeRunNotFound
	}
	return nil
}

// MarkCreativeRunSucceeded records cost and succeeds a running, provider_succeeded, or settlement_pending run.
func (r *creativeRunRepository) MarkCreativeRunSucceeded(ctx context.Context, runID string, actualCost float64, now time.Time) error {
	affected, err := r.client.CreativeRun.Update().
		Where(
			creativerun.RunIDEQ(runID),
			creativerun.StatusIn(
				creative.CreativeRunStatusRunning,
				creative.CreativeRunStatusProviderSucceeded,
				creative.CreativeRunStatusSettlementPending,
			),
		).
		SetStatus(creative.CreativeRunStatusSucceeded).
		SetActualCost(actualCost).
		SetCompletedAt(now).
		SetUpdatedAt(now).
		AddVersion(1).
		Save(ctx)
	if err != nil {
		return err
	}
	if affected == 0 {
		current, getErr := r.client.CreativeRun.Query().
			Where(creativerun.RunIDEQ(runID)).
			Only(ctx)
		if getErr != nil {
			return translatePersistenceError(getErr, creative.ErrCreativeRunNotFound, nil)
		}
		if current.Status == creative.CreativeRunStatusSucceeded || current.Status == creative.CreativeRunStatusCancelled {
			return nil
		}
		return creative.ErrCreativeInvalidTransition
	}
	return nil
}

// MarkCreativeRunProviderSucceeded records provider success for settlement recovery.
// Callers retain the generated output so settlement retries do not repeat generation.
func (r *creativeRunRepository) MarkCreativeRunProviderSucceeded(ctx context.Context, runID string, providerID int64, now time.Time) error {
	current, err := r.client.CreativeRun.Query().Where(creativerun.RunIDEQ(runID)).Only(ctx)
	if err != nil {
		return translatePersistenceError(err, creative.ErrCreativeRunNotFound, nil)
	}
	if current.Status == creative.CreativeRunStatusSucceeded || current.Status == creative.CreativeRunStatusResultLost {
		return nil
	}
	builder := r.client.CreativeRun.Update().
		Where(creativerun.RunIDEQ(runID), creativerun.VersionEQ(current.Version)).
		SetProviderResultRecordedAt(now).
		SetUpdatedAt(now).
		AddVersion(1)
	if providerID > 0 {
		builder.SetProviderID(providerID)
	}
	if current.Status == creative.CreativeRunStatusRunning {
		builder.SetStatus(creative.CreativeRunStatusProviderSucceeded)
	}
	if _, err := builder.Save(ctx); err != nil {
		return err
	}
	return nil
}

// UpdateCreativeRunOutput updates output metadata while leaving acknowledged rows unchanged.
func (r *creativeRunRepository) UpdateCreativeRunOutput(ctx context.Context, runID string, outputIndex int, status, mimeType string, byteSize int64, transientExpiresAt *time.Time, errorCode, errorMessage string) error {
	builder := r.client.CreativeRunOutput.Update().
		Where(
			creativerunoutput.RunIDEQ(runID),
			creativerunoutput.OutputIndexEQ(outputIndex),
			// Acknowledged output has already been received by the client and must not be overwritten.
			creativerunoutput.StatusNEQ(creative.CreativeRunOutputStatusAcked),
		).
		SetStatus(status).
		SetUpdatedAt(time.Now())
	if mimeType != "" {
		builder.SetMimeType(mimeType)
	}
	if byteSize > 0 {
		builder.SetByteSize(byteSize)
	}
	if transientExpiresAt != nil {
		builder.SetTransientExpiresAt(*transientExpiresAt)
	}
	if errorCode != "" {
		builder.SetErrorCode(errorCode)
	}
	if errorMessage != "" {
		builder.SetErrorMessage(errorMessage)
	}
	affected, err := builder.Save(ctx)
	if err != nil {
		return err
	}
	if affected == 0 {
		// An acknowledged row is an idempotent success; a missing row is an error.
		exists, existsErr := r.client.CreativeRunOutput.Query().
			Where(
				creativerunoutput.RunIDEQ(runID),
				creativerunoutput.OutputIndexEQ(outputIndex),
			).
			Exist(ctx)
		if existsErr != nil {
			return existsErr
		}
		if !exists {
			return creative.ErrCreativeOutputNotFound
		}
	}
	return nil
}

func (r *creativeRunRepository) GetCreativeRunOutput(ctx context.Context, runID string, outputIndex int) (*creative.CreativeRunOutput, error) {
	entity, err := r.client.CreativeRunOutput.Query().
		Where(
			creativerunoutput.RunIDEQ(runID),
			creativerunoutput.OutputIndexEQ(outputIndex),
		).
		Only(ctx)
	if err != nil {
		return nil, translatePersistenceError(err, creative.ErrCreativeOutputNotFound, nil)
	}
	return creativeRunOutputEntityToService(entity), nil
}

func (r *creativeRunRepository) ListCreativeRunOutputs(ctx context.Context, runID string) ([]*creative.CreativeRunOutput, error) {
	entities, err := r.client.CreativeRunOutput.Query().
		Where(creativerunoutput.RunIDEQ(runID)).
		Order(dbent.Asc(creativerunoutput.FieldOutputIndex)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*creative.CreativeRunOutput, 0, len(entities))
	for _, entity := range entities {
		out = append(out, creativeRunOutputEntityToService(entity))
	}
	return out, nil
}

// ListCreativeRunOutputsForRuns fetches output metadata for multiple history or active-list runs.
func (r *creativeRunRepository) ListCreativeRunOutputsForRuns(ctx context.Context, runIDs []string) (map[string][]*creative.CreativeRunOutput, error) {
	result := make(map[string][]*creative.CreativeRunOutput, len(runIDs))
	if len(runIDs) == 0 {
		return result, nil
	}
	entities, err := r.client.CreativeRunOutput.Query().
		Where(creativerunoutput.RunIDIn(runIDs...)).
		Order(dbent.Asc(creativerunoutput.FieldRunID), dbent.Asc(creativerunoutput.FieldOutputIndex)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	for _, entity := range entities {
		result[entity.RunID] = append(result[entity.RunID], creativeRunOutputEntityToService(entity))
	}
	return result, nil
}

// MarkCreativeRunOutputAcked acknowledges a succeeded output; repeated acknowledgments succeed.
func (r *creativeRunRepository) MarkCreativeRunOutputAcked(ctx context.Context, runID string, outputIndex int, now time.Time) error {
	affected, err := r.client.CreativeRunOutput.Update().
		Where(
			creativerunoutput.RunIDEQ(runID),
			creativerunoutput.OutputIndexEQ(outputIndex),
			creativerunoutput.StatusEQ(creative.CreativeRunOutputStatusSucceeded),
		).
		SetStatus(creative.CreativeRunOutputStatusAcked).
		SetAckedAt(now).
		SetUpdatedAt(now).
		Save(ctx)
	if err != nil {
		return err
	}
	if affected == 0 {
		exists, existsErr := r.client.CreativeRunOutput.Query().
			Where(
				creativerunoutput.RunIDEQ(runID),
				creativerunoutput.OutputIndexEQ(outputIndex),
			).
			Only(ctx)
		if existsErr != nil {
			return translatePersistenceError(existsErr, creative.ErrCreativeOutputNotFound, nil)
		}
		if exists.Status == creative.CreativeRunOutputStatusAcked {
			return nil
		}
		return creative.ErrCreativeOutputNotReady
	}
	return nil
}

// ListCreativeRunsDueForTransientCleanup lists terminal runs completed at or before cutoff for transient cleanup.
func (r *creativeRunRepository) ListCreativeRunsDueForTransientCleanup(ctx context.Context, cutoff time.Time, limit int) ([]*creative.CreativeRun, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	entities, err := r.client.CreativeRun.Query().
		Where(
			creativerun.StatusIn(
				creative.CreativeRunStatusSucceeded,
				creative.CreativeRunStatusFailed,
				creative.CreativeRunStatusCancelled,
				creative.CreativeRunStatusResultLost,
			),
			creativerun.CompletedAtLTE(cutoff),
		).
		Order(dbent.Asc(creativerun.FieldID)).
		Limit(limit).
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*creative.CreativeRun, 0, len(entities))
	for _, entity := range entities {
		out = append(out, creativeRunEntityToService(entity))
	}
	return out, nil
}

// IncrementCreativeRunAttempt atomically increments attempt_count and returns the new value.
func (r *creativeRunRepository) IncrementCreativeRunAttempt(ctx context.Context, runID string) (int, error) {
	affected, err := r.client.CreativeRun.Update().
		Where(creativerun.RunIDEQ(runID)).
		AddAttemptCount(1).
		SetUpdatedAt(time.Now()).
		Save(ctx)
	if err != nil {
		return 0, err
	}
	if affected == 0 {
		return 0, creative.ErrCreativeRunNotFound
	}
	entity, err := r.client.CreativeRun.Query().
		Where(creativerun.RunIDEQ(runID)).
		Select(creativerun.FieldAttemptCount).
		Only(ctx)
	if err != nil {
		return 0, translatePersistenceError(err, creative.ErrCreativeRunNotFound, nil)
	}
	return entity.AttemptCount, nil
}

func (r *creativeRunRepository) IncrementCreativeRunSettlementAttempt(ctx context.Context, runID string) (int, error) {
	return r.incrementCreativeRunCounter(ctx, runID, true)
}

func (r *creativeRunRepository) IncrementCreativeRunReleaseAttempt(ctx context.Context, runID string) (int, error) {
	return r.incrementCreativeRunCounter(ctx, runID, false)
}

func (r *creativeRunRepository) incrementCreativeRunCounter(ctx context.Context, runID string, settlement bool) (int, error) {
	update := r.client.CreativeRun.Update().Where(creativerun.RunIDEQ(runID)).SetUpdatedAt(time.Now())
	if settlement {
		update.AddSettlementAttemptCount(1)
	} else {
		update.AddReleaseAttemptCount(1)
	}
	if _, err := update.Save(ctx); err != nil {
		return 0, err
	}
	query := r.client.CreativeRun.Query().Where(creativerun.RunIDEQ(runID))
	if settlement {
		entity, err := query.Select(creativerun.FieldSettlementAttemptCount).Only(ctx)
		if err != nil {
			return 0, translatePersistenceError(err, creative.ErrCreativeRunNotFound, nil)
		}
		return entity.SettlementAttemptCount, nil
	}
	entity, err := query.Select(creativerun.FieldReleaseAttemptCount).Only(ctx)
	if err != nil {
		return 0, translatePersistenceError(err, creative.ErrCreativeRunNotFound, nil)
	}
	return entity.ReleaseAttemptCount, nil
}

func (r *creativeRunRepository) SetCreativeRunProvisioningPhase(ctx context.Context, runID, phase string) error {
	if strings.TrimSpace(phase) == "" {
		return creative.ErrCreativeInvalidParams
	}
	affected, err := r.client.CreativeRun.Update().
		Where(creativerun.RunIDEQ(runID)).
		SetProvisioningPhase(phase).
		SetUpdatedAt(time.Now()).
		Save(ctx)
	if err != nil {
		return err
	}
	if affected == 0 {
		return creative.ErrCreativeRunNotFound
	}
	return nil
}

// SetCreativeRunAllowanceReserved persists the allowance reservation flag, cleared after release.
func (r *creativeRunRepository) SetCreativeRunAllowanceReserved(ctx context.Context, runID string, reserved bool) error {
	affected, err := r.client.CreativeRun.Update().
		Where(creativerun.RunIDEQ(runID)).
		SetAllowanceReserved(reserved).
		SetUpdatedAt(time.Now()).
		Save(ctx)
	if err != nil {
		return err
	}
	if affected == 0 {
		return creative.ErrCreativeRunNotFound
	}
	return nil
}

func (r *creativeRunRepository) SetCreativeRunReconcileError(ctx context.Context, runID, message string, next time.Time) error {
	builder := r.client.CreativeRun.Update().
		Where(creativerun.RunIDEQ(runID)).
		SetUpdatedAt(time.Now())
	if strings.TrimSpace(message) == "" {
		builder.ClearLastReconcileError()
	} else {
		builder.SetLastReconcileError(message)
	}
	if next.IsZero() {
		builder.ClearNextReconcileAt()
	} else {
		builder.SetNextReconcileAt(next)
	}
	affected, err := builder.Save(ctx)
	if err != nil {
		return err
	}
	if affected == 0 {
		return creative.ErrCreativeRunNotFound
	}
	return nil
}

func creativeRunEntityToService(entity *dbent.CreativeRun) *creative.CreativeRun {
	if entity == nil {
		return nil
	}
	requestedModel := entity.RequestedModel
	if requestedModel == "" {
		requestedModel = entity.Model
	}
	return &creative.CreativeRun{
		ID:                          entity.ID,
		RunID:                       entity.RunID,
		UserID:                      entity.UserID,
		WorkspaceID:                 entity.WorkspaceID,
		GroupID:                     entity.GroupID,
		APIKeyID:                    entity.APIKeyID,
		ProviderID:                  entity.ProviderID,
		Platform:                    entity.Platform,
		Model:                       entity.Model,
		RequestedModel:              requestedModel,
		Operation:                   entity.Operation,
		RequestedOutputCount:        entity.RequestedOutputCount,
		ImageSize:                   entity.ImageSize,
		AspectRatio:                 entity.AspectRatio,
		ResponseMIMEType:            entity.ResponseMimeType,
		PromptHash:                  entity.PromptHash,
		RequestFingerprint:          entity.RequestFingerprint,
		IdempotencyKey:              entity.IdempotencyKey,
		Status:                      entity.Status,
		EstimatedCost:               entity.EstimatedCost,
		HoldAmount:                  entity.HoldAmount,
		ActualCost:                  entity.ActualCost,
		BalanceHoldAmount:           entity.BalanceHoldAmount,
		SubscriptionHoldAllocations: entity.SubscriptionHoldAllocations,
		AllowanceReserved:           entity.AllowanceReserved,
		BaseUnitPrice:               entity.BaseUnitPrice,
		SubscriptionRateMultiplier:  entity.SubscriptionRateMultiplier,
		BalanceRateMultiplier:       entity.BalanceRateMultiplier,
		PlanGroupRateEnabled:        entity.PlanGroupRateMultiplierEnabled,
		ErrorCode:                   entity.ErrorCode,
		ErrorMessage:                entity.ErrorMessage,
		ReleaseTargetStatus:         entity.ReleaseTargetStatus,
		AttemptCount:                entity.AttemptCount,
		SettlementAttemptCount:      entity.SettlementAttemptCount,
		ReleaseAttemptCount:         entity.ReleaseAttemptCount,
		ProvisioningPhase:           entity.ProvisioningPhase,
		ProviderResultRecordedAt:    entity.ProviderResultRecordedAt,
		NextReconcileAt:             entity.NextReconcileAt,
		LastReconcileError:          entity.LastReconcileError,
		Version:                     entity.Version,
		CreatedAt:                   entity.CreatedAt,
		UpdatedAt:                   entity.UpdatedAt,
		StartedAt:                   entity.StartedAt,
		CompletedAt:                 entity.CompletedAt,
		CancelledAt:                 entity.CancelledAt,
	}
}

func creativeRunOutputEntityToService(entity *dbent.CreativeRunOutput) *creative.CreativeRunOutput {
	if entity == nil {
		return nil
	}
	return &creative.CreativeRunOutput{
		ID:                 entity.ID,
		RunID:              entity.RunID,
		OutputIndex:        entity.OutputIndex,
		Status:             entity.Status,
		MimeType:           entity.MimeType,
		ByteSize:           entity.ByteSize,
		TransientExpiresAt: entity.TransientExpiresAt,
		AckedAt:            entity.AckedAt,
		ErrorCode:          entity.ErrorCode,
		ErrorMessage:       entity.ErrorMessage,
		CreatedAt:          entity.CreatedAt,
		UpdatedAt:          entity.UpdatedAt,
	}
}
