package provider

import (
	"context"
	"net/http"

	"github.com/TokenFlux/TokenRouter/internal/provider"
)

type anthropicTestTarget struct {
	executor *AnthropicProviderTest
	record   *provider.Record
}

// Target exposes probe information while keeping credentials in the execution handle.
func (s *AnthropicProviderTest) Target(value *provider.Record) provider.TestTarget {
	return anthropicTestTarget{executor: s, record: value}
}

func (t anthropicTestTarget) Information() provider.TestTargetInfo {
	return provider.TestTargetInfo{ProviderSnapshot: t.record.RoutingSnapshot(), APIProtocol: (provider.ProtocolTarget{Record: t.record}).GetAPIProtocol()}
}

func (t anthropicTestTarget) Execute(ctx context.Context, request provider.PreparedTestRequest, sink provider.TestEventSink) error {
	headers := make(http.Header)
	headers.Set("User-Agent", request.UserAgent)
	run := NewTestRun(ctx, headers, sink)
	defer run.Cancel()
	// Keep the automatic probe User-Agent local to this execution.
	executor := *t.executor
	if request.Automatic {
		executor.UserAgent = request.UserAgent
	}
	return run.Result(executor.Execute(run, t.record, request.Model, request.Prompt))
}
