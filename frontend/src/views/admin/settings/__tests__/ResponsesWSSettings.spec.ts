import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import ResponsesWSSettings from '../ResponsesWSSettings.vue'
import {
  createSettingsSaveRegistry,
  settingsSaveRegistryKey,
} from '@/composables/useSettingsSaveRegistry'

/**
 * ResponsesWSSettings.spec.ts mounts the WebSocket editor with the page save
 * registry to check override patches, default restoration, and failed writes.
 * Assertions inspect submitted API payloads and dirty state together so the
 * tests protect what the shared save action observes.
 */

// Hoisted API substitutes expose initial hydration and subsequent saves while leaving the mounted save registry active.
const { getSettings, updateSettings } = vi.hoisted(() => ({
  getSettings: vi.fn(),
  updateSettings: vi.fn(),
}))
vi.mock('@/api/admin/settings', () => ({ getSettings, updateSettings }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

// The fixture supplies one stored timeout override and effective defaults for byte conversion and connection controls.
const initial = {
  responses_ws: { read_timeout_seconds: 90 },
  responses_ws_effective: {
    read_timeout_seconds: 90,
    client_read_limit_bytes: 67108864,
    max_conns_per_provider: 128,
  },
}
/**
 * setup mounts the editor with a real settings save registry and resolves its
 * initial read. It returns both the rendered controls and registry so each case
 * can edit the form and observe the same save operation used by the page.
 */
async function setup() {
  const registry = createSettingsSaveRegistry()
  const wrapper = mount(ResponsesWSSettings, {
    global: {
      provide: { [settingsSaveRegistryKey as symbol]: registry.registry },
      stubs: {
        SettingsCard: { template: '<div><slot name="actions"/><slot/></div>' },
        SettingsSection: { template: '<section><slot/></section>' },
        SettingsNotice: { template: '<div><slot/></div>' },
        SettingToggleRow: true,
      },
    },
  })
  await flushPromises()
  return { wrapper, registry }
}

beforeEach(() => {
  vi.clearAllMocks()
  getSettings.mockResolvedValue(initial)
  updateSettings.mockResolvedValue(initial)
})

describe('Responses WS online settings', () => {
  // An administrator changes a byte-limit input displayed in MiB after initial hydration.
  // Saving submits the converted field alone and marks the refreshed editor clean.
  it('submits only edited fields and converts MiB to bytes', async () => {
    const { wrapper, registry } = await setup()
    expect(registry.dirty.value).toBe(false)
    await wrapper.get('#responses-ws-input-client_read_limit_bytes').setValue('32')
    expect(registry.dirty.value).toBe(true)
    expect(await registry.saveDirty()).toBe(true)
    expect(updateSettings).toHaveBeenCalledWith({
      responses_ws: { client_read_limit_bytes: 33554432 },
    })
    expect(registry.dirty.value).toBe(false)
  })
  // An administrator changes a saved timeout and then restores its original value. The
  // registry becomes clean and does not submit the reverted local patch.
  it('does not save when an edit is reverted', async () => {
    const { wrapper, registry } = await setup()
    await wrapper.get('#responses-ws-input-read_timeout_seconds').setValue('100')
    expect(registry.dirty.value).toBe(true)
    await wrapper.get('#responses-ws-input-read_timeout_seconds').setValue('90')
    expect(registry.dirty.value).toBe(false)
    await registry.saveDirty()
    expect(updateSettings).not.toHaveBeenCalled()
  })
  // An administrator empties an input that currently overrides a deployment setting.
  // Saving sends a null field so the server can remove that individual override.
  it('clears a single override with null', async () => {
    const { wrapper, registry } = await setup()
    await wrapper.get('#responses-ws-input-read_timeout_seconds').setValue('')
    await registry.saveDirty()
    expect(updateSettings).toHaveBeenCalledWith({ responses_ws: { read_timeout_seconds: null } })
  })
  // An administrator chooses the action to restore all deployment defaults. The registered
  // save submits a null override object to distinguish whole-object removal from a field
  // patch.
  it('restores all deployment defaults with a null object', async () => {
    const { wrapper, registry } = await setup()
    await wrapper.get('button').trigger('click')
    await registry.saveDirty()
    expect(updateSettings).toHaveBeenCalledWith({ responses_ws: null })
  })
  // The server reports that persistence succeeded but runtime application failed after an
  // edit. The save remains unsuccessful, displays the apply error, and keeps the draft
  // dirty.
  it('keeps edits when persisted settings could not be applied', async () => {
    const { wrapper, registry } = await setup()
    updateSettings.mockRejectedValue({
      status: 500,
      code: 500,
      reason: 'SETTINGS_APPLY_FAILED',
      metadata: { persisted: true },
    })
    await wrapper.get('#responses-ws-input-read_timeout_seconds').setValue('100')
    expect(await registry.saveDirty()).toBe(false)
    expect(registry.dirty.value).toBe(true)
    expect(wrapper.text()).toContain('admin.settings.responsesWS.applyError')
  })
  // The API rejects a submitted parameter combination with a structured validation
  // message. The editor displays that exact message and retains its dirty state for
  // correction.
  it('shows the structured validation message returned by the API client', async () => {
    const { wrapper, registry } = await setup()
    updateSettings.mockRejectedValue({
      status: 400,
      reason: 'INVALID_RESPONSES_WS_SETTINGS',
      message: '连接数量组合无效',
    })
    await wrapper.get('#responses-ws-input-read_timeout_seconds').setValue('100')
    expect(await registry.saveDirty()).toBe(false)
    expect(wrapper.text()).toContain('连接数量组合无效')
    expect(registry.dirty.value).toBe(true)
  })
  // The administrator sets both minimum and maximum idle connection counts to zero. The
  // rendered bounds allow zero and the saved patch carries both values.
  it('allows zero idle connections in the form and payload', async () => {
    const { wrapper, registry } = await setup()
    expect(wrapper.get('#responses-ws-input-max_idle_per_provider').attributes('min')).toBe('0')
    await wrapper.get('#responses-ws-input-min_idle_per_provider').setValue('0')
    await wrapper.get('#responses-ws-input-max_idle_per_provider').setValue('0')
    expect(await registry.saveDirty()).toBe(true)
    expect(updateSettings).toHaveBeenCalledWith({
      responses_ws: { min_idle_per_provider: 0, max_idle_per_provider: 0 },
    })
  })
  // An overridden field displays its reset button while a defaulted field has none.
  // Activating reset empties that input and saves a null removal for its key.
  it('clears one override with the field reset button', async () => {
    const { wrapper, registry } = await setup()
    expect(wrapper.find('[data-testid="responses-ws-reset-max_conns_per_provider"]').exists()).toBe(
      false,
    )
    await wrapper.get('[data-testid="responses-ws-reset-read_timeout_seconds"]').trigger('click')
    expect(
      wrapper.get<HTMLInputElement>('#responses-ws-input-read_timeout_seconds').element.value,
    ).toBe('')
    await registry.saveDirty()
    expect(updateSettings).toHaveBeenCalledWith({ responses_ws: { read_timeout_seconds: null } })
  })
  // The server returns effective defaults and an empty override object. The restore action
  // stays disabled while the connection input displays its effective value as a
  // placeholder.
  it('disables restoring defaults when nothing is overridden', async () => {
    getSettings.mockResolvedValue({ ...initial, responses_ws: {} })
    const { wrapper } = await setup()
    expect(wrapper.get('button').attributes('disabled')).toBeDefined()
    expect(
      wrapper.get<HTMLInputElement>('#responses-ws-input-max_conns_per_provider').element
        .placeholder,
    ).toBe('128')
  })
})
