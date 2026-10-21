<template>
  <span :class="['inline-flex shrink-0 items-center justify-center', sizeClass]">
    <span
      v-if="sanitizedSvg"
      class="balance-icon-svg inline-flex h-full w-full items-center justify-center"
      v-html="sanitizedSvg"
    ></span>
    <Icon v-else :name="fallbackName" :size="size" :stroke-width="strokeWidth" />
  </span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import Icon from '@/components/icons/Icon.vue'
import { useBalanceDisplay } from '@/composables/useBalanceDisplay'
import { sanitizeSvg } from '@/utils/sanitize'

/**
 * BalanceIcon.vue renders a sanitized balance SVG or a registry icon at a named size.
 * A nonempty local SVG takes precedence over the global balance icon; callers can
 * disable global lookup when presenting an isolated configuration preview.
 */

/**
 * IconName restricts the fallback glyph to the shared Icon component names.
 */
type IconName = InstanceType<typeof Icon>['$props']['name']

/**
 * props selects local markup, permission to use the global icon, and the registry
 * glyph and dimensions used when SVG content is unavailable.
 */
const props = withDefaults(
  defineProps<{
    /**
     * svg takes precedence when its trimmed markup is nonempty; resolved markup is
     * sanitized before insertion into the page.
     */
    svg?: string
    fallbackName?: IconName /* registry fallback glyph */
    size?: 'xs' | 'sm' | 'md' | 'lg' | 'xl' /* rendered icon size */
    strokeWidth?: number /* fallback stroke width */
    /**
     * useGlobalFallback permits the configured balance icon when local markup is
     * empty; false leaves the registry fallback available.
     */
    useGlobalFallback?: boolean
  }>(),
  {
    fallbackName: 'dollar',
    size: 'sm',
    strokeWidth: 2,
    useGlobalFallback: true,
  },
)

/**
 * The global balance configuration supplies the icon when callers permit fallback and
 * provide no local SVG.
 */
const { balanceIconSvg: globalBalanceIconSvg } = useBalanceDisplay()

/**
 * resolvedSvg selects nonempty local markup first, then the global icon when permitted
 * by useGlobalFallback.
 */
const resolvedSvg = computed(() => {
  const localSvg = props.svg?.trim() ?? ''
  if (localSvg) {
    return localSvg
  }
  if (!props.useGlobalFallback) {
    return ''
  }
  /**
   * When local markup is empty and global lookup is enabled, the shared configured SVG
   * supplies the balance unit mark.
   */
  return globalBalanceIconSvg.value
})

/**
 * sanitizedSvg holds markup accepted by the sanitizer before the template inserts it
 * as HTML.
 */
const sanitizedSvg = computed(() => sanitizeSvg(resolvedSvg.value))
/**
 * sizeClass gives the SVG wrapper the same named dimensions as the fallback Icon
 * component.
 */
const sizeClass = computed(
  () =>
    ({
      xs: 'h-3 w-3',
      sm: 'h-4 w-4',
      md: 'h-5 w-5',
      lg: 'h-6 w-6',
      xl: 'h-8 w-8',
    })[props.size],
)
</script>

<style scoped>
/*
 * The sanitized SVG root fills the wrapper dimensions so custom markup follows
 * the same size selection as registry balance icons.
 */
.balance-icon-svg :deep(svg) {
  display: block;
  height: 100%;
  width: 100%;
}
</style>
