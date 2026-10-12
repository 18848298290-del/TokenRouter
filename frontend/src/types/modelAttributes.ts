import type { LocalizedUpdate } from '@/i18n/content'
// 模型属性由目录、平台和管理员配置解析，缺失字段表示未知或继承。
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
  conditions?: Partial<Record<'temperature' | 'top_p' | 'tool_call', ParameterCondition>>
  media?: MediaCapabilities
  attachment?: boolean
  route_differences?: boolean
}

export const attributeCapabilities = ['reasoning', 'tool_call', 'structured_output', 'temperature', 'top_p', 'attachment'] as const
export const attributeLimits = ['context', 'input_limit', 'output_limit'] as const
export const attributeModalities = ['text', 'image', 'audio', 'video', 'pdf'] as const
