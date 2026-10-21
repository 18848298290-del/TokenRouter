<template>
  <div
    class="pointer-events-none absolute inset-0 z-0 select-none overflow-hidden transition-opacity duration-700"
    :class="loaded ? 'opacity-100' : 'opacity-0'"
    aria-hidden="true"
  >
    <div
      ref="container"
      class="absolute left-1/2 top-1/2 h-[2160px] w-[3840px] -translate-x-1/2 -translate-y-1/2"
    ></div>
  </div>
</template>

<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import type { AnimationItem, LottiePlayer } from 'lottie-web'
import { useTheme } from '@/composables/useTheme'

// AuthBackground.vue renders the theme-specific Lottie artwork behind
// authentication pages. Theme changes can finish loading in a different order from
// their requests, so each load captures a generation before importing the player
// and artwork. The displayed animation is replaced only after the current
// generation has both resources and a mounted container.
//
// Unmounting increments that generation and destroys the current animation,
// preventing a late import from attaching to the removed container. The
// reduced-motion preference is sampled during setup and selects a stationary first
// frame instead of autoplay. The player module is retained for later theme loads.

// isDark follows the shared theme preference. The background watcher uses it to
// choose which artwork import may replace the currently attached animation.
const { isDark } = useTheme() // current authentication background theme

// container and loaded connect player attachment to background visibility. player,
// animation, and loadToken retain the module, current resource, and generation
// needed for theme replacement and cleanup.
const container = ref<HTMLDivElement>() // mounted Lottie target element
const loaded = ref(false) // first successful artwork display flag

let player: LottiePlayer | null = null // reusable dynamically imported player
// animation owns the currently attached player instance. Replacement destroys it,
// and unmount destroys it again if an instance is still attached.
let animation: AnimationItem | null = null // currently attached animation instance
// loadToken advances for each artwork request and unmount. A load may replace the
// player only while its captured value remains current.
let loadToken = 0 // latest artwork request generation

const prefersReducedMotion = window.matchMedia('(prefers-reduced-motion: reduce)').matches // setup-time reduced-motion preference

// loadAnimation loads theme artwork and the Lottie player for the current
// container. Each request captures a generation, and a later theme change or
// unmount invalidates earlier results. Only the winning load destroys and replaces
// the displayed animation after both imports finish. Reduced-motion users receive
// a stationary first frame, while other users receive a looping animation; import
// failures propagate to the lifecycle caller.
async function loadAnimation(dark: boolean) {
  const token = ++loadToken

  const [lottieModule, dataModule] = await Promise.all([
    player ? Promise.resolve(null) : import('lottie-web'),
    dark
      ? import('@/assets/lottie/auth-bg-dark.json')
      : import('@/assets/lottie/auth-bg-light.json'),
  ])

  // A newer theme request or unmount invalidates this generation. Discard its
  // imports before destroying the currently displayed animation or touching the
  // container.
  if (token !== loadToken || !container.value) return

  if (lottieModule) {
    player = lottieModule.default
  }

  // Destroy the previously attached player before loading its replacement so the
  // same container does not retain both theme animations.
  animation?.destroy()
  animation = player!.loadAnimation({
    container: container.value,
    renderer: 'svg',
    loop: true,
    autoplay: !prefersReducedMotion,
    animationData: dataModule.default,
    rendererSettings: { preserveAspectRatio: 'xMidYMid slice' },
  })

  if (prefersReducedMotion) {
    animation.goToAndStop(0, true)
  }

  loaded.value = true
}

// Begin artwork loading after the container is mounted, allowing the winning
// import to attach its player to this component element.
onMounted(() => {
  loadAnimation(isDark.value)
})

// A theme change starts a separate artwork load. The generation check inside
// loadAnimation decides which asynchronous result may replace the currently
// displayed player.
watch(isDark, (dark) => {
  loadAnimation(dark)
})

// Release the component-owned timer or rendered resource when this view leaves, so
// detached UI does not retain its scheduled work.
onBeforeUnmount(() => {
  // Invalidate outstanding imports before destroying the animation, so a late
  // theme load cannot attach another player after this component leaves.
  loadToken++
  // Destroy the previously attached player before loading its replacement so the
  // same container does not retain both theme animations.
  animation?.destroy()
  animation = null
})
</script>
