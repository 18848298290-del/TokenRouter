import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import type { ApiKey } from '@/api/keys.types'
import TfCliImportDialog from '../TfCliImportDialog.vue'

// TfCliImportDialog.spec.ts exercises the production CLI import dialog with controlled
// discovery and import results. The protocol helpers are mocked while the component owns
// confirmation, payload construction, retry states, and cancellation of the browser wait.

// Protocol mocks control listener discovery and import completion so dialog confirmation and
// cancellation remain observable without a local CLI process.
const protocol = vi.hoisted(() => ({
  findTfCli: vi.fn(),
  importKeyToTf: vi.fn(),
}))

vi.mock('@/utils/tfCliImport', () => protocol)
vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key }),
}))

// The selected key fixture includes one ordinary group so payload assertions can distinguish
// normal group metadata from composite-key omission.
const apiKey = {
  id: 7,
  key: 'sk-secret',
  name: 'Laptop',
  group_id: 12,
  group: { id: 12, name: 'GPT' },
  is_composite: false,
} as ApiKey

// mountDialog opens the production import flow with a selected key and a slot-preserving
// shell. Protocol mocks are configured by each caller before mounting so initial discovery
// receives its scenario response.
function mountDialog(key: ApiKey = apiKey) {
  return mount(TfCliImportDialog, {
    props: {
      show: true,
      apiKey: key,
    },
    global: {
      stubs: {
        BaseDialog: {
          props: ['show', 'title', 'width'],
          emits: ['close'],
          template: `
            <div v-if="show" data-test="base-dialog">
              <button data-test="base-close" @click="$emit('close')">close</button>
              <slot />
              <slot name="footer" />
            </div>
          `,
        },
        Icon: { template: '<span />' },
      },
    },
  })
}

describe('TfCliImportDialog', () => {
  beforeEach(() => {
    protocol.findTfCli.mockReset()
    protocol.importKeyToTf.mockReset()
  })

  // Discovery finds a verified local CLI session before the user confirms sending. The test
  // protects the extra click and checks the exact key, origin, and group payload.
  it('requires a second click before sending to a verified tf session', async () => {
    protocol.findTfCli.mockResolvedValue({ port: 43110, verified: true })
    protocol.importKeyToTf.mockResolvedValue({ ok: true, status: 202 })
    const wrapper = mountDialog()
    await flushPromises()

    expect(wrapper.find('[data-test="tf-cli-verified"]').exists()).toBe(true)
    expect(protocol.importKeyToTf).not.toHaveBeenCalled()

    await wrapper.get('[data-test="tf-cli-send"]').trigger('click')
    await flushPromises()

    expect(protocol.importKeyToTf).toHaveBeenCalledWith(
      { port: 43110, verified: true },
      {
        key: 'sk-secret',
        host: window.location.origin,
        key_name: 'Laptop',
        group_id: 12,
        group_name: 'GPT',
      },
      expect.any(AbortSignal),
    )
    expect(wrapper.find('[data-test="tf-cli-accepted"]').exists()).toBe(true)
  })

  // A verified destination loses its session proof when import begins. The test protects the
  // return to unverified confirmation and requires another click before retrying
  // transmission.
  it('requires another click after a verified proof becomes unavailable', async () => {
    protocol.findTfCli.mockResolvedValue({ port: 43110, verified: true })
    protocol.importKeyToTf
      .mockResolvedValueOnce({ ok: false, error: 'session_proof_unavailable' })
      .mockResolvedValueOnce({ ok: true, status: 202 })
    const wrapper = mountDialog()
    await flushPromises()

    await wrapper.get('[data-test="tf-cli-send"]').trigger('click')
    await flushPromises()

    expect(protocol.importKeyToTf).toHaveBeenCalledTimes(1)
    expect(wrapper.find('[data-test="tf-cli-unverified"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="tf-cli-accepted"]').exists()).toBe(false)

    await wrapper.get('[data-test="tf-cli-send"]').trigger('click')
    await flushPromises()

    expect(protocol.importKeyToTf).toHaveBeenCalledTimes(2)
    expect(protocol.importKeyToTf.mock.calls[1]?.[0]).toEqual({ port: 43110, verified: false })
    expect(wrapper.find('[data-test="tf-cli-accepted"]').exists()).toBe(true)
  })

  // Discovery finds an unverified compatibility listener that rejects the submitted key. The
  // test checks visible trust feedback, an available confirmation action, and the resulting
  // rejection message.
  it('shows a blocking visual warning but permits the unverified compatibility path', async () => {
    protocol.findTfCli.mockResolvedValue({ port: 43114, verified: false })
    protocol.importKeyToTf.mockResolvedValue({ ok: false, status: 409, error: 'rejected' })
    const wrapper = mountDialog()
    await flushPromises()

    expect(wrapper.find('[data-test="tf-cli-unverified"]').exists()).toBe(true)
    expect(wrapper.get('[data-test="tf-cli-send"]').attributes('disabled')).toBeUndefined()

    await wrapper.get('[data-test="tf-cli-send"]').trigger('click')
    await flushPromises()

    expect(wrapper.find('[data-test="tf-cli-import-error"]').exists()).toBe(true)
    expect(wrapper.text()).toContain('keys.tfImport.errors.rejected')
  })

  // A composite key contains multiple prefix bindings while retaining incidental single-
  // group metadata. The test checks that import sends the secret and origin while omitting
  // misleading group fields.
  it('omits misleading single-group metadata for a composite key', async () => {
    protocol.findTfCli.mockResolvedValue({ port: 43111, verified: true })
    protocol.importKeyToTf.mockResolvedValue({ ok: true, status: 202 })
    const composite = {
      ...apiKey,
      id: 8,
      is_composite: true,
      composite_groups: [
        { group_id: 12, prefix: 'GPT' },
        { group_id: 13, prefix: 'CLAUDE' },
      ],
    } as ApiKey
    const wrapper = mountDialog(composite)
    await flushPromises()

    await wrapper.get('[data-test="tf-cli-send"]').trigger('click')
    await flushPromises()

    expect(protocol.importKeyToTf.mock.calls[0]?.[1]).toEqual({
      key: 'sk-secret',
      host: window.location.origin,
      key_name: 'Laptop',
    })
  })

  // The first discovery finds no local listener and the user retries. The test checks a
  // second discovery request and the resulting unverified destination confirmation state.
  it('offers retry when no local tf service is found', async () => {
    protocol.findTfCli.mockResolvedValueOnce(null).mockResolvedValueOnce({
      port: 43112,
      verified: false,
    })
    const wrapper = mountDialog()
    await flushPromises()

    expect(wrapper.find('[data-test="tf-cli-not-found"]').exists()).toBe(true)
    await wrapper.get('[data-test="tf-cli-retry"]').trigger('click')
    await flushPromises()

    expect(protocol.findTfCli).toHaveBeenCalledTimes(2)
    expect(wrapper.find('[data-test="tf-cli-unverified"]').exists()).toBe(true)
  })

  // The dialog closes while terminal confirmation leaves the import promise pending. The
  // test protects cancellation of the browser signal before the delayed successful response
  // settles.
  it('aborts the browser wait when dismissed during terminal confirmation', async () => {
    let finishImport:
      | ((value: {
          ok: true // Successful import result flag.
          status: 202 // Accepted HTTP response status.
        }) => void)
      | undefined
    protocol.findTfCli.mockResolvedValue({ port: 43110, verified: true })
    protocol.importKeyToTf.mockReturnValue(
      new Promise((resolve) => {
        finishImport = resolve
      }),
    )
    const wrapper = mountDialog()
    await flushPromises()

    await wrapper.get('[data-test="tf-cli-send"]').trigger('click')
    const requestSignal = protocol.importKeyToTf.mock.calls[0]?.[2] as AbortSignal
    await wrapper.get('[data-test="base-close"]').trigger('click')

    expect(wrapper.emitted('close')).toHaveLength(1)
    expect(requestSignal.aborted).toBe(true)
    finishImport?.({ ok: true, status: 202 })
    await flushPromises()
  })
})
