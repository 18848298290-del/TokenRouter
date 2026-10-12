package clientmeta

import (
	"strings"

	"github.com/tidwall/gjson"

	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// Codex review markers are request claims; entrypoints perform authorization and provider selection.
const (
	CodexAutoReviewModel      = "codex-auto-review"
	OpenAISubagentHeader      = "x-openai-subagent"
	CodexParentThreadIDHeader = "x-codex-parent-thread-id"
	CodexTurnMetadataHeader   = "x-codex-turn-metadata"
)

type CodexReviewInput struct {
	Model, Subagent, ParentThreadID, TurnMetadata string
	Body                                          []byte
}

// CodexReviewParent returns a parent thread ID when model and review claims agree without conflicting IDs.
func CodexReviewParent(input CodexReviewInput) string {
	if !IsCodexReviewModel(input.Model) {
		return ""
	}
	headerMetadata := input.TurnMetadata
	bodyMetadata := protocolopenai.RequestPayloadView(input.Body).Get("client_metadata.x-codex-turn-metadata").String()
	if !hasUnambiguousOpenAICodexReviewSubagent(
		input.Subagent,
		codexSubagentKindFromMetadata(headerMetadata),
		codexSubagentKindFromMetadata(bodyMetadata),
	) {
		return ""
	}

	parentID := ""
	for _, candidate := range []string{
		strings.TrimSpace(input.ParentThreadID),
		codexParentThreadIDFromMetadata(headerMetadata),
		codexParentThreadIDFromMetadata(bodyMetadata),
	} {
		if candidate == "" {
			continue
		}
		if parentID != "" && parentID != candidate {
			return ""
		}
		parentID = candidate
	}
	if parentID == "" {
		return ""
	}

	return parentID
}

func codexParentThreadIDFromMetadata(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || !gjson.Valid(raw) {
		return ""
	}
	return strings.TrimSpace(gjson.Get(raw, "parent_thread_id").String())
}

func codexSubagentKindFromMetadata(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || !gjson.Valid(raw) {
		return ""
	}
	return strings.TrimSpace(gjson.Get(raw, "subagent_kind").String())
}

func hasUnambiguousOpenAICodexReviewSubagent(candidates ...string) bool {
	subagent := ""
	for _, candidate := range candidates {
		candidate = strings.ToLower(strings.TrimSpace(candidate))
		if candidate == "" {
			continue
		}
		if subagent != "" && subagent != candidate {
			return false
		}
		subagent = candidate
	}
	return subagent == "guardian" || subagent == "review"
}

// IsCodexReviewModel reports whether the model uses Codex review markers.
func IsCodexReviewModel(model string) bool {
	return strings.EqualFold(strings.TrimSpace(model), CodexAutoReviewModel)
}
