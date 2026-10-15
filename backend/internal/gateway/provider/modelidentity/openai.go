package modelidentity

import "strings"

// openai.go selects the billing model identity used for completion usage.
// Settlement needs one billing identity even when a forward result carries
// several model names. The selected billing model wins when present; otherwise
// the first nonempty alternate supplies the identity, with versions, prefixes,
// and effort suffixes retained.

// UsageCandidates returns one trimmed billing model, preferring primary over
// the ordered alternates. Blank names are skipped, and an entirely blank input
// returns nil so settlement can handle missing model identity.
func UsageCandidates(primary string, alternates ...string) []string {
	if strings.TrimSpace(primary) != "" {
		return []string{strings.TrimSpace(primary)}
	}
	// Forward results can omit the billing model. Use the first populated alternate
	// so later names cannot redirect settlement away from the caller's chosen order.
	for _, model := range alternates {
		if strings.TrimSpace(model) != "" {
			return []string{strings.TrimSpace(model)}
		}
	}
	return nil
}
