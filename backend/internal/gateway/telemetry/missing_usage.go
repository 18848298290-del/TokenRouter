package telemetry

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// A successful terminal response can carry no positive input, output, or image
// output tokens. Diagnostics count every eligible observation but emit at most
// one sampled message per minute for the process. Cache-only counts do not exclude
// a response from this diagnostic check, and client disconnect evidence is logged
// as context rather than changing eligibility.
//
// The sampler locks its timestamp and counters together. The first observation
// logs immediately; the next interval's accepted observation reports accumulated
// suppression and resets that suppression count. SuccessMissingUsage publishes
// the returned total to a separate atomic diagnostic value after sampling, so
// concurrent callers can publish totals out of observation order.

// openAIMissingUsageLogInterval controls the earliest next sampled diagnostic
// while every eligible observation still increments the sampler count.
const openAIMissingUsageLogInterval = time.Minute

// The process-wide sampler counts eligible responses, and the atomic total
// exposes the latest published sample result to diagnostic readers.
var (
	openAIMissingUsageLogSamplerState openAIMissingUsageLogSampler // process diagnostic sampler
	// openAIMissingUsageTotal is stored after the sampler unlocks; concurrent
	// callers can publish an older returned count after a newer one.
	openAIMissingUsageTotal atomic.Uint64
)

// openAIMissingUsageLogSampler rate-limits missing-usage diagnostics while
// counting all observations.
type openAIMissingUsageLogSampler struct {
	// mu protects lastLogged, total, and suppressed as one sampling decision;
	// an accepted log resets suppression after copying the values it reports.
	mu         sync.Mutex // sampling decision lock
	lastLogged time.Time  // last accepted observation
	total      uint64     // eligible observation count
	suppressed uint64     // observations since emission
}

// sample counts an observation and reports whether it should emit a diagnostic.
// A nil sampler returns an unaccepted zero result. The first observation and one
// at or beyond the next minute are accepted. A supplied zero time keeps
// lastLogged uninitialized, so repeated zero-time observations are accepted.
//
// The mutex keeps timestamp choice, cumulative count, and suppression reset in
// one decision across callers. Accepted results include the suppression accumulated
// before that observation; suppressed results expose the current total and zero
// suppression, leaving the accumulated suppression for the next accepted log.
// Callers provide observation times and can publish returned values after unlocking.
func (s *openAIMissingUsageLogSampler) sample(now time.Time) (logNow bool, total, suppressed uint64) {
	if s == nil {
		return false, 0, 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Count under the same lock as interval selection so each accepted report
	// includes the suppressed observations already recorded before its own decision.
	s.total++
	if s.lastLogged.IsZero() || !now.Before(s.lastLogged.Add(openAIMissingUsageLogInterval)) {
		s.lastLogged = now
		total = s.total
		suppressed = s.suppressed
		s.suppressed = 0
		return true, total, suppressed
	}
	s.suppressed++
	return false, s.total, 0
}

// SuccessMissingUsage samples 2xx terminal responses with no positive input,
// output, or image-output usage. Only response.completed, response.done, json,
// and [DONE] are eligible; nil usage also qualifies and cache counts are ignored.
// Client disconnection is recorded on emitted debug messages and does not change
// eligibility. The process-wide sampler controls frequency across all providers.
func SuccessMissingUsage(ctx context.Context, providerID int64, status int, usage *openai.ForwardUsage, terminalEvent string, clientDisconnected bool) {
	if status < 200 || status >= 300 || usage != nil && (usage.InputTokens > 0 || usage.OutputTokens > 0 || usage.ImageOutputTokens > 0) {
		return
	}
	terminalEvent = strings.TrimSpace(terminalEvent)
	if terminalEvent != "response.completed" && terminalEvent != "response.done" && terminalEvent != "json" && terminalEvent != "[DONE]" {
		return
	}
	logNow, count, suppressed := openAIMissingUsageLogSamplerState.sample(time.Now())
	openAIMissingUsageTotal.Store(count)
	// Suppressed observations still update the process diagnostic count; only an
	// accepted sample formats a log entry with accumulated suppression evidence.
	if !logNow {
		return
	}
	logging.FromContext(ctx).With(
		zap.Int64("provider_id", providerID),
		zap.String("terminal_event", terminalEvent),
		zap.Bool("client_disconnected", clientDisconnected),
		zap.Uint64("missing_usage_total", count),
		zap.Uint64("missing_usage_suppressed", suppressed),
	).Debug("openai_usage.success_missing_usage")
}
