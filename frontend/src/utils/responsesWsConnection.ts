/**
 * responsesWsConnection.ts reads the current Responses WebSocket connection
 * setting and removes retired override fields from provider drafts. Unknown or
 * absent current values use pooled connections, and hint selection follows the
 * same two-mode presentation used by the editing controls.
 */

/**
 * These stored string values identify pooled reuse and per-session isolation
 * for the shared Responses WebSocket connection field.
 */
export const RESPONSES_WS_POOLED = 'pooled'
export const RESPONSES_WS_PER_SESSION = 'per_session'
/**
 * ResponsesWSConnectionMode names pooled or per-session connection reuse in
 * provider settings.
 */
export type ResponsesWSConnectionMode = typeof RESPONSES_WS_POOLED | typeof RESPONSES_WS_PER_SESSION

/**
 * readResponsesWSConnectionMode reads the shared connection field and defaults
 * to pooled mode. Only the exact per_session value selects session isolation;
 * retired fields do not influence this normalized form value.
 */
export function readResponsesWSConnectionMode(
  extra: Record<string, unknown> | null | undefined,
): ResponsesWSConnectionMode {
  return extra?.responses_ws_connection_mode === RESPONSES_WS_PER_SESSION
    ? RESPONSES_WS_PER_SESSION
    : RESPONSES_WS_POOLED
}

/**
 * responsesWSConnectionHint returns the translation key for the selected
 * connection mode.
 */
export function responsesWSConnectionHint(mode: ResponsesWSConnectionMode) {
  return mode === RESPONSES_WS_PER_SESSION
    ? 'admin.providers.openai.wsConnectionPerSessionHint'
    : 'admin.providers.openai.wsConnectionPooledHint'
}

/**
 * clearLegacyResponsesWSSettings deletes retired WebSocket keys from the
 * supplied mutable extra object. The current shared mode and unrelated
 * settings remain intact; callers own the draft and persist the cleanup
 * afterward.
 */
export function clearLegacyResponsesWSSettings(extra: Record<string, unknown>) {
  for (const key of [
    'openai_oauth_responses_websockets_v2_mode',
    'openai_apikey_responses_websockets_v2_mode',
    'openai_oauth_responses_websockets_v2_enabled',
    'openai_apikey_responses_websockets_v2_enabled',
    'responses_websockets_v2_enabled',
    'openai_ws_enabled',
    'openai_ws_force_http',
  ])
    delete extra[key]
}
