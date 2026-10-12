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

const (
	// The group's available-provider count uses the same eligibility filters as ListSchedulableByGroupID.
	groupProviderAvailableSQL = `a.deleted_at IS NULL
				AND a.status = 'active'
				AND a.schedulable = true
				AND (a.expires_at IS NULL OR a.expires_at > NOW() OR a.auto_pause_on_expired = FALSE)
				AND (a.rate_limit_reset_at IS NULL OR a.rate_limit_reset_at <= NOW())
				AND (a.overload_until IS NULL OR a.overload_until <= NOW())
				AND (a.temp_unschedulable_until IS NULL OR a.temp_unschedulable_until <= NOW())`

	// groupProviderTemporarilyLimitedSQL counts providers inside temporary scheduling-block windows.
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

type GroupLinkParticipant interface {
	Clear(context.Context, int64) (sql.Result, error)
	Bind(context.Context, int64, []int64) error
	Copy(context.Context, int64, int64, bool) (sql.Result, error)
}

type GroupAccessParticipant interface {
	Delete(context.Context, int64) error
}

type GroupStoreOptions struct {
	Providers func(postgresinfra.Executor) GroupLinkParticipant
	Users     func(postgresinfra.Executor) GroupAccessParticipant
	Enqueue   func(context.Context, postgresinfra.Executor, *int64) error
}

type GroupStore struct {
	options GroupStoreOptions
	client  *dbent.Client
	sql     postgresinfra.Executor
}

type groupProviderCounts struct {
	Total       int64
	Active      int64
	RateLimited int64
}

func NewGroupStore(client *dbent.Client, db postgresinfra.Executor, options GroupStoreOptions) *GroupStore {
	return &GroupStore{client: client, sql: db, options: options}
}

// Mutate runs group and association changes in an Ent transaction, reusing one already in ctx.
func (r *GroupStore) Mutate(ctx context.Context, fn func(context.Context) error) error {
	if dbent.TxFromContext(ctx) != nil || r.client == nil {
		return fn(ctx)
	}

	tx, err := r.client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin group mutation transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	txCtx := dbent.NewTxContext(ctx, tx)
	if err := fn(txCtx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit group mutation transaction: %w", err)
	}
	return nil
}

func (r *GroupStore) sqlExecutorFromContext(ctx context.Context) postgresinfra.Executor {
	if tx := dbent.TxFromContext(ctx); tx != nil {
		return tx.Client()
	}
	return r.sql
}

// LockGroupSortOrder serializes tail-sort reads and group creation within the current transaction.
func (r *GroupStore) LockGroupSortOrder(ctx context.Context) error {
	if dbent.TxFromContext(ctx) == nil {
		return errors.New("group sort order lock requires transaction")
	}
	sqlq := r.sqlExecutorFromContext(ctx)
	if sqlq == nil {
		return errors.New("sql executor is not configured")
	}
	_, err := sqlq.ExecContext(
		ctx,
		"SELECT pg_advisory_xact_lock(hashtext($1))",
		"tokenrouter_group_sort_order",
	)
	return err
}

func (r *GroupStore) Create(ctx context.Context, groupIn *routing.Group) error {
	client := clientFromContext(ctx, r.client)
	sqlq := r.sqlExecutorFromContext(ctx)
	if err := createGroupRecord(ctx, client, groupIn); err != nil {
		return err
	}
	if err := r.options.Enqueue(ctx, sqlq, &groupIn.ID); err != nil {
		logger.LegacyPrintf("repository.group", "[SchedulerOutbox] enqueue group create failed: group=%d err=%v", groupIn.ID, err)
	}
	return nil
}

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

	// Persist the legacy model-family metadata, including an empty list.
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

func (r *GroupStore) FindByDuplicateOperationID(ctx context.Context, operationID string) (*routing.Group, error) {
	operationID = strings.TrimSpace(operationID)
	if operationID == "" {
		return nil, nil
	}
	row, err := r.client.Group.Query().
		Where(group.DuplicateOperationIDEQ(operationID)).
		Order(dbent.Asc(group.FieldID)).
		First(ctx)
	if dbent.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find group duplicate operation: %w", err)
	}
	return GroupFromEnt(row), nil
}

// CreateFromSource saves a duplicate group, source provider bindings, and its scheduling event in one transaction.
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
		defer func() { _ = tx.Rollback() }()
		txClient = tx.Client()
		txCtx = dbent.NewTxContext(ctx, tx)
	}

	if err := createGroupRecord(txCtx, txClient, groupIn); err != nil {
		return err
	}
	result, err := r.options.Providers(txClient).Copy(txCtx, groupIn.ID, sourceGroupID, groupIn.RequireOAuthOnly)
	if err != nil {
		return err
	}
	if count, countErr := result.RowsAffected(); countErr == nil {
		groupIn.ProviderCount = count
	}
	if err := r.options.Enqueue(txCtx, txClient, &groupIn.ID); err != nil {
		return err
	}

	if ownsTx {
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func (r *GroupStore) GetByID(ctx context.Context, id int64) (*routing.Group, error) {
	out, err := r.GetByIDLite(ctx, id)
	if err != nil {
		return nil, err
	}
	counts, err := r.loadProviderCounts(ctx, []int64{out.ID})
	if err == nil {
		c := counts[out.ID]
		out.ProviderCount = c.Total
		out.ActiveProviderCount = c.Active
		out.RateLimitedProviderCount = c.RateLimited
	}
	return out, nil
}

func (r *GroupStore) GetByIDLite(ctx context.Context, id int64) (*routing.Group, error) {
	client := clientFromContext(ctx, r.client)

	// Use GetByID when provider counts are needed.
	m, err := client.Group.Query().
		Where(group.IDEQ(id)).
		Only(ctx)
	if err != nil {
		return nil, translatePersistenceError(err, routing.ErrGroupNotFound, nil)
	}
	return GroupFromEnt(m), nil
}

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

	// Nil clears the stored fallback; this method receives the complete group state.
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

	// Persist the legacy model-family metadata, including an empty list.
	builder = builder.SetSupportedModelScopes(groupIn.SupportedModelScopes)

	updated, err := builder.Save(ctx)
	if dbent.IsNotFound(err) {
		return locale.ErrConflict
	}
	if err != nil {
		return translatePersistenceError(err, routing.ErrGroupNotFound, routing.ErrGroupExists)
	}
	groupIn.UpdatedAt = updated.UpdatedAt
	if err := r.options.Enqueue(ctx, sqlq, &groupIn.ID); err != nil {
		logger.LegacyPrintf("repository.group", "[SchedulerOutbox] enqueue group update failed: group=%d err=%v", groupIn.ID, err)
	}
	return nil
}

func (r *GroupStore) Delete(ctx context.Context, id int64) error {
	client := clientFromContext(ctx, r.client)
	sqlq := r.sqlExecutorFromContext(ctx)

	_, err := client.Group.Delete().Where(group.IDEQ(id)).Exec(ctx)
	if err != nil {
		return translatePersistenceError(err, routing.ErrGroupNotFound, nil)
	}
	if err := r.options.Enqueue(ctx, sqlq, &id); err != nil {
		logger.LegacyPrintf("repository.group", "[SchedulerOutbox] enqueue group delete failed: group=%d err=%v", id, err)
	}
	return nil
}

func (r *GroupStore) List(ctx context.Context, params pagination.PaginationParams) ([]routing.Group, *pagination.PaginationResult, error) {
	return r.ListWithFilters(ctx, params, "", "", "", nil)
}

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

func (r *GroupStore) listWithProviderCountSort(ctx context.Context, q *dbent.GroupQuery, params pagination.PaginationParams, total int) ([]routing.Group, *pagination.PaginationResult, error) {
	// Load IDs and sort order for the full result set before sorting by provider count.
	rows, err := q.Clone().
		Select(group.FieldID, group.FieldSortOrder).
		Order(dbent.Asc(group.FieldSortOrder), dbent.Asc(group.FieldID)).
		All(ctx)
	if err != nil {
		return nil, nil, err
	}

	type sortEntry struct {
		id            int64
		sortOrder     int
		providerCount int64
	}
	entries := make([]sortEntry, 0, len(rows))
	groupIDs := make([]int64, len(rows))
	for i, r := range rows {
		groupIDs[i] = r.ID
		entries = append(entries, sortEntry{id: r.ID, sortOrder: r.SortOrder})
	}

	// Fetch counts in one query for all candidate groups.
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

	// Sort the full set before pagination; configured sort order and ID break count ties.
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

	// Load full group records only for the selected page.
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

// ListActiveIDs loads active group IDs for queries such as capacity aggregation.
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

func (r *GroupStore) ExistsByName(ctx context.Context, name string) (bool, error) {
	client := clientFromContext(ctx, r.client)
	return client.Group.Query().Where(group.NameEQ(name)).Exist(ctx)
}

// ExistsByIDs checks non-deleted groups in a batch.
// Missing IDs have no entry in the returned map.
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

func (r *GroupStore) DeleteProviderGroupsByGroupID(ctx context.Context, groupID int64) (int64, error) {
	sqlq := r.sqlExecutorFromContext(ctx)

	res, err := r.options.Providers(sqlq).Clear(ctx, groupID)
	if err != nil {
		return 0, err
	}
	affected, _ := res.RowsAffected()
	if err := r.options.Enqueue(ctx, sqlq, &groupID); err != nil {
		logger.LegacyPrintf("repository.group", "[SchedulerOutbox] enqueue group provider clear failed: group=%d err=%v", groupID, err)
	}
	return affected, nil
}

func (r *GroupStore) DeleteCascade(ctx context.Context, id int64) ([]int64, error) {
	g, err := r.client.Group.Query().Where(group.IDEQ(id)).Only(ctx)
	if err != nil {
		return nil, translatePersistenceError(err, routing.ErrGroupNotFound, nil)
	}
	_ = GroupFromEnt(g)

	// Use an Ent transaction to commit the cascade together.
	// Ent's transaction driver must also back the client used by the cascade.
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
	// ErrTxStarted means the current client already participates in the transaction.

	// Lock the group row to serialize the cascade with concurrent writes.
	// Read through the transaction client to distinguish a missing group from a query failure.
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

	// The Ent delete hook soft-deletes the group.
	if _, err := txClient.Group.Delete().Where(group.IDEQ(id)).Exec(ctx); err != nil {
		return nil, err
	}

	if tx != nil {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
	}
	if err := r.options.Enqueue(ctx, r.sql, &id); err != nil {
		logger.LegacyPrintf("repository.group", "[SchedulerOutbox] enqueue group cascade delete failed: group=%d err=%v", id, err)
	}

	return affectedUserIDs, nil
}

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

// GetProviderIDsByGroupIDs returns distinct provider IDs across the supplied groups.
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

// BindProvidersToGroup bulk-binds providers, ignoring existing associations.
func (r *GroupStore) BindProvidersToGroup(ctx context.Context, groupID int64, providerIDs []int64) error {
	sqlq := r.sqlExecutorFromContext(ctx)
	if len(providerIDs) == 0 {
		return nil
	}

	err := r.options.Providers(sqlq).Bind(ctx, groupID, providerIDs)
	if err != nil {
		return err
	}

	// Failure to enqueue is logged after the binding succeeds.
	if err := r.options.Enqueue(ctx, sqlq, &groupID); err != nil {
		logger.LegacyPrintf("repository.group", "[SchedulerOutbox] enqueue bind providers to group failed: group=%d err=%v", groupID, err)
	}

	return nil
}

// UpdateSortOrders updates group sort order in a batch.
func (r *GroupStore) UpdateSortOrders(ctx context.Context, updates []routing.GroupSortOrderUpdate) error {
	sqlq := r.sqlExecutorFromContext(ctx)
	if len(updates) == 0 {
		return nil
	}

	// For repeated group IDs, the last supplied sort order wins.
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

	// Reject the update if any requested group is missing or deleted.
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

	for _, id := range groupIDs {
		if err := r.options.Enqueue(ctx, sqlq, &id); err != nil {
			logger.LegacyPrintf("repository.group", "[SchedulerOutbox] enqueue group sort update failed: group=%d err=%v", id, err)
		}
	}
	return nil
}

// clientFromContext returns the current transaction client or the supplied default.
func clientFromContext(ctx context.Context, defaultClient *dbent.Client) *dbent.Client {
	if tx := dbent.TxFromContext(ctx); tx != nil {
		return tx.Client()
	}
	return defaultClient
}

// translatePersistenceError attaches database causes to the supplied not-found or conflict errors.
// Unmatched errors, or errors with no supplied mapping, pass through unchanged.
func translatePersistenceError(err error, notFound, conflict *infraerrors.ApplicationError) error {
	if err == nil {
		return nil
	}

	// Ent and database/sql missing-row errors map to the same domain error.
	if notFound != nil && (errors.Is(err, sql.ErrNoRows) || dbent.IsNotFound(err)) {
		return notFound.WithCause(err)
	}

	if conflict != nil && postgresinfra.IsUniqueConstraintViolation(err) {
		return conflict.WithCause(err)
	}

	return err
}
