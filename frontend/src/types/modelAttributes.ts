import type { LocalizedUpdate } from '@/i18n/content'
// modelAttributes.ts defines catalogue attributes, administrator overrides, and
// optional display presentations. Missing fields represent unknown information or
// inherited configuration, while explicit false values and empty arrays retain
// their meaning when routes are summarized for marketplace consumers.
export interface ReasoningOption {
  type: 'toggle' | 'effort' | 'budget_tokens'
  values?: (string | null)[]
  min?: number
  max?: number
}

export interface ParameterCondition {
  efforts?: string[]
  thinking?: boolean
}

export interface MediaCapabilities {
  video_operations?: string[]
  video_parameters?: string[]
  image_sizes?: string[]
  aspect_ratios?: string[]
  qualities?: string[]
  background_options?: string[]
  output_formats?: string[]
  max_reference_images?: number
  max_output_count?: number
  input_fidelity?: boolean
}

export interface ModelAttributes {
  search_terms?: string[]
  display_name_localization?: LocalizedUpdate<string>
  display_name?: string
  context?: number
  input_limit?: number
  output_limit?: number
  input_modalities?: string[]
  output_modalities?: string[]
  reasoning?: boolean
  tool_call?: boolean
  structured_output?: boolean
  temperature?: boolean
  top_p?: boolean
  reasoning_options?: ReasoningOption[]
  interleaved?: true | { field: 'reasoning_content' | 'reasoning_details' }
  conditions?: Partial<Record<ConditionalCapability, ParameterCondition>>
  media?: MediaCapabilities
  attachment?: boolean
  route_differences?: boolean
}

export const attributeCapabilities = ['reasoning', 'tool_call', 'structured_output', 'temperature', 'top_p', 'attachment'] as const
export const attributeLimits = ['context', 'input_limit', 'output_limit'] as const
export const attributeModalities = ['text', 'image', 'audio', 'video', 'pdf'] as const
// 推理档位与后端校验的取值一致。effort 选项里的 null 表示请求不带档位，由供应商决定。
export const reasoningEfforts = ['default', 'none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max'] as const
export const conditionalCapabilities = ['temperature', 'top_p', 'tool_call'] as const
export type ConditionalCapability = typeof conditionalCapabilities[number]
export const mediaListFields = ['image_sizes', 'aspect_ratios', 'qualities', 'background_options', 'output_formats', 'video_operations', 'video_parameters'] as const
export const mediaLimitFields = ['max_reference_images', 'max_output_count'] as const

// ModelAttributePresentation summarizes route values for public display consumers.
export interface ModelAttributePresentation extends ModelAttributes {
  // Variants retain distinct attribute combinations, including unknown route fields,
  // so display consumers can explain alternatives without inferring universal support.
  variants?: ModelAttributes[]
}
