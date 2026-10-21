<script setup lang="ts">
import { ref, useId, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import HelpTooltip from '@/components/common/HelpTooltip.vue'
import Icon from '@/components/icons/Icon.vue'

// ModelRedirectHelp.vue illustrates how an API-key redirect replaces a requested model
// before later routing. The tooltip uses fixed demonstration values and animation events to
// mark arrivals, with a replay counter remounting the illustration for another run.

// Translation and tooltip state identify the current demonstration; replay and arrivedNodes
// reset its visual progress whenever the help panel reopens.
const { t } = useI18n()
const open = ref(false)
const replay = ref(0)
const tooltipId = useId()
const arrivedNodes = ref(new Set<string>())

watch([open, replay], () => {
  arrivedNodes.value = new Set()
})

// animateArrival marks a demonstration node when its own arrival animation begins. Bubbling
// child animation events are ignored so a nested effect cannot advance the request
// illustration prematurely.
function animateArrival(event: AnimationEvent, node: string) {
  if (event.target === event.currentTarget) arrivedNodes.value.add(node)
}

// stages defines independent demonstration checkpoints in animation seconds; opening or
// replaying the tooltip remounts them so request movement starts from the first node.
const stages = [
  { label: 'request', icon: 'monitor', arrival: 0 },
  { label: 'match', icon: 'key', arrival: 0.32 },
  { label: 'target', icon: 'server', arrival: 0.78 },
] as const
// sourceModel and targetModel provide fixed illustrative identifiers independent from the
// editable key redirect rules owned by the surrounding form.
const sourceModel = 'codex-auto-review'
const targetModel = 'gpt-6-luna'
</script>

<template>
  <HelpTooltip v-model:open="open" trigger="click" width-class="w-[32rem]" :tooltip-id="tooltipId">
    <template #trigger>
      <button
        type="button"
        class="inline-flex rounded-compact p-1 text-gray-400 transition-colors hover:text-primary-600 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary-500 dark:text-dark-400 dark:hover:text-primary-400"
        :aria-label="t('keys.modelRedirect.help.title')"
        :aria-expanded="open"
        :aria-controls="tooltipId"
        :aria-describedby="open ? tooltipId : undefined"
      >
        <Icon name="infoCircle" size="sm" />
      </button>
    </template>

    <div class="space-y-3">
      <div class="pr-6">
        <p class="text-sm font-semibold">{{ t('keys.modelRedirect.help.title') }}</p>
        <p class="mt-1 text-gray-300 dark:text-dark-300">
          {{ t('keys.modelRedirect.help.description') }}
        </p>
      </div>

      <div class="flex items-center justify-between gap-2 text-gray-300 dark:text-dark-300">
        <div class="flex flex-wrap items-center gap-2">
          <span>{{ t('keys.modelRedirect.help.example') }}</span>
          <span class="font-mono text-primary-300">{{ sourceModel }}</span>
          <Icon name="arrowRight" size="xs" :animate-on-hover="false" />
          <span class="font-mono text-amber-300">{{ targetModel }}</span>
        </div>
        <button
          type="button"
          class="inline-flex items-center gap-1 rounded-compact px-2 py-1 text-primary-300 hover:bg-white/10 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary-400"
          @click="replay += 1"
        >
          <Icon name="refresh" size="xs" />
          {{ t('keys.modelRedirect.help.replay') }}
        </button>
      </div>

      <figure
        v-if="open"
        :key="replay"
        class="redirect-demo relative m-0 rounded-surface border border-white/10 px-2 pb-3 pt-4 dark:border-dark-600"
        :aria-label="t('keys.modelRedirect.help.flow', { from: sourceModel, to: targetModel })"
      >
        <!-- Nodes and request cards each occupy one third of the track, keeping percentage travel aligned with their centers. -->
        <div class="relative" aria-hidden="true">
          <div
            class="redirect-track absolute top-5 border-t border-dashed border-white/20 dark:border-dark-600"
          />
          <!-- The track packet and lower card share one travel timeline; node backgrounds cover the packet as it arrives. -->
          <div
            class="redirect-packet redirect-line-packet pointer-events-none absolute left-0 top-5 w-1/3"
          >
            <span
              class="relative left-1/2 block h-2 w-3 -translate-x-1/2 -translate-y-1/2 rounded-compact bg-gray-200 ring-4 ring-white/10 dark:bg-dark-100"
            />
          </div>
          <ol class="relative grid grid-cols-3">
            <li
              v-for="stage in stages"
              :key="stage.label"
              class="flex min-w-0 flex-col items-center gap-2 px-1 text-center"
            >
              <span
                class="redirect-node relative flex h-10 w-10 items-center justify-center rounded-control border border-white/20 bg-gray-900 text-gray-400 dark:border-dark-600 dark:bg-dark-900 dark:text-dark-400"
                :style="{ '--node-arrival': stage.arrival }"
                @animationstart="animateArrival($event, stage.label)"
              >
                <Icon
                  :name="stage.icon"
                  size="md"
                  :animate-on-hover="false"
                  :animation-active="arrivedNodes.has(stage.label)"
                />
                <span
                  class="redirect-node-check absolute -bottom-1 -right-1 flex h-4 w-4 items-center justify-center rounded-full bg-gray-900 text-emerald-400 dark:bg-dark-900"
                >
                  <Icon name="check" size="xs" :animate-on-hover="false" />
                </span>
              </span>
              <span class="text-xs font-medium">{{
                t(`keys.modelRedirect.help.${stage.label}`)
              }}</span>
            </li>
          </ol>
          <div class="redirect-packet mt-4 w-1/3 px-1">
            <div
              class="redirect-payload relative rounded-control border border-white/20 bg-gray-900 p-2 shadow-sm dark:border-dark-600 dark:bg-dark-900"
            >
              <span
                class="absolute -top-1 left-1/2 h-2 w-2 -translate-x-1/2 rotate-45 border-l border-t border-white/20 bg-gray-900 dark:border-dark-600 dark:bg-dark-900"
              />
              <p class="text-xs text-gray-400 dark:text-dark-400">model</p>
              <p
                class="redirect-original mt-1 break-words font-mono text-xs font-semibold text-primary-300"
              >
                {{ sourceModel }}
              </p>
              <div class="redirect-replaced grid">
                <p
                  class="min-h-0 overflow-hidden break-words font-mono text-sm font-semibold text-amber-300"
                >
                  {{ targetModel }}
                </p>
              </div>
            </div>
          </div>
        </div>
      </figure>
    </div>
  </HelpTooltip>
</template>

<style scoped>
/* One timeline moves the request and replaces its model label after the key stage before forwarding continues. */
.redirect-demo {
  --redirect-duration: 6s;
}

.redirect-track {
  left: calc(100% / 6);
  right: calc(100% / 6);
}

.redirect-packet,
.redirect-original,
.redirect-replaced {
  animation-duration: var(--redirect-duration);
  animation-fill-mode: both;
}

.redirect-packet {
  animation-name: redirect-travel;
  animation-timing-function: cubic-bezier(0.22, 1, 0.36, 1);
}

.redirect-original {
  animation-name: redirect-original;
}

.redirect-replaced {
  animation-name: redirect-replaced;
}

/* Arrival offsets correspond to the travel animation stops, and reached nodes remain marked until replay resets them. */
.redirect-node,
.redirect-node-check {
  animation-duration: var(--motion-fast);
  animation-timing-function: var(--motion-ease);
  animation-fill-mode: forwards;
  animation-delay: calc(var(--node-arrival) * var(--redirect-duration));
}

.redirect-node {
  animation-name: redirect-node;
}

.redirect-node-check {
  opacity: 0;
  transform: scale(0.7);
  animation-name: redirect-node-check;
}

@keyframes redirect-travel {
  0%,
  15% {
    transform: translateX(0);
  }
  32%,
  58% {
    transform: translateX(100%);
  }
  78%,
  100% {
    transform: translateX(200%);
  }
}

@keyframes redirect-original {
  0%,
  40% {
    opacity: 1;
    text-decoration: none;
  }
  46%,
  100% {
    opacity: 0.65;
    text-decoration: line-through;
  }
}

@keyframes redirect-replaced {
  0%,
  42% {
    grid-template-rows: 0fr;
    opacity: 0;
    margin-top: 0;
  }
  50%,
  100% {
    grid-template-rows: 1fr;
    opacity: 1;
    margin-top: 4px;
  }
}

@keyframes redirect-node {
  to {
    color: theme('colors.gray.100');
    border-color: rgb(255 255 255 / 40%);
  }
}

@keyframes redirect-node-check {
  to {
    opacity: 1;
    transform: scale(1);
  }
}

/* Reduced motion shows the final target state while retaining source and destination names for comparison. */
@media (prefers-reduced-motion: reduce) {
  .redirect-demo * {
    animation: none;
  }

  .redirect-packet {
    transform: translateX(200%);
  }

  .redirect-line-packet {
    display: none;
  }

  .redirect-node {
    color: theme('colors.gray.100');
    border-color: rgb(255 255 255 / 40%);
  }

  .redirect-node-check {
    opacity: 1;
    transform: none;
  }

  .redirect-original {
    opacity: 0.65;
    text-decoration: line-through;
  }

  .redirect-replaced {
    grid-template-rows: 1fr;
    margin-top: 4px;
  }
}
</style>
