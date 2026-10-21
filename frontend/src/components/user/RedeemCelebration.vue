<template>
  <div class="relative min-w-0">
    <!-- Keep summary space reserved while feedback is visible, then restore its contents after the message exits. -->
    <div :class="{ invisible: message !== null }" :aria-hidden="message !== null || undefined">
      <slot />
    </div>

    <div class="sr-only" role="status" aria-live="polite" aria-atomic="true">
      <span v-if="announcement" :key="sequence">{{ announcement }}</span>
    </div>

    <MotionTransition name="fade" @after-leave="finishFeedback">
      <div
        v-if="visible && message"
        :key="message.sequence"
        :data-sequence="message.sequence"
        data-testid="redeem-celebration"
        class="pointer-events-none absolute inset-0 flex min-w-0 items-center gap-3"
        aria-hidden="true"
      >
        <div
          v-if="confettiVisible"
          :data-sequence="message.sequence"
          class="redeem-confetti pointer-events-none absolute inset-x-0 -top-4 h-24 overflow-hidden"
          @animationend.self="finishConfetti"
        >
          <span
            v-for="particle in particles"
            :key="particle.id"
            class="redeem-confetti-piece absolute h-2 w-1"
            :class="particle.color"
            :style="particle.style"
          />
        </div>

        <div
          class="relative flex h-10 w-10 shrink-0 items-center justify-center rounded-full bg-emerald-100 text-emerald-600 dark:bg-emerald-900/30 dark:text-emerald-400"
          :data-sequence="message.sequence"
          :class="{ 'redeem-success-pop': iconAnimating }"
          @animationend.self="finishIcon"
        >
          <Icon name="check" size="lg" :animate-on-hover="false" />
        </div>
        <div class="relative min-w-0">
          <p class="truncate text-sm font-medium text-gray-600 dark:text-dark-300">
            {{ message.title }}
          </p>
          <p
            class="truncate text-lg font-semibold tabular-nums text-emerald-700 dark:text-emerald-400"
          >
            {{ message.detail }}
          </p>
        </div>
      </div>
    </MotionTransition>
  </div>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import { usePreferredReducedMotion } from '@vueuse/core'
import Icon from '@/components/icons/Icon.vue'
import MotionTransition from '@/components/common/MotionTransition.vue'

// RedeemCelebration.vue displays redemption success feedback with independently removable
// icon and confetti effects. Each result carries a sequence identity, and the displayed
// message is captured from props when that sequence changes so its content survives the exit
// transition.
//
// A watcher-owned timer controls the feedback hold period and is cleaned up on replacement
// or disposal. Animation events carry the sequence of their rendered element, allowing
// completion handlers to ignore an older result after another redemption succeeds. Reduced-
// motion changes immediately remove decoration while retaining readable success text, and
// zero sequence cancels the active feedback.

// The parent supplies a success sequence and its message; zero cancels feedback, while each
// positive sequence identifies one independently completed redemption.
const props = defineProps<{
  sequence: number // Success identity; zero cancels.
  title: string // Success heading display text.
  detail: string // Granted benefit display text.
}>()

// Motion preference and visibility flags control decoration separately from feedback text;
// message retains the current result through its exit transition.
const preference = usePreferredReducedMotion()
const visible = ref(false)
const confettiVisible = ref(false)
const iconAnimating = ref(false)
const announcement = ref('')
const message = ref<{
  sequence: number // Captured redemption result identity.
  title: string // Captured success heading text.
  detail: string // Captured granted-benefit explanation text.
} | null>(null)

// FEEDBACK_HOLD_MS controls when success feedback begins hiding; animation-completion events
// independently remove the confetti and icon decoration when their CSS effects finish.
const FEEDBACK_HOLD_MS = 3000
// colors cycles particle colors across fixed trajectories so each redemption has the same
// decorative distribution around the summary area.
const colors = ['bg-primary-400', 'bg-emerald-400', 'bg-amber-400', 'bg-sky-400']
// particles assigns stable identities, colors, and deterministic movement variables to the
// sixteen decorative pieces rendered around redemption success feedback.
const particles = Array.from({ length: 16 }, (_, id) => ({
  id,
  color: colors[id % colors.length],
  style: {
    left: `${8 + (id % 8) * 11}%`,
    '--confetti-x': `${(id % 2 === 0 ? -1 : 1) * (12 + (id % 4) * 8)}px`,
    '--confetti-rise': `${-12 - (id % 4) * 8}px`,
    '--confetti-fall': `${32 + (id % 3) * 12}px`,
    '--confetti-turn': `${(id % 2 === 0 ? -1 : 1) * (120 + id * 30)}deg`,
  },
}))

// Every sequence replaces the current feedback and registers its timeout cleanup
// with the watcher. Retain message text through exit so a leaving result keeps its
// own content while completion handlers reject events from older sequences.
watch(
  () => props.sequence,
  (sequence, _previous, onCleanup) => {
    visible.value = false
    confettiVisible.value = false
    iconAnimating.value = false
    announcement.value = ''
    if (sequence === 0) return

    message.value = { sequence, title: props.title, detail: props.detail }
    announcement.value = `${props.title} ${props.detail}`
    visible.value = true
    confettiVisible.value = preference.value !== 'reduce'
    iconAnimating.value = preference.value !== 'reduce'

    const timer = window.setTimeout(() => {
      visible.value = false
    }, FEEDBACK_HOLD_MS)
    onCleanup(() => window.clearTimeout(timer))
  },
  { immediate: true },
)

// A reduced-motion change removes decoration immediately. Restoring motion does
// not create another success sequence, so this result does not replay its effects.
watch(preference, (value) => {
  if (value !== 'reduce') return
  confettiVisible.value = false
  iconAnimating.value = false
})

// finishConfetti removes confetti when its rendered element belongs to the retained
// redemption message. The animation event supplies currentTarget, whose data-sequence
// captures the result that created the element. Compare that identity with message before
// clearing the flag because an earlier animation can finish after a new result becomes
// visible. Matching completion changes decoration alone, leaving the message and its
// separate feedback timer to control readable success text.
function finishConfetti(event: AnimationEvent) {
  const element = event.currentTarget as HTMLElement
  if (Number(element.dataset.sequence) === message.value?.sequence) {
    confettiVisible.value = false
  }
}

// finishIcon stops the icon effect when its rendered element matches the retained
// redemption message. The event currentTarget carries the sequence captured for that
// element, while message may already refer to a later success. Comparing those identities
// prevents an old completion from clearing the flag that animates the newly visible icon.
// A matching event changes the icon flag independently of confetti and leaves the retained
// text available through the feedback exit.
function finishIcon(event: AnimationEvent) {
  const element = event.currentTarget as HTMLElement
  if (Number(element.dataset.sequence) === message.value?.sequence) {
    iconAnimating.value = false
  }
}

// finishFeedback releases the retained message after its rendered result finishes leaving.
// MotionTransition passes the leaving element, whose data-sequence identifies the message
// captured when that element was rendered. A new result can become visible before an older
// exit completes, so cleanup requires both hidden feedback and a matching retained sequence.
// Visible feedback keeps its text even for a matching identity, and a different sequence
// keeps the newer message after it has begun leaving.
function finishFeedback(element: Element) {
  if (
    !visible.value &&
    Number((element as HTMLElement).dataset.sequence) === message.value?.sequence
  ) {
    message.value = null
  }
}
</script>

<style scoped>
.redeem-success-pop {
  animation: redeem-success-pop var(--motion-layout) var(--motion-ease);
}

.redeem-confetti {
  --celebration-duration: 2000ms;
  animation: redeem-confetti-fade var(--celebration-duration) linear;
}

.redeem-confetti-piece {
  top: 24px;
  animation: redeem-confetti-flight var(--celebration-duration) ease-out both;
}

@keyframes redeem-success-pop {
  0% {
    transform: scale(0.8);
  }
  65% {
    transform: scale(1.08);
  }
  100% {
    transform: scale(1);
  }
}

@keyframes redeem-confetti-fade {
  0%,
  60% {
    opacity: 1;
  }
  100% {
    opacity: 0;
  }
}

@keyframes redeem-confetti-flight {
  0% {
    opacity: 0;
    transform: translateY(8px) rotate(0deg) scale(0.6);
  }
  12% {
    opacity: 0.9;
  }
  35% {
    transform: translate(var(--confetti-x), var(--confetti-rise)) rotate(var(--confetti-turn));
  }
  100% {
    transform: translate(var(--confetti-x), var(--confetti-fall)) rotate(var(--confetti-turn))
      scale(0.8);
  }
}

@media (prefers-reduced-motion: reduce) {
  .redeem-success-pop,
  .redeem-confetti,
  .redeem-confetti-piece {
    animation: none;
  }

  .redeem-confetti {
    display: none;
  }
}
</style>
