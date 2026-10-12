package provider

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

type testLoaderStub struct {
	target  TestTarget
	err     error
	loads   int
	request TestRequest
}

type testTargetStub struct {
	info TestTargetInfo
	run  func(context.Context, PreparedTestRequest, TestEventSink) error
}

type testSinkStub struct {
	emitted []TestEvent
	begin   int
	err     error
}

func TestNormalizeProviderTestMode(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{input: "", want: ProviderTestModeDefault},
		{input: "default", want: ProviderTestModeDefault},
		{input: " compact ", want: ProviderTestModeCompact},
		{input: "COMPACT", want: ProviderTestModeCompact},
		{input: " legacy_compact ", want: ProviderTestModeLegacyCompact},
		{input: "unknown", want: ProviderTestModeDefault},
	}

	for _, tt := range tests {
		if got := NormalizeProviderTestMode(tt.input); got != tt.want {
			t.Fatalf("normalizeProviderTestMode(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// TestDecisionTestingRequiresJev checks that Jev receives structured decision input and other platforms reject it.
func TestDecisionTestingRequiresJev(t *testing.T) {
	for _, platform := range []string{PlatformJev, PlatformOpenAI} {
		t.Run(platform, func(t *testing.T) {
			called := false
			kind := ProviderTestTypeDecision
			request := TestRequest{Type: &kind, SystemOne: []byte(`{"state":["ready"],"questions":{"x":{"type":"noul","instructions":"Ready?"}}}`)}
			loader := &testLoaderStub{target: testTargetStub{info: TestTargetInfo{ProviderSnapshot: ProviderSnapshot{Platform: platform, Type: ProviderTypeAPIKey}}, run: func(_ context.Context, got PreparedTestRequest, _ TestEventSink) error {
				called = true
				require.Equal(t, TestRouteJev, got.Route)
				require.Equal(t, kind, got.TestType)
				require.Equal(t, request.SystemOne, got.SystemOne)
				return nil
			}}}
			err := NewTestService(loader, TestOptions{}).Test(context.Background(), request, &testSinkStub{})
			if platform == PlatformJev {
				require.NoError(t, err)
				require.True(t, called)
			} else {
				require.EqualError(t, err, "Decision tests require a Jev provider")
				require.False(t, called)
			}
		})
	}
}

func TestResolveProviderTestModeAndType(t *testing.T) {
	tests := []struct {
		name      string
		mode      string
		testTypes []string
		wantMode  string
		wantType  string
		explicit  bool
	}{
		{name: "explicit image", mode: "default", testTypes: []string{"image"}, wantMode: ProviderTestModeDefault, wantType: ProviderTestTypeImage, explicit: true},
		{name: "explicit text", mode: "default", testTypes: []string{"text"}, wantMode: ProviderTestModeDefault, wantType: ProviderTestTypeText, explicit: true},
		{name: "legacy compact", mode: "compact", wantMode: ProviderTestModeCompact, wantType: "", explicit: false},
		{name: "mode alias", mode: "image", wantMode: ProviderTestModeDefault, wantType: ProviderTestTypeImage, explicit: true},
		{name: "swapped new call", mode: "image", testTypes: []string{"compact"}, wantMode: ProviderTestModeCompact, wantType: ProviderTestTypeImage, explicit: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mode, testType, explicit := ResolveProviderTestModeAndType(tt.mode, tt.testTypes...)
			if mode != tt.wantMode || testType != tt.wantType || explicit != tt.explicit {
				t.Fatalf("resolveProviderTestModeAndType(%q, %#v) = (%q, %q, %v), want (%q, %q, %v)", tt.mode, tt.testTypes, mode, testType, explicit, tt.wantMode, tt.wantType, tt.explicit)
			}
		})
	}
}

func (l *testLoaderStub) LoadTestTarget(_ context.Context, request TestRequest) (TestTarget, error) {
	l.loads++
	l.request = request
	return l.target, l.err
}

func (t testTargetStub) Information() TestTargetInfo { return t.info }

func (t testTargetStub) Execute(ctx context.Context, request PreparedTestRequest, sink TestEventSink) error {
	return t.run(ctx, request, sink)
}

func (s *testSinkStub) Begin(context.Context, bool) error { s.begin++; return s.err }

func (s *testSinkStub) Emit(_ context.Context, event TestEvent) error {
	s.emitted = append(s.emitted, event)
	return s.err
}

// TestTestingValidatesBeforeLoadAndPreservesMissingProvider checks early protocol validation and sanitized
// load errors.
func TestTestingValidatesBeforeLoadAndPreservesMissingProvider(t *testing.T) {
	loader := &testLoaderStub{err: errors.New("private database error")}
	svc := NewTestService(loader, TestOptions{})
	sink := &testSinkStub{}
	require.EqualError(t, svc.Test(context.Background(), TestRequest{Protocol: " responses "}, sink), "Invalid test protocol")
	require.Zero(t, loader.loads)
	require.Zero(t, sink.begin)
	require.EqualError(t, svc.Test(context.Background(), TestRequest{ProviderID: 1}, sink), "Provider not found")
	require.Equal(t, 1, loader.loads)
	require.Equal(t, "Provider not found", sink.emitted[1].Error)
}

// TestTestingDispatchesExplicitImageWithoutDuplicateLoad checks that image type overrides Compact with one
// provider load.
func TestTestingDispatchesExplicitImageWithoutDuplicateLoad(t *testing.T) {
	var got PreparedTestRequest
	loader := &testLoaderStub{target: testTargetStub{info: TestTargetInfo{ProviderSnapshot: ProviderSnapshot{Platform: PlatformGemini, Type: ProviderTypeAPIKey}}, run: func(_ context.Context, req PreparedTestRequest, _ TestEventSink) error { got = req; return nil }}}
	svc := NewTestService(loader, TestOptions{})
	kind := ProviderTestTypeImage
	require.NoError(t, svc.Test(context.Background(), TestRequest{ProviderID: 9, Model: "explicit", Mode: ProviderTestModeCompact, Type: &kind}, &testSinkStub{}))
	require.Equal(t, 1, loader.loads)
	require.Equal(t, TestRouteGemini, got.Route)
	require.Equal(t, ProviderTestModeDefault, got.Mode)
	require.True(t, got.ExplicitType)
}

// TestTestingCancelsExecutionOnFirstWriteFailure checks cancellation and error reuse when an executor ignores
// a write failure.
func TestTestingCancelsExecutionOnFirstWriteFailure(t *testing.T) {
	failed := errors.New("sink failed")
	sink := &testSinkStub{err: failed}
	loader := &testLoaderStub{target: testTargetStub{info: TestTargetInfo{}, run: func(ctx context.Context, _ PreparedTestRequest, out TestEventSink) error {
		require.ErrorIs(t, out.Emit(ctx, TestEvent{Type: "content", Text: "one"}), failed)
		require.ErrorIs(t, ctx.Err(), context.Canceled)
		require.ErrorIs(t, out.Emit(ctx, TestEvent{Type: "content", Text: "two"}), failed)
		return nil
	}}}
	require.ErrorIs(t, NewTestService(loader, TestOptions{}).Test(context.Background(), TestRequest{}, sink), failed)
	require.Len(t, sink.emitted, 1)
}

// TestTestingBackgroundPreservesWireTextAndClock checks UTF-8 normalization, dropped unencodable events, and
// the last error.
func TestTestingBackgroundPreservesWireTextAndClock(t *testing.T) {
	start := time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC)
	clockCalls := 0
	loader := &testLoaderStub{target: testTargetStub{info: TestTargetInfo{}, run: func(ctx context.Context, _ PreparedTestRequest, sink TestEventSink) error {
		for _, event := range []TestEvent{
			{Type: "content", Text: "a\xff"},
			{Type: "content", Text: "ignored", Data: make(chan int)},
			{Type: "image", ImageURL: "ignored"},
			{Type: "error", Error: "earlier"},
			{Type: "error", Error: ""},
		} {
			require.NoError(t, sink.Emit(ctx, event))
		}
		return nil
	}}}
	svc := NewTestService(loader, TestOptions{Now: func() time.Time {
		at := start.Add(time.Duration(clockCalls) * 500 * time.Millisecond)
		clockCalls++
		return at
	}})
	result, err := svc.RunTestBackgroundWithPromptAndUserAgent(context.Background(), 1, "model", "prompt", "probe/1.0")
	require.NoError(t, err)
	require.Equal(t, "success", result.Status)
	require.Equal(t, "a\ufffd", result.ResponseText)
	require.Empty(t, result.ErrorMessage)
	require.Equal(t, int64(500), result.LatencyMs)
	require.Equal(t, start, result.StartedAt)
	require.True(t, loader.request.Automatic)
	require.Equal(t, "probe/1.0", loader.request.UserAgent)
}

// TestTestingRejectsProtocolOutsideProviderCapability checks text protocol restrictions and separate image
// eligibility.
func TestTestingRejectsProtocolOutsideProviderCapability(t *testing.T) {
	cases := []struct {
		name     string
		snapshot ProviderSnapshot
		protocol string
		allowed  bool
	}{
		{"OpenAI API Key 可选 Chat", ProviderSnapshot{Platform: PlatformOpenAI, Type: ProviderTypeAPIKey}, APIProtocolChatCompletions, true},
		{"OpenAI API Key 不支持 Messages", ProviderSnapshot{Platform: PlatformOpenAI, Type: ProviderTypeAPIKey}, APIProtocolAnthropic, false},
		{"OpenAI OAuth 只有 Responses", ProviderSnapshot{Platform: PlatformOpenAI, Type: ProviderTypeOAuth}, APIProtocolChatCompletions, false},
		{"国产平台已启用 Messages", ProviderSnapshot{Platform: PlatformZhipu, Type: ProviderTypeAPIKey, EnabledProtocols: []capability.ProtocolID{capability.ProtocolAnthropicMessages}}, APIProtocolAnthropic, true},
		{"国产平台未启用 Responses", ProviderSnapshot{Platform: PlatformKimi, Type: ProviderTypeAPIKey, EnabledProtocols: []capability.ProtocolID{capability.ProtocolAnthropicMessages}}, APIProtocolResponses, false},
		{"其他平台不接受显式协议", ProviderSnapshot{Platform: PlatformAnthropic, Type: ProviderTypeAPIKey}, APIProtocolAnthropic, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			executed := false
			loader := &testLoaderStub{target: testTargetStub{info: TestTargetInfo{ProviderSnapshot: tc.snapshot}, run: func(context.Context, PreparedTestRequest, TestEventSink) error {
				executed = true
				return nil
			}}}
			err := NewTestService(loader, TestOptions{}).Test(context.Background(), TestRequest{ProviderID: 1, Protocol: tc.protocol}, &testSinkStub{})
			require.Equal(t, tc.allowed, err == nil)
			require.Equal(t, tc.allowed, executed)
		})
	}

	kind := ProviderTestTypeImage
	loader := &testLoaderStub{target: testTargetStub{info: TestTargetInfo{ProviderSnapshot: ProviderSnapshot{Platform: PlatformGemini, Type: ProviderTypeAPIKey}}, run: func(context.Context, PreparedTestRequest, TestEventSink) error { return nil }}}
	require.NoError(t, NewTestService(loader, TestOptions{}).Test(context.Background(), TestRequest{ProviderID: 1, Type: &kind, Protocol: APIProtocolResponses}, &testSinkStub{}))
}
