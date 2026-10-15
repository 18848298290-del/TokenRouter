package routing

import (
	"slices"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// group_protocols.go normalizes group protocol admission and rebuilds legacy
// permission mirrors.
// A group's protocol set is the saved admission policy, and compatibility
// booleans are derived views consumed by existing execution paths. Empty
// selections close client entries; legacy switches patch a supplied selection
// only after its original values have passed validation.
//
// Validation precedes compatibility conversion because adding or removing
// legacy media entries could otherwise conceal duplicate or unknown input.
// After the patch, fallback routes and the Responses image policy are checked,
// then the legacy mirrors are rebuilt from the final protocol set.
// Normalization mutates its working group as it advances, so callers discard
// that value when a later check fails.

// LegacyGroupProtocolPatch carries optional legacy switches applied to a
// validated protocol set.
type LegacyGroupProtocolPatch struct {
	// Messages, Image, Batch, and Live distinguish omitted compatibility input
	// from an explicit false. Nil pointers retain their entries, and
	// ApplyLegacyGroupProtocolPatch defines the order when media switches
	// interact.
	// Messages toggles Messages admission; Image toggles image generation and
	// editing; Batch toggles image batches; Live toggles Live admission.
	Messages, Image, Batch, Live *bool
}

// EffectiveAllowedProtocols returns a separate slice containing the saved
// client protocol selection. Nil groups and nil selections produce a non-nil
// empty slice; callers can edit the returned slice without changing the group.
func (g *Group) EffectiveAllowedProtocols() []protocol.ProtocolID {
	if g == nil {
		return []protocol.ProtocolID{}
	}
	return append([]protocol.ProtocolID{}, g.AllowedProtocols...)
}

// AllowsClientProtocol reports whether the saved selection contains a client
// protocol. A nil group denies admission, and legacy booleans are ignored so an
// empty authoritative protocol set continues to close every entry.
func (g *Group) AllowsClientProtocol(protocol protocol.ProtocolID) bool {
	if g == nil {
		return false
	}
	return slices.Contains(g.AllowedProtocols, protocol)
}

// NormalizeGroupProtocolPolicy validates admission input and updates the
// group's protocol and legacy fields in place. Supply a non-nil working group
// and discard it on error, since earlier normalization may already be applied.
//
// The original selection is validated before a legacy patch can remove invalid
// entries. Fallbacks and image policy are then checked, and compatibility
// mirrors are rebuilt from the accepted selection so their values follow one
// admission source. A nil fallback map becomes an empty map.
// @project-doc docs/interfaces/protocol_capabilities.md#group_protocol_routes
func NormalizeGroupProtocolPolicy(group *Group, legacy *LegacyGroupProtocolPatch) error {
	normalized, err := capability.ValidateGroupClientProtocols("", group.AllowedProtocols)
	if err != nil {
		return infraerrors.BadRequest("INVALID_ALLOWED_CLIENT_PROTOCOLS", err.Error())
	}
	// Validate the submitted set before compatibility changes so removing a legacy
	// entry cannot hide an unknown protocol or a duplicate supplied ID.
	group.AllowedProtocols = normalized
	ApplyLegacyGroupProtocolPatch(group, legacy)
	if err := capability.ValidateProtocolFallbacks("", group.ProtocolFallbacks); err != nil {
		return infraerrors.BadRequest("GROUP_PROTOCOL_FALLBACK_INVALID", err.Error())
	}
	if group.ProtocolFallbacks == nil {
		group.ProtocolFallbacks = map[protocol.ProtocolID][]protocol.ProtocolID{}
	}
	policy, err := capability.NormalizeResponsesImagePolicy(group.ResponsesImagePolicy)
	if err != nil {
		return infraerrors.BadRequest("GROUP_RESPONSES_IMAGE_POLICY_INVALID", err.Error())
	}
	group.ResponsesImagePolicy = policy
	// Rebuild execution-facing compatibility fields from the accepted selection so
	// authentication snapshots and existing media consumers see the same client
	// admission decisions.
	group.AllowMessagesDispatch = slices.Contains(group.AllowedProtocols, protocol.ProtocolAnthropicMessages)
	group.AllowImageGeneration = slices.Contains(group.AllowedProtocols, protocol.ProtocolImagesGenerations) || slices.Contains(group.AllowedProtocols, protocol.ProtocolImagesEdits) || slices.Contains(group.AllowedProtocols, protocol.ProtocolImageBatches) || slices.Contains(group.AllowedProtocols, protocol.ProtocolGeminiGenerateContent)
	group.AllowBatchImageGeneration = slices.Contains(group.AllowedProtocols, protocol.ProtocolImageBatches)
	group.AllowLive = slices.Contains(group.AllowedProtocols, protocol.ProtocolLive)
	return nil
}

// ApplyLegacyGroupProtocolPatch applies supplied compatibility switches to the
// group's validated selection. Pass a non-nil group whose original protocol set
// has already passed validation; a nil patch leaves it unchanged.
//
// Messages and Live each adjust their own entry. Image controls generation and
// editing, and disabling Image also removes batches before a supplied Batch
// switch is applied. This ordering lets an explicit Batch value determine its
// final entry while protocols unrelated to these switches remain selected.
func ApplyLegacyGroupProtocolPatch(group *Group, patch *LegacyGroupProtocolPatch) {
	if patch == nil {
		return
	}
	supported := capability.SupportedGroupClientProtocols("")
	set := func(protocol protocol.ProtocolID, value *bool) {
		if value != nil && slices.Contains(supported, protocol) {
			group.AllowedProtocols = capability.SetGroupClientProtocol(group.AllowedProtocols, protocol, *value)
		}
	}
	set(protocol.ProtocolAnthropicMessages, patch.Messages)
	set(protocol.ProtocolImagesGenerations, patch.Image)
	set(protocol.ProtocolImagesEdits, patch.Image)
	// Turning off legacy image admission also closes batches, then an explicitly
	// supplied Batch value can override that inherited media decision below.
	if patch.Image != nil && !*patch.Image {
		set(protocol.ProtocolImageBatches, patch.Image)
	}
	set(protocol.ProtocolImageBatches, patch.Batch)
	set(protocol.ProtocolLive, patch.Live)
}
