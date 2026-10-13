package ops

import "time"

// User error responses copy a fixed set of request fields from internal error
// logs and translate stored classifications into frontend category codes.
// Ownership is checked by GetUserErrorRequestDetail before conversion; the
// converters copy captured body text and retain selected pointers from the
// supplied record.

// UserErrorRequest carries the request fields selected for the owning user
// response.
type UserErrorRequest struct {
	RequestID string    `json:"request_id"` // stored platform request identifier
	ID        int64     `json:"id"`         // persisted error observation identity
	CreatedAt time.Time `json:"created_at"` // stored error creation time
	// Model prefers nonempty RequestedModel from the internal error log and
	// otherwise uses its execution Model. This keeps the response label tied to
	// the recorded requested model when available.
	Model           string `json:"model"`            // user-visible requested model label
	InboundEndpoint string `json:"inbound_endpoint"` // observed incoming endpoint path
	// StatusCode copies the internal query display status. PostgreSQL prefers a
	// nonnull upstream status, then client status, then zero, so this value can
	// differ from the final client response code.
	StatusCode int    `json:"status_code"`
	Category   string `json:"category"` // frontend error category code
	Platform   string `json:"platform"` // recorded execution platform snapshot
	Message    string `json:"message"`  // gateway error description text
	KeyName    string `json:"key_name"` // owner-visible API-key name
	// KeyDeleted copies the joined API-key soft-deletion flag. PostgreSQL sets it
	// when api_keys.deleted_at is present; false also covers a missing joined key
	// row.
	KeyDeleted bool `json:"key_deleted"`
	// ClientIP copies the address string from the internal pointer when present.
	// An absent address becomes empty text and is omitted from JSON.
	ClientIP  string `json:"client_ip,omitempty"`  // observed request client address
	GroupName string `json:"group_name,omitempty"` // current joined group name
	// RequestType retains the internal record pointer when present. Callers
	// treating the DTO and source as separate values must leave this pointed-to
	// code unchanged.
	RequestType *int16 `json:"request_type,omitempty"` // stored request protocol code
	Stream      bool   `json:"stream"`                 // observed streaming request flag
	UserAgent   string `json:"user_agent,omitempty"`   // observed request user agent
}

// UserErrorRequestList carries a page of owner-visible error observations and
// its pagination.
type UserErrorRequestList struct {
	Items    []*UserErrorRequest `json:"items"`     // owner-visible error observation entries
	Total    int                 `json:"total"`     // matching owner observation count
	Page     int                 `json:"page"`      // one-based returned observation page
	PageSize int                 `json:"page_size"` // returned observations per page
}

// UserErrorRequestDetail carries the owner-visible request and its captured
// error response fields.
type UserErrorRequestDetail struct {
	UserErrorRequest // owner-visible base request fields
	// ErrorBody copies captured error response text from the internal detail.
	// Conversion uses that text as supplied, so its redaction follows the earlier
	// capture path.
	ErrorBody string `json:"error_body"`
	// UpstreamStatusCode retains the source pointer when present. Readers treat
	// the pointed-to status as immutable while the internal detail and DTO are
	// both used.
	UpstreamStatusCode *int `json:"upstream_status_code,omitempty"`
}

// MapUserErrorCategory returns the frontend category for exact stored phase and
// type labels. Request-phase categories depend on their error type, while
// unrecognized phase/type combinations use other; the frontend localizes these
// stable codes.
func MapUserErrorCategory(phase, errType string) string {
	switch phase {
	case "auth":
		return "auth"
	case "routing":
		return "service_unavailable"
	case "provider_auth", "upstream", "network":
		return "upstream"
	case "internal":
		return "internal"
	case "request":
		switch errType {
		case "rate_limit_error":
			return "rate_limit"
		case "billing_error", "subscription_error":
			return "quota"
		case "invalid_request_error":
			return "invalid_request"
		case "cyber_policy", "cyber_policy_session_blocked":
			return "cyber"
		}
	}
	return "other"
}

// CategoryToFilter returns stored phase and type selections for a user-facing
// category. The repository combines supplied phase and type lists as AND
// predicates; other and unknown categories return nil lists, leaving all
// categories eligible.
func CategoryToFilter(category string) (phases []string, errorTypes []string) {
	switch category {
	case "auth":
		return []string{"auth"}, nil
	case "service_unavailable":
		return []string{"routing"}, nil
	case "upstream":
		return []string{"provider_auth", "upstream", "network"}, nil
	case "internal":
		return []string{"internal"}, nil
	case "rate_limit":
		return nil, []string{"rate_limit_error"}
	case "quota":
		return nil, []string{"billing_error", "subscription_error"}
	case "invalid_request":
		return nil, []string{"invalid_request_error"}
	case "cyber":
		return []string{"request"}, []string{"cyber_policy", "cyber_policy_session_blocked"}
	default:
		return nil, nil
	}
}

// ToUserErrorRequest copies selected request fields from an internal error log
// and returns nil for nil input. RequestedModel takes display precedence over
// execution Model, category codes follow the stored classification, and
// RequestType remains shared with the source. Authorization belongs to the
// owning service caller.
func ToUserErrorRequest(e *OpsErrorLog) *UserErrorRequest {
	if e == nil {
		return nil
	}
	model := e.RequestedModel
	// When requested-model metadata is absent, use the execution model so the
	// user-facing label still has its available recorded source.
	if model == "" {
		model = e.Model
	}
	clientIP := ""
	if e.ClientIP != nil {
		clientIP = *e.ClientIP
	}
	return &UserErrorRequest{
		ID:              e.ID,
		RequestID:       e.RequestID,
		CreatedAt:       e.CreatedAt,
		Model:           model,
		InboundEndpoint: e.InboundEndpoint,
		StatusCode:      e.StatusCode,
		Category:        MapUserErrorCategory(e.Phase, e.Type),
		Platform:        e.Platform,
		Message:         e.Message,
		KeyName:         e.APIKeyName,
		KeyDeleted:      e.APIKeyDeleted,
		ClientIP:        clientIP,
		GroupName:       e.GroupName,
		RequestType:     e.RequestType,
		Stream:          e.Stream,
		UserAgent:       e.UserAgent,
	}
}

// ToUserErrorRequestDetail copies selected request and captured response
// fields, returning nil for nil input. It performs no body redaction or
// ownership check; callers use the capture policy and owning service
// authorization, and UpstreamStatusCode and RequestType remain shared pointers.
func ToUserErrorRequestDetail(e *OpsErrorLogDetail) *UserErrorRequestDetail {
	if e == nil {
		return nil
	}
	base := ToUserErrorRequest(&e.OpsErrorLog)
	return &UserErrorRequestDetail{
		UserErrorRequest:   *base,
		ErrorBody:          e.ErrorBody,
		UpstreamStatusCode: e.UpstreamStatusCode,
	}
}
