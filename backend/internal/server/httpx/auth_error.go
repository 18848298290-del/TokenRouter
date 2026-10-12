package httpx

import (
	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
)

// ErrorResponse carries a string error code and message.
type ErrorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// NewErrorResponse creates an error code/message pair.
func NewErrorResponse(code, message string) ErrorResponse {
	return ErrorResponse{
		Code:    code,
		Message: message,
	}
}

// AbortWithError writes a localized JSON error and aborts the Gin handler chain.
func AbortWithError(c *gin.Context, statusCode int, code, message string) {
	language := locale.Default()
	if c.Request != nil {
		var selected bool
		language, selected = locale.Explicit(c.Request.Context())
		if !selected {
			language = locale.Negotiate(c.GetHeader("Accept-Language"), locale.Default())
		}
	}
	c.JSON(statusCode, NewErrorResponse(code, locale.ErrorText(language, code, statusCode, message)))
	c.Abort()
}
