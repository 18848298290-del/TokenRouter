<template>
  <div ref="rootRef" class="relative shrink-0" @keydown.esc.stop.prevent="closeWithFocus">
    <button
      ref="triggerRef"
      type="button"
      class="relative"
      :class="[triggerClass, activeCount ? 'filter-trigger-active' : '']"
      :aria-label="t('common.filter')"
      :title="t('common.filter')"
      :aria-expanded="open"
      aria-haspopup="dialog"
      @click="toggle"
    >
      <Icon name="filter" size="sm" />
      <span v-if="activeCount" class="filter-trigger-count">{{ activeCount }}</span>
    </button>

    <MotionTransition name="dropdown-fade" :persisted="keepMounted">
      <div
        v-if="keepMounted || open"
        v-show="open"
        :inert="!open || undefined"
        class="filter-panel"
        :style="panelStyle"
        role="dialog"
        :aria-label="t('common.filter')"
        @click.stop
      >
        <div class="filter-panel-header">
          <div class="min-w-0">
            <div class="flex items-center gap-2">
              <span class="text-sm font-semibold text-gray-900 dark:text-dark-50">{{
                t('common.filter')
              }}</span>
              <span v-if="activeCount" class="filter-panel-count">{{ activeCount }}</span>
            </div>
            <p v-if="description" class="mt-0.5 text-xs text-gray-500 dark:text-dark-400">
              {{ description }}
            </p>
          </div>
          <button
            type="button"
            class="filter-panel-reset"
            data-testid="filter-reset"
            :disabled="!activeCount"
            @click="emit('reset')"
          >
            <Icon name="refresh" size="xs" :animate-on-hover="false" />
            {{ t('common.reset') }}
          </button>
        </div>
        <div v-if="chips.length" class="filter-panel-chips" data-testid="filter-chips">
          <span v-for="(chip, index) in chips" :key="`${chip.label}-${index}`" class="filter-chip">
            <span v-if="chip.label" class="shrink-0 text-gray-500 dark:text-dark-400">{{
              chip.label
            }}</span>
            <span
              class="min-w-0 truncate font-medium text-gray-900 dark:text-dark-50"
              :title="chip.text"
              >{{ chip.text }}</span
            >
            <button
              type="button"
              class="filter-chip-remove"
              :aria-label="`${t('common.remove')} ${chip.label}`"
              @click="chip.clear()"
            >
              <Icon name="x" size="xs" :animate-on-hover="false" />
            </button>
          </span>
        </div>
        <div class="filter-panel-body" :class="COLUMN_CLASSES[columns]">
          <slot />
        </div>
      </div>
    </MotionTransition>
  </div>
</template>

<script setup lang="ts">
import {
  computed,
  onBeforeUnmount,
  onMounted,
  provide,
  ref,
  shallowReactive,
  type ComputedRef,
} from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import MotionTransition from '@/components/common/MotionTransition.vue'
import { getFloatingPanelPosition } from '@/utils/floatingPanel'
import { FILTER_PANEL_KEY, type FilterChip } from './filterPanel'

// FilterDropdown.vue presents a responsive filter panel and the active chips
// registered by its fields. The panel gathers reactive chip records through injection
// and orders them by their current elements, so chip order follows the visible field
// layout. The parent owns filter values and reset handling.
//
// Outside-click handling treats teleported Select menus as part of the interaction
// area, because those options are attached to the document body. Panel width is
// measured in root-font units and clamped through the shared position helper; the
// resulting horizontal coordinate is converted back into the relatively positioned
// wrapper.

// FilterColumns selects the responsive panel width and field grid recipe.
type FilterColumns = 1 | 2 | 3

// props holds parent-supplied component options consumed by the rendering and
// interaction handlers below.
const props = withDefaults(
  defineProps<{
    activeCount: number // Active filter condition count.

    // Selects panel width and responsive grid columns; narrow viewports retain the
    // single-column field layout.
    columns?: FilterColumns
    description?: string // Optional panel explanatory text.

    // Retains child DOM while closed so nested controls keep their current local input
    // state between openings.
    keepMounted?: boolean
    triggerClass?: string // Trigger button class list.
  }>(),
  {
    columns: 1,
    description: '',
    keepMounted: false,
    triggerClass: 'btn btn-secondary btn-icon',
  },
)
// emit publishes the declared component notifications so the parent can update its
// owned state.
const emit = defineEmits<{
  // Requests parent filter reset.
  (event: 'reset'): void
}>()
// t resolves the component action and status labels using the active application
// locale.
const { t } = useI18n()

// PANEL_WIDTH_REM and COLUMN_CLASSES map each column count to its panel width and
// responsive field layout, keeping narrow viewports at one column.
const PANEL_WIDTH_REM: Record<FilterColumns, number> = { 1: 18, 2: 34, 3: 48 }
const COLUMN_CLASSES: Record<FilterColumns, string> = {
  1: 'grid-cols-1',
  2: 'grid-cols-1 sm:grid-cols-2',
  3: 'grid-cols-1 sm:grid-cols-2 lg:grid-cols-3',
}

// open controls visibility, rootRef supplies bounds and click ownership, triggerRef
// restores keyboard focus, and panelStyle holds the clamped horizontal placement.
const open = ref(false)
const rootRef = ref<HTMLElement | null>(null)
const triggerRef = ref<HTMLButtonElement | null>(null)
const panelStyle = ref<Record<string, string>>({})

// registeredChips holds field-owned computed chip records; each registration returns a
// removal callback for the field lifetime.
const registeredChips = shallowReactive(new Set<ComputedRef<FilterChip | null>>())
provide(FILTER_PANEL_KEY, {
  register: (chip) => {
    // The field owns registration cleanup; returning deletion with registration keeps
    // removed field chips out of the parent summary after unmount.
    registeredChips.add(chip)
    return () => registeredChips.delete(chip)
  },
})
// chips filters inactive registrations and sorts active entries by field DOM position
// for the panel summary.
const chips = computed(() =>
  Array.from(registeredChips)
    .map((chip) => chip.value)
    .filter((chip): chip is FilterChip => chip !== null)
    .sort((a, b) => {
      if (!a.el || !b.el) return 0
      return a.el.compareDocumentPosition(b.el) & Node.DOCUMENT_POSITION_FOLLOWING ? -1 : 1
    }),
)

// updatePosition measures the wrapper and delegates viewport clamping to the shared
// helper. It converts rem widths using the root font size and stores an offset
// relative to the wrapper.
function updatePosition() {
  const rect = rootRef.value?.getBoundingClientRect()
  if (!rect) return
  // Use the root font size to convert panel recipes into pixels; unavailable numeric
  // font sizing uses sixteen so width calculations remain usable.
  const rem = Number.parseFloat(getComputedStyle(document.documentElement).fontSize) || 16
  const viewportWidth = document.documentElement.clientWidth || window.innerWidth
  const position = getFloatingPanelPosition(rect, viewportWidth, window.innerHeight, {
    maxWidth: PANEL_WIDTH_REM[props.columns] * rem,
    align: 'left',
    viewportPadding: rem,
  })
  panelStyle.value = {
    width: `${position.width}px`,
    left: `${position.left - rect.left}px`,
  }
}

// toggle changes panel visibility and measures its position when opening. Closing
// keeps child state when keepMounted is enabled, with the template applying hidden and
// inert states during exit.
function toggle() {
  open.value = !open.value
  if (open.value) updatePosition()
}

// closeWithFocus closes the filter panel and focuses its trigger button. Keyboard
// dismissal uses this path so users can continue moving through the surrounding
// toolbar after leaving the panel.
function closeWithFocus() {
  open.value = false
  triggerRef.value?.focus()
}

// closeOutside dismisses clicks outside the wrapper except teleported Select menus.
// Those menus live under body, so their option selection must remain inside the
// enclosing filter interaction.
function closeOutside(event: MouseEvent) {
  const target = event.target
  if (!(target instanceof Node) || rootRef.value?.contains(target)) return
  // Select options are teleported outside this wrapper; exempt their portal clicks so
  // choosing a nested value does not dismiss the filter.
  if (target instanceof Element && target.closest('.select-dropdown-portal')) return
  open.value = false
}

// handleResize refreshes panel placement when the open panel receives a viewport
// resize. Closed panels defer measurement until their next opening, when their current
// wrapper bounds are available.
function handleResize() {
  if (open.value) updatePosition()
}

// Capture document clicks before dialog handlers stop bubbling, allowing this panel to
// dismiss when another region inside the dialog is clicked.
onMounted(() => {
  document.addEventListener('click', closeOutside, true)
  window.addEventListener('resize', handleResize)
})

// Release document and viewport listeners with the panel owner so removed filters
// cannot receive clicks or schedule fresh position updates.
onBeforeUnmount(() => {
  document.removeEventListener('click', closeOutside, true)
  window.removeEventListener('resize', handleResize)
})

defineExpose({
  // close hides the panel through the exposed component handle.
  close: () => {
    open.value = false
  },
})
</script>

<style scoped>
/*
  Active filters use a brand-colored trigger outline and icon with a count badge at
  the upper right.
*/
.filter-trigger-active {
  @apply border-primary-300 text-primary-700 dark:border-primary-500/40 dark:text-primary-400;
}

.filter-trigger-count {
  @apply pointer-events-none absolute -right-1.5 -top-1.5 inline-flex h-4 min-w-4 items-center justify-center rounded-full px-1;
  @apply bg-primary-600 text-xs font-semibold leading-none text-white;
  @apply ring-2 ring-white dark:ring-dark-950;
}

/* The panel uses the date-range picker surface radius, subdued outline, and shadow. */
.filter-panel {
  @apply absolute top-full z-modal-nested mt-2 flex flex-col overflow-hidden;
  @apply max-h-[min(70vh,42rem)];
  @apply bg-white dark:bg-dark-900;
  @apply rounded-surface border border-primary-900/10 dark:border-dark-600;
  @apply shadow-lg shadow-black/10 dark:shadow-black/30;
}

/* Spacing separates the header, selected chips, and editable fields within the panel. */
.filter-panel-header {
  @apply flex shrink-0 items-center justify-between gap-3 px-4 pb-1 pt-3;
}

.filter-panel-count {
  @apply inline-flex h-5 min-w-5 items-center justify-center rounded-full px-1.5;
  @apply bg-primary-50 text-xs font-medium text-primary-700 dark:bg-primary-500/10 dark:text-primary-400;
}

.filter-panel-reset {
  @apply inline-flex shrink-0 items-center gap-1 rounded-compact px-2 py-1 text-xs font-medium;
  @apply text-gray-500 hover:bg-gray-100 hover:text-gray-900 dark:text-dark-300 dark:hover:bg-dark-800 dark:hover:text-dark-50;
  @apply transition-colors duration-fast;
  @apply disabled:pointer-events-none disabled:opacity-40;
}

.filter-panel-chips {
  @apply flex shrink-0 flex-wrap gap-1.5 px-4 pt-2;
}

/*
  Selected chips use a neutral background and emphasize values over field labels;
  truncated values retain their full text in title attributes.
*/
.filter-chip {
  @apply inline-flex max-w-full items-center gap-1 rounded-compact py-0.5 pl-2 pr-0.5 text-xs;
  @apply bg-gray-100 dark:bg-dark-800;
}

.filter-chip-remove {
  @apply inline-flex h-5 w-5 shrink-0 items-center justify-center rounded-compact;
  @apply text-gray-400 hover:bg-gray-200 hover:text-gray-700 dark:text-dark-400 dark:hover:bg-dark-700 dark:hover:text-dark-100;
  @apply transition-colors duration-fast;
}

.filter-panel-body {
  @apply grid min-h-0 gap-x-3 gap-y-4 overflow-y-auto p-4;
}
</style>
