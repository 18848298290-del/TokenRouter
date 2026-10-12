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

const (
	passkeySessionRegistration = "registration"
	passkeySessionLogin        = "login"
	passkeySessionTTL          = 5 * time.Minute
	defaultPasskeyName         = "Passkey"
	maxPasskeyNameLength       = 100
)

var (
	ErrPasskeysDisabled = infraerrors.Forbidden("PASSKEY_DISABLED", "passkey authentication is not enabled")
	ErrPasskeyNotFound  = infraerrors.NotFound("PASSKEY_NOT_FOUND", "passkey not found")
	ErrPasskeyExists    = infraerrors.Conflict("PASSKEY_ALREADY_EXISTS", "this passkey is already registered")
	ErrPasskeySession   = infraerrors.BadRequest("PASSKEY_SESSION_INVALID", "passkey session is invalid or expired")
	ErrPasskeyVerify    = infraerrors.Unauthorized("PASSKEY_VERIFICATION_FAILED", "passkey verification failed")

	_ webauthn.User = (*passkeyUser)(nil)
)

// PasskeyCredentialRecord stores a WebAuthn credential and its owner.
// Credential retains the full library record, including signature counters and validation state.
type PasskeyCredentialRecord struct {
	ID         int64
	UserID     int64
	UserHandle []byte
	Name       string
	Credential webauthn.Credential
	LastUsedAt *time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// PasskeyRepository persists user handles and WebAuthn credentials.
type PasskeyRepository interface {
	EnsureUserHandle(ctx context.Context, userID int64, candidate []byte) ([]byte, error)
	GetUserHandle(ctx context.Context, userID int64) ([]byte, error)
	GetByCredentialID(ctx context.Context, credentialID []byte) (*PasskeyCredentialRecord, error)
	ListByUserID(ctx context.Context, userID int64) ([]PasskeyCredentialRecord, error)
	Create(ctx context.Context, record *PasskeyCredentialRecord) (*PasskeyCredentialRecord, error)
	UpdateCredential(ctx context.Context, userID int64, credential *webauthn.Credential, usedAt time.Time) error
	Rename(ctx context.Context, userID, credentialID int64, name string) error
	Delete(ctx context.Context, userID, credentialID int64) error
}

// PasskeySession stores the server challenge for one registration or login.
type PasskeySession struct {
	Kind     string               `json:"kind"`
	UserID   int64                `json:"user_id,omitempty"`
	WebAuthn webauthn.SessionData `json:"webauthn"`
}

// PasskeySessionStore stores expiring WebAuthn sessions for one-time consumption.
type PasskeySessionStore interface {
	Store(ctx context.Context, session *PasskeySession, ttl time.Duration) (string, error)
	Consume(ctx context.Context, token string) (*PasskeySession, error)
}

// PasskeyCredentialSummary contains credential metadata safe to return to the user.
type PasskeyCredentialSummary struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	Backup     bool       `json:"backup"`
}

type passkeyUser struct {
	account     *User
	handle      []byte
	credentials []webauthn.Credential
}

// PasskeyService handles passkey registration, login, and credential management.
type PasskeyService struct {
	operationClock
	enabled  bool
	webAuthn PasskeyVerifier
	repo     PasskeyRepository
	sessions PasskeySessionStore
	userRepo SessionUserReader
}

// PasskeyVerifier parses and verifies ceremony input after the caller consumes the session.
type PasskeyVerifier interface {
	BeginRegistration(webauthn.User, ...webauthn.RegistrationOption) (*protocol.CredentialCreation, *webauthn.SessionData, error)
	BeginDiscoverableLogin(...webauthn.LoginOption) (*protocol.CredentialAssertion, *webauthn.SessionData, error)
	FinishRegistration(webauthn.User, webauthn.SessionData, io.Reader) (*webauthn.Credential, error)
	FinishPasskeyLogin(webauthn.DiscoverableUserHandler, webauthn.SessionData, io.Reader) (webauthn.User, *webauthn.Credential, error)
}

func (u *passkeyUser) WebAuthnID() []byte {
	return u.handle
}

func (u *passkeyUser) WebAuthnName() string {
	return u.account.Email
}

func (u *passkeyUser) WebAuthnDisplayName() string {
	if name := strings.TrimSpace(u.account.Username); name != "" {
		return name
	}
	return u.account.Email
}

func (u *passkeyUser) WebAuthnCredentials() []webauthn.Credential {
	return u.credentials
}

// NewPasskeyService accepts an assembled verifier and stores its dependencies.
func NewPasskeyService(enabled bool, verifier PasskeyVerifier, repo PasskeyRepository, sessions PasskeySessionStore, users SessionUserReader, clocks ...func() time.Time) *PasskeyService {
	return &PasskeyService{operationClock: clockFromOptional(clocks), enabled: enabled, webAuthn: verifier, repo: repo, sessions: sessions, userRepo: users}
}

func (s *PasskeyService) Enabled() bool {
	return s != nil && s.enabled && s.webAuthn != nil
}

func (s *PasskeyService) requireEnabled() error {
	if !s.Enabled() {
		return ErrPasskeysDisabled
	}
	return nil
}

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

	candidate := make([]byte, 32)
	if _, err = rand.Read(candidate); err != nil {
		return nil, "", fmt.Errorf("generate passkey user handle: %w", err)
	}
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

func (s *PasskeyService) FinishRegistration(
	ctx context.Context,
	userID int64,
	sessionToken, name string,
	request io.Reader,
) (*PasskeyCredentialSummary, error) {
	if err := s.requireEnabled(); err != nil {
		return nil, err
	}
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
	credential, err := s.webAuthn.FinishRegistration(waUser, session.WebAuthn, request)
	if err != nil {
		return nil, ErrPasskeyVerify
	}

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
	sessionToken, err = s.sessions.Store(ctx, &PasskeySession{
		Kind:     passkeySessionLogin,
		WebAuthn: *session,
	}, passkeySessionTTL)
	if err != nil {
		return nil, "", err
	}
	return assertion, sessionToken, nil
}

func (s *PasskeyService) FinishLogin(
	ctx context.Context,
	sessionToken string,
	request io.Reader,
) (*User, error) {
	if err := s.requireEnabled(); err != nil {
		return nil, err
	}
	session, err := s.sessions.Consume(ctx, sessionToken)
	if err != nil {
		return nil, err
	}
	if session == nil || session.Kind != passkeySessionLogin {
		return nil, ErrPasskeySession
	}

	handler := func(rawID, userHandle []byte) (webauthn.User, error) {
		record, lookupErr := s.repo.GetByCredentialID(ctx, rawID)
		if lookupErr != nil || record == nil || !bytes.Equal(record.UserHandle, userHandle) {
			return nil, ErrPasskeyVerify
		}
		account, lookupErr := s.userRepo.GetByID(ctx, record.UserID)
		if lookupErr != nil || account == nil || !account.IsActive() {
			return nil, ErrPasskeyVerify
		}
		return s.loadWebAuthnUser(ctx, account, record.UserHandle)
	}

	validatedUser, credential, err := s.webAuthn.FinishPasskeyLogin(handler, session.WebAuthn, request)
	if err != nil {
		return nil, ErrPasskeyVerify
	}
	waUser, ok := validatedUser.(*passkeyUser)
	if !ok || waUser.account == nil {
		return nil, ErrPasskeyVerify
	}
	if err = s.repo.UpdateCredential(ctx, waUser.account.ID, credential, s.now().UTC()); err != nil {
		return nil, err
	}
	return waUser.account, nil
}

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

func (s *PasskeyService) Rename(ctx context.Context, userID, credentialID int64, name string) error {
	if err := s.requireEnabled(); err != nil {
		return err
	}
	return s.repo.Rename(ctx, userID, credentialID, normalizePasskeyName(name))
}

func (s *PasskeyService) Delete(ctx context.Context, userID, credentialID int64) error {
	if err := s.requireEnabled(); err != nil {
		return err
	}
	return s.repo.Delete(ctx, userID, credentialID)
}

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

func normalizePasskeyName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return defaultPasskeyName
	}
	runes := []rune(name)
	if len(runes) > maxPasskeyNameLength {
		name = string(runes[:maxPasskeyNameLength])
	}
	return name
}

func passkeySummary(record *PasskeyCredentialRecord) *PasskeyCredentialSummary {
	return &PasskeyCredentialSummary{
		ID:         record.ID,
		Name:       record.Name,
		CreatedAt:  record.CreatedAt,
		LastUsedAt: record.LastUsedAt,
		Backup:     record.Credential.Flags.BackupEligible,
	}
}
