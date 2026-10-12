package openai

import (
	"encoding/json"
	"errors"
	"strings"
)

// LiveCallRequest holds a normalized downstream creation request and the raw session JSON.
type LiveCallRequest struct {
	SDP     string          `json:"sdp"`
	Session json.RawMessage `json:"session"`
}

// ValidateLiveCallRequest requires nonempty SDP and a JSON object for the session.
func ValidateLiveCallRequest(request *LiveCallRequest) error {
	if request == nil || strings.TrimSpace(request.SDP) == "" {
		return errors.New("sdp is required")
	}
	if len(request.Session) == 0 || !json.Valid(request.Session) {
		return errors.New("session must be valid JSON")
	}
	var sessionObject map[string]json.RawMessage
	if err := json.Unmarshal(request.Session, &sessionObject); err != nil {
		return errors.New("session must be a JSON object")
	}
	if sessionObject == nil {
		return errors.New("session must be a JSON object")
	}
	return nil
}
