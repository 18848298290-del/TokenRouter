package modelidentity

import "strings"

// UsageCandidates 只使用已选定的计费模型；仅缺少模型元数据时补取首个明确值。
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
