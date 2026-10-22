package identity

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"log/slog"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// totp_service.go manages TOTP enrollment, account verification, temporary
// login challenges, and session step-up grants.
// TOTP enrollment caches a plaintext secret and setup token before storing
// the account's encrypted secret. Enabling the account follows that secret
// write separately, while the ciphertext round-trip check produces
// diagnostics and does not decide whether setup succeeds.
//
// Proof selection gives administrators password verification and lets other
// accounts follow the runtime email switch. Login challenges and
// user/session-bound step-up grants have different consumers: login reads
// leave a challenge for later deletion, whereas step-up checks a cached
// presence marker. Account-wide invalid-code counts are maintained separately
// from OTP validation, and their read/write failures have different fail-open
// behavior, so callers cannot treat challenge cleanup or counting as atomic
// proof consumption.

// These lifetimes, attempt limits and issuer text govern enrollment,
// second-factor challenges and the session grants consumed by
// sensitive-operation checks.
const (
	totpSetupTTL = 5 * time.Minute // pending enrollment cache lifetime
	totpLoginTTL = 5 * time.Minute // second-factor login cache lifetime

	maxTotpAttempts = 5             // maximum readable invalid-proof count
	totpIssuer      = "TokenRouter" // enrollment issuer display text

	// StepUpGrantTTL is the validity window for sensitive-operation TOTP grants.
	StepUpGrantTTL = 15 * time.Minute
)

// These errors distinguish feature policy, enrollment state, invalid proof
// and attempt limits when HTTP callers report TOTP operation failures.
var (
	ErrTotpNotEnabled      = infraerrors.BadRequest("TOTP_NOT_ENABLED", "totp feature is not enabled")
	ErrTotpAlreadyEnabled  = infraerrors.BadRequest("TOTP_ALREADY_ENABLED", "totp is already enabled for this account")
	ErrTotpNotSetup        = infraerrors.BadRequest("TOTP_NOT_SETUP", "totp is not set up for this account")
	ErrTotpInvalidCode     = infraerrors.BadRequest("TOTP_INVALID_CODE", "invalid totp code")
	ErrTotpSetupExpired    = infraerrors.BadRequest("TOTP_SETUP_EXPIRED", "totp setup session expired")
	ErrTotpTooManyAttempts = infraerrors.TooManyRequests("TOTP_TOO_MANY_ATTEMPTS", "too many verification attempts, please try again later")
	ErrVerifyCodeRequired  = infraerrors.BadRequest("VERIFY_CODE_REQUIRED", "email verification code is required")
	ErrPasswordRequired    = infraerrors.BadRequest("PASSWORD_REQUIRED", "password is required")
)

// TotpCache stores TOTP challenges, attempt counts and user/session-bound
// step-up markers.
type TotpCache interface {
	// GetSetupSession reads enrollment state for the account without consuming
	// it. Absence returns nil in the Redis adapter, while read/decode failures
	// remain errors for CompleteSetup to classify as an expired challenge.
	GetSetupSession(ctx context.Context, userID int64) (*TotpSetupSession, error)
	// SetSetupSession replaces the account's pending enrollment state for the
	// supplied TTL. The service uses five minutes and requires stable input
	// through serialization; a successful write precedes returning sensitive
	// enrollment material.
	SetSetupSession(ctx context.Context, userID int64, session *TotpSetupSession, ttl time.Duration) error
	// DeleteSetupSession removes the account's pending enrollment state. Missing
	// state succeeds in Redis, and CompleteSetup attempts deletion after
	// enabling the account while suppressing any cleanup failure.
	DeleteSetupSession(ctx context.Context, userID int64) error

	// GetLoginSession reads a temporary token's second-factor state without
	// deletion. Redis absence returns nil and other failures remain errors; HTTP
	// proof and current-account checks precede the separate DeleteLoginSession
	// call.
	GetLoginSession(ctx context.Context, tempToken string) (*TotpLoginSession, error)
	// SetLoginSession stores second-factor state under the supplied temporary
	// token and lifetime. The service uses five minutes; serialization happens
	// before publication and the token is returned only after that write
	// succeeds.
	SetLoginSession(ctx context.Context, tempToken string, session *TotpLoginSession, ttl time.Duration) error
	// DeleteLoginSession removes temporary second-factor state selected by
	// token. Missing Redis state succeeds, allowing later cleanup to repeat; the
	// HTTP login caller chooses deletion timing after proof and account checks.
	DeleteLoginSession(ctx context.Context, tempToken string) error

	// IncrementVerifyAttempts records another invalid account-wide proof. Redis
	// increments before refreshing a fifteen-minute idle expiry in a pipeline,
	// so errors can follow partial recording; VerifyCode suppresses those errors
	// when returning invalid-code.
	IncrementVerifyAttempts(ctx context.Context, userID int64) (int, error)
	// GetVerifyAttempts reads account-wide invalid-proof history. Missing Redis
	// state returns zero; other failures remain errors that VerifyCode ignores
	// for its admission decision, permitting code verification to continue.
	GetVerifyAttempts(ctx context.Context, userID int64) (int, error)
	// ClearVerifyAttempts deletes account-wide invalid-proof history after
	// successful TOTP validation. Missing state succeeds in Redis; VerifyCode
	// suppresses cleanup failures and still treats the proven code as valid.
	ClearVerifyAttempts(ctx context.Context, userID int64) error

	// SetStepUpGrant stores a presence marker for the supplied user/session pair
	// and lifetime. VerifyStepUp uses StepUpGrantTTL; callers retain the
	// matching session key for later sensitive-operation checks.
	SetStepUpGrant(ctx context.Context, userID int64, sessionKey string, ttl time.Duration) error
	// HasStepUpGrant checks the supplied user/session marker. Missing Redis
	// state returns false and read failures remain errors; sensitive-operation
	// callers distinguish unavailable storage from a usable recently verified
	// grant.
	HasStepUpGrant(ctx context.Context, userID int64, sessionKey string) (bool, error)
}

// SecretEncryptor protects enrollment secrets and recoverable administrator credentials
// using authenticated ciphertext. Consumers of durable stored values require a
// stable deployment key.
type SecretEncryptor interface {
	// Encrypt returns an authenticated ciphertext representation of the supplied
	// secret. Encryption failures prevent persistence; callers store ciphertext
	// while delivering plaintext only through their authorized setup or copy flow.
	Encrypt(plaintext string) (string, error)
	// Decrypt authenticates stored ciphertext before returning its original secret.
	// Callers authorize disclosure or proof verification before using the plaintext;
	// failures must not return a partial or unchecked secret value.
	Decrypt(ciphertext string) (string, error)
}

// TotpSetupSession carries the cached plaintext enrollment secret and its
// request proof.
type TotpSetupSession struct {
	Secret     string    // base32 plaintext enrollment secret
	SetupToken string    // random enrollment request proof
	CreatedAt  time.Time // setup operation clock time
}

// TotpLoginSession carries account context for a temporary second-factor
// login challenge.
type TotpLoginSession struct {
	UserID int64  // first-factor target account identifier
	Email  string // first-factor account email hint
	// TokenExpiry records intended challenge expiry at creation. Login2FA uses
	// cache availability rather than comparing this timestamp, so the store's
	// lifetime governs admission.
	TokenExpiry time.Time
	// PendingOAuthBind requests OAuth binding after successful proof when
	// nonnil. createLoginSession retains this pointer until the cache encodes
	// the challenge.
	PendingOAuthBind *PendingOAuthBindLoginSession `json:"pending_oauth_bind,omitempty"`
}

// PendingOAuthBindLoginSession retains browser-bound OAuth identifiers
// through the second-factor login step.
type PendingOAuthBindLoginSession struct {
	// PendingSessionToken and BrowserSessionKey refer to the same pending
	// browser flow. CompleteSecondFactorBinding reloads that pair before forcing
	// identity binding to the second-factor account.
	PendingSessionToken string `json:"pending_session_token,omitempty"` // pending OAuth session identifier
	BrowserSessionKey   string `json:"browser_session_key,omitempty"`   // pending browser binding key
}

// TotpStatus combines observed account enrollment state with the global
// feature switch.
type TotpStatus struct {
	Enabled bool `json:"enabled"` // observed account enrollment flag
	// EnabledAt retains the account's enable timestamp pointer. GetStatus passes
	// it through unchanged, so callers treat the pointed-to value as shared.
	EnabledAt      *time.Time `json:"enabled_at,omitempty"`
	FeatureEnabled bool       `json:"feature_enabled"` // observed global feature switch
}

// TotpSetupResponse supplies sensitive enrollment material for frontend
// authenticator setup.
type TotpSetupResponse struct {
	Secret string `json:"secret"` // base32 plaintext enrollment secret
	// QRCodeURL contains the key's otpauth enrollment URI. The frontend turns
	// this value into a QR image for the authenticator to scan.
	QRCodeURL  string `json:"qr_code_url"`
	SetupToken string `json:"setup_token"` // random enrollment request proof
	Countdown  int    `json:"countdown"`   // seconds
}

// TotpService manages account TOTP enrollment, proof checks and temporary
// challenge grants.
type TotpService struct {
	// operationClock supplies the first optional clock selected by
	// NewTotpService. OTP validation and challenge timestamps use that retained
	// clock throughout operations.
	operationClock
	userRepo       TotpUserStore   // current account security storage
	encryptor      SecretEncryptor // account secret ciphertext conversion
	cache          TotpCache       // temporary TOTP state storage
	settingService TotpSettings    // runtime feature/proof settings reader
	// emailService checks mailbox proofs for the selected email path. Password
	// verification does not use it, so password-only test fixtures can leave it
	// nil.
	emailService      VerificationEmail
	emailQueueService VerificationQueue // verification email queue admission
}

// VerificationMethod names the proof selected for an account's TOTP settings
// changes.
type VerificationMethod struct {
	Method string `json:"method"` // email or password
}

// TotpUserStore reads current accounts and persists their TOTP security
// state.
type TotpUserStore interface {
	// SessionUserReader supplies current account records used before proof or
	// enrollment changes.
	SessionUserReader
	// UpdateTotpSecret stores encrypted text or clears it when the pointer is
	// nil. This write is separate from EnableTotp in setup, so an enable failure
	// can leave the secret saved.
	UpdateTotpSecret(context.Context, int64, *string) error
	// EnableTotp marks the account enabled and records enable time.
	// CompleteSetup writes the encrypted secret first; the repository operation
	// itself does not require that secret to exist before enabling.
	EnableTotp(context.Context, int64) error
	// DisableTotp clears the account's enabled flag, encrypted secret and enable
	// timestamp. The service performs email/password proof first, while
	// repository errors remain failures of this security-state write.
	DisableTotp(context.Context, int64) error
}

// TotpSettings supplies feature/proof switches and verification email display
// text.
type TotpSettings interface {
	// IsTotpEnabled reports the runtime switch admitting new TOTP setup
	// operations.
	IsTotpEnabled(context.Context) bool
	// IsEmailVerifyEnabled reports whether nonadministrators use email proof for
	// TOTP settings changes.
	IsEmailVerifyEnabled(context.Context) bool
	// GetSiteName returns the display name supplied to queued verification
	// email.
	GetSiteName(context.Context) string
}

// VerificationEmail checks mailbox challenge proof before TOTP settings
// changes.
type VerificationEmail interface {
	// VerifyCode validates the account mailbox's submitted email challenge. TOTP
	// settings select this operation for nonadministrators when email
	// verification is enabled; missing/incorrect proofs remain errors before
	// security-state changes.
	VerifyCode(context.Context, string, string) error
}

// VerificationQueue queues mailbox challenge delivery for the identity email
// worker.
type VerificationQueue interface {
	// EnqueueVerifyCode queues a mailbox challenge with site text and optional
	// locale. Queue admission errors remain errors; actual code generation and
	// delivery occur later through the identity email queue worker.
	EnqueueVerifyCode(string, string, ...string) error
}

// NewTotpService retains TOTP account, encryption, cache, settings and
// verification dependencies. The first optional clock is used when nonnil;
// otherwise wall time supplies operation time. Omitted dependencies remain
// nil, so callers assemble the services required by their chosen setup,
// verification or email path.
func NewTotpService(
	userRepo TotpUserStore,
	encryptor SecretEncryptor,
	cache TotpCache,
	settingService TotpSettings,
	emailService VerificationEmail,
	emailQueueService VerificationQueue,
	clocks ...func() time.Time,
) *TotpService {
	return &TotpService{
		operationClock:    clockFromOptional(clocks),
		userRepo:          userRepo,
		encryptor:         encryptor,
		cache:             cache,
		settingService:    settingService,
		emailService:      emailService,
		emailQueueService: emailQueueService,
	}
}

// GetStatus reads the global TOTP feature flag and current account state
// separately. The returned EnabledAt pointer remains shared with the account
// value; user lookup errors are wrapped, and status visibility does not
// itself admit verification.
func (s *TotpService) GetStatus(ctx context.Context, userID int64) (*TotpStatus, error) {
	featureEnabled := s.settingService.IsTotpEnabled(ctx)

	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("get user: %w", err)
	}

	return &TotpStatus{
		Enabled:        user.TotpEnabled,
		EnabledAt:      user.TotpEnabledAt,
		FeatureEnabled: featureEnabled,
	}, nil
}

// usesEmailVerification selects email proof for a nonnil nonadministrator
// when email verification is enabled. Administrators and other accounts use
// passwords; this selection can request email proof even when the account has
// a placeholder address.
func (s *TotpService) usesEmailVerification(ctx context.Context, user *User) bool {
	return user != nil && user.Role != RoleAdmin && s.settingService.IsEmailVerifyEnabled(ctx)
}

// verifyIdentity checks the proof selected for this account's TOTP settings
// change. Nil users return ErrUserNotFound; email selection requires a
// nonempty code and configured verifier, while password selection checks the
// supplied password against the stored hash.
func (s *TotpService) verifyIdentity(ctx context.Context, user *User, emailCode, password string) error {
	if user == nil {
		return ErrUserNotFound
	}
	if s.usesEmailVerification(ctx, user) {
		if emailCode == "" {
			return ErrVerifyCodeRequired
		}
		return s.emailService.VerifyCode(ctx, user.Email, emailCode)
	}
	if password == "" {
		return ErrPasswordRequired
	}
	if !user.CheckPassword(password) {
		return ErrPasswordIncorrect
	}
	return nil
}

// InitiateSetup creates and caches enrollment material after checking feature
// availability, account state and the selected email/password proof. An
// already enabled account is rejected, while successful setup replaces that
// user's cached challenge and returns its plaintext base32 secret, enrollment
// URI and random setup token.
//
// The cache stores the secret before this response is returned, with a
// five-minute lifetime separate from persistent encrypted account state. No
// encrypted account secret is written yet. Callers retain these sensitive
// enrollment values until CompleteSetup proves the challenge; the QRCodeURL
// field contains an otpauth URI that a frontend renders, not an image
// address.
func (s *TotpService) InitiateSetup(ctx context.Context, userID int64, emailCode, password string) (*TotpSetupResponse, error) {
	if !s.settingService.IsTotpEnabled(ctx) {
		return nil, ErrTotpNotEnabled
	}

	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("get user: %w", err)
	}

	if user.TotpEnabled {
		return nil, ErrTotpAlreadyEnabled
	}

	if err := s.verifyIdentity(ctx, user, emailCode, password); err != nil {
		return nil, err
	}

	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      totpIssuer,
		AccountName: user.Email,
	})
	if err != nil {
		return nil, fmt.Errorf("generate totp key: %w", err)
	}

	setupToken, err := GenerateRandomToken(32)
	if err != nil {
		return nil, fmt.Errorf("generate setup token: %w", err)
	}

	session := &TotpSetupSession{
		Secret:     key.Secret(),
		SetupToken: setupToken,
		CreatedAt:  s.now(),
	}

	// Publish enrollment state before returning its secret/token pair, so
	// CompleteSetup can find the matching challenge when the client submits its
	// authenticator proof.
	if err := s.cache.SetSetupSession(ctx, userID, session, totpSetupTTL); err != nil {
		return nil, fmt.Errorf("store setup session: %w", err)
	}

	return &TotpSetupResponse{
		Secret:     key.Secret(),
		QRCodeURL:  key.URL(),
		SetupToken: setupToken,
		// Expose the cache lifetime as whole seconds for the client countdown.
		Countdown: int(totpSetupTTL.Seconds()),
	}, nil
}

// CompleteSetup checks the cached setup token and current TOTP proof before
// persisting enrollment. Cache read failure or missing state becomes
// setup-expired, and a token mismatch uses constant-time comparison before
// any encryption or account write.
//
// The encrypted secret is written before EnableTotp is called as a separate
// repository operation. A failed enable can therefore follow a stored secret.
// Decryption of the candidate ciphertext supplies debug observations only: an
// error or mismatch does not reject enrollment. Successful enable attempts
// cache deletion and ignores deletion failure; setup reads and writes do not
// atomically claim the challenge, so callers cannot infer one-time setup
// consumption from this sequence.
func (s *TotpService) CompleteSetup(ctx context.Context, userID int64, totpCode, setupToken string) error {
	if !s.settingService.IsTotpEnabled(ctx) {
		return ErrTotpNotEnabled
	}

	// Unreadable setup state cannot supply its secret or request proof; classify
	// storage/decode failure as expired setup before any account security write.
	session, err := s.cache.GetSetupSession(ctx, userID)
	if err != nil {
		return ErrTotpSetupExpired
	}

	if session == nil {
		return ErrTotpSetupExpired
	}

	// Compare the submitted enrollment proof before encrypting or saving account
	// state, so possession of a valid TOTP alone does not select another setup challenge.
	if subtle.ConstantTimeCompare([]byte(session.SetupToken), []byte(setupToken)) != 1 {
		return ErrTotpSetupExpired
	}

	if !s.validateCode(totpCode, session.Secret) {
		return ErrTotpInvalidCode
	}

	setupSecretPrefix := "N/A"
	// Keep at most four secret bytes in setup diagnostics.
	if len(session.Secret) >= 4 {
		setupSecretPrefix = session.Secret[:4]
	}
	slog.Debug("totp_complete_setup_before_encrypt",
		"user_id", userID,
		"secret_len", len(session.Secret),
		"secret_prefix", setupSecretPrefix)

	encryptedSecret, err := s.encryptor.Encrypt(session.Secret)
	if err != nil {
		return fmt.Errorf("encrypt totp secret: %w", err)
	}

	slog.Debug("totp_complete_setup_encrypted",
		"user_id", userID,
		"encrypted_len", len(encryptedSecret))

	// Observe the ciphertext round trip for diagnostics; its error or mismatch
	// does not reject setup, and the encrypted candidate still proceeds to
	// persistence.
	decrypted, decErr := s.encryptor.Decrypt(encryptedSecret)
	if decErr != nil {
		slog.Debug("totp_complete_setup_verify_failed",
			"user_id", userID,
			"error", decErr)
	} else {
		decryptedPrefix := "N/A"
		// Limit decrypted-secret diagnostics to the same byte prefix.
		if len(decrypted) >= 4 {
			decryptedPrefix = decrypted[:4]
		}
		slog.Debug("totp_complete_setup_verified",
			"user_id", userID,
			"original_len", len(session.Secret),
			"decrypted_len", len(decrypted),
			"match", session.Secret == decrypted,
			"decrypted_prefix", decryptedPrefix)
	}

	// Save ciphertext before the separate enable write; a later enable failure
	// leaves the secret persisted and requires the caller to handle incomplete
	// enrollment.
	if err := s.userRepo.UpdateTotpSecret(ctx, userID, &encryptedSecret); err != nil {
		return fmt.Errorf("update totp secret: %w", err)
	}

	if err := s.userRepo.EnableTotp(ctx, userID); err != nil {
		return fmt.Errorf("enable totp: %w", err)
	}

	// Account enrollment is already enabled, so setup-cache deletion is
	// attempted as cleanup and its failure cannot turn that completed write into
	// an expired setup result.
	_ = s.cache.DeleteSetupSession(ctx, userID)

	return nil
}

// Disable verifies the account's selected email/password proof before asking
// the repository to clear TOTP state. It requires an already enabled account,
// accepts operation when the global TOTP feature switch is off, and returns
// lookup/proof/write failures.
func (s *TotpService) Disable(ctx context.Context, userID int64, emailCode, password string) error {
	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return fmt.Errorf("get user: %w", err)
	}

	if !user.TotpEnabled {
		return ErrTotpNotSetup
	}

	if err := s.verifyIdentity(ctx, user, emailCode, password); err != nil {
		return err
	}

	if err := s.userRepo.DisableTotp(ctx, userID); err != nil {
		return fmt.Errorf("disable totp: %w", err)
	}

	return nil
}

// VerifyCode validates an enabled account's encrypted-secret TOTP against
// operation time. A readable attempt count at the configured maximum rejects
// the request before account lookup; count read failure allows verification
// to continue. The global feature switch is not checked here.
//
// Account lookup and decryption failures become the common verification
// error. An invalid code attempts to increment the account-wide counter,
// while a valid code attempts to clear it; both cache-write failures are
// suppressed. The Redis adapter refreshes the counter's idle lifetime on
// recording, so this path combines a time-window OTP proof with separately
// maintained attempt state rather than atomically reserving a verification
// slot.
func (s *TotpService) VerifyCode(ctx context.Context, userID int64, code string) error {
	slog.Debug("totp_verify_code_called",
		"user_id", userID,
		"code_len", len(code))

	// A readable exhausted counter rejects before secret lookup; counter read
	// failure supplies no usable limit observation and permits the OTP check to
	// continue.
	attempts, err := s.cache.GetVerifyAttempts(ctx, userID)
	if err == nil && attempts >= maxTotpAttempts {
		return ErrTotpTooManyAttempts
	}

	// Account lookup failure supplies no secret to validate; return the common
	// verification error while retaining the detailed failure in debug logging.
	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		slog.Debug("totp_verify_get_user_failed",
			"user_id", userID,
			"error", err)
		return infraerrors.InternalServer("TOTP_VERIFY_ERROR", "failed to verify totp code")
	}

	if !user.TotpEnabled || user.TotpSecretEncrypted == nil {
		slog.Debug("totp_verify_not_setup",
			"user_id", userID,
			"enabled", user.TotpEnabled,
			"has_secret", user.TotpSecretEncrypted != nil)
		return ErrTotpNotSetup
	}

	slog.Debug("totp_verify_encrypted_secret",
		"user_id", userID,
		"encrypted_len", len(*user.TotpSecretEncrypted))

	// Ciphertext failure prevents proof validation; report the common
	// verification error rather than exposing the encryptor's failure text to
	// the HTTP caller.
	secret, err := s.encryptor.Decrypt(*user.TotpSecretEncrypted)
	if err != nil {
		slog.Debug("totp_verify_decrypt_failed",
			"user_id", userID,
			"error", err)
		return infraerrors.InternalServer("TOTP_VERIFY_ERROR", "failed to verify totp code")
	}

	secretPrefix := "N/A"
	// Limit verification diagnostics to a four-byte secret prefix.
	if len(secret) >= 4 {
		secretPrefix = secret[:4]
	}
	slog.Debug("totp_verify_decrypted",
		"user_id", userID,
		"secret_len", len(secret),
		"secret_prefix", secretPrefix)

	valid := s.validateCode(code, secret)
	slog.Debug("totp_verify_result",
		"user_id", userID,
		"valid", valid,
		"secret_len", len(secret),
		"secret_prefix", secretPrefix,
		"server_time", s.now().UTC().Format(time.RFC3339))

	// Record failed proof before returning invalid-code, but suppress
	// counter-write errors so storage availability does not replace the observed
	// OTP result.
	if !valid {
		_, _ = s.cache.IncrementVerifyAttempts(ctx, userID)
		return ErrTotpInvalidCode
	}

	// A proven code resets attempt history when cleanup succeeds; suppress
	// deletion failure so already validated proof still admits this operation.
	_ = s.cache.ClearVerifyAttempts(ctx, userID)

	return nil
}

// VerifyStepUp verifies the account's TOTP and then stores a
// user/session-bound grant. Callers pass the same sessionKey used by
// sensitive-operation checks; a grant write failure follows successful proof
// and an attempt to clear invalid-code history but returns no granted lifetime.
func (s *TotpService) VerifyStepUp(ctx context.Context, userID int64, sessionKey, code string) (time.Duration, error) {
	if err := s.VerifyCode(ctx, userID, code); err != nil {
		return 0, err
	}
	// Store the grant after successful proof, using the caller's session key;
	// grant-write failure follows verified TOTP but leaves sensitive-operation
	// permission unconfirmed.
	if err := s.cache.SetStepUpGrant(ctx, userID, sessionKey, StepUpGrantTTL); err != nil {
		return 0, fmt.Errorf("store step-up grant: %w", err)
	}
	return StepUpGrantTTL, nil
}

// HasStepUpGrant delegates the supplied account/session presence check to the
// cache. Missing grants return false in the Redis adapter, while storage
// failures remain errors so sensitive-operation callers can reject an
// unavailable check.
func (s *TotpService) HasStepUpGrant(ctx context.Context, userID int64, sessionKey string) (bool, error) {
	return s.cache.HasStepUpGrant(ctx, userID, sessionKey)
}

// CreateLoginSession stores a temporary second-factor challenge for the
// supplied account and email. It returns the random token after the cache
// write succeeds; the caller performs first-factor/current-account checks and
// later deletes the challenge separately.
func (s *TotpService) CreateLoginSession(ctx context.Context, userID int64, email string) (string, error) {
	return s.createLoginSession(ctx, userID, email, nil)
}

// CreatePendingOAuthBindLoginSession stores a TOTP challenge with its pending
// OAuth browser identifiers. The caller supplies a verified target account
// and matching pending session; successful second-factor proof lets the HTTP
// flow perform binding before separate challenge deletion.
func (s *TotpService) CreatePendingOAuthBindLoginSession(
	ctx context.Context,
	userID int64,
	email string,
	pendingSessionToken string,
	browserSessionKey string,
) (string, error) {
	return s.createLoginSession(ctx, userID, email, &PendingOAuthBindLoginSession{
		PendingSessionToken: pendingSessionToken,
		BrowserSessionKey:   browserSessionKey,
	})
}

// createLoginSession generates a temporary token and stores the supplied
// account/email with an optional pending-binding pointer. The token is
// returned only after a successful five-minute cache write; that pointer
// stays shared until the store encodes it.
//
// TokenExpiry records the same intended lifetime in the value, but Login2FA
// relies on cache availability and does not compare that timestamp itself.
// Getting the challenge leaves it available, and later deletion is a separate
// operation. This helper therefore establishes temporary stored context,
// while the HTTP caller owns proof checks, deletion timing and eventual
// platform token issuance.
func (s *TotpService) createLoginSession(
	ctx context.Context,
	userID int64,
	email string,
	pendingOAuthBind *PendingOAuthBindLoginSession,
) (string, error) {
	tempToken, err := GenerateRandomToken(32)
	if err != nil {
		return "", fmt.Errorf("generate temp token: %w", err)
	}

	session := &TotpLoginSession{
		UserID:           userID,
		Email:            email,
		TokenExpiry:      s.now().Add(totpLoginTTL),
		PendingOAuthBind: pendingOAuthBind,
	}

	// Publish the challenge before returning its temporary token, so the next
	// proof request can resolve account and pending-binding context from that
	// token.
	if err := s.cache.SetLoginSession(ctx, tempToken, session, totpLoginTTL); err != nil {
		return "", fmt.Errorf("store login session: %w", err)
	}

	return tempToken, nil
}

// GetLoginSession reads the temporary token's cached challenge without
// consuming it. The Redis adapter returns nil for absence and separately
// decoded state for presence; callers handle cache errors and perform
// proof/current-account checks before deleting it.
func (s *TotpService) GetLoginSession(ctx context.Context, tempToken string) (*TotpLoginSession, error) {
	return s.cache.GetLoginSession(ctx, tempToken)
}

// DeleteLoginSession delegates removal of the temporary token's challenge to
// the cache. Missing state succeeds in the Redis adapter; Login2FA invokes
// this after proof/account checks and suppresses its failure before issuing
// platform credentials.
func (s *TotpService) DeleteLoginSession(ctx context.Context, tempToken string) error {
	return s.cache.DeleteLoginSession(ctx, tempToken)
}

// MaskEmail builds a display hint from byte positions around the first @
// character. Input shorter than three bytes becomes three stars; malformed
// input with no usable local part keeps its first byte, while a short local
// part keeps only its first byte plus the complete domain.
//
// Longer local parts keep their first and last bytes around the mask. These
// slices do not preserve complete UTF-8 characters and the helper performs no
// mailbox validation, so callers use its result as a hint rather than a
// normalized email identifier. The full supplied domain remains visible for
// ordinary addresses.
func MaskEmail(email string) string {
	// Very short input exposes no mailbox prefix in the display hint.
	if len(email) < 3 {
		return "***"
	}

	atIdx := -1
	for i, c := range email {
		if c == '@' {
			atIdx = i
			break
		}
	}

	// Malformed address text has no usable domain split, so retain only its
	// first byte as a hint instead of presenting the whole supplied input.
	if atIdx == -1 || atIdx < 1 {
		return email[:1] + "***"
	}

	localPart := email[:atIdx]
	domain := email[atIdx:]

	// Short local parts expose only their first byte before masking.
	if len(localPart) <= 2 {
		return localPart[:1] + "***" + domain
	}

	return localPart[:1] + "***" + localPart[len(localPart)-1:] + domain
}

// GenerateRandomToken returns hexadecimal text for byteLength cryptographically
// random bytes with nil error. Zero returns empty text, a negative length
// panics at allocation, and encoding doubles the selected byte count.
// crypto/rand.Read terminates the process when its io.ReadFull operation
// reports an error.
func GenerateRandomToken(byteLength int) (string, error) {
	b := make([]byte, byteLength)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// GetVerificationMethod reports the proof kind selected for the current
// account's TOTP settings changes. Administrators receive password even with
// email verification enabled; missing users and lookup failures remain
// errors, and a usable nonadministrator follows the runtime email switch.
func (s *TotpService) GetVerificationMethod(ctx context.Context, userID int64) (*VerificationMethod, error) {
	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("get user: %w", err)
	}
	if user == nil {
		return nil, ErrUserNotFound
	}
	if s.usesEmailVerification(ctx, user) {
		return &VerificationMethod{Method: "email"}, nil
	}
	return &VerificationMethod{Method: "password"}, nil
}

// SendVerifyCode queues the account's email challenge when global email
// verification is enabled. It loads the current email and site name before
// enqueueing with optional locale; this path does not restrict account role
// or require global TOTP enablement.
func (s *TotpService) SendVerifyCode(ctx context.Context, userID int64, locale ...string) error {
	if !s.settingService.IsEmailVerifyEnabled(ctx) {
		return infraerrors.BadRequest("EMAIL_VERIFY_NOT_ENABLED", "email verification is not enabled")
	}

	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return fmt.Errorf("get user: %w", err)
	}

	siteName := s.settingService.GetSiteName(ctx)

	return s.emailQueueService.EnqueueVerifyCode(user.Email, siteName, locale...)
}

// validateCode checks a six-digit SHA-1 TOTP using thirty-second periods and
// one adjacent step on each side. Library validation errors become false; the
// service clock supplies UTC time so callers see the same rejection as an
// incorrect code.
func (s *TotpService) validateCode(code, secret string) bool {
	// Accept the configured adjacent time steps for clock skew; library errors
	// become invalid proof, keeping malformed secrets/codes out of the
	// successful verification path.
	valid, _ := totp.ValidateCustom(code, secret, s.now().UTC(), totp.ValidateOpts{Period: 30, Skew: 1, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
	return valid
}
