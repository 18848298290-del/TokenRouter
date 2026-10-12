package modelidentity

import "strings"

// UsageCandidates returns the selected billing model, or the first nonempty alternate when it is absent.
func UsageCandidates(primary string, alternates ...string) []string {
	if strings.TrimSpace(primary) != "" {
		return []string{strings.TrimSpace(primary)}
	}
	for _, model := range alternates {
		if strings.TrimSpace(model) != "" {
			return []string{strings.TrimSpace(model)}
		}
	}
	return nil
}
