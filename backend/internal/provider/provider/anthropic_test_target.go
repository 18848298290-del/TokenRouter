package provider

import (
	"context"
	"net/http"

	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// anthropic_test_target.go binds a selected provider and prepared test request
// to the Anthropic probe executor. Automatic Anthropic probes carry a
// User-Agent selected for that execution. The target copies its executor value
// before installing that header policy, so platform dependencies remain shared
// while each probe can use its own agent string and cancellation context.

// anthropicTestTarget retains an Anthropic executor and record for a prepared
// provider probe.
type anthropicTestTarget struct {
	executor *AnthropicProviderTest // shared Anthropic probe dependencies
	// record retains the probe's credentials and route. The caller keeps this
	// record stable until Information and Execute finish.
	record *provider.Record
}

// Target binds the supplied record to this Anthropic executor and returns a
// test handle. Keep the record stable through Information and Execute, because
// the handle retains its credentials and routing configuration.
func (s *AnthropicProviderTest) Target(value *provider.Record) provider.TestTarget {
	return anthropicTestTarget{executor: s, record: value}
}

// Information returns routing and configured-protocol information for the
// retained Anthropic record.
func (t anthropicTestTarget) Information() provider.TestTargetInfo {
	return provider.TestTargetInfo{ProviderSnapshot: t.record.RoutingSnapshot(), APIProtocol: (provider.ProtocolTarget{Record: t.record}).GetAPIProtocol()}
}

// Execute runs the prepared Anthropic probe with a cancellable event run.
// Automatic requests apply their User-Agent to an executor copy; Result
// includes any sink error retained while platform execution was running.
func (t anthropicTestTarget) Execute(ctx context.Context, request provider.PreparedTestRequest, sink provider.TestEventSink) error {
	headers := make(http.Header)
	headers.Set("User-Agent", request.UserAgent)
	run := NewTestRun(ctx, headers, sink)
	run.GroupID = request.GroupID
	defer run.Cancel()
	// Copy the executor value before changing its User-Agent. Each automatic probe
	// keeps that string local while token, transport, and store dependencies
	// remain shared.
	executor := *t.executor
	if request.Automatic {
		executor.UserAgent = request.UserAgent
	}
	return run.Result(executor.Execute(run, t.record, request.Model, request.Prompt))
}
