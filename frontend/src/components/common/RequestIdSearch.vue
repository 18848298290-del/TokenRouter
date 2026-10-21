<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'

/**
 * RequestIdSearch.vue edits a request identifier through a local search draft and an
 * exposed apply method. Enter publishes the trimmed value before emitting search,
 * while clearing an already active identifier publishes an empty filter and triggers
 * another query.
 */

/**
 * props supplies the applied request identifier while the input owns a draft that the
 * toolbar can apply through the exposed method.
 */
const props = defineProps<{
  modelValue?: string | null /* parent controlled value */
}>()
/**
 * emit publishes the trimmed draft or cleared identifier before requesting a parent
 * search through the same toolbar interaction.
 */
const emit = defineEmits<{
  /**
   * update:modelValue publishes the edited request identifier to its owning parent.
   */
  (event: 'update:modelValue', value: string): void
  /**
   * search asks the parent to query the currently published request identifier.
   */
  (event: 'search'): void
}>()

/**
 * The i18n translator resolves this component's message keys using the current
 * application locale and its configured translation catalog.
 */
const { t } = useI18n()
/**
 * draft holds the editable request identifier while the parent model retains the
 * currently applied query value.
 */
const draft = ref(props.modelValue ?? '')

/**
 * Parent query changes replace the local draft so navigation with an identifier or a
 * filter reset updates the input consistently.
 */
watch(
  () => props.modelValue,
  (value) => {
    draft.value = value ?? ''
  },
)

/**
 * apply trims the draft and publishes it before requesting search. Every invocation
 * emits the search event, including when the identifier matches the parent value, so
 * callers can request a refresh.
 */
function apply() {
  const value = draft.value.trim()
  draft.value = value
  emit('update:modelValue', value)
  emit('search')
}

/**
 * clear empties the draft and removes an active parent filter. When the parent already
 * has no identifier, it returns after changing the draft and does not request another
 * search.
 */
function clear() {
  draft.value = ''
  if (!props.modelValue) return
  emit('update:modelValue', '')
  emit('search')
}

/**
 * The exposed apply method lets a containing toolbar publish the current trimmed draft
 * and request a search through the same input path.
 */
defineExpose({ apply })
</script>

<template>
  <div class="input-icon-wrap min-w-0">
    <Icon name="search" size="md" class="input-icon text-gray-400 dark:text-dark-400" />
    <input
      v-model="draft"
      type="text"
      class="input input-has-icon font-mono placeholder:font-sans"
      :class="{ 'input-has-icon-right': draft }"
      :aria-label="t('requests.id')"
      :placeholder="t('requests.searchPlaceholder')"
      maxlength="255"
      spellcheck="false"
      autocomplete="off"
      @keydown.enter.prevent="apply"
    />
    <button
      v-if="draft"
      type="button"
      class="input-icon-right input-icon-action flex items-center text-gray-400 transition-colors hover:text-gray-600 dark:text-dark-400 dark:hover:text-dark-200"
      :aria-label="t('requests.clearSearch')"
      :title="t('requests.clearSearch')"
      @click="clear"
    >
      <Icon name="x" size="sm" />
    </button>
  </div>
</template>
