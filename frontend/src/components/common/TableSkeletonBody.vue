<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import Skeleton from './Skeleton.vue'

/**
 * TableSkeletonBody.vue renders loading rows within a table body using the caller
 * supplied visible column count. Table layout continues to determine column widths,
 * while alternating placeholder widths suggest content and the body exposes a single
 * accessible loading label.
 */

/**
 * The table props supply the visible column count, loading row count, and cell
 * padding needed to match the containing table.
 */
withDefaults(
  defineProps<{
    columns: number /* visible column count */
    rows?: number /* placeholder row count */
    cellClass?: string /* table cell classes */
  }>(),
  {
    rows: 5,
    cellClass: 'px-4 py-3',
  },
)

/**
 * The i18n translator resolves this component's message keys using the current
 * application locale and its configured translation catalog.
 */
const { t } = useI18n()
</script>

<template>
  <tbody :aria-label="t('common.loading')" aria-busy="true" data-loading-skeleton>
    <tr v-for="row in rows" :key="row" aria-hidden="true">
      <td v-for="column in columns" :key="column" :class="cellClass">
        <Skeleton :width="column % 2 ? '80%' : '60%'" :height="16" />
      </td>
    </tr>
  </tbody>
</template>
