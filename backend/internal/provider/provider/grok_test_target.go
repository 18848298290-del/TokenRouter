package provider

import (
	"context"
	"net/http"

	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// grok_test_target.go binds a selected Grok provider to a synchronous probe
// with its group and request identity headers. TestRun records event-sink
// failures and cancels execution after an output failure; the target returns that recorded
// error together with the probe result and releases its run context.

// grokTestTarget borrows the selected provider and its Grok probe event executor.
type grokTestTarget struct {
	// executor and record are borrowed for header preparation, quota writes and
	// event output. Their owners retain them across handle calls and serialize
	// external mutation with Information and synchronous Execute.
	executor *GrokProviderTest // selected probe event adapter
	record   *provider.Record  // borrowed selected provider
}

// Target binds one provider record to test information and Grok execution.
// The handle borrows the executor and record rather than copying their
// dependencies.
// Callers retain them through the synchronous probe and serialize any selected
// record mutations.
// Supply a nonnil record for information and execution.
func (s *GrokProviderTest) Target(value *provider.Record) provider.TestTarget {
	return grokTestTarget{executor: s, record: value}
}

// Information reports the provider's routing snapshot together with its
// inferred API protocol.
func (t grokTestTarget) Information() provider.TestTargetInfo {
	return provider.TestTargetInfo{ProviderSnapshot: t.record.RoutingSnapshot(), APIProtocol: (provider.ProtocolTarget{Record: t.record}).GetAPIProtocol()}
}

// Execute runs one Grok probe with its prompt and optional test type.
// It creates a TestRun carrying the group, User-Agent, and originator and returns
// its combined execution/sink result. The run context is cancelled on every
// return from this target.
func (t grokTestTarget) Execute(ctx context.Context, request provider.PreparedTestRequest, sink provider.TestEventSink) error {
	headers := make(http.Header)
	headers.Set("User-Agent", request.UserAgent)
	headers.Set("originator", request.Originator)
	run := NewTestRun(ctx, headers, sink)
	run.GroupID = request.GroupID
	defer run.Cancel()
	return run.Result(t.executor.Execute(run, t.record, request.Model, request.Prompt, request.TestType))
}
