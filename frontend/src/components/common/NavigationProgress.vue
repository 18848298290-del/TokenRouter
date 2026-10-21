<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { useNavigationLoadingState } from '@/composables/useNavigationLoading'

/**
 * NavigationProgress.vue renders navigation loading state as an indeterminate progress
 * line with a completion animation. The shared navigation controller owns request
 * identity; animation completion passes the rendered navigation identifier back to
 * that controller before it can hide the indicator.
 */

/**
 * The i18n translator resolves this component's message keys using the current
 * application locale and its configured translation catalog.
 */
const { t } = useI18n()
/**
 * The shared navigation state controls visibility, progress phase, and identity; this
 * component reports completion for the rendered identifier.
 */
const { isLoading, isNavigating, navigationId, finishNavigation } = useNavigationLoadingState()

/**
 * onAnimationEnd forwards completion animations to the shared navigation controller.
 * It ignores advance animations and reads the rendered element identifier, allowing
 * the controller to reject completion from an older navigation.
 */
const onAnimationEnd = (event: AnimationEvent) => {
  if (!event.animationName.startsWith('navigation-complete')) return
  const target = event.currentTarget as HTMLElement
  finishNavigation(Number(target.dataset.navigationId))
}
</script>

<template>
  <div
    v-if="isLoading"
    :key="navigationId"
    class="navigation-progress"
    :class="{ 'is-complete': !isNavigating }"
    role="progressbar"
    :aria-label="t('common.loading')"
    aria-valuemin="0"
    aria-valuemax="100"
  >
    <div
      class="navigation-progress-bar"
      :data-navigation-id="navigationId"
      @animationend="onAnimationEnd"
    />
  </div>
</template>

<style scoped>
.navigation-progress {
  position: fixed;
  inset: 0 0 auto;
  height: 2px;
  z-index: var(--z-toast);
  overflow: hidden;
  pointer-events: none;
}

/*
 * The advance animation is indeterminate visual feedback; its endpoint below
 * full width leaves a distinct completion phase when navigation finishes.
 */
.navigation-progress-bar {
  height: 100%;
  background: theme('colors.primary.500');
  transform-origin: left;
  animation: navigation-advance 8s cubic-bezier(0.1, 0.5, 0.2, 1) forwards;
}

/*
 * The completion animation emits the event used to finish displayed feedback, so
 * this phase remains present even for immediately completed navigation.
 */
.navigation-progress.is-complete .navigation-progress-bar {
  transform: scaleX(1);
  animation: navigation-complete 180ms ease-out forwards;
}

@keyframes navigation-advance {
  from {
    transform: scaleX(0.08);
  }
  to {
    transform: scaleX(0.9);
  }
}

@keyframes navigation-complete {
  from {
    opacity: 1;
  }
  to {
    opacity: 0;
  }
}

/*
 * Reduced motion keeps the loading line static but retains a brief completion
 * animation so the shared completion event still fires.
 */
@media (prefers-reduced-motion: reduce) {
  .navigation-progress-bar {
    animation: none;
    transform: scaleX(0.35);
  }

  .navigation-progress.is-complete .navigation-progress-bar {
    animation-duration: 1ms;
  }
}
</style>
