package identity

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// Passkey registration uses a stable account handle, while login discovers
// the account from a credential and then checks its handle bytes and current
// owner state. Begin operations save short-lived WebAuthn ceremony data
// separately from durable credentials.
//
// Finish consumes that temporary data before kind/user checks and verifier
// parsing, so subsequent failures require a new ceremony. Registration
// persists verified credential data before returning metadata; login persists
// authenticator updates before returning an account for platform token
// issuance. Handle persistence keeps concurrent registration candidates
// aligned, while account pointers, handle bytes and nested library credential
// values remain shared during verifier calls. Operation time records
// credential use; the SDK owns its own ceremony-expiry clock.

// These ceremony labels, cache lifetime and name limits connect saved
// challenge kind to finish admission and user-facing credential metadata.
const (
	passkeySessionRegistration = "registration"  // saved registration ceremony label
	passkeySessionLogin        = "login"         // saved discoverable-login ceremony label
	passkeySessionTTL          = 5 * time.Minute // temporary ceremony cache lifetime
	defaultPasskeyName         = "Passkey"       // blank credential-name display value
	maxPasskeyNameLength       = 100             // maximum credential-name rune count
)

// These errors identify Passkey policy, persisted credential state and
// ceremony failures; the interface assertion keeps the SDK account
// representation compatible.
var (
	ErrPasskeysDisabled = infraerrors.Forbidden("PASSKEY_DISABLED", "passkey authentication is not enabled")
	ErrPasskeyNotFound  = infraerrors.NotFound("PASSKEY_NOT_FOUND", "passkey not found")
	ErrPasskeyExists    = infraerrors.Conflict("PASSKEY_ALREADY_EXISTS", "this passkey is already registered")
	ErrPasskeySession   = infraerrors.BadRequest("PASSKEY_SESSION_INVALID", "passkey session is invalid or expired")
	ErrPasskeyVerify    = infraerrors.Unauthorized("PASSKEY_VERIFICATION_FAILED", "passkey verification failed")

	_ webauthn.User = (*passkeyUser)(nil)
)

// PasskeyCredentialRecord holds a stored authenticator credential with its
// account and display metadata.
type PasskeyCredentialRecord struct {
	ID     int64 // stored credential record identifier
	UserID int64 // credential owner account identifier
	// UserHandle carries the owner's stable opaque WebAuthn identifier.
	// FinishLogin compares these bytes with the assertion before loading the
	// current owner account.
	UserHandle []byte
	Name       string // normalized credential display text
	// Credential retains the library record, including validation flags and
	// signature counters. Struct copies retain nested references, so ceremony
	// callers keep them stable.
	Credential webauthn.Credential
	// LastUsedAt is the optional stored use timestamp. passkeySummary passes its
	// pointer through unchanged, so returned metadata retains this shared
	// observation.
	LastUsedAt *time.Time
	CreatedAt  time.Time // credential record creation time
	UpdatedAt  time.Time // credential record update time
}

// PasskeyRepository persists stable account handles and verified
// authenticator credential records.
type PasskeyRepository interface {
	// EnsureUserHandle establishes a stable account handle or returns its stored
	// value. PostgreSQL accepts sixteen to sixty-four candidate bytes and
	// inserts with conflict suppression before reading the account handle back.
	//
	// The first successful stored candidate remains authoritative when
	// registrations race, so callers use the returned handle rather than their
	// proposed bytes. A read failure can follow a successful insertion and still
	// returns an error. Candidate data remains stable through persistence, and
	// user/account admission belongs to the service before this operation.
	EnsureUserHandle(ctx context.Context, userID int64, candidate []byte) ([]byte, error)
	// GetUserHandle reads the stable handle for an account. PostgreSQL reports
	// absent state as ErrPasskeyNotFound; the service uses the returned bytes
	// through its ceremony rather than replacing them with another random
	// candidate.
	GetUserHandle(ctx context.Context, userID int64) ([]byte, error)
	// GetByCredentialID resolves raw authenticator credential bytes to a stored
	// owner and WebAuthn record. Missing credentials return ErrPasskeyNotFound
	// in PostgreSQL; FinishLogin separately compares the returned handle and
	// confirms owner activity before verification.
	GetByCredentialID(ctx context.Context, credentialID []byte) (*PasskeyCredentialRecord, error)
	// ListByUserID reads stored credentials for the account, ordered newest
	// first in PostgreSQL. Returned credential values can retain nested mutable
	// references, so service callers hold the observations stable while
	// constructing exclusions or validating an assertion.
	ListByUserID(ctx context.Context, userID int64) ([]PasskeyCredentialRecord, error)
	// Create persists a verified credential for its supplied owner and returns
	// assigned record metadata. PostgreSQL requires a nonempty credential ID,
	// rejects uniqueness conflicts as ErrPasskeyExists and retains nested
	// credential references in its returned struct.
	Create(ctx context.Context, record *PasskeyCredentialRecord) (*PasskeyCredentialRecord, error)
	// UpdateCredential persists the verifier's updated authenticator state and
	// use timestamp under the supplied owner. PostgreSQL requires a matching
	// credential row; FinishLogin completes this write before returning the
	// verified account for token issuance.
	UpdateCredential(ctx context.Context, userID int64, credential *webauthn.Credential, usedAt time.Time) error
	// Rename updates display text for the supplied account-owned record.
	// PostgreSQL requires a matching row and reports absence as
	// ErrPasskeyNotFound; PasskeyService normalizes the submitted name before
	// this operation.
	Rename(ctx context.Context, userID, credentialID int64, name string) error
	// Delete removes the supplied account-owned credential row. PostgreSQL
	// requires an affected row and reports absence as ErrPasskeyNotFound;
	// account proof and feature policy are established by the service caller.
	Delete(ctx context.Context, userID, credentialID int64) error
}

// PasskeySession carries saved server challenge state for one WebAuthn
// ceremony.
type PasskeySession struct {
	Kind string `json:"kind"` // registration or login ceremony
	// UserID binds registration state to its authenticated account. Discoverable
	// login leaves it zero and resolves the account from the credential
	// callback.
	UserID int64 `json:"user_id,omitempty"`
	// WebAuthn holds the verifier's saved session data. Store encodes that value
	// before publication, and finish uses it after consuming the temporary
	// token.
	WebAuthn webauthn.SessionData `json:"webauthn"`
}

// PasskeySessionStore publishes expiring ceremony state and consumes it
// before verification.
type PasskeySessionStore interface {
	// Store saves a nonnil ceremony for a positive lifetime and returns its
	// opaque token after persistence succeeds. The Redis adapter serializes
	// caller-stable state and uses a random unpadded base64url token; the
	// service supplies five minutes.
	Store(ctx context.Context, session *PasskeySession, ttl time.Duration) (string, error)
	// Consume obtains and removes a temporary ceremony selected by token. Redis
	// GETDEL precedes decoding and later service kind/user/SDK checks, so
	// missing or malformed state is rejected and any later failure leaves the
	// challenge consumed.
	Consume(ctx context.Context, token string) (*PasskeySession, error)
}

// PasskeyCredentialSummary exposes the credential's display fields and
// observed backup eligibility.
type PasskeyCredentialSummary struct {
	ID        int64     `json:"id"`         // stored credential record identifier
	Name      string    `json:"name"`       // normalized credential display text
	CreatedAt time.Time `json:"created_at"` // credential record creation time
	// LastUsedAt retains the source record's optional timestamp pointer. Callers
	// treat the pointed-to value as shared when handling returned credential
	// summaries.
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	// Backup reports the authenticator's BackupEligible flag. A true value
	// describes backup capability and does not assert its current BackupState or
	// synchronized availability.
	Backup bool `json:"backup"`
}

// passkeyUser adapts current account and credential observations to the
// WebAuthn user interface.
type passkeyUser struct {
	// account retains the supplied current user pointer. Profile names and email
	// are read through that shared account during the verifier ceremony.
	account *User
	// handle retains the supplied stable user-handle bytes. WebAuthnID returns
	// them directly, so callers keep this storage unchanged during ceremony
	// verification.
	handle []byte
	// credentials has a service-allocated slice of library credential structs.
	// WebAuthnCredentials returns that slice directly and the structs retain
	// their nested references.
	credentials []webauthn.Credential
}

// PasskeyService performs Passkey ceremonies and account-scoped credential
// management before platform login.
type PasskeyService struct {
	// operationClock records credential use through the constructor's selected
	// optional clock. The WebAuthn verifier separately owns the clock used for
	// ceremony expiry checks.
	operationClock
	enabled  bool                // startup Passkey feature switch
	webAuthn PasskeyVerifier     // configured ceremony verification service
	repo     PasskeyRepository   // durable authenticator credential storage
	sessions PasskeySessionStore // consumable temporary ceremony storage
	userRepo SessionUserReader   // current credential-owner account reader
}

// PasskeyVerifier prepares ceremonies and parses/verifies their responses
// against saved session state.
type PasskeyVerifier interface {
	// BeginRegistration prepares creation options and session state for the
	// supplied user and options. The service provides stable handle/credential
	// observations and persists the returned session before exposing a token;
	// verifier failures remain errors. Nil error requires nonnil creation options
	// and session state before the service prepares its stored challenge.
	BeginRegistration(webauthn.User, ...webauthn.RegistrationOption) (*protocol.CredentialCreation, *webauthn.SessionData, error)
	// BeginDiscoverableLogin prepares assertion options and session state for a
	// user-discoverable ceremony. The service requests user verification and
	// saves the returned session; account resolution occurs later through the
	// finish callback. Nil error requires nonnil assertion options and session
	// state, which the service reads before publishing its challenge token.
	BeginDiscoverableLogin(...webauthn.LoginOption) (*protocol.CredentialAssertion, *webauthn.SessionData, error)
	// FinishRegistration parses and verifies the supplied creation response
	// against user and saved session data. The service consumes that session
	// first and persists the returned credential afterward; callers retain
	// response-reader ownership. Nil error requires a nonnil credential before
	// the service constructs its persistence request.
	FinishRegistration(webauthn.User, webauthn.SessionData, io.Reader) (*webauthn.Credential, error)
	// FinishPasskeyLogin parses and verifies an assertion using the supplied
	// discoverable-user callback and saved session. The service consumes the
	// challenge first, then persists the returned credential state before
	// returning the account for platform login. Nil error requires a nonnil
	// service-created passkeyUser and credential for the following owner/state
	// reads and persistence.
	FinishPasskeyLogin(webauthn.DiscoverableUserHandler, webauthn.SessionData, io.Reader) (webauthn.User, *webauthn.Credential, error)
}

// WebAuthnID returns the account's stable WebAuthn handle as shared bytes.
// SDK consumers use this value for credential ownership; callers leave the
// handle unchanged during ceremonies and copy it before independent mutation.
func (u *passkeyUser) WebAuthnID() []byte {
	return u.handle
}

// WebAuthnName returns the current account email used by WebAuthn ceremonies.
func (u *passkeyUser) WebAuthnName() string {
	return u.account.Email
}

// WebAuthnDisplayName returns trimmed account username or the account email
// when blank. The account pointer remains shared, so callers keep profile
// data stable while the verifier constructs ceremony output.
func (u *passkeyUser) WebAuthnDisplayName() string {
	if name := strings.TrimSpace(u.account.Username); name != "" {
		return name
	}
	// Blank profile names use the current account email for the SDK display
	// label, keeping a usable label while leaving the stable handle as identity.
	return u.account.Email
}

// WebAuthnCredentials returns the shared credential slice supplied to the
// SDK. Credential structs and their nested byte/slice values remain mutable,
// so callers keep them stable through verification and copy references before
// independent edits.
func (u *passkeyUser) WebAuthnCredentials() []webauthn.Credential {
	return u.credentials
}

// NewPasskeyService retains the configured verifier, credential repository,
// temporary session store and account reader. The first optional clock is
// used when nonnil; otherwise wall time supplies use timestamps. Callers
// assemble required stores before enabled ceremony operations.
func NewPasskeyService(enabled bool, verifier PasskeyVerifier, repo PasskeyRepository, sessions PasskeySessionStore, users SessionUserReader, clocks ...func() time.Time) *PasskeyService {
	return &PasskeyService{operationClock: clockFromOptional(clocks), enabled: enabled, webAuthn: verifier, repo: repo, sessions: sessions, userRepo: users}
}

// Enabled reports whether the service has its feature flag and verifier. It
// accepts a nil receiver but does not check the repository, session store or
// account reader used by enabled operations.
func (s *PasskeyService) Enabled() bool {
	return s != nil && s.enabled && s.webAuthn != nil
}

// requireEnabled returns ErrPasskeysDisabled when the service
// feature/verifier check fails. A successful result permits ceremony or
// management work but leaves dependency readiness to construction, because
// Enabled does not inspect the stores.
func (s *PasskeyService) requireEnabled() error {
	if !s.Enabled() {
		return ErrPasskeysDisabled
	}
	return nil
}

// BeginRegistration prepares a resident credential ceremony for a current
// active account. It generates a handle candidate, asks persistence for the
// account's stable handle, and loads existing credentials before creating
// exclusions and requesting credential properties.
//
// Concurrent begins can propose different handles, but the PostgreSQL
// repository keeps the first stored value and returns that stable value to
// each ceremony. Temporary registration state is saved for five minutes
// before the token is returned; failures after handle persistence can leave
// the handle established without a completed challenge. The caller retains
// authenticated account context for the finish request.
func (s *PasskeyService) BeginRegistration(
	ctx context.Context,
	userID int64,
) (creation *protocol.CredentialCreation, sessionToken string, err error) {
	if err = s.requireEnabled(); err != nil {
		return nil, "", err
	}
	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return nil, "", err
	}
	if !user.IsActive() {
		return nil, "", ErrUserNotActive
	}

	// Use an opaque handle candidate within the repository byte-length range.
	candidate := make([]byte, 32)
	if _, err = rand.Read(candidate); err != nil {
		return nil, "", fmt.Errorf("generate passkey user handle: %w", err)
	}
	// Persist or read the account's stable handle before building ceremony
	// options; concurrent candidates converge on the stored value instead of
	// changing existing credential identity.
	handle, err := s.repo.EnsureUserHandle(ctx, userID, candidate)
	if err != nil {
		return nil, "", err
	}
	waUser, err := s.loadWebAuthnUser(ctx, user, handle)
	if err != nil {
		return nil, "", err
	}

	creation, session, err := s.webAuthn.BeginRegistration(
		waUser,
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired),
		webauthn.WithExclusions(webauthn.Credentials(waUser.credentials).CredentialDescriptors()),
		webauthn.WithExtensions(protocol.AuthenticationExtensions{"credProps": true}),
	)
	if err != nil {
		return nil, "", fmt.Errorf("begin passkey registration: %w", err)
	}
	// Save registration state before returning its lookup token; a failed save
	// can leave the stable account handle established but supplies no challenge
	// to finish.
	sessionToken, err = s.sessions.Store(ctx, &PasskeySession{
		Kind:     passkeySessionRegistration,
		UserID:   userID,
		WebAuthn: *session,
	}, passkeySessionTTL)
	if err != nil {
		return nil, "", err
	}
	return creation, sessionToken, nil
}

// FinishRegistration consumes a saved challenge before checking its
// registration kind, account ID and browser response. A wrong kind/user,
// later account/handle read failure or verifier rejection leaves the Redis
// challenge consumed; the caller supplies authenticated userID and an owned
// response reader.
//
// The account is reloaded for profile/credential data, but this method does
// not repeat BeginRegistration's active-account check. A verified credential
// is persisted with the normalized display name before its summary is
// returned. SDK errors become ErrPasskeyVerify, while repository errors
// remain errors; callers restart the ceremony after failures that follow
// consumption.
func (s *PasskeyService) FinishRegistration(
	ctx context.Context,
	userID int64,
	sessionToken, name string,
	request io.Reader,
) (*PasskeyCredentialSummary, error) {
	if err := s.requireEnabled(); err != nil {
		return nil, err
	}
	// Consume the saved challenge before checking kind or parsing the response;
	// Redis removal survives later rejection, so another finish cannot reuse
	// this ceremony.
	session, err := s.sessions.Consume(ctx, sessionToken)
	if err != nil {
		return nil, err
	}
	if session == nil || session.Kind != passkeySessionRegistration || session.UserID != userID {
		return nil, ErrPasskeySession
	}
	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	handle, err := s.repo.GetUserHandle(ctx, userID)
	if err != nil {
		return nil, err
	}
	waUser, err := s.loadWebAuthnUser(ctx, user, handle)
	if err != nil {
		return nil, err
	}
	// Parsing or SDK validation failure becomes the common verification
	// rejection; the saved challenge is already consumed and no credential
	// success is returned.
	credential, err := s.webAuthn.FinishRegistration(waUser, session.WebAuthn, request)
	if err != nil {
		return nil, ErrPasskeyVerify
	}

	// Persist the verifier's credential before exposing success metadata;
	// uniqueness or storage failure occurs after the registration challenge has
	// already been consumed.
	record, err := s.repo.Create(ctx, &PasskeyCredentialRecord{
		UserID:     userID,
		UserHandle: handle,
		Name:       normalizePasskeyName(name),
		Credential: *credential,
	})
	if err != nil {
		return nil, err
	}
	return passkeySummary(record), nil
}

// BeginLogin creates a discoverable credential ceremony requiring user
// verification. It stores the returned WebAuthn session for five minutes
// before returning its token; the browser assertion is later matched to a
// current credential owner during FinishLogin.
func (s *PasskeyService) BeginLogin(
	ctx context.Context,
) (assertion *protocol.CredentialAssertion, sessionToken string, err error) {
	if err = s.requireEnabled(); err != nil {
		return nil, "", err
	}
	assertion, session, err := s.webAuthn.BeginDiscoverableLogin(
		webauthn.WithUserVerification(protocol.VerificationRequired),
	)
	if err != nil {
		return nil, "", fmt.Errorf("begin passkey login: %w", err)
	}
	// Publish the discoverable challenge before returning its token, so finish
	// can resolve the saved verifier state before performing its irreversible
	// consumption.
	sessionToken, err = s.sessions.Store(ctx, &PasskeySession{
		Kind:     passkeySessionLogin,
		WebAuthn: *session,
	}, passkeySessionTTL)
	if err != nil {
		return nil, "", err
	}
	return assertion, sessionToken, nil
}

// FinishLogin consumes the saved login challenge before parsing/verifying the
// assertion. Its discoverable-user callback matches raw credential ID and
// stored user-handle bytes, then loads an active owner and the owner's
// credentials for the verifier. Lookup/mismatch failures become verification
// rejection.
//
// Successful verification requires a nonnil service-created passkeyUser and
// credential. The updated authenticator credential and operation-clock
// use time are persisted before the account is returned, so a later
// credential write failure follows proven possession and consumed challenge
// state. Platform token issuance belongs to the HTTP caller after this method
// succeeds; SDK ceremony expiry uses the verifier's own clock.
func (s *PasskeyService) FinishLogin(
	ctx context.Context,
	sessionToken string,
	request io.Reader,
) (*User, error) {
	if err := s.requireEnabled(); err != nil {
		return nil, err
	}
	// Consume the saved challenge before checking kind or parsing the response;
	// Redis removal survives later rejection, so another finish cannot reuse
	// this ceremony.
	session, err := s.sessions.Consume(ctx, sessionToken)
	if err != nil {
		return nil, err
	}
	if session == nil || session.Kind != passkeySessionLogin {
		return nil, ErrPasskeySession
	}

	handler := func(rawID, userHandle []byte) (webauthn.User, error) {
		record, lookupErr := s.repo.GetByCredentialID(ctx, rawID)
		// Credential lookup and handle mismatch supply no verified owner; return
		// common verification rejection after consumption while account lookup
		// still checks current owner activity.
		if lookupErr != nil || record == nil || !bytes.Equal(record.UserHandle, userHandle) {
			return nil, ErrPasskeyVerify
		}
		account, lookupErr := s.userRepo.GetByID(ctx, record.UserID)
		// An unreadable or inactive owner cannot authenticate through the stored
		// credential; collapse this account result into the same verification
		// rejection before SDK completion.
		if lookupErr != nil || account == nil || !account.IsActive() {
			return nil, ErrPasskeyVerify
		}
		return s.loadWebAuthnUser(ctx, account, record.UserHandle)
	}

	// Parsing or SDK validation failure becomes the common verification
	// rejection; the saved challenge is already consumed and no credential
	// success is returned.
	validatedUser, credential, err := s.webAuthn.FinishPasskeyLogin(handler, session.WebAuthn, request)
	if err != nil {
		return nil, ErrPasskeyVerify
	}
	waUser, ok := validatedUser.(*passkeyUser)
	if !ok || waUser.account == nil {
		return nil, ErrPasskeyVerify
	}
	// Save returned authenticator counters/flags before exposing the account to
	// token issuance; write failure follows successful proof and does not
	// restore the consumed login challenge.
	if err = s.repo.UpdateCredential(ctx, waUser.account.ID, credential, s.now().UTC()); err != nil {
		return nil, err
	}
	return waUser.account, nil
}

// List reads the account's stored credentials after feature/verifier
// admission and returns an allocated summary slice. LastUsedAt pointers
// retain record sharing, while Backup reports authenticator backup
// eligibility; repository failures remain errors.
func (s *PasskeyService) List(ctx context.Context, userID int64) ([]PasskeyCredentialSummary, error) {
	if err := s.requireEnabled(); err != nil {
		return nil, err
	}
	records, err := s.repo.ListByUserID(ctx, userID)
	if err != nil {
		return nil, err
	}
	result := make([]PasskeyCredentialSummary, 0, len(records))
	for i := range records {
		result = append(result, *passkeySummary(&records[i]))
	}
	return result, nil
}

// Rename normalizes a credential display name and delegates the
// account-scoped write after feature/verifier admission. Blank text selects
// Passkey and overlong names retain one hundred complete runes; a missing
// account-owned credential remains a repository error.
func (s *PasskeyService) Rename(ctx context.Context, userID, credentialID int64, name string) error {
	if err := s.requireEnabled(); err != nil {
		return err
	}
	return s.repo.Rename(ctx, userID, credentialID, normalizePasskeyName(name))
}

// Delete delegates account-scoped credential removal after feature/verifier
// admission. The PostgreSQL repository requires an affected row and reports a
// missing account-owned credential; this operation leaves existing platform
// sessions to their separate lifecycle.
func (s *PasskeyService) Delete(ctx context.Context, userID, credentialID int64) error {
	if err := s.requireEnabled(); err != nil {
		return err
	}
	return s.repo.Delete(ctx, userID, credentialID)
}

// loadWebAuthnUser loads the account's credentials into a fresh slice of
// credential structs. It requires a nonnil account, retains that account
// pointer and supplied handle, and shares nested credential references;
// callers keep these observations stable throughout the verifier call.
func (s *PasskeyService) loadWebAuthnUser(
	ctx context.Context,
	user *User,
	handle []byte,
) (*passkeyUser, error) {
	records, err := s.repo.ListByUserID(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	credentials := make([]webauthn.Credential, 0, len(records))
	for i := range records {
		credentials = append(credentials, records[i].Credential)
	}
	return &passkeyUser{account: user, handle: handle, credentials: credentials}, nil
}

// normalizePasskeyName trims display text, selects Passkey when blank and
// limits nonblank names to one hundred runes. Truncation keeps complete
// Unicode characters; this helper does not validate whether the result names
// a particular device.
func normalizePasskeyName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return defaultPasskeyName
	}
	runes := []rune(name)
	// Keep whole characters within the credential display-name rune limit.
	if len(runes) > maxPasskeyNameLength {
		name = string(runes[:maxPasskeyNameLength])
	}
	return name
}

// passkeySummary selects display metadata from a required nonnil credential
// record. LastUsedAt remains a shared pointer, while Backup reads
// BackupEligible rather than current BackupState; callers treat the result as
// credential metadata rather than proof of synchronization.
func passkeySummary(record *PasskeyCredentialRecord) *PasskeyCredentialSummary {
	return &PasskeyCredentialSummary{
		ID:         record.ID,
		Name:       record.Name,
		CreatedAt:  record.CreatedAt,
		LastUsedAt: record.LastUsedAt,
		Backup:     record.Credential.Flags.BackupEligible,
	}
}
