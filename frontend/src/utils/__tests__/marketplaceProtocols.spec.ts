import { describe, expect, it } from 'vitest'
import fixture from '@/__tests__/fixtures/protocol-catalog.json'
import { MARKETPLACE_PROTOCOLS, marketplaceProtocols } from '../marketplaceProtocols'
import { resolveProviderBrandKey } from '../providerBrand'
import zhMisc from '@/i18n/locales/zh/misc'
import enMisc from '@/i18n/locales/en/misc'

/**
 * marketplaceProtocols.spec.ts tests the public protocol list against the
 * catalog fixture and locale dictionaries. It verifies endpoint order,
 * translated short names, resolvable brand identities, and selection behavior
 * for missing or unsupported protocol identifiers.
 */

describe('marketplaceProtocols', () => {
  /**
   * The public protocol catalog is compared with client entries from the protocol
   * fixture. Endpoint text and ordering match the fixture after upstream-only entries
   * are removed.
   */
  it('端点和顺序与后端协议目录的客户端协议一致', () => {
    const clientProtocols = fixture.protocols
      .filter((protocol) => !protocol.upstream_only)
      .map((protocol) => ({ id: protocol.id, endpoint: protocol.endpoint }))
    expect(MARKETPLACE_PROTOCOLS.map(({ id, endpoint }) => ({ id, endpoint }))).toEqual(
      clientProtocols,
    )
  })

  /**
   * Every public protocol is checked against both language dictionaries and brand
   * resolution. Each has localized short-name keys and a brand identifier resolvable to
   * its expected icon identity.
   */
  it('每个协议都有中英文短名，品牌能解析出图标', () => {
    for (const protocol of MARKETPLACE_PROTOCOLS) {
      expect(zhMisc.marketplace.protocolNames).toHaveProperty(protocol.id)
      expect(enMisc.marketplace.protocolNames).toHaveProperty(protocol.id)
      expect(resolveProviderBrandKey(protocol.brand)).toBe(protocol.brand)
    }
  })

  /**
   * Selections arrive out of catalog order and include an upstream-only entry. Results
   * follow the public catalog order, omit unsupported IDs, and return empty for missing
   * selections.
   */
  it('按目录顺序返回，跳过上游专用和未知的协议', () => {
    expect(
      marketplaceProtocols(['openai_chat_completions', 'qoder_chat', 'anthropic_messages']).map(
        (protocol) => protocol.id,
      ),
    ).toEqual(['anthropic_messages', 'openai_chat_completions'])
    expect(marketplaceProtocols(undefined)).toEqual([])
  })
})
