<template>
  <div v-if="contactEntries.length" class="relative" ref="dropdownRef">
    <button
      type="button"
      data-testid="contact-support-toggle"
      class="header-status-icon-button"
      :aria-label="t('common.contactSupport')"
      :aria-expanded="isOpen"
      :title="t('common.contactSupport')"
      @click="toggleDropdown"
    >
      <Icon name="chat" size="md" />
    </button>

    <!-- Each contact occupies one row; links open a new tab and text values can be copied. -->
    <MotionTransition name="dropdown-fade">
      <div v-if="isOpen" class="dropdown right-0 z-50 mt-2 w-72 origin-top-right py-0">
        <div class="menu-section">
          <div class="menu-heading">{{ t('common.contactSupport') }}</div>
          <template v-for="(entry, idx) in contactEntries" :key="idx">
            <a
              v-if="entry.url"
              :href="entry.url"
              target="_blank"
              rel="noopener noreferrer"
              class="menu-item"
              :title="entry.value"
              @click="closeDropdown"
            >
              <span
                v-if="entry.label"
                class="shrink-0 font-normal text-primary-900/60 dark:text-dark-400"
                >{{ entry.label }}</span
              >
              <span class="ml-auto min-w-0 truncate">{{ formatContactUrl(entry.url) }}</span>
              <Icon name="externalLink" size="sm" class="shrink-0" />
            </a>
            <button
              v-else
              type="button"
              class="menu-item"
              :title="t('common.copy')"
              @click="copyContact(entry.value)"
            >
              <span
                v-if="entry.label"
                class="shrink-0 font-normal text-primary-900/60 dark:text-dark-400"
                >{{ entry.label }}</span
              >
              <span class="ml-auto min-w-0 truncate tabular-nums">{{ entry.value }}</span>
              <Icon name="copy" size="sm" class="shrink-0" />
            </button>
          </template>
        </div>
      </div>
    </MotionTransition>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import MotionTransition from '@/components/common/MotionTransition.vue'
import Icon from '@/components/icons/Icon.vue'
import { useClipboard } from '@/composables/useClipboard'
import { useAppStore } from '@/stores'

// HeaderContactSupport.vue turns configured contact text into a header menu of links and
// copy actions. Commas, semicolons, and newlines separate entries, while a colon can
// split a label from its value. HTTP and HTTPS values become external links and other
// values remain copyable text.
//
// The parser skips a colon followed by two slashes when searching for a label separator,
// allowing ordinary scheme-prefixed links to remain intact. A later colon can still
// split the entry, including one in a port or URL path. Menu visibility is local, and
// document click observation lasts until the component unmounts.

// ContactEntry represents one parsed contact value and its optional label.
interface ContactEntry {
  label: string // Optional display label
  value: string // Trimmed contact value
  // Contains the value when it is a whitespace-free HTTP or HTTPS URL; otherwise the
  // menu uses a copy button.
  url: string
}

// Translation, public contact settings, and clipboard feedback supply the menu labels
// and actions for this mounted support control.
const { t } = useI18n()
const appStore = useAppStore()
const { copyToClipboard } = useClipboard()

// isOpen owns menu visibility, and dropdownRef identifies the root used to keep internal
// clicks from triggering the document outside-click close handler.
const isOpen = ref(false)
const dropdownRef = ref<HTMLElement | null>(null)

// contactEntries parses configured contact text into menu rows. Separators split entries
// before labels are extracted, and empty values are discarded. Only whitespace-free HTTP
// or HTTPS values become external links; other values remain available for copying.
//
// Label extraction skips scheme colons followed by two slashes and accepts the first
// other colon after the beginning of an entry. This keeps a plain scheme-prefixed URL
// intact unless it contains another colon, which the same label rule can interpret as a
// separator.
const contactEntries = computed<ContactEntry[]>(() => {
  const raw = appStore.contactInfo?.trim()
  if (!raw) return []
  return raw
    .split(/[，,;；\n]+/)
    .map((part) => part.trim())
    .filter(Boolean)
    .map((part) => {
      // Skip the scheme delimiter so a plain HTTP link can remain the value; other colon
      // occurrences follow the label-separator rule.
      let sep = -1
      for (let i = 0; i < part.length; i++) {
        const ch = part[i]
        if (ch === '：') {
          sep = i
          break
        }
        if (ch === ':' && part.slice(i, i + 3) !== '://') {
          sep = i
          break
        }
      }
      let label = ''
      let value = part
      if (sep > 0) {
        label = part.slice(0, sep).trim()
        value = part.slice(sep + 1).trim()
      }
      const url = /^https?:\/\/\S+$/.test(value) ? value : ''
      return { label, value, url }
    })
    .filter((e) => e.value)
})

// toggleDropdown toggles the support menu when its header button is clicked.
function toggleDropdown() {
  isOpen.value = !isOpen.value
}

// closeDropdown closes the support menu after a link or copy action.
function closeDropdown() {
  isOpen.value = false
}

// formatContactUrl removes the HTTP scheme and one trailing slash from display text. The
// link target still uses the original parsed URL, so shortening the visible label does
// not change the address opened by the browser.
function formatContactUrl(url: string) {
  return url.replace(/^https?:\/\//, '').replace(/\/$/, '')
}

// copyContact waits for clipboard handling before closing the support menu.
//
// Clipboard feedback is owned by useClipboard. A resolved false still closes the menu; a
// thrown clipboard-command error propagates and leaves the menu open.
async function copyContact(value: string) {
  await copyToClipboard(value)
  closeDropdown()
}

// handleClickOutside closes support when a document click is outside its root.
//
// If the template root is absent, the handler leaves local visibility unchanged until a
// later toggle or close action.
function handleClickOutside(event: MouseEvent) {
  if (dropdownRef.value && !dropdownRef.value.contains(event.target as Node)) {
    closeDropdown()
  }
}

// Observe document clicks during the mounted support control lifetime and remove the
// same callback before unmount so detached menus stop reacting to clicks.
onMounted(() => {
  document.addEventListener('click', handleClickOutside)
})

onBeforeUnmount(() => {
  document.removeEventListener('click', handleClickOutside)
})
</script>
