package httpx

import (
	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
)

// Authentication and access checks return a string code alongside a readable
// message. Error text follows an injected request language or negotiated browser
// language, then Gin's chain is aborted so subsequent middleware handlers do
// not continue the rejected request.

// ErrorResponse carries a string error code and message.
type ErrorResponse struct {
	Code    string `json:"code"`    // string-form response error code
	Message string `json:"message"` // response error message text
}

// NewErrorResponse pairs the supplied string code and message for JSON encoding.
func NewErrorResponse(code, message string) ErrorResponse {
	return ErrorResponse{
		Code:    code,
		Message: message,
	}
}

// AbortWithError writes a localized string-code error and aborts remaining Gin handlers.
// Stored request language takes precedence over Accept-Language negotiation;
// absent requests use the default language. The caller returns after invocation
// when later statements would otherwise write another response.
func AbortWithError(c *gin.Context, statusCode int, code, message string) {
	language := locale.Default()
	if c.Request != nil {
		var selected bool
		language, selected = locale.Explicit(c.Request.Context())
		// A stored language already represents this request's chosen presentation.
		// Negotiate the header only when that choice is absent so it remains authoritative.
		if !selected {
			language = locale.Negotiate(c.GetHeader("Accept-Language"), locale.Default())
		}
	}
	c.JSON(statusCode, NewErrorResponse(code, locale.ErrorText(language, code, statusCode, message)))
	c.Abort()
}
