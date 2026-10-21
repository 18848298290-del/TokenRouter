<template>
  <div :class="variant === 'card' ? 'space-y-4' : 'space-y-2'" :data-testid="testId || undefined">
    <div v-if="hasHeader" class="space-y-2">
      <div class="flex items-center justify-between gap-2">
        <div class="flex min-w-0 items-center">
          <h4
            v-if="title && titleStyle === 'section'"
            class="text-sm font-semibold text-primary-900 dark:text-dark-50"
          >
            {{ title }}
          </h4>
          <p v-else-if="title" class="input-label mb-0">{{ title }}</p>
          <slot name="title-suffix" />
        </div>
        <div class="flex shrink-0 flex-wrap items-center justify-end gap-2">
          <slot name="header-actions" />
          <button
            v-if="addPlacement === 'header'"
            type="button"
            class="btn btn-secondary shrink-0"
            :disabled="addBlocked"
            :data-testid="testIdFor('add')"
            @click="requestAdd"
          >
            <Icon name="plus" size="sm" class="mr-1.5" />
            {{ resolvedAddLabel }}
          </button>
        </div>
      </div>
      <p v-if="hint" class="input-hint">{{ hint }}</p>
    </div>

    <slot name="header-extra" />

    <p
      v-if="items.length === 0 && leavingCount === 0 && emptyText"
      class="rounded-control border border-dashed border-gray-200 px-3 py-4 text-center text-sm text-gray-500 dark:border-dark-600 dark:text-dark-400"
    >
      {{ emptyText }}
    </p>

    <TransitionGroup
      ref="listRef"
      :name="animated ? 'motion-list' : 'rule-list-static'"
      :css="animated"
      tag="div"
      class="relative"
      :class="variant === 'card' ? 'space-y-4' : 'space-y-2'"
      @before-leave="onBeforeLeave"
      @after-leave="onLeaveDone"
      @leave-cancelled="onLeaveDone"
      @before-enter="restoreEnteringElement"
    >
      <div
        v-for="(item, index) in items"
        :key="resolveKey(item, index)"
        :data-rule-list-row="listId"
        :data-testid="testIdFor('row')"
        class="grid min-w-0 grid-cols-[minmax(0,1fr)_auto] items-start gap-2"
        :class="rowClass"
      >
        <p
          v-if="itemLabel"
          class="self-center text-xs font-medium text-gray-500 dark:text-dark-400"
        >
          {{ itemLabel(index) }}
        </p>
        <div class="min-w-0" :class="{ 'col-span-2 row-start-2': itemLabel }">
          <slot name="row" :item="item" :index="index" />
        </div>
        <div
          v-if="reorderable || removable"
          class="flex items-center gap-1"
          :class="{ 'col-start-2 row-start-1 justify-self-end': itemLabel }"
        >
          <template v-if="reorderable">
            <button
              type="button"
              :class="[actionSizeClass, moveButtonClass]"
              :disabled="disabled || index === 0"
              :title="t('common.moveUp')"
              :aria-label="t('common.moveUp')"
              :data-testid="testIdFor(`move-up-${index}`)"
              @click="emit('move', index, index - 1)"
            >
              <Icon name="arrowUp" size="sm" />
            </button>
            <button
              type="button"
              :class="[actionSizeClass, moveButtonClass]"
              :disabled="disabled || index === items.length - 1"
              :title="t('common.moveDown')"
              :aria-label="t('common.moveDown')"
              :data-testid="testIdFor(`move-down-${index}`)"
              @click="emit('move', index, index + 1)"
            >
              <Icon name="arrowDown" size="sm" />
            </button>
          </template>
          <button
            v-if="removable"
            type="button"
            :class="[actionSizeClass, removeButtonClass]"
            :disabled="disabled || items.length <= min"
            :title="resolvedRemoveLabel"
            :aria-label="resolvedRemoveLabel"
            :data-testid="testIdFor(`remove-${index}`)"
            @click="emit('remove', index)"
          >
            <Icon name="trash" size="sm" />
          </button>
        </div>
      </div>
    </TransitionGroup>

    <p v-if="error" class="text-xs text-red-500" role="alert">{{ error }}</p>

    <div v-if="addPlacement === 'footer'">
      <button
        type="button"
        class="btn btn-secondary"
        :disabled="addBlocked"
        :data-testid="testIdFor('add')"
        @click="requestAdd"
      >
        <Icon name="plus" size="sm" class="mr-1.5" />
        {{ resolvedAddLabel }}
      </button>
    </div>

    <slot name="footer" />
  </div>
</template>

<script setup lang="ts" generic="T">
import { computed, nextTick, ref, useId, useSlots, watch, type ComponentPublicInstance } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import { prepareListLeave, restoreEnteringElement } from '@/utils/leavingElement'
import { createStableObjectKeyResolver } from '@/utils/stableObjectKey'

// RuleListEditor.vue renders editable rows and requests collection operations from its
// parent. Object rows receive stable display keys, while primitive rows use indices
// unless the caller supplies itemKey. Surviving row identity lets input nodes remain
// associated with their data across removal and reordering.
//
// Leave hooks count outgoing rows so the empty message appears after their transitions
// finish. A trailing row remains in document flow while fading; earlier rows yield
// space to following rows. Focus requests span the add event and the next DOM update,
// so externally appended rows do not receive focus automatically.

// props holds parent-supplied component options consumed by the rendering and
// interaction handlers below.
const props = withDefaults(
  defineProps<{
    items: T[] // Parent-owned editable row collection.
    // Supplies display keys when object identity or primitive indices do not identify
    // the intended row across collection updates.
    itemKey?: (item: T, index: number) => string | number
    title?: string // Dialog or section heading.
    hint?: string // Optional explanatory hint text.
    titleStyle?: 'label' | 'section' // Editor heading style recipe.
    addLabel?: string // Add action label override.
    addPlacement?: 'header' | 'footer' // Add button placement selector.
    addDisabled?: boolean // Independent add-action disabled flag.
    removeLabel?: string // Remove action label override.
    emptyText?: string // Empty collection message text.
    // Disables removal when the collection is at or below this count; the default zero
    // permits deleting the final row.
    min?: number
    // Disables adding when the collection reaches this count; undefined leaves the
    // maximum unbounded in the shared list shell.
    max?: number
    disabled?: boolean // Disables user control interaction.
    removable?: boolean // Remove button visibility flag.
    reorderable?: boolean // Move button visibility flag.
    variant?: 'line' | 'card' // Row presentation recipe selector.
    // Formats a row heading from its zero-based index and enables the labeled-card
    // action layout.
    itemLabel?: (index: number) => string
    // Enables row transitions; callers using primitive rows can disable animation when
    // index keys would misidentify departing content.
    animated?: boolean
    error?: string // Visible field error feedback.
    testId?: string // Optional test identifier prefix.
  }>(),
  {
    titleStyle: 'label',
    addPlacement: 'header',
    min: 0,
    removable: true,
    variant: 'line',
    animated: true,
  },
)

// emit publishes the declared component notifications so the parent can update its
// owned state.
const emit = defineEmits<{
  add: [] // New row request payload.
  remove: [index: number] // Removed row event payload.
  move: [from: number, to: number] // Source and destination indices.
}>()

defineSlots<{
  // row renders the editable content for the supplied item and index.
  row(props: {
    item: T // Current editable row object.
    index: number // Zero-based editable row index.
  }): unknown
  // title-suffix renders optional content beside the editor title text.
  'title-suffix'?(): unknown
  // header-actions renders optional parent actions beside the add button.
  'header-actions'?(): unknown
  // header-extra renders parent content between the header and row list.
  'header-extra'?(): unknown
  // footer renders parent content after the list and its actions.
  footer?(): unknown
}>()

// t resolves the component action and status labels using the active application
// locale.
const { t } = useI18n()
// slots controls optional header visibility, listId distinguishes nested row
// ownership, listRef exposes rendered rows, and resolveObjectKey retains display
// identity for object items.
const slots = useSlots()
const listId = useId()
const listRef = ref<ComponentPublicInstance | null>(null)
const resolveObjectKey = createStableObjectKeyResolver<object>('rule-list-row')

// hasHeader determines header presence, resolvedAddLabel and resolvedRemoveLabel
// provide action text, and addBlocked combines global disabling, add-only disabling,
// and maximum row count.
const hasHeader = computed(
  () =>
    Boolean(props.title || props.hint || slots['title-suffix'] || slots['header-actions']) ||
    props.addPlacement === 'header',
)
const resolvedAddLabel = computed(() => props.addLabel || t('common.add'))
const resolvedRemoveLabel = computed(() => props.removeLabel || t('common.delete'))
const addBlocked = computed(
  () =>
    props.disabled ||
    props.addDisabled ||
    (props.max !== undefined && props.items.length >= props.max),
)

// rowClass selects card or separator presentation; actionSizeClass fits labeled cards,
// while moveButtonClass and removeButtonClass provide neutral and destructive hover
// feedback respectively.
const rowClass = computed(() =>
  props.variant === 'card'
    ? 'rounded-surface border border-gray-200 bg-gray-50/50 p-4 dark:border-dark-600 dark:bg-dark-800/40'
    : 'border-b border-gray-200 pb-3 last:border-b-0 last:pb-0 dark:border-dark-600',
)

const actionSizeClass = computed(() =>
  props.variant === 'card' && props.itemLabel ? 'btn-icon-sm' : 'btn-icon',
)
const moveButtonClass =
  'text-gray-400 transition-colors hover:bg-gray-100 hover:text-gray-700 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary-500 disabled:cursor-not-allowed disabled:opacity-40 disabled:hover:bg-transparent disabled:hover:text-gray-400 dark:text-dark-400 dark:hover:bg-dark-800 dark:hover:text-dark-100'
const removeButtonClass =
  'text-gray-400 transition-colors hover:bg-red-50 hover:text-red-500 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary-500 disabled:cursor-not-allowed disabled:opacity-40 disabled:hover:bg-transparent disabled:hover:text-gray-400 dark:text-dark-400 dark:hover:bg-red-900/20 dark:hover:text-red-400'

// resolveKey returns the caller key, stable object key, or primitive row index.
// Callers using primitive rows can disable animation when index identity would
// associate an outgoing transition with another item.
const resolveKey = (item: T, index: number): string | number => {
  if (props.itemKey) return props.itemKey(item, index)
  return typeof item === 'object' && item !== null ? resolveObjectKey(item) : index
}

// testIdFor builds a suffixed test identifier when a prefix is supplied. It returns
// undefined for an empty prefix so Vue omits optional test attributes on the rendered
// elements.
const testIdFor = (suffix: string) => (props.testId ? `${props.testId}-${suffix}` : undefined)

// leavingCount tracks outgoing row transitions so an empty list message waits until
// all departing rows finish or cancel.
const leavingCount = ref(0)

// onBeforeLeave prepares an outgoing row and tracks its pending transition. It
// examines following siblings after skipping inert rows, since those siblings are also
// leaving and cannot fill the removed position.
//
// A row with no remaining following sibling stays in document flow until its exit
// completes. Earlier rows use the shared absolute leave preparation, allowing
// surviving rows to occupy their position while the outgoing content fades.
const onBeforeLeave = (element: Element) => {
  leavingCount.value += 1
  prepareListLeave(element)
  let sibling = element.nextElementSibling
  // Ignore siblings already leaving when deciding whether another live row can fill
  // this position; outgoing rows cannot supply that replacement layout.
  while (sibling?.hasAttribute('inert')) sibling = sibling.nextElementSibling
  if (!sibling) element.classList.add('rule-list-leave-in-flow')
}

// onLeaveDone releases one outgoing row from the shared pending transition count and
// removes its temporary flow class. TransitionGroup calls this hook for both completed
// and cancelled leaves, so either outcome lets the row stop delaying the empty state.
// Several rows can finish independently; the template waits for the count to reach
// zero and for the parent collection to be empty before showing its message. The count
// stays nonnegative when cleanup receives a zero count.
const onLeaveDone = (element: Element) => {
  // Release pending-row accounting and temporary layout on either exit path so a
  // cancelled leave cannot keep an empty collection waiting for completion.
  leavingCount.value = Math.max(0, leavingCount.value - 1)
  element.classList.remove('rule-list-leave-in-flow')
}

// pendingFocus spans an add-button request and its next DOM update, distinguishing
// user additions from imported or preset rows.
let pendingFocus = false

// requestAdd marks a user-triggered addition and publishes the add request before
// waiting for Vue's next DOM update. The parent owns the collection and must append
// synchronously in its add handler for this focus request to cover that update.
//
// The post-flush length watcher sees the marker after the appended row renders and
// focuses its first editable control. Clearing the marker after nextTick limits the
// request to that update. Imports, presets, and asynchronous additions arriving after
// that tick do not inherit this button action's focus request.
const requestAdd = async () => {
  // The marker precedes the parent add handler so its synchronous array update is
  // visible to the post-render focus watcher during this tick.
  pendingFocus = true
  emit('add')
  await nextTick()
  pendingFocus = false
}

// focusableSelector targets editable inputs, textareas, and shared selection triggers
// when a newly added row receives focus.
const focusableSelector = [
  'input:not([type="hidden"]):not(:disabled):not(.sr-only)',
  'textarea:not(:disabled)',
  '.input-trigger',
].join(', ')

// focusRow focuses the first editable control in the indexed rendered row. The
// instance identifier excludes nested lists, inert rows are skipped, and absent rows
// or controls produce no focus change.
const focusRow = (index: number) => {
  const root = listRef.value?.$el as HTMLElement | undefined
  const rows = Array.from(
    root?.querySelectorAll<HTMLElement>(`[data-rule-list-row="${listId}"]:not([inert])`) ?? [],
  )
  rows[index]?.querySelector<HTMLElement>(focusableSelector)?.focus()
}

// Observe length after DOM updates so a user-requested appended row exists before
// focusing it; imported rows lack the pending-focus marker.
watch(
  () => props.items.length,
  (length, previous) => {
    if (pendingFocus && length > previous) focusRow(length - 1)
  },
  { flush: 'post' },
)

defineExpose({ focusRow })
</script>

<style scoped>
/*
  Trailing rows override the shared absolute leave positioning so following content
  moves upward after the fade completes.
*/
.rule-list-leave-in-flow.motion-list-leave-active {
  position: static;
  width: auto;
}
</style>
