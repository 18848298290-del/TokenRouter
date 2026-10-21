import { useProtocolCatalogFixture } from '@/__tests__/helpers/protocolCatalog'
import { describe, expect, it } from 'vitest'
import { protocolCatalog } from '@/api/admin/protocolCapabilities'
import {
  defaultGroupClientProtocols,
  effectiveGroupClientProtocols,
  sanitizeGroupProtocolFallbacks,
  setGroupClientProtocol,
  supportedGroupClientProtocols,
} from '../groupClientProtocols'

/**
 * groupClientProtocols.spec.ts tests group protocol selection against a
 * controlled catalog fixture. Cases cover catalog order, empty selections,
 * unsupported sources and targets, duplicate conversion targets, explicit empty
 * target lists, and copying when catalog data is unavailable.
 */

useProtocolCatalogFixture()
describe('通用分组协议', () => {
  /**
   * The loaded fixture supplies a common group protocol catalog and its defaults.
   * Supported choices follow client entries, while default choices contain the three
   * configured text entrypoints.
   */
  it('所有分组使用统一入口目录，新组只默认开放三个文本入口', () => {
    expect(supportedGroupClientProtocols()).toEqual(
      protocolCatalog
        .value!.protocols.filter((protocol) => !protocol.upstream_only)
        .map((protocol) => protocol.id),
    )
    expect(defaultGroupClientProtocols()).toEqual([
      'anthropic_messages',
      'openai_responses',
      'openai_chat_completions',
    ])
  })
  /**
   * Both omitted configuration and an explicit empty selection reach the
   * effective-protocol helper. Neither automatically enables a protocol while the
   * catalog fixture is loaded.
   */
  it('缺少配置和显式空集合都不自动开放入口', () => {
    expect(effectiveGroupClientProtocols(undefined)).toEqual([])
    expect(effectiveGroupClientProtocols([])).toEqual([])
  })
  /**
   * Protocols are disabled down to an empty selection and an upstream-only protocol is
   * requested. Remaining selections stay ordered and the unsupported client entry
   * remains absent.
   */
  it('全部入口均可关闭，排序由服务端目录维护', () => {
    expect(
      setGroupClientProtocol(
        ['openai_chat_completions', 'openai_responses'],
        'openai_responses',
        false,
      ),
    ).toEqual(['openai_chat_completions'])
    expect(
      setGroupClientProtocol(['openai_chat_completions'], 'openai_chat_completions', false),
    ).toEqual([])
    expect(setGroupClientProtocol([], 'qoder_chat', true)).toEqual([])
  })
  /**
   * The administrator protocol catalog is unavailable for a user-side selection. The
   * supplied recognized-looking protocol IDs pass through in their existing order for
   * display.
   */
  it('用户侧直接读取后端集合，不需要管理员目录', () => {
    protocolCatalog.value = null
    expect(effectiveGroupClientProtocols(['openai_responses', 'openai_images_edits'])).toEqual([
      'openai_responses',
      'openai_images_edits',
    ])
  })
  /**
   * A historical conversion map contains valid and unsupported source protocols.
   * Sanitization removes the unsupported sources while retaining the valid source and
   * its allowed conversion target.
   */
  it('历史分组的未知转换源在提交前被剔除', () => {
    const sanitized = sanitizeGroupProtocolFallbacks({
      anthropic_messages: ['openai_responses'],
      qoder_chat: ['openai_responses'],
      vertex_batch_prediction: [],
      gemini_batch_generate_content: ['openai_responses'],
    })
    expect(sanitized).toEqual({ anthropic_messages: ['openai_responses'] })
  })
  /**
   * Conversion targets include duplicates and invalid edges alongside a source with no
   * allowed survivors. Sanitization deduplicates targets and retains an explicit empty
   * target list for that source.
   */
  it('非法目标被过滤去重，显式空数组保留仅原生语义', () => {
    const sanitized = sanitizeGroupProtocolFallbacks({
      anthropic_messages: ['openai_responses', 'openai_responses', 'bogus_target'],
      gemini_generate_content: ['openai_responses'],
    })
    expect(sanitized).toEqual({
      anthropic_messages: ['openai_responses'],
      gemini_generate_content: [],
    })
  })
  /**
   * The protocol catalog is missing when conversion configuration is sanitized. The
   * returned outer object is a separate copy with the supplied mapping content intact.
   */
  it('目录未加载时原样透传，由提交守卫兜底', () => {
    protocolCatalog.value = null
    const fallbacks = { qoder_chat: ['openai_responses' as const] }
    const sanitized = sanitizeGroupProtocolFallbacks(fallbacks)
    expect(sanitized).toEqual(fallbacks)
    expect(sanitized).not.toBe(fallbacks)
  })
})
