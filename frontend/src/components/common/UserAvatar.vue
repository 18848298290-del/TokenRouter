<template>
  <img
    v-if="normalizedAvatarUrl"
    :src="normalizedAvatarUrl"
    :alt="alt"
    :class="[sizeClass, 'rounded-full object-cover']"
  />
  <img v-else :src="defaultAvatarUrl" :alt="alt" :class="[sizeClass, 'rounded-full']" />
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { identiconDataUri } from '@/utils/identicon'

/**
 * UserAvatar.vue displays a supplied avatar URL or a deterministic identicon for a
 * user. Trimmed nonempty URLs take priority, user identifiers supply the preferred
 * identicon seed, and callers can provide a stable seed when the user identifier is
 * unavailable.
 */

/**
 * Props describes the caller supplied values consumed by UserAvatar.
 */
interface Props {
  /**
   * avatarUrl takes precedence when trimming leaves a nonempty URL; otherwise the
   * deterministic identicon supplies the displayed image.
   */
  avatarUrl?: string

  /**
   * userId becomes the preferred identicon seed when present, including zero; a
   * missing identifier delegates seed choice to the caller.
   */
  userId?: number

  /**
   * seed supplies a stable identicon identity when userId is absent; callers reuse the
   * seed to keep the same avatar.
   */
  seed?: string
  alt?: string /* image alternative text */
  sizeClass?: string /* avatar dimension classes */
}

/**
 * props supplies the uploaded image or deterministic seed inputs, alternative text,
 * and dimensions used by both avatar branches.
 */
const props = withDefaults(defineProps<Props>(), {
  avatarUrl: '',
  userId: undefined,
  seed: '',
  alt: '',
  sizeClass: 'h-9 w-9',
})

/**
 * normalizedAvatarUrl trims surrounding whitespace before deciding whether to render
 * the caller supplied avatar image.
 */
const normalizedAvatarUrl = computed(() => props.avatarUrl.trim())

/**
 * defaultAvatarUrl prefers a user identifier seed and otherwise passes the caller seed
 * to the shared deterministic identicon generator.
 */
const defaultAvatarUrl = computed(() =>
  identiconDataUri(props.userId != null ? `user-${props.userId}` : props.seed),
)
</script>
