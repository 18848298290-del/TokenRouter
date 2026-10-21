<template>
  <tr v-if="present">
    <td :colspan="colspan" class="border-0 p-0">
      <Collapse :open="open" appear unmount-on-hide @after-leave="finishLeave">
        <slot />
      </Collapse>
    </td>
  </tr>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import Collapse from './Collapse.vue'

/**
 * ExpandableTableRow.vue keeps an expanded table row mounted until its collapse
 * transition finishes. The row uses table cells so expanded content spans the caller
 * supplied columns. Its requested open state can change while the child transition is
 * still leaving, so DOM presence and requested visibility are separate reactive
 * values.
 *
 * Opening restores the row synchronously before the collapse content enters. Closing
 * retains the row until the child reports after-leave. That completion checks the
 * latest open prop before removing the row, allowing a reopen during a pending leave
 * to keep its table structure and content present.
 */

/**
 * props separates the requested open state from the retained row presence and supplies
 * the number of table columns spanned.
 */
const props = defineProps<{
  open: boolean // requested expansion state
  colspan: number /* spanned table columns */
}>()
/**
 * present owns the table row lifetime, remaining true while Collapse runs its leave
 * transition.
 */
const present = ref(props.open)
/**
 * Opening synchronously restores the table row before Collapse renders its entering
 * content, while closing leaves removal to the completed leave event.
 */
watch(
  () => props.open,
  (open) => {
    if (open) present.value = true
  },
  { flush: 'sync' },
)
/**
 * finishLeave removes the table row when the child reports a completed leave and the
 * latest open prop remains false. Collapse completion can arrive after the parent has
 * reopened the row. Checking the current prop keeps that late completion from hiding
 * the reopened content. The watcher restores presence synchronously on opening, while
 * this callback is the sole path that removes the retained row.
 */
function finishLeave() {
  if (!props.open) present.value = false
}
</script>
