package provider

import (
	"context"
	"net/http"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/antigravity"
)

// AntigravityProviderTest reports OAuth probe results as test events.
type AntigravityProviderTest struct {
	Probe func(context.Context, *provider.Record, provider.PreparedTestRequest) (*antigravity.TestConnectionResult, error)
}

type antigravityTestTarget struct {
	executor *AntigravityProviderTest
	record   *provider.Record
}

func (s *AntigravityProviderTest) Target(value *provider.Record) provider.TestTarget {
	return antigravityTestTarget{executor: s, record: value}
}

func (t antigravityTestTarget) Information() provider.TestTargetInfo {
	return provider.TestTargetInfo{ProviderSnapshot: t.record.RoutingSnapshot(), APIProtocol: (provider.ProtocolTarget{Record: t.record}).GetAPIProtocol()}
}

func (t antigravityTestTarget) Execute(ctx context.Context, request provider.PreparedTestRequest, sink provider.TestEventSink) error {
	image, explicit := provider.ProviderTestTypeFromArgs(request.TestType)
	run := NewTestRun(ctx, make(http.Header), sink)
	defer run.Cancel()
	if explicit && image == provider.ProviderTestTypeImage {
		return run.Result((TestStreamOutput{}).Error(run, "Image tests are not supported for this Antigravity provider type"))
	}
	if request.Model == "" {
		request.Model = "claude-sonnet-4-5"
	}
	if t.executor.Probe == nil {
		return run.Result((TestStreamOutput{}).Error(run, "Antigravity gateway service not configured"))
	}
	run.Begin(true)
	(TestStreamOutput{}).SendEvent(run, provider.TestEvent{Type: "test_start", Model: request.Model})
	result, err := t.executor.Probe(run.Context, t.record, request)
	if err != nil {
		return run.Result((TestStreamOutput{}).Error(run, err.Error()))
	}
	if result.Text != "" {
		(TestStreamOutput{}).SendEvent(run, provider.TestEvent{Type: "content", Text: result.Text})
	}
	(TestStreamOutput{}).SendEvent(run, provider.TestEvent{Type: "test_complete", Success: true})
	return run.Result(nil)
}
