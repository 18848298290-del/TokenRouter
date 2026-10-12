package clientmeta

import (
	"strings"

	"github.com/tidwall/gjson"

	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// Codex review requests can declare their parent thread in a dedicated header,
// header turn metadata, and body turn metadata. Scheduling affinity needs one
// parent identity, so conflicting nonempty declarations produce no usable
// parent. Review recognition likewise requires the nonempty subagent declarations
// to agree on guardian or review, in addition to the review model.
//
// Empty declarations do not override a supplied identity. Header and body
// metadata are parsed independently, allowing clients that provide just one
// representation. The resolved parent remains requester-supplied format evidence;
// HTTP entrypoints perform authentication and authorization before using its
// hash for affinity.

// Codex review markers identify the model and header fields consumed during
// parent-thread resolution; entrypoints separately authorize the request.
const (
	CodexAutoReviewModel      = "codex-auto-review"        // review request model
	OpenAISubagentHeader      = "x-openai-subagent"        // review agent kind
	CodexParentThreadIDHeader = "x-codex-parent-thread-id" // explicit parent-thread claim
	CodexTurnMetadataHeader   = "x-codex-turn-metadata"    // JSON turn metadata
)

// CodexReviewInput is the requester-supplied evidence for review-parent affinity resolution.
type CodexReviewInput struct {
	// Model selects review eligibility, Subagent names the agent kind,
	// ParentThreadID supplies the parent header, and TurnMetadata holds the JSON
	// metadata header. Resolution reconciles their nonempty kind and parent claims
	// with body metadata, rejecting conflicting declarations from these sources.
	Model, Subagent, ParentThreadID, TurnMetadata string
	// Body is the Responses payload consulted for client_metadata turn metadata.
	// Resolution reads this byte slice synchronously and leaves its contents unchanged.
	Body []byte
}

// CodexReviewParent returns the agreed parent thread for a review-model request
// whose nonempty subagent declarations agree on guardian or review. Empty and
// invalid metadata contributes no declaration, while unknown or conflicting
// subagent kinds reject affinity resolution.
//
// Parent IDs are trimmed and compared across the dedicated header and both
// metadata representations. Any conflicting nonempty parent produces an empty
// result, as does an absent parent. Callers use a nonempty result as scheduling
// affinity evidence after authorizing the request, rather than as authorization
// for the claimed parent itself.
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
	// A parent supplied in multiple representations must identify the same
	// thread. Reject conflicting claims so affinity cannot combine unrelated review
	// context from a header and the request body.
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

// codexParentThreadIDFromMetadata reads and trims parent_thread_id from valid
// metadata JSON. Empty or invalid JSON yields no identity, allowing another
// supported representation to provide the parent-thread claim.
func codexParentThreadIDFromMetadata(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || !gjson.Valid(raw) {
		return ""
	}
	return strings.TrimSpace(gjson.Get(raw, "parent_thread_id").String())
}

// codexSubagentKindFromMetadata reads and trims subagent_kind from valid metadata
// JSON. Empty or invalid JSON contributes no declaration; the caller reconciles
// this value with the other supplied review-kind claims.
func codexSubagentKindFromMetadata(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || !gjson.Valid(raw) {
		return ""
	}
	return strings.TrimSpace(gjson.Get(raw, "subagent_kind").String())
}

// hasUnambiguousOpenAICodexReviewSubagent accepts agreeing guardian or review
// declarations after trimming and case folding. Empty declarations are ignored;
// any unknown kind or disagreement returns false, including guardian mixed
// with review.
//
// Candidates come from independent request representations, so the first
// nonempty declaration establishes the kind that later declarations need to
// match. Requiring agreement prevents one header's review identity from hiding
// a conflicting kind in turn metadata. An entirely absent kind provides no
// review evidence and is rejected as well.
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
