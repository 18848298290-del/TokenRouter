<template>
  <Teleport to="body">
    <div
      v-if="show && group && position"
      class="fixed inset-0 z-menu-overlay"
      aria-hidden="true"
      @click="emit('close')"
    ></div>
    <MotionTransition name="dropdown-fade">
      <div
        v-if="show && group && position"
        :id="`group-action-menu-${group.id}`"
        class="action-menu w-48 overflow-y-auto"
        :style="{
          top: `${position.top}px`,
          left: `${position.left}px`,
          maxHeight: `calc(100dvh - ${position.top + 8}px)`,
        }"
        role="menu"
        :aria-label="t('common.actions')"
        @click.stop
      >
        <div class="py-1">
          <button
            type="button"
            data-testid="group-duplicate"
            class="dropdown-item min-h-9 disabled:cursor-not-allowed disabled:opacity-50"
            role="menuitem"
            :title="duplicating ? t('admin.groups.duplicating') : t('admin.groups.duplicate')"
            :disabled="duplicating"
            @click="emitAction('duplicate')"
          >
            <Icon name="copy" size="sm" class="text-blue-500" />
            {{ duplicating ? t('admin.groups.duplicating') : t('admin.groups.duplicate') }}
          </button>
          <button
            type="button"
            class="dropdown-item min-h-9"
            role="menuitem"
            @click="emitAction('rate-multipliers')"
          >
            <Icon name="dollar" size="sm" class="text-violet-500" />
            {{ t('admin.groups.rateMultipliers') }}
          </button>
          <button
            type="button"
            class="dropdown-item min-h-9"
            role="menuitem"
            @click="emitAction('rpm-overrides')"
          >
            <Icon name="bolt" size="sm" class="text-amber-500" />
            {{ t('admin.groups.rpmOverrides') }}
          </button>
          <div class="my-1 border-t border-gray-100 dark:border-dark-700"></div>
          <button
            type="button"
            class="dropdown-item min-h-9 text-red-600 dark:text-red-400"
            role="menuitem"
            @click="emitAction('delete')"
          >
            <Icon name="trash" size="sm" />
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
import type { AdminGroup } from '@/api/groups.types'

// GroupActionMenu.vue presents actions for the selected administrator group in a
// viewport-positioned menu. The page owns mutations and dialogs; this component forwards the
// selected group, closes after an action, and attaches Escape handling while visible.

// props supplies parent-owned editor values and display controls consumed by the fields below.
const props = defineProps<{
  show: boolean // dialog visibility flag
  // group identifies the selected administrator record. Null suppresses menu actions until the
  // parent provides a target.
  group: AdminGroup | null
  // position supplies viewport pixel coordinates for the teleported menu. Null prevents the
  // positioned menu from rendering.
  position: {
    top: number // viewport top pixels
    left: number // viewport left pixels
  } | null
  // duplicating disables the duplicate action while its selected group request is pending.
  // Other menu actions remain available.
  duplicating: boolean
}>()

// GroupAction identifies the group operations forwarded to the owning page.
type GroupAction = 'duplicate' | 'rate-multipliers' | 'rpm-overrides' | 'delete'
// emit forwards typed edits and actions to the parent that owns the displayed draft.
const emit = defineEmits<{
  // The close event asks the page to hide the menu. Action events carry the selected AdminGroup
  // so the page can open its matching editor or confirmation.
  (event: 'close'): void
  // The close event asks the page to hide the menu. Action events carry the selected AdminGroup
  // so the page can open its matching editor or confirmation.
  (event: GroupAction, group: AdminGroup): void
}>()
// t resolves the current locale messages used by labels, validation, and action notifications.
const { t } = useI18n()

// emitAction forwards the selected group and closes the action menu. Missing groups and
// duplicate requests already in progress produce no events; the page opens any subsequent
// dialog.
const emitAction = (event: GroupAction) => {
  if (!props.group || (event === 'duplicate' && props.duplicating)) return
  emit(event, props.group)
  emit('close')
}

// handleEscape closes a visible menu when the Escape key is pressed.
const handleEscape = (event: KeyboardEvent) => {
  if (props.show && event.key === 'Escape') emit('close')
}

// Visibility owns the Escape listener: attach it while the menu is shown and remove it when
// the menu closes.
watch(
  () => props.show,
  (visible) => {
    if (visible) window.addEventListener('keydown', handleEscape)
    else window.removeEventListener('keydown', handleEscape)
  },
  { immediate: true },
)

// Unmount can occur while the menu is visible, so remove its Escape listener even if the
// visibility watcher never observed closure.
onUnmounted(() => window.removeEventListener('keydown', handleEscape))
</script>
