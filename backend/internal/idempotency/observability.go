package idempotency

import (
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Idempotency diagnostics count claims, replays, conflicts, and store failures
// across every coordinator in the process. Counters are updated independently,
// while observer messages add route and scope context using sorted attributes.
// Values replace common ASCII whitespace so routine route and status text fits
// the key=value event layout.

// defaultIdempotencyMetrics accumulates counters across coordinator instances for
// process-level diagnostics; constructing another coordinator does not reset observations already recorded.
var defaultIdempotencyMetrics idempotencyMetrics

// IdempotencyMetricsSnapshot reports cumulative independently sampled process-wide idempotency counters.
type IdempotencyMetricsSnapshot struct {
	ClaimTotal        uint64 `json:"claim_total"`         // new and reclaimed executions
	ReplayTotal       uint64 `json:"replay_total"`        // stored responses returned
	ConflictTotal     uint64 `json:"conflict_total"`      // requests denied by existing records
	RetryBackoffTotal uint64 `json:"retry_backoff_total"` // failure and backoff observations
	// ProcessingDurationCount and ProcessingDurationTotalMs describe measured execution
	// attempts, including outcome storage after the callback. They are loaded separately,
	// so a concurrent observation can appear in one field before the other.
	ProcessingDurationCount   uint64  `json:"processing_duration_count"`    // timed idempotent execution attempts
	ProcessingDurationTotalMs float64 `json:"processing_duration_total_ms"` // milliseconds
	StoreUnavailableTotal     uint64  `json:"store_unavailable_total"`      // observed idempotency storage failures
}

// idempotencyMetrics accumulates atomic event counts shared by all process coordinators.
type idempotencyMetrics struct {
	// claimTotal, replayTotal, conflictTotal, retryBackoffTotal, processingDurationCount,
	// processingDurationMicros, and storeUnavailableTotal are updated independently.
	// Atomic operations protect each value across coordinators; snapshots may span
	// different update instants rather than reading one shared transaction.
	claimTotal               atomic.Uint64 // accepted execution claim count
	replayTotal              atomic.Uint64 // successful response replay count
	conflictTotal            atomic.Uint64 // denied duplicate request count
	retryBackoffTotal        atomic.Uint64 // retry-backoff event observation count
	processingDurationCount  atomic.Uint64 // measured idempotent execution count
	processingDurationMicros atomic.Uint64 // microseconds
	storeUnavailableTotal    atomic.Uint64 // storage unavailability observation count
}

// Observer receives idempotency event messages prepared by the caller.
type Observer interface {
	// Observe receives a prepared component name and event message synchronously.
	// Implementations decide where to send the message; callers wait for Observe
	// to return before continuing the idempotency operation.
	Observe(component, message string)
}

// ObserverFunc adapts a callback to the idempotency event observer interface.
type ObserverFunc func(component, message string)

// GetIdempotencyMetricsSnapshot reads process-wide counters and converts accumulated
// processing microseconds to milliseconds. Each counter is loaded atomically but
// separately, so the result is a diagnostic sample rather than one transactionally consistent instant.
func GetIdempotencyMetricsSnapshot() IdempotencyMetricsSnapshot {
	totalMicros := defaultIdempotencyMetrics.processingDurationMicros.Load()
	return IdempotencyMetricsSnapshot{
		ClaimTotal:              defaultIdempotencyMetrics.claimTotal.Load(),
		ReplayTotal:             defaultIdempotencyMetrics.replayTotal.Load(),
		ConflictTotal:           defaultIdempotencyMetrics.conflictTotal.Load(),
		RetryBackoffTotal:       defaultIdempotencyMetrics.retryBackoffTotal.Load(),
		ProcessingDurationCount: defaultIdempotencyMetrics.processingDurationCount.Load(),
		// Export milliseconds while retaining microsecond precision in the accumulator.
		ProcessingDurationTotalMs: float64(totalMicros) / 1000.0,
		StoreUnavailableTotal:     defaultIdempotencyMetrics.storeUnavailableTotal.Load(),
	}
}

// recordIdempotencyClaim counts an accepted execution claim and emits its context.
func (c *IdempotencyCoordinator) recordIdempotencyClaim(endpoint, scope string, attrs map[string]string) {
	defaultIdempotencyMetrics.claimTotal.Add(1)
	c.logIdempotencyMetric("idempotency_claim_total", endpoint, scope, "1", attrs)
}

// recordIdempotencyReplay counts a stored-response replay and emits its request context.
func (c *IdempotencyCoordinator) recordIdempotencyReplay(endpoint, scope string, attrs map[string]string) {
	defaultIdempotencyMetrics.replayTotal.Add(1)
	c.logIdempotencyMetric("idempotency_replay_total", endpoint, scope, "1", attrs)
}

// recordIdempotencyConflict counts a denied duplicate request and emits its classification.
func (c *IdempotencyCoordinator) recordIdempotencyConflict(endpoint, scope string, attrs map[string]string) {
	defaultIdempotencyMetrics.conflictTotal.Add(1)
	c.logIdempotencyMetric("idempotency_conflict_total", endpoint, scope, "1", attrs)
}

// recordIdempotencyRetryBackoff counts a failure-backoff observation and emits its request context.
func (c *IdempotencyCoordinator) recordIdempotencyRetryBackoff(endpoint, scope string, attrs map[string]string) {
	defaultIdempotencyMetrics.retryBackoffTotal.Add(1)
	c.logIdempotencyMetric("idempotency_retry_backoff_total", endpoint, scope, "1", attrs)
}

// recordIdempotencyProcessingDuration adds one measured execution duration to process
// counters and emits milliseconds with three decimal places. Negative durations
// become zero, and accumulated microseconds are sampled independently of the attempt count.
func (c *IdempotencyCoordinator) recordIdempotencyProcessingDuration(endpoint, scope string, duration time.Duration, attrs map[string]string) {
	if duration < 0 {
		// Keep negative inputs out of the unsigned duration accumulator.
		duration = 0
	}
	defaultIdempotencyMetrics.processingDurationCount.Add(1)
	// Accumulate whole microseconds; finer duration precision remains outside the counter.
	defaultIdempotencyMetrics.processingDurationMicros.Add(uint64(duration.Microseconds()))
	c.logIdempotencyMetric("idempotency_processing_duration_ms", endpoint, scope, strconv.FormatFloat(duration.Seconds()*1000, 'f', 3, 64), attrs)
}

// RecordIdempotencyStoreUnavailable counts a storage failure and emits the
// caller-selected handling strategy with its route and scope. Empty strategies
// omit the extra attribute while still recording the failure observation.
func (c *IdempotencyCoordinator) RecordIdempotencyStoreUnavailable(endpoint, scope, strategy string) {
	defaultIdempotencyMetrics.storeUnavailableTotal.Add(1)
	attrs := map[string]string{}
	if strategy != "" {
		attrs["strategy"] = strategy
	}
	c.logIdempotencyMetric("idempotency_store_unavailable_total", endpoint, scope, "1", attrs)
}

// logIdempotencyAudit emits a state-transition event with its hashed key and
// replay flag. Attribute keys are sorted and values follow safeAuditField's
// ASCII whitespace replacements; callers supply field-safe attribute names
// because keys are written as supplied.
func (c *IdempotencyCoordinator) logIdempotencyAudit(endpoint, scope, keyHash, stateTransition string, replayed bool, attrs map[string]string) {
	var b strings.Builder
	builderWriteString(&b, "[IdempotencyAudit]")
	builderWriteString(&b, " endpoint=")
	builderWriteString(&b, safeAuditField(endpoint))
	builderWriteString(&b, " scope=")
	builderWriteString(&b, safeAuditField(scope))
	builderWriteString(&b, " key_hash=")
	builderWriteString(&b, safeAuditField(keyHash))
	builderWriteString(&b, " state_transition=")
	builderWriteString(&b, safeAuditField(stateTransition))
	builderWriteString(&b, " replayed=")
	builderWriteString(&b, strconv.FormatBool(replayed))
	if len(attrs) > 0 {
		appendSortedAttrs(&b, attrs)
	}
	c.observe("service.idempotency", b.String())
}

// logIdempotencyMetric emits a named metric event with route, scope, and value.
// Extra attribute keys are sorted, and callers supply field-safe names for those
// keys; values use the same normalization as the primary fields.
func (c *IdempotencyCoordinator) logIdempotencyMetric(name, endpoint, scope, value string, attrs map[string]string) {
	var b strings.Builder
	builderWriteString(&b, "[IdempotencyMetric]")
	builderWriteString(&b, " name=")
	builderWriteString(&b, safeAuditField(name))
	builderWriteString(&b, " endpoint=")
	builderWriteString(&b, safeAuditField(endpoint))
	builderWriteString(&b, " scope=")
	builderWriteString(&b, safeAuditField(scope))
	builderWriteString(&b, " value=")
	builderWriteString(&b, safeAuditField(value))
	if len(attrs) > 0 {
		appendSortedAttrs(&b, attrs)
	}
	c.observe("service.idempotency", b.String())
}

// appendSortedAttrs appends deterministic key=value attributes to the supplied
// builder. Keys retain their supplied spelling, so callers provide field-safe
// names; values follow safeAuditField's replacements of common ASCII whitespace.
func appendSortedAttrs(builder *strings.Builder, attrs map[string]string) {
	if len(attrs) == 0 {
		return
	}
	keys := make([]string, 0, len(attrs))
	for k := range attrs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		builderWriteByte(builder, ' ')
		builderWriteString(builder, k)
		builderWriteByte(builder, '=')
		builderWriteString(builder, safeAuditField(attrs[k]))
	}
}

// safeAuditField trims an event value and replaces four ASCII whitespace characters.
// Empty values use a dash marker; ASCII spaces, tabs, carriage returns, and newlines
// become underscores so prepared observer messages retain their key=value layout.
func safeAuditField(v string) string {
	value := strings.TrimSpace(v)
	if value == "" {
		return "-"
	}
	// Convert ASCII separators to underscores so key=value consumers do not split one
	// attribute into several fields or interpret embedded line endings as another event.
	value = strings.ReplaceAll(value, "\n", "_")
	value = strings.ReplaceAll(value, "\r", "_")
	value = strings.ReplaceAll(value, "\t", "_")
	value = strings.ReplaceAll(value, " ", "_")
	return value
}

// builderWriteString appends text to the in-memory idempotency log message.
func builderWriteString(builder *strings.Builder, value string) {
	// strings.Builder.WriteString always reports a nil error, so appending an in-memory
	// event field has no recoverable I/O failure that the observer path could handle.
	_, _ = builder.WriteString(value)
}

// builderWriteByte appends one separator byte to an idempotency log message.
func builderWriteByte(builder *strings.Builder, value byte) {
	// strings.Builder.WriteByte writes into memory and returns a nil error, so these
	// log separators require no alternate output or recovery path after the append.
	_ = builder.WriteByte(value)
}

// Observe forwards the component and prepared message synchronously to f.
// Callers supply a nonnil callback and account for its execution time because the
// coordinator waits for observation before continuing the request.
func (f ObserverFunc) Observe(component, message string) { f(component, message) }

// notifyObserver synchronously delivers the prepared message when an observer is
// configured. Nil observers leave counters and execution behavior in place while
// skipping the optional diagnostic message delivery.
func notifyObserver(observer Observer, component, message string) {
	if observer != nil {
		observer.Observe(component, message)
	}
}

// observe delivers an idempotency event through this coordinator's optional
// observer. Nil coordinators and missing observers suppress event delivery, letting
// process counters remain available even when no message sink is installed.
func (c *IdempotencyCoordinator) observe(component, message string) {
	if c != nil {
		notifyObserver(c.observer, component, message)
	}
}
