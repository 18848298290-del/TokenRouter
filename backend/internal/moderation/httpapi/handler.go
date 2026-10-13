package httpapi

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/moderation"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// Administrator review exposes moderation summaries, full input detail, and
// retained image bytes through separate endpoints. App mounts these handlers
// beneath administrator authentication and audit middleware; request filters
// and positive identifiers are checked before the service reads sensitive
// records. Configuration responses use the masked view prepared by moderation.

// ContentModerationHandler adapts administrator risk-control requests to the
// shared moderation service and response helpers.
type ContentModerationHandler struct {
	// service is required by every handler method. App binds it before mounting
	// these routes under administrator authentication and audit middleware.
	// assembled moderation service instance
	service *moderation.ContentModerationService
}

// contentModerationConfigRequest carries partial administrator changes whose
// field presence is retained during service conversion.
type contentModerationConfigRequest struct {
	// Enabled, Mode, BaseURL, and Model override the saved values when their
	// pointers are present. Omitted or JSON-null fields remain nil and leave the
	// corresponding saved values unchanged.
	// ordinary audit enablement switch
	Enabled *bool   `json:"enabled"`
	Mode    *string `json:"mode"`     // ordinary audit execution mode
	BaseURL *string `json:"base_url"` // external moderation endpoint base
	Model   *string `json:"model"`    // external moderation model name
	// ProxyID leaves selection unchanged when nil. A positive value selects that
	// stored proxy; a present nonpositive value clears selection and uses direct
	// audit transport.
	// selected outbound proxy identifier
	ProxyID *int64 `json:"proxy_id"`
	// APIKey, APIKeys, APIKeyEntries, APIKeyUpdates, APIKeysMode,
	// DeleteAPIKeyHashes, and ClearAPIKey describe credential editing in
	// UpdateConfig. ClearAPIKey wins over other key operations; replacement skips
	// hash deletion, and supplied entry attributes override metadata updates for
	// the same retained key.
	// single legacy credential input
	APIKey  *string   `json:"api_key"`
	APIKeys *[]string `json:"api_keys"` // ordered credential list input
	// credential and metadata additions
	APIKeyEntries *[]moderation.ContentModerationAPIKeyEntryInput `json:"api_key_entries"`
	// metadata updates by hash
	APIKeyUpdates *[]moderation.ContentModerationAPIKeyMetadata `json:"api_key_updates"`
	// append or replace selection
	APIKeysMode string `json:"api_keys_mode"`
	// credential hashes for deletion
	DeleteAPIKeyHashes *[]string `json:"delete_api_key_hashes"`
	// credential pool clearing request
	ClearAPIKey bool `json:"clear_api_key"`
	// TimeoutMS and SampleRate retain saved values when their pointers are absent.
	// Present values pass through the core normalization rules before
	// configuration persistence.
	// milliseconds
	TimeoutMS  *int `json:"timeout_ms"`
	SampleRate *int `json:"sample_rate"` // percent
	// AllGroups, GroupIDs, RecordNonHits, and Thresholds contribute writes when
	// supplied. Missing pointers retain saved values; the core normalizes IDs and
	// overlays recognized thresholds on its default category set.
	// all-group audit scope switch
	AllGroups *bool `json:"all_groups"`
	// selected group scope identifiers
	GroupIDs *[]int64 `json:"group_ids"`
	// successful non-hit logging switch
	RecordNonHits *bool `json:"record_non_hits"`
	// category score decision thresholds
	Thresholds *map[string]float64 `json:"thresholds"`
	// WorkerCount, QueueSize, BlockStatus, and BlockMessage retain saved values
	// when absent. Supplied values are validated or normalized by UpdateConfig
	// before the settings write can publish them.
	// configured admitted worker count
	WorkerCount *int `json:"worker_count"`
	// configured queue admission limit
	QueueSize   *int `json:"queue_size"`
	BlockStatus *int `json:"block_status"` // blocked request HTTP status
	// blocked request client message
	BlockMessage *string `json:"block_message"`
	// EmailOnHit, AutoBanEnabled, BanThreshold, ViolationWindowHours, RetryCount,
	// HitRetentionDays, and NonHitRetentionDays retain saved values when absent.
	// Present values select ordinary hit side effects, retry policy, and retention
	// through core normalization.
	// ordinary violation notice switch
	EmailOnHit *bool `json:"email_on_hit"`
	// ordinary automatic ban switch
	AutoBanEnabled *bool `json:"auto_ban_enabled"`
	// ordinary counted-hit ban threshold
	BanThreshold         *int `json:"ban_threshold"`
	ViolationWindowHours *int `json:"violation_window_hours"` // hours
	// retries after initial attempt
	RetryCount          *int `json:"retry_count"`
	HitRetentionDays    *int `json:"hit_retention_days"`     // days
	NonHitRetentionDays *int `json:"non_hit_retention_days"` // days
	// PreHashCheckEnabled, CyberWarningEnabled, CyberAutoBanEnabled,
	// CyberBanThreshold, and CyberWindowHours retain saved values when absent.
	// Their supplied values govern hash admission and the separately counted Cyber
	// evidence and ban paths.
	// recorded hash lookup switch
	PreHashCheckEnabled *bool `json:"pre_hash_check_enabled"`
	// Cyber evidence recording switch
	CyberWarningEnabled *bool `json:"cyber_warning_enabled"`
	// Cyber automatic ban switch
	CyberAutoBanEnabled *bool `json:"cyber_auto_ban_enabled"`
	// Cyber warning ban threshold
	CyberBanThreshold *int `json:"cyber_ban_threshold"`
	CyberWindowHours  *int `json:"cyber_violation_window_hours"` // hours
	// BlockedKeywords, KeywordBlockingMode, and ModelFilter retain saved values
	// when absent. The core normalizes supplied lists and applies keyword mode
	// before hash checking on the pre-block request path.
	// ordered substring block keywords
	BlockedKeywords *[]string `json:"blocked_keywords"`
	// keyword and API selection
	KeywordBlockingMode *string `json:"keyword_blocking_mode"`
	// requested model scope selection
	ModelFilter *moderation.ContentModerationModelFilter `json:"model_filter"`
	// AuditUserTextMaxChars, AuditImages, AuditToolOutputs, and
	// AuditToolOutputMaxChars retain saved values when absent. Supplied values
	// select what reaches external auditing while full review material follows the
	// retention field on each Check input.
	// user text rune budget
	AuditUserTextMaxChars *int `json:"audit_user_text_max_chars"`
	// image audit inclusion switch
	AuditImages *bool `json:"audit_images"`
	// tool audit inclusion switch
	AuditToolOutputs *bool `json:"audit_tool_outputs"`
	// tool text rune budget
	AuditToolOutputMaxChars *int `json:"audit_tool_output_max_chars"`
}

// contentModerationAPIKeyTestRequest supplies temporary audit credentials,
// endpoint overrides, and administrator test content.
type contentModerationAPIKeyTestRequest struct {
	// APIKeys selects supplied nonblank credentials, or the saved pool when the
	// list normalizes to empty. A saved pool with audit content tests one
	// scheduled usable key.
	APIKeys []string `json:"api_keys"` // candidate moderation credentials
	// BaseURL, Model, and TimeoutMS supply temporary connection choices. Blank
	// endpoint or model text and nonpositive timeout retain saved values; supplied
	// values are normalized for this probe rather than persisted as configuration.
	BaseURL   string `json:"base_url"`   // temporary endpoint base override
	Model     string `json:"model"`      // temporary moderation model override
	TimeoutMS int    `json:"timeout_ms"` // milliseconds
	// ProxyID uses the saved selection when nil. Positive values select that
	// proxy, while nonpositive values request direct transport for this key test.
	ProxyID *int64 `json:"proxy_id"` // temporary outbound proxy selection
	// Prompt and Images choose administrator audit content. Empty selected content
	// sends hello; nonblank images pass the core data-URL and byte-size validation
	// before an audit request begins.
	Prompt string   `json:"prompt"` // administrator test text input
	Images []string `json:"images"` // inline base64 test references
}

// contentModerationHashRequest identifies the flagged-input member requested
// for deletion by an administrator.
type contentModerationHashRequest struct {
	InputHash string `json:"input_hash"` // requested flagged input hash
}

// NewContentModerationHandler binds the required service instance for
// administrator risk-control routes. Construction performs no request work or
// authentication; app mounts its methods on the protected administrator group.
func NewContentModerationHandler(svc *moderation.ContentModerationService) *ContentModerationHandler {
	return &ContentModerationHandler{service: svc}
}

// GetConfig returns the moderation configuration with masked credential
// observations. It passes the HTTP request context to the service and converts
// a settings read error through the common response helper.
func (h *ContentModerationHandler) GetConfig(c *gin.Context) {
	cfg, err := h.service.GetConfig(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, cfg)
}

// UpdateConfig decodes partial administrator configuration and passes its
// presence-aware fields to the service. Invalid JSON returns a bad request;
// persistence or validation errors use the common error response, and success
// returns the resulting masked view.
func (h *ContentModerationHandler) UpdateConfig(c *gin.Context) {
	var req contentModerationConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	cfg, err := h.service.UpdateConfig(c.Request.Context(), moderation.UpdateContentModerationConfigInput{
		Enabled:                 req.Enabled,
		Mode:                    req.Mode,
		BaseURL:                 req.BaseURL,
		Model:                   req.Model,
		ProxyID:                 req.ProxyID,
		APIKey:                  req.APIKey,
		APIKeys:                 req.APIKeys,
		APIKeyEntries:           req.APIKeyEntries,
		APIKeyUpdates:           req.APIKeyUpdates,
		APIKeysMode:             req.APIKeysMode,
		DeleteAPIKeyHashes:      req.DeleteAPIKeyHashes,
		ClearAPIKey:             req.ClearAPIKey,
		TimeoutMS:               req.TimeoutMS,
		SampleRate:              req.SampleRate,
		AllGroups:               req.AllGroups,
		GroupIDs:                req.GroupIDs,
		RecordNonHits:           req.RecordNonHits,
		Thresholds:              req.Thresholds,
		WorkerCount:             req.WorkerCount,
		QueueSize:               req.QueueSize,
		BlockStatus:             req.BlockStatus,
		BlockMessage:            req.BlockMessage,
		EmailOnHit:              req.EmailOnHit,
		AutoBanEnabled:          req.AutoBanEnabled,
		BanThreshold:            req.BanThreshold,
		ViolationWindowHours:    req.ViolationWindowHours,
		RetryCount:              req.RetryCount,
		HitRetentionDays:        req.HitRetentionDays,
		NonHitRetentionDays:     req.NonHitRetentionDays,
		PreHashCheckEnabled:     req.PreHashCheckEnabled,
		CyberWarningEnabled:     req.CyberWarningEnabled,
		CyberAutoBanEnabled:     req.CyberAutoBanEnabled,
		CyberBanThreshold:       req.CyberBanThreshold,
		CyberWindowHours:        req.CyberWindowHours,
		BlockedKeywords:         req.BlockedKeywords,
		KeywordBlockingMode:     req.KeywordBlockingMode,
		ModelFilter:             req.ModelFilter,
		AuditUserTextMaxChars:   req.AuditUserTextMaxChars,
		AuditImages:             req.AuditImages,
		AuditToolOutputs:        req.AuditToolOutputs,
		AuditToolOutputMaxChars: req.AuditToolOutputMaxChars,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, cfg)
}

// TestAPIKeys decodes administrator audit candidates and temporary connection
// overrides. It passes the request context through key testing, returns
// malformed input as a bad request, and exposes service results or validation
// errors through standard responses.
func (h *ContentModerationHandler) TestAPIKeys(c *gin.Context) {
	var req contentModerationAPIKeyTestRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	result, err := h.service.TestAPIKeys(c.Request.Context(), moderation.TestContentModerationAPIKeysInput{
		APIKeys:   req.APIKeys,
		BaseURL:   req.BaseURL,
		Model:     req.Model,
		TimeoutMS: req.TimeoutMS,
		ProxyID:   req.ProxyID,
		Prompt:    req.Prompt,
		Images:    req.Images,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

// GetStatus returns sampled moderation queue, synchronous audit, key-health,
// and hash observations. Service reads use the HTTP request context; the result
// reflects separate samples taken by GetStatus rather than one locked runtime
// snapshot.
func (h *ContentModerationHandler) GetStatus(c *gin.Context) {
	status, err := h.service.GetStatus(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, status)
}

// ListLogs builds administrator audit filters from query values and parsed
// pagination. Group IDs must be positive; from and to accept RFC3339 or UTC
// dates. A date-only to value becomes the inclusive final nanosecond of its
// fixed twenty-four-hour day.
func (h *ContentModerationHandler) ListLogs(c *gin.Context) {
	page, pageSize := response.ParsePagination(c)
	filter := moderation.ContentModerationLogFilter{
		RequestID: strings.TrimSpace(c.Query("request_id")),
		Pagination: pagination.PaginationParams{
			Page:      page,
			PageSize:  pageSize,
			SortOrder: pagination.SortOrderDesc,
		},
		Result:   c.Query("result"),
		Endpoint: c.Query("endpoint"),
		Search:   c.Query("search"),
	}
	if raw := strings.TrimSpace(c.Query("group_id")); raw != "" {
		groupID, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || groupID <= 0 {
			response.BadRequest(c, "Invalid group_id")
			return
		}
		filter.GroupID = &groupID
	}
	if raw := strings.TrimSpace(c.Query("from")); raw != "" {
		t, _, err := parseContentModerationDate(raw)
		if err != nil {
			response.BadRequest(c, "Invalid from")
			return
		}
		filter.From = &t
	}
	if raw := strings.TrimSpace(c.Query("to")); raw != "" {
		t, dateOnly, err := parseContentModerationDate(raw)
		if err != nil {
			response.BadRequest(c, "Invalid to")
			return
		}
		if dateOnly {
			// Dates parse at UTC midnight; include their full fixed-duration day.
			t = t.Add(24*time.Hour - time.Nanosecond)
		}
		filter.To = &t
	}
	items, pageResult, err := h.service.ListLogs(c.Request.Context(), filter)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Paginated(c, items, pageResult.Total, pageResult.Page, pageResult.PageSize)
}

// GetLog returns full administrator review material for a positive decimal path
// ID. Invalid IDs return a bad request before storage access; missing review
// data and service errors use the common error response.
func (h *ContentModerationHandler) GetLog(c *gin.Context) {
	id, err := strconv.ParseInt(strings.TrimSpace(c.Param("id")), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid id")
		return
	}
	item, err := h.service.GetLog(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, item)
}

// ListCyberWarnings parses administrator warning filters with pagination
// enabled and returns their result page. Invalid identifiers or dates stop
// before repository access, while service read failures use the shared error
// response.
func (h *ContentModerationHandler) ListCyberWarnings(c *gin.Context) {
	filter, errMsg, ok := parseCyberWarningFilter(c, true)
	if !ok {
		response.BadRequest(c, errMsg)
		return
	}
	items, pageResult, err := h.service.ListCyberWarnings(c.Request.Context(), filter)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Paginated(c, items, pageResult.Total, pageResult.Page, pageResult.PageSize)
}

// GetCyberWarning returns full Cyber review material for a positive decimal
// path ID. Invalid IDs fail before service access; the service classifies
// unavailable review storage and missing rows for the common error response.
func (h *ContentModerationHandler) GetCyberWarning(c *gin.Context) {
	id, err := strconv.ParseInt(strings.TrimSpace(c.Param("id")), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid id")
		return
	}
	item, err := h.service.GetCyberWarning(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, item)
}

// GetMediaContent serves a ready administrator image snapshot for a positive
// decimal path ID. It returns the stored MIME type and bytes with
// private/no-store and nosniff headers; unavailable or failed snapshots receive
// a service error response.
func (h *ContentModerationHandler) GetMediaContent(c *gin.Context) {
	id, err := strconv.ParseInt(strings.TrimSpace(c.Param("id")), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid id")
		return
	}
	item, err := h.service.GetMediaContent(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	c.Header("Cache-Control", "private, no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Data(http.StatusOK, item.MIMEType, item.Content)
}

// GetCyberSummary parses administrator warning filters with pagination omitted
// and returns grouped observations. Identifier and date errors stop the request
// before service access; the repository applies its own fixed summary group
// limits.
func (h *ContentModerationHandler) GetCyberSummary(c *gin.Context) {
	filter, errMsg, ok := parseCyberWarningFilter(c, false)
	if !ok {
		response.BadRequest(c, errMsg)
		return
	}
	summary, err := h.service.GetCyberSummary(c.Request.Context(), filter)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, summary)
}

// parseCyberWarningFilter builds common warning filters and reports a
// field-specific message with false on invalid input. withPagination adds page
// parameters; positive user/provider IDs and RFC3339 or UTC date limits are
// accepted, with date-only to expanded inclusively.
func parseCyberWarningFilter(c *gin.Context, withPagination bool) (moderation.ContentModerationCyberWarningFilter, string, bool) {
	filter := moderation.ContentModerationCyberWarningFilter{Search: c.Query("search"), RequestID: strings.TrimSpace(c.Query("request_id"))}
	if withPagination {
		page, pageSize := response.ParsePagination(c)
		filter.Pagination = pagination.PaginationParams{
			Page:      page,
			PageSize:  pageSize,
			SortOrder: pagination.SortOrderDesc,
		}
	}
	if raw := strings.TrimSpace(c.Query("user_id")); raw != "" {
		userID, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || userID <= 0 {
			return filter, "Invalid user_id", false
		}
		filter.UserID = &userID
	}
	if raw := strings.TrimSpace(c.Query("provider_id")); raw != "" {
		providerID, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || providerID <= 0 {
			return filter, "Invalid provider_id", false
		}
		filter.ProviderID = &providerID
	}
	if raw := strings.TrimSpace(c.Query("from")); raw != "" {
		t, _, err := parseContentModerationDate(raw)
		if err != nil {
			return filter, "Invalid from", false
		}
		filter.From = &t
	}
	if raw := strings.TrimSpace(c.Query("to")); raw != "" {
		t, dateOnly, err := parseContentModerationDate(raw)
		if err != nil {
			return filter, "Invalid to", false
		}
		if dateOnly {
			// Dates parse at UTC midnight; include their full fixed-duration day.
			t = t.Add(24*time.Hour - time.Nanosecond)
		}
		filter.To = &t
	}
	return filter, "", true
}

// UnbanUser requests active status for a positive decimal user path ID. Invalid
// IDs fail before service access; successful responses follow any needed
// status write and authentication invalidation performed by the service.
func (h *ContentModerationHandler) UnbanUser(c *gin.Context) {
	userID, err := strconv.ParseInt(strings.TrimSpace(c.Param("user_id")), 10, 64)
	if err != nil || userID <= 0 {
		response.BadRequest(c, "Invalid user_id")
		return
	}
	result, err := h.service.UnbanUser(c.Request.Context(), userID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

// DeleteFlaggedHash decodes a requested input hash and delegates normalization
// and deletion to moderation. Invalid JSON fails before cache access; service
// validation and Redis failures return through standard error responses.
func (h *ContentModerationHandler) DeleteFlaggedHash(c *gin.Context) {
	var req contentModerationHashRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	result, err := h.service.DeleteFlaggedInputHash(c.Request.Context(), req.InputHash)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

// ClearFlaggedHashes requests removal of the shared flagged-input set through
// moderation. Success returns the adapter count sampled before deletion;
// service or Redis errors receive the common response instead of a successful
// removal result.
func (h *ContentModerationHandler) ClearFlaggedHashes(c *gin.Context) {
	result, err := h.service.ClearFlaggedInputHashes(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

// parseContentModerationDate parses trimmed RFC3339 text first, then a UTC
// calendar date. Empty input returns zero time, false, nil; the boolean
// identifies a successful date-only parse so list callers can expand an
// inclusive upper limit.
func parseContentModerationDate(raw string) (time.Time, bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false, nil
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t, false, nil
	}
	// When RFC3339 fails, try the UTC date form; the boolean tells range callers
	// to expand a valid date-only upper limit through its final nanosecond.
	t, err := time.Parse("2006-01-02", raw)
	return t, err == nil, err
}
