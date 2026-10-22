<template>
  <AppLayout>
    <TablePageLayout>
      <template v-if="confirmation || fileID || error" #actions>
        <div
          v-if="confirmation"
          class="mt-4 rounded-surface border border-primary-200 bg-primary-50 p-4 dark:border-primary-800 dark:bg-primary-950/30"
        >
          <div class="flex flex-wrap items-start justify-between gap-3">
            <div>
              <h2 class="font-semibold text-gray-900 dark:text-gray-100">{{
                t('admin.mcp.review')
              }}</h2>
              <p class="mt-1 text-sm text-gray-700 dark:text-gray-300">{{
                operationLabel(confirmation.operation)
              }}</p>
              <p class="mt-1 font-mono text-xs text-gray-500 dark:text-gray-400">{{
                confirmation.operation.operation_id
              }}</p>
            </div>
            <button
              v-if="confirmation.state === 'pending'"
              class="btn btn-primary"
              type="button"
              :disabled="approving"
              @click="approve"
            >
              {{ approving ? t('common.processing') : t('admin.mcp.approve') }}
            </button>
            <p v-else class="text-sm text-primary-700 dark:text-primary-300" role="status">
              {{
                t(confirmation.state === 'approved' ? 'admin.mcp.approved' : 'admin.mcp.consumed')
              }}
            </p>
          </div>
          <details class="mt-3 text-sm">
            <summary class="cursor-pointer text-gray-700 dark:text-gray-300">{{
              t('admin.mcp.operationDetails')
            }}</summary>
            <p class="mt-2 whitespace-pre-wrap text-gray-600 dark:text-gray-400">{{
              confirmation.operation.description
            }}</p>
          </details>
          <details open class="mt-3 text-sm">
            <summary class="cursor-pointer text-gray-700 dark:text-gray-300">{{
              t('admin.mcp.preview')
            }}</summary>
            <pre
              class="mt-2 max-h-64 overflow-auto whitespace-pre-wrap break-all rounded-control bg-white p-3 text-xs dark:bg-dark-900"
              >{{ formattedPreview }}</pre>
          </details>
        </div>
        <div
          v-if="fileID"
          class="mt-4 flex flex-wrap items-center justify-between gap-3 rounded-surface border border-gray-200 p-4 dark:border-dark-700"
        >
          <div>
            <h2 class="font-medium">{{ t('admin.mcp.fileTitle') }}</h2>
            <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">{{
              t('admin.mcp.downloadHint')
            }}</p>
          </div>
          <button class="btn btn-secondary" type="button" :disabled="downloading" @click="download">
            {{ downloading ? t('common.processing') : t('admin.mcp.download') }}
          </button>
        </div>
        <p v-if="error" class="mt-3 text-sm text-red-600 dark:text-red-400" role="alert">{{
          error
        }}</p>
      </template>
      <template #filters>
        <div class="flex flex-col gap-2">
          <div class="flex flex-wrap items-center justify-between gap-2">
            <div class="input-icon-wrap w-full sm:w-72">
              <Icon name="search" size="md" class="input-icon text-gray-400" />
              <input
                v-model="search"
                class="input input-has-icon"
                :placeholder="t('admin.mcp.search')"
                :aria-label="t('admin.mcp.search')"
              />
            </div>
            <div class="ml-auto flex gap-2">
              <button
                class="btn btn-secondary btn-icon"
                type="button"
                :disabled="loading"
                :title="t('admin.mcp.refresh')"
                :aria-label="t('admin.mcp.refresh')"
                @click="load"
                ><Icon name="refresh" size="sm" :class="loading ? 'animate-spin' : ''"
              /></button>
              <button class="btn btn-secondary" type="button" @click="connectionOpen = true">
                <Icon name="link" size="sm" class="mr-2" />{{ t('admin.mcp.connection') }}
              </button>
              <button
                class="btn btn-primary"
                type="button"
                :disabled="loading || !operations.length"
                @click="openEditor()"
              >
                <Icon name="plus" size="sm" class="mr-2" />{{ t('admin.mcp.create') }}
              </button>
            </div>
          </div>
          <EndpointPopover
            class="-mb-2"
            api-base-url=""
            :custom-endpoints="[{ name: t('admin.mcp.endpoint'), endpoint, description: '' }]"
            :show-speed-test="false"
          />
        </div>
      </template>
      <template #table>
        <DataTable
          column-order-storage-key="admin-mcp-column-order"
          :columns="columns"
          :data="visibleTokens"
          :loading="loading"
          row-key="id"
        >
          <template #cell-name="{ row }">
            <div class="font-medium text-gray-900 dark:text-gray-100">{{ row.name }}</div>
          </template>
          <template #cell-prefix="{ value, row }">
            <div class="flex items-center gap-2">
              <code class="code text-xs">{{ value }}…</code>
              <button
                v-if="!row.revoked_at"
                type="button"
                class="rounded-compact p-1 transition-colors"
                :class="
                  copiedTokenID === row.id
                    ? 'text-green-500'
                    : 'text-gray-400 hover:text-gray-600 dark:hover:text-gray-300'
                "
                :disabled="copyingTokenID !== null"
                :title="copiedTokenID === row.id ? t('keys.copied') : t('keys.copyToClipboard')"
                :aria-label="t('keys.copyToClipboard')"
                @click="copyToken(row)"
              >
                <Icon
                  v-if="copiedTokenID === row.id"
                  name="check"
                  size="sm"
                  :stroke-width="2"
                  :animate-on-hover="false"
                />
                <Icon v-else name="clipboard" size="sm" />
              </button>
            </div>
          </template>
          <template #cell-permissions="{ row }">
            <span class="text-sm">{{
              t('admin.mcp.permissionCount', { count: row.permissions.length })
            }}</span>
            <p
              v-if="row.skip_browser_confirmation"
              class="mt-0.5 text-xs text-gray-500 dark:text-gray-400"
              >{{ t('admin.mcp.confirmationSkipped') }}</p
            >
          </template>
          <template #cell-enabled="{ row }">
            <span
              :class="[
                'badge gap-1',
                row.revoked_at || isExpired(row)
                  ? 'badge-danger'
                  : row.enabled
                    ? 'badge-success'
                    : 'badge-gray',
              ]"
            >
              <span class="self-baseline">{{ tokenStatus(row) }}</span>
            </span>
          </template>
          <template #cell-expires_at="{ value }"
            ><span class="whitespace-nowrap tabular-nums">{{
              formatTime(value, t('admin.mcp.never'))
            }}</span></template
          >
          <template #cell-last_used_at="{ value }"
            ><span class="whitespace-nowrap tabular-nums">{{
              formatTime(value, t('admin.mcp.unused'))
            }}</span></template
          >
          <template #cell-actions="{ row }">
            <div class="flex items-center gap-1">
              <button
                v-if="!row.revoked_at"
                type="button"
                :disabled="busyTokens.has(row.id)"
                class="flex flex-col items-center gap-0.5 rounded-control p-1.5 text-gray-500 transition-colors hover:bg-gray-100 hover:text-primary-600 dark:hover:bg-dark-700 dark:hover:text-primary-400"
                @click="openEditor(row)"
                ><Icon name="edit" size="sm" /><span class="text-xs">{{
                  t('common.edit')
                }}</span></button
              >
              <button
                v-if="!row.revoked_at && !isExpired(row)"
                type="button"
                :disabled="busyTokens.has(row.id)"
                :class="[
                  'flex flex-col items-center gap-0.5 rounded-control p-1.5 transition-colors',
                  row.enabled
                    ? 'text-gray-500 hover:bg-yellow-50 hover:text-yellow-600 dark:hover:bg-yellow-900/20 dark:hover:text-yellow-400'
                    : 'text-gray-500 hover:bg-green-50 hover:text-green-600 dark:hover:bg-green-900/20 dark:hover:text-green-400',
                ]"
                @click="toggleToken(row, !row.enabled)"
              >
                <Icon v-if="row.enabled" name="ban" size="sm" />
                <Icon v-else name="checkCircle" size="sm" :animate-on-hover="false" />
                <span class="text-xs">{{
                  row.enabled ? t('keys.disable') : t('keys.enable')
                }}</span>
              </button>
              <button
                type="button"
                class="flex flex-col items-center gap-0.5 rounded-control p-1.5 text-gray-500 transition-colors hover:bg-gray-100 hover:text-gray-900 dark:hover:bg-dark-700 dark:hover:text-white"
                :class="{
                  'bg-gray-100 text-gray-900 dark:bg-dark-700 dark:text-white':
                    actionToken?.id === row.id,
                }"
                aria-haspopup="menu"
                :disabled="busyTokens.has(row.id)"
                :aria-expanded="actionToken?.id === row.id"
                :aria-controls="
                  actionToken?.id === row.id ? `mcp-action-menu-${row.id}` : undefined
                "
                @click="openActionMenu(row, $event)"
                ><Icon name="more" size="sm" /><span class="text-xs">{{
                  t('common.more')
                }}</span></button
              >
            </div>
          </template>
          <template #empty>
            <div class="flex flex-col items-center gap-2 p-8 text-center">
              <Icon name="key" size="xl" class="text-gray-400" />
              <p class="font-medium text-gray-900 dark:text-gray-100">{{
                search ? t('admin.mcp.noResults') : t('admin.mcp.empty')
              }}</p>
              <p v-if="!search" class="max-w-sm text-sm text-gray-500 dark:text-gray-400">{{
                t('admin.mcp.emptyHint')
              }}</p>
            </div>
          </template>
        </DataTable>
      </template>
    </TablePageLayout>

    <Teleport to="body">
      <div
        v-if="actionToken"
        class="fixed inset-0 z-menu-overlay"
        aria-hidden="true"
        @click="closeActionMenu"
      ></div>
      <MotionTransition name="dropdown-fade">
        <div
          v-if="actionToken"
          :id="`mcp-action-menu-${actionToken.id}`"
          class="action-menu w-48 overflow-hidden"
          :style="{ top: `${actionPosition.top}px`, left: `${actionPosition.left}px` }"
          role="menu"
          :aria-label="t('common.actions')"
          @click.stop
        >
          <div class="py-1">
            <RouterLink
              :to="{
                path: '/admin/audit-logs',
                query: { q: actionToken.prefix, auth_method: 'mcp_token' },
              }"
              class="dropdown-item"
              role="menuitem"
              @click="closeActionMenu"
              ><Icon name="shieldCheck" size="sm" class="text-primary-500" :stroke-width="2" />{{
                t('admin.mcp.audit')
              }}</RouterLink
            >
            <template v-if="!actionToken.revoked_at">
              <div class="my-1 border-t border-gray-100 dark:border-dark-700"></div>
              <button
                type="button"
                class="dropdown-item text-red-600 dark:text-red-400"
                role="menuitem"
                @click="requestRevoke"
              >
                <Icon
                  name="trash"
                  size="sm"
                  class="text-red-500 dark:text-red-400"
                  :stroke-width="2"
                />{{ t('admin.mcp.revoke') }}
              </button>
            </template>
          </div>
        </div>
      </MotionTransition>
    </Teleport>

    <BaseDialog
      :show="editorOpen"
      :title="t(editingID ? 'admin.mcp.edit' : 'admin.mcp.create')"
      width="wide"
      @close="closeEditor"
    >
      <form id="mcp-token-form" class="space-y-5" @submit.prevent="save">
        <div class="grid gap-4 sm:grid-cols-2">
          <label class="block"
            ><span class="input-label">{{ t('admin.mcp.name') }}</span
            ><input
              v-model.trim="form.name"
              class="input"
              required
              maxlength="120"
              autocomplete="off"
          /></label>
          <div
            ><label class="input-label" for="mcp-expiry">{{ t('admin.mcp.expires') }}</label
            ><DateTimePicker
              id="mcp-expiry"
              v-model="form.expiry"
              :placeholder="t('admin.mcp.never')"
          /></div>
        </div>
        <div class="flex items-center justify-between gap-4">
          <div>
            <label class="input-label mb-0" for="mcp-skip-confirmation">{{
              t('admin.mcp.skipBrowserConfirmation')
            }}</label>
            <p class="input-hint">{{ t('admin.mcp.skipConfirmationHint') }}</p>
          </div>
          <Toggle
            id="mcp-skip-confirmation"
            v-model="form.skipBrowserConfirmation"
            size="sm"
            :aria-label="t('admin.mcp.skipBrowserConfirmation')"
          />
        </div>
        <div class="space-y-3">
          <div class="flex flex-wrap items-center justify-between gap-2">
            <h3 class="input-label mb-0">{{ t('admin.mcp.permissions') }}</h3>
            <span class="text-xs text-gray-500 dark:text-dark-400" aria-live="polite">{{
              t('admin.mcp.selected', { count: selected.size })
            }}</span>
          </div>
          <p class="input-hint">{{ t('admin.mcp.permissionHint') }}</p>
          <div>
            <label class="input-label">{{ t('admin.mcp.preset') }}</label>
            <Select
              :model-value="selectedPreset"
              :options="presetOptions"
              :aria-label="t('admin.mcp.preset')"
              @update:model-value="applyPreset"
            />
            <p class="input-hint">{{ t(`admin.mcp.presetHints.${selectedPreset}`) }}</p>
          </div>
          <p v-if="unknownPermissions" class="text-sm text-amber-700 dark:text-amber-400">{{
            t('admin.mcp.unknownPermissions')
          }}</p>
          <button
            type="button"
            class="input-label mb-0 flex items-center gap-2"
            :aria-expanded="permissionsExpanded"
            @click="permissionsExpanded = !permissionsExpanded"
          >
            <Icon
              name="chevronDown"
              size="sm"
              class="transition-transform duration-fast"
              :class="permissionsExpanded ? 'rotate-180' : ''"
            />
            {{ t('admin.mcp.adjustPermissions') }}
          </button>
          <div v-if="permissionsExpanded" class="space-y-3">
            <div class="grid gap-2 sm:grid-cols-2">
              <input
                v-model="permissionSearch"
                class="input"
                :placeholder="t('admin.mcp.permissionSearch')"
                :aria-label="t('admin.mcp.permissionSearch')"
              />
              <Select
                v-model="moduleFilter"
                :options="moduleOptions"
                :aria-label="t('admin.mcp.allModules')"
              />
            </div>
            <label class="flex items-center gap-2 text-sm"
              ><input v-model="selectedOnly" type="checkbox" class="checkbox" />{{
                t('admin.mcp.selectedOnly')
              }}</label
            >
            <div
              class="max-h-80 overflow-y-auto rounded-surface border border-gray-200 dark:border-dark-700"
            >
              <section
                v-for="group in permissionGroups"
                :key="group.name"
                class="border-b border-gray-100 last:border-0 dark:border-dark-800"
              >
                <div
                  class="sticky top-0 z-10 flex items-center justify-between gap-3 bg-gray-50 px-4 py-2 dark:bg-dark-800"
                >
                  <h4 class="input-label mb-0">{{ moduleLabel(group.name) }}</h4>
                  <button
                    class="text-xs text-primary-800 hover:text-primary-900 dark:text-primary-400 dark:hover:text-primary-300"
                    type="button"
                    @click="selectGroup(group.items)"
                    >{{
                      t(
                        group.items.every((op) => selected.has(op.operation_id))
                          ? 'admin.mcp.clearVisible'
                          : 'admin.mcp.selectVisible',
                      )
                    }}</button
                  >
                </div>
                <div v-for="operation in group.items" :key="operation.operation_id">
                  <label
                    class="flex cursor-pointer items-start gap-3 px-4 py-3 transition-colors duration-fast hover:bg-gray-50 dark:hover:bg-dark-800/50"
                  >
                    <input
                      :checked="selected.has(operation.operation_id)"
                      class="checkbox mt-1"
                      type="checkbox"
                      @change="
                        selectOperation(
                          operation.operation_id,
                          ($event.target as HTMLInputElement).checked,
                        )
                      "
                    />
                    <span class="min-w-0 flex-1">
                      <span class="flex flex-wrap items-center justify-between gap-2 text-sm"
                        ><span>{{ operationLabel(operation) }}</span
                        ><span
                          v-if="operation.sensitive"
                          class="text-xs text-amber-700 dark:text-amber-400"
                          >{{
                            t(
                              form.skipBrowserConfirmation
                                ? 'admin.mcp.sensitiveOperation'
                                : 'admin.mcp.sensitive',
                            )
                          }}</span
                        ><span v-else class="text-xs text-gray-500 dark:text-gray-400">{{
                          t(operation.read_only ? 'admin.mcp.read' : 'admin.mcp.write')
                        }}</span></span
                      >
                      <span
                        class="mt-1 block break-all font-mono text-xs text-gray-500 dark:text-gray-400"
                        >{{ operation.operation_id }}</span
                      >
                    </span>
                  </label>
                  <details v-if="operation.sensitive" class="px-4 pb-3 text-sm">
                    <summary class="cursor-pointer text-gray-600 dark:text-gray-400">{{
                      t('admin.mcp.operationDetails')
                    }}</summary>
                    <p class="mt-2 whitespace-pre-wrap text-gray-600 dark:text-gray-400">{{
                      operation.description
                    }}</p>
                  </details>
                </div>
              </section>
              <p v-if="!permissionGroups.length" class="p-6 text-center text-sm text-gray-500">{{
                t('admin.mcp.noResults')
              }}</p>
            </div>
            <p v-if="!form.skipBrowserConfirmation" class="input-hint">{{
              t('admin.mcp.sensitiveHint')
            }}</p>
          </div>
        </div>
        <p v-if="formError" class="text-sm text-red-600 dark:text-red-400" role="alert">{{
          formError
        }}</p>
      </form>
      <template #footer
        ><div class="flex justify-end gap-2"
          ><button
            class="btn btn-secondary"
            type="button"
            :disabled="saving"
            @click="closeEditor"
            >{{ t('common.cancel') }}</button
          ><button class="btn btn-primary" type="submit" form="mcp-token-form" :disabled="saving">{{
            saving ? t('common.saving') : t('common.save')
          }}</button></div
        ></template
      >
    </BaseDialog>

    <BaseDialog
      :show="!!credential"
      :title="t('admin.mcp.credentialTitle')"
      @close="clearCredential"
    >
      <p class="mb-4 text-sm text-gray-600 dark:text-gray-400">{{
        t('admin.mcp.credentialHint')
      }}</p>
      <pre
        class="whitespace-pre-wrap break-all rounded-surface bg-gray-100 p-4 font-mono text-sm dark:bg-dark-800"
        >{{ credential }}</pre>
      <div class="mt-4 flex flex-wrap gap-2"
        ><button class="btn btn-primary" type="button" @click="copyToClipboard(credential)">{{
          t('admin.mcp.copyToken')
        }}</button
        ><button
          class="btn btn-secondary"
          type="button"
          @click="copyToClipboard(connectionText(credential))"
          >{{ t('admin.mcp.copyConfig') }}</button
        ></div
      >
    </BaseDialog>
    <BaseDialog
      :show="connectionOpen"
      :title="t('admin.mcp.connection')"
      @close="connectionOpen = false"
    >
      <pre
        class="overflow-auto whitespace-pre-wrap break-all rounded-surface bg-gray-100 p-4 text-sm dark:bg-dark-800"
        >{{ connectionText() }}</pre>
      <button
        class="btn btn-secondary mt-4"
        type="button"
        @click="copyToClipboard(connectionText())"
        >{{ t('admin.mcp.copyConfig') }}</button
      >
      <button
        type="button"
        class="btn btn-secondary mt-4 ml-2"
        :disabled="downloading"
        @click="downloadSpec"
        >{{ t('admin.mcp.downloadSpec') }}</button
      >
    </BaseDialog>
    <ConfirmDialog
      :show="!!revoking"
      :title="t('admin.mcp.revoke')"
      :message="t('admin.mcp.revokeMessage', { name: revoking?.name })"
      :confirm-text="t('admin.mcp.revoke')"
      danger
      :loading="!!revoking && busyTokens.has(revoking.id)"
      @confirm="revoke"
      @cancel="revoking = null"
    />
    <TotpStepUpDialog :controller="stepUp" />
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, reactive, ref, watch } from 'vue'
import { RouterLink, useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { saveAs } from 'file-saver'
import AppLayout from '@/components/layout/AppLayout.vue'
import TablePageLayout from '@/components/layout/TablePageLayout.vue'
import DataTable from '@/components/common/table/DataTable.vue'
import type { Column } from '@/components/common/table/types'
import MotionTransition from '@/components/common/MotionTransition.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import DateTimePicker from '@/components/common/DateTimePicker.vue'
import Select from '@/components/common/Select.vue'
import Toggle from '@/components/common/Toggle.vue'
import Icon from '@/components/icons/Icon.vue'
import EndpointPopover from '@/components/keys/EndpointPopover.vue'
import TotpStepUpDialog from '@/components/auth/TotpStepUpDialog.vue'
import { useStepUp, isStepUpCancelled } from '@/composables/useStepUp'
import { useClipboard } from '@/composables/useClipboard'
import { useAppStore } from '@/stores/app'
import * as api from '@/api/admin/mcp'
import { buildGatewayUrl } from '@/api/client'
import { COPY_FEEDBACK_MS } from '@/constants/ui'
import { getFloatingPanelPosition } from '@/utils/floatingPanel'
import { formatDateTime } from '@/utils/format'
import type { MCPToken, MCPTokenInput, MCPOperation, MCPConfirmation } from '@/api/admin/mcp'
import { permissionPresets } from './mcp/permissions'

// McpManagementView.vue manages administrator-owned agent credentials and reviews
// incoming confirmation links. Permission choices come from the backend operation
// catalogue, while token creation keeps the returned secret only in this mounted
// view. Closing the delivery dialog or leaving the page clears that value.
//
// Reloads cancel their preceding request group before publishing token and
// operation snapshots. Mutation failures keep the editor draft intact; successful
// creation publishes its credential before refreshing the list so a later read
// failure cannot interrupt immediate setup. Row copies fetch the complete secret
// through an owner-authenticated endpoint and keep it local to that copy attempt.
// Browser confirmation uses the existing TOTP flow and leaves the approved action for the agent to retry.

// Shared UI services supply locale, browser-route state, feedback, and strong authentication.
const { t, te, locale } = useI18n()
const route = useRoute()
const app = useAppStore()
const stepUp = useStepUp()
const { copyToClipboard } = useClipboard()

// Page snapshots and pending flags remain local to this administrator view.
const tokens = ref<MCPToken[]>([])
const operations = ref<MCPOperation[]>([])
const confirmation = ref<MCPConfirmation | null>(null)
const loading = ref(false)
const error = ref('')
const search = ref('')
const editorOpen = ref(false)
const connectionOpen = ref(route.query.connection === '1')
const editingID = ref<number | null>(null)
const credential = ref('')
const saving = ref(false)
const approving = ref(false)
const downloading = ref(false)
// Each token retains its own mutation lock until write and readback finish;
// another row's completion cannot unlock this token or expose an obsolete editor snapshot.
const busyTokens = ref(new Set<number>())
const editingVersion = ref<string | undefined>()
// Copy feedback and menu state belong to this page; disposal releases timers and listeners.
const copyingTokenID = ref<number | null>(null)
const copiedTokenID = ref<number | null>(null)
let copyTimer: ReturnType<typeof setTimeout> | undefined
const actionToken = ref<MCPToken | null>(null)
const actionPosition = ref({ top: 0, left: 0 })
const revoking = ref<MCPToken | null>(null)
const formError = ref('')
const unknownPermissions = ref(false)
// The editor retains the saved admission flag while row actions control status; editing configuration must not re-enable a disabled token.
const form = reactive({ name: '', expiry: '', enabled: true, skipBrowserConfirmation: false })
const selected = ref(new Set<string>())
// selectedPreset is an editor convenience; persisted grants always use selected operation IDs.
const selectedPreset = ref('custom')
// permissionsExpanded keeps manual controls available after a preset initializes the draft.
const permissionsExpanded = ref(true)
const permissionSearch = ref('')
const moduleFilter = ref('')
const selectedOnly = ref(false)
let loadController: AbortController | null = null
let disposed = false

// endpoint uses the configured API origin so separate frontend hosts show the gateway address.
const endpoint = buildGatewayUrl('/mcp')
const fileID = computed(() => (typeof route.query.file_id === 'string' ? route.query.file_id : ''))
const visibleTokens = computed(() =>
  tokens.value.filter((token) =>
    `${token.name} ${token.prefix}`.toLowerCase().includes(search.value.toLowerCase()),
  ),
)
const columns = computed<Column[]>(() => [
  { key: 'name', label: t('admin.mcp.name') },
  { key: 'prefix', label: t('admin.mcp.token') },
  { key: 'permissions', label: t('admin.mcp.permissions') },
  { key: 'enabled', label: t('common.status') },
  { key: 'expires_at', label: t('admin.mcp.expires') },
  { key: 'last_used_at', label: t('admin.mcp.lastUsed') },
  { key: 'actions', label: t('common.actions') },
])
// presets expand the mounted catalogue only when the administrator chooses a preset.
const presets = computed(() => permissionPresets(operations.value))
const presetOptions = computed(() => [
  { value: 'custom', label: t('admin.mcp.presets.custom') },
  ...presets.value.map((preset) => ({
    value: preset.id,
    label: `${t(`admin.mcp.presets.${preset.id}`)} · ${t('admin.mcp.permissionCount', { count: preset.permissions.length })}`,
  })),
])

// applyPreset replaces the complete permission draft with one current preset.
// Search and module filters do not narrow this selection. Choosing custom keeps
// the current grants available for manual adjustment instead of adding future permissions.
function applyPreset(value: string | number | boolean | null) {
  const id = String(value ?? 'custom')
  selectedPreset.value = id
  const preset = presets.value.find((item) => item.id === id)
  if (preset) selected.value = new Set(preset.permissions)
  permissionsExpanded.value = !preset
}
const moduleOptions = computed(() => [
  { value: '', label: t('admin.mcp.allModules') },
  ...[...new Set(operations.value.map((op) => op.module))]
    .sort()
    .map((module) => ({ value: module, label: moduleLabel(module) })),
])

// permissionGroups groups the visible matches without changing hidden selected grants.
const permissionGroups = computed(() => {
  const groups = new Map<string, MCPOperation[]>()
  for (const operation of operations.value) {
    if (moduleFilter.value && operation.module !== moduleFilter.value) continue
    if (selectedOnly.value && !selected.value.has(operation.operation_id)) continue
    if (
      !`${operation.title} ${operation.title_zh ?? ''} ${operation.operation_id}`
        .toLowerCase()
        .includes(permissionSearch.value.toLowerCase())
    )
      continue
    const items = groups.get(operation.module) ?? []
    items.push(operation)
    groups.set(operation.module, items)
  }
  return [...groups].map(([name, items]) => ({ name, items }))
})

// moduleLabel resolves catalogue grouping names through presentation-only translations.
function moduleLabel(module: string): string {
  const key = `admin.mcp.modules.${module}`
  return te(key) ? t(key) : module
}

// operationLabel displays a translated action when the catalogue supplies one.
function operationLabel(operation: MCPOperation): string {
  return locale.value.startsWith('zh') && operation.title_zh ? operation.title_zh : operation.title
}

// formattedPreview displays the server's formatted, redacted text without decoding numeric identifiers.
const formattedPreview = computed(() => confirmation.value?.preview ?? '')

// messageOf chooses server feedback when present and otherwise uses a local translated message.
function messageOf(err: unknown, fallback: string): string {
  return err && typeof err === 'object' && 'message' in err && typeof err.message === 'string'
    ? err.message
    : fallback
}

// load fetches the token list, operation catalogue, and optional browser review for the
// current route. Starting another load aborts the preceding request group. Responses publish
// only while their controller remains active, so a delayed confirmation read cannot
// overwrite a newer link.
//
// Previously loaded rows remain available when refresh fails, accompanied by an error
// message. Component disposal aborts the remaining reads, and only the current controller
// can clear loading after its request group settles.
async function load() {
  if (disposed) return
  loadController?.abort()
  const controller = new AbortController()
  loadController = controller
  closeActionMenu()
  loading.value = true
  error.value = ''
  confirmation.value = null
  try {
    const [rows, catalogue] = await Promise.all([
      api.listTokens(controller.signal),
      api.listOperations(controller.signal),
    ])
    if (controller.signal.aborted) return
    tokens.value = rows
    operations.value = catalogue
    if (typeof route.query.confirmation_id === 'string') {
      const review = await api.getConfirmation(route.query.confirmation_id, controller.signal)
      if (!controller.signal.aborted) confirmation.value = review
    }
  } catch (err) {
    if (!controller.signal.aborted) error.value = messageOf(err, t('admin.mcp.loadFailed'))
  } finally {
    if (loadController === controller) loading.value = false
  }
}

// copyToken retrieves a complete credential only after the administrator selects its row.
// Requests are serialized to keep row feedback tied to the clipboard write that
// actually completed. The response stays in a function-local value, and disposal
// before retrieval completes prevents a delayed write to the user's clipboard.
// Successful feedback replaces the prior timer so an earlier copy cannot clear a
// later row's checkmark; failed requests leave the clipboard untouched and expose
// the server's message through the page alert.
async function copyToken(token: MCPToken) {
  if (copyingTokenID.value !== null || token.revoked_at) return
  copyingTokenID.value = token.id
  error.value = ''
  try {
    const secret = await api.getCredential(token.id)
    if (disposed) return
    const copied = await copyToClipboard(secret)
    if (disposed || !copied) return
    // Reset the previous feedback timer before assigning the new row, so older feedback cannot erase this result.
    clearTimeout(copyTimer)
    copiedTokenID.value = token.id
    copyTimer = setTimeout(() => {
      copiedTokenID.value = null
    }, COPY_FEEDBACK_MS)
  } catch (err) {
    if (!disposed) error.value = messageOf(err, t('admin.mcp.requestFailed'))
  } finally {
    copyingTokenID.value = null
  }
}

// openActionMenu positions the row's audit and revoke choices using the shared
// floating-panel geometry. Reopening the same trigger dismisses its menu, and
// revoked rows display only their audit action.
function openActionMenu(token: MCPToken, event: MouseEvent) {
  if (busyTokens.value.has(token.id)) return
  if (actionToken.value?.id === token.id) return closeActionMenu()
  const target = event.currentTarget as HTMLElement | null
  if (!target) return
  // Reserve space for both action rows and their divider so viewport placement keeps the complete menu accessible.
  const position = getFloatingPanelPosition(
    target.getBoundingClientRect(),
    window.innerWidth,
    window.innerHeight,
    {
      maxWidth: 192,
      fixedHeight: token.revoked_at ? 48 : 97,
      viewportPadding: 8,
      gap: 4,
      pinLeftOnMobile: false,
    },
  )
  actionPosition.value = { top: position.top ?? 8, left: position.left }
  actionToken.value = token
}

// closeActionMenu dismisses the overlay and releases the selected row reference.
function closeActionMenu() {
  actionToken.value = null
}

// requestRevoke transfers the chosen row to the existing destructive-action confirmation dialog.
function requestRevoke() {
  if (!actionToken.value || busyTokens.value.has(actionToken.value.id)) return
  revoking.value = actionToken.value
  closeActionMenu()
}

// handleMenuEscape dismisses the currently open menu on the standard Escape key.
function handleMenuEscape(event: KeyboardEvent) {
  if (event.key === 'Escape') closeActionMenu()
}

// The Escape listener exists only while the teleported menu is visible and is removed again during disposal.
watch(actionToken, (token) => {
  if (token) window.addEventListener('keydown', handleMenuEscape)
  else window.removeEventListener('keydown', handleMenuEscape)
})

// localDate converts an absolute timestamp into the shared date picker's local input shape.
function localDate(value: string): string {
  const date = new Date(value)
  return new Date(date.getTime() - date.getTimezoneOffset() * 60_000).toISOString().slice(0, 16)
}

// openEditor initializes an independent draft from saved metadata or creation defaults.
// Removed operation identifiers are omitted and reported in the form; selecting
// a module later affects matching visible operations without expanding future grants.
function openEditor(token?: MCPToken) {
  if (token && busyTokens.value.has(token.id)) return
  editingVersion.value = token?.updated_at
  editingID.value = token?.id ?? null
  form.name = token?.name ?? ''
  form.enabled = token?.enabled ?? true
  form.skipBrowserConfirmation = token?.skip_browser_confirmation ?? false
  form.expiry = token
    ? token.expires_at
      ? localDate(token.expires_at)
      : ''
    : localDate(new Date(Date.now() + 30 * 86400_000).toISOString())
  const known = new Set(operations.value.map((op) => op.operation_id))
  selected.value = new Set((token?.permissions ?? []).filter((id) => known.has(id)))
  selectedPreset.value = selected.value.size
    ? (presets.value.find(
        (preset) =>
          preset.permissions.length === selected.value.size &&
          preset.permissions.every((id) => selected.value.has(id)),
      )?.id ?? 'custom')
    : 'custom'
  permissionsExpanded.value = selectedPreset.value === 'custom'
  unknownPermissions.value = (token?.permissions.length ?? 0) !== selected.value.size
  permissionSearch.value = ''
  moduleFilter.value = ''
  selectedOnly.value = false
  formError.value = ''
  editorOpen.value = true
}

// closeEditor dismisses an idle draft while keeping in-flight secret creation visible.
function closeEditor() {
  if (!saving.value) editorOpen.value = false
}

// selectOperation replaces the selected set so permission summaries update together.
function selectOperation(id: string, checked: boolean) {
  selectedPreset.value = 'custom'
  const next = new Set(selected.value)
  if (checked) next.add(id)
  else next.delete(id)
  selected.value = next
}

// selectGroup selects or clears exactly the currently displayed matching operations.
function selectGroup(items: MCPOperation[]) {
  selectedPreset.value = 'custom'
  const checked = !items.every((op) => selected.value.has(op.operation_id))
  const next = new Set(selected.value)
  for (const operation of items) {
    if (checked) next.add(operation.operation_id)
    else next.delete(operation.operation_id)
  }
  selected.value = next
}

// save persists the editor draft with its observed configuration version, then
// refreshes the list. A stale version returns a conflict instead of replacing a
// completed status or permission change. Failed writes keep the entered fields
// available, and successful creation publishes its credential before list readback
// so an unrelated refresh failure cannot interrupt immediate client setup.
//
// The form submission guard and per-token lock remain held through readback.
// Other rows keep their own locks, and a response after component disposal cannot
// restore the creation credential into view state.
async function save() {
  const tokenID = editingID.value
  if (saving.value || (tokenID !== null && busyTokens.value.has(tokenID))) return
  if (tokenID !== null) busyTokens.value.add(tokenID)
  saving.value = true
  formError.value = ''
  try {
    const input: MCPTokenInput = {
      name: form.name,
      permissions: [...selected.value],
      enabled: form.enabled,
      skip_browser_confirmation: form.skipBrowserConfirmation,
      expires_at: form.expiry ? new Date(form.expiry).toISOString() : null,
    }
    if (tokenID !== null)
      await api.updateToken(tokenID, { ...input, expected_updated_at: editingVersion.value })
    else {
      const created = await api.createToken(input)
      if (!disposed) credential.value = created.credential
    }
    editorOpen.value = false
    app.showSuccess(t('admin.mcp.saved'))
    await load()
  } catch (err) {
    formError.value = messageOf(err, t('admin.mcp.saveFailed'))
  } finally {
    // Release this token only after its own mutation and list readback settle;
    // other rows keep their independent locks during concurrent operations.
    if (tokenID !== null) busyTokens.value.delete(tokenID)
    saving.value = false
  }
}

// toggleToken publishes saved admission state after the server accepts the change.
// The current row remains unchanged on failure, and the subsequent readback
// confirms the persistent state used by MCP authentication.
async function toggleToken(token: MCPToken, enabled: boolean) {
  if (busyTokens.value.has(token.id)) return
  busyTokens.value.add(token.id)
  try {
    await api.updateToken(token.id, {
      name: token.name,
      permissions: token.permissions,
      enabled,
      skip_browser_confirmation: token.skip_browser_confirmation,
      expires_at: token.expires_at,
      expected_updated_at: token.updated_at,
    })
    await load()
  } catch (err) {
    error.value = messageOf(err, t('admin.mcp.saveFailed'))
  } finally {
    busyTokens.value.delete(token.id)
  }
}

// revoke permanently disables the token selected in the confirmation dialog. Failure retains
// that selection and displays the server error; success closes the dialog and refreshes the
// owner's list.
async function revoke() {
  const token = revoking.value
  if (!token || busyTokens.value.has(token.id)) return
  busyTokens.value.add(token.id)
  try {
    await api.revokeToken(token.id)
    revoking.value = null
    await load()
  } catch (err) {
    error.value = messageOf(err, t('admin.mcp.requestFailed'))
  } finally {
    busyTokens.value.delete(token.id)
  }
}

// approve performs any required TOTP flow before recording browser confirmation.
// Approval does not execute the business action; the refreshed state tells the
// user that the agent can retry its original request with the confirmation ID.
async function approve() {
  if (!confirmation.value || approving.value) return
  const id = confirmation.value.id
  approving.value = true
  try {
    await stepUp.run(() => api.approveConfirmation(id))
    await load()
  } catch (err) {
    if (!isStepUpCancelled(err)) error.value = messageOf(err, t('admin.mcp.requestFailed'))
  } finally {
    approving.value = false
  }
}

// download retrieves protected bytes after satisfying the endpoint's current TOTP policy.
// A renewed browser grant applies to the retried archive request. Cancellation
// leaves the protected link available until its server-managed expiry.
async function download() {
  downloading.value = true
  try {
    const file = await stepUp.run(() => api.downloadFile(fileID.value))
    saveAs(file.blob, file.filename)
  } catch (err) {
    if (!isStepUpCancelled(err)) error.value = messageOf(err, t('admin.mcp.requestFailed'))
  } finally {
    downloading.value = false
  }
}

// downloadSpec saves the mounted management API description through the authenticated
// client. The shared busy state prevents a second download while it runs, and failures
// remain visible on the page.
async function downloadSpec() {
  downloading.value = true
  try {
    saveAs(await api.downloadSpecification(), 'tokenrouter-management.openapi.json')
  } catch (err) {
    error.value = messageOf(err, t('admin.mcp.requestFailed'))
  } finally {
    downloading.value = false
  }
}

// isExpired compares the saved expiry with the current browser clock.
function isExpired(token: MCPToken): boolean {
  return !!token.expires_at && Date.parse(token.expires_at) <= Date.now()
}

// tokenStatus gives permanent revocation precedence over expiry and the enabled switch.
function tokenStatus(token: MCPToken): string {
  return t(
    `admin.mcp.${token.revoked_at ? 'revoked' : isExpired(token) ? 'expired' : token.enabled ? 'active' : 'disabled'}`,
  )
}

// formatTime displays an absolute timestamp or its supplied empty-state label.
function formatTime(value: string | null, empty: string): string {
  return value ? formatDateTime(value) : empty
}

// connectionText describes the remote endpoint and authentication header for client setup.
function connectionText(secret = '<MCP_ACCESS_TOKEN>'): string {
  return JSON.stringify({ url: endpoint, headers: { Authorization: `Bearer ${secret}` } }, null, 2)
}

// clearCredential removes the only complete credential retained by this mounted page.
function clearCredential() {
  credential.value = ''
}

// A changed incoming workflow cancels stale reads before publishing its review state.
watch(() => [route.query.confirmation_id, route.query.file_id], load, { immediate: true })
onBeforeUnmount(() => {
  // Leaving the page cancels reads, settles a pending verification prompt, and
  // removes setup credential text so it cannot survive in a shared store.
  disposed = true
  clearTimeout(copyTimer)
  window.removeEventListener('keydown', handleMenuEscape)
  loadController?.abort()
  stepUp.onCancel()
  clearCredential()
})
</script>
