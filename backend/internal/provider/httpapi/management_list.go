package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

const providerListGroupUngroupedQueryValue = "ungrouped"

// List handles listing all providers with pagination
// GET /api/v1/admin/providers
func (h *ManagementHandler) List(c *gin.Context) {
	page, pageSize := response.ParsePagination(c)
	platform := c.Query("platform")
	providerType := c.Query("type")
	status := c.Query("status")
	search := c.Query("search")
	privacyMode := strings.TrimSpace(c.Query("privacy_mode"))
	sortBy := c.DefaultQuery("sort_by", "name")
	sortOrder := c.DefaultQuery("sort_order", "asc")
	// Trim search text and limit it to 100 bytes.
	search = strings.TrimSpace(search)
	if len(search) > 100 {
		search = search[:100]
	}
	lite := response.ParseBoolQueryWithDefault(c.Query("lite"), false)
	// Scheduler scores require a batch load lookup; compute them when the client requests the score column.
	includeSchedulerScore := response.ParseBoolQueryWithDefault(c.Query("include_scheduler_score"), false)

	var groupID int64
	if groupIDStr := c.Query("group"); groupIDStr != "" {
		if groupIDStr == providerListGroupUngroupedQueryValue {
			groupID = providercore.ProviderListGroupUngrouped
		} else {
			parsedGroupID, parseErr := strconv.ParseInt(groupIDStr, 10, 64)
			if parseErr != nil {
				response.ErrorFrom(c, infraerrors.BadRequest("INVALID_GROUP_FILTER", "invalid group filter"))
				return
			}
			if parsedGroupID < 0 {
				response.ErrorFrom(c, infraerrors.BadRequest("INVALID_GROUP_FILTER", "invalid group filter"))
				return
			}
			groupID = parsedGroupID
		}
	}

	listing, err := h.listing.List(c.Request.Context(), providercore.ManagementListInput{Page: page, PageSize: pageSize, Platform: platform, ProviderType: providerType, Status: status, Search: search, GroupID: groupID, PrivacyMode: privacyMode, SortBy: sortBy, SortOrder: sortOrder, IncludeSchedulerScore: includeSchedulerScore})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	total := listing.Total
	result := make([]ProviderWithConcurrency, len(listing.Items))
	for i, state := range listing.Items {
		result[i] = h.runtimePresenter.Project(state)
	}
	h.runtimePresenter.EnrichShadowParents(c.Request.Context(), result)

	etag := BuildProvidersListETag(result, total, page, pageSize, platform, providerType, status, search, lite)
	if etag != "" {
		c.Header("ETag", etag)
		c.Header("Vary", "If-None-Match")
		if response.IfNoneMatchMatched(c.GetHeader("If-None-Match"), etag) {
			c.Status(http.StatusNotModified)
			return
		}
	}

	response.Paginated(c, result, total, page, pageSize)
}

func BuildProvidersListETag(
	items []ProviderWithConcurrency,
	total int64,
	page, pageSize int,
	platform, providerType, status, search string,
	lite bool,
) string {
	payload := struct {
		Total        int64                     `json:"total"`
		Page         int                       `json:"page"`
		PageSize     int                       `json:"page_size"`
		Platform     string                    `json:"platform"`
		ProviderType string                    `json:"type"`
		Status       string                    `json:"status"`
		Search       string                    `json:"search"`
		Lite         bool                      `json:"lite"`
		Items        []ProviderWithConcurrency `json:"items"`
	}{
		Total:        total,
		Page:         page,
		PageSize:     pageSize,
		Platform:     platform,
		ProviderType: providerType,
		Status:       status,
		Search:       search,
		Lite:         lite,
		Items:        items,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return "\"" + hex.EncodeToString(sum[:]) + "\""
}
