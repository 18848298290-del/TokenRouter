package telemetry

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"maps"
	"reflect"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// request_record.go captures logical request diagnostics and merges versioned
// snapshots for storage.
// One logical request can outlive its HTTP handler while upstream attempts
// and detached completion work add diagnostics. Its capture serializes
// mutations and ordinary observer publication under one mutex, assigning a
// later update timestamp to each changed record. Request IDs identify the
// logical record; external IDs, billing references, and related requests
// remain labeled aliases.
//
// Saved snapshots merge by update time so later sparse metadata can fill an
// existing record while retained failure or cancellation state survives later
// completion work. Collection cloning protects aliases, attempts, and timing
// maps, while the FinishedAt pointer remains shared. Related-record
// publication occurs outside the parent lock before adding the parent alias,
// and normalization prepares bounded diagnostic text for PostgreSQL storage.

// requestCaptureKey names the context slot for a shared logical recorder,
// allowing detached completion work to update the same request snapshot.
const requestCaptureKey ContextKey = "request_capture"

// RequestAlias labels an external identifier, billing key, or related request ID.
type RequestAlias struct {
	Kind          string `json:"kind"`                     // alias category text
	Source        string `json:"source,omitempty"`         // observed header or response field
	AttemptNumber int    `json:"attempt_number,omitempty"` // owning upstream attempt number
	ProviderID    int64  `json:"provider_id,omitempty"`    // owning upstream provider identifier
	// Value is trimmed during normalization; values above 255 bytes or containing
	// CR, LF, or NUL are rejected before aliases enter diagnostic storage.
	Value string `json:"value"` // external or related identifier
}

// RequestAttempt holds bounded diagnostics describing one observed upstream attempt.
type RequestAttempt struct {
	Stage  string `json:"stage,omitempty"`  // observed failure phase
	Reason string `json:"reason,omitempty"` // bounded failure explanation

	StartedAt *time.Time `json:"started_at,omitempty"` // observed exchange dispatch time

	// FailureTimings retains bounded connection and failure-stage observations for an
	// unsuccessful exchange. A usage-producing attempt stores its performance on usage.
	FailureTimings map[string]int64 `json:"failure_timings,omitempty"`

	Number          int    `json:"number"`                               // upstream attempt ordinal
	ProviderID      int64  `json:"provider_id,omitempty"`                // attempted provider database ID
	RequestID       string `json:"upstream_request_id,omitempty"`        // external upstream request ID
	RequestIDHeader string `json:"upstream_request_id_header,omitempty"` // selected upstream response header
	Status          int    `json:"status_code,omitempty"`                // observed HTTP status code
	DurationMs      *int64 `json:"duration_ms,omitempty"`                // attempt duration in milliseconds
	Outcome         string `json:"outcome,omitempty"`                    // observed attempt outcome text
}

// RequestRecord summarizes a request independently of monitoring sampling.
// Keep request bodies and credentials out of its fields.
type RequestRecord struct {
	UsageWriteUpdatedAt  time.Time `json:"usage_write_updated_at,omitzero"`   // last persistence evidence time
	UsageWriteDurationMs *int64    `json:"usage_write_duration_ms,omitempty"` // analytical write operation duration

	UsageWriteState      string     `json:"usage_write_state,omitempty"`      // observed persistence outcome
	UsageWriteError      string     `json:"usage_write_error,omitempty"`      // bounded persistence diagnostic
	UsageAttempt         int64      `json:"usage_attempt,omitempty"`          // usage-producing exchange ordinal
	ClientDisconnectedAt *time.Time `json:"client_disconnected_at,omitempty"` // observed downstream cancellation

	ErrorCode       string `json:"error_code,omitempty"`        // diagnostic error category text
	RequestID       string `json:"request_id"`                  // internal logical request ID
	ParentRequestID string `json:"parent_request_id,omitempty"` // parent logical request ID
	// StartedAt is retained from the saved snapshot during MergeRequestRecord,
	// keeping one request window while later observations fill its metadata.
	StartedAt time.Time `json:"started_at"` // logical request start time
	// UpdatedAt orders snapshot merging. Capture mutations leave this value to
	// UpdateRequest, which assigns a later value relative to the post-mutation
	// record.
	UpdatedAt time.Time `json:"updated_at"` // snapshot update ordering time
	// FinishedAt remains shared across collection clones and merged records.
	// Callers replace the pointer for a new observation and treat any published
	// timestamp as immutable.
	FinishedAt *time.Time `json:"finished_at,omitempty"` // optional terminal observation time
	Method     string     `json:"method,omitempty"`      // request HTTP method text
	Path       string     `json:"path,omitempty"`        // request URL path text
	// State retains saved failed or canceled outcomes during merging; a running
	// update also retains saved state when the saved request has a finish time.
	State      string `json:"state"`                 // logical request lifecycle state
	Status     int    `json:"status_code,omitempty"` // final HTTP status code
	DurationMs int64  `json:"duration_ms,omitempty"` // request duration in milliseconds
	UserID     int64  `json:"user_id,omitempty"`     // acting user database ID
	TeamID     int64  `json:"team_id,omitempty"`     // owning team database ID
	APIKeyID   int64  `json:"api_key_id,omitempty"`  // authenticated key database ID
	ProviderID int64  `json:"provider_id,omitempty"` // observed provider database ID
	Platform   string `json:"platform,omitempty"`    // observed upstream platform text
	Model      string `json:"model,omitempty"`       // requested display model text
	// Aliases, Attempts, and FailureTimings are copied by CloneRequestRecord and
	// NormalizeRequestRecord. WithRequestCapture retains the supplied
	// collections, and MergeRequestRecord can reuse absent collections from the
	// saved record.
	Aliases        []RequestAlias   `json:"aliases,omitempty"`         // labeled correlated identifier values
	Attempts       []RequestAttempt `json:"attempts,omitempty"`        // retained upstream attempt diagnostics
	FailureTimings map[string]int64 `json:"failure_timings,omitempty"` // stage elapsed milliseconds by name
}

// RequestCapture serializes record updates and their observer notifications.
type RequestCapture struct {
	// mu protects record mutations and ordinary observe callbacks together.
	// Callbacks cannot reenter capture operations; RecordRelatedRequest samples
	// its observer here and publishes after unlocking.
	mu sync.Mutex // record and observer mutex
	// record retains the supplied snapshot and its mutable values at creation.
	// Callers transfer collection use to locked capture updates after attaching it.
	record  RequestRecord       // current logical request snapshot
	observe func(RequestRecord) // optional record publication callback
}

// NewRequestID returns a randomly generated UUID with version-four and RFC
// variant bits. The selected Go crypto/rand.Read fills all sixteen bytes or
// terminates the process on entropy-reader failure, so this API has no
// recoverable error result.
func NewRequestID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return formatRequestID(b[:])
}

// StableRequestID derives a repeatable UUID-shaped version-eight identifier
// from the exact task key. A fixed TokenRouter namespace is hashed with
// SHA-256 before formatting sixteen bytes, so callers reuse the same key for
// replay identity.
func StableRequestID(key string) string {
	b := sha256.Sum256([]byte("TokenRouter/request/" + key))
	b[6] = b[6]&0x0f | 0x80
	b[8] = b[8]&0x3f | 0x80
	return formatRequestID(b[:16])
}

// formatRequestID formats a sixteen-byte UUID as lowercase hyphenated text.
// Callers supply exactly sixteen bytes and set version and variant bits first;
// shorter input panics, while longer input extends the final hex segment.
func formatRequestID(b []byte) string {
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// RequestIDValue returns the trimmed string stored under the internal
// RequestID key. Nil contexts, absent values, and differently typed values
// return empty text for callers choosing or creating an identity.
func RequestIDValue(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(RequestID).(string)
	return strings.TrimSpace(id)
}

// WithRequestCapture attaches a new capture to non-nil ctx and delegates an
// initial no-op mutation to UpdateRequest. Zero UpdatedAt forces initial
// publication; an existing version with an equal cloned comparison can skip
// it. Supplied mutable values remain retained, so callers transfer collection
// use to capture updates and leave shared FinishedAt immutable.
func WithRequestCapture(ctx context.Context, record RequestRecord, observe func(RequestRecord)) context.Context {
	capture := &RequestCapture{record: record, observe: observe}
	ctx = context.WithValue(ctx, requestCaptureKey, capture)
	UpdateRequest(ctx, func(*RequestRecord) {})
	return ctx
}

// UpdateRequest invokes a mutation under the capture mutex and publishes a
// changed snapshot before unlocking. Nil contexts or absent captures are
// ignored. A non-nil update and any observer run under the same lock and must
// not reenter capture operations or retain mutable record storage.
//
// Mutators leave UpdatedAt versioning to this function. Except for initial
// zero-version publication, an equal cloned record skips notification;
// changed records receive now or one nanosecond after their post-mutation
// UpdatedAt. Collection copies isolate observer snapshots while FinishedAt
// remains shared and immutable by caller obligation.
func UpdateRequest(ctx context.Context, update func(*RequestRecord)) {
	if ctx == nil {
		return
	}
	capture, _ := ctx.Value(requestCaptureKey).(*RequestCapture)
	if capture == nil {
		return
	}
	capture.mu.Lock()
	defer capture.mu.Unlock()
	previous := cloneRequestRecord(capture.record)
	update(&capture.record)
	if !previous.UpdatedAt.IsZero() && reflect.DeepEqual(previous, capture.record) {
		return
	}
	now := time.Now().UTC()
	// Clock precision or a supplied future timestamp can otherwise repeat a
	// snapshot version. Advance from the post-mutation record before observer publication.
	if !now.After(capture.record.UpdatedAt) {
		now = capture.record.UpdatedAt.Add(time.Nanosecond)
	}
	capture.record.UpdatedAt = now
	if capture.observe != nil {
		capture.observe(cloneRequestRecord(capture.record))
	}
}

// UpdateRequestForID invokes update when the captured RequestID exactly
// equals id and reports that match. Missing captures and mismatched IDs
// return false; matched callbacks follow UpdateRequest's lock, non-reentry,
// and versioning rules even when they make no change.
func UpdateRequestForID(ctx context.Context, id string, update func(*RequestRecord)) bool {
	matched := false
	UpdateRequest(ctx, func(record *RequestRecord) {
		if record.RequestID == id {
			matched = true
			update(record)
		}
	})
	return matched
}

// AddRequestAlias adds kind with its trimmed value when that value is valid
// and the capture has room. Empty, over-255-byte, or CR/LF/NUL values are
// ignored; duplicate pairs and aliases past the 128-entry cap add no record
// change. kind is normalized later during storage preparation.
func AddRequestAlias(ctx context.Context, kind, value string) {
	AddRequestAliasWithSource(ctx, RequestAlias{Kind: kind, Value: value})
}

// CopyRequestCapture attaches source's shared recorder to destination when
// present. A nil or uncaptured source returns destination unchanged;
// destination must be non-nil when a capture is attached, and both contexts
// then synchronize through the same recorder.
func CopyRequestCapture(destination, source context.Context) context.Context {
	if source == nil {
		return destination
	}
	if capture, ok := source.Value(requestCaptureKey).(*RequestCapture); ok {
		return context.WithValue(destination, requestCaptureKey, capture)
	}
	return destination
}

// RecordRelatedRequest prepares a supplied child and calls the sampled parent
// observer when present, filling absent identity, method, and path from the
// parent. Missing captures are ignored. It assigns update time and supplies
// finish time for completed, failed, or canceled children when absent.
//
// The parent and observer are sampled under lock, then child publication runs
// after unlocking. The related alias is added afterward through ordinary
// parent mutation, so concurrent publication can interleave and the two
// notifications are not atomic. Supplied child mutable values are passed
// directly to the observer.
func RecordRelatedRequest(ctx context.Context, record RequestRecord) {
	if ctx == nil {
		return
	}
	capture, _ := ctx.Value(requestCaptureKey).(*RequestCapture)
	if capture == nil {
		return
	}
	capture.mu.Lock()
	parent := cloneRequestRecord(capture.record)
	observe := capture.observe
	capture.mu.Unlock()
	record.ParentRequestID = parent.RequestID
	if record.UserID == 0 {
		record.UserID = parent.UserID
	}
	if record.TeamID == 0 {
		record.TeamID = parent.TeamID
	}
	if record.APIKeyID == 0 {
		record.APIKeyID = parent.APIKeyID
	}
	if record.Method == "" {
		record.Method = parent.Method
	}
	if record.Path == "" {
		record.Path = parent.Path
	}
	if record.StartedAt.IsZero() {
		record.StartedAt = time.Now().UTC()
	}
	record.UpdatedAt = time.Now().UTC()
	if record.FinishedAt == nil && (record.State == "completed" || record.State == "failed" || record.State == "canceled") {
		finished := record.UpdatedAt
		record.FinishedAt = &finished
	}
	// Publish the sampled child first, then update the parent alias through
	// its lock. These separately ordered notifications may interleave with other captures.
	if observe != nil {
		observe(record)
	}
	AddRequestAlias(ctx, "related", record.RequestID)
}

// cloneRequestRecord copies alias and attempt slices and clones request and
// attempt failure-timing maps. Optional timestamp and duration pointers remain
// shared; callers treat their values as immutable when retaining these records.
func cloneRequestRecord(record RequestRecord) RequestRecord {
	record.Aliases = append([]RequestAlias(nil), record.Aliases...)
	record.Attempts = append([]RequestAttempt(nil), record.Attempts...)
	for i := range record.Attempts {
		record.Attempts[i].FailureTimings = maps.Clone(record.Attempts[i].FailureTimings)
	}
	record.FailureTimings = maps.Clone(record.FailureTimings)
	return record
}

// MergeRequestRecord combines snapshots for the same logical ID, which
// callers verify before invoking it. Older or equal UpdatedAt returns
// previous directly. A newer snapshot keeps the saved start and nonempty
// parent identity, and fills missing metadata, terminal time, failure diagnostics, or
// attempts from previous. Timing maps are merged into independent storage,
// retaining earlier keys when a newer snapshot only adds transport stages.
//
// Saved failed or canceled state wins; a running next state also retains a
// previous state when the saved record has a finish time. Aliases are
// deduplicated in previous-then-next order up to 128. Other retained
// collections and FinishedAt can share either input's storage, so callers
// clone before independent mutation.
func MergeRequestRecord(previous, next RequestRecord) RequestRecord {
	if !next.UpdatedAt.After(previous.UpdatedAt) {
		return previous
	}
	next.StartedAt = previous.StartedAt
	if previous.ParentRequestID != "" {
		next.ParentRequestID = previous.ParentRequestID
	}
	if next.Method == "" {
		next.Method = previous.Method
	}
	if next.Path == "" {
		next.Path = previous.Path
	}
	if next.UserID == 0 {
		next.UserID = previous.UserID
	}
	if next.TeamID == 0 {
		next.TeamID = previous.TeamID
	}
	if next.APIKeyID == 0 {
		next.APIKeyID = previous.APIKeyID
	}
	if next.ProviderID == 0 {
		next.ProviderID = previous.ProviderID
	}
	if next.Model == "" {
		next.Model = previous.Model
	}
	if next.Platform == "" {
		next.Platform = previous.Platform
	}
	if next.ErrorCode == "" {
		next.ErrorCode = previous.ErrorCode
	}
	if next.Status == 0 {
		next.Status = previous.Status
	}
	if next.DurationMs == 0 {
		next.DurationMs = previous.DurationMs
	}
	if next.FinishedAt == nil {
		next.FinishedAt = previous.FinishedAt
	}
	// Sparse completion snapshots can add transport observations after admission
	// was published. Merge into a fresh map so neither caller-owned snapshot is
	// mutated and earlier stage durations survive those later additions.
	mergedFailureTimings := maps.Clone(previous.FailureTimings)
	if mergedFailureTimings == nil && next.FailureTimings != nil {
		mergedFailureTimings = make(map[string]int64)
	}
	maps.Copy(mergedFailureTimings, next.FailureTimings)
	next.FailureTimings = mergedFailureTimings
	if len(next.Attempts) == 0 {
		next.Attempts = previous.Attempts
	}
	// Accept newer metadata while retaining a saved failure or cancellation. A
	// running update also cannot reopen a record that already has a finish timestamp.
	if previous.State == "failed" || previous.State == "canceled" || next.State == "running" && previous.FinishedAt != nil {
		next.State = previous.State
	}
	// Preserve previous aliases before newer aliases while deduplicating pairs
	// and retaining the earliest 128, so sparse updates keep known correlations.
	seen := make(map[RequestAlias]bool)
	aliases := append(append([]RequestAlias(nil), previous.Aliases...), next.Aliases...)
	next.Aliases = nil
	for _, alias := range aliases {
		if !seen[alias] && len(next.Aliases) < 128 {
			next.Aliases = append(next.Aliases, alias)
			seen[alias] = true
		}
	}
	return next
}

// CloneRequestRecord copies aliases, attempts, and failure-timing maps for
// independent collection mutation. Optional timestamp and duration pointers
// remain shared and must be treated as immutable; scalar values are copied.
func CloneRequestRecord(record RequestRecord) RequestRecord { return cloneRequestRecord(record) }

// WithChildRequest creates a separate logical capture on non-nil ctx and sets
// its RequestID to the supplied child's ID. An existing parent capture
// supplies its observer and overwrites the child's user, team, key, method,
// and path metadata.
//
// Without a parent capture, context identity supplies ParentRequestID and an
// absent identity is generated. The parent receives a related alias before
// the child's initial publication. The new capture has its own lock but can
// share the same observer, so observers must support separate concurrent
// logical captures.
func WithChildRequest(ctx context.Context, record RequestRecord) context.Context {
	var observe func(RequestRecord)
	if capture, ok := ctx.Value(requestCaptureKey).(*RequestCapture); ok {
		capture.mu.Lock()
		parent := capture.record
		observe = capture.observe
		capture.mu.Unlock()
		record.ParentRequestID = parent.RequestID
		record.UserID = parent.UserID
		record.TeamID = parent.TeamID
		record.APIKeyID = parent.APIKeyID
		record.Method = parent.Method
		record.Path = parent.Path
	} else {
		record.ParentRequestID = RequestIDValue(ctx)
		if record.ParentRequestID == "" {
			record.ParentRequestID = NewRequestID()
		}
	}
	// Publish the parent relationship before the child capture can notify its
	// observer, while keeping the two captures on separate locks for later updates.
	AddRequestAlias(ctx, "related", record.RequestID)
	ctx = context.WithValue(ctx, RequestID, record.RequestID)
	return WithRequestCapture(ctx, record, observe)
}

// IsChildRequest reports whether the captured record has a nonempty parent
// request ID. Nil contexts or absent captures return false; the parent field
// is read while the capture mutex excludes updates.
func IsChildRequest(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	capture, ok := ctx.Value(requestCaptureKey).(*RequestCapture)
	if !ok {
		return false
	}
	capture.mu.Lock()
	defer capture.mu.Unlock()
	return capture.record.ParentRequestID != ""
}

// NormalizeRequestRecord copies collections and prepares bounded text and IDs
// for diagnostic storage. Text fields remove invalid UTF-8 and NULs before
// byte limits retain complete characters. Alias values are trimmed, rejected
// for length or CR/LF/NUL, and deduplicated after kind normalization.
//
// Only the first 128 accepted aliases and first 256 attempts remain. Attempt
// outcomes are normalized, while invalid attempt IDs are cleared and valid ID
// spelling remains as supplied. FinishedAt stays shared; request identity and
// timing-map entries are retained without additional validation.
func NormalizeRequestRecord(record RequestRecord) RequestRecord {
	record = cloneRequestRecord(record)
	record.Model = requestText(record.Model, 200)
	record.Path = requestText(record.Path, 2048)
	record.Method = requestText(record.Method, 32)
	record.ErrorCode = requestText(record.ErrorCode, 64)
	record.UsageWriteState = requestText(record.UsageWriteState, 32)
	record.UsageWriteError = requestText(record.UsageWriteError, 512)
	record.Platform = requestText(record.Platform, 64)
	aliases := record.Aliases
	record.Aliases = nil
	seen := make(map[RequestAlias]bool)
	// Normalize alias kinds before deduplication because distinct supplied text
	// can become the same stored pair. Keep accepted pairs in input order until
	// the diagnostic storage cap is reached.
	for _, alias := range aliases {
		alias.Value = strings.TrimSpace(alias.Value)
		alias.Kind = requestText(alias.Kind, 32)
		alias.Source = requestText(alias.Source, 128)
		if alias.Value == "" || len(alias.Value) > 255 || strings.ContainsAny(alias.Value, "\r\n\x00") || seen[alias] {
			continue
		}
		seen[alias] = true
		record.Aliases = append(record.Aliases, alias)
		if len(record.Aliases) == 128 {
			break
		}
	}
	// Retain the earliest attempt diagnostics within the storage cap; metadata
	// and failure diagnostics still describe the request when later attempt details are omitted.
	if len(record.Attempts) > 256 {
		record.Attempts = record.Attempts[:256]
	}
	for i := range record.Attempts {
		record.Attempts[i].Outcome = requestText(record.Attempts[i].Outcome, 32)
		record.Attempts[i].Stage = requestText(record.Attempts[i].Stage, 64)
		record.Attempts[i].Reason = requestText(record.Attempts[i].Reason, 512)
		record.Attempts[i].RequestIDHeader = requestText(record.Attempts[i].RequestIDHeader, 64)
		if len(record.Attempts[i].RequestID) > 255 || strings.ContainsAny(record.Attempts[i].RequestID, "\r\n\x00") {
			record.Attempts[i].RequestID = ""
		}
	}
	return record
}

// requestText removes invalid UTF-8 and NULs, then fits text to a nonnegative
// byte limit. It backs off a cut multibyte suffix until valid UTF-8 remains;
// callers supply a nonnegative limit because negative slicing panics.
func requestText(value string, limit int) string {
	value = strings.ToValidUTF8(value, "")
	// PostgreSQL text and JSONB reject NUL characters. Remove them before
	// applying text byte budgets so decoded paths and diagnostics can be stored.
	value = strings.ReplaceAll(value, "\x00", "")
	if len(value) <= limit {
		return value
	}
	// Cut to the byte budget, then back off an incomplete multibyte suffix
	// so diagnostic text remains storable as valid UTF-8 after truncation.
	value = value[:limit]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
