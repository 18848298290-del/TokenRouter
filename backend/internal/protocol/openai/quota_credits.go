package openai

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
)

// quota_credits.go parses reset-credit response variants while retaining count
// presence, list presence, and eligible expiry details.
// Reset-credit responses can expose their count and list through several
// compatible field spellings. Parsing preserves the distinction between an
// absent list and an explicitly empty list, while the first supplied nonnull
// list owns the decision even when its decoding fails.
//
// A credit is eligible when its type and status are blank or match the
// supported Codex/available labels. Eligible entries contribute to the available
// entry count even when expiry text is absent; only nonblank expiry observations
// enter the reduced detail list. Expiry text is retained in source order rather
// than parsed or filtered by time here. Callers merge these observations with
// usage and choose their own expiry-validation and available-count policy.

// OpenAIRateLimitResetCreditDetailPayload decodes compatible expiry, type, and
// status fields.
type OpenAIRateLimitResetCreditDetailPayload struct {
	// ExpiresAt and ExpiresAtCamel describe alternate expiry spellings.
	// Parsing prefers trimmed ExpiresAt and uses the camelCase value when it
	// is blank; missing expiry still contributes to the eligible-entry count.
	ExpiresAt      string `json:"expires_at,omitempty"` // snake_case expiry text
	ExpiresAtCamel string `json:"expiresAt,omitempty"`  // camelCase expiry text
	// ResetType and ResetTypeCamel name the credit's scope. Parsing prefers
	// the trimmed snake_case spelling and accepts a blank scope or a
	// case-insensitive codex_rate_limits value from the selected spelling.
	ResetType      string `json:"reset_type,omitempty"` // snake_case credit scope
	ResetTypeCamel string `json:"resetType,omitempty"`  // camelCase credit scope
	// Status is eligible when blank or case-insensitively available after
	// trimming. Other status labels remove this entry from count and expiry results.
	Status string `json:"status,omitempty"`
}

// OpenAIRateLimitResetCreditDetailsPayload decodes alternate count and list
// fields from quota JSON.
type OpenAIRateLimitResetCreditDetailsPayload struct {
	// AvailableCount and AvailableCountCamel retain raw number/string forms.
	// ParseOpenAIResetCreditAvailableCount selects the first usable nonnegative
	// integer in this order, including zero, while skipping invalid observations.
	AvailableCount      json.RawMessage `json:"available_count,omitempty"` // snake_case count encoding
	AvailableCountCamel json.RawMessage `json:"availableCount,omitempty"`  // camelCase count encoding
	// Credits, RateLimitResetCredits, Items, and Data are alternative list
	// containers in precedence order. The first nonnull value owns list
	// decoding, including an empty list or an error; later containers are skipped.
	Credits               json.RawMessage `json:"credits,omitempty"`                  // preferred credit list
	RateLimitResetCredits json.RawMessage `json:"rate_limit_reset_credits,omitempty"` // alternate credit list
	Items                 json.RawMessage `json:"items,omitempty"`                    // generic items container
	Data                  json.RawMessage `json:"data,omitempty"`                     // generic data container
}

// OpenAIRateLimitResetCreditDetails holds filtered counts and expiry details
// for merging into usage.
type OpenAIRateLimitResetCreditDetails struct {
	// AvailableCount retains the first usable count observation, including
	// explicit zero. It can be returned alongside a later credit-list decoding error.
	AvailableCount *int
	// AvailableCreditCount counts eligible entries before checking expiry text.
	// Credits contains their nonblank expiry details, so it can be shorter than
	// that count. CreditListPresent distinguishes a decoded empty list from absence.
	AvailableCreditCount int                                // eligible entry count
	CreditListPresent    bool                               // decoded list presence
	Credits              []OpenAIRateLimitResetCreditDetail // retained expiry details
}

// ParseOpenAIRateLimitResetCreditDetails reads a direct credit array or a
// compatible object containing count and list fields. Blank input returns
// empty observations; malformed top-level input returns a decoding error.
// A list-decoding error can accompany an already parsed AvailableCount.
//
// Blank type/status fields remain eligible alongside Codex available credits.
// Eligible entries count before expiry inspection, while only nonblank expiry
// strings enter Credits in source order. This parser does not validate expiry
// format or compare timestamps with the clock; callers choose that policy when
// merging or displaying the reduced observations.
func ParseOpenAIRateLimitResetCreditDetails(body []byte) (OpenAIRateLimitResetCreditDetails, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return OpenAIRateLimitResetCreditDetails{}, nil
	}

	var rawCredits []*OpenAIRateLimitResetCreditDetailPayload
	var availableCount *int
	var creditListPresent bool
	if trimmed[0] == '[' {
		if err := json.Unmarshal(trimmed, &rawCredits); err != nil {
			return OpenAIRateLimitResetCreditDetails{}, err
		}
		creditListPresent = true
	} else {
		var payload OpenAIRateLimitResetCreditDetailsPayload
		if err := json.Unmarshal(trimmed, &payload); err != nil {
			return OpenAIRateLimitResetCreditDetails{}, err
		}
		availableCount = ParseOpenAIResetCreditAvailableCount(payload.AvailableCount, payload.AvailableCountCamel)
		var err error
		rawCredits, creditListPresent, err = FirstPresentResetCreditPayload(
			payload.Credits,
			payload.RateLimitResetCredits,
			payload.Items,
			payload.Data,
		)
		if err != nil {
			return OpenAIRateLimitResetCreditDetails{AvailableCount: availableCount}, err
		}
	}

	credits := make([]OpenAIRateLimitResetCreditDetail, 0, len(rawCredits))
	availableCreditCount := 0
	for _, raw := range rawCredits {
		if raw == nil {
			continue
		}
		resetType := strings.TrimSpace(raw.ResetType)
		// Alternate type spelling supplies scope when the preferred field is
		// blank, keeping missing scope compatible while excluding a supplied
		// non-Codex credit scope.
		if resetType == "" {
			resetType = strings.TrimSpace(raw.ResetTypeCamel)
		}
		if resetType != "" && !strings.EqualFold(resetType, "codex_rate_limits") {
			continue
		}
		if status := strings.TrimSpace(raw.Status); status != "" && !strings.EqualFold(status, "available") {
			continue
		}
		availableCreditCount++
		// Eligibility establishes the available-entry count independently of expiry.
		// Missing expiry omits only the reduced detail, letting callers
		// distinguish count from usable timestamp observations.
		expiresAt := strings.TrimSpace(raw.ExpiresAt)
		if expiresAt == "" {
			expiresAt = strings.TrimSpace(raw.ExpiresAtCamel)
		}
		if expiresAt == "" {
			continue
		}
		credits = append(credits, OpenAIRateLimitResetCreditDetail{ExpiresAt: expiresAt})
	}
	return OpenAIRateLimitResetCreditDetails{
		AvailableCount:       availableCount,
		AvailableCreditCount: availableCreditCount,
		CreditListPresent:    creditListPresent,
		Credits:              credits,
	}, nil
}

// ParseOpenAIResetCreditAvailableCount returns the first usable nonnegative int
// encoded as a JSON number or trimmed decimal string. Null, missing, invalid,
// negative, fractional, and out-of-range candidates are skipped; nil reports
// that no candidate supplied a usable count.
func ParseOpenAIResetCreditAvailableCount(values ...json.RawMessage) *int {
	for _, value := range values {
		trimmed := bytes.TrimSpace(value)
		if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
			continue
		}

		var count int
		if trimmed[0] == '"' {
			var text string
			if err := json.Unmarshal(trimmed, &text); err != nil {
				// A malformed string candidate cannot provide the count. Continue
				// through later spellings so another compatible field can still
				// supply a usable observation.
				continue
			}
			parsed, err := strconv.Atoi(strings.TrimSpace(text))
			if err != nil {
				// Decimal parsing enforces int range and integer spelling. Skip
				// an unusable text count so a later candidate can provide the
				// available-count observation.
				continue
			}
			count = parsed
		} else if err := json.Unmarshal(trimmed, &count); err != nil {
			// A noninteger or incompatible JSON candidate cannot populate int.
			// Try the later available-count spelling instead of failing the
			// entire reset-credit response.
			continue
		}
		if count >= 0 {
			return &count
		}
	}
	return nil
}

// FirstPresentResetCreditPayload decodes the first nonblank, nonnull candidate
// as a credit list. An empty list returns true; malformed first-present input
// returns an error without trying later candidates. Nil and false indicate
// that every candidate was absent or null.
func FirstPresentResetCreditPayload(values ...json.RawMessage) ([]*OpenAIRateLimitResetCreditDetailPayload, bool, error) {
	for _, value := range values {
		trimmed := bytes.TrimSpace(value)
		if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
			continue
		}
		var credits []*OpenAIRateLimitResetCreditDetailPayload
		if err := json.Unmarshal(trimmed, &credits); err != nil {
			return nil, false, err
		}
		return credits, true, nil
	}
	return nil, false, nil
}
