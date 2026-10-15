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
              <span class="text-sm font-semibold text-gray-900 dark:text-dark-50">{{ t('common.filter') }}</span>
              <span v-if="activeCount" class="filter-panel-count">{{ activeCount }}</span>
            </div>
            <p v-if="description" class="mt-0.5 text-xs text-gray-500 dark:text-dark-400">{{ description }}</p>
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
            <span v-if="chip.label" class="shrink-0 text-gray-500 dark:text-dark-400">{{ chip.label }}</span>
            <span class="min-w-0 truncate font-medium text-gray-900 dark:text-dark-50" :title="chip.text">{{ chip.text }}</span>
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
import { computed, onBeforeUnmount, onMounted, provide, ref, shallowReactive, type ComputedRef } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import MotionTransition from '@/components/common/MotionTransition.vue'
import { getFloatingPanelPosition } from '@/utils/floatingPanel'
import { FILTER_PANEL_KEY, type FilterChip } from './filterPanel'

type FilterColumns = 1 | 2 | 3

const props = withDefaults(defineProps<{
  activeCount: number
  // columns 决定面板宽度和栅格列数，条件越多列数越多。
  columns?: FilterColumns
  description?: string
  // keepMounted 让面板收起后保留 DOM，适合内部有输入状态或测试需要直接访问字段的场景。
  keepMounted?: boolean
  triggerClass?: string
}>(), {
  columns: 1,
  description: '',
  keepMounted: false,
  triggerClass: 'btn btn-secondary btn-icon',
})
const emit = defineEmits<{ (event: 'reset'): void }>()
const { t } = useI18n()

// 各列数对应的面板宽度（rem）和栅格配方；窄屏统一退回单列。
const PANEL_WIDTH_REM: Record<FilterColumns, number> = { 1: 18, 2: 34, 3: 48 }
const COLUMN_CLASSES: Record<FilterColumns, string> = {
  1: 'grid-cols-1',
  2: 'grid-cols-1 sm:grid-cols-2',
  3: 'grid-cols-1 sm:grid-cols-2 lg:grid-cols-3',
}

const open = ref(false)
const rootRef = ref<HTMLElement | null>(null)
const triggerRef = ref<HTMLButtonElement | null>(null)
const panelStyle = ref<Record<string, string>>({})

// 各字段登记自己的已选条件，面板顶部按字段在面板里的先后顺序展示。
const registeredChips = shallowReactive(new Set<ComputedRef<FilterChip | null>>())
provide(FILTER_PANEL_KEY, {
  register: (chip) => {
    registeredChips.add(chip)
    return () => registeredChips.delete(chip)
  },
})
const chips = computed(() => Array.from(registeredChips)
  .map((chip) => chip.value)
  .filter((chip): chip is FilterChip => chip !== null)
  .sort((a, b) => {
    if (!a.el || !b.el) return 0
    return a.el.compareDocumentPosition(b.el) & Node.DOCUMENT_POSITION_FOLLOWING ? -1 : 1
  }))

// 面板默认左缘对齐触发器，右侧放不下时向左平移，水平夹取规则复用公共浮层定位。
function updatePosition() {
  const rect = rootRef.value?.getBoundingClientRect()
  if (!rect) return
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

function toggle() {
  open.value = !open.value
  if (open.value) updatePosition()
}

// 键盘关闭后回到筛选按钮，便于继续操作工具栏。
function closeWithFocus() {
  open.value = false
  triggerRef.value?.focus()
}

// Select 的 Teleport 面板挂在 body 上，点选候选项时不能误关外层筛选面板。
function closeOutside(event: MouseEvent) {
  const target = event.target
  if (!(target instanceof Node) || rootRef.value?.contains(target)) return
  if (target instanceof Element && target.closest('.select-dropdown-portal')) return
  open.value = false
}

function handleResize() {
  if (open.value) updatePosition()
}

onMounted(() => {
  // 外层弹窗会停止点击冒泡，捕获阶段仍可判断点击是否来自筛选面板。
  document.addEventListener('click', closeOutside, true)
  window.addEventListener('resize', handleResize)
})

onBeforeUnmount(() => {
  document.removeEventListener('click', closeOutside, true)
  window.removeEventListener('resize', handleResize)
})

defineExpose({ close: () => { open.value = false } })
</script>

<style scoped>
/* 有生效条件时按钮描边和图标换成品牌色，右上角再挂数量角标。 */
.filter-trigger-active {
  @apply border-primary-300 text-primary-700 dark:border-primary-500/40 dark:text-primary-400;
}

.filter-trigger-count {
  @apply pointer-events-none absolute -right-1.5 -top-1.5 inline-flex h-4 min-w-4 items-center justify-center rounded-full px-1;
  @apply bg-primary-600 text-xs font-semibold leading-none text-white;
  @apply ring-2 ring-white dark:ring-dark-950;
}

/* 面板外观与日期范围选择器一致：surface 圆角、淡描边、柔和阴影。 */
.filter-panel {
  @apply absolute top-full z-modal-nested mt-2 flex flex-col overflow-hidden;
  @apply max-h-[min(70vh,42rem)];
  @apply bg-white dark:bg-dark-900;
  @apply rounded-surface border border-primary-900/10 dark:border-dark-600;
  @apply shadow-lg shadow-black/10 dark:shadow-black/30;
}

/* 头部、已选条件和字段区之间不画分割线，只靠留白区分层次。 */
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

/* 已选条件标签：中性灰底，标签名弱化、取值加重，过长的取值截断并用 title 展示全文。 */
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
