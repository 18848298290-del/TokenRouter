package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// test_handler.go binds provider connection-test requests and streams their
// results before optional recovery.
// Provider connection tests accept an empty body as a default request and
// stream events through the shared TestService. The HTTP wrapper prefers
// test_type over its legacy test_mode alias and starts recovery only when the
// test returns success; recovery errors are attached to Gin after event output.

// TestHandler binds provider-test requests to synchronous SSE output and
// optional success recovery.
type TestHandler struct {
	tests *provider.TestService // borrowed connection test service
	// recover runs only after a nil test result. Nil skips maintenance; errors
	// are attached to Gin after streaming and cannot replace the emitted response.
	recover func(context.Context, int64) error // optional successful-test recovery callback
}

// TestProviderRequest carries structured decision input and provider-specific
// connection-test choices.
type TestProviderRequest struct {
	// GroupID selects the group model attribute profile. Omission uses catalogue
	// defaults, and negative identifiers are rejected before streaming begins.
	GroupID int64 `json:"group_id" binding:"gte=0"`
	// SystemOne retains the supplied JSON bytes for decision preparation.
	// TestService restricts any nonempty value, including JSON null, to Jev targets.
	SystemOne json.RawMessage `json:"systemone"` // structured decision test input
	// ModelID is forwarded to the selected target for mapping and default model
	// selection. Blank input therefore follows that platform's test-model rule.
	ModelID string `json:"model_id"` // requested test model identifier
	// Prompt is forwarded to the selected target, whose text or image executor
	// chooses its empty-prompt payload. OpenAI compaction modes construct fixed
	// input independently of this supplied prompt.
	Prompt string `json:"prompt"` // requested test prompt text
	// Mode recognizes default, compact and legacy_compact after normalization.
	// Legacy text/image values can select kind, while an explicit image kind
	// forces the normalized mode to default before dispatch.
	Mode string `json:"mode"` // OpenAI connection test mode
	// Protocol selects a supported text endpoint for the current provider.
	// TestService validates platform and enabled-protocol eligibility before an
	// upstream test is dispatched.
	Protocol string `json:"protocol"` // requested text endpoint selector
	// TestType takes precedence over TestMode when its raw string is nonempty.
	// TestService trims and normalizes the selection: unknown nonblank kinds
	// become explicit text tests, while blank kinds permit compatibility selection.
	TestType string `json:"test_type"` // selected test operation kind
	// TestMode supplies the older kind spelling when TestType is exactly empty.
	// It follows the same trimming and normalization rules, including unknown
	// nonblank values selecting an explicit text test.
	TestMode string `json:"test_mode"` // legacy test operation selector
}

// NewTestHandler borrows provider testing and an optional successful-test
// recovery callback. Keep both dependencies available through mounted requests;
// nil recovery skips post-test maintenance while the test service remains
// required.
func NewTestHandler(tests *provider.TestService, recover func(context.Context, int64) error) *TestHandler {
	return &TestHandler{tests: tests, recover: recover}
}

// Test binds a decimal provider ID and optional JSON body before synchronous
// SSE execution. EOF admits platform defaults, malformed JSON returns 400, and
// a nonempty test_type takes precedence over test_mode. A failed test returns
// before recovery.
//
// Successful tests can invoke the optional recovery callback with the HTTP
// context. Recovery errors are recorded on Gin after streaming has completed,
// so they do not replace already emitted test events. Protocol and test-kind
// eligibility, upstream execution and event failures remain with the shared
// test service.
// POST /api/v1/admin/providers/:id/test
func (h *TestHandler) Test(c *gin.Context) {
	providerID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid provider ID")
		return
	}

	var req TestProviderRequest
	// EOF leaves the request at its zero values for platform default selection.
	// Other binding failures stop before the service can prepare an upstream target.
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		response.BadRequest(c, "Invalid test request JSON")
		return
	}

	// Older callers use test_mode for the same operation selection. Resolve its
	// lower-priority value before the shared test service validates and dispatches the request.
	testType := req.TestType
	if testType == "" {
		testType = req.TestMode
	}
	if err := h.tests.Test(c.Request.Context(), provider.TestRequest{GroupID: req.GroupID, SystemOne: req.SystemOne, ProviderID: providerID, Model: req.ModelID, Prompt: req.Prompt, Mode: req.Mode, Type: &testType, Protocol: req.Protocol, UserAgent: c.GetHeader("User-Agent"), Originator: c.GetHeader("originator")}, NewTestEventSink(c.Writer)); err != nil {
		// A failed test cannot justify restoring provider health. Return before
		// recovery even when the service has already emitted partial output or a failure event.
		return
	}

	if h.recover != nil {
		if err := h.recover(c.Request.Context(), providerID); err != nil {
			// Streaming has already produced the response. Attach the recovery failure
			// for Gin observers; Error returns the registered object and offers no failure result.
			_ = c.Error(err)
		}
	}
}
