import { describe, expect, it } from 'vitest'
import {
  readResponsesWSConnectionMode,
  clearLegacyResponsesWSSettings,
} from '@/utils/responsesWsConnection'

/**
 * responsesWsConnection.spec.ts checks the current Responses connection
 * setting and removal of retired overrides. The reader defaults unknown or
 * absent current data to pooled reuse, and cleanup preserves the shared mode
 * together with unrelated provider settings.
 */

describe('Responses WS connection settings', () => {
  /**
   * Current connection data is absent, explicitly per-session, or accompanied
   * only by a legacy flag. The reader must select per-session only from the
   * current recognized field.
   */
  it('defaults to pooled connections and reads the shared field', () => {
    expect(readResponsesWSConnectionMode(undefined)).toBe('pooled')
    expect(readResponsesWSConnectionMode({ responses_ws_connection_mode: 'per_session' })).toBe(
      'per_session',
    )
    expect(readResponsesWSConnectionMode({ openai_ws_enabled: false })).toBe('pooled')
  })
  /**
   * A mutable extra object contains retired WebSocket settings and unrelated
   * data. Cleanup must remove obsolete keys while preserving the current
   * connection mode and unrelated value.
   */
  it('removes retired fields without changing unrelated settings', () => {
    const extra = {
      responses_ws_connection_mode: 'per_session',
      openai_ws_enabled: false,
      openai_oauth_responses_websockets_v2_mode: 'off',
      custom: 3,
    }
    clearLegacyResponsesWSSettings(extra)
    expect(extra).toEqual({ responses_ws_connection_mode: 'per_session', custom: 3 })
  })
})
