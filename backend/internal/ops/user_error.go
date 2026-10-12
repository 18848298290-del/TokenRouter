package ops

import "time"

// UserErrorRequest exposes allowlisted fields from the user's error request.
// Message is the gateway description; KeyName is the API key name visible to its owner.
// Client IP, user agent, group, request type, and stream describe the user's own request,
// matching the fields available in usage details.
// GetUserErrorRequestDetail checks ownership before returning error_body.
// Provider details, API key prefixes, upstream endpoints, and user email remain internal.
type UserErrorRequest struct {
	RequestID       string    `json:"request_id"`
	ID              int64     `json:"id"`
	CreatedAt       time.Time `json:"created_at"`
	Model           string    `json:"model"`
	InboundEndpoint string    `json:"inbound_endpoint"`
	StatusCode      int       `json:"status_code"`
	Category        string    `json:"category"`
	Platform        string    `json:"platform"`
	Message         string    `json:"message"`
	KeyName         string    `json:"key_name"`
	KeyDeleted      bool      `json:"key_deleted"`
	ClientIP        string    `json:"client_ip,omitempty"`
	GroupName       string    `json:"group_name,omitempty"`
	RequestType     *int16    `json:"request_type,omitempty"`
	Stream          bool      `json:"stream"`
	UserAgent       string    `json:"user_agent,omitempty"`
}

// UserErrorRequestList is a page of user error requests.
type UserErrorRequestList struct {
	Items    []*UserErrorRequest `json:"items"`
	Total    int                 `json:"total"`
	Page     int                 `json:"page"`
	PageSize int                 `json:"page_size"`
}

// UserErrorRequestDetail is the sanitized detail view for a selected request.
// It adds the upstream error body and status to UserErrorRequest.
type UserErrorRequestDetail struct {
	UserErrorRequest
	ErrorBody          string `json:"error_body"`
	UpstreamStatusCode *int   `json:"upstream_status_code,omitempty"`
}

// MapUserErrorCategory maps an error phase and type to a user-facing category code.
// The frontend localizes these stable codes.
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

// CategoryToFilter converts a user category into phase and type ANY filters.
// The other category and unknown values return empty slices to include all categories.
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

// ToUserErrorRequest copies allowlisted fields from an internal error log.
func ToUserErrorRequest(e *OpsErrorLog) *UserErrorRequest {
	if e == nil {
		return nil
	}
	model := e.RequestedModel
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

// ToUserErrorRequestDetail copies allowlisted fields from internal error details.
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
