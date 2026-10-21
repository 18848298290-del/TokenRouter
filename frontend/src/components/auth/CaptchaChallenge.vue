<template>
  <TurnstileWidget
    v-if="turnstileEnabled && turnstileSiteKey"
    ref="turnstileRef"
    :site-key="turnstileSiteKey"
    @verify="(token) => emit('verify', token, '')"
    @expire="emit('expire')"
    @error="emit('error')"
  />
  <TencentCaptchaGate
    v-else-if="tencentEnabled && tencentAppId"
    ref="tencentRef"
    :app-id="tencentAppId"
    :region="tencentRegion"
  />
  <AliyunCaptchaWidget
    v-else-if="aliyunEnabled && aliyunSceneId && aliyunPrefix"
    ref="aliyunRef"
    :scene-id="aliyunSceneId"
    :prefix="aliyunPrefix"
    :region="aliyunRegion === 'sgp' ? 'sgp' : 'cn'"
    @verify="(param: string) => emit('verify', param, '')"
    @expire="emit('expire')"
    @error="emit('error')"
  />
</template>

<script setup lang="ts">
import { ref } from 'vue'
import TurnstileWidget from '@/components/auth/TurnstileWidget.vue'
import TencentCaptchaGate from '@/components/auth/TencentCaptchaGate.vue'
import AliyunCaptchaWidget from '@/components/auth/AliyunCaptchaWidget.vue'

// CaptchaChallenge.vue selects the configured verification widget and exposes
// common reset and action verification methods to authentication forms. Inline
// Turnstile verification emits its token immediately, while Tencent and Aliyun
// action verification returns a proof or a cancellation result.

// ActionCaptchaResult carries provider proof values submitted by authentication forms.
// Tencent supplies a ticket and random string; Aliyun supplies its verification
// parameter as token with an empty random string.
export interface ActionCaptchaResult {
  token: string // Provider verification proof value.
  randstr: string // Tencent random proof string.
}

// props selects provider widgets using the public authentication settings
// supplied by the owning form. Template selection prefers a configured
// Turnstile widget, then Tencent, then Aliyun; action verification separately
// checks Tencent before Aliyun, so callers supply the intended provider flags.
const props = defineProps<{
  siteKey?: string // Legacy site key input.
  turnstileEnabled: boolean // Enable inline Turnstile verification.
  turnstileSiteKey: string // Turnstile public site key.
  tencentEnabled: boolean // Enable Tencent action verification.
  tencentAppId: string // Tencent public application identifier.
  tencentRegion?: string // Tencent SDK region selection.
  aliyunEnabled?: boolean // Enable Aliyun action verification.
  aliyunSceneId?: string // Aliyun configured scene identifier.
  aliyunPrefix?: string // Aliyun deployment prefix value.
  aliyunRegion?: string // Aliyun SDK region selection.
}>()

// emit forwards verification proof, expiration, and provider errors to the
// owning authentication form.
const emit = defineEmits<{
  verify: [tokenOrTicket: string, randstr: string] /* Authentication proof verification callback. */
  expire: [] /* Current proof expiration notification. */
  error: [] /* Verification failure notification event. */
}>()

// turnstileRef, tencentRef, and aliyunRef address the mounted widgets for reset
// and action verification calls.
const turnstileRef = ref<InstanceType<typeof TurnstileWidget> | null>(null)
const tencentRef = ref<InstanceType<typeof TencentCaptchaGate> | null>(null)
const aliyunRef = ref<InstanceType<typeof AliyunCaptchaWidget> | null>(null)

// reset asks every mounted verification widget to discard its current proof.
// Authentication callers invoke it after a rejected submission so the next
// attempt can obtain fresh provider verification.
function reset(): void {
  turnstileRef.value?.reset()
  tencentRef.value?.reset()
  aliyunRef.value?.reset()
}

// verifyAction opens the configured Tencent or Aliyun challenge and normalizes
// its proof for authentication requests. User cancellation, missing widget
// instances, and verification errors return null; errors also emit the component
// error event.
async function verifyAction(): Promise<ActionCaptchaResult | null> {
  if (props.tencentEnabled && props.tencentAppId) {
    try {
      const proof = (await tencentRef.value?.verify()) ?? null
      if (!proof) return null
      return { token: proof.ticket, randstr: proof.randstr }
    } catch {
      // Convert provider rejection into the shared error event and null result
      // so forms can stop the pending submission.
      emit('error')
      return null
    }
  }
  if (props.aliyunEnabled && props.aliyunSceneId && props.aliyunPrefix) {
    try {
      const param = (await aliyunRef.value?.verify()) ?? null
      if (!param) return null
      return { token: param, randstr: '' }
    } catch {
      // Convert provider rejection into the shared error event and null result
      // so forms can stop the pending submission.
      emit('error')
      return null
    }
  }
  return null
}

defineExpose({ reset, verifyAction })
</script>
