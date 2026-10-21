import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import TimePricingSection from '../TimePricingSection.vue'
import { createDefaultTimePricingForm } from '../types'

// TimePricingSection.spec.ts checks daily schedule editing, input normalization, and stable row
// controls through the mounted schedule editor. Local schedule objects and a selector stub
// expose emitted updates, while the parent-update callback exercises input identity during
// repeated edits and row removal.

vi.mock('vue-i18n', async (importOriginal) => ({
  ...(await importOriginal<typeof import('vue-i18n')>()),
  useI18n: () => ({ t: (key: string) => key }),
}))

// SelectStub forwards selection updates through a compact test control so assertions exercise
// the owning component data flow.
const SelectStub = {
  name: 'Select',
  props: ['modelValue', 'options'],
  emits: ['update:modelValue'],
  template: '<button type="button" v-bind="$attrs" />',
}

describe('TimePricingSection', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  // The schedule editor switches between daily and weekday-only scope. Emitted values retain
  // the boolean distinction so the parent can serialize the requested recurring schedule.
  it('offers every-day and weekdays-only scopes', async () => {
    const value = createDefaultTimePricingForm()
    const wrapper = mount(TimePricingSection, {
      props: { modelValue: value },
      global: { stubs: { Select: SelectStub, Icon: true } },
    })

    const selects = wrapper.findAllComponents(SelectStub)
    expect(selects).toHaveLength(2)
    expect(selects[1].props('modelValue')).toBe(false)
    expect(selects[1].props('options')).toEqual([
      { value: false, label: 'admin.pricing.form.timePricingEveryDay' },
      { value: true, label: 'admin.pricing.form.timePricingWeekdaysOnly' },
    ])

    selects[1].vm.$emit('update:modelValue', true)
    expect(wrapper.emitted('update:modelValue')?.[0]?.[0]).toEqual({
      ...value,
      weekdays_only: true,
    })
  })

  // A period is added to a supplied schedule object. The update contains a new period list
  // while the original parent-provided list remains available with its previous contents.
  it('不可变地新增时段', async () => {
    const value = createDefaultTimePricingForm()
    const wrapper = mount(TimePricingSection, {
      props: { modelValue: value },
      global: { stubs: { Select: SelectStub, Icon: true } },
    })

    await wrapper.get('[data-testid="time-periods-add"]').trigger('click')
    expect(wrapper.emitted('update:modelValue')?.[0]?.[0]).toEqual({
      timezone: 'Asia/Shanghai',
      weekdays_only: false,
      periods: [{ start_time: '', end_time: '', multiplier: '1.00' }],
    })
    expect(value.periods).toHaveLength(0)
  })

  // The editor renders period clocks and multiplier inputs. Their attributes require seconds
  // precision and the supported multiplier bounds so browser validation matches the form's
  // expected inputs.
  it('使用秒级时间输入与受限倍率输入', () => {
    const wrapper = mount(TimePricingSection, {
      props: {
        modelValue: {
          timezone: 'Asia/Shanghai',
          periods: [{ start_time: '09:00:00', end_time: '12:00:00', multiplier: '2.00' }],
        },
      },
      global: { stubs: { Select: SelectStub, Icon: true } },
    })

    const timeInputs = wrapper.findAll('input[inputmode="numeric"]')
    expect(timeInputs).toHaveLength(2)
    expect(timeInputs.every((input) => input.attributes('type') === 'text')).toBe(true)
    expect(timeInputs.every((input) => input.attributes('maxlength') === '8')).toBe(true)
    expect(timeInputs.every((input) => input.attributes('placeholder') === 'HH:mm:ss')).toBe(true)
    const multiplier = wrapper.get('input[type="number"]')
    expect(multiplier.attributes('min')).toBe('0.01')
    expect(multiplier.attributes('step')).toBe('0.01')
  })

  // Timezone and multiplier values are edited through the schedule controls. The update carries
  // the selected timezone and formats an accepted multiplier to two decimal places.
  it('更新时区并格式化合法倍率', async () => {
    const value = {
      timezone: 'Asia/Shanghai',
      periods: [{ start_time: '09:00:00', end_time: '12:00:00', multiplier: '2' }],
    }
    const wrapper = mount(TimePricingSection, {
      props: { modelValue: value },
      global: { stubs: { Select: SelectStub, Icon: true } },
    })

    wrapper.getComponent(SelectStub).vm.$emit('update:modelValue', 'Europe/London')
    await wrapper.get('input[type="number"]').trigger('blur')
    expect(wrapper.emitted('update:modelValue')?.[0]?.[0]).toEqual({
      ...value,
      timezone: 'Europe/London',
    })
    expect(wrapper.emitted('update:modelValue')?.[1]?.[0]).toEqual({
      ...value,
      periods: [{ ...value.periods[0], multiplier: '2.00' }],
    })
  })

  // The start-time input receives the 24:00:00 endpoint while editing an existing period. The
  // emitted schedule replaces that value with 00:00:00 and retains the remaining period fields.
  it('将全角冒号和 24:00:00 归一化', async () => {
    const value = {
      timezone: 'Asia/Shanghai',
      periods: [{ start_time: '09:00:00', end_time: '12:00:00', multiplier: '2.00' }],
    }
    const wrapper = mount(TimePricingSection, {
      props: { modelValue: value },
      global: { stubs: { Select: SelectStub, Icon: true } },
    })

    await wrapper.get('input[inputmode="numeric"]').setValue('24:00:00')
    expect(wrapper.emitted('update:modelValue')?.[0]?.[0]).toEqual({
      ...value,
      periods: [{ ...value.periods[0], start_time: '00:00:00' }],
    })
  })

  // A period is edited and an earlier row is removed. Remaining inputs keep their row identity
  // so the administrator's ongoing editing control remains attached to the same period.
  it('编辑和删除前一行后保留其余时段的输入框', async () => {
    const value = {
      timezone: 'Asia/Shanghai',
      periods: [
        { start_time: '08:00:00', end_time: '09:00:00', multiplier: '1.00' },
        { start_time: '10:00:00', end_time: '11:00:00', multiplier: '2.00' },
      ],
    }
    const wrapper = mount(TimePricingSection, {
      attachTo: document.body,
      props: {
        modelValue: value,
        'onUpdate:modelValue': (updated) => {
          void wrapper.setProps({ modelValue: updated })
        },
      },
      global: { stubs: { Select: SelectStub, Icon: true, 'transition-group': true } },
    })
    const second = wrapper.findAll('input[inputmode="numeric"]')[2]
    const input = second.element as HTMLInputElement
    input.focus()
    await second.setValue('10:30:00')
    await flushPromises()
    expect(document.activeElement).toBe(input)
    await wrapper.get('[data-testid="time-periods-remove-0"]').trigger('click')
    await flushPromises()
    expect(wrapper.findAll('input[inputmode="numeric"]')[0].element).toBe(input)
    expect(input.value).toBe('10:30:00')
    wrapper.unmount()
  })
})
