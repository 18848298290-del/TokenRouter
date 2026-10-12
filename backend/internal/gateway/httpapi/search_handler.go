package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/gateway/searchtools"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// grokStandaloneSearchRequest gives JSON type errors an HTTP request type name; execution receives a standalone value.
type grokStandaloneSearchRequest searchtools.StandaloneRequest

type SearchAccess struct {
	GroupPresent bool
	Platform     string
	GroupID      *int64
}
type SearchHTTPFailure struct {
	Status        int
	Code, Message string
	RetryAfter    int
}

// SearchHTTPRun uses the selected platform adapter and captures completion data before queueing.
type SearchHTTPRun interface {
	searchtools.StandalonePorts
	Complete(*gin.Context, searchtools.StandaloneRequest, searchtools.StandaloneResult, bool)
}
type SearchHTTPPorts interface {
	DefaultModel() string
	NormalizeMaxResults(int) int
	Access(*gin.Context) (SearchAccess, bool)
	Billing(*gin.Context) *SearchHTTPFailure
	Moderate(*gin.Context, string, []byte) *SearchHTTPFailure
	Run(*gin.Context, int64, bool) SearchHTTPRun
	ConcurrencyError(*gin.Context, error)
}
type SearchHandler struct {
	RequestLifetime
	ports SearchHTTPPorts
}

func NewSearchHandler(ports SearchHTTPPorts) *SearchHandler { return &SearchHandler{ports: ports} }
func (h *SearchHandler) XSearch(c *gin.Context) {
	done, accepted := h.BeginRequest(c, "openai")
	if !accepted {
		return
	}
	defer done()

	c.Set("grok_x_search_endpoint", true)
	h.WebSearch(c)
}

func searchError(c *gin.Context, status int, code, message string) {
	c.JSON(status, gin.H{"error": gin.H{"type": code, "message": message}})
}

// WebSearch parses and authenticates a request before funding, moderation, and provider selection.
func (h *SearchHandler) WebSearch(c *gin.Context) {
	done, accepted := h.BeginRequest(c, "openai")
	if !accepted {
		return
	}
	defer done()

	isX := c.GetBool("grok_x_search_endpoint")
	var decoded grokStandaloneSearchRequest
	if err := c.ShouldBindJSON(&decoded); err != nil {
		searchError(c, 400, "invalid_request_error", err.Error())
		return
	}
	request := searchtools.StandaloneRequest(decoded)
	query := strings.TrimSpace(request.Query)
	if query == "" {
		query = strings.TrimSpace(request.Input)
	}
	if query == "" {
		searchError(c, 400, "invalid_request_error", "query is required")
		return
	}
	request.Query = query
	maxResults := 0
	if request.MaxResults != nil {
		maxResults = *request.MaxResults
	}
	maxResults = h.ports.NormalizeMaxResults(maxResults)
	model := h.ports.DefaultModel()
	label := "web_search"
	if isX {
		label = "x_search"
	}
	access, ok := h.ports.Access(c)
	if !ok {
		searchError(c, 401, "authentication_error", "API key required")
		return
	}
	if !access.GroupPresent || access.Platform != "grok" {
		searchError(c, 400, "invalid_request_error", label+" is only supported for grok groups")
		return
	}
	if failure := h.ports.Billing(c); failure != nil {
		if failure.RetryAfter > 0 {
			c.Header("Retry-After", strconv.Itoa(failure.RetryAfter))
		}
		searchError(c, failure.Status, failure.Code, failure.Message)
		return
	}
	auditBody, _ := json.Marshal(map[string]any{"messages": []map[string]any{{"role": "user", "content": query}}})
	if failure := h.ports.Moderate(c, model, auditBody); failure != nil {
		searchError(c, failure.Status, failure.Code, failure.Message)
		return
	}
	if access.GroupID == nil {
		searchError(c, 400, "invalid_request_error", "group required")
		return
	}
	lease := scheduler.NewLease(c.Request.Context(), scheduler.ReleaseOnCompletion)
	defer lease.Release()
	run := h.ports.Run(c, *access.GroupID, isX)
	result, err := searchtools.RunStandalone(c.Request.Context(), request, model, maxResults, run, lease)
	if err != nil {
		if failure, ok := errors.AsType[*searchtools.StandaloneFailure](err); ok {
			switch failure.Stage {
			case "selection":
				searchError(c, 503, "scheduling_error", failure.Error())
				return
			case "concurrency":
				h.ports.ConcurrencyError(c, failure.Cause)
				return
			}
		}
		searchError(c, 502, "web_search_error", err.Error())
		return
	}
	run.Complete(c, request, result, isX)
	c.JSON(http.StatusOK, gin.H{"query": request.Query, "results": result.Response.Results, "provider": result.Provider, "max_results": maxResults})
}
