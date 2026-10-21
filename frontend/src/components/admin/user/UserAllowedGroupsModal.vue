<template>
  <BaseDialog
    :show="show"
    :title="t('admin.users.groupConfig')"
    width="wide"
    @close="$emit('close')"
  >
    <div v-if="user" class="space-y-6">
      <!-- Selected user identity. -->
      <div
        class="flex items-center gap-4 rounded-surface bg-gradient-to-r from-primary-50 to-primary-100 p-5 dark:from-primary-900/30 dark:to-primary-800/20"
      >
        <UserAvatar
          :avatar-url="user.avatar_url || ''"
          :user-id="user.id"
          :alt="user.email"
          size-class="h-14 w-14"
        />
        <div class="flex-1">
          <p class="text-lg font-semibold text-gray-900 dark:text-white">{{ user.email }}</p>
          <p class="mt-1 text-sm text-gray-600 dark:text-gray-400">
            {{ t('admin.users.groupConfigHint', { email: user.email }) }}
          </p>
        </div>
      </div>

      <!-- Catalog loading feedback. -->
      <ContentSkeleton v-if="loading" variant="form" :rows="4" class="py-4" />

      <div v-else class="space-y-6">
        <!-- Exclusive group access. -->
        <div v-if="exclusiveGroups.length > 0">
          <div class="mb-3 flex items-center gap-2">
            <div class="h-1.5 w-1.5 rounded-full bg-purple-500"></div>
            <h4 class="text-sm font-semibold text-gray-700 dark:text-gray-300">
              {{ t('admin.users.exclusiveGroups') }}
            </h4>
            <span class="text-xs text-gray-400"
              >({{ exclusiveGroupConfigs.filter((c) => c.isSelected).length }}/{{
                exclusiveGroupConfigs.length
              }})</span
            >
          </div>
          <div class="grid gap-3">
            <div
              v-for="config in exclusiveGroupConfigs"
              :key="config.groupId"
              class="group relative overflow-hidden rounded-surface border-2 p-4 transition duration-normal"
              :class="
                config.isSelected
                  ? 'border-primary-400 bg-primary-50/50 shadow-sm dark:border-primary-500/15 dark:bg-primary-500/8 dark:text-primary-500'
                  : 'border-gray-200 bg-white hover:border-gray-300 dark:border-dark-600 dark:bg-dark-800 dark:hover:border-dark-500'
              "
            >
              <div class="flex items-center gap-4">
                <!-- Group access checkbox. -->
                <div class="flex-shrink-0">
                  <label class="relative flex h-6 w-6 cursor-pointer items-center justify-center">
                    <input
                      type="checkbox"
                      :checked="config.isSelected"
                      @change="toggleExclusiveGroup(config.groupId)"
                      class="peer sr-only"
                    />
                    <div
                      class="h-5 w-5 rounded-control border-2 border-gray-300 transition peer-checked:border-primary-500 peer-checked:bg-primary-500 dark:border-dark-500 peer-checked:dark:border-primary-500"
                    >
                      <Icon
                        name="check"
                        size="md"
                        :animate-on-hover="false"
                        v-if="config.isSelected"
                        class="h-full w-full text-white"
                      />
                    </div>
                  </label>
                </div>

                <!-- Group name and rate. -->
                <div class="min-w-0 flex-1">
                  <div class="flex items-center gap-2">
                    <span class="text-base font-semibold text-gray-900 dark:text-white">{{
                      config.groupName
                    }}</span>
                    <span
                      class="inline-flex items-center rounded-full bg-purple-100 px-2 py-0.5 text-xs font-medium text-purple-700 dark:bg-purple-900/40 dark:text-purple-300"
                    >
                      {{ t('admin.groups.exclusive') }}
                    </span>
                  </div>
                  <div class="mt-1.5 flex items-center gap-3 text-sm">
                    <span class="inline-flex items-center gap-1 text-gray-500 dark:text-gray-400">
                    </span>
                    <span class="text-gray-300 dark:text-dark-500">•</span>
                    <span class="text-gray-500 dark:text-gray-400">
                      {{ t('admin.users.defaultRate') }}:
                      <span class="font-medium text-gray-700 dark:text-gray-300"
                        >{{ config.defaultRate }}x</span
                      >
                    </span>
                  </div>
                </div>

                <!-- User-specific rate override. -->
                <div class="flex flex-shrink-0 items-center gap-3">
                  <label class="text-sm font-medium text-gray-600 dark:text-gray-400">{{
                    t('admin.users.customRate')
                  }}</label>
                  <input
                    type="number"
                    step="0.001"
                    min="0.001"
                    :value="config.customRate ?? ''"
                    @input="
                      updateCustomRate(config.groupId, ($event.target as HTMLInputElement).value)
                    "
                    :placeholder="String(config.defaultRate)"
                    class="input hide-spinner w-24 px-3 font-medium"
                  />
                </div>
              </div>
            </div>
          </div>
        </div>

        <!-- Public group access. -->
        <div v-if="publicGroups.length > 0">
          <div class="mb-3 flex items-center gap-2">
            <div class="h-1.5 w-1.5 rounded-full bg-green-500"></div>
            <h4 class="text-sm font-semibold text-gray-700 dark:text-gray-300">
              {{ t('admin.users.publicGroups') }}
            </h4>
            <span class="text-xs text-gray-400"
              >({{ publicGroupConfigs.filter((c) => c.isSelected).length }}/{{
                publicGroupConfigs.length
              }})</span
            >
          </div>
          <div class="grid gap-3">
            <div
              v-for="config in publicGroupConfigs"
              :key="config.groupId"
              class="group relative overflow-hidden rounded-surface border-2 p-4 transition duration-normal"
              :class="
                config.isSelected
                  ? 'border-green-300 bg-green-50/50 shadow-sm dark:border-green-700/60 dark:bg-green-900/10'
                  : 'border-gray-200 bg-white hover:border-gray-300 dark:border-dark-600 dark:bg-dark-800 dark:hover:border-dark-500'
              "
            >
              <div class="flex items-center gap-4">
                <!-- Unchecking a public group adds it to this user's denied public groups. -->
                <div class="flex-shrink-0">
                  <label class="relative flex h-6 w-6 cursor-pointer items-center justify-center">
                    <input
                      type="checkbox"
                      :checked="config.isSelected"
                      @change="togglePublicGroup(config.groupId)"
                      class="peer sr-only"
                    />
                    <div
                      class="h-5 w-5 rounded-control border-2 border-gray-300 transition peer-checked:border-green-500 peer-checked:bg-green-500 dark:border-dark-500 peer-checked:dark:border-green-600 peer-checked:dark:bg-green-600"
                    >
                      <Icon
                        name="check"
                        size="md"
                        :animate-on-hover="false"
                        v-if="config.isSelected"
                        class="h-full w-full text-white"
                      />
                    </div>
                  </label>
                </div>

                <!-- Group name and rate. -->
                <div class="min-w-0 flex-1">
                  <div class="flex items-center gap-2">
                    <span class="text-base font-semibold text-gray-900 dark:text-white">{{
                      config.groupName
                    }}</span>
                    <span
                      v-if="!config.isSelected"
                      class="inline-flex items-center rounded-full bg-gray-100 px-2 py-0.5 text-xs font-medium text-gray-600 dark:bg-dark-700 dark:text-gray-300"
                    >
                      {{ t('admin.users.publicGroupDisabled') }}
                    </span>
                  </div>
                  <div class="mt-1.5 flex items-center gap-3 text-sm">
                    <span class="inline-flex items-center gap-1 text-gray-500 dark:text-gray-400">
                    </span>
                    <span class="text-gray-300 dark:text-dark-500">•</span>
                    <span class="text-gray-500 dark:text-gray-400">
                      {{ t('admin.users.defaultRate') }}:
                      <span class="font-medium text-gray-700 dark:text-gray-300"
                        >{{ config.defaultRate }}x</span
                      >
                    </span>
                  </div>
                </div>

                <!-- User-specific rate override. -->
                <div class="flex flex-shrink-0 items-center gap-3">
                  <label class="text-sm font-medium text-gray-600 dark:text-gray-400">{{
                    t('admin.users.customRate')
                  }}</label>
                  <input
                    type="number"
                    step="0.001"
                    min="0.001"
                    :value="config.customRate ?? ''"
                    @input="
                      updateCustomRate(config.groupId, ($event.target as HTMLInputElement).value)
                    "
                    :placeholder="String(config.defaultRate)"
                    class="input hide-spinner w-24 px-3 font-medium"
                  />
                </div>
              </div>
            </div>
          </div>
        </div>

        <!-- Empty group catalog. -->
        <div
          v-if="groups.length === 0"
          class="flex flex-col items-center justify-center py-12 text-center"
        >
          <div
            class="mb-4 flex h-16 w-16 items-center justify-center rounded-full bg-gray-100 dark:bg-dark-700"
          >
            <Icon name="layers" size="xl" class="h-8 w-8 text-gray-400" />
          </div>
          <p class="text-gray-500 dark:text-gray-400">{{ t('common.noGroupsAvailable') }}</p>
        </div>
      </div>
    </div>

    <template #footer>
      <div class="flex justify-end gap-3">
        <button @click="$emit('close')" class="btn btn-secondary px-5">
          {{ t('common.cancel') }}
        </button>
        <button @click="handleSave" :disabled="submitting" class="btn btn-primary px-6">
          <Icon
            name="loader"
            size="sm"
            :animate-on-hover="false"
            v-if="submitting"
            class="-ml-1 mr-2 h-4 w-4 animate-spin"
          />
          {{ submitting ? t('common.saving') : t('common.save') }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import ContentSkeleton from '@/components/common/ContentSkeleton.vue'
import Icon from '@/components/icons/Icon.vue'
import { ref, watch, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { adminAPI } from '@/api/admin'
import type { AdminUser } from '@/api/admin/users.types'
import type { Group } from '@/api/groups.types'
import BaseDialog from '@/components/common/BaseDialog.vue'
import UserAvatar from '@/components/common/UserAvatar.vue'

// UserAllowedGroupsModal.vue edits per-user group access and custom rate overrides.
// Exclusive groups require inclusion in allowed_groups; public groups remain available
// unless included in disabled_public_groups. The editor presents both rules as
// selection checkboxes and converts them back when saving.
//
// Rate overrides need a separate snapshot because clearing an existing rate differs
// from leaving an absent rate untouched. Loaded rates are copied into
// originalGroupRates before draft rows are built. Saving sends null for previously
// configured rates that were cleared, numeric values for configured overrides, and
// omits untouched absent rates from the update map.

// GroupRateConfig combines group metadata with the user's editable access and rate
// overrides.
interface GroupRateConfig {
  groupId: number // Catalog group identifier.
  groupName: string // Catalog group name.
  isExclusive: boolean // Exclusive group access mode.
  defaultRate: number // Group default multiplier.
  // customRate holds a user override; null removes an existing override when the
  // loaded-rate snapshot contains this group.
  customRate: number | null
  // isSelected grants exclusive access when true and blocks public access when false
  // during save conversion.
  isSelected: boolean
}

// props and emit connect the selected user and modal visibility to parent refresh and
// close events after saving access settings.
const props = defineProps<{
  show: boolean // Parent-controlled dialog visibility.
  // user supplies the selected account; null leaves identity-dependent operations waiting
  // until the parent chooses an account.
  user: AdminUser | null
}>()
const emit = defineEmits(['close', 'success'])
// The translation and notification services provide group-setting labels and the
// success message after the user update completes.
const { t } = useI18n()
const appStore = useAppStore()

// groups and groupConfigs hold the active catalog and editable user settings;
// originalGroupRates records loaded overrides so clearing a draft can request deletion.
const groups = ref<Group[]>([])
const groupConfigs = ref<GroupRateConfig[]>([])
const originalGroupRates = ref<Record<number, number>>({})
// loading and submitting drive separate catalog-load and save feedback, leaving the
// current draft available when either operation reports an error.
const loading = ref(false)
const submitting = ref(false)

// exclusiveGroups and publicGroups partition catalog entries by access model for the
// matching sections of the group editor.
const exclusiveGroups = computed(() => groups.value.filter((g) => g.is_exclusive))
const publicGroups = computed(() => groups.value.filter((g) => !g.is_exclusive))

// exclusiveGroupConfigs and publicGroupConfigs partition editable rows so each access
// model uses the same checkbox and rate controls.
const exclusiveGroupConfigs = computed(() => groupConfigs.value.filter((c) => c.isExclusive))
const publicGroupConfigs = computed(() => groupConfigs.value.filter((c) => !c.isExclusive))

// Load the catalog when opening with a user; the watcher does not reload merely because
// the selected user changes while already visible.
watch(
  () => props.show,
  (v) => {
    if (v && props.user) {
      load()
    }
  },
)

// load fetches active groups and builds editable rows from the selected user's access
// lists and rates. It snapshots existing rate overrides so saving can distinguish
// clearing a configured rate from leaving one absent.
//
// Exclusive selections follow allowed_groups, while public selections invert
// disabled_public_groups. The user prop is read after the catalog request settles, and
// errors are logged while the previous draft remains available. Loading feedback ends
// in the finally block.
const load = async () => {
  loading.value = true
  try {
    const res = await adminAPI.groups.list(1, 1000)
    groups.value = res.items.filter((g) => g.status === 'active')

    const userAllowedGroups = props.user?.allowed_groups || []
    const userDisabledPublicGroups = props.user?.disabled_public_groups || []
    const userGroupRates = props.user?.group_rates || {}

    // Keep a separate snapshot of persisted overrides so clearing a draft value can
    // later produce an explicit deletion for that group.
    originalGroupRates.value = { ...userGroupRates }

    groupConfigs.value = groups.value.map((g) => ({
      groupId: g.id, // Catalog group identifier.
      groupName: g.name, // Catalog group name.
      isExclusive: g.is_exclusive, // Exclusive group access mode.
      defaultRate: g.rate_multiplier, // Group default multiplier.
      customRate: userGroupRates[g.id] ?? null, // User-specific multiplier override.

      // Exclusive membership is an allowlist, whereas public membership is a denylist;
      // convert both rules into the checkbox's selected state for editing.
      isSelected: g.is_exclusive
        ? userAllowedGroups.includes(g.id)
        : !userDisabledPublicGroups.includes(g.id), // Draft group access selection.
    }))
  } catch (error) {
    // A failed catalog request retains the previous draft and logs the error, so no
    // partially rebuilt set of group rows replaces it.
    console.error('Failed to load groups:', error)
  } finally {
    loading.value = false
  }
}

// toggleExclusiveGroup reverses selection for a matching exclusive draft row. Missing
// rows and public groups leave the draft unchanged, keeping this handler limited to the
// exclusive section's access model.
const toggleExclusiveGroup = (groupId: number) => {
  const config = groupConfigs.value.find((c) => c.groupId === groupId)
  if (config && config.isExclusive) {
    config.isSelected = !config.isSelected
  }
}

// togglePublicGroup reverses selection for a matching public draft row. Missing rows
// and exclusive groups leave the draft unchanged, keeping this handler limited to the
// public section's access model.
const togglePublicGroup = (groupId: number) => {
  const config = groupConfigs.value.find((c) => c.groupId === groupId)
  if (config && !config.isExclusive) {
    config.isSelected = !config.isSelected
  }
}

// updateCustomRate parses a draft rate for the matching group row. Empty or nonnumeric
// input clears the override to null; saving later uses the loaded-rate snapshot to
// decide whether that means deletion.
const updateCustomRate = (groupId: number, value: string) => {
  const config = groupConfigs.value.find((c) => c.groupId === groupId)
  if (config) {
    if (value === '' || value === null || value === undefined) {
      config.customRate = null
    } else {
      const numValue = parseFloat(value)
      config.customRate = isNaN(numValue) ? null : numValue
    }
  }
}

// handleSave converts checked exclusive groups and unchecked public groups into their
// respective access lists. It builds rate updates by comparing each draft override with
// the snapshot captured during loading.
//
// Numeric overrides are included directly, cleared existing overrides become null, and
// untouched absent rates stay omitted. The user update is awaited before success and
// close events are emitted. Errors are logged and leave the dialog available, while the
// pending flag is cleared after either outcome.
const handleSave = async () => {
  if (!props.user) return
  submitting.value = true

  try {
    const allowedGroups = groupConfigs.value
      .filter((c) => c.isExclusive && c.isSelected)
      .map((c) => c.groupId)

    const disabledPublicGroups = groupConfigs.value
      .filter((c) => !c.isExclusive && !c.isSelected)
      .map((c) => c.groupId)

    // Construct rate changes separately from access lists: null deletes a previously
    // loaded override, while an absent untouched override contributes no map entry.
    const groupRates: Record<number, number | null> = {}
    for (const c of groupConfigs.value) {
      const hadOriginalRate = originalGroupRates.value[c.groupId] !== undefined

      if (c.customRate !== null) {
        groupRates[c.groupId] = c.customRate
      } else if (hadOriginalRate) {
        groupRates[c.groupId] = null
      }
    }

    // Wait for the user update before emitting success and close, so the parent's
    // refresh observes the submitted access lists and overrides.
    await adminAPI.users.update(props.user.id, {
      allowed_groups: allowedGroups, // Granted exclusive-group identifiers.
      disabled_public_groups: disabledPublicGroups, // Blocked public-group identifiers.
      group_rates: Object.keys(groupRates).length > 0 ? groupRates : undefined, // Per-group multiplier changes.
    })

    appStore.showSuccess(t('admin.users.groupConfigUpdated'))
    emit('success')
    emit('close')
  } catch (error) {
    // Keep the draft open after a failed update and record the failure, allowing the
    // administrator to retry the same access choices.
    console.error('Failed to update user group config:', error)
  } finally {
    submitting.value = false
  }
}
</script>

<style scoped>
/* Hide number-input steppers while retaining direct numeric entry. */
.hide-spinner::-webkit-outer-spin-button,
.hide-spinner::-webkit-inner-spin-button {
  -webkit-appearance: none;
  margin: 0;
}
.hide-spinner {
  -moz-appearance: textfield;
}
</style>
