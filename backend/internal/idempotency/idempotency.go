package idempotency

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
)

// idempotency.go coordinates command claims, fingerprint conflicts, response
// replay, and retryable outcomes.
// A command key identifies one record within its business scope, while the
// request fingerprint checks whether a retry still names the same actor, route,
// method, and JSON payload. A retained record with another fingerprint rejects
// the request even after its retention deadline passes. Matching retries can
// replay success, observe processing, wait through failure backoff, or reclaim an
// eligible expired record using the repository's conditional status and lock check.
//
// Business execution and outcome storage are separate steps. A callback can
// finish successfully and then encounter response encoding or persistence failure.
// That path returns store unavailability rather than a replayable result. Stored
// responses are redacted and may be truncated at a valid UTF-8 prefix, which can
// make their JSON unusable for later replay. Callers choose payloads, retention,
// and callback behavior with these states in mind rather than treating a claim
// as a funding transaction.

// Command states select whether a duplicate can replay, must wait, or may reclaim
// its existing record after the relevant retention or retry-lock window expires.
const (
	IdempotencyStatusProcessing      = "processing"       // execution outcome still pending
	IdempotencyStatusSucceeded       = "succeeded"        // execution succeeded; response stored
	IdempotencyStatusFailedRetryable = "failed_retryable" // failed execution awaiting retry eligibility
)

// Idempotency errors distinguish invalid command identity, conflicting records,
// retry delays, and unavailable persistence for the shared HTTP response mapping.
var (
	ErrIdempotencyKeyRequired    = infraerrors.BadRequest("IDEMPOTENCY_KEY_REQUIRED", "idempotency key is required")
	ErrIdempotencyKeyInvalid     = infraerrors.BadRequest("IDEMPOTENCY_KEY_INVALID", "idempotency key is invalid")
	ErrIdempotencyKeyConflict    = infraerrors.Conflict("IDEMPOTENCY_KEY_CONFLICT", "idempotency key reused with different payload")
	ErrIdempotencyInProgress     = infraerrors.Conflict("IDEMPOTENCY_IN_PROGRESS", "idempotent request is still processing")
	ErrIdempotencyRetryBackoff   = infraerrors.Conflict("IDEMPOTENCY_RETRY_BACKOFF", "idempotent request is in retry backoff window")
	ErrIdempotencyStoreUnavail   = infraerrors.ServiceUnavailable("IDEMPOTENCY_STORE_UNAVAILABLE", "idempotency store unavailable")
	ErrIdempotencyInvalidPayload = infraerrors.BadRequest("IDEMPOTENCY_PAYLOAD_INVALID", "failed to normalize request payload")
)

// IdempotencyRecord stores command identity, lease timing, and the retained execution outcome.
type IdempotencyRecord struct {
	ID int64 // database record identifier
	// Scope and IdempotencyKeyHash select the unique stored row. RequestFingerprint
	// checks command identity before replay decisions; OperationLeaseStore uses that
	// same field for a maintenance operation ID when comparing acquisition ownership.
	Scope              string // command business scope
	IdempotencyKeyHash string // SHA-256 hexadecimal key digest
	RequestFingerprint string // request digest or maintenance operation
	Status             string // stored execution state
	ResponseStatus     *int   // recorded HTTP response status
	// ResponseBody holds redacted response text for ordinary command replay. Maintenance
	// claims use it for ownership JSON while processing; truncated command text may
	// remain valid UTF-8 while failing the JSON decoder used for replay.
	ResponseBody *string
	ErrorReason  *string // recorded retryable failure reason
	// LockedUntil and ExpiresAt have different roles: the former restricts reclaim
	// and supplies retry-delay metadata, while the latter selects expiry handling and cleanup.
	LockedUntil *time.Time // processing or retry-backoff deadline
	ExpiresAt   time.Time  // stored record retention deadline
	CreatedAt   time.Time  // database record creation time
	UpdatedAt   time.Time  // latest database mutation time
}

// IdempotencyRepository stores command claims and their conditional retry state transitions.
type IdempotencyRepository interface {
	// CreateProcessing inserts a new scope/key record and reports initial ownership.
	// A successful claim fills the supplied record's ID; duplicate keys return false
	// so the coordinator can inspect the existing fingerprint and state.
	CreateProcessing(ctx context.Context, record *IdempotencyRecord) (bool, error)
	// GetByScopeAndKeyHash returns the stored record for the supplied command key.
	// Missing records return nil without an error; read errors prevent the coordinator
	// from deciding whether a duplicate can replay or execute.
	GetByScopeAndKeyHash(ctx context.Context, scope, keyHash string) (*IdempotencyRecord, error)
	// TryReclaim changes the observed fromStatus to processing when its lock is
	// absent or expired at now, returning whether the row changed. It replaces
	// lock and retention deadlines and clears the failure reason.
	//
	// The expected status and lock comparison share the update so a competing
	// request can win between the coordinator's read and this attempt. False sends
	// the coordinator back to conflict or refreshed-record handling; record expiration
	// is checked by the coordinator before its expired-record path invokes this method.
	TryReclaim(ctx context.Context, id int64, fromStatus string, now, newLockedUntil, newExpiresAt time.Time) (bool, error)
	// ExtendProcessingLock updates deadlines when ID, processing state, and request
	// fingerprint match, returning whether the record changed. Deadline expiry itself
	// does not reject the update.
	//
	// A fingerprint represents the request, so another acquisition of that same
	// request can retain the same value. Callers needing acquisition-specific fencing
	// use OperationLeaseStore and its ownership token; this method distinguishes
	// request identities and completed states rather than separate owners of one identity.
	ExtendProcessingLock(ctx context.Context, id int64, requestFingerprint string, newLockedUntil, newExpiresAt time.Time) (bool, error)
	// MarkSucceeded stores response text and status by record ID, clearing its lock
	// and failure reason. The coordinator calls it after callback success and response
	// encoding. Errors leave successful persistence unconfirmed for the current caller,
	// even when the database may have applied the update before reporting an error.
	MarkSucceeded(ctx context.Context, id int64, responseStatus int, responseBody string, expiresAt time.Time) error
	// MarkFailedRetryable stores the callback error reason and next retry deadline
	// by record ID. The coordinator returns the callback's original error even if
	// recording this retry state also fails.
	MarkFailedRetryable(ctx context.Context, id int64, errorReason string, lockedUntil, expiresAt time.Time) error
	// DeleteExpired deletes up to limit records expired at now and reports their count.
	// Repository adapters define their nonpositive-limit default; cleanup uses this
	// method once per scheduled batch and handles storage errors through observation.
	DeleteExpired(ctx context.Context, now time.Time, limit int) (int64, error)
}

// IdempotencyConfig supplies timing and response-storage policy for one coordinator instance.
type IdempotencyConfig struct {
	// DefaultTTL is used when Execute receives a nonpositive per-command TTL.
	// Construction retains this value as supplied; app prepares positive runtime defaults.
	DefaultTTL time.Duration
	// SystemOperationTTL is exposed to maintenance handlers by the TTL accessor.
	// That accessor chooses the default hour when this configured value is nonpositive.
	SystemOperationTTL time.Duration
	// ProcessingTimeout and FailedRetryBackoff are added directly to observed times
	// when creating their lock deadlines. They describe stored waiting windows; the
	// caller's context controls cancellation of callback work independently.
	ProcessingTimeout  time.Duration // initial processing lock duration
	FailedRetryBackoff time.Duration // callback-failure retry delay
	// MaxStoredResponseLen bounds redacted response bytes before the marker is appended.
	// Nonpositive values leave response size unrestricted, and truncation may prevent JSON replay.
	MaxStoredResponseLen int
	// ObserveOnly permits a missing key even when RequireKey requests one. Present
	// keys still use normal persistence, fingerprint conflict, and replay behavior.
	ObserveOnly bool
}

// IdempotencyExecuteOptions identifies a command and selects its per-call retention policy.
type IdempotencyExecuteOptions struct {
	// Scope selects the record namespace and is required for keyed execution.
	// Missing-key pass-through returns before scope validation because it stores no record.
	Scope          string
	ActorScope     string // authenticated actor fingerprint label
	Method         string // HTTP method fingerprint input
	Route          string // route template fingerprint input
	IdempotencyKey string // caller-supplied command retry key
	// Payload is JSON-encoded to build request identity. Encoding failures reject
	// keyed execution before claiming a row, so callers provide serializable command data.
	Payload any
	// TTL overrides record retention when positive. A nonpositive duration uses
	// the coordinator's configured DefaultTTL directly, without normalizing that configuration here.
	TTL time.Duration
	// RequireKey rejects missing keys when ObserveOnly is false. When keys are
	// optional or observe-only is enabled, missing keys execute directly without recording.
	RequireKey bool
}

// IdempotencyExecuteResult carries live callback data or a decoded stored replay.
type IdempotencyExecuteResult struct {
	// Data is the original callback value after execution, or decoded redacted JSON
	// after replay. Replay therefore has encoding/json's value types rather than the callback's concrete type.
	Data     any
	Replayed bool // response came from storage
}

// IdempotencyCoordinator arbitrates command retries through persisted fingerprints and state transitions.
type IdempotencyCoordinator struct {
	// observer receives prepared events inline with execution. It can receive
	// concurrent calls from separate requests, so its implementation handles shared output accordingly.
	observer Observer
	// repo serves keyed executions and can receive concurrent request calls. A nil
	// repository returns store unavailability for keyed commands, while keyless calls execute directly.
	repo IdempotencyRepository
	cfg  IdempotencyConfig // copied coordinator policy values
}

// DefaultIdempotencyConfig returns command timing, response-size, and observe-only defaults.
// Missing keys are accepted by its observe-only policy; callers can disable that
// policy when commands need to reject clients that omit their retry key.
func DefaultIdempotencyConfig() IdempotencyConfig {
	return IdempotencyConfig{
		DefaultTTL:           24 * time.Hour,
		SystemOperationTTL:   1 * time.Hour,
		ProcessingTimeout:    30 * time.Second,
		FailedRetryBackoff:   5 * time.Second,
		MaxStoredResponseLen: 64 * 1024,
		ObserveOnly:          true, // Accept missing keys by default for clients that do not yet send them.
	}
}

// NewIdempotencyCoordinator retains the repository, copied configuration, and
// first optional observer for command execution. It does not normalize configuration;
// app starts from DefaultIdempotencyConfig before applying positive runtime overrides.
// Observers are invoked synchronously and may receive concurrent request events.
func NewIdempotencyCoordinator(repo IdempotencyRepository, cfg IdempotencyConfig, observers ...Observer) *IdempotencyCoordinator {
	var observer Observer
	if len(observers) > 0 {
		observer = observers[0]
	}
	return &IdempotencyCoordinator{
		observer: observer,
		repo:     repo,
		cfg:      cfg,
	}
}

// DefaultWriteIdempotencyTTL returns this instance's positive configured write
// retention period. Nil receivers and nonpositive values choose DefaultIdempotencyConfig's
// period, so HTTP helpers can select a usable TTL even in unbound fixtures.
func (c *IdempotencyCoordinator) DefaultWriteIdempotencyTTL() time.Duration {
	if c != nil && c.cfg.DefaultTTL > 0 {
		return c.cfg.DefaultTTL
	}
	return DefaultIdempotencyConfig().DefaultTTL
}

// DefaultSystemOperationIdempotencyTTL returns this instance's positive maintenance
// retention period. Nil receivers and nonpositive configuration choose the default
// maintenance period of one hour exposed to system-operation HTTP helpers.
func (c *IdempotencyCoordinator) DefaultSystemOperationIdempotencyTTL() time.Duration {
	if c != nil && c.cfg.SystemOperationTTL > 0 {
		return c.cfg.SystemOperationTTL
	}
	return DefaultIdempotencyConfig().SystemOperationTTL
}

// NormalizeIdempotencyKey trims a supplied retry key and validates its printable
// ASCII bytes and its 128-byte maximum. Empty text returns an empty key without error;
// callers apply required-key policy separately after normalization.
func NormalizeIdempotencyKey(raw string) (string, error) {
	key := strings.TrimSpace(raw)
	if key == "" {
		return "", nil
	}
	if len(key) > 128 {
		// Bound the normalized caller key before hashing it for command storage.
		return "", ErrIdempotencyKeyInvalid
	}
	for _, r := range key {
		// Limit nonempty keys to printable ASCII without internal spaces or control
		// characters, keeping one header value usable as a deterministic retry identifier.
		if r < 33 || r > 126 {
			return "", ErrIdempotencyKeyInvalid
		}
	}
	return key, nil
}

// HashIdempotencyKey returns the hexadecimal SHA-256 digest of the supplied key.
func HashIdempotencyKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// BuildIdempotencyFingerprint hashes method, route, actor label, and JSON payload
// to detect changed request identity. Empty method, route, and actor choose POST,
// slash, and anonymous; the method is uppercased before hashing, while unsupported
// payload values return ErrIdempotencyInvalidPayload with their encoding error.
func BuildIdempotencyFingerprint(method, route, actorScope string, payload any) (string, error) {
	if method == "" {
		method = "POST"
	}
	if route == "" {
		route = "/"
	}
	if actorScope == "" {
		actorScope = "anonymous"
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		return "", ErrIdempotencyInvalidPayload.WithCause(err)
	}
	sum := sha256.Sum256([]byte(
		strings.ToUpper(method) + "\n" + route + "\n" + actorScope + "\n" + string(raw),
	))
	return hex.EncodeToString(sum[:]), nil
}

// RetryAfterSecondsFromError extracts positive decimal retry_after metadata from
// an application error. Missing, malformed, nonpositive, and unrelated errors return
// zero, telling HTTP helpers to omit the optional Retry-After header.
func RetryAfterSecondsFromError(err error) int {
	appErr := new(infraerrors.ApplicationError)
	if !errors.As(err, &appErr) || appErr == nil || appErr.Metadata == nil {
		return 0
	}
	v := strings.TrimSpace(appErr.Metadata["retry_after"])
	if v == "" {
		return 0
	}
	seconds, convErr := strconv.Atoi(v)
	if convErr != nil || seconds <= 0 {
		// Omit unusable retry metadata rather than sending an invalid HTTP delay,
		// while retaining the underlying application error for the response's normal status mapping.
		return 0
	}
	return seconds
}

// Execute returns callback data after a keyed claim or a decoded successful
// replay, using the supplied context for repository calls and business execution.
// Callers supply a nonnil callback and coordinator, a serializable payload, and a
// business scope for keyed requests. Missing keys execute directly unless both
// RequireKey and enforced key policy reject them.
//
// Existing fingerprints are compared before expiry handling, so retained records
// reject changed requests even when expired. Matching expired records attempt
// conditional reclaim; unexpired processing records and active failure backoff return
// conflicts with available retry-delay metadata. A lost expired reclaim reloads
// the latest record before deciding whether to replay or report conflict.
//
// Callback execution is separate from recording its outcome. Callback failures
// return their original error while retry-state persistence is attempted; successful
// callbacks can return store unavailability if response encoding or success
// persistence fails. Stored replay decodes the redacted response and can fail
// after truncation. Callers account for these outcomes in their own business side effects.
//
// @project-doc docs/interfaces/http_api.md#write_idempotency
func (c *IdempotencyCoordinator) Execute(
	ctx context.Context,
	opts IdempotencyExecuteOptions,
	execute func(context.Context) (any, error),
) (*IdempotencyExecuteResult, error) {
	if execute == nil {
		return nil, infraerrors.InternalServer("IDEMPOTENCY_EXECUTOR_NIL", "idempotency executor is nil")
	}

	key, err := NormalizeIdempotencyKey(opts.IdempotencyKey)
	if err != nil {
		return nil, err
	}
	if key == "" {
		if opts.RequireKey && !c.cfg.ObserveOnly {
			return nil, ErrIdempotencyKeyRequired
		}
		// Keyless requests admitted by optional or observe-only policy have no stored
		// retry identity; invoke the callback directly before repository and scope validation.
		data, execErr := execute(ctx)
		if execErr != nil {
			return nil, execErr
		}
		return &IdempotencyExecuteResult{Data: data}, nil
	}
	if c.repo == nil {
		c.RecordIdempotencyStoreUnavailable(opts.Route, opts.Scope, "repo_nil")
		return nil, ErrIdempotencyStoreUnavail
	}

	if opts.Scope == "" {
		return nil, infraerrors.BadRequest("IDEMPOTENCY_SCOPE_REQUIRED", "idempotency scope is required")
	}

	fingerprint, err := BuildIdempotencyFingerprint(opts.Method, opts.Route, opts.ActorScope, opts.Payload)
	if err != nil {
		return nil, err
	}

	ttl := opts.TTL
	if ttl <= 0 {
		ttl = c.cfg.DefaultTTL
	}
	now := time.Now()
	expiresAt := now.Add(ttl)
	lockedUntil := now.Add(c.cfg.ProcessingTimeout)
	keyHash := HashIdempotencyKey(key)

	record := &IdempotencyRecord{
		Scope:              opts.Scope,
		IdempotencyKeyHash: keyHash,
		RequestFingerprint: fingerprint,
		Status:             IdempotencyStatusProcessing,
		LockedUntil:        &lockedUntil,
		ExpiresAt:          expiresAt,
	}

	// Persist initial ownership before invoking business work. A duplicate insertion
	// leaves the existing fingerprint and status available for the retry decision below.
	owner, err := c.repo.CreateProcessing(ctx, record)
	if err != nil {
		c.RecordIdempotencyStoreUnavailable(opts.Route, opts.Scope, "create_processing_error")
		c.logIdempotencyAudit(opts.Route, opts.Scope, keyHash, "unknown->store_unavailable", false, map[string]string{
			"operation": "create_processing",
		})
		return nil, ErrIdempotencyStoreUnavail.WithCause(err)
	}
	if owner {
		c.recordIdempotencyClaim(opts.Route, opts.Scope, map[string]string{"mode": "new_claim"})
		c.logIdempotencyAudit(opts.Route, opts.Scope, keyHash, "none->processing", false, map[string]string{
			"claim_mode": "new",
		})
	}
	if !owner {
		existing, getErr := c.repo.GetByScopeAndKeyHash(ctx, opts.Scope, keyHash)
		if getErr != nil {
			c.RecordIdempotencyStoreUnavailable(opts.Route, opts.Scope, "get_existing_error")
			c.logIdempotencyAudit(opts.Route, opts.Scope, keyHash, "unknown->store_unavailable", false, map[string]string{
				"operation": "get_existing",
			})
			return nil, ErrIdempotencyStoreUnavail.WithCause(getErr)
		}
		if existing == nil {
			c.RecordIdempotencyStoreUnavailable(opts.Route, opts.Scope, "missing_existing")
			c.logIdempotencyAudit(opts.Route, opts.Scope, keyHash, "unknown->store_unavailable", false, map[string]string{
				"operation": "missing_existing",
			})
			return nil, ErrIdempotencyStoreUnavail
		}
		if existing.RequestFingerprint != fingerprint {
			c.recordIdempotencyConflict(opts.Route, opts.Scope, map[string]string{"reason": "fingerprint_mismatch"})
			c.logIdempotencyAudit(opts.Route, opts.Scope, keyHash, "existing->fingerprint_mismatch", false, nil)
			return nil, ErrIdempotencyKeyConflict
		}
		reclaimedByExpired := false
		if !existing.ExpiresAt.After(now) {
			// Expired matching records may execute again, but reclaim also checks their
			// observed status and lock deadline so expiry alone cannot replace an active lock.
			taken, reclaimErr := c.repo.TryReclaim(ctx, existing.ID, existing.Status, now, lockedUntil, expiresAt)
			if reclaimErr != nil {
				c.RecordIdempotencyStoreUnavailable(opts.Route, opts.Scope, "try_reclaim_expired_error")
				c.logIdempotencyAudit(opts.Route, opts.Scope, keyHash, existing.Status+"->store_unavailable", false, map[string]string{
					"operation": "try_reclaim_expired",
				})
				return nil, ErrIdempotencyStoreUnavail.WithCause(reclaimErr)
			}
			if taken {
				reclaimedByExpired = true
				c.recordIdempotencyClaim(opts.Route, opts.Scope, map[string]string{"mode": "expired_reclaim"})
				c.logIdempotencyAudit(opts.Route, opts.Scope, keyHash, existing.Status+"->processing", false, map[string]string{
					"claim_mode": "expired_reclaim",
				})
				record.ID = existing.ID
			} else {
				// Another caller can change the record between our read and failed reclaim.
				// Reload its current fingerprint and state before deciding whether this retry can replay.
				latest, latestErr := c.repo.GetByScopeAndKeyHash(ctx, opts.Scope, keyHash)
				if latestErr != nil {
					c.RecordIdempotencyStoreUnavailable(opts.Route, opts.Scope, "get_existing_after_expired_reclaim_error")
					c.logIdempotencyAudit(opts.Route, opts.Scope, keyHash, "unknown->store_unavailable", false, map[string]string{
						"operation": "get_existing_after_expired_reclaim",
					})
					return nil, ErrIdempotencyStoreUnavail.WithCause(latestErr)
				}
				if latest == nil {
					c.RecordIdempotencyStoreUnavailable(opts.Route, opts.Scope, "missing_existing_after_expired_reclaim")
					c.logIdempotencyAudit(opts.Route, opts.Scope, keyHash, "unknown->store_unavailable", false, map[string]string{
						"operation": "missing_existing_after_expired_reclaim",
					})
					return nil, ErrIdempotencyStoreUnavail
				}
				if latest.RequestFingerprint != fingerprint {
					c.recordIdempotencyConflict(opts.Route, opts.Scope, map[string]string{"reason": "fingerprint_mismatch"})
					c.logIdempotencyAudit(opts.Route, opts.Scope, keyHash, "existing->fingerprint_mismatch", false, nil)
					return nil, ErrIdempotencyKeyConflict
				}
				existing = latest
			}
		}

		if !reclaimedByExpired {
			switch existing.Status {
			case IdempotencyStatusSucceeded:
				data, parseErr := c.decodeStoredResponse(existing.ResponseBody)
				if parseErr != nil {
					c.RecordIdempotencyStoreUnavailable(opts.Route, opts.Scope, "decode_stored_response_error")
					c.logIdempotencyAudit(opts.Route, opts.Scope, keyHash, "succeeded->store_unavailable", false, map[string]string{
						"operation": "decode_stored_response",
					})
					return nil, ErrIdempotencyStoreUnavail.WithCause(parseErr)
				}
				c.recordIdempotencyReplay(opts.Route, opts.Scope, nil)
				c.logIdempotencyAudit(opts.Route, opts.Scope, keyHash, "succeeded->replayed", true, nil)
				return &IdempotencyExecuteResult{Data: data, Replayed: true}, nil
			case IdempotencyStatusProcessing:
				c.recordIdempotencyConflict(opts.Route, opts.Scope, map[string]string{"reason": "in_progress"})
				c.logIdempotencyAudit(opts.Route, opts.Scope, keyHash, "processing->conflict", false, nil)
				return nil, c.conflictWithRetryAfter(ErrIdempotencyInProgress, existing.LockedUntil, now)
			case IdempotencyStatusFailedRetryable:
				if existing.LockedUntil != nil && existing.LockedUntil.After(now) {
					// Keep failed callbacks inside their recorded backoff window so repeated
					// client retries do not immediately invoke the same unsuccessful business operation again.
					c.recordIdempotencyConflict(opts.Route, opts.Scope, map[string]string{"reason": "retry_backoff"})
					c.recordIdempotencyRetryBackoff(opts.Route, opts.Scope, nil)
					c.logIdempotencyAudit(opts.Route, opts.Scope, keyHash, "failed_retryable->retry_backoff_conflict", false, nil)
					return nil, c.conflictWithRetryAfter(ErrIdempotencyRetryBackoff, existing.LockedUntil, now)
				}
				// The backoff read can race with another retry's claim. Compare the failed
				// status and unlocked deadline in storage before admitting this callback for another attempt.
				taken, reclaimErr := c.repo.TryReclaim(ctx, existing.ID, IdempotencyStatusFailedRetryable, now, lockedUntil, expiresAt)
				if reclaimErr != nil {
					c.RecordIdempotencyStoreUnavailable(opts.Route, opts.Scope, "try_reclaim_error")
					c.logIdempotencyAudit(opts.Route, opts.Scope, keyHash, "failed_retryable->store_unavailable", false, map[string]string{
						"operation": "try_reclaim",
					})
					return nil, ErrIdempotencyStoreUnavail.WithCause(reclaimErr)
				}
				if !taken {
					c.recordIdempotencyConflict(opts.Route, opts.Scope, map[string]string{"reason": "reclaim_race"})
					c.logIdempotencyAudit(opts.Route, opts.Scope, keyHash, "failed_retryable->conflict", false, map[string]string{
						"conflict": "reclaim_race",
					})
					return nil, c.conflictWithRetryAfter(ErrIdempotencyInProgress, existing.LockedUntil, now)
				}
				c.recordIdempotencyClaim(opts.Route, opts.Scope, map[string]string{"mode": "reclaim"})
				c.logIdempotencyAudit(opts.Route, opts.Scope, keyHash, "failed_retryable->processing", false, map[string]string{
					"claim_mode": "reclaim",
				})
				record.ID = existing.ID
			default:
				// An unknown stored state cannot establish completed or retryable work.
				// Return conflict so this request does not execute under an unsupported persistence state.
				c.recordIdempotencyConflict(opts.Route, opts.Scope, map[string]string{"reason": "unexpected_status"})
				c.logIdempotencyAudit(opts.Route, opts.Scope, keyHash, "existing->conflict", false, map[string]string{
					"status": existing.Status,
				})
				return nil, ErrIdempotencyKeyConflict
			}
		}
	}

	if record.ID == 0 {
		c.RecordIdempotencyStoreUnavailable(opts.Route, opts.Scope, "record_id_missing")
		c.logIdempotencyAudit(opts.Route, opts.Scope, keyHash, "processing->store_unavailable", false, map[string]string{
			"operation": "record_id_missing",
		})
		return nil, ErrIdempotencyStoreUnavail
	}

	execStart := time.Now()
	defer func() {
		c.recordIdempotencyProcessingDuration(opts.Route, opts.Scope, time.Since(execStart), nil)
	}()

	data, execErr := execute(ctx)
	if execErr != nil {
		// Record a retry deadline after callback failure, while retaining the callback's
		// original error as the caller-visible result even when saving retry state also fails.
		backoffUntil := time.Now().Add(c.cfg.FailedRetryBackoff)
		reason := infraerrors.Reason(execErr)
		if reason == "" {
			// Plain errors have no application reason code. Store a stable execution-failure
			// label so retry diagnostics remain classifiable without recording the raw error message.
			reason = "EXECUTION_FAILED"
		}
		c.recordIdempotencyRetryBackoff(opts.Route, opts.Scope, nil)
		c.logIdempotencyAudit(opts.Route, opts.Scope, keyHash, "processing->failed_retryable", false, map[string]string{
			"reason": reason,
		})
		if markErr := c.repo.MarkFailedRetryable(ctx, record.ID, reason, backoffUntil, expiresAt); markErr != nil {
			c.RecordIdempotencyStoreUnavailable(opts.Route, opts.Scope, "mark_failed_retryable_error")
			c.logIdempotencyAudit(opts.Route, opts.Scope, keyHash, "processing->store_unavailable", false, map[string]string{
				"operation": "mark_failed_retryable",
			})
		}
		return nil, execErr
	}

	// The business callback has already succeeded. Encode and persist its response
	// before returning success, so later duplicates can replay the recorded outcome when storage works.
	storedBody, marshalErr := c.marshalStoredResponse(data)
	if marshalErr != nil {
		c.RecordIdempotencyStoreUnavailable(opts.Route, opts.Scope, "marshal_response_error")
		c.logIdempotencyAudit(opts.Route, opts.Scope, keyHash, "processing->store_unavailable", false, map[string]string{
			"operation": "marshal_response",
		})
		return nil, ErrIdempotencyStoreUnavail.WithCause(marshalErr)
	}
	if markErr := c.repo.MarkSucceeded(ctx, record.ID, 200, storedBody, expiresAt); markErr != nil {
		c.RecordIdempotencyStoreUnavailable(opts.Route, opts.Scope, "mark_succeeded_error")
		c.logIdempotencyAudit(opts.Route, opts.Scope, keyHash, "processing->store_unavailable", false, map[string]string{
			"operation": "mark_succeeded",
		})
		return nil, ErrIdempotencyStoreUnavail.WithCause(markErr)
	}
	c.logIdempotencyAudit(opts.Route, opts.Scope, keyHash, "processing->succeeded", false, nil)

	return &IdempotencyExecuteResult{Data: data}, nil
}

// conflictWithRetryAfter adds retry_after metadata to a conflict with a lock
// deadline. A nil deadline returns the base error unchanged; computed delays use
// whole seconds with a one-second minimum even when the deadline has passed.
func (c *IdempotencyCoordinator) conflictWithRetryAfter(base *infraerrors.ApplicationError, lockedUntil *time.Time, now time.Time) error {
	if lockedUntil == nil {
		return base
	}
	// Export a whole-second HTTP delay while keeping expired or fractional windows
	// positive, so a supplied lock deadline still yields usable Retry-After metadata.
	sec := int(lockedUntil.Sub(now).Seconds())
	if sec <= 0 {
		sec = 1
	}
	return base.WithMetadata(map[string]string{"retry_after": strconv.Itoa(sec)})
}

// marshalStoredResponse JSON-encodes and redacts callback data before recording
// it. A positive byte limit truncates at a valid UTF-8 prefix and appends a marker;
// the resulting text may fail JSON decoding on replay, while nonpositive limits
// leave the redacted response length unrestricted.
func (c *IdempotencyCoordinator) marshalStoredResponse(data any) (string, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return "", err
	}
	redacted := logredact.RedactText(string(raw))
	if c.cfg.MaxStoredResponseLen > 0 && len(redacted) > c.cfg.MaxStoredResponseLen {
		// Database text requires valid UTF-8 even when a byte cutoff lands inside a
		// multibyte rune. The marker identifies that the stored response is only a bounded prefix.
		redacted = truncateUTF8(redacted, c.cfg.MaxStoredResponseLen) + "...(truncated)"
	}
	return redacted, nil
}

// truncateUTF8 returns a valid UTF-8 prefix of an oversized string within the
// supplied byte limit. Nonpositive limits and already short input return s unchanged,
// so callers provide valid UTF-8 if they need that property on those pass-through paths.
func truncateUTF8(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	// Back up from an incomplete rune rather than storing invalid UTF-8 bytes,
	// retaining complete preceding characters that the database text column can accept.
	for maxBytes > 0 && !utf8.ValidString(s[:maxBytes]) {
		maxBytes--
	}
	return s[:maxBytes]
}

// decodeStoredResponse parses recorded JSON into encoding/json's generic value
// types for replay. Nil or whitespace-only text returns an empty object; malformed
// or truncated data returns a decoding error that Execute reports as store unavailability.
func (c *IdempotencyCoordinator) decodeStoredResponse(stored *string) (any, error) {
	if stored == nil || strings.TrimSpace(*stored) == "" {
		return map[string]any{}, nil
	}
	var out any
	if err := json.Unmarshal([]byte(*stored), &out); err != nil {
		return nil, fmt.Errorf("decode stored response: %w", err)
	}
	return out, nil
}
