package completion

import "strings"

// RequestIdentity contains correlation identifiers captured before the HTTP request ends.
type RequestIdentity struct{ Client, Local, Upstream, PayloadHash string }

func ForcedRequestID(id string) bool {
	id = strings.TrimSpace(id)
	return strings.HasPrefix(id, "web_search:") || strings.HasPrefix(id, "grok-video:") || strings.HasPrefix(id, "grok_audio:") || strings.HasPrefix(id, "grok_realtime:")
}

// ResolveBillingKey chooses a financial deduplication key compatible with historical settlements.
// @project-doc docs/operations/request_lookup.md#billing_identity
func ResolveBillingKey(in RequestIdentity, generate func() string) string {
	upstream := strings.TrimSpace(in.Upstream)
	if upstream != "" && ForcedRequestID(upstream) {
		return upstream
	}
	if client := strings.TrimSpace(in.Client); client != "" {
		return "client:" + client
	}
	if local := strings.TrimSpace(in.Local); local != "" {
		return "local:" + local
	}
	if upstream != "" {
		return upstream
	}
	return "generated:" + generate()
}

func PayloadFingerprint(in RequestIdentity) string {
	if hash := strings.TrimSpace(in.PayloadHash); hash != "" {
		return hash
	}
	if client := strings.TrimSpace(in.Client); client != "" {
		return "client:" + client
	}
	if local := strings.TrimSpace(in.Local); local != "" {
		return "local:" + local
	}
	return ""
}

func StableAudioRequestID(id string, generate func() string) string {
	return stableRequestID("grok_audio:", id, generate)
}

func StableRealtimeRequestID(id string, generate func() string) string {
	return stableRequestID("grok_realtime:", id, generate)
}

func stableRequestID(prefix, id string, generate func() string) string {
	id = strings.TrimSpace(id)
	if strings.HasPrefix(id, prefix) {
		return id
	}
	if id == "" {
		id = generate()
	}
	return prefix + id
}

// SettlementKey returns the stable financial-operation identity, including WS-turn and video-task overrides.
func (in *Input) SettlementKey() string {
	key := in.BillingKey
	if key == "" {
		key = in.RequestID
	}
	if in.Result == nil {
		return key
	}
	result := in.Result
	if result.OpenAIWSMode && strings.TrimSpace(result.RequestID) != "" {
		key = strings.TrimSpace(result.RequestID)
	}
	if result.VideoCount > 0 {
		if stable := stableVideoRequestID(firstNonEmpty(strings.TrimPrefix(strings.TrimSpace(result.RequestID), "grok-video:"), strings.TrimSpace(result.ResponseID), strings.TrimPrefix(strings.TrimSpace(key), "grok-video:"))); stable != "" {
			key = stable
		}
	}
	return key
}
