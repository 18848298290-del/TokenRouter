package ops

import (
	"encoding/json"
	"strings"
)

// Stored attempt arrays can still use account_id and account_name from the
// earlier provider vocabulary. Readers translate those keys to provider_id and
// provider_name while keeping a supplied current key authoritative, including
// when its JSON value is null. RawMessage retains the original numeric and text
// encodings during that key translation, so a large identity is not first
// converted through a floating-point map value.
//
// After translation, typed decoding applies the attempt schema and ignores
// unknown fields. Empty text means an allocated empty list, while JSON null
// retains nil-list encoding. Writers omit empty event lists and use the event
// JSON tags, including the exclusion of request-local SkipMonitoring.

// ParseOpsUpstreamErrors decodes stored attempt JSON after translating legacy
// provider identity keys. Blank text returns an allocated empty list, JSON null
// returns nil, and invalid array or typed-field input returns a decoding error.
//
// For each record, a present provider_id or provider_name wins over its account
// counterpart even when the current JSON value is null. Key migration keeps
// values as RawMessage until typed decoding, retaining large integer identities
// through the round trip. Unknown event fields are discarded by that final
// decode.
func ParseOpsUpstreamErrors(raw string) ([]*OpsUpstreamErrorEvent, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return []*OpsUpstreamErrorEvent{}, nil
	}
	// Keep record values as RawMessage while renaming identity keys, so the
	// compatibility round trip does not convert integer provider IDs through
	// float64.
	var records []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &records); err != nil {
		return nil, err
	}
	for _, record := range records {
		for old, current := range map[string]string{"account_id": "provider_id", "account_name": "provider_name"} {
			if value, exists := record[old]; exists {
				// Presence decides precedence, including a current JSON null; replacing it
				// with a legacy value would silently change the saved provider identity.
				if _, present := record[current]; !present {
					record[current] = value
				}
				delete(record, old)
			}
		}
	}
	normalized, err := json.Marshal(records)
	if err != nil {
		return nil, err
	}
	var out []*OpsUpstreamErrorEvent
	if err := json.Unmarshal(normalized, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// marshalOpsUpstreamErrors encodes a nonempty attempt list for optional
// error-log storage. Empty lists or an encoding failure return nil, and event
// JSON tags omit request-local SkipMonitoring from the stored value.
func marshalOpsUpstreamErrors(events []*OpsUpstreamErrorEvent) *string {
	if len(events) == 0 {
		return nil
	}
	// Error-log preparation consumes an optional JSON pointer. If encoding fails,
	// omit the attempt payload so callers do not persist malformed JSON text.
	raw, err := json.Marshal(events)
	if err != nil || len(raw) == 0 {
		return nil
	}
	s := string(raw)
	return &s
}
