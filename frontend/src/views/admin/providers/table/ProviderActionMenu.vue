<template>
  <Teleport to="body">
    <!-- Backdrop: click anywhere outside to close -->
    <div v-if="show && position" class="fixed inset-0 z-menu-overlay" @click="emit('close')"></div>
    <MotionTransition name="dropdown-fade">
      <div
        v-if="show && position"
        class="action-menu action-menu-content max-h-[calc(100dvh-16px)] w-52 overflow-y-auto"
        :style="{ top: position.top + 'px', left: position.left + 'px' }"
        @click.stop
      >
        <div class="py-1">
          <template v-if="provider">
            <button
              @click="
                $emit('test', provider);
                $emit('close');
              "
              class="dropdown-item"
            >
              <Icon name="play" size="sm" class="text-green-500" :stroke-width="2" />
              {{ t('admin.providers.testConnection') }}
            </button>
            <button
              @click="
                $emit('stats', provider);
                $emit('close');
              "
              class="dropdown-item"
            >
              <Icon name="chart" size="sm" class="text-indigo-500" />
              {{ t('admin.providers.viewStats') }}
            </button>
            <button
              @click="
                $emit('advanced-scheduler-score', provider);
                $emit('close');
              "
              class="dropdown-item"
            >
              <Icon name="calculator" size="sm" class="text-violet-500" />
              {{ t('admin.providers.advancedSchedulerScore.action') }}
            </button>
            <button
              @click="
                $emit('schedule', provider);
                $emit('close');
              "
              class="dropdown-item"
            >
              <Icon name="clock" size="sm" class="text-orange-500" />
              {{ t('admin.scheduledTests.schedule') }}
            </button>
            <button
              v-if="canDuplicate"
              @click="
                $emit('duplicate', provider);
                $emit('close');
              "
              class="dropdown-item"
            >
              <Icon name="copy" size="sm" class="text-sky-500" />
              {{ t('admin.providers.duplicateProvider') }}
            </button>
            <!-- Linked shadow records use parent credentials, so their menu excludes reauthorization and token-refresh actions owned by the credential-bearing parent. -->
            <template v-if="supportsReauth">
              <button
                @click="
                  $emit('reauth', provider);
                  $emit('close');
                "
                class="dropdown-item text-blue-600"
              >
                <Icon name="link" size="sm" />
                {{ t('admin.providers.reAuthorize') }}
              </button>
            </template>
            <template v-if="supportsTokenRefresh">
              <button
                @click="
                  $emit('refresh-token', provider);
                  $emit('close');
                "
                class="dropdown-item text-purple-600"
              >
                <Icon name="refresh" size="sm" />
                {{ t('admin.providers.refreshToken') }}
              </button>
            </template>
            <button
              v-if="isOpenAIOAuthParent"
              @click="
                $emit('create-spark-shadow', provider);
                $emit('close');
              "
              class="dropdown-item text-amber-600"
            >
              <Icon name="sparkles" size="sm" />
              {{ t('admin.providers.createSparkShadow') }}
            </button>
            <button
              v-if="supportsPrivacy"
              @click="
                $emit('set-privacy', provider);
                $emit('close');
              "
              class="dropdown-item text-emerald-600"
            >
              <Icon name="shield" size="sm" />
              {{ t('admin.providers.setPrivacy') }}
            </button>
            <button
              v-if="isOpenAIOAuth"
              @click="
                $emit('invite-reset', provider);
                $emit('close');
              "
              class="dropdown-item text-cyan-600"
            >
              <Icon name="gift" size="sm" />
              {{ t('admin.providers.inviteReset') }}
            </button>
            <div
              v-if="hasRecoverableState"
              class="my-1 border-t border-gray-100 dark:border-dark-700"
            ></div>
            <button
              v-if="hasRecoverableState"
              @click="
                $emit('recover-state', provider);
                $emit('close');
              "
              class="dropdown-item text-emerald-600"
            >
              <Icon name="sync" size="sm" />
              {{ t('admin.providers.recoverState') }}
            </button>
            <button
              v-if="hasQuotaLimit"
              @click="
                $emit('reset-quota', provider);
                $emit('close');
              "
              class="dropdown-item text-teal-600"
            >
              <Icon name="refresh" size="sm" />
              {{ t('admin.providers.resetQuota') }}
            </button>
            <!-- Deletion remains the final action and delegates confirmation to the provider page after menu dismissal. -->
            <div class="my-1 border-t border-gray-100 dark:border-dark-700"></div>
            <button
              type="button"
              @click="
                $emit('delete', provider);
                $emit('close');
              "
              class="dropdown-item text-red-600 hover:bg-red-50 dark:text-red-400 dark:hover:bg-red-900/20"
            >
              <Icon name="trash" size="sm" />
              {{ t('common.delete') }}
            </button>
          </template>
        </div>
      </div>
    </MotionTransition>
  </Teleport>
</template>

<script setup lang="ts">
import MotionTransition from '@/components/common/MotionTransition.vue'
import { computed, watch, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { Icon } from '@/components/icons'
import type { Provider } from '@/api/admin/providers.types'

/**
 * ProviderActionMenu.vue offers maintenance actions for the selected provider and delegates
 * execution to its parent through emitted events. Platform, authentication type, refresh-token
 * presence, spending limits, and linked shadow status determine which actions are presented.
 * The parent supplies viewport position, while the menu owns Escape and backdrop dismissal.
 */

/**
 * props provides visibility, selected provider, and viewport coordinates; action events carry
 * the selected provider back to the owning page.
 */
const props = defineProps<{
  show: boolean // parent-controlled menu visibility
  provider: Provider | null // selected provider or no selection
  /**
   * Position supplied by the parent anchors the floating menu; null leaves no usable screen
   * coordinates.
   */
  position: {
    top: number // viewport top offset pixels
    left: number // viewport left offset pixels
  } | null
}>()
/**
 * emit delegates test, statistics, scheduling, duplication, authorization, credential, quota,
 * privacy, invitation, shadow creation, and deletion actions to the parent.
 */
const emit = defineEmits([
  'close',
  'test',
  'stats',
  'advanced-scheduler-score',
  'schedule',
  'duplicate',
  'reauth',
  'refresh-token',
  'recover-state',
  'reset-quota',
  'set-privacy',
  'invite-reset',
  'create-spark-shadow',
  'delete',
])
/**
 * Translation and capability predicates determine which provider maintenance actions are
 * visible for the currently selected row and its saved state.
 */
const { t } = useI18n()
const canDuplicate = computed(() => {
  if (!props.provider || props.provider.parent_provider_id != null) return false
  return ['apikey', 'upstream', 'bedrock', 'service_account'].includes(props.provider.type)
})
/**
 * Recovery predicates consider provider and model cooldowns, overload, temporary suspension,
 * and stored error status before displaying the recover action.
 */
const isRateLimited = computed(() => {
  if (
    props.provider?.rate_limit_reset_at &&
    new Date(props.provider.rate_limit_reset_at) > new Date()
  ) {
    return true
  }
  const modelLimits = (props.provider?.extra as Record<string, unknown> | undefined)
    ?.model_rate_limits as
    | Record<
        string,
        {
          rate_limit_reset_at: string // model limit expiry timestamp
        }
      >
    | undefined
  if (modelLimits) {
    const now = new Date()
    return Object.values(modelLimits).some((info) => new Date(info.rate_limit_reset_at) > now)
  }
  return false
})
const isOverloaded = computed(
  () => props.provider?.overload_until && new Date(props.provider.overload_until) > new Date(),
)
const isTempUnschedulable = computed(
  () =>
    props.provider?.temp_unschedulable_until &&
    new Date(props.provider.temp_unschedulable_until) > new Date(),
)
const hasRecoverableState = computed(() => {
  return (
    props.provider?.status === 'error' ||
    Boolean(isRateLimited.value) ||
    Boolean(isOverloaded.value) ||
    Boolean(isTempUnschedulable.value)
  )
})
/**
 * OAuth platform predicates and shadow identity determine whether credential, privacy,
 * invitation, and Spark-shadow maintenance applies to this selected row.
 */
const isAntigravityOAuth = computed(
  () => props.provider?.platform === 'antigravity' && props.provider?.type === 'oauth',
)
const isOpenAIOAuth = computed(
  () => props.provider?.platform === 'openai' && props.provider?.type === 'oauth',
)

/**
 * isShadow identifies linked providers whose parent owns credentials, excluding their
 * credential refresh, reauthorization, and privacy actions from this menu.
 */
const isShadow = computed(() => props.provider?.parent_provider_id != null)
/**
 * Reauthorization and refresh predicates include supported OAuth types and Qoder Cosy records
 * with a recorded refresh token, excluding linked shadows.
 */
const supportsReauth = computed(
  () =>
    (props.provider?.type === 'oauth' || props.provider?.type === 'setup-token') && !isShadow.value,
)
const supportsTokenRefresh = computed(
  () =>
    supportsReauth.value ||
    (props.provider?.platform === 'qoder' &&
      props.provider?.type === 'cosy' &&
      props.provider.credentials_status?.has_refresh_token === true &&
      !isShadow.value),
)

/**
 * isOpenAIOAuthParent selects OpenAI OAuth parent rows for Spark shadow creation.
 * Providers already linked to a parent do not offer that creation action.
 */
const isOpenAIOAuthParent = computed(() => isOpenAIOAuth.value && !isShadow.value)
/**
 * supportsPrivacy enables privacy changes for Antigravity or OpenAI OAuth records when their
 * credentials belong to this provider rather than a parent.
 */
const supportsPrivacy = computed(
  () => (isAntigravityOAuth.value || isOpenAIOAuth.value) && !isShadow.value,
)
/**
 * hasQuotaLimit enables local quota reset for API-key and Bedrock records with any positive
 * total, daily, weekly, or monthly spending limit.
 */
const hasQuotaLimit = computed(() => {
  return (
    (props.provider?.type === 'apikey' || props.provider?.type === 'bedrock') &&
    ((props.provider?.quota_limit ?? 0) > 0 ||
      (props.provider?.quota_daily_limit ?? 0) > 0 ||
      (props.provider?.quota_weekly_limit ?? 0) > 0 ||
      (props.provider?.quota_monthly_limit ?? 0) > 0)
  )
})

/**
 * handleKeydown asks the parent to close the action menu on Escape.
 */
const handleKeydown = (event: KeyboardEvent) => {
  if (event.key === 'Escape') emit('close')
}

watch(
  () => props.show,
  (visible) => {
    if (visible) {
      // Register Escape while the menu is visible so keyboard dismissal follows the same
      // parent-owned close event as the backdrop.
      window.addEventListener('keydown', handleKeydown)
    } else {
      // Release the visibility-owned listener when the menu closes so hidden menus do not
      // respond to later Escape presses.
      window.removeEventListener('keydown', handleKeydown)
    }
  },
  { immediate: true },
)

onUnmounted(() => {
  // Unmount also releases the listener when the parent removes this menu before its visibility
  // watcher observes a close.
  window.removeEventListener('keydown', handleKeydown)
})
</script>
