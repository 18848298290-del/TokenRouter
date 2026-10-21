<template>
  <button
    v-if="queryEnabled"
    type="button"
    class="inline-flex items-center gap-0.5 rounded-compact px-1.5 py-0.5 text-xs font-medium text-primary-600 transition-colors hover:bg-primary-50 disabled:cursor-not-allowed disabled:opacity-50 dark:text-primary-500 dark:hover:bg-primary-500/8"
    :disabled="loading"
    :title="t('admin.providers.upstreamUsage.query')"
    :aria-label="t('admin.providers.upstreamUsage.query')"
    @click="query"
  >
    <Icon name="refresh" size="xs" :class="{ 'animate-spin': loading }" :stroke-width="2" />
    {{ t('admin.providers.usageWindow.activeQuery') }}
  </button>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { Provider } from '@/api/admin/providers.types'
import Icon from '@/components/icons/Icon.vue'
import { isUpstreamUsageQueryEnabled } from '@/utils/upstreamUsage'

/**
 * ProviderUpstreamUsageQueryButton.vue exposes the administrator-triggered upstream usage query
 * action for one provider. Configuration controls button visibility, and the parent owns
 * loading state and request execution. Every click delegates a forced refresh through the
 * optional callback.
 */

/**
 * props supplies the row, loading feedback, and optional query callback used by the explicit
 * upstream refresh button.
 */
const props = withDefaults(
  defineProps<{
    provider: Provider // current provider row record
    loading?: boolean // parent query request activity
    /**
     * Optional parent callback owns request execution and receives force true for each
     * administrator click.
     */
    request?:
      | ((
          provider: Provider,
          options?: {
            force?: boolean // bypass parent usage cache
          },
        ) => void)
      | null
  }>(),
  {
    loading: false,
    request: null,
  },
)

/**
 * The translator supplies the query tooltip, while queryEnabled follows the saved provider
 * query configuration to control visibility.
 */
const { t } = useI18n()

/**
 * queryEnabled follows the provider query configuration; disabled-query explanations remain
 * with the owning usage display component.
 */
const queryEnabled = computed(() => isUpstreamUsageQueryEnabled(props.provider))

/**
 * query requests a forced upstream usage refresh for the current provider. An absent request
 * callback leaves the action inert; request lifetime and loading updates remain the parent
 * responsibility.
 */
const query = () => {
  props.request?.(props.provider, { force: true })
}
</script>
