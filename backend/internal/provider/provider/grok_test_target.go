package provider

import (
	"context"
	"net/http"

	"github.com/TokenFlux/TokenRouter/internal/provider"
)

type grokTestTarget struct {
	executor *GrokProviderTest
	record   *provider.Record
}

// Target binds the selected record to a probe whose public information omits credentials.
func (s *GrokProviderTest) Target(value *provider.Record) provider.TestTarget {
	return grokTestTarget{executor: s, record: value}
}

func (t grokTestTarget) Information() provider.TestTargetInfo {
	return provider.TestTargetInfo{ProviderSnapshot: t.record.RoutingSnapshot(), APIProtocol: (provider.ProtocolTarget{Record: t.record}).GetAPIProtocol()}
}

func (t grokTestTarget) Execute(ctx context.Context, request provider.PreparedTestRequest, sink provider.TestEventSink) error {
	headers := make(http.Header)
	headers.Set("User-Agent", request.UserAgent)
	headers.Set("originator", request.Originator)
	run := NewTestRun(ctx, headers, sink)
	defer run.Cancel()
	return run.Result(t.executor.Execute(run, t.record, request.Model, request.Prompt, request.TestType))
}
