package timing

import (
	"context"
	"net/http/httptrace"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
)

// httptrace.go attaches generation-exchange callbacks to the request collector.
// Each outgoing request receives an attempt ordinal before connection acquisition.
// The callbacks carry that ordinal, so delayed work from an abandoned connection
// cannot replace the next provider's observations. Requests without the collector
// keep their existing transport behavior and do not allocate performance state.
//
// A retry retains the prior attempt's bounded failure-location diagnostics in
// the request record, while the current exchange writes directly into its sole
// collector. Connection callbacks neither retain a second timestamp snapshot nor
// publish performance through access logs. The completed usage row receives a
// frozen copy after response reading and existing downstream writes finish.

// BeginHTTPTrace registers one actual exchange and binds its transport observations.
// Call immediately before dispatch; request setup supplies provider observation policy.
// Existing unrelated trace callbacks are composed by net/http, but performance writes
// accept only the current ordinal. Repeated dispatch starts another isolated exchange
// and preserves the previous attempt's diagnostic identity without joining timestamps
// across providers. Nil context uses a background context with no performance observer.
func BeginHTTPTrace(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	stages := StagesFromContext(ctx)
	if stages == nil {
		return telemetry.BeginRequestAttempt(ctx, 0)
	}
	previousNumber := stages.Attempt()
	previousFailure := stages.AttemptFailureSnapshot()
	number := stages.BeginAttempt()
	telemetry.UpdateRequest(ctx, func(record *telemetry.RequestRecord) {
		usageOwned := record.UsageAttempt == previousNumber && (record.UsageWriteState == "pending" || record.UsageWriteState == "persisted")
		// Completed or usage-owned exchanges retain their identity and outcome.
		// Only an abandoned dispatch needs stage evidence before the collector begins another exchange.
		for i := range record.Attempts {
			attempt := &record.Attempts[i]
			if previousNumber == 0 || int64(attempt.Number) != previousNumber || attempt.Outcome == "completed" || usageOwned {
				continue
			}
			attempt.FailureTimings = previousFailure
			if start, ok := previousFailure["upstream_started_ms"]; ok {
				if end, known := previousFailure["failed_at_ms"]; known && end >= start {
					duration := end - start
					attempt.DurationMs = &duration
				}
			}
			if attempt.Outcome == "running" {
				attempt.Outcome = "failed"
			}
		}
	})
	ctx = telemetry.BeginRequestAttempt(ctx, int(number))
	trace := &httptrace.ClientTrace{
		GetConn: func(string) { stages.Transport(number, "upstream_get_conn_ms", time.Now(), false) },
		GotConn: func(info httptrace.GotConnInfo) {
			stages.Transport(number, "upstream_got_conn_ms", time.Now(), info.Reused)
		},
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			stages.Transport(number, "upstream_wrote_request_ms", time.Now(), info.Err != nil)
		},
		GotFirstResponseByte: func() { stages.Transport(number, "upstream_first_response_byte_ms", time.Now(), false) },
	}
	return httptrace.WithClientTrace(ctx, trace)
}
