<template>
  <Teleport to="body">
    <div v-if="show && team && position">
      <div class="fixed inset-0 z-menu-overlay" @click="emit('close')"></div>
      <div
        class="action-menu w-52 overflow-hidden"
        :style="{ top: `${position.top}px`, left: `${position.left}px` }"
        @click.stop
      >
        <div class="py-1">
          <button class="dropdown-item" @click="emitAction('details')">
            <Icon name="eye" size="sm" class="text-gray-400" :stroke-width="2" />
            {{ t('team.viewDetails') }}
          </button>
          <button class="dropdown-item" @click="emitAction('statistics')">
            <Icon name="chart" size="sm" class="text-indigo-500" :stroke-width="2" />
            {{ t('team.viewStatistics') }}
          </button>
          <div class="my-1 border-t border-gray-100 dark:border-dark-700"></div>
          <button
            class="dropdown-item text-red-600 dark:text-red-400"
            @click="emitAction('dissolve')"
          >
            <Icon name="trash" size="sm" :stroke-width="2" />
            {{ t('team.dissolve') }}
          </button>
        </div>
      </div>
    </div>
  </Teleport>
</template>

<script setup lang="ts">
import { onUnmounted, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import type { AdminTeam } from '@/api/admin/teams'

// TeamActionMenu.vue renders administrator actions for a selected team at a
// parent-supplied viewport position. Action events carry the selected team to the
// parent, and the menu then requests closure. Its Escape listener follows visibility
// and is also removed on unmount.

// props supplies the menu visibility, selected team, and viewport coordinates needed to
// render the teleported administrator action panel.
const props = defineProps<{
  show: boolean // Parent-controlled dialog visibility.
  // team carries the action payload; null hides the menu and suppresses action
  // emission until a team is selected.
  team: AdminTeam | null
  // position supplies viewport coordinates; null hides the teleported menu until the
  // parent has measured its trigger.
  position: {
    top: number // Viewport top offset in pixels.
    left: number // Viewport left offset in pixels.
  } | null
}>()

// emit forwards team actions and closure requests to the parent, which owns the
// corresponding dialogs and selected team state.
const emit = defineEmits<{
  (event: 'close'): void // Requests parent-owned dialog closure.
  (event: 'details', team: AdminTeam): void // Requests the selected team details.
  (event: 'statistics', team: AdminTeam): void // Requests the selected team statistics.
  (event: 'dissolve', team: AdminTeam): void // Requests selected team dissolution.
}>()

// The translation service supplies the details, statistics, and dissolve labels
// rendered in the team action menu.
const { t } = useI18n()

// emitAction sends the selected team with the requested action, then emits close. A
// missing team suppresses both events, so callers can safely invoke it while selection
// is being cleared.
const emitAction = (event: 'details' | 'statistics' | 'dissolve') => {
  if (!props.team) return
  if (event === 'details') emit('details', props.team)
  else if (event === 'statistics') emit('statistics', props.team)
  else emit('dissolve', props.team)
  emit('close')
}

// handleEscape requests closure when Escape is pressed while the menu is visible. The
// visibility watcher installs this listener, and both hiding the menu and unmounting
// remove it.
const handleEscape = (event: KeyboardEvent) => {
  if (props.show && event.key === 'Escape') emit('close')
}

// Keep the Escape listener installed only while the parent shows the menu, so hidden
// actions do not handle page key events.
watch(
  () => props.show,
  (visible) => {
    if (visible) window.addEventListener('keydown', handleEscape)
    else window.removeEventListener('keydown', handleEscape)
  },
  {
    immediate: true, // Runs on watcher registration.
  },
)

// Unmount also removes the listener because the component can disappear while its
// visible prop still indicates an open menu.
onUnmounted(() => window.removeEventListener('keydown', handleEscape))
</script>
