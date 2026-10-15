package httpapi

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/provider/googleforward"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
)

// google_executors.go creates request-local HTTP output adapters for Gemini
// and Antigravity runtime calls.
// Google platform preparation receives synchronous HTTP output for each
// attempt. Gemini and Antigravity retain their own runtime instances, while
// these entry adapters choose the client protocol and create the corresponding
// request-local error output before delegation.

// GeminiExecutor creates per-request synchronous output for shared Gemini request preparation.
type GeminiExecutor struct {
	Runtime *googleforward.Gemini // bound Gemini execution
}

// AntigravityExecutor connects Antigravity requests to HTTP output and error handling.
type AntigravityExecutor struct {
	Runtime *googleforward.Antigravity // bound Antigravity execution
}

// Forward delegates Messages execution to the bound Gemini runtime and its
// HTTP output. The caller retains provider selection and completion ownership,
// including any partial result returned with an error.
func (s *GeminiExecutor) Forward(ctx context.Context, c *gin.Context, a *gatewayadapter.ExecutionProvider, body []byte) (*forward.MessagesResult, error) {
	return s.Runtime.Forward(ctx, NewGoogleBoundary(c, s.Runtime.Options, false), a, body)
}

// ForwardNative delegates one Gemini model action with its explicit stream
// flag and body. The runtime uses Google-format output, and the caller
// interprets observed usage and any failure for completion.
func (s *GeminiExecutor) ForwardNative(ctx context.Context, c *gin.Context, a *gatewayadapter.ExecutionProvider, model, action string, stream bool, body []byte) (*forward.MessagesResult, error) {
	return s.Runtime.ForwardNative(ctx, NewGoogleBoundary(c, s.Runtime.Options, false), a, model, action, stream, body)
}

// ForwardAsResponses runs the selected provider through Gemini preparation for
// Responses output. The supplied HTTP context owns synchronous writes, and the
// runtime receives this attempt's original body and parsed request.
func (s *GeminiExecutor) ForwardAsResponses(ctx context.Context, c *gin.Context, a *gatewayadapter.ExecutionProvider, body []byte, parsed *requeststate.ParsedRequest) (*forward.MessagesResult, error) {
	return s.Runtime.ForwardAsResponses(ctx, NewGoogleBoundary(c, s.Runtime.Options, false), a, body, parsed)
}

// ForwardAsChatCompletions delegates this attempt to Gemini preparation with
// Chat output. The runtime performs conversion and response handling; the
// outer request loop interprets failures before any provider switch.
func (s *GeminiExecutor) ForwardAsChatCompletions(ctx context.Context, c *gin.Context, a *gatewayadapter.ExecutionProvider, body []byte) (*forward.MessagesResult, error) {
	return s.Runtime.ForwardAsChatCompletions(ctx, NewGoogleBoundary(c, s.Runtime.Options, false), a, body)
}

// Forward delegates Messages execution to the bound Antigravity runtime and
// its HTTP output. The caller retains provider selection and completion
// ownership, including any partial result returned with an error.
func (s *AntigravityExecutor) Forward(ctx context.Context, c *gin.Context, a *gatewayadapter.ExecutionProvider, body []byte, sticky bool) (*forward.MessagesResult, error) {
	return s.Runtime.Forward(ctx, NewGoogleBoundary(c, s.Runtime.Options, true), a, body, sticky)
}

// ForwardGemini delegates a Gemini-format attempt to Antigravity with sticky
// session state and optional Gemini session settings. Runtime output uses the
// native client format; outer orchestration owns provider switching and
// completion.
func (s *AntigravityExecutor) ForwardGemini(ctx context.Context, c *gin.Context, a *gatewayadapter.ExecutionProvider, model, action string, stream bool, body []byte, sticky bool, options ...forward.GeminiSessionOption) (*forward.MessagesResult, error) {
	return s.Runtime.ForwardGemini(ctx, NewGoogleBoundary(c, s.Runtime.Options, true), a, model, action, stream, body, sticky, options...)
}

// ForwardAsResponses runs the selected provider through Antigravity
// preparation for Responses output. The supplied HTTP context owns synchronous
// writes, and the runtime receives this attempt's original body and parsed
// request.
func (s *AntigravityExecutor) ForwardAsResponses(ctx context.Context, c *gin.Context, a *gatewayadapter.ExecutionProvider, body []byte, parsed *requeststate.ParsedRequest) (*forward.MessagesResult, error) {
	return s.Runtime.ForwardAsResponses(ctx, NewGoogleBoundary(c, s.Runtime.Options, true), a, body, parsed)
}

// ForwardAsChatCompletions delegates this attempt to Antigravity preparation
// with Chat output. The runtime performs conversion and response handling; the
// outer request loop interprets failures before any provider switch.
func (s *AntigravityExecutor) ForwardAsChatCompletions(ctx context.Context, c *gin.Context, a *gatewayadapter.ExecutionProvider, body []byte, parsed *requeststate.ParsedRequest) (*forward.MessagesResult, error) {
	return s.Runtime.ForwardAsChatCompletions(ctx, NewGoogleBoundary(c, s.Runtime.Options, true), a, body, parsed)
}

// WriteMappedClaudeError presents the supplied Antigravity upstream failure as
// a Messages error. It creates request-local output using runtime options,
// records diagnostics there and returns the presentation error to its caller.
func (s *AntigravityExecutor) WriteMappedClaudeError(c *gin.Context, a *gatewayadapter.ExecutionProvider, status int, id string, body []byte) error {
	return NewGoogleBoundary(c, s.Runtime.Options, true).MappedClaudeError(a, status, id, body)
}
