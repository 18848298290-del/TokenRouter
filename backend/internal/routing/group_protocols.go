package routing

import (
	"slices"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// LegacyGroupProtocolPatch represents supplied legacy switches; nil fields retain the current selection.
type LegacyGroupProtocolPatch struct {
	Messages, Image, Batch, Live *bool
}

// EffectiveAllowedProtocols returns an independent protocol slice for responses and edit snapshots.
// A nil group or selection produces a non-nil empty slice.
func (g *Group) EffectiveAllowedProtocols() []protocol.ProtocolID {
	if g == nil {
		return []protocol.ProtocolID{}
	}
	return append([]protocol.ProtocolID{}, g.AllowedProtocols...)
}

// AllowsClientProtocol reports whether the group admits the requested protocol.
func (g *Group) AllowsClientProtocol(protocol protocol.ProtocolID) bool {
	if g == nil {
		return false
	}
	return slices.Contains(g.AllowedProtocols, protocol)
}

// NormalizeGroupProtocolPolicy validates the original set before applying compatibility input and deriving
// legacy fields.
// @project-doc docs/interfaces/protocol_capabilities.md#group_protocol_routes
func NormalizeGroupProtocolPolicy(group *Group, legacy *LegacyGroupProtocolPatch) error {
	normalized, err := capability.ValidateGroupClientProtocols("", group.AllowedProtocols)
	if err != nil {
		return infraerrors.BadRequest("INVALID_ALLOWED_CLIENT_PROTOCOLS", err.Error())
	}
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
	// Existing consumers read these derived fields; AllowedProtocols is the authoritative selection.
	group.AllowMessagesDispatch = slices.Contains(group.AllowedProtocols, protocol.ProtocolAnthropicMessages)
	group.AllowImageGeneration = slices.Contains(group.AllowedProtocols, protocol.ProtocolImagesGenerations) || slices.Contains(group.AllowedProtocols, protocol.ProtocolImagesEdits) || slices.Contains(group.AllowedProtocols, protocol.ProtocolImageBatches) || slices.Contains(group.AllowedProtocols, protocol.ProtocolGeminiGenerateContent)
	group.AllowBatchImageGeneration = slices.Contains(group.AllowedProtocols, protocol.ProtocolImageBatches)
	group.AllowLive = slices.Contains(group.AllowedProtocols, protocol.ProtocolLive)
	return nil
}

// ApplyLegacyGroupProtocolPatch updates a previously validated set. Validate first so conversion cannot hide
// invalid input.
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
	if patch.Image != nil && !*patch.Image {
		set(protocol.ProtocolImageBatches, patch.Image)
	}
	set(protocol.ProtocolImageBatches, patch.Batch)
	set(protocol.ProtocolLive, patch.Live)
}
