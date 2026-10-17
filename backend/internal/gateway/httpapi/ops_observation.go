package httpapi

import (
	"slices"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/timing"
	"github.com/TokenFlux/TokenRouter/internal/ops"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
)

// ops_observation.go records request and WebSocket-turn failure observations
// separately from wire status.
// A committed HTTP status can remain successful while an in-band failure ends a
// stream. Ops retains intended failure status separately from wire status, and
// WebSocket turns keep their own captured provider and upstream evidence so
// later context reuse does not change earlier attribution. Ordinary stream
// markers and SLA-counted failures use different status rules.
//
// Marker handling is a serial request/turn operation. Ordered turn setup resets
// specific rule and upstream keys, the first marker is retained for the current
// observation scope, and recent WebSocket failures are kept in a bounded outer
// list. Event snapshots copy struct fields, while getters share nested storage.
// Callers serialize updates and keep retrieved event data immutable; individual
// Gin key locks do not establish one atomic turn transition.

const (
	// Gin context keys used by Ops error logger for capturing upstream error details.
	// Gateway adapters set these keys; OpsErrorLoggerMiddleware reads them.
	OpsUpstreamStatusCodeKey   = "ops_upstream_status_code"
	OpsUpstreamErrorMessageKey = "ops_upstream_error_message"
	OpsUpstreamErrorDetailKey  = "ops_upstream_error_detail"
	OpsUpstreamErrorsKey       = "ops_upstream_errors"
	OpsUpstreamModelKey        = "ops_upstream_model"

	// These keys store optional nonnegative millisecond stage observations read
	// by final error capture to locate time spent in failed request stages.
	OpsAuthLatencyMsKey     = "ops_auth_latency_ms"
	OpsRoutingLatencyMsKey  = "ops_routing_latency_ms"
	OpsUpstreamLatencyMsKey = "ops_upstream_latency_ms"
	OpsResponseLatencyMsKey = "ops_response_latency_ms"
	// These keys associate WebSocket queue/pick timing and connection reuse with
	// request diagnostics set by the transport observation adapters.
	OpsOpenAIWSQueueWaitMsKey = "ops_openai_ws_queue_wait_ms"
	OpsOpenAIWSConnPickMsKey  = "ops_openai_ws_conn_pick_ms"
	OpsOpenAIWSConnReusedKey  = "ops_openai_ws_conn_reused"
	OpsOpenAIWSConnIDKey      = "ops_openai_ws_conn_id"

	// OpsSkipPassthroughKey records a matched skip_monitoring=true passthrough rule.
	// The error logger checks this flag before recording the final failure.
	OpsSkipPassthroughKey = "ops_skip_passthrough"

	// OpsStreamErrorKey stores an in-band error after HTTP status commitment.
	// The error logger reads this marker when the wire status remains below 400.
	// Examples include concurrency wait timeouts and funds recheck failures
	// after a heartbeat has already committed HTTP 200.
	OpsStreamErrorKey  = "ops_stream_error"
	OpsStreamErrorsKey = "ops_stream_errors"
	OpsStreamTurnKey   = "ops_stream_turn"

	// ResponseCommittedKey records handled error output so later writers avoid
	// duplicate presentation. Some callers set it before delivery, which can still fail.
	ResponseCommittedKey = "response_committed"

	// Client business restrictions are logged but excluded from SLA and error-rate statistics.
	OpsClientBusinessLimitedKey                           = "ops_client_business_limited"
	OpsClientBusinessLimitedReasonKey                     = "ops_client_business_limited_reason"
	OpsClientBusinessLimitedReasonIPRestriction           = "api_key_ip_restriction"    // caller address rejected by Key policy
	OpsClientBusinessLimitedReasonAPIKeyGroupUnavailable  = "api_key_group_unavailable" // assigned group unavailable for this Key
	OpsClientBusinessLimitedReasonAPIKeyGroupUnassigned   = "api_key_group_unassigned"  // Key has no assigned routing group
	OpsClientBusinessLimitedReasonLocalFeatureGate        = "local_feature_gate"        // request blocked by local feature settings
	OpsClientBusinessLimitedReasonLocalPolicyDenied       = "local_policy_denied"       // request denied by local policy evaluation
	OpsClientBusinessLimitedReasonLocalModelConfiguration = "local_model_configuration" // model has invalid local routing configuration

	// maxOpsStreamErrorsPerRequest bounds retained failures from a long-lived
	// WebSocket connection, keeping recent turn snapshots available without
	// unbounded request history.
	maxOpsStreamErrorsPerRequest = 64
)

// OpsStreamError describes a client-visible failure after stream commitment.
// A keepalive or partial output can leave the HTTP status at 200 despite failure.
// The error logger uses the intended status and error details to record that failure.
// Examples include concurrency limits, funds rechecks after waiting,
// and exhausted provider selection after output starts.
// WebSocket turns retain separate snapshots; HTTP/SSE uses turn zero.
type OpsStreamError struct {
	ErrType string // client error category label
	Code    string // optional client error code
	Message string // trimmed client failure message
	// IntendedStatus supplies severity classification when positive, otherwise
	// logging uses wire status. CountTowardsSLA can also select it as the recorded
	// failure status after commitment.
	IntendedStatus int
	// CountTowardsSLA asks logging to record an IntendedStatus of at least 400 as
	// the logical failure status, while ordinary markers keep the committed wire
	// status.
	CountTowardsSLA bool
	// Turn identifies the captured WebSocket turn; zero denotes ordinary HTTP/SSE.
	// Serial marker handling compares the last retained turn before accepting
	// another failure snapshot.
	Turn int
	// SkipMonitoring captures the current rule decision when this failure is
	// marked. WebSocket logging uses this value even after another turn replaces
	// live request rule state.
	SkipMonitoring bool
	// ProviderID, UpstreamModel, UpstreamStatus, UpstreamMessage and
	// UpstreamDetail capture the selected execution evidence when marking this
	// failure. Later turn updates replace live context values while these scalar
	// fields retain that earlier attribution.
	ProviderID      int64  // captured selected provider identity
	UpstreamModel   string // captured effective upstream model
	UpstreamStatus  int    // captured upstream HTTP status
	UpstreamMessage string // captured upstream diagnostic message
	UpstreamDetail  string // captured upstream diagnostic detail
	// UpstreamErrors owns copied event structs at initial capture. Getter values
	// later share this slice and its pointers, so consumers keep those captured
	// events immutable.
	UpstreamErrors []*ops.OpsUpstreamErrorEvent
}

// MarkResponseCommitted marks error output handled in a required Gin context.
// Adapters can call it before delivery, so this marker prevents duplicate
// presentation but does not confirm that client bytes were successfully
// written.
func MarkResponseCommitted(c *gin.Context) { c.Set(ResponseCommittedKey, true) }

// IsResponseCommitted reads the handled-output marker from a required Gin
// context. Missing or differently typed values return false; this observation
// remains separate from the HTTP writer's actual status or byte commitment.
func IsResponseCommitted(c *gin.Context) bool {
	v, ok := c.Get(ResponseCommittedKey)
	if !ok {
		return false
	}
	b, _ := v.(bool)
	return b
}

// SetOpsLatencyMs stores a nonnegative millisecond observation under the
// supplied Gin key. Missing context, whitespace-only keys and negative values
// are ignored; accepted key spelling is retained rather than normalized.
func SetOpsLatencyMs(c *gin.Context, key string, value int64) {
	if c == nil || strings.TrimSpace(key) == "" || value < 0 {
		return
	}
	c.Set(key, value)
}

// MarkOpsProviderSlotAcquired freezes initial admission in the HTTP text collector.
// Requests outside that scope have no admission observation. Repeated provider
// acquisitions during retries leave the first successful admission endpoint unchanged.
func MarkOpsProviderSlotAcquired(c *gin.Context) {
	if c != nil && c.Request != nil {
		if stages := timing.StagesFromContext(c.Request.Context()); stages != nil {
			stages.Admit()
		}
	}
}

// SetOpsUpstreamModel stores a nonblank trimmed effective model before an
// upstream attempt. Nil context and blank values retain existing attribution;
// callers clear the prior attempt separately before selected-provider
// preparation should replace it.
func SetOpsUpstreamModel(c *gin.Context, model string) {
	if c == nil {
		return
	}
	if model = strings.TrimSpace(model); model != "" {
		c.Set(OpsUpstreamModelKey, model)
	}
}

// ClearOpsUpstreamModel clears retained model attribution before another
// provider's credentials or dispatch. Nil context is ignored; assigning an
// empty value prevents error capture from borrowing the previous attempt's
// upstream model.
func ClearOpsUpstreamModel(c *gin.Context) {
	if c == nil {
		return
	}
	c.Set(OpsUpstreamModelKey, "")
}

// MarkOpsClientBusinessLimited marks a client business restriction and stores a
// nonblank trimmed reason. Nil context skips updates; blank reason keeps any
// earlier reason, so callers choose the appropriate observation scope before
// reusing context state.
func MarkOpsClientBusinessLimited(c *gin.Context, reason string) {
	if c == nil {
		return
	}
	c.Set(OpsClientBusinessLimitedKey, true)
	if reason = strings.TrimSpace(reason); reason != "" {
		c.Set(OpsClientBusinessLimitedReasonKey, reason)
	}
}

// HasOpsClientBusinessLimited reports the typed client-restriction marker for
// this request. Nil context, absent values or a different stored type return
// false, leaving the error classifier to interpret its other evidence.
func HasOpsClientBusinessLimited(c *gin.Context) bool {
	if c == nil {
		return false
	}
	v, ok := c.Get(OpsClientBusinessLimitedKey)
	if !ok {
		return false
	}
	marked, _ := v.(bool)
	return marked
}

// OpsClientBusinessLimitedReason returns the trimmed stored client restriction
// reason. Missing context, absent values or a nonstring returns empty; reason
// text is read separately from the boolean marker needed by classification.
func OpsClientBusinessLimitedReason(c *gin.Context) string {
	if c == nil {
		return ""
	}
	v, ok := c.Get(OpsClientBusinessLimitedReasonKey)
	if !ok {
		return ""
	}
	reason, _ := v.(string)
	return strings.TrimSpace(reason)
}

// BeginOpsStreamTurn establishes the next positive WebSocket turn number and
// clears its rule/error observations before execution. Nil context or
// nonpositive turn makes no change. The retained stream-error list and other
// provider/model state remain available for their own owners.
//
// Callers serialize this setup before recording events for the new turn. It
// resets the current skip flag, upstream events, status, message and detail
// through separate Gin writes; it does not publish an atomic multi-key
// snapshot. Unique ordered turn numbers keep the following first-marker check
// scoped to the last retained turn while previous failure snapshots survive for
// final error logging.
func BeginOpsStreamTurn(c *gin.Context, turn int) {
	if c == nil || turn <= 0 {
		return
	}
	c.Set(OpsStreamTurnKey, turn)
	// Reset current rule and upstream-error keys before this serial turn runs,
	// retaining earlier failure snapshots and other attribution until their owners update it.
	c.Set(OpsSkipPassthroughKey, false)
	c.Set(OpsUpstreamErrorsKey, []*ops.OpsUpstreamErrorEvent{})
	c.Set(OpsUpstreamStatusCodeKey, 0)
	c.Set(OpsUpstreamErrorMessageKey, "")
	c.Set(OpsUpstreamErrorDetailKey, "")
}

// MarkOpsStreamError records the first ordinary in-band failure through shared
// marker handling. Serial HTTP observations retain one marker; WebSocket
// observations use the current turn and keep recent snapshots. Intended status
// affects severity while ordinary logging retains wire status.
func MarkOpsStreamError(c *gin.Context, errType, message string, intendedStatus int) {
	markOpsStreamError(c, OpsStreamError{
		ErrType:        errType,
		Message:        message,
		IntendedStatus: intendedStatus,
	})
}

// MarkOpsStreamFailure records in-band failure with an explicit code and SLA
// accounting request. Shared handling marks telemetry failed before
// deduplication; an already stored first marker can remain unchanged even when
// this later observation updates request failure telemetry.
func MarkOpsStreamFailure(c *gin.Context, errType, code, message string, intendedStatus int) {
	markOpsStreamError(c, OpsStreamError{
		ErrType:         errType,
		Code:            code,
		Message:         message,
		IntendedStatus:  intendedStatus,
		CountTowardsSLA: true,
	})
}

// markOpsStreamError captures failure metadata and applies serial HTTP or
// WebSocket marker retention. Callers serialize updates and establish ordered
// turn state; Gin locking around individual keys does not make this whole
// read-and-publish sequence atomic.
//
// SLA observations update request failure telemetry before first-marker checks.
// HTTP retains the first present marker. WebSocket suppresses another marker
// matching the last retained turn, appends a new turn snapshot and caps its
// history to recent entries. Provider and upstream evidence are captured before
// publication so later serial turns can reuse live context fields without
// rewriting those stored values.
func markOpsStreamError(c *gin.Context, streamErr OpsStreamError) {
	if c == nil {
		return
	}
	// Update request failure telemetry before marker deduplication, so a later SLA
	// observation can mark failure while the original client-visible marker
	// remains first.
	if streamErr.CountTowardsSLA && c.Request != nil {
		telemetry.UpdateRequest(c.Request.Context(), func(record *telemetry.RequestRecord) { record.State = "failed"; record.ErrorCode = "stream_error" })
	}
	streamErr.ErrType = strings.TrimSpace(streamErr.ErrType)
	streamErr.Code = strings.TrimSpace(streamErr.Code)
	streamErr.Message = strings.TrimSpace(streamErr.Message)
	streamErr.SkipMonitoring = currentOpsFailureSkipMonitoring(c)
	snapshotOpsStreamErrorContext(c, &streamErr)
	if GetOpenAIClientTransport(c) == OpenAIClientTransportWS {
		if value, ok := c.Get(OpsStreamTurnKey); ok {
			streamErr.Turn, _ = value.(int)
		}
		var errorsForRequest []OpsStreamError
		if value, ok := c.Get(OpsStreamErrorsKey); ok {
			errorsForRequest, _ = value.([]OpsStreamError)
		}
		// The serial flow checks the last stored turn to keep its first visible
		// failure; callers advance turn numbers before later observations can append
		// another snapshot.
		if len(errorsForRequest) > 0 && errorsForRequest[len(errorsForRequest)-1].Turn == streamErr.Turn {
			return
		}
		errorsForRequest = append(errorsForRequest, streamErr)
		// Retain the most recent turn failures at the connection history cap,
		// preventing a long-lived client from growing this request list without
		// bound.
		if len(errorsForRequest) > maxOpsStreamErrorsPerRequest {
			errorsForRequest = append([]OpsStreamError(nil), errorsForRequest[len(errorsForRequest)-maxOpsStreamErrorsPerRequest:]...)
		}
		c.Set(OpsStreamErrorsKey, errorsForRequest)
		c.Set(OpsStreamErrorKey, streamErr)
		return
	}
	// An existing HTTP marker keeps the first visible failure even when its stored
	// type is unexpected; later generic observations cannot replace that request
	// evidence.
	if _, exists := c.Get(OpsStreamErrorKey); exists {
		return
	}
	c.Set(OpsStreamErrorKey, streamErr)
}

// snapshotOpsStreamErrorContext captures provider and upstream evidence in one
// failure value. Nil context or destination is ignored. Positive telemetry
// provider identity and the supported Gin status/string values supply the
// scalar fields.
//
// Each upstream event struct is copied into new pointer storage, retaining nil
// entries and order. Those independent values keep the captured turn's attempt
// details stable when later serial turn setup replaces live context keys or
// changes the original event fields. Callers serialize this multi-key read with
// other observation updates.
func snapshotOpsStreamErrorContext(c *gin.Context, streamErr *OpsStreamError) {
	if c == nil || streamErr == nil {
		return
	}
	if c.Request != nil {
		if providerID, ok := c.Request.Context().Value(telemetry.ProviderID).(int64); ok && providerID > 0 {
			streamErr.ProviderID = providerID
		}
	}
	if value, ok := c.Get(OpsUpstreamModelKey); ok {
		streamErr.UpstreamModel, _ = value.(string)
		streamErr.UpstreamModel = strings.TrimSpace(streamErr.UpstreamModel)
	}
	if value, ok := c.Get(OpsUpstreamStatusCodeKey); ok {
		switch status := value.(type) {
		case int:
			streamErr.UpstreamStatus = status
		case int64:
			streamErr.UpstreamStatus = int(status)
		}
	}
	if value, ok := c.Get(OpsUpstreamErrorMessageKey); ok {
		streamErr.UpstreamMessage, _ = value.(string)
		streamErr.UpstreamMessage = strings.TrimSpace(streamErr.UpstreamMessage)
	}
	if value, ok := c.Get(OpsUpstreamErrorDetailKey); ok {
		streamErr.UpstreamDetail, _ = value.(string)
		streamErr.UpstreamDetail = strings.TrimSpace(streamErr.UpstreamDetail)
	}
	if value, ok := c.Get(OpsUpstreamErrorsKey); ok {
		if events, ok := value.([]*ops.OpsUpstreamErrorEvent); ok {
			streamErr.UpstreamErrors = make([]*ops.OpsUpstreamErrorEvent, 0, len(events))
			for _, event := range events {
				if event == nil {
					streamErr.UpstreamErrors = append(streamErr.UpstreamErrors, nil)
					continue
				}
				// Copy each event value before retaining its pointer in this turn snapshot,
				// preventing later event-field updates from rewriting the failure already
				// captured.
				copyOfEvent := *event
				streamErr.UpstreamErrors = append(streamErr.UpstreamErrors, &copyOfEvent)
			}
		}
	}
}

// currentOpsFailureSkipMonitoring selects suppression from request and
// final-attempt observations. Nil context or no usable evidence returns false.
// A true typed request flag wins immediately; an absent or false flag lets the
// last nonnil upstream event decide.
//
// Earlier attempt decisions can differ from the final one when failover
// recovers or another provider fails. When the request flag has not already
// selected suppression, using the last event prevents an earlier hidden
// attempt's event flag from suppressing this later client-visible failure.
func currentOpsFailureSkipMonitoring(c *gin.Context) bool {
	if c == nil {
		return false
	}
	if value, ok := c.Get(OpsSkipPassthroughKey); ok {
		if skip, _ := value.(bool); skip {
			return true
		}
	}
	if value, ok := c.Get(OpsUpstreamErrorsKey); ok {
		if events, ok := value.([]*ops.OpsUpstreamErrorEvent); ok {
			for _, event := range slices.Backward(events) {
				if event != nil {
					return event.SkipMonitoring
				}
			}
		}
	}
	return false
}

// GetOpsStreamError returns the current typed marker and whether one was found.
// Missing or mistyped state yields false; its nested event slice and pointers
// remain shared with the stored marker, so callers keep them immutable.
func GetOpsStreamError(c *gin.Context) (OpsStreamError, bool) {
	if c == nil {
		return OpsStreamError{}, false
	}
	v, ok := c.Get(OpsStreamErrorKey)
	if !ok {
		return OpsStreamError{}, false
	}
	se, ok := v.(OpsStreamError)
	return se, ok
}

// GetOpsStreamErrors copies the retained outer WebSocket failure list, or wraps
// the current typed marker when no nonempty list exists. Missing context yields
// nil. Nested event slices and pointers stay shared, requiring immutable access
// after retrieval.
func GetOpsStreamErrors(c *gin.Context) []OpsStreamError {
	if c == nil {
		return nil
	}
	if value, ok := c.Get(OpsStreamErrorsKey); ok {
		if errorsForRequest, ok := value.([]OpsStreamError); ok && len(errorsForRequest) > 0 {
			return append([]OpsStreamError(nil), errorsForRequest...)
		}
	}
	// When no nonempty turn list is available, expose the current typed marker
	// so final logging can still consume retained failure evidence through one list.
	if streamErr, ok := GetOpsStreamError(c); ok {
		return []OpsStreamError{streamErr}
	}
	return nil
}

// SetOpsUpstreamError stores positive upstream status and nonblank trimmed
// diagnostic strings. Missing context skips updates, while zero/blank inputs
// retain earlier fields. Callers supply appropriately sanitized text because
// this setter performs trimming rather than credential or URL redaction.
func SetOpsUpstreamError(c *gin.Context, upstreamStatusCode int, upstreamMessage, upstreamDetail string) {
	if c == nil {
		return
	}
	if upstreamStatusCode > 0 {
		c.Set(OpsUpstreamStatusCodeKey, upstreamStatusCode)
	}
	if msg := strings.TrimSpace(upstreamMessage); msg != "" {
		c.Set(OpsUpstreamErrorMessageKey, msg)
	}
	if detail := strings.TrimSpace(upstreamDetail); detail != "" {
		c.Set(OpsUpstreamErrorDetailKey, detail)
	}
}

// AppendOpsUpstreamError appends one attempt value and updates request
// correlation observations. Missing context skips processing; nonpositive
// timestamps receive current Unix milliseconds, string fields are trimmed, and
// Message receives query sanitization. Detail, URL and response body remain for
// later queue preparation.
//
// Request telemetry retains bounded attempt outcome and provider evidence
// before the event pointer is published in Gin. Header observers own identifiers;
// errors never replace them with their separately collected Ops values.
// The first optional dispatch reference identifies an HTTP response or captured
// result even after another attempt starts; a conflicting provider is rejected.
// Authentication failures without an upstream status remain unnumbered beside
// earlier exchanges. A usage_invalid event reports
// unknown usage rather than failed inference. Rule matching then updates that
// published event's suppression flag, so callers serialize observation until
// this function returns and keep retained event values immutable for later
// snapshots.
func AppendOpsUpstreamError(c *gin.Context, ev ops.OpsUpstreamErrorEvent, dispatch ...telemetry.RequestAttemptRef) {
	if c == nil {
		return
	}
	if ev.AtUnixMs <= 0 {
		ev.AtUnixMs = time.Now().UnixMilli()
	}
	ev.Platform = strings.TrimSpace(ev.Platform)
	ev.UpstreamRequestID = strings.TrimSpace(ev.UpstreamRequestID)
	ev.UpstreamResponseBody = strings.TrimSpace(ev.UpstreamResponseBody)
	ev.Kind = strings.TrimSpace(ev.Kind)
	ev.Stage = strings.TrimSpace(ev.Stage)
	ev.Scope = strings.TrimSpace(ev.Scope)
	ev.Reason = strings.TrimSpace(ev.Reason)
	ev.UpstreamURL = strings.TrimSpace(ev.UpstreamURL)
	ev.Message = strings.TrimSpace(ev.Message)
	ev.Detail = strings.TrimSpace(ev.Detail)
	if ev.Message != "" {
		ev.Message = logredact.SanitizeUpstreamQueries(ev.Message)
	}

	var existing []*ops.OpsUpstreamErrorEvent
	if v, ok := c.Get(OpsUpstreamErrorsKey); ok {
		if arr, ok := v.([]*ops.OpsUpstreamErrorEvent); ok {
			existing = arr
		}
	}

	if c.Request != nil {
		number := 0
		beforeDispatch := ev.Stage == string(forward.GatewayFailureStageProviderAuth) && ev.UpstreamStatusCode == 0
		bound := len(dispatch) > 0 && dispatch[0].Number > 0
		if !beforeDispatch {
			if bound {
				number = dispatch[0].Number
			} else if stages := timing.StagesFromContext(c.Request.Context()); stages != nil {
				number = int(stages.Attempt())
			} else {
				number = telemetry.FindRequestAttempt(c.Request.Context(), 0, ev.ProviderID).Number
			}
		}
		telemetry.UpdateRequest(c.Request.Context(), func(record *telemetry.RequestRecord) {
			// A response-owned reference takes precedence over mutable timing state.
			// Reject provider conflicts instead of attributing this event to a different exchange.
			if bound && dispatch[0].ProviderID != ev.ProviderID {
				return
			}
			// Match provider and ordinal before updating diagnostic outcome. An
			// unattributed error remains in Ops instead of creating a conflicting attempt.
			outcome := "failed"
			if ev.Kind == "usage_invalid" {
				outcome = "usage_unknown"
			}
			matched := false
			providerObserved := false
			for i := range record.Attempts {
				attempt := &record.Attempts[i]
				providerObserved = providerObserved || attempt.ProviderID == ev.ProviderID
				if attempt.Number == number && attempt.ProviderID == ev.ProviderID && (number > 0 || attempt.RequestID == ev.UpstreamRequestID) {
					// Headers already selected the diagnostic ID before this error event.
					// Ops may carry another upstream header, so it cannot overwrite that identity.
					attempt.Outcome, attempt.Status = outcome, ev.UpstreamStatusCode
					attempt.Stage, attempt.Reason = ev.Stage, ev.Reason
					matched = true
					break
				}
			}
			if !matched && len(record.Attempts) < 256 && (beforeDispatch || !providerObserved) {
				// Credential failures have no dispatch ordinal. Keep their provider evidence
				// beside earlier exchanges, while late errors for observed providers remain in Ops.
				record.Attempts = append(record.Attempts, telemetry.RequestAttempt{Number: 0, ProviderID: ev.ProviderID, Status: ev.UpstreamStatusCode, Outcome: outcome, Stage: ev.Stage, Reason: ev.Reason})
			}
		})
	}
	evCopy := ev
	existing = append(existing, &evCopy)
	c.Set(OpsUpstreamErrorsKey, existing)

	// Match suppression after publishing the event pointer; serial callers
	// complete this decision before another observer captures the retained attempt
	// for logging.
	checkSkipMonitoringForUpstreamEvent(c, &evCopy)
}

// checkSkipMonitoringForUpstreamEvent marks an attempt when a bound final-error
// rule requests monitoring suppression. The event is required; status zero or
// no rule service skips lookup. Detail precedes Message for keyword evidence,
// and empty text still permits status-only matching.
func checkSkipMonitoringForUpstreamEvent(c *gin.Context, ev *ops.OpsUpstreamErrorEvent) {
	if ev.UpstreamStatusCode == 0 {
		return
	}

	svc := BoundErrorPassthroughService(c)
	if svc == nil {
		return
	}

	// Use the best available body representation for keyword matching.
	// Even when body is empty, MatchRule can still match rules that only
	// specify ErrorCodes (no Keywords), so we always call it.
	body := ev.Detail
	if body == "" {
		body = ev.Message
	}

	rule := svc.MatchRule(ev.Platform, ev.UpstreamStatusCode, []byte(body))
	if rule != nil && rule.SkipMonitoring {
		ev.SkipMonitoring = true
	}
}
