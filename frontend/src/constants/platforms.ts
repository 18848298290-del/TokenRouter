import type { ProviderPlatform } from '@/api/admin/providers.types'

// platforms.ts defines the concrete provider-platform options used by administration
// filters. Each entry pairs a provider API identifier with its display label, and the
// typed catalog lets downstream controls derive consistent choices while preserving
// their declared order.

// PlatformOption pairs a selectable platform identifier with display data and optional
// extensions.
export interface PlatformOption<T extends string = string> {
  value: T // Backend platform identifier value.
  label: string // Visible platform option label.
  [key: string]: unknown // Optional consumer-specific display data.
}
// CONCRETE_PLATFORM_OPTIONS lists provider identifiers and display labels in the order
// shared by administrator platform filters.
export const CONCRETE_PLATFORM_OPTIONS = [
  { value: 'anthropic', label: 'Anthropic' },
  { value: 'openai', label: 'OpenAI' },
  { value: 'gemini', label: 'Gemini' },
  { value: 'antigravity', label: 'Antigravity' },
  { value: 'qoder', label: 'Qoder' },
  { value: 'grok', label: 'Grok' },
  { value: 'kimi', label: 'Kimi' },
  { value: 'zhipu', label: 'Zhipu' },
  { value: 'deepseek', label: 'DeepSeek' },
  { value: 'jev', label: 'Jev' },
] as const satisfies readonly PlatformOption<ProviderPlatform>[]
