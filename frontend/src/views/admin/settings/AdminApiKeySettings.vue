<template>
  <SettingsCard
    :title="t('admin.settings.adminApiKey.title')"
    :description="t('admin.settings.adminApiKey.description')"
  >
    <SettingsSection>
      <SettingsNotice tone="warning">
        {{ t('admin.settings.adminApiKey.securityWarning') }}
      </SettingsNotice>

      <ContentSkeleton v-if="adminApiKeyLoading" variant="form" :rows="1" />

      <div v-else-if="!adminApiKeyExists" class="flex items-center justify-between gap-4">
        <span class="text-sm text-primary-900/80 dark:text-dark-300">
          {{ t('admin.settings.adminApiKey.notConfigured') }}
        </span>
        <button
          type="button"
          :disabled="adminApiKeyOperating"
          class="btn btn-primary btn-sm h-9"
          @click="createAdminApiKey"
        >
          <Icon
            v-if="adminApiKeyOperating"
            name="loader"
            size="sm"
            :animate-on-hover="false"
            class="mr-1 h-4 w-4 animate-spin"
          />
          {{
            adminApiKeyOperating
              ? t('admin.settings.adminApiKey.creating')
              : t('admin.settings.adminApiKey.create')
          }}
        </button>
      </div>

      <template v-else>
        <div class="flex flex-wrap items-end justify-between gap-4">
          <div>
            <p class="input-label">
              {{ t('admin.settings.adminApiKey.currentKey') }}
            </p>
            <code
              class="rounded-compact bg-gray-100 px-2 py-1 font-mono text-sm text-gray-900 dark:bg-dark-700 dark:text-dark-50"
            >
              {{ adminApiKeyMasked }}
            </code>
          </div>
          <div class="flex gap-2">
            <button
              type="button"
              :disabled="adminApiKeyOperating"
              class="btn btn-secondary btn-sm h-9"
              @click="regenerateAdminApiKey"
            >
              {{
                adminApiKeyOperating
                  ? t('admin.settings.adminApiKey.regenerating')
                  : t('admin.settings.adminApiKey.regenerate')
              }}
            </button>
            <button
              type="button"
              :disabled="adminApiKeyOperating"
              class="btn btn-secondary btn-sm h-9 text-red-600 hover:text-red-700 dark:text-red-400"
              @click="deleteAdminApiKey"
            >
              {{ t('admin.settings.adminApiKey.delete') }}
            </button>
          </div>
        </div>

        <!-- The generation response supplies the full credential; status reads provide a mask, while this controller retains the generated value. -->
        <div
          v-if="newAdminApiKey"
          class="space-y-3 rounded-control border border-green-200 bg-green-50 p-4 dark:border-green-800/60 dark:bg-green-900/20"
        >
          <p class="text-sm font-medium text-green-700 dark:text-green-300">
            {{ t('admin.settings.adminApiKey.keyWarning') }}
          </p>
          <div class="flex items-center gap-2">
            <code
              class="flex-1 select-all break-all rounded-compact border border-green-300 bg-white px-3 py-2 font-mono text-sm dark:border-green-700 dark:bg-dark-800"
            >
              {{ newAdminApiKey }}
            </code>
            <button
              type="button"
              class="btn btn-primary btn-sm h-9 flex-shrink-0"
              @click="copyNewKey"
            >
              {{ t('admin.settings.adminApiKey.copyKey') }}
            </button>
          </div>
          <p class="text-xs text-green-600 dark:text-green-400">
            {{ t('admin.settings.adminApiKey.usage') }}
          </p>
        </div>
      </template>
    </SettingsSection>
  </SettingsCard>
</template>

<script setup lang="ts">
import ContentSkeleton from '@/components/common/ContentSkeleton.vue'
import SettingsCard from '@/components/common/settings/SettingsCard.vue'
import SettingsNotice from '@/components/common/settings/SettingsNotice.vue'
import SettingsSection from '@/components/common/settings/SettingsSection.vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import type { useAdminApiKeySettings } from './useAdminApiKeySettings'

/**
 * AdminApiKeySettings.vue displays administrator API key status and exposes
 * creation, rotation, deletion, and copying actions. These buttons invoke immediate
 * server operations through the controller, and the full newly generated credential
 * remains visible in that controller until replacement, deletion, or page teardown.
 */

/**
 * props receives the page-owned credential controller whose immediate operations
 * update the status displayed by this panel.
 */
const props = defineProps<{
  /** controller retains the generated credential and operation flags for the parent page lifetime. */
  controller: ReturnType<typeof useAdminApiKeySettings>
}>()
/**
 * Controller bindings retain Vue refs and action functions so template edits and
 * buttons use the parent instance.
 */
const {
  adminApiKeyLoading,
  adminApiKeyExists,
  adminApiKeyMasked,
  adminApiKeyOperating,
  newAdminApiKey,
  createAdminApiKey,
  regenerateAdminApiKey,
  deleteAdminApiKey,
  copyNewKey,
} = props.controller
/**
 * t resolves settings labels and hints from the active interface locale during
 * rendering.
 */
const { t } = useI18n()
</script>
