import { beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import TLSFingerprintRoutersModal from '../TLSFingerprintRoutersModal.vue'

// TLSFingerprintRoutersModal.spec.ts checks TLS router editing, import,
// export, and deletion through the operations dialog. YAML fixtures exercise
// ordered rules, import defaults, and preservation of the draft after invalid
// input. API and clipboard spies capture the records and text produced by
// form interactions.
//
// The native select stub receives string event values and restores typed
// choices by finding the first option with a matching string representation.
// Null and empty-string options share that representation, so option order
// resolves their collision. Unmatched strings pass through. The stub emits
// the model update before the change event, which supplies the recovered value
// and its matching option or null. The router form updates its state through
// that change handler rather than consuming the model-update event.

// Router CRUD, profile listing, clipboard, and notification spies expose form
// persistence and import feedback without changing server-managed TLS
// configuration.
const {
  listRoutersMock,
  createRouterMock,
  updateRouterMock,
  deleteRouterMock,
  listProfilesMock,
  copyToClipboardMock,
  showSuccessMock,
  showErrorMock,
} = vi.hoisted(() => ({
  listRoutersMock: vi.fn(),
  createRouterMock: vi.fn(),
  updateRouterMock: vi.fn(),
  deleteRouterMock: vi.fn(),
  listProfilesMock: vi.fn(),
  copyToClipboardMock: vi.fn(),
  showSuccessMock: vi.fn(),
  showErrorMock: vi.fn(),
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: showErrorMock,
    showSuccess: showSuccessMock,
    showInfo: vi.fn(),
  }),
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    tlsFingerprintRouters: {
      list: listRoutersMock,
      create: createRouterMock,
      update: updateRouterMock,
      delete: deleteRouterMock,
    },
    tlsFingerprintProfiles: {
      list: listProfilesMock,
    },
  },
}))

vi.mock('@/composables/useClipboard', () => ({
  useClipboard: () => ({
    copyToClipboard: copyToClipboardMock,
  }),
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) => {
        if (params && typeof params.index !== 'undefined') return `${key}:${params.index}`
        if (params && typeof params.name !== 'undefined') return `${key}:${params.name}`
        return key
      },
    }),
  }
})

// BaseDialogStub exposes router content and footer slots only while shown so
// the real modal can own edit and creation controls.
const BaseDialogStub = defineComponent({
  name: 'BaseDialog',
  props: {
    // show controls slot mounting in this stub and defaults to hidden until
    // the parent dialog opens the corresponding content or confirmation.
    show: {
      type: Boolean,
      default: false,
    },
  },
  template: '<div v-if="show"><slot /><slot name="footer" /></div>',
})

// ConfirmDialogStub renders a confirm event button only while open, keeping
// deletion dependent on the modal confirmation state in these tests.
const ConfirmDialogStub = defineComponent({
  name: 'ConfirmDialog',
  props: {
    // show controls slot mounting in this stub and defaults to hidden until
    // the parent dialog opens the corresponding content or confirmation.
    show: {
      type: Boolean,
      default: false,
    },
  },
  emits: [
    'confirm', // confirm pending dialog action
    'cancel', // cancel pending dialog action
  ],
  template:
    '<div v-if="show"><button data-testid="confirm-delete" @click="$emit(\'confirm\')">confirm</button></div>',
})

// SelectStub restores native DOM option strings to typed profile and matching
// values before emitting model and change events to the router form.
const SelectStub = defineComponent({
  name: 'SelectStub',
  props: {
    // modelValue retains the form's typed selection; the native control
    // renders it as text until onChange restores its matching option value.
    modelValue: {
      type: [String, Number, Boolean, null],
      default: '',
    },
    // options supplies typed values for native select conversion. Each stub
    // receives an empty list until the parent provides its available choices.
    options: {
      type: Array,
      default: () => [],
    },
  },
  emits: [
    'update:modelValue', // replacement controlled input value
    'change', // notify parent of input change
  ],
  // setup exposes the native select change handler used by the router form
  // stub. It reads current props when events arrive so updated option lists
  // retain their typed values.
  setup(props, { emit }) {
    // onChange restores the selected DOM string to the first matching option
    // value so the router form receives typed profile IDs and other choices.
    // Null and empty-string options share an empty native representation, so
    // their order determines which value is recovered. Other colliding string
    // representations likewise use the first match.
    //
    // An unmatched string passes through with a null option. The model update
    // is emitted before change, which receives both the recovered value and
    // matching option. The router form uses that change event to update its state;
    // emitting the earlier model-update event does not itself change this form.
    const onChange = (event: Event) => {
      const target = event.target as HTMLSelectElement
      // Recover typed profile IDs before emitting either event, because the
      // router payload assertions distinguish numeric IDs from native select
      // strings.
      const option =
        (props.options as Array<Record<string, unknown>>).find(
          (item) => String(item.value ?? '') === target.value,
        ) ?? null
      const value = option ? (option.value as string | number | boolean | null) : target.value
      emit('update:modelValue', value)
      emit('change', value, option)
    }

    return {
      onChange,
    }
  },
  template: `
    <select v-bind="$attrs" :value="modelValue ?? ''" @change="onChange">
      <option v-for="option in options" :key="String(option.value)" :value="option.value">
        {{ option.label }}
      </option>
    </select>
  `,
})

// routerRecord supplies one enabled router with token, invitation, and rule
// identities so list actions, editing, and YAML export share concrete expected
// fields.
const routerRecord = {
  id: 9,
  name: 'Codex Router',
  description: 'UA based',
  enabled: true,
  chatgpt_oauth_token_user_agent: 'codex-token-ua/1.0',
  chatgpt_oauth_token_tls_fingerprint_profile_id: 7,
  codex_invite_reset_user_agent: 'Codex Desktop/0.135.0-alpha.1 (Windows 10.0.26200; x86_64)',
  codex_invite_reset_tls_fingerprint_profile_id: 7,
  rules: [
    {
      name: 'codex mac',
      enabled: true,
      match_type: 'contains',
      pattern: 'codex_cli',
      case_sensitive: false,
      tls_fingerprint_profile_id: 7,
      upstream_user_agent: 'codex_cli_rs/0.125.0',
      upstream_originator: 'codex_cli_rs',
    },
  ],
  created_at: '2026-06-01T00:00:00Z',
  updated_at: '2026-06-01T00:00:00Z',
}

// importedRouterYaml supplies two ordered rules and explicit identity
// settings, including an unknown match type and omitted headers that exercise
// import defaults.
const importedRouterYaml = [
  'tls_fingerprint_router:',
  '  name: "Imported Router"',
  '  description: "Imported from YAML"',
  '  enabled: false',
  '  chatgpt_oauth_token_user_agent: "import-token-ua/1.0"',
  '  chatgpt_oauth_token_tls_fingerprint_profile_id: -1',
  '  codex_invite_reset_user_agent: "import-codex-desktop-ua/1.0"',
  '  codex_invite_reset_tls_fingerprint_profile_id: 7',
  '  rules:',
  '    - name: "codex exact"',
  '      enabled: true',
  '      match_type: "exact"',
  '      pattern: "codex_cli_rs/0.125.0"',
  '      case_sensitive: true',
  '      tls_fingerprint_profile_id: 7',
  '      upstream_user_agent: "codex_cli_rs/0.125.0 upstream"',
  '      upstream_originator: "codex_cli_rs"',
  '    - name: "fallback"',
  '      enabled: false',
  '      match_type: "unknown"',
  '      pattern: "OpenAI"',
  '      case_sensitive: false',
  '      tls_fingerprint_profile_id: -1',
].join('\n')

// mountModal opens router management with typed select and confirmation stubs.
// Configure router and profile mocks before mounting, then await list loading
// before creating, editing, exporting, or deleting a record.
function mountModal() {
  return mount(TLSFingerprintRoutersModal, {
    props: {
      show: true,
    },
    global: {
      stubs: {
        BaseDialog: BaseDialogStub,
        ConfirmDialog: ConfirmDialogStub,
        Select: SelectStub,
        Icon: true,
      },
    },
  })
}

describe('TLSFingerprintRoutersModal', () => {
  beforeEach(() => {
    listRoutersMock.mockReset()
    createRouterMock.mockReset()
    updateRouterMock.mockReset()
    deleteRouterMock.mockReset()
    listProfilesMock.mockReset()
    copyToClipboardMock.mockReset()
    showSuccessMock.mockReset()
    showErrorMock.mockReset()

    listRoutersMock.mockResolvedValue([routerRecord])
    createRouterMock.mockResolvedValue(routerRecord)
    updateRouterMock.mockResolvedValue(routerRecord)
    deleteRouterMock.mockResolvedValue({ message: 'ok' })
    listProfilesMock.mockResolvedValue([{ id: 7, name: 'Codex TLS' }])
    copyToClipboardMock.mockResolvedValue(true)
  })

  // The router list returns an enabled record when the dialog opens. Clicking
  // its status action sends a disabling update for that record's ID.
  it('加载列表并支持启停路由器', async () => {
    const wrapper = mountModal()
    await flushPromises()

    expect(listRoutersMock).toHaveBeenCalledTimes(1)
    expect(wrapper.text()).toContain('Codex Router')

    const toggleButton = wrapper
      .findAll('button')
      .find((button) => button.text().includes('common.enabled'))
    expect(toggleButton).toBeTruthy()
    await toggleButton!.trigger('click')
    await flushPromises()

    expect(updateRouterMock).toHaveBeenCalledWith(9, { enabled: false })
  })

  // A new router receives user agents, profile selections, and a prefix rule.
  // Creation submits those values with numeric profile IDs and the configured
  // rule array.
  it('创建路由器时会提交有序规则', async () => {
    const wrapper = mountModal()
    await flushPromises()

    await wrapper
      .findAll('button')
      .find((button) => button.text().includes('admin.tlsFingerprintRouters.createRouter'))!
      .trigger('click')
    await flushPromises()

    await wrapper.find('input[required]').setValue('OpenCode Router')
    await wrapper
      .findAll('button')
      .find((button) => button.text().includes('admin.tlsFingerprintRouters.form.addRule'))!
      .trigger('click')
    await flushPromises()

    const textInputs = wrapper.findAll('input[type="text"]')
    await textInputs[2].setValue('token-ua/1.0')
    await textInputs[3].setValue('Codex Desktop/0.135.0-alpha.1 (Windows 10.0.26200; x86_64)')
    await textInputs[4].setValue('opencode')
    await textInputs[5].setValue('opencode/')
    await textInputs[6].setValue('opencode/1.0 upstream')
    await textInputs[7].setValue('opencode')
    const selects = wrapper.findAll('select')
    await selects[0].setValue('7')
    await selects[1].setValue('7')
    await selects[2].setValue('7')
    await selects[3].setValue('prefix')
    await wrapper
      .findAll('button')
      .find((button) => button.text().includes('common.create'))!
      .trigger('click')
    await flushPromises()

    expect(createRouterMock).toHaveBeenCalledWith({
      name: 'OpenCode Router',
      description: null,
      enabled: true,
      chatgpt_oauth_token_user_agent: 'token-ua/1.0',
      chatgpt_oauth_token_tls_fingerprint_profile_id: 7,
      codex_invite_reset_user_agent: 'Codex Desktop/0.135.0-alpha.1 (Windows 10.0.26200; x86_64)',
      codex_invite_reset_tls_fingerprint_profile_id: 7,
      rules: [
        {
          name: 'opencode',
          enabled: true,
          match_type: 'prefix',
          pattern: 'opencode/',
          case_sensitive: false,
          tls_fingerprint_profile_id: 7,
          upstream_user_agent: 'opencode/1.0 upstream',
          upstream_originator: 'opencode',
        },
      ],
    })
  })

  // A new router has a name but its added rule has no pattern. Saving reports
  // the required-pattern error and leaves the create endpoint uncalled.
  it('规则 pattern 为空时阻止保存', async () => {
    const wrapper = mountModal()
    await flushPromises()

    await wrapper
      .findAll('button')
      .find((button) => button.text().includes('admin.tlsFingerprintRouters.createRouter'))!
      .trigger('click')
    await flushPromises()

    await wrapper.find('input[required]').setValue('Invalid Router')
    await wrapper
      .findAll('button')
      .find((button) => button.text().includes('admin.tlsFingerprintRouters.form.addRule'))!
      .trigger('click')
    await flushPromises()
    await wrapper
      .findAll('button')
      .find((button) => button.text().includes('common.create'))!
      .trigger('click')

    expect(createRouterMock).not.toHaveBeenCalled()
    expect(showErrorMock).toHaveBeenCalledWith('admin.tlsFingerprintRouters.form.patternRequired')
  })

  // A stored router contains token and invitation identities plus an ordered
  // rule. Copying the row exports those fields as YAML and supplies the
  // success message to the clipboard helper.
  it('可以将路由器配置导出为 YAML 并复制', async () => {
    const wrapper = mountModal()
    await flushPromises()

    await wrapper
      .findAll('button')
      .find((button) => button.attributes('title') === 'admin.tlsFingerprintRouters.copyYaml')!
      .trigger('click')
    await flushPromises()

    expect(copyToClipboardMock).toHaveBeenCalledWith(
      [
        'tls_fingerprint_router:',
        '  name: "Codex Router"',
        '  description: "UA based"',
        '  enabled: true',
        '  chatgpt_oauth_token_user_agent: "codex-token-ua/1.0"',
        '  chatgpt_oauth_token_tls_fingerprint_profile_id: 7',
        '  codex_invite_reset_user_agent: "Codex Desktop/0.135.0-alpha.1 (Windows 10.0.26200; x86_64)"',
        '  codex_invite_reset_tls_fingerprint_profile_id: 7',
        '  rules:',
        '    - name: "codex mac"',
        '      enabled: true',
        '      match_type: "contains"',
        '      pattern: "codex_cli"',
        '      case_sensitive: false',
        '      tls_fingerprint_profile_id: 7',
        '      upstream_user_agent: "codex_cli_rs/0.125.0"',
        '      upstream_originator: "codex_cli_rs"',
      ].join('\n'),
      'admin.tlsFingerprintRouters.copyYamlSuccess',
    )
  })

  // Imported YAML contains explicit identities and two differently configured
  // rules. Parsing and creating preserves their order and maps an unknown
  // match type to contains with empty optional headers.
  it('粘贴 YAML 后会自动解析并提交路由器配置', async () => {
    const wrapper = mountModal()
    await flushPromises()

    await wrapper
      .findAll('button')
      .find((button) => button.text().includes('admin.tlsFingerprintRouters.createRouter'))!
      .trigger('click')
    await flushPromises()

    await wrapper.find('textarea').setValue(importedRouterYaml)
    await wrapper
      .findAll('button')
      .find((button) => button.text().includes('admin.tlsFingerprintRouters.form.parseYaml'))!
      .trigger('click')
    await flushPromises()

    expect(showSuccessMock).toHaveBeenCalledWith('admin.tlsFingerprintRouters.form.yamlParsed')

    await wrapper
      .findAll('button')
      .find((button) => button.text().includes('common.create'))!
      .trigger('click')
    await flushPromises()

    expect(createRouterMock).toHaveBeenCalledWith({
      name: 'Imported Router',
      description: 'Imported from YAML',
      enabled: false,
      chatgpt_oauth_token_user_agent: 'import-token-ua/1.0',
      chatgpt_oauth_token_tls_fingerprint_profile_id: -1,
      codex_invite_reset_user_agent: 'import-codex-desktop-ua/1.0',
      codex_invite_reset_tls_fingerprint_profile_id: 7,
      rules: [
        {
          name: 'codex exact',
          enabled: true,
          match_type: 'exact',
          pattern: 'codex_cli_rs/0.125.0',
          case_sensitive: true,
          tls_fingerprint_profile_id: 7,
          upstream_user_agent: 'codex_cli_rs/0.125.0 upstream',
          upstream_originator: 'codex_cli_rs',
        },
        {
          name: 'fallback',
          enabled: false,
          match_type: 'contains',
          pattern: 'OpenAI',
          case_sensitive: false,
          tls_fingerprint_profile_id: -1,
          upstream_user_agent: '',
          upstream_originator: '',
        },
      ],
    })
  })

  // A paste event follows textarea input containing valid router YAML.
  // Deferred parsing populates the form before creation, and the submitted
  // router includes the imported identity and rule.
  it('触发粘贴事件后会自动解析 YAML', async () => {
    vi.useFakeTimers()
    const wrapper = mountModal()
    await flushPromises()

    await wrapper
      .findAll('button')
      .find((button) => button.text().includes('admin.tlsFingerprintRouters.createRouter'))!
      .trigger('click')
    await flushPromises()

    await wrapper.find('textarea').setValue(importedRouterYaml)
    await wrapper.find('textarea').trigger('paste')
    // Run the paste delay after updating textarea content so the parser sees
    // the pasted YAML rather than the draft preceding that event.
    await vi.runAllTimersAsync()
    await flushPromises()
    vi.useRealTimers()

    await wrapper
      .findAll('button')
      .find((button) => button.text().includes('common.create'))!
      .trigger('click')
    await flushPromises()

    expect(createRouterMock).toHaveBeenCalledWith(
      expect.objectContaining({
        name: 'Imported Router',
        enabled: false,
        chatgpt_oauth_token_user_agent: 'import-token-ua/1.0',
        chatgpt_oauth_token_tls_fingerprint_profile_id: -1,
        codex_invite_reset_user_agent: 'import-codex-desktop-ua/1.0',
        codex_invite_reset_tls_fingerprint_profile_id: 7,
        rules: expect.arrayContaining([
          expect.objectContaining({
            name: 'codex exact',
            match_type: 'exact',
            pattern: 'codex_cli_rs/0.125.0',
          }),
        ]),
      }),
    )
  })

  // An existing router draft receives YAML without a valid name and pattern.
  // Parsing reports failure, and the subsequent update still contains the
  // original draft's identity and rules.
  it('YAML 解析失败时不会清空编辑中的既有规则', async () => {
    const wrapper = mountModal()
    await flushPromises()

    await wrapper
      .findAll('button')
      .find((button) => button.attributes('title') === 'common.edit')!
      .trigger('click')
    await flushPromises()

    await wrapper
      .find('textarea')
      .setValue(
        [
          'tls_fingerprint_router:',
          '  description: "bad config without name"',
          '  rules:',
          '    - pattern: ""',
        ].join('\n'),
      )
    await wrapper
      .findAll('button')
      .find((button) => button.text().includes('admin.tlsFingerprintRouters.form.parseYaml'))!
      .trigger('click')
    await flushPromises()

    expect(showErrorMock).toHaveBeenCalledWith('admin.tlsFingerprintRouters.form.yamlParseFailed')

    await wrapper
      .findAll('button')
      .find((button) => button.text().includes('common.update'))!
      .trigger('click')
    await flushPromises()

    expect(updateRouterMock).toHaveBeenCalledWith(
      9,
      expect.objectContaining({
        name: 'Codex Router',
        description: 'UA based',
        chatgpt_oauth_token_user_agent: 'codex-token-ua/1.0',
        chatgpt_oauth_token_tls_fingerprint_profile_id: 7,
        codex_invite_reset_user_agent: 'Codex Desktop/0.135.0-alpha.1 (Windows 10.0.26200; x86_64)',
        codex_invite_reset_tls_fingerprint_profile_id: 7,
        rules: [
          {
            name: 'codex mac',
            enabled: true,
            match_type: 'contains',
            pattern: 'codex_cli',
            case_sensitive: false,
            tls_fingerprint_profile_id: 7,
            upstream_user_agent: 'codex_cli_rs/0.125.0',
            upstream_originator: 'codex_cli_rs',
          },
        ],
      }),
    )
  })

  // The administrator requests deletion of a listed router and confirms the
  // dialog. The delete endpoint receives that router's ID and a success
  // notification follows.
  it('删除路由器会调用删除接口', async () => {
    const wrapper = mountModal()
    await flushPromises()

    await wrapper
      .findAll('button')
      .find((button) => button.attributes('title') === 'common.delete')!
      .trigger('click')
    await flushPromises()
    await wrapper.get('[data-testid="confirm-delete"]').trigger('click')
    await flushPromises()

    expect(deleteRouterMock).toHaveBeenCalledWith(9)
    expect(showSuccessMock).toHaveBeenCalledWith('admin.tlsFingerprintRouters.deleteSuccess')
  })
})
