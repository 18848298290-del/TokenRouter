<template>
  <BaseDialog :show="show" :title="t('keys.useKeyModal.title')" width="wide" @close="emit('close')">
    <div class="space-y-4">
      <template v-if="compositeGroups?.length">
        <p class="text-sm text-gray-600 dark:text-gray-400">
          {{ t('keys.useKeyModal.compositeDescription') }}
        </p>
        <div
          v-for="binding in compositeGroups"
          :key="binding.group_id"
          class="rounded-control border border-gray-200 p-3 dark:border-dark-600"
        >
          <p class="text-sm font-medium">
            {{ binding.group?.display_name || binding.group?.name || `#${binding.group_id}` }}
          </p>
          <template v-if="binding.group?.models?.length">
            <code class="text-xs">{{ binding.prefix }}/{{ binding.group.models[0] }}</code>
            <button
              type="button"
              class="btn btn-secondary btn-sm ml-2"
              @click="copyContent(`${binding.prefix}/${binding.group.models[0]}`, binding.group_id)"
            >
              {{ t('keys.useKeyModal.copy') }}
            </button>
          </template>
          <p v-else class="input-hint">{{ t('keys.useKeyModal.noModels') }}</p>
        </div>
      </template>
      <div
        v-else-if="!group"
        class="rounded-control bg-amber-50 p-4 text-sm text-amber-800 dark:bg-amber-900/20 dark:text-amber-200"
      >
        <p class="font-medium">{{ t('keys.useKeyModal.noGroupTitle') }}</p>
        <p>{{ t('keys.useKeyModal.noGroupDescription') }}</p>
      </div>
      <div
        v-else-if="!clients.length"
        data-testid="no-text-protocols"
        class="text-sm text-gray-600 dark:text-gray-400"
      >
        {{ t('keys.useKeyModal.noTextProtocolsDescription') }}
      </div>
      <template v-else>
        <div class="flex flex-wrap gap-2" role="tablist">
          <button
            v-for="client in clients"
            :key="client"
            type="button"
            role="tab"
            :aria-selected="activeClient === client"
            class="btn btn-secondary btn-sm"
            :class="{ 'text-primary-600': activeClient === client }"
            @click="activeClient = client"
          >
            <Icon v-if="client === 'opencode'" name="terminal" size="sm" />
            <svg
              v-else
              width="16"
              height="16"
              viewBox="0 0 24 24"
              fill-rule="evenodd"
              class="shrink-0"
              aria-hidden="true"
            >
              <path
                v-for="(path, index) in modelIconData[CLIENT_ICON_KEYS[client]].paths"
                :key="index"
                :d="path"
                :fill="modelIconData[CLIENT_ICON_KEYS[client]].color"
              />
            </svg>
            {{ CLIENT_LABELS[client] }}
          </button>
        </div>
        <div class="grid gap-3 sm:grid-cols-2">
          <div :class="{ 'sm:col-span-2': activeClient === 'opencode' }">
            <label class="input-label">{{ t('keys.useKeyModal.model') }}</label>
            <Select
              v-model="selectedModel"
              :options="modelOptions"
              searchable
              :placeholder="t('keys.useKeyModal.selectModel')"
            />
            <p v-if="!modelOptions.length" class="input-hint">
              {{ t('keys.useKeyModal.noModels') }}
            </p>
          </div>
          <div v-if="activeClient !== 'opencode'">
            <label class="input-label">{{ t('keys.useKeyModal.shell') }}</label>
            <Select v-model="shell" :options="shellOptions" />
          </div>
        </div>
        <div v-if="activeClient === 'codex'" class="space-y-4">
          <div class="flex items-center justify-between gap-4">
            <div>
              <p class="text-sm font-medium">{{ t('keys.useKeyModal.directAuth') }}</p>
              <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
                {{ t('keys.useKeyModal.directAuthDescription') }}
              </p>
            </div>
            <Toggle v-model="directAuth" />
          </div>
          <div v-if="websocketAllowed" class="flex items-center justify-between gap-4">
            <div>
              <p class="text-sm font-medium">Responses WebSocket</p>
              <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
                {{ t('keys.useKeyModal.websocketDescription') }}
              </p>
            </div>
            <Toggle v-model="websocket" />
          </div>
        </div>
        <div
          v-for="(file, index) in files"
          :key="file.path"
          class="overflow-hidden rounded-control border border-gray-200 dark:border-dark-700"
        >
          <div
            class="flex items-center justify-between gap-3 bg-gray-50 px-4 py-2 dark:bg-dark-800"
          >
            <code class="text-xs">{{ file.path }}</code>
            <button
              type="button"
              class="btn btn-secondary btn-sm"
              @click="copyContent(file.content, index)"
            >
              {{
                copiedIndex === index ? t('keys.useKeyModal.copied') : t('keys.useKeyModal.copy')
              }}
            </button>
          </div>
          <pre
            class="overflow-x-auto whitespace-pre-wrap break-all p-4 text-xs"
          ><code>{{ file.content }}</code></pre>
        </div>
      </template>
    </div>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { ApiKeyCompositeGroup } from '@/api/keys.types'
import type { Group } from '@/api/groups.types'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Select from '@/components/common/Select.vue'
import Toggle from '@/components/common/Toggle.vue'
import Icon from '@/components/icons/Icon.vue'
import { useClipboard } from '@/composables/useClipboard'
import { COPY_FEEDBACK_MS } from '@/constants/ui'
import { modelIconData } from '@/utils/modelIconData'
import {
  availableClients,
  buildClientConfig,
  clientProtocol,
  CLIENT_ICON_KEYS,
  CLIENT_LABELS,
  modelsForProtocol,
  type ClientKind,
  type ConfigShell,
} from '@/utils/clientConfig'

// UseKeyModal.vue generates client setup instructions from a selected API key and its group
// capabilities. Client and model choices follow the available protocols, and configuration
// is produced only when the current model remains among the permitted options.
//
// Group-level websocket support is combined with model-specific protocol data before
// enabling that optional mode. Reactive watchers repair client and model selections after
// capability changes, while reopening resets shell and authentication switches. Generated
// content uses the configured base URL or page origin, and successful copies display a per-
// file confirmation whose timer belongs to this modal instance.

// The parent supplies the selected key secret, API base, and group capabilities used to
// build client setup files for the visible modal.
const props = defineProps<{
  show: boolean // Setup modal visibility.
  apiKey: string // Selected API-key secret.
  baseUrl: string // Configured API base URL.
  group?: Group | null // Selected group capability record.
  compositeGroups?: ApiKeyCompositeGroup[] // Composite key group bindings.
}>()
// emit requests modal dismissal; all client, model, shell, and copy selections remain local
// to this setup view.
const emit = defineEmits<{ close: [] /* Parent dismissal request. */ }>()
// Translation and clipboard dependencies label generated files and report copying while
// local selectors determine which client configuration is shown.
const { t } = useI18n()
const { copyToClipboard } = useClipboard()
// Client, model, shell, authentication, and websocket selections drive generated files;
// copiedIndex and copiedTimer own the latest per-file copy confirmation.
const activeClient = ref<ClientKind>('claude')
const selectedModel = ref('')
const shell = ref<ConfigShell>('unix')
const directAuth = ref(false)
const websocket = ref(false)
const copiedIndex = ref<number | null>(null)
let copiedTimer: ReturnType<typeof setTimeout> | undefined
// shellOptions offers the supported command syntaxes used by clientConfig when generating
// setup instructions for the selected client.
const shellOptions = [
  { value: 'unix', label: 'macOS / Linux' },
  { value: 'powershell', label: 'Windows PowerShell' },
  { value: 'cmd', label: 'Windows CMD' },
]
// Protocol and client choices derive from the group; model options retain actual permitted
// model identifiers for the selected client protocol.
const protocols = computed(() => props.group?.allowed_protocols ?? [])
const clients = computed(() => availableClients(protocols.value))
const protocol = computed(() => clientProtocol(activeClient.value, protocols.value))
const modelOptions = computed(() =>
  protocol.value
    ? modelsForProtocol(props.group ?? undefined, protocol.value).map((model) => ({
        value: model,
        label: model,
      }))
    : [],
)
// websocketAllowed requires group websocket support and, when model mappings exist,
// websocket support for the currently selected model as well.
const websocketAllowed = computed(
  () =>
    protocols.value.includes('openai_responses_websocket') &&
    (!props.group?.model_protocols ||
      props.group.model_protocols[selectedModel.value]?.includes('openai_responses_websocket')),
)
// files builds configuration only for an available protocol and selected listed model, using
// the current origin when the configured base URL is empty.
const files = computed(() =>
  protocol.value && modelOptions.value.some((option) => option.value === selectedModel.value)
    ? buildClientConfig({
        client: activeClient.value,
        protocol: protocol.value,
        model: selectedModel.value,
        baseUrl: props.baseUrl || window.location.origin,
        apiKey: props.apiKey,
        shell: shell.value,
        attributes: props.group?.model_attributes?.[selectedModel.value],
        directAuth: directAuth.value,
        websocket: websocketAllowed.value && websocket.value,
      })
    : [],
)

// Key capability changes can invalidate a client or model choice. Reconcile each
// selection with its current options before generating configuration for the modal.
watch(
  clients,
  (values) => {
    if (!values.includes(activeClient.value) && values[0]) activeClient.value = values[0]
  },
  { immediate: true },
)
watch(
  modelOptions,
  (values) => {
    if (!values.some((option) => option.value === selectedModel.value))
      selectedModel.value = values[0]?.value ?? ''
  },
  { immediate: true },
)
watch(
  () => props.show,
  (show) => {
    if (show) {
      shell.value = 'unix'
      directAuth.value = false
      websocket.value = false
    }
  },
)
// copyContent copies one generated file and marks its index after clipboard success. It
// replaces the previous feedback timeout, and component disposal clears the handle already
// registered. A clipboard completion after disposal can still update the marker and create
// another timer because the continuation does not check component lifetime.
async function copyContent(content: string, index: number) {
  if (!(await copyToClipboard(content))) return
  copiedIndex.value = index
  clearTimeout(copiedTimer)
  copiedTimer = setTimeout(() => {
    copiedIndex.value = null
  }, COPY_FEEDBACK_MS)
}
// Clear the feedback timeout currently registered when the modal unmounts. A pending
// clipboard continuation can still update the marker and create another timeout afterward.
onBeforeUnmount(() => clearTimeout(copiedTimer))
</script>
