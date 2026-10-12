package ws

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/gateway/tierpolicy"
)

// ApplyServiceTierFrame applies HTTP Fast policy rules to upstream response.create frames.
// Pass normalizes fast to priority; filter removes the tier; force actions set the tier; block returns a policy rejection.
// Valid JSON frames first undergo reasoning-effort validation. Other frame types then pass through unchanged.
// The tier is a top-level field; callers supply this turn's upstream model for policy matching.
func ApplyServiceTierFrame(frame []byte, model string, input tierpolicy.DecisionInput) ([]byte, *tierpolicy.BlockedError, error) {
	if len(frame) == 0 {
		return frame, nil, nil
	}
	if !gjson.ValidBytes(frame) {
		return frame, nil, nil
	}
	// Validate reasoning effort on each frame because session parameters can change between frames.
	if err := requeststate.ValidateOpenAIReasoningEffort(frame, model); err != nil {
		return frame, nil, err
	}
	frameType := strings.TrimSpace(gjson.GetBytes(frame, "type").String())
	// Apply Fast policy to response.create; leave other event types to upstream validation.
	if frameType != "response.create" {
		return frame, nil, nil
	}
	tierResult := gjson.GetBytes(frame, "service_tier")
	decision := tierpolicy.Resolve(input, tierResult.String(), tierResult.Exists())
	if decision.Blocked != nil {
		return frame, decision.Blocked, nil
	}
	if decision.DeleteField {
		trimmed, err := sjson.DeleteBytes(frame, "service_tier")
		if err != nil {
			return frame, nil, fmt.Errorf("strip service_tier from ws frame: %w", err)
		}
		return trimmed, nil, nil
	}
	if decision.Tier != "" && (!tierResult.Exists() || decision.Tier != tierResult.String()) {
		updated, err := sjson.SetBytes(frame, "service_tier", decision.Tier)
		if err != nil {
			return frame, nil, fmt.Errorf("apply service_tier in ws frame: %w", err)
		}
		return updated, nil, nil
	}
	return frame, nil, nil
}

// newOpenAIFastPolicyWSEventID returns an evt_-prefixed random identifier
// for correlating a server-emitted policy error in client logs.
func newOpenAIFastPolicyWSEventID() string {
	id, err := uuid.NewRandom()
	if err != nil {
		// Keep the identifier non-empty if random generation fails.
		return "evt_openai_fast_policy"
	}
	// Use a compact hexadecimal UUID after the event prefix.
	return "evt_" + strings.ReplaceAll(id.String(), "-", "")
}

// BuildFastPolicyBlockedEvent renders a Realtime-style error for a Fast policy rejection.
// The event ID supports log correlation and policy_violation gives clients a stable error code.
// It returns nil when err is nil.
//
//	{
//	  "event_id": "evt_<random>",
//	  "type": "error",
//	  "error": {
//	    "type": "invalid_request_error",
//	    "code": "policy_violation",
//	    "message": "..."
//	  }
//	}
func BuildFastPolicyBlockedEvent(err *tierpolicy.BlockedError) []byte {
	if err == nil {
		return nil
	}
	eventID := newOpenAIFastPolicyWSEventID()
	payload, mErr := json.Marshal(map[string]any{
		"event_id": eventID,
		"type":     "error",
		"error": map[string]any{
			"type":    "invalid_request_error",
			"code":    "policy_violation",
			"message": err.Message,
		},
	})
	if mErr != nil {
		// Return a minimal event if encoding fails.
		return []byte(`{"event_id":"` + eventID + `","type":"error","error":{"type":"invalid_request_error","code":"policy_violation","message":"openai fast policy blocked this request"}}`)
	}
	return payload
}
