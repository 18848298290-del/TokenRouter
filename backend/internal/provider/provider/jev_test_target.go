package provider

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/protocol/systemone"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/jev"
)

// jev_test_target.go executes native SystemOne decision probes through the
// shared Jev upstream executor. A supplied decision body keeps its state,
// questions, and extensions while model replacement applies the provider's
// mapped target; absent structured input creates a minimal Noul availability
// question.
//
// Credentials and the configured root are validated before committing output
// or making the request. The target retains the provider for allowed header
// overrides and transport parameters, then emits the returned decision JSON as
// content and includes usage validity in completion data. This gives manual
// and automatic probes the same native request and event representation while
// preserving the response model reported by upstream.

// JevProviderTest prepares native SystemOne probes through the shared upstream
// request transport.
type JevProviderTest struct {
	// Attributes resolves the final model for routed decision probes. A nil
	// callback skips profile lookup before the native SystemOne request is sent.
	Attributes  ModelAttributeLookup
	Transport   httpclient.UpstreamTransport // SystemOne HTTP request sender
	ValidateURL func(string) (string, error) // configured upstream root validator
}

// jevTestTarget retains Jev dependencies and a provider record for prepared
// probes.
type jevTestTarget struct {
	executor *JevProviderTest // shared native probe dependencies
	// record supplies the decision model, credentials, and transport parameters.
	// Callers keep it stable until the retained target finishes execution.
	record *provider.Record
}

// Target binds the supplied Jev record to this executor and returns a prepared
// test handle. Keep the retained record stable through execution because
// authentication, model mapping, headers, and transport read it.
func (s *JevProviderTest) Target(value *provider.Record) provider.TestTarget {
	return jevTestTarget{s, value}
}

// Information returns the routing snapshot of the retained Jev provider.
func (t jevTestTarget) Information() provider.TestTargetInfo {
	return provider.TestTargetInfo{ProviderSnapshot: t.record.RoutingSnapshot()}
}

// Execute submits native SystemOne questions and emits the decision response as
// test content. An empty model selects jev-latest before provider mapping, and
// absent structured input creates a minimal Noul probe using the prepared
// prompt.
//
// Supplied SystemOne JSON replaces its model with that mapped target before
// request validation, retaining the caller's other decision fields. Invalid
// credentials, roots, or structured bodies fail before upstream execution. The
// upstream executor returns response JSON and usage validity, which become
// completion data. Successful platform execution returns Result, which also
// reports any retained sink failure.
func (t jevTestTarget) Execute(ctx context.Context, request provider.PreparedTestRequest, sink provider.TestEventSink) error {
	run := NewTestRun(ctx, nil, sink)
	defer run.Cancel()
	output := TestStreamOutput{}
	if err := provider.ValidateJevCredentials(t.record); err != nil {
		return output.Error(run, err.Error())
	}
	base, err := t.executor.ValidateURL(t.record.GetJevBaseURL())
	if err != nil {
		return output.Error(run, "Invalid Jev base URL")
	}
	model := request.Model
	if model == "" {
		model = "jev-latest"
	}
	model = mappedTestModel(t.record, model)
	if request.ProtocolRoute.ClientProtocol != "" && t.executor.Attributes != nil {
		if _, err := t.executor.Attributes(ctx, t.record, request.GroupID, model, string(request.ProtocolRoute.UpstreamProtocol)); err != nil {
			return output.Error(run, err.Error())
		}
	}
	proxy := ""
	if t.record.Proxy != nil {
		proxy = t.record.Proxy.URL()
	}
	body := systemone.ProbeBody(model, request.Prompt)
	// A structured decision probe carries caller state and questions. Replace the
	// mapped model in that body before validation so those fields survive provider
	// alias selection.
	if len(request.SystemOne) > 0 {
		body, err = systemone.ReplaceModel(request.SystemOne, model)
		if err != nil {
			return output.Error(run, "Invalid SystemOne test request")
		}
		if _, err = systemone.ParseRequest(body); err != nil {
			return output.Error(run, err.Error())
		}
	}
	run.Begin(true)
	output.SendEvent(run, provider.TestEvent{Type: "test_start", Model: model})
	target := &jev.Target{
		ProviderID: t.record.ID, URL: jev.EndpointURL(base, "systemone"), Model: model, Token: t.record.GetCredential("api_key"),
		ApplyHeaders: func(headers http.Header) { ApplyProviderHeaderOverrides(t.record, headers) },
		Do: func(req *http.Request) (*http.Response, error) {
			return t.executor.Transport.Do(req, proxy, t.record.ID, t.record.Concurrency)
		},
	}
	result, err := (jev.Executor{}).Execute(run.Context, upstream.AttemptInput{Protocol: protocol.ProtocolSystemOne, Body: body, Target: target}, nil)
	if err != nil {
		return output.Error(run, "SystemOne test failed: "+err.Error())
	}
	output.SendEvent(run, provider.TestEvent{Type: "content", Text: string(result.MediaBody)})
	// Completion reports the upstream attempt duration in milliseconds.
	data := map[string]any{"duration_ms": result.Duration.Milliseconds(), "usage_valid": result.HasUsage, "response": json.RawMessage(result.MediaBody)}
	output.SendEvent(run, provider.TestEvent{Type: "test_complete", Model: result.UpstreamResponseModel, Success: true, Data: data})
	return run.Result(nil)
}
