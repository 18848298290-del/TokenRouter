import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { setActivePinia, createPinia } from 'pinia'
import { defineComponent, reactive, ref } from 'vue'
import { useAuthStore } from '@/stores/auth'

// LoginForm.spec.ts exercises a small login harness around the real auth store.
// The fixture reproduces required-field checks, failure display, pending requests,
// and second-factor responses; it does not mount the full LoginView or its
// agreement and captcha controls.

// These fixtures provide controllable dependencies and component handles for the
// cases below. Each test configures the responses it needs and inspects calls or
// rendered state at that boundary.
const mockLogin = vi.fn() // test-controlled mock login fixture
const mockLogin2FA = vi.fn() // test-controlled mock login2fa fixture
const mockPush = vi.fn() // test-controlled mock push fixture

vi.mock('@/api', () => ({
  authAPI: {
    // login forwards submitted password credentials to the configured
    // authentication mock.
    login: (...args: any[]) => mockLogin(...args),
    // login2FA forwards the challenge credential and code to the verification
    // mock.
    login2FA: (...args: any[]) => mockLogin2FA(...args),
    logout: vi.fn(),
    getCurrentUser: vi.fn().mockResolvedValue({ data: {} }),
    register: vi.fn(),
    refreshToken: vi.fn(),
  },
  // isTotp2FARequired returns the fixture result used to select authentication
  // behavior.
  isTotp2FARequired: (response: any) => response?.requires_2fa === true,
}))

vi.mock('@/api/admin/system', () => ({
  checkUpdates: vi.fn(),
}))

vi.mock('@/api/auth', () => ({
  getPublicSettings: vi.fn().mockResolvedValue({}),
}))

// LoginFormTestComponent reproduces a minimal login form around the real auth
// store. Its template exposes pending and error state while external login and
// navigation calls remain observable mocks.
const LoginFormTestComponent = defineComponent({
  // setup builds the minimal store-backed login harness used by these tests. It
  // exposes its form and request state to the template while keeping full-page
  // layout, captcha, and agreement dependencies outside the fixture.
  setup() {
    const authStore = useAuthStore()
    const formData = reactive({
      email: '', // entered account email
      password: '', // entered account password
    })
    const isLoading = ref(false)
    const errorMessage = ref('')

    // handleLogin submits the harness draft only when both credentials are
    // present. It exposes endpoint errors, leaves second-factor responses pending
    // for the caller, and clears busy state after each request settles.
    const handleLogin = async () => {
      if (!formData.email || !formData.password) {
        errorMessage.value = '请输入邮箱和密码'
        return
      }

      isLoading.value = true
      errorMessage.value = ''

      try {
        const response = await authStore.login({
          email: formData.email,
          password: formData.password,
        })

        // The harness exposes a second-factor response as pending feedback. Its
        // caller handles that separate challenge, so a password response alone
        // cannot navigate to the dashboard.
        if ((response as any)?.requires_2fa) {
          errorMessage.value = '需要 2FA 验证'
          return
        }

        mockPush('/dashboard')
      } catch (error: any) {
        errorMessage.value = error.message || '登录失败'
      } finally {
        isLoading.value = false
      }
    }

    return { formData, isLoading, errorMessage, handleLogin }
  },
  template: `
    <form @submit.prevent="handleLogin">
      <input id="email" v-model="formData.email" type="email" />
      <input id="password" v-model="formData.password" type="password" />
      <p v-if="errorMessage" class="error">{{ errorMessage }}</p>
      <button type="submit" :disabled="isLoading">登录</button>
    </form>
  `,
}) // test-controlled login form test component fixture

describe('LoginForm 核心逻辑', () => {
  // Reset the dependency fixtures before each case so request histories and
  // configured responses belong to the scenario being asserted in that case.
  // Install a fresh active Pinia instance before mounting store consumers.
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  // The harness submits valid credentials through the auth store. The test checks
  // the exact login payload and dashboard navigation after the request succeeds.
  it('成功登录后跳转到 dashboard', async () => {
    mockLogin.mockResolvedValue({
      access_token: 'token',
      token_type: 'Bearer',
      user: {
        id: 1,
        username: 'test',
        email: 'test@example.com',
        role: 'user',
        balance: 0,
        concurrency: 5,
        status: 'active',
        allowed_groups: null,
        created_at: '',
        updated_at: '',
      },
    })

    const wrapper = mount(LoginFormTestComponent)

    await wrapper.find('#email').setValue('test@example.com')
    await wrapper.find('#password').setValue('password123')
    await wrapper.find('form').trigger('submit')
    await flushPromises()

    expect(mockLogin).toHaveBeenCalledWith({
      email: 'test@example.com',
      password: 'password123',
    })
    expect(mockPush).toHaveBeenCalledWith('/dashboard')
  })

  // The login endpoint rejects the submitted credentials with a message. The
  // harness must expose that message in its error element after the promise
  // settles.
  it('登录失败时显示错误信息', async () => {
    mockLogin.mockRejectedValue(new Error('Invalid credentials'))

    const wrapper = mount(LoginFormTestComponent)

    await wrapper.find('#email').setValue('test@example.com')
    await wrapper.find('#password').setValue('wrong')
    await wrapper.find('form').trigger('submit')
    await flushPromises()

    expect(wrapper.find('.error').text()).toBe('Invalid credentials')
  })

  // The user submits the harness without an email or password. Required-field
  // feedback must appear locally and the login endpoint must remain uncalled.
  it('空表单提交显示验证错误', async () => {
    const wrapper = mount(LoginFormTestComponent)

    await wrapper.find('form').trigger('submit')
    await flushPromises()

    expect(wrapper.find('.error').text()).toBe('请输入邮箱和密码')
    expect(mockLogin).not.toHaveBeenCalled()
  })

  // The password endpoint returns a second-factor challenge instead of a completed
  // login. The harness shows the challenge marker and leaves dashboard navigation
  // uncalled.
  it('需要 2FA 时不跳转', async () => {
    mockLogin.mockResolvedValue({
      requires_2fa: true,
      temp_token: 'temp-123',
    })

    const wrapper = mount(LoginFormTestComponent)

    await wrapper.find('#email').setValue('test@example.com')
    await wrapper.find('#password').setValue('password123')
    await wrapper.find('form').trigger('submit')
    await flushPromises()

    expect(mockPush).not.toHaveBeenCalled()
    expect(wrapper.find('.error').text()).toBe('需要 2FA 验证')
  })

  // The login request remains pending until the test resolves it. The submit
  // button must stay disabled during that interval and become available afterward.
  it('提交过程中按钮被禁用', async () => {
    let resolveLogin: (v: any) => void
    mockLogin.mockImplementation(
      () =>
        new Promise((resolve) => {
          resolveLogin = resolve
        }),
    )

    const wrapper = mount(LoginFormTestComponent)

    await wrapper.find('#email').setValue('test@example.com')
    await wrapper.find('#password').setValue('password123')
    await wrapper.find('form').trigger('submit')

    expect(wrapper.find('button').attributes('disabled')).toBeDefined()

    resolveLogin!({
      access_token: 'token',
      token_type: 'Bearer',
      user: {
        id: 1,
        username: 'test',
        email: 'test@example.com',
        role: 'user',
        balance: 0,
        concurrency: 5,
        status: 'active',
        allowed_groups: null,
        created_at: '',
        updated_at: '',
      },
    })
    await flushPromises()

    expect(wrapper.find('button').attributes('disabled')).toBeUndefined()
  })
})
