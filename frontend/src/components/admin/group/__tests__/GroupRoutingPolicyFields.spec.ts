import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import GroupRequestCompatibilityFields from '../GroupRequestCompatibilityFields.vue'
import GroupRoutingPolicyFields from '../GroupRoutingPolicyFields.vue'
import { cloneRoutingPolicy, defaultRoutingPolicy } from '../routingPolicy'
import type { GroupRoutingPolicy } from '@/api/groups.types'

// GroupRoutingPolicyFields.spec.ts checks editable group mapping rows and policy copies using
// the real routing fields. Local incomplete rows survive parent round trips, while external
// policy changes rebuild rows and validation blocks mappings that cannot yet be submitted.

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
// stubs supplies the display and dialog replacements used by these mounted tests, leaving the
// target component behavior active for assertions.
const stubs = { Select: true, ModelTagInput: true, Icon: true, 'transition-group': true }
// sourceInputs returns the model-source inputs used by mapping draft interaction tests.
const sourceInputs = (wrapper: ReturnType<typeof mount>) =>
  wrapper.findAll('input[aria-label="admin.groups.routingPolicy.source"]')

describe('分组独立路由策略', () => {
  // An empty model mapping is opened in the policy editor. The mapping controls remain usable
  // and the form does not expose a separate policy-wide enable switch.
  it('空映射可以正常打开，表单不再显示策略总开关', () => {
    const raw = {
      ...defaultRoutingPolicy(),
      enabled: false,
      model_mapping: null,
      allowed_models: null,
      features_config: null,
    } as unknown as GroupRoutingPolicy
    const wrapper = mount(GroupRoutingPolicyFields, {
      props: { modelValue: raw },
      global: { stubs },
    })
    expect(wrapper.findComponent({ name: 'Toggle' }).props('modelValue')).toBe(false)
    expect(wrapper.findAllComponents({ name: 'Toggle' })).toHaveLength(1)
    expect(wrapper.text()).not.toContain('admin.groups.routingPolicy.enabled')
    expect(wrapper.text()).not.toContain('admin.groups.routingPolicy.features')
    expect(wrapper.text()).not.toContain('admin.groups.routingPolicy.imageBridge')
    expect(cloneRoutingPolicy(raw).model_mapping).toEqual({})
    expect(cloneRoutingPolicy(raw).enabled).toBe(true)
    expect(raw.enabled).toBe(false)
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
  })

  // A stored disabled policy includes compatibility text outside the visible controls. Editing
  // a feature emits an enabled policy and retains that stored compatibility text.
  it('编辑功能设置时直接启用策略并保留未展示的历史文字', () => {
    const policy = { ...defaultRoutingPolicy(), enabled: false, features: '旧展示文字' }
    const wrapper = mount(GroupRequestCompatibilityFields, {
      props: { idPrefix: 'test', modelValue: policy },
      global: { stubs },
    })
    wrapper.findAllComponents({ name: 'Toggle' })[0].vm.$emit('update:modelValue', true)
    expect(wrapper.emitted('update:modelValue')?.[0]?.[0]).toMatchObject({
      enabled: true,
      features: '旧展示文字',
      features_config: { web_search_emulation: { anthropic: true } },
    })
    expect(policy.enabled).toBe(false)
  })

  // A saved policy contains nested mappings, allowlists, and feature data. Editing its clone
  // leaves the source collections unchanged so another group cannot inherit draft mutations.
  it('复制策略后编辑映射、白名单和特性不会污染原分组', () => {
    const source = defaultRoutingPolicy()
    source.model_mapping = { alias: 'gpt-original' }
    source.allowed_models = ['gpt-original']
    source.features_config.codex_image_generation_bridge = { openai: false }
    const copy = cloneRoutingPolicy(source)
    copy.model_mapping.alias = 'changed'
    copy.allowed_models.push('changed')
    ;(copy.features_config.codex_image_generation_bridge as Record<string, boolean>).openai = true
    expect(source.model_mapping.alias).toBe('gpt-original')
    expect(source.allowed_models).toEqual(['gpt-original'])
    expect(source.features_config.codex_image_generation_bridge).toEqual({ openai: false })
  })

  // Local mapping rows are published and returned through the parent prop before an external
  // replacement arrives. Self-publication retains rows, while a different mapping rebuilds the
  // editor.
  it('外部改写映射时重建行，自身发布的映射不重建', async () => {
    const policy = { ...defaultRoutingPolicy(), model_mapping: { alias: 'gpt-a' } }
    const wrapper = mount(GroupRoutingPolicyFields, {
      props: { modelValue: policy },
      global: { stubs },
    })
    const firstInput = sourceInputs(wrapper)[0].element

    await sourceInputs(wrapper)[0].setValue('alias-2')
    const published = wrapper.emitted('update:modelValue')?.at(-1)?.[0] as GroupRoutingPolicy
    expect(published.model_mapping).toEqual({ 'alias-2': 'gpt-a' })
    await wrapper.setProps({ modelValue: published })
    expect(sourceInputs(wrapper)[0].element).toBe(firstInput)

    await wrapper.setProps({
      modelValue: { ...policy, model_mapping: { other: 'gpt-b', next: 'gpt-c' } },
    })
    expect(sourceInputs(wrapper).map((input) => (input.element as HTMLInputElement).value)).toEqual(
      ['other', 'next'],
    )
  })

  // An unfinished mapping row is present when validation is requested. The editor reports the
  // mapping error and prevents successful form validation until its endpoints are complete.
  it('存在未完成的映射时阻止表单提交', async () => {
    const wrapper = mount(
      {
        components: { GroupRoutingPolicyFields },
        template: '<form><GroupRoutingPolicyFields :model-value="policy" /></form>',
        data: () => ({ policy: defaultRoutingPolicy() }),
      },
      { global: { stubs }, attachTo: document.body },
    )
    const form = wrapper.get('form').element as HTMLFormElement
    expect(form.checkValidity()).toBe(true)

    await wrapper.get('button').trigger('click')
    expect(form.checkValidity()).toBe(false)
    expect(wrapper.get('[role="alert"]').text()).toBe(
      'admin.groups.routingPolicy.incompleteMapping',
    )
    wrapper.unmount()
  })
})
