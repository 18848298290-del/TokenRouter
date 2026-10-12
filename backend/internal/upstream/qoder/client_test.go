package qoder

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func testSession() *SessionContext {
	return &SessionContext{
		CosyKey: "test-cosy-key",
		Info:    "test-info",
		Identity: &AuthIdentity{
			Name:           "test",
			UID:            "u-123",
			AID:            "a-456",
			OrganizationID: "org-789",
		},
		Machine: &MachineIdentity{
			MachineID:    "mid-abc",
			MachineToken: "mytoken",
			MachineType:  "5",
		},
	}
}

func TestCNClientUsesGatewayEndpointVersionAndCanonicalSignaturePath(t *testing.T) {
	profile := MustProfileForSite(SiteCN)
	profile.GatewayBaseURL = "https://gateway.example"
	client := NewClientForProfile(profile)
	client.MachineOS = "aarch64_darwin"
	session := testSession()
	session.Site = SiteCN
	session.ClientVersion = CNClientVersion
	var captured *http.Request
	var encodedBody string
	doer := func(req *http.Request) (*http.Response, error) {
		captured = req
		body, err := io.ReadAll(req.Body)
		require.NoError(t, err)
		encodedBody = string(body)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("data: [DONE]\n\n")),
			Request:    req,
		}, nil
	}

	resp, err := client.StreamRequestContextWithDoer(context.Background(), session, "", []byte(`{"model":"auto"}`), nil, doer)
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Equal(t, "gateway.example", captured.URL.Host)
	require.Equal(t, "/algo/api/v2/service/pro/sse/agent_chat_generation", captured.URL.Path)
	require.Equal(t, "1.24.2", captured.Header.Get("Cosy-Version"))
	require.Equal(t, "aarch64_darwin", captured.Header.Get("Cosy-Machineos"))
	require.Equal(t, "mid-abc", captured.Header.Get("Cosy-Machineid"))
	require.Equal(t, []string{""}, captured.Header.Values("Cosy-Machinetoken"))
	require.Equal(t, []string{""}, captured.Header.Values("Cosy-Machinetype"))
	require.Equal(t, []string{""}, captured.Header.Values("Cosy-Machinecode"))

	authorization := strings.TrimPrefix(captured.Header.Get("Authorization"), "Bearer COSY.")
	parts := strings.Split(authorization, ".")
	require.Len(t, parts, 2)
	expectedSignature := SignQoderRequest(
		parts[0],
		session.CosyKey,
		captured.Header.Get("Cosy-Date"),
		encodedBody,
		"/api/v2/service/pro/sse/agent_chat_generation",
	)
	require.Equal(t, expectedSignature, parts[1])
}

func TestJSONRequestAddsEncodeQueryWithoutSigningIt(t *testing.T) {
	profile := MustProfileForSite(SiteCN)
	profile.GatewayBaseURL = "https://gateway.example"
	client := NewClientForProfile(profile)
	session := testSession()
	session.Site = SiteCN
	var captured *http.Request
	var encodedBody string
	doer := func(req *http.Request) (*http.Response, error) {
		captured = req
		body, err := io.ReadAll(req.Body)
		require.NoError(t, err)
		encodedBody = string(body)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{}`)),
			Request:    req,
		}, nil
	}

	err := client.JSONRequestContextWithDoer(
		context.Background(),
		http.MethodPost,
		session,
		"/api/v3/user/status?region=cn",
		[]byte(`{"userId":"user-1"}`),
		nil,
		doer,
		&map[string]any{},
	)
	require.NoError(t, err)
	require.Equal(t, "/algo/api/v3/user/status", captured.URL.Path)
	require.Equal(t, "1", captured.URL.Query().Get("Encode"))
	require.Equal(t, "cn", captured.URL.Query().Get("region"))

	authorization := strings.TrimPrefix(captured.Header.Get("Authorization"), "Bearer COSY.")
	parts := strings.Split(authorization, ".")
	require.Len(t, parts, 2)
	expectedSignature := SignQoderRequest(
		parts[0],
		session.CosyKey,
		captured.Header.Get("Cosy-Date"),
		encodedBody,
		"/api/v3/user/status",
	)
	require.Equal(t, expectedSignature, parts[1])
}

func TestSignatureJSONRequestUsesAppcodeHeadersWithoutAuthorization(t *testing.T) {
	profile := MustProfileForSite(SiteCN)
	profile.GatewayBaseURL = "https://gateway.example"
	client := NewClientForProfile(profile)
	client.ClientIP = "172.18.0.1"
	session := testSession()
	session.Identity = nil
	session.Site = SiteCN
	var captured *http.Request
	doer := func(req *http.Request) (*http.Response, error) {
		captured = req
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{}`)),
			Request:    req,
		}, nil
	}

	err := client.SignatureJSONRequestContextWithDoer(
		context.Background(),
		http.MethodPost,
		session,
		AuthStatusPath,
		[]byte(`{"userId":"user-1"}`),
		nil,
		doer,
		&map[string]any{},
	)

	require.NoError(t, err)
	require.Equal(t, "1", captured.URL.Query().Get("Encode"))
	require.Equal(t, "0", captured.Header.Get("Cosy-Clienttype"))
	require.NotEmpty(t, captured.Header.Get("Date"))
	require.Equal(t, AppCode, captured.Header.Get("Appcode"))
	require.NotEmpty(t, captured.Header.Get("Signature"))
	require.NotContains(t, captured.Header, "Authorization")
	require.NotContains(t, captured.Header, "Cosy-Key")
	require.NotContains(t, captured.Header, "Cosy-User")
	require.NotContains(t, captured.Header, "Cosy-Date")
	require.NotContains(t, captured.Header, "Cosy-Data-Policy")
	require.NotContains(t, captured.Header, "Cosy-Organization-Id")
	require.NotContains(t, captured.Header, "Cosy-Organization-Tags")
}

func TestBearerJSONRequestUsesSecurityOAuthToken(t *testing.T) {
	profile := MustProfileForSite(SiteCN)
	profile.GatewayBaseURL = "https://gateway.example"
	client := NewClientForProfile(profile)
	session := testSession()
	session.Site = SiteCN
	session.Identity.SecurityOauthToken = "security-token"
	var captured *http.Request
	doer := func(req *http.Request) (*http.Response, error) {
		captured = req
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{}`)),
			Request:    req,
		}, nil
	}

	err := client.BearerJSONRequestContextWithDoer(
		context.Background(),
		http.MethodGet,
		session,
		QuotaUsagePath,
		nil,
		nil,
		doer,
		&map[string]any{},
	)

	require.NoError(t, err)
	require.Equal(t, "/algo"+QuotaUsagePath, captured.URL.Path)
	require.Equal(t, "Bearer security-token", captured.Header.Get("Authorization"))
	require.NotContains(t, captured.Header, "Cosy-Key")
	require.NotContains(t, captured.Header, "Cosy-Date")
}

func getHeaders(t *testing.T) http.Header {
	t.Helper()
	c := NewClient("https://test.qoder.sh")
	req, _ := http.NewRequest(http.MethodPost, "https://test.qoder.sh/test", nil)
	c.setHeaders(req, testSession(), "/test", "encoded-body")
	return req.Header
}

func TestHeadersClientIPIsMachineID(t *testing.T) {
	h := getHeaders(t)
	if h.Get("cosy-clientip") != "mid-abc" {
		t.Errorf("cosy-clientip = %q, want mid-abc", h.Get("cosy-clientip"))
	}
}

func TestHeadersMachineTypeIs5(t *testing.T) {
	h := getHeaders(t)
	if h.Get("cosy-machinetype") != "5" {
		t.Errorf("cosy-machinetype = %q, want 5", h.Get("cosy-machinetype"))
	}
}

func TestHeadersUsePersistedMachineToken(t *testing.T) {
	h := getHeaders(t)
	if h.Get("cosy-machinetoken") != "mytoken" {
		t.Errorf("cosy-machinetoken = %q, want mytoken", h.Get("cosy-machinetoken"))
	}
}

func TestHeadersDataPolicyIsDisagree(t *testing.T) {
	h := getHeaders(t)
	if h.Get("cosy-data-policy") != "disagree" {
		t.Errorf("cosy-data-policy = %q, want disagree", h.Get("cosy-data-policy"))
	}
}

func TestHeadersUseGlobalSiteVersion(t *testing.T) {
	h := getHeaders(t)
	if h.Get("cosy-version") != "1.24.2" {
		t.Errorf("cosy-version = %q, want 1.24.2", h.Get("cosy-version"))
	}
}

func TestHeadersIncludeMachineOSAndLegacyFallbacks(t *testing.T) {
	session := testSession()
	session.Machine.MachineToken = ""
	session.Machine.MachineType = ""
	client := NewClient("https://test.qoder.sh")
	client.MachineOS = "aarch64_darwin"
	req, _ := http.NewRequest(http.MethodPost, "https://test.qoder.sh/test", nil)
	client.setHeaders(req, session, "/test", "encoded-body")
	require.Equal(t, session.Machine.MachineID, req.Header.Get("cosy-machinetoken"))
	require.Equal(t, "5", req.Header.Get("cosy-machinetype"))
	require.Equal(t, "aarch64_darwin", req.Header.Get("cosy-machineos"))
}

func TestCNHeadersIgnoreLegacyRandomMachineFields(t *testing.T) {
	session := testSession()
	session.Site = SiteCN
	client := NewClientForProfile(MustProfileForSite(SiteCN))
	client.ClientIP = "172.18.0.1"
	req, _ := http.NewRequest(http.MethodPost, "https://gateway.qoder.com.cn/test", nil)

	client.setHeaders(req, session, "/test", "encoded-body")

	require.Equal(t, "mid-abc", req.Header.Get("cosy-machineid"))
	require.Equal(t, []string{""}, req.Header.Values("cosy-machinetoken"))
	require.Equal(t, []string{""}, req.Header.Values("cosy-machinetype"))
	require.Equal(t, []string{""}, req.Header.Values("cosy-machinecode"))
	require.Equal(t, "172.18.0.1", req.Header.Get("cosy-clientip"))
	require.Equal(t, "0", req.Header.Get("cosy-clienttype"))
	require.Equal(t, "DISAGREE", req.Header.Get("cosy-data-policy"))
	require.Equal(t, []string{""}, req.Header.Values("cosy-organization-tags"))
	require.NotContains(t, req.Header, "Cosy-Scene")
	require.NotContains(t, req.Header, "Cosy-Business-Product")
	require.NotContains(t, req.Header, "Cosy-Business-Type")
}

func TestHeadersOrganizationID(t *testing.T) {
	h := getHeaders(t)
	if h.Get("cosy-organization-id") != "org-789" {
		t.Errorf("cosy-organization-id = %q, want org-789", h.Get("cosy-organization-id"))
	}
}

func TestHeadersOrganizationTags(t *testing.T) {
	h := getHeaders(t)
	if h.Get("cosy-organization-tags") != "Normal" {
		t.Errorf("cosy-organization-tags = %q, want Normal", h.Get("cosy-organization-tags"))
	}
}

func TestHeadersScene(t *testing.T) {
	h := getHeaders(t)
	if h.Get("cosy-scene") != "assistant" {
		t.Errorf("cosy-scene = %q, want assistant", h.Get("cosy-scene"))
	}
}

func TestHeadersBusinessProduct(t *testing.T) {
	h := getHeaders(t)
	if h.Get("cosy-business-product") != "cli" {
		t.Errorf("cosy-business-product = %q, want cli", h.Get("cosy-business-product"))
	}
}

func TestHeadersBusinessType(t *testing.T) {
	h := getHeaders(t)
	if h.Get("cosy-business-type") != "agent" {
		t.Errorf("cosy-business-type = %q, want agent", h.Get("cosy-business-type"))
	}
}

func TestHeadersNoHardcodedIP(t *testing.T) {
	h := getHeaders(t)
	if h.Get("cosy-clientip") == "169.254.198.161" {
		t.Error("cosy-clientip should not be hardcoded 169.254.198.161")
	}
}

// TestQoderGatewayShouldFailoverRetryableUpstreamErrors rejects ordinary errors and HTTP 401 for compatible requests.
func TestQoderGatewayShouldFailoverRetryableUpstreamErrors(t *testing.T) {
	require.True(t, MaySwitchCompatibleAttempt(&APIError{StatusCode: http.StatusTooManyRequests}))
	require.True(t, MaySwitchCompatibleAttempt(&APIError{StatusCode: http.StatusBadGateway, Code: "115"}))
	require.True(t, MaySwitchCompatibleAttempt(&APIError{StatusCode: http.StatusForbidden, Code: "115"}))
	require.True(t, MaySwitchCompatibleAttempt(&APIError{StatusCode: http.StatusForbidden, Code: "112"}))
	require.True(t, MaySwitchCompatibleAttempt(&APIError{StatusCode: http.StatusInternalServerError}))
	require.False(t, MaySwitchCompatibleAttempt(&APIError{StatusCode: http.StatusUnauthorized}))
	require.False(t, MaySwitchCompatibleAttempt(fmt.Errorf("plain error")))
}

func TestParseSSELineTextDelta(t *testing.T) {
	line := `data: {"body": "{\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}"}`
	events, err := ParseSSELine(line)
	if err != nil {
		t.Fatalf("ParseSSELine: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events length = %d, want 1", len(events))
	}
	if events[0].Type != "text_delta" {
		t.Errorf("event type = %q, want text_delta", events[0].Type)
	}
	if events[0].Text != "Hello" {
		t.Errorf("event text = %q, want Hello", events[0].Text)
	}
}

func TestParseSSELineDone(t *testing.T) {
	events, err := ParseSSELine(`data: [DONE]`)
	if err != nil {
		t.Fatalf("ParseSSELine: %v", err)
	}
	if len(events) != 1 || !events[0].IsDone {
		t.Error("expected IsDone event")
	}
}

func TestParseSSELineDoneInBody(t *testing.T) {
	events, err := ParseSSELine(`data: {"body": "[DONE]"}`)
	if err != nil {
		t.Fatalf("ParseSSELine: %v", err)
	}
	if len(events) != 1 || !events[0].IsDone {
		t.Error("expected IsDone event")
	}
}

func TestParseSSELineEmptyBody(t *testing.T) {
	events, err := ParseSSELine(`data: {"body": ""}`)
	if err != nil {
		t.Fatalf("ParseSSELine: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("expected 0 events, got %d", len(events))
	}
}

func TestParseSSELineToolCall(t *testing.T) {
	line := `data: {"body": "{\"choices\":[{\"delta\":{\"tool_calls\":[{\"id\":\"tc1\",\"function\":{\"name\":\"bash\",\"arguments\":\"{\\\"cmd\\\":\\\"ls\\\"}\"}}]}}]}"}`
	events, err := ParseSSELine(line)
	if err != nil {
		t.Fatalf("ParseSSELine: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events length = %d, want 1", len(events))
	}
	if events[0].Type != "tool_call_delta" {
		t.Errorf("event type = %q, want tool_call_delta", events[0].Type)
	}
	if events[0].ToolName != "bash" {
		t.Errorf("tool name = %q, want bash", events[0].ToolName)
	}
}

func TestParseSSELineToolCallPreservesIndexTypeAndObjectArguments(t *testing.T) {
	line := `data: {"body": "{\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"tc1\",\"type\":\"function\",\"function\":{\"name\":\"bash\",\"arguments\":{\"cmd\":\"ls\"}}}]}}]}"}`
	events, err := ParseSSELine(line)
	if err != nil {
		t.Fatalf("ParseSSELine: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events length = %d, want 1", len(events))
	}
	event := events[0]
	if !event.HasToolCallIndex || event.ToolCallIndex != 0 {
		t.Fatalf("tool call index = (%v, %d), want (true, 0)", event.HasToolCallIndex, event.ToolCallIndex)
	}
	if event.ToolType != "function" {
		t.Fatalf("tool type = %q, want function", event.ToolType)
	}
	if event.Arguments != `{"cmd":"ls"}` {
		t.Fatalf("arguments = %q, want object JSON", event.Arguments)
	}
}

func TestParseSSELineToolCallSyntheticIndexSkipsEmptyPlaceholders(t *testing.T) {
	line := `data: {"body": "{\"choices\":[{\"delta\":{\"tool_calls\":[{}, {\"type\":\"function\"}, {\"type\":\"function\",\"function\":{\"arguments\":\"{\\\"command\\\":\\\"pwd\\\"}\"}}, {\"type\":\"function\",\"function\":{\"arguments\":\"{\\\"command\\\":\\\"printf OPENCODE_PARALLEL_OK\\\"}\"}}, {\"type\":\"function\",\"function\":{\"arguments\":\"{\\\"pattern\\\":\\\"docs/*.md\\\"}\"}}]}}]}"}`
	events, err := ParseSSELine(line)
	if err != nil {
		t.Fatalf("ParseSSELine: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("events length = %d, want 3: %#v", len(events), events)
	}
	for i, event := range events {
		if !event.HasToolCallIndex || event.ToolCallIndex != i {
			t.Fatalf("event %d index = (%v, %d), want (true, %d): %#v", i, event.HasToolCallIndex, event.ToolCallIndex, i, events)
		}
	}
	if events[0].Arguments != `{"command":"pwd"}` || events[1].Arguments != `{"command":"printf OPENCODE_PARALLEL_OK"}` || events[2].Arguments != `{"pattern":"docs/*.md"}` {
		t.Fatalf("arguments = %#v, want compact synthetic indexes with arguments", events)
	}
}

func TestParseSSELineToolUseEnvelopeEventsSkipTypeOnlyPlaceholder(t *testing.T) {
	line := `data: {"body": "{\"event\":\"tool_use_delta\",\"data\":{\"type\":\"function\"}}"}`
	events, err := ParseSSELine(line)
	if err != nil {
		t.Fatalf("ParseSSELine: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("events = %#v, want no event for type-only placeholder", events)
	}
}

func TestParseSSELineFlatToolCall(t *testing.T) {
	line := `data: {"body": "{\"choices\":[{\"delta\":{\"tool_calls\":[{\"tool_call_id\":\"tc1\",\"name\":\"Bash\",\"arguments\":{\"command\":\"pwd\"}}]}}]}"}`
	events, err := ParseSSELine(line)
	if err != nil {
		t.Fatalf("ParseSSELine: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events length = %d, want 1", len(events))
	}
	event := events[0]
	if event.Type != "tool_call_delta" || event.ToolCallID != "tc1" || event.ToolName != "Bash" {
		t.Fatalf("tool event = %#v, want tc1 Bash", event)
	}
	if event.ToolType != "function" {
		t.Fatalf("tool type = %q, want function", event.ToolType)
	}
	if event.Arguments != `{"command":"pwd"}` {
		t.Fatalf("arguments = %q, want object JSON", event.Arguments)
	}
}

func TestParseSSELineToolUseEnvelopeEvents(t *testing.T) {
	start := `data: {"body": "{\"event\":\"tool_use_start\",\"data\":{\"id\":\"tc1\",\"name\":\"Bash\"}}"}`
	delta := `data: {"body": "{\"event\":\"tool_use_delta\",\"data\":{\"tool_call_id\":\"tc1\",\"name\":\"Bash\",\"arguments\":\"{\\\"command\\\":\\\"pwd\\\"}\"}}"}`
	events, err := ParseSSELine(start)
	if err != nil {
		t.Fatalf("ParseSSELine start: %v", err)
	}
	if len(events) != 1 || events[0].ToolCallID != "tc1" || events[0].ToolName != "Bash" {
		t.Fatalf("start events = %#v, want tc1 Bash", events)
	}
	events, err = ParseSSELine(delta)
	if err != nil {
		t.Fatalf("ParseSSELine delta: %v", err)
	}
	if len(events) != 1 || events[0].ToolCallID != "tc1" || events[0].Arguments != `{"command":"pwd"}` {
		t.Fatalf("delta events = %#v, want arguments", events)
	}
}

func TestParseSSELineToolUseEnvelopeEventsPreserveIndex(t *testing.T) {
	start := `data: {"body": "{\"event\":\"tool_use_start\",\"data\":{\"index\":2,\"id\":\"tc1\",\"name\":\"Bash\"}}"}`
	delta := `data: {"body": "{\"event\":\"tool_use_delta\",\"data\":{\"index\":2,\"arguments\":\"{\\\"command\\\":\\\"pwd\\\"}\"}}"}`

	events, err := ParseSSELine(start)
	if err != nil {
		t.Fatalf("ParseSSELine start: %v", err)
	}
	if len(events) != 1 || !events[0].HasToolCallIndex || events[0].ToolCallIndex != 2 || events[0].ToolName != "Bash" {
		t.Fatalf("start events = %#v, want index 2 Bash", events)
	}
	events, err = ParseSSELine(delta)
	if err != nil {
		t.Fatalf("ParseSSELine delta: %v", err)
	}
	if len(events) != 1 || !events[0].HasToolCallIndex || events[0].ToolCallIndex != 2 || events[0].Arguments != `{"command":"pwd"}` {
		t.Fatalf("delta events = %#v, want index 2 arguments", events)
	}
}

func TestParseSSELineFinalMessageToolCalls(t *testing.T) {
	line := `data: {"body": "{\"choices\":[{\"message\":{\"tool_calls\":[{\"id\":\"tc1\",\"type\":\"function\",\"function\":{\"name\":\"bash\",\"arguments\":{\"cmd\":\"ls\"}}}]},\"finish_reason\":\"tool_calls\"}]}"}`
	events, err := ParseSSELine(line)
	if err != nil {
		t.Fatalf("ParseSSELine: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events length = %d, want 2", len(events))
	}
	event := events[0]
	if event.Type != "tool_call_delta" || event.ToolCallID != "tc1" || event.ToolName != "bash" {
		t.Fatalf("tool event = %#v, want tc1 bash", event)
	}
	if event.Arguments != `{"cmd":"ls"}` {
		t.Fatalf("arguments = %q, want object JSON", event.Arguments)
	}
	if !events[1].IsDone {
		t.Fatalf("second event = %#v, want done", events[1])
	}
}

func TestParseSSELineReasoning(t *testing.T) {
	line := `data: {"body": "{\"choices\":[{\"delta\":{\"reasoning_content\":\"Let me think...\"}}]}"}`
	events, err := ParseSSELine(line)
	if err != nil {
		t.Fatalf("ParseSSELine: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events length = %d, want 1", len(events))
	}
	if events[0].Type != "reasoning_delta" {
		t.Errorf("event type = %q, want reasoning_delta", events[0].Type)
	}
	if events[0].Text != "Let me think..." {
		t.Errorf("event text = %q, want Let me think...", events[0].Text)
	}
}

func TestParseSSELineContentAndReasoningAreSeparate(t *testing.T) {
	line := `data: {"body": "{\"choices\":[{\"delta\":{\"reasoning_content\":\"Think\",\"content\":\"Answer\"}}]}"}`
	events, err := ParseSSELine(line)
	if err != nil {
		t.Fatalf("ParseSSELine: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events length = %d, want 2", len(events))
	}
	if events[0].Type != "text_delta" || events[0].Text != "Answer" {
		t.Fatalf("first event = %#v, want text_delta Answer", events[0])
	}
	if events[1].Type != "reasoning_delta" || events[1].Text != "Think" {
		t.Fatalf("second event = %#v, want reasoning_delta Think", events[1])
	}
}

func TestParseSSELineUsage(t *testing.T) {
	line := `data: {"body": "{\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":34,\"total_tokens\":46}}"}`
	events, err := ParseSSELine(line)
	if err != nil {
		t.Fatalf("ParseSSELine: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events length = %d, want 1", len(events))
	}
	if events[0].Type != "usage" || !events[0].HasUsage {
		t.Fatalf("event = %#v, want usage event", events[0])
	}
	if events[0].PromptTokens != 12 {
		t.Fatalf("prompt tokens = %d, want 12", events[0].PromptTokens)
	}
	if events[0].CompletionTokens != 34 {
		t.Fatalf("completion tokens = %d, want 34", events[0].CompletionTokens)
	}
	if events[0].TotalTokens != 46 {
		t.Fatalf("total tokens = %d, want 46", events[0].TotalTokens)
	}
}

func TestParseSSELineUsageAcceptsAnthropicNames(t *testing.T) {
	line := `data: {"body": "{\"usage\":{\"input_tokens\":7,\"output_tokens\":9}}"}`
	events, err := ParseSSELine(line)
	if err != nil {
		t.Fatalf("ParseSSELine: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events length = %d, want 1", len(events))
	}
	if events[0].PromptTokens != 7 || events[0].CompletionTokens != 9 || events[0].TotalTokens != 16 {
		t.Fatalf("usage event = %#v, want 7/9/16", events[0])
	}
}

func TestParseSSELineWrappedUsagePreservesDetails(t *testing.T) {
	line := `data: {"body": "{\"usage\":{\"prompt_tokens\":66637,\"completion_tokens\":6,\"total_tokens\":66643,\"prompt_tokens_details\":{\"cached_tokens\":66612,\"cacheable_tokens\":19},\"completion_tokens_details\":{\"reasoning_tokens\":0}}}"}`
	events, err := ParseSSELine(line)
	if err != nil {
		t.Fatalf("ParseSSELine: %v", err)
	}
	if len(events) != 1 || !events[0].HasUsage {
		t.Fatalf("events = %#v, want one usage event", events)
	}
	event := events[0]
	if event.PromptTokens != 66637 || event.CompletionTokens != 6 || event.TotalTokens != 66643 {
		t.Fatalf("usage = %#v, want upstream totals", event)
	}
	if event.UsageDetails.PromptTokensDetails == nil {
		t.Fatal("prompt token details missing")
	}
	if event.UsageDetails.PromptTokensDetails.CachedTokens != 66612 {
		t.Fatalf("cached tokens = %d, want 66612", event.UsageDetails.PromptTokensDetails.CachedTokens)
	}
	if event.UsageDetails.PromptTokensDetails.CacheableTokens != 19 {
		t.Fatalf("cacheable tokens = %d, want 19", event.UsageDetails.PromptTokensDetails.CacheableTokens)
	}
	if event.UsageDetails.CompletionTokensDetails == nil {
		t.Fatal("completion token details missing")
	}
	if event.UsageDetails.CompletionTokensDetails.ReasoningTokens != 0 {
		t.Fatalf("reasoning tokens = %d, want 0", event.UsageDetails.CompletionTokensDetails.ReasoningTokens)
	}
}

func TestParseSSELineUsageAcceptsNumericStringsAndFloats(t *testing.T) {
	line := `data: {"body": "{\"usage\":{\"prompt_tokens\":\"66637.0\",\"completion_tokens\":6.0,\"total_tokens\":\"66643\",\"prompt_tokens_details\":{\"cached_tokens\":\"66612.0\",\"cacheable_tokens\":19.0},\"completion_tokens_details\":{\"reasoning_tokens\":\"7.0\"}}}"}`
	events, err := ParseSSELine(line)
	if err != nil {
		t.Fatalf("ParseSSELine: %v", err)
	}
	if len(events) != 1 || !events[0].HasUsage {
		t.Fatalf("events = %#v, want one usage event", events)
	}
	event := events[0]
	if event.PromptTokens != 66637 || event.CompletionTokens != 6 || event.TotalTokens != 66643 {
		t.Fatalf("usage = %#v, want upstream totals", event)
	}
	if event.UsageDetails.PromptTokensDetails == nil || event.UsageDetails.PromptTokensDetails.CachedTokens != 66612 || event.UsageDetails.PromptTokensDetails.CacheableTokens != 19 {
		t.Fatalf("prompt details = %#v, want cached/cacheable", event.UsageDetails.PromptTokensDetails)
	}
	if event.UsageDetails.CompletionTokensDetails == nil || event.UsageDetails.CompletionTokensDetails.ReasoningTokens != 7 {
		t.Fatalf("completion details = %#v, want reasoning 7", event.UsageDetails.CompletionTokensDetails)
	}
}

func TestParseSSELineMalformedWrapper(t *testing.T) {
	_, err := ParseSSELine("data: not json")
	if err == nil {
		t.Error("expected error for malformed wrapper")
	}
}

func TestParseSSELineMalformedBody(t *testing.T) {
	_, err := ParseSSELine(`data: {"body": "not json"}`)
	if err == nil {
		t.Error("expected error for malformed inner JSON")
	}
}

func TestParseSSELineUpstreamErrorWrapper(t *testing.T) {
	line := `data: {"headers":{"Content-Type":["application/json"]},"body":"{\"code\":\"101\",\"message\":\"Signature invalid\"}","statusCodeValue":403,"statusCode":"FORBIDDEN"}`
	events, err := ParseSSELine(line)
	if err == nil {
		t.Fatal("expected upstream API error")
	}
	if len(events) != 0 {
		t.Fatalf("events length = %d, want 0", len(events))
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error type = %T, want *APIError", err)
	}
	if apiErr.StatusCode != http.StatusForbidden {
		t.Errorf("status code = %d, want 403", apiErr.StatusCode)
	}
	if apiErr.Code != "101" {
		t.Errorf("code = %q, want 101", apiErr.Code)
	}
	if apiErr.Message != "Signature invalid" {
		t.Errorf("message = %q, want Signature invalid", apiErr.Message)
	}
	if got := apiErr.Error(); got != "Qoder upstream error 101: Signature invalid" {
		t.Errorf("error = %q, want Qoder upstream error 101: Signature invalid", got)
	}
}

func TestParseSSELineUpstreamErrorWrapperUsesStatusCodeNameFallback(t *testing.T) {
	line := `data: {"headers":{"Content-Type":["application/json"]},"body":"{\"code\":\"101\",\"message\":\"Signature invalid\"}","statusCode":"FORBIDDEN"}`
	_, err := ParseSSELine(line)
	if err == nil {
		t.Fatal("expected upstream API error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error type = %T, want *APIError", err)
	}
	if apiErr.StatusCode != http.StatusForbidden {
		t.Fatalf("status code = %d, want 403", apiErr.StatusCode)
	}
	if apiErr.Code != "101" {
		t.Fatalf("code = %q, want 101", apiErr.Code)
	}
}

func TestParseAPIErrorBodyRedactsSensitiveFields(t *testing.T) {
	body := `{"code":"101","message":"Authorization: Bearer secret-token securityOauthToken=dt-secret refreshToken=drt-secret cookie=session=abc uid=user-secret aid=provider-secret","data":{"securityOauthToken":"nested-secret","refreshToken":"nested-refresh","uid":"nested-user","aid":"nested-account"}}`
	apiErr := ParseAPIErrorBody(http.StatusForbidden, body)

	if strings.Contains(apiErr.Body, "secret-token") ||
		strings.Contains(apiErr.Body, "dt-secret") ||
		strings.Contains(apiErr.Body, "drt-secret") ||
		strings.Contains(apiErr.Body, "session=abc") ||
		strings.Contains(apiErr.Body, "user-secret") ||
		strings.Contains(apiErr.Body, "account-secret") ||
		strings.Contains(apiErr.Body, "nested-secret") ||
		strings.Contains(apiErr.Body, "nested-refresh") ||
		strings.Contains(apiErr.Body, "nested-user") ||
		strings.Contains(apiErr.Body, "nested-account") ||
		strings.Contains(apiErr.Message, "secret-token") ||
		strings.Contains(apiErr.Message, "dt-secret") ||
		strings.Contains(apiErr.Message, "drt-secret") ||
		strings.Contains(apiErr.Message, "session=abc") ||
		strings.Contains(apiErr.Message, "user-secret") ||
		strings.Contains(apiErr.Message, "account-secret") {
		t.Fatalf("sensitive value leaked: body=%s message=%s", apiErr.Body, apiErr.Message)
	}
	if apiErr.Code != "101" {
		t.Fatalf("code = %q, want 101", apiErr.Code)
	}
}

func TestRedactSensitiveTextPreservesQoderNumericErrorCodes(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		contains string
	}{
		{
			name:     "json string code",
			input:    `{"code":"115","message":"securityOauthToken=secret"}`,
			contains: `"code":"115"`,
		},
		{
			name:     "json numeric code",
			input:    `{"code":115,"message":"securityOauthToken=secret"}`,
			contains: `"code":115`,
		},
		{
			name:     "plain equals code",
			input:    `code=115 securityOauthToken=secret`,
			contains: `code=115`,
		},
		{
			name:     "plain colon code",
			input:    `code: 115 securityOauthToken=secret`,
			contains: `code: 115`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			redacted := RedactSensitiveText(tt.input)
			if !strings.Contains(redacted, tt.contains) {
				t.Fatalf("redacted = %q, want to contain %q", redacted, tt.contains)
			}
			if strings.Contains(redacted, "secret") {
				t.Fatalf("secret leaked: %q", redacted)
			}
		})
	}
}

func TestRedactSensitiveTextRedactsCNAccessToken(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{
			name:  "JSON 字段",
			input: `{"token":"cn-json-secret","message":"invalid"}`,
		},
		{
			name:  "非结构化等号字段",
			input: `token=cn-plain-secret message=invalid`,
		},
		{
			name:  "非结构化冒号字段",
			input: `token: cn-colon-secret`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			redacted := RedactSensitiveText(tt.input)
			if strings.Contains(redacted, "secret") {
				t.Fatalf("secret leaked: %q", redacted)
			}
			if !strings.Contains(redacted, "***") {
				t.Fatalf("redacted = %q, want redaction marker", redacted)
			}
		})
	}
}

func TestParseSSELineAgentLimitError(t *testing.T) {
	line := `data: {"headers":{"Content-Type":["application/json"]},"body":"{\"code\":\"115\",\"message\":\"{\\\"agentLimitResetTime\\\":1783841289162}\"}","statusCodeValue":429,"statusCode":"TOO_MANY_REQUESTS"}`
	_, err := ParseSSELine(line)
	if err == nil {
		t.Fatal("expected upstream API error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error type = %T, want *APIError", err)
	}
	if apiErr.Code != "115" {
		t.Fatalf("code = %q, want 115", apiErr.Code)
	}
	if apiErr.AgentLimitResetTime != 1783841289162 {
		t.Fatalf("agentLimitResetTime = %d", apiErr.AgentLimitResetTime)
	}
	resetAt, ok := apiErr.AgentLimitResetAt()
	if !ok {
		t.Fatal("expected reset time")
	}
	if got := resetAt.In(time.FixedZone("Asia/Shanghai", 8*60*60)).Format("2006-01-02 15:04:05"); got != "2026-07-12 15:28:09" {
		t.Fatalf("reset time = %s", got)
	}
	if got := apiErr.Error(); got != "Qoder agent limit reached; resets at 2026-07-12 15:28:09 Asia/Shanghai" {
		t.Fatalf("error = %q", got)
	}
}

func TestParseSSELineAgentLimitErrorNumericCode(t *testing.T) {
	line := `data: {"headers":{"Content-Type":["application/json"]},"body":"{\"code\":115,\"message\":\"agent limited\",\"agentLimitResetTime\":1783841289162,\"securityOauthToken\":\"secret\"}","statusCodeValue":429,"statusCode":"TOO_MANY_REQUESTS"}`
	_, err := ParseSSELine(line)
	if err == nil {
		t.Fatal("expected upstream API error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error type = %T, want *APIError", err)
	}
	if apiErr.Code != "115" {
		t.Fatalf("code = %q, want 115", apiErr.Code)
	}
	if !apiErr.IsAgentLimit() {
		t.Fatal("expected numeric code=115 to be treated as agent limit")
	}
	if apiErr.AgentLimitResetTime != 1783841289162 {
		t.Fatalf("agentLimitResetTime = %d", apiErr.AgentLimitResetTime)
	}
	if strings.Contains(apiErr.Body, "secret") {
		t.Fatalf("secret leaked in body: %s", apiErr.Body)
	}
	if !strings.Contains(apiErr.Body, `"code":115`) {
		t.Fatalf("body = %q, want numeric code preserved", apiErr.Body)
	}
}

func TestParseAPIErrorBodyAgentLimitDirectBody(t *testing.T) {
	apiErr := ParseAPIErrorBody(429, `{"agentLimitResetTime":1783841289162}`)
	if apiErr.AgentLimitResetTime != 1783841289162 {
		t.Fatalf("agentLimitResetTime = %d", apiErr.AgentLimitResetTime)
	}
}

func TestParseAPIErrorBodyAgentLimitNumericCode(t *testing.T) {
	apiErr := ParseAPIErrorBody(429, `{"code":115,"message":"agent limited","securityOauthToken":"secret"}`)
	if apiErr.Code != "115" {
		t.Fatalf("code = %q, want 115", apiErr.Code)
	}
	if !apiErr.IsAgentLimit() {
		t.Fatal("expected numeric code=115 to be treated as agent limit")
	}
	if strings.Contains(apiErr.Body, "secret") || strings.Contains(apiErr.Message, "secret") {
		t.Fatalf("secret leaked: body=%s message=%s", apiErr.Body, apiErr.Message)
	}
	if !strings.Contains(apiErr.Body, `"code":115`) {
		t.Fatalf("body = %q, want numeric code preserved", apiErr.Body)
	}
}
