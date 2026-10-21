import type { LocalizedUpdate } from '@/i18n/content'

// modelAttributes.types.ts defines catalog capabilities and the values used by attribute
// editors. Omitted attributes represent unknown or inherited information, while explicit
// false values and empty arrays remain meaningful when consumers display capabilities
// across eligible model routes.

// ReasoningOption describes an available reasoning control and its selectable values or
// bounds.
export interface ReasoningOption {
  type: 'toggle' | 'effort' | 'budget_tokens' // Reasoning control format.
  // A null effort choice leaves the request without an explicit effort so the upstream
  // provider chooses its behavior.
  values?: (string | null)[] // Selectable reasoning control values.
  min?: number // Minimum permitted value.
  max?: number // Maximum permitted value.
}

// ParameterCondition restricts a capability to particular reasoning efforts or thinking
// modes.
export interface ParameterCondition {
  efforts?: string[] // Permitted reasoning effort values.
  thinking?: boolean // Required thinking mode.
}

// MediaCapabilities describes supported media operations, output formats, and generation
// limits.
export interface MediaCapabilities {
  video_operations?: string[] // Supported video operations.
  video_parameters?: string[] // Supported video parameters.
  image_sizes?: string[] // Supported image resolutions.
  aspect_ratios?: string[] // Supported aspect ratios.
  qualities?: string[] // Supported quality levels.
  background_options?: string[] // Supported background modes.
  output_formats?: string[] // Supported output formats.
  max_reference_images?: number // Maximum reference images.
  max_output_count?: number // Maximum output count.
  input_fidelity?: boolean // Input fidelity support.
}

// ModelAttributes describes known model capabilities, context limits, and conditional
// parameter support.
export interface ModelAttributes {
  search_terms?: string[] // Additional searchable text.
  display_name_localization?: LocalizedUpdate<string> // Display name localization.
  display_name?: string // Human-readable display name.
  context?: number // Context token capacity.
  input_limit?: number // Input token ceiling.
  output_limit?: number // Output token ceiling.
  input_modalities?: string[] // Accepted input media.
  output_modalities?: string[] // Produced output media.
  reasoning?: boolean // Reasoning capability support.
  tool_call?: boolean // Tool-calling capability support.
  structured_output?: boolean // Structured output support.
  temperature?: boolean // Temperature parameter support.
  top_p?: boolean // Top-p parameter support.
  reasoning_options?: ReasoningOption[] // Available reasoning controls.
  interleaved?:
    | true
    | {
        field: 'reasoning_content' | 'reasoning_details' // Upstream reasoning field.
      } // Interleaved reasoning representation.
  conditions?: Partial<Record<ConditionalCapability, ParameterCondition>> // Conditional capability requirements.
  media?: MediaCapabilities // Media generation capabilities.
  attachment?: boolean // Attachment input support.
  route_differences?: boolean // Differing route capabilities.
}

// attributeCapabilities lists the boolean model features rendered by capability editors
// and marketplace summaries.
export const attributeCapabilities = [
  'reasoning',
  'tool_call',
  'structured_output',
  'temperature',
  'top_p',
  'attachment',
] as const

// attributeLimits lists token capacity fields edited as numeric bounds in model attribute
// forms.
export const attributeLimits = ['context', 'input_limit', 'output_limit'] as const

// attributeModalities lists accepted media labels for model input and output capability
// selectors.
export const attributeModalities = ['text', 'image', 'audio', 'video', 'pdf'] as const

// reasoningEfforts lists effort labels accepted by backend validation and offered by the
// reasoning editor.
export const reasoningEfforts = [
  'default',
  'none',
  'minimal',
  'low',
  'medium',
  'high',
  'xhigh',
  'max',
] as const

// conditionalCapabilities identifies model parameters whose support can depend on
// selected reasoning settings.
export const conditionalCapabilities = ['temperature', 'top_p', 'tool_call'] as const

// ConditionalCapability names a capability whose availability depends on reasoning
// configuration.
export type ConditionalCapability = (typeof conditionalCapabilities)[number]

// mediaListFields groups the media attributes edited as selectable lists in model
// capability forms.
export const mediaListFields = [
  'image_sizes',
  'aspect_ratios',
  'qualities',
  'background_options',
  'output_formats',
  'video_operations',
  'video_parameters',
] as const

// mediaLimitFields identifies numeric media generation limits displayed and edited by
// model capability forms.
export const mediaLimitFields = ['max_reference_images', 'max_output_count'] as const

// ModelAttributePresentation summarizes route attributes and distinct capability variants
// for public display.
export interface ModelAttributePresentation extends ModelAttributes {
  // Variants retain distinct attribute combinations, including unknown route fields, so
  // display consumers can explain alternatives without inferring universal support.
  variants?: ModelAttributes[] // Distinct route capability combinations.
}
