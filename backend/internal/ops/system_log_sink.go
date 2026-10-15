package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logevent"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
)

// system_log_sink.go queues accepted log events and batches them into the
// system-log repository.
// Indexed system logs retain accepted event pointers until one worker prepares
// their stored representation. Admission selects warning/error levels plus
// access/audit components, with an explicit boolean skip marker taking
// precedence. Producers keep event fields and reachable mutable values stable
// after submitting because indexing does not freeze or copy the event.
//
// The worker owns batching and failure state. A failed write discards that
// attempted batch and delays future attempts; batches during the delay are
// dropped rather than retried. Success clears suppression and the error string.
// Stop seals admission and cancels active work, then drains through independent
// write budgets, but suppression still drops drained batches. Joining therefore
// establishes worker completion rather than guaranteed delivery. Health samples
// counters independently and divides successful flush duration by reported
// written rows, separately from queue waiting time.

const (
	// maxSystemLogHostLength bounds hostname runes to migration204's VARCHAR
	// length, so a long captured host does not alone invalidate every row in the
	// batch.
	maxSystemLogHostLength = 255

	// defaultOpsSystemLogFlushBackoff and defaultOpsSystemLogFlushBackoffMax
	// govern suppression after attempted write failure. Dropped batches during
	// that wait do not increase the consecutive attempted-failure count.
	defaultOpsSystemLogFlushBackoff    = 2 * time.Second
	defaultOpsSystemLogFlushBackoffMax = 60 * time.Second
)

// OpsSystemLogSinkHealth reports separately sampled queue, attempt, and
// persistence observations.
type OpsSystemLogSinkHealth struct {
	// QueueDepth, QueueCapacity, DroppedCount, WriteFailed, WrittenCount,
	// AvgWriteDelayMs, and LastError are sampled independently. Worker updates can
	// occur between loads, so health is not one atomic run snapshot.
	QueueDepth    int64  `json:"queue_depth"`        // sampled buffered event count
	QueueCapacity int64  `json:"queue_capacity"`     // configured event channel capacity
	DroppedCount  uint64 `json:"dropped_count"`      // queue-full or suppressed events
	WriteFailed   uint64 `json:"write_failed_count"` // entries in failed attempted batches
	WrittenCount  uint64 `json:"written_count"`      // repository-reported successfully written rows
	// AvgWriteDelayMs divides accumulated successful flush durations by reported
	// written rows, then truncates to milliseconds. It excludes queue waiting and
	// is not an average per-batch or per-event end-to-end delay.
	AvgWriteDelayMs uint64 `json:"avg_write_delay_ms"` // successful flush time per row
	// LastError holds the latest attempted write error, trimmed on read. A
	// successful attempted flush clears it; dropped batches during suppression
	// retain that previous diagnostic.
	LastError string `json:"last_error"` // latest failed attempt diagnostic
}

// OpsSystemLogSink owns one bounded event channel and a permanently stopped
// writer lifecycle. Use NewOpsSystemLogSink for stop coordination, and complete
// startup before installing the sink when accepted events must reach a worker.
type OpsSystemLogSink struct {
	opsRepo SystemLogWriter // assembled system-log batch writer
	host    string          // normalized process hostname value

	// lifecycleMu protects started, stopped, queue admission, and wg registration.
	// Start registers one writer before releasing the lock; Stop seals future
	// admission before cancellation, while the queue itself remains open.
	queue            chan *logevent.LogEvent // retained accepted event pointers
	lifecycleMu      sync.RWMutex            // admission and registration lifecycle lock
	started, stopped bool                    // started: writer launched; stopped: admission sealed
	// stopOnce installs one join worker and stopDone publishes eventual completion
	// to all StopContext callers. Caller timeout affects that wait rather than
	// reopening admission or canceling the detached drain budgets.
	stopOnce sync.Once     // shared shutdown setup guard
	stopDone chan struct{} // writer completion publication signal
	// synchronous failed-attempt reporting callback
	// onWriteFailure receives error, event count, consecutive attempted failures,
	// and delay on the writer goroutine. Callbacks finish before processing
	// resumes; app reports to stderr so failure reporting does not recursively
	// index another failure.
	onWriteFailure func(error, int, int, time.Duration)

	// batchSize is initialized by construction and read by the worker. Tests
	// replace it before Start; callers keep pacing fields unchanged once work can
	// read them.
	batchSize     int           // maximum local batch event count
	flushInterval time.Duration // periodic flush tick cadence

	// flushBackoff and flushBackoffMax stay immutable after Start. flushBackoffFor
	// supplies defaults for nonpositive values and raises an inverted maximum to
	// the base before configured suppression arithmetic.
	flushBackoff    time.Duration // first-failure suppression duration
	flushBackoffMax time.Duration // maximum suppression delay duration

	// ctx and cancel control active worker selects and inherited write
	// cancellation. wg joins that writer; shutdown drains use background contexts
	// so completion can continue after cancellation.
	ctx    context.Context    // worker active-write parent context
	cancel context.CancelFunc // worker cancellation signal function
	wg     sync.WaitGroup     // registered single writer completion

	// droppedCount counts rejected full-channel sends and suppressed events,
	// writeFailed counts attempted failed entries, writtenCount counts reported
	// successful rows, and totalDelayNs adds one successful flush duration per
	// attempt. Failed batches are discarded without entering droppedCount.
	droppedCount uint64 // queue-full and suppressed event total
	writeFailed  uint64 // failed attempted batch entry total
	writtenCount uint64 // successfully reported written row total
	totalDelayNs uint64 // successful flush durations in nanoseconds

	lastError atomic.Value // latest attempted failure text
}

// SystemLogWriter persists prepared system-log batches consumed by the sink
// worker.
type SystemLogWriter interface {
	// BatchInsertSystemLogs writes stable prepared entries with the supplied
	// context and returns reported rows. The PostgreSQL adapter submits eligible
	// entries through COPY, flushes and closes it, and commits its transaction
	// before successful return.
	//
	// A returned count on failure can describe attempted rows rather than durable
	// progress, including an uncertain commit outcome. The sink counts rows only
	// on success, discards failed attempted batches, and starts suppression rather
	// than replaying that count. Other implementations supply equivalent result
	// interpretation for their caller.
	BatchInsertSystemLogs(context.Context, []*OpsInsertSystemLogInput) (int64, error)
}

// SystemLogSinkOptions supplies captured process metadata and one
// failed-attempt callback at construction.
type SystemLogSinkOptions struct {
	// Host and HostError represent one hostname lookup. Any error selects unknown
	// even when Host is nonempty; otherwise normalization trims and rune-bounds
	// the supplied name.
	Host      string // captured raw process hostname
	HostError error  // captured hostname lookup error
	// synchronous failed-attempt reporting callback
	// OnWriteFailure is retained from the first options value and called
	// synchronously on attempted failure. Bind a callback whose reporting can
	// complete without requiring this worker's successful persistence.
	OnWriteFailure func(error, int, int, time.Duration)
}

// NewOpsSystemLogSink allocates a five-thousand-event queue, two-hundred-event
// batches, one-second flush cadence, and a cancellable background lifetime.
// Only the first optional configuration is used. Call Start separately before
// relying on processing; nil repository prevents startup but does not reject
// direct admission by itself.
func NewOpsSystemLogSink(opsRepo SystemLogWriter, options ...SystemLogSinkOptions) *OpsSystemLogSink {
	ctx, cancel := context.WithCancel(context.Background())
	option := SystemLogSinkOptions{}
	if len(options) > 0 {
		option = options[0]
	}
	s := &OpsSystemLogSink{
		opsRepo:  opsRepo,
		stopDone: make(chan struct{}), onWriteFailure: option.OnWriteFailure,
		host:            normalizeSystemLogHost(option.Host, option.HostError),
		queue:           make(chan *logevent.LogEvent, 5000),
		batchSize:       200,
		flushInterval:   time.Second,
		flushBackoff:    defaultOpsSystemLogFlushBackoff,
		flushBackoffMax: defaultOpsSystemLogFlushBackoffMax,
		ctx:             ctx,
		cancel:          cancel,
	}
	s.lastError.Store("")
	return s
}

// flushBackoffFor returns a doubling suppression delay for consecutive
// attempted failures, with nonpositive failure count using the base.
// Nonpositive base or maximum selects the corresponding constructor default,
// and a maximum below the base is raised to it.
//
// The loop stops once it reaches or crosses that selected maximum, then caps an
// overshooting step. Default two-second/sixty-second bounds keep these
// multiplication steps in range. Arbitrary large private overrides receive no
// overflow check, so callers keep duration doubling within supported integer
// arithmetic rather than inferring universal overflow protection.
func (s *OpsSystemLogSink) flushBackoffFor(failures int) time.Duration {
	base := s.flushBackoff
	if base <= 0 {
		base = defaultOpsSystemLogFlushBackoff
	}
	maxBackoff := s.flushBackoffMax
	if maxBackoff <= 0 {
		maxBackoff = defaultOpsSystemLogFlushBackoffMax
	}
	if maxBackoff < base {
		maxBackoff = base
	}
	// Double only while below the selected maximum, then cap the final step.
	// Current small defaults avoid duration overflow; private overrides need
	// supported arithmetic bounds.
	backoff := base
	for i := 1; i < failures && backoff < maxBackoff; i++ {
		backoff *= 2
	}
	if backoff > maxBackoff {
		backoff = maxBackoff
	}
	return backoff
}

// normalizeSystemLogHost trims supplied host text and selects unknown for any
// lookup error or empty result. Oversized host text keeps at most 255 runes to
// fit the database column; fitting malformed UTF-8 stays supplied, while rune
// reconstruction on truncation repairs invalid sequences.
func normalizeSystemLogHost(host string, err error) string {
	host = strings.TrimSpace(host)
	// Use a stable host label when lookup fails or trims empty, so accepted logs
	// retain this indexed dimension instead of a partial lookup result.
	if err != nil || host == "" {
		return "unknown"
	}
	runes := []rune(host)
	// Count Unicode runes to match the database VARCHAR bound; rebuilding the
	// shortened prefix can replace malformed bytes with valid replacement runes.
	if len(runes) > maxSystemLogHostLength {
		return string(runes[:maxSystemLogHostLength])
	}
	return host
}

// Start registers one writer unless service/storage is absent or
// startup/shutdown already occurred. The lifecycle write lock publishes started
// and adds the WaitGroup before another Stop can seal admission and join that
// work.
//
// Repeated starts share one writer and its failure state. A stopped constructed
// sink cannot reopen; events accepted before successful startup can remain
// buffered if no writer ever runs, so install the sink after Start when drain
// participation matters. Pacing and callbacks stay configured before work
// begins.
func (s *OpsSystemLogSink) Start() {
	if s == nil || s.opsRepo == nil {
		return
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.started || s.stopped {
		return
	}
	// Publish startup and register the writer under the same lock that seals
	// admission. Stop's join then includes every writer accepted by Start.
	s.started = true
	s.wg.Add(1)
	go s.run()
}

// Stop waits for the constructed sink's StopContext with a background context
// and discards its returned error. This wrapper has no caller deadline;
// completion reports joined work rather than guaranteed log persistence.
func (s *OpsSystemLogSink) Stop() { _ = s.StopContext(context.Background()) }

// StopContext permanently seals a constructed sink, cancels its worker context,
// and waits for the shared join result or supplied nonnil context. Nil service
// succeeds. Setup acquires the lifecycle lock inside sync.Once before the
// caller select, so a timeout does not bound that setup wait.
//
// The queue remains open, active inherited writes can cancel, and the worker
// drains with background budgets while retaining suppression/drop behavior. If
// no writer started, buffered accepted events are outside the join. Repeated
// callers share stopDone; when done and caller cancellation are ready together,
// either select result can win. A timeout leaves shutdown in progress.
func (s *OpsSystemLogSink) StopContext(ctx context.Context) error {
	if s == nil {
		return nil
	}
	// Seal admission and cancel active work before publishing one eventual join.
	// The caller's context controls the later select, while lock acquisition
	// precedes that wait.
	s.stopOnce.Do(func() {
		s.lifecycleMu.Lock()
		s.stopped = true
		s.cancel()
		s.lifecycleMu.Unlock()
		go func() { s.wg.Wait(); close(s.stopDone) }()
	})
	select {
	case <-s.stopDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// WriteLogEvent selects eligible observations and tries a queue send under the
// admission read lock. Nil/unselected events and stopped/canceled sinks are
// skipped; a full queue increments dropped. Events may be accepted before Start
// and retain their original pointer plus reachable values.
//
// Producers keep that event and mutable fields stable after submission until
// processing completes. The channel send does not wait for capacity, while
// filtering can invoke a supplied Stringer and acquiring the read lock can wait
// behind shutdown. Those operations are separate from queue-full dropping and
// are not a universal nonblocking logging guarantee.
func (s *OpsSystemLogSink) WriteLogEvent(event *logevent.LogEvent) {
	if s == nil || event == nil || !s.shouldIndex(event) {
		return
	}
	s.lifecycleMu.RLock()
	defer s.lifecycleMu.RUnlock()
	if s.stopped {
		return
	}
	if s.ctx != nil {
		select {
		case <-s.ctx.Done():
			return
		default:
		}
	}

	// Retain this event pointer only if a queue slot is immediately available. The
	// admission lock keeps stopped-state publication ahead of future sends.
	select {
	case s.queue <- event:
	default:
		atomic.AddUint64(&s.droppedCount, 1)
	}
}

// shouldIndex tests a nonnil event for indexing, with a concrete true boolean
// skip marker overriding all other eligibility. Warning/error/fatal/panic
// variants match directly; other levels require a component containing
// http.access or audit.
//
// For those lower levels, a nonempty string or Stringer component attribute
// overrides event.Component after trimming and case folding. Selection can
// invoke that Stringer but performs no persistence, and a selected fatal event
// still relies on later worker lifetime for delivery.
func (s *OpsSystemLogSink) shouldIndex(event *logevent.LogEvent) bool {
	if event != nil && event.Fields != nil {
		if skip, _ := event.Fields[logevent.OpsSystemLogSkipField].(bool); skip {
			return false
		}
	}
	level := strings.ToLower(strings.TrimSpace(event.Level))
	switch level {
	case "warn", "warning", "error", "fatal", "panic", "dpanic":
		return true
	}

	component := strings.ToLower(strings.TrimSpace(event.Component))
	// Prefer a usable component attribute for access/audit classification so
	// accepted structured logger names can still point to the concrete event
	// category.
	if event.Fields != nil {
		if fc := strings.ToLower(strings.TrimSpace(AsString(event.Fields["component"]))); fc != "" {
			component = fc
		}
	}
	if strings.Contains(component, "http.access") {
		return true
	}
	if strings.Contains(component, "audit") {
		return true
	}
	return false
}

// run processes queued event pointers until worker cancellation, then drains
// available entries and flushes remaining batches. Start registers this single
// worker and keeps queue/batch/pacing fields stable. Local failure count and
// suppression timestamp are owned here, so repeated Start cannot create another
// independent retry state.
//
// Attempt failure increments failed-entry health, stores error text, reports
// synchronously, and discards the batch. Suppressed batches increment dropped
// without touching storage or failure streak; successful attempts clear
// suppression/error and add reported rows plus one attempt duration.
// Cancellation switches draining to background contexts but does not bypass
// suppression, so joining can finish after dropped drain entries. The queue
// stays open and producers are sealed separately.
func (s *OpsSystemLogSink) run() {
	defer s.wg.Done()

	ticker := time.NewTicker(s.flushInterval)
	// Stop the flush ticker when this registered writer exits. Tick delivery can
	// coalesce while preparation, repository work, or failure reporting blocks the
	// worker.
	defer ticker.Stop()

	batch := make([]*logevent.LogEvent, 0, s.batchSize)
	// Keep suppression and the attempted-failure streak on this single writer.
	// Dropping a batch during the delay does not create another retry state or
	// advance the streak.
	failures := 0
	var suppressedUntil time.Time
	flush := func(baseCtx context.Context) {
		if len(batch) == 0 {
			return
		}
		now := time.Now()
		if now.Before(suppressedUntil) {
			// Drop batches during backoff to bound memory and avoid repeated database
			// attempts consuming pool connections during an outage.
			atomic.AddUint64(&s.droppedCount, uint64(len(batch)))
			batch = batch[:0]
			return
		}
		started := time.Now()
		inserted, err := s.flushBatch(baseCtx, batch)
		delay := time.Since(started)
		if err != nil {
			failures++
			backoff := s.flushBackoffFor(failures)
			suppressedUntil = time.Now().Add(backoff)
			atomic.AddUint64(&s.writeFailed, uint64(len(batch)))
			s.lastError.Store(err.Error())
			// Report only attempted failures after their next delay is installed.
			// Suppressed batches do not invoke the callback, which runs synchronously
			// before clearing this failed batch.
			if s.onWriteFailure != nil {
				s.onWriteFailure(err, len(batch), failures, backoff)
			}
		} else {
			failures = 0
			suppressedUntil = time.Time{}
			atomic.AddUint64(&s.writtenCount, uint64(inserted))
			atomic.AddUint64(&s.totalDelayNs, uint64(delay.Nanoseconds()))
			s.lastError.Store("")
		}
		batch = batch[:0]
	}
	// Drain after producers are sealed, using independent background write
	// budgets. The same flush closure still drops batches during suppression, so
	// drainage does not guarantee delivery.
	drainAndFlush := func() {
		for {
			select {
			case item := <-s.queue:
				if item == nil {
					continue
				}
				batch = append(batch, item)
				if len(batch) >= s.batchSize {
					flush(context.Background())
				}
			default:
				flush(context.Background())
				return
			}
		}
	}

	for {
		select {
		case <-s.ctx.Done():
			drainAndFlush()
			return
		case item := <-s.queue:
			if item == nil {
				continue
			}
			batch = append(batch, item)
			if len(batch) >= s.batchSize {
				flush(s.ctx)
			}
		case <-ticker.C:
			flush(s.ctx)
		}
	}
}

// flushBatch converts stable event observations into stored rows and submits
// them once through the configured writer. Nil members are skipped, timestamps
// become UTC with now for zero, and selected identifiers are extracted before
// metadata redaction. Nonempty component attributes override the event
// component, while blank final component selects app.
//
// The top-level attribute map is copied, then recognized message/metadata forms
// are redacted. Metadata marshal error is suppressed into an empty object,
// retaining indexed scalar fields. Missing or already canceled base context
// selects background; otherwise the five-second child inherits active
// cancellation. Repository error returns zero here even if storage reports
// partial attempts, while success narrows its reported count unchecked to int.
// This method adds no replay and does not freeze original input events.
func (s *OpsSystemLogSink) flushBatch(baseCtx context.Context, batch []*logevent.LogEvent) (int, error) {
	inputs := make([]*OpsInsertSystemLogInput, 0, len(batch))
	for _, event := range batch {
		if event == nil {
			continue
		}
		createdAt := event.Time.UTC()
		if createdAt.IsZero() {
			createdAt = time.Now().UTC()
		}

		// Read correlation/identity scalars from a shallow attribute copy before
		// redaction. Values used by conversion and nested metadata remain stable by
		// producer ownership until encoding finishes.
		fields := CopyMap(event.Fields)
		requestID := AsString(fields["request_id"])
		clientRequestID := AsString(fields["client_request_id"])
		platform := AsString(fields["platform"])
		model := AsString(fields["model"])
		component := strings.TrimSpace(event.Component)
		if fieldComponent := AsString(fields["component"]); fieldComponent != "" {
			component = fieldComponent
		}
		if component == "" {
			component = "app"
		}

		userID := AsInt64Ptr(fields["user_id"])
		apiKeyID := AsInt64Ptr(fields["api_key_id"])
		providerID := AsInt64Ptr(fields["provider_id"])

		// Redact supported message and metadata forms before indexing. Suppress
		// metadata encoding failure into an empty object so selected scalar
		// diagnostics can still be stored.
		message := logredact.RedactText(strings.TrimSpace(event.Message))
		redactedExtra := logredact.RedactMap(fields)
		extraJSONBytes, _ := json.Marshal(redactedExtra)
		extraJSON := string(extraJSONBytes)
		if strings.TrimSpace(extraJSON) == "" {
			extraJSON = "{}"
		}

		inputs = append(inputs, &OpsInsertSystemLogInput{
			CreatedAt:       createdAt,
			Host:            s.host,
			Level:           strings.ToLower(strings.TrimSpace(event.Level)),
			Component:       component,
			Message:         message,
			RequestID:       requestID,
			ClientRequestID: clientRequestID,
			UserID:          userID,
			APIKeyID:        apiKeyID,
			ProviderID:      providerID,
			Platform:        platform,
			Model:           model,
			ExtraJSON:       extraJSON,
		})
	}

	if len(inputs) == 0 {
		return 0, nil
	}
	// A canceled active context cannot serve drain work, so replace it with
	// background before creating this write budget. Already created active
	// children still inherit cancellation.
	if baseCtx == nil || baseCtx.Err() != nil {
		baseCtx = context.Background()
	}
	ctx, cancel := context.WithTimeout(baseCtx, 5*time.Second)
	defer cancel()
	inserted, err := s.opsRepo.BatchInsertSystemLogs(ctx, inputs)
	if err != nil {
		return 0, err
	}
	return int(inserted), nil
}

// Health returns separately sampled queue and attempt counters, with zero
// output for nil service. Average delay divides successful flush nanoseconds by
// reported written rows and then truncates milliseconds; no rows yields zero,
// and queue waiting is excluded.
//
// Counters and last error can change between loads, so the result does not
// establish an atomic complete run. Failed attempted batches count under
// WriteFailed rather than DroppedCount, suppressed/full-channel events count as
// dropped, and skipped filtering or stopped admission increments neither
// category.
func (s *OpsSystemLogSink) Health() OpsSystemLogSinkHealth {
	if s == nil {
		return OpsSystemLogSinkHealth{}
	}
	written := atomic.LoadUint64(&s.writtenCount)
	totalDelay := atomic.LoadUint64(&s.totalDelayNs)
	var avgDelay uint64
	// One duration is added per successful flush but divided by reported row
	// count, then converted to integer milliseconds; this excludes time waiting in
	// the queue.
	if written > 0 {
		avgDelay = (totalDelay / written) / uint64(time.Millisecond)
	}

	lastErr, _ := s.lastError.Load().(string)
	return OpsSystemLogSinkHealth{
		QueueDepth:      int64(len(s.queue)),
		QueueCapacity:   int64(cap(s.queue)),
		DroppedCount:    atomic.LoadUint64(&s.droppedCount),
		WriteFailed:     atomic.LoadUint64(&s.writeFailed),
		WrittenCount:    written,
		AvgWriteDelayMs: avgDelay,
		LastError:       strings.TrimSpace(lastErr),
	}
}

// CopyMap allocates a new top-level attribute map, with empty output for nil or
// empty input. Values are assigned shallowly, so nested mutable values remain
// shared and callers keep them stable during later redaction/encoding.
func CopyMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(in))
	maps.Copy(out, in)
	return out
}

// AsString trims a concrete string or the result of fmt.Stringer, returning
// empty for other types. It invokes supplied Stringer methods synchronously;
// error-only values and separately defined strings without that method are not
// converted.
func AsString(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case fmt.Stringer:
		return strings.TrimSpace(t.String())
	default:
		return ""
	}
}

// AsInt64Ptr returns a new scalar pointer for supported positive identity
// values, otherwise nil. Supported inputs are concrete int/int64/float64,
// integer json.Number text, and trimmed base-ten string text. Parse errors and
// nonpositive converted values are suppressed into absence.
//
// Float conversion truncates fractions and has no explicit finite/range check,
// while large floating-point identities may already have lost precision.
// json.Number requires integer spelling, so decimal/exponent text is not
// accepted through that representation. Producers use exact integer or string
// identities when association must survive without floating-point rounding;
// unsupported pointer or named numeric forms remain absent.
func AsInt64Ptr(v any) *int64 {
	switch t := v.(type) {
	case int:
		n := int64(t)
		if n <= 0 {
			return nil
		}
		return &n
	case int64:
		n := t
		if n <= 0 {
			return nil
		}
		return &n
	// Float identities can already be rounded by their producer. Truncating to
	// int64 does not restore lost precision, and only positive converted results
	// survive.
	case float64:
		n := int64(t)
		if n <= 0 {
			return nil
		}
		return &n
	// Only successful integer parsing supplies an association. Failed JSON-number
	// or base-ten string parsing falls through to absence before a row is indexed.
	case json.Number:
		if n, err := t.Int64(); err == nil {
			if n <= 0 {
				return nil
			}
			return &n
		}
	case string:
		raw := strings.TrimSpace(t)
		if raw == "" {
			return nil
		}
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
			if n <= 0 {
				return nil
			}
			return &n
		}
	}
	return nil
}
