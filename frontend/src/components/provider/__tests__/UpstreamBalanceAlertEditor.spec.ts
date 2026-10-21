import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import UpstreamBalanceAlertEditor from '../UpstreamBalanceAlertEditor.vue'
import type { UpstreamBalanceAlertConfig } from '@/api/admin/providers.types'
import {
  buildUpstreamBalanceAlert,
  defaultUpstreamBalanceAlert,
  readUpstreamBalanceAlert,
  sameUpstreamBalanceAlert,
  supportsUpstreamBalanceAlert,
} from '@/utils/upstreamBalanceAlert'

// UpstreamBalanceAlertEditor.spec.ts tests balance-reminder configuration normalization
// and saved-check display. Deferred responses reproduce provider changes and disabled
// policies, while input interactions check replacement models, browser validation, and
// retry behavior after a failed status read.

// getStatusMock controls saved-check responses so tests can resolve or reject
// individual reads after changing the provider or active reminder state.
const { getStatusMock } = vi.hoisted(() => ({ getStatusMock: vi.fn() }))

vi.mock('@/api/admin/providers', () => ({
  getUpstreamBalanceAlertStatus: getStatusMock,
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  }
})

// enabledPolicy creates an enabled reminder policy with shared defaults and a valid
// recipient address. Each call returns a fresh outer object for payload comparisons and
// editor props.
function enabledPolicy(): UpstreamBalanceAlertConfig {
  return { ...defaultUpstreamBalanceAlert(), enabled: true, recipient_email: 'ops@example.com' }
}

// mountEditor mounts a supported reminder editor with an enabled policy and caller prop
// overrides. Tests can supply a saved provider identity or disable support while
// controlling status reads through the mock.
function mountEditor(props: Record<string, unknown> = {}) {
  return mount(UpstreamBalanceAlertEditor, {
    props: {
      modelValue: enabledPolicy(),
      supported: true,
      queryEnabled: true,
      ...props,
    },
    global: { stubs: { Icon: true } },
  })
}

describe('upstreamBalanceAlert helpers', () => {
  // Saved reminder data includes valid fields alongside malformed configuration values.
  // Parsing must retain accepted values and substitute the documented defaults for fields
  // that cannot be used.
  it('reads saved policies and replaces malformed fields with defaults', () => {
    const saved = {
      enabled: true,
      threshold: 500,
      interval_minutes: 5,
      recipient_email: 'a@example.com',
    }
    expect(readUpstreamBalanceAlert({ upstream_balance_alert: saved })).toEqual(saved)
    expect(readUpstreamBalanceAlert({ upstream_balance_alert: saved })).not.toBe(saved)
    expect(readUpstreamBalanceAlert(undefined)).toEqual(defaultUpstreamBalanceAlert())
    expect(
      readUpstreamBalanceAlert({
        upstream_balance_alert: {
          enabled: 'yes',
          threshold: -1,
          interval_minutes: 4,
          recipient_email: 7,
        },
      }),
    ).toEqual(defaultUpstreamBalanceAlert())
  })

  // Provider usage adapters expose different kinds of balance information to the form.
  // Reminder support must be enabled only for the adapters that return a wallet balance.
  it('limits reminders to wallet-balance adapters', () => {
    expect(supportsUpstreamBalanceAlert('apikey', 'openai', 'payg')).toBe(true)
    expect(supportsUpstreamBalanceAlert('apikey', 'anthropic', 'coding')).toBe(true)
    expect(supportsUpstreamBalanceAlert('apikey', 'deepseek', 'payg')).toBe(true)
    expect(supportsUpstreamBalanceAlert('apikey', 'kimi', 'coding')).toBe(false)
    expect(supportsUpstreamBalanceAlert('apikey', 'zhipu', 'payg')).toBe(false)
    expect(supportsUpstreamBalanceAlert('oauth', 'openai', 'payg')).toBe(false)
  })

  // Reminder drafts are converted into configuration sent with provider Extra updates.
  // The builder must produce accepted normalized fields and replace out-of-range fields with
  // supported defaults before the payload is sent.
  it('builds request payloads the backend accepts', () => {
    const edited = {
      enabled: true,
      threshold: Number.NaN,
      interval_minutes: 2,
      recipient_email: ' ops@example.com ',
    }
    expect(buildUpstreamBalanceAlert(edited, false)).toEqual({
      enabled: false,
      threshold: 1000,
      interval_minutes: 60,
      recipient_email: 'ops@example.com',
    })
    expect(
      sameUpstreamBalanceAlert(buildUpstreamBalanceAlert(enabledPolicy(), true), enabledPolicy()),
    ).toBe(true)
  })
})

describe('UpstreamBalanceAlertEditor', () => {
  beforeEach(() => {
    getStatusMock.mockReset()
  })

  // The editor changes threshold, interval, and recipient fields through its visible
  // controls. Model updates must carry the edited values and retain browser validation
  // attributes on those inputs.
  it('emits field edits and applies native validation', async () => {
    const wrapper = mountEditor()
    await wrapper.get('[data-testid="balance-alert-threshold"]').setValue('999')
    expect(wrapper.emitted('update:modelValue')?.at(-1)?.[0]).toEqual({
      ...enabledPolicy(),
      threshold: 999,
    })

    const interval = wrapper.get<HTMLInputElement>('[data-testid="balance-alert-interval"]')
    await interval.setValue('4')
    expect(interval.element.validity.valid).toBe(false)

    const email = wrapper.get<HTMLInputElement>('[data-testid="balance-alert-email"]')
    await email.setValue('invalid')
    expect(email.element.validity.valid).toBe(false)
  })

  // An enabled reminder policy is supplied for a provider lacking wallet balance support.
  // Its switch must display disabled without overwriting the policy object owned by the
  // caller.
  it('disables the switch for unsupported providers', () => {
    const wrapper = mountEditor({ supported: false })
    const toggle = wrapper.get('[data-testid="balance-alert-enabled"]')
    expect(toggle.attributes('aria-checked')).toBe('false')
    expect(toggle.attributes('disabled')).toBeDefined()
    expect(wrapper.find('[data-testid="balance-alert-threshold"]').exists()).toBe(false)
  })

  // A create form has no saved provider identifier for its reminder policy. The editor
  // must avoid the status endpoint and still allow configuration through its editable
  // policy fields.
  it('skips the status read while creating a provider', async () => {
    const wrapper = mountEditor()
    await flushPromises()
    expect(getStatusMock).not.toHaveBeenCalled()
    expect(wrapper.find('[data-testid="balance-alert-status"]').exists()).toBe(false)
  })

  // A status request remains pending while the editor switches to another provider
  // identity. Only the latest provider response may appear in the status display after
  // both requests complete.
  it('shows the latest status and drops responses for an earlier provider', async () => {
    let resolveFirst!: (value: unknown) => void
    getStatusMock.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          resolveFirst = resolve
        }),
    )
    getStatusMock.mockResolvedValueOnce({
      provider_id: 2,
      round: 3,
      last_checked_at: '2026-10-16T00:00:00Z',
      balance: 999,
      unit: 'CNY',
      error_code: 'UPSTREAM_BALANCE_ALERT_EMAIL_FAILED',
    })
    const wrapper = mountEditor({ providerId: 1 })
    await wrapper.setProps({ providerId: 2 })
    await flushPromises()
    resolveFirst({ provider_id: 1, round: 1, balance: 123 })
    await flushPromises()

    expect(getStatusMock).toHaveBeenNthCalledWith(1, 1)
    expect(getStatusMock).toHaveBeenNthCalledWith(2, 2)
    expect(wrapper.text()).toContain('999 CNY')
    expect(wrapper.text()).not.toContain('123')
    expect(wrapper.get('[data-testid="balance-alert-error"]').text()).toBe(
      'admin.providers.balanceAlert.errors.UPSTREAM_BALANCE_ALERT_EMAIL_FAILED',
    )
  })

  // A status request remains pending when the next provider has an inactive reminder. The
  // earlier result must be discarded even though no replacement status read is started.
  it('discards a pending response when switching to a disabled provider', async () => {
    let resolveFirst!: (value: unknown) => void
    let resolveSecond!: (value: unknown) => void
    getStatusMock.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          resolveFirst = resolve
        }),
    )
    getStatusMock.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          resolveSecond = resolve
        }),
    )
    const wrapper = mountEditor({ providerId: 1 })
    await wrapper.setProps({ providerId: 2, modelValue: { ...enabledPolicy(), enabled: false } })
    resolveFirst({ provider_id: 1, round: 1, balance: 123, unit: 'CNY' })
    await flushPromises()
    await wrapper.setProps({ modelValue: enabledPolicy() })
    await flushPromises()
    expect(wrapper.text()).not.toContain('123 CNY')
    expect(getStatusMock).toHaveBeenNthCalledWith(2, 2)
    expect(
      wrapper.get('[data-testid="balance-alert-refresh"]').attributes('disabled'),
    ).toBeDefined()
    resolveSecond({ provider_id: 2, round: 1, balance: 456, unit: 'CNY' })
    await flushPromises()
    expect(wrapper.text()).toContain('456 CNY')
    wrapper.unmount()
  })

  // A pending status request fails after the reminder panel is disabled by its caller.
  // The invalidated failure must not appear in the current status area or restore
  // loading.
  it('discards a pending failure after the reminder closes', async () => {
    let rejectFirst!: (reason: Error) => void
    let resolveSecond!: (value: unknown) => void
    getStatusMock.mockImplementationOnce(
      () =>
        new Promise((_resolve, reject) => {
          rejectFirst = reject
        }),
    )
    getStatusMock.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          resolveSecond = resolve
        }),
    )
    const wrapper = mountEditor({ providerId: 1 })
    await wrapper.setProps({ modelValue: { ...enabledPolicy(), enabled: false } })
    rejectFirst(new Error('late failure'))
    await flushPromises()
    await wrapper.setProps({ modelValue: enabledPolicy() })
    await flushPromises()
    expect(wrapper.text()).not.toContain('admin.providers.balanceAlert.loadFailed')
    resolveSecond({ provider_id: 1 })
    await flushPromises()
    expect(wrapper.text()).toContain('admin.providers.balanceAlert.notChecked')
    wrapper.unmount()
  })

  // The initial latest-check request fails, and the retry returns a provider with
  // no recorded check. The editor shows the read failure first, then displays the
  // no-check state after its retry action succeeds.
  it('reports missing checks and retries failed reads', async () => {
    getStatusMock.mockRejectedValueOnce(new Error('offline'))
    getStatusMock.mockResolvedValueOnce({ provider_id: 1 })
    const wrapper = mountEditor({ providerId: 1 })
    await flushPromises()
    expect(wrapper.text()).toContain('admin.providers.balanceAlert.loadFailed')

    await wrapper.get('[data-testid="balance-alert-refresh"]').trigger('click')
    await flushPromises()
    expect(getStatusMock).toHaveBeenCalledTimes(2)
    expect(wrapper.text()).toContain('admin.providers.balanceAlert.notChecked')
  })
})
