package httpapi

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// TestAllowOpenAICompatibleMessagesDispatchUsesProtocolCollectionForCN checks
// nil input and group protocol collections in the listed CN cases. It checks
// Messages admission only for the nil or explicitly allowed group fixtures.
func TestAllowOpenAICompatibleMessagesDispatchUsesProtocolCollectionForCN(t *testing.T) {
	require.True(t, (openAITextHTTPBackend{}).AllowsMessages(nil))
	for _, platform := range []string{capability.PlatformKimi, capability.PlatformZhipu, capability.PlatformDeepseek} {
		disabled := &apikey.APIKey{Group: &routing.Group{}}
		require.False(t, (openAITextHTTPBackend{}).AllowsMessages(disabled), platform)

		enabled := &apikey.APIKey{Group: &routing.Group{
			AllowedProtocols: []protocol.ProtocolID{protocol.ProtocolAnthropicMessages},
		}}
		require.True(t, (openAITextHTTPBackend{}).AllowsMessages(enabled), platform)
	}
}

// TestMessagesProviderModelKeepsGroupMapping passes three group-mapped model
// strings through Messages provider-layer resolution. It checks each exact
// value so this step does not remap the listed models.
func TestMessagesProviderModelKeepsGroupMapping(t *testing.T) {
	require.Equal(t, "group-model", ResolveOpenAIMessagesProviderLayerModel("group-model"))
	require.Equal(t, "claude-sonnet-4-6", ResolveOpenAIMessagesProviderLayerModel("claude-sonnet-4-6"))
	require.Equal(t, "gpt-5.4-high", ResolveOpenAIMessagesProviderLayerModel("gpt-5.4-high"))
}

// TestOpenAITextHTTPPreludeErrorOrder injects authentication, protocol,
// parsing, owner and image failures across text entries. It checks their
// status, output and observed ordering, including the different stream-field
// acceptance in Messages and Chat.
func TestOpenAITextHTTPPreludeErrorOrder(t *testing.T) {
	t.Run("credential rejection does not read body", func(t *testing.T) {
		p, h, c, w, r := newOpenAITextEntryProbe(t, `broken`)
		p.key = nil
		h.Responses(c)
		require.Equal(t, 401, w.Code)
		require.Zero(t, r.reads)
	})
	t.Run("messages policy precedes body and dependencies", func(t *testing.T) {
		p, h, c, w, r := newOpenAITextEntryProbe(t, `broken`)
		p.allowed = false
		h.Messages(c)
		require.Equal(t, 403, w.Code)
		require.Zero(t, r.reads)
		require.NotContains(t, p.events, "dependencies")
	})
	t.Run("responses keepalive begins before JSON validation", func(t *testing.T) {
		p, h, c, w, _ := newOpenAITextEntryProbe(t, `broken`)
		h.Responses(c)
		require.Equal(t, 400, w.Code)
		require.Contains(t, w.Body.String(), "Failed to parse request body")
		require.Contains(t, p.events, "keepalive-start")
		require.Equal(t, "keepalive-stop", p.events[len(p.events)-1])
		require.NotContains(t, p.events, "reasoning")
	})
	t.Run("owner denial precedes moderation and slot", func(t *testing.T) {
		p, h, c, w, _ := newOpenAITextEntryProbe(t, `{"model":"gpt-5","previous_response_id":"resp_private","input":"hi"}`)
		p.owned = false
		h.Responses(c)
		require.Equal(t, 400, w.Code)
		require.Contains(t, w.Body.String(), "not available for this user")
		require.NotContains(t, p.events, "moderate")
		require.NotContains(t, p.events, "user-slot")
	})
	t.Run("chat channel image restriction precedes moderation", func(t *testing.T) {
		p, h, c, w, _ := newOpenAITextEntryProbe(t, `{"model":"gpt-image-1","messages":[]}`)
		p.image = true
		h.ChatCompletions(c)
		require.Equal(t, 400, w.Code)
		require.NotContains(t, p.events, "moderate")
		require.Contains(t, p.events, "plan")
	})
	t.Run("messages keeps permissive stream while chat rejects", func(t *testing.T) {
		p, h, c, w, _ := newOpenAITextEntryProbe(t, `{"model":"gpt-5","stream":"true","messages":[]}`)
		h.Messages(c)
		require.Equal(t, 200, w.Code)
		require.NotNil(t, p.call)
		require.True(t, p.call.Stream)
		_, h, c, w, _ = newOpenAITextEntryProbe(t, `{"model":"gpt-5","stream":"true","messages":[]}`)
		h.ChatCompletions(c)
		require.Equal(t, 400, w.Code)
		require.Contains(t, w.Body.String(), "invalid stream field type")
	})
}

// TestOpenAITextHTTPWaitAndSnapshot records entry callbacks around user-slot
// waiting and execution for all three protocols. It checks release and
// eligibility ordering, cancellation and failed rechecks, plus Responses
// hashing before reasoning rewriting.
func TestOpenAITextHTTPWaitAndSnapshot(t *testing.T) {
	for _, entry := range []string{"responses", "messages", "chat"} {
		t.Run(entry, func(t *testing.T) {
			p, h, c, w, _ := newOpenAITextEntryProbe(t, `{"model":"gpt-5","input":"hi","messages":[]}`)
			run := h.Responses
			if entry == "messages" {
				run = h.Messages
			}
			if entry == "chat" {
				run = h.ChatCompletions
			}
			run(c)
			require.Equal(t, 200, w.Code)
			require.NotNil(t, p.call)
			assertOpenAITextEventBefore(t, p.events, "user-slot", "eligibility")
			assertOpenAITextEventBefore(t, p.events, "eligibility", "execution")
			assertOpenAITextEventBefore(t, p.events, "execution", "user-release")
			if entry == "chat" {
				assertOpenAITextEventBefore(t, p.events, "plan", "moderate")
			} else {
				assertOpenAITextEventBefore(t, p.events, "moderate", "plan")
			}
		})
	}
	t.Run("failed recheck releases without session execution", func(t *testing.T) {
		p, h, c, _, _ := newOpenAITextEntryProbe(t, `{"model":"gpt-5","input":"hi"}`)
		p.eligibility = errors.New("unavailable")
		h.Responses(c)
		require.Contains(t, p.events, "user-release")
		require.NotContains(t, p.events, "execution")
		require.NotContains(t, p.events, "cyber-check")
	})
	t.Run("wait cancellation does not recheck or execute", func(t *testing.T) {
		p, h, c, _, _ := newOpenAITextEntryProbe(t, `{"model":"gpt-5","input":"hi"}`)
		p.canceled = true
		h.Responses(c)
		require.NotContains(t, p.events, "eligibility")
		require.NotContains(t, p.events, "execution")
	})
	t.Run("responses hashes prompt before reasoning rewrite", func(t *testing.T) {
		original := `{"model":"gpt-5","input":"hi"}`
		p, h, c, _, _ := newOpenAITextEntryProbe(t, original)
		p.rewrite = []byte(`{"model":"gpt-5","input":"rewritten"}`)
		h.Responses(c)
		require.NotNil(t, p.call)
		require.Equal(t, []byte(original), p.call.SessionHashBody)
		require.Equal(t, p.rewrite, p.call.Body)
		require.NotNil(t, p.call.SelectionContext)
	})
}
