import { flushPromises, mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import AnnouncementTargetingEditor from '../AnnouncementTargetingEditor.vue'
import type { AnnouncementTargeting } from '@/api/announcements.types'

// AnnouncementTargetingEditor.spec.ts checks targeting payload edits and input identity
// through the actual nested rule-list editors. Select and transition behavior are
// stubbed so tests can follow cloned balance-condition rows, empty-group validation,
// and add-button limits without popup interactions.

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => key, // Translation key passthrough.
  }), // Translation composable substitute.
}))

// mountEditor mounts the targeting editor with parent-style model feedback and real
// rule-list shells. The wrapper attaches to document.body for focus assertions, so each
// test unmounts it after checking input identity.
function mountEditor(value: AnnouncementTargeting) {
  const wrapper = mount(AnnouncementTargetingEditor, {
    attachTo: document.body, // Mounted DOM container.
    props: {
      modelValue: value, // Parent-owned editor value.
      plans: [], // Available subscription plans.
      'onUpdate:modelValue': (updated) => {
        void wrapper.setProps({
          modelValue: updated, // Parent-owned editor value.
        })
      }, // Parent model feedback callback.
    }, // Accepted component props.
    global: {
      stubs: {
        Select: true, // Selector rendering substitute.
        Icon: true, // Icon rendering substitute.
        'transition-group': true, // List animation substitute.
      }, // Replaced child components.
    }, // Mount-wide component overrides.
  })
  return wrapper
}

// balanceGroup creates one greater-than-or-equal balance condition for targeting editor
// fixtures.
const balanceGroup = (value: number) => ({
  all_of: [
    {
      type: 'balance' as const, // Eligibility condition kind.
      operator: 'gte' as const, // Eligibility comparison operator.
      value, // Fixture balance threshold.
    },
  ], // Jointly required conditions.
})

describe('AnnouncementTargetingEditor', () => {
  // Editing and removing OR groups clones the payload while keeping a surviving input
  // focused. The assertions protect unchanged source values, replacement payload
  // contents, and retained DOM identity.
  it('克隆更新保留焦点和行身份，提交数据不包含展示 key', async () => {
    const original = {
      any_of: [balanceGroup(1), balanceGroup(2)], // Alternative eligibility groups.
    }
    const wrapper = mountEditor(original)
    const input = wrapper.findAll('input[type="number"]')[1]
    const element = input.element as HTMLInputElement
    element.focus()
    await input.setValue('25')
    await flushPromises()
    expect(document.activeElement).toBe(element)
    expect(original.any_of[1].all_of[0].value).toBe(2)
    expect(wrapper.props('modelValue')).toEqual({
      any_of: [balanceGroup(1), balanceGroup(25)], // Alternative eligibility groups.
    })
    await wrapper.get('[data-testid="announcement-groups-remove-0"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('input[type="number"]').element).toBe(element)
    expect(wrapper.findAll('[data-testid="announcement-groups-row"]')).toHaveLength(1)
    wrapper.unmount()
  })

  // Removing AND conditions first leaves the surviving input element in place, then
  // leaves an empty group. The editor must display its required-condition message for
  // that empty group.
  it('删除 AND 条件后保留剩余输入，并沿用空组校验', async () => {
    const wrapper = mountEditor({
      any_of: [
        {
          all_of: [...balanceGroup(1).all_of, ...balanceGroup(2).all_of], // Jointly required conditions.
        },
      ], // Alternative eligibility groups.
    })
    const second = wrapper.findAll('input[type="number"]')[1].element
    await wrapper.get('[data-testid="announcement-conditions-0-remove-0"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('input[type="number"]').element).toBe(second)
    await wrapper.get('[data-testid="announcement-conditions-0-remove-0"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toBe('admin.announcements.form.addAndCondition')
    wrapper.unmount()
  })

  // An editor begins with fifty OR groups and fifty conditions in its first group. Both
  // add buttons must be disabled at their respective configured limits.
  it('OR 和 AND 条目达到 50 时禁用各自的添加按钮', () => {
    const wrapper = mountEditor({
      any_of: Array.from(
        {
          length: 50, // Requested fixture entry count.
        },
        (_, index) =>
          index === 0
            ? {
                all_of: Array.from(
                  {
                    length: 50, // Requested fixture entry count.
                  },
                  () => balanceGroup(1).all_of[0],
                ), // Jointly required conditions.
              }
            : balanceGroup(1),
      ), // Alternative eligibility groups.
    })
    expect(
      wrapper.get('[data-testid="announcement-groups-add"]').attributes('disabled'),
    ).toBeDefined()
    expect(
      wrapper.get('[data-testid="announcement-conditions-0-add"]').attributes('disabled'),
    ).toBeDefined()
    wrapper.unmount()
  })
})
