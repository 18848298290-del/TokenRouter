package provider

import (
	"context"
	"net/http"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/antigravity"
)

// Antigravity provider tests report the selected OAuth probe through typed test
// events. Explicit image tests fail before probing; text probes use a default
// model when omitted, publish any returned text, and finish through TestRun so
// sink errors and cancellation are retained for the caller.

// AntigravityProviderTest reports OAuth probe results as test events.
type AntigravityProviderTest struct {
	// Probe executes the selected record with prepared test arguments. A nil
	// callback emits a test error after model defaulting instead of starting a
	// request.
	Probe func(context.Context, *provider.Record, provider.PreparedTestRequest) (*antigravity.TestConnectionResult, error)
}

// antigravityTestTarget retains the selected record and its Antigravity probe
// executor.
type antigravityTestTarget struct {
	// executor and record are borrowed until the OAuth probe returns and event
	// output completes. Their owners retain both across handle calls and
	// serialize external configuration changes with Information and Execute.
	executor *AntigravityProviderTest // bound probe event adapter
	record   *provider.Record         // borrowed selected provider
}

// Target binds a provider record for information lookup and probe execution.
// The returned handle borrows both the executor and record. Callers keep their
// configuration and credentials valid through the synchronous test execution.
// Supply a nonnil record for information and execution.
func (s *AntigravityProviderTest) Target(value *provider.Record) provider.TestTarget {
	return antigravityTestTarget{executor: s, record: value}
}

// Information reports the selected provider's routing snapshot and inferred
// API protocol.
func (t antigravityTestTarget) Information() provider.TestTargetInfo {
	return provider.TestTargetInfo{ProviderSnapshot: t.record.RoutingSnapshot(), APIProtocol: (provider.ProtocolTarget{Record: t.record}).GetAPIProtocol()}
}

// Execute emits one Antigravity text probe and returns its test-run result.
// Explicit image tests emit an unsupported error; an omitted model uses
// claude-sonnet-4-5. The run is cancelled on return, and Result includes any
// recorded sink error even when the probe itself succeeds.
func (t antigravityTestTarget) Execute(ctx context.Context, request provider.PreparedTestRequest, sink provider.TestEventSink) error {
	image, explicit := provider.ProviderTestTypeFromArgs(request.TestType)
	run := NewTestRun(ctx, make(http.Header), sink)
	// Release the run context on every exit so failed writes or unsupported test
	// arguments cannot leave the probe context alive after this execution returns.
	defer run.Cancel()
	// Antigravity has no image event adapter in this target. Reject an explicit
	// image selection before invoking the text-oriented probe or reporting
	// test_start.
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
