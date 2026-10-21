<template>
  <span
    class="rounded-full border border-gray-200 bg-gray-100 px-3 py-1 text-xs font-semibold text-gray-700 dark:border-dark-700 dark:bg-dark-900 dark:text-dark-200"
  >
    {{ label }}
  </span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

/**
 * BillingModeBadge.vue labels a pricing mode in a neutral badge shared by tables and
 * pricing details. Translation keys follow the pricing mode identifier, and the
 * identifier itself supplies the label when the catalog has no matching translation.
 */

/**
 * props supplies the pricing mode identifier used to select the translated billing
 * label in the neutral pill.
 */
const props = defineProps<{ mode: string /* pricing mode identifier */ }>()

/**
 * The i18n translator resolves this component's message keys using the current
 * application locale and its configured translation catalog.
 */
const { t } = useI18n()

/**
 * label translates the pricing mode and uses the supplied identifier as its missing
 * translation text.
 */
const label = computed(() => t(`admin.pricing.defaults.modes.${props.mode}`, props.mode))
</script>
