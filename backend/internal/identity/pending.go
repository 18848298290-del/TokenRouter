package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"strings"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// Pending authentication keeps provider identity, browser binding and
// completion credentials beside local decision state. A session lifetime and
// an optional completion lifetime can both reject a snapshot, while any
// recorded consumption takes precedence over those expiry checks.
//
// The core validator reads one time observation and checks browser text after
// state eligibility. Storage consumption rechecks its current-row conditions
// separately, including a lifetime comparison that differs from the core's
// zero-expiry exemption. Local-state sanitization removes immediate
// credentials from recognized completion maps, while shallow copies preserve
// nested sharing. Initial session creation and completion-code issuance have
// their own persistence boundaries, so these models do not imply one atomic
// flow-wide decision.

// These defaults bound newly created pending sessions and separately issued
// completion credentials when their adapter receives an omitted or nonpositive lifetime.
const (
	DefaultPendingAuthTTL           = 15 * time.Minute
	DefaultPendingAuthCompletionTTL = 5 * time.Minute
)

// Pending-state errors distinguish missing sessions, expiry, consumption and
// browser mismatch so each credential entry point can select its response category.
var (
	ErrPendingAuthSessionNotFound = infraerrors.NotFound("PENDING_AUTH_SESSION_NOT_FOUND", "pending auth session not found")
	ErrPendingAuthSessionExpired  = infraerrors.Unauthorized("PENDING_AUTH_SESSION_EXPIRED", "pending auth session has expired")
	ErrPendingAuthSessionConsumed = infraerrors.Unauthorized("PENDING_AUTH_SESSION_CONSUMED", "pending auth session has already been used")
	ErrPendingAuthCodeInvalid     = infraerrors.Unauthorized("PENDING_AUTH_CODE_INVALID", "pending auth completion code is invalid")
	ErrPendingAuthCodeExpired     = infraerrors.Unauthorized("PENDING_AUTH_CODE_EXPIRED", "pending auth completion code has expired")
	ErrPendingAuthCodeConsumed    = infraerrors.Unauthorized("PENDING_AUTH_CODE_CONSUMED", "pending auth completion code has already been used")
	ErrPendingAuthBrowserMismatch = infraerrors.Unauthorized("PENDING_AUTH_BROWSER_MISMATCH", "pending auth completion code does not match this browser session")
)

// PendingAuthIdentityKey identifies one provider subject within its
// provider-specific namespace.
type PendingAuthIdentityKey struct {
	ProviderType    string // authentication provider type label
	ProviderKey     string // provider identity namespace key
	ProviderSubject string // opaque upstream subject identifier
}

// CreatePendingAuthSessionInput supplies identity, browser and decision
// values for a new pending record.
type CreatePendingAuthSessionInput struct {
	// SessionToken is the browser lookup credential. PostgreSQL creation trims
	// it and generates twenty-four random bytes as hexadecimal text when it is
	// blank.
	SessionToken string
	Intent       string                 // selected pending authentication action
	Identity     PendingAuthIdentityKey // selected provider identity tuple
	// TargetUserID selects an existing local account. Nil leaves target
	// resolution to later pending steps. The caller keeps this pointer stable
	// until its selected store reads the target value.
	TargetUserID             *int64
	RedirectTo               string // selected frontend return destination
	ResolvedEmail            string // selected account email hint
	RegistrationPasswordHash string // optional registration password digest
	// BrowserSessionKey binds later exchange checks to a browser. A blank
	// trimmed stored key disables that comparison in the core validator.
	BrowserSessionKey string
	// UpstreamIdentityClaims and LocalFlowState are caller-supplied maps.
	// PostgreSQL creation copies their outer containers, while nested mutable
	// values remain shared until serialization or an additional copy.
	UpstreamIdentityClaims map[string]any // selected upstream identity observations
	// LocalFlowState supplies local completion choices and promotion data.
	// Initial PostgreSQL creation copies its outer map without stripping
	// completion credentials from it.
	LocalFlowState map[string]any
	// ExpiresAt selects the pending lifetime endpoint. PostgreSQL creation
	// stores UTC and substitutes its default lifetime when the supplied time is
	// zero.
	ExpiresAt time.Time
}

// IssuePendingAuthCompletionCodeInput selects a pending record and its
// replacement completion credential lifetime.
type IssuePendingAuthCompletionCodeInput struct {
	PendingAuthSessionID int64 // pending session record identifier
	// BrowserSessionKey replaces the stored binding when its trimmed value is
	// nonblank. Empty input retains the selected session's previous browser key.
	BrowserSessionKey string
	// TTL governs the separately issued completion credential. Nonpositive
	// values select DefaultPendingAuthCompletionTTL, independently of the
	// pending session's existing expiry.
	TTL time.Duration
}

// IssuePendingAuthCompletionCodeResult returns the raw credential and its
// selected expiry observation.
type IssuePendingAuthCompletionCodeResult struct {
	// Code is returned to its browser flow, while ExpiresAt describes the
	// corresponding saved hash lifetime. PostgreSQL stores the digest rather
	// than this raw code.
	Code      string    // raw hexadecimal completion credential
	ExpiresAt time.Time // completion credential expiry timestamp
}

// PendingIdentityAdoptionDecisionInput supplies profile choices associated
// with one pending session.
type PendingIdentityAdoptionDecisionInput struct {
	PendingAuthSessionID int64 // pending session record identifier
	// IdentityID links the decision to a bound identity when positive. The
	// PostgreSQL upsert clears competing session links before assigning that
	// identity association.
	IdentityID *int64
	// AdoptDisplayName and AdoptAvatar are concrete saved choices, including
	// false. Partial-choice merging belongs to PendingFlow.AdoptionDecision
	// before it passes this complete input to storage.
	AdoptDisplayName bool // selected display-name adoption choice
	AdoptAvatar      bool // selected profile-avatar adoption choice
}

// PendingAuthSession projects stored identity, browser, credential and
// decision observations for an unfinished flow. Projection retains scalar
// pointers and map storage, so callers coordinate any mutation of those
// shared values.
type PendingAuthSession struct {
	ID           int64     `json:"id,omitempty"`            // pending session record identifier
	CreatedAt    time.Time `json:"created_at,omitempty"`    // pending record creation timestamp
	UpdatedAt    time.Time `json:"updated_at,omitempty"`    // pending record modification timestamp
	SessionToken string    `json:"session_token,omitempty"` // opaque browser lookup credential
	Intent       string    `json:"intent,omitempty"`        // selected pending authentication action
	// ProviderType, ProviderKey and ProviderSubject address the same upstream
	// identity. Store lookups trim their text; local account selection remains
	// separate in TargetUserID and the pending intent.
	ProviderType    string `json:"provider_type,omitempty"`    // authentication provider type label
	ProviderKey     string `json:"provider_key,omitempty"`     // provider identity namespace key
	ProviderSubject string `json:"provider_subject,omitempty"` // opaque upstream subject identifier
	// TargetUserID identifies the selected local account, or remains nil before
	// resolution. Binding can supply an explicit override rather than using this
	// pointer.
	TargetUserID             *int64 `json:"target_user_id,omitempty"`
	RedirectTo               string `json:"redirect_to,omitempty"`                // selected frontend return destination
	ResolvedEmail            string `json:"resolved_email,omitempty"`             // selected account email hint
	RegistrationPasswordHash string `json:"registration_password_hash,omitempty"` // stored registration password digest
	// UpstreamIdentityClaims and LocalFlowState retain map references through
	// PostgreSQL projection. Outer-map preparation does not clone nested mutable
	// values, so callers keep their shared values stable during consumption or
	// serialization.
	UpstreamIdentityClaims map[string]any `json:"upstream_identity_claims,omitempty"` // stored upstream identity observations
	// LocalFlowState retains stored completion choices and promotion data.
	// Creation can preserve credentials here. Ordinary PendingStore consumption
	// replaces this map with sanitized state; the PostgreSQL binding/adoption
	// consumer leaves the stored map unchanged while marking consumption.
	LocalFlowState map[string]any `json:"local_flow_state,omitempty"`
	// BrowserSessionKey participates in the validator only when its trimmed
	// value is nonblank. Other values require the supplied browser key to match
	// after trimming.
	BrowserSessionKey string `json:"browser_session_key,omitempty"`
	// CompletionCodeHash addresses the separately issued credential by its
	// SHA-256 hexadecimal digest. Successful storage consumption clears it,
	// while issuance replaces it before a later lookup.
	CompletionCodeHash string `json:"completion_code_hash,omitempty"`
	// CompletionCodeExpiresAt is optional; a present pointer participates in
	// expiry validation even when its time is zero. Successful consumption
	// clears this endpoint with the saved hash.
	CompletionCodeExpiresAt *time.Time `json:"completion_code_expires_at,omitempty"`
	// EmailVerifiedAt, PasswordVerifiedAt and TotpVerifiedAt retain optional
	// stored proof observations. Projections preserve their pointers, while
	// ValidatePendingSessionStateWithClock uses consumption, lifetimes and
	// browser binding rather than these proof timestamps.
	EmailVerifiedAt    *time.Time `json:"email_verified_at,omitempty"`    // stored email-proof observation timestamp
	PasswordVerifiedAt *time.Time `json:"password_verified_at,omitempty"` // stored password-proof observation timestamp
	TotpVerifiedAt     *time.Time `json:"totp_verified_at,omitempty"`     // stored TOTP-proof observation timestamp
	// ExpiresAt bounds the pending session. The core validator ignores a zero
	// time, while the PostgreSQL conditional consumption write still compares
	// this field with its clock.
	ExpiresAt time.Time `json:"expires_at,omitempty"`
	// ConsumedAt marks consumption whenever its pointer is present, including a
	// zero-valued timestamp. The core rejects that state before checking either
	// lifetime or browser key.
	ConsumedAt *time.Time `json:"consumed_at,omitempty"`
}

// IdentityAdoptionDecision stores a pending flow's profile choices and
// optional bound-identity association.
type IdentityAdoptionDecision struct {
	ID                   int64     `json:"id,omitempty"`                      // adoption decision record identifier
	CreatedAt            time.Time `json:"created_at,omitempty"`              // decision record creation timestamp
	UpdatedAt            time.Time `json:"updated_at,omitempty"`              // decision record modification timestamp
	PendingAuthSessionID int64     `json:"pending_auth_session_id,omitempty"` // owning pending session identifier
	// IdentityID associates the choice with a bound provider identity when
	// present. Storage can clear a prior association when another pending
	// session claims it.
	IdentityID *int64 `json:"identity_id,omitempty"`
	// AdoptDisplayName and AdoptAvatar remain independent saved choices.
	// PendingFlow merges omitted caller options with an existing decision before
	// updating these concrete booleans in storage.
	AdoptDisplayName bool      `json:"adopt_display_name,omitempty"` // saved display-name adoption choice
	AdoptAvatar      bool      `json:"adopt_avatar,omitempty"`       // saved profile-avatar adoption choice
	DecidedAt        time.Time `json:"decided_at,omitempty"`         // saved decision observation timestamp
}

// PendingStore creates pending records, manages completion credentials and
// conditionally consumes stored sessions.
type PendingStore interface {
	// CreatePendingSession persists the supplied identity, browser and
	// local-decision state. The PostgreSQL adapter generates a token for blank
	// input, applies the default lifetime for zero expiry and copies outer maps
	// without stripping initial completion credentials.
	CreatePendingSession(ctx context.Context, input CreatePendingAuthSessionInput) (*PendingAuthSession, error)
	// IssueCompletionCode replaces the selected session's saved completion hash
	// and expiry and returns its raw code. Nonpositive TTL selects the
	// completion default; the PostgreSQL adapter does not first reject an
	// already consumed or expired row.
	IssueCompletionCode(ctx context.Context, input IssuePendingAuthCompletionCodeInput) (*IssuePendingAuthCompletionCodeResult, error)
	// ConsumeCompletionCode looks up a trimmed code digest and conditionally
	// consumes its pending session. PostgreSQL rechecks consumption, lifetime
	// and browser state in the write; the earlier hash lookup is separate and is
	// not rechecked against concurrent reissuance.
	ConsumeCompletionCode(ctx context.Context, rawCode, browserSessionKey string) (*PendingAuthSession, error)
	// ConsumeBrowserSession loads the trimmed session token and conditionally
	// marks that session consumed. The PostgreSQL write rechecks current
	// lifetime and browser state, sanitizes local completion fields and clears
	// completion credentials; failures remain errors for the caller.
	ConsumeBrowserSession(ctx context.Context, sessionToken, browserSessionKey string) (*PendingAuthSession, error)
	// GetBrowserSession loads and validates a browser-bound pending snapshot
	// without consuming it. Missing, consumed, expired or mismatched states
	// remain errors; a successful read does not reserve the row against later
	// issuance or consumption.
	GetBrowserSession(ctx context.Context, sessionToken, browserSessionKey string) (*PendingAuthSession, error)
	// UpsertAdoptionDecision saves one pending session's profile choices and
	// optional identity association. The PostgreSQL adapter coordinates
	// competing identity links, replaces supplied values and timestamps the
	// decision; callers still handle persistence errors and adapter-owned
	// transaction completion.
	UpsertAdoptionDecision(ctx context.Context, input PendingIdentityAdoptionDecisionInput) (*IdentityAdoptionDecision, error)
}

// SanitizePendingAuthLocalFlowState removes immediate token fields from a
// typed completion_response map while preserving other pending choices. It
// copies the outer map and that recognized inner map; nil or empty input
// becomes an allocated empty map.
//
// A missing completion entry leaves the outer copy unchanged. Values of
// another Go type also remain unchanged without inner cleanup. Only
// access_token, refresh_token, expires_in and token_type are removed; nested
// mutable values remain shared. Ordinary PendingStore consumption uses this
// preparation; initial creation and transactional binding consumption retain
// their separately supplied local flow state.
func SanitizePendingAuthLocalFlowState(localFlowState map[string]any) map[string]any {
	sanitized := CopyPendingMap(localFlowState)
	if len(sanitized) == 0 {
		return sanitized
	}

	rawCompletion, ok := sanitized["completion_response"]
	if !ok {
		return sanitized
	}
	// Only the recognized map shape supports these named credential fields;
	// preserve other representations instead of applying an unverified recursive
	// transformation to caller data.
	completion, ok := rawCompletion.(map[string]any)
	if !ok {
		return sanitized
	}

	// Copy the recognized completion container before deleting immediate
	// credentials, so the supplied completion map keeps its entries while
	// unrelated nested values remain shared.
	cleanedCompletion := CopyPendingMap(completion)
	for _, key := range []string{"access_token", "refresh_token", "expires_in", "token_type"} {
		delete(cleanedCompletion, key)
	}
	sanitized["completion_response"] = cleanedCompletion
	return sanitized
}

// ValidatePendingSessionStateWithClock returns the first invalid
// pending-state result using a supplied clock and caller-selected
// expiry/consumption errors. Nil sessions return not-found before reading
// time; nonnil input requires a callable clock and nonnil rejection errors
// when those states must fail.
//
// Consumption presence wins before session or completion expiry, and browser
// comparison follows both. Zero session expiry disables that core check;
// completion expiry is checked whenever its pointer exists, with equality
// still accepted. A blank trimmed stored browser key disables matching, while
// other keys compare trimmed text. Storage consumption adds its own
// current-row conditions after this snapshot validation.
func ValidatePendingSessionStateWithClock(session *PendingAuthSession, browserSessionKey string, expiredErr error, consumedErr error, readTime func() time.Time) error {
	if session == nil {
		return ErrPendingAuthSessionNotFound
	}

	now := readTime().UTC()
	// Reject recorded consumption before the two lifetime checks and browser
	// comparison, keeping the caller's consumed-state response authoritative
	// when several snapshot conditions already fail.
	if session.ConsumedAt != nil {
		return consumedErr
	}
	if !session.ExpiresAt.IsZero() && now.After(session.ExpiresAt) {
		return expiredErr
	}
	if session.CompletionCodeExpiresAt != nil && now.After(*session.CompletionCodeExpiresAt) {
		return expiredErr
	}
	if strings.TrimSpace(session.BrowserSessionKey) != "" && strings.TrimSpace(browserSessionKey) != strings.TrimSpace(session.BrowserSessionKey) {
		return ErrPendingAuthBrowserMismatch
	}
	return nil
}

// CopyPendingMap allocates an outer map with the same entries. Nil and empty
// input both produce an allocated empty result; nested maps, slices and
// pointers stay shared, so callers copy those values before independent
// mutation.
func CopyPendingMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(in))
	maps.Copy(out, in)
	return out
}

// RandomOpaqueToken returns hexadecimal text generated from random bytes.
// Nonpositive byteLen selects sixteen bytes; each byte contributes two
// characters, and random-read errors remain errors before any token text is
// returned.
func RandomOpaqueToken(byteLen int) (string, error) {
	if byteLen <= 0 {
		byteLen = 16
	}
	buf := make([]byte, byteLen)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// HashPendingAuthCode returns the hexadecimal SHA-256 digest of the exact
// supplied code bytes.
func HashPendingAuthCode(code string) string {
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}
