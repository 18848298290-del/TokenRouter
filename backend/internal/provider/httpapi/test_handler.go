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

// TestHandler binds probe inputs, SSE output, and recovery after a successful test.
type TestHandler struct {
	tests   *provider.TestService
	recover func(context.Context, int64) error
}

// TestProviderRequest is the request body for a provider connection test.
type TestProviderRequest struct {
	SystemOne json.RawMessage `json:"systemone"`
	ModelID   string          `json:"model_id"`
	Prompt    string          `json:"prompt"`
	Mode      string          `json:"mode"`
	// Protocol selects Responses or Chat for OpenAI, or an enabled native protocol for CN platforms, for this text test.
	Protocol string `json:"protocol"`
	// TestType selects a text, image, or decision test.
	TestType string `json:"test_type"`
	// TestMode is the older field name and has lower priority than test_type.
	TestMode string `json:"test_mode"`
}

func NewTestHandler(tests *provider.TestService, recover func(context.Context, int64) error) *TestHandler {
	return &TestHandler{tests: tests, recover: recover}
}

// Test handles testing provider connectivity with SSE streaming
// POST /api/v1/admin/providers/:id/test
func (h *TestHandler) Test(c *gin.Context) {
	providerID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid provider ID")
		return
	}

	var req TestProviderRequest
	// An empty body uses platform defaults; malformed JSON is rejected before an upstream request.
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		response.BadRequest(c, "Invalid test request JSON")
		return
	}

	// The shared test service writes SSE events synchronously through the HTTP sink.
	testType := req.TestType
	if testType == "" {
		testType = req.TestMode
	}
	if err := h.tests.Test(c.Request.Context(), provider.TestRequest{SystemOne: req.SystemOne, ProviderID: providerID, Model: req.ModelID, Prompt: req.Prompt, Mode: req.Mode, Type: &testType, Protocol: req.Protocol, UserAgent: c.GetHeader("User-Agent"), Originator: c.GetHeader("originator")}, NewTestEventSink(c.Writer)); err != nil {
		// The test service handles failure events; stop before running recovery.
		return
	}

	if h.recover != nil {
		if err := h.recover(c.Request.Context(), providerID); err != nil {
			_ = c.Error(err)
		}
	}
}
