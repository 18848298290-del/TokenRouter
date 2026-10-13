package postgres

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"slices"
	"strings"
	"sync"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/identityadoptiondecision"
	"github.com/TokenFlux/TokenRouter/ent/pendingauthsession"
	dbpredicate "github.com/TokenFlux/TokenRouter/ent/predicate"
	"github.com/TokenFlux/TokenRouter/internal/identity"
)

// Browser pending state pairs a stored session token with a browser key and a
// separately hashed completion code. Reads validate expiry, consumption, and
// browser proof before a conditional write claims the session and removes
// completion-code state, so a stale loaded record cannot simply replay
// consumption.
//
// Adoption decisions use their pending-session ID as the update key and may
// also reference one provider identity. Scoped mutexes serialize overlapping
// keys within the process, and PostgreSQL transaction advisory locks extend
// that serialization across participants using the database transaction.
// Decisions clear another session identity reference before updating the
// current decision. Owned transactions commit their result; callers that
// supplied an existing transaction retain its completion and lock lifetime.

// Pending default lifetimes come from identity policy and are applied when
// session expiry or completion-code duration is unspecified by the caller.
const (
	defaultPendingAuthTTL           = identity.DefaultPendingAuthTTL
	defaultPendingAuthCompletionTTL = identity.DefaultPendingAuthCompletionTTL
)

// The scoped lock registry serializes adoption writes, while exported error
// aliases keep pending lifecycle and browser-proof failures recognizable by
// identity callers.
var (
	authPendingIdentityScopedKeyLocks = newAuthPendingIdentityScopedKeyLockRegistry()

	ErrPendingAuthSessionNotFound = identity.ErrPendingAuthSessionNotFound
	ErrPendingAuthSessionExpired  = identity.ErrPendingAuthSessionExpired
	ErrPendingAuthSessionConsumed = identity.ErrPendingAuthSessionConsumed
	ErrPendingAuthCodeInvalid     = identity.ErrPendingAuthCodeInvalid
	ErrPendingAuthCodeExpired     = identity.ErrPendingAuthCodeExpired
	ErrPendingAuthCodeConsumed    = identity.ErrPendingAuthCodeConsumed
	ErrPendingAuthBrowserMismatch = identity.ErrPendingAuthBrowserMismatch
)

// AuthPendingIdentityService stores browser pending lifecycle state and
// profile-adoption decisions through Ent.
type AuthPendingIdentityService struct {
	readTime  func() time.Time // pending lifecycle observation clock
	entClient *dbent.Client    // configured pending state client
}

// authPendingIdentityScopedKeyLockRegistry retains shared key locks while any
// holder or waiter references them.
type authPendingIdentityScopedKeyLockRegistry struct {
	// mu guards locks and each entry reference count. Holders reserve all
	// normalized entries before waiting on their individual mutexes, keeping the
	// same key entry alive for later waiters.
	mu    sync.Mutex                                        // registry entry lifetime lock
	locks map[string]*authPendingIdentityScopedKeyLockEntry // retained key lock entries
}

// authPendingIdentityScopedKeyLockEntry pairs one key mutex with its holder
// and waiter reference count.
type authPendingIdentityScopedKeyLockEntry struct {
	// mu serializes one normalized key. refs is guarded by the registry mutex and
	// counts reserved holders plus waiters, so entries disappear only after all
	// reservations are released.
	mu   sync.Mutex // per-key operation mutex
	refs int        // reserved holder and waiter count
}

type (
	// PendingAuthIdentityKey aliases the provider tuple retained in pending
	// authentication state.
	PendingAuthIdentityKey = identity.PendingAuthIdentityKey
	// CreatePendingAuthSessionInput aliases prepared browser proof, provider
	// claims, and session state.
	CreatePendingAuthSessionInput = identity.CreatePendingAuthSessionInput
	// IssuePendingAuthCompletionCodeInput aliases the session selection and
	// requested one-time code lifetime.
	IssuePendingAuthCompletionCodeInput = identity.IssuePendingAuthCompletionCodeInput
	// IssuePendingAuthCompletionCodeResult aliases the plaintext issued code and
	// its expiry time.
	IssuePendingAuthCompletionCodeResult = identity.IssuePendingAuthCompletionCodeResult
	// PendingIdentityAdoptionDecisionInput aliases the pending-session profile
	// choices and optional identity reference.
	PendingIdentityAdoptionDecisionInput = identity.PendingIdentityAdoptionDecisionInput
)

// newAuthPendingIdentityScopedKeyLockRegistry creates an empty registry for
// pending adoption key reservations.
func newAuthPendingIdentityScopedKeyLockRegistry() *authPendingIdentityScopedKeyLockRegistry {
	return &authPendingIdentityScopedKeyLockRegistry{
		locks: make(map[string]*authPendingIdentityScopedKeyLockEntry),
	}
}

// lock reserves normalized adoption keys, acquires their mutexes in sorted
// order, and returns a release callback. Empty key selections return a
// callback with no effect.
//
// Reference counts are increased under the registry mutex before waiting, so
// overlapping callers retain the same key entries. Sorted acquisition avoids
// callers holding the same keys in opposite order. Release unlocks entries in
// reverse order and removes only unreferenced registry entries. Invoke a
// nonempty release once; mutex waits are not canceled by a request context.
func (r *authPendingIdentityScopedKeyLockRegistry) lock(keys ...string) func() {
	normalized := IdentityNormalizeLockKeys(keys...)
	if len(normalized) == 0 {
		return func() {}
	}

	entries := make([]*authPendingIdentityScopedKeyLockEntry, 0, len(normalized))
	// Reserve every key entry under the registry mutex before waiting. Counts
	// include waiters, so release cannot remove an entry while another caller is
	// queued for it.
	r.mu.Lock()
	for _, key := range normalized {
		entry := r.locks[key]
		if entry == nil {
			entry = &authPendingIdentityScopedKeyLockEntry{}
			r.locks[key] = entry
		}
		entry.refs++
		entries = append(entries, entry)
	}
	r.mu.Unlock()

	// Acquire the normalized keys in the same sorted order across callers,
	// preventing overlapping key sets from waiting while holding them in opposite
	// orders.
	for _, entry := range entries {
		entry.mu.Lock()
	}

	// Release held key mutexes before dropping reservations under the registry
	// lock. Remaining waiters keep entries alive until their own release removes
	// the final reference.
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

// authPendingIdentityAdvisoryLockHash returns the signed FNV-1a key value used
// by PostgreSQL advisory locks.
func authPendingIdentityAdvisoryLockHash(key string) int64 {
	hasher := fnv.New64a()
	// FNV-1a accepts every input byte and its Write implementation returns no
	// error. Use the digest bits as a signed PostgreSQL advisory-lock key after
	// hashing.
	_, _ = hasher.Write([]byte(key))
	return int64(hasher.Sum64())
}

// lockAuthPendingIdentityKeys acquires process adoption locks and optional
// PostgreSQL transaction advisory locks for normalized keys. The returned
// callback releases process locks; the caller transaction controls advisory
// lock lifetime.
//
// No keys, missing client, or another dialect skips the database lock stage.
// PostgreSQL queries run in sorted key order after local reservations. A
// failed database lock query releases the local reservations before returning
// an error; prior transaction advisory locks remain with their transaction.
// Successful row results are closed with close failures discarded.
func lockAuthPendingIdentityKeys(ctx context.Context, client *dbent.Client, keys ...string) (func(), error) {
	release := authPendingIdentityScopedKeyLocks.lock(keys...)
	normalized := IdentityNormalizeLockKeys(keys...)
	if len(normalized) == 0 || client == nil || client.Driver().Dialect() != dialect.Postgres {
		return release, nil
	}

	for _, key := range normalized {
		var rows entsql.Rows
		if err := client.Driver().Query(ctx, "SELECT pg_advisory_xact_lock($1)", []any{authPendingIdentityAdvisoryLockHash(key)}, &rows); err != nil {
			release()
			return nil, err
		}
		// The advisory-lock query has completed its transaction-scoped effect. Close
		// the unused row result while discarding cleanup errors; lock ownership
		// remains with the transaction.
		_ = rows.Close()
	}

	return release, nil
}

// pendingIdentityAdoptionLockKeys returns the session key and an optional
// positive identity-reference key. Both keys identify adoption records whose
// updates can overlap; the lock helper normalizes their acquisition order.
func pendingIdentityAdoptionLockKeys(pendingAuthSessionID int64, identityID *int64) []string {
	keys := []string{fmt.Sprintf("pending-auth-adoption:pending:%d", pendingAuthSessionID)}
	if identityID != nil && *identityID > 0 {
		keys = append(keys, fmt.Sprintf("pending-auth-adoption:identity:%d", *identityID))
	}
	return keys
}

// NewAuthPendingIdentityService retains the Ent client and first optional
// lifecycle clock. An absent or nil first clock uses time.Now; later supplied
// clocks do not change that selection.
func NewAuthPendingIdentityService(entClient *dbent.Client, clocks ...func() time.Time) *AuthPendingIdentityService {
	now := time.Now
	if len(clocks) > 0 && clocks[0] != nil {
		now = clocks[0]
	}
	return &AuthPendingIdentityService{entClient: entClient, readTime: now}
}

// CreatePendingSession stores prepared browser state and provider claims using
// the configured client. A blank trimmed token receives forty-eight hex
// characters from twenty-four random bytes; zero expiry uses the current clock
// and default TTL. Metadata receives outer-map copies.
func (s *AuthPendingIdentityService) CreatePendingSession(ctx context.Context, input CreatePendingAuthSessionInput) (*dbent.PendingAuthSession, error) {
	if s == nil || s.entClient == nil {
		return nil, fmt.Errorf("pending auth ent client is not configured")
	}

	sessionToken := strings.TrimSpace(input.SessionToken)
	if sessionToken == "" {
		var err error
		sessionToken, err = randomOpaqueToken(24)
		if err != nil {
			return nil, err
		}
	}

	// Store the supplied expiry instant in UTC before pending validation and
	// persistence; the configured clock supplies only an unspecified expiry,
	// preserving an explicit caller instant.
	expiresAt := input.ExpiresAt.UTC()
	if expiresAt.IsZero() {
		expiresAt = s.readTime().UTC().Add(defaultPendingAuthTTL)
	}

	create := s.entClient.PendingAuthSession.Create().
		SetSessionToken(sessionToken).
		SetIntent(strings.TrimSpace(input.Intent)).
		SetProviderType(strings.TrimSpace(input.Identity.ProviderType)).
		SetProviderKey(strings.TrimSpace(input.Identity.ProviderKey)).
		SetProviderSubject(strings.TrimSpace(input.Identity.ProviderSubject)).
		SetRedirectTo(strings.TrimSpace(input.RedirectTo)).
		SetResolvedEmail(strings.TrimSpace(input.ResolvedEmail)).
		SetRegistrationPasswordHash(strings.TrimSpace(input.RegistrationPasswordHash)).
		SetBrowserSessionKey(strings.TrimSpace(input.BrowserSessionKey)).
		SetUpstreamIdentityClaims(copyPendingMap(input.UpstreamIdentityClaims)).
		SetLocalFlowState(copyPendingMap(input.LocalFlowState)).
		SetExpiresAt(expiresAt)
	if input.TargetUserID != nil {
		create = create.SetTargetUserID(*input.TargetUserID)
	}
	return create.Save(ctx)
}

// IssueCompletionCode replaces a stored session completion digest and returns
// the new plaintext code with expiry. Nonpositive TTL selects the default;
// nonblank browser input replaces the stored browser key. This method checks
// row presence but not session expiry or prior consumption.
func (s *AuthPendingIdentityService) IssueCompletionCode(ctx context.Context, input IssuePendingAuthCompletionCodeInput) (*IssuePendingAuthCompletionCodeResult, error) {
	if s == nil || s.entClient == nil {
		return nil, fmt.Errorf("pending auth ent client is not configured")
	}

	session, err := s.entClient.PendingAuthSession.Get(ctx, input.PendingAuthSessionID)
	if err != nil {
		if dbent.IsNotFound(err) {
			return nil, ErrPendingAuthSessionNotFound
		}
		return nil, err
	}

	code, err := randomOpaqueToken(24)
	if err != nil {
		return nil, err
	}
	ttl := input.TTL
	if ttl <= 0 {
		ttl = defaultPendingAuthCompletionTTL
	}
	expiresAt := s.readTime().UTC().Add(ttl)

	update := s.entClient.PendingAuthSession.UpdateOneID(session.ID).
		SetCompletionCodeHash(hashPendingAuthCode(code)).
		SetCompletionCodeExpiresAt(expiresAt)
	if strings.TrimSpace(input.BrowserSessionKey) != "" {
		update = update.SetBrowserSessionKey(strings.TrimSpace(input.BrowserSessionKey))
	}
	if _, err := update.Save(ctx); err != nil {
		return nil, err
	}

	return &IssuePendingAuthCompletionCodeResult{
		Code:      code,
		ExpiresAt: expiresAt,
	}, nil
}

// ConsumeCompletionCode finds the digest of trimmed code text and delegates
// guarded session consumption. Missing digest matches return
// ErrPendingAuthCodeInvalid, and lifecycle or browser failures come from the
// shared conditional consumption path.
func (s *AuthPendingIdentityService) ConsumeCompletionCode(ctx context.Context, rawCode, browserSessionKey string) (*dbent.PendingAuthSession, error) {
	if s == nil || s.entClient == nil {
		return nil, fmt.Errorf("pending auth ent client is not configured")
	}

	codeHash := hashPendingAuthCode(strings.TrimSpace(rawCode))
	session, err := s.entClient.PendingAuthSession.Query().
		Where(pendingauthsession.CompletionCodeHashEQ(codeHash)).
		Only(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			return nil, ErrPendingAuthCodeInvalid
		}
		return nil, err
	}

	return s.consumeSession(ctx, session, browserSessionKey, ErrPendingAuthCodeExpired, ErrPendingAuthCodeConsumed)
}

// ConsumeBrowserSession finds the trimmed session token and delegates guarded
// browser-session consumption. Missing tokens or rows return the
// session-not-found error, while lifecycle and browser failures remain
// recognizable to callers.
func (s *AuthPendingIdentityService) ConsumeBrowserSession(ctx context.Context, sessionToken, browserSessionKey string) (*dbent.PendingAuthSession, error) {
	if s == nil || s.entClient == nil {
		return nil, fmt.Errorf("pending auth ent client is not configured")
	}

	session, err := s.getBrowserSession(ctx, sessionToken)
	if err != nil {
		return nil, err
	}

	return s.consumeSession(ctx, session, browserSessionKey, ErrPendingAuthSessionExpired, ErrPendingAuthSessionConsumed)
}

// GetBrowserSession reads a trimmed token and validates browser proof and
// lifecycle through the configured clock. It leaves the row unconsumed and
// returns the stored entity for later pending decisions.
func (s *AuthPendingIdentityService) GetBrowserSession(ctx context.Context, sessionToken, browserSessionKey string) (*dbent.PendingAuthSession, error) {
	if s == nil || s.entClient == nil {
		return nil, fmt.Errorf("pending auth ent client is not configured")
	}

	session, err := s.getBrowserSession(ctx, sessionToken)
	if err != nil {
		return nil, err
	}
	if err := validatePendingSessionState(session, browserSessionKey, ErrPendingAuthSessionExpired, ErrPendingAuthSessionConsumed, s.readTime); err != nil {
		return nil, err
	}
	return session, nil
}

// getBrowserSession resolves a trimmed session token using the configured
// client. Blank or missing tokens become ErrPendingAuthSessionNotFound; the
// caller separately validates expiry, consumption, and browser proof.
func (s *AuthPendingIdentityService) getBrowserSession(ctx context.Context, sessionToken string) (*dbent.PendingAuthSession, error) {
	if s == nil || s.entClient == nil {
		return nil, fmt.Errorf("pending auth ent client is not configured")
	}

	sessionToken = strings.TrimSpace(sessionToken)
	if sessionToken == "" {
		return nil, ErrPendingAuthSessionNotFound
	}

	session, err := s.entClient.PendingAuthSession.Query().
		Where(pendingauthsession.SessionTokenEQ(sessionToken)).
		Only(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			return nil, ErrPendingAuthSessionNotFound
		}
		return nil, err
	}
	return session, nil
}

// consumeSession validates loaded state and conditionally claims its
// unconsumed, unexpired row before clearing completion-code values. The caller
// supplies the lifecycle error labels appropriate for browser or code
// consumption.
//
// The write rechecks session expiry, optional code expiry, consumption, and
// any retained browser key using a fresh clock observation. It also scrubs
// legacy credentials from local completion state. A failed conditional match
// reloads current state to report its observed lifecycle error; if that state
// still appears valid, the method returns the supplied consumed error to
// refuse replay.
func (s *AuthPendingIdentityService) consumeSession(
	ctx context.Context,
	session *dbent.PendingAuthSession,
	browserSessionKey string,
	expiredErr error,
	consumedErr error,
) (*dbent.PendingAuthSession, error) {
	if err := validatePendingSessionState(session, browserSessionKey, expiredErr, consumedErr, s.readTime); err != nil {
		return nil, err
	}

	// Recheck lifecycle and browser state in the claim write using a new clock
	// observation. Scrub legacy completion credentials before saving the consumed
	// row so replay cannot forward them.
	sanitizedLocalFlowState := sanitizePendingAuthLocalFlowState(session.LocalFlowState)
	now := s.readTime().UTC()
	update := s.entClient.PendingAuthSession.UpdateOneID(session.ID).
		Where(
			pendingauthsession.ConsumedAtIsNil(),
			pendingauthsession.ExpiresAtGTE(now),
			pendingauthsession.Or(
				pendingauthsession.CompletionCodeExpiresAtIsNil(),
				pendingauthsession.CompletionCodeExpiresAtGTE(now),
			),
		).
		SetConsumedAt(now).
		SetLocalFlowState(sanitizedLocalFlowState).
		SetCompletionCodeHash("").
		ClearCompletionCodeExpiresAt()
	if expectedBrowserSessionKey := strings.TrimSpace(session.BrowserSessionKey); expectedBrowserSessionKey != "" {
		update = update.Where(pendingauthsession.BrowserSessionKeyEQ(expectedBrowserSessionKey))
	}
	updated, err := update.Save(ctx)
	if err == nil {
		return updated, nil
	}
	if !dbent.IsNotFound(err) {
		return nil, err
	}

	// A conditional miss can follow another consumption or expiry transition.
	// Reload the current row to select its lifecycle error instead of trusting
	// the earlier caller snapshot.
	current, currentErr := s.entClient.PendingAuthSession.Get(ctx, session.ID)
	if currentErr != nil {
		if dbent.IsNotFound(currentErr) {
			return nil, ErrPendingAuthSessionNotFound
		}
		return nil, currentErr
	}
	if err := validatePendingSessionState(current, browserSessionKey, expiredErr, consumedErr, s.readTime); err != nil {
		return nil, err
	}
	return nil, consumedErr
}

// UpsertAdoptionDecision saves profile choices for a pending ID and optional
// identity reference. It first asks the configured client for a new
// transaction; a regular client opens one even when context carries another.
// After ErrTxStarted, an injected transaction is preferred over the retained
// transaction client, with completion left to that external owner.
//
// Normalized session and identity keys serialize overlapping updates before
// another decision identity reference is cleared. The current pending row is
// then inserted or updated by its unique session key and reloaded. A newly
// owned transaction commits the combined result; existing transaction callers
// retain completion responsibility. Local locks release on return, while
// PostgreSQL advisory locks remain tied to transaction completion.
func (s *AuthPendingIdentityService) UpsertAdoptionDecision(ctx context.Context, input PendingIdentityAdoptionDecisionInput) (*dbent.IdentityAdoptionDecision, error) {
	if s == nil || s.entClient == nil {
		return nil, fmt.Errorf("pending auth ent client is not configured")
	}

	tx, err := s.entClient.Tx(ctx)
	if err != nil && !errors.Is(err, dbent.ErrTxStarted) {
		return nil, err
	}

	client := s.entClient
	txCtx := ctx
	if err == nil {
		// Rollback earlier identity-reference removal if current decision upsert or
		// readback fails. Cleanup errors are discarded, while an existing
		// transaction retains its own completion and database-lock lifetime.
		defer func() { _ = tx.Rollback() }()
		client = tx.Client()
		txCtx = dbent.NewTxContext(ctx, tx)
	} else if existingTx := dbent.TxFromContext(ctx); existingTx != nil {
		client = existingTx.Client()
	}

	releaseLocks, err := lockAuthPendingIdentityKeys(txCtx, client, pendingIdentityAdoptionLockKeys(input.PendingAuthSessionID, input.IdentityID)...)
	if err != nil {
		return nil, err
	}
	defer releaseLocks()

	// The identity reference belongs to at most one adoption decision. Clear
	// references from other sessions, including legacy null-session rows, before
	// claiming it for this pending-session upsert.
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
			return nil, err
		}
	}

	create := client.IdentityAdoptionDecision.Create().
		SetPendingAuthSessionID(input.PendingAuthSessionID).
		SetAdoptDisplayName(input.AdoptDisplayName).
		SetAdoptAvatar(input.AdoptAvatar).
		SetDecidedAt(s.readTime().UTC())
	if input.IdentityID != nil && *input.IdentityID > 0 {
		create = create.SetIdentityID(*input.IdentityID)
	}

	decisionID, err := create.
		OnConflictColumns(identityadoptiondecision.FieldPendingAuthSessionID).
		UpdateNewValues().
		ID(txCtx)
	if err != nil {
		return nil, err
	}

	decision, err := client.IdentityAdoptionDecision.Get(txCtx, decisionID)
	if err != nil {
		return nil, err
	}

	if tx != nil {
		// Commit reference reassignment with the current pending-session choice
		// before returning its stored decision. Local release follows return, and
		// database advisory locks follow this transaction completion.
		if err := tx.Commit(); err != nil {
			return nil, err
		}
	}

	return decision, nil
}

// sanitizePendingAuthLocalFlowState delegates copying and removal of legacy
// credentials from stored completion state.
func sanitizePendingAuthLocalFlowState(localFlowState map[string]any) map[string]any {
	return identity.SanitizePendingAuthLocalFlowState(localFlowState)
}

// validatePendingSessionState maps the entity and delegates lifecycle checks
// using the supplied clock and error labels. It leaves row reservation to
// consumption and preserves the caller browser proof input.
func validatePendingSessionState(session *dbent.PendingAuthSession, browserSessionKey string, expiredErr error, consumedErr error, readTime func() time.Time) error {
	return identity.ValidatePendingSessionStateWithClock(PendingAuthSessionFromEntity(session), browserSessionKey, expiredErr, consumedErr, readTime)
}

// copyPendingMap delegates a new outer map with shared nested pending values.
// Nil and empty input produce allocated empty maps, so callers can populate
// the result before storing it.
func copyPendingMap(in map[string]any) map[string]any { return identity.CopyPendingMap(in) }

// randomOpaqueToken delegates random-byte generation encoded as hexadecimal
// pending token text. Nonpositive byte length selects the identity default,
// while entropy read failures remain errors before token delivery.
func randomOpaqueToken(byteLen int) (string, error) { return identity.RandomOpaqueToken(byteLen) }

// hashPendingAuthCode delegates the completion-code digest used for persistent
// lookup and consumption.
func hashPendingAuthCode(code string) string { return identity.HashPendingAuthCode(code) }
