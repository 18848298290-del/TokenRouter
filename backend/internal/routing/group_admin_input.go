package routing

import (
	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
)

// group_admin_input.go defines create and update inputs for group settings and
// provider copying.
// New groups default to shared text admission when AllowedProtocols is omitted.
// Updates keep omitted values and use pointer presence or non-nil collections
// for selected clears, including nonpositive fallback IDs and empty provider
// preference maps. GroupAdmin validates those inputs before saving settings
// alongside requested membership changes.

// CreateGroupInput contains new group settings and optional sources for
// provider copying. Accepted FallbackGroupID, FallbackGroupIDOnInvalidRequest,
// and UnavailableFallbackGroupID pointers stay attached to the returned creation
// working Group. Keep their positive ID values stable while input and result
// are both in use.
type CreateGroupInput struct {
	// Localization supplies a versioned copy edit. Nil derives initial source
	// content from Name and Description, which remain the group's administrative
	// fields.
	Localization *locale.Update[GroupCopy] // versioned user-facing group copy
	// RoutingPolicy is validated and cloned into the creation working value. Its
	// model permissions remain independent of any shared pricing configuration.
	RoutingPolicy GroupRoutingPolicy // group model and feature rules
	Name          string             // administrative group name
	Description   string             // administrative group description
	// SchedulerType selects basic when blank. Nonblank input is trimmed,
	// case-normalized, and accepted only as basic or advanced during creation.
	SchedulerType string // provider selection algorithm name
	// AdvancedSchedulerOverrides supplies sparse settings that inherit absent
	// values. Supplied overrides are validated and copied before the group is
	// persisted.
	AdvancedSchedulerOverrides GroupAdvancedSchedulerOverrides // sparse advanced scheduler values
	DisplayBrand               string                          // marketplace display brand label
	// SortOrder selects an explicit display position when present. Nil requests
	// automatic tail allocation within the creation mutation context.
	SortOrder *int // administrative display sort position
	// RateMultiplier is supplied as the group's base price multiplier. CreateGroup
	// rejects values that compare less than or equal to zero.
	RateMultiplier          float64 // base user price multiplier
	IsExclusive             bool    // exclusive group access flag
	SessionIsolationEnabled bool    // cross-group session isolation flag

	// AllowImageGeneration and AllowBatchImageGeneration are compatibility inputs
	// used when the full protocol set is omitted or LegacyProtocolInput is true.
	// Batch is initially disabled when image generation is false.
	AllowImageGeneration      bool // legacy image admission input
	AllowBatchImageGeneration bool // legacy batch image admission input

	ClaudeCodeOnly bool // Claude Code client restriction
	// FallbackGroupID selects the client-restriction target when present. Creation
	// checks its reachable chain and requires the immediate target to accept other
	// clients.
	FallbackGroupID *int64 // client-restriction fallback group ID
	// FallbackGroupIDOnInvalidRequest selects an existing target with no
	// invalid-request fallback of its own. Nonpositive supplied IDs clear this
	// optional target before validation.
	FallbackGroupIDOnInvalidRequest *int64 // invalid-request fallback group ID
	// UnavailableFallbackGroupID selects an existing active target. Nonpositive
	// supplied IDs clear the target, and request-time fallback performs the
	// caller's permission checks.
	UnavailableFallbackGroupID *int64 // unavailable-request fallback group ID
	// ModelRouting is attached directly to the creation working value. Its map and
	// provider-ID slices remain shared with the returned Group after creation, so
	// callers coordinate edits while retaining both values.
	ModelRouting map[string][]int64 // model-pattern provider preference lists
	// ModelRoutingEnabled remains part of the creation input schema. CreateGroup
	// leaves the new Group switch false; UpdateGroup applies its separate optional
	// enable field.
	ModelRoutingEnabled bool // requested provider-preference enable flag
	// MCPXMLInject defaults to true when omitted during creation. A supplied false
	// sets the stored compatibility field, while execution adapters determine
	// whether it affects their payload.
	MCPXMLInject *bool // stored MCP XML compatibility flag
	// SupportedModelScopes retains stored compatibility input for model families.
	// The returned creation working value shares this slice, so edits after the
	// call can affect both the input and returned Group.
	SupportedModelScopes []string // stored model-family compatibility list
	// LegacyProtocolInput permits legacy media switches to patch a supplied text
	// selection. The original protocol set is validated before those switches are
	// applied.
	LegacyProtocolInput bool // legacy text-selection compatibility marker
	// AllowedProtocols starts with shared text defaults when nil and otherwise the
	// supplied selection, including an empty set. Applicable legacy switches can
	// then patch that validated set.
	AllowedProtocols []protocol.ProtocolID // authoritative client protocol selection
	// ProtocolFallbacks supplies ordered conversion targets per client entry. Nil
	// selects shared defaults, an empty map selects automatic behavior, and an
	// empty target list selects native-only handling. Supplied maps and target
	// slices remain shared with the returned creation working Group, so callers
	// coordinate edits while retaining both values.
	ProtocolFallbacks map[protocol.ProtocolID][]protocol.ProtocolID // ordered protocol conversion targets
	// ResponsesImagePolicy is normalized after admission input is validated. Blank
	// input selects the capability package's default image policy.
	ResponsesImagePolicy string // Responses image handling policy
	// AllowMessagesDispatch retains legacy request input. Creation derives the
	// stored flag from final protocol admission, including the default text
	// selection when AllowedProtocols is omitted.
	AllowMessagesDispatch bool // legacy Messages admission input
	// AllowLive patches the Live entry for legacy protocol input. A complete
	// supplied selection determines the saved mirror when compatibility patching
	// is not requested.
	AllowLive bool // legacy Live admission input
	// ForceOpenAIFast selects priority when the named policy is omitted or blank.
	// A supplied valid named policy takes precedence over this compatibility
	// input.
	ForceOpenAIFast bool // legacy priority forcing input
	// OpenAIFastPolicy selects the named acceleration rule. Nil or blank uses
	// ForceOpenAIFast, while an invalid nonblank policy returns a bad-request
	// error.
	OpenAIFastPolicy *string // named OpenAI acceleration policy

	DefaultMappedModel string // stored default mapped model
	// RequireOAuthOnly removes API-key records from provider IDs copied during
	// creation. Request-time provider admission applies the group's authentication
	// restrictions separately.
	RequireOAuthOnly  bool // OAuth-only provider eligibility flag
	RequirePrivacySet bool // verified provider privacy requirement
	// ModelsListConfig is normalized into separate slice storage before saving.
	// Blank names and exact duplicates are removed while the selected order is
	// retained.
	ModelsListConfig GroupModelsListConfig // ordered custom request-model list
	// AvailabilityProbeConfig is normalized before persistence. Disabled input
	// clears all probe fields, while invalid enabled settings return the
	// administrative bad-request reason.
	AvailabilityProbeConfig GroupAvailabilityProbeConfig // active availability probe settings
	// RPMLimit supplies the default requests-per-minute allowance per user in this
	// group. Runtime admission disables this group counter for nonpositive values
	// and prefers a configured user-group override.
	RPMLimit int // group requests per minute
	// MaxReasoningEffort is normalized using the shared effort vocabulary during
	// creation. A blank value leaves the group without an effort cap.
	MaxReasoningEffort string // maximum requested reasoning effort
	// MaxReasoningEffortOverLimit controls excessive requested effort. Blank input
	// selects downgrade, while a supported deny value enables rejection instead of
	// rewriting.
	MaxReasoningEffortOverLimit string // reasoning-cap excess handling action
	// ReasoningEffortMappings is normalized and validated as a complete
	// collection. Duplicate sources and invalid effort values reject the
	// administrative operation before persistence.
	ReasoningEffortMappings []ReasoningEffortMapping // ordered model-scoped effort rewrites
	// CopyProvidersFromGroupIDs names source groups checked before creation
	// begins. Their distinct provider IDs are bound to the new group inside the
	// mutation transaction.
	CopyProvidersFromGroupIDs []int64 // source groups for provider copying
}

// UpdateGroupInput contains optional saved-setting changes and provider
// membership replacement sources. Accepted positive FallbackGroupID,
// FallbackGroupIDOnInvalidRequest, and UnavailableFallbackGroupID pointers stay
// attached to the returned working Group. Keep their ID values stable while
// input and result are both in use.
type UpdateGroupInput struct {
	// Localization supplies a versioned edit based on the group's current content.
	// Nil retains saved copy, and revision validation can reject a stale edit.
	Localization *locale.Update[GroupCopy] // versioned user-facing group copy
	// RoutingPolicy replaces the saved rules when supplied. Nil retains them, and
	// the new value is validated and cloned before persistence.
	RoutingPolicy *GroupRoutingPolicy // group model and feature rules
	// Name changes the administrative name when nonempty. An empty string retains
	// the existing value; localized display copy is updated through Localization.
	Name string // administrative group name
	// Description retains the saved description when nil. A non-nil pointer to an
	// empty string clears it during the update.
	Description *string // administrative group description
	// SchedulerType retains the current algorithm when nil. A supplied blank value
	// selects basic after normalization; unsupported nonblank values return a
	// bad-request error.
	SchedulerType *string // provider selection algorithm name
	// AdvancedSchedulerOverrides retains sparse values when nil. A supplied empty
	// object clears the overrides so advanced scheduling resumes inheritance from
	// shared settings.
	AdvancedSchedulerOverrides *GroupAdvancedSchedulerOverrides // sparse advanced scheduler values
	// DisplayBrand retains the current brand when nil. Supplied strings are
	// trimmed, and an empty result lets marketplace display use the administrative
	// group name.
	DisplayBrand *string // marketplace display brand label
	// SortOrder retains the saved display position when nil. A supplied integer
	// updates that position directly rather than invoking automatic tail
	// allocation.
	SortOrder *int // administrative display sort position
	// RateMultiplier retains the current value when nil. A supplied value that
	// compares less than or equal to zero is rejected before persistence.
	RateMultiplier *float64 // base user price multiplier
	// IsExclusive retains the saved access mode when nil. An explicit false makes
	// the group eligible for ordinary public visibility checks.
	IsExclusive *bool // exclusive group access flag
	// SessionIsolationEnabled retains the saved isolation setting when nil.
	// Explicit true or false replaces the group value used by runtime session
	// checks.
	SessionIsolationEnabled *bool // cross-group session isolation flag
	// Status retains the saved status when empty. A nonempty string is assigned as
	// supplied, leaving status admission behavior to the group's runtime
	// consumers.
	Status string // saved group activity status

	// AllowImageGeneration and AllowBatchImageGeneration retain omitted values.
	// Supplied compatibility flags patch protocol admission when the complete
	// selection is absent or LegacyProtocolInput is true; the saved booleans are
	// then derived from the final set.
	AllowImageGeneration      *bool // legacy image admission input
	AllowBatchImageGeneration *bool // legacy batch image admission input

	// ClaudeCodeOnly retains the current client restriction when nil. A supplied
	// boolean replaces the stored value used by client-group resolution.
	ClaudeCodeOnly *bool // Claude Code client restriction
	// FallbackGroupID retains the client-restriction target when nil. Positive
	// supplied IDs are validated, and nonpositive supplied IDs clear the target.
	FallbackGroupID *int64 // client-restriction fallback group ID
	// FallbackGroupIDOnInvalidRequest retains the current target when nil and
	// clears it for nonpositive input. Any remaining target is revalidated during
	// the update.
	FallbackGroupIDOnInvalidRequest *int64 // invalid-request fallback group ID
	// UnavailableFallbackGroupID retains the saved target when nil and clears it
	// for nonpositive input. Any remaining target must still exist and be active.
	UnavailableFallbackGroupID *int64 // unavailable-request fallback group ID
	// ModelRouting retains saved preferences when nil. A non-nil map replaces them
	// on the working group and remains shared with the returned Group, so callers
	// coordinate later edits of its nested provider-ID slices.
	ModelRouting map[string][]int64 // model-pattern provider preference lists
	// ModelRoutingEnabled retains the saved switch when nil. A supplied false
	// disables provider preferences while leaving the stored model-pattern map
	// available for later use.
	ModelRoutingEnabled *bool // requested provider-preference enable flag
	// MCPXMLInject retains the stored compatibility value when nil. A supplied
	// boolean changes that field independently of runtime adapters that determine
	// actual XML injection.
	MCPXMLInject *bool // stored MCP XML compatibility flag
	// SupportedModelScopes retains saved family input when nil. A supplied slice
	// replaces it, including an empty slice that clears the compatibility list,
	// and its backing storage stays shared with the returned Group.
	SupportedModelScopes *[]string // stored model-family compatibility list
	// LegacyProtocolInput marks a supplied text-only AllowedProtocols selection.
	// Set it with a non-nil replacement so existing nontext entries can be carried
	// forward before compatibility switches are applied.
	LegacyProtocolInput bool // legacy text-selection compatibility marker
	// AllowedProtocols retains the effective saved set when nil. A supplied slice
	// replaces admission, including an empty slice, before optional legacy text
	// preservation and switch patching.
	AllowedProtocols *[]protocol.ProtocolID // authoritative client protocol selection
	// ProtocolFallbacks retains saved conversion choices when nil. A non-nil empty
	// map clears explicit choices, while per-entry empty lists continue to require
	// native handling. Supplied maps and target slices remain shared with the
	// returned working Group, so callers coordinate edits while retaining both
	// values.
	ProtocolFallbacks map[protocol.ProtocolID][]protocol.ProtocolID // ordered protocol conversion targets
	// ResponsesImagePolicy retains the current policy when empty. A nonempty value
	// is validated and normalized before the group update is saved.
	ResponsesImagePolicy string // Responses image handling policy
	// AllowMessagesDispatch patches Messages admission when the complete selection
	// is omitted. Nil retains it, and a supplied authoritative protocol list takes
	// precedence over this compatibility switch.
	AllowMessagesDispatch *bool // legacy Messages admission input
	// AllowLive is compatibility input for updates without a complete
	// authoritative selection. Nil retains its entry, and a supplied boolean
	// patches Live before derived fields are rebuilt.
	AllowLive *bool // legacy Live admission input
	// ForceOpenAIFast retains acceleration when neither acceleration input is
	// supplied. A supplied false with no named policy resolves to follow_request,
	// and named input takes precedence.
	ForceOpenAIFast *bool // legacy priority forcing input
	// OpenAIFastPolicy retains the current rule when both acceleration fields are
	// omitted. A supplied valid name wins over ForceOpenAIFast; blank named input
	// uses the accompanying legacy value.
	OpenAIFastPolicy *string // named OpenAI acceleration policy

	// DefaultMappedModel retains the saved default name when nil. A supplied empty
	// string clears it independently of protocol admission normalization.
	DefaultMappedModel *string // stored default mapped model
	// RequireOAuthOnly retains the current requirement when nil. A supplied value
	// also controls filtering of API-key IDs when replacement provider sources are
	// requested.
	RequireOAuthOnly *bool // OAuth-only provider eligibility flag
	// RequirePrivacySet retains the saved provider-privacy requirement when nil.
	// Explicit true or false updates the group field used by runtime eligibility
	// checks.
	RequirePrivacySet *bool // verified provider privacy requirement
	// ModelsListConfig retains the saved custom selection when nil. Supplied
	// values replace the normalized enabled flag and ordered deduplicated names.
	ModelsListConfig *GroupModelsListConfig // ordered custom request-model list
	// AvailabilityProbeConfig retains current probe settings when nil. A supplied
	// disabled value clears them, while invalid enabled settings reject the update
	// before persistence.
	AvailabilityProbeConfig *GroupAvailabilityProbeConfig // active availability probe settings
	// RPMLimit retains the per-user group allowance when nil. A supplied
	// nonpositive value disables that default counter; user-group overrides and
	// total-user RPM admission remain separate checks.
	RPMLimit *int // group requests per minute
	// MaxReasoningEffort retains the saved cap when nil. A supplied empty string
	// clears it, and nonempty values use shared effort normalization.
	MaxReasoningEffort *string // maximum requested reasoning effort
	// MaxReasoningEffortOverLimit retains the saved action when nil. A supplied
	// blank string resolves to downgrade during reasoning-policy normalization.
	MaxReasoningEffortOverLimit *string // reasoning-cap excess handling action
	// ReasoningEffortMappings retains the saved collection when nil. A supplied
	// empty list clears it, and a nonempty list replaces it after validation.
	ReasoningEffortMappings *[]ReasoningEffortMapping // ordered model-scoped effort rewrites
	// CopyProvidersFromGroupIDs leaves memberships unchanged when empty. Nonempty
	// sources replace all current bindings inside the mutation transaction, even
	// when no eligible provider IDs remain.
	CopyProvidersFromGroupIDs []int64 // source groups for provider copying
}
