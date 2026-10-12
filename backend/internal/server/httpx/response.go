package httpx

import (
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
)

// Response is the panel API response envelope.
type Response struct {
	Code     int               `json:"code"`
	Message  string            `json:"message"`
	Reason   string            `json:"reason,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
	Data     any               `json:"data,omitempty"`
}

// PaginatedData is the frontend's paginated response data.
type PaginatedData struct {
	Items    any   `json:"items"`
	Total    int64 `json:"total"`
	Page     int   `json:"page"`
	PageSize int   `json:"page_size"`
	Pages    int   `json:"pages"`
}

// PaginationResult carries page counts compatible with pagination.PaginationResult.
type PaginationResult struct {
	Total    int64
	Page     int
	PageSize int
	Pages    int
}

// Success writes an HTTP 200 success envelope.
func Success(c *gin.Context, data any) {
	c.JSON(http.StatusOK, Response{
		Code:    0,
		Message: "success",
		Data:    data,
	})
}

// Created writes an HTTP 201 success envelope.
func Created(c *gin.Context, data any) {
	c.JSON(http.StatusCreated, Response{
		Code:    0,
		Message: "success",
		Data:    data,
	})
}

// Accepted writes an HTTP 202 acceptance envelope.
func Accepted(c *gin.Context, data any) {
	c.JSON(http.StatusAccepted, Response{
		Code:    0,
		Message: "accepted",
		Data:    data,
	})
}

// Error writes an error envelope with the supplied HTTP status.
func Error(c *gin.Context, statusCode int, message string) {
	ErrorWithDetails(c, statusCode, message, "", nil)
}

// ErrorWithDetails writes an error envelope with optional reason and metadata.
// User-facing routes receive a localized message.
func ErrorWithDetails(c *gin.Context, statusCode int, message, reason string, metadata map[string]string) {
	if c.Request != nil && !strings.HasPrefix(c.Request.URL.Path, "/api/v1/admin/") && !strings.HasPrefix(c.Request.URL.Path, "/setup/") {
		if reason == "" {
			reason = "HTTP_" + strconv.Itoa(statusCode)
		}
		language, exists := locale.Explicit(c.Request.Context())
		if !exists {
			language = locale.Negotiate(c.GetHeader("Accept-Language"), locale.Default())
		}
		message = locale.ErrorText(language, reason, statusCode, message)
	}
	c.JSON(statusCode, Response{
		Code:     statusCode,
		Message:  message,
		Reason:   reason,
		Metadata: metadata,
	})
}

// ErrorFrom maps an error to the API envelope and logs redacted server-error details.
// It returns true if an error was written.
func ErrorFrom(c *gin.Context, err error) bool {
	if err == nil {
		return false
	}

	statusCode, status := ToHTTP(err)

	// Redact internal error details before logging them.
	if statusCode >= 500 && c.Request != nil {
		log.Printf("[ERROR] %s %s\n  Error: %s", c.Request.Method, c.Request.URL.Path, logredact.RedactText(err.Error()))
	}

	ErrorWithDetails(c, statusCode, status.Message, status.Reason, status.Metadata)
	return true
}

// BadRequest writes an HTTP 400 error.
func BadRequest(c *gin.Context, message string) {
	Error(c, http.StatusBadRequest, message)
}

// Unauthorized writes an HTTP 401 error.
func Unauthorized(c *gin.Context, message string) {
	Error(c, http.StatusUnauthorized, message)
}

// Forbidden writes an HTTP 403 error.
func Forbidden(c *gin.Context, message string) {
	Error(c, http.StatusForbidden, message)
}

// NotFound writes an HTTP 404 error.
func NotFound(c *gin.Context, message string) {
	Error(c, http.StatusNotFound, message)
}

// InternalError writes an HTTP 500 error.
func InternalError(c *gin.Context, message string) {
	Error(c, http.StatusInternalServerError, message)
}

// Paginated writes page data and computes at least one page. pageSize must be positive.
func Paginated(c *gin.Context, items any, total int64, page, pageSize int) {
	pages := max(int(math.Ceil(float64(total)/float64(pageSize))), 1)

	Success(c, PaginatedData{
		Items:    items,
		Total:    total,
		Page:     page,
		PageSize: pageSize,
		Pages:    pages,
	})
}

// PaginatedWithResult writes page data; nil pagination uses page 1, size 20, and zero total.
func PaginatedWithResult(c *gin.Context, items any, pagination *PaginationResult) {
	if pagination == nil {
		Success(c, PaginatedData{
			Items:    items,
			Total:    0,
			Page:     1,
			PageSize: 20,
			Pages:    1,
		})
		return
	}

	Success(c, PaginatedData{
		Items:    items,
		Total:    pagination.Total,
		Page:     pagination.Page,
		PageSize: pagination.PageSize,
		Pages:    pagination.Pages,
	})
}

// ParsePagination reads page and page size, defaulting to 1 and 20; sizes above 1000 are ignored.
func ParsePagination(c *gin.Context) (page, pageSize int) {
	page = 1
	pageSize = 20

	if p := c.Query("page"); p != "" {
		if val, err := parseInt(p); err == nil && val > 0 {
			page = val
		}
	}

	// page_size takes precedence over the limit alias, including when its value is invalid.
	if ps := c.Query("page_size"); ps != "" {
		if val, err := parseInt(ps); err == nil && val > 0 && val <= 1000 {
			pageSize = val
		}
	} else if l := c.Query("limit"); l != "" {
		if val, err := parseInt(l); err == nil && val > 0 && val <= 1000 {
			pageSize = val
		}
	}

	return page, pageSize
}

func parseInt(s string) (int, error) {
	var result int
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, nil
		}
		result = result*10 + int(c-'0')
	}
	return result, nil
}
