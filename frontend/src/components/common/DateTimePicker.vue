<template>
  <div class="relative" ref="containerRef" @keydown.esc="handleEscape">
    <button
      :id="id"
      type="button"
      @click="toggle"
      :aria-expanded="isOpen"
      aria-haspopup="dialog"
      :class="[
        'input input-trigger',
        modelValue && 'pr-10',
        isOpen && 'date-time-picker-trigger-open'
      ]"
    >
      <span class="date-time-picker-icon">
        <Icon name="calendar" size="sm" />
      </span>
      <span
        :class="[
          'date-time-picker-value',
          !modelValue && 'date-time-picker-placeholder'
        ]"
      >
        {{ displayValue }}
      </span>
      <span v-if="!modelValue" class="date-time-picker-icon">
        <Icon
          name="chevronDown"
          size="sm"
          :class="['transition-transform duration-normal', isOpen && 'rotate-180']"
          :animate-on-hover="false"
        />
      </span>
    </button>

    <!-- 清除按钮和触发器是兄弟节点，按钮里不能再嵌套按钮 -->
    <button
      v-if="modelValue"
      type="button"
      class="date-time-picker-clear btn-icon-sm"
      :aria-label="t('dates.clear')"
      @click="clear"
    >
      <Icon name="x" size="sm" />
    </button>

    <MotionTransition name="dropdown-fade">
      <div v-if="isOpen" class="date-time-picker-dropdown" :style="dropdownStyle" role="dialog">
        <div class="date-time-picker-body">
          <!-- 快捷选项：桌面端是左侧纵向列表，窄屏折成可换行的 chip -->
          <div v-if="presets?.length" class="date-time-picker-presets">
            <p v-if="presetsTitle" class="date-time-picker-presets-title">{{ presetsTitle }}</p>
            <button
              v-for="preset in presets"
              :key="preset.key"
              type="button"
              :class="['date-time-picker-preset', isPresetActive(preset) && 'date-time-picker-preset-active']"
              @click="selectPreset(preset)"
            >
              <span class="truncate">{{ preset.label }}</span>
              <Icon
                v-if="isPresetActive(preset)"
                name="check"
                size="sm"
                class="date-time-picker-preset-check"
                :animate-on-hover="false"
              />
            </button>
          </div>

          <!-- 单月日历，点击即选中日期 -->
          <div class="date-time-picker-calendar">
            <div class="date-time-picker-header">
              <button
                type="button"
                class="date-time-picker-nav btn-icon-sm"
                :aria-label="t('dates.previousMonth')"
                @click="shiftMonth(-1)"
              >
                <Icon name="chevronLeft" size="sm" />
              </button>
              <span class="date-time-picker-title">{{ monthLabel }}</span>
              <button
                type="button"
                class="date-time-picker-nav btn-icon-sm"
                :aria-label="t('dates.nextMonth')"
                @click="shiftMonth(1)"
              >
                <Icon name="chevronRight" size="sm" />
              </button>
            </div>

            <div class="date-time-picker-grid">
              <span v-for="weekday in weekdayLabels" :key="weekday" class="date-time-picker-weekday">
                {{ weekday }}
              </span>
              <div v-for="cell in calendarCells" :key="cell.key" class="date-time-picker-day-cell">
                <button
                  v-if="cell.date"
                  type="button"
                  :disabled="cell.disabled"
                  :aria-label="cell.ariaLabel"
                  :aria-pressed="cell.selected"
                  :class="[
                    'date-time-picker-day',
                    cell.selected && 'date-time-picker-day-selected',
                    cell.isToday && 'date-time-picker-day-today'
                  ]"
                  @click="selectDay(cell.date)"
                >
                  {{ cell.day }}
                </button>
              </div>
            </div>
          </div>

          <!-- 时、分两列滚动选择，列高跟随左侧日历 -->
          <div class="date-time-picker-time">
            <div class="date-time-picker-header date-time-picker-time-header">
              <span class="date-time-picker-title tabular-nums">{{ timeLabel }}</span>
            </div>
            <div class="date-time-picker-columns-frame">
              <div class="date-time-picker-columns">
                <div
                  v-for="column in timeColumns"
                  :key="column.key"
                  :ref="(el) => setColumnRef(column.key, el)"
                  class="date-time-picker-column"
                  role="listbox"
                  :aria-label="column.label"
                >
                  <button
                    v-for="value in column.values"
                    :key="value"
                    type="button"
                    role="option"
                    :aria-selected="column.selected === value"
                    :data-value="value"
                    :class="[
                      'date-time-picker-option',
                      column.selected === value && 'date-time-picker-option-selected'
                    ]"
                    @click="column.select(value)"
                  >
                    {{ pad(value) }}
                  </button>
                </div>
              </div>
            </div>
          </div>
        </div>

        <!-- 底栏：左侧一键选当前时间，右侧取消与确定 -->
        <div class="date-time-picker-actions">
          <button type="button" class="date-time-picker-now" @click="selectNow">
            {{ t('dates.now') }}
          </button>
          <div class="flex shrink-0 items-center gap-2">
            <button type="button" class="btn btn-secondary btn-sm h-8" @click="close">
              {{ t('common.cancel') }}
            </button>
            <button type="button" class="btn btn-primary btn-sm h-8" :disabled="!localDate" @click="apply">
              {{ t('common.confirm') }}
            </button>
          </div>
        </div>
      </div>
    </MotionTransition>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, type ComponentPublicInstance } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import MotionTransition from '@/components/common/MotionTransition.vue'
import { useFloatingMotion } from '@/composables/useFloatingMotion'
import { getFloatingPanelPosition, type FloatingPanelPosition } from '@/utils/floatingPanel'

interface CalendarCell {
  key: string
  date: string | null
  day: number
  ariaLabel: string
  disabled: boolean
  isToday: boolean
  selected: boolean
}

type TimeColumnKey = 'hour' | 'minute'

export interface DateTimePreset {
  key: string
  label: string
  /** 点击时计算目标值，格式同 modelValue。 */
  resolve: () => string
}

interface Props {
  /** 本地时间，格式和 datetime-local 一致（YYYY-MM-DDTHH:mm），空字符串表示未设置。 */
  modelValue: string
  /** 未设置时触发器显示的文字，可以写成空值的含义，例如“立即生效”。 */
  placeholder?: string
  /** 最早可选的日期时间，格式同 modelValue。早于它所在日期的日子置灰。 */
  min?: string
  /** 弹层左侧的快捷选项，点击后直接写回并关闭弹层。 */
  presets?: DateTimePreset[]
  /** 快捷选项列表的标题。 */
  presetsTitle?: string
  id?: string
}

const props = defineProps<Props>()
const emit = defineEmits<{ 'update:modelValue': [value: string] }>()

const { t, locale } = useI18n()

const isOpen = ref(false)
const containerRef = ref<HTMLElement | null>(null)
const dropdownPosition = ref<FloatingPanelPosition | null>(null)
const columnRefs: Partial<Record<TimeColumnKey, HTMLElement>> = {}

// 弹层里的草稿，点确定后才写回 modelValue。
const localDate = ref('')
const localHour = ref(0)
const localMinute = ref(0)
// 日历当前展示月份的 1 日，格式 YYYY-MM-DD。
const viewMonth = ref('')

const dropdownWidth = computed(() => (props.presets?.length ? 544 : 432))
const dropdownMargin = 12

const hours = Array.from({ length: 24 }, (_, index) => index)
const minutes = Array.from({ length: 60 }, (_, index) => index)

const pad = (value: number): string => String(value).padStart(2, '0')

const formatDate = (date: Date): string => {
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`
}

const parseLocalDate = (value: string): Date => new Date(`${value.slice(0, 10)}T00:00:00`)

// 解析 YYYY-MM-DDTHH:mm 开头的值，秒和毫秒忽略。
const parseValue = (value: string): { date: string; hour: number; minute: number } | null => {
  const match = /^(\d{4,}-\d{2}-\d{2})T(\d{2}):(\d{2})/.exec(value)
  if (!match) return null
  return { date: match[1], hour: Number(match[2]), minute: Number(match[3]) }
}

const dropdownStyle = computed(() => {
  const position = dropdownPosition.value
  if (!position) return {}
  return {
    left: `${position.left}px`,
    top: position.top === null ? 'auto' : `${position.top}px`,
    bottom: position.bottom === null ? 'auto' : `${position.bottom}px`,
    width: `${position.width}px`,
    maxHeight: `${position.maxHeight}px`
  }
})

const displayValue = computed(() => {
  const parsed = parseValue(props.modelValue)
  if (!parsed) return props.placeholder || t('dates.selectDateTime')
  const date = parseLocalDate(parsed.date)
  date.setHours(parsed.hour, parsed.minute)
  return date.toLocaleString(locale.value, { dateStyle: 'medium', timeStyle: 'short' })
})

const timeLabel = computed(() => `${pad(localHour.value)}:${pad(localMinute.value)}`)

const monthLabel = computed(() => {
  if (!viewMonth.value) return ''
  return parseLocalDate(viewMonth.value).toLocaleDateString(locale.value, { year: 'numeric', month: 'long' })
})

// 中文日历以周一开头，英文以周日开头。
const weekStartsOn = computed(() => (locale.value === 'zh-Hans' ? 1 : 0))

const weekdayLabels = computed(() => {
  // 2023-01-01 是周日，以它为基准依次生成一周的星期缩写。
  return Array.from({ length: 7 }, (_, index) => {
    const date = new Date(2023, 0, 1 + ((index + weekStartsOn.value) % 7))
    return date.toLocaleDateString(locale.value, { weekday: 'narrow' })
  })
})

const minDate = computed(() => parseValue(props.min ?? '')?.date ?? '')

// 固定生成 6 周 42 格，切换月份时面板高度不变。
const calendarCells = computed<CalendarCell[]>(() => {
  if (!viewMonth.value) return []
  const first = parseLocalDate(viewMonth.value)
  const leading = (first.getDay() - weekStartsOn.value + 7) % 7
  const daysInMonth = new Date(first.getFullYear(), first.getMonth() + 1, 0).getDate()
  const today = formatDate(new Date())

  return Array.from({ length: 42 }, (_, index) => {
    const day = index - leading + 1
    if (day < 1 || day > daysInMonth) {
      return {
        key: `blank-${index}`,
        date: null,
        day: 0,
        ariaLabel: '',
        disabled: true,
        isToday: false,
        selected: false
      }
    }
    const current = new Date(first.getFullYear(), first.getMonth(), day)
    const date = formatDate(current)
    return {
      key: date,
      date,
      day,
      ariaLabel: current.toLocaleDateString(locale.value, { dateStyle: 'full' }),
      disabled: !!minDate.value && date < minDate.value,
      isToday: date === today,
      selected: date === localDate.value
    }
  })
})

// 用户先点时间再点日期时，日期默认取今天，确定按钮随即可用。
const ensureDate = () => {
  if (!localDate.value) localDate.value = formatDate(new Date())
}

const timeColumns = computed(() => [
  {
    key: 'hour' as const,
    label: t('dates.hour'),
    values: hours,
    selected: localHour.value,
    select: (value: number) => {
      localHour.value = value
      ensureDate()
    }
  },
  {
    key: 'minute' as const,
    label: t('dates.minute'),
    values: minutes,
    selected: localMinute.value,
    select: (value: number) => {
      localMinute.value = value
      ensureDate()
    }
  }
])

const setColumnRef = (key: TimeColumnKey, el: Element | ComponentPublicInstance | null) => {
  if (el instanceof HTMLElement) {
    columnRefs[key] = el
  } else {
    delete columnRefs[key]
  }
}

// 打开时把选中的时、分滚到列中间。
const scrollColumnsToSelection = () => {
  const selections: Record<TimeColumnKey, number> = { hour: localHour.value, minute: localMinute.value }
  for (const key of ['hour', 'minute'] as const) {
    const column = columnRefs[key]
    const option = column?.querySelector<HTMLElement>(`[data-value="${selections[key]}"]`)
    if (!column || !option) continue
    const offset = option.offsetTop - column.offsetTop
    column.scrollTop = offset - (column.clientHeight - option.offsetHeight) / 2
  }
}

const showMonthOf = (date: string) => {
  const base = date ? parseLocalDate(date) : new Date()
  viewMonth.value = formatDate(new Date(base.getFullYear(), base.getMonth(), 1))
}

const shiftMonth = (offset: number) => {
  const current = parseLocalDate(viewMonth.value)
  viewMonth.value = formatDate(new Date(current.getFullYear(), current.getMonth() + offset, 1))
}

const selectDay = (date: string) => {
  localDate.value = date
}

const emitValue = (date: string, hour: number, minute: number) => {
  emit('update:modelValue', `${date}T${pad(hour)}:${pad(minute)}`)
}

const isPresetActive = (preset: DateTimePreset): boolean => {
  return !!props.modelValue && preset.resolve() === props.modelValue
}

const selectPreset = (preset: DateTimePreset) => {
  emit('update:modelValue', preset.resolve())
  isOpen.value = false
}

// “此刻”直接写回当前时间并关闭弹层。
const selectNow = () => {
  const now = new Date()
  emitValue(formatDate(now), now.getHours(), now.getMinutes())
  isOpen.value = false
}

const open = () => {
  const parsed = parseValue(props.modelValue)
  const now = new Date()
  localDate.value = parsed?.date ?? ''
  localHour.value = parsed?.hour ?? now.getHours()
  localMinute.value = parsed?.minute ?? now.getMinutes()
  showMonthOf(localDate.value)
  isOpen.value = true
  updateDropdownPosition()
  void nextTick(scrollColumnsToSelection)
}

// 未点确定就关闭时丢弃草稿，触发器继续显示已生效的值。
const close = () => {
  isOpen.value = false
}

const toggle = () => {
  if (isOpen.value) {
    close()
  } else {
    open()
  }
}

const apply = () => {
  if (!localDate.value) return
  emitValue(localDate.value, localHour.value, localMinute.value)
  isOpen.value = false
}

const clear = () => {
  emit('update:modelValue', '')
  isOpen.value = false
}

// 弹层打开时按 Esc 关闭弹层，并阻止事件冒泡到外层弹窗的 Esc 处理。
const handleEscape = (event: KeyboardEvent) => {
  if (!isOpen.value) return
  event.stopPropagation()
  close()
}

// 触发器在视口右半侧时右缘对齐，否则左缘对齐。空间不足时由公共定位函数翻转和夹取。
const updateDropdownPosition = () => {
  const trigger = containerRef.value?.getBoundingClientRect()
  if (!trigger) return
  const triggerCenter = trigger.left + trigger.width / 2
  dropdownPosition.value = getFloatingPanelPosition(trigger, window.innerWidth, window.innerHeight, {
    viewportPadding: dropdownMargin,
    maxWidth: dropdownWidth.value,
    maxHeightRatio: 0.85,
    align: triggerCenter > window.innerWidth / 2 ? 'right' : 'left',
    pinLeftOnMobile: false
  })
}

useFloatingMotion(containerRef, () => isOpen.value, close, updateDropdownPosition)

const handleClickOutside = (event: MouseEvent) => {
  if (containerRef.value && !containerRef.value.contains(event.target as Node)) {
    close()
  }
}

const handleViewportChange = () => {
  if (isOpen.value) {
    updateDropdownPosition()
  }
}

onMounted(() => {
  // 在弹窗拦截冒泡前处理外部点击，关闭日期时间草稿。
  document.addEventListener('click', handleClickOutside, true)
  window.addEventListener('resize', handleViewportChange)
  window.addEventListener('scroll', handleViewportChange, true)
})

onUnmounted(() => {
  document.removeEventListener('click', handleClickOutside, true)
  window.removeEventListener('resize', handleViewportChange)
  window.removeEventListener('scroll', handleViewportChange, true)
})
</script>

<style scoped>
/* 触发器基线来自 input input-trigger，展开态描边和 DateRangePicker、Select 一致。 */
.date-time-picker-trigger-open {
  @apply border-primary-900/10 ring-2 ring-black/10 dark:border-dark-400 dark:ring-white/6;
}

.date-time-picker-icon {
  @apply shrink-0 text-gray-400 dark:text-dark-400;
}

.date-time-picker-value {
  @apply min-w-0 flex-1 truncate text-left tabular-nums;
}

.date-time-picker-placeholder {
  @apply text-primary-900/45 dark:text-dark-400;
}

.date-time-picker-clear {
  @apply absolute right-0.5 top-1/2 -translate-y-1/2;
  @apply text-gray-400 hover:bg-gray-100 hover:text-gray-700 dark:text-dark-400 dark:hover:bg-dark-800 dark:hover:text-dark-100;
  @apply transition-colors duration-fast;
}

.date-time-picker-dropdown {
  @apply fixed z-tooltip flex flex-col;
  @apply bg-white dark:bg-dark-900;
  @apply rounded-surface;
  @apply border border-primary-900/10 dark:border-dark-600;
  @apply shadow-lg shadow-black/10 dark:shadow-black/30;
  @apply overflow-y-auto;
}

.date-time-picker-body {
  @apply flex flex-col sm:flex-row;
}

/* 窄屏时快捷选项横排换行，sm 起改为左侧纵向列表。 */
.date-time-picker-presets {
  @apply flex flex-wrap items-center gap-1 p-2;
  @apply border-b border-primary-900/10 dark:border-dark-600;
  @apply sm:w-28 sm:shrink-0 sm:flex-col sm:flex-nowrap sm:items-stretch sm:gap-0.5 sm:border-b-0 sm:border-r;
}

.date-time-picker-presets-title {
  @apply px-2.5 py-1.5 text-xs font-medium text-gray-400 dark:text-dark-400;
}

.date-time-picker-preset {
  @apply flex items-center justify-between gap-2 rounded-control px-2.5 py-1.5 text-left text-sm;
  @apply text-gray-700 dark:text-gray-300;
  @apply hover:bg-gray-100 dark:hover:bg-dark-800 dark:hover:text-primary-500;
  @apply transition-colors duration-fast;
  @apply max-sm:border max-sm:border-primary-900/10 max-sm:dark:border-dark-600;
}

/* 和 DateRangePicker 的选中快捷项同一配色。 */
.date-time-picker-preset-active {
  @apply bg-gray-100 font-medium text-primary-700 dark:bg-primary-500/8 dark:text-primary-500 dark:hover:bg-primary-500/8;
}

.date-time-picker-preset-check {
  @apply hidden shrink-0 sm:block;
}

.date-time-picker-calendar {
  @apply min-w-0 flex-1 p-3;
}

.date-time-picker-header {
  @apply mb-2 flex h-8 items-center justify-between;
}

.date-time-picker-time-header {
  @apply justify-center;
}

.date-time-picker-title {
  @apply text-sm font-semibold text-gray-900 dark:text-dark-50;
}

.date-time-picker-nav {
  @apply text-gray-500 hover:bg-gray-100 hover:text-gray-900 dark:text-dark-300 dark:hover:bg-dark-800 dark:hover:text-dark-50;
  @apply transition-colors duration-fast;
}

.date-time-picker-grid {
  @apply grid grid-cols-7 gap-y-1;
}

.date-time-picker-weekday {
  @apply flex h-8 items-center justify-center text-xs font-medium text-gray-400 dark:text-dark-400;
}

.date-time-picker-day-cell {
  @apply flex h-9 items-center justify-center;
}

.date-time-picker-day {
  @apply relative flex h-9 w-9 items-center justify-center rounded-control text-sm tabular-nums;
  @apply text-gray-700 dark:text-gray-300;
  @apply hover:bg-gray-100 dark:hover:bg-dark-800;
  @apply transition-colors duration-fast;
  @apply disabled:cursor-not-allowed disabled:text-gray-300 disabled:hover:bg-transparent dark:disabled:text-dark-600;
}

/* 今天用底部小圆点标记，和选中态的实心底色区分。 */
.date-time-picker-day-today::after {
  content: '';
  @apply absolute bottom-1 left-1/2 h-1 w-1 -translate-x-1/2 rounded-full bg-primary-500;
}

.date-time-picker-day.date-time-picker-day-selected {
  @apply bg-primary-600 font-semibold text-white hover:bg-primary-700;
  @apply dark:text-white dark:hover:bg-primary-700;
}

.date-time-picker-day-selected.date-time-picker-day-today::after {
  @apply bg-white;
}

/* 窄屏时时间区排在日历下方，sm 起放到右侧并用竖线隔开。 */
.date-time-picker-time {
  @apply flex flex-col p-3;
  @apply border-t border-primary-900/10 dark:border-dark-600;
  @apply sm:w-32 sm:shrink-0 sm:border-l sm:border-t-0;
}

/* 列表绝对定位，列高由左侧日历撑出，和日历网格等高。 */
.date-time-picker-columns-frame {
  @apply relative min-h-0 flex-1 max-sm:h-48;
}

/* 上下边缘渐隐，提示列表还能继续滚动。 */
.date-time-picker-columns {
  @apply absolute inset-0 flex gap-1;
  mask-image: linear-gradient(to bottom, transparent, black 1.5rem, black calc(100% - 1.5rem), transparent);
}

.date-time-picker-column {
  @apply flex flex-1 flex-col gap-0.5 overflow-y-auto overscroll-contain;
  scrollbar-width: none;
}

.date-time-picker-column::-webkit-scrollbar {
  display: none;
}

.date-time-picker-option {
  @apply flex h-8 shrink-0 items-center justify-center rounded-control text-sm tabular-nums;
  @apply text-gray-700 dark:text-gray-300;
  @apply hover:bg-gray-100 dark:hover:bg-dark-800;
  @apply transition-colors duration-fast;
}

.date-time-picker-option.date-time-picker-option-selected {
  @apply bg-primary-600 font-semibold text-white hover:bg-primary-700;
  @apply dark:text-white dark:hover:bg-primary-700;
}

.date-time-picker-actions {
  @apply flex items-center justify-between gap-2 px-3 py-2.5;
  @apply border-t border-primary-900/10 dark:border-dark-600;
}

.date-time-picker-now {
  @apply rounded-compact px-1 text-sm font-medium text-primary-600 hover:text-primary-700 dark:text-primary-500 dark:hover:text-primary-400;
  @apply transition-colors duration-fast;
}
</style>
