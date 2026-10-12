package provider

import (
	"context"
	"net/http"

	"github.com/TokenFlux/TokenRouter/internal/provider"
)

type cnTestTarget struct {
	executor *CNProviderTest
	record   *provider.Record
}

// Target binds the selected provider record to the prepared test route.
func (s *CNProviderTest) Target(value *provider.Record) provider.TestTarget {
	return cnTestTarget{executor: s, record: value}
}

func (t cnTestTarget) Information() provider.TestTargetInfo {
	return provider.TestTargetInfo{ProviderSnapshot: t.record.RoutingSnapshot(), APIProtocol: (provider.ProtocolTarget{Record: t.record}).GetAPIProtocol()}
}

func (t cnTestTarget) Execute(ctx context.Context, request provider.PreparedTestRequest, sink provider.TestEventSink) error {
	run := NewTestRun(ctx, make(http.Header), sink)
	run.Automatic = request.Automatic
	run.Headers.Set("User-Agent", request.UserAgent)
	defer run.Cancel()
	var err error
	switch request.Route {
	case provider.TestRouteCNAdaptive:
		// An explicit protocol selects one endpoint; otherwise all enabled endpoints are probed.
		err = t.executor.ExecuteAdaptive(run, t.record, request.Model, request.Prompt, provider.TestProtocolID(request.Protocol))
	case provider.TestRouteCNResponses:
		err = t.executor.Responses.Execute(run, t.record, request.Model, request.Prompt, request.Mode, request.TestType)
	default:
		err = t.executor.Execute(run, t.record, request.Model, request.Prompt)
	}
	return run.Result(err)
}
