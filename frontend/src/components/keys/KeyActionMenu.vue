<template>
  <Teleport to="body">
    <div
      v-if="show && apiKey && position"
      class="fixed inset-0 z-menu-overlay"
      aria-hidden="true"
      @click="emit('close')"
    ></div>
    <MotionTransition name="dropdown-fade">
      <div
        v-if="show && apiKey && position"
        :id="`key-action-menu-${apiKey.id}`"
        class="action-menu w-48 overflow-hidden"
        :style="{ top: `${position.top}px`, left: `${position.left}px` }"
        role="menu"
        :aria-label="t('common.actions')"
        @click.stop
      >
        <div class="py-1">
          <button type="button" class="dropdown-item" role="menuitem" @click="emitAction('use')">
            <Icon name="terminal" size="sm" class="text-emerald-500" :stroke-width="2" />
            {{ t('keys.useKey') }}
          </button>
          <button
            type="button"
            class="dropdown-item"
            role="menuitem"
            @click="emitAction('import-tf')"
          >
            <Icon name="upload" size="sm" class="text-blue-500" :stroke-width="2" />
            {{ t('keys.importToTf') }}
          </button>
          <button
            v-if="allowImport"
            type="button"
            class="dropdown-item"
            role="menuitem"
            @click="emitAction('import')"
          >
            <Icon name="upload" size="sm" class="text-violet-500" :stroke-width="2" />
            {{ t('keys.importToCcSwitch') }}
          </button>
          <div class="my-1 border-t border-gray-100 dark:border-dark-700"></div>
          <button type="button" class="dropdown-item" role="menuitem" @click="emitAction('rotate')">
            <Icon name="refresh" size="sm" class="text-primary-500" :stroke-width="2" />
            {{ t('keys.rotateKey') }}
          </button>
          <button
            type="button"
            class="dropdown-item text-red-600 dark:text-red-400"
            role="menuitem"
            @click="emitAction('delete')"
          >
            <Icon name="trash" size="sm" class="text-red-500 dark:text-red-400" :stroke-width="2" />
            {{ t('common.delete') }}
          </button>
        </div>
      </div>
    </MotionTransition>
  </Teleport>
</template>

<script setup lang="ts">
import MotionTransition from '@/components/common/MotionTransition.vue'
import { onUnmounted, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import type { ApiKey } from '@/api/keys.types'

// KeyActionMenu.vue presents actions for the API key selected by its parent list. Menu
// events pass that record to the page before closing the overlay, and a visibility-owned
// Escape listener supports dismissal while the menu is open.

// The parent supplies the selected key, viewport position, visibility, and optional import
// availability for this teleported action menu.
const props = defineProps<{
  show: boolean // Action menu visibility.
  apiKey: ApiKey | null // Currently selected key record.
  position: {
    top: number // Viewport top offset pixels.
    left: number // Viewport left offset pixels.
  } | null // Viewport coordinates in pixels.
  allowImport: boolean // Optional client import availability.
}>()

// Action events carry the currently selected key back to its page owner; close releases the
// menu after selection or dismissal.
const emit = defineEmits<{
  (event: 'close'): void // Dismiss the open menu.
  (event: 'use', apiKey: ApiKey): void // Open client configuration instructions.
  (event: 'import-tf', apiKey: ApiKey): void // Open TokenFlux CLI import.
  (event: 'import', apiKey: ApiKey): void // Open alternate client import.
  (event: 'rotate', apiKey: ApiKey): void // Request key secret rotation.
  (event: 'delete', apiKey: ApiKey): void // Request key deletion confirmation.
}>()

// Translations label the menu actions while the parent owns each operation and its
// subsequent dialog or request.
const { t } = useI18n()

// emitAction forwards the selected key through the chosen action and then requests menu
// closure. Missing key data prevents both events, keeping actions tied to the record
// currently supplied by the parent.
const emitAction = (event: 'use' | 'import-tf' | 'import' | 'rotate' | 'delete') => {
  if (!props.apiKey) return
  if (event === 'use') emit('use', props.apiKey)
  else if (event === 'import-tf') emit('import-tf', props.apiKey)
  else if (event === 'import') emit('import', props.apiKey)
  else if (event === 'rotate') emit('rotate', props.apiKey)
  else emit('delete', props.apiKey)
  emit('close')
}

// handleEscape requests dismissal when Escape reaches an open menu. The visibility watcher
// installs its global listener only while shown, and unmount removes that listener
// regardless of the current visibility.
const handleEscape = (event: KeyboardEvent) => {
  if (props.show && event.key === 'Escape') emit('close')
}

watch(
  () => props.show,
  (visible) => {
    if (visible) window.addEventListener('keydown', handleEscape)
    else window.removeEventListener('keydown', handleEscape)
  },
  { immediate: true },
)

// Remove the same global Escape listener on disposal even if the parent unmounts the menu
// before its visibility watcher closes it.
onUnmounted(() => window.removeEventListener('keydown', handleEscape))
</script>
