package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/lib/pq"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/apikey"
	"github.com/TokenFlux/TokenRouter/ent/authidentity"
	"github.com/TokenFlux/TokenRouter/ent/authidentitychannel"
	dbgroup "github.com/TokenFlux/TokenRouter/ent/group"
	"github.com/TokenFlux/TokenRouter/ent/identityadoptiondecision"
	"github.com/TokenFlux/TokenRouter/ent/predicate"
	"github.com/TokenFlux/TokenRouter/ent/schema/mixins"
	dbuser "github.com/TokenFlux/TokenRouter/ent/user"
	"github.com/TokenFlux/TokenRouter/ent/userallowedgroup"
	"github.com/TokenFlux/TokenRouter/ent/userdisabledpublicgroup"
	"github.com/TokenFlux/TokenRouter/ent/usersubscription"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	billingpostgres "github.com/TokenFlux/TokenRouter/internal/billing/postgres"
	identitycore "github.com/TokenFlux/TokenRouter/internal/identity"
	postgresinfra "github.com/TokenFlux/TokenRouter/internal/infra/postgres"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	"github.com/TokenFlux/TokenRouter/internal/team"
	usagequery "github.com/TokenFlux/TokenRouter/internal/usage/postgres/query"
)

const (
	// Keep these expressions consistent with identitycore.NormalizeRegistrationEmailAddress
	// and the index in 220_users_registration_email_normalized_index_notx.sql.
	IdentityNormalizedUserEmailValueSQL     = `lower(btrim(email))`
	IdentityNormalizedUserEmailLocalSQL     = `split_part(` + IdentityNormalizedUserEmailValueSQL + `, '@', 1)`
	IdentityNormalizedUserEmailDomainSQL    = `rtrim(split_part(` + IdentityNormalizedUserEmailValueSQL + `, '@', 2), '.')`
	IdentityNormalizedUserEmailBaseLocalSQL = `CASE WHEN strpos(` + IdentityNormalizedUserEmailLocalSQL + `, '+') > 1 ` +
		`THEN left(` + IdentityNormalizedUserEmailLocalSQL + `, strpos(` + IdentityNormalizedUserEmailLocalSQL + `, '+') - 1) ` +
		`ELSE ` + IdentityNormalizedUserEmailLocalSQL + ` END`
	IdentityNormalizedUserEmailSQL = `CASE WHEN ` + IdentityNormalizedUserEmailDomainSQL + ` IN ('gmail.com', 'googlemail.com') ` +
		`THEN coalesce(nullif(replace(` + IdentityNormalizedUserEmailBaseLocalSQL + `, '.', ''), ''), ` + IdentityNormalizedUserEmailBaseLocalSQL + `) || '@gmail.com' ` +
		`ELSE ` + IdentityNormalizedUserEmailBaseLocalSQL + ` || '@' || ` + IdentityNormalizedUserEmailDomainSQL + ` END`

	IdentityRegistrationEmailLockNamespace = 148623451

	// IdentityEmailAliasCandidateLimit bounds alias candidates loaded into memory.
	// Candidates are rechecked using the Go email normalization rules.
	IdentityEmailAliasCandidateLimit = 50
)

var (
	// IdentityLikeWildcardEscaper quotes %, _, and backslashes in email local parts for LIKE.
	IdentityLikeWildcardEscaper = strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`)

	likePatternReplacer = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
)

type UserStore struct {
	client *dbent.Client
	sql    SQLExecutor
}

func NewUserStore(client *dbent.Client, sqlDB *sql.DB) *UserStore {
	return NewUserStoreWithSQL(client, sqlDB)
}

func NewUserStoreWithSQL(client *dbent.Client, sqlq SQLExecutor) *UserStore {
	return &UserStore{client: client, sql: sqlq}
}

func (r *UserStore) Create(ctx context.Context, userIn *identitycore.User) error {
	return r.IdentityCreateWithNormalizationGuard(ctx, userIn, "", "")
}

func (r *UserStore) CreateWithNormalizedEmailGuard(ctx context.Context, userIn *identitycore.User, normalizedEmail string) error {
	return r.IdentityCreateWithNormalizationGuard(ctx, userIn, normalizedEmail, "")
}

// CountUsersByEmailDomain counts undeleted users in a registrable domain and its subdomains.
func (r *UserStore) CountUsersByEmailDomain(ctx context.Context, domain string) (int, error) {
	return IdentityCountUsersByEmailDomainWithClient(ctx, clientFromContext(ctx, r.client), domain)
}

// CreateWithRegistrationEmailGuards creates a user under email uniqueness and domain quota locks.
func (r *UserStore) CreateWithRegistrationEmailGuards(ctx context.Context, userIn *identitycore.User, normalizedEmail, domain string) error {
	return r.IdentityCreateWithNormalizationGuard(ctx, userIn, normalizedEmail, IdentityNormalizeEmailDomain(domain))
}

func (r *UserStore) IdentityCreateWithNormalizationGuard(ctx context.Context, userIn *identitycore.User, normalizedEmail, domainLimit string) error {
	if userIn == nil {
		return nil
	}

	// User, invitation, and group writes share the Ent transaction from the context.
	// An Ent client built manually from *sql.Tx fails the ExecQuerier assertion.
	// Starting a separate transaction could commit the user while the invitation rolls back.
	var txClient *dbent.Client
	txCtx := ctx
	var ownedTx *dbent.Tx
	if existingTx := dbent.TxFromContext(ctx); existingTx != nil {
		txClient = existingTx.Client()
	} else {
		tx, err := r.client.Tx(ctx)
		switch {
		case err == nil:
			ownedTx = tx
			defer func() { _ = ownedTx.Rollback() }()
			txClient = tx.Client()
			txCtx = dbent.NewTxContext(ctx, tx)
		case errors.Is(err, dbent.ErrTxStarted):
			// The client may already be bound to a transaction, as in integration fixtures.
			// Its owner is responsible for committing or rolling back.
			txClient = r.client
		default:
			return err
		}
	}

	lockKeys := []string{IdentityNormalizedEmailUniquenessLockKey(userIn.Email)}
	if domainLimit != "" {
		lockKeys = append(lockKeys, IdentityRegistrationEmailDomainLockKey(domainLimit))
	}
	releaseEmailLock, err := IdentityLockRepositoryScopedKeys(
		txCtx,
		txClient,
		IdentityTxAwareSQLExecutor(txCtx, r.sql, r.client),
		lockKeys...,
	)
	if err != nil {
		return err
	}
	defer releaseEmailLock()

	if domainLimit != "" {
		count, err := IdentityCountUsersByEmailDomainWithClient(txCtx, txClient, domainLimit)
		if err != nil {
			return err
		}
		if count > 0 {
			return identitycore.ErrEmailDomainRegistrationLimit
		}
	}

	if err := IdentityEnsureNormalizedEmailAvailableWithClient(txCtx, txClient, 0, userIn.Email); err != nil {
		return err
	}

	if normalizedEmail != "" {
		if err := r.LockRegistrationEmail(txCtx, normalizedEmail); err != nil {
			return err
		}
		exists, err := r.IdentityExistsByNormalizedEmail(txCtx, normalizedEmail, 0)
		if err != nil {
			return err
		}
		if exists {
			return identitycore.ErrEmailExists
		}
	}

	created, err := r.IdentityCreateWithClient(txCtx, txClient, userIn)
	if err != nil {
		return translatePersistenceError(err, nil, identitycore.ErrEmailExists)
	}

	if err := r.IdentitySyncUserAllowedGroupsWithClient(txCtx, txClient, created.ID, userIn.AllowedGroups); err != nil {
		return err
	}
	if err := r.IdentitySyncUserDisabledPublicGroupsWithClient(txCtx, txClient, created.ID, userIn.DisabledPublicGroups); err != nil {
		return err
	}
	if err := IdentityEnsureEmailAuthIdentityWithClient(txCtx, txClient, created.ID, created.Email, "user_repo_create"); err != nil {
		return err
	}

	if ownedTx != nil {
		if err := ownedTx.Commit(); err != nil {
			return err
		}
	}

	IdentityApplyUserEntityToService(userIn, created)
	return nil
}

func (r *UserStore) GetByID(ctx context.Context, id int64) (*identitycore.User, error) {
	m, err := r.client.User.Query().Where(dbuser.IDEQ(id)).Only(ctx)
	if err != nil {
		return nil, translatePersistenceError(err, identitycore.ErrUserNotFound, nil)
	}

	out := UserFromEntity(m)
	groups, err := r.IdentityLoadAllowedGroups(ctx, []int64{id})
	if err != nil {
		return nil, err
	}
	if v, ok := groups[id]; ok {
		out.AllowedGroups = v
	}
	disabledPublicGroups, err := r.IdentityLoadDisabledPublicGroups(ctx, []int64{id})
	if err != nil {
		return nil, err
	}
	if v, ok := disabledPublicGroups[id]; ok {
		out.DisabledPublicGroups = v
	}
	out.GroupRestrictionsLoaded = true
	return out, nil
}

func (r *UserStore) GetByIDIncludeDeleted(ctx context.Context, id int64) (*identitycore.User, error) {
	ctx = mixins.SkipSoftDelete(ctx)
	m, err := r.client.User.Query().Where(dbuser.IDEQ(id)).Only(ctx)
	if err != nil {
		return nil, translatePersistenceError(err, identitycore.ErrUserNotFound, nil)
	}
	out := UserFromEntity(m)
	groups, err := r.IdentityLoadAllowedGroups(ctx, []int64{id})
	if err != nil {
		return nil, err
	}
	if v, ok := groups[id]; ok {
		out.AllowedGroups = v
	}
	disabledPublicGroups, err := r.IdentityLoadDisabledPublicGroups(ctx, []int64{id})
	if err != nil {
		return nil, err
	}
	if v, ok := disabledPublicGroups[id]; ok {
		out.DisabledPublicGroups = v
	}
	out.GroupRestrictionsLoaded = true
	return out, nil
}

func (r *UserStore) GetByEmail(ctx context.Context, email string) (*identitycore.User, error) {
	matches, err := r.client.User.Query().
		Where(IdentityUserEmailLookupPredicate(email)).
		Order(dbent.Asc(dbuser.FieldID)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, identitycore.ErrUserNotFound
	}
	if len(matches) > 1 {
		return nil, fmt.Errorf("normalized email lookup matched multiple users for %q", strings.TrimSpace(email))
	}
	m := matches[0]

	out := UserFromEntity(m)
	groups, err := r.IdentityLoadAllowedGroups(ctx, []int64{m.ID})
	if err != nil {
		return nil, err
	}
	if v, ok := groups[m.ID]; ok {
		out.AllowedGroups = v
	}
	disabledPublicGroups, err := r.IdentityLoadDisabledPublicGroups(ctx, []int64{m.ID})
	if err != nil {
		return nil, err
	}
	if v, ok := disabledPublicGroups[m.ID]; ok {
		out.DisabledPublicGroups = v
	}
	out.GroupRestrictionsLoaded = true
	return out, nil
}

func (r *UserStore) Update(ctx context.Context, userIn *identitycore.User, fields identitycore.UserUpdateFields) error {
	return r.IdentityUpdateWithNormalizationGuard(ctx, userIn, "", fields)
}

func (r *UserStore) UpdateWithNormalizedEmailGuard(ctx context.Context, userIn *identitycore.User, normalizedEmail string, fields identitycore.UserUpdateFields) error {
	return r.IdentityUpdateWithNormalizationGuard(ctx, userIn, normalizedEmail, fields)
}

func (r *UserStore) IdentityUpdateWithNormalizationGuard(ctx context.Context, userIn *identitycore.User, normalizedEmail string, fields identitycore.UserUpdateFields) error {
	if userIn == nil || fields.IsEmpty() {
		return nil
	}

	// Update user fields and group relations in one Ent transaction.
	tx, err := r.client.Tx(ctx)
	if err != nil && !errors.Is(err, dbent.ErrTxStarted) {
		return err
	}

	var txClient *dbent.Client
	txCtx := ctx
	if err == nil {
		defer func() { _ = tx.Rollback() }()
		txClient = tx.Client()
		txCtx = dbent.NewTxContext(ctx, tx)
	} else {
		// Reuse an external transaction; its caller owns commit and rollback.
		if existingTx := dbent.TxFromContext(ctx); existingTx != nil {
			txClient = existingTx.Client()
		} else {
			txClient = r.client
		}
	}

	// Serialize writes to the destination email address.
	if fields.Email {
		releaseEmailLock, err := IdentityLockRepositoryScopedKeys(
			txCtx,
			txClient,
			IdentityTxAwareSQLExecutor(txCtx, r.sql, r.client),
			IdentityNormalizedEmailUniquenessLockKey(userIn.Email),
		)
		if err != nil {
			return err
		}
		defer releaseEmailLock()

		if err := IdentityEnsureNormalizedEmailAvailableWithClient(txCtx, txClient, userIn.ID, userIn.Email); err != nil {
			return err
		}
		if normalizedEmail != "" {
			if err := r.LockRegistrationEmail(txCtx, normalizedEmail); err != nil {
				return err
			}
			exists, err := r.IdentityExistsByNormalizedEmail(txCtx, normalizedEmail, userIn.ID)
			if err != nil {
				return err
			}
			if exists {
				return identitycore.ErrEmailExists
			}
		}
	}

	existing, err := clientFromContext(txCtx, txClient).User.Get(txCtx, userIn.ID)
	if err != nil {
		return translatePersistenceError(err, identitycore.ErrUserNotFound, nil)
	}
	oldEmail := existing.Email

	updated, err := r.IdentityUpdateWithClient(txCtx, txClient, userIn, fields)
	if err != nil {
		return translatePersistenceError(err, identitycore.ErrUserNotFound, identitycore.ErrEmailExists)
	}

	if fields.AllowedGroups {
		if err := r.IdentitySyncUserAllowedGroupsWithClient(txCtx, txClient, updated.ID, userIn.AllowedGroups); err != nil {
			return err
		}
	}
	if fields.DisabledPublicGroups {
		if err := r.IdentitySyncUserDisabledPublicGroupsWithClient(txCtx, txClient, updated.ID, userIn.DisabledPublicGroups); err != nil {
			return err
		}
	}
	// Synchronize the email identity from the stored address; repeated calls are idempotent.
	if err := IdentityReplaceEmailAuthIdentityWithClient(txCtx, txClient, updated.ID, oldEmail, updated.Email, "user_repo_update"); err != nil {
		return err
	}

	if tx != nil {
		if err := tx.Commit(); err != nil {
			return err
		}
	}

	userIn.UpdatedAt = updated.UpdatedAt
	return nil
}

func IdentityEnsureEmailAuthIdentityWithClient(ctx context.Context, client *dbent.Client, userID int64, email string, source string) error {
	client = clientFromContext(ctx, client)
	if client == nil || userID <= 0 {
		return nil
	}

	subject := IdentityNormalizeEmailAuthIdentitySubject(email)
	if subject == "" {
		return nil
	}

	if err := client.AuthIdentity.Create().
		SetUserID(userID).
		SetProviderType("email").
		SetProviderKey("email").
		SetProviderSubject(subject).
		SetVerifiedAt(time.Now().UTC()).
		SetMetadata(map[string]any{"source": source}).
		OnConflictColumns(
			authidentity.FieldProviderType,
			authidentity.FieldProviderKey,
			authidentity.FieldProviderSubject,
		).
		DoNothing().
		Exec(ctx); err != nil {
		if !isSQLNoRowsError(err) {
			return err
		}
	}

	identity, err := client.AuthIdentity.Query().
		Where(
			authidentity.ProviderTypeEQ("email"),
			authidentity.ProviderKeyEQ("email"),
			authidentity.ProviderSubjectEQ(subject),
		).
		Only(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			return nil
		}
		return err
	}
	if identity.UserID != userID {
		return ErrAuthIdentityOwnershipConflict
	}
	return nil
}

func IdentityReplaceEmailAuthIdentityWithClient(ctx context.Context, client *dbent.Client, userID int64, oldEmail, newEmail string, source string) error {
	newSubject := IdentityNormalizeEmailAuthIdentitySubject(newEmail)
	if err := IdentityEnsureEmailAuthIdentityWithClient(ctx, client, userID, newEmail, source); err != nil {
		return err
	}

	oldSubject := IdentityNormalizeEmailAuthIdentitySubject(oldEmail)
	if oldSubject == "" || oldSubject == newSubject {
		return nil
	}

	_, err := clientFromContext(ctx, client).AuthIdentity.Delete().
		Where(
			authidentity.UserIDEQ(userID),
			authidentity.ProviderTypeEQ("email"),
			authidentity.ProviderKeyEQ("email"),
			authidentity.ProviderSubjectEQ(oldSubject),
		).
		Exec(ctx)
	return err
}

func IdentityNormalizeEmailAuthIdentitySubject(email string) string {
	normalized := strings.ToLower(strings.TrimSpace(email))
	if normalized == "" {
		return ""
	}
	if strings.HasSuffix(normalized, identitycore.LinuxDoConnectSyntheticEmailDomain) ||
		strings.HasSuffix(normalized, identitycore.OIDCConnectSyntheticEmailDomain) ||
		strings.HasSuffix(normalized, identitycore.WeChatConnectSyntheticEmailDomain) ||
		strings.HasSuffix(normalized, identitycore.DingTalkConnectSyntheticEmailDomain) {
		return ""
	}
	return normalized
}

func (r *UserStore) Delete(ctx context.Context, id int64) error {
	// Reuse the caller's transaction so administrative key and user deletion commit together.
	if existingTx := dbent.TxFromContext(ctx); existingTx != nil {
		return r.IdentityDeleteUser(ctx, existingTx.Client(), id)
	}

	tx, err := r.client.Tx(ctx)
	if err != nil && !errors.Is(err, dbent.ErrTxStarted) {
		return translatePersistenceError(err, identitycore.ErrUserNotFound, nil)
	}
	exec := r.client
	if err == nil {
		defer func() { _ = tx.Rollback() }()
		exec = tx.Client()
	}

	if err := r.IdentityDeleteUser(ctx, exec, id); err != nil {
		return err
	}

	if tx != nil {
		if err := tx.Commit(); err != nil {
			return translatePersistenceError(err, identitycore.ErrUserNotFound, nil)
		}
	}
	return nil
}

// IdentityDeleteUser deletes a user and identity references using a caller-managed transaction.
func (r *UserStore) IdentityDeleteUser(ctx context.Context, exec *dbent.Client, id int64) error {
	identityIDs, err := exec.AuthIdentity.Query().
		Where(authidentity.UserIDEQ(id)).
		IDs(ctx)
	if err != nil {
		return translatePersistenceError(err, identitycore.ErrUserNotFound, nil)
	}
	if len(identityIDs) > 0 {
		if _, err := exec.IdentityAdoptionDecision.Update().
			Where(identityadoptiondecision.IdentityIDIn(identityIDs...)).
			ClearIdentityID().
			Save(ctx); err != nil {
			return translatePersistenceError(err, identitycore.ErrUserNotFound, nil)
		}
		if _, err := exec.AuthIdentityChannel.Delete().
			Where(authidentitychannel.IdentityIDIn(identityIDs...)).
			Exec(ctx); err != nil {
			return translatePersistenceError(err, identitycore.ErrUserNotFound, nil)
		}
		if _, err := exec.AuthIdentity.Delete().
			Where(authidentity.UserIDEQ(id)).
			Exec(ctx); err != nil {
			return translatePersistenceError(err, identitycore.ErrUserNotFound, nil)
		}
	}

	affected, err := exec.User.Delete().Where(dbuser.IDEQ(id)).Exec(ctx)
	if err != nil {
		var pqErr *pq.Error
		if errors.As(err, &pqErr) && strings.Contains(pqErr.Message, "TEAM_OWNER_TRANSFER_REQUIRED") {
			return team.ErrTeamOwnerTransferRequired
		}
		return translatePersistenceError(err, identitycore.ErrUserNotFound, nil)
	}
	if affected == 0 {
		return identitycore.ErrUserNotFound
	}
	return nil
}

func (r *UserStore) List(ctx context.Context, params pagination.PaginationParams) ([]identitycore.User, *pagination.PaginationResult, error) {
	return r.ListWithFilters(ctx, params, identitycore.UserListFilters{})
}

func (r *UserStore) ListWithFilters(ctx context.Context, params pagination.PaginationParams, filters identitycore.UserListFilters) ([]identitycore.User, *pagination.PaginationResult, error) {
	// IncludeDeleted affects user queries; subscription and group queries retain soft-delete filtering.
	userCtx := ctx
	if filters.IncludeDeleted {
		userCtx = mixins.SkipSoftDelete(ctx)
	}

	q := r.client.User.Query()

	if filters.Status != "" {
		q = q.Where(dbuser.StatusEQ(filters.Status))
	}
	if filters.Role != "" {
		q = q.Where(dbuser.RoleEQ(filters.Role))
	}
	if filters.Search != "" {
		q = q.Where(identityUserSearch(filters.Search))
	}

	if filters.GroupName != "" {
		q = q.Where(dbuser.HasAllowedGroupsWith(
			dbgroup.NameContainsFold(filters.GroupName),
		))
	}

	if filters.APIKeyGroupID > 0 {
		// Match users with an undeleted API key in the requested group.
		// The HasAPIKeysWith subquery needs its own DeletedAtIsNil predicate.
		q = q.Where(dbuser.HasAPIKeysWith(
			apikey.GroupIDEQ(filters.APIKeyGroupID),
			apikey.DeletedAtIsNil(),
		))
	}

	// Resolve attribute matches to user IDs before counting and paging.
	var allowedUserIDs []int64
	if len(filters.Attributes) > 0 {
		var attrErr error
		allowedUserIDs, attrErr = r.IdentityFilterUsersByAttributes(ctx, filters.Attributes)
		if attrErr != nil {
			return nil, nil, attrErr
		}
		if len(allowedUserIDs) == 0 {
			return []identitycore.User{}, pagination.ResultFromTotal(0, params), nil
		}
		q = q.Where(dbuser.IDIn(allowedUserIDs...))
	}

	total, err := q.Clone().Count(userCtx)
	if err != nil {
		return nil, nil, err
	}

	usersQuery := q.
		Offset(params.Offset()).
		Limit(params.Limit())
	for _, order := range IdentityUserListOrder(params) {
		usersQuery = usersQuery.Order(order)
	}

	users, err := usersQuery.All(userCtx)
	if err != nil {
		return nil, nil, err
	}

	outUsers := make([]identitycore.User, 0, len(users))
	if len(users) == 0 {
		return outUsers, pagination.ResultFromTotal(int64(total), params), nil
	}

	userIDs := make([]int64, 0, len(users))
	userMap := make(map[int64]*identitycore.User, len(users))
	for i := range users {
		userIDs = append(userIDs, users[i].ID)
		u := UserFromEntity(users[i])
		outUsers = append(outUsers, *u)
		userMap[u.ID] = &outUsers[len(outUsers)-1]
	}

	shouldLoadSubscriptions := filters.IncludeSubscriptions == nil || *filters.IncludeSubscriptions
	if shouldLoadSubscriptions {
		// Load active subscriptions and their plans in a batch.
		subs, err := r.client.UserSubscription.Query().
			Where(
				usersubscription.UserIDIn(userIDs...),
				usersubscription.StatusEQ(billing.SubscriptionStatusActive),
			).
			WithPlan().
			All(ctx)
		if err != nil {
			return nil, nil, err
		}

		for i := range subs {
			if u, ok := userMap[subs[i].UserID]; ok {
				u.Subscriptions = append(u.Subscriptions, *billingpostgres.SubscriptionFromEntity(subs[i]))
			}
		}
	}

	allowedGroupsByUser, err := r.IdentityLoadAllowedGroups(ctx, userIDs)
	if err != nil {
		return nil, nil, err
	}
	for id, u := range userMap {
		if groups, ok := allowedGroupsByUser[id]; ok {
			u.AllowedGroups = groups
		}
	}
	disabledPublicGroupsByUser, err := r.IdentityLoadDisabledPublicGroups(ctx, userIDs)
	if err != nil {
		return nil, nil, err
	}
	for id, u := range userMap {
		if groups, ok := disabledPublicGroupsByUser[id]; ok {
			u.DisabledPublicGroups = groups
		}
		u.GroupRestrictionsLoaded = true
	}

	return outUsers, pagination.ResultFromTotal(int64(total), params), nil
}

// identityUserSearch queries each field separately so each can use its fuzzy-search index.
func identityUserSearch(search string) predicate.User {
	return func(s *entsql.Selector) {
		builder := entsql.Dialect(s.Dialect())
		users := builder.Table(dbuser.Table).As("search_users")
		keys := builder.Table(apikey.Table).As("search_keys")
		matches := builder.Select(users.C(dbuser.FieldID)).From(users).
			Where(entsql.ContainsFold(users.C(dbuser.FieldEmail), search))
		for _, field := range []string{dbuser.FieldUsername, dbuser.FieldNotes} {
			matches.Union(builder.Select(users.C(dbuser.FieldID)).From(users).
				Where(entsql.ContainsFold(users.C(field), search)))
		}
		keyMatches := builder.Select(keys.C(apikey.FieldUserID)).From(keys).
			Where(entsql.ContainsFold(keys.C(apikey.FieldKey), search))
		s.Where(entsql.In(s.C(dbuser.FieldID), matches.Union(keyMatches)))
	}
}

func IdentityUserListOrder(params pagination.PaginationParams) []func(*entsql.Selector) {
	sortBy := strings.ToLower(strings.TrimSpace(params.SortBy))
	sortOrder := params.NormalizedSortOrder(pagination.SortOrderDesc)

	if sortBy == "last_used_at" {
		return IdentityUserLastUsedAtOrder(sortOrder)
	}

	var field string
	defaultField := true
	nullsLastField := false
	switch sortBy {
	case "email":
		field = dbuser.FieldEmail
		defaultField = false
	case "username":
		field = dbuser.FieldUsername
		defaultField = false
	case "role":
		field = dbuser.FieldRole
		defaultField = false
	case "balance":
		field = dbuser.FieldBalance
		defaultField = false
	case "concurrency":
		field = dbuser.FieldConcurrency
		defaultField = false
	case "status":
		field = dbuser.FieldStatus
		defaultField = false
	case "created_at":
		field = dbuser.FieldCreatedAt
		defaultField = false
	case "last_active_at":
		field = dbuser.FieldLastActiveAt
		defaultField = false
		nullsLastField = true
	default:
		field = dbuser.FieldID
	}

	if sortOrder == pagination.SortOrderAsc {
		if defaultField && field == dbuser.FieldID {
			return []func(*entsql.Selector){dbent.Asc(dbuser.FieldID)}
		}
		if nullsLastField {
			return []func(*entsql.Selector){
				entsql.OrderByField(field, entsql.OrderNullsLast()).ToFunc(),
				dbent.Asc(dbuser.FieldID),
			}
		}
		return []func(*entsql.Selector){dbent.Asc(field), dbent.Asc(dbuser.FieldID)}
	}
	if defaultField && field == dbuser.FieldID {
		return []func(*entsql.Selector){dbent.Desc(dbuser.FieldID)}
	}
	if nullsLastField {
		return []func(*entsql.Selector){
			entsql.OrderByField(field, entsql.OrderDesc(), entsql.OrderNullsLast()).ToFunc(),
			dbent.Desc(dbuser.FieldID),
		}
	}
	return []func(*entsql.Selector){dbent.Desc(field), dbent.Desc(dbuser.FieldID)}
}

func (r *UserStore) GetLatestUsedAtByUserIDs(ctx context.Context, userIDs []int64) (map[int64]*time.Time, error) {
	if len(userIDs) == 0 {
		return map[int64]*time.Time{}, nil
	}
	return usagequery.GetLatestUsedAtByUserIDs(ctx, r.sql, userIDs)
}

func (r *UserStore) GetLatestUsedAtByUserID(ctx context.Context, userID int64) (*time.Time, error) {
	latestByUserID, err := r.GetLatestUsedAtByUserIDs(ctx, []int64{userID})
	if err != nil {
		return nil, err
	}
	return latestByUserID[userID], nil
}

func IdentityUserLastUsedAtOrder(sortOrder string) []func(*entsql.Selector) {
	return usagequery.IdentityUserLastUsedAtOrder(sortOrder)
}

// IdentityFilterUsersByAttributes returns user IDs matching every attribute filter.
func (r *UserStore) IdentityFilterUsersByAttributes(ctx context.Context, attrs map[int64]string) ([]int64, error) {
	if len(attrs) == 0 {
		return nil, nil
	}

	if r.sql == nil {
		return nil, fmt.Errorf("sql executor is not configured")
	}

	clauses := make([]string, 0, len(attrs))
	args := make([]any, 0, len(attrs)*2+1)
	argIndex := 1
	for attrID, value := range attrs {
		clauses = append(clauses, fmt.Sprintf("(attribute_id = $%d AND value ILIKE $%d)", argIndex, argIndex+1))
		args = append(args, attrID, "%"+value+"%")
		argIndex += 2
	}

	query := fmt.Sprintf(
		`SELECT user_id
		 FROM user_attribute_values
		 WHERE %s
		 GROUP BY user_id
		 HAVING COUNT(DISTINCT attribute_id) = $%d`,
		strings.Join(clauses, " OR "),
		argIndex,
	)
	args = append(args, len(attrs))

	rows, err := r.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	result := make([]int64, 0)
	for rows.Next() {
		var userID int64
		if scanErr := rows.Scan(&userID); scanErr != nil {
			return nil, scanErr
		}
		result = append(result, userID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func (r *UserStore) UpdateBalance(ctx context.Context, id int64, amount float64) error {
	return r.IdentityBillingBalance(ctx).UpdateBalance(ctx, id, amount)
}

func (r *UserStore) AddBalance(ctx context.Context, id int64, amount float64) error {
	return r.IdentityBillingBalance(ctx).AddBalance(ctx, id, amount)
}

func (r *UserStore) ApplyRedeemBalanceAdjustment(ctx context.Context, id int64, delta float64) error {
	return r.IdentityBillingBalance(ctx).ApplyRedeemBalanceAdjustment(ctx, id, delta)
}

// DeductBalance deducts from positive balances down to zero and leaves existing negative balances unchanged.
func (r *UserStore) DeductBalance(ctx context.Context, id int64, amount float64) (float64, error) {
	sqlq := r.IdentitySqlExecutorFromContext(ctx)
	if sqlq == nil {
		return 0, fmt.Errorf("sql executor is not configured")
	}

	_, deductedAmount, err := IdentityDeductUserBalance(ctx, sqlq, id, amount)
	if err != nil {
		return 0, err
	}
	return deductedAmount, nil
}

func (r *UserStore) AdjustBalance(ctx context.Context, id int64, delta float64) (identitycore.BalanceChange, error) {
	return r.IdentityBillingBalance(ctx).AdjustBalance(ctx, id, delta)
}

func (r *UserStore) SetBalance(ctx context.Context, id int64, value float64) (identitycore.BalanceChange, error) {
	return r.IdentityBillingBalance(ctx).SetBalance(ctx, id, value)
}

func (r *UserStore) UpdateConcurrency(ctx context.Context, id int64, amount int) error {
	return NewConcurrencyStore(r.client).UpdateConcurrency(ctx, id, amount)
}

func (r *UserStore) ApplyRedeemConcurrencyAdjustment(ctx context.Context, id int64, delta int) error {
	return NewConcurrencyStore(r.client).ApplyRedeemConcurrencyAdjustment(ctx, id, delta)
}

func (r *UserStore) BatchSetConcurrency(ctx context.Context, userIDs []int64, value int) (int, error) {
	if len(userIDs) == 0 {
		return 0, nil
	}
	if value < 0 {
		value = 0
	}
	sqlq := r.IdentitySqlExecutorFromContext(ctx)
	if sqlq == nil {
		return 0, fmt.Errorf("sql executor is not configured")
	}
	res, err := sqlq.ExecContext(ctx,
		"UPDATE users SET concurrency = $1, updated_at = NOW() WHERE id = ANY($2) AND deleted_at IS NULL",
		value, pq.Array(userIDs))
	if err != nil {
		return 0, fmt.Errorf("batch set concurrency: %w", err)
	}
	affected, _ := res.RowsAffected()
	return int(affected), nil
}

func (r *UserStore) BatchAddConcurrency(ctx context.Context, userIDs []int64, delta int) (int, error) {
	if len(userIDs) == 0 {
		return 0, nil
	}
	sqlq := r.IdentitySqlExecutorFromContext(ctx)
	if sqlq == nil {
		return 0, fmt.Errorf("sql executor is not configured")
	}
	res, err := sqlq.ExecContext(ctx,
		"UPDATE users SET concurrency = GREATEST(concurrency + $1, 0), updated_at = NOW() WHERE id = ANY($2) AND deleted_at IS NULL",
		delta, pq.Array(userIDs))
	if err != nil {
		return 0, fmt.Errorf("batch add concurrency: %w", err)
	}
	affected, _ := res.RowsAffected()
	return int(affected), nil
}

// BatchUpdateLimits sets supplied concurrency and RPM limits for selected users in one SQL statement.
func (r *UserStore) BatchUpdateLimits(ctx context.Context, userIDs []int64, concurrency, rpmLimit *int) (int, error) {
	if len(userIDs) == 0 || (concurrency == nil && rpmLimit == nil) {
		return 0, nil
	}
	sqlq := r.IdentitySqlExecutorFromContext(ctx)
	if sqlq == nil {
		return 0, fmt.Errorf("sql executor is not configured")
	}

	setClauses := make([]string, 0, 3)
	args := make([]any, 0, 3)
	if concurrency != nil {
		value := max(*concurrency, 0)
		args = append(args, value)
		setClauses = append(setClauses, fmt.Sprintf("concurrency = $%d", len(args)))
	}
	if rpmLimit != nil {
		value := max(*rpmLimit, 0)
		args = append(args, value)
		setClauses = append(setClauses, fmt.Sprintf("rpm_limit = $%d", len(args)))
	}
	setClauses = append(setClauses, "updated_at = NOW()")
	args = append(args, pq.Array(userIDs))

	query := fmt.Sprintf(
		"UPDATE users SET %s WHERE id = ANY($%d) AND deleted_at IS NULL",
		strings.Join(setClauses, ", "),
		len(args),
	)
	res, err := sqlq.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("batch update user limits: %w", err)
	}
	affected, _ := res.RowsAffected()
	return int(affected), nil
}

func (r *UserStore) ExistsByEmail(ctx context.Context, email string) (bool, error) {
	client := clientFromContext(ctx, r.client)
	return client.User.Query().Where(IdentityUserEmailLookupPredicate(email)).Exist(ctx)
}

// ExistsByEmailAlias reports whether a user owns the same normalized mailbox.
// The User soft-delete interceptor filters deleted users.
func (r *UserStore) ExistsByEmailAlias(ctx context.Context, email string) (bool, error) {
	_, exists, err := r.EmailAliasOwnerID(ctx, email, 0)
	return exists, err
}

// EmailAliasOwnerID returns the mailbox owner, using currentUserID to identify the caller.
// When duplicate rows exist, another owner takes precedence so callers can detect conflicts.
func (r *UserStore) EmailAliasOwnerID(ctx context.Context, email string, currentUserID int64) (int64, bool, error) {
	return IdentityEmailAliasOwnerIDWithClient(ctx, clientFromContext(ctx, r.client), email, currentUserID)
}

func IdentityEmailAliasOwnerIDWithClient(ctx context.Context, client *dbent.Client, email string, currentUserID int64) (int64, bool, error) {
	if client == nil {
		return 0, false, nil
	}
	probes := identitycore.EmailAliasDedupProbes(email)
	if len(probes) == 0 {
		return 0, false, nil
	}

	preds := make([]predicate.User, 0, 2*len(probes))
	for _, probe := range probes {
		probeEmail := probe.Local + "@" + probe.Domain
		preds = append(preds,
			IdentityDotStrippedEmailEQ(probeEmail),
			// The plus suffix is unknown, so match the escaped local-part prefix.
			IdentityDotStrippedEmailLike(IdentityEscapeLikeWildcards(probe.Local)+"+%@"+IdentityEscapeLikeWildcards(probe.Domain)),
		)
	}

	candidates, err := client.User.Query().
		Where(dbuser.Or(preds...)).
		Limit(IdentityEmailAliasCandidateLimit).
		Select(dbuser.FieldID, dbuser.FieldEmail).
		All(ctx)
	if err != nil {
		return 0, false, err
	}

	identity := identitycore.NormalizeEmailForAliasDedup(email)
	var selfID int64
	selfExists := false
	for _, candidate := range candidates {
		if identitycore.NormalizeEmailForAliasDedup(candidate.Email) != identity {
			continue
		}
		if candidate.ID != 0 && candidate.ID != currentUserID {
			return candidate.ID, true, nil
		}
		if candidate.ID == currentUserID {
			selfID = candidate.ID
			selfExists = true
		}
	}
	return selfID, selfExists, nil
}

// IdentityDotStrippedEmailExpr removes dots for an alias candidate probe, including Gmail
// local-part dots and a domain root dot. Go normalization rechecks the candidates.
func IdentityDotStrippedEmailExpr(b *entsql.Builder, s *entsql.Selector) *entsql.Builder {
	return b.WriteString("REPLACE(LOWER(TRIM(").
		Ident(s.C(dbuser.FieldEmail)).
		WriteString(")), '.', '')")
}

func IdentityDotStrippedEmailEQ(value string) predicate.User {
	return predicate.User(func(s *entsql.Selector) {
		s.Where(entsql.P(func(b *entsql.Builder) {
			IdentityDotStrippedEmailExpr(b, s).WriteString(" = ").Arg(value)
		}))
	})
}

func IdentityDotStrippedEmailLike(pattern string) predicate.User {
	return predicate.User(func(s *entsql.Selector) {
		s.Where(entsql.P(func(b *entsql.Builder) {
			IdentityDotStrippedEmailExpr(b, s).WriteString(" LIKE ").Arg(pattern).WriteString(` ESCAPE '\'`)
		}))
	})
}

func IdentityEscapeLikeWildcards(value string) string {
	return IdentityLikeWildcardEscaper.Replace(value)
}

// UpdateEmailWithAliasGuard locks and rechecks mailbox ownership in the caller's transaction
// before updating the email and password hash, closing the gap after the service precheck.
func (r *UserStore) UpdateEmailWithAliasGuard(
	ctx context.Context,
	userID int64,
	email string,
	passwordHash string,
) error {
	if userID <= 0 {
		return identitycore.ErrUserNotFound
	}
	if strings.TrimSpace(email) == "" || passwordHash == "" {
		return fmt.Errorf("email identity update requires email and password hash")
	}
	tx := dbent.TxFromContext(ctx)
	if tx == nil {
		return fmt.Errorf("email identity update requires a transaction")
	}
	client := tx.Client()

	releaseEmailLock, err := IdentityLockRepositoryScopedKeys(
		ctx,
		client,
		IdentityTxAwareSQLExecutor(ctx, r.sql, r.client),
		IdentityNormalizedEmailUniquenessLockKey(email),
		IdentityEmailAliasUniquenessLockKey(email),
	)
	if err != nil {
		return err
	}
	defer releaseEmailLock()

	ownerID, exists, err := IdentityEmailAliasOwnerIDWithClient(ctx, client, email, userID)
	if err != nil {
		return err
	}
	if exists && ownerID != userID {
		return identitycore.ErrEmailExists
	}

	if _, err := client.User.UpdateOneID(userID).
		SetEmail(email).
		SetPasswordHash(passwordHash).
		Save(ctx); err != nil {
		return translatePersistenceError(err, identitycore.ErrUserNotFound, identitycore.ErrEmailExists)
	}
	return nil
}

func (r *UserStore) ExistsByNormalizedEmail(ctx context.Context, normalizedEmail string) (bool, error) {
	return r.IdentityExistsByNormalizedEmail(ctx, normalizedEmail, 0)
}

func (r *UserStore) ExistsByNormalizedEmailExcluding(ctx context.Context, normalizedEmail string, excludedUserID int64) (bool, error) {
	return r.IdentityExistsByNormalizedEmail(ctx, normalizedEmail, excludedUserID)
}

func (r *UserStore) IdentityExistsByNormalizedEmail(ctx context.Context, normalizedEmail string, excludedUserID int64) (bool, error) {
	sqlq := r.IdentitySqlExecutorFromContext(ctx)
	if sqlq == nil {
		return false, fmt.Errorf("sql executor is not configured")
	}

	var exists bool
	query := fmt.Sprintf(`
		SELECT EXISTS(
			SELECT 1
			FROM users
			WHERE deleted_at IS NULL
			  AND %s = $1
			  AND ($2 = 0 OR id <> $2)
		)
	`, IdentityNormalizedUserEmailSQL)
	if err := scanSingleRow(ctx, sqlq, query, []any{normalizedEmail, excludedUserID}, &exists); err != nil {
		return false, err
	}
	return exists, nil
}

func (r *UserStore) LockRegistrationEmail(ctx context.Context, normalizedEmail string) error {
	if normalizedEmail == "" {
		return nil
	}
	if r.client != nil && r.client.Driver().Dialect() != dialect.Postgres {
		return nil
	}

	sqlq := r.IdentitySqlExecutorFromContext(ctx)
	if sqlq == nil {
		return fmt.Errorf("sql executor is not configured")
	}
	_, err := sqlq.ExecContext(
		ctx,
		"SELECT pg_advisory_xact_lock($1, hashtext($2))",
		IdentityRegistrationEmailLockNamespace,
		normalizedEmail,
	)
	return err
}

func IdentityEnsureNormalizedEmailAvailableWithClient(ctx context.Context, client *dbent.Client, userID int64, email string) error {
	client = clientFromContext(ctx, client)
	if client == nil {
		return nil
	}

	matches, err := client.User.Query().
		Where(IdentityUserEmailLookupPredicate(email)).
		All(ctx)
	if err != nil {
		return err
	}
	for _, match := range matches {
		if match.ID != userID {
			return identitycore.ErrEmailExists
		}
	}
	return nil
}

func IdentityUserEmailLookupPredicate(email string) predicate.User {
	normalized := IdentityNormalizeEmailLookupValue(email)
	if normalized == "" {
		return dbuser.EmailEQ(email)
	}
	return predicate.User(func(s *entsql.Selector) {
		s.Where(entsql.P(func(b *entsql.Builder) {
			b.WriteString("LOWER(TRIM(").
				Ident(s.C(dbuser.FieldEmail)).
				WriteString(")) = ").
				Arg(normalized)
		}))
	})
}

func IdentityNormalizeEmailLookupValue(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func IdentityNormalizedEmailUniquenessLockKey(email string) string {
	normalized := IdentityNormalizeEmailLookupValue(email)
	if normalized == "" {
		return ""
	}
	return "users:normalized-email:" + normalized
}

// IdentityEmailAliasUniquenessLockKey serializes changes across aliases of the same mailbox.
func IdentityEmailAliasUniquenessLockKey(email string) string {
	identity := identitycore.NormalizeEmailForAliasDedup(email)
	if identity == "" {
		return ""
	}
	return "users:email-alias-identity:" + identity
}

func IdentityRegistrationEmailDomainLockKey(domain string) string {
	domain = IdentityNormalizeEmailDomain(domain)
	if domain == "" {
		return ""
	}
	return "users:registration-email-domain:" + domain
}

func IdentityNormalizeEmailDomain(domain string) string {
	return identitycore.NormalizeRegistrationEmailDomain(domain)
}

func IdentityCountUsersByEmailDomainWithClient(ctx context.Context, client *dbent.Client, domain string) (int, error) {
	client = clientFromContext(ctx, client)
	domain = IdentityNormalizeEmailDomain(domain)
	if client == nil || domain == "" {
		return 0, nil
	}
	return client.User.Query().Where(IdentityUserEmailDomainPredicate(domain)).Count(ctx)
}

func IdentityUserEmailDomainPredicate(domain string) predicate.User {
	domain = IdentityNormalizeEmailDomain(domain)
	escapedDomain := escapeLikePattern(domain)
	exactPattern := "%@" + escapedDomain
	subdomainPattern := "%@%." + escapedDomain
	return predicate.User(func(s *entsql.Selector) {
		s.Where(entsql.P(func(b *entsql.Builder) {
			b.WriteString("(RTRIM(LOWER(TRIM(").
				Ident(s.C(dbuser.FieldEmail)).
				WriteString(")), '.') LIKE ").
				Arg(exactPattern).
				WriteString(` ESCAPE '\' OR RTRIM(LOWER(TRIM(`).
				Ident(s.C(dbuser.FieldEmail)).
				WriteString(")), '.') LIKE ").
				Arg(subdomainPattern).
				WriteString(` ESCAPE '\'`).
				WriteString(")")
		}))
	})
}

func (r *UserStore) AddGroupToAllowedGroups(ctx context.Context, userID int64, groupID int64) error {
	client := clientFromContext(ctx, r.client)
	err := client.UserAllowedGroup.Create().
		SetUserID(userID).
		SetGroupID(groupID).
		OnConflictColumns(userallowedgroup.FieldUserID, userallowedgroup.FieldGroupID).
		DoNothing().
		Exec(ctx)
	if isSQLNoRowsError(err) {
		return nil
	}
	return err
}

func (r *UserStore) RemoveGroupFromAllowedGroups(ctx context.Context, groupID int64) (int64, error) {
	// Grants are stored in user_allowed_groups; users.allowed_groups is deprecated.
	affected, err := r.client.UserAllowedGroup.Delete().
		Where(userallowedgroup.GroupIDEQ(groupID)).
		Exec(ctx)
	if err != nil {
		return 0, err
	}
	return int64(affected), nil
}

// RemoveGroupFromUserAllowedGroups revokes one user's access to the given group.
func (r *UserStore) RemoveGroupFromUserAllowedGroups(ctx context.Context, userID int64, groupID int64) error {
	client := clientFromContext(ctx, r.client)
	_, err := client.UserAllowedGroup.Delete().
		Where(userallowedgroup.UserIDEQ(userID), userallowedgroup.GroupIDEQ(groupID)).
		Exec(ctx)
	return err
}

func (r *UserStore) GetFirstAdmin(ctx context.Context) (*identitycore.User, error) {
	m, err := r.client.User.Query().
		Where(
			dbuser.RoleEQ(identitycore.RoleAdmin),
			dbuser.StatusEQ(identitycore.StatusActive),
		).
		Order(dbent.Asc(dbuser.FieldID)).
		First(ctx)
	if err != nil {
		return nil, translatePersistenceError(err, identitycore.ErrUserNotFound, nil)
	}

	out := UserFromEntity(m)
	groups, err := r.IdentityLoadAllowedGroups(ctx, []int64{m.ID})
	if err != nil {
		return nil, err
	}
	if v, ok := groups[m.ID]; ok {
		out.AllowedGroups = v
	}
	disabledPublicGroups, err := r.IdentityLoadDisabledPublicGroups(ctx, []int64{m.ID})
	if err != nil {
		return nil, err
	}
	if v, ok := disabledPublicGroups[m.ID]; ok {
		out.DisabledPublicGroups = v
	}
	out.GroupRestrictionsLoaded = true
	return out, nil
}

func (r *UserStore) IdentityLoadAllowedGroups(ctx context.Context, userIDs []int64) (map[int64][]int64, error) {
	out := make(map[int64][]int64, len(userIDs))
	if len(userIDs) == 0 {
		return out, nil
	}

	rows, err := r.client.UserAllowedGroup.Query().
		Where(userallowedgroup.UserIDIn(userIDs...)).
		All(ctx)
	if err != nil {
		return nil, err
	}

	for i := range rows {
		out[rows[i].UserID] = append(out[rows[i].UserID], rows[i].GroupID)
	}

	for userID := range out {
		slices.Sort(out[userID])
	}

	return out, nil
}

func (r *UserStore) IdentityLoadDisabledPublicGroups(ctx context.Context, userIDs []int64) (map[int64][]int64, error) {
	out := make(map[int64][]int64, len(userIDs))
	if len(userIDs) == 0 {
		return out, nil
	}

	rows, err := r.client.UserDisabledPublicGroup.Query().
		Where(userdisabledpublicgroup.UserIDIn(userIDs...)).
		All(ctx)
	if err != nil {
		return nil, err
	}

	for i := range rows {
		out[rows[i].UserID] = append(out[rows[i].UserID], rows[i].GroupID)
	}

	for userID := range out {
		slices.Sort(out[userID])
	}

	return out, nil
}

// IdentitySyncUserAllowedGroupsWithClient synchronizes grants using the supplied Ent client.
// Grants are stored in user_allowed_groups; users.allowed_groups is deprecated.
func (r *UserStore) IdentitySyncUserAllowedGroupsWithClient(ctx context.Context, client *dbent.Client, userID int64, groupIDs []int64) error {
	if client == nil {
		return nil
	}

	existingRows, err := client.UserAllowedGroup.Query().
		Where(userallowedgroup.UserIDEQ(userID)).
		All(ctx)
	if err != nil {
		return err
	}

	desired := make(map[int64]struct{}, len(groupIDs))
	for _, id := range groupIDs {
		if id <= 0 {
			continue
		}
		desired[id] = struct{}{}
	}

	existing := make(map[int64]struct{}, len(existingRows))
	removed := make([]int64, 0)
	for _, row := range existingRows {
		existing[row.GroupID] = struct{}{}
		if _, keep := desired[row.GroupID]; !keep {
			removed = append(removed, row.GroupID)
		}
	}
	if len(removed) > 0 {
		if _, err := client.UserAllowedGroup.Delete().
			Where(userallowedgroup.UserIDEQ(userID), userallowedgroup.GroupIDIn(removed...)).
			Exec(ctx); err != nil {
			return err
		}
	}

	creates := make([]*dbent.UserAllowedGroupCreate, 0, len(desired))
	for groupID := range desired {
		if _, present := existing[groupID]; !present {
			creates = append(creates, client.UserAllowedGroup.Create().SetUserID(userID).SetGroupID(groupID))
		}
	}
	if len(creates) > 0 {
		if err := client.UserAllowedGroup.
			CreateBulk(creates...).
			OnConflictColumns(userallowedgroup.FieldUserID, userallowedgroup.FieldGroupID).
			DoNothing().
			Exec(ctx); err != nil {
			if isSQLNoRowsError(err) {
				return nil
			}
			return err
		}
	}

	return nil
}

// IdentitySyncUserDisabledPublicGroupsWithClient replaces the user's disabled public groups.
// Only groups verified as public are inserted.
func (r *UserStore) IdentitySyncUserDisabledPublicGroupsWithClient(ctx context.Context, client *dbent.Client, userID int64, groupIDs []int64) error {
	if client == nil {
		return nil
	}

	if _, err := client.UserDisabledPublicGroup.Delete().Where(userdisabledpublicgroup.UserIDEQ(userID)).Exec(ctx); err != nil {
		return err
	}

	unique := make(map[int64]struct{}, len(groupIDs))
	for _, id := range groupIDs {
		if id <= 0 {
			continue
		}
		unique[id] = struct{}{}
	}
	if len(unique) == 0 {
		return nil
	}

	candidateIDs := make([]int64, 0, len(unique))
	for groupID := range unique {
		candidateIDs = append(candidateIDs, groupID)
	}
	slices.Sort(candidateIDs)

	publicIDs, err := client.Group.Query().
		Where(
			dbgroup.IDIn(candidateIDs...),
			dbgroup.IsExclusiveEQ(false),
		).
		IDs(ctx)
	if err != nil {
		return err
	}
	publicSet := make(map[int64]struct{}, len(publicIDs))
	for _, groupID := range publicIDs {
		publicSet[groupID] = struct{}{}
	}

	creates := make([]*dbent.UserDisabledPublicGroupCreate, 0, len(publicIDs))
	for _, groupID := range candidateIDs {
		if _, ok := publicSet[groupID]; !ok {
			continue
		}
		creates = append(creates, client.UserDisabledPublicGroup.Create().SetUserID(userID).SetGroupID(groupID))
	}
	if len(creates) == 0 {
		return nil
	}
	if err := client.UserDisabledPublicGroup.
		CreateBulk(creates...).
		OnConflictColumns(userdisabledpublicgroup.FieldUserID, userdisabledpublicgroup.FieldGroupID).
		DoNothing().
		Exec(ctx); err != nil {
		if isSQLNoRowsError(err) {
			return nil
		}
		return err
	}

	return nil
}

func IdentityApplyUserEntityToService(dst *identitycore.User, src *dbent.User) {
	if dst == nil || src == nil {
		return
	}
	dst.ID = src.ID
	dst.SignupSource = src.SignupSource
	dst.LastLoginAt = src.LastLoginAt
	dst.LastActiveAt = src.LastActiveAt
	dst.CreatedAt = src.CreatedAt
	dst.UpdatedAt = src.UpdatedAt
}

func (r *UserStore) IdentitySqlExecutorFromContext(ctx context.Context) SQLExecutor {
	if tx := dbent.TxFromContext(ctx); tx != nil {
		return tx.Client()
	}
	return r.sql
}

// IdentityDeductUserBalance delegates balance deductions to billing.
func IdentityDeductUserBalance(ctx context.Context, q sqlQueryer, userID int64, amount float64) (float64, float64, error) {
	return billingpostgres.DeductAvailableBalance(ctx, q, userID, amount)
}

func (r *UserStore) IdentityUpdateWithClient(ctx context.Context, client *dbent.Client, userIn *identitycore.User, fields identitycore.UserUpdateFields) (*dbent.User, error) {
	updateOp := client.User.UpdateOneID(userIn.ID)
	if fields.Email {
		updateOp = updateOp.SetEmail(userIn.Email)
	}
	if fields.PreferredLocale {
		if userIn.PreferredLocale == nil {
			updateOp = updateOp.ClearPreferredLocale()
		} else {
			updateOp = updateOp.SetPreferredLocale(*userIn.PreferredLocale)
		}
	}
	if fields.Username {
		updateOp = updateOp.SetUsername(userIn.Username)
	}
	if fields.Notes {
		updateOp = updateOp.SetNotes(userIn.Notes)
	}
	if fields.PasswordHash {
		updateOp = updateOp.SetPasswordHash(userIn.PasswordHash)
	}
	if fields.Role {
		updateOp = updateOp.SetRole(userIn.Role)
	}
	if fields.Concurrency {
		updateOp = updateOp.SetConcurrency(userIn.Concurrency)
	}
	if fields.Status {
		updateOp = updateOp.SetStatus(userIn.Status)
	}
	if fields.SignupSource {
		updateOp = updateOp.SetSignupSource(IdentityUserSignupSourceOrDefault(userIn.SignupSource))
	}
	if fields.LastLoginAt {
		if userIn.LastLoginAt != nil {
			updateOp = updateOp.SetLastLoginAt(*userIn.LastLoginAt)
		} else {
			updateOp = updateOp.ClearLastLoginAt()
		}
	}
	if fields.LastActiveAt {
		if userIn.LastActiveAt != nil {
			updateOp = updateOp.SetLastActiveAt(*userIn.LastActiveAt)
		} else {
			updateOp = updateOp.ClearLastActiveAt()
		}
	}
	if fields.RPMLimit {
		updateOp = updateOp.SetRpmLimit(userIn.RPMLimit)
	}
	if fields.APIKeyLimit {
		updateOp = updateOp.SetAPIKeyLimit(userIn.APIKeyLimit)
	}
	if fields.BalanceNotifySettings {
		updateOp = updateOp.
			SetBalanceNotifyEnabled(userIn.BalanceNotifyEnabled).
			SetBalanceNotifyThresholdType(userIn.BalanceNotifyThresholdType)
		if userIn.BalanceNotifyThreshold != nil {
			updateOp = updateOp.SetBalanceNotifyThreshold(*userIn.BalanceNotifyThreshold)
		} else {
			updateOp = updateOp.ClearBalanceNotifyThreshold()
		}
	}
	if fields.BalanceNotifyExtraEmails {
		updateOp = updateOp.SetBalanceNotifyExtraEmails(IdentityMarshalExtraEmails(userIn.BalanceNotifyExtraEmails))
	}
	return updateOp.Save(ctx)
}

func (r *UserStore) IdentityCreateWithClient(ctx context.Context, client *dbent.Client, userIn *identitycore.User) (*dbent.User, error) {
	createOp := client.User.Create().
		SetEmail(userIn.Email).
		SetUsername(userIn.Username).
		SetNotes(userIn.Notes).
		SetPasswordHash(userIn.PasswordHash).
		SetRole(userIn.Role).
		SetConcurrency(userIn.Concurrency).
		SetStatus(userIn.Status).
		SetSignupSource(IdentityUserSignupSourceOrDefault(userIn.SignupSource)).
		SetNillableLastLoginAt(userIn.LastLoginAt).
		SetNillableLastActiveAt(userIn.LastActiveAt).
		SetRpmLimit(userIn.RPMLimit).
		SetAPIKeyLimit(userIn.APIKeyLimit)
	billingpostgres.ApplyInitialUserFunds(createOp, billing.InitialUserFunds{Balance: userIn.Balance})
	return createOp.Save(ctx)
}

func IdentityUserSignupSourceOrDefault(signupSource string) string {
	switch strings.TrimSpace(strings.ToLower(signupSource)) {
	case "", "email":
		return "email"
	case "linuxdo", "wechat", "oidc", "github", "google", "dingtalk":
		return strings.TrimSpace(strings.ToLower(signupSource))
	default:
		return "email"
	}
}

// IdentityMarshalExtraEmails encodes notification email entries as storage JSON.
func IdentityMarshalExtraEmails(entries []identitycore.NotifyEmailEntry) string {
	return identitycore.MarshalNotifyEmails(entries)
}

// UpdateTotpSecret replaces or clears the user's encrypted TOTP secret.
func (r *UserStore) UpdateTotpSecret(ctx context.Context, userID int64, encryptedSecret *string) error {
	client := clientFromContext(ctx, r.client)
	update := client.User.UpdateOneID(userID)
	if encryptedSecret == nil {
		update = update.ClearTotpSecretEncrypted()
	} else {
		update = update.SetTotpSecretEncrypted(*encryptedSecret)
	}
	_, err := update.Save(ctx)
	if err != nil {
		return translatePersistenceError(err, identitycore.ErrUserNotFound, nil)
	}
	return nil
}

// EnableTotp enables TOTP and records the activation time.
func (r *UserStore) EnableTotp(ctx context.Context, userID int64) error {
	client := clientFromContext(ctx, r.client)
	_, err := client.User.UpdateOneID(userID).
		SetTotpEnabled(true).
		SetTotpEnabledAt(time.Now()).
		Save(ctx)
	if err != nil {
		return translatePersistenceError(err, identitycore.ErrUserNotFound, nil)
	}
	return nil
}

// DisableTotp disables TOTP and clears its secret and activation time.
func (r *UserStore) DisableTotp(ctx context.Context, userID int64) error {
	client := clientFromContext(ctx, r.client)
	_, err := client.User.UpdateOneID(userID).
		SetTotpEnabled(false).
		ClearTotpEnabledAt().
		ClearTotpSecretEncrypted().
		Save(ctx)
	if err != nil {
		return translatePersistenceError(err, identitycore.ErrUserNotFound, nil)
	}
	return nil
}

// IdentityBillingBalance uses the Ent transaction from ctx for balance operations when available.
func (r *UserStore) IdentityBillingBalance(ctx context.Context) *billingpostgres.BalanceStore {
	if tx := dbent.TxFromContext(ctx); tx != nil {
		return billingpostgres.BalanceInTx(tx)
	}
	return billingpostgres.NewBalanceStore(r.client)
}

// clientFromContext returns the Ent transaction client from ctx or the default client.
func clientFromContext(ctx context.Context, defaultClient *dbent.Client) *dbent.Client {
	if tx := dbent.TxFromContext(ctx); tx != nil {
		return tx.Client()
	}
	return defaultClient
}

// translatePersistenceError maps missing records and unique conflicts to supplied application errors.
// A nil target skips that mapping; unmatched errors are returned unchanged.
func translatePersistenceError(err error, notFound, conflict *infraerrors.ApplicationError) error {
	if err == nil {
		return nil
	}

	// Both Ent NotFoundError and sql.ErrNoRows indicate a missing record.
	if notFound != nil && (errors.Is(err, sql.ErrNoRows) || dbent.IsNotFound(err)) {
		return notFound.WithCause(err)
	}

	if conflict != nil && isUniqueConstraintViolation(err) {
		return conflict.WithCause(err)
	}

	return err
}

// isUniqueConstraintViolation recognizes PostgreSQL code 23505 and supported message patterns.
func isUniqueConstraintViolation(err error) bool {
	return postgresinfra.IsUniqueConstraintViolation(err)
}

// isSQLNoRowsError reports whether the SQL operation found no record.
func isSQLNoRowsError(err error) bool { return postgresinfra.IsNoRows(err) }

// scanSingleRow queries one row and scans its columns into dest.
func scanSingleRow(ctx context.Context, q sqlQueryer, query string, args []any, dest ...any) error {
	return postgresinfra.ScanSingleRow(ctx, q, query, args, dest...)
}

// escapeLikePattern escapes backslashes, percent signs, and underscores in LIKE/ILIKE patterns.
// PostgreSQL uses backslash as the default pattern escape character.
func escapeLikePattern(s string) string {
	return likePatternReplacer.Replace(s)
}
