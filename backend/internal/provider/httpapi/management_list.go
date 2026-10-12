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

// Provider list responses pair paginated configuration with loaded runtime
// observations and a JSON-derived entity tag. The list use case owns database
// sorting and optional scheduler scoring, while this handler converts its
// loaded states, enriches parent hints and hashes the exact response items.
// Search normalization also becomes part of the tag input.
//
// Conditional requests compare against that assembled response after the list
// query completes. The lite flag is parsed into the tag input but the same item
// view is returned for either value. JSON hashing failures leave the tag empty,
// permitting normal response encoding to handle any unencodable payload; no
// conditional match is attempted without a tag.

// providerListGroupUngroupedQueryValue names the list query spelling for
// selecting providers with no routing group association.
const providerListGroupUngroupedQueryValue = "ungrouped"

// List queries a paginated provider selection and presents loaded runtime
// observations with parent hints. Search is trimmed and truncated to one
// hundred bytes, group=ungrouped maps to its sentinel, and negative or
// malformed group identifiers return 400. Optional scheduler scoring is
// requested by its separate query flag.
//
// The ETag hashes the assembled item view, pagination and selected query values
// after loading. A matching conditional header returns 304; an empty tag
// proceeds to ordinary JSON output. Lite changes the fingerprint value but
// leaves the returned item shape unchanged, so callers should treat it as a tag
// input rather than a DTO omission rule.
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
	// Storage search receives a bounded byte string. Apply the same hundred-byte
	// budget as archive export, retaining byte truncation even across UTF-8 characters.
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

// BuildProvidersListETag hashes item JSON together with counts, pagination,
// supplied filters and lite into a quoted SHA-256 tag. The input slice and any
// reachable mutable values need to remain stable during encoding so the tag
// matches the intended response.
//
// A marshal error returns an empty tag, which List treats as unavailable for
// conditional matching. Group and privacy are represented through the loaded
// items rather than separate fields in this payload. Nil and empty item slices
// keep their distinct JSON encodings and therefore may produce different tags.
func BuildProvidersListETag(
	items []ProviderWithConcurrency,
	total int64,
	page, pageSize int,
	platform, providerType, status, search string,
	lite bool,
) string {
	payload := struct {
		Total        int64  `json:"total"`     // complete selection row count
		Page         int    `json:"page"`      // requested page number
		PageSize     int    `json:"page_size"` // requested page row capacity
		Platform     string `json:"platform"`  // supplied upstream platform filter
		ProviderType string `json:"type"`      // supplied authentication kind filter
		Status       string `json:"status"`    // supplied provider state filter
		Search       string `json:"search"`    // normalized provider search text
		Lite         bool   `json:"lite"`      // parsed list fingerprint flag
		// Items borrows the supplied wrappers and their reachable DTOs or runtime
		// observations. Keep that data stable while this payload is JSON encoded
		// and while List sends the corresponding response.
		Items []ProviderWithConcurrency `json:"items"` // presented provider response items
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
		// Unencodable items have no usable representation to hash. Leave the tag
		// unavailable so the caller proceeds with ordinary response encoding and its errors.
		return ""
	}
	sum := sha256.Sum256(raw)
	return "\"" + hex.EncodeToString(sum[:]) + "\""
}
