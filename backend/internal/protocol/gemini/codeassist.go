package gemini

import (
	"bytes"
	"encoding/json"
)

// Code Assist project discovery can return both a paid tier and a current
// tier, with tier identifiers represented as either strings or objects.
// Authorization prefers a nonempty paid identifier before the current tier,
// while allowed onboarding tiers provide another source when neither registered
// tier supplies an identifier.
//
// TierInfo decodes both representations into the same ID field, retaining
// existing receiver state for empty or null input and replacing it after a
// successful string or object decode. Project discovery reads the load response's
// project string directly; onboarding can return the project as a string or an
// object containing id, and authorization inspects that result after Done is true.

// LoadCodeAssistRequest supplies client metadata to the internal Code Assist endpoint.
type LoadCodeAssistRequest struct {
	Metadata LoadCodeAssistMetadata `json:"metadata"` // requesting client metadata
}

// LoadCodeAssistMetadata identifies the IDE, platform, and plugin requesting discovery.
type LoadCodeAssistMetadata struct {
	IDEType    string `json:"ideType"`    // client IDE identifier
	Platform   string `json:"platform"`   // client platform identifier
	PluginType string `json:"pluginType"` // client plugin identifier
}

// TierInfo stores the tier identifier decoded from a string or object.
type TierInfo struct {
	ID string `json:"id"` // upstream tier identifier
}

// LoadCodeAssistResponse holds registered tiers, project identity, and onboarding choices.
type LoadCodeAssistResponse struct {
	// CurrentTier and PaidTier identify registered tiers. GetTier prefers
	// PaidTier when its ID is nonempty, then returns CurrentTier's ID; absent
	// objects remain nil so authorization can inspect allowed onboarding tiers.
	CurrentTier             *TierInfo `json:"currentTier,omitempty"`             // current registered tier
	PaidTier                *TierInfo `json:"paidTier,omitempty"`                // paid registered tier
	CloudAICompanionProject string    `json:"cloudaicompanionProject,omitempty"` // discovered project identifier
	// AllowedTiers supplies onboarding choices when registered tiers provide
	// no ID. ExtractTierIDFromAllowedTiers selects a nonblank default, then scans
	// for the first nonblank ID if that selection remains LEGACY.
	AllowedTiers []AllowedTier `json:"allowedTiers,omitempty"`
}

// AllowedTier describes an onboarding tier advertised by the Code Assist response.
type AllowedTier struct {
	ID string `json:"id"` // upstream onboarding tier
	// IsDefault marks a preferred onboarding choice. ExtractTierIDFromAllowedTiers
	// considers nonblank default IDs first, then scans other IDs if the selection
	// remains LEGACY, including when a default tier itself has that literal ID.
	IsDefault bool `json:"isDefault,omitempty"`
}

// OnboardUserRequest supplies a selected tier and client metadata for enrollment.
type OnboardUserRequest struct {
	TierID   string                 `json:"tierId"`   // selected onboarding tier
	Metadata LoadCodeAssistMetadata `json:"metadata"` // requesting client metadata
}

// OnboardUserResponse holds completion evidence and the optional discovered project result.
type OnboardUserResponse struct {
	// Done and Response are consumed together by GeminiAuthorization project
	// discovery: it reads the optional Response project only after Done is true.
	// String projects and object id strings return directly after trimming, even
	// when empty; missing or unsupported result values use Resource Manager.
	Done     bool                   `json:"done"`               // upstream completion flag
	Response *OnboardUserResultData `json:"response,omitempty"` // optional onboarding result
	Name     string                 `json:"name,omitempty"`     // upstream operation name
}

// OnboardUserResultData holds the polymorphic project value returned after enrollment.
type OnboardUserResultData struct {
	// CloudAICompanionProject can decode as a project string or an object.
	// GeminiAuthorization trims the string or the object's string id field and
	// returns it, including an empty result, when onboarding reports completion.
	CloudAICompanionProject any `json:"cloudaicompanionProject,omitempty"`
}

// UnmarshalJSON decodes a tier from a JSON string or an object containing id.
// Empty and null input leaves the receiver unchanged; a successfully decoded
// empty string clears ID, as does an object without an id field.
//
// Both accepted representations replace the receiver after successful
// decoding, so an invalid value returns its decoding error without publishing
// partial tier state. The local alias avoids invoking this decoder recursively
// for object input. Callers supply a writable receiver and retain responsibility
// for resolving paid versus current tier precedence.
func (t *TierInfo) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || string(data) == "null" {
		return nil
	}
	if data[0] == '"' {
		// Legacy tier responses encode the identifier as a string. Decode that
		// representation before the object path so both forms expose the same ID field.
		var id string
		if err := json.Unmarshal(data, &id); err != nil {
			return err
		}
		t.ID = id
		return nil
	}
	// alias retains TierInfo's fields while bypassing its custom JSON decoder.
	type alias TierInfo
	var decoded alias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*t = TierInfo(decoded)
	return nil
}

// GetTier returns the preferred registered tier ID from a nonnil response.
// A paid tier with a nonempty ID wins; otherwise the current tier's ID is
// returned when that object exists, including its possible empty value.
//
// Missing registered tier objects produce an empty string for authorization's
// allowed-tier or onboarding selection. IDs are returned as supplied rather
// than trimmed, so callers apply their own blank-string policy when interpreting
// registration or persisting the selected tier.
func (r *LoadCodeAssistResponse) GetTier() string {
	if r.PaidTier != nil && r.PaidTier.ID != "" {
		return r.PaidTier.ID
	}
	if r.CurrentTier != nil {
		return r.CurrentTier.ID
	}
	return ""
}
