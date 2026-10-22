package provider

import (
	"context"
	"net/http"

	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// qoder_test_target.go binds a selected Qoder provider to a TestRun that emits
// typed probe results. Request User-Agent and originator headers accompany
// that run, while the executor prepares its site session and streaming
// payload; returned errors include event-sink failures after the run context
// is cancelled.

// qoderTestTarget borrows one provider record and its Qoder connection-probe
// executor.
type qoderTestTarget struct {
	// executor and record supply the selected site session and model settings.
	// The handle borrows both across calls; retain their dependencies and
	// serialize external configuration changes with Information and Execute.
	executor *QoderProviderTest // selected probe event adapter
	record   *provider.Record   // borrowed selected provider
}

// Target binds a provider record to Qoder test information and execution.
// The returned target borrows the executor and record. Callers retain their
// dependencies through synchronous execution and serialize mutations of the
// selected provider identity.
// Supply a nonnil record for information and execution.
func (s *QoderProviderTest) Target(value *provider.Record) provider.TestTarget {
	return qoderTestTarget{executor: s, record: value}
}

// Information returns the selected provider's routing snapshot and inferred
// API protocol.
func (t qoderTestTarget) Information() provider.TestTargetInfo {
	return provider.TestTargetInfo{ProviderSnapshot: t.record.RoutingSnapshot(), APIProtocol: (provider.ProtocolTarget{Record: t.record}).GetAPIProtocol()}
}

// Execute runs the selected Qoder probe with the supplied request metadata.
// It creates a TestRun carrying User-Agent and originator headers, then returns
// the combined probe and sink result. The run context is cancelled on return.
func (t qoderTestTarget) Execute(ctx context.Context, request provider.PreparedTestRequest, sink provider.TestEventSink) error {
	headers := make(http.Header)
	headers.Set("User-Agent", request.UserAgent)
	headers.Set("originator", request.Originator)
	run := NewTestRun(ctx, headers, sink)
	run.GroupID = request.GroupID
	defer run.Cancel()
	return run.Result(t.executor.Execute(run, t.record, request.Model, request.Prompt))
}
