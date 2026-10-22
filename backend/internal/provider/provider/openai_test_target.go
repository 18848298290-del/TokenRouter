package provider

import (
	"context"
	"net/http"

	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// openai_test_target.go prepares OpenAI test runs and applies automatic client
// policy before credential execution. The target creates per-run headers for
// the selected User-Agent and originator, carries the requested text protocol,
// and sends preparation failures through the same event output as execution
// errors.

// openaiTestTarget retains an OpenAI executor and provider record for prepared
// probe execution.
type openaiTestTarget struct {
	executor *OpenAIProviderTest // shared OpenAI probe dependencies
	// record supplies credentials and routing throughout the test. The caller
	// keeps it stable until preparation, execution, and information reads finish.
	record *provider.Record
}

// Target binds the supplied record to this OpenAI executor and exposes a test
// handle. The handle retains the record for credential and routing reads, so
// callers keep it stable during the probe.
func (s *OpenAIProviderTest) Target(value *provider.Record) provider.TestTarget {
	return openaiTestTarget{executor: s, record: value}
}

// Information returns the retained OpenAI record's routing snapshot and
// configured protocol.
func (t openaiTestTarget) Information() provider.TestTargetInfo {
	return provider.TestTargetInfo{ProviderSnapshot: t.record.RoutingSnapshot(), APIProtocol: (provider.ProtocolTarget{Record: t.record}).GetAPIProtocol()}
}

// Execute creates one event run, applies optional preparation, and executes the
// selected model, mode, and test type. Preparation errors are emitted before
// return, and Result includes any sink failure that cancelled execution.
func (t openaiTestTarget) Execute(ctx context.Context, request provider.PreparedTestRequest, sink provider.TestEventSink) error {
	headers := make(http.Header)
	headers.Set("User-Agent", request.UserAgent)
	headers.Set("originator", request.Originator)
	run := NewTestRun(ctx, headers, sink)
	run.GroupID = request.GroupID
	defer run.Cancel()
	run.Automatic = request.Automatic
	run.RequestedProtocol = provider.TextProtocol(request.Protocol)
	// The executor's existing text selector uses legacy provider spellings. Map
	// the resolved upstream identity here so provider defaults cannot replace it.
	switch request.ProtocolRoute.UpstreamProtocol {
	case "openai_responses":
		run.RequestedProtocol = provider.TextProtocolResponses
	case "openai_chat_completions":
		run.RequestedProtocol = provider.TextProtocolChatCompletions
	}
	if t.executor.Prepare != nil {
		if err := t.executor.Prepare(run, t.record); err != nil {
			return run.Result((TestStreamOutput{}).Error(run, err.Error()))
		}
	}
	return run.Result(t.executor.Execute(run, t.record, request.Model, request.Prompt, request.Mode, request.TestType))
}
