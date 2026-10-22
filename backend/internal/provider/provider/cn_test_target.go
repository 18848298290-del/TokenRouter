package provider

import (
	"context"
	"net/http"

	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// cn_test_target.go dispatches prepared CN probes to adaptive coverage, shared
// Responses execution, or the fixed route. Every path receives one run
// carrying its group, automatic flag, and User-Agent, and the target combines platform
// failures with retained sink errors before returning.

// cnTestTarget retains CN probe dependencies and the record selected for
// execution.
type cnTestTarget struct {
	executor *CNProviderTest // shared CN probe dependencies
	// record retains the provider's authentication and selected protocols. Keep it
	// stable until the prepared probe and information reads finish.
	record *provider.Record
}

// Target binds the selected record to this CN executor and returns a prepared
// test handle. The handle retains the record, so callers keep its credentials
// and protocol configuration stable during execution.
func (s *CNProviderTest) Target(value *provider.Record) provider.TestTarget {
	return cnTestTarget{executor: s, record: value}
}

// Information returns the CN record's routing snapshot and configured API
// protocol.
func (t cnTestTarget) Information() provider.TestTargetInfo {
	return provider.TestTargetInfo{ProviderSnapshot: t.record.RoutingSnapshot(), APIProtocol: (provider.ProtocolTarget{Record: t.record}).GetAPIProtocol()}
}

// Execute routes the prepared CN request through adaptive, shared Responses, or
// fixed execution. An explicit adaptive protocol narrows coverage; the run
// carries automatic identity and returns platform and retained sink failures
// together. A routed group probe executes only its resolved upstream protocol,
// regardless of the record's inferred standalone test family.
func (t cnTestTarget) Execute(ctx context.Context, request provider.PreparedTestRequest, sink provider.TestEventSink) error {
	run := NewTestRun(ctx, make(http.Header), sink)
	run.GroupID = request.GroupID
	run.Automatic = request.Automatic
	run.Headers.Set("User-Agent", request.UserAgent)
	defer run.Cancel()
	var err error
	if request.ProtocolRoute.UpstreamProtocol != "" {
		// Group routing already chose one enabled endpoint. Narrow adaptive execution
		// to that endpoint so the record's other protocols cannot add requests.
		return run.Result(t.executor.ExecuteAdaptive(run, t.record, request.Model, request.Prompt, request.ProtocolRoute.UpstreamProtocol))
	}
	switch request.Route {
	case provider.TestRouteCNAdaptive:
		// Adaptive preparation distinguishes an omitted protocol from a selected one.
		// Pass that selection through so the executor can reject disabled protocols
		// before any upstream request.
		err = t.executor.ExecuteAdaptive(run, t.record, request.Model, request.Prompt, provider.TestProtocolID(request.Protocol))
	case provider.TestRouteCNResponses:
		err = t.executor.Responses.Execute(run, t.record, request.Model, request.Prompt, request.Mode, request.TestType)
	default:
		err = t.executor.Execute(run, t.record, request.Model, request.Prompt)
	}
	return run.Result(err)
}
