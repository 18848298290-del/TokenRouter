package telemetry

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const requestCaptureKey ContextKey = "request_capture"

// RequestAlias labels an external identifier, billing key, or related request ID.
type RequestAlias struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

// RequestAttempt holds diagnostics for one upstream attempt.
type RequestAttempt struct {
	Number     int    `json:"number"`
	ProviderID int64  `json:"provider_id,omitempty"`
	RequestID  string `json:"upstream_request_id,omitempty"`
	Status     int    `json:"status_code,omitempty"`
	DurationMs int64  `json:"duration_ms,omitempty"`
	Outcome    string `json:"outcome,omitempty"`
}

// RequestRecord summarizes a request independently of monitoring sampling.
// Keep request bodies and credentials out of its fields.
type RequestRecord struct {
	ErrorCode       string           `json:"error_code,omitempty"`
	RequestID       string           `json:"request_id"`
	ParentRequestID string           `json:"parent_request_id,omitempty"`
	StartedAt       time.Time        `json:"started_at"`
	UpdatedAt       time.Time        `json:"updated_at"`
	FinishedAt      *time.Time       `json:"finished_at,omitempty"`
	Method          string           `json:"method,omitempty"`
	Path            string           `json:"path,omitempty"`
	State           string           `json:"state"`
	Status          int              `json:"status_code,omitempty"`
	DurationMs      int64            `json:"duration_ms,omitempty"`
	UserID          int64            `json:"user_id,omitempty"`
	TeamID          int64            `json:"team_id,omitempty"`
	APIKeyID        int64            `json:"api_key_id,omitempty"`
	ProviderID      int64            `json:"provider_id,omitempty"`
	Platform        string           `json:"platform,omitempty"`
	Model           string           `json:"model,omitempty"`
	Aliases         []RequestAlias   `json:"aliases,omitempty"`
	Attempts        []RequestAttempt `json:"attempts,omitempty"`
	Timings         map[string]int64 `json:"timings,omitempty"`
}

// RequestCapture serializes record updates and their observer notifications.
type RequestCapture struct {
	mu      sync.Mutex
	record  RequestRecord
	observe func(RequestRecord)
}

// NewRequestID generates a random version 4 UUID.
func NewRequestID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return formatRequestID(b[:])
}

// StableRequestID derives a deterministic request ID from a background task key.
func StableRequestID(key string) string {
	b := sha256.Sum256([]byte("TokenRouter/request/" + key))
	b[6] = b[6]&0x0f | 0x80
	b[8] = b[8]&0x3f | 0x80
	return formatRequestID(b[:16])
}

func formatRequestID(b []byte) string {
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// RequestIDValue returns the trimmed internal request ID from context.
func RequestIDValue(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(RequestID).(string)
	return strings.TrimSpace(id)
}

// WithRequestCapture attaches a record and observer, then publishes its initial snapshot.
func WithRequestCapture(ctx context.Context, record RequestRecord, observe func(RequestRecord)) context.Context {
	capture := &RequestCapture{record: record, observe: observe}
	ctx = context.WithValue(ctx, requestCaptureKey, capture)
	UpdateRequest(ctx, func(*RequestRecord) {})
	return ctx
}

// UpdateRequest serializes a mutation and publishes a changed snapshot with an increasing timestamp.
// The update and observer callbacks run under the capture lock and must not reenter it.
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
	if !now.After(capture.record.UpdatedAt) {
		now = capture.record.UpdatedAt.Add(time.Nanosecond)
	}
	capture.record.UpdatedAt = now
	if capture.observe != nil {
		capture.observe(cloneRequestRecord(capture.record))
	}
}

// UpdateRequestForID applies update when the captured request ID matches and reports the match.
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

// AddRequestAlias registers a unique alias, ignoring invalid values and entries beyond the limit.
func AddRequestAlias(ctx context.Context, kind, value string) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 255 || strings.ContainsAny(value, "\r\n\x00") {
		return
	}
	UpdateRequest(ctx, func(record *RequestRecord) {
		alias := RequestAlias{Kind: kind, Value: value}
		if slices.Contains(record.Aliases, alias) {
			return
		}
		if len(record.Aliases) < 128 {
			record.Aliases = append(record.Aliases, alias)
		}
	})
}

// CopyRequestCapture shares the recorder with a context used by detached completion work.
func CopyRequestCapture(destination, source context.Context) context.Context {
	if source == nil {
		return destination
	}
	if capture, ok := source.Value(requestCaptureKey).(*RequestCapture); ok {
		return context.WithValue(destination, requestCaptureKey, capture)
	}
	return destination
}

// RecordRelatedRequest publishes a child record with inherited request metadata
// and adds its ID to the parent's aliases.
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
	if observe != nil {
		observe(record)
	}
	AddRequestAlias(ctx, "related", record.RequestID)
}

func cloneRequestRecord(record RequestRecord) RequestRecord {
	record.Aliases = append([]RequestAlias(nil), record.Aliases...)
	record.Attempts = append([]RequestAttempt(nil), record.Attempts...)
	record.Timings = maps.Clone(record.Timings)
	return record
}

// MergeRequestRecord merges a newer snapshot with saved metadata and aliases.
// Older or equal timestamps return previous; saved failure and cancellation states take precedence.
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
	if next.Timings == nil {
		next.Timings = previous.Timings
	}
	if len(next.Attempts) == 0 {
		next.Attempts = previous.Attempts
	}
	if previous.State == "failed" || previous.State == "canceled" || next.State == "running" && previous.FinishedAt != nil {
		next.State = previous.State
	}
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

// CloneRequestRecord copies slices and the timings map for independent mutation.
// The FinishedAt pointer remains shared and should be treated as immutable.
func CloneRequestRecord(record RequestRecord) RequestRecord { return cloneRequestRecord(record) }

// WithChildRequest creates a separate capture for a logical request on the same connection.
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
	AddRequestAlias(ctx, "related", record.RequestID)
	ctx = context.WithValue(ctx, RequestID, record.RequestID)
	return WithRequestCapture(ctx, record, observe)
}

// IsChildRequest reports whether the captured record has a parent request ID.
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

// NormalizeRequestRecord bounds diagnostic text and collections, discarding invalid external IDs.
func NormalizeRequestRecord(record RequestRecord) RequestRecord {
	record = cloneRequestRecord(record)
	record.Model = requestText(record.Model, 200)
	record.Path = requestText(record.Path, 2048)
	record.Method = requestText(record.Method, 32)
	record.ErrorCode = requestText(record.ErrorCode, 64)
	record.Platform = requestText(record.Platform, 64)
	aliases := record.Aliases
	record.Aliases = nil
	seen := make(map[RequestAlias]bool)
	for _, alias := range aliases {
		alias.Value = strings.TrimSpace(alias.Value)
		alias.Kind = requestText(alias.Kind, 32)
		if alias.Value == "" || len(alias.Value) > 255 || strings.ContainsAny(alias.Value, "\r\n\x00") || seen[alias] {
			continue
		}
		seen[alias] = true
		record.Aliases = append(record.Aliases, alias)
		if len(record.Aliases) == 128 {
			break
		}
	}
	if len(record.Attempts) > 256 {
		record.Attempts = record.Attempts[:256]
	}
	for i := range record.Attempts {
		record.Attempts[i].Outcome = requestText(record.Attempts[i].Outcome, 32)
		if len(record.Attempts[i].RequestID) > 255 || strings.ContainsAny(record.Attempts[i].RequestID, "\r\n\x00") {
			record.Attempts[i].RequestID = ""
		}
	}
	return record
}

func requestText(value string, limit int) string {
	value = strings.ToValidUTF8(value, "")
	// PostgreSQL text and JSONB reject NUL characters, which decoded paths may contain.
	value = strings.ReplaceAll(value, "\x00", "")
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
