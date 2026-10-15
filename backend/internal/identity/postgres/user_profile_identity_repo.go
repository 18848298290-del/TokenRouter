package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"hash/fnv"
	"maps"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unsafe"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/authidentity"
	"github.com/TokenFlux/TokenRouter/ent/authidentitychannel"
	"github.com/TokenFlux/TokenRouter/ent/identityadoptiondecision"
	dbpredicate "github.com/TokenFlux/TokenRouter/ent/predicate"
	identitycore "github.com/TokenFlux/TokenRouter/internal/identity"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// user_profile_identity_repo.go persists provider identities, channel
// identities, profile-adoption decisions, and avatar records.
// Provider account identity uses a canonical type/key/subject tuple, with
// separate channel tuples for application-scoped subjects. Binding can reuse
// compatible WeChat keys while retaining stored row IDs, and profile adoption
// links one saved decision to a pending session and optionally one provider
// identity.
//
// Account identity writes join a supplied Ent transaction or own one through
// WithUserProfileIdentityTx. Sorted process key reservations and PostgreSQL
// advisory locks serialize callers sharing those exact keys; transaction
// completion owns database lock lifetime. Metadata copies replace the outer
// map while retaining nested values. Raw avatar and grant SQL prefer the
// injected transaction driver when it supports the query interface, then use
// configured fallbacks; driver extraction depends on the current generated Ent
// config layout.

// Provider grant reason labels distinguish signup entitlements from
// first-binding entitlements in the unique account/provider/reason claim
// stored by RecordProviderGrant.
const (
	ProviderGrantReasonSignup    ProviderGrantReason = "signup"     // account creation entitlement claim
	ProviderGrantReasonFirstBind ProviderGrantReason = "first_bind" // initial provider binding claim
)

// Identity and channel errors expose ownership/provider mismatch failures,
// while the shared key registry retains reservations across overlapping
// account identity and adoption writes.
var (
	ErrAuthIdentityOwnershipConflict = infraerrors.Conflict(
		"AUTH_IDENTITY_OWNERSHIP_CONFLICT",
		"auth identity already belongs to another user",
	)
	ErrAuthIdentityChannelOwnershipConflict = infraerrors.Conflict(
		"AUTH_IDENTITY_CHANNEL_OWNERSHIP_CONFLICT",
		"auth identity channel already belongs to another user",
	)
	ErrAuthIdentityChannelProviderMismatch = infraerrors.BadRequest(
		"AUTH_IDENTITY_CHANNEL_PROVIDER_MISMATCH",
		"auth identity channel provider must match canonical identity",
	)

	IdentityRepositoryScopedKeyLocks = IdentityNewScopedKeyLockRegistry()
)

// ProviderGrantReason identifies the account event whose entitlement grant
// claim is being recorded.
type ProviderGrantReason string

// AuthIdentityKey identifies one stable provider subject inside its provider
// namespace.
type AuthIdentityKey struct {
	// ProviderType, ProviderKey, and ProviderSubject select one provider identity
	// after caller trimming. ProviderKey supplies its namespace, so the same
	// subject text can identify different accounts under different keys.
	ProviderType    string // provider identity type label
	ProviderKey     string // provider issuer or namespace
	ProviderSubject string // stable external provider subject
}

// AuthIdentityChannelKey identifies a provider subject scoped to channel and
// application.
type AuthIdentityChannelKey struct {
	// ProviderType, ProviderKey, Channel, ChannelAppID, and ChannelSubject form
	// the channel lookup tuple. Application-scoped subjects are resolved with all
	// of those fields before their canonical identity owner is used.
	ProviderType   string // provider identity type label
	ProviderKey    string // provider issuer or namespace
	Channel        string // provider channel type label
	ChannelAppID   string // channel application identity text
	ChannelSubject string // channel-scoped external subject text
}

// CreateAuthIdentityInput supplies an account identity, optional channel, and
// verified provider metadata.
type CreateAuthIdentityInput struct {
	UserID    int64           // owning account row identifier
	Canonical AuthIdentityKey // canonical provider identity tuple
	// Channel requests a scoped identity row when present. Its provider type and
	// key must match Canonical; nil skips channel creation and updates.
	Channel *AuthIdentityChannelKey // optional channel identity tuple
	// Issuer and VerifiedAt supply optional identity observations. Creation omits
	// absent values, and binding replaces each existing value only when its
	// corresponding pointer is present.
	Issuer     *string    // optional provider issuer text
	VerifiedAt *time.Time // optional verification observation timestamp
	// Metadata and ChannelMetadata receive new outer maps during creation,
	// retaining nested mutable values. Binding updates existing metadata when the
	// corresponding map is nonnil; callers keep shared nested data stable.
	Metadata        map[string]any // canonical provider metadata map
	ChannelMetadata map[string]any // channel-specific provider metadata map
}

// BindAuthIdentityInput aliases the prepared identity and channel data used by
// account binding.
type BindAuthIdentityInput = CreateAuthIdentityInput

// CreateAuthIdentityResult retains stored identity and optional channel
// entities after a write.
type CreateAuthIdentityResult struct {
	Identity *dbent.AuthIdentity        // stored provider identity entity
	Channel  *dbent.AuthIdentityChannel // optional stored channel entity
}

// UserAuthIdentityLookup retains the account and identity entities loaded by a
// tuple lookup.
type UserAuthIdentityLookup struct {
	User     *dbent.User                // loaded identity account entity
	Identity *dbent.AuthIdentity        // stored provider identity entity
	Channel  *dbent.AuthIdentityChannel // optional stored channel entity
}

// ProviderGrantRecordInput identifies the account/provider/event claim used to
// prevent repeat entitlements.
type ProviderGrantRecordInput struct {
	UserID       int64               // owning account row identifier
	ProviderType string              // provider identity type label
	GrantReason  ProviderGrantReason // recorded entitlement event label
}

// IdentityAdoptionDecisionInput supplies the pending profile choices and
// optional provider reference.
type IdentityAdoptionDecisionInput struct {
	PendingAuthSessionID int64 // owning pending session identifier
	// IdentityID selects the adopted identity when positive. Nil or nonpositive
	// input skips reference clearing and the explicit identity setter during upsert.
	IdentityID       *int64 // optional adopted provider reference
	AdoptDisplayName bool   // provider-name adoption choice flag
	AdoptAvatar      bool   // provider-avatar adoption choice flag
}

// IdentitySqlQueryExecutor supplies raw account-profile and identity query
// operations for selected storage.
type IdentitySqlQueryExecutor interface {
	// ExecContext executes raw account identity statements and returns their SQL
	// result. The selected driver or fallback executor supplies transaction
	// participation, and callers retain its required affected-row and failure
	// checks.
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	// QueryContext queries raw account identity rows for caller decoding and
	// cleanup. The selected driver or fallback executor supplies transaction
	// participation; successful callers close the result and inspect
	// scan/iteration failures.
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// IdentityScopedKeyLockRegistry retains one mutex entry per reserved
// normalized identity key.
type IdentityScopedKeyLockRegistry struct {
	// mu guards locks and every entry reservation count. Holders and waiters
	// reserve entries before blocking on key mutexes, so another release cannot
	// replace a key entry while it is queued.
	mu    sync.Mutex                             // registry entry lifetime mutex
	locks map[string]*IdentityScopedKeyLockEntry // reserved normalized key entries
}

// IdentityScopedKeyLockEntry tracks one key mutex and its holder plus waiter
// reservations.
type IdentityScopedKeyLockEntry struct {
	// mu serializes its key operation. refs counts holders and waiters under the
	// registry mutex, retaining that entry until the final reservation is
	// released.
	mu   sync.Mutex // per-key operation mutex
	refs int        // holder and waiter reservations
}

// IdentityRef returns scalar canonical tuple values from the retained identity
// entity. Nil result or identity returns an empty tuple, allowing callers to
// handle an absent stored result.
func (r *CreateAuthIdentityResult) IdentityRef() AuthIdentityKey {
	if r == nil || r.Identity == nil {
		return AuthIdentityKey{}
	}
	return AuthIdentityKey{
		ProviderType:    r.Identity.ProviderType,
		ProviderKey:     r.Identity.ProviderKey,
		ProviderSubject: r.Identity.ProviderSubject,
	}
}

// ChannelRef returns a new scalar channel tuple from the retained entity. Nil
// result or channel returns nil, letting callers distinguish absent channel
// storage from a populated tuple.
func (r *CreateAuthIdentityResult) ChannelRef() *AuthIdentityChannelKey {
	if r == nil || r.Channel == nil {
		return nil
	}
	return &AuthIdentityChannelKey{
		ProviderType:   r.Channel.ProviderType,
		ProviderKey:    r.Channel.ProviderKey,
		Channel:        r.Channel.Channel,
		ChannelAppID:   r.Channel.ChannelAppID,
		ChannelSubject: r.Channel.ChannelSubject,
	}
}

// IdentityNewScopedKeyLockRegistry creates an empty reservation registry for
// account identity key mutexes.
func IdentityNewScopedKeyLockRegistry() *IdentityScopedKeyLockRegistry {
	return &IdentityScopedKeyLockRegistry{
		locks: make(map[string]*IdentityScopedKeyLockEntry),
	}
}

// lock reserves normalized keys and acquires their mutexes in sorted order
// before returning a release callback. Empty key selection returns a callback
// with no effect.
//
// Reservations increase under the registry mutex before a caller blocks,
// keeping holders and waiters on the same key entry. Sorted acquisition
// prevents opposite-order overlapping sets from waiting on each other. Release
// unlocks in reverse order then removes only unreferenced entries. Invoke a
// nonempty release once; these mutex waits do not observe request
// cancellation.
func (r *IdentityScopedKeyLockRegistry) lock(keys ...string) func() {
	normalized := IdentityNormalizeLockKeys(keys...)
	if len(normalized) == 0 {
		return func() {}
	}

	entries := make([]*IdentityScopedKeyLockEntry, 0, len(normalized))
	// Reserve entries before waiting on their mutexes, counting both holders and
	// waiters. This prevents release from deleting a key entry while a queued
	// caller still references it.
	r.mu.Lock()
	for _, key := range normalized {
		entry := r.locks[key]
		if entry == nil {
			entry = &IdentityScopedKeyLockEntry{}
			r.locks[key] = entry
		}
		entry.refs++
		entries = append(entries, entry)
	}
	r.mu.Unlock()

	// The normalized order makes overlapping callers acquire their shared keys
	// in the same direction. This prevents a writer holding one key from reversing
	// another writer's acquisition order while both entries remain reserved.
	for _, entry := range entries {
		entry.mu.Lock()
	}

	// Unlock held entries before decrementing their registry reservations. A
	// remaining waiter keeps the same entry alive until the final callback
	// removes its zero-reference key.
	return func() {
		for _, entrie := range slices.Backward(entries) {
			entrie.mu.Unlock()
		}

		r.mu.Lock()
		defer r.mu.Unlock()
		for idx, key := range normalized {
			entry := entries[idx]
			entry.refs--
			if entry.refs == 0 {
				delete(r.locks, key)
			}
		}
	}
}

// IdentityNormalizeLockKeys returns sorted unique nonblank keys after trimming
// caller values. Empty or wholly blank selections return nil, so locking can
// skip them before reserving any entries.
func IdentityNormalizeLockKeys(keys ...string) []string {
	if len(keys) == 0 {
		return nil
	}

	deduped := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		trimmed := strings.TrimSpace(key)
		if trimmed == "" {
			continue
		}
		deduped[trimmed] = struct{}{}
	}
	if len(deduped) == 0 {
		return nil
	}

	normalized := make([]string, 0, len(deduped))
	for key := range deduped {
		normalized = append(normalized, key)
	}
	sort.Strings(normalized)
	return normalized
}

// IdentityAdvisoryLockHash returns signed FNV-1a key text hashing for
// PostgreSQL advisory lock selection.
func IdentityAdvisoryLockHash(key string) int64 {
	hasher := fnv.New64a()
	// FNV-1a accepts every input byte and its Write implementation returns no
	// error. The signed conversion retains digest bits for the PostgreSQL
	// transaction advisory-lock key.
	_, _ = hasher.Write([]byte(key))
	return int64(hasher.Sum64())
}

// IdentityLockRepositoryScopedKeys reserves process keys and optionally
// obtains matching PostgreSQL transaction advisory locks. The returned release
// callback handles process reservations, while caller transaction completion
// releases database locks.
//
// Missing keys, client, raw executor, or a non-PostgreSQL dialect skips
// database acquisition. Keys are queried in sorted order after local
// reservations. Query failure releases process locks before returning but
// leaves already obtained advisory locks with their transaction. Result rows
// are closed after the acquisition effect, with close errors discarded.
// Callers use the same transaction executor for their subsequent guarded
// writes.
func IdentityLockRepositoryScopedKeys(ctx context.Context, client *dbent.Client, exec IdentitySqlQueryExecutor, keys ...string) (func(), error) {
	release := IdentityRepositoryScopedKeyLocks.lock(keys...)
	normalized := IdentityNormalizeLockKeys(keys...)
	if len(normalized) == 0 || client == nil || exec == nil || client.Driver().Dialect() != dialect.Postgres {
		return release, nil
	}

	for _, key := range normalized {
		rows, err := exec.QueryContext(ctx, "SELECT pg_advisory_xact_lock($1)", IdentityAdvisoryLockHash(key))
		if err != nil {
			release()
			return nil, err
		}
		// Advisory-lock acquisition completed before this unused row result closes.
		// Discard its cleanup failure while the database lock remains owned by the
		// active transaction.
		_ = rows.Close()
	}
	return release, nil
}

// WithUserProfileIdentityTx invokes the supplied nonnil callback in the
// injected Ent transaction or a newly owned one. Existing transaction callers
// retain completion responsibility.
//
// When this method owns a transaction, the callback receives its injected
// context before any identity/profile stages run. A callback failure returns
// before commit and reaches deferred rollback; success commits its selected
// writes. Rollback cleanup errors are discarded, and callback effects outside
// that transaction retain their own failure and compensation rules.
func (r *UserStore) WithUserProfileIdentityTx(ctx context.Context, fn func(txCtx context.Context) error) error {
	if dbent.TxFromContext(ctx) != nil {
		return fn(ctx)
	}

	tx, err := r.client.Tx(ctx)
	if err != nil {
		return err
	}
	// Rollback earlier identity/profile callback writes when a later stage fails.
	// Cleanup errors remain secondary, and callback effects outside this
	// transaction keep their caller-owned recovery scope.
	defer func() { _ = tx.Rollback() }()

	txCtx := dbent.NewTxContext(ctx, tx)
	if err := fn(txCtx); err != nil {
		return err
	}
	// Commit the callback selected identity and profile writes after its
	// successful return, while an injected transaction path keeps database
	// completion with the external caller.
	return tx.Commit()
}

// CreateAuthIdentity stores a trimmed canonical provider tuple and optional
// matching channel using the selected client. Caller validation ensures the
// prepared account and subjects; channel provider fields must match canonical
// fields after trimming.
//
// Identity creation precedes optional channel creation and returns their
// stored entities. This method creates no separate transaction, so callers
// needing combined persistence supply an injected transaction or
// transaction-bound client. A channel write failure can otherwise follow a
// completed identity insertion. Metadata maps receive outer copies, while
// nested values remain shared during storage encoding.
func (r *UserStore) CreateAuthIdentity(ctx context.Context, input CreateAuthIdentityInput) (*CreateAuthIdentityResult, error) {
	if err := IdentityValidateAuthIdentityChannelProviderMatch(input.Canonical, input.Channel); err != nil {
		return nil, err
	}

	client := clientFromContext(ctx, r.client)

	create := client.AuthIdentity.Create().
		SetUserID(input.UserID).
		SetProviderType(strings.TrimSpace(input.Canonical.ProviderType)).
		SetProviderKey(strings.TrimSpace(input.Canonical.ProviderKey)).
		SetProviderSubject(strings.TrimSpace(input.Canonical.ProviderSubject)).
		SetMetadata(IdentityCopyMetadata(input.Metadata)).
		SetNillableIssuer(input.Issuer).
		SetNillableVerifiedAt(input.VerifiedAt)

	identity, err := create.Save(ctx)
	if err != nil {
		return nil, err
	}

	var channel *dbent.AuthIdentityChannel
	if input.Channel != nil {
		channel, err = client.AuthIdentityChannel.Create().
			SetIdentityID(identity.ID).
			SetProviderType(strings.TrimSpace(input.Channel.ProviderType)).
			SetProviderKey(strings.TrimSpace(input.Channel.ProviderKey)).
			SetChannel(strings.TrimSpace(input.Channel.Channel)).
			SetChannelAppID(strings.TrimSpace(input.Channel.ChannelAppID)).
			SetChannelSubject(strings.TrimSpace(input.Channel.ChannelSubject)).
			SetMetadata(IdentityCopyMetadata(input.ChannelMetadata)).
			Save(ctx)
		if err != nil {
			return nil, err
		}
	}

	return &CreateAuthIdentityResult{Identity: identity, Channel: channel}, nil
}

// GetUserByCanonicalIdentity reads the exact trimmed canonical tuple and
// eagerly loads its account. Ent lookup errors remain errors, and returned
// entities retain the loaded values for caller lifecycle and ownership
// decisions.
func (r *UserStore) GetUserByCanonicalIdentity(ctx context.Context, key AuthIdentityKey) (*UserAuthIdentityLookup, error) {
	identity, err := clientFromContext(ctx, r.client).AuthIdentity.Query().
		Where(
			authidentity.ProviderTypeEQ(strings.TrimSpace(key.ProviderType)),
			authidentity.ProviderKeyEQ(strings.TrimSpace(key.ProviderKey)),
			authidentity.ProviderSubjectEQ(strings.TrimSpace(key.ProviderSubject)),
		).
		WithUser().
		Only(ctx)
	if err != nil {
		return nil, err
	}

	return &UserAuthIdentityLookup{
		User:     identity.Edges.User,
		Identity: identity,
	}, nil
}

// GetUserByChannelIdentity reads the complete trimmed channel tuple and loads
// its identity and account. Ent lookup failures remain errors; callers
// validate the loaded account state before using that ownership result.
func (r *UserStore) GetUserByChannelIdentity(ctx context.Context, key AuthIdentityChannelKey) (*UserAuthIdentityLookup, error) {
	channel, err := clientFromContext(ctx, r.client).AuthIdentityChannel.Query().
		Where(
			authidentitychannel.ProviderTypeEQ(strings.TrimSpace(key.ProviderType)),
			authidentitychannel.ProviderKeyEQ(strings.TrimSpace(key.ProviderKey)),
			authidentitychannel.ChannelEQ(strings.TrimSpace(key.Channel)),
			authidentitychannel.ChannelAppIDEQ(strings.TrimSpace(key.ChannelAppID)),
			authidentitychannel.ChannelSubjectEQ(strings.TrimSpace(key.ChannelSubject)),
		).
		WithIdentity(func(q *dbent.AuthIdentityQuery) {
			q.WithUser()
		}).
		Only(ctx)
	if err != nil {
		return nil, err
	}

	return &UserAuthIdentityLookup{
		User:     channel.Edges.Identity.Edges.User,
		Identity: channel.Edges.Identity,
		Channel:  channel,
	}, nil
}

// ListUserAuthIdentities returns stored provider summaries for the selected
// account with trimmed tuple text. Metadata receives new outer maps, while
// issuer and verification pointers and nested metadata values remain shared.
func (r *UserStore) ListUserAuthIdentities(ctx context.Context, userID int64) ([]identitycore.UserAuthIdentityRecord, error) {
	identities, err := clientFromContext(ctx, r.client).AuthIdentity.Query().
		Where(authidentity.UserIDEQ(userID)).
		All(ctx)
	if err != nil {
		return nil, err
	}

	records := make([]identitycore.UserAuthIdentityRecord, 0, len(identities))
	for _, identity := range identities {
		if identity == nil {
			continue
		}
		records = append(records, identitycore.UserAuthIdentityRecord{
			ProviderType:    strings.TrimSpace(identity.ProviderType),
			ProviderKey:     strings.TrimSpace(identity.ProviderKey),
			ProviderSubject: strings.TrimSpace(identity.ProviderSubject),
			VerifiedAt:      identity.VerifiedAt,
			Issuer:          identity.Issuer,
			Metadata:        IdentityCopyMetadata(identity.Metadata),
			CreatedAt:       identity.CreatedAt,
			UpdatedAt:       identity.UpdatedAt,
		})
	}

	return records, nil
}

// UnbindUserAuthProvider removes all selected provider identities after
// trimming and case folding, rejecting blank or email providers. The caller
// service decides whether enough login identities remain before this storage
// operation.
//
// The selected transaction clears adoption references and deletes channel rows
// before deleting provider identities. A missing identity set succeeds, and
// any later failure relies on the owned or caller transaction rollback. This
// storage operation does not revoke sessions; HTTP and identity callers
// perform their configured post-removal effects separately.
func (r *UserStore) UnbindUserAuthProvider(ctx context.Context, userID int64, provider string) error {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" || provider == "email" {
		return identitycore.ErrIdentityProviderInvalid
	}

	return r.WithUserProfileIdentityTx(ctx, func(txCtx context.Context) error {
		client := clientFromContext(txCtx, r.client)
		identityIDs, err := client.AuthIdentity.Query().
			Where(
				authidentity.UserIDEQ(userID),
				authidentity.ProviderTypeEQ(provider),
			).
			IDs(txCtx)
		if err != nil {
			return err
		}
		if len(identityIDs) == 0 {
			return nil
		}

		// Clear adoption links and remove channel rows before their identities.
		// A later deletion failure needs transaction rollback to restore these
		// earlier relationship removals.
		if _, err := client.IdentityAdoptionDecision.Update().
			Where(identityadoptiondecision.IdentityIDIn(identityIDs...)).
			ClearIdentityID().
			Save(txCtx); err != nil {
			return err
		}
		if _, err := client.AuthIdentityChannel.Delete().
			Where(authidentitychannel.IdentityIDIn(identityIDs...)).
			Exec(txCtx); err != nil {
			return err
		}
		_, err = client.AuthIdentity.Delete().
			Where(
				authidentity.UserIDEQ(userID),
				authidentity.ProviderTypeEQ(provider),
			).
			Exec(txCtx)
		return err
	})
}

// BindAuthIdentityToUser validates matching channel provider fields and saves
// canonical and optional channel data in the selected transaction. Prepared
// account and tuple validity belong to the caller.
//
// Compatible owned records are selected by provider-key rank and reused with
// their row IDs. A conflict is rejected when no owned compatible record was
// selected; supplied metadata, issuer, and verification values update existing
// records only when present. WeChat keys can be canonicalized while channels
// are relinked to the selected identity. A later stage failure rolls back in
// the owned scope or reaches the external transaction owner.
func (r *UserStore) BindAuthIdentityToUser(ctx context.Context, input BindAuthIdentityInput) (*CreateAuthIdentityResult, error) {
	if err := IdentityValidateAuthIdentityChannelProviderMatch(input.Canonical, input.Channel); err != nil {
		return nil, err
	}

	var result *CreateAuthIdentityResult
	err := r.WithUserProfileIdentityTx(ctx, func(txCtx context.Context) error {
		client := clientFromContext(txCtx, r.client)
		canonical := input.Canonical

		identityRecords, err := client.AuthIdentity.Query().
			Where(
				authidentity.ProviderTypeEQ(strings.TrimSpace(canonical.ProviderType)),
				authidentity.ProviderKeyIn(IdentityCompatibleIdentityProviderKeys(canonical.ProviderType, canonical.ProviderKey)...),
				authidentity.ProviderSubjectEQ(strings.TrimSpace(canonical.ProviderSubject)),
			).
			All(txCtx)
		if err != nil {
			return err
		}
		identity := IdentitySelectOwnedCompatibleIdentity(identityRecords, input.UserID)
		if identity == nil && IdentityHasCompatibleIdentityConflict(identityRecords, input.UserID) {
			return ErrAuthIdentityOwnershipConflict
		}
		if identity == nil {
			identity, err = client.AuthIdentity.Create().
				SetUserID(input.UserID).
				SetProviderType(strings.TrimSpace(canonical.ProviderType)).
				SetProviderKey(strings.TrimSpace(canonical.ProviderKey)).
				SetProviderSubject(strings.TrimSpace(canonical.ProviderSubject)).
				SetMetadata(IdentityCopyMetadata(input.Metadata)).
				SetNillableIssuer(input.Issuer).
				SetNillableVerifiedAt(input.VerifiedAt).
				Save(txCtx)
			if err != nil {
				return err
			}
		} else {
			targetProviderKey := IdentityCanonicalizeCompatibleIdentityProviderKey(canonical.ProviderType, identity.ProviderKey, canonical.ProviderKey)
			update := client.AuthIdentity.UpdateOneID(identity.ID)
			if targetProviderKey != "" && !strings.EqualFold(targetProviderKey, identity.ProviderKey) {
				update = update.SetProviderKey(targetProviderKey)
			}
			if input.Metadata != nil {
				update = update.SetMetadata(IdentityCopyMetadata(input.Metadata))
			}
			if input.Issuer != nil {
				update = update.SetIssuer(strings.TrimSpace(*input.Issuer))
			}
			if input.VerifiedAt != nil {
				update = update.SetVerifiedAt(*input.VerifiedAt)
			}
			identity, err = update.Save(txCtx)
			if err != nil {
				return err
			}
		}

		var channel *dbent.AuthIdentityChannel
		if input.Channel != nil {
			channelRecords, err := client.AuthIdentityChannel.Query().
				Where(
					authidentitychannel.ProviderTypeEQ(strings.TrimSpace(input.Channel.ProviderType)),
					authidentitychannel.ProviderKeyIn(IdentityCompatibleIdentityProviderKeys(input.Channel.ProviderType, input.Channel.ProviderKey)...),
					authidentitychannel.ChannelEQ(strings.TrimSpace(input.Channel.Channel)),
					authidentitychannel.ChannelAppIDEQ(strings.TrimSpace(input.Channel.ChannelAppID)),
					authidentitychannel.ChannelSubjectEQ(strings.TrimSpace(input.Channel.ChannelSubject)),
				).
				WithIdentity().
				All(txCtx)
			if err != nil {
				return err
			}
			channel = IdentitySelectOwnedCompatibleChannel(channelRecords, input.UserID)
			if channel == nil && IdentityHasCompatibleChannelConflict(channelRecords, input.UserID) {
				return ErrAuthIdentityChannelOwnershipConflict
			}
			if channel == nil {
				channel, err = client.AuthIdentityChannel.Create().
					SetIdentityID(identity.ID).
					SetProviderType(strings.TrimSpace(input.Channel.ProviderType)).
					SetProviderKey(strings.TrimSpace(input.Channel.ProviderKey)).
					SetChannel(strings.TrimSpace(input.Channel.Channel)).
					SetChannelAppID(strings.TrimSpace(input.Channel.ChannelAppID)).
					SetChannelSubject(strings.TrimSpace(input.Channel.ChannelSubject)).
					SetMetadata(IdentityCopyMetadata(input.ChannelMetadata)).
					Save(txCtx)
				if err != nil {
					return err
				}
			} else {
				targetProviderKey := IdentityCanonicalizeCompatibleIdentityProviderKey(input.Channel.ProviderType, channel.ProviderKey, input.Channel.ProviderKey)
				update := client.AuthIdentityChannel.UpdateOneID(channel.ID).
					SetIdentityID(identity.ID)
				if targetProviderKey != "" && !strings.EqualFold(targetProviderKey, channel.ProviderKey) {
					update = update.SetProviderKey(targetProviderKey)
				}
				if input.ChannelMetadata != nil {
					update = update.SetMetadata(IdentityCopyMetadata(input.ChannelMetadata))
				}
				channel, err = update.Save(txCtx)
				if err != nil {
					return err
				}
			}
		}

		result = &CreateAuthIdentityResult{Identity: identity, Channel: channel}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// IdentityCompatibleIdentityProviderKeys returns the requested key and WeChat
// canonical/historical alternatives when applicable. Non-WeChat types and
// blank keys retain one requested key, with case-insensitive checks preventing
// duplicate alias additions.
func IdentityCompatibleIdentityProviderKeys(providerType, providerKey string) []string {
	providerType = strings.TrimSpace(strings.ToLower(providerType))
	providerKey = strings.TrimSpace(providerKey)
	if providerKey == "" {
		return []string{providerKey}
	}
	if providerType != "wechat" {
		return []string{providerKey}
	}
	keys := []string{providerKey}
	if !strings.EqualFold(providerKey, "wechat-main") {
		keys = append(keys, "wechat-main")
	}
	if !strings.EqualFold(providerKey, "wechat") {
		keys = append(keys, "wechat")
	}
	return keys
}

// IdentityCanonicalizeCompatibleIdentityProviderKey chooses the saved key when
// binding compatible identities. WeChat canonical or historical keys select
// wechat-main; otherwise nonblank requested keys win, with existing text
// supplying the missing request.
func IdentityCanonicalizeCompatibleIdentityProviderKey(providerType, existingKey, requestedKey string) string {
	providerType = strings.TrimSpace(strings.ToLower(providerType))
	existingKey = strings.TrimSpace(existingKey)
	requestedKey = strings.TrimSpace(requestedKey)
	if providerType != "wechat" {
		if requestedKey != "" {
			return requestedKey
		}
		return existingKey
	}
	if strings.EqualFold(existingKey, "wechat") || strings.EqualFold(existingKey, "wechat-main") || strings.EqualFold(requestedKey, "wechat-main") {
		return "wechat-main"
	}
	if requestedKey != "" {
		return requestedKey
	}
	return existingKey
}

// IdentityCompatibleIdentityProviderKeyRank ranks WeChat canonical, other, and
// historical keys for owned-row selection. Non-WeChat records receive rank
// zero; case-insensitive key comparison recognizes the two stored WeChat
// aliases.
func IdentityCompatibleIdentityProviderKeyRank(providerType, providerKey string) int {
	providerType = strings.TrimSpace(strings.ToLower(providerType))
	providerKey = strings.TrimSpace(providerKey)
	if providerType != "wechat" {
		return 0
	}
	switch {
	case strings.EqualFold(providerKey, "wechat-main"):
		return 0
	case strings.EqualFold(providerKey, "wechat"):
		return 2
	default:
		return 1
	}
}

// IdentitySelectOwnedCompatibleIdentity returns the owned record with the
// lowest compatible provider-key rank. Ties retain supplied order and absent
// owned records return nil; callers provide nonnil queried identity entities.
func IdentitySelectOwnedCompatibleIdentity(records []*dbent.AuthIdentity, userID int64) *dbent.AuthIdentity {
	var selected *dbent.AuthIdentity
	for _, record := range records {
		if record.UserID != userID {
			continue
		}
		if selected == nil || IdentityCompatibleIdentityProviderKeyRank(record.ProviderType, record.ProviderKey) < IdentityCompatibleIdentityProviderKeyRank(selected.ProviderType, selected.ProviderKey) {
			selected = record
		}
	}
	return selected
}

// IdentityHasCompatibleIdentityConflict reports any supplied identity
// belonging to another account. Empty input returns false, and binding invokes
// it when no owned compatible record was selected from the same query.
func IdentityHasCompatibleIdentityConflict(records []*dbent.AuthIdentity, userID int64) bool {
	for _, record := range records {
		if record.UserID != userID {
			return true
		}
	}
	return false
}

// IdentitySelectOwnedCompatibleChannel returns the account-owned channel with
// the lowest compatible key rank. Missing identity edges are skipped and ties
// retain supplied order; callers load channel ownership before this selection.
func IdentitySelectOwnedCompatibleChannel(records []*dbent.AuthIdentityChannel, userID int64) *dbent.AuthIdentityChannel {
	var selected *dbent.AuthIdentityChannel
	for _, record := range records {
		if record.Edges.Identity == nil || record.Edges.Identity.UserID != userID {
			continue
		}
		if selected == nil || IdentityCompatibleIdentityProviderKeyRank(record.ProviderType, record.ProviderKey) < IdentityCompatibleIdentityProviderKeyRank(selected.ProviderType, selected.ProviderKey) {
			selected = record
		}
	}
	return selected
}

// IdentityHasCompatibleChannelConflict reports another owner through a loaded
// channel identity edge. Missing edges do not establish ownership conflict,
// and callers load those edges before selecting a compatible binding record.
func IdentityHasCompatibleChannelConflict(records []*dbent.AuthIdentityChannel, userID int64) bool {
	for _, record := range records {
		if record.Edges.Identity != nil && record.Edges.Identity.UserID != userID {
			return true
		}
	}
	return false
}

// RecordProviderGrant inserts the account/provider/reason marker with
// do-nothing conflict handling using the selected raw executor. True reports
// positive inserted rows, false reports an existing claim, and affected-count
// read failures remain errors.
func (r *UserStore) RecordProviderGrant(ctx context.Context, input ProviderGrantRecordInput) (bool, error) {
	exec := IdentityTxAwareSQLExecutor(ctx, r.sql, r.client)
	if exec == nil {
		return false, fmt.Errorf("sql executor is not configured")
	}

	result, err := exec.ExecContext(ctx, `
INSERT INTO user_provider_default_grants (user_id, provider_type, grant_reason)
VALUES ($1, $2, $3)
ON CONFLICT (user_id, provider_type, grant_reason) DO NOTHING`,
		input.UserID,
		strings.TrimSpace(input.ProviderType),
		string(input.GrantReason),
	)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

// UpsertIdentityAdoptionDecision saves pending-session profile choices and
// optional identity reference in the selected Ent transaction. Session and
// identity lock keys serialize overlapping writes sharing that exact key
// namespace.
//
// Another decision identity reference, including a legacy null-session row, is
// cleared before the current session upsert. The unique pending-session key
// updates existing choices, then the stored decision is reloaded. The
// transaction helper commits owned writes or leaves completion to its external
// caller. Process locks release when the callback returns, before an owned
// transaction commits. PostgreSQL advisory locks remain until transaction
// completion, so their protection extends through that commit.
func (r *UserStore) UpsertIdentityAdoptionDecision(ctx context.Context, input IdentityAdoptionDecisionInput) (*dbent.IdentityAdoptionDecision, error) {
	var result *dbent.IdentityAdoptionDecision
	err := r.WithUserProfileIdentityTx(ctx, func(txCtx context.Context) error {
		client := clientFromContext(txCtx, r.client)
		releaseLocks, err := IdentityLockRepositoryScopedKeys(
			txCtx,
			client,
			IdentityTxAwareSQLExecutor(txCtx, r.sql, r.client),
			IdentityIdentityAdoptionDecisionLockKeys(input.PendingAuthSessionID, input.IdentityID)...,
		)
		if err != nil {
			return err
		}
		// Release process reservations when this callback finishes its writes and
		// readback. The transaction wrapper commits afterward; PostgreSQL advisory
		// locks stay with the transaction through that later completion.
		defer releaseLocks()

		// Clear competing identity references before saving this session choice.
		// A failed upsert relies on the enclosing transaction to restore those
		// already removed references.
		if input.IdentityID != nil && *input.IdentityID > 0 {
			if _, err := client.IdentityAdoptionDecision.Update().
				Where(
					identityadoptiondecision.IdentityIDEQ(*input.IdentityID),
					dbpredicate.IdentityAdoptionDecision(func(s *entsql.Selector) {
						col := s.C(identityadoptiondecision.FieldPendingAuthSessionID)
						s.Where(entsql.Or(
							entsql.IsNull(col),
							entsql.NEQ(col, input.PendingAuthSessionID),
						))
					}),
				).
				ClearIdentityID().
				Save(txCtx); err != nil {
				return err
			}
		}

		create := client.IdentityAdoptionDecision.Create().
			SetPendingAuthSessionID(input.PendingAuthSessionID).
			SetAdoptDisplayName(input.AdoptDisplayName).
			SetAdoptAvatar(input.AdoptAvatar).
			SetDecidedAt(time.Now().UTC())
		if input.IdentityID != nil && *input.IdentityID > 0 {
			create = create.SetIdentityID(*input.IdentityID)
		}

		decisionID, err := create.
			OnConflictColumns(identityadoptiondecision.FieldPendingAuthSessionID).
			UpdateNewValues().
			ID(txCtx)
		if err != nil {
			return err
		}

		result, err = client.IdentityAdoptionDecision.Get(txCtx, decisionID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// IdentityIdentityAdoptionDecisionLockKeys returns session and optional
// positive-identity adoption reservation keys. Both values protect writes that
// can share those references, and lock normalization supplies their consistent
// acquisition order.
func IdentityIdentityAdoptionDecisionLockKeys(pendingAuthSessionID int64, identityID *int64) []string {
	keys := []string{fmt.Sprintf("identity-adoption:pending:%d", pendingAuthSessionID)}
	if identityID != nil && *identityID > 0 {
		keys = append(keys, fmt.Sprintf("identity-adoption:identity:%d", *identityID))
	}
	return keys
}

// GetIdentityAdoptionDecisionByPendingAuthSessionID reads one stored session
// choice using the selected Ent client. Missing or ambiguous rows retain Ent
// lookup errors for callers deciding the next pending completion step.
func (r *UserStore) GetIdentityAdoptionDecisionByPendingAuthSessionID(ctx context.Context, pendingAuthSessionID int64) (*dbent.IdentityAdoptionDecision, error) {
	return clientFromContext(ctx, r.client).IdentityAdoptionDecision.Query().
		Where(identityadoptiondecision.PendingAuthSessionIDEQ(pendingAuthSessionID)).
		Only(ctx)
}

// UpdateUserLastLoginAt writes the supplied account login observation through
// the selected Ent client. Stored time is passed directly, and account lookup
// or persistence failures remain errors for the identity caller.
func (r *UserStore) UpdateUserLastLoginAt(ctx context.Context, userID int64, loginAt time.Time) error {
	_, err := clientFromContext(ctx, r.client).User.UpdateOneID(userID).
		SetLastLoginAt(loginAt).
		Save(ctx)
	return err
}

// UpdateUserLastActiveAt writes the supplied account activity observation
// through the selected Ent client. Stored time is passed directly, and account
// lookup or persistence failures remain errors for the identity caller.
func (r *UserStore) UpdateUserLastActiveAt(ctx context.Context, userID int64, activeAt time.Time) error {
	_, err := clientFromContext(ctx, r.client).User.UpdateOneID(userID).
		SetLastActiveAt(activeAt).
		Save(ctx)
	return err
}

// GetUserAvatar reads raw avatar storage metadata using the selected
// transaction driver or fallback executor. Missing rows return nil with no
// error, and row cleanup errors are discarded while scan/iteration errors
// remain errors.
func (r *UserStore) GetUserAvatar(ctx context.Context, userID int64) (*identitycore.UserAvatar, error) {
	exec, err := r.IdentityUserProfileIdentitySQL(ctx)
	if err != nil {
		return nil, err
	}

	rows, err := exec.QueryContext(ctx, `
SELECT storage_provider, storage_key, url, content_type, byte_size, sha256
FROM user_avatars
WHERE user_id = $1`, userID)
	if err != nil {
		return nil, err
	}
	// Close the avatar result on missing-row, scan-error, and successful exits.
	// Cleanup errors are discarded while row observation and iterator status
	// determine the reported profile result.
	defer func() { _ = rows.Close() }()

	if !rows.Next() {
		return nil, rows.Err()
	}

	var avatar identitycore.UserAvatar
	if err := rows.Scan(
		&avatar.StorageProvider,
		&avatar.StorageKey,
		&avatar.URL,
		&avatar.ContentType,
		&avatar.ByteSize,
		&avatar.SHA256,
	); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return &avatar, nil
}

// UpsertUserAvatar stores trimmed avatar metadata by account ID using the
// selected raw executor. Conflict replaces those fields and update time; the
// returned value is constructed from normalized input rather than a row
// readback.
func (r *UserStore) UpsertUserAvatar(ctx context.Context, userID int64, input identitycore.UpsertUserAvatarInput) (*identitycore.UserAvatar, error) {
	exec, err := r.IdentityUserProfileIdentitySQL(ctx)
	if err != nil {
		return nil, err
	}

	_, err = exec.ExecContext(ctx, `
INSERT INTO user_avatars (user_id, storage_provider, storage_key, url, content_type, byte_size, sha256, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, NOW())
ON CONFLICT (user_id) DO UPDATE SET
	storage_provider = EXCLUDED.storage_provider,
	storage_key = EXCLUDED.storage_key,
	url = EXCLUDED.url,
	content_type = EXCLUDED.content_type,
	byte_size = EXCLUDED.byte_size,
	sha256 = EXCLUDED.sha256,
	updated_at = NOW()`,
		userID,
		strings.TrimSpace(input.StorageProvider),
		strings.TrimSpace(input.StorageKey),
		strings.TrimSpace(input.URL),
		strings.TrimSpace(input.ContentType),
		input.ByteSize,
		strings.TrimSpace(input.SHA256),
	)
	if err != nil {
		return nil, err
	}

	return &identitycore.UserAvatar{
		StorageProvider: strings.TrimSpace(input.StorageProvider),
		StorageKey:      strings.TrimSpace(input.StorageKey),
		URL:             strings.TrimSpace(input.URL),
		ContentType:     strings.TrimSpace(input.ContentType),
		ByteSize:        input.ByteSize,
		SHA256:          strings.TrimSpace(input.SHA256),
	}, nil
}

// DeleteUserAvatar removes avatar metadata by account ID through the selected
// raw executor. Missing rows succeed when SQL reports no error, while executor
// readiness and statement failures remain errors.
func (r *UserStore) DeleteUserAvatar(ctx context.Context, userID int64) error {
	exec, err := r.IdentityUserProfileIdentitySQL(ctx)
	if err != nil {
		return err
	}
	_, err = exec.ExecContext(ctx, `DELETE FROM user_avatars WHERE user_id = $1`, userID)
	return err
}

// IdentityCopyMetadata allocates an outer map and copies each supplied
// metadata entry. Nil and empty input yield allocated empty maps, while nested
// maps, slices, and pointers remain shared.
func IdentityCopyMetadata(in map[string]any) map[string]any {
	if len(in) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(in))
	maps.Copy(out, in)
	return out
}

// IdentityValidateAuthIdentityChannelProviderMatch checks exact trimmed
// provider type and key equality between canonical and channel tuples. Nil
// channels succeed, and mismatches return the channel-provider error before
// any persistence begins.
func IdentityValidateAuthIdentityChannelProviderMatch(canonical AuthIdentityKey, channel *AuthIdentityChannelKey) error {
	if channel == nil {
		return nil
	}

	canonicalProviderType := strings.TrimSpace(canonical.ProviderType)
	canonicalProviderKey := strings.TrimSpace(canonical.ProviderKey)
	channelProviderType := strings.TrimSpace(channel.ProviderType)
	channelProviderKey := strings.TrimSpace(channel.ProviderKey)

	if canonicalProviderType != channelProviderType || canonicalProviderKey != channelProviderKey {
		return ErrAuthIdentityChannelProviderMismatch
	}

	return nil
}

// IdentityTxAwareSQLExecutor prefers an injected transaction driver supporting
// the raw query interface. Failed driver extraction selects a configured
// fallback, then attempts the default client driver; no available
// implementation returns nil.
func IdentityTxAwareSQLExecutor(ctx context.Context, fallback SQLExecutor, client *dbent.Client) IdentitySqlQueryExecutor {
	if tx := dbent.TxFromContext(ctx); tx != nil {
		if exec := IdentitySqlExecutorFromEntClient(tx.Client()); exec != nil {
			return exec
		}
	}
	// Unsupported transaction-driver extraction leaves raw work with the supplied
	// fallback, which can have a separate persistence scope. Callers relying on
	// rollback need that executor to participate in their transaction.
	if fallback != nil {
		return fallback
	}
	return IdentitySqlExecutorFromEntClient(client)
}

// IdentityUserProfileIdentitySQL returns the selected transaction-aware raw
// executor for avatar and identity operations. Missing implementations return
// a configuration error before the caller can query or mutate profile storage.
func (r *UserStore) IdentityUserProfileIdentitySQL(ctx context.Context) (IdentitySqlQueryExecutor, error) {
	exec := IdentityTxAwareSQLExecutor(ctx, r.sql, r.client)
	if exec == nil {
		return nil, fmt.Errorf("sql executor is not configured")
	}
	return exec, nil
}

// IdentitySqlExecutorFromEntClient reads the current generated Ent config
// driver and returns it when it implements raw SQL execution and querying. Nil
// clients, absent driver fields, or unsupported driver values return nil.
//
// Reflection and unsafe access expose the private driver value at its address
// before interface assertion. This depends on the generated config field
// layout being the one used by this repository; callers keep client
// configuration stable during extraction. It selects the driver value only,
// and subsequent transaction or query support failures still belong to that
// driver operation.
func IdentitySqlExecutorFromEntClient(client *dbent.Client) IdentitySqlQueryExecutor {
	if client == nil {
		return nil
	}

	clientValue := reflect.ValueOf(client).Elem()
	configValue := clientValue.FieldByName("config")
	driverValue := configValue.FieldByName("driver")
	if !driverValue.IsValid() {
		return nil
	}

	// The driver field is private in generated Ent config. Read its addressable
	// interface value before asserting raw SQL support; the accessor relies on
	// that concrete layout.
	driver := reflect.NewAt(driverValue.Type(), unsafe.Pointer(driverValue.UnsafeAddr())).Elem().Interface()
	exec, ok := driver.(IdentitySqlQueryExecutor)
	if !ok {
		return nil
	}
	return exec
}
