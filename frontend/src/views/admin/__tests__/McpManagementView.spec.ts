import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import McpManagementView from '../McpManagementView.vue'

// McpManagementView.spec.ts exercises permission selection and token creation through
// the page's actual form events. API replacements distinguish failed writes from
// successful creation followed by failed readback, protecting both retained drafts
// and immediate credential delivery from unrelated list refresh failures.

// Shared API replacements allow each case to select its write and readback outcomes.
const api = vi.hoisted(() => ({
  listTokens: vi.fn(),
  listOperations: vi.fn(),
  createToken: vi.fn(),
  updateToken: vi.fn(),
  revokeToken: vi.fn(),
  getCredential: vi.fn(),
  getConfirmation: vi.fn(),
  approveConfirmation: vi.fn(),
  downloadFile: vi.fn(),
}))
vi.mock('@/api/admin/mcp', () => api)
vi.mock('vue-router', async (importOriginal) => ({
  ...(await importOriginal<typeof import('vue-router')>()),
  useRoute: () => ({ query: {} }),
  RouterLink: { name: 'RouterLink', props: ['to'], template: '<a><slot /></a>' },
}))
vi.mock('vue-i18n', async (importOriginal) => ({
  ...(await importOriginal<typeof import('vue-i18n')>()),
  useI18n: () => ({ t: (key: string) => key, te: () => false, locale: { value: 'en' } }),
}))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showSuccess: vi.fn() }) }))
// Clipboard replacement records the exact text copied by an explicit row action.
const copyToClipboard = vi.hoisted(() => vi.fn())
vi.mock('@/composables/useClipboard', () => ({
  useClipboard: () => ({ copyToClipboard }),
}))
vi.mock('@/composables/useStepUp', () => ({
  useStepUp: () => ({ run: (action: () => Promise<void>) => action(), onCancel: vi.fn() }),
  isStepUpCancelled: () => false,
}))

// catalogue mixes routine queries, gateway writes, and sensitive actions for preset assertions.
const catalogue = [
  {
    operation_id: 'groups.list',
    module: 'groups',
    title: 'List groups',
    read_only: true,
    sensitive: false,
  },
  {
    operation_id: 'users.create',
    module: 'users',
    title: 'Create user',
    read_only: false,
    sensitive: true,
  },
  {
    operation_id: 'providers.create',
    module: 'providers',
    title: 'Create provider',
    read_only: false,
    sensitive: false,
  },
  {
    operation_id: 'providers.export',
    module: 'providers',
    title: 'Export credentials',
    read_only: true,
    sensitive: true,
    response_mode: 'file',
  },
  {
    operation_id: 'mcp.tokens.create',
    module: 'mcp',
    title: 'Delegate permissions',
    read_only: false,
    sensitive: true,
  },
]

// page mounts form-bearing slots while replacing unrelated shell and overlay machinery.
function page() {
  return mount(McpManagementView, {
    global: {
      stubs: {
        AppLayout: { template: '<main><slot /></main>' },
        TablePageLayout: {
          template: '<div><slot name="actions"/><slot name="filters"/><slot name="table"/></div>',
        },
        DataTable: {
          props: ['data'],
          template:
            '<div><template v-for="row in data"><slot name="cell-name" :row="row"/><slot name="cell-prefix" :row="row" :value="row.prefix"/><slot name="cell-enabled" :row="row"/><slot name="cell-actions" :row="row"/></template><slot v-if="!data?.length" name="empty"/></div>',
        },
        BaseDialog: {
          props: ['show', 'title'],
          template:
            '<section v-if="show"><h2>{{ title }}</h2><slot/><slot name="footer"/></section>',
        },
        Teleport: true,
        MotionTransition: { template: '<slot />' },
        ConfirmDialog: true,
        TotpStepUpDialog: true,
        Icon: true,
        DateTimePicker: true,
        Select: true,
        Toggle: true,
        EndpointPopover: true,
      },
    },
  })
}

// clickText activates the uniquely labelled page button used by the scenario.
async function clickText(wrapper: VueWrapper, label: string) {
  const button = wrapper.findAll('button').find((value) => value.text() === label)
  expect(button).toBeDefined()
  await button!.trigger('click')
}

describe('MCP token management', () => {
  beforeEach(() => {
    vi.resetAllMocks()
    copyToClipboard.mockResolvedValue(true)
    api.listTokens.mockResolvedValue([])
    api.listOperations.mockResolvedValue(catalogue)
  })

  // A selected grant becomes hidden by another search before submission. The saved
  // token must retain that exact operation without granting the newly visible action.
  it('retains selected permissions across filtering and submits exact grants', async () => {
    api.createToken.mockResolvedValue({ credential: 'tr_mcp_fixture_only', token: { id: 1 } })
    const wrapper = page()
    await flushPromises()
    await clickText(wrapper, 'admin.mcp.create')
    await wrapper.get('input[required]').setValue('Catalogue viewer')
    const label = wrapper.findAll('label').find((value) => value.text().includes('List groups'))
    expect(label).toBeDefined()
    await label!.get('input[type="checkbox"]').setValue(true)
    await wrapper.get('input[aria-label="admin.mcp.permissionSearch"]').setValue('users')
    await wrapper.get('#mcp-token-form').trigger('submit')
    await flushPromises()
    expect(api.createToken).toHaveBeenCalledWith(
      expect.objectContaining({
        name: 'Catalogue viewer',
        enabled: true,
        skip_browser_confirmation: false,
        permissions: ['groups.list'],
      }),
    )
    expect(wrapper.text()).toContain('tr_mcp_fixture_only')
    wrapper.unmount()
  })

  // A rejected save leaves the editor open with its entered name and selected grant.
  // Users can correct the failure without reconstructing the permission selection.
  it('retains the draft and reports a failed write', async () => {
    api.createToken.mockRejectedValue(new Error('Permission validation failed'))
    const wrapper = page()
    await flushPromises()
    await clickText(wrapper, 'admin.mcp.create')
    await wrapper.get('input[required]').setValue('Retry this draft')
    await wrapper.get('#mcp-token-form').trigger('submit')
    await flushPromises()
    expect((wrapper.get('input[required]').element as HTMLInputElement).value).toBe(
      'Retry this draft',
    )
    expect(wrapper.text()).toContain('Permission validation failed')
    expect(wrapper.find('#mcp-token-form').exists()).toBe(true)
    wrapper.unmount()
  })

  // Creation succeeds before the following list read fails. The secret-delivery dialog
  // must still display the credential so immediate client setup can continue despite the list failure.
  it('keeps the setup credential when post-save readback fails', async () => {
    api.createToken.mockResolvedValue({ credential: 'tr_mcp_readback_fixture', token: { id: 2 } })
    const wrapper = page()
    await flushPromises()
    await clickText(wrapper, 'admin.mcp.create')
    await wrapper.get('input[required]').setValue('Saved token')
    api.listTokens.mockRejectedValueOnce(new Error('Readback unavailable'))
    await wrapper.get('#mcp-token-form').trigger('submit')
    await flushPromises()
    expect(wrapper.text()).toContain('tr_mcp_readback_fixture')
    expect(wrapper.text()).toContain('Readback unavailable')
    expect(wrapper.find('#mcp-token-form').exists()).toBe(false)
    wrapper.unmount()
  })

  // A preset is chosen while the list is filtered to users. It must select the
  // complete gateway grant set, omit sensitive exports and delegation, and preserve
  // the explicitly enabled confirmation preference when saving.
  it('applies complete presets independently of search and saves confirmation preference', async () => {
    api.createToken.mockResolvedValue({ credential: 'tr_mcp_preset_fixture', token: { id: 3 } })
    const wrapper = page()
    await flushPromises()
    await clickText(wrapper, 'admin.mcp.create')
    await wrapper.get('input[required]').setValue('Gateway assistant')
    await wrapper.get('input[aria-label="admin.mcp.permissionSearch"]').setValue('users')
    wrapper.getComponent('[aria-label="admin.mcp.preset"]').vm.$emit('update:modelValue', 'gateway')
    wrapper
      .getComponent('[aria-label="admin.mcp.skipBrowserConfirmation"]')
      .vm.$emit('update:modelValue', true)
    await wrapper.get('#mcp-token-form').trigger('submit')
    await flushPromises()
    expect(api.createToken).toHaveBeenCalledWith(
      expect.objectContaining({
        permissions: ['groups.list', 'providers.create'],
        skip_browser_confirmation: true,
      }),
    )
    wrapper.unmount()
  })

  // An administrator selects read-only access and then changes one grant manually.
  // The editor returns to custom mode and submits only the resulting explicit set.
  it('keeps manual choices after a preset is applied', async () => {
    api.createToken.mockResolvedValue({ credential: 'tr_mcp_manual_fixture', token: { id: 4 } })
    const wrapper = page()
    await flushPromises()
    await clickText(wrapper, 'admin.mcp.create')
    await wrapper.get('input[required]').setValue('Custom reader')
    const preset = wrapper.getComponent('[aria-label="admin.mcp.preset"]')
    preset.vm.$emit('update:modelValue', 'readOnly')
    await flushPromises()
    await clickText(wrapper, 'admin.mcp.adjustPermissions')
    const label = wrapper.findAll('label').find((value) => value.text().includes('Create provider'))
    await label!.get('input[type="checkbox"]').setValue(true)
    expect(preset.attributes('modelvalue')).toBe('custom')
    await wrapper.get('#mcp-token-form').trigger('submit')
    await flushPromises()
    expect(api.createToken).toHaveBeenCalledWith(
      expect.objectContaining({
        permissions: ['groups.list', 'providers.create'],
        skip_browser_confirmation: false,
      }),
    )
    wrapper.unmount()
  })

  // Disabling a token updates admission but must retain its saved confirmation policy.
  // The row refresh reads back the disabled token with the same explicit preference.
  it('preserves the confirmation preference when toggling admission', async () => {
    const token = {
      id: 1,
      name: 'Direct assistant',
      prefix: 'tr_mcp_fixture',
      permissions: ['groups.list'],
      enabled: true,
      skip_browser_confirmation: true,
      expires_at: null,
      revoked_at: null,
    }
    api.listTokens.mockResolvedValueOnce([token])
    const wrapper = page()
    await flushPromises()
    await clickText(wrapper, 'common.more')
    expect(wrapper.getComponent({ name: 'RouterLink' }).props('to')).toEqual({
      path: '/admin/audit-logs',
      query: { q: token.prefix, auth_method: 'mcp_token' },
    })
    api.listTokens.mockResolvedValue([{ ...token, enabled: false }])
    await clickText(wrapper, 'keys.disable')
    await flushPromises()
    expect(api.updateToken).toHaveBeenCalledWith(
      1,
      expect.objectContaining({ enabled: false, skip_browser_confirmation: true }),
    )
    wrapper.unmount()
  })

  // Editing configuration after admission controls move to row actions must preserve
  // the saved status. Both enabled and disabled tokens retain their state after saving.
  it.each([true, false])(
    'preserves enabled=%s when saving the editor without a status switch',
    async (enabled) => {
      const token = {
        id: 12,
        name: 'Existing token',
        prefix: 'tr_mcp_edit_fixture',
        permissions: ['groups.list'],
        enabled,
        skip_browser_confirmation: false,
        expires_at: null,
        revoked_at: null,
      }
      api.listTokens.mockResolvedValue([token])
      api.updateToken.mockResolvedValue(token)
      const wrapper = page()
      await flushPromises()
      await clickText(wrapper, 'common.edit')
      expect(wrapper.findComponent('[aria-label="admin.mcp.enabled"]').exists()).toBe(false)
      await wrapper.get('input[required]').setValue('Renamed token')
      await wrapper.get('#mcp-token-form').trigger('submit')
      await flushPromises()
      expect(api.updateToken).toHaveBeenCalledWith(
        12,
        expect.objectContaining({ name: 'Renamed token', enabled }),
      )
      wrapper.unmount()
    },
  )

  // The table displays a prefix while the copy endpoint returns its complete credential.
  // Copying must use the fetched value and keep reusable secrets out of rendered rows.
  it('copies the full credential on demand and keeps row metadata masked', async () => {
    api.listTokens.mockResolvedValue([
      { id: 9, prefix: 'tr_mcp_display', name: 'Copy fixture', permissions: [], enabled: true },
    ])
    api.getCredential.mockResolvedValue('tr_mcp_complete_secret_fixture')
    const wrapper = page()
    await flushPromises()
    await wrapper.get('button[aria-label="keys.copyToClipboard"]').trigger('click')
    await flushPromises()
    expect(api.getCredential).toHaveBeenCalledWith(9)
    expect(copyToClipboard).toHaveBeenCalledWith('tr_mcp_complete_secret_fixture')
    expect(wrapper.text()).not.toContain('tr_mcp_complete_secret_fixture')
    expect(wrapper.get('button[aria-label="keys.copyToClipboard"]').attributes('title')).toBe(
      'keys.copied',
    )
    expect(wrapper.findComponent('[aria-label="admin.mcp.enabled"]').exists()).toBe(false)
    await clickText(wrapper, 'common.more')
    await clickText(wrapper, 'admin.mcp.revoke')
    expect(wrapper.getComponent({ name: 'ConfirmDialog' }).attributes('show')).toBe('true')
    expect(api.revokeToken).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  // A credential retrieval failure must leave the clipboard untouched. A delayed
  // successful retrieval after leaving the page must also avoid writing a secret.
  it('does not copy prefixes or publish a late credential after disposal', async () => {
    api.listTokens.mockResolvedValue([
      { id: 9, prefix: 'tr_mcp_display', permissions: [], enabled: true },
    ])
    api.getCredential.mockRejectedValueOnce(new Error('Credential unavailable'))
    const wrapper = page()
    await flushPromises()
    await wrapper.get('button[aria-label="keys.copyToClipboard"]').trigger('click')
    await flushPromises()
    expect(copyToClipboard).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('Credential unavailable')
    let resolve!: (secret: string) => void
    api.getCredential.mockImplementationOnce(
      () =>
        new Promise<string>((done) => {
          resolve = done
        }),
    )
    await wrapper.get('button[aria-label="keys.copyToClipboard"]').trigger('click')
    wrapper.unmount()
    resolve('tr_mcp_late_fixture')
    await flushPromises()
    expect(copyToClipboard).not.toHaveBeenCalled()
  })
})
