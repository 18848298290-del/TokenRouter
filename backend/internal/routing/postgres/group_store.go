package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	entsql "entgo.io/ent/dialect/sql"
	"github.com/lib/pq"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/group"
	postgresinfra "github.com/TokenFlux/TokenRouter/internal/infra/postgres"
	logger "github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// Group mutations can change provider membership, user permissions, and
// scheduler visibility alongside the stored group. Context-carried Ent
// transactions supply both entity writes and SQL participants, allowing
// administrative creation and updates to commit their dependent writes
// together. Duplication treats outbox insertion as required transaction work,
// while ordinary mutations log outbox failures after their database operation.
// Transaction-backed mutations still depend on their enclosing owner's commit.
//
// Group reads expose stored policy and optionally aggregate provider counts
// from membership rows. Available counts apply current scheduling windows, and
// temporarily limited counts retain eligible providers blocked by those
// windows. Count-sorted lists must inspect the complete filtered set before
// selecting a page; ordinary lists keep their group records when optional count
// loading fails. Ent's soft-delete hooks retain removed group rows, so direct
// SQL reads apply their own deleted_at filters.

// The provider-count predicates classify linked records against stored
// eligibility and temporary scheduling windows for single and batched group
// reads.
const (
	// groupProviderAvailableSQL counts currently schedulable linked providers
	// using the eligibility filters applied by provider storage's
	// ListSchedulableByGroupID.
	groupProviderAvailableSQL = `a.deleted_at IS NULL
				AND a.status = 'active'
				AND a.schedulable = true
				AND (a.expires_at IS NULL OR a.expires_at > NOW() OR a.auto_pause_on_expired = FALSE)
				AND (a.rate_limit_reset_at IS NULL OR a.rate_limit_reset_at <= NOW())
				AND (a.overload_until IS NULL OR a.overload_until <= NOW())
				AND (a.temp_unschedulable_until IS NULL OR a.temp_unschedulable_until <= NOW())`

	// groupProviderTemporarilyLimitedSQL counts otherwise eligible providers
	// blocked by a rate-limit, overload, or temporary unschedulable window at
	// database time.
	groupProviderTemporarilyLimitedSQL = `a.deleted_at IS NULL
				AND a.status = 'active'
				AND a.schedulable = true
				AND (a.expires_at IS NULL OR a.expires_at > NOW() OR a.auto_pause_on_expired = FALSE)
				AND (
					a.rate_limit_reset_at > NOW() OR
					a.overload_until > NOW() OR
					a.temp_unschedulable_until > NOW()
				)`
)

// GroupLinkParticipant changes provider membership through the group
// operation's selected executor.
type GroupLinkParticipant interface {
	// Clear removes all provider memberships for a group and returns affected-row
	// metadata. It uses the executor selected by the group operation, whose caller
	// owns transaction completion and scheduling notification.
	Clear(context.Context, int64) (sql.Result, error)
	// Bind adds provider IDs to a group while retaining existing membership pairs.
	// The group caller validates the selected providers, supplies the executor,
	// and owns commit and scheduling notification.
	Bind(context.Context, int64, []int64) error
	// Copy inserts source memberships into the destination and returns
	// inserted-row metadata. Deleted providers are skipped; the boolean requests
	// API-Key exclusion, and the group operation owns the supplied executor's
	// transaction.
	Copy(context.Context, int64, int64, bool) (sql.Result, error)
}

// GroupAccessParticipant removes group permissions using the deletion
// operation's selected executor.
type GroupAccessParticipant interface {
	// Delete removes a group's user grants and public-group denials through the
	// supplied executor. The group-deletion owner rolls back permission writes
	// when later work fails and completes the transaction on success.
	Delete(context.Context, int64) error
}

// GroupStoreOptions supplies membership, permission, and scheduler callbacks
// for group mutations.
type GroupStoreOptions struct {
	// Providers, Users, and Enqueue receive the operation's selected executor so
	// cross-module writes can join its transaction. Configure each callback before
	// invoking mutations that use it.
	// provider membership participant factory
	Providers func(postgresinfra.Executor) GroupLinkParticipant
	// user permission participant factory
	Users func(postgresinfra.Executor) GroupAccessParticipant
	// Enqueue writes the group's scheduling event. Duplication propagates its
	// errors, while ordinary mutations log them after their database operation
	// succeeds.
	// scheduler event insertion callback
	Enqueue func(context.Context, postgresinfra.Executor, *int64) error
}

// GroupStore persists routing groups and coordinates their cross-module
// database participants.
type GroupStore struct {
	// options selects the cross-module writers used by mutations. Its callbacks
	// are invoked synchronously with the transaction or default executor chosen
	// for that operation.
	options GroupStoreOptions
	// client and sql are borrowed entity and SQL executors for the same database.
	// Context transactions replace them in supported paths; the application owns
	// their connection lifetime.
	client *dbent.Client          // default entity persistence client
	sql    postgresinfra.Executor // default relational query executor
}

// groupProviderCounts holds membership totals and current scheduling
// classifications for one group.
type groupProviderCounts struct {
	// Total counts undeleted providers; Active and RateLimited partition otherwise
	// eligible providers by current scheduling windows. Inactive, unschedulable,
	// or expired paused records can remain in Total alone.
	Total       int64 // undeleted linked provider count
	Active      int64 // currently available provider count
	RateLimited int64 // temporarily blocked provider count
}

// NewGroupStore retains borrowed entity and SQL executors with mutation
// callbacks. Keep their connections available through store calls, and
// configure the callbacks required by each mutation before invoking it.
func NewGroupStore(client *dbent.Client, db postgresinfra.Executor, options GroupStoreOptions) *GroupStore {
	return &GroupStore{client: client, sql: db, options: options}
}

// Mutate runs a nonnil group-operation callback in an Ent transaction, passing
// that transaction through its context. When ctx already carries a transaction,
// or the store has no entity client, it invokes the callback directly and
// leaves completion to the caller.
//
// For a new transaction, the callback uses the supplied context so group writes
// and SQL participants share one connection. Callback failure exits through
// deferred rollback; successful callback completion is followed by commit. The
// helper returns begin and commit errors with group-operation context, while
// in-memory callback effects remain outside rollback.
func (r *GroupStore) Mutate(ctx context.Context, fn func(context.Context) error) error {
	if dbent.TxFromContext(ctx) != nil || r.client == nil {
		return fn(ctx)
	}

	tx, err := r.client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin group mutation transaction: %w", err)
	}
	// Roll back callback failures while this method still owns an open
	// transaction; deferred cleanup also covers other early exits before commit
	// is attempted.
	defer func() { _ = tx.Rollback() }()

	txCtx := dbent.NewTxContext(ctx, tx)
	if err := fn(txCtx); err != nil {
		return err
	}
	// Commit only after the callback has accepted every participant write,
	// publishing its group and association mutations through one completion point.
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit group mutation transaction: %w", err)
	}
	return nil
}

// sqlExecutorFromContext returns the context transaction's client or the
// default SQL executor. Participant operations use that borrowed executor to
// join group writes; they leave connection and transaction completion with the
// owner.
func (r *GroupStore) sqlExecutorFromContext(ctx context.Context) postgresinfra.Executor {
	if tx := dbent.TxFromContext(ctx); tx != nil {
		return tx.Client()
	}
	return r.sql
}

// LockGroupSortOrder acquires the group-order advisory lock within the
// transaction carried by ctx. Call it before reading the tail and creating a
// group; missing transaction or SQL executor returns an error.
func (r *GroupStore) LockGroupSortOrder(ctx context.Context) error {
	if dbent.TxFromContext(ctx) == nil {
		return errors.New("group sort order lock requires transaction")
	}
	sqlq := r.sqlExecutorFromContext(ctx)
	if sqlq == nil {
		return errors.New("sql executor is not configured")
	}
	// Use a transaction-scoped advisory lock so competing tail readers hold the
	// same ordering lock until their group creation commits or rolls back.
	_, err := sqlq.ExecContext(
		ctx,
		"SELECT pg_advisory_xact_lock(hashtext($1))",
		"tokenrouter_group_sort_order",
	)
	return err
}

// Create persists a nonnil group through the context transaction when present
// and fills generated identity and timestamps. Scheduler-event insertion
// follows the record write; its failure is logged while the successful group
// operation returns nil.
func (r *GroupStore) Create(ctx context.Context, groupIn *routing.Group) error {
	client := clientFromContext(ctx, r.client)
	sqlq := r.sqlExecutorFromContext(ctx)
	if err := createGroupRecord(ctx, client, groupIn); err != nil {
		return err
	}
	// Attempt scheduling notification after the row write. Event errors are
	// logged here; when ctx carries a transaction, its enclosing owner still has
	// to complete that transaction.
	if err := r.options.Enqueue(ctx, sqlq, &groupIn.ID); err != nil {
		logger.LegacyPrintf("repository.group", "[SchedulerOutbox] enqueue group create failed: group=%d err=%v", groupIn.ID, err)
	}
	return nil
}

// createGroupRecord validates scheduler type and writes a nonnil group's stored
// settings through client. It normalizes SchedulerType in place and fills
// generated ID and timestamps on success; unique constraints map to
// ErrGroupExists.
func createGroupRecord(ctx context.Context, client *dbent.Client, groupIn *routing.Group) error {
	if groupIn == nil {
		return errors.New("group is nil")
	}
	schedulerType, err := routing.NormalizeGroupSchedulerType(string(groupIn.SchedulerType))
	if err != nil {
		return err
	}
	groupIn.SchedulerType = schedulerType
	routingPolicy, err := json.Marshal(groupIn.RoutingPolicy)
	if err != nil {
		return err
	}
	builder := client.Group.Create().
		SetName(groupIn.Name).
		SetDescription(groupIn.Description).
		SetLocalization(groupIn.Localization).
		SetSchedulerType(string(groupIn.SchedulerType)).
		SetAdvancedSchedulerOverrides(groupIn.AdvancedSchedulerOverrides).
		SetDisplayBrand(groupIn.DisplayBrand).
		SetRateMultiplier(groupIn.RateMultiplier).
		SetSortOrder(groupIn.SortOrder).
		SetIsExclusive(groupIn.IsExclusive).
		SetStatus(groupIn.Status).
		SetSessionIsolationEnabled(groupIn.SessionIsolationEnabled).
		SetAllowImageGeneration(groupIn.AllowImageGeneration).
		SetAllowBatchImageGeneration(groupIn.AllowBatchImageGeneration).
		SetRoutingPolicy(routingPolicy).
		SetClaudeCodeOnly(groupIn.ClaudeCodeOnly).
		SetNillableFallbackGroupID(groupIn.FallbackGroupID).
		SetNillableFallbackGroupIDOnInvalidRequest(groupIn.FallbackGroupIDOnInvalidRequest).
		SetNillableUnavailableFallbackGroupID(groupIn.UnavailableFallbackGroupID).
		SetModelRoutingEnabled(groupIn.ModelRoutingEnabled).
		SetMcpXMLInject(groupIn.MCPXMLInject).
		SetAllowedProtocols(groupIn.AllowedProtocols).
		SetProtocolFallbacks(groupIn.ProtocolFallbacks).
		SetResponsesImagePolicy(groupIn.ResponsesImagePolicy).
		SetAllowMessagesDispatch(groupIn.AllowMessagesDispatch).
		SetAllowLive(groupIn.AllowLive).
		SetForceOpenaiFast(groupIn.ForceOpenAIFast).
		SetOpenaiFastPolicy(groupIn.EffectiveOpenAIFastPolicy()).
		SetRequireOauthOnly(groupIn.RequireOAuthOnly).
		SetRequirePrivacySet(groupIn.RequirePrivacySet).
		SetDefaultMappedModel(groupIn.DefaultMappedModel).
		SetModelsListConfig(groupIn.ModelsListConfig).
		SetAvailabilityProbeConfig(groupIn.AvailabilityProbeConfig).
		SetRpmLimit(groupIn.RPMLimit).
		SetMaxReasoningEffort(groupIn.MaxReasoningEffort).
		SetMaxReasoningEffortOverLimit(groupIn.MaxReasoningEffortOverLimit).
		SetReasoningEffortMappings(groupIn.ReasoningEffortMappings)
	if groupIn.DuplicateOperationID != "" {
		builder = builder.SetDuplicateOperationID(groupIn.DuplicateOperationID)
	}

	if groupIn.ModelRouting != nil {
		builder = builder.SetModelRouting(groupIn.ModelRouting)
	}

	// Write supported-model metadata even for an empty list so the stored
	// administrative value follows the supplied group rather than an entity
	// default.
	builder = builder.SetSupportedModelScopes(groupIn.SupportedModelScopes)

	created, err := builder.Save(ctx)
	if err != nil {
		return translatePersistenceError(err, nil, routing.ErrGroupExists)
	}
	groupIn.ID = created.ID
	groupIn.CreatedAt = created.CreatedAt
	groupIn.UpdatedAt = created.UpdatedAt
	return nil
}

// FindByDuplicateOperationID reads the earliest-ID group matching a trimmed
// duplication token through the retained client. Empty tokens and missing rows
// return nil without error, letting the caller decide whether duplication needs
// a new record.
func (r *GroupStore) FindByDuplicateOperationID(ctx context.Context, operationID string) (*routing.Group, error) {
	operationID = strings.TrimSpace(operationID)
	if operationID == "" {
		return nil, nil
	}
	row, err := r.client.Group.Query().
		Where(group.DuplicateOperationIDEQ(operationID)).
		Order(dbent.Asc(group.FieldID)).
		First(ctx)
	// A token lookup miss allows duplication to create its record; return no group
	// rather than treating that absence as a failed operation.
	if dbent.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find group duplicate operation: %w", err)
	}
	return GroupFromEnt(row), nil
}

// CreateFromSource saves a nonnil duplicate group, copies source memberships,
// and inserts its scheduler event in one transaction. Context transaction
// clients are reused; otherwise this method starts and owns a transaction.
// Copying skips deleted providers and applies the group's API-Key exclusion
// when RequireOAuthOnly is true.
//
// Record creation supplies the destination ID before membership copying. Outbox
// failure returns an error so the transaction owner can roll back all three
// writes. This method commits or rolls back only its own transaction, while
// generated group identity and timestamps remain assigned in memory.
// ProviderCount is filled when the copied-row count is available.
func (r *GroupStore) CreateFromSource(ctx context.Context, groupIn *routing.Group, sourceGroupID int64) error {
	if groupIn == nil {
		return errors.New("group is nil")
	}

	baseClient := clientFromContext(ctx, r.client)
	tx, err := baseClient.Tx(ctx)
	if err != nil && !errors.Is(err, dbent.ErrTxStarted) {
		return err
	}

	txClient := baseClient
	txCtx := ctx
	ownsTx := err == nil
	if ownsTx {
		// Own cleanup only when this method started the transaction; failures during
		// membership or outbox writes must roll back the newly created group.
		defer func() { _ = tx.Rollback() }()
		txClient = tx.Client()
		txCtx = dbent.NewTxContext(ctx, tx)
	}

	// Create the destination before copying memberships and its outbox event on
	// this transaction, giving both dependent writes the generated group ID.
	if err := createGroupRecord(txCtx, txClient, groupIn); err != nil {
		return err
	}
	result, err := r.options.Providers(txClient).Copy(txCtx, groupIn.ID, sourceGroupID, groupIn.RequireOAuthOnly)
	if err != nil {
		return err
	}
	// Treat inserted-row metadata as optional display information; failure to
	// obtain it leaves ProviderCount unchanged while the copied memberships remain
	// valid transaction work.
	if count, countErr := result.RowsAffected(); countErr == nil {
		groupIn.ProviderCount = count
	}
	if err := r.options.Enqueue(txCtx, txClient, &groupIn.ID); err != nil {
		return err
	}

	// Commit the duplicate only after its memberships and scheduling event
	// succeed; reused transactions remain for the enclosing caller to complete.
	if ownsTx {
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// GetByID reads a hydrated group and attempts to attach provider counts. Group
// lookup failures are returned, while count failures leave all count fields at
// zero so the stored group remains available.
func (r *GroupStore) GetByID(ctx context.Context, id int64) (*routing.Group, error) {
	out, err := r.GetByIDLite(ctx, id)
	if err != nil {
		return nil, err
	}
	// Provider counts supplement administrative display; a count-query failure
	// keeps the successfully loaded group and its zero-valued counters available.
	counts, err := r.loadProviderCounts(ctx, []int64{out.ID})
	if err == nil {
		c := counts[out.ID]
		out.ProviderCount = c.Total
		out.ActiveProviderCount = c.Active
		out.RateLimitedProviderCount = c.RateLimited
	}
	return out, nil
}

// GetByIDLite reads a hydrated group through the context transaction when
// present. It leaves provider counts at zero; callers needing those optional
// display aggregates use GetByID, and missing rows map to ErrGroupNotFound.
func (r *GroupStore) GetByIDLite(ctx context.Context, id int64) (*routing.Group, error) {
	client := clientFromContext(ctx, r.client)

	m, err := client.Group.Query().
		Where(group.IDEQ(id)).
		Only(ctx)
	if err != nil {
		return nil, translatePersistenceError(err, routing.ErrGroupNotFound, nil)
	}
	return GroupFromEnt(m), nil
}

// Update replaces a nonnil group's complete stored settings using UpdatedAt as
// the optimistic version. Nil fallback IDs and model routing clear their
// columns, and missing or stale rows return a conflict. Success refreshes
// UpdatedAt and logs scheduler-event failures.
func (r *GroupStore) Update(ctx context.Context, groupIn *routing.Group) error {
	if groupIn == nil {
		return errors.New("group is nil")
	}
	schedulerType, err := routing.NormalizeGroupSchedulerType(string(groupIn.SchedulerType))
	if err != nil {
		return err
	}
	groupIn.SchedulerType = schedulerType
	client := clientFromContext(ctx, r.client)
	sqlq := r.sqlExecutorFromContext(ctx)
	routingPolicy, err := json.Marshal(groupIn.RoutingPolicy)
	if err != nil {
		return err
	}

	builder := client.Group.UpdateOneID(groupIn.ID).
		Where(group.UpdatedAtEQ(groupIn.UpdatedAt)).
		SetName(groupIn.Name).
		SetDescription(groupIn.Description).
		SetLocalization(groupIn.Localization).
		SetSchedulerType(string(schedulerType)).
		SetAdvancedSchedulerOverrides(groupIn.AdvancedSchedulerOverrides).
		SetDisplayBrand(groupIn.DisplayBrand).
		SetRateMultiplier(groupIn.RateMultiplier).
		SetSortOrder(groupIn.SortOrder).
		SetIsExclusive(groupIn.IsExclusive).
		SetStatus(groupIn.Status).
		SetSessionIsolationEnabled(groupIn.SessionIsolationEnabled).
		SetAllowImageGeneration(groupIn.AllowImageGeneration).
		SetAllowBatchImageGeneration(groupIn.AllowBatchImageGeneration).
		SetRoutingPolicy(routingPolicy).
		SetClaudeCodeOnly(groupIn.ClaudeCodeOnly).
		SetModelRoutingEnabled(groupIn.ModelRoutingEnabled).
		SetMcpXMLInject(groupIn.MCPXMLInject).
		SetAllowedProtocols(groupIn.AllowedProtocols).
		SetProtocolFallbacks(groupIn.ProtocolFallbacks).
		SetResponsesImagePolicy(groupIn.ResponsesImagePolicy).
		SetAllowMessagesDispatch(groupIn.AllowMessagesDispatch).
		SetAllowLive(groupIn.AllowLive).
		SetForceOpenaiFast(groupIn.ForceOpenAIFast).
		SetOpenaiFastPolicy(groupIn.EffectiveOpenAIFastPolicy()).
		SetRequireOauthOnly(groupIn.RequireOAuthOnly).
		SetRequirePrivacySet(groupIn.RequirePrivacySet).
		SetDefaultMappedModel(groupIn.DefaultMappedModel).
		SetModelsListConfig(groupIn.ModelsListConfig).
		SetAvailabilityProbeConfig(groupIn.AvailabilityProbeConfig).
		SetRpmLimit(groupIn.RPMLimit).
		SetMaxReasoningEffort(groupIn.MaxReasoningEffort).
		SetMaxReasoningEffortOverLimit(groupIn.MaxReasoningEffortOverLimit).
		SetReasoningEffortMappings(groupIn.ReasoningEffortMappings)

	// Apply all nullable fallback IDs and model routing as complete state,
	// clearing absent values so removed settings cannot remain active in stored
	// reads.
	if groupIn.FallbackGroupID != nil {
		builder = builder.SetFallbackGroupID(*groupIn.FallbackGroupID)
	} else {
		builder = builder.ClearFallbackGroupID()
	}

	if groupIn.FallbackGroupIDOnInvalidRequest != nil {
		builder = builder.SetFallbackGroupIDOnInvalidRequest(*groupIn.FallbackGroupIDOnInvalidRequest)
	} else {
		builder = builder.ClearFallbackGroupIDOnInvalidRequest()
	}

	if groupIn.UnavailableFallbackGroupID != nil {
		builder = builder.SetUnavailableFallbackGroupID(*groupIn.UnavailableFallbackGroupID)
	} else {
		builder = builder.ClearUnavailableFallbackGroupID()
	}

	if groupIn.ModelRouting != nil {
		builder = builder.SetModelRouting(groupIn.ModelRouting)
	} else {
		builder = builder.ClearModelRouting()
	}

	// Persist the supplied metadata including an empty list, so clearing that
	// administrative value survives subsequent group reads.
	builder = builder.SetSupportedModelScopes(groupIn.SupportedModelScopes)

	updated, err := builder.Save(ctx)
	if dbent.IsNotFound(err) {
		return locale.ErrConflict
	}
	if err != nil {
		return translatePersistenceError(err, routing.ErrGroupNotFound, routing.ErrGroupExists)
	}
	groupIn.UpdatedAt = updated.UpdatedAt
	// Attempt notification using the selected executor after the versioned write.
	// Event errors are logged here; an enclosing transaction still controls
	// whether the updated settings become durable.
	if err := r.options.Enqueue(ctx, sqlq, &groupIn.ID); err != nil {
		logger.LegacyPrintf("repository.group", "[SchedulerOutbox] enqueue group update failed: group=%d err=%v", groupIn.ID, err)
	}
	return nil
}

// Delete invokes the Ent deletion hook and then attempts scheduler
// notification. Normal contexts soft-delete the group; zero affected rows still
// succeed, and notification failures are logged after the database operation.
func (r *GroupStore) Delete(ctx context.Context, id int64) error {
	client := clientFromContext(ctx, r.client)
	sqlq := r.sqlExecutorFromContext(ctx)

	_, err := client.Group.Delete().Where(group.IDEQ(id)).Exec(ctx)
	if err != nil {
		return translatePersistenceError(err, routing.ErrGroupNotFound, nil)
	}
	// Notify after the deletion hook succeeds. This method logs event errors and
	// returns nil, while an enclosing transaction still governs whether deletion
	// becomes durable.
	if err := r.options.Enqueue(ctx, sqlq, &id); err != nil {
		logger.LegacyPrintf("repository.group", "[SchedulerOutbox] enqueue group delete failed: group=%d err=%v", id, err)
	}
	return nil
}

// List reads an unfiltered page of groups with optional provider counts. It
// delegates ordering and pagination to ListWithFilters, which keeps ordinary
// group records when supplementary count loading fails.
func (r *GroupStore) List(ctx context.Context, params pagination.PaginationParams) ([]routing.Group, *pagination.PaginationResult, error) {
	return r.ListWithFilters(ctx, params, "", "", "", nil)
}

// ListWithFilters filters groups by status, case-insensitive text, and optional
// exclusivity before pagination. The platform argument is unused. Ordinary
// pages tolerate provider-count errors; provider-count sorting requires those
// counts and returns their failures.
func (r *GroupStore) ListWithFilters(ctx context.Context, params pagination.PaginationParams, platform, status, search string, isExclusive *bool) ([]routing.Group, *pagination.PaginationResult, error) {
	client := clientFromContext(ctx, r.client)
	q := client.Group.Query()

	if status != "" {
		q = q.Where(group.StatusEQ(status))
	}
	if search != "" {
		q = q.Where(group.Or(
			group.NameContainsFold(search),
			group.DescriptionContainsFold(search),
			group.DisplayBrandContainsFold(search),
		))
	}
	if isExclusive != nil {
		q = q.Where(group.IsExclusiveEQ(*isExclusive))
	}

	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, nil, err
	}

	if strings.EqualFold(strings.TrimSpace(params.SortBy), "provider_count") {
		return r.listWithProviderCountSort(ctx, q, params, total)
	}

	groupsQuery := q.
		Offset(params.Offset()).
		Limit(params.Limit())
	for _, order := range groupListOrder(params) {
		groupsQuery = groupsQuery.Order(order)
	}

	groups, err := groupsQuery.All(ctx)
	if err != nil {
		return nil, nil, err
	}

	groupIDs := make([]int64, 0, len(groups))
	outGroups := make([]routing.Group, 0, len(groups))
	for i := range groups {
		g := GroupFromEnt(groups[i])
		outGroups = append(outGroups, *g)
		groupIDs = append(groupIDs, g.ID)
	}

	// Keep an ordinary filtered page when optional count loading fails; its zero
	// counts do not participate in this page's selected ordering.
	counts, err := r.loadProviderCounts(ctx, groupIDs)
	if err == nil {
		for i := range outGroups {
			c := counts[outGroups[i].ID]
			outGroups[i].ProviderCount = c.Total
			outGroups[i].ActiveProviderCount = c.Active
			outGroups[i].RateLimitedProviderCount = c.RateLimited
		}
	}

	return outGroups, pagination.ResultFromTotal(int64(total), params), nil
}

// listWithProviderCountSort orders the complete filtered group set by provider
// total before selecting a page. Missing count rows behave as zero; requested
// direction governs totals, while configured sort order and ID break ties in
// ascending order. Count-loading failures are returned.
//
// Selecting the page before counting would rank only a previous page rather
// than the full result set. After sorting lightweight entries, the method
// reloads full records for selected IDs and places them in their ranked
// positions. The final query uses the retained client. When ctx selected a
// different transaction client, ranked records can be reloaded outside that
// transaction. Selected IDs missing from the final read leave zero-valued slots
// in the returned page.
func (r *GroupStore) listWithProviderCountSort(ctx context.Context, q *dbent.GroupQuery, params pagination.PaginationParams, total int) ([]routing.Group, *pagination.PaginationResult, error) {
	// Collect every filtered group before ranking provider totals; applying an
	// earlier page limit would omit groups that belong in the requested
	// count-sorted page.
	rows, err := q.Clone().
		Select(group.FieldID, group.FieldSortOrder).
		Order(dbent.Asc(group.FieldSortOrder), dbent.Asc(group.FieldID)).
		All(ctx)
	if err != nil {
		return nil, nil, err
	}

	// sortEntry retains a group's ranking inputs until its full page record is
	// loaded.
	type sortEntry struct {
		id            int64 // group identity tie breaker
		sortOrder     int   // configured order tie breaker
		providerCount int64 // undeleted linked provider total
	}
	entries := make([]sortEntry, 0, len(rows))
	groupIDs := make([]int64, len(rows))
	for i, r := range rows {
		groupIDs[i] = r.ID
		entries = append(entries, sortEntry{id: r.ID, sortOrder: r.SortOrder})
	}

	// Require counts for the full candidate set because silently replacing a query
	// failure with zero would change the page's ranking.
	counts, err := r.loadProviderCounts(ctx, groupIDs)
	if err != nil {
		return nil, nil, err
	}
	for i := range entries {
		c := counts[entries[i].id]
		if c.Total > 0 {
			entries[i].providerCount = c.Total
		}
	}

	// Keep configured order and ID ascending for equal totals, making tied groups
	// retain the same relative order in either count direction.
	sortOrder := params.NormalizedSortOrder(pagination.SortOrderDesc)
	tieCmp := func(a, b sortEntry) bool {
		if a.sortOrder == b.sortOrder {
			return a.id < b.id
		}
		return a.sortOrder < b.sortOrder
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].providerCount == entries[j].providerCount {
			return tieCmp(entries[i], entries[j])
		}
		if sortOrder == pagination.SortOrderAsc {
			return entries[i].providerCount < entries[j].providerCount
		}
		return entries[i].providerCount > entries[j].providerCount
	})

	// Choose ranked IDs before loading full records, then use pageIdx to restore
	// that rank because the final ID query has no ordering clause.
	page := pagination.Slice(entries, params)
	if len(page) == 0 {
		return nil, pagination.ResultFromTotal(int64(total), params), nil
	}

	pageIDs := make([]int64, len(page))
	pageIdx := make(map[int64]int, len(page))
	for i, e := range page {
		pageIDs[i] = e.id
		pageIdx[e.id] = i
	}

	groups, err := r.client.Group.Query().
		Where(group.IDIn(pageIDs...)).
		All(ctx)
	if err != nil {
		return nil, nil, err
	}

	outGroups := make([]routing.Group, len(page))
	for i := range groups {
		g := GroupFromEnt(groups[i])
		c := counts[g.ID]
		g.ProviderCount = c.Total
		g.ActiveProviderCount = c.Active
		g.RateLimitedProviderCount = c.RateLimited
		if idx, ok := pageIdx[g.ID]; ok {
			outGroups[idx] = *g
		}
	}

	return outGroups, pagination.ResultFromTotal(int64(total), params), nil
}

// groupListOrder selects supported group columns and stable ID ties for list
// queries. Empty keys use ascending configured order, unknown keys use
// configured order, and an explicit descending direction applies when a key was
// supplied.
func groupListOrder(params pagination.PaginationParams) []func(*entsql.Selector) {
	sortBy := strings.ToLower(strings.TrimSpace(params.SortBy))
	sortOrder := params.NormalizedSortOrder(pagination.SortOrderAsc)

	var field string
	tieField := group.FieldID
	defaultOrder := true
	switch sortBy {
	case "", "sort_order":
		field = group.FieldSortOrder
	case "name":
		field = group.FieldName
		defaultOrder = false
	case "display_brand":
		field = group.FieldDisplayBrand
		defaultOrder = false
	case "rate_multiplier":
		field = group.FieldRateMultiplier
		defaultOrder = false
	case "is_exclusive":
		field = group.FieldIsExclusive
		defaultOrder = false
	case "session_isolation_enabled":
		field = group.FieldSessionIsolationEnabled
		defaultOrder = false
	case "status":
		field = group.FieldStatus
		defaultOrder = false
	case "created_at":
		field = group.FieldCreatedAt
		defaultOrder = false
	case "id":
		field = group.FieldID
		defaultOrder = false
		tieField = ""
	default:
		field = group.FieldSortOrder
	}

	if sortOrder == pagination.SortOrderDesc && sortBy != "" {
		if tieField == "" {
			return []func(*entsql.Selector){dbent.Desc(field)}
		}
		return []func(*entsql.Selector){dbent.Desc(field), dbent.Desc(tieField)}
	}
	if defaultOrder {
		return []func(*entsql.Selector){dbent.Asc(group.FieldSortOrder), dbent.Asc(group.FieldID)}
	}
	if tieField == "" {
		return []func(*entsql.Selector){dbent.Asc(field)}
	}
	return []func(*entsql.Selector){dbent.Asc(field), dbent.Asc(tieField)}
}

// ListActive reads active groups in configured order and ascending ID ties,
// then attempts provider counts. Count failures leave the successfully loaded
// groups available with zero-valued counters; group-query failures are
// returned.
func (r *GroupStore) ListActive(ctx context.Context) ([]routing.Group, error) {
	client := clientFromContext(ctx, r.client)

	groups, err := client.Group.Query().
		Where(group.StatusEQ(routing.StatusActive)).
		Order(dbent.Asc(group.FieldSortOrder), dbent.Asc(group.FieldID)).
		All(ctx)
	if err != nil {
		return nil, err
	}

	groupIDs := make([]int64, 0, len(groups))
	outGroups := make([]routing.Group, 0, len(groups))
	for i := range groups {
		g := GroupFromEnt(groups[i])
		outGroups = append(outGroups, *g)
		groupIDs = append(groupIDs, g.ID)
	}

	// Retain active group records when their supplementary counts cannot load,
	// allowing callers to use stored configuration with zero-valued display
	// aggregates.
	counts, err := r.loadProviderCounts(ctx, groupIDs)
	if err == nil {
		for i := range outGroups {
			c := counts[outGroups[i].ID]
			outGroups[i].ProviderCount = c.Total
			outGroups[i].ActiveProviderCount = c.Active
			outGroups[i].RateLimitedProviderCount = c.RateLimited
		}
	}

	return outGroups, nil
}

// ListActiveIDs reads active undeleted group IDs in configured order and
// ascending ID ties. It uses the default SQL executor when configured,
// otherwise the retained Ent client; context transactions do not replace either
// path.
func (r *GroupStore) ListActiveIDs(ctx context.Context) ([]int64, error) {
	if r.sql != nil {
		rows, err := r.sql.QueryContext(ctx, `
			SELECT id
			FROM groups
			WHERE status = $1
			  AND deleted_at IS NULL
			ORDER BY sort_order ASC, id ASC
		`, routing.StatusActive)
		if err != nil {
			return nil, err
		}
		defer func() { _ = rows.Close() }()

		ids := make([]int64, 0)
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				return nil, err
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return ids, nil
	}

	groups, err := r.client.Group.Query().
		Where(group.StatusEQ(routing.StatusActive)).
		Select(group.FieldID).
		Order(dbent.Asc(group.FieldSortOrder), dbent.Asc(group.FieldID)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(groups))
	for i := range groups {
		ids = append(ids, groups[i].ID)
	}
	return ids, nil
}

// ExistsByName reports whether the selected Ent client finds an exact group
// name.
func (r *GroupStore) ExistsByName(ctx context.Context, name string) (bool, error) {
	client := clientFromContext(ctx, r.client)
	return client.Group.Query().Where(group.NameEQ(name)).Exist(ctx)
}

// ExistsByIDs checks undeleted groups using the context transaction when
// present. Each unique positive input ID has a boolean entry, including false
// for missing groups; nonpositive IDs are skipped, and empty valid input avoids
// SQL.
func (r *GroupStore) ExistsByIDs(ctx context.Context, ids []int64) (map[int64]bool, error) {
	sqlq := r.sqlExecutorFromContext(ctx)
	result := make(map[int64]bool, len(ids))
	if len(ids) == 0 {
		return result, nil
	}

	uniqueIDs := make([]int64, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		uniqueIDs = append(uniqueIDs, id)
		result[id] = false
	}
	if len(uniqueIDs) == 0 {
		return result, nil
	}

	rows, err := sqlq.QueryContext(ctx, `
		SELECT id
		FROM groups
		WHERE id = ANY($1) AND deleted_at IS NULL
	`, pq.Array(uniqueIDs))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// GetProviderCount returns undeleted membership totals and currently available
// provider totals for a group. Groups without memberships produce zero counts;
// scheduling eligibility uses database time, and query failures are returned.
func (r *GroupStore) GetProviderCount(ctx context.Context, groupID int64) (total int64, active int64, err error) {
	sqlq := r.sqlExecutorFromContext(ctx)
	var rateLimited int64
	err = postgresinfra.ScanSingleRow(ctx, sqlq,
		fmt.Sprintf(`SELECT
			COUNT(*) FILTER (WHERE a.deleted_at IS NULL),
			COUNT(*) FILTER (WHERE %s),
			COUNT(*) FILTER (WHERE %s)
		FROM provider_groups ag JOIN providers a ON a.id = ag.provider_id
		WHERE ag.group_id = $1`, groupProviderAvailableSQL, groupProviderTemporarilyLimitedSQL),
		[]any{groupID}, &total, &active, &rateLimited)
	return
}

// DeleteProviderGroupsByGroupID clears a group's provider memberships on the
// selected executor and returns affected-row metadata. Metadata errors are
// ignored, and scheduler-notification failures are logged after clearing;
// transaction completion belongs to the caller.
func (r *GroupStore) DeleteProviderGroupsByGroupID(ctx context.Context, groupID int64) (int64, error) {
	sqlq := r.sqlExecutorFromContext(ctx)

	res, err := r.options.Providers(sqlq).Clear(ctx, groupID)
	if err != nil {
		return 0, err
	}
	// Treat the affected count as optional metadata for the caller; ignoring its
	// error keeps a successful membership clear from becoming an operation
	// failure.
	affected, _ := res.RowsAffected()
	// Notify after clearing memberships. Event errors are logged while this
	// method returns the clear's affected count; an enclosing transaction still
	// controls the durability of both writes.
	if err := r.options.Enqueue(ctx, sqlq, &groupID); err != nil {
		logger.LegacyPrintf("repository.group", "[SchedulerOutbox] enqueue group provider clear failed: group=%d err=%v", groupID, err)
	}
	return affected, nil
}

// DeleteCascade removes group permissions and provider memberships before
// invoking the Ent deletion hook on a locked group. It starts a transaction
// through the retained client or reuses that client's existing transaction. A
// missing locked group returns ErrGroupNotFound; successful calls return a nil
// user-ID slice.
//
// The row lock and participant writes use the same transaction client, so a
// later failure can roll back permission and membership removal with group
// deletion. Normal contexts soft-delete the row. A newly owned transaction
// commits before scheduler notification, whose failure is logged; an already
// transactional retained client leaves completion to its caller. Notification
// uses the retained SQL executor even when the group client is transactional.
func (r *GroupStore) DeleteCascade(ctx context.Context, id int64) ([]int64, error) {
	g, err := r.client.Group.Query().Where(group.IDEQ(id)).Only(ctx)
	if err != nil {
		return nil, translatePersistenceError(err, routing.ErrGroupNotFound, nil)
	}
	_ = GroupFromEnt(g)

	// Use one transaction client for the group lock and both participant deletes;
	// a later delete failure then rolls back their preceding writes.
	tx, err := r.client.Tx(ctx)
	if err != nil && !errors.Is(err, dbent.ErrTxStarted) {
		return nil, err
	}
	exec := r.client
	txClient := r.client
	if err == nil {
		defer func() { _ = tx.Rollback() }()
		exec = tx.Client()
		txClient = exec
	}
	// When the retained client already has a transaction, keep that client for all
	// cascade writes and leave completion to its enclosing owner.

	// Lock the undeleted group before removing its permissions and memberships,
	// serializing these writes with concurrent updates and rejecting a group
	// deleted after the initial read.
	rows, err := exec.QueryContext(ctx, "SELECT id FROM groups WHERE id = $1 AND deleted_at IS NULL FOR UPDATE", id)
	if err != nil {
		return nil, err
	}
	var lockedID int64
	if rows.Next() {
		if err := rows.Scan(&lockedID); err != nil {
			_ = rows.Close()
			return nil, err
		}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if lockedID == 0 {
		return nil, routing.ErrGroupNotFound
	}

	var affectedUserIDs []int64

	if err := r.options.Users(exec).Delete(ctx, id); err != nil {
		return nil, err
	}
	if _, err := r.options.Providers(exec).Clear(ctx, id); err != nil {
		return nil, err
	}

	// Invoke deletion after both participant clears on the transaction client, so
	// the normal soft-delete hook retains the row with its removed associations.
	if _, err := txClient.Group.Delete().Where(group.IDEQ(id)).Exec(ctx); err != nil {
		return nil, err
	}

	// Complete an owned cascade transaction before the separate notification
	// attempt, so observers can read the committed permission and membership
	// removals.
	if tx != nil {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
	}
	// Attempt notification through the default executor after any owned commit;
	// failures are logged, leaving later scheduler rebuilding to observe the
	// removed group.
	if err := r.options.Enqueue(ctx, r.sql, &id); err != nil {
		logger.LegacyPrintf("repository.group", "[SchedulerOutbox] enqueue group cascade delete failed: group=%d err=%v", id, err)
	}

	return affectedUserIDs, nil
}

// loadProviderCounts groups membership totals and current scheduling
// classifications for supplied groups. Empty IDs return an empty map; scan,
// iteration, or close failures discard the map, and groups without membership
// rows remain absent.
func (r *GroupStore) loadProviderCounts(ctx context.Context, groupIDs []int64) (counts map[int64]groupProviderCounts, err error) {
	sqlq := r.sqlExecutorFromContext(ctx)
	counts = make(map[int64]groupProviderCounts, len(groupIDs))
	if len(groupIDs) == 0 {
		return counts, nil
	}

	rows, err := sqlq.QueryContext(
		ctx,
		fmt.Sprintf(`SELECT ag.group_id,
			COUNT(*) FILTER (WHERE a.deleted_at IS NULL) AS total,
			COUNT(*) FILTER (WHERE %s) AS active,
			COUNT(*) FILTER (WHERE %s) AS rate_limited
		FROM provider_groups ag
		JOIN providers a ON a.id = ag.provider_id
		WHERE ag.group_id = ANY($1)
		GROUP BY ag.group_id`, groupProviderAvailableSQL, groupProviderTemporarilyLimitedSQL),
		pq.Array(groupIDs),
	)
	if err != nil {
		return nil, err
	}
	// Treat a close failure as a failed count read when no earlier error exists,
	// discarding accumulated counts rather than returning uncertain aggregates.
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
			counts = nil
		}
	}()

	for rows.Next() {
		var groupID int64
		var c groupProviderCounts
		if err = rows.Scan(&groupID, &c.Total, &c.Active, &c.RateLimited); err != nil {
			return nil, err
		}
		counts[groupID] = c
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}

	return counts, nil
}

// GetProviderIDsByGroupIDs reads distinct provider IDs linked to any supplied
// group in ascending order. Empty input returns nil without SQL; membership
// links are read as stored without provider status or deletion filtering.
func (r *GroupStore) GetProviderIDsByGroupIDs(ctx context.Context, groupIDs []int64) ([]int64, error) {
	sqlq := r.sqlExecutorFromContext(ctx)
	if len(groupIDs) == 0 {
		return nil, nil
	}

	rows, err := sqlq.QueryContext(
		ctx,
		"SELECT DISTINCT provider_id FROM provider_groups WHERE group_id = ANY($1) ORDER BY provider_id",
		pq.Array(groupIDs),
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var providerIDs []int64
	for rows.Next() {
		var providerID int64
		if err := rows.Scan(&providerID); err != nil {
			return nil, err
		}
		providerIDs = append(providerIDs, providerID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return providerIDs, nil
}

// BindProvidersToGroup adds memberships through the selected participant,
// retaining existing pairs. Empty input skips mutation and notification;
// provider validation belongs to the caller, and scheduler-event errors are
// logged after the binding succeeds.
func (r *GroupStore) BindProvidersToGroup(ctx context.Context, groupID int64, providerIDs []int64) error {
	sqlq := r.sqlExecutorFromContext(ctx)
	if len(providerIDs) == 0 {
		return nil
	}

	err := r.options.Providers(sqlq).Bind(ctx, groupID, providerIDs)
	if err != nil {
		return err
	}

	// Return the binding result after logging notification errors. A supplied
	// transaction still belongs to its enclosing owner, which decides whether
	// those membership changes commit.
	if err := r.options.Enqueue(ctx, sqlq, &groupID); err != nil {
		logger.LegacyPrintf("repository.group", "[SchedulerOutbox] enqueue bind providers to group failed: group=%d err=%v", groupID, err)
	}

	return nil
}

// UpdateSortOrders writes the final supplied order for each unique positive
// group ID using one CASE update. Nonpositive IDs are skipped; empty valid
// input returns success. Missing undeleted groups cause ErrGroupNotFound before
// the write or when the affected count differs.
//
// Repeated IDs collapse before validation so the existence count compares
// distinct groups and each ID has one CASE branch. The update uses the context
// transaction when present, followed by one best-effort scheduling event per
// group. Callers needing validation, writes, and events in one commit supply
// that transaction; an error after a pool-backed write does not roll it back.
func (r *GroupStore) UpdateSortOrders(ctx context.Context, updates []routing.GroupSortOrderUpdate) error {
	sqlq := r.sqlExecutorFromContext(ctx)
	if len(updates) == 0 {
		return nil
	}

	// Collapse repeated IDs before the existence check so one group contributes
	// one expected row and one CASE branch using its last supplied order.
	sortOrderByID := make(map[int64]int, len(updates))
	groupIDs := make([]int64, 0, len(updates))
	for _, u := range updates {
		if u.ID <= 0 {
			continue
		}
		if _, exists := sortOrderByID[u.ID]; !exists {
			groupIDs = append(groupIDs, u.ID)
		}
		sortOrderByID[u.ID] = u.SortOrder
	}
	if len(groupIDs) == 0 {
		return nil
	}

	// Check the complete distinct set before writing, so a missing group is
	// reported before any CASE assignment on this executor.
	var existingCount int
	if err := postgresinfra.ScanSingleRow(
		ctx,
		sqlq,
		`SELECT COUNT(*) FROM groups WHERE deleted_at IS NULL AND id = ANY($1)`,
		[]any{pq.Array(groupIDs)},
		&existingCount,
	); err != nil {
		return err
	}
	if existingCount != len(groupIDs) {
		return routing.ErrGroupNotFound
	}

	args := make([]any, 0, len(groupIDs)*2+1)
	caseClauses := make([]string, 0, len(groupIDs))
	placeholder := 1
	for _, id := range groupIDs {
		caseClauses = append(caseClauses, fmt.Sprintf("WHEN $%d THEN $%d", placeholder, placeholder+1))
		args = append(args, id, sortOrderByID[id])
		placeholder += 2
	}
	args = append(args, pq.Array(groupIDs))

	query := fmt.Sprintf(`
		UPDATE groups
		SET sort_order = CASE id
			%s
			ELSE sort_order
		END
		WHERE deleted_at IS NULL AND id = ANY($%d)
	`, strings.Join(caseClauses, "\n\t\t\t"), placeholder)

	result, err := sqlq.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != int64(len(groupIDs)) {
		return routing.ErrGroupNotFound
	}

	// Notify each updated group after the batch write. An event failure is logged
	// for that group and leaves the other notification attempts available; any
	// enclosing transaction still needs completion.
	for _, id := range groupIDs {
		if err := r.options.Enqueue(ctx, sqlq, &id); err != nil {
			logger.LegacyPrintf("repository.group", "[SchedulerOutbox] enqueue group sort update failed: group=%d err=%v", id, err)
		}
	}
	return nil
}

// clientFromContext selects the context transaction's Ent client or the
// supplied default. Group operations borrow that client for persistence and
// leave transaction completion and connection lifetime with its owner.
func clientFromContext(ctx context.Context, defaultClient *dbent.Client) *dbent.Client {
	if tx := dbent.TxFromContext(ctx); tx != nil {
		return tx.Client()
	}
	return defaultClient
}

// translatePersistenceError attaches database causes to supplied not-found or
// unique-conflict errors. Nil errors return nil, and unmapped failures pass
// through; callers select which routing error applies to each persistence
// operation.
func translatePersistenceError(err error, notFound, conflict *infraerrors.ApplicationError) error {
	if err == nil {
		return nil
	}

	// Map either entity lookup misses or SQL missing-row errors to the supplied
	// routing error, retaining the database cause for diagnosis.
	if notFound != nil && (errors.Is(err, sql.ErrNoRows) || dbent.IsNotFound(err)) {
		return notFound.WithCause(err)
	}

	if conflict != nil && postgresinfra.IsUniqueConstraintViolation(err) {
		return conflict.WithCause(err)
	}

	return err
}
