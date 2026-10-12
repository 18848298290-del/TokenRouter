package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

const (
	TestRouteClaude TestRoute = iota
	TestRouteCNAdaptive
	TestRouteCNResponses
	TestRouteCNChat
	TestRouteCNAnthropic
	TestRouteOpenAI
	TestRouteGemini
	TestRouteGrok
	TestRouteAntigravity
	TestRouteQoder
	TestRouteJev

	ProviderTestTypeDecision      = "decision"
	ProviderTestTypeText          = "text"
	ProviderTestTypeImage         = "image"
	ProviderTestModeDefault       = "default"
	ProviderTestModeCompact       = "compact"
	ProviderTestModeLegacyCompact = "legacy_compact"
)

// TestEvent is an SSE event emitted during provider testing.
type TestEvent struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Model    string `json:"model,omitempty"`
	Status   string `json:"status,omitempty"`
	Code     string `json:"code,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
	Data     any    `json:"data,omitempty"`
	Success  bool   `json:"success,omitempty"`
	Error    string `json:"error,omitempty"`
}

// TestRequest describes a provider test. When Type and Mode omit a test type,
// the execution adapter infers it.
type TestRequest struct {
	// SystemOne holds the state and questions for a decision test.
	SystemOne json.RawMessage

	ProviderID            int64
	Model, Prompt, Mode   string
	Type                  *string
	Protocol              string
	UserAgent, Originator string
	Automatic             bool
}
type TestRoute uint8

type PreparedTestRequest struct {
	TestRequest
	TestType     string
	ExplicitType bool
	Route        TestRoute
}
type TestTargetInfo struct {
	ProviderSnapshot
	APIProtocol string
}

// TestTarget executes one test and exposes provider metadata without credentials.
type TestTarget interface {
	Information() TestTargetInfo
	Execute(context.Context, PreparedTestRequest, TestEventSink) error
}
type TestLoader interface {
	LoadTestTarget(context.Context, TestRequest) (TestTarget, error)
}

// TestEventSink receives test output. Begin commits metadata when its boolean argument is true; the adapter
// handles HTTP headers and flushing.
type TestEventSink interface {
	Begin(context.Context, bool) error
	Emit(context.Context, TestEvent) error
}
type TestOptions struct {
	Now        func() time.Time
	Error      func(string)
	WriteError func(error)
}
type TestService struct {
	loader  TestLoader
	options TestOptions
}

// testGuardedSink cancels execution on the first write failure and returns that error on later writes.
type testGuardedSink struct {
	mu     sync.Mutex
	next   TestEventSink
	cancel context.CancelFunc
	err    error
}

// testResultSink collects background events through JSON to normalize invalid UTF-8 and drops unencodable events.
type testResultSink struct {
	mu    sync.Mutex
	texts []string
	err   string
}

// NormalizeProviderTestType normalizes known types and defaults empty or unknown values to text.
func NormalizeProviderTestType(testType string) string {
	switch strings.ToLower(strings.TrimSpace(testType)) {
	case ProviderTestTypeDecision:
		return ProviderTestTypeDecision
	case ProviderTestTypeImage:
		return ProviderTestTypeImage
	case ProviderTestTypeText:
		return ProviderTestTypeText
	default:
		return ProviderTestTypeText
	}
}

// ProviderTestTypeFromArgs returns the normalized type and whether a nonempty argument was supplied.
// An omitted or blank argument returns text with false so callers can apply type inference.
func ProviderTestTypeFromArgs(testTypes ...string) (string, bool) {
	if len(testTypes) == 0 || strings.TrimSpace(testTypes[0]) == "" {
		return ProviderTestTypeText, false
	}
	return NormalizeProviderTestType(testTypes[0]), true
}

// ResolveProviderTestModeAndType accepts legacy calls with text or image in the mode argument.
func ResolveProviderTestModeAndType(mode string, testTypes ...string) (string, string, bool) {
	// Also accept arguments supplied in testType, mode order.
	if len(testTypes) > 0 {
		rawMode := strings.ToLower(strings.TrimSpace(mode))
		rawTypeOrMode := strings.ToLower(strings.TrimSpace(testTypes[0]))
		if (rawMode == ProviderTestTypeText || rawMode == ProviderTestTypeImage) &&
			(rawTypeOrMode == ProviderTestModeDefault || rawTypeOrMode == ProviderTestModeCompact || rawTypeOrMode == ProviderTestModeLegacyCompact) {
			return NormalizeProviderTestMode(testTypes[0]), NormalizeProviderTestType(mode), true
		}
	}
	testType, explicit := ProviderTestTypeFromArgs(testTypes...)
	normalizedMode := NormalizeProviderTestMode(mode)
	if !explicit {
		switch strings.ToLower(strings.TrimSpace(mode)) {
		case ProviderTestTypeText, ProviderTestTypeImage:
			return ProviderTestModeDefault, NormalizeProviderTestType(mode), true
		}
	}
	if !explicit {
		testType = ""
	}
	return normalizedMode, testType, explicit
}

func NormalizeProviderTestMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case ProviderTestModeCompact:
		return ProviderTestModeCompact
	case ProviderTestModeLegacyCompact:
		return ProviderTestModeLegacyCompact
	default:
		return ProviderTestModeDefault
	}
}

func NewTestService(loader TestLoader, options TestOptions) *TestService {
	return &TestService{loader: loader, options: options}
}

func (s *TestService) Test(ctx context.Context, request TestRequest, sink TestEventSink) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	guarded := &testGuardedSink{next: sink, cancel: cancel}
	err := s.execute(runCtx, request, guarded)
	if writeErr := guarded.failure(); writeErr != nil && !errors.Is(err, writeErr) {
		return errors.Join(err, writeErr)
	}
	return err
}

func (s *TestService) execute(ctx context.Context, request TestRequest, sink TestEventSink) error {
	if request.Protocol != "" && TestProtocolID(request.Protocol) == "" {
		return s.fail(ctx, sink, "Invalid test protocol")
	}
	var types []string
	if request.Type != nil {
		types = []string{*request.Type}
	}
	mode, kind, explicit := ResolveProviderTestModeAndType(request.Mode, types...)
	if explicit && kind == ProviderTestTypeImage {
		mode = ProviderTestModeDefault
	}
	target, err := s.loader.LoadTestTarget(ctx, request)
	if err != nil {
		return s.fail(ctx, sink, "Provider not found")
	}
	info := target.Information()
	if (kind == ProviderTestTypeDecision || len(request.SystemOne) > 0) && info.Platform != PlatformJev {
		return s.fail(ctx, sink, "Decision tests require a Jev provider")
	}
	if explicit && kind == ProviderTestTypeImage && info.Platform != PlatformOpenAI && info.Platform != PlatformGemini && info.Platform != PlatformGrok {
		return s.fail(ctx, sink, fmt.Sprintf("Image tests are not supported for platform %s", info.Platform))
	}
	if request.Protocol != "" && kind != ProviderTestTypeImage && !testProtocolAllowed(info, request.Protocol) {
		return s.fail(ctx, sink, fmt.Sprintf("Test protocol %s is not supported for this provider", request.Protocol))
	}
	prepared := PreparedTestRequest{TestRequest: request, TestType: kind, ExplicitType: explicit}
	prepared.Mode = mode
	switch info.Platform {
	case PlatformKimi, PlatformZhipu, PlatformDeepseek:
		switch info.APIProtocol {
		case APIProtocolAdaptive:
			prepared.Route = TestRouteCNAdaptive
		case APIProtocolResponses:
			prepared.Route = TestRouteCNResponses
		case APIProtocolChatCompletions:
			prepared.Route = TestRouteCNChat
		default:
			prepared.Route = TestRouteCNAnthropic
		}
	case PlatformOpenAI:
		prepared.Route = TestRouteOpenAI
	case PlatformGemini:
		prepared.Route = TestRouteGemini
	case PlatformGrok:
		prepared.Route = TestRouteGrok
	case PlatformAntigravity:
		prepared.Route = TestRouteAntigravity
	case PlatformJev:
		prepared.Route = TestRouteJev
	case PlatformQoder:
		prepared.Route = TestRouteQoder
	default:
		prepared.Route = TestRouteClaude
	}
	return target.Execute(ctx, prepared, sink)
}

// TestProtocolID maps an admin test protocol to its native ID, returning an empty string for unknown values.
func TestProtocolID(protocol string) capability.ProtocolID {
	switch protocol {
	case APIProtocolSystemOne:
		return capability.ProtocolSystemOne
	case APIProtocolAnthropic:
		return capability.ProtocolAnthropicMessages
	case APIProtocolResponses:
		return capability.ProtocolOpenAIResponses
	case APIProtocolChatCompletions:
		return capability.ProtocolOpenAIChatCompletions
	default:
		return ""
	}
}

// testProtocolAllowed reports whether a test may select the requested protocol.
// OpenAI API keys allow Responses or Chat; other OpenAI types allow Responses.
// CN platforms allow enabled protocols; other platforms reject explicit protocol selection.
func testProtocolAllowed(info TestTargetInfo, protocol string) bool {
	switch info.Platform {
	case PlatformJev:
		return protocol == APIProtocolSystemOne && slices.Contains(info.EnabledProtocols, capability.ProtocolSystemOne)
	case PlatformOpenAI:
		if info.Type == ProviderTypeAPIKey {
			return protocol == APIProtocolResponses || protocol == APIProtocolChatCompletions
		}
		return protocol == APIProtocolResponses
	case PlatformKimi, PlatformZhipu, PlatformDeepseek:
		return slices.Contains(info.EnabledProtocols, TestProtocolID(protocol))
	default:
		return false
	}
}

func (s *TestService) fail(ctx context.Context, sink TestEventSink, message string) error {
	if s.options.Error != nil {
		s.options.Error(message)
	}
	if err := sink.Emit(ctx, TestEvent{Type: "error", Error: message}); err != nil && s.options.WriteError != nil {
		s.options.WriteError(err)
	}
	return errors.New(message)
}

func (s *testGuardedSink) apply(write func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	if err := write(); err != nil {
		s.err = err
		s.cancel()
	}
	return s.err
}

func (s *testGuardedSink) Begin(ctx context.Context, commit bool) error {
	return s.apply(func() error { return s.next.Begin(ctx, commit) })
}

func (s *testGuardedSink) Emit(ctx context.Context, event TestEvent) error {
	return s.apply(func() error { return s.next.Emit(ctx, event) })
}
func (s *testGuardedSink) failure() error { s.mu.Lock(); defer s.mu.Unlock(); return s.err }
func (s *TestService) RunTestBackground(ctx context.Context, id int64, model string) (*ScheduledTestResult, error) {
	return s.RunTestBackgroundWithPromptAndUserAgent(ctx, id, model, "", "")
}

func (s *TestService) RunTestBackgroundWithPromptAndUserAgent(ctx context.Context, id int64, model, prompt, userAgent string) (*ScheduledTestResult, error) {
	started := s.options.Now()
	sink := &testResultSink{}
	testErr := s.Test(ctx, TestRequest{ProviderID: id, Model: model, Prompt: prompt, Mode: ProviderTestModeDefault, Automatic: true, UserAgent: userAgent}, sink)
	finished := s.options.Now()
	text, message := sink.result()
	status := "success"
	if testErr != nil || message != "" {
		status = "failed"
		if message == "" && testErr != nil {
			message = testErr.Error()
		}
	}
	return &ScheduledTestResult{Status: status, ResponseText: text, ErrorMessage: message, LatencyMs: finished.Sub(started).Milliseconds(), StartedAt: started, FinishedAt: finished}, nil
}

func (*testResultSink) Begin(context.Context, bool) error { return nil }
func (s *testResultSink) Emit(_ context.Context, event TestEvent) error {
	if event.Type != "content" && event.Type != "error" {
		return nil
	}
	raw, err := json.Marshal(event)
	if err != nil {
		return nil
	}
	var wire TestEvent
	if json.Unmarshal(raw, &wire) != nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch wire.Type {
	case "content":
		if wire.Text != "" {
			s.texts = append(s.texts, wire.Text)
		}
	case "error":
		s.err = wire.Error
	}
	return nil
}

func (s *testResultSink) result() (string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.texts, ""), s.err
}
