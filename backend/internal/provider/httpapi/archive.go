package httpapi

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	idempotencyhttp "github.com/TokenFlux/TokenRouter/internal/idempotency/httpapi"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/provider/transfer"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// archive.go handles provider archive import and export requests with filter
// parsing and response idempotency.
// Provider backup exports contain raw upstream credentials and can include the
// selected proxies, so route registration protects export with step-up
// authentication. Explicit IDs override list filters; otherwise filter parsing
// distinguishes ungrouped selection from a positive group ID. Search is
// byte-limited before the export use case receives it.
//
// Import validates the archive header and strips deprecated Extra before
// response idempotency fingerprints the data. The archive use case owns proxy
// handling, per-provider creation and privacy follow-up. Export input errors
// receive 400, while storage errors keep their normal mapping; parsing
// preserves supplied ID order and duplicates for the use case to interpret.

// ArchiveHandler binds raw provider backup transfers to the archive use case
// and response idempotency.
type ArchiveHandler struct {
	idempotencyhttp.Executor                   // administrator write idempotency binding
	archive                  *provider.Archive // borrowed provider backup use case
}

// NewArchiveHandler borrows provider archive operations for administrator
// backup transfers. Bind response idempotency separately and mount export
// behind step-up authentication because its payload contains raw provider and
// proxy credentials.
func NewArchiveHandler(archive *provider.Archive) *ArchiveHandler {
	return &ArchiveHandler{archive: archive}
}

// ExportData returns raw provider backup data selected by explicit IDs or list
// filters. ID parsing rejects nonpositive values; a nonempty ID list bypasses
// group parsing and list selection. A supplied list-filter group must be
// ungrouped or a positive integer.
//
// Search is trimmed and truncated to one hundred bytes before export,
// potentially splitting UTF-8 text. Proxy inclusion is supplied as a deferred
// parser for the archive use case. ArchiveInputError becomes 400, while other
// failures retain their shared error mapping; callers obtain step-up protection
// from route registration.
func (h *ArchiveHandler) ExportData(c *gin.Context) {
	ids, err := parseProviderIDs(c)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	query := provider.ArchiveExportQuery{IDs: ids, Platform: c.Query("platform"), Type: c.Query("type"), Status: c.Query("status"), PrivacyMode: strings.TrimSpace(c.Query("privacy_mode")), Search: strings.TrimSpace(c.Query("search")), SortBy: c.DefaultQuery("sort_by", "name"), SortOrder: c.DefaultQuery("sort_order", "asc"), IncludeProxies: func() (bool, error) { return parseIncludeProxies(c) }}
	// Archive list search follows the management request's byte budget. The
	// truncation can split UTF-8 text because the storage query receives bytes as supplied.
	if len(query.Search) > 100 {
		query.Search = query.Search[:100]
	}
	if len(ids) == 0 {
		if group := c.Query("group"); group != "" {
			if group == "ungrouped" {
				query.GroupID = provider.ProviderListGroupUngrouped
			} else {
				value, parseErr := strconv.ParseInt(group, 10, 64)
				if parseErr != nil || value <= 0 {
					response.ErrorFrom(c, infraerrors.BadRequest("INVALID_GROUP_FILTER", "invalid group filter"))
					return
				}
				query.GroupID = value
			}
		}
	}
	payload, err := h.archive.Export(c.Request.Context(), query)
	if err != nil {
		if inputErr, ok := errors.AsType[*provider.ArchiveInputError](err); ok {
			response.BadRequest(c, inputErr.Error())
		} else {
			response.ErrorFrom(c, err)
		}
		return
	}
	response.Success(c, payload)
}

// ImportData strictly binds an archive, validates its format header and strips
// retired provider Extra keys. Administrator response idempotency wraps the
// delegated import, whose per-item outcomes are produced by the archive use
// case.
func (h *ArchiveHandler) ImportData(c *gin.Context) {
	var req transfer.DataImportRequest
	if err := response.BindJSONStrict(c, &req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	if err := transfer.ValidateHeader(req.Data); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	// Exclude deprecated fields from the idempotency fingerprint, including malformed values from older clients.
	for index := range req.Data.Providers {
		provider.DiscardDeprecatedExtra(req.Data.Providers[index].Extra)
	}

	h.ExecuteAdminIdempotentJSON(c, "admin.providers.import_data", req, h.DefaultWriteIdempotencyTTL(), func(ctx context.Context) (any, error) {
		return h.archive.Import(ctx, req)
	})
}

// parseProviderIDs returns positive decimal IDs from repeated or
// comma-separated query values. Whitespace and empty parts are skipped; missing
// input returns nil, while order and duplicates are retained for archive
// selection.
func parseProviderIDs(c *gin.Context) ([]int64, error) {
	values := c.QueryArray("ids")
	if len(values) == 0 {
		raw := strings.TrimSpace(c.Query("ids"))
		if raw != "" {
			values = []string{raw}
		}
	}
	if len(values) == 0 {
		return nil, nil
	}

	ids := make([]int64, 0, len(values))
	for _, item := range values {
		for part := range strings.SplitSeq(item, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			id, err := strconv.ParseInt(part, 10, 64)
			if err != nil || id <= 0 {
				return nil, fmt.Errorf("invalid provider id: %s", part)
			}
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// parseIncludeProxies interprets trimmed, case-insensitive boolean query
// spellings and defaults omission to true. Unrecognized text returns true with
// an error, which the export use case handles before using the inclusion value.
func parseIncludeProxies(c *gin.Context) (bool, error) {
	raw := strings.TrimSpace(strings.ToLower(c.Query("include_proxies")))
	if raw == "" {
		return true, nil
	}
	switch raw {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	default:
		return true, fmt.Errorf("invalid include_proxies value: %s", raw)
	}
}
