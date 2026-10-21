<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import HelpTooltip from '@/components/common/HelpTooltip.vue'

// UpstreamRequestIdHeaderField.vue edits the response header name used for upstream
// request identification. It displays relay examples for API-key providers and
// platform-specific examples when known, helping administrators choose a header while
// leaving extraction rules and default selection to the backend.

// props exposes caller-owned inputs used by the template and derived state in this
// component.
const props = defineProps<{
  platform?: string // Provider platform identifier.
  type?: string // Provider authentication type.
}>()
// model holds the parent-owned editable value and sends replacement values through the
// component model event.
const model = defineModel<string>({ default: '' })
// t resolves interface labels from the active component locale while reactive consumers
// update their displayed text.
const { t } = useI18n()

// HeaderExample pairs a displayed header recommendation with an optional explanatory
// note.
interface HeaderExample {
  label: string // Displayed input label.
  header: string // Response header name example.
  note?: string // Optional translated example explanation.
}

// OFFICIAL_REQUEST_ID_HEADERS supplies documented platform examples for the help list;
// administrators may choose the actual response header their upstream returns.
const OFFICIAL_REQUEST_ID_HEADERS: Record<
  string,
  {
    platform: string // Provider platform identifier.
    header: string // Response header name example.
  }
> = {
  openai: { platform: 'OpenAI', header: 'x-request-id' },
  anthropic: { platform: 'Anthropic', header: 'request-id' },
  gemini: { platform: 'Gemini', header: 'x-goog-request-id' },
  antigravity: { platform: 'Antigravity', header: 'x-goog-request-id' },
  grok: { platform: 'Grok', header: 'xai-request-id' },
}

// examples includes relay suggestions for API-key credentials and a platform example when
// known, without determining the actual destination of requests.
const examples = computed<HeaderExample[]>(() => {
  const items: HeaderExample[] = []
  if (props.type === 'apikey') {
    items.push({ label: 'TokenRouter', header: 'X-TokenRouter-Request-ID' })
    items.push({
      label: 'Sub2API',
      header: 'X-Client-Request-ID',
      note: t('admin.providers.upstreamRequestIdHeaderHelp.sub2apiNote'),
    })
    items.push({ label: 'new-api / one-api', header: 'X-Oneapi-Request-Id' })
  }
  const official = props.platform ? OFFICIAL_REQUEST_ID_HEADERS[props.platform] : undefined
  if (official) {
    items.push({
      label: t('admin.providers.upstreamRequestIdHeaderHelp.official', {
        platform: official.platform,
      }),
      header: official.header,
    })
  }
  return items
})
</script>

<template>
  <div>
    <div class="flex items-center">
      <label class="input-label">{{ t('admin.providers.upstreamRequestIdHeader') }}</label>
      <HelpTooltip width-class="w-80">
        <p>{{ t('admin.providers.upstreamRequestIdHeaderHelp.intro') }}</p>
        <template v-if="examples.length">
          <p class="mt-2 font-medium">
            {{ t('admin.providers.upstreamRequestIdHeaderHelp.examplesTitle') }}
          </p>
          <ul class="mt-1 space-y-1.5">
            <li v-for="item in examples" :key="item.label">
              <div class="flex items-center justify-between gap-3">
                <span class="text-gray-300">{{ item.label }}</span>
                <code
                  class="select-all rounded-compact bg-white/10 px-1.5 py-0.5 font-mono text-xs text-white"
                  >{{ item.header }}</code
                >
              </div>
              <p v-if="item.note" class="mt-0.5 text-gray-400">{{ item.note }}</p>
            </li>
          </ul>
        </template>
      </HelpTooltip>
    </div>
    <input
      v-model="model"
      data-testid="upstream-request-id-header"
      type="text"
      class="input"
      :placeholder="t('admin.providers.upstreamRequestIdHeaderPlaceholder')"
    />
  </div>
</template>
