package openai

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
)

// OpenAIRateLimitResetCreditDetailPayload accepts alternate upstream reset credit field names.
type OpenAIRateLimitResetCreditDetailPayload struct {
	ExpiresAt      string `json:"expires_at,omitempty"`
	ExpiresAtCamel string `json:"expiresAt,omitempty"`
	ResetType      string `json:"reset_type,omitempty"`
	ResetTypeCamel string `json:"resetType,omitempty"`
	Status         string `json:"status,omitempty"`
}

// OpenAIRateLimitResetCreditDetailsPayload accepts count and list containers from different API versions.
type OpenAIRateLimitResetCreditDetailsPayload struct {
	AvailableCount        json.RawMessage `json:"available_count,omitempty"`
	AvailableCountCamel   json.RawMessage `json:"availableCount,omitempty"`
	Credits               json.RawMessage `json:"credits,omitempty"`
	RateLimitResetCredits json.RawMessage `json:"rate_limit_reset_credits,omitempty"`
	Items                 json.RawMessage `json:"items,omitempty"`
	Data                  json.RawMessage `json:"data,omitempty"`
}

// OpenAIRateLimitResetCreditDetails holds filtered counts and expiry details for merging into usage.
type OpenAIRateLimitResetCreditDetails struct {
	AvailableCount       *int
	AvailableCreditCount int
	CreditListPresent    bool
	Credits              []OpenAIRateLimitResetCreditDetail
}

// ParseOpenAIRateLimitResetCreditDetails accepts alternate response shapes and filters by credit type and status.
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

// ParseOpenAIResetCreditAvailableCount returns the first nonnegative integer encoded as a number or string.
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
				continue
			}
			parsed, err := strconv.Atoi(strings.TrimSpace(text))
			if err != nil {
				continue
			}
			count = parsed
		} else if err := json.Unmarshal(trimmed, &count); err != nil {
			continue
		}
		if count >= 0 {
			return &count
		}
	}
	return nil
}

// FirstPresentResetCreditPayload parses the first non-null list; an empty list counts as present.
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
