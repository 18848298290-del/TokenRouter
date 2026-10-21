import { shallowMount, flushPromises, enableAutoUnmount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import VersionBadge from '../VersionBadge.vue'

// VersionBadge.rollback.spec.ts checks the commands an administrator receives
// after selecting a historical release. Version discovery and all system actions
// use local mocks, so rendering and copying rollback instructions cannot update
// binaries, restart services, or communicate with a running backend.

// actions records every system operation and clipboard write during the manual
// rollback interaction.
const actions = vi.hoisted(() => ({
  update: vi.fn(), // Binary update action recorder.
  restart: vi.fn(), // Service restart action recorder.
  rollback: vi.fn(), // Online rollback action recorder.
  copy: vi.fn(), // Clipboard command capture recorder.
}))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/i18n', () => ({ getLocale: () => 'en-US' }))
vi.mock('@/directives/segmented', () => ({ vSegmented: {} }))
vi.mock('@/stores', () => ({
  useAuthStore: () => ({ isAdmin: true }),
  useAppStore: () => ({
    currentVersion: '1.2.3',
    hasUpdate: false,
    buildType: 'release',
    fetchVersion: vi.fn(),
  }),
}))
vi.mock('@/composables/useClipboard', () => ({
  useClipboard: () => ({ copied: false, copyToClipboard: actions.copy }),
}))
vi.mock('@/api/admin/system', () => ({
  performUpdate: actions.update,
  restartService: actions.restart,
  rollback: actions.rollback,
  getRollbackVersions: vi
    .fn()
    .mockResolvedValue({ versions: [{ version: '1.2.2', published_at: '2026-01-01T00:00:00Z' }] }),
}))
enableAutoUnmount(afterEach)

describe('VersionBadge rollback commands', () => {
  // Selecting a historical release produces installer and Docker instructions
  // for TokenRouter. Copying those instructions must never invoke an online system action.
  it('renders and copies TokenRouter repository and GHCR commands', async () => {
    const wrapper = shallowMount(VersionBadge, { global: { renderStubDefaultSlot: true } })
    await wrapper.get('button').trigger('click')
    await wrapper
      .findAll('button')
      .find((button) => button.text() === 'version.rollback')!
      .trigger('click')
    await flushPromises()
    await wrapper
      .findAll('button')
      .find((button) => button.text().includes('v1.2.2'))!
      .trigger('click')
    const script =
      'curl -sSL https://raw.githubusercontent.com/TokenFlux/TokenRouter/v1.2.2/deploy/install.sh | sudo bash -s -- rollback v1.2.2'
    expect(wrapper.get('code').text()).toBe(script)
    await wrapper
      .findAll('button')
      .find((button) => button.text() === 'version.copyCommand')!
      .trigger('click')
    expect(actions.copy).toHaveBeenLastCalledWith(script)
    await wrapper
      .findAll('button')
      .find((button) => button.text() === 'version.deployDocker')!
      .trigger('click')
    const docker =
      '# version.dockerEditCompose\nimage: ghcr.io/tokenflux/tokenrouter:1.2.2\n\n# version.dockerRecreate\ndocker compose up -d'
    expect(wrapper.get('code').text()).toBe(docker)
    await wrapper
      .findAll('button')
      .find((button) => button.text() === 'version.copyCommand')!
      .trigger('click')
    expect(actions.copy).toHaveBeenLastCalledWith(docker)
    expect(actions.update).not.toHaveBeenCalled()
    expect(actions.restart).not.toHaveBeenCalled()
    expect(actions.rollback).not.toHaveBeenCalled()
  })
})
