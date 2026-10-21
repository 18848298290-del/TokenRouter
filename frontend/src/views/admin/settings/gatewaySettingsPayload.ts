import type { SettingsForm } from './settingsState'
import type { UpdateSettingsRequest } from '@/api/admin/settings'
import type { useGatewaySettings } from './useGatewaySettings'

/**
 * gatewaySettingsPayload.ts serializes the gateway portion of the administrator
 * bulk settings request. It reads the shared draft after ordered page validation
 * and keeps each field's empty-value, numeric, and secret treatment alongside
 * the settings domain that owns those fields.
 */

/**
 * buildGatewaySettingsPayload returns gateway fields for the ordered bulk settings
 * update after validation. Empty text overrides remain empty for the server's
 * default handling, scheduler strings are trimmed, and quota fractions retain
 * their numeric values. The prompt replacement owner supplies normalized rules.
 */
export function buildGatewaySettingsPayload(
  form: Pick<
    SettingsForm,
    | 'enable_model_fallback'
    | 'fallback_model_anthropic'
    | 'fallback_model_openai'
    | 'fallback_model_gemini'
    | 'fallback_model_antigravity'
    | 'grok_default_text_model'
    | 'grok_default_base_url_mode'
    | 'enable_identity_patch'
    | 'identity_patch_prompt'
    | 'min_claude_code_version'
    | 'max_claude_code_version'
    | 'ttft_mode'
    | 'enable_fingerprint_unification'
    | 'enable_metadata_passthrough'
    | 'enable_claude_oauth_system_prompt_injection'
    | 'claude_oauth_system_prompt'
    | 'claude_oauth_system_prompt_blocks'
    | 'enable_anthropic_cache_ttl_1h_injection'
    | 'rewrite_message_cache_control'
    | 'enable_client_dateline_normalization'
    | 'antigravity_user_agent_version'
    | 'openai_codex_user_agent'
    | 'openai_allow_claude_code_codex_plugin'
    | 'advanced_scheduler_sticky_weighted_enabled'
    | 'advanced_scheduler_subscription_priority_enabled'
    | 'advanced_scheduler_ewma_error_rate_alpha'
    | 'advanced_scheduler_ewma_ttft_alpha'
    | 'advanced_scheduler_sticky_escape_enabled'
    | 'advanced_scheduler_sticky_escape_ttft_ms'
    | 'advanced_scheduler_sticky_escape_error_rate'
    | 'advanced_scheduler_lb_top_k'
    | 'advanced_scheduler_weight_priority'
    | 'advanced_scheduler_weight_load'
    | 'advanced_scheduler_weight_queue'
    | 'advanced_scheduler_weight_error_rate'
    | 'advanced_scheduler_weight_ttft'
    | 'advanced_scheduler_weight_reset'
    | 'advanced_scheduler_weight_quota_headroom'
    | 'advanced_scheduler_weight_previous_response'
    | 'advanced_scheduler_weight_session_sticky'
    | 'openai_provider_quota_auto_pause'
    | 'allow_user_view_error_requests'
  >,
  actions: Pick<
    ReturnType<typeof useGatewaySettings>,
    'normalizeUserPromptReplacementConfigForSave'
  >,
): Pick<
  UpdateSettingsRequest,
  | 'enable_model_fallback'
  | 'fallback_model_anthropic'
  | 'fallback_model_openai'
  | 'fallback_model_gemini'
  | 'fallback_model_antigravity'
  | 'grok_default_text_model'
  | 'grok_default_base_url_mode'
  | 'enable_identity_patch'
  | 'identity_patch_prompt'
  | 'min_claude_code_version'
  | 'max_claude_code_version'
  | 'ttft_mode'
  | 'enable_fingerprint_unification'
  | 'enable_metadata_passthrough'
  | 'enable_claude_oauth_system_prompt_injection'
  | 'claude_oauth_system_prompt'
  | 'claude_oauth_system_prompt_blocks'
  | 'enable_anthropic_cache_ttl_1h_injection'
  | 'rewrite_message_cache_control'
  | 'enable_client_dateline_normalization'
  | 'antigravity_user_agent_version'
  | 'openai_codex_user_agent'
  | 'openai_allow_claude_code_codex_plugin'
  | 'user_prompt_replacement_config'
  | 'advanced_scheduler_sticky_weighted_enabled'
  | 'advanced_scheduler_subscription_priority_enabled'
  | 'advanced_scheduler_ewma_error_rate_alpha'
  | 'advanced_scheduler_ewma_ttft_alpha'
  | 'advanced_scheduler_sticky_escape_enabled'
  | 'advanced_scheduler_sticky_escape_ttft_ms'
  | 'advanced_scheduler_sticky_escape_error_rate'
  | 'advanced_scheduler_lb_top_k'
  | 'advanced_scheduler_weight_priority'
  | 'advanced_scheduler_weight_load'
  | 'advanced_scheduler_weight_queue'
  | 'advanced_scheduler_weight_error_rate'
  | 'advanced_scheduler_weight_ttft'
  | 'advanced_scheduler_weight_reset'
  | 'advanced_scheduler_weight_quota_headroom'
  | 'advanced_scheduler_weight_previous_response'
  | 'advanced_scheduler_weight_session_sticky'
  | 'openai_provider_quota_auto_pause'
  | 'allow_user_view_error_requests'
> {
  const { normalizeUserPromptReplacementConfigForSave } = actions
  return {
    enable_model_fallback: form.enable_model_fallback,
    fallback_model_anthropic: form.fallback_model_anthropic,
    fallback_model_openai: form.fallback_model_openai,
    fallback_model_gemini: form.fallback_model_gemini,
    fallback_model_antigravity: form.fallback_model_antigravity,

    // An empty model input supplies the built-in Grok model to the settings endpoint,
    // giving requests that permit omitted model names a concrete default to select.
    grok_default_text_model: form.grok_default_text_model.trim() || 'grok-4.5',
    grok_default_base_url_mode: form.grok_default_base_url_mode,
    enable_identity_patch: form.enable_identity_patch,
    identity_patch_prompt: form.identity_patch_prompt,
    min_claude_code_version: form.min_claude_code_version,
    max_claude_code_version: form.max_claude_code_version,

    // The serialized timing choice accepts first_output directly; any other draft value
    // uses first_event so unexpected input cannot introduce another timing mode into the payload.
    ttft_mode: form.ttft_mode === 'first_output' ? 'first_output' : 'first_event',
    enable_fingerprint_unification: form.enable_fingerprint_unification,
    enable_metadata_passthrough: form.enable_metadata_passthrough,
    enable_claude_oauth_system_prompt_injection: form.enable_claude_oauth_system_prompt_injection,
    claude_oauth_system_prompt: form.claude_oauth_system_prompt?.trim() || '',
    claude_oauth_system_prompt_blocks: form.claude_oauth_system_prompt_blocks?.trim() || '',
    enable_anthropic_cache_ttl_1h_injection: form.enable_anthropic_cache_ttl_1h_injection,
    rewrite_message_cache_control: form.rewrite_message_cache_control,
    enable_client_dateline_normalization: form.enable_client_dateline_normalization,
    antigravity_user_agent_version: form.antigravity_user_agent_version?.trim() || '',
    openai_codex_user_agent: form.openai_codex_user_agent?.trim() || '',
    openai_allow_claude_code_codex_plugin: form.openai_allow_claude_code_codex_plugin,
    user_prompt_replacement_config: normalizeUserPromptReplacementConfigForSave(),
    advanced_scheduler_sticky_weighted_enabled: form.advanced_scheduler_sticky_weighted_enabled,
    advanced_scheduler_subscription_priority_enabled:
      form.advanced_scheduler_subscription_priority_enabled,
    advanced_scheduler_ewma_error_rate_alpha: form.advanced_scheduler_ewma_error_rate_alpha.trim(),
    advanced_scheduler_ewma_ttft_alpha: form.advanced_scheduler_ewma_ttft_alpha.trim(),
    advanced_scheduler_sticky_escape_enabled: form.advanced_scheduler_sticky_escape_enabled,
    advanced_scheduler_sticky_escape_ttft_ms: form.advanced_scheduler_sticky_escape_ttft_ms.trim(),
    advanced_scheduler_sticky_escape_error_rate:
      form.advanced_scheduler_sticky_escape_error_rate.trim(),
    advanced_scheduler_lb_top_k: form.advanced_scheduler_lb_top_k.trim(),
    advanced_scheduler_weight_priority: form.advanced_scheduler_weight_priority.trim(),
    advanced_scheduler_weight_load: form.advanced_scheduler_weight_load.trim(),
    advanced_scheduler_weight_queue: form.advanced_scheduler_weight_queue.trim(),
    advanced_scheduler_weight_error_rate: form.advanced_scheduler_weight_error_rate.trim(),
    advanced_scheduler_weight_ttft: form.advanced_scheduler_weight_ttft.trim(),
    advanced_scheduler_weight_reset: form.advanced_scheduler_weight_reset.trim(),
    advanced_scheduler_weight_quota_headroom: form.advanced_scheduler_weight_quota_headroom.trim(),
    advanced_scheduler_weight_previous_response:
      form.advanced_scheduler_weight_previous_response.trim(),
    advanced_scheduler_weight_session_sticky: form.advanced_scheduler_weight_session_sticky.trim(),
    openai_provider_quota_auto_pause: {
      default_threshold_5h: form.openai_provider_quota_auto_pause.default_threshold_5h,
      default_threshold_7d: form.openai_provider_quota_auto_pause.default_threshold_7d,
    },
    allow_user_view_error_requests: form.allow_user_view_error_requests,
  }
}
