package compact

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/sjson"
)

// recoveryPorts records model-rule lookups and recovery effects for order assertions.
type recoveryPorts struct {
	calls         []string // ordered lookup and effect trace
	providerModel string   // configured provider fallback model
}

// ProviderModel records the requested model and returns the configured provider
// fallback. An empty configured value reports no mapping, letting the test
// observe subsequent global-model reads.
func (p *recoveryPorts) ProviderModel(model string) (string, bool) {
	p.calls = append(p.calls, "provider:"+model)
	return p.providerModel, p.providerModel != ""
}

// GlobalModel records a global lookup and returns the fixed fallback fixture.
func (p *recoveryPorts) GlobalModel() string {
	p.calls = append(p.calls, "global")
	return "global-fallback"
}

// ResolveGlobalModel records the global resolution input and returns it unchanged.
func (p *recoveryPorts) ResolveGlobalModel(model string) string {
	p.calls = append(p.calls, "resolve:"+model)
	return model
}

// ObserveRetry records retry observation for the combined recovery ordering assertion.
func (p *recoveryPorts) ObserveRetry(_ []byte, _ string) { p.calls = append(p.calls, "observe") }

// CloseResponse records a response-release effect without owning a response resource.
func (p *recoveryPorts) CloseResponse() { p.calls = append(p.calls, "close") }

// SetModel records the replacement model used by the recovery ordering assertion.
func (p *recoveryPorts) SetModel(model string) { p.calls = append(p.calls, "model:"+model) }

// LogRetry records old and replacement models with their upstream error code.
func (p *recoveryPorts) LogRetry(from, to, code string) {
	p.calls = append(p.calls, "log:"+from+":"+to+":"+code)
}

// TestRecoveryPortOrderAndSingleRetry exercises provider and global fallback
// paths. It protects lazy global lookup, body rewriting, observation-before-close
// ordering, model diagnostics, and the rejection of a second recovery attempt.
func TestRecoveryPortOrderAndSingleRetry(t *testing.T) {
	for _, tc := range []struct {
		// name labels the test scenario, providerModel supplies the configured
		// provider mapping, and fallback names the expected replacement model.
		name, providerModel, fallback string
		reads                         []string // expected model lookup sequence
	}{
		{name: "provider", providerModel: "provider-fallback", fallback: "provider-fallback", reads: []string{"provider:client"}},
		{name: "global", fallback: "global-fallback", reads: []string{"provider:client", "global", "resolve:global-fallback"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &recoveryPorts{providerModel: tc.providerModel}
			r := Recovery{
				Models:        p,
				ContextWindow: func(string, []byte) bool { return false },
				RewriteModel: func(body []byte, model string) []byte {
					v, err := sjson.SetBytes(body, "model", model)
					require.NoError(t, err)
					return v
				},
			}
			body := []byte(`{"model":"current","input":[{"type":"compaction_trigger"}],"stream":true}`)
			in := Request{Explicit: true, RequestedModel: "client", Body: body}
			failure := r.NewFailure(true, []byte(`{"error":{"code":"model_not_found"}}`), "model not found")
			result, model, ok := r.ApplySignal(in, failure, p)
			require.True(t, ok)
			require.Equal(t, tc.fallback, model)
			require.JSONEq(t, `{"model":"`+model+`","input":[{"type":"compaction_trigger"}],"stream":true}`, string(result))
			require.Equal(t, append(tc.reads, "observe", "close", "model:"+model, "log:current:"+model+":model_not_found"), p.calls)
			p.calls = nil
			in.Body = result
			in.AlreadyRetried = true
			unchanged, _, retry := r.ApplySignal(in, failure, p)
			require.False(t, retry)
			require.Equal(t, result, unchanged)
			require.Empty(t, p.calls)
		})
	}
}
