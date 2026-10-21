import { describe, expect, it } from 'vitest'
import en from '../locales/en/admin/providers'
import zh from '../locales/zh/admin/providers'

// wsModeLocaleDesc.spec.ts checks provider WebSocket connection-lifetime
// descriptions in the bilingual catalogs. These labels explain session
// completion and connection closing to administrators, while the tested
// description excludes the internal configuration field name from the visible
// explanation.

describe('Responses WS connection descriptions', () => {
  // Connection policy hints explain what happens when a Responses WebSocket
  // session ends. The tested translations describe closing or session completion
  // and exclude the internal mode-router setting name.
  it('explains connection lifetime without internal mode names', () => {
    expect(zh.providers.openai.wsConnectionPooledHint).toContain('会话结束后')
    expect(zh.providers.openai.wsConnectionPerSessionHint).toContain('关闭连接')
    expect(en.providers.openai.wsConnectionPerSessionHint).toContain('closes')
    expect(zh.providers.openai.wsModeDesc).not.toContain('mode_router_v2_enabled')
  })
})
