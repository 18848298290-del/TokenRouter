<template>
  <BaseDialog
    :show="show"
    :title="t('admin.users.replaceGroupTitle')"
    width="narrow"
    @close="$emit('close')"
  >
    <div v-if="oldGroup" class="space-y-4">
      <!-- Replacement guidance. -->
      <p class="text-sm text-gray-600 dark:text-gray-400">
        {{ t('admin.users.replaceGroupHint', { old: oldGroup.name }) }}
      </p>

      <!-- Current source group. -->
      <div
        class="rounded-surface border border-gray-200 bg-gray-50 p-3 dark:border-dark-600 dark:bg-dark-800"
      >
        <div class="flex items-center gap-2">
          <Icon name="shield" size="sm" class="text-purple-500" />
          <span class="font-medium text-gray-900 dark:text-white">{{ oldGroup.name }}</span>
          <Icon name="arrowRight" size="sm" class="ml-auto text-gray-400" />
          <span v-if="selectedGroupId" class="font-medium text-primary-600 dark:text-primary-400">
            {{ availableGroups.find((g) => g.id === selectedGroupId)?.name }}
          </span>
          <span v-else class="text-sm text-gray-400">?</span>
        </div>
      </div>

      <!-- Available destination groups. -->
      <div v-if="availableGroups.length > 0" class="max-h-64 space-y-2 overflow-y-auto">
        <label
          v-for="group in availableGroups"
          :key="group.id"
          class="flex cursor-pointer items-center gap-3 rounded-surface border-2 p-3 transition"
          :class="
            selectedGroupId === group.id
              ? 'border-primary-400 bg-primary-50/50 dark:border-primary-500/15 dark:bg-primary-500/8 dark:text-primary-500'
              : 'border-gray-200 hover:border-gray-300 dark:border-dark-600 dark:hover:border-dark-500'
          "
        >
          <input type="radio" :value="group.id" v-model="selectedGroupId" class="sr-only" />
          <div
            class="flex h-5 w-5 items-center justify-center rounded-full border-2 transition"
            :class="
              selectedGroupId === group.id
                ? 'border-primary-500 bg-primary-500'
                : 'border-gray-300 dark:border-dark-500'
            "
          >
            <div v-if="selectedGroupId === group.id" class="h-2 w-2 rounded-full bg-white"></div>
          </div>
          <div class="flex-1">
            <span class="font-medium text-gray-900 dark:text-white">{{ group.name }}</span>
          </div>
        </label>
      </div>

      <!-- Empty destination catalog. -->
      <div v-else class="py-6 text-center text-sm text-gray-400">
        {{ t('admin.users.noOtherGroups') }}
      </div>
    </div>

    <template #footer>
      <div class="flex justify-end gap-3">
        <button @click="$emit('close')" class="btn btn-secondary px-5">
          {{ t('common.cancel') }}
        </button>
        <button
          @click="handleReplace"
          :disabled="!selectedGroupId || submitting"
          class="btn btn-primary px-6"
        >
          <Icon
            name="loader"
            size="sm"
            :animate-on-hover="false"
            v-if="submitting"
            class="-ml-1 mr-2 h-4 w-4 animate-spin"
          />
          {{ submitting ? t('common.saving') : t('admin.users.replaceGroupConfirm') }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { ref, watch, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { adminAPI } from '@/api/admin'
import type { AdminUser } from '@/api/admin/users.types'
import type { AdminGroup } from '@/api/groups.types'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Icon from '@/components/icons/Icon.vue'

// GroupReplaceModal.vue replaces a user's exclusive group and reports the number of
// migrated API keys. The picker lists active exclusive groups other than the source
// group, and reopening clears the selected destination before another replacement can
// be submitted.

// Props supplies the user, source group, and destination catalog for group replacement.
interface Props {
  show: boolean // Parent-controlled dialog visibility.
  // user supplies the selected account; null leaves identity-dependent operations
  // waiting until the parent chooses an account.
  user: AdminUser | null
  oldGroup: {
    id: number // Group numeric identifier.
    name: string // Group display name.
  } | null // Source exclusive group.
  allGroups: AdminGroup[] // Available group catalog.
}

// props and emit connect the parent-owned replacement target with the success and close
// events produced after a completed request.
const props = defineProps<Props>()
const emit = defineEmits(['close', 'success'])
// The translation and notification services provide the replacement controls and
// migrated-key count shown after the server completes the operation.
const { t } = useI18n()
const appStore = useAppStore()

// selectedGroupId stores the chosen destination, while submitting controls request
// feedback until replacement finishes or fails for the selected user.
const selectedGroupId = ref<number | null>(null)
const submitting = ref(false)

// availableGroups exposes active exclusive destinations other than oldGroup, returning
// an empty catalog until the parent supplies a source group.
const availableGroups = computed(() => {
  if (!props.oldGroup) return []
  return props.allGroups.filter(
    (g) => g.status === 'active' && g.is_exclusive && g.id !== props.oldGroup!.id,
  )
})

// Clear the destination when opening the replacement dialog so a previous user's group
// choice is not reused for the next replacement.
watch(
  () => props.show,
  (v) => {
    if (v) {
      selectedGroupId.value = null
    }
  },
)

// handleReplace submits the selected user, source group, and destination to the
// replacement endpoint. Missing inputs stop the operation; success reports migrated
// keys and emits success before close, while failures are logged.
const handleReplace = async () => {
  if (!props.user || !props.oldGroup || !selectedGroupId.value) return
  submitting.value = true

  try {
    // Complete group replacement before emitting success and close, so the parent
    // refresh observes migrated keys and failed requests leave the destination available.
    const result = await adminAPI.users.replaceGroup(
      props.user.id,
      props.oldGroup.id,
      selectedGroupId.value,
    )
    appStore.showSuccess(
      t('admin.users.replaceGroupSuccess', {
        count: result.migrated_keys, // Displayed affected-record count.
      }),
    )
    emit('success')
    emit('close')
  } catch (error) {
    // A rejected replacement leaves the dialog and destination available for another
    // attempt; this handler records the failure in the console.
    console.error('Failed to replace group:', error)
  } finally {
    submitting.value = false
  }
}
</script>
