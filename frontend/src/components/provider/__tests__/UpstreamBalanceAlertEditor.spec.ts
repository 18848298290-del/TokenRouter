import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import UpstreamBalanceAlertEditor from '../UpstreamBalanceAlertEditor.vue'
import type { UpstreamBalanceAlertConfig } from '@/types'
import {
  buildUpstreamBalanceAlert,
  defaultUpstreamBalanceAlert,
  readUpstreamBalanceAlert,
  sameUpstreamBalanceAlert,
  supportsUpstreamBalanceAlert
} from '@/utils/upstreamBalanceAlert'

const { getStatusMock } = vi.hoisted(() => ({ getStatusMock: vi.fn() }))

vi.mock('@/api/admin/providers', () => ({
  getUpstreamBalanceAlertStatus: getStatusMock
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})

// UpstreamBalanceAlertEditor.spec.ts covers the reminder policy helpers and the
// editor component. The component cases check that edits replace the parent's
// policy object, that status reads happen only for saved providers, and that a
// late response for an earlier provider cannot appear under the current one.

/** enabledPolicy returns an enabled reminder fixture with a valid recipient address. */
function enabledPolicy(): UpstreamBalanceAlertConfig {
  return { ...defaultUpstreamBalanceAlert(), enabled: true, recipient_email: 'ops@example.com' }
}

/** mountEditor mounts the reminder editor with defaults and caller-supplied props. */
function mountEditor(props: Record<string, unknown> = {}) {
  return mount(UpstreamBalanceAlertEditor, {
    props: {
      modelValue: enabledPolicy(),
      supported: true,
      queryEnabled: true,
      ...props
    },
    global: { stubs: { Icon: true } }
  })
}

describe('upstreamBalanceAlert helpers', () => {
  // Saved Extra values are read field by field. Wrong types and out-of-range
  // numbers fall back to defaults, so the form never starts from a rejected value.
  it('reads saved policies and replaces malformed fields with defaults', () => {
    const saved = { enabled: true, threshold: 500, interval_minutes: 5, recipient_email: 'a@example.com' }
    expect(readUpstreamBalanceAlert({ upstream_balance_alert: saved })).toEqual(saved)
    expect(readUpstreamBalanceAlert({ upstream_balance_alert: saved })).not.toBe(saved)
    expect(readUpstreamBalanceAlert(undefined)).toEqual(defaultUpstreamBalanceAlert())
    expect(readUpstreamBalanceAlert({
      upstream_balance_alert: { enabled: 'yes', threshold: -1, interval_minutes: 4, recipient_email: 7 }
    })).toEqual(defaultUpstreamBalanceAlert())
  })

  // Eligibility follows the backend adapter rules: generic API-key adapters and
  // Kimi or DeepSeek pay-as-you-go have wallet balances; the rest do not.
  it('limits reminders to wallet-balance adapters', () => {
    expect(supportsUpstreamBalanceAlert('apikey', 'openai', 'payg')).toBe(true)
    expect(supportsUpstreamBalanceAlert('apikey', 'anthropic', 'coding')).toBe(true)
    expect(supportsUpstreamBalanceAlert('apikey', 'deepseek', 'payg')).toBe(true)
    expect(supportsUpstreamBalanceAlert('apikey', 'kimi', 'coding')).toBe(false)
    expect(supportsUpstreamBalanceAlert('apikey', 'zhipu', 'payg')).toBe(false)
    expect(supportsUpstreamBalanceAlert('oauth', 'openai', 'payg')).toBe(false)
  })

  // The request payload turns unsupported reminders off, trims the recipient,
  // and swaps invalid numbers for defaults so a disabled policy still validates.
  it('builds request payloads the backend accepts', () => {
    const edited = { enabled: true, threshold: Number.NaN, interval_minutes: 2, recipient_email: ' ops@example.com ' }
    expect(buildUpstreamBalanceAlert(edited, false)).toEqual({
      enabled: false,
      threshold: 1000,
      interval_minutes: 60,
      recipient_email: 'ops@example.com'
    })
    expect(sameUpstreamBalanceAlert(buildUpstreamBalanceAlert(enabledPolicy(), true), enabledPolicy())).toBe(true)
  })
})

describe('UpstreamBalanceAlertEditor', () => {
  beforeEach(() => {
    getStatusMock.mockReset()
  })

  // Each edit emits a replacement object with the other fields kept, and the
  // native constraints reject intervals and addresses the backend would refuse.
  it('emits field edits and applies native validation', async () => {
    const wrapper = mountEditor()
    await wrapper.get('[data-testid="balance-alert-threshold"]').setValue('999')
    expect(wrapper.emitted('update:modelValue')?.at(-1)?.[0]).toEqual({ ...enabledPolicy(), threshold: 999 })

    const interval = wrapper.get<HTMLInputElement>('[data-testid="balance-alert-interval"]')
    await interval.setValue('4')
    expect(interval.element.validity.valid).toBe(false)

    const email = wrapper.get<HTMLInputElement>('[data-testid="balance-alert-email"]')
    await email.setValue('invalid')
    expect(email.element.validity.valid).toBe(false)
  })

  // Unsupported providers show the switch off and disabled even when a saved
  // policy is enabled, and the dependent fields stay hidden.
  it('disables the switch for unsupported providers', () => {
    const wrapper = mountEditor({ supported: false })
    const toggle = wrapper.get('[data-testid="balance-alert-enabled"]')
    expect(toggle.attributes('aria-checked')).toBe('false')
    expect(toggle.attributes('disabled')).toBeDefined()
    expect(wrapper.find('[data-testid="balance-alert-threshold"]').exists()).toBe(false)
  })

  // A new provider has no stored checks, so the status area and its request are skipped.
  it('skips the status read while creating a provider', async () => {
    const wrapper = mountEditor()
    await flushPromises()
    expect(getStatusMock).not.toHaveBeenCalled()
    expect(wrapper.find('[data-testid="balance-alert-status"]').exists()).toBe(false)
  })

  // Switching providers while the first read is pending must drop that response.
  // The second provider's status, including its translated error, stays shown.
  it('shows the latest status and drops responses for an earlier provider', async () => {
    let resolveFirst!: (value: unknown) => void
    getStatusMock.mockImplementationOnce(() => new Promise(resolve => { resolveFirst = resolve }))
    getStatusMock.mockResolvedValueOnce({
      provider_id: 2,
      round: 3,
      last_checked_at: '2026-10-16T00:00:00Z',
      balance: 999,
      unit: 'CNY',
      error_code: 'UPSTREAM_BALANCE_ALERT_EMAIL_FAILED'
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
    expect(wrapper.get('[data-testid="balance-alert-error"]').text())
      .toBe('admin.providers.balanceAlert.errors.UPSTREAM_BALANCE_ALERT_EMAIL_FAILED')
  })

  // A status with only provider_id means no check matches the saved settings.
  // A failed read shows an error, and the refresh button sends a new request.
  // A disabled replacement provider does not start a new request, so switching
  // identity must invalidate the previous response before the reminder is reopened.
  it('discards a pending response when switching to a disabled provider', async () => {
    let resolveFirst!: (value: unknown) => void
    let resolveSecond!: (value: unknown) => void
    getStatusMock.mockImplementationOnce(() => new Promise(resolve => { resolveFirst = resolve }))
    getStatusMock.mockImplementationOnce(() => new Promise(resolve => { resolveSecond = resolve }))
    const wrapper = mountEditor({ providerId: 1 })
    await wrapper.setProps({ providerId: 2, modelValue: { ...enabledPolicy(), enabled: false } })
    resolveFirst({ provider_id: 1, round: 1, balance: 123, unit: 'CNY' })
    await flushPromises()
    await wrapper.setProps({ modelValue: enabledPolicy() })
    await flushPromises()
    expect(wrapper.text()).not.toContain('123 CNY')
    expect(getStatusMock).toHaveBeenNthCalledWith(2, 2)
    expect(wrapper.get('[data-testid="balance-alert-refresh"]').attributes('disabled')).toBeDefined()
    resolveSecond({ provider_id: 2, round: 1, balance: 456, unit: 'CNY' })
    await flushPromises()
    expect(wrapper.text()).toContain('456 CNY')
    wrapper.unmount()
  })

  // Closing a reminder panel invalidates its pending failure as well as success.
  // Reopening begins with a loading state until the replacement read resolves.
  it('discards a pending failure after the reminder closes', async () => {
    let rejectFirst!: (reason: Error) => void
    let resolveSecond!: (value: unknown) => void
    getStatusMock.mockImplementationOnce(() => new Promise((_resolve, reject) => { rejectFirst = reject }))
    getStatusMock.mockImplementationOnce(() => new Promise(resolve => { resolveSecond = resolve }))
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
