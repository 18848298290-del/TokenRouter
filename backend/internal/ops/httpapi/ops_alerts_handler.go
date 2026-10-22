package httpapi

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"

	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/ops"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// ops_alerts_handler.go validates alert administration requests and exposes
// rule, event, resolution, and silence endpoints.
// Alert rule writes require name, metric, operator, and threshold members for
// both creation and replacement. The body is decoded into raw members and a
// complete rule before normalized controls are applied, retaining descriptions,
// filters, and typed metadata. Replacing a rule applies defaults to omitted
// controls. A present null boolean becomes false, while an omitted boolean
// enables its control.
//
// Event listing combines optional dimensions with a paired time-and-ID cursor,
// applying a time range only when the client supplies a nonblank time
// parameter. Resolution writes accept the two resolved states and attach the
// current UTC time. Silence creation passes group and region through to the
// service and records a context subject when present; it does not enforce a
// future deadline or positive creator identity here.

// These metric, comparison, and severity lists define rule payload validation
// and the supported-value text returned in validation errors.
var (
	// validOpsAlertMetricTypes lists the evaluator indicators selectable in rule
	// payloads and retains their validation-error display order.
	validOpsAlertMetricTypes = []string{
		"success_rate",                // raw dashboard SLA percentage
		"error_rate",                  // raw dashboard error-rate percentage
		"upstream_error_rate",         // raw dashboard upstream-error percentage
		"cpu_usage_percent",           // system-snapshot CPU percentage
		"memory_usage_percent",        // system-snapshot memory percentage
		"disk_usage_percent",          // system-snapshot disk percentage
		"concurrency_queue_depth",     // system-snapshot queued request count
		"group_available_providers",   // selected group available provider count
		"group_available_ratio",       // selected group availability percentage
		"group_rate_limit_ratio",      // selected group rate-limited percentage
		"provider_rate_limited_count", // rate-limited providers in scope
		"provider_error_count",        // HasError providers with no temporary deadline
		"provider_error_ratio",        // qualifying provider errors percentage
		"overload_provider_count",     // overloaded providers in scope
		"proxy_expired_count",         // expired proxy repository count
		"proxy_expiring_soon_count",   // proxy repository near-expiry count
	}

	// validOpsAlertMetricTypeSet is built once from the indicator list for exact
	// case-sensitive membership checks during payload validation.
	validOpsAlertMetricTypeSet = func() map[string]struct{} {
		set := make(map[string]struct{}, len(validOpsAlertMetricTypes))
		for _, v := range validOpsAlertMetricTypes {
			set[v] = struct{}{}
		}
		return set
	}()

	// validOpsAlertOperators lists numeric comparison operators accepted by
	// validation and displayed when an unsupported operator is supplied.
	validOpsAlertOperators = []string{">", "<", ">=", "<=", "==", "!="}

	// validOpsAlertOperatorSet is built once from the operator list and checks
	// trimmed operator text without changing its spelling.
	validOpsAlertOperatorSet = func() map[string]struct{} {
		set := make(map[string]struct{}, len(validOpsAlertOperators))
		for _, v := range validOpsAlertOperators {
			set[v] = struct{}{}
		}
		return set
	}()

	// validOpsAlertSeverities permits P0 through P3 after text normalization.
	// Evaluator email classification maps P0 to critical, P1 to warning, and P2/P3
	// to info; blank HTTP severity defaults to P2.
	validOpsAlertSeverities = []string{"P0", "P1", "P2", "P3"}

	// validOpsAlertSeveritySet is built once from the severity list for membership
	// checks after the payload's case normalization.
	validOpsAlertSeveritySet = func() map[string]struct{} {
		set := make(map[string]struct{}, len(validOpsAlertSeverities))
		for _, v := range validOpsAlertSeverities {
			set[v] = struct{}{}
		}
		return set
	}()
)

// opsAlertRuleValidatedInput holds normalized required values and optional rule
// controls.
type opsAlertRuleValidatedInput struct {
	// Name, MetricType, Operator, and Threshold come from required raw members.
	// bindOpsAlertRule uses these values to override the corresponding fields from
	// its complete typed rule decode.
	Name       string  // trimmed rule display name
	MetricType string  // validated evaluator indicator label
	Operator   string  // validated numeric comparison operator
	Threshold  float64 // validated comparison cutoff value

	// Severity uses uppercased trimmed text and defaults to P2 when its member is
	// absent, blank, or JSON null.
	Severity string // normalized alert severity label

	// WindowMinutes accepts 1, 5, or 60; SustainedMinutes accepts 1 through 1440;
	// CooldownMinutes accepts 0 through 1440. Missing members default to 1, 1, and
	// 0 respectively for both create and update.
	WindowMinutes    int // minutes
	SustainedMinutes int // minutes
	CooldownMinutes  int // minutes

	// Enabled and NotifyEmail default true when their raw members are absent. A
	// present false or JSON null produces false. The binder applies these values
	// after decoding the complete rule.
	Enabled     bool // rule evaluation enable flag
	NotifyEmail bool // firing email request flag
}

// isPercentOrRateMetric reports whether an exact metric label uses the
// zero-to-one-hundred range in HTTP validation. Unknown labels return false;
// validateOpsAlertRulePayload rejects them separately before selecting the
// threshold rule.
func isPercentOrRateMetric(metricType string) bool {
	switch metricType {
	case "success_rate",
		"error_rate",
		"upstream_error_rate",
		"cpu_usage_percent",
		"memory_usage_percent",
		"disk_usage_percent",
		"group_available_ratio",
		"group_rate_limit_ratio",
		"provider_error_ratio":
		return true
	default:
		return false
	}
}

// validateOpsAlertRulePayload decodes required rule members and optional
// controls from a nonnil raw map. Name, metric, and operator are trimmed and
// checked; thresholds use a finite nonnegative range, with percentage
// indicators capped at one hundred. JSON null threshold decodes as zero.
//
// Missing optional controls select window one, sustained one, cooldown zero, P2
// severity, and enabled/email true. Present null booleans decode as false,
// while null severity selects P2. Unknown raw members are ignored here and
// remain available to the complete typed rule decode, which runs after this
// validation succeeds.
func validateOpsAlertRulePayload(raw map[string]json.RawMessage) (*opsAlertRuleValidatedInput, error) {
	if raw == nil {
		return nil, fmt.Errorf("invalid request body")
	}

	requiredFields := []string{"name", "metric_type", "operator", "threshold"}
	for _, field := range requiredFields {
		if _, ok := raw[field]; !ok {
			return nil, fmt.Errorf("%s is required", field)
		}
	}

	var name string
	if err := json.Unmarshal(raw["name"], &name); err != nil || strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("name is required")
	}
	name = strings.TrimSpace(name)

	var metricType string
	if err := json.Unmarshal(raw["metric_type"], &metricType); err != nil || strings.TrimSpace(metricType) == "" {
		return nil, fmt.Errorf("metric_type is required")
	}
	metricType = strings.TrimSpace(metricType)
	if _, ok := validOpsAlertMetricTypeSet[metricType]; !ok {
		return nil, fmt.Errorf("metric_type must be one of: %s", strings.Join(validOpsAlertMetricTypes, ", "))
	}

	var operator string
	if err := json.Unmarshal(raw["operator"], &operator); err != nil || strings.TrimSpace(operator) == "" {
		return nil, fmt.Errorf("operator is required")
	}
	operator = strings.TrimSpace(operator)
	if _, ok := validOpsAlertOperatorSet[operator]; !ok {
		return nil, fmt.Errorf("operator must be one of: %s", strings.Join(validOpsAlertOperators, ", "))
	}

	var threshold float64
	if err := json.Unmarshal(raw["threshold"], &threshold); err != nil {
		return nil, fmt.Errorf("threshold must be a number")
	}
	if math.IsNaN(threshold) || math.IsInf(threshold, 0) {
		return nil, fmt.Errorf("threshold must be a finite number")
	}
	// Percentage indicators use the evaluator's percentage unit; HTTP bounds their
	// configured threshold while count metrics have no upper cutoff.
	if isPercentOrRateMetric(metricType) {
		if threshold < 0 || threshold > 100 {
			return nil, fmt.Errorf("threshold must be between 0 and 100 for metric_type %s", metricType)
		}
	} else if threshold < 0 {
		return nil, fmt.Errorf("threshold must be >= 0")
	}

	validated := &opsAlertRuleValidatedInput{
		Name:       name,
		MetricType: metricType,
		Operator:   operator,
		Threshold:  threshold,
	}

	if v, ok := raw["severity"]; ok {
		var sev string
		if err := json.Unmarshal(v, &sev); err != nil {
			return nil, fmt.Errorf("severity must be a string")
		}
		sev = strings.ToUpper(strings.TrimSpace(sev))
		if sev != "" {
			if _, ok := validOpsAlertSeveritySet[sev]; !ok {
				return nil, fmt.Errorf("severity must be one of: %s", strings.Join(validOpsAlertSeverities, ", "))
			}
			validated.Severity = sev
		}
	}
	if validated.Severity == "" {
		validated.Severity = "P2"
	}

	// Use raw member presence to distinguish omitted true defaults from explicit
	// false or null values; the typed rule decode alone cannot make that
	// distinction.
	if v, ok := raw["enabled"]; ok {
		if err := json.Unmarshal(v, &validated.Enabled); err != nil {
			return nil, fmt.Errorf("enabled must be a boolean")
		}
	} else {
		validated.Enabled = true
	}

	if v, ok := raw["notify_email"]; ok {
		if err := json.Unmarshal(v, &validated.NotifyEmail); err != nil {
			return nil, fmt.Errorf("notify_email must be a boolean")
		}
	} else {
		validated.NotifyEmail = true
	}

	if v, ok := raw["window_minutes"]; ok {
		if err := json.Unmarshal(v, &validated.WindowMinutes); err != nil {
			return nil, fmt.Errorf("window_minutes must be an integer")
		}
		// Restrict request-metric windows to the supported one, five, and sixty
		// minute selections.
		switch validated.WindowMinutes {
		case 1, 5, 60:
		default:
			return nil, fmt.Errorf("window_minutes must be one of: 1, 5, 60")
		}
	} else {
		validated.WindowMinutes = 1
	}

	if v, ok := raw["sustained_minutes"]; ok {
		if err := json.Unmarshal(v, &validated.SustainedMinutes); err != nil {
			return nil, fmt.Errorf("sustained_minutes must be an integer")
		}
		// Limit the configured sustained duration to one day expressed in minutes.
		if validated.SustainedMinutes < 1 || validated.SustainedMinutes > 1440 {
			return nil, fmt.Errorf("sustained_minutes must be between 1 and 1440")
		}
	} else {
		validated.SustainedMinutes = 1
	}

	if v, ok := raw["cooldown_minutes"]; ok {
		if err := json.Unmarshal(v, &validated.CooldownMinutes); err != nil {
			return nil, fmt.Errorf("cooldown_minutes must be an integer")
		}
		// Allow zero cooldown and at most one day in minutes.
		if validated.CooldownMinutes < 0 || validated.CooldownMinutes > 1440 {
			return nil, fmt.Errorf("cooldown_minutes must be between 0 and 1440")
		}
	} else {
		validated.CooldownMinutes = 0
	}

	return validated, nil
}

// ListAlertRules returns the service's saved rule list after checking
// monitoring. A missing service is unavailable; the service can return an
// allocated empty list when its repository is absent.
// GET /api/v1/admin/ops/alert-rules
func (h *OpsHandler) ListAlertRules(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	if err := h.opsService.RequireMonitoringEnabled(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	rules, err := h.opsService.ListAlertRules(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, rules)
}

// CreateAlertRule creates a complete rule after service and monitoring checks.
// Binding failures return bad request before the service receives a rule;
// omitted controls use the same defaults as replacements. Service errors use
// the shared HTTP mapping.
// POST /api/v1/admin/ops/alert-rules
func (h *OpsHandler) CreateAlertRule(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	if err := h.opsService.RequireMonitoringEnabled(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	rule := bindOpsAlertRule(c)
	if rule == nil {
		return
	}

	created, err := h.opsService.CreateAlertRule(c.Request.Context(), rule)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, created)
}

// UpdateAlertRule replaces the positive route-selected rule after service and
// monitoring checks. The route ID is validated before reading the body and
// overrides its ID after binding; omitted controls use defaults for the
// complete replacement. Service errors use the shared HTTP mapping.
// PUT /api/v1/admin/ops/alert-rules/:id
func (h *OpsHandler) UpdateAlertRule(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	if err := h.opsService.RequireMonitoringEnabled(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid rule ID")
		return
	}

	rule := bindOpsAlertRule(c)
	if rule == nil {
		return
	}

	rule.ID = id
	updated, err := h.opsService.UpdateAlertRule(c.Request.Context(), rule)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, updated)
}

// bindOpsAlertRule decodes a complete rule with validated controls for creation
// or replacement. It writes a bad-request response and returns nil when either
// binding or validation fails. The raw-member binding runs first so omitted
// controls can select defaults and member errors precede typed-field errors.
//
// Gin caches the body for the second binding, which retains description,
// filters, identity, and metadata from the request. Normalized controls then
// replace the decoded values, including defaults for omitted members. Both
// binding failures use the generic body message; validation uses its first
// member error text.
func bindOpsAlertRule(c *gin.Context) *ops.OpsAlertRule {
	var raw map[string]json.RawMessage
	if err := c.ShouldBindBodyWith(&raw, binding.JSON); err != nil {
		response.BadRequest(c, "Invalid request body")
		return nil
	}
	validated, err := validateOpsAlertRulePayload(raw)
	if err != nil {
		response.BadRequest(c, err.Error())
		return nil
	}

	var rule ops.OpsAlertRule
	if err := c.ShouldBindBodyWith(&rule, binding.JSON); err != nil {
		response.BadRequest(c, "Invalid request body")
		return nil
	}

	rule.Name = validated.Name
	rule.MetricType = validated.MetricType
	rule.Operator = validated.Operator
	rule.Threshold = validated.Threshold
	rule.WindowMinutes = validated.WindowMinutes
	rule.SustainedMinutes = validated.SustainedMinutes
	rule.CooldownMinutes = validated.CooldownMinutes
	rule.Severity = validated.Severity
	rule.Enabled = validated.Enabled
	rule.NotifyEmail = validated.NotifyEmail

	return &rule
}

// DeleteAlertRule deletes a positive decimal route identity after checking
// monitoring. Invalid IDs return bad request; service failures use
// application-error mapping, and a successful call returns deleted=true.
// DELETE /api/v1/admin/ops/alert-rules/:id
func (h *OpsHandler) DeleteAlertRule(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	if err := h.opsService.RequireMonitoringEnabled(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid rule ID")
		return
	}

	if err := h.opsService.DeleteAlertRule(c.Request.Context(), id); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"deleted": true})
}

// GetAlertEvent loads a positive decimal event identity after checking
// monitoring. Invalid route IDs return bad request, and missing-event or
// storage errors are mapped by the service and shared response helper.
// GET /api/v1/admin/ops/alert-events/:id
func (h *OpsHandler) GetAlertEvent(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	if err := h.opsService.RequireMonitoringEnabled(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid event ID")
		return
	}

	ev, err := h.opsService.GetAlertEventByID(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, ev)
}

// UpdateAlertEventStatus accepts resolved or manual_resolved for a positive
// event ID after checking monitoring. It trims but does not fold status case,
// assigns current UTC resolution time, and returns service errors through the
// shared response helper.
// PUT /api/v1/admin/ops/alert-events/:id/status
func (h *OpsHandler) UpdateAlertEventStatus(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	if err := h.opsService.RequireMonitoringEnabled(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid event ID")
		return
	}

	var payload UpdateAlertEventStatusRequest
	if err := c.ShouldBindJSON(&payload); err != nil {
		response.BadRequest(c, "Invalid request body")
		return
	}
	payload.Status = strings.TrimSpace(payload.Status)
	if payload.Status == "" {
		response.BadRequest(c, "Invalid status")
		return
	}
	if payload.Status != ops.OpsAlertStatusResolved && payload.Status != ops.OpsAlertStatusManualResolved {
		response.BadRequest(c, "Invalid status")
		return
	}

	var resolvedAt *time.Time
	if payload.Status == ops.OpsAlertStatusResolved || payload.Status == ops.OpsAlertStatusManualResolved {
		now := time.Now().UTC()
		resolvedAt = &now
	}
	if err := h.opsService.UpdateAlertEventStatus(c.Request.Context(), id, payload.Status, resolvedAt); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"updated": true})
}

// CreateAlertSilence parses a trimmed RFC3339 deadline and forwards rule,
// platform, group, region, and reason after checking monitoring. A context
// subject supplies CreatedBy without a positivity check here; service
// validation requires a positive rule and nonblank platform but no
// future-deadline check.
// POST /api/v1/admin/ops/alert-silences
func (h *OpsHandler) CreateAlertSilence(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	if err := h.opsService.RequireMonitoringEnabled(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	var payload CreateAlertSilenceRequest
	if err := c.ShouldBindJSON(&payload); err != nil {
		response.BadRequest(c, "Invalid request body")
		return
	}
	until, err := time.Parse(time.RFC3339, strings.TrimSpace(payload.Until))
	if err != nil {
		response.BadRequest(c, "Invalid until")
		return
	}

	createdBy := (*int64)(nil)
	if subject, ok := authctx.GetAuthSubjectFromContext(c); ok {
		uid := subject.UserID
		createdBy = &uid
	}

	silence := &ops.OpsAlertSilence{
		RuleID:    payload.RuleID,
		Platform:  strings.TrimSpace(payload.Platform),
		GroupID:   payload.GroupID,
		Region:    payload.Region,
		Until:     until,
		Reason:    strings.TrimSpace(payload.Reason),
		CreatedBy: createdBy,
	}

	created, err := h.opsService.CreateAlertSilence(c.Request.Context(), silence)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, created)
}

// ListAlertEvents returns filtered alert events after checking monitoring,
// using limit twenty when omitted and requiring a positive supplied limit.
// PostgreSQL subsequently caps the requested limit at five hundred.
//
// The cursor requires before_fired_at and positive before_id together, and its
// timestamp accepts the two RFC3339 layouts. email_sent accepts case-folded
// true/1 or false/0. Status and severity are trimmed without local enum
// validation. Optional time parameters use the shared twenty-four-hour parser,
// but absent parameters leave both time fields unset; platform and a positive
// optional group are independent filters.
func (h *OpsHandler) ListAlertEvents(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	if err := h.opsService.RequireMonitoringEnabled(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	limit := 20
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			response.BadRequest(c, "Invalid limit")
			return
		}
		limit = n
	}

	filter := &ops.OpsAlertEventFilter{
		Limit:    limit,
		Status:   strings.TrimSpace(c.Query("status")),
		Severity: strings.TrimSpace(c.Query("severity")),
	}

	if v := strings.TrimSpace(c.Query("email_sent")); v != "" {
		vv := strings.ToLower(v)
		switch vv {
		case "true", "1":
			b := true
			filter.EmailSent = &b
		case "false", "0":
			b := false
			filter.EmailSent = &b
		default:
			response.BadRequest(c, "Invalid email_sent")
			return
		}
	}

	// Use firing time and row ID together to continue the repository's descending
	// tie-broken order; either cursor component alone is rejected.
	rawTS := strings.TrimSpace(c.Query("before_fired_at"))
	rawID := strings.TrimSpace(c.Query("before_id"))
	if (rawTS == "") != (rawID == "") {
		response.BadRequest(c, "before_fired_at and before_id must be provided together")
		return
	}
	if rawTS != "" {
		ts, err := time.Parse(time.RFC3339Nano, rawTS)
		if err != nil {
			// Try the second RFC3339 layout before rejecting cursor text, matching the
			// two-layout handling used by other Ops timestamp parameters.
			if t2, err2 := time.Parse(time.RFC3339, rawTS); err2 == nil {
				ts = t2
			} else {
				response.BadRequest(c, "Invalid before_fired_at")
				return
			}
		}
		filter.BeforeFiredAt = &ts
	}
	if rawID != "" {
		id, err := strconv.ParseInt(rawID, 10, 64)
		if err != nil || id <= 0 {
			response.BadRequest(c, "Invalid before_id")
			return
		}
		filter.BeforeID = &id
	}

	if platform := strings.TrimSpace(c.Query("platform")); platform != "" {
		filter.Platform = platform
	}
	if v := strings.TrimSpace(c.Query("group_id")); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			response.BadRequest(c, "Invalid group_id")
			return
		}
		filter.GroupID = &id
	}
	if startTime, endTime, err := parseOpsTimeRange(c, "24h"); err == nil {
		// Install parsed time bounds only when a time parameter is nonblank, keeping
		// an omitted range from narrowing event history to the parser's default.
		if strings.TrimSpace(c.Query("start_time")) != "" || strings.TrimSpace(c.Query("end_time")) != "" || strings.TrimSpace(c.Query("time_range")) != "" {
			filter.StartTime = &startTime
			filter.EndTime = &endTime
		}
	} else {
		response.BadRequest(c, err.Error())
		return
	}

	events, err := h.opsService.ListAlertEvents(c.Request.Context(), filter)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, events)
}
