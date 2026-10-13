package httpapi

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// HTTP Responses tool outputs need enough surrounding input to associate
// results with their originating calls. Validation accepts supplied
// continuation or call context, otherwise requires call IDs and matching item
// references before writing an OpenAI invalid-request response.

// ValidateOpenAIFunctionCallOutput reports whether HTTP tool outputs pass its
// call-context checks. Requests without function_call_output pass immediately.
// A nonblank previous_response_id or explicit tool-call context also passes;
// remaining outputs need call IDs and matching item_reference entries, with
// failures logged and written as HTTP 400.
func ValidateOpenAIFunctionCallOutput(c *gin.Context, body []byte, reqLog *zap.Logger) bool {
	if !gjson.GetBytes(body, `input.#(type=="function_call_output")`).Exists() {
		return true
	}

	validation := openai.ValidateFunctionCallOutputContextBytes(body)
	if !validation.HasFunctionCallOutput {
		return true
	}

	previousResponseID := gjson.GetBytes(body, "previous_response_id").String()
	// Continuation or inline tool-call context short-circuits standalone
	// output checks. Keep the supplied input intact for subsequent upstream
	// handling while skipping the later call-ID and item-reference guards.
	if strings.TrimSpace(previousResponseID) != "" || validation.HasToolCallContext {
		return true
	}

	if validation.HasFunctionCallOutputMissingCallID {
		reqLog.Warn("openai.request_validation_failed",
			zap.String("reason", "function_call_output_missing_call_id"),
		)
		DefaultOpenAIErrorOutput().WriteError(c, http.StatusBadRequest, "invalid_request_error", "function_call_output requires call_id on HTTP requests; continuation via previous_response_id is only supported on Responses WebSocket v2")
		return false
	}
	if validation.HasItemReferenceForAllCallIDs {
		return true
	}

	reqLog.Warn("openai.request_validation_failed",
		zap.String("reason", "function_call_output_missing_item_reference"),
	)
	DefaultOpenAIErrorOutput().WriteError(c, http.StatusBadRequest, "invalid_request_error", "function_call_output requires item_reference ids matching each call_id on HTTP requests; continuation via previous_response_id is only supported on Responses WebSocket v2")
	return false
}
