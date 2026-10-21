import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { nextTick } from 'vue'
import type { Group } from '@/api/groups.types'
import UseKeyModal from '../UseKeyModal.vue'
import Select from '@/components/common/Select.vue'

// UseKeyModal.spec.ts mounts key setup instructions against controlled group capability
// records and clipboard support. The suite exercises protocol-driven client and model
// selection, generated configuration content, and composite prefixed identifiers through the
// actual modal.

// The clipboard mock captures generated configuration while the mixed-group fixture supplies
// real model identifiers for each supported client protocol.
const { copy } = vi.hoisted(() => ({ copy: vi.fn().mockResolvedValue(true) }))
vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual('vue-i18n')),
  useI18n: () => ({ t: (key: string) => key }),
}))
vi.mock('@/composables/useClipboard', () => ({ useClipboard: () => ({ copyToClipboard: copy }) }))
// mixedGroup pairs each model with its allowed protocol set so client switches can be
// checked against actual capability filtering.
const mixedGroup = {
  id: 7,
  name: 'Mixed',
  allowed_protocols: [
    'anthropic_messages',
    'openai_responses',
    'openai_responses_websocket',
    'gemini_generate_content',
  ],
  models: ['claude-custom', 'gpt-custom', 'gemini-custom'],
  model_protocols: {
    'claude-custom': ['anthropic_messages'],
    'gpt-custom': ['openai_responses', 'openai_responses_websocket'],
    'gemini-custom': ['gemini_generate_content'],
  },
} as Group
// create mounts client setup with a supplied group or the mixed-protocol fixture. Dialog and
// selection controls are stubbed while the production component generates the displayed
// configuration content.
const create = (group: Group | null = mixedGroup) =>
  mount(UseKeyModal, {
    props: { show: true, apiKey: 'sk-example', baseUrl: 'https://api.example.com/v1', group },
    global: {
      stubs: { BaseDialog: { template: '<div><slot /></div>' }, Select: true, Toggle: true },
    },
  })
describe('按客户端能力配置 Key', () => {
  // A key without a bound group opens the setup modal. The test protects the group-selection
  // explanation and prevents generation of configuration from absent capabilities.
  it('未绑定分组时提示选组，不生成配置', () => {
    const wrapper = create(null)
    expect(wrapper.text()).toContain('keys.useKeyModal.noGroupDescription')
    expect(wrapper.find('pre').exists()).toBe(false)
  })
  // A mixed group offers several text protocols with different model capabilities. The test
  // checks client tabs and verifies that changing client selects an actual protocol-
  // compatible model.
  it('混合分组显示多个客户端并按协议筛选真实模型', async () => {
    const wrapper = create()
    expect(wrapper.findAll('[role="tab"]').map((tab) => tab.text())).toEqual([
      'Claude Code',
      'Codex CLI',
      'Gemini CLI',
      'Grok CLI',
      'OpenCode',
    ])
    expect(wrapper.findComponent(Select).props('options')).toEqual([
      { value: 'claude-custom', label: 'claude-custom' },
    ])
    expect(wrapper.get('pre').text()).toContain('claude-custom')
    await wrapper.findAll('[role="tab"]')[1].trigger('click')
    expect(wrapper.findComponent(Select).props('options')).toEqual([
      { value: 'gpt-custom', label: 'gpt-custom' },
    ])
    expect(wrapper.get('pre').text()).toContain('model = "gpt-custom"')
  })
  // A bound group has no requestable models in its catalog. The test protects the empty-
  // model explanation and rejects configuration containing an invented example model.
  it('没有可请求模型时不写入默认示例', () => {
    const wrapper = create({ ...mixedGroup, models: [] })
    expect(wrapper.find('pre').exists()).toBe(false)
    expect(wrapper.text()).toContain('keys.useKeyModal.noModels')
  })
  // The bound group changes after a model was selected in the modal. The test checks that
  // generated configuration uses the replacement catalog and drops the unavailable previous
  // model.
  it('切换分组后清除已失效的模型', async () => {
    const wrapper = create()
    await wrapper.setProps({
      group: {
        ...mixedGroup,
        models: ['custom'],
        model_protocols: { custom: ['anthropic_messages'] },
      },
    })
    await nextTick()
    expect(wrapper.get('pre').text()).toContain("ANTHROPIC_MODEL='custom'")
    expect(wrapper.get('pre').text()).not.toContain('claude-custom')
  })
  // A group exposes only a media protocol rather than text client capabilities. The test
  // protects the explanatory empty state and absence of generated text configuration.
  it('仅媒体入口不提供文本客户端配置', () => {
    const wrapper = create({ ...mixedGroup, allowed_protocols: ['image_batches'] })
    expect(wrapper.find('[data-testid="no-text-protocols"]').exists()).toBe(true)
    expect(wrapper.find('pre').exists()).toBe(false)
  })
  // The user copies the configuration displayed by the selected client. The test checks the
  // clipboard receives the actual generated contents shown in the modal.
  it('复制实际生成内容', async () => {
    const wrapper = create()
    const button = wrapper
      .findAll('button')
      .find((button) => button.text() === 'keys.useKeyModal.copy')!
    await button.trigger('click')
    expect(copy).toHaveBeenCalledWith(wrapper.get('pre').text())
    wrapper.unmount()
  })
  // A composite key exposes a prefix binding with requestable models. The test checks the
  // displayed prefixed identifier and verifies that copying retains its routing prefix.
  it('复合 Key 示例使用可请求模型并复制带前缀的 ID', async () => {
    const wrapper = create(null)
    await wrapper.setProps({
      compositeGroups: [{ group_id: 7, prefix: 'shared', group: mixedGroup }],
    })
    expect(wrapper.text()).toContain('shared/claude-custom')
    await wrapper.get('button').trigger('click')
    expect(copy).toHaveBeenCalledWith('shared/claude-custom')
    wrapper.unmount()
  })
})
