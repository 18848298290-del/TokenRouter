package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/apikey"
	"github.com/TokenFlux/TokenRouter/ent/apikeycompositegroup"
	"github.com/TokenFlux/TokenRouter/ent/group"
	"github.com/TokenFlux/TokenRouter/ent/schema/mixins"
	"github.com/TokenFlux/TokenRouter/ent/user"
	keycore "github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	billingpostgres "github.com/TokenFlux/TokenRouter/internal/billing/postgres"
	postgresinfra "github.com/TokenFlux/TokenRouter/internal/infra/postgres"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	usagequery "github.com/TokenFlux/TokenRouter/internal/usage/postgres/query"
)

type KeyStore struct {
	client      *dbent.Client
	sql         SQLExecutor
	usageTotals UsageTotalsReader
}

// UsageTotalsReader loads usage totals by key ID for sorting.
type UsageTotalsReader func(context.Context, []int64) (map[int64]float64, error)

func NewKeyStore(client *dbent.Client, sqlDB *sql.DB, usage UsageTotalsReader) *KeyStore {
	return NewKeyStoreWithSQL(client, sqlDB, usage)
}

func NewKeyStoreWithSQL(client *dbent.Client, sqlq SQLExecutor, usage UsageTotalsReader) *KeyStore {
	return &KeyStore{client: client, sql: sqlq, usageTotals: usage}
}

func (r *KeyStore) KeyActiveQuery() *dbent.APIKeyQuery {
	// Exclude soft-deleted keys from ordinary reads.
	return r.client.APIKey.Query().Where(apikey.DeletedAtIsNil())
}

// KeyApiKeyFastModePolicyForPersistence supplies a valid default for callers that bypass service validation.
func KeyApiKeyFastModePolicyForPersistence(value string) string {
	policy, ok := keycore.NormalizeAPIKeyFastModePolicy(value)
	if ok {
		return policy
	}
	return value
}

// KeyApiKeyBillingModeForPersistence supplies a valid default for callers that bypass service validation.
func KeyApiKeyBillingModeForPersistence(value string) string {
	mode, ok := keycore.NormalizeAPIKeyBillingMode(value)
	if ok {
		return mode
	}
	return value
}

func (r *KeyStore) Create(ctx context.Context, key *keycore.APIKey) error {
	if key == nil {
		return fmt.Errorf("api key is required")
	}

	// Count and insert in one transaction; the PostgreSQL user-row lock serializes creation for that user.
	txClient, sqlq, commit, rollback, err := r.KeyBeginAPIKeyCreateTransaction(ctx)
	if err != nil {
		return err
	}
	defer rollback()

	current, limit, err := r.KeyLockAPIKeyOwnerAndCount(ctx, sqlq, key.UserID)
	if err != nil {
		return err
	}
	if limit > 0 && current >= int64(limit) {
		return keycore.NewAPIKeyLimitReachedError(current, limit)
	}

	created, err := KeyCreateAPIKeyRecord(ctx, txClient, key)
	if err != nil {
		return translatePersistenceError(err, nil, keycore.ErrAPIKeyExists)
	}
	if err := commit(); err != nil {
		return err
	}

	key.ID = created.ID
	key.LastUsedAt = created.LastUsedAt
	key.CreatedAt = created.CreatedAt
	key.UpdatedAt = created.UpdatedAt
	return nil
}

// KeyBeginAPIKeyCreateTransaction returns the Ent client, SQL executor, and transaction cleanup functions.
func (r *KeyStore) KeyBeginAPIKeyCreateTransaction(ctx context.Context) (*dbent.Client, SQLExecutor, func() error, func(), error) {
	if existing := dbent.TxFromContext(ctx); existing != nil {
		client := existing.Client()
		return client, client, func() error { return nil }, func() {}, nil
	}

	tx, err := r.client.Tx(ctx)
	if err == nil {
		client := tx.Client()
		return client, client, tx.Commit, func() { _ = tx.Rollback() }, nil
	}
	if !errors.Is(err, dbent.ErrTxStarted) {
		return nil, nil, nil, nil, err
	}

	// A store already bound to a caller transaction leaves commit and rollback to that caller.
	if r.sql == nil {
		return nil, nil, nil, nil, fmt.Errorf("sql executor is not configured")
	}
	return r.client, r.sql, func() error { return nil }, func() {}, nil
}

// KeyLockAPIKeyOwnerAndCount locks the user on PostgreSQL and counts keys that are not soft-deleted.
func (r *KeyStore) KeyLockAPIKeyOwnerAndCount(ctx context.Context, sqlq SQLExecutor, userID int64) (int64, int, error) {
	lockQuery := `SELECT api_key_limit FROM users WHERE id = $1 AND deleted_at IS NULL`
	if r.client.Driver().Dialect() == dialect.Postgres {
		lockQuery += ` FOR UPDATE`
	}

	var limit int
	if err := scanSingleRow(ctx, sqlq, lockQuery, []any{userID}, &limit); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, 0, keycore.ErrUserNotFound
		}
		return 0, 0, err
	}

	var current int64
	const countQuery = `SELECT COUNT(*) FROM api_keys WHERE user_id = $1 AND deleted_at IS NULL`
	if err := scanSingleRow(ctx, sqlq, countQuery, []any{userID}, &current); err != nil {
		return 0, 0, err
	}
	return current, limit, nil
}

// KeyCreateAPIKeyRecord inserts a key using the supplied transaction client.
func KeyCreateAPIKeyRecord(ctx context.Context, client *dbent.Client, key *keycore.APIKey) (*dbent.APIKey, error) {
	builder := client.APIKey.Create().
		SetUserID(key.UserID).
		SetNillableTeamID(key.TeamID).
		SetKey(key.Key).
		SetName(key.Name).
		SetStatus(key.Status).
		SetIsComposite(key.IsComposite).
		SetFastModePolicy(KeyApiKeyFastModePolicyForPersistence(key.FastModePolicy)).
		SetBillingMode(KeyApiKeyBillingModeForPersistence(key.BillingMode)).
		SetNillablePreferredSubscriptionID(key.PreferredSubscriptionID).
		SetModelMapping(keycore.CloneModelMapping(key.ModelMapping)).
		SetNillableGroupID(key.GroupID).
		SetNillableLastUsedAt(key.LastUsedAt).
		SetNillableManagedBy(key.ManagedBy).
		SetQuota(key.Quota).
		SetQuotaUsed(key.QuotaUsed).
		SetNillableExpiresAt(key.ExpiresAt).
		SetConcurrencyLimit(key.ConcurrencyLimit).
		SetRpmLimit(key.RPMLimit).
		SetRateLimit5h(key.RateLimit5h).
		SetRateLimit1d(key.RateLimit1d).
		SetRateLimit7d(key.RateLimit7d).
		SetFallbackWhenGroupUnavailable(key.FallbackWhenGroupUnavailable)

	if len(key.IPWhitelist) > 0 {
		builder.SetIPWhitelist(key.IPWhitelist)
	}
	if len(key.IPBlacklist) > 0 {
		builder.SetIPBlacklist(key.IPBlacklist)
	}

	created, err := builder.Save(ctx)
	if err != nil {
		return nil, err
	}
	if err := KeyReplaceCompositeGroupRecords(ctx, client, created.ID, key.CompositeGroups); err != nil {
		return nil, err
	}
	return created, nil
}

// KeyReplaceCompositeGroupRecords replaces all mappings within the caller's transaction.
func KeyReplaceCompositeGroupRecords(ctx context.Context, client *dbent.Client, apiKeyID int64, bindings []keycore.APIKeyCompositeGroup) error {
	if _, err := client.APIKeyCompositeGroup.Delete().
		Where(apikeycompositegroup.APIKeyIDEQ(apiKeyID)).
		Exec(ctx); err != nil {
		return err
	}
	if len(bindings) == 0 {
		return nil
	}
	builders := make([]*dbent.APIKeyCompositeGroupCreate, 0, len(bindings))
	for _, binding := range bindings {
		builders = append(builders, client.APIKeyCompositeGroup.Create().
			SetAPIKeyID(apiKeyID).
			SetGroupID(binding.GroupID).
			SetPrefix(binding.Prefix).
			SetNormalizedPrefix(binding.NormalizedPrefix).
			SetSortOrder(binding.SortOrder))
	}
	return client.APIKeyCompositeGroup.CreateBulk(builders...).Exec(ctx)
}

// KeyWithAPIKeyCompositeGroups loads mappings and groups in user-configured order.
func KeyWithAPIKeyCompositeGroups(query *dbent.APIKeyQuery) *dbent.APIKeyQuery {
	return query.WithCompositeGroups(func(q *dbent.APIKeyCompositeGroupQuery) {
		q.Order(dbent.Asc(apikeycompositegroup.FieldSortOrder), dbent.Asc(apikeycompositegroup.FieldID)).
			WithGroup()
	})
}

func (r *KeyStore) GetByID(ctx context.Context, id int64) (*keycore.APIKey, error) {
	m, err := KeyWithAPIKeyCompositeGroups(r.KeyActiveQuery()).
		Where(apikey.IDEQ(id)).
		WithUser().
		WithGroup().
		Only(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			return nil, keycore.ErrAPIKeyNotFound
		}
		return nil, err
	}
	return KeyApiKeyEntityToService(m), nil
}

// GetManagedKeyByUserAndGroup finds a hidden managed key for the user, group, and manager.
func (r *KeyStore) GetManagedKeyByUserAndGroup(ctx context.Context, userID, groupID int64, managedBy string) (*keycore.APIKey, error) {
	m, err := r.KeyActiveQuery().
		Where(
			apikey.UserIDEQ(userID),
			apikey.GroupIDEQ(groupID),
			apikey.ManagedByEQ(managedBy),
		).
		WithGroup().
		Only(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			return nil, keycore.ErrAPIKeyNotFound
		}
		return nil, err
	}
	return KeyApiKeyEntityToService(m), nil
}

// CreateManagedKey uses the normal creation path, including the key count limit.
// The caller sets key.ManagedBy to identify the manager.
func (r *KeyStore) CreateManagedKey(ctx context.Context, key *keycore.APIKey) error {
	return r.Create(ctx, key)
}

// GetKeyAndOwnerID reads the credential and owner ID for a key.
// It selects the two fields needed by operations such as deletion.
// Related user and group records are not loaded.
// Soft-deleted keys are excluded.
// A missing active key returns ErrAPIKeyNotFound.
func (r *KeyStore) GetKeyAndOwnerID(ctx context.Context, id int64) (string, int64, error) {
	m, err := r.KeyActiveQuery().
		Where(apikey.IDEQ(id)).
		Select(apikey.FieldKey, apikey.FieldUserID).
		Only(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			return "", 0, keycore.ErrAPIKeyNotFound
		}
		return "", 0, err
	}
	return m.Key, m.UserID, nil
}

func (r *KeyStore) GetByKey(ctx context.Context, key string) (*keycore.APIKey, error) {
	m, err := KeyWithAPIKeyCompositeGroups(r.KeyActiveQuery()).
		Where(apikey.KeyEQ(key)).
		WithUser(func(q *dbent.UserQuery) {
			q.WithAllowedGroups(func(gq *dbent.GroupQuery) {
				gq.Select(group.FieldID)
			})
			q.WithUserDisabledPublicGroups()
		}).
		WithGroup().
		Only(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			return nil, keycore.ErrAPIKeyNotFound
		}
		return nil, err
	}
	return KeyApiKeyEntityToService(m), nil
}

func (r *KeyStore) GetByKeyForAuth(ctx context.Context, key string) (*keycore.APIKey, error) {
	m, err := r.KeyActiveQuery().
		Where(apikey.KeyEQ(key)).
		Select(
			apikey.FieldID,
			apikey.FieldUserID,
			apikey.FieldTeamID,
			apikey.FieldTeamOwnerDisabled,
			apikey.FieldCreatedAt,
			apikey.FieldGroupID,
			apikey.FieldIsComposite,
			apikey.FieldName,
			apikey.FieldStatus,
			apikey.FieldFastModePolicy,
			apikey.FieldBillingMode,
			apikey.FieldPreferredSubscriptionID,
			apikey.FieldModelMapping,
			apikey.FieldIPWhitelist,
			apikey.FieldIPBlacklist,
			apikey.FieldQuota,
			apikey.FieldQuotaUsed,
			apikey.FieldExpiresAt,
			apikey.FieldConcurrencyLimit,
			apikey.FieldRpmLimit,
			apikey.FieldRateLimit5h,
			apikey.FieldRateLimit1d,
			apikey.FieldRateLimit7d,
			apikey.FieldFallbackWhenGroupUnavailable,
		).
		WithUser(func(q *dbent.UserQuery) {
			q.Select(
				user.FieldID,
				user.FieldEmail,
				user.FieldUsername,
				user.FieldStatus,
				user.FieldRole,
				user.FieldBalance,
				user.FieldConcurrency,
				user.FieldBalanceNotifyEnabled,
				user.FieldBalanceNotifyThresholdType,
				user.FieldBalanceNotifyThreshold,
				user.FieldBalanceNotifyExtraEmails,
				user.FieldTotalRecharged,
				user.FieldSignupSource,
				user.FieldLastLoginAt,
				user.FieldLastActiveAt,
				user.FieldRpmLimit,
			)
			q.WithUserDisabledPublicGroups()
			q.WithAllowedGroups(func(gq *dbent.GroupQuery) {
				gq.Select(group.FieldID)
			})
		}).
		WithGroup(func(q *dbent.GroupQuery) {
			q.Select(
				group.FieldID,
				group.FieldName,

				group.FieldSchedulerType,
				group.FieldAdvancedSchedulerOverrides,
				group.FieldIsExclusive,
				group.FieldStatus,
				group.FieldRateMultiplier,
				group.FieldAllowImageGeneration,
				group.FieldAllowBatchImageGeneration,
				group.FieldRoutingPolicy,
				group.FieldClaudeCodeOnly,
				group.FieldFallbackGroupID,
				group.FieldFallbackGroupIDOnInvalidRequest,
				group.FieldModelRoutingEnabled,
				group.FieldModelRouting,
				group.FieldMcpXMLInject,
				group.FieldSupportedModelScopes,
				group.FieldAllowedProtocols,
				group.FieldProtocolFallbacks,
				group.FieldResponsesImagePolicy,
				group.FieldAllowLive,
				group.FieldForceOpenaiFast,
				group.FieldOpenaiFastPolicy,
				group.FieldDefaultMappedModel,
				group.FieldModelsListConfig,
				group.FieldRpmLimit,
				group.FieldMaxReasoningEffort,
				group.FieldMaxReasoningEffortOverLimit,
				group.FieldReasoningEffortMappings,
				group.FieldSessionIsolationEnabled,
			)
		}).
		WithCompositeGroups(func(q *dbent.APIKeyCompositeGroupQuery) {
			q.Order(dbent.Asc(apikeycompositegroup.FieldSortOrder), dbent.Asc(apikeycompositegroup.FieldID)).
				WithGroup()
		}).
		Only(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			return nil, keycore.ErrAPIKeyNotFound
		}
		return nil, err
	}
	return KeyApiKeyEntityToService(m), nil
}

func (r *KeyStore) Update(ctx context.Context, key *keycore.APIKey, fields keycore.APIKeyUpdateFields) error {
	// An empty field mask means the caller requested no writes.
	if fields.IsEmpty() {
		return nil
	}

	// Composite mappings and the key record must change in the same transaction.
	if fields.CompositeConfiguration && dbent.TxFromContext(ctx) == nil {
		tx, err := r.client.Tx(ctx)
		if err == nil {
			defer func() { _ = tx.Rollback() }()
			opCtx := dbent.NewTxContext(ctx, tx)
			if err := r.Update(opCtx, key, fields); err != nil {
				return err
			}
			return tx.Commit()
		}
		if !errors.Is(err, dbent.ErrTxStarted) {
			return err
		}
	}
	// Include the soft-delete predicate in the update to prevent writing a concurrently deleted key.
	// A separate existence check would race with deletion.
	// Update().Where() applies the predicate and mutation in one statement.
	// Zero affected rows report a missing or deleted key.
	// Set updated_at here so the returned timestamp needs no second query.
	client := clientFromContext(ctx, r.client)
	now := time.Now()
	builder := client.APIKey.Update().
		Where(apikey.IDEQ(key.ID), apikey.DeletedAtIsNil()).
		SetUpdatedAt(now)
	if fields.Name {
		builder.SetName(key.Name)
	}
	if fields.Status {
		builder.SetStatus(key.Status)
	}
	if fields.CompositeConfiguration {
		builder.SetIsComposite(key.IsComposite)
	}
	if fields.FastModePolicy {
		builder.SetFastModePolicy(KeyApiKeyFastModePolicyForPersistence(key.FastModePolicy))
	}
	if fields.BillingConfiguration {
		builder.SetBillingMode(KeyApiKeyBillingModeForPersistence(key.BillingMode))
		if key.PreferredSubscriptionID == nil {
			builder.ClearPreferredSubscriptionID()
		} else {
			builder.SetPreferredSubscriptionID(*key.PreferredSubscriptionID)
		}
	}
	if fields.ModelMapping {
		builder.SetModelMapping(keycore.CloneModelMapping(key.ModelMapping))
	}
	if fields.FallbackWhenGroupUnavailable {
		builder.SetFallbackWhenGroupUnavailable(key.FallbackWhenGroupUnavailable)
	}
	if fields.Quota {
		builder.SetQuota(key.Quota)
	}
	if fields.ConcurrencyLimit {
		builder.SetConcurrencyLimit(key.ConcurrencyLimit)
	}
	if fields.RPMLimit {
		builder.SetRpmLimit(key.RPMLimit)
	}
	if fields.RateLimits {
		builder.
			SetRateLimit5h(key.RateLimit5h).
			SetRateLimit1d(key.RateLimit1d).
			SetRateLimit7d(key.RateLimit7d)
	}
	if fields.GroupID {
		if key.GroupID != nil {
			builder.SetGroupID(*key.GroupID)
		} else {
			builder.ClearGroupID()
		}
	}

	// Expiration time
	if fields.ExpiresAt {
		if key.ExpiresAt != nil {
			builder.SetExpiresAt(*key.ExpiresAt)
		} else {
			builder.ClearExpiresAt()
		}
	}

	// An empty supplied IP rule set clears the corresponding stored field.
	if fields.IPRules {
		if len(key.IPWhitelist) > 0 {
			builder.SetIPWhitelist(key.IPWhitelist)
		} else {
			builder.ClearIPWhitelist()
		}
		if len(key.IPBlacklist) > 0 {
			builder.SetIPBlacklist(key.IPBlacklist)
		} else {
			builder.ClearIPBlacklist()
		}
	}

	billingpostgres.ApplyKeyUsageReset(builder, billing.KeyUsageReset{ResetQuota: fields.QuotaUsed, ResetWindows: fields.RateLimitUsage, QuotaUsed: key.QuotaUsed, Windows: billing.APIKeyRateLimitData{Usage5h: key.Usage5h, Usage1d: key.Usage1d, Usage7d: key.Usage7d, Window5hStart: key.Window5hStart, Window1dStart: key.Window1dStart, Window7dStart: key.Window7dStart}})
	affected, err := builder.Save(ctx)
	if err != nil {
		return err
	}
	if affected == 0 {
		// No matching active record remains.
		return keycore.ErrAPIKeyNotFound
	}
	if fields.CompositeConfiguration {
		if err := KeyReplaceCompositeGroupRecords(ctx, client, key.ID, key.CompositeGroups); err != nil {
			return err
		}
	}

	// Reuse the write timestamp; a second query could race with deletion.
	key.UpdatedAt = now
	return nil
}

// RotateCredential conditionally replaces the credential without writing stale configuration or usage fields.
func (r *KeyStore) RotateCredential(ctx context.Context, key *keycore.APIKey, oldKey string) error {
	now := time.Now()
	affected, err := clientFromContext(ctx, r.client).APIKey.Update().
		Where(
			apikey.IDEQ(key.ID),
			apikey.UserIDEQ(key.UserID),
			apikey.KeyEQ(oldKey),
			apikey.DeletedAtIsNil(),
			apikey.ManagedByIsNil(),
		).
		SetKey(key.Key).
		SetUpdatedAt(now).
		Save(ctx)
	if err != nil {
		return err
	}
	if affected == 0 {
		// Rotation, deletion, or ownership changes make the supplied old credential ineligible.
		return keycore.ErrAPIKeyRotationConflict
	}
	key.UpdatedAt = now
	return nil
}

func (r *KeyStore) Delete(ctx context.Context, id int64) error {
	// A short tombstone frees the unique credential while fitting the 128-character column.
	tombstoneKey := fmt.Sprintf("__deleted__%d__%d", id, time.Now().UnixNano())
	// Set deleted_at in the update so soft deletion does not depend on hooks.
	affected, err := r.client.APIKey.Update().
		Where(apikey.IDEQ(id), apikey.DeletedAtIsNil()).
		SetKey(tombstoneKey).
		SetDeletedAt(time.Now()).
		Save(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			return keycore.ErrAPIKeyNotFound
		}
		return err
	}
	if affected == 0 {
		exists, err := r.client.APIKey.Query().
			Where(apikey.IDEQ(id)).
			Exist(mixins.SkipSoftDelete(ctx))
		if err != nil {
			return err
		}
		if exists {
			return nil
		}
		return keycore.ErrAPIKeyNotFound
	}
	return nil
}

// DeleteWithAudit keeps the historical API name for callers.
// It atomically soft-deletes the key and replaces the credential with a tombstone, freeing the unique value.
func (r *KeyStore) DeleteWithAudit(ctx context.Context, id int64) error {
	tombstoneKey := fmt.Sprintf("__deleted__%d__%d", id, time.Now().UnixNano())

	if existingTx := dbent.TxFromContext(ctx); existingTx != nil {
		return r.KeyDeleteWithTombstone(ctx, existingTx.Client(), id, tombstoneKey)
	}

	tx, err := r.client.Tx(ctx)
	if err != nil && !errors.Is(err, dbent.ErrTxStarted) {
		return err
	}
	exec := r.client
	if err == nil {
		defer func() { _ = tx.Rollback() }()
		exec = tx.Client()
	}

	if err := r.KeyDeleteWithTombstone(ctx, exec, id, tombstoneKey); err != nil {
		return err
	}

	if tx != nil {
		return tx.Commit()
	}
	return nil
}

func (r *KeyStore) KeyDeleteWithTombstone(ctx context.Context, exec *dbent.Client, id int64, tombstoneKey string) error {
	res, err := exec.ExecContext(ctx, `
		UPDATE api_keys
		SET key = $1, deleted_at = NOW(), updated_at = NOW()
		WHERE id = $2 AND deleted_at IS NULL`, tombstoneKey, id)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		// Repeated deletion succeeds if the record still exists as a tombstone; an unknown ID returns NotFound.
		exists, existErr := r.client.APIKey.Query().
			Where(apikey.IDEQ(id)).
			Exist(mixins.SkipSoftDelete(ctx))
		if existErr != nil {
			return existErr
		}
		if exists {
			return nil
		}
		return keycore.ErrAPIKeyNotFound
	}
	return nil
}

func (r *KeyStore) KeyApiKeyListByUserIDQuery(userID int64, filters keycore.APIKeyListFilters) *dbent.APIKeyQuery {
	q := r.KeyActiveQuery().Where(apikey.UserIDEQ(userID))
	// Hide server-managed keys, including creative execution keys, from ordinary key lists.
	q = q.Where(apikey.ManagedByIsNil())

	if filters.Search != "" {
		q = q.Where(apikey.Or(
			apikey.NameContainsFold(filters.Search),
			apikey.KeyContainsFold(filters.Search),
		))
	}
	if filters.Status != "" {
		q = q.Where(apikey.StatusEQ(filters.Status))
	}
	if filters.GroupID != nil {
		if *filters.GroupID == 0 {
			q = q.Where(apikey.GroupIDIsNil(), apikey.IsCompositeEQ(false))
		} else {
			q = q.Where(apikey.Or(
				apikey.GroupIDEQ(*filters.GroupID),
				apikey.HasCompositeGroupsWith(apikeycompositegroup.GroupIDEQ(*filters.GroupID)),
			))
		}
	}
	// Personal and team are the supported scope filters; empty includes both.
	switch filters.Scope {
	case "personal":
		q = q.Where(apikey.TeamIDIsNil())
	case "team":
		q = q.Where(apikey.TeamIDNotNil())
	}

	return q
}

func (r *KeyStore) ListByUserID(ctx context.Context, userID int64, params pagination.PaginationParams, filters keycore.APIKeyListFilters) ([]keycore.APIKey, *pagination.PaginationResult, error) {
	q := r.KeyApiKeyListByUserIDQuery(userID, filters)

	total, err := q.Count(ctx)
	if err != nil {
		return nil, nil, err
	}

	if strings.EqualFold(strings.TrimSpace(params.SortBy), "usage") {
		return r.KeyListByUserIDWithUsageSort(ctx, q, params, total)
	}

	keysQuery := KeyWithAPIKeyCompositeGroups(q.WithGroup())
	keysQuery = keysQuery.
		Offset(params.Offset()).
		Limit(params.Limit())
	for _, order := range KeyApiKeyListOrder(params) {
		keysQuery = keysQuery.Order(order)
	}

	keys, err := keysQuery.All(ctx)
	if err != nil {
		return nil, nil, err
	}

	outKeys := make([]keycore.APIKey, 0, len(keys))
	for i := range keys {
		outKeys = append(outKeys, *KeyApiKeyEntityToService(keys[i]))
	}
	if err := r.KeyAttachLastUsedIPs(ctx, outKeys); err != nil {
		return nil, nil, err
	}

	return outKeys, pagination.ResultFromTotal(int64(total), params), nil
}

// KeyAttachLastUsedIPs loads each page key's latest nonempty usage IP in one batch.
func (r *KeyStore) KeyAttachLastUsedIPs(ctx context.Context, keys []keycore.APIKey) error {
	if len(keys) == 0 || r.sql == nil {
		return nil
	}

	apiKeyIDs := make([]int64, 0, len(keys))
	for i := range keys {
		apiKeyIDs = append(apiKeyIDs, keys[i].ID)
	}

	lastUsedIPs, err := r.KeyLatestUsageLogIPs(ctx, apiKeyIDs)
	if err != nil {
		return err
	}
	for i := range keys {
		if ipAddress, ok := lastUsedIPs[keys[i].ID]; ok {
			keys[i].LastUsedIP = &ipAddress
		}
	}
	return nil
}

func (r *KeyStore) KeyLatestUsageLogIPs(ctx context.Context, apiKeyIDs []int64) (result map[int64]string, err error) {
	// Return before reading the dialect when there is no work or executor.
	if len(apiKeyIDs) == 0 || r.sql == nil {
		return map[int64]string{}, nil
	}
	return usagequery.KeyLatestUsageLogIPs(ctx, r.sql, r.client.Driver().Dialect(), apiKeyIDs)
}

func (r *KeyStore) KeyListByUserIDWithUsageSort(ctx context.Context, q *dbent.APIKeyQuery, params pagination.PaginationParams, total int) ([]keycore.APIKey, *pagination.PaginationResult, error) {
	keys, err := KeyWithAPIKeyCompositeGroups(q.WithGroup()).
		Order(dbent.Desc(apikey.FieldID)).
		All(ctx)
	if err != nil {
		return nil, nil, err
	}

	keyIDs := make([]int64, 0, len(keys))
	outKeys := make([]keycore.APIKey, 0, len(keys))
	for i := range keys {
		key := KeyApiKeyEntityToService(keys[i])
		outKeys = append(outKeys, *key)
		keyIDs = append(keyIDs, key.ID)
	}

	usageTotals, err := r.KeyLoadAPIKeyUsageTotals(ctx, keyIDs)
	if err != nil {
		return nil, nil, err
	}

	sortOrder := params.NormalizedSortOrder(pagination.SortOrderDesc)
	sort.SliceStable(outKeys, func(i, j int) bool {
		left := usageTotals[outKeys[i].ID]
		right := usageTotals[outKeys[j].ID]
		if left == right {
			if sortOrder == pagination.SortOrderAsc {
				return outKeys[i].ID < outKeys[j].ID
			}
			return outKeys[i].ID > outKeys[j].ID
		}
		if sortOrder == pagination.SortOrderAsc {
			return left < right
		}
		return left > right
	})

	pageKeys := pagination.Slice(outKeys, params)
	if err := r.KeyAttachLastUsedIPs(ctx, pageKeys); err != nil {
		return nil, nil, err
	}
	return pageKeys, pagination.ResultFromTotal(int64(total), params), nil
}

func (r *KeyStore) VerifyOwnership(ctx context.Context, userID int64, apiKeyIDs []int64) ([]int64, error) {
	if len(apiKeyIDs) == 0 {
		return []int64{}, nil
	}

	ids, err := r.client.APIKey.Query().
		Where(apikey.UserIDEQ(userID), apikey.IDIn(apiKeyIDs...), apikey.DeletedAtIsNil()).
		IDs(ctx)
	if err != nil {
		return nil, err
	}
	return ids, nil
}

func (r *KeyStore) CountByUserID(ctx context.Context, userID int64) (int64, error) {
	count, err := r.KeyActiveQuery().Where(apikey.UserIDEQ(userID)).Count(ctx)
	return int64(count), err
}

func (r *KeyStore) ExistsByKey(ctx context.Context, key string) (bool, error) {
	count, err := r.KeyActiveQuery().Where(apikey.KeyEQ(key)).Count(ctx)
	return count > 0, err
}

func (r *KeyStore) ListByGroupID(ctx context.Context, groupID int64, params pagination.PaginationParams) ([]keycore.APIKey, *pagination.PaginationResult, error) {
	q := r.KeyActiveQuery().Where(apikey.Or(
		apikey.GroupIDEQ(groupID),
		apikey.HasCompositeGroupsWith(apikeycompositegroup.GroupIDEQ(groupID)),
	))

	total, err := q.Count(ctx)
	if err != nil {
		return nil, nil, err
	}

	keysQuery := KeyWithAPIKeyCompositeGroups(q.WithUser().WithGroup())
	keysQuery = keysQuery.
		Offset(params.Offset()).
		Limit(params.Limit())
	for _, order := range KeyApiKeyListOrder(params) {
		keysQuery = keysQuery.Order(order)
	}

	keys, err := keysQuery.All(ctx)
	if err != nil {
		return nil, nil, err
	}

	outKeys := make([]keycore.APIKey, 0, len(keys))
	for i := range keys {
		outKeys = append(outKeys, *KeyApiKeyEntityToService(keys[i]))
	}

	return outKeys, pagination.ResultFromTotal(int64(total), params), nil
}

func KeyApiKeyListOrder(params pagination.PaginationParams) []func(*entsql.Selector) {
	sortBy := strings.ToLower(strings.TrimSpace(params.SortBy))
	sortOrder := params.NormalizedSortOrder(pagination.SortOrderDesc)

	var field string
	switch sortBy {
	case "name":
		field = apikey.FieldName
	case "status":
		field = apikey.FieldStatus
	case "expires_at":
		field = apikey.FieldExpiresAt
	case "last_used_at":
		field = apikey.FieldLastUsedAt
	case "created_at":
		field = apikey.FieldCreatedAt
	case "id":
		field = apikey.FieldID
	default:
		field = apikey.FieldID
	}

	if sortOrder == pagination.SortOrderAsc {
		orders := []func(*entsql.Selector){dbent.Asc(field)}
		if field != apikey.FieldID {
			orders = append(orders, dbent.Asc(apikey.FieldID))
		}
		return orders
	}
	orders := []func(*entsql.Selector){dbent.Desc(field)}
	if field != apikey.FieldID {
		orders = append(orders, dbent.Desc(apikey.FieldID))
	}
	return orders
}

// SearchAPIKeys searches API keys by user ID and/or keyword (name).
func (r *KeyStore) SearchAPIKeys(ctx context.Context, userID int64, keyword string, limit int) ([]keycore.APIKey, error) {
	q := r.KeyActiveQuery()
	if userID > 0 {
		q = q.Where(apikey.UserIDEQ(userID))
	}

	if keyword != "" {
		q = q.Where(apikey.NameContainsFold(keyword))
	}

	keys, err := KeyWithAPIKeyCompositeGroups(q.WithGroup()).
		Limit(limit).
		Order(dbent.Desc(apikey.FieldID)).
		All(ctx)
	if err != nil {
		return nil, err
	}

	outKeys := make([]keycore.APIKey, 0, len(keys))
	for i := range keys {
		outKeys = append(outKeys, *KeyApiKeyEntityToService(keys[i]))
	}
	return outKeys, nil
}

// ClearGroupIDByGroupID clears ordinary bindings and removes composite mappings for the group.
func (r *KeyStore) ClearGroupIDByGroupID(ctx context.Context, groupID int64) (int64, error) {
	client := clientFromContext(ctx, r.client)
	n, err := client.APIKey.Update().
		Where(apikey.GroupIDEQ(groupID), apikey.DeletedAtIsNil()).
		ClearGroupID().
		Save(ctx)
	if err != nil {
		return 0, err
	}
	deleted, err := client.APIKeyCompositeGroup.Delete().
		Where(apikeycompositegroup.GroupIDEQ(groupID)).
		Exec(ctx)
	return int64(n + deleted), err
}

// UpdateGroupIDByUserAndGroup moves the user's key bindings from oldGroupID to newGroupID.
func (r *KeyStore) UpdateGroupIDByUserAndGroup(ctx context.Context, userID, oldGroupID, newGroupID int64) (int64, error) {
	client := clientFromContext(ctx, r.client)
	n, err := client.APIKey.Update().
		Where(apikey.UserIDEQ(userID), apikey.GroupIDEQ(oldGroupID), apikey.DeletedAtIsNil()).
		SetGroupID(newGroupID).
		Save(ctx)
	if err != nil {
		return 0, err
	}
	// If the target mapping exists, retain its prefix and remove the old mapping to avoid a uniqueness conflict.
	bindings, err := client.APIKeyCompositeGroup.Query().
		Where(apikeycompositegroup.GroupIDEQ(oldGroupID), apikeycompositegroup.HasAPIKeyWith(apikey.UserIDEQ(userID), apikey.DeletedAtIsNil())).
		All(ctx)
	if err != nil {
		return 0, err
	}
	for _, binding := range bindings {
		exists, err := client.APIKeyCompositeGroup.Query().
			Where(apikeycompositegroup.APIKeyIDEQ(binding.APIKeyID), apikeycompositegroup.GroupIDEQ(newGroupID)).
			Exist(ctx)
		if err != nil {
			return 0, err
		}
		if exists {
			if err := client.APIKeyCompositeGroup.DeleteOneID(binding.ID).Exec(ctx); err != nil {
				return 0, err
			}
			continue
		}
		if _, err := client.APIKeyCompositeGroup.UpdateOneID(binding.ID).SetGroupID(newGroupID).Save(ctx); err != nil {
			return 0, err
		}
	}
	return int64(n + len(bindings)), nil
}

// CountByGroupID counts non-deleted keys bound directly or through a composite mapping.
func (r *KeyStore) CountByGroupID(ctx context.Context, groupID int64) (int64, error) {
	count, err := r.KeyActiveQuery().Where(apikey.Or(
		apikey.GroupIDEQ(groupID),
		apikey.HasCompositeGroupsWith(apikeycompositegroup.GroupIDEQ(groupID)),
	)).Count(ctx)
	return int64(count), err
}

func (r *KeyStore) ListKeysByUserID(ctx context.Context, userID int64) ([]string, error) {
	keys, err := r.KeyActiveQuery().
		Where(apikey.UserIDEQ(userID)).
		Select(apikey.FieldKey).
		Strings(ctx)
	if err != nil {
		return nil, err
	}
	return keys, nil
}

func (r *KeyStore) ListKeysByGroupID(ctx context.Context, groupID int64) ([]string, error) {
	keys, err := r.KeyActiveQuery().
		Where(apikey.Or(
			apikey.GroupIDEQ(groupID),
			apikey.HasCompositeGroupsWith(apikeycompositegroup.GroupIDEQ(groupID)),
		)).
		Select(apikey.FieldKey).
		Strings(ctx)
	if err != nil {
		return nil, err
	}
	return keys, nil
}

func (r *KeyStore) UpdateLastUsed(ctx context.Context, id int64, usedAt time.Time) error {
	affected, err := r.client.APIKey.Update().
		Where(apikey.IDEQ(id), apikey.DeletedAtIsNil()).
		SetLastUsedAt(usedAt).
		SetUpdatedAt(usedAt).
		Save(ctx)
	if err != nil {
		return err
	}
	if affected == 0 {
		return keycore.ErrAPIKeyNotFound
	}
	return nil
}

func KeyApiKeyEntityToService(m *dbent.APIKey) *keycore.APIKey {
	if m == nil {
		return nil
	}
	out := &keycore.APIKey{
		ID:                           m.ID,
		UserID:                       m.UserID,
		TeamID:                       m.TeamID,
		TeamOwnerDisabled:            m.TeamOwnerDisabled,
		Key:                          m.Key,
		Name:                         m.Name,
		Status:                       m.Status,
		FastModePolicy:               m.FastModePolicy,
		BillingMode:                  m.BillingMode,
		PreferredSubscriptionID:      m.PreferredSubscriptionID,
		ModelMapping:                 keycore.CloneModelMapping(m.ModelMapping),
		IPWhitelist:                  m.IPWhitelist,
		IPBlacklist:                  m.IPBlacklist,
		LastUsedAt:                   m.LastUsedAt,
		CreatedAt:                    m.CreatedAt,
		UpdatedAt:                    m.UpdatedAt,
		GroupID:                      m.GroupID,
		IsComposite:                  m.IsComposite,
		Quota:                        m.Quota,
		QuotaUsed:                    m.QuotaUsed,
		ExpiresAt:                    m.ExpiresAt,
		ConcurrencyLimit:             m.ConcurrencyLimit,
		RPMLimit:                     m.RpmLimit,
		RateLimit5h:                  m.RateLimit5h,
		RateLimit1d:                  m.RateLimit1d,
		RateLimit7d:                  m.RateLimit7d,
		Usage5h:                      m.Usage5h,
		Usage1d:                      m.Usage1d,
		Usage7d:                      m.Usage7d,
		Window5hStart:                m.Window5hStart,
		Window1dStart:                m.Window1dStart,
		Window7dStart:                m.Window7dStart,
		FallbackWhenGroupUnavailable: m.FallbackWhenGroupUnavailable,
		ManagedBy:                    m.ManagedBy,
	}
	if m.Edges.User != nil {
		out.User = userEntityToKeyView(m.Edges.User)
		out.ActorUser = out.User
		if allowed := m.Edges.User.Edges.AllowedGroups; len(allowed) > 0 {
			out.User.AllowedGroups = make([]int64, 0, len(allowed))
			for _, g := range allowed {
				if g != nil {
					out.User.AllowedGroups = append(out.User.AllowedGroups, g.ID)
				}
			}
			slices.Sort(out.User.AllowedGroups)
		}
		if disabledPublicRows, err := m.Edges.User.Edges.UserDisabledPublicGroupsOrErr(); err == nil {
			out.User.DisabledPublicGroups = make([]int64, 0, len(disabledPublicRows))
			for _, row := range disabledPublicRows {
				out.User.DisabledPublicGroups = append(out.User.DisabledPublicGroups, row.GroupID)
			}
			slices.Sort(out.User.DisabledPublicGroups)
			out.User.GroupRestrictionsLoaded = true
		}
	}
	if m.Edges.Group != nil {
		out.Group = groupEntityToKeyView(m.Edges.Group)
	}
	if rows, err := m.Edges.CompositeGroupsOrErr(); err == nil {
		out.CompositeGroups = make([]keycore.APIKeyCompositeGroup, 0, len(rows))
		for _, row := range rows {
			if row == nil {
				continue
			}
			binding := keycore.APIKeyCompositeGroup{
				ID:               row.ID,
				APIKeyID:         row.APIKeyID,
				GroupID:          row.GroupID,
				Prefix:           row.Prefix,
				NormalizedPrefix: row.NormalizedPrefix,
				SortOrder:        row.SortOrder,
			}
			if row.Edges.Group != nil {
				binding.Group = groupEntityToKeyView(row.Edges.Group)
			}
			out.CompositeGroups = append(out.CompositeGroups, binding)
		}
	}
	return out
}

func KeyDerefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (r *KeyStore) IncrementQuotaUsed(ctx context.Context, id int64, amount float64) (float64, error) {
	return billingpostgres.NewKeyUsageStore(r.client, r.sql).IncrementQuotaUsed(ctx, id, amount)
}

func (r *KeyStore) IncrementQuotaUsedAndGetState(ctx context.Context, id int64, amount float64) (*keycore.APIKeyQuotaUsageState, error) {
	return billingpostgres.NewKeyUsageStore(r.client, r.sql).IncrementQuotaUsedAndGetState(ctx, id, amount)
}

func (r *KeyStore) IncrementRateLimitUsage(ctx context.Context, id int64, cost float64) error {
	return billingpostgres.NewKeyUsageStore(r.client, r.sql).IncrementRateLimitUsage(ctx, id, cost)
}

func (r *KeyStore) ResetRateLimitWindows(ctx context.Context, id int64) error {
	return billingpostgres.NewKeyUsageStore(r.client, r.sql).ResetRateLimitWindows(ctx, id)
}

func (r *KeyStore) GetRateLimitData(ctx context.Context, id int64) (result *keycore.APIKeyRateLimitData, err error) {
	return billingpostgres.NewKeyUsageStore(r.client, r.sql).GetRateLimitData(ctx, id)
}

func (r *KeyStore) KeyLoadAPIKeyUsageTotals(ctx context.Context, ids []int64) (map[int64]float64, error) {
	if r.usageTotals == nil {
		return nil, fmt.Errorf("API key usage totals reader is not configured")
	}
	return r.usageTotals(ctx, ids)
}

// clientFromContext returns the context transaction's client, or defaultClient when absent.
func clientFromContext(ctx context.Context, defaultClient *dbent.Client) *dbent.Client {
	if tx := dbent.TxFromContext(ctx); tx != nil {
		return tx.Client()
	}
	return defaultClient
}

// translatePersistenceError maps missing rows and unique conflicts to caller-supplied application errors.
func translatePersistenceError(err error, notFound, conflict *infraerrors.ApplicationError) error {
	if err == nil {
		return nil
	}

	// Ent and database/sql report missing rows with different error types.
	// Handle both dbent.NotFoundError and sql.ErrNoRows.
	// The application error retains the persistence error as its cause.
	if notFound != nil && (errors.Is(err, sql.ErrNoRows) || dbent.IsNotFound(err)) {
		return notFound.WithCause(err)
	}

	// Map unique constraint violations when the caller supplies a conflict error.
	if conflict != nil && isUniqueConstraintViolation(err) {
		return conflict.WithCause(err)
	}

	// Other persistence failures pass through unchanged.
	return err
}

// isUniqueConstraintViolation delegates PostgreSQL error classification.
func isUniqueConstraintViolation(err error) bool {
	return postgresinfra.IsUniqueConstraintViolation(err)
}

// scanSingleRow reads one row and closes the result set after scanning.
func scanSingleRow(ctx context.Context, q SQLExecutor, query string, args []any, dest ...any) error {
	return postgresinfra.ScanSingleRow(ctx, q, query, args, dest...)
}
