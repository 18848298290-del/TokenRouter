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

const (
	// maxSystemLogHostLength matches the database column to prevent an oversized hostname from failing a batch.
	maxSystemLogHostLength = 255

	// Pause after the first write failure, then double the delay up to the cap.
	defaultOpsSystemLogFlushBackoff = 2 * time.Second
	// Cap retry delay so logging can resume while limiting pressure on the connection pool.
	defaultOpsSystemLogFlushBackoffMax = 60 * time.Second
)

type OpsSystemLogSinkHealth struct {
	QueueDepth      int64  `json:"queue_depth"`
	QueueCapacity   int64  `json:"queue_capacity"`
	DroppedCount    uint64 `json:"dropped_count"`
	WriteFailed     uint64 `json:"write_failed_count"`
	WrittenCount    uint64 `json:"written_count"`
	AvgWriteDelayMs uint64 `json:"avg_write_delay_ms"`
	LastError       string `json:"last_error"`
}

type OpsSystemLogSink struct {
	opsRepo SystemLogWriter
	// host identifies the process host in stored system logs.
	host string

	queue            chan *logevent.LogEvent
	lifecycleMu      sync.RWMutex
	started, stopped bool
	stopOnce         sync.Once
	stopDone         chan struct{}
	onWriteFailure   func(error, int, int, time.Duration)

	batchSize     int
	flushInterval time.Duration

	// Backoff settings are read-only after construction; tests may override them before Start.
	flushBackoff    time.Duration
	flushBackoffMax time.Duration

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	droppedCount uint64
	writeFailed  uint64
	writtenCount uint64
	totalDelayNs uint64

	lastError atomic.Value
}

// SystemLogWriter stores batches consumed by the log queue.
type SystemLogWriter interface {
	BatchInsertSystemLogs(context.Context, []*OpsInsertSystemLogInput) (int64, error)
}

// SystemLogSinkOptions supplies host metadata and the failure callback from app adapters.
type SystemLogSinkOptions struct {
	Host           string
	HostError      error
	OnWriteFailure func(error, int, int, time.Duration)
}

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

// flushBackoffFor returns capped exponential backoff after consecutive failures.
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
	backoff := base
	for i := 1; i < failures && backoff < maxBackoff; i++ {
		backoff *= 2
	}
	if backoff > maxBackoff {
		backoff = maxBackoff
	}
	return backoff
}

// normalizeSystemLogHost trims and bounds the hostname, using unknown on error or empty input.
func normalizeSystemLogHost(host string, err error) string {
	host = strings.TrimSpace(host)
	if err != nil || host == "" {
		return "unknown"
	}
	runes := []rune(host)
	if len(runes) > maxSystemLogHostLength {
		return string(runes[:maxSystemLogHostLength])
	}
	return host
}

func (s *OpsSystemLogSink) Start() {
	if s == nil || s.opsRepo == nil {
		return
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.started || s.stopped {
		return
	}
	s.started = true
	s.wg.Add(1)
	go s.run()
}

func (s *OpsSystemLogSink) Stop() { _ = s.StopContext(context.Background()) }

// StopContext closes admission and waits for the writer; a timeout leaves shutdown in progress.
func (s *OpsSystemLogSink) StopContext(ctx context.Context) error {
	if s == nil {
		return nil
	}
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

	select {
	case s.queue <- event:
	default:
		atomic.AddUint64(&s.droppedCount, 1)
	}
}

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
	// The component field can carry the business component when the logger name is empty or different.
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

func (s *OpsSystemLogSink) run() {
	defer s.wg.Done()

	ticker := time.NewTicker(s.flushInterval)
	defer ticker.Stop()

	batch := make([]*logevent.LogEvent, 0, s.batchSize)
	// The writer goroutine owns this retry state.
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
			// Report at most once per backoff window to limit outage log volume.
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

		// Redact the message and fields before indexing.
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

func (s *OpsSystemLogSink) Health() OpsSystemLogSinkHealth {
	if s == nil {
		return OpsSystemLogSinkHealth{}
	}
	written := atomic.LoadUint64(&s.writtenCount)
	totalDelay := atomic.LoadUint64(&s.totalDelayNs)
	var avgDelay uint64
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

func CopyMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(in))
	maps.Copy(out, in)
	return out
}

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
	case float64:
		n := int64(t)
		if n <= 0 {
			return nil
		}
		return &n
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
