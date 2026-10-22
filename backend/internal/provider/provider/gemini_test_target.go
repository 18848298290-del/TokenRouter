package provider

import (
	"context"
	"net/http"

	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// gemini_test_target.go binds prepared Gemini probes to an executor copy with
// per-run User-Agent settings. The target creates a TestRun and a shallow
// executor copy, assigns that run's automatic override, and returns the
// combined execution and event-output result after cancelling its context.

// geminiTestTarget borrows a provider record and the shared Gemini probe executor.
type geminiTestTarget struct {
	// executor and record are borrowed across route preparation and token
	// acquisition. Keep them available through handle calls and serialize
	// external configuration changes with Information and synchronous Execute.
	executor *GeminiProviderTest // shared probe preparation adapter
	record   *provider.Record    // borrowed selected provider
}

// Target binds a record for Gemini information lookup and probe execution.
// The handle borrows the record and executor dependencies. Callers retain them
// through execution and serialize mutations of the selected provider record.
// Supply a nonnil record for information and execution.
func (s *GeminiProviderTest) Target(value *provider.Record) provider.TestTarget {
	return geminiTestTarget{executor: s, record: value}
}

// Information reports the provider's routing snapshot together with its
// inferred API protocol.
func (t geminiTestTarget) Information() provider.TestTargetInfo {
	return provider.TestTargetInfo{ProviderSnapshot: t.record.RoutingSnapshot(), APIProtocol: (provider.ProtocolTarget{Record: t.record}).GetAPIProtocol()}
}

// Execute runs one Gemini probe and includes its event-output error in the result.
// Automatic probes override UserAgent on a shallow executor copy, so dependencies
// remain shared while that string is local to this call. The TestRun context is
// cancelled on every return path.
func (t geminiTestTarget) Execute(ctx context.Context, request provider.PreparedTestRequest, sink provider.TestEventSink) error {
	headers := make(http.Header)
	headers.Set("User-Agent", request.UserAgent)
	run := NewTestRun(ctx, headers, sink)
	run.GroupID = request.GroupID
	defer run.Cancel()
	// Copy the executor before assigning an automatic UA; concurrent probes can
	// supply different strings while sharing the same token and transport
	// dependencies.
	executor := *t.executor
	if request.Automatic {
		executor.UserAgent = request.UserAgent
	}
	return run.Result(executor.Execute(run, t.record, request.Model, request.Prompt, request.TestType))
}
