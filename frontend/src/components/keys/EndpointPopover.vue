<script setup lang="ts">
import Icon from '@/components/icons/Icon.vue'
import { computed, onBeforeUnmount, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useClipboard } from '@/composables/useClipboard'
import { COPY_FEEDBACK_MS } from '@/constants/ui'
import type { CustomEndpoint } from '@/api/settings.types'

// EndpointPopover.vue lists named connection addresses for API-key and MCP setup.
// Each endpoint can be copied with temporary per-URL confirmation, while custom
// entries retain their names and descriptions. Consumers can hide the external
// diagnostic link when it does not apply to their connection workflow.

// The configured API base and custom endpoint records supply the copyable destinations shown
// for the current key usage flow.
const props = withDefaults(
  defineProps<{
    apiBaseUrl: string // Default API base URL.
    customEndpoints: CustomEndpoint[] // Configured alternate endpoint records.
    // showSpeedTest keeps the external diagnostic link visible by default;
    // management transports can hide it while retaining the clipboard actions.
    showSpeedTest?: boolean
  }>(),
  { showSpeedTest: true },
)

// Clipboard feedback uses translated messages; copiedEndpoint and its reset timer retain the
// last successfully copied URL for a short visual confirmation.
const { t } = useI18n()
const { copyToClipboard } = useClipboard()
const copiedEndpoint = ref<string | null>(null)

let copiedResetTimer: number | undefined

// allEndpoints places a nonempty default API base before custom endpoints, marking its
// origin so the popover can distinguish its label and actions.
const allEndpoints = computed(() => {
  const items: Array<{
    name: string // Displayed endpoint label.
    endpoint: string // Copyable API base URL.
    description: string // Configured endpoint explanation text.
    isDefault: boolean // Default API endpoint flag.
  }> = []
  if (props.apiBaseUrl) {
    items.push({
      name: t('keys.endpoints.title'),
      endpoint: props.apiBaseUrl,
      description: '',
      isDefault: true,
    })
  }
  for (const ep of props.customEndpoints) {
    items.push({ ...ep, isDefault: false })
  }
  return items
})

// copy sends the selected URL to the clipboard and shows endpoint-specific feedback after
// success. It replaces the previous reset timer, and disposal clears the handle registered
// at that moment. A pending clipboard operation can still complete after disposal and
// register another timer because its continuation has no disposal check.
async function copy(url: string) {
  const success = await copyToClipboard(url, t('keys.endpoints.copied'))
  if (!success) return

  copiedEndpoint.value = url
  if (copiedResetTimer !== undefined) {
    window.clearTimeout(copiedResetTimer)
  }
  copiedResetTimer = window.setTimeout(() => {
    if (copiedEndpoint.value === url) {
      copiedEndpoint.value = null
    }
  }, COPY_FEEDBACK_MS)
}

// tooltipHint selects the copy instruction or confirmation for the hovered endpoint.
function tooltipHint(endpoint: string): string {
  return copiedEndpoint.value === endpoint
    ? t('keys.endpoints.copiedHint')
    : t('keys.endpoints.clickToCopy')
}

// speedTestUrl encodes the endpoint into the external HTTP speed-test URL.
function speedTestUrl(endpoint: string): string {
  return `https://www.tcptest.cn/http/${encodeURIComponent(endpoint)}`
}

// Clear the currently registered feedback timeout when the popover unmounts. Clipboard
// operations still awaiting completion can later change the marker and register another timer.
onBeforeUnmount(() => {
  if (copiedResetTimer !== undefined) {
    window.clearTimeout(copiedResetTimer)
  }
})
</script>

<template>
  <div v-if="allEndpoints.length > 0" class="flex flex-wrap gap-2">
    <div
      v-for="(item, index) in allEndpoints"
      :key="index"
      class="flex items-center gap-1.5 rounded-control border border-gray-200 bg-white px-2.5 py-1.5 text-xs transition-colors hover:border-black/20 dark:border-dark-600 dark:bg-dark-800 dark:hover:border-primary-700"
    >
      <span class="font-medium text-gray-600 dark:text-gray-300">{{ item.name }}</span>
      <span
        v-if="item.isDefault"
        class="rounded-compact bg-primary-50 px-1 py-px text-xs font-medium leading-tight text-primary-600 dark:bg-primary-900/30 dark:text-primary-400"
        >{{ t('keys.endpoints.default') }}</span
      >

      <span class="text-gray-300 dark:text-dark-500">|</span>

      <div class="group/endpoint relative flex items-center gap-1.5">
        <div
          class="pointer-events-none absolute bottom-full left-1/2 z-20 mb-2 w-max max-w-[24rem] -translate-x-1/2 translate-y-1 rounded-control border border-primary-900/10 bg-white px-3 py-2.5 text-left opacity-0 shadow-[0_14px_36px_-20px_rgba(0,0,0,0.32)] ring-1 ring-black/10 transition duration-fast group-hover/endpoint:translate-y-0 group-hover/endpoint:opacity-100 group-focus-within/endpoint:translate-y-0 group-focus-within/endpoint:opacity-100 dark:border-dark-600 dark:bg-dark-900 dark:ring-dark-600/70"
        >
          <p
            v-if="item.description"
            class="max-w-[24rem] break-words text-xs leading-5 text-primary-900/80 dark:text-dark-100"
          >
            {{ item.description }}
          </p>
          <p
            class="flex items-center gap-1.5 text-xs leading-4 text-primary-600 dark:text-primary-300"
            :class="item.description ? 'mt-1.5' : ''"
          >
            <span class="h-1.5 w-1.5 rounded-full bg-primary-500 dark:bg-primary-300"></span>
            {{ tooltipHint(item.endpoint) }}
          </p>
          <div
            class="absolute left-1/2 top-full h-3 w-3 -translate-x-1/2 -translate-y-1/2 rotate-45 border-b border-r border-primary-900/10 bg-white dark:border-dark-600 dark:bg-dark-900"
          ></div>
        </div>

        <code
          class="cursor-pointer font-mono text-gray-500 decoration-gray-400 decoration-dashed underline-offset-2 hover:text-primary-600 hover:underline focus:text-primary-600 focus:underline focus:outline-none dark:text-gray-400 dark:decoration-gray-500 dark:hover:text-primary-400 dark:focus:text-primary-400"
          role="button"
          tabindex="0"
          @click="copy(item.endpoint)"
          @keydown.enter.prevent="copy(item.endpoint)"
          @keydown.space.prevent="copy(item.endpoint)"
          >{{ item.endpoint }}</code
        >

        <button
          type="button"
          class="rounded-compact p-0.5 transition-colors"
          :class="
            copiedEndpoint === item.endpoint
              ? 'text-emerald-500 dark:text-emerald-400'
              : 'text-gray-400 hover:text-primary-500 dark:text-gray-500 dark:hover:text-primary-400'
          "
          :aria-label="tooltipHint(item.endpoint)"
          @click="copy(item.endpoint)"
        >
          <Icon
            name="check"
            size="xs"
            :animate-on-hover="false"
            v-if="copiedEndpoint === item.endpoint"
            class="h-3 w-3"
          />
          <Icon name="copy" size="xs" v-else class="h-3 w-3" />
        </button>

        <a
          v-if="showSpeedTest"
          :href="speedTestUrl(item.endpoint)"
          target="_blank"
          rel="noopener noreferrer"
          class="rounded-compact p-0.5 text-gray-400 transition-colors hover:text-amber-500 dark:text-gray-500 dark:hover:text-amber-400"
          :title="t('keys.endpoints.speedTest')"
        >
          <Icon name="bolt" size="xs" class="h-3 w-3" />
        </a>
      </div>
    </div>
  </div>
</template>
