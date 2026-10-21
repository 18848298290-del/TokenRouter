import { mount } from '@vue/test-utils'
import { defineComponent, nextTick } from 'vue'
import { describe, expect, it, vi } from 'vitest'

import GroupAdvancedSchedulerOverridesModal from '../GroupAdvancedSchedulerOverridesModal.vue'

// GroupAdvancedSchedulerOverridesModal.spec.ts checks scheduler override hydration and save
// events through dialog and select stubs. Sparse values retain explicit false and numeric
// overrides, while the reset action produces complete inheritance in the emitted settings
// object.

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => key,
  }),
}))

// BaseDialogStub and adjacent control stubs expose test-visible slots and events while keeping
// dialog layout outside the tested page behavior.
const BaseDialogStub = defineComponent({
  name: 'BaseDialog',
  props: {
    show: Boolean,
    title: String,
    width: String,
    zIndex: Number,
  },
  emits: ['close'],
  template: '<section v-if="show" :data-z-index="zIndex"><slot /><slot name="footer" /></section>',
})

// SelectStub forwards selection updates through a compact test control so assertions exercise
// the owning component data flow.
const SelectStub = defineComponent({
  name: 'SelectStub',
  props: ['modelValue', 'options'],
  emits: ['update:modelValue'],
  template: '<button type="button" class="select-stub">{{ modelValue }}</button>',
})

// mountModal mounts scheduler editing with dialog and selector stubs. The supplied override
// object defaults to empty, and emitted save values remain available for assertions against the
// parent-facing payload.
function mountModal(modelValue = {}) {
  return mount(GroupAdvancedSchedulerOverridesModal, {
    props: {
      show: true,
      modelValue,
    },
    global: {
      stubs: {
        BaseDialog: BaseDialogStub,
        Select: SelectStub,
        Icon: true,
      },
    },
  })
}

describe('GroupAdvancedSchedulerOverridesModal', () => {
  // A sparse override object contains explicit boolean and numeric values. Hydration displays
  // those values and saving emits configured overrides while leaving inherited fields absent.
  it('hydrates sparse values and emits only explicit overrides', async () => {
    const wrapper = mountModal({
      sticky_weighted_enabled: false,
      lb_top_k: 3,
      weight_queue: 0,
    })
    await nextTick()

    const inputs = wrapper.findAll('input')
    expect(inputs).toHaveLength(14)
    expect((inputs[4].element as HTMLInputElement).value).toBe('3')
    expect((inputs[7].element as HTMLInputElement).value).toBe('0')
    expect(wrapper.get('[data-z-index="60"]').exists()).toBe(true)

    await inputs[4].setValue('4')
    await wrapper.get('form').trigger('submit')

    expect(wrapper.emitted('save')).toEqual([
      [
        {
          sticky_weighted_enabled: false,
          lb_top_k: 4,
          weight_queue: 0,
        },
      ],
    ])
  })

  // The reset action is used after scheduler overrides were loaded. Saving afterward emits an
  // empty object so the parent can restore inheritance for every setting.
  it('restores complete inheritance from the compact reset action', async () => {
    const wrapper = mountModal({
      subscription_priority_enabled: true,
      lb_top_k: 3,
      weight_priority: 2,
    })
    await nextTick()

    await wrapper.get('[data-test="advanced-scheduler-overrides-reset"]').trigger('click')
    await wrapper.get('form').trigger('submit')

    expect(wrapper.emitted('save')).toEqual([[{}]])
  })
})
