import { defineComponent, ref } from 'vue'
import { enableAutoUnmount, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useOpenAICreateAuthorization } from '../useOpenAICreateAuthorization'

// useOpenAICreateAuthorization.spec.ts checks callback identity and partial batch
// persistence through the extracted OpenAI controller. Controlled authorization
// results expose which session is consumed and which provider request follows,
// independently of the dialog's layout and shared field rendering.

// transport captures authorization side effects and notifications; each test
// resets its results while preserving the controller's real batch control flow.
const transport = vi.hoisted(() => ({
  exchange: vi.fn(),
  remove: vi.fn(),
  create: vi.fn(),
  success: vi.fn(),
  warning: vi.fn(),
  error: vi.fn(),
}))

vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({ t: (key: string) => key }),
}))
vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showSuccess: transport.success,
    showWarning: transport.warning,
    showError: transport.error,
  }),
}))
vi.mock('@/composables/useOpenAIOAuth', async () => {
  const { ref } = await import('vue')
  return {
    useOpenAIOAuth: () => ({
      authUrl: ref(''),
      sessionId: ref(''),
      oauthState: ref(''),
      authSessions: ref([
        { authUrl: 'https://example.test/a', sessionId: 'session-a', state: 'state-a' },
        { authUrl: 'https://example.test/b', sessionId: 'session-b', state: 'state-b' },
      ]),
      loading: ref(false),
      error: ref(''),
      exchangeAuthCode: transport.exchange,
      removeAuthSession: transport.remove,
      buildCredentials: (token: Record<string, unknown>) => ({ ...token }),
      buildExtraInfo: () => undefined,
    }),
  }
})

// mountController installs the real controller in Vue setup with isolated policy
// callbacks. It returns the controller and created/close spies for assertions;
// the suite's automatic unmount registration cleans up the mounted harness.
function mountController() {
  const created = vi.fn()
  const close = vi.fn()
  let controller!: ReturnType<typeof useOpenAICreateAuthorization>
  mount(
    defineComponent({
      // setup binds the controller to isolated callbacks before the mounted
      // harness returns, allowing tests to invoke its public authorization methods.
      setup() {
        controller = useOpenAICreateAuthorization({
          form: {
            name: 'Imported',
            notes: '',
            proxy_id: 7,
            concurrency: 10,
            load_factor: null,
            priority: 1,
            rate_multiplier: 1,
            group_ids: [],
            expires_at: null,
          },
          autoPauseOnExpired: ref(true),
          upstreamProtocols: ref(undefined),
          inputMethod: () => 'manual',
          buildCredentialExtras: () => ({}),
          applyCredentialPolicies: (credentials) => {
            delete credentials.model_mapping
            credentials.model_whitelist = []
            return true
          },
          buildExtra: (base) => base,
          withRequestIdHeader: (base) => base,
          validateTemporaryRules: () => true,
          tokenTLSRouterId: () => null,
          createProvider: transport.create,
          created,
          close,
        })
        return () => null
      },
    }),
  )
  return { controller, created, close }
}

enableAutoUnmount(afterEach)
beforeEach(() => {
  vi.resetAllMocks()
  transport.exchange.mockResolvedValue({ access_token: 'token', model_mapping: { old: 'old' } })
  transport.create.mockResolvedValue({ id: 1 })
})

describe('OpenAI creation authorization', () => {
  // Reversed callback states must follow their corresponding authorization sessions.
  // The second request also retains explicit whitelist clearing and removes inherited mappings before provider persistence.
  it('matches callback states before queue position and decorates original credentials', async () => {
    const { controller, created, close } = mountController()
    await controller.handleOpenAIExchange(
      'https://example.test/callback?code=b&state=state-b\nhttps://example.test/callback?code=a&state=state-a',
    )
    expect(transport.exchange.mock.calls.map((call) => call.slice(0, 3))).toEqual([
      ['b', 'session-b', 'state-b'],
      ['a', 'session-a', 'state-a'],
    ])
    expect(transport.create.mock.calls.map(([payload]) => payload.name)).toEqual([
      'Imported #1',
      'Imported #2',
    ])
    expect(transport.create.mock.calls[0][0].credentials).toEqual({
      access_token: 'token',
      model_whitelist: [],
    })
    expect(created).toHaveBeenCalledOnce()
    expect(close).toHaveBeenCalledOnce()
  })

  // A consumed callback can still fail during provider persistence while a later
  // callback succeeds. Partial completion refreshes the list and retains the dialog with indexed failure text.
  it('consumes exchanged sessions before persistence and continues after creation failure', async () => {
    transport.create.mockRejectedValueOnce(new Error('persistence failed'))
    const { controller, created, close } = mountController()
    await controller.handleOpenAIExchange('first-code\nsecond-code')
    expect(transport.remove.mock.calls).toEqual([['session-a'], ['session-b']])
    expect(transport.remove.mock.invocationCallOrder[0]).toBeLessThan(
      transport.create.mock.invocationCallOrder[0],
    )
    expect(transport.create).toHaveBeenCalledTimes(2)
    expect(controller.openaiOAuth.error.value).toContain('#1: persistence failed')
    expect(controller.openaiOAuth.loading.value).toBe(false)
    expect(transport.warning).toHaveBeenCalledOnce()
    expect(created).toHaveBeenCalledOnce()
    expect(close).not.toHaveBeenCalled()
  })

  // Two pasted callbacks cannot consume the same state-bound authorization session.
  // The unmatched second entry remains visible as a partial failure instead of exchanging the first session twice.
  it('reserves each session for at most one callback in a batch', async () => {
    const { controller, close } = mountController()
    await controller.handleOpenAIExchange('?code=a&state=state-a\n?code=b&state=state-a')
    expect(transport.exchange).toHaveBeenCalledOnce()
    expect(controller.openaiOAuth.error.value).toContain(
      '#2: admin.providers.oauth.openai.noMatchingAuthUrl',
    )
    expect(close).not.toHaveBeenCalled()
  })
})
