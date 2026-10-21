import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

import ReasoningEffortPolicyFields from '../ReasoningEffortPolicyFields.vue'
import { createReasoningEffortMappingRow } from '@/components/admin/group/groupsReasoningEffort'

// ReasoningEffortPolicyFields.spec.ts checks scoped reasoning controls with the real field
// component and shared mapping helpers. Model scope inputs stay attached to each mapping group,
// and new groups begin with an identified empty pair suitable for validation.

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  }
})

describe('ReasoningEffortPolicyFields', () => {
  // A mapping group includes a matching mode, model, and effort pair. Rendering exposes its
  // scope controls so edits can target that group independently from other mappings.
  it('renders model scope fields for each mapping', () => {
    const mapping = createReasoningEffortMappingRow({
      from: 'max',
      to: 'low',
      match_type: 'prefix',
      model: 'gpt',
    })
    const wrapper = mount(ReasoningEffortPolicyFields, {
      props: {
        idPrefix: 'create-group-reasoning',
        platform: 'openai',
        maxEffort: '',
        overLimit: 'downgrade',
        mappings: [mapping],
      },
      global: {
        stubs: {
          Icon: { props: ['name'], template: '<i :data-icon="name" />' },
          Select: {
            props: ['id', 'modelValue', 'placeholder'],
            template: '<input :id="id" :value="modelValue ?? \'\'" :placeholder="placeholder" />',
          },
        },
      },
    })

    expect(wrapper.text()).toContain('admin.groups.form.reasoningEffortMatchType')
    expect(wrapper.text()).toContain('admin.groups.form.reasoningEffortModel')
    expect(wrapper.text()).toContain('admin.groups.form.reasoningEffortFrom')
    expect(wrapper.text()).toContain('admin.groups.form.reasoningEffortTo')
    expect(wrapper.text()).toContain('admin.groups.form.addReasoningEffortPair')
    expect(wrapper.text()).not.toContain('class=')

    const modelInput = wrapper.get(`#create-group-reasoning-${mapping.id}-model`)
    expect(modelInput.element).toMatchObject({
      value: 'gpt',
      placeholder: 'admin.groups.form.reasoningEffortModelPlaceholder',
    })
  })

  // The add-group action starts from the current reasoning mapping list. Its update event
  // appends one new group containing an empty pair with local identifiers for subsequent
  // validation.
  it('emits a new mapping group with one empty pair', async () => {
    const wrapper = mount(ReasoningEffortPolicyFields, {
      props: {
        idPrefix: 'edit-group-reasoning',
        platform: 'openai',
        maxEffort: '',
        overLimit: 'downgrade',
        mappings: [],
      },
      global: {
        stubs: {
          Icon: { props: ['name'], template: '<i :data-icon="name" />' },
          Select: { template: '<div />' },
        },
      },
    })

    await wrapper.get('button').trigger('click')

    const emitted = wrapper.emitted('update:mappings')
    expect(emitted).toHaveLength(1)
    expect(emitted?.[0]?.[0]).toEqual([
      expect.objectContaining({
        match_type: '',
        model: '',
        pairs: [
          expect.objectContaining({
            from: '',
            to: '',
          }),
        ],
      }),
    ])
  })
})
