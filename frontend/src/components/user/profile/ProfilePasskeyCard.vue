<template>
  <div class="card">
    <div
      class="flex items-end justify-between border-b border-gray-100 px-6 py-4 dark:border-dark-700"
    >
      <div>
        <h2 class="text-lg font-medium text-gray-900 dark:text-white">
          {{ t('profile.passkey.title') }}
        </h2>
        <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
          {{ t('profile.passkey.description') }}
        </p>
      </div>
      <button
        v-if="enabled && supported && !showAddForm"
        type="button"
        class="btn btn-primary"
        :disabled="busy"
        @click="showAddForm = true"
      >
        <Icon name="plus" size="sm" class="mr-2" />
        {{ t('profile.passkey.add') }}
      </button>
    </div>

    <div class="px-6 py-6">
      <div v-if="!enabled" class="text-sm text-gray-500 dark:text-gray-400">
        {{ t('profile.passkey.featureDisabled') }}
      </div>
      <div v-else-if="!supported" class="text-sm text-amber-600 dark:text-amber-400">
        {{ t('profile.passkey.unsupported') }}
      </div>
      <div v-else>
        <Collapse :open="showAddForm" unmount-on-hide>
          <form
            class="mb-5 flex flex-col gap-3 rounded-control border border-gray-200 p-4 dark:border-dark-700 sm:flex-row sm:items-end"
            @submit.prevent="addPasskey"
          >
            <div class="flex-1">
              <label for="passkey-name" class="input-label">{{ t('profile.passkey.name') }}</label>
              <input
                id="passkey-name"
                v-model="newName"
                class="input"
                maxlength="100"
                :placeholder="t('profile.passkey.namePlaceholder')"
                autofocus
              />
            </div>
            <div class="flex gap-2">
              <button type="button" class="btn btn-secondary" :disabled="busy" @click="cancelAdd">
                {{ t('common.cancel') }}
              </button>
              <button type="submit" class="btn btn-primary" :disabled="busy">
                {{ busy ? t('common.processing') : t('profile.passkey.continue') }}
              </button>
            </div>
          </form>
        </Collapse>

        <ContentSkeleton v-if="loading" variant="list" :rows="3" class="py-4" />

        <div
          v-else-if="credentials.length === 0"
          class="rounded-control border border-dashed border-gray-200 px-4 py-8 text-center text-sm text-gray-500 dark:border-dark-700 dark:text-gray-400"
        >
          {{ t('profile.passkey.empty') }}
        </div>

        <div v-else class="divide-y divide-gray-100 dark:divide-dark-700">
          <div
            v-for="credential in credentials"
            :key="credential.id"
            class="flex items-center justify-between gap-4 py-4 first:pt-0 last:pb-0"
          >
            <div class="min-w-0">
              <div class="flex items-center gap-2">
                <Icon name="key" size="md" class="shrink-0 text-primary-500" />
                <p class="truncate font-medium text-gray-900 dark:text-white">
                  {{ credential.name }}
                </p>
                <span
                  v-if="credential.backup"
                  class="rounded-full bg-green-50 px-2 py-0.5 text-xs text-green-700 dark:bg-green-900/30 dark:text-green-300"
                >
                  {{ t('profile.passkey.synced') }}
                </span>
              </div>
              <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
                {{ t('profile.passkey.createdAt', { date: formatDate(credential.created_at) }) }}
                <template v-if="credential.last_used_at">
                  ·
                  {{ t('profile.passkey.lastUsed', { date: formatDate(credential.last_used_at) }) }}
                </template>
              </p>
            </div>
            <div class="flex shrink-0 gap-2">
              <button
                type="button"
                class="btn btn-secondary btn-icon-sm"
                :disabled="busy"
                :aria-label="t('common.edit')"
                :title="t('common.edit')"
                @click="renamePasskey(credential)"
              >
                <Icon name="edit" size="sm" />
              </button>
              <button
                type="button"
                class="btn btn-ghost btn-sm text-red-600 hover:bg-red-50 dark:text-red-300 dark:hover:bg-red-950/30 btn-icon-sm"
                :disabled="busy"
                :aria-label="t('common.delete')"
                :title="t('common.delete')"
                @click="deletePasskey(credential)"
              >
                <Icon name="trash" size="sm" />
              </button>
            </div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import ContentSkeleton from '@/components/common/ContentSkeleton.vue'
import Collapse from '@/components/common/Collapse.vue'

import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { passkeyAPI, type PasskeyCredentialSummary } from '@/api'
import { Icon } from '@/components/icons'
import { useAppStore } from '@/stores/app'

// ProfilePasskeyCard.vue manages the current account's stored passkeys and their display
// names. Server availability and browser WebAuthn support control loading, while
// registration delegates the browser ceremony to passkeyAPI and refreshes the credential
// list after success.

// enabled reflects server passkey availability; browser support is checked separately before
// the component requests stored credentials or offers registration.
const props = defineProps<{ enabled: boolean /* Server passkey feature enabled. */ }>()

// Translations and app notifications describe passkey operations; local flags coordinate
// credential loading, action feedback, and the optional registration-name form.
const { t } = useI18n()
const appStore = useAppStore()
const supported = passkeyAPI.isSupported()
const loading = ref(false)
const busy = ref(false)
const showAddForm = ref(false)
const newName = ref('')
const credentials = ref<PasskeyCredentialSummary[]>([])

// loadCredentials requests passkeys when the server feature is enabled and WebAuthn is
// supported. Disabled settings clear the list, unsupported browsers skip loading, and other
// failures retain the existing list while reporting an error.
async function loadCredentials(): Promise<void> {
  if (!props.enabled) {
    credentials.value = []
    return
  }
  if (!supported) return
  loading.value = true
  try {
    credentials.value = await passkeyAPI.list()
  } catch (error) {
    // API reason carries the string error category. Settings may change after the
    // enabled prop was read, so PASSKEY_DISABLED suppresses an obsolete load error.
    const reason = (error as { reason?: string /* String API failure category. */ }).reason
    if (reason !== 'PASSKEY_DISABLED') {
      appStore.showError(t('profile.passkey.loadFailed'))
    }
  } finally {
    loading.value = false
  }
}

// addPasskey registers a credential using the trimmed optional name and then reloads the
// list. Browser NotAllowedError dismissals remain silent, while other failures show an error
// and the busy flag resets on completion.
async function addPasskey(): Promise<void> {
  busy.value = true
  try {
    await passkeyAPI.register(newName.value.trim())
    appStore.showSuccess(t('profile.passkey.added'))
    cancelAdd()
    await loadCredentials()
  } catch (error) {
    // Browser credential prompts reject with NotAllowedError when registration is not
    // allowed to complete; keep that dismissal quiet while reporting other registration
    // failures.
    if (!(error instanceof DOMException && error.name === 'NotAllowedError')) {
      appStore.showError(t('profile.passkey.addFailed'))
    }
  } finally {
    busy.value = false
  }
}

// cancelAdd closes the registration-name form and clears its draft label.
function cancelAdd(): void {
  showAddForm.value = false
  newName.value = ''
}

// renamePasskey prompts for a different nonempty label and saves it through the API. It
// updates the supplied credential object after success, leaving the existing label available
// if persistence fails.
async function renamePasskey(credential: PasskeyCredentialSummary): Promise<void> {
  const name = window.prompt(t('profile.passkey.renamePrompt'), credential.name)?.trim()
  if (!name || name === credential.name) return
  busy.value = true
  try {
    await passkeyAPI.rename(credential.id, name)
    credential.name = name
    appStore.showSuccess(t('profile.passkey.renamed'))
  } catch {
    appStore.showError(t('profile.passkey.renameFailed'))
  } finally {
    busy.value = false
  }
}

// deletePasskey asks for confirmation before removing a credential by ID. Successful
// deletion removes its local list entry; request failure retains the entry and reports an
// error through the app store.
async function deletePasskey(credential: PasskeyCredentialSummary): Promise<void> {
  if (!window.confirm(t('profile.passkey.deleteConfirm', { name: credential.name }))) return
  busy.value = true
  try {
    await passkeyAPI.remove(credential.id)
    credentials.value = credentials.value.filter((item) => item.id !== credential.id)
    appStore.showSuccess(t('profile.passkey.deleted'))
  } catch {
    appStore.showError(t('profile.passkey.deleteFailed'))
  } finally {
    busy.value = false
  }
}

// formatDate displays a credential timestamp with the browser's calendar date locale.
function formatDate(value: string): string {
  return new Intl.DateTimeFormat(undefined, {
    year: 'numeric',
    month: 'short',
    day: 'numeric',
  }).format(new Date(value))
}

watch(
  () => props.enabled,
  () => {
    void loadCredentials()
  },
  { immediate: true },
)
</script>
