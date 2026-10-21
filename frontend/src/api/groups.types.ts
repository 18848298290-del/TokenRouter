import type { ModelAttributes } from '@/api/modelAttributes.types'
import type { ProtocolID } from '@/api/protocols.types'
import type { MarketplaceGroupCapacity } from '@/api/marketplace.types'
import type { LocalizedUpdate } from '@/i18n/content'

// groups.types.ts defines group admission, model routing, and provider scheduling
// settings. Public group records describe requestable capabilities, while administrative
// forms also edit routing maps and sparse advanced-scheduler overrides whose omitted
// fields inherit the shared gateway configuration.

// GroupSchedulerType selects basic or advanced provider scheduling for a group.
export type GroupSchedulerType = 'basic' | 'advanced'

// GroupAdvancedSchedulerOverrides holds sparse scheduling overrides; omitted fields
// inherit shared gateway settings.
export interface GroupAdvancedSchedulerOverrides {
  sticky_weighted_enabled?: boolean // Weighted sticky scheduling activation.
  subscription_priority_enabled?: boolean // Subscription priority scheduling switch.
  ewma_error_rate_alpha?: number // Error-rate EWMA smoothing factor.
  ewma_ttft_alpha?: number // First-token EWMA smoothing factor.
  sticky_escape_enabled?: boolean // Sticky-session escape switch.
  sticky_escape_ttft_ms?: number // Sticky escape latency milliseconds.
  sticky_escape_error_rate?: number // Sticky escape error-rate threshold.
  lb_top_k?: number // Load-balancing candidate count.
  weight_priority?: number // Scheduler priority score weight.
  weight_load?: number // Scheduler load score weight.
  weight_queue?: number // Scheduler queue score weight.
  weight_error_rate?: number // Scheduler error-rate score weight.
  weight_ttft?: number // Scheduler latency score weight.
  weight_reset?: number // Scheduler reset score weight.
  weight_quota_headroom?: number // Scheduler remaining-quota score weight.
  weight_previous_response?: number // Previous-response affinity score weight.
  weight_session_sticky?: number // Session affinity score weight.
}

// GroupAvailabilityProbeConfig configures recurring requests used to measure group
// availability.
export interface GroupAvailabilityProbeConfig {
  enabled: boolean // Feature activation switch.
  interval_minutes?: number // Minutes between scheduled checks.
  model_id?: string // Selected model identifier.
  prompt?: string // Model test prompt.
  timeout_seconds?: number // Probe timeout seconds.
  // Maximum retries after the first failed probe; the server supplies its default when
  // omitted.
  max_retries?: number // Additional probe attempt ceiling.
  user_agent?: string // HTTP User-Agent value.
}

// ReasoningEffortMatchType selects how a model name matches an effort mapping.
export type ReasoningEffortMatchType = 'exact' | 'prefix' | 'suffix'

// ReasoningEffortMapping translates a requested reasoning effort for matching model
// requests.
export interface ReasoningEffortMapping {
  from: string // Requested reasoning effort.
  to: string // Mapped reasoning effort.
  match_type?: ReasoningEffortMatchType // Model-name matching rule.
  model?: string // Requested model identifier.
}

// Group describes user-visible routing policy and request admission for one group.
export interface Group {
  search_terms?: string[] // Additional searchable text.
  display_name?: string // Human-readable display name.
  // Requestable models resolved by the backend from the capabilities of providers in this
  // group.
  models?: string[] // Requestable model identifiers.
  model_attributes?: Record<string, ModelAttributes> // Capabilities by model identifier.
  model_protocols?: Record<string, ProtocolID[]> // Protocols by model identifier.
  id: number // Record numeric identifier.
  name: string // Record display name.
  description: string | null // Human-readable record description.
  display_brand?: string // Displayed provider brand.
  rate_multiplier: number // Applied billing multiplier.
  capacity?: MarketplaceGroupCapacity // Aggregated group load.
  // This default controls requests per user within the group, subject to user-group
  // overrides. The effective group gate runs before the independent user RPM gate;
  // an effective zero disables only the group gate.
  rpm_limit?: number // Group request rate ceiling.
  // An empty ceiling allows any requested OpenAI/Codex reasoning effort; other values
  // bound the permitted reasoning level.
  max_reasoning_effort?: string // Maximum reasoning effort.
  // When requested effort exceeds the ceiling, downgrade selects the permitted level and
  // deny rejects the request instead.
  max_reasoning_effort_over_limit?: string // Excess effort handling policy.
  reasoning_effort_mappings?: ReasoningEffortMapping[] // Configured reasoning translations.
  is_exclusive: boolean // Exclusive group access.
  session_isolation_enabled: boolean // Group session isolation.
  status: 'active' | 'inactive' // Current lifecycle state.

  // Image generation permissions.
  allow_image_generation: boolean // Image generation permission.
  allow_batch_image_generation: boolean // Batch image permission.

  // Claude Code client restrictions.
  claude_code_only: boolean // Claude Code admission restriction.
  fallback_group_id: number | null // Client restriction fallback group.
  fallback_group_id_on_invalid_request: number | null // Invalid-request fallback group identifier.
  unavailable_fallback_group_id: number | null // Unavailable-group fallback identifier.
  // Each protocol_fallbacks entry contains ordered alternatives for its client protocol.
  // Consumers retain the returned order when displaying or editing the group's conversion
  // preferences.
  protocol_fallbacks?: Partial<Record<ProtocolID, ProtocolID[]>> // Ordered protocol fallback choices.
  responses_image_policy?: 'inherit' | 'enabled' | 'disabled' | 'block' // Responses image admission policy.
  allowed_protocols: ProtocolID[] // Admitted client protocol identifiers.
  // Deprecated OpenAI Messages compatibility switch; consumers use allowed_protocols when
  // deciding which protocols are enabled.
  allow_messages_dispatch?: boolean // Messages dispatch compatibility switch.
  // OpenAI Live request switch.
  allow_live: boolean // Live API admission switch.
  default_mapped_model?: string // Default mapped model identifier.
  availability_probe_config?: GroupAvailabilityProbeConfig // Group availability probe settings.
  require_oauth_only: boolean // OAuth credential admission requirement.
  require_privacy_set: boolean // Provider privacy admission requirement.
  created_at: string // Record creation timestamp.
  updated_at: string // Record update timestamp.
}

// GroupOpenAIFastPolicy selects group acceleration behavior and takes precedence over the
// compatibility boolean switch.
export type GroupOpenAIFastPolicy =
  'follow_request' | 'force_priority' | 'force_ultrafast' | 'force_off'

// GroupRoutingPolicy controls model mapping, restrictions, and feature settings within a
// group.
export interface GroupRoutingPolicy {
  // Compatibility switch for formerly disabled policies; the administrator form submits
  // true when saving routing policies.
  enabled: boolean // Feature activation switch.
  model_mapping: Record<string, string> // Requested-to-upstream model mappings.
  restrict_models: boolean // Model restriction switch.
  restriction_model_source: 'requested' | 'group_mapped' | 'upstream' // Model identity used for restrictions.
  allowed_models: string[] // Permitted model name patterns.
  features: string // Serialized feature configuration.
  features_config: Record<string, unknown> // Structured feature settings.
}

// AdminGroup extends public group information with administrator routing and scheduling
// configuration.
export interface AdminGroup extends Group {
  localization?: LocalizedUpdate<{
    display_name: string // Human-readable display name.
    description: string // Human-readable record description.
  }> // Localized content versions.
  routing_policy: GroupRoutingPolicy // Group model routing policy.
  // Configured in administrator forms and returned by administrative group endpoints for
  // policy editing.
  force_openai_fast?: boolean // Fast-mode compatibility switch.
  openai_fast_policy?: GroupOpenAIFastPolicy // Group acceleration policy.

  // Scheduler mode configured by administrators and returned in administrative group
  // responses for editing.
  scheduler_type: GroupSchedulerType // Provider scheduling mode.
  advanced_scheduler_overrides?: GroupAdvancedSchedulerOverrides // Sparse advanced scheduler overrides.

  // Administrator-visible model routing configuration.
  model_routing: Record<string, number[]> | null // Provider assignments by model.
  model_routing_enabled: boolean // Model-specific routing switch.

  // MCP XML injection for Antigravity.
  mcp_xml_inject: boolean // Antigravity MCP XML injection.

  // Model families supported by Antigravity.
  supported_model_scopes?: string[] // Supported Antigravity model families.

  // Provider counts returned to administrators.
  provider_count?: number // Attached provider count.
  active_provider_count?: number // Active provider count.
  rate_limited_provider_count?: number // Rate-limited provider count.

  // OpenAI platform Messages scheduling configuration.
  default_mapped_model?: string // Default mapped model identifier.
  models_list_config?: ModelsListConfig // Explicit model list configuration.

  // Group display sorting order.
  sort_order: number // Display sorting position.
}

// ModelsListConfig controls whether an administrator-supplied model list is enabled.
export interface ModelsListConfig {
  enabled: boolean // Feature activation switch.
  models: string[] // Administrator-supplied model identifiers.
}

// CreateGroupRequest supplies routing, capability, and scheduling settings for a new
// group.
export interface CreateGroupRequest {
  localization?: LocalizedUpdate<{
    display_name: string // Human-readable display name.
    description: string // Human-readable record description.
  }> // Localized content versions.
  name: string // Record display name.
  description?: string | null // Human-readable record description.
  scheduler_type?: GroupSchedulerType // Provider scheduling mode.
  advanced_scheduler_overrides?: GroupAdvancedSchedulerOverrides // Sparse advanced scheduler overrides.
  display_brand?: string // Displayed provider brand.
  sort_order?: number // Display sorting position.
  rate_multiplier?: number // Applied billing multiplier.
  is_exclusive?: boolean // Exclusive group access.
  session_isolation_enabled?: boolean // Group session isolation.

  force_openai_fast?: boolean // Fast-mode compatibility switch.
  openai_fast_policy?: GroupOpenAIFastPolicy // Group acceleration policy.

  routing_policy?: GroupRoutingPolicy // Group model routing policy.

  allow_image_generation?: boolean // Image generation permission.
  allow_batch_image_generation?: boolean // Batch image permission.

  claude_code_only?: boolean // Claude Code admission restriction.
  fallback_group_id?: number | null // Client restriction fallback group.
  fallback_group_id_on_invalid_request?: number | null // Invalid-request fallback group identifier.
  unavailable_fallback_group_id?: number | null // Unavailable-group fallback identifier.
  mcp_xml_inject?: boolean // Antigravity MCP XML injection.
  supported_model_scopes?: string[] // Supported model family identifiers.
  models_list_config?: ModelsListConfig // Explicit model list configuration.
  availability_probe_config?: GroupAvailabilityProbeConfig // Group availability probe settings.
  protocol_fallbacks?: Partial<Record<ProtocolID, ProtocolID[]>> // Ordered protocol fallback choices.
  responses_image_policy?: 'inherit' | 'enabled' | 'disabled' | 'block' // Responses image admission policy.
  allowed_protocols?: ProtocolID[] // Admitted client protocol identifiers.
  allow_messages_dispatch?: boolean // Messages dispatch compatibility switch.
  allow_live?: boolean // Live API admission switch.
  default_mapped_model?: string // Default mapped model identifier.
  model_routing?: Record<string, number[]> | null // Provider assignments by model.
  model_routing_enabled?: boolean // Model-specific routing switch.
  rpm_limit?: number // Requests per minute ceiling.
  max_reasoning_effort?: string // Maximum reasoning effort.
  max_reasoning_effort_over_limit?: string // Excess reasoning handling.
  reasoning_effort_mappings?: ReasoningEffortMapping[] // Configured reasoning translations.
  require_oauth_only?: boolean // OAuth credential admission requirement.
  require_privacy_set?: boolean // Provider privacy admission requirement.
  // Groups whose providers are copied.
  copy_providers_from_group_ids?: number[] // Provider source group identifiers.
}

// UpdateGroupRequest supplies optional changes to an existing group's admission and
// routing settings.
export interface UpdateGroupRequest {
  localization?: LocalizedUpdate<{
    display_name: string // Human-readable display name.
    description: string // Human-readable record description.
  }> // Localized content versions.
  name?: string // Record display name.
  description?: string | null // Human-readable record description.
  scheduler_type?: GroupSchedulerType // Provider scheduling mode.
  advanced_scheduler_overrides?: GroupAdvancedSchedulerOverrides // Sparse advanced scheduler overrides.
  display_brand?: string // Displayed provider brand.
  sort_order?: number // Display sorting position.
  rate_multiplier?: number // Applied billing multiplier.
  is_exclusive?: boolean // Exclusive group access.
  session_isolation_enabled?: boolean // Group session isolation.
  status?: 'active' | 'inactive' // Current lifecycle state.

  force_openai_fast?: boolean // Fast-mode compatibility switch.
  openai_fast_policy?: GroupOpenAIFastPolicy // Group acceleration policy.

  routing_policy?: GroupRoutingPolicy // Group model routing policy.

  allow_image_generation?: boolean // Image generation permission.
  allow_batch_image_generation?: boolean // Batch image permission.

  claude_code_only?: boolean // Claude Code admission restriction.
  fallback_group_id?: number | null // Client restriction fallback group.
  fallback_group_id_on_invalid_request?: number | null // Invalid-request fallback group identifier.
  unavailable_fallback_group_id?: number | null // Unavailable-group fallback identifier.
  mcp_xml_inject?: boolean // Antigravity MCP XML injection.
  supported_model_scopes?: string[] // Supported model family identifiers.
  models_list_config?: ModelsListConfig // Explicit model list configuration.
  availability_probe_config?: GroupAvailabilityProbeConfig // Group availability probe settings.
  protocol_fallbacks?: Partial<Record<ProtocolID, ProtocolID[]>> // Ordered protocol fallback choices.
  responses_image_policy?: 'inherit' | 'enabled' | 'disabled' | 'block' // Responses image admission policy.
  allowed_protocols?: ProtocolID[] // Admitted client protocol identifiers.
  allow_messages_dispatch?: boolean // Messages dispatch compatibility switch.
  allow_live?: boolean // Live API admission switch.
  default_mapped_model?: string // Default mapped model identifier.
  model_routing?: Record<string, number[]> | null // Provider assignments by model.
  model_routing_enabled?: boolean // Model-specific routing switch.
  rpm_limit?: number // Requests per minute ceiling.
  max_reasoning_effort?: string // Maximum reasoning effort.
  max_reasoning_effort_over_limit?: string // Excess reasoning handling.
  reasoning_effort_mappings?: ReasoningEffortMapping[] // Configured reasoning translations.
  require_oauth_only?: boolean // OAuth credential admission requirement.
  require_privacy_set?: boolean // Provider privacy admission requirement.
  copy_providers_from_group_ids?: number[] // Provider source group identifiers.
}
